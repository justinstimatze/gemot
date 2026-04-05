package cost

import (
	"sync"
	"testing"
	"time"
)

func TestRecordAndGet(t *testing.T) {
	tr := NewTracker()
	tr.Record("d1", "claude-sonnet-4-6", 1000, 500)

	u := tr.Get("d1")
	if u.InputTokens != 1000 {
		t.Errorf("input tokens: got %d, want 1000", u.InputTokens)
	}
	if u.OutputTokens != 500 {
		t.Errorf("output tokens: got %d, want 500", u.OutputTokens)
	}
	if u.TotalTokens != 1500 {
		t.Errorf("total tokens: got %d, want 1500", u.TotalTokens)
	}
	if u.Calls != 1 {
		t.Errorf("calls: got %d, want 1", u.Calls)
	}
	// Sonnet pricing: 3.0/M input + 15.0/M output = 0.003 + 0.0075 = 0.0105
	if u.EstCostUSD < 0.010 || u.EstCostUSD > 0.011 {
		t.Errorf("est cost: got %f, want ~0.0105", u.EstCostUSD)
	}
}

func TestAccumulation(t *testing.T) {
	tr := NewTracker()
	tr.Record("d1", "claude-sonnet-4-6", 100, 50)
	tr.Record("d1", "claude-sonnet-4-6", 200, 100)

	u := tr.Get("d1")
	if u.InputTokens != 300 || u.OutputTokens != 150 || u.Calls != 2 {
		t.Errorf("accumulation failed: input=%d output=%d calls=%d", u.InputTokens, u.OutputTokens, u.Calls)
	}
}

func TestUnknownModelFallsBackToSonnet(t *testing.T) {
	tr := NewTracker()
	tr.Record("d1", "unknown-model", 1_000_000, 0)

	u := tr.Get("d1")
	// Sonnet input pricing: $3.0/M tokens = $3.0 for 1M tokens
	if u.EstCostUSD < 2.9 || u.EstCostUSD > 3.1 {
		t.Errorf("expected ~$3.0 for unknown model fallback, got %f", u.EstCostUSD)
	}
}

func TestGetNonexistent(t *testing.T) {
	tr := NewTracker()
	u := tr.Get("nonexistent")
	if u.TotalTokens != 0 || u.Calls != 0 {
		t.Error("expected zero usage for nonexistent deliberation")
	}
}

func TestReset(t *testing.T) {
	tr := NewTracker()
	tr.Record("d1", "claude-sonnet-4-6", 100, 50)
	tr.Reset("d1")

	u := tr.Get("d1")
	if u.TotalTokens != 0 {
		t.Error("expected zero after reset")
	}
}

func TestCleanup(t *testing.T) {
	tr := NewTracker()
	tr.Record("old", "claude-sonnet-4-6", 100, 50)

	// Manually backdate the entry
	tr.mu.Lock()
	tr.costs["old"].LastAccess = time.Now().Add(-2 * time.Hour)
	tr.mu.Unlock()

	tr.Record("new", "claude-sonnet-4-6", 100, 50)

	tr.Cleanup(1 * time.Hour)

	if tr.Get("old").TotalTokens != 0 {
		t.Error("old entry should have been cleaned up")
	}
	if tr.Get("new").TotalTokens == 0 {
		t.Error("new entry should not have been cleaned up")
	}
}

func TestConcurrentAccess(t *testing.T) {
	tr := NewTracker()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr.Record("d1", "claude-sonnet-4-6", 10, 5)
		}()
	}
	wg.Wait()

	u := tr.Get("d1")
	if u.InputTokens != 1000 || u.OutputTokens != 500 || u.Calls != 100 {
		t.Errorf("concurrent accumulation: input=%d output=%d calls=%d", u.InputTokens, u.OutputTokens, u.Calls)
	}
}

func TestModelPricing(t *testing.T) {
	tr := NewTracker()

	// Opus: $15/M input, $75/M output
	tr.Record("opus", "claude-opus-4-6", 1_000_000, 1_000_000)
	u := tr.Get("opus")
	expected := 15.0 + 75.0 // $90
	if u.EstCostUSD < expected-0.1 || u.EstCostUSD > expected+0.1 {
		t.Errorf("opus pricing: got %f, want ~%f", u.EstCostUSD, expected)
	}

	// Haiku: $0.80/M input, $4.0/M output
	tr.Record("haiku", "claude-haiku-4-5", 1_000_000, 1_000_000)
	u = tr.Get("haiku")
	expected = 0.80 + 4.0 // $4.80
	if u.EstCostUSD < expected-0.1 || u.EstCostUSD > expected+0.1 {
		t.Errorf("haiku pricing: got %f, want ~%f", u.EstCostUSD, expected)
	}
}
