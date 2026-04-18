package bft

import (
	"context"
	"fmt"
)

// Replay reconstructs a replica's committed state from a durable
// LogStore. Session-4 MVP: restores knownBlocks, committed,
// committedLog, view, highQC, and lockedQC from the persisted log.
// Does NOT restore lastVotedView or proposedInView — those are
// session-5 work because they require a separate durable
// vote-history table to preserve anti-equivocation across restarts
// under Byzantine peers. Session 4's replay is safe for crash-
// recovery of a single honest replica; it is NOT safe yet under a
// Byzantine adversary that can race the restart.
//
// Call on a freshly-constructed Replica (from NewReplica) BEFORE
// any protocol methods are driven. The passed log is also attached
// via SetLog so subsequent commits persist back to it.
//
// After a successful Replay:
//   - knownBlocks has every logged block + every QC'd block's parent
//     reachable via the logged QC chain.
//   - committed/committedLog contain exactly the logged committed
//     blocks plus genesis, in height order.
//   - view is max(logged_block.view) + 1 — the replica resumes in
//     the view AFTER the last one that produced a commit.
//   - highQC is the QC of the highest logged block.
//   - lockedQC is the QC of the second-highest logged block, or
//     genesis if only one block is logged. The "one chain behind
//     highQC" heuristic is conservative — under the locked-QC vote
//     rule a replica never unsafely unlocks from this position.
func Replay(r *Replica, log LogStore) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := log.Load(context.Background())
	if err != nil {
		return fmt.Errorf("bft: replay load: %w", err)
	}
	for i, e := range entries {
		h := e.Block.Hash()
		if e.Block.Height != Height(i+1) {
			return fmt.Errorf("bft: replay height gap at index %d: got %d want %d",
				i, e.Block.Height, i+1)
		}
		r.knownBlocks[h] = e.Block
		r.committed[h] = true
		r.committedLog = append(r.committedLog, h)
	}
	if len(entries) > 0 {
		last := entries[len(entries)-1]
		r.view = last.Block.View + 1
		r.highQC = last.QC
		if len(entries) >= 2 {
			r.lockedQC = entries[len(entries)-2].QC
		} else {
			// Only one committed block — no "one chain behind" yet.
			// lockedQC stays at genesis.
			r.lockedQC = QC{}
		}
		// preparedQC tracks the last block this replica voted for;
		// after replay we conservatively set it to the highest QC —
		// the replica effectively "voted" for the last committed
		// block in the sense that it contributed to that quorum.
		r.preparedQC = last.QC
	}
	r.log = log
	return nil
}
