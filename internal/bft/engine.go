package bft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Engine drives the HotStuff state machine for the current gemot
// deployment. Session 5b ships a single-node configuration (N=1,
// F=0): the engine is both leader and sole voter for every block, so
// Submit drives propose → self-vote → QC formation synchronously in
// one call. Multi-node support lands later — the N=1 quorum=1 case is
// genuinely degenerate (trivial BFT), but the wiring exercised here
// is the same shape a multi-node engine will use, and it gives the
// service layer a concrete API to build against.
//
// Commit semantics: because HotStuff's two-chain commit rule requires
// a QC on a block's successor, Submit returns a PREPARED QC (proof
// that ≥2f+1 voted for this block) but does NOT wait for commit.
// Commit fires when the NEXT Submit's proposal carries this QC as its
// justify. A quiescent system leaves the last block uncommitted; a
// heartbeat or explicit "close chain" op lands in a later session if
// that becomes a practical problem.
//
// Thread safety: Submit is serialized via the engine mutex so a
// single gemot process driving concurrent HTTP handlers produces a
// coherent block chain. The replica's own mutex guards state; the
// engine mutex additionally serializes the propose/vote/recv sequence
// so two Submits cannot interleave mid-round.
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

// Submit proposes payload as the next block, drives the self-vote
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
func (e *Engine) Submit(ctx context.Context, payload []byte) (QC, Block, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

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
