package bft

import (
	"errors"
	"testing"
)

// Session-1 happy-path tests plus the session-2 adversarial suite
// (adversarial_test.go). Session-3 swapped the placeholder signer
// for real BLS12-381 multi-signatures via gnark-crypto; newCluster
// now generates a fresh BLS keyset per cluster and distributes
// matched (keypair, roster) pairs to each replica.

// newCluster spins up N replicas with real BLS signers, an in-memory
// network with room for many messages, and a deterministic roster.
// Returns the replicas keyed by ID so tests drive messages directly
// rather than through a goroutine-driven dispatch loop.
func newCluster(t *testing.T, n, f int) map[ReplicaID]*Replica {
	t.Helper()
	roster := make([]ReplicaID, n)
	for i := 0; i < n; i++ {
		roster[i] = ReplicaID(i)
	}
	keys, pubRoster, err := GenerateBLSKeyset(n)
	if err != nil {
		t.Fatalf("GenerateBLSKeyset: %v", err)
	}
	net := NewInMemoryNetwork(roster, 256)
	reps := make(map[ReplicaID]*Replica, n)
	for _, id := range roster {
		signer, err := NewBLSSigner(id, keys[id], pubRoster)
		if err != nil {
			t.Fatalf("NewBLSSigner(%d): %v", id, err)
		}
		r, err := NewReplica(id, n, f, signer, net[id], roster)
		if err != nil {
			t.Fatalf("NewReplica(%d): %v", id, err)
		}
		reps[id] = r
	}
	return reps
}

// drainInbox pulls messages from a replica's transport inbox until
// it would block. Used after Broadcast to materialise delivered
// messages the test will feed back through HandleProposal.
func drainInbox(t *testing.T, r *Replica) []Message {
	t.Helper()
	ch := r.transport.Recv()
	var msgs []Message
	for {
		select {
		case m := <-ch:
			msgs = append(msgs, m)
		default:
			return msgs
		}
	}
}

// runRound drives one view worth of proposal+vote+QC formation. The
// leader proposes a block with the given parent and justify, broadcasts
// it, every honest replica votes, the leader aggregates to a QC, and
// every replica advances to the next view. Returns the block hash and
// the formed QC for the caller to chain.
func runRound(t *testing.T, reps map[ReplicaID]*Replica, parent Hash, justify QC, payload []byte) (Hash, QC) {
	t.Helper()
	// Pick the leader for the current view (all replicas agree, so ask
	// replica 0).
	anyRep := reps[0]
	leader := anyRep.leader(anyRep.View())
	leaderRep := reps[leader]

	prop, err := leaderRep.Propose(parent, payload, justify)
	if err != nil {
		t.Fatalf("Propose in view %d by replica %d: %v", leaderRep.View(), leader, err)
	}
	// Deliver the proposal to every replica.
	for id, r := range reps {
		if err := r.HandleProposal(*prop); err != nil {
			t.Fatalf("HandleProposal at replica %d (view %d): %v", id, prop.View, err)
		}
	}
	// Collect the votes the replicas sent to the leader.
	msgs := drainInbox(t, leaderRep)
	var formedQC *QC
	for _, m := range msgs {
		if m.Vote == nil {
			continue
		}
		qc, err := leaderRep.HandleVote(*m.Vote)
		if err != nil {
			t.Fatalf("HandleVote: %v", err)
		}
		if qc != nil {
			formedQC = qc
		}
	}
	if formedQC == nil {
		t.Fatalf("no QC formed in view %d", prop.View)
	}
	// Advance every replica to the next view so the next round proceeds.
	next := prop.View + 1
	for id, r := range reps {
		if err := r.AdvanceView(next); err != nil {
			t.Fatalf("AdvanceView on replica %d: %v", id, err)
		}
	}
	return prop.Block.Hash(), *formedQC
}

// TestHappyPathCommitsAfterFourRounds is the core session-1 acceptance
// test. Four consecutive honest rounds commit blocks 1 and 2 via the
// chained two-chain rule: block 1 commits when view-3's proposal
// arrives (its justify is QC on block 2, whose parent is block 1, with
// consecutive views 1 and 2), and block 2 commits one view later.
func TestHappyPathCommitsAfterFourRounds(t *testing.T) {
	reps := newCluster(t, 4, 1)

	genesisQC := QC{}
	h1, qc1 := runRound(t, reps, Hash{}, genesisQC, []byte("op1"))
	h2, qc2 := runRound(t, reps, h1, qc1, []byte("op2"))
	h3, qc3 := runRound(t, reps, h2, qc2, []byte("op3"))
	_, _ = runRound(t, reps, h3, qc3, []byte("op4"))

	// After 4 rounds: every replica should have committed h1 and h2.
	// h3 needs one more round to commit; h4 two more. At height 2 we
	// expect consistent commits across all replicas.
	for id, r := range reps {
		committed := r.Committed()
		committedSet := make(map[Hash]bool, len(committed))
		for _, h := range committed {
			committedSet[h] = true
		}
		if !committedSet[h1] {
			t.Fatalf("replica %d did not commit h1", id)
		}
		if !committedSet[h2] {
			t.Fatalf("replica %d did not commit h2", id)
		}
	}
}

// TestSafetyAgreementAtHeight verifies all honest replicas agree on
// the block committed at each height. This is the core safety invariant
// — no two honest replicas ever commit different blocks at the same
// height (Yin et al. §5, Theorem 1). For the happy path with a single
// honest leader per view, every replica's committed log at height h is
// the same sequence by construction; the test pins this explicitly.
func TestSafetyAgreementAtHeight(t *testing.T) {
	reps := newCluster(t, 4, 1)

	genesisQC := QC{}
	h1, qc1 := runRound(t, reps, Hash{}, genesisQC, []byte("op1"))
	h2, qc2 := runRound(t, reps, h1, qc1, []byte("op2"))
	h3, qc3 := runRound(t, reps, h2, qc2, []byte("op3"))
	_, _ = runRound(t, reps, h3, qc3, []byte("op4"))

	// Build: per-replica map height → hash, for every committed block
	// with a known Height.
	perReplica := make(map[ReplicaID]map[Height]Hash, len(reps))
	for id, r := range reps {
		perReplica[id] = make(map[Height]Hash)
		for _, h := range r.Committed() {
			if b, ok := r.knownBlocks[h]; ok {
				perReplica[id][b.Height] = h
			}
		}
	}

	// Pairwise compare: if two replicas both committed at height h,
	// the hashes must match.
	replicaIDs := make([]ReplicaID, 0, len(reps))
	for id := range reps {
		replicaIDs = append(replicaIDs, id)
	}
	for i, id1 := range replicaIDs {
		for _, id2 := range replicaIDs[i+1:] {
			for h, hash1 := range perReplica[id1] {
				if hash2, ok := perReplica[id2][h]; ok {
					if hash1 != hash2 {
						t.Fatalf("replicas %d and %d disagree at height %d: %x vs %x",
							id1, id2, h, hash1[:8], hash2[:8])
					}
				}
			}
		}
	}
}

// TestQuorumThresholdAtTwoFPlusOne confirms that HandleVote returns
// nil (no QC) at 2f votes and a non-nil QC at 2f+1 votes. For N=4, f=1
// the threshold is 3; 2 votes must not form a QC but 3 must.
func TestQuorumThresholdAtTwoFPlusOne(t *testing.T) {
	reps := newCluster(t, 4, 1)
	anyRep := reps[0]
	leaderID := anyRep.leader(anyRep.View())
	leader := reps[leaderID]

	// Build a real proposal from the actual leader so the vote digest
	// and proposal signature line up.
	genesisQC := QC{}
	prop, err := leader.Propose(Hash{}, []byte("op"), genesisQC)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	blockHash := prop.Block.Hash()
	digest := voteDigest(prop.View, blockHash)

	// Two non-leader voters submit F+1 = 2 votes. No QC expected.
	var voters []ReplicaID
	for id := range reps {
		if id != leaderID {
			voters = append(voters, id)
		}
	}
	if len(voters) < 3 {
		t.Fatalf("expected at least 3 non-leader voters, got %d", len(voters))
	}
	for _, voter := range voters[:2] {
		sig := reps[voter].signer.Sign(digest)
		qc, err := leader.HandleVote(Vote{View: prop.View, BlockHash: blockHash, Voter: voter, Sig: sig})
		if err != nil {
			t.Fatalf("HandleVote from %d: %v", voter, err)
		}
		if qc != nil {
			t.Fatalf("QC formed at %d votes; should need 2f+1=%d", len(qc.Signers), leader.Quorum())
		}
	}

	// Third vote crosses the 2f+1 threshold.
	voter := voters[2]
	sig := reps[voter].signer.Sign(digest)
	qc, err := leader.HandleVote(Vote{View: prop.View, BlockHash: blockHash, Voter: voter, Sig: sig})
	if err != nil {
		t.Fatalf("HandleVote from %d: %v", voter, err)
	}
	if qc == nil {
		t.Fatalf("no QC formed at 2f+1 votes")
	}
	if len(qc.Signers) != leader.Quorum() {
		t.Fatalf("QC has %d signers, want %d", len(qc.Signers), leader.Quorum())
	}
}

// TestDoubleVoteRejected verifies the anti-equivocation rule: a replica
// that has voted in view V refuses to vote again in view V. Relevant
// to safety because a Byzantine leader that proposes two conflicting
// blocks in the same view cannot trick honest replicas into voting
// twice.
func TestDoubleVoteRejected(t *testing.T) {
	reps := newCluster(t, 4, 1)
	leaderID := reps[0].leader(1)
	leader := reps[leaderID]

	// First proposal in view 1.
	prop1, err := leader.Propose(Hash{}, []byte("op-a"), QC{})
	if err != nil {
		t.Fatalf("Propose A: %v", err)
	}
	// Deliver to a non-leader replica — should succeed.
	var targetID ReplicaID
	for id := range reps {
		if id != leaderID {
			targetID = id
			break
		}
	}
	target := reps[targetID]
	if err := target.HandleProposal(*prop1); err != nil {
		t.Fatalf("HandleProposal A: %v", err)
	}

	// Second (equivocating) proposal in the SAME view with different
	// payload. Propose() would refuse (ErrDoublePropose), so we
	// fabricate it directly to simulate a Byzantine leader that has
	// bypassed its own equivocation guard.
	prop2 := *prop1
	prop2.Block.Payload = []byte("op-b")
	prop2.Block.View = prop1.View
	prop2.Sig = leader.signer.Sign(proposalSignDigest(prop2.View, prop2.Block.Hash()))

	err = target.HandleProposal(prop2)
	if !errors.Is(err, ErrDoubleVote) {
		t.Fatalf("expected ErrDoubleVote on equivocating proposal; got %v", err)
	}
}

// TestDoubleProposeBlocked verifies the leader-side equivocation guard:
// a leader that has emitted a proposal in view V refuses to emit a
// second proposal in the same view. Must call AdvanceView first.
func TestDoubleProposeBlocked(t *testing.T) {
	reps := newCluster(t, 4, 1)
	leaderID := reps[0].leader(1)
	leader := reps[leaderID]

	if _, err := leader.Propose(Hash{}, []byte("first"), QC{}); err != nil {
		t.Fatalf("first Propose: %v", err)
	}
	_, err := leader.Propose(Hash{}, []byte("second"), QC{})
	if !errors.Is(err, ErrDoublePropose) {
		t.Fatalf("expected ErrDoublePropose on second proposal in same view; got %v", err)
	}
}

// TestWrongLeaderRejected verifies that a replica rejects a proposal
// whose Sender is not the designated leader for the proposal's view,
// even if the proposal is otherwise well-formed.
func TestWrongLeaderRejected(t *testing.T) {
	reps := newCluster(t, 4, 1)
	leaderID := reps[0].leader(1)
	leader := reps[leaderID]

	prop, err := leader.Propose(Hash{}, []byte("op"), QC{})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	// Pick a non-leader victim replica and a non-leader fake sender.
	var target, fake ReplicaID
	for id := range reps {
		if id != leaderID {
			if target == 0 && id != 0 {
				target = id
			} else if fake == 0 && id != 0 && id != target {
				fake = id
			}
		}
	}
	if target == fake || target == leaderID || fake == leaderID {
		// Fall back to explicit selection; tests with N=4 should always
		// have three non-leader IDs.
		for id := range reps {
			if id != leaderID && id != target {
				fake = id
				break
			}
		}
	}
	// Forge a proposal claiming to be from a non-leader.
	forged := *prop
	forged.Sender = fake
	// Sign with the fake's signer so the sig validates against the
	// wrong sender — otherwise we'd trip ErrBadProposalSig first.
	forged.Sig = reps[fake].signer.Sign(proposalSignDigest(forged.View, forged.Block.Hash()))
	err = reps[target].HandleProposal(forged)
	if !errors.Is(err, ErrWrongLeader) {
		t.Fatalf("expected ErrWrongLeader on forged proposal; got %v", err)
	}
}

// TestBadProposalSigRejected verifies that a proposal whose signature
// fails verification is rejected with ErrBadProposalSig even when the
// sender is the correct leader.
func TestBadProposalSigRejected(t *testing.T) {
	reps := newCluster(t, 4, 1)
	leaderID := reps[0].leader(1)
	leader := reps[leaderID]

	prop, err := leader.Propose(Hash{}, []byte("op"), QC{})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	// Corrupt the signature.
	forged := *prop
	if len(forged.Sig) == 0 {
		t.Fatalf("placeholder sig unexpectedly empty")
	}
	forged.Sig = append(Signature{}, forged.Sig...)
	forged.Sig[len(forged.Sig)-1] ^= 0xff

	var targetID ReplicaID
	for id := range reps {
		if id != leaderID {
			targetID = id
			break
		}
	}
	err = reps[targetID].HandleProposal(forged)
	if !errors.Is(err, ErrBadProposalSig) {
		t.Fatalf("expected ErrBadProposalSig on tampered signature; got %v", err)
	}
}

// TestPipelinedCommitOrdering verifies that blocks commit in chain
// order — committing h_k implies every ancestor down to genesis is
// already committed. The commitBlock walk-back-unseen logic is the
// invariant.
func TestPipelinedCommitOrdering(t *testing.T) {
	reps := newCluster(t, 4, 1)

	genesisQC := QC{}
	h1, qc1 := runRound(t, reps, Hash{}, genesisQC, []byte("op1"))
	h2, qc2 := runRound(t, reps, h1, qc1, []byte("op2"))
	h3, qc3 := runRound(t, reps, h2, qc2, []byte("op3"))
	_, _ = runRound(t, reps, h3, qc3, []byte("op4"))

	for id, r := range reps {
		committed := r.Committed()
		// committed[0] must be genesis (zero hash).
		if committed[0] != (Hash{}) {
			t.Fatalf("replica %d: committed[0] is not genesis", id)
		}
		// If h2 appears, h1 must appear before it.
		idx1, idx2 := -1, -1
		for i, h := range committed {
			if h == h1 {
				idx1 = i
			}
			if h == h2 {
				idx2 = i
			}
		}
		if idx2 != -1 && (idx1 == -1 || idx1 > idx2) {
			t.Fatalf("replica %d: h2 committed before h1 (idx1=%d idx2=%d)", id, idx1, idx2)
		}
	}
}

// TestByzantineBoundConstructor checks the f<N/3 guard in NewReplica.
// A caller that tries to build an over-tolerated cluster fails loudly
// at construction rather than silently running an unsafe protocol.
func TestByzantineBoundConstructor(t *testing.T) {
	roster := []ReplicaID{0, 1, 2, 3}
	net := NewInMemoryNetwork(roster, 16)
	keys, pubRoster, err := GenerateBLSKeyset(4)
	if err != nil {
		t.Fatalf("GenerateBLSKeyset: %v", err)
	}
	signer, err := NewBLSSigner(0, keys[0], pubRoster)
	if err != nil {
		t.Fatalf("NewBLSSigner: %v", err)
	}
	_, err = NewReplica(0, 4, 2, signer, net[0], roster)
	if err == nil {
		t.Fatalf("expected error for N=4 F=2 (3F=6 not < 4)")
	}
}
