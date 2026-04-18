package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/bft"
	"github.com/justinstimatze/gemot/internal/store"
)

// TestBFTVoteHistoryRoundTripPostgres exercises the session-5a
// bft_vote_history table: empty-load, save + reload, monotonic UPSERT
// (a stale write never regresses the stored value), and replica-id
// isolation (two replicas in the same DB stay independent).
func TestBFTVoteHistoryRoundTripPostgres(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()
	vh0 := store.NewPostgresVoteHistoryStore(db, bft.ReplicaID(0))

	// Empty state.
	lv, lp, err := vh0.Load(ctx)
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if lv != 0 || lp != 0 {
		t.Fatalf("empty returned (%d, %d), want (0, 0)", lv, lp)
	}

	// Save and reload.
	if err := vh0.SaveVote(ctx, 5); err != nil {
		t.Fatalf("SaveVote 5: %v", err)
	}
	if err := vh0.SaveProposal(ctx, 3); err != nil {
		t.Fatalf("SaveProposal 3: %v", err)
	}
	lv, lp, _ = vh0.Load(ctx)
	if lv != 5 || lp != 3 {
		t.Fatalf("Load after save: (%d, %d), want (5, 3)", lv, lp)
	}

	// Monotonic UPSERT: a stale write (lower view) must NOT regress
	// the stored value. This protects against an in-flight retry
	// arriving after a newer write.
	if err := vh0.SaveVote(ctx, 2); err != nil {
		t.Fatalf("SaveVote 2 (stale): %v", err)
	}
	if err := vh0.SaveProposal(ctx, 1); err != nil {
		t.Fatalf("SaveProposal 1 (stale): %v", err)
	}
	lv, lp, _ = vh0.Load(ctx)
	if lv != 5 {
		t.Fatalf("after stale SaveVote, lastVoted = %d; want 5 (no regression)", lv)
	}
	if lp != 3 {
		t.Fatalf("after stale SaveProposal, lastProposed = %d; want 3 (no regression)", lp)
	}

	// Replica-id isolation: a different replica in the same DB sees
	// its own (0, 0) state, not replica 0's values.
	vh1 := store.NewPostgresVoteHistoryStore(db, bft.ReplicaID(1))
	lv1, lp1, err := vh1.Load(ctx)
	if err != nil {
		t.Fatalf("Load replica 1: %v", err)
	}
	if lv1 != 0 || lp1 != 0 {
		t.Fatalf("replica 1 returned (%d, %d), want (0, 0) — cross-replica leakage", lv1, lp1)
	}
}

// TestBFTVoteHistoryRestoreBlocksDoubleVotePostgres is the end-to-end
// safety test: a replica that persisted lastVotedView = 1 to Postgres
// under a prior boot is restarted with a fresh Replica wrapping the
// same store via RestoreVoteHistory. The restored lastVotedView
// prevents the second boot from voting again in view 1 — closing the
// session-4 known gap where a crash-restart could equivocate under
// a Byzantine peer racing the restart.
func TestBFTVoteHistoryRestoreBlocksDoubleVotePostgres(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	// Simulate the prior boot: replica 1 voted in view 1.
	vh := store.NewPostgresVoteHistoryStore(db, bft.ReplicaID(1))
	if err := vh.SaveVote(ctx, 1); err != nil {
		t.Fatalf("seed SaveVote: %v", err)
	}

	// Fresh boot: construct a full cluster, attach the persisted
	// store to replica 1 via RestoreVoteHistory.
	roster := []bft.ReplicaID{0, 1, 2, 3}
	keys, pubRoster, err := bft.GenerateBLSKeyset(4)
	if err != nil {
		t.Fatalf("GenerateBLSKeyset: %v", err)
	}
	net := bft.NewInMemoryNetwork(roster, 64)
	reps := make([]*bft.Replica, 4)
	for i := range roster {
		signer, err := bft.NewBLSSigner(bft.ReplicaID(i), keys[i], pubRoster)
		if err != nil {
			t.Fatalf("NewBLSSigner %d: %v", i, err)
		}
		reps[i], err = bft.NewReplica(bft.ReplicaID(i), 4, 1, signer, net[bft.ReplicaID(i)], roster)
		if err != nil {
			t.Fatalf("NewReplica %d: %v", i, err)
		}
	}
	if err := reps[1].RestoreVoteHistory(ctx, vh); err != nil {
		t.Fatalf("RestoreVoteHistory: %v", err)
	}

	// Leader (replica 0) proposes in view 1 — replica 1's restored
	// lastVotedView blocks the second vote.
	prop, err := reps[0].Propose(bft.Hash{}, []byte("op1"), bft.QC{})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	err = reps[1].HandleProposal(*prop)
	if err == nil {
		t.Fatalf("expected error after restored lastVotedView=1; got nil")
	}
}
