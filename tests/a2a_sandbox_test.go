package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/mcp"
	"github.com/justinstimatze/gemot/internal/payments"
)

// TestA2ASandboxBearerlessJoin exercises the sandbox-auth path added
// to A2AAuthMiddleware: an A2A request without a Bearer header that
// carries a valid join code in params.code is accepted for
// sandbox-allowed methods. Matches the /try invite-block promise that
// recipients without an API key can still use /a2a.
func TestA2ASandboxBearerlessJoin(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// Create a deliberation + join code that the test simulates a
	// sandbox visitor hitting.
	d, err := svc.CreateDeliberation(ctx, "Sandbox A2A test", "",
		deliberation.WithVisibility("link"),
		deliberation.WithMaxParticipants(10),
	)
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}
	jc, err := svc.GenerateJoinCode(ctx, d.ID, "participant", 48*time.Hour, 10)
	if err != nil {
		t.Fatalf("GenerateJoinCode: %v", err)
	}

	// Build the A2A chain with sandbox auth enabled. apiSecret set to
	// a real value so the default Bearer path would reject bearer-less
	// requests; sandbox path must kick in.
	rateLim := payments.NewRateLimiter(ctx, 100, time.Minute)
	sandboxLim := payments.NewRateLimiter(ctx, 100, time.Minute)
	cache := auth.NewMemoryNonceCache(0, 0)
	handler := mcp.A2AHandler(svc, nil, nil, nil)
	authMW := mcp.A2AAuthMiddleware("test-secret", nil, rateLim, svc, sandboxLim)
	envMW := mcp.EnvelopeMiddleware(svc, cache, mcp.EnvelopeOff, 0)
	chain := authMW(envMW(handler))

	// 1) coordinate/join without Bearer, with a valid code.
	joinBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  "gemot/coordinate",
		"params":  map[string]any{"action": "join", "code": jc.Code, "agent_id": "walker"},
		"id":      1,
	}
	joinJSON, _ := json.Marshal(joinBody)
	req := httptest.NewRequest("POST", "/a2a", bytes.NewReader(joinJSON))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("join: status=%d body=%s (expected 200, sandbox path should accept)", w.Code, w.Body.String())
	}
	var joinResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &joinResp)
	if e, ok := joinResp["error"].(map[string]any); ok {
		t.Fatalf("join returned JSON-RPC error: %v", e)
	}

	// 2) participate/submit_position without Bearer, with join_code.
	subBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  "gemot/participate",
		"params": map[string]any{
			"action":          "submit_position",
			"deliberation_id": d.ID,
			"agent_id":        "walker",
			"content":         "sandbox write via bearer-less A2A",
			"join_code":       jc.Code,
		},
		"id": 2,
	}
	subJSON, _ := json.Marshal(subBody)
	req = httptest.NewRequest("POST", "/a2a", bytes.NewReader(subJSON))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	chain.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("submit: status=%d body=%s", w.Code, w.Body.String())
	}
	var subResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &subResp)
	if e, ok := subResp["error"].(map[string]any); ok {
		t.Fatalf("submit returned JSON-RPC error: %v", e)
	}

	// 3) Without a join_code AND without a Bearer → must reject.
	noAuthBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  "gemot/participate",
		"params":  map[string]any{"action": "submit_position", "deliberation_id": d.ID, "agent_id": "walker", "content": "no auth"},
		"id":      3,
	}
	noAuthJSON, _ := json.Marshal(noAuthBody)
	req = httptest.NewRequest("POST", "/a2a", bytes.NewReader(noAuthJSON))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	chain.ServeHTTP(w, req)
	var noAuthResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &noAuthResp)
	errObj, ok := noAuthResp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected JSON-RPC error when no auth + no join_code; got %v", noAuthResp)
	}
	msg, _ := errObj["message"].(string)
	if msg == "" || (!contains(msg, "Bearer") && !contains(msg, "sandbox")) {
		t.Errorf("expected Bearer/sandbox error; got %q", msg)
	}

	// 4) Method outside the sandbox allowlist must still require Bearer.
	adminBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  "gemot/admin",
		"params":  map[string]any{"action": "get_audit_log", "deliberation_id": d.ID, "join_code": jc.Code},
		"id":      4,
	}
	adminJSON, _ := json.Marshal(adminBody)
	req = httptest.NewRequest("POST", "/a2a", bytes.NewReader(adminJSON))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	chain.ServeHTTP(w, req)
	var adminResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &adminResp)
	if _, ok := adminResp["error"].(map[string]any); !ok {
		t.Errorf("admin method without Bearer should reject even with join_code; got %v", adminResp)
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}
