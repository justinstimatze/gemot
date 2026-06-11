package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/mcp"
	"github.com/justinstimatze/gemot/internal/payments"
)

// TestA2A_AnalyzeGetResult_PendingShape verifies that analyze:get_result
// over A2A returns a structured pending/not_started object (with the
// pipeline's analysis_status) when no analysis result is available yet,
// instead of a bare null or empty array. A single poll loop on
// get_result can now replace the old "call deliberation:get to check
// status, then call analyze:get_result for the result" two-tool dance.
func TestA2A_AnalyzeGetResult_PendingShape(t *testing.T) {
	svc, db := newTestService(t)
	ctx := context.Background()

	credits, err := payments.NewCreditStore(db.RawDB())
	if err != nil {
		t.Fatalf("credit store: %v", err)
	}
	token, err := credits.GenerateKey("getres@example.com", "cus_gr", "cs_gr", 1000)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	d, err := svc.CreateDeliberation(ctx, "Get-result status test", "")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}

	rateLim := payments.NewRateLimiter(ctx, 100, time.Minute)
	handler := mcp.A2AHandler(svc, credits, nil, db)
	authMW := mcp.A2AAuthMiddleware("test-secret", credits, rateLim, nil, nil)
	chain := authMW(handler)

	call := func(t *testing.T, id any) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"method":  "gemot/analyze",
			"params":  map[string]any{"action": "get_result", "deliberation_id": d.ID},
			"id":      id,
		})
		req := httptest.NewRequest("POST", "/a2a", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		chain.ServeHTTP(w, req)
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v body=%q", err, w.Body.String())
		}
		if e, ok := resp["error"]; ok && e != nil {
			t.Fatalf("unexpected JSON-RPC error: %v", e)
		}
		result, ok := resp["result"].(map[string]any)
		if !ok {
			t.Fatalf("result missing or wrong type: %v", resp["result"])
		}
		return result
	}

	// Case 1: never-analyzed open deliberation → status=not_started.
	result := call(t, 1)
	if got := result["status"]; got != "not_started" {
		t.Fatalf("never-analyzed: want status=not_started, got %v", got)
	}
	if got := result["deliberation_status"]; got != "open" {
		t.Fatalf("never-analyzed: want deliberation_status=open, got %v", got)
	}
	if _, present := result["analysis_status"]; present {
		t.Fatalf("never-analyzed: analysis_status must be omitted, got %v", result["analysis_status"])
	}

	// Force the deliberation into "analyzing" with a concrete sub_status
	// so the get_result response surfaces it directly.
	if err := db.UpdateDeliberationStatus(ctx, d.ID, "analyzing"); err != nil {
		t.Fatalf("UpdateDeliberationStatus: %v", err)
	}
	if err := db.UpdateSubStatus(ctx, d.ID, "crux_detection"); err != nil {
		t.Fatalf("UpdateSubStatus: %v", err)
	}

	// Case 2: in-progress analyze → status=pending + analysis_status carries
	// the pipeline stage.
	result = call(t, 2)
	if got := result["status"]; got != "pending" {
		t.Fatalf("in-progress: want status=pending, got %v", got)
	}
	if got := result["deliberation_status"]; got != "analyzing" {
		t.Fatalf("in-progress: want deliberation_status=analyzing, got %v", got)
	}
	if got := result["analysis_status"]; got != "crux_detection" {
		t.Fatalf("in-progress: want analysis_status=crux_detection, got %v", got)
	}
}
