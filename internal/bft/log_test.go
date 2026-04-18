package bft

import (
	"context"
	"errors"
	"testing"
)

// Session-4 log + replay tests. InMemoryLogStore unit tests cover
// append monotonicity, fork detection, and idempotent re-append.
// Replay roundtrip test drives 4 protocol rounds with a log attached,
// then constructs a fresh replica and confirms Replay reproduces the
// committed state.

func TestInMemoryLogStoreAppendAndLoad(t *testing.T) {
	ls := NewInMemoryLogStore()
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		b := Block{Height: Height(i), View: View(i)}
		qc := QC{View: View(i), BlockHash: b.Hash()}
		if err := ls.Append(ctx, LogEntry{Block: b, QC: qc}); err != nil {
			t.Fatalf("Append height %d: %v", i, err)
		}
	}
	entries, err := ls.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("Load returned %d entries, want 3", len(entries))
	}
	for i, e := range entries {
		if e.Block.Height != Height(i+1) {
			t.Fatalf("entry %d: height %d, want %d", i, e.Block.Height, i+1)
		}
	}
	h, err := ls.HighestHeight(ctx)
	if err != nil {
		t.Fatalf("HighestHeight: %v", err)
	}
	if h != 3 {
		t.Fatalf("HighestHeight = %d, want 3", h)
	}
}

func TestInMemoryLogStoreForkDetection(t *testing.T) {
	ls := NewInMemoryLogStore()
	ctx := context.Background()

	b1 := Block{Height: 1, View: 1, Payload: []byte("A")}
	if err := ls.Append(ctx, LogEntry{Block: b1}); err != nil {
		t.Fatalf("Append A: %v", err)
	}
	// Re-append same block — idempotent.
	if err := ls.Append(ctx, LogEntry{Block: b1}); err != nil {
		t.Fatalf("re-Append identical block should be idempotent: %v", err)
	}
	// Append DIFFERENT block at same height — fork.
	b1alt := Block{Height: 1, View: 1, Payload: []byte("B")}
	err := ls.Append(ctx, LogEntry{Block: b1alt})
	if !errors.Is(err, ErrLogForkDetected) {
		t.Fatalf("expected ErrLogForkDetected for conflicting height-1 block; got %v", err)
	}
}

func TestInMemoryLogStoreRejectsGap(t *testing.T) {
	ls := NewInMemoryLogStore()
	ctx := context.Background()

	b1 := Block{Height: 1, View: 1}
	if err := ls.Append(ctx, LogEntry{Block: b1}); err != nil {
		t.Fatalf("Append h1: %v", err)
	}
	// Skip height 2, try height 3 — gap.
	b3 := Block{Height: 3, View: 3}
	if err := ls.Append(ctx, LogEntry{Block: b3}); err == nil {
		t.Fatal("expected error for append with height gap")
	}
}

func TestLogWritesOnProtocolCommit(t *testing.T) {
	reps := newCluster(t, 4, 1)
	ls := NewInMemoryLogStore()
	// Attach the log to every replica so each one persists commits.
	for _, r := range reps {
		r.SetLog(ls)
	}

	genesisQC := QC{}
	h1, qc1 := runRound(t, reps, Hash{}, genesisQC, []byte("op1"))
	h2, qc2 := runRound(t, reps, h1, qc1, []byte("op2"))
	h3, qc3 := runRound(t, reps, h2, qc2, []byte("op3"))
	_, _ = runRound(t, reps, h3, qc3, []byte("op4"))

	// After 4 rounds, h1 and h2 commit across all replicas; h3 also
	// commits on the round-4 leader (replica 3) via the session-2
	// HandleVote→processJustify integration — that leader forms qc4
	// and immediately fires the two-chain commit on h3. The log is
	// shared across replicas, so the union is h1, h2, h3. All
	// appends are idempotent (same hash at same height = no-op).
	entries, err := ls.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 committed entries in log (h1, h2, h3); got %d", len(entries))
	}
	if entries[0].Block.Hash() != h1 {
		t.Fatalf("log[0] hash mismatch: expected h1")
	}
	if entries[1].Block.Hash() != h2 {
		t.Fatalf("log[1] hash mismatch: expected h2")
	}
	if entries[2].Block.Hash() != h3 {
		t.Fatalf("log[2] hash mismatch: expected h3")
	}
}

func TestReplayReconstructsCommittedState(t *testing.T) {
	// Drive one cluster through 4 rounds with a log attached.
	reps := newCluster(t, 4, 1)
	ls := NewInMemoryLogStore()
	for _, r := range reps {
		r.SetLog(ls)
	}
	genesisQC := QC{}
	h1, qc1 := runRound(t, reps, Hash{}, genesisQC, []byte("op1"))
	h2, qc2 := runRound(t, reps, h1, qc1, []byte("op2"))
	h3Hash, qc3 := runRound(t, reps, h2, qc2, []byte("op3"))
	_, _ = runRound(t, reps, h3Hash, qc3, []byte("op4"))

	// Spin up a fresh replica (simulating a crashed-and-restarted
	// node) and replay the shared log.
	roster := []ReplicaID{0, 1, 2, 3}
	keys, pubRoster, err := GenerateBLSKeyset(4)
	if err != nil {
		t.Fatalf("GenerateBLSKeyset: %v", err)
	}
	signer, err := NewBLSSigner(0, keys[0], pubRoster)
	if err != nil {
		t.Fatalf("NewBLSSigner: %v", err)
	}
	net := NewInMemoryNetwork(roster, 64)
	fresh, err := NewReplica(0, 4, 1, signer, net[0], roster)
	if err != nil {
		t.Fatalf("NewReplica: %v", err)
	}
	if err := Replay(fresh, ls); err != nil {
		t.Fatalf("Replay: %v", err)
	}

	// Post-replay: the fresh replica's committed log contains genesis
	// plus h1, h2, and h3 (round-4 leader committed h3 via the
	// session-2 HandleVote→processJustify integration).
	committed := fresh.Committed()
	if len(committed) != 4 {
		t.Fatalf("after replay, committed log has %d entries; want 4 (genesis + h1 + h2 + h3)", len(committed))
	}
	if committed[0] != (Hash{}) {
		t.Fatalf("replayed committed[0] is not genesis")
	}
	if committed[1] != h1 {
		t.Fatalf("replayed committed[1] != h1")
	}
	if committed[2] != h2 {
		t.Fatalf("replayed committed[2] != h2")
	}
	if committed[3] != h3Hash {
		t.Fatalf("replayed committed[3] != h3")
	}
	// The replica is now in view 4 (one past the last committed
	// block's view of 3) — ready to participate.
	if fresh.View() != 4 {
		t.Fatalf("after replay, view = %d; want 4", fresh.View())
	}
}
