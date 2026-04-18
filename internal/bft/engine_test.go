package bft

import (
	"context"
	"testing"
)

// Session-5b engine tests: single-node Submit roundtrip, QC
// progression across multiple Submits, and durable-log persistence
// of committed blocks (session-4 integration).

func newSingleNodeEngine(t *testing.T) (*Engine, *Replica, *InMemoryLogStore) {
	t.Helper()
	roster := []ReplicaID{0}
	keys, pubRoster, err := GenerateBLSKeyset(1)
	if err != nil {
		t.Fatalf("GenerateBLSKeyset: %v", err)
	}
	signer, err := NewBLSSigner(0, keys[0], pubRoster)
	if err != nil {
		t.Fatalf("NewBLSSigner: %v", err)
	}
	net := NewInMemoryNetwork(roster, 64)
	r, err := NewReplica(0, 1, 0, signer, net[0], roster)
	if err != nil {
		t.Fatalf("NewReplica: %v", err)
	}
	log := NewInMemoryLogStore()
	r.SetLog(log)
	r.SetVoteHistory(NewInMemoryVoteHistoryStore())
	return NewEngine(r, net[0]), r, log
}

func TestEngineSingleNodeSubmit(t *testing.T) {
	eng, _, _ := newSingleNodeEngine(t)
	ctx := context.Background()

	qc, block, err := eng.Submit(ctx, []byte("op1"))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if qc.View != 1 {
		t.Fatalf("first Submit prepared QC view = %d; want 1", qc.View)
	}
	if block.Height != 1 {
		t.Fatalf("first Submit block height = %d; want 1", block.Height)
	}
	if qc.BlockHash != block.Hash() {
		t.Fatalf("QC hash != block hash")
	}
}

// Commit fires on the SECOND Submit: the two-chain rule commits the
// first block when its successor carries its QC as justify.
func TestEngineTwoChainCommit(t *testing.T) {
	eng, _, log := newSingleNodeEngine(t)
	ctx := context.Background()

	_, block1, err := eng.Submit(ctx, []byte("op1"))
	if err != nil {
		t.Fatalf("Submit 1: %v", err)
	}
	// After first Submit: QC formed but block1 NOT yet committed —
	// the two-chain rule waits for a successor.
	entries, err := log.Load(ctx)
	if err != nil {
		t.Fatalf("Load 1: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("after 1 Submit, log has %d entries; want 0 (commit waits for successor)", len(entries))
	}

	_, block2, err := eng.Submit(ctx, []byte("op2"))
	if err != nil {
		t.Fatalf("Submit 2: %v", err)
	}
	if block2.Parent != block1.Hash() {
		t.Fatalf("block2.Parent != block1.Hash")
	}
	// After second Submit: block1 committed via two-chain (QC on
	// block2 with block1.view == block2.view - 1).
	entries, err = log.Load(ctx)
	if err != nil {
		t.Fatalf("Load 2: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("after 2 Submits, log has %d entries; want 1 (block1 committed)", len(entries))
	}
	if entries[0].Block.Hash() != block1.Hash() {
		t.Fatalf("committed entry != block1")
	}

	// Third Submit commits block2.
	_, _, err = eng.Submit(ctx, []byte("op3"))
	if err != nil {
		t.Fatalf("Submit 3: %v", err)
	}
	entries, err = log.Load(ctx)
	if err != nil {
		t.Fatalf("Load 3: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("after 3 Submits, log has %d entries; want 2", len(entries))
	}
}
