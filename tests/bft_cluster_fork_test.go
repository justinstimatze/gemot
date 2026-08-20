package tests

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/justinstimatze/gemot/internal/bft"
	"github.com/justinstimatze/gemot/internal/store"
)

// TestBFTClusterLockPreventsPostgresFork reproduces the height-4800 production
// incident against REAL Postgres. Two gemot "machines" — separate
// PostgresLogStore / vote-history / key-store instances over the same schema,
// i.e. the same shared tables — each run their own single-node BFT engine and
// submit concurrently. Before the fix, their independent single-node engines
// appended different blocks at the same height (ErrLogForkDetected) and then
// wedged on the shared anti-equivocation counters. With the pg_advisory_lock in
// PostgresLogStore.WithLock installed (auto-wired by BootstrapSingleNode because
// PostgresLogStore implements bft.ClusterLocker), the append path is serialized
// cluster-wide: no submit fails, and the shared bft_log is one gapless
// ascending chain.
func TestBFTClusterLockPreventsPostgresFork(t *testing.T) {
	db := tempDB(t) // isolated schema + schema.sql applied (bft_log, etc.)
	ctx := context.Background()

	// Two independent store layers over the SAME schema model two machines
	// sharing one Postgres. They share bft_log, bft_vote_history, and the
	// replica key — exactly the production topology (main.go wires all three to
	// one shared DB).
	logA := store.NewPostgresLogStore(db)
	logB := store.NewPostgresLogStore(db)
	engA, err := bft.BootstrapSingleNode(ctx, logA,
		store.NewPostgresVoteHistoryStore(db, bft.ReplicaID(0)),
		store.NewPostgresReplicaKeyStore(db))
	if err != nil {
		t.Fatalf("bootstrap machine A: %v", err)
	}
	engB, err := bft.BootstrapSingleNode(ctx, logB,
		store.NewPostgresVoteHistoryStore(db, bft.ReplicaID(0)),
		store.NewPostgresReplicaKeyStore(db))
	if err != nil {
		t.Fatalf("bootstrap machine B: %v", err)
	}

	const perEngine = 12
	var wg sync.WaitGroup
	errs := make(chan error, 2*perEngine)
	drive := func(e *bft.Engine, tag string) {
		defer wg.Done()
		for i := 0; i < perEngine; i++ {
			if _, _, err := e.Submit(ctx, []byte(fmt.Sprintf("%s-%d", tag, i))); err != nil {
				errs <- fmt.Errorf("machine %s submit %d: %w", tag, i, err)
				return
			}
		}
	}
	wg.Add(2)
	go drive(engA, "a")
	go drive(engB, "b")
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("submit failed — advisory lock should have serialized the append path: %v", err)
	}

	// The shared bft_log must be one gapless ascending chain: heights 1..N with
	// no fork and no gap. A fork would surface as a submit error above; a silent
	// divergence would show here as a height gap.
	entries, err := logA.Load(ctx)
	if err != nil {
		t.Fatalf("load shared log: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected committed blocks in shared bft_log")
	}
	for i, e := range entries {
		if int(e.Block.Height) != i+1 {
			t.Fatalf("bft_log height gap at index %d: got height %d (fork/divergence)", i, e.Block.Height)
		}
	}
	t.Logf("shared bft_log converged to %d gapless committed blocks across 2 machines", len(entries))
}
