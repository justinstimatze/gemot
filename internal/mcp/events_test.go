package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
	"github.com/justinstimatze/gemot/internal/store"
)

// TestEventsHandlerAdminSecretOnlyViaHeader is the regression test for the
// code-review finding that /events uniquely accepted the admin secret via a
// ?token= query parameter (unlike /metrics, /export, and /a2a, which are
// header-only) — a query string can leak into access logs, browser
// history, and Referer headers in a way a header never does. The admin
// secret must now be rejected via ?token=, even though a plain API key
// still works there (browser EventSource clients can't set headers).
func TestEventsHandlerAdminSecretOnlyViaHeader(t *testing.T) {
	const apiSecret = "test-admin-secret"
	backend := store.NewMemoryStore()
	svc := deliberation.NewService(backend, nil)
	svc.SetEventBus(deliberation.NewEventBus())
	limiter := payments.NewRateLimiter(context.Background(), 100, time.Minute)
	handler := EventsHandler(svc, nil, apiSecret, limiter)

	t.Run("admin secret via query param is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/events?token="+apiSecret, nil)
		handler(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("admin secret via Authorization header is accepted", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+apiSecret)
		handler(rec, req)
		if rec.Code != 0 && rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (or unset, since SSE never calls WriteHeader), body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"type":"connected"`) {
			t.Errorf("expected a connected event, got: %s", rec.Body.String())
		}
	})

	t.Run("missing credential is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/events", nil)
		handler(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401, body=%s", rec.Code, rec.Body.String())
		}
	})
}
