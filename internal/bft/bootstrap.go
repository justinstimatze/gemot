package bft

import (
	"context"
	"fmt"
)

// BootstrapSingleNode constructs a ready-to-serve single-node BFT engine:
// builds and recovers the replica from the durable stores (see
// buildSingleNodeReplica), then returns an engine that retains those stores so
// it can resync from the log if a concurrent instance forks an append.
//
// Passing a durable keyStore (e.g., PostgresReplicaKeyStore) is required for
// QCs to remain verifiable across restarts — session 5b's fresh-per-boot keys
// broke that contract, and session 5c fixes it via persisted keys. A nil
// keyStore falls back to a process-local in-memory store — test-only, and
// reproduces the pre-5c behavior where QCs from a prior boot cannot be
// verified after restart.
func BootstrapSingleNode(ctx context.Context, log LogStore, voteHist VoteHistoryStore, keyStore ReplicaKeyStore) (*Engine, error) {
	replica, transport, err := buildSingleNodeReplica(ctx, log, voteHist, keyStore)
	if err != nil {
		return nil, err
	}
	engine := NewEngine(replica, transport)
	// Retain the stores so the engine can rebuild from the log on fork.
	engine.log = log
	engine.voteHist = voteHist
	engine.keyStore = keyStore
	engine.RestoreChainState()
	return engine, nil
}

// buildSingleNodeReplica constructs and recovers a single-node (N=1, F=0)
// replica from the given durable stores: it loads/generates the replica-0 key,
// constructs the replica, replays the committed log, restores the
// anti-equivocation vote history, and advances the view past any prior
// proposed-but-uncommitted view. Shared by BootstrapSingleNode (first boot)
// and (*Engine).resyncFromLog (in-process recovery after a log fork) so both
// rebuild identical state from the same source of truth — the durable log.
func buildSingleNodeReplica(ctx context.Context, log LogStore, voteHist VoteHistoryStore, keyStore ReplicaKeyStore) (*Replica, Transport, error) {
	if keyStore == nil {
		keyStore = NewInMemoryReplicaKeyStore()
	}
	myKey, err := keyStore.LoadOrGenerate(ctx, ReplicaID(0))
	if err != nil {
		return nil, nil, fmt.Errorf("bft: load replica keypair: %w", err)
	}
	pubRoster := []BLSPublicKey{myKey.Public}
	signer, err := NewBLSSigner(0, myKey, pubRoster)
	if err != nil {
		return nil, nil, fmt.Errorf("bft: construct signer: %w", err)
	}
	roster := []ReplicaID{0}
	net := NewInMemoryNetwork(roster, 64)
	replica, err := NewReplica(0, 1, 0, signer, net[0], roster)
	if err != nil {
		return nil, nil, fmt.Errorf("bft: construct replica: %w", err)
	}
	if log != nil {
		if err := Replay(replica, log); err != nil {
			return nil, nil, fmt.Errorf("bft: replay log: %w", err)
		}
	}
	var lastProposed View
	if voteHist != nil {
		var lastVoted View
		lastVoted, lastProposed, err = voteHist.Load(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("bft: load vote history: %w", err)
		}
		if err := replica.RestoreVoteHistory(ctx, voteHist); err != nil {
			return nil, nil, fmt.Errorf("bft: restore vote history: %w", err)
		}
		_ = lastVoted
	}
	currentView := replica.View()
	needed := lastProposed + 1
	if needed > currentView {
		if err := replica.AdvanceView(needed); err != nil {
			return nil, nil, fmt.Errorf("bft: advance view past prior proposedInView: %w", err)
		}
	}
	return replica, net[0], nil
}
