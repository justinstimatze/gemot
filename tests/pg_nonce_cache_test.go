package tests

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
)

func TestPostgresNonceCache_FirstObservePasses(t *testing.T) {
	db := tempDB(t).RawDB()
	c := auth.NewPostgresNonceCache(db, 0)
	if err := c.Observe("pg-n1", time.Now()); err != nil {
		t.Fatalf("first observe: %v", err)
	}
}

func TestPostgresNonceCache_RejectsReplay(t *testing.T) {
	db := tempDB(t).RawDB()
	c := auth.NewPostgresNonceCache(db, 0)
	now := time.Now()
	if err := c.Observe("pg-replay", now); err != nil {
		t.Fatal(err)
	}
	if err := c.Observe("pg-replay", now); !errors.Is(err, auth.ErrReplay) {
		t.Fatalf("want ErrReplay on duplicate nonce, got %v", err)
	}
}

func TestPostgresNonceCache_JanitorExpiresRows(t *testing.T) {
	// Short TTL so the janitor has something to sweep within the test window.
	db := tempDB(t).RawDB()
	c := auth.NewPostgresNonceCache(db, 50*time.Millisecond)
	if err := c.Observe("pg-expire", time.Now()); err != nil {
		t.Fatal(err)
	}

	// Run the janitor on a tight interval so we don't have to wait long.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartJanitor(ctx, 25*time.Millisecond)

	// Wait past expiry + a sweep cycle. After the janitor runs, the row should
	// be gone and re-observing the same nonce must succeed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := c.Observe("pg-expire", time.Now())
		if err == nil {
			return // success — row was swept, re-observation allowed
		}
		if !errors.Is(err, auth.ErrReplay) {
			t.Fatalf("unexpected error during poll: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("janitor never swept expired row within 2s")
}

func TestPostgresNonceCache_ConcurrentSafeAcrossReplicas(t *testing.T) {
	// Simulate two replicas sharing the same DB: two independent cache
	// instances both try to Observe the same nonce. Exactly one must win.
	db := tempDB(t).RawDB()
	a := auth.NewPostgresNonceCache(db, 0)
	b := auth.NewPostgresNonceCache(db, 0)

	const N = 50
	var wg sync.WaitGroup
	// atomic.Int32 so that a regression letting both replicas win the race
	// is observed correctly (non-atomic ++ would lose one of the two writes
	// and silently look like a single-winner success).
	winners := make([]atomic.Int32, N)
	for i := range N {
		// Iteration index guarantees nonce uniqueness without depending on
		// wall-clock resolution.
		nonce := "concurrent-" + strconv.Itoa(i)
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := a.Observe(nonce, time.Now()); err == nil {
				winners[i].Add(1)
			} else if !errors.Is(err, auth.ErrReplay) {
				t.Errorf("cache A unexpected error: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := b.Observe(nonce, time.Now()); err == nil {
				winners[i].Add(1)
			} else if !errors.Is(err, auth.ErrReplay) {
				t.Errorf("cache B unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	for i := range winners {
		if w := winners[i].Load(); w != 1 {
			t.Errorf("nonce %d: want exactly 1 winner across replicas, got %d", i, w)
		}
	}
}
