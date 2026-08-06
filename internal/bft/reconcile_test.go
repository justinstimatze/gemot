package bft

import (
	"context"
	"testing"
)

// TestSubmitRecoversFromForkAndRetries simulates two gemot instances sharing
// one durable log — the multi-instance topology that caused the height-4792
// vote wedge. The behind instance loses an append race; Submit must resync
// from the log and retry rather than wedge with ErrLogForkDetected.
func TestSubmitRecoversFromForkAndRetries(t *testing.T) {
	ctx := context.Background()
	log := NewInMemoryLogStore()
	keys := NewInMemoryReplicaKeyStore() // shared: both instances are replica-0 with one key

	a, err := BootstrapSingleNode(ctx, log, NewInMemoryVoteHistoryStore(), keys)
	if err != nil {
		t.Fatalf("bootstrap A: %v", err)
	}
	b, err := BootstrapSingleNode(ctx, log, NewInMemoryVoteHistoryStore(), keys)
	if err != nil {
		t.Fatalf("bootstrap B: %v", err)
	}

	// A commits a block at height 1 (two-chain: prepare, then commit-on-next).
	if _, _, err := a.Submit(ctx, []byte("a1")); err != nil {
		t.Fatalf("A submit 1: %v", err)
	}
	if _, _, err := a.Submit(ctx, []byte("a2")); err != nil {
		t.Fatalf("A submit 2: %v", err)
	}

	// B is behind: it also proposes at height 1. First submit prepares b1.
	if _, _, err := b.Submit(ctx, []byte("b1")); err != nil {
		t.Fatalf("B submit 1: %v", err)
	}
	// B's second submit tries to commit b1 at height 1 — where A already
	// committed a different block. Pre-fix this wedged (ErrLogForkDetected,
	// then "already proposed in this view" forever). Now Submit must resync
	// from the log and retry to success.
	if _, _, err := b.Submit(ctx, []byte("b2")); err != nil {
		t.Fatalf("B submit 2 should have recovered from the fork, got: %v", err)
	}

	// Not wedged: another submit still works.
	if _, _, err := b.Submit(ctx, []byte("b3")); err != nil {
		t.Fatalf("B submit 3 (post-recovery): %v", err)
	}

	// The shared log is one consistent ascending chain — no divergence.
	entries, err := log.Load(ctx)
	if err != nil {
		t.Fatalf("load log: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected committed blocks in shared log")
	}
	for i, e := range entries {
		if int(e.Block.Height) != i+1 {
			t.Fatalf("log height gap at index %d: got height %d", i, e.Block.Height)
		}
	}
}

// TestResyncRequiresDurableLog: an engine with no durable log (constructed via
// NewEngine directly) cannot self-resync and must say so rather than proceed.
func TestResyncRequiresDurableLog(t *testing.T) {
	ctx := context.Background()
	keys := NewInMemoryReplicaKeyStore()
	myKey, err := keys.LoadOrGenerate(ctx, ReplicaID(0))
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	signer, err := NewBLSSigner(0, myKey, []BLSPublicKey{myKey.Public})
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	net := NewInMemoryNetwork([]ReplicaID{0}, 64)
	r, err := NewReplica(0, 1, 0, signer, net[0], []ReplicaID{0})
	if err != nil {
		t.Fatalf("replica: %v", err)
	}
	e := NewEngine(r, net[0]) // no durable stores → e.log == nil
	if err := e.resyncFromLog(ctx); err == nil {
		t.Fatal("expected resyncFromLog to error when no durable log is configured")
	}
}
