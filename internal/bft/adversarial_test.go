package bft

import (
	"errors"
	"testing"
)

// Session-2 adversarial tests: view change under Byzantine leader
// failure, equivocation across views, partition, and stale-NewView
// attack. All tests use the InMemoryTransport and drive messages
// manually; the view synchronizer timer is tested as a function call
// (Timeout()) rather than a real wall-clock fire.

// collectNewViews drains a replica's inbox and feeds every NewView
// message through HandleNewView. Returns the number of NewViews
// processed. Skips stale-NewView errors silently so tests can ignore
// the inbox ordering.
func collectNewViews(t *testing.T, r *Replica) int {
	t.Helper()
	msgs := drainInbox(t, r)
	n := 0
	for _, m := range msgs {
		if m.NewView == nil {
			continue
		}
		if err := r.HandleNewView(*m.NewView); err != nil {
			if errors.Is(err, ErrStaleNewView) {
				continue
			}
			t.Fatalf("HandleNewView from %d: %v", m.NewView.Sender, err)
		}
		n++
	}
	return n
}

// TestLeaderStall simulates the canonical view-change trigger: the
// designated leader of view 1 (replica 0) never proposes. Replicas
// 1, 2, 3 time out and broadcast NewView(view=2). The new leader —
// replica 1 under (view-1)%N rotation — collects 2f+1=3 NewViews and
// proposes in view 2. The protocol resumes without replica 0.
func TestLeaderStall(t *testing.T) {
	reps := newCluster(t, 4, 1)

	// Leader of view 1 is replica 0 — we simulate stall by not calling
	// Propose on replica 0. Replicas 1, 2, 3 time out.
	for _, id := range []ReplicaID{1, 2, 3} {
		if _, err := reps[id].Timeout(); err != nil {
			t.Fatalf("Timeout on replica %d: %v", id, err)
		}
	}

	// New leader of view 2 is replica 1 (= roster[(2-1)%4]). Collect
	// the broadcast NewViews from its inbox.
	newLeader := reps[1]
	collected := collectNewViews(t, newLeader)
	if collected < newLeader.Quorum() {
		t.Fatalf("new leader collected %d NewViews, need %d for quorum", collected, newLeader.Quorum())
	}
	if _, ok := newLeader.viewChangeHighQC[2]; !ok {
		t.Fatalf("viewChangeHighQC[2] not populated after 2f+1 collection")
	}

	// New leader proposes in view 2. Propose uses the collected highest
	// QC (which is genesis, since no QC formed in view 1).
	prop, err := newLeader.Propose(Hash{}, []byte("after-stall"), QC{})
	if err != nil {
		t.Fatalf("Propose in view 2 after view change: %v", err)
	}
	if prop.View != 2 {
		t.Fatalf("post-view-change proposal view = %d, want 2", prop.View)
	}
	if !prop.Justify.IsGenesis() {
		t.Fatalf("expected genesis justify after stall-then-timeout; got view %d", prop.Justify.View)
	}

	// Deliver proposal to replicas 1, 2, 3 (replica 0 remains silent).
	// Replica 1 already self-delivered the proposal on Propose's local
	// knownBlocks write; we still need to drive HandleProposal so it
	// emits a vote. But HandleProposal on the self-proposer trips
	// lastVotedView since Propose didn't set it — wait, Propose doesn't
	// set lastVotedView. So HandleProposal on the proposer works.
	for _, id := range []ReplicaID{1, 2, 3} {
		if err := reps[id].HandleProposal(*prop); err != nil {
			t.Fatalf("HandleProposal on %d: %v", id, err)
		}
	}
	// Drain leader's inbox for votes, process them, form QC.
	msgs := drainInbox(t, newLeader)
	var formed *QC
	for _, m := range msgs {
		if m.Vote == nil {
			continue
		}
		qc, err := newLeader.HandleVote(*m.Vote)
		if err != nil {
			t.Fatalf("HandleVote: %v", err)
		}
		if qc != nil {
			formed = qc
		}
	}
	if formed == nil {
		t.Fatalf("no QC formed in view 2; expected 3 votes from honest replicas {1,2,3}")
	}
	if formed.View != 2 {
		t.Fatalf("QC formed for view %d, want 2", formed.View)
	}
	// Safety: only one block has been proposed, and no block at any
	// height has committed yet (need a second-chain in view 3).
}

// TestLeaderEquivocatesAcrossViews simulates a Byzantine leader that
// proposes two conflicting blocks in the same view to different
// replica subsets. The honest majority + anti-equivocation rule
// (lastVotedView) ensures at most one of the two blocks accumulates
// 2f+1 votes; the other cannot form a QC. Safety holds even though
// the leader is Byzantine.
//
// Since Propose's ErrDoublePropose guard prevents a single leader
// call site from emitting two proposals, the test manually crafts
// the second (equivocating) proposal signed by the Byzantine leader
// — modeling a leader that has bypassed its own guard (as a real
// Byzantine would).
func TestLeaderEquivocatesAcrossViews(t *testing.T) {
	reps := newCluster(t, 4, 1)
	leaderID := reps[0].leader(1)
	leader := reps[leaderID]

	// First proposal — delivered to the first honest subset.
	propA, err := leader.Propose(Hash{}, []byte("block-A"), QC{})
	if err != nil {
		t.Fatalf("Propose A: %v", err)
	}

	// Second (equivocating) proposal. Same view, different payload →
	// different hash. Forged directly because Propose() would trip
	// ErrDoublePropose.
	propB := *propA
	propB.Block.Payload = []byte("block-B")
	propB.Sig = leader.signer.Sign(proposalSignDigest(propB.View, propB.Block.Hash()))

	// Partition the honest replicas: {1} sees A, {2, 3} see B. Leader
	// (replica 0) is Byzantine and doesn't vote.
	if err := reps[1].HandleProposal(*propA); err != nil {
		t.Fatalf("HandleProposal A on 1: %v", err)
	}
	for _, id := range []ReplicaID{2, 3} {
		if err := reps[id].HandleProposal(propB); err != nil {
			t.Fatalf("HandleProposal B on %d: %v", id, err)
		}
	}

	// Collect votes at the leader. Only 1 vote for A (from replica 1)
	// and 2 votes for B (from 2, 3) — neither meets 2f+1 = 3.
	msgs := drainInbox(t, leader)
	var qcFormed *QC
	for _, m := range msgs {
		if m.Vote == nil {
			continue
		}
		qc, err := leader.HandleVote(*m.Vote)
		if err != nil {
			t.Fatalf("HandleVote: %v", err)
		}
		if qc != nil {
			qcFormed = qc
		}
	}
	if qcFormed != nil {
		t.Fatalf("QC formed under equivocation; votes per block: A=1, B=2, quorum=%d", leader.Quorum())
	}

	// Safety post-condition: no honest replica has committed a block
	// at height 1, because no QC on height-1 exists.
	for _, id := range []ReplicaID{1, 2, 3} {
		for _, h := range reps[id].Committed() {
			if b, ok := reps[id].knownBlocks[h]; ok && b.Height >= 1 {
				t.Fatalf("replica %d committed a height-1+ block under equivocation", id)
			}
		}
	}
}

// TestPartitionedMinority: 1 Byzantine silent + 1 crashed honest = 2
// replicas not participating. Remaining 2 replicas cannot form quorum
// (need 2f+1 = 3). No commit happens while partitioned. After the
// crashed replica rejoins, progress resumes via view change + new
// leader proposal.
func TestPartitionedMinority(t *testing.T) {
	reps := newCluster(t, 4, 1)

	// Replica 0 (leader of view 1) is crashed — doesn't propose.
	// Replica 3 is Byzantine and silent too (doesn't time out).
	// Only replicas 1 and 2 time out.
	for _, id := range []ReplicaID{1, 2} {
		if _, err := reps[id].Timeout(); err != nil {
			t.Fatalf("Timeout on replica %d: %v", id, err)
		}
	}

	// New leader = replica 1. Collect NewViews — only 2 arrived (from
	// 1 and 2), below the 2f+1 = 3 quorum. viewChangeHighQC[2] must
	// NOT be populated.
	newLeader := reps[1]
	collected := collectNewViews(t, newLeader)
	if collected >= newLeader.Quorum() {
		t.Fatalf("collected %d NewViews under partition; expected < %d", collected, newLeader.Quorum())
	}
	if _, ok := newLeader.viewChangeHighQC[2]; ok {
		t.Fatalf("viewChangeHighQC[2] populated despite sub-quorum NewView count")
	}

	// Liveness check: once the crashed replica 3 heals and times out,
	// we reach 2f+1 = 3 NewViews and view change fires. (Replica 0 is
	// truly byzantine-silent for this test — we heal via replica 3.)
	if _, err := reps[3].Timeout(); err != nil {
		t.Fatalf("Timeout on replica 3 (heal): %v", err)
	}
	collected2 := collectNewViews(t, newLeader)
	if collected2 < 1 {
		t.Fatalf("post-heal new leader saw %d additional NewViews; expected ≥ 1", collected2)
	}
	if _, ok := newLeader.viewChangeHighQC[2]; !ok {
		t.Fatalf("viewChangeHighQC[2] still not populated after heal; protocol cannot progress")
	}
}

// TestStaleNewView: Byzantine replica sends a NewView with a stale
// highQC (genesis), while honest replicas send NewViews carrying a
// real view-1 QC. The new leader's selection rule — pick the highest-
// view QC among 2f+1 collected — must pick the real QC, not the
// stale one. Otherwise a Byzantine majority could silently regress
// the protocol's commit chain.
func TestStaleNewView(t *testing.T) {
	reps := newCluster(t, 4, 1)

	// Round 1: happy path. Leader 0 proposes, all vote, QC forms for
	// view 1.
	_, qc1 := runRound(t, reps, Hash{}, QC{}, []byte("op1"))
	if qc1.View != 1 {
		t.Fatalf("setup: round 1 QC should be view 1, got %d", qc1.View)
	}

	// At this point the leader of view 1 (replica 0) has integrated
	// qc1 into its own highQC (HandleVote → processJustify). The
	// other replicas still have highQC = genesis because they've
	// seen no proposal whose Justify is qc1. Drive a view-2 proposal
	// (leader of view 2 = replica 1) to propagate qc1 into everyone's
	// highQC via processJustify in HandleProposal. We don't complete
	// the round — just deliver the proposal and drain the inbox so
	// vote accumulation doesn't produce a QC that would trip the
	// subsequent timeout logic.
	leaderV2 := reps[reps[0].leader(2)]
	propV2, err := leaderV2.Propose(Hash{}, []byte("op2"), qc1)
	if err != nil {
		t.Fatalf("Propose in view 2: %v", err)
	}
	for _, id := range []ReplicaID{1, 2, 3} {
		if err := reps[id].HandleProposal(*propV2); err != nil {
			t.Fatalf("HandleProposal view-2 on %d: %v", id, err)
		}
	}
	// Drain vote inbox so HandleVote doesn't accumulate; we don't want
	// to form a view-2 QC (the test needs view 1 to be the highest).
	drainInbox(t, leaderV2)

	// Replicas are now in view 2 with highQC pointing at the view-1
	// block. Leader of view 2 (replica 1) has stalled — simulate by
	// triggering timeout on replicas 2 and 3 honestly. Replica 0 is
	// treated as Byzantine; it times out with a stale genesis highQC
	// despite knowing about qc1.
	honestTimeouts := []ReplicaID{2, 3}
	for _, id := range honestTimeouts {
		if _, err := reps[id].Timeout(); err != nil {
			t.Fatalf("Timeout on %d: %v", id, err)
		}
	}

	// Byzantine NewView from replica 0: target view 3, carries genesis
	// highQC even though replica 0 knows about view-1's QC.
	byzID := ReplicaID(0)
	byzSigner := reps[byzID].signer
	staleNV := NewView{
		View:   3,
		HighQC: QC{}, // genesis — strictly lower than the real view-1 QC
		Sender: byzID,
	}
	staleNV.Sig = byzSigner.Sign(newViewDigest(staleNV.View, 0, Hash{}))

	// New leader of view 3 is replica 2. Its Timeout call already
	// advanced its local view from 2 → 3, so it's in view 3 and will
	// accept NewView(3) messages.
	newLeader := reps[2]
	if newLeader.View() != 3 {
		t.Fatalf("setup: new leader view = %d, want 3", newLeader.View())
	}

	// Deliver the two honest NewViews and the Byzantine stale NewView.
	// Use drainInbox for the honest ones (they were broadcast) then
	// manually inject the Byzantine forgery.
	collectNewViews(t, newLeader) // drains honest NewView broadcasts
	if err := newLeader.HandleNewView(staleNV); err != nil {
		t.Fatalf("HandleNewView on Byzantine stale NewView: %v", err)
	}

	// viewChangeHighQC[3] must reference the view-1 QC (the highest
	// among collected), NOT genesis.
	chosen, ok := newLeader.viewChangeHighQC[3]
	if !ok {
		t.Fatalf("viewChangeHighQC[3] not populated — quorum not reached?")
	}
	if chosen.View != 1 {
		t.Fatalf("chosen highQC view = %d, want 1 (stale Byzantine NewView won selection)", chosen.View)
	}
}
