package tests

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/mcp"
	"github.com/justinstimatze/gemot/internal/payments"
)

// buildA2AChain wires A2AAuthMiddleware(envelopeMW(inner)) so tests can pick
// an inner handler appropriate to what they're verifying. In dev mode
// (apiSecret=""), the auth middleware stamps ContextKeyIsAdmin on every
// request, bypassing token checks so envelope behavior is testable in
// isolation from credit validation.
func buildA2AChain(t *testing.T, svc *deliberation.Service, mode mcp.EnvelopeMode, cache auth.NonceCache, inner http.Handler) http.Handler {
	t.Helper()
	if cache == nil {
		cache = auth.NewMemoryNonceCache(0, 0)
	}
	authMW := mcp.A2AAuthMiddleware("", nil, nil, nil, nil, false)
	envMW := mcp.EnvelopeMiddleware(svc, cache, mode, 0)
	return authMW(envMW(inner))
}

func TestA2AEnvelope_OffPassesThrough(t *testing.T) {
	svc, _ := newTestService(t)
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	h := buildA2AChain(t, svc, mcp.EnvelopeOff, nil, inner)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/a2a", bytes.NewReader([]byte("{}")))
	h.ServeHTTP(rw, r)

	if !called || rw.Code != 200 {
		t.Fatalf("off-mode a2a must pass through: code=%d called=%v", rw.Code, called)
	}
}

func TestA2AEnvelope_RequiredRejectsUnsigned(t *testing.T) {
	svc, _ := newTestService(t)
	h := buildA2AChain(t, svc, mcp.EnvelopeRequired, nil,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("inner must not run") }),
	)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodPost, "/a2a", bytes.NewReader([]byte("{}"))))
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("required+unsigned must 401, got %d", rw.Code)
	}
}

func TestA2AEnvelope_AcceptsValidSignature(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	body := []byte(`{"jsonrpc":"2.0","method":"agent/info","id":1}`)
	ts := time.Now().Unix()
	sigB64 := signEnvelope(t, priv, "alice", "POST /a2a", body, "nonce-a2a-1", ts)

	called := false
	var gotBody []byte
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		w.WriteHeader(200)
	})
	h := buildA2AChain(t, svc, mcp.EnvelopeRequired, nil, inner)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, newEnvelopeRequest(t, http.MethodPost, "/a2a", body, "alice", "nonce-a2a-1", ts, sigB64))

	if !called || rw.Code != 200 {
		t.Fatalf("valid envelope on /a2a should pass: code=%d body=%q", rw.Code, rw.Body.String())
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("inner handler saw %q, want original body %q", gotBody, body)
	}
}

func TestA2AEnvelope_RejectsBadSignature(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	signedBody := []byte(`{"jsonrpc":"2.0","method":"agent/info","id":1}`)
	ts := time.Now().Unix()
	sigB64 := signEnvelope(t, priv, "alice", "POST /a2a", signedBody, "n-bad", ts)

	h := buildA2AChain(t, svc, mcp.EnvelopeRequired, nil,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not reach inner") }),
	)

	tampered := []byte(`{"jsonrpc":"2.0","method":"agent/info","id":2}`)
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, newEnvelopeRequest(t, http.MethodPost, "/a2a", tampered, "alice", "n-bad", ts, sigB64))
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("tampered body on /a2a must 401, got %d", rw.Code)
	}
}

func TestA2AEnvelope_RejectsReplay(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	body := []byte(`{"jsonrpc":"2.0","method":"agent/info","id":1}`)
	ts := time.Now().Unix()
	sigB64 := signEnvelope(t, priv, "alice", "POST /a2a", body, "nonce-a2a-replay", ts)

	cache := auth.NewMemoryNonceCache(0, 0)
	hits := 0
	h := buildA2AChain(t, svc, mcp.EnvelopeRequired, cache,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.WriteHeader(200)
		}),
	)

	rw1 := httptest.NewRecorder()
	h.ServeHTTP(rw1, newEnvelopeRequest(t, http.MethodPost, "/a2a", body, "alice", "nonce-a2a-replay", ts, sigB64))
	if rw1.Code != 200 {
		t.Fatalf("first a2a request must pass: %d", rw1.Code)
	}

	rw2 := httptest.NewRecorder()
	h.ServeHTTP(rw2, newEnvelopeRequest(t, http.MethodPost, "/a2a", body, "alice", "nonce-a2a-replay", ts, sigB64))
	if rw2.Code != http.StatusUnauthorized {
		t.Fatalf("a2a replay must 401, got %d", rw2.Code)
	}
	if hits != 1 {
		t.Fatalf("inner should run once, ran %d times", hits)
	}
}

func TestA2AEnvelope_AdvisoryUnsignedPasses(t *testing.T) {
	svc, _ := newTestService(t)
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	h := buildA2AChain(t, svc, mcp.EnvelopeAdvisory, nil, inner)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodPost, "/a2a", bytes.NewReader([]byte("{}"))))
	if !called || rw.Code != 200 {
		t.Fatalf("advisory unsigned must pass: code=%d", rw.Code)
	}
}

func TestA2AEnvelope_AdvisoryInvalidRejects(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	signed := []byte(`{"v":1}`)
	ts := time.Now().Unix()
	sigB64 := signEnvelope(t, priv, "alice", "POST /a2a", signed, "n-adv-bad", ts)

	h := buildA2AChain(t, svc, mcp.EnvelopeAdvisory, nil,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not reach inner") }),
	)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, newEnvelopeRequest(t, http.MethodPost, "/a2a", []byte(`{"v":2}`), "alice", "n-adv-bad", ts, sigB64))
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("advisory+invalid on /a2a must 401, got %d", rw.Code)
	}
}

// TestA2AEnvelope_AuthRejectsMissingBearer verifies the auth middleware
// emits a JSON-RPC error (200 with error object), not plain 401 text, so A2A
// clients get a parseable response on the same codepath they already handle.
func TestA2AEnvelope_AuthRejectsMissingBearer(t *testing.T) {
	svc, _ := newTestService(t)
	// apiSecret set → auth required (not dev mode).
	authMW := mcp.A2AAuthMiddleware("secret", nil, nil, nil, nil, false)
	envMW := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeOff, 0)
	h := authMW(envMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run without auth")
	})))

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodPost, "/a2a", bytes.NewReader([]byte("{}"))))

	if rw.Code != http.StatusOK {
		t.Fatalf("auth middleware must respond with JSON-RPC 200 envelope, got %d", rw.Code)
	}
	var parsed struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("response not JSON-RPC: %v (body=%q)", err, rw.Body.String())
	}
	if parsed.Error == nil || parsed.Error.Code != -32000 {
		t.Fatalf("want JSON-RPC error code -32000, got %+v", parsed.Error)
	}
	if !strings.Contains(parsed.Error.Message, "Bearer") {
		t.Fatalf("missing-bearer message should mention Bearer, got %q", parsed.Error.Message)
	}
}

// TestA2AEnvelope_AuthRateLimitRejects verifies that when a customer gmt_
// key exceeds the per-minute budget, the middleware returns a JSON-RPC
// rate-limit error without ever invoking the inner handler. Admin callers
// bypass the limiter, so this matters only for real customer paths.
func TestA2AEnvelope_AuthRateLimitRejects(t *testing.T) {
	_, db := newTestService(t)
	credits, err := payments.NewCreditStore(db.RawDB())
	if err != nil {
		t.Fatalf("credit store: %v", err)
	}
	token, err := credits.GenerateKey("rl@example.com", "cus_rl", "cs_rl", 1000)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Budget of 1 request per minute — second request must hit the limiter.
	limiter := payments.NewRateLimiter(context.Background(), 1, time.Minute)
	authMW := mcp.A2AAuthMiddleware("admin-secret", credits, limiter, nil, nil, false)
	hits := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	})
	h := authMW(inner)

	makeReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/a2a", bytes.NewReader([]byte("{}")))
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}

	rw1 := httptest.NewRecorder()
	h.ServeHTTP(rw1, makeReq())
	if rw1.Code != http.StatusOK || hits != 1 {
		t.Fatalf("first request must pass: code=%d hits=%d", rw1.Code, hits)
	}

	rw2 := httptest.NewRecorder()
	h.ServeHTTP(rw2, makeReq())
	if hits != 1 {
		t.Fatalf("rate-limited second request must not reach inner handler, hits=%d", hits)
	}
	var parsed struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rw2.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("response not JSON-RPC: %v body=%q", err, rw2.Body.String())
	}
	if parsed.Error == nil || !strings.Contains(parsed.Error.Message, "rate limit") {
		t.Fatalf("want rate-limit JSON-RPC error, got %+v", parsed.Error)
	}
}

func TestA2AEnvelope_AuthRejectsBadToken(t *testing.T) {
	svc, _ := newTestService(t)
	authMW := mcp.A2AAuthMiddleware("secret", nil, nil, nil, nil, false)
	envMW := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeOff, 0)
	h := authMW(envMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run with bad token")
	})))

	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/a2a", bytes.NewReader([]byte("{}")))
	r.Header.Set("Authorization", "Bearer wrongsecret")
	h.ServeHTTP(rw, r)

	if rw.Code != http.StatusOK {
		t.Fatalf("bad-token rejection must still use JSON-RPC envelope, got %d", rw.Code)
	}
}

// TestA2AEnvelope_HostedModeScopedKey verifies the full middleware ordering:
// the auth middleware must populate ContextKeyKeyID before envelope runs so
// scopeAgentID(ctx) rewrites the unscoped signed agent_id to the scoped stored
// form during key lookup. This is the main point of the A2A envelope refactor.
func TestA2AEnvelope_HostedModeScopedKey(t *testing.T) {
	svc, db := newTestService(t)
	ctx := context.Background()

	credits, err := payments.NewCreditStore(db.RawDB())
	if err != nil {
		t.Fatalf("credit store: %v", err)
	}
	token, err := credits.GenerateKey("hosted@example.com", "cus_h", "cs_h", 1000)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	scopedKeyID := payments.KeyID(token)

	pub, priv := newKeypair(t)
	// Register the agent key under the scoped form, which is what hosted-mode
	// A2A persists (MCP does the same in server.go).
	if err := svc.RegisterAgentKey(ctx, scopedKeyID+":alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register scoped: %v", err)
	}

	body := []byte(`{"jsonrpc":"2.0","method":"agent/info","id":1}`)
	ts := time.Now().Unix()
	// Client signs with its unscoped view.
	sigB64 := signEnvelope(t, priv, "alice", "POST /a2a", body, "n-hosted", ts)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Confirm the downstream handler observes the hosted key via ctx.
		if got, _ := r.Context().Value(payments.ContextKeyKeyID{}).(string); got != scopedKeyID {
			t.Errorf("handler ctx keyID=%q want %q", got, scopedKeyID)
		}
		called = true
		w.WriteHeader(200)
	})
	authMW := mcp.A2AAuthMiddleware("admin-secret", credits, payments.NewRateLimiter(ctx, 100, time.Minute), nil, nil, false)
	envMW := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeRequired, 0)
	h := authMW(envMW(inner))

	req := newEnvelopeRequest(t, http.MethodPost, "/a2a", body, "alice", "n-hosted", ts, sigB64)
	req.Header.Set("Authorization", "Bearer "+token)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if !called || rw.Code != 200 {
		t.Fatalf("hosted-mode a2a envelope must verify: code=%d body=%q", rw.Code, rw.Body.String())
	}
}

// TestA2AEnvelope_SignatureParamRoundTrip exercises the per-action signature
// path through the real A2A handler: the client posts a `submit_position` with
// a `signature` field, and SubmitPositionWithSigningID verifies it against the
// unscoped agent_id.
func TestA2AEnvelope_SignatureParamRoundTrip(t *testing.T) {
	svc, db := newTestService(t)
	ctx := context.Background()

	d, err := svc.CreateDeliberation(ctx, "Signed", "", deliberation.WithSignaturePolicy("advisory"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	content := "my signed a2a position"
	sig := ed25519.Sign(priv, auth.PositionPayload("alice", d.ID, d.Round, content))
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	// Build the full A2A stack in dev mode so no bearer is required.
	authMW := mcp.A2AAuthMiddleware("", nil, nil, nil, nil, false)
	envMW := mcp.EnvelopeMiddleware(svc, auth.NewMemoryNonceCache(0, 0), mcp.EnvelopeOff, 0)
	handler := mcp.A2AHandler(svc, nil, db, db, nil)
	chain := authMW(envMW(handler))

	rpc := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "gemot/participate",
		"params": map[string]any{
			"action":          "submit_position",
			"deliberation_id": d.ID,
			"agent_id":        "alice",
			"content":         content,
			"signature":       sigB64,
		},
	}
	reqBody, _ := json.Marshal(rpc)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/a2a", bytes.NewReader(reqBody))
	chain.ServeHTTP(rw, r)

	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rw.Code, rw.Body.String())
	}

	var resp struct {
		Result *deliberation.Position `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%q", err, rw.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}
	if resp.Result == nil || len(resp.Result.Signature) == 0 {
		t.Fatalf("signature not persisted: %+v", resp.Result)
	}
}

// TestA2AEnvelope_PartialHeadersRejected confirms the always-fail-closed rule:
// a request with some envelope headers but not all is rejected even in
// advisory mode, since it can only be a client bug or tamper.
func TestA2AEnvelope_PartialHeadersRejected(t *testing.T) {
	svc, _ := newTestService(t)
	h := buildA2AChain(t, svc, mcp.EnvelopeAdvisory, nil,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not reach inner") }),
	)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/a2a", bytes.NewReader([]byte("{}")))
	r.Header.Set(mcp.HeaderEnvelopeAgentID, "alice")
	// Only agent_id set.
	h.ServeHTTP(rw, r)

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("partial headers on /a2a must 401, got %d", rw.Code)
	}
}

func TestA2AEnvelope_StaleTimestampRejected(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	body := []byte(`{}`)
	staleTs := time.Now().Unix() - int64(auth.ReplayWindow.Seconds()) - 60
	sigB64 := signEnvelope(t, priv, "alice", "POST /a2a", body, "n-stale-a2a", staleTs)

	h := buildA2AChain(t, svc, mcp.EnvelopeRequired, nil,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not reach inner") }),
	)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, newEnvelopeRequest(t, http.MethodPost, "/a2a", body, "alice", "n-stale-a2a", staleTs, sigB64))
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("stale timestamp on /a2a must 401, got %d", rw.Code)
	}
}
