package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/mcp"
	"github.com/justinstimatze/gemot/internal/payments"
)

// TestA2A_AnalyzeRunRequiresQuorumBeforeCharge locks in the
// payment-fidelity guard that A2A's analyze:run must check quorum
// BEFORE deducting credits. Before this fix the A2A path only ran
// CheckAccess; a customer key would get charged for an analyze whose
// async pipeline would later reject on quorum. Mirrors the MCP path's
// server.go "never consume a credential for a service we can't render"
// rule. If this test regresses, the credit-balance assertion catches
// the drift before any customer sees a phantom charge.
func TestA2A_AnalyzeRunRequiresQuorumBeforeCharge(t *testing.T) {
	svc, db := newTestService(t)
	ctx := context.Background()

	credits, err := payments.NewCreditStore(db.RawDB())
	if err != nil {
		t.Fatalf("credit store: %v", err)
	}
	const startBalance = 1000
	token, err := credits.GenerateKey("quorum@example.com", "cus_q", "cs_q", startBalance)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	d, err := svc.CreateDeliberation(ctx, "Quorum A2A test", "",
		deliberation.WithRules(map[string]any{"min_participants": 3}),
	)
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}
	// Sub-quorum: only 2 positions, need 3.
	if _, err := svc.SubmitPosition(ctx, d.ID, "agent1", "Position A"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitPosition(ctx, d.ID, "agent2", "Position B"); err != nil {
		t.Fatal(err)
	}

	rateLim := payments.NewRateLimiter(ctx, 100, time.Minute)
	handler := mcp.A2AHandler(svc, credits, nil, db, nil)
	authMW := mcp.A2AAuthMiddleware("test-secret", credits, rateLim, nil, nil, false)
	chain := authMW(handler)

	body := map[string]any{
		"jsonrpc": "2.0",
		"method":  "gemot/analyze",
		"params": map[string]any{
			"action":          "run",
			"deliberation_id": d.ID,
			"model":           "claude-sonnet-4-6",
		},
		"id": 1,
	}
	bodyJSON, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/a2a", bytes.NewReader(bodyJSON))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, req)

	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%q", err, w.Body.String())
	}
	if resp.Error == nil {
		t.Fatalf("expected JSON-RPC error, got body=%q", w.Body.String())
	}
	if !strings.Contains(resp.Error.Message, "quorum") {
		t.Fatalf("expected quorum error, got: %q", resp.Error.Message)
	}

	// Payment-fidelity assertion: a quorum failure must not consume
	// credits. If this regresses, paying customers get phantom charges.
	balance, err := credits.GetBalance(token)
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance != startBalance {
		t.Fatalf("balance must be unchanged on quorum failure: want %d, got %d", startBalance, balance)
	}
}

// TestA2A_AnalyzeFollowUpPreconditionGate locks in the precondition gap
// closure on A2A's analyze:follow_up. Before the refactor follow_up
// jumped straight to deductCredits — a customer key could trigger a paid
// follow_up against any (potentially nonexistent) deliberation_id.
// Mirrors the analyze:run quorum test but on the existence check: a
// follow_up against a bogus deliberation_id must error AND leave the
// balance untouched.
func TestA2A_AnalyzeFollowUpPreconditionGate(t *testing.T) {
	svc, db := newTestService(t)
	ctx := context.Background()

	credits, err := payments.NewCreditStore(db.RawDB())
	if err != nil {
		t.Fatalf("credit store: %v", err)
	}
	const startBalance = 1000
	token, err := credits.GenerateKey("fu@example.com", "cus_fu", "cs_fu", startBalance)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	rateLim := payments.NewRateLimiter(ctx, 100, time.Minute)
	handler := mcp.A2AHandler(svc, credits, nil, db, nil)
	authMW := mcp.A2AAuthMiddleware("test-secret", credits, rateLim, nil, nil, false)
	chain := authMW(handler)

	body := map[string]any{
		"jsonrpc": "2.0",
		"method":  "gemot/analyze",
		"params": map[string]any{
			"action":          "follow_up",
			"deliberation_id": "delib_does_not_exist",
			"model":           "claude-sonnet-4-6",
		},
		"id": 1,
	}
	bodyJSON, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/a2a", bytes.NewReader(bodyJSON))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, req)

	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%q", err, w.Body.String())
	}
	if resp.Error == nil {
		t.Fatalf("expected JSON-RPC error for missing deliberation, got body=%q", w.Body.String())
	}
	if !strings.Contains(resp.Error.Message, "not found") {
		t.Fatalf("expected 'not found' error, got: %q", resp.Error.Message)
	}

	balance, err := credits.GetBalance(token)
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance != startBalance {
		t.Fatalf("balance must be unchanged on missing-delib follow_up: want %d, got %d", startBalance, balance)
	}
}
