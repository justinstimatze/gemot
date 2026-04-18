package bft

import (
	"context"
	"fmt"
)

// BootstrapSingleNode constructs a ready-to-serve single-node BFT
// engine: loads (or generates on first boot) the replica's BLS
// keypair from keyStore, constructs the replica with the provided
// log + vote history stores, replays the log + restores the
// vote-history counters, and returns the engine. Passing a durable
// keyStore (e.g., PostgresReplicaKeyStore) is required for QCs to
// remain verifiable across restarts — session 5b's fresh-per-boot
// keys broke that contract, and session 5c fixes it via persisted
// keys.
//
// A nil keyStore falls back to a process-local in-memory store —
// this is test-only and reproduces the pre-5c behavior where QCs
// from a prior boot cannot be verified after restart.
func BootstrapSingleNode(ctx context.Context, log LogStore, voteHist VoteHistoryStore, keyStore ReplicaKeyStore) (*Engine, error) {
	if keyStore == nil {
		keyStore = NewInMemoryReplicaKeyStore()
	}
	myKey, err := keyStore.LoadOrGenerate(ctx, ReplicaID(0))
	if err != nil {
		return nil, fmt.Errorf("bft: load replica keypair: %w", err)
	}
	pubRoster := []BLSPublicKey{myKey.Public}
	signer, err := NewBLSSigner(0, myKey, pubRoster)
	if err != nil {
		return nil, fmt.Errorf("bft: construct signer: %w", err)
	}
	roster := []ReplicaID{0}
	net := NewInMemoryNetwork(roster, 64)
	replica, err := NewReplica(0, 1, 0, signer, net[0], roster)
	if err != nil {
		return nil, fmt.Errorf("bft: construct replica: %w", err)
	}
	if log != nil {
		if err := Replay(replica, log); err != nil {
			return nil, fmt.Errorf("bft: replay log: %w", err)
		}
	}
	var lastProposed View
	if voteHist != nil {
		var lastVoted View
		lastVoted, lastProposed, err = voteHist.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("bft: load vote history: %w", err)
		}
		if err := replica.RestoreVoteHistory(ctx, voteHist); err != nil {
			return nil, fmt.Errorf("bft: restore vote history: %w", err)
		}
		_ = lastVoted
	}
	// After Replay, replica.view = last-committed-view + 1. But the
	// prior boot may have proposed (or voted) past that view without
	// committing — those prepared-but-uncommitted views live in the
	// vote-history counters, not the log. The next Submit must
	// propose at a view strictly greater than proposedInView, so
	// advance the replica past it.
	currentView := replica.View()
	needed := lastProposed + 1
	if needed > currentView {
		if err := replica.AdvanceView(needed); err != nil {
			return nil, fmt.Errorf("bft: advance view past prior proposedInView: %w", err)
		}
	}
	engine := NewEngine(replica, net[0])
	engine.RestoreChainState()
	return engine, nil
}
