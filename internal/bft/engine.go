package bft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

type Engine struct {
	mu        sync.Mutex
	replica   *Replica
	transport Transport
	// latestHash is the hash of the most recently proposed block,
	// used as the parent pointer for the next Submit. Seeded to
	// genesis (zero hash).
	latestHash Hash
	// latestQC is the highest-view QC observed so far, used as the
	// justify for the next proposal. Seeded to genesis QC.
	latestQC QC
	// log, voteHist, keyStore are the durable stores this engine was
	// bootstrapped from. Retained so resyncFromLog can rebuild the replica
	// from the log after a fork. nil for engines built via NewEngine directly
	// (tests) — such engines cannot self-resync.
	log      LogStore
	voteHist VoteHistoryStore
	keyStore ReplicaKeyStore
	// clusterLock serializes the propose→append→commit round across every
	// process that shares this engine's durable log. nil in single-process /
	// in-memory deployments, where the engine mutex alone suffices. See
	// ClusterLocker.
	clusterLock ClusterLocker
}

// NewEngine constructs a single-node engine. Caller is responsible
// for attaching a log (via replica.SetLog) and vote history (via
// replica.SetVoteHistory or RestoreVoteHistory) before the first
// Submit so commits and anti-equivocation counters are durable.
//
// The engine advances r.view to stay in sync with the replica's
// leader rotation — for N=1 the sole replica is always leader, so
// view advancement happens after every Submit.
func NewEngine(r *Replica, transport Transport) *Engine {
	return &Engine{
		replica:   r,
		transport: transport,
		latestQC:  QC{}, // genesis
	}
}

// RestoreChainState primes latestHash / latestQC from the replica's
// committed log so a restart continues the chain instead of forking
// genesis. Call after Replay (if using a durable log). Safe to call
// on an empty log — leaves genesis-seeded state unchanged.
func (e *Engine) RestoreChainState() {
	e.mu.Lock()
	defer e.mu.Unlock()
	committed := e.replica.Committed()
	if len(committed) <= 1 {
		// Only genesis.
		return
	}
	e.latestHash = committed[len(committed)-1]
	e.latestQC = e.replica.HighQC()
}

// Submit orders payload into the shared log. It drives one
// propose→self-vote→commit round via submitOnce; if that round loses an
// append race to another instance sharing the log (ErrLogForkDetected —
// the height PRIMARY KEY guarantees one block per height, so a fork is a
// rejected duplicate, never divergence), it resyncs this engine's replica
// from the log and retries on the reconciled head.
//
// When a ClusterLocker is configured (multi-machine deployments sharing one
// Postgres log), the entire round is held under the cluster-wide append lock
// so no peer machine can interleave an append mid-round. That is what makes
// the resync retry converge: without it, an active peer re-forks every retry
// and the engine wedges (the height-4800 production incident). Single-process
// engines pass clusterLock == nil and rely on the engine mutex alone.
func (e *Engine) Submit(ctx context.Context, payload []byte) (QC, Block, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.clusterLock == nil {
		return e.submitLocked(ctx, payload)
	}
	// Hold the cluster-wide append lock for the whole propose→append→commit
	// round. fn's own error is returned verbatim; a non-nil lock error with
	// fn success is a lock-infra failure (acquire/release) worth surfacing
	// distinctly from a protocol error.
	var qc QC
	var blk Block
	var innerErr error
	lockErr := e.clusterLock.WithLock(ctx, BFTAppendLockKey, func() error {
		qc, blk, innerErr = e.submitLocked(ctx, payload)
		return innerErr
	})
	if innerErr != nil {
		return QC{}, Block{}, innerErr
	}
	if lockErr != nil {
		return QC{}, Block{}, fmt.Errorf("bft: cluster append lock: %w", lockErr)
	}
	return qc, blk, nil
}

// submitOnce proposes payload as the next block, drives the self-vote
// through the replica, and returns the prepared QC plus the block.
// Serialized: concurrent callers queue behind the engine mutex.
//
// Single-node flow:
//
//  1. Replica.Propose forms a block + signs proposal + persists
//     proposedInView via the vote-history store.
//  2. Self-deliver the proposal through Replica.HandleProposal,
//     which signs the vote, persists lastVotedView, emits the vote
//     via the transport (to self, since N=1).
//  3. Drain the vote from the transport inbox.
//  4. Replica.HandleVote tallies the vote, forms the QC at
//     quorum=1, runs processJustify (which advances highQC and
//     triggers the two-chain commit of the PRIOR block, if any).
//  5. Read the freshly-formed QC from Replica.HighQC.
//
// Returns the prepared QC. The block is committed on the NEXT
// Submit when its QC is carried as the justify.
func (e *Engine) submitOnce(ctx context.Context, payload []byte) (QC, Block, error) {

	// Advance view for the new round. After the first Submit we're
	// one view past the previous; r.view is already set correctly on
	// construction (= 1) and after HandleVote it auto-advances via
	// FormQC (view+1 on the view-advance clause). Re-checking here
	// guards against any protocol-layer change that doesn't advance.
	currentView := e.replica.View()

	proposal, err := e.replica.Propose(e.latestHash, payload, e.latestQC)
	if err != nil {
		return QC{}, Block{}, fmt.Errorf("bft: propose: %w", err)
	}

	// The replica has now proposed in currentView. Any failure BELOW must
	// still advance the view, or the next Submit hits ErrDoublePropose in
	// perpetuity — a permanent single-node wedge cleared only by restart.
	// Advance-on-exit unless the success path already did.
	advanced := false
	defer func() {
		if !advanced {
			_ = e.replica.AdvanceView(currentView + 1)
		}
	}()

	if err := e.replica.HandleProposal(*proposal); err != nil {
		return QC{}, Block{}, fmt.Errorf("bft: handle own proposal: %w", err)
	}

	// Drain the self-vote the replica just sent via transport.
	var vote *Vote
	select {
	case msg := <-e.transport.Recv():
		if msg.Vote == nil {
			return QC{}, Block{}, errors.New("bft: expected vote from self-transport, got non-vote message")
		}
		vote = msg.Vote
	case <-ctx.Done():
		return QC{}, Block{}, fmt.Errorf("bft: submit canceled before vote delivery: %w", ctx.Err())
	}

	qc, err := e.replica.HandleVote(*vote)
	if err != nil {
		return QC{}, Block{}, fmt.Errorf("bft: handle own vote: %w", err)
	}
	if qc == nil {
		return QC{}, Block{}, errors.New("bft: self-vote did not form QC at quorum=1 — protocol invariant violated")
	}
	preparedQC := *qc
	if preparedQC.View != currentView {
		return QC{}, Block{}, fmt.Errorf("bft: expected prepared QC at view %d, got view %d", currentView, preparedQC.View)
	}

	e.latestHash = proposal.Block.Hash()
	e.latestQC = preparedQC

	// Advance view so the next Submit proposes under a fresh view.
	// The protocol layer does not auto-advance on QC formation; the
	// multi-node path drives this via NewView / AdvanceView and the
	// test harness does it explicitly. Here the engine owns the
	// single-node advance so service-layer callers don't need to
	// touch the replica directly.
	if err := e.replica.AdvanceView(currentView + 1); err != nil {
		return QC{}, Block{}, fmt.Errorf("bft: advance view: %w", err)
	}
	advanced = true
	return preparedQC, proposal.Block, nil
}

// PublicKey returns the single-replica roster's BLS public key in
// its compressed 96-byte G2 encoding. Clients receive this via the
// MCP admin:replica_pubkey endpoint and use it to verify QCs from
// the audit log offline.
func (e *Engine) PublicKey() ([]byte, error) {
	signer, ok := e.replica.signer.(*BLSSigner)
	if !ok {
		return nil, errors.New("bft: replica signer is not BLSSigner — cannot expose public key")
	}
	return signer.myKey.Public.Marshal(), nil
}

// AuditEntries returns the full tamper-evident log in height order.
// Each entry is a committed block plus its QC. Callers parse the
// block's payload to recover the domain-level action. Used by the
// service layer to render a verifiable audit trail of every write.
func (e *Engine) AuditEntries(ctx context.Context) ([]LogEntry, error) {
	e.mu.Lock()
	log := e.replica.log
	e.mu.Unlock()
	if log == nil {
		return nil, nil
	}
	return log.Load(ctx)
}

// EncodeQCProof serializes a QC for inclusion in a client-facing
// response. JSON for MVP debuggability; a binary format lands
// alongside multi-node wire stability if QC size becomes a concern.
func EncodeQCProof(qc QC) ([]byte, error) {
	bytes, err := json.Marshal(qc)
	if err != nil {
		return nil, fmt.Errorf("bft: encode QC proof: %w", err)
	}
	return bytes, nil
}

// resyncFromLog rebuilds this engine's replica from the durable log after a
// fork was detected on append — another instance committed a different block
// at a height this replica also targeted. It adopts the log's committed chain
// (the single source of truth), discarding this replica's divergent,
// uncommitted state, and re-primes the chain pointers. The rebuild reuses the
// exact bootstrap path, so recovered state is identical to a fresh restart
// against the same log. Caller must hold e.mu. Requires the durable stores
// retained at bootstrap; returns an error if the engine has no log configured
// (e.g. constructed via NewEngine directly in a test).
func (e *Engine) resyncFromLog(ctx context.Context) error {
	if e.log == nil {
		return errors.New("bft: cannot resync — engine has no durable log")
	}
	replica, transport, err := buildSingleNodeReplica(ctx, e.log, e.voteHist, e.keyStore)
	if err != nil {
		return fmt.Errorf("bft: resync rebuild: %w", err)
	}
	e.replica = replica
	e.transport = transport
	// Re-prime chain pointers from the rebuilt, log-derived committed head.
	// CRITICAL: always reset to match the rebuilt replica — when the log has no
	// commits yet, reset to genesis rather than leaving the discarded replica's
	// pointers in place. A stale latestHash from the old replica references a
	// block the fresh replica has never seen, so the next Propose fails with
	// "parent block unknown". (This bites specifically when a resync fires
	// before the two-chain rule has committed anything — e.g. the cluster
	// staleness gate resyncing on an as-yet-uncommitted peer proposal.)
	if committed := e.replica.Committed(); len(committed) > 1 {
		e.latestHash = committed[len(committed)-1]
		e.latestQC = e.replica.HighQC()
	} else {
		e.latestHash = Hash{}
		e.latestQC = QC{}
	}
	return nil
}

// BFTAppendLockKey is the fixed advisory-lock key guarding the single shared
// BFT log. One log ⇒ one key. Value is ASCII "gemotbft" so it is recognizable
// in pg_locks during debugging.
const BFTAppendLockKey int64 = 0x67656d6f74626674

// ClusterLocker serializes the BFT append path across processes that share
// one durable log. A single-node (N=1) engine assumes it is the SOLE writer
// of the log — but when gemot runs on more than one machine (Fly autoscaling,
// or the brief old+new overlap during any deploy) every machine runs its own
// single-node engine against ONE shared Postgres log. Without cluster-wide
// mutual exclusion, two machines append different blocks at the same height
// and the log forks (ErrLogForkDetected); the shared anti-equivocation
// counters then wedge the proposer with ErrDoublePropose. WithLock makes the
// whole round atomic across machines: only one holds the lock at a time, so
// the resync-on-fork retry runs UNCONTESTED and converges instead of
// perpetually re-racing an active peer.
//
// Implemented by store.PostgresLogStore via pg_advisory_lock — a session lock
// Postgres auto-releases if the holder's connection drops, so a crashed
// machine can never wedge the cluster. nil for single-process/in-memory
// engines.
type ClusterLocker interface {
	WithLock(ctx context.Context, key int64, fn func() error) error
}

// submitLocked drives the propose→append→commit round with a resync-on-conflict
// retry. The caller holds e.mu; in a multi-machine deployment the caller also
// holds the cluster append lock (see Submit) so the retry converges uncontested.
// Two conflict classes are recoverable by adopting the shared log and retrying:
// a log fork (a peer committed a different block at our target height) and a
// vote-history regression (a peer advanced the shared anti-equivocation counter
// past our view). Both mean "we're behind a peer" — resync from the log rebuilds
// us at the reconciled head and view. Anything else is a genuine error. Bounded,
// so a real pathology fails loudly instead of spinning.
func (e *Engine) submitLocked(ctx context.Context, payload []byte) (QC, Block, error) {
	const maxAttempts = 4
	var lastConflict error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		qc, blk, err := e.submitOnce(ctx, payload)
		if err == nil {
			return qc, blk, nil
		}
		recoverable := errors.Is(err, ErrLogForkDetected) || errors.Is(err, ErrHistoryRegression)
		if !recoverable {
			return QC{}, Block{}, err
		}
		lastConflict = err
		if rerr := e.resyncFromLog(ctx); rerr != nil {
			return QC{}, Block{}, fmt.Errorf("bft: conflict recovery failed: %w (conflict: %v)", rerr, err)
		}
	}
	return QC{}, Block{}, fmt.Errorf("bft: log conflict persisted after %d resync+retry attempts: %w", maxAttempts, lastConflict)
}
