package bft

import (
	"context"
	"testing"
)

// Session-5a vote-history unit tests. Unit-level: the in-memory store
// respects monotonicity and roundtrips. Protocol-level: a replica with
// a store attached persists lastVotedView and proposedInView before
// emitting messages; RestoreVoteHistory propagates those counters
// into a fresh replica so a restart does not reset the anti-
// equivocation guards.

func TestInMemoryVoteHistoryRoundtrip(t *testing.T) {
	vh := NewInMemoryVoteHistoryStore()
	ctx := context.Background()

	lv, lp, err := vh.Load(ctx)
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if lv != 0 || lp != 0 {
		t.Fatalf("empty store returned (%d, %d), want (0, 0)", lv, lp)
	}

	if err := vh.SaveVote(ctx, 5); err != nil {
		t.Fatalf("SaveVote: %v", err)
	}
	if err := vh.SaveProposal(ctx, 3); err != nil {
		t.Fatalf("SaveProposal: %v", err)
	}
	lv, lp, _ = vh.Load(ctx)
	if lv != 5 || lp != 3 {
		t.Fatalf("Load after save: (%d, %d), want (5, 3)", lv, lp)
	}
}

func TestInMemoryVoteHistoryRejectsRegression(t *testing.T) {
	vh := NewInMemoryVoteHistoryStore()
	ctx := context.Background()
	if err := vh.SaveVote(ctx, 10); err != nil {
		t.Fatalf("SaveVote 10: %v", err)
	}
	if err := vh.SaveVote(ctx, 3); err == nil {
		t.Fatalf("SaveVote 3 after 10 should error; got nil")
	}
	if err := vh.SaveProposal(ctx, 10); err != nil {
		t.Fatalf("SaveProposal 10: %v", err)
	}
	if err := vh.SaveProposal(ctx, 3); err == nil {
		t.Fatalf("SaveProposal 3 after 10 should error; got nil")
	}
	// SaveVote at the same value is allowed (idempotent).
	if err := vh.SaveVote(ctx, 10); err != nil {
		t.Fatalf("SaveVote 10 twice should be idempotent: %v", err)
	}
}

// Protocol integration: a replica with vote history attached persists
// lastVotedView before emitting its vote. We drive a single proposal
// through HandleProposal and confirm the store captured the view.
func TestHandleProposalPersistsLastVotedView(t *testing.T) {
	reps := newCluster(t, 4, 1)
	vh := NewInMemoryVoteHistoryStore()
	// Replica 1 is a follower in view 1 (leader is replica 0). Attach
	// vote history to it so its vote persists.
	reps[1].SetVoteHistory(vh)

	// Leader proposes.
	prop, err := reps[0].Propose(Hash{}, []byte("op1"), QC{})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if err := reps[1].HandleProposal(*prop); err != nil {
		t.Fatalf("HandleProposal: %v", err)
	}

	lv, _, err := vh.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if lv != 1 {
		t.Fatalf("after HandleProposal in view 1, persisted lastVoted = %d; want 1", lv)
	}
}

// Propose persists proposedInView on the leader before returning the
// proposal.
func TestProposePersistsProposedInView(t *testing.T) {
	reps := newCluster(t, 4, 1)
	vh := NewInMemoryVoteHistoryStore()
	reps[0].SetVoteHistory(vh)

	if _, err := reps[0].Propose(Hash{}, []byte("op1"), QC{}); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	_, lp, err := vh.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if lp != 1 {
		t.Fatalf("after Propose in view 1, persisted lastProposed = %d; want 1", lp)
	}
}

// RestoreVoteHistory loads persisted counters into a fresh replica and
// makes the restored view the new anti-equivocation floor — a second
// vote in the already-voted view is rejected. This is the scenario
// the session-4 known-gap left open.
func TestRestoreVoteHistoryBlocksDoubleVote(t *testing.T) {
	// Pre-populate a store as if the replica had already voted in
	// view 1 under a prior boot.
	vh := NewInMemoryVoteHistoryStore()
	ctx := context.Background()
	if err := vh.SaveVote(ctx, 1); err != nil {
		t.Fatalf("seed SaveVote: %v", err)
	}

	// Construct a fresh cluster; attach the pre-populated store to
	// replica 1 via RestoreVoteHistory.
	reps := newCluster(t, 4, 1)
	if err := reps[1].RestoreVoteHistory(ctx, vh); err != nil {
		t.Fatalf("RestoreVoteHistory: %v", err)
	}

	// Leader proposes in view 1 — replica 1 refuses because its
	// restored lastVotedView is already 1.
	prop, err := reps[0].Propose(Hash{}, []byte("op1"), QC{})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	err = reps[1].HandleProposal(*prop)
	if err == nil {
		t.Fatalf("expected ErrDoubleVote after restore; got nil")
	}
}
