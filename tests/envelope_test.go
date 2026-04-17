package tests

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
	"github.com/justinstimatze/gemot/internal/mcp"
	"github.com/justinstimatze/gemot/internal/payments"
)

// signEnvelope applies the envelope canonicalization used by EnvelopeMiddleware.
// The `method` argument must match what the middleware computes on its side —
// currently "VERB <request-URI>" where request-URI includes any query string.
func signEnvelope(t *testing.T, priv ed25519.PrivateKey, agentID, method string, body []byte, nonce string, ts int64) string {
	t.Helper()
	h := sha256.Sum256(body)
	msg := auth.EnvelopePayload(agentID, method, h[:], nonce, ts)
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))
}

// newEnvelopeRequest builds a POST request with the four envelope headers set.
func newEnvelopeRequest(t *testing.T, method, path string, body []byte, agentID, nonce string, ts int64, sigB64 string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set(mcp.HeaderEnvelopeAgentID, agentID)
	r.Header.Set(mcp.HeaderEnvelopeNonce, nonce)
	r.Header.Set(mcp.HeaderEnvelopeTimestamp, strconv.FormatInt(ts, 10))
	r.Header.Set(mcp.HeaderEnvelopeSignature, sigB64)
	return r
}

func TestEnvelopeMiddleware_OffPassesThrough(t *testing.T) {
	svc, _ := newTestService(t)
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	mw := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeOff, 0)
	h := mw(inner)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte("{}")))
	h.ServeHTTP(rw, r)

	if !called || rw.Code != 200 {
		t.Fatalf("off-mode must pass through, code=%d called=%v", rw.Code, called)
	}
}

func TestEnvelopeMiddleware_AdvisorySkipsWhenNoHeaders(t *testing.T) {
	svc, _ := newTestService(t)
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	mw := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeAdvisory, 0)
	h := mw(inner)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte("{}")))
	h.ServeHTTP(rw, r)

	if !called || rw.Code != 200 {
		t.Fatalf("advisory without headers must pass through, code=%d", rw.Code)
	}
}

func TestEnvelopeMiddleware_RequiredRejectsUnsigned(t *testing.T) {
	svc, _ := newTestService(t)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner must not be called when envelope required is missing")
	})
	mw := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeRequired, 0)
	h := mw(inner)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte("{}"))))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("required mode unsigned must be 401, got %d", rw.Code)
	}
}

func TestEnvelopeMiddleware_AcceptsValidSignature(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	bodyBytes := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1}`)
	ts := time.Now().Unix()
	sigB64 := signEnvelope(t, priv, "alice", "POST /mcp", bodyBytes, "nonce-1", ts)

	called := false
	var gotBody []byte
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		w.WriteHeader(200)
	})
	mw := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeRequired, 0)
	h := mw(inner)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, newEnvelopeRequest(t, http.MethodPost, "/mcp", bodyBytes, "alice", "nonce-1", ts, sigB64))

	if !called || rw.Code != 200 {
		t.Fatalf("valid envelope should pass, code=%d called=%v body=%q", rw.Code, called, rw.Body.String())
	}
	// Inner handler must still see the original body — middleware buffered + restored.
	if !bytes.Equal(gotBody, bodyBytes) {
		t.Fatalf("body not restored to inner handler: got %q want %q", gotBody, bodyBytes)
	}
}

func TestEnvelopeMiddleware_RejectsBadSignature(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	bodyBytes := []byte(`{"jsonrpc":"2.0"}`)
	ts := time.Now().Unix()
	// Sign one body, send a different one — should fail.
	sigB64 := signEnvelope(t, priv, "alice", "POST /mcp", bodyBytes, "n1", ts)

	mw := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeRequired, 0)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not reach inner on bad sig")
	}))

	rw := httptest.NewRecorder()
	tampered := []byte(`{"jsonrpc":"2.0","extra":1}`)
	h.ServeHTTP(rw, newEnvelopeRequest(t, http.MethodPost, "/mcp", tampered, "alice", "n1", ts, sigB64))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("tampered body must 401, got %d", rw.Code)
	}
}

func TestEnvelopeMiddleware_RejectsReplay(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	bodyBytes := []byte(`{"jsonrpc":"2.0"}`)
	ts := time.Now().Unix()
	sigB64 := signEnvelope(t, priv, "alice", "POST /mcp", bodyBytes, "nonce-replay", ts)

	cache := auth.NewMemoryNonceCache(0, 0)
	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(200)
	})
	mw := mcp.EnvelopeMiddleware(svc, cache, mcp.EnvelopeRequired, 0)
	h := mw(inner)

	// First request passes.
	rw1 := httptest.NewRecorder()
	h.ServeHTTP(rw1, newEnvelopeRequest(t, http.MethodPost, "/mcp", bodyBytes, "alice", "nonce-replay", ts, sigB64))
	if rw1.Code != 200 {
		t.Fatalf("first valid request must pass: %d %s", rw1.Code, rw1.Body.String())
	}

	// Replaying the exact same signed request must fail with 401 (nonce seen).
	rw2 := httptest.NewRecorder()
	h.ServeHTTP(rw2, newEnvelopeRequest(t, http.MethodPost, "/mcp", bodyBytes, "alice", "nonce-replay", ts, sigB64))
	if rw2.Code != http.StatusUnauthorized {
		t.Fatalf("replay must 401, got %d", rw2.Code)
	}
	if callCount != 1 {
		t.Fatalf("inner should only run once, got %d", callCount)
	}
}

func TestEnvelopeMiddleware_RejectsStaleTimestamp(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	bodyBytes := []byte(`{"jsonrpc":"2.0"}`)
	staleTs := time.Now().Unix() - int64(auth.ReplayWindow.Seconds()) - 10
	sigB64 := signEnvelope(t, priv, "alice", "POST /mcp", bodyBytes, "n-stale", staleTs)

	mw := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeRequired, 0)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not reach inner on stale timestamp")
	}))

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, newEnvelopeRequest(t, http.MethodPost, "/mcp", bodyBytes, "alice", "n-stale", staleTs, sigB64))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("stale timestamp must 401, got %d", rw.Code)
	}
}

func TestEnvelopeMiddleware_RejectsMissingKey(t *testing.T) {
	svc, _ := newTestService(t)
	_, priv := newKeypair(t)
	bodyBytes := []byte(`{}`)
	ts := time.Now().Unix()
	// No RegisterAgentKey call — signature is validly formed but no registered key.
	sigB64 := signEnvelope(t, priv, "ghost", "POST /mcp", bodyBytes, "n", ts)

	mw := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeRequired, 0)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not reach inner with no registered key")
	}))

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, newEnvelopeRequest(t, http.MethodPost, "/mcp", bodyBytes, "ghost", "n", ts, sigB64))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("unregistered key must 401, got %d", rw.Code)
	}
}

func TestEnvelopeMiddleware_AdvisoryValidPasses(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	bodyBytes := []byte(`{"x":1}`)
	ts := time.Now().Unix()
	sigB64 := signEnvelope(t, priv, "alice", "POST /mcp", bodyBytes, "n-adv", ts)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) })
	h := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeAdvisory, 0)(inner)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, newEnvelopeRequest(t, http.MethodPost, "/mcp", bodyBytes, "alice", "n-adv", ts, sigB64))

	if !called || rw.Code != 200 {
		t.Fatalf("advisory+valid must pass through, code=%d", rw.Code)
	}
}

func TestEnvelopeMiddleware_AdvisoryInvalidRejects(t *testing.T) {
	// Documents the design choice: advisory mode ONLY relaxes the unsigned
	// case. A present-but-invalid signature is always rejected, so client
	// signing bugs surface during rollout rather than silently passing.
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	bodyBytes := []byte(`{"x":1}`)
	ts := time.Now().Unix()
	// Sign over one body, submit another → invalid sig.
	sigB64 := signEnvelope(t, priv, "alice", "POST /mcp", bodyBytes, "n-adv-bad", ts)

	h := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeAdvisory, 0)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not reach inner") }),
	)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, newEnvelopeRequest(t, http.MethodPost, "/mcp", []byte(`{"x":2}`), "alice", "n-adv-bad", ts, sigB64))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("advisory+invalid must 401, got %d", rw.Code)
	}
}

func TestEnvelopeMiddleware_NonPOSTBypassesEvenInRequired(t *testing.T) {
	// SSE establishment uses GET; forcing envelope verification on GETs would
	// break SSE clients that don't (and can't usefully) sign an empty body.
	svc, _ := newTestService(t)
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) })
	h := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeRequired, 0)(inner)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	h.ServeHTTP(rw, r)

	if !called || rw.Code != 200 {
		t.Fatalf("non-POST must pass through even in required mode, code=%d", rw.Code)
	}
}

func TestEnvelopeMiddleware_BodyTooLargeRejected(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	// 128-byte limit — any body over 128 bytes should be rejected.
	bodyBytes := bytes.Repeat([]byte("A"), 256)
	ts := time.Now().Unix()
	sigB64 := signEnvelope(t, priv, "alice", "POST /mcp", bodyBytes, "n-big", ts)

	h := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeRequired, 128)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("oversized body must not reach inner") }),
	)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, newEnvelopeRequest(t, http.MethodPost, "/mcp", bodyBytes, "alice", "n-big", ts, sigB64))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("body > maxBody must 401, got %d", rw.Code)
	}
}

// TestEnvelopeMiddleware_HostedModeScopedKey mirrors the B1.5 fix for
// per-action signatures: in hosted mode, keys are stored under a scoped agent
// ID ("<keyID>:alice") but the client signs with the unscoped form ("alice").
// The middleware must scope for lookup while canonicalizing with the unscoped
// form. Here we simulate the hosted context by writing ContextKeyKeyID into
// the request context manually (production plumbing is via paymentMiddleware).
func TestEnvelopeMiddleware_HostedModeScopedKey(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, priv := newKeypair(t)
	// Simulate what hosted-mode MCP does: register under the scoped name.
	if err := svc.RegisterAgentKey(ctx, "k_test:alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	bodyBytes := []byte(`{"x":1}`)
	ts := time.Now().Unix()
	// Client signs with its unscoped view.
	sigB64 := signEnvelope(t, priv, "alice", "POST /mcp", bodyBytes, "n-hosted", ts)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) })
	h := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeRequired, 0)(inner)

	req := newEnvelopeRequest(t, http.MethodPost, "/mcp", bodyBytes, "alice", "n-hosted", ts, sigB64)
	// Inject the keyID the way paymentMiddleware would.
	reqCtx := context.WithValue(req.Context(), payments.ContextKeyKeyID{}, "k_test")
	req = req.WithContext(reqCtx)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if !called || rw.Code != 200 {
		t.Fatalf("hosted-mode envelope must verify, code=%d body=%q", rw.Code, rw.Body.String())
	}
}

// TestEnvelopeMiddleware_HostedModeMissingContextRejects confirms the
// failure mode when envelope headers exist but the stored key is scoped and
// the request carries no keyID in ctx: the middleware falls back to unscoped
// lookup and correctly reports "no active key registered for alice".
func TestEnvelopeMiddleware_HostedModeMissingContextRejects(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "k_test:alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	bodyBytes := []byte(`{}`)
	ts := time.Now().Unix()
	sigB64 := signEnvelope(t, priv, "alice", "POST /mcp", bodyBytes, "n-miss-ctx", ts)

	h := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeRequired, 0)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not reach inner") }),
	)

	rw := httptest.NewRecorder()
	// No keyID in ctx — lookup happens under "alice" (unscoped), misses the
	// "k_test:alice" record.
	h.ServeHTTP(rw, newEnvelopeRequest(t, http.MethodPost, "/mcp", bodyBytes, "alice", "n-miss-ctx", ts, sigB64))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 when ctx lacks keyID for scoped key, got %d", rw.Code)
	}
}

func TestEnvelopeMiddleware_PartialHeadersRejected(t *testing.T) {
	svc, _ := newTestService(t)
	mw := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeAdvisory, 0)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not reach inner with partial headers")
	}))

	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte("{}")))
	// Only agent_id set — missing nonce/ts/sig.
	r.Header.Set(mcp.HeaderEnvelopeAgentID, "alice")
	h.ServeHTTP(rw, r)

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("partial headers must 401, got %d", rw.Code)
	}
}
