package bft

import (
	"errors"
	"fmt"
)

// Session-1 scope: happy-path state transitions only. View change,
// NewView handling, and real timeout-driven leader rotation are
// session 2. The protocol layer here is synchronous and single-
// threaded per replica (mutex-guarded); adversarial reordering tests
// live in session 2+.

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
	// ErrSafetyRule fires when a proposal fails the HotStuff safety
	// rule (doesn't extend locked QC and justify view isn't newer).
	ErrSafetyRule = errors.New("bft: proposal fails safety rule")
	// ErrBadJustify fires when a proposal's Justify QC is invalid.
	ErrBadJustify = errors.New("bft: invalid justify QC")
)

// proposalDigest is the byte sequence a replica signs when voting for a
// proposal. Includes view + block hash so a signature for one view can
// never be replayed in another.
func proposalDigest(view View, blockHash Hash) []byte {
	out := make([]byte, 0, 8+len(blockHash))
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
	// Session 1: validate the justify QC is well-formed (threshold met).
	// Genesis QC is accepted as a special case.
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
	// Genesis lock is trivially satisfied (every block extends genesis).
	blockHash := p.Block.Hash()
	r.knownBlocks[blockHash] = p.Block
	if !r.safeProposal(p) {
		return ErrSafetyRule
	}

	// Accept: update prepared, emit vote. highQC / lockedQC are updated
	// on QC receipt (chained HotStuff advances them via the arriving
	// justify QC, not via the vote path).
	r.preparedQC = QC{
		View:      p.View,
		BlockHash: blockHash,
	}
	r.lastVotedView = p.View

	sig := r.signer.Sign(proposalDigest(p.View, blockHash))
	vote := Vote{
		View:      p.View,
		BlockHash: blockHash,
		Voter:     r.ID,
		Sig:       sig,
	}
	leader := r.leader(p.View)
	// Process the justify QC side-effects (advance highQC, lock, try
	// commit) before sending the vote. The order doesn't affect
	// correctness but it keeps the commit path inline with the
	// message that triggered it.
	r.processJustify(p.Justify)

	if err := r.transport.Send(leader, Message{Vote: &vote}); err != nil {
		return fmt.Errorf("bft: send vote: %w", err)
	}
	return nil
}

// safeProposal applies the HotStuff §4 voting rule: accept iff the
// proposed block extends the locked block, OR the proposal's justify
// QC is in a view strictly greater than the locked QC's view. The
// second clause is the "liveness" branch that lets replicas unlock
// when the honest majority has moved on.
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
func (r *Replica) commitBlock(b Block) {
	h := b.Hash()
	if r.committed[h] {
		return
	}
	// Walk up the unseen ancestor chain in order so the log stays
	// chain-consistent.
	var toCommit []Block
	cur := b
	for !r.committed[cur.Hash()] {
		toCommit = append([]Block{cur}, toCommit...)
		parentBlock, ok := r.knownBlocks[cur.Parent]
		if !ok {
			break
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
	if err := r.signer.Verify(v.Voter, proposalDigest(v.View, v.BlockHash), v.Sig); err != nil {
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
	// Free the tally bucket; one QC per (view, block).
	delete(r.pendingVotes, key)
	return qc, nil
}

// Propose is invoked by the leader to broadcast a new block in the
// current view. Caller is responsible for: (a) being the leader;
// (b) having collected the justify QC for the parent block.
//
// Session 1: the caller is the test harness. Session 2 wires this to
// a view-advance timer and a mempool of gemot operations to batch
// into the payload.
func (r *Replica) Propose(parent Hash, payload []byte, justify QC) (*Proposal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ID != r.leader(r.view) {
		return nil, ErrNotLeader
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
	p.Sig = r.signer.Sign(proposalDigest(p.View, blockHash))
	return p, nil
}

// AdvanceView sets the replica's view to the given value (must be
// strictly greater than current). Session 1 helper for test harnesses;
// session 2's view synchronizer drives this automatically via NewView
// messages and timeouts.
func (r *Replica) AdvanceView(v View) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v <= r.view {
		return fmt.Errorf("bft: cannot advance to non-increasing view %d (current %d)", v, r.view)
	}
	r.view = v
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
	return r.signer.VerifyAggregate(qc.Signers, proposalDigest(qc.View, qc.BlockHash), qc.AggSig)
}
