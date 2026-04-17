package auth

import (
	"errors"
	"testing"
	"time"
)

func TestValidateTimestamp_WithinWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	// Exactly on the window edge should still pass.
	if err := ValidateTimestamp(now.Unix()-int64(ReplayWindow.Seconds()), now); err != nil {
		t.Errorf("edge of window should pass, got %v", err)
	}
	if err := ValidateTimestamp(now.Unix(), now); err != nil {
		t.Errorf("same instant should pass, got %v", err)
	}
}

func TestValidateTimestamp_TooOld(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	past := now.Unix() - int64(ReplayWindow.Seconds()) - 1
	if err := ValidateTimestamp(past, now); !errors.Is(err, ErrStaleTimestamp) {
		t.Fatalf("want ErrStaleTimestamp, got %v", err)
	}
}

func TestValidateTimestamp_FutureSkew(t *testing.T) {
	// Symmetric — a client whose clock is too far ahead is also rejected.
	now := time.Unix(1_700_000_000, 0)
	future := now.Unix() + int64(ReplayWindow.Seconds()) + 1
	if err := ValidateTimestamp(future, now); !errors.Is(err, ErrStaleTimestamp) {
		t.Fatalf("future-skew timestamp must be rejected, got %v", err)
	}
}

func TestMemoryNonceCache_FirstObservationPasses(t *testing.T) {
	c := NewMemoryNonceCache(0, 0)
	if err := c.Observe("n1", time.Now()); err != nil {
		t.Fatalf("first observe: %v", err)
	}
}

func TestMemoryNonceCache_RejectsReplay(t *testing.T) {
	c := NewMemoryNonceCache(0, 0)
	now := time.Now()
	if err := c.Observe("n1", now); err != nil {
		t.Fatal(err)
	}
	if err := c.Observe("n1", now); !errors.Is(err, ErrReplay) {
		t.Fatalf("want ErrReplay on duplicate nonce, got %v", err)
	}
}

func TestMemoryNonceCache_ExpiresAfterTTL(t *testing.T) {
	// ttl=1s keeps the test fast; observe at t=0, replay at t=2s should pass.
	c := NewMemoryNonceCache(1*time.Second, 0)
	t0 := time.Unix(0, 0)
	if err := c.Observe("n1", t0); err != nil {
		t.Fatal(err)
	}
	if err := c.Observe("n1", t0.Add(2*time.Second)); err != nil {
		t.Fatalf("nonce should have expired, got %v", err)
	}
}

func TestMemoryNonceCache_CapacityEviction(t *testing.T) {
	c := NewMemoryNonceCache(time.Hour, 3)
	t0 := time.Unix(0, 0)
	for _, n := range []string{"a", "b", "c"} {
		if err := c.Observe(n, t0); err != nil {
			t.Fatal(err)
		}
	}
	// Inserting a 4th evicts the oldest ("a"). Re-observing "a" is then allowed
	// — that's the documented tradeoff when capacity is undersized for the
	// working set.
	if err := c.Observe("d", t0); err != nil {
		t.Fatal(err)
	}
	if err := c.Observe("a", t0); err != nil {
		t.Fatalf("evicted nonce should be observable again, got %v", err)
	}
	// "c" and "d" remain tracked after the cascaded eviction — replay must fail.
	if err := c.Observe("c", t0); !errors.Is(err, ErrReplay) {
		t.Fatalf("want ErrReplay for still-tracked nonce %q, got %v", "c", err)
	}
}
