package bft

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func (l *lockingInMemoryLog) WithLock(_ context.Context, _ int64, fn func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return fn()
}

// lockingInMemoryLog is an InMemoryLogStore that ALSO implements
// bft.ClusterLocker via a shared mutex — the in-process analog of
// PostgresLogStore's pg_advisory_lock. Two engines bootstrapped against one
// instance model two machines sharing one Postgres log plus the cluster-wide
// append lock. Because it implements ClusterLocker, BootstrapSingleNode wires
// it automatically, exercising the real production wiring path.
type lockingInMemoryLog struct {
	*InMemoryLogStore
	mu *sync.Mutex
}

// TestClusterLockPreventsForkUnderConcurrency is the regression for the
// height-4800 production incident: multiple gemot machines sharing one BFT log,
// each running its own single-node engine, forked the shared log and then
// wedged participant writes (ErrLogForkDetected, then "already proposed in this
// view"). With a ClusterLocker installed, the shared append lock serializes the
// propose→append→commit round across "machines" so only one engine appends at a
// time and the resync-on-fork retry converges instead of perpetually re-racing
// an active peer.
//
// Two engines share one log + one lock + shared vote-history/keys (exactly the
// production topology) and submit concurrently. Under the shared append lock
// the lagging engine always loses the append race, resyncs from the log head,
// and retries uncontested — so no submit fails and the shared log stays one
// gapless ascending chain.
func TestClusterLockPreventsForkUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	shared := &lockingInMemoryLog{InMemoryLogStore: NewInMemoryLogStore(), mu: &sync.Mutex{}}
	keys := NewInMemoryReplicaKeyStore() // shared identity — both are replica-0
	vh := NewInMemoryVoteHistoryStore()  // shared anti-equivocation counters

	a, err := BootstrapSingleNode(ctx, shared, vh, keys)
	if err != nil {
		t.Fatalf("bootstrap A: %v", err)
	}
	b, err := BootstrapSingleNode(ctx, shared, vh, keys)
	if err != nil {
		t.Fatalf("bootstrap B: %v", err)
	}
	// Guard: the lock must actually be wired, else the test is vacuous.
	if a.clusterLock == nil || b.clusterLock == nil {
		t.Fatal("cluster lock not wired by BootstrapSingleNode — test would prove nothing")
	}

	const perEngine = 15
	var wg sync.WaitGroup
	errs := make(chan error, 2*perEngine)
	submit := func(e *Engine, tag string) {
		defer wg.Done()
		for i := 0; i < perEngine; i++ {
			if _, _, err := e.Submit(ctx, []byte(tag)); err != nil {
				errs <- fmt.Errorf("%s submit %d: %w", tag, i, err)
				return
			}
		}
	}
	wg.Add(2)
	go submit(a, "a")
	go submit(b, "b")
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("submit failed under cluster lock (fork/regression should have been serialized away): %v", err)
	}

	// The shared log is one gapless ascending chain — no fork, no divergence.
	entries, err := shared.Load(ctx)
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
