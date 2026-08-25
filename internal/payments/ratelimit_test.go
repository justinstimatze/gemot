package payments

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestRateLimiterSetRateLimitHeaders covers item 5 of the Is Agentic audit:
// standard rate-limit headers so an agent can self-throttle instead of
// guessing. Verifies the IETF-draft RateLimit-* headers are present and
// numerically sane on both an allowed and a rejected request, and that
// Retry-After appears only on rejection.
func TestRateLimiterSetRateLimitHeaders(t *testing.T) {
	ctx := context.Background()
	rl := NewRateLimiter(ctx, 2, time.Minute)

	checkHeaders := func(t *testing.T, w http.ResponseWriter, wantRemaining string) {
		t.Helper()
		if got := w.Header().Get("RateLimit-Limit"); got != "2" {
			t.Errorf("RateLimit-Limit = %q, want 2", got)
		}
		if got := w.Header().Get("RateLimit-Remaining"); got != wantRemaining {
			t.Errorf("RateLimit-Remaining = %q, want %q", got, wantRemaining)
		}
		if got := w.Header().Get("RateLimit-Reset"); got == "" {
			t.Error("RateLimit-Reset missing")
		}
	}

	t.Run("allowed request: no Retry-After, remaining decremented", func(t *testing.T) {
		rec := httptest.NewRecorder()
		allowed := rl.Allow("key-a")
		rl.SetRateLimitHeaders(rec, "key-a", allowed)
		if !allowed {
			t.Fatal("first request should be allowed")
		}
		checkHeaders(t, rec, "1")
		if rec.Header().Get("Retry-After") != "" {
			t.Error("Retry-After must not be set on an allowed request")
		}
	})

	t.Run("rejected request: Retry-After present, remaining is 0", func(t *testing.T) {
		rl.Allow("key-b") // 1/2
		rl.Allow("key-b") // 2/2 — at limit
		rec := httptest.NewRecorder()
		allowed := rl.Allow("key-b") // 3rd — rejected
		rl.SetRateLimitHeaders(rec, "key-b", allowed)
		if allowed {
			t.Fatal("third request should be rejected")
		}
		checkHeaders(t, rec, "0")
		if rec.Header().Get("Retry-After") == "" {
			t.Error("Retry-After must be set on a rejected (429) request")
		}
	})
}

// TestRateLimiterStatusIsReadOnly ensures Status never consumes a request —
// calling it repeatedly must not affect Allow's own accounting.
func TestRateLimiterStatusIsReadOnly(t *testing.T) {
	rl := NewRateLimiter(context.Background(), 1, time.Minute)
	for i := 0; i < 5; i++ {
		rl.Status("key")
	}
	if !rl.Allow("key") {
		t.Fatal("Status calls must not consume the rate limit budget")
	}
	if rl.Allow("key") {
		t.Fatal("second Allow should be rejected at limit=1")
	}
}
