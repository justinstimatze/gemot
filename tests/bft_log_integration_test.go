package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/justinstimatze/gemot/internal/bft"
	"github.com/justinstimatze/gemot/internal/store"
)

// TestBFTLogAppendLoadRoundTripPostgres exercises the session-4
// bft_log table against real Postgres. Covers: append happy path,
// height-ordered Load, idempotent re-append, HighestHeight.
func TestBFTLogAppendLoadRoundTripPostgres(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()
	ls := store.NewPostgresLogStore(db)

	// Empty state.
	h, err := ls.HighestHeight(ctx)
	if err != nil {
		t.Fatalf("HighestHeight on empty log: %v", err)
	}
	if h != 0 {
		t.Fatalf("empty HighestHeight = %d, want 0", h)
	}

	// Append 3 entries.
	for i := 1; i <= 3; i++ {
		block := bft.Block{Height: bft.Height(i), View: bft.View(i), Payload: []byte{byte(i)}}
		qc := bft.QC{View: bft.View(i), BlockHash: block.Hash()}
		if err := ls.Append(ctx, bft.LogEntry{Block: block, QC: qc}); err != nil {
			t.Fatalf("Append height %d: %v", i, err)
		}
	}

	h, err = ls.HighestHeight(ctx)
	if err != nil {
		t.Fatalf("HighestHeight: %v", err)
	}
	if h != 3 {
		t.Fatalf("HighestHeight = %d, want 3", h)
	}

	entries, err := ls.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("Load returned %d entries, want 3", len(entries))
	}
	for i, e := range entries {
		if e.Block.Height != bft.Height(i+1) {
			t.Fatalf("entry[%d].Height = %d, want %d", i, e.Block.Height, i+1)
		}
	}
}

// TestBFTLogForkDetectionPostgres confirms a duplicate-height append
// with a different block hash surfaces as ErrLogForkDetected. The
// INSERT ... ON CONFLICT DO NOTHING path preserves the first-write
// and the post-insert compare catches the fork.
func TestBFTLogForkDetectionPostgres(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()
	ls := store.NewPostgresLogStore(db)

	blockA := bft.Block{Height: 1, View: 1, Payload: []byte("A")}
	blockB := bft.Block{Height: 1, View: 1, Payload: []byte("B")} // same height, different payload → different hash

	if err := ls.Append(ctx, bft.LogEntry{Block: blockA, QC: bft.QC{View: 1, BlockHash: blockA.Hash()}}); err != nil {
		t.Fatalf("Append A: %v", err)
	}
	// Re-append identical block — idempotent.
	if err := ls.Append(ctx, bft.LogEntry{Block: blockA, QC: bft.QC{View: 1, BlockHash: blockA.Hash()}}); err != nil {
		t.Fatalf("re-Append A should be idempotent: %v", err)
	}
	// Append conflicting block at same height — fork.
	err := ls.Append(ctx, bft.LogEntry{Block: blockB, QC: bft.QC{View: 1, BlockHash: blockB.Hash()}})
	if !errors.Is(err, bft.ErrLogForkDetected) {
		t.Fatalf("expected ErrLogForkDetected for conflicting height-1 block; got %v", err)
	}
}
