package bft

import (
	"context"
	"fmt"
)

// BootstrapSingleNode constructs a ready-to-serve single-node BFT
// engine: generates a fresh BLS keypair (single-node — key rotation
// across restarts is harmless because there is no external client
// verifying QCs across boots yet), constructs the replica with the
// provided log + vote history stores, replays the log + restores the
// vote-history counters, and returns the engine.
//
// Fresh-key-per-boot is a single-node simplification. A multi-node or
// client-verifiable configuration MUST persist the keypair so the
// replica's public key is stable across restarts; otherwise a QC
// issued under boot-N's key cannot be verified after boot-N+1.
// Session 5c wires persisted keys (DB-stored or file-stored) when
// client-side QC verification becomes a real requirement.
func BootstrapSingleNode(ctx context.Context, log LogStore, voteHist VoteHistoryStore) (*Engine, error) {
	keys, pubRoster, err := GenerateBLSKeyset(1)
	if err != nil {
		return nil, fmt.Errorf("bft: generate BLS keypair: %w", err)
	}
	signer, err := NewBLSSigner(0, keys[0], pubRoster)
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
