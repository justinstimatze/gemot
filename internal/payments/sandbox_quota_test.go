package payments

import (
	"sync"
	"testing"
	"time"
)

func TestSandboxQuota_AllowsUnderLimit(t *testing.T) {
	q := NewSandboxQuota(3, time.Hour)
	for i := 0; i < 3; i++ {
		allowed, remaining := q.Allow("1.2.3.4")
		if !allowed {
			t.Fatalf("call %d: expected allow, got deny (remaining=%d)", i+1, remaining)
		}
	}
}

func TestSandboxQuota_DeniesAboveLimit(t *testing.T) {
	q := NewSandboxQuota(3, time.Hour)
	for i := 0; i < 3; i++ {
		q.Allow("1.2.3.4")
	}
	allowed, _ := q.Allow("1.2.3.4")
	if allowed {
		t.Fatal("4th call should be denied — quota of 3 exhausted")
	}
}

func TestSandboxQuota_PerIdentityIsolation(t *testing.T) {
	q := NewSandboxQuota(2, time.Hour)
	q.Allow("1.2.3.4")
	q.Allow("1.2.3.4")
	// Different IP gets its own quota
	if allowed, _ := q.Allow("5.6.7.8"); !allowed {
		t.Fatal("different IP should have independent quota")
	}
	// First IP still over
	if allowed, _ := q.Allow("1.2.3.4"); allowed {
		t.Fatal("first IP should still be over quota")
	}
}

func TestSandboxQuota_WindowReset(t *testing.T) {
	// 1ms window for fast test
	q := NewSandboxQuota(2, 10*time.Millisecond)
	q.Allow("1.2.3.4")
	q.Allow("1.2.3.4")
	if allowed, _ := q.Allow("1.2.3.4"); allowed {
		t.Fatal("should be over quota before window expires")
	}
	time.Sleep(15 * time.Millisecond)
	if allowed, _ := q.Allow("1.2.3.4"); !allowed {
		t.Fatal("quota should reset after window expires")
	}
}

func TestSandboxQuota_EmptyIdentityNotTracked(t *testing.T) {
	q := NewSandboxQuota(1, time.Hour)
	// Empty identity is permitted without tracking — fail-open behavior
	// so an unexpectedly-empty IP doesn't cache-poison the quota.
	for i := 0; i < 100; i++ {
		if allowed, _ := q.Allow(""); !allowed {
			t.Fatalf("call %d: empty identity should always be permitted", i+1)
		}
	}
}

func TestSandboxQuota_Refund(t *testing.T) {
	q := NewSandboxQuota(2, time.Hour)
	q.Allow("1.2.3.4")
	q.Allow("1.2.3.4")
	if allowed, _ := q.Allow("1.2.3.4"); allowed {
		t.Fatal("should be over quota before refund")
	}
	q.Refund("1.2.3.4")
	if allowed, _ := q.Allow("1.2.3.4"); !allowed {
		t.Fatal("after refund, should have 1 quota slot back")
	}
}

func TestSandboxQuota_RefundNeverNegative(t *testing.T) {
	q := NewSandboxQuota(10, time.Hour)
	q.Allow("1.2.3.4") // count=1
	q.Refund("1.2.3.4")
	q.Refund("1.2.3.4") // should not go below 0
	q.Refund("1.2.3.4")
	// Still has full quota
	for i := 0; i < 10; i++ {
		if allowed, _ := q.Allow("1.2.3.4"); !allowed {
			t.Fatalf("call %d: refund-below-zero should not corrupt counter", i+1)
		}
	}
}

func TestSandboxQuota_Concurrent(t *testing.T) {
	q := NewSandboxQuota(100, time.Hour)
	var wg sync.WaitGroup
	allowed := make(chan bool, 200)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, _ := q.Allow("shared")
			allowed <- a
		}()
	}
	wg.Wait()
	close(allowed)
	count := 0
	for a := range allowed {
		if a {
			count++
		}
	}
	if count != 100 {
		t.Errorf("expected exactly 100 successful allows under concurrent load, got %d", count)
	}
}
