package payments

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRequireAuth_RejectsAnonymous verifies that GEMOT_REQUIRE_AUTH=1 closes
// the sandbox-degrade hole on /mcp. With cfg.Enabled=false (no Stripe) the
// middleware still has a bearer-less path that falls into sandbox mode
// (rate-limited by IP). RequireAuth=true must reject instead of degrade.
func TestRequireAuth_RejectsAnonymous(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	cases := []struct {
		name string
		cfg  Config
	}{
		{
			name: "dev mode (Enabled=false) with bearer secret set",
			cfg:  Config{RequireAuth: true},
		},
		{
			name: "MPP mode (Enabled=true) with no Stripe",
			cfg:  Config{Enabled: true, HMACSecret: "x", Realm: "test", RequireAuth: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mw := Middleware(ctx, tc.cfg, "admin-secret")(inner)

			req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous request: want 401, got %d (body=%q)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "required") {
				t.Errorf("expected helpful error mentioning required auth, got %q", rec.Body.String())
			}
		})
	}
}

// TestRequireAuth_AcceptsAdminBearer verifies that valid auth still passes
// when RequireAuth is on — the gate is meant to reject *anonymous*, not all
// traffic.
func TestRequireAuth_AcceptsAdminBearer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})

	cfg := Config{RequireAuth: true}
	mw := Middleware(ctx, cfg, "admin-secret")(inner)

	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer admin-secret")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("admin request: want 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("inner handler not reached on admin bearer")
	}
}

// TestRequireAuth_OffPreservesSandbox confirms that with RequireAuth=false
// (the public-gemot.dev default) anonymous traffic still degrades to
// sandbox — we haven't regressed the hosted onboarding path.
func TestRequireAuth_OffPreservesSandbox(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sawSandbox := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, _ := r.Context().Value(ContextKeySandbox{}).(bool); v {
			sawSandbox = true
		}
		w.WriteHeader(200)
	})

	cfg := Config{RequireAuth: false} // explicit
	mw := Middleware(ctx, cfg, "admin-secret")(inner)

	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous (sandbox) request: want 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	if !sawSandbox {
		t.Fatal("ContextKeySandbox not set on anonymous path")
	}
}
