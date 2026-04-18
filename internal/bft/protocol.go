package bft

import (
	"errors"
	"fmt"
)

// Session-1 scope: happy-path state transitions (HandleProposal,
// HandleVote, Propose, AdvanceView). Session-2 additions: Timeout()
// and HandleNewView() drive view change under Byzantine leader
// failure; Propose transparently extends the highest-view QC from
// 2f+1 collected NewViews when acting as the new leader. The
// protocol layer remains synchronous and single-threaded per replica
// (mutex-guarded); real time.Timer wiring and multi-node transport
// land in session 4+.

var (
	// ErrNotLeader fires when a replica receives a vote for a view
	// where it is not the designated leader.
	ErrNotLeader = errors.New("bft: not leader for this view")
	// ErrStaleView fires when a message's view is below the replica's
	// current view.
	ErrStaleView = errors.New("bft: message view is stale")
	// ErrDoubleVote fires when a replica attempts to vote twice in the
	// same view.
	ErrDoubleVote = errors.New("bft: already voted in this view")
	// ErrDoublePropose fires when a leader attempts to propose a second
	// block in the same view.
	ErrDoublePropose = errors.New("bft: already proposed in this view")
	// ErrSafetyRule fires when a proposal fails the HotStuff safety
	// rule (doesn't extend locked QC and justify view isn't newer).
	ErrSafetyRule = errors.New("bft: proposal fails safety rule")
	// ErrBadJustify fires when a proposal's Justify QC is invalid.
	ErrBadJustify = errors.New("bft: invalid justify QC")
	// ErrBadProposalSig fires when a proposal's Sig fails verification
	// against the expected leader for its view.
	ErrBadProposalSig = errors.New("bft: proposal signature verification failed")
	// ErrWrongLeader fires when a proposal's Sender is not the designated
	// leader for its view.
	ErrWrongLeader = errors.New("bft: proposal sender is not leader for view")
	// ErrStaleNewView fires when a NewView message targets a view the
	// replica has already advanced past.
	ErrStaleNewView = errors.New("bft: NewView targets a view already passed")
	// ErrBadNewViewSig fires when a NewView signature verification fails.
	ErrBadNewViewSig = errors.New("bft: NewView signature verification failed")
	// ErrBadNewViewQC fires when a NewView's carried highQC is malformed
	// or fails threshold verification. Protects the new leader from
	// accepting forged-QC NewViews that would let a Byzantine sender
	// trick the collected set into extending a non-existent chain.
	ErrBadNewViewQC = errors.New("bft: NewView high QC verification failed")
)

// Digest domain separation: one-byte prefix distinguishes what is being
// signed. Without this, a real signature scheme lets a vote signature be
// mechanically replayed as a proposal signature (both are sigs over
// (view, blockHash)). Session-1 PlaceholderSigner doesn't enforce this
// at the crypto level, but the digest functions here enforce it at the
// message-layer boundary.
const (
	domainVote     byte = 0x01
	domainProposal byte = 0x02
	domainNewView  byte = 0x03
)

// voteDigest is the byte sequence a replica signs when voting for a
// proposal. Prefixed with domainVote so the resulting signature cannot
// be replayed as a proposal signature.
func voteDigest(view View, blockHash Hash) []byte {
	return digestFor(domainVote, view, blockHash)
}

// proposalSignDigest is the byte sequence a leader signs when
// broadcasting a proposal. Prefixed with domainProposal so the
// resulting signature cannot be replayed as a vote.
func proposalSignDigest(view View, blockHash Hash) []byte {
	return digestFor(domainProposal, view, blockHash)
}

func digestFor(domain byte, view View, blockHash Hash) []byte {
	out := make([]byte, 0, 1+8+len(blockHash))
	out = append(out, domain)
	var buf [8]byte
	bigEndian8(buf[:], uint64(view))
	out = append(out, buf[:]...)
	out = append(out, blockHash[:]...)
	return out
}

// HandleProposal processes a proposal received from the leader. If the
// proposal passes the safety check, the replica updates its state and
// sends a Vote back to the leader.
func (r *Replica) HandleProposal(p Proposal) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if p.View < r.view {
		return ErrStaleView
	}
	if p.Block.View != p.View {
		return fmt.Errorf("bft: proposal view %d does not match block view %d", p.View, p.Block.View)
	}
	// Sender must be the designated leader for this view. Without this
	// check, any entity with transport access can inject proposals
	// claiming to be any leader, and an honest replica would accept
	// whichever arrives first that passes the remaining rules.
	expectedLeader := r.leader(p.View)
	if p.Sender != expectedLeader {
		return fmt.Errorf("%w: sender %d, expected %d for view %d",
			ErrWrongLeader, p.Sender, expectedLeader, p.View)
	}
	// Verify the proposal signature against the claimed sender.
	blockHash := p.Block.Hash()
	if err := r.signer.Verify(p.Sender, proposalSignDigest(p.View, blockHash), p.Sig); err != nil {
		return fmt.Errorf("%w: %v", ErrBadProposalSig, err)
	}
	// Validate the justify QC is well-formed (threshold met). Genesis
	// QC is accepted as a special case.
	if !p.Justify.IsGenesis() {
		if err := r.verifyQC(p.Justify); err != nil {
			return fmt.Errorf("%w: %v", ErrBadJustify, err)
		}
	}
	// Anti-equivocation: vote at most once per view.
	if r.lastVotedView >= p.View {
		return ErrDoubleVote
	}
	// Safety rule: block extends locked OR justify view > lockedQC view.
	// Evaluated BEFORE storing the block so an unsafe proposal doesn't
	// leave residue in knownBlocks (which was a footgun for future
	// memory-bound adversarial tests).
	if !r.safeProposal(p) {
		return ErrSafetyRule
	}
	r.knownBlocks[blockHash] = p.Block

	// Accept: update prepared, emit vote. highQC / lockedQC are updated
	// on QC receipt (chained HotStuff advances them via the arriving
	// justify QC, not via the vote path).
	r.preparedQC = QC{
		View:      p.View,
		BlockHash: blockHash,
	}
	r.lastVotedView = p.View

	sig := r.signer.Sign(voteDigest(p.View, blockHash))
	vote := Vote{
		View:      p.View,
		BlockHash: blockHash,
		Voter:     r.ID,
		Sig:       sig,
	}
	// Process the justify QC side-effects (advance highQC, lock, try
	// commit) before sending the vote. The order doesn't affect
	// correctness but it keeps the commit path inline with the
	// message that triggered it.
	r.processJustify(p.Justify)

	if err := r.transport.Send(expectedLeader, Message{Vote: &vote}); err != nil {
		return fmt.Errorf("bft: send vote: %w", err)
	}
	return nil
}

// safeProposal applies the HotStuff §4 voting rule: accept iff the
// proposed block extends the locked block, OR the proposal's justify
// QC is in a view strictly greater than the locked QC's view. The
// second clause is the "liveness" branch that lets replicas unlock
// when the honest majority has moved on after a view change.
//
// Note: the TLA+ spec models the safety branch only, not the liveness
// branch — see specs/HotStuff.tla commentary. For session-1 happy-path
// flows (no view change), the liveness branch is never needed; it's
// retained here so the impl matches the paper even before session 2's
// view-change work lands.
func (r *Replica) safeProposal(p Proposal) bool {
	lockedHash := r.lockedQC.BlockHash
	// Liveness branch: justify is in a newer view than our lock.
	if p.Justify.View > r.lockedQC.View {
		return true
	}
	// Safety branch: chain extends the locked block.
	return r.extends(p.Block.Parent, lockedHash)
}

// processJustify applies a proposal's or vote's justify QC to the
// replica's state: updates highQC, potentially locks on the justified
// block's grand-parent, and tries to commit under the two-chain rule.
// Must be called with r.mu held.
func (r *Replica) processJustify(qc QC) {
	if qc.IsGenesis() {
		return
	}
	if qc.View > r.highQC.View {
		r.highQC = qc
	}
	// Lock rule: a QC on block b → we lock on b's parent. This is the
	// "one-chain" precursor to the two-chain commit.
	justified, ok := r.knownBlocks[qc.BlockHash]
	if !ok {
		// Can't look up the justified block — we'll catch up when we
		// see the block directly. Session 2 adds explicit block sync.
		return
	}
	if parentBlock, ok := r.knownBlocks[justified.Parent]; ok {
		parentQC := QC{View: parentBlock.View, BlockHash: justified.Parent}
		if parentQC.View > r.lockedQC.View {
			r.lockedQC = parentQC
		}
		// Two-chain commit rule: when we see a QC on `justified` whose
		// parent block has a view exactly one less, commit the parent
		// block. The subsequent QC at justified.View is the second
		// chain link that locks in parentBlock's QC as committable.
		// Yin et al. PODC 2019 §5, the chained-HotStuff commit rule.
		if justified.View == parentBlock.View+1 {
			r.commitBlock(parentBlock)
		}
	}
}

// commitBlock extends committedLog with b (and any prior unseen
// ancestors). Idempotent.
//
// If the ancestor chain can't be walked all the way back to a
// committed block (because an intermediate ancestor isn't in
// knownBlocks — e.g., this replica joined mid-protocol or missed a
// proposal), commitBlock refuses to commit rather than leave a gap
// in committedLog. A future proposal that includes the missing
// ancestor, or session 2's explicit log-replay, will retry and
// succeed. Violating CommittedChainConsistent by committing a block
// with unknown ancestors would break the spec invariant.
func (r *Replica) commitBlock(b Block) {
	h := b.Hash()
	if r.committed[h] {
		return
	}
	// Walk ancestors in reverse chain order, stopping when we reach
	// genesis or a committed block. If we hit an unknown ancestor,
	// refuse the commit.
	var toCommit []Block
	cur := b
	for !r.committed[cur.Hash()] {
		toCommit = append([]Block{cur}, toCommit...)
		if cur.Parent == (Hash{}) {
			// Reached genesis; it's already committed at init.
			break
		}
		parentBlock, ok := r.knownBlocks[cur.Parent]
		if !ok {
			// Chain broken — refuse to commit rather than create a gap.
			return
		}
		cur = parentBlock
	}
	for _, blk := range toCommit {
		bh := blk.Hash()
		if r.committed[bh] {
			continue
		}
		r.committed[bh] = true
		r.committedLog = append(r.committedLog, bh)
	}
}

// HandleVote is invoked on the leader when a vote arrives from a
// replica. When 2f+1 votes for the same (view, block) have been
// collected, the leader forms a QC and proposes the next block.
//
// Session 1: the leader's "next proposal" is driven by the test
// harness, not automatically. HandleVote just tallies and returns
// the formed QC (if any) for the harness to act on.
func (r *Replica) HandleVote(v Vote) (*QC, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ID != r.leader(v.View) {
		return nil, ErrNotLeader
	}
	if err := r.signer.Verify(v.Voter, voteDigest(v.View, v.BlockHash), v.Sig); err != nil {
		return nil, fmt.Errorf("bft: vote signature: %w", err)
	}
	key := voteKey{view: v.View, hash: v.BlockHash}
	bucket := r.pendingVotes[key]
	if bucket == nil {
		bucket = make(map[ReplicaID]Signature)
		r.pendingVotes[key] = bucket
	}
	if _, dup := bucket[v.Voter]; dup {
		// Duplicate vote from same replica for same (view, block).
		// Silent ignore — no error; votes can legitimately retransmit.
		return nil, nil
	}
	bucket[v.Voter] = v.Sig

	if len(bucket) < r.Quorum() {
		return nil, nil
	}
	// Quorum reached — form QC.
	signers := make([]ReplicaID, 0, len(bucket))
	sigs := make([]Signature, 0, len(bucket))
	for id, sig := range bucket {
		signers = append(signers, id)
		sigs = append(sigs, sig)
	}
	agg := r.signer.Aggregate(sigs)
	qc := &QC{
		View:      v.View,
		BlockHash: v.BlockHash,
		Signers:   signers,
		AggSig:    agg,
	}
	// Free the tally bucket; one QC per (view, block). Any late votes
	// for this same (view, block) will silently land in a fresh bucket
	// and never reach quorum again; AdvanceView GC cleans those up.
	delete(r.pendingVotes, key)
	// The leader that just formed the QC must integrate it into its own
	// state — advance highQC, potentially lock, try the two-chain
	// commit. Without this, the leader's highQC lags behind its own
	// output: non-leader replicas learn the QC via the next round's
	// proposal-Justify, but the leader would only catch up in round
	// N+2 when someone else proposes. In a timeout scenario between
	// round N and N+1, the ex-leader's NewView would carry a stale
	// highQC, undermining the view-change selection rule.
	r.processJustify(*qc)
	return qc, nil
}

// verifyQCWithDigest is the QC verifier used by verifyQC. Split out for
// clarity — the signed digest under verification is always voteDigest
// (since a QC aggregates votes, not proposals).

// Propose is invoked by the leader to broadcast a new block in the
// current view. Caller is responsible for: (a) being the leader;
// (b) having collected the justify QC for the parent block.
//
// Session 2 addition: if 2f+1 NewView messages have been collected
// for the current view (via HandleNewView), the caller's (parent,
// justify) arguments are overridden with the highest-view QC from
// that collected set. This is the HotStuff safety requirement for
// view change — the new leader MUST extend the highest QC known to
// the honest majority, otherwise a locked branch could be silently
// abandoned and safety violated.
//
// Propose refuses to emit a second proposal in the same view. A leader
// that has already proposed must AdvanceView before proposing again.
// Without this guard, a confused or malicious leader could emit
// equivocating proposals in the same view; honest replicas would reject
// the second via lastVotedView, but the network would see two valid
// proposals from the leader under a real signature scheme.
func (r *Replica) Propose(parent Hash, payload []byte, justify QC) (*Proposal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ID != r.leader(r.view) {
		return nil, ErrNotLeader
	}
	if r.proposedInView >= r.view {
		return nil, ErrDoublePropose
	}
	// View-change override: if 2f+1 NewViews have been collected for
	// r.view, the new leader must extend the highest QC among them.
	// Silently overrides caller's (parent, justify) — the caller is
	// the transport layer or test harness and doesn't need to know
	// whether this is a happy-path or post-timeout proposal.
	if vcQC, ok := r.viewChangeHighQC[r.view]; ok {
		parent = vcQC.BlockHash
		justify = vcQC
	}
	// Determine block height: parent's height + 1, or 1 if parent is
	// genesis.
	var height Height = 1
	if parent != (Hash{}) {
		pb, ok := r.knownBlocks[parent]
		if !ok {
			return nil, fmt.Errorf("bft: parent block %x unknown", parent[:8])
		}
		height = pb.Height + 1
	}
	block := Block{
		Height:  height,
		Parent:  parent,
		View:    r.view,
		Payload: payload,
		Justify: justify,
	}
	// Record the block locally so subsequent HandleProposal (when the
	// message comes back through the transport) finds it in knownBlocks.
	blockHash := block.Hash()
	r.knownBlocks[blockHash] = block
	p := &Proposal{
		View:    r.view,
		Block:   block,
		Justify: justify,
		Sender:  r.ID,
	}
	p.Sig = r.signer.Sign(proposalSignDigest(p.View, blockHash))
	r.proposedInView = r.view
	return p, nil
}

// AdvanceView sets the replica's view to the given value (must be
// strictly greater than current). Session 1 helper for test harnesses;
// session 2's view synchronizer drives this automatically via NewView
// messages and timeouts.
//
// Garbage-collects pending vote buckets from strictly prior views —
// any votes that didn't reach quorum before the view advanced are
// permanently stale and safe to drop.
func (r *Replica) AdvanceView(v View) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v <= r.view {
		return fmt.Errorf("bft: cannot advance to non-increasing view %d (current %d)", v, r.view)
	}
	r.view = v
	for key := range r.pendingVotes {
		if key.view < v {
			delete(r.pendingVotes, key)
		}
	}
	return nil
}

// Timeout abandons the current view and emits a NewView message
// targeting view+1, carrying this replica's current highQC. Broadcasts
// to all peers (including the new leader) and advances local view.
//
// Session 2 uses manual trigger (tests call Timeout explicitly); the
// view-synchronizer timer that fires Timeout on a real wall clock is
// session-4 work (needs a time.Timer wired into the service loop).
//
// Returns the NewView that was broadcast so callers (tests) can
// inspect it. Advances local view regardless of whether the new
// leader ultimately collects 2f+1 NewViews — if not, this replica
// will eventually timeout again in view+1 and chain further.
func (r *Replica) Timeout() (*NewView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	nextView := r.view + 1
	nv := &NewView{
		View:   nextView,
		HighQC: r.highQC,
		Sender: r.ID,
	}
	nv.Sig = r.signer.Sign(newViewDigest(nextView, r.highQC.View, r.highQC.BlockHash))

	if err := r.transport.Broadcast(Message{NewView: nv}); err != nil {
		return nil, fmt.Errorf("bft: broadcast NewView: %w", err)
	}
	// Advance local view after broadcasting. GC stale pending-vote
	// buckets from the abandoned view — they can never reach quorum
	// since 2f+1 replicas have now timed out.
	r.view = nextView
	for key := range r.pendingVotes {
		if key.view < nextView {
			delete(r.pendingVotes, key)
		}
	}
	return nv, nil
}

// HandleNewView processes a NewView from a peer. Verifies the sender
// signature and the carried highQC (via verifyQC, which enforces 2f+1
// signer threshold). Accumulates messages in pendingNewViews keyed by
// target view. When 2f+1 unique senders have been collected for a
// target view, selects the highest-view QC among the collected set and
// stores it in viewChangeHighQC so the leader of that view — when it
// calls Propose — transparently extends the correct branch.
//
// Stale NewViews (targeting a view already passed by this replica) are
// rejected with ErrStaleNewView so the tally buckets don't accumulate
// unbounded state. Byzantine-sender NewViews with forged QCs fail
// verifyQC and never enter the bucket.
func (r *Replica) HandleNewView(nv NewView) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if nv.View < r.view {
		// Already past this target view — stale. Note: nv.View == r.view
		// is valid and collected, because the replica may have already
		// self-advanced via its own Timeout and is now accumulating the
		// 2f+1 evidence that the rest of the network followed.
		return ErrStaleNewView
	}
	digest := newViewDigest(nv.View, nv.HighQC.View, nv.HighQC.BlockHash)
	if err := r.signer.Verify(nv.Sender, digest, nv.Sig); err != nil {
		return fmt.Errorf("%w: %v", ErrBadNewViewSig, err)
	}
	// Reject NewViews whose carried highQC fails threshold verification.
	// Genesis QC (View=0, zero hash) is the special-case valid empty QC.
	if !nv.HighQC.IsGenesis() {
		if err := r.verifyQC(nv.HighQC); err != nil {
			return fmt.Errorf("%w: %v", ErrBadNewViewQC, err)
		}
	}

	bucket := r.pendingNewViews[nv.View]
	if bucket == nil {
		bucket = make(map[ReplicaID]NewView)
		r.pendingNewViews[nv.View] = bucket
	}
	if _, dup := bucket[nv.Sender]; dup {
		// Duplicate from same sender for same target view — ignore.
		return nil
	}
	bucket[nv.Sender] = nv

	if len(bucket) < r.Quorum() {
		return nil
	}
	// 2f+1 collected. Select the highest-view QC in the bucket. This
	// is the HotStuff view-change rule: the new leader extends the
	// highest QC among 2f+1 NewViews. Because 2f+1 intersects with
	// any prior 2f+1 quorum in at least one honest replica, the
	// selected QC is guaranteed to be at least as new as the highest
	// QC any honest replica had locked before the view change —
	// preserving the locked-QC safety invariant.
	var highest QC
	for _, msg := range bucket {
		if msg.HighQC.View > highest.View {
			highest = msg.HighQC
		}
	}
	r.viewChangeHighQC[nv.View] = highest
	// Free the bucket — one selection per target view. Any late
	// NewViews for this view will be rejected as stale once the
	// replica advances past nv.View.
	delete(r.pendingNewViews, nv.View)
	return nil
}

// verifyQC validates a QC's threshold signature. Must be called with
// r.mu held.
func (r *Replica) verifyQC(qc QC) error {
	if len(qc.Signers) < r.Quorum() {
		return fmt.Errorf("bft: QC has %d signers, need %d", len(qc.Signers), r.Quorum())
	}
	seen := make(map[ReplicaID]bool, len(qc.Signers))
	for _, s := range qc.Signers {
		if seen[s] {
			return fmt.Errorf("bft: QC duplicate signer %d", s)
		}
		seen[s] = true
	}
	return r.signer.VerifyAggregate(qc.Signers, voteDigest(qc.View, qc.BlockHash), qc.AggSig)
}
