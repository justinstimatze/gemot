package payments

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestMiddlewareEnabledWithEmptyBearerSecretGrantsNoAdminBypass is the
// regression test for a real vulnerability caught (and reverted) during
// this session's own admin-token consolidation: with MPP payments enabled
// (cfg.Enabled) and no bearerSecret configured, the admin-secret branch of
// Middleware must NOT use IsAdminToken directly -- IsAdminToken's
// "empty apiSecret means everyone is admin" convention is only correct for
// the demo-mode (!cfg.Enabled) fallback path, which has its own explicit
// bearerSecret == "" handling further down. Naively swapping in
// IsAdminToken here would have granted admin access to ANY bearer token
// whenever bearerSecret was unset and payments were live.
func TestMiddlewareEnabledWithEmptyBearerSecretGrantsNoAdminBypass(t *testing.T) {
	var reached, reachedAsAdmin bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		reachedAsAdmin, _ = r.Context().Value(ContextKeyIsAdmin{}).(bool)
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(context.Background(), Config{Enabled: true}, "")(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer some-arbitrary-string-not-a-real-secret")
	handler.ServeHTTP(rec, req)

	if reached && reachedAsAdmin {
		t.Fatal("an arbitrary bearer token must not be granted admin access when bearerSecret is empty and MPP is enabled")
	}
}
