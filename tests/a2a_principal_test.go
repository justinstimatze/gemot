package tests

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/mcp"
	"github.com/justinstimatze/gemot/internal/payments"
	"github.com/justinstimatze/gemot/internal/principal"
	"github.com/justinstimatze/gemot/internal/store"
)

// A2A must reach the same delegation guarantees as MCP. Enforcement itself is
// transport-independent — both paths funnel through SubmitPositionWithSigningID
// — so what these tests actually pin down is the *surface*: that an A2A-only
// caller can both create a deliberation demanding credentials and present one.

// a2aChain builds the authenticated A2A handler and a bearer token for it.
func a2aChain(t *testing.T, db *store.DB, svc *deliberation.Service) (http.Handler, string) {
	t.Helper()
	credits, err := payments.NewCreditStore(db.RawDB())
	if err != nil {
		t.Fatalf("credit store: %v", err)
	}
	token, err := credits.GenerateKey("principal@example.com", "cus_p", "cs_p", 1000)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	rateLim := payments.NewRateLimiter(context.Background(), 100, time.Minute)
	handler := mcp.A2AHandler(svc, credits, nil, db)
	return mcp.A2AAuthMiddleware("test-secret", credits, rateLim, nil, nil, false)(handler), token
}

// a2aCall issues one JSON-RPC request and returns the decoded envelope.
func a2aCall(t *testing.T, chain http.Handler, token, method string, params map[string]any) a2aResponse {
	t.Helper()
	bodyJSON, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest("POST", "/a2a", bytes.NewReader(bodyJSON))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, req)

	var resp a2aResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%q", err, w.Body.String())
	}
	return resp
}

type a2aResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// credentialParams renders a credential in the A2A wire shape.
func credentialParams(t *testing.T, priv ed25519.PrivateKey, c principal.Credential) map[string]any {
	t.Helper()
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = time.Now().Add(time.Hour)
	}
	c.Signature = ed25519.Sign(priv, c.SigningPayload())
	return map[string]any{
		"principal":  c.Principal,
		"agent":      c.Agent,
		"scope":      c.Scope,
		"issuer":     c.Issuer,
		"expires_at": c.ExpiresAt.Format(time.RFC3339),
		"signature":  base64.StdEncoding.EncodeToString(c.Signature),
	}
}

// An A2A caller must be able to create a deliberation that demands backed
// claims. Without principal_policy on the A2A create surface, an A2A-only
// deployment could present credentials but never require them.
func TestA2A_CreateHonorsPrincipalPolicy(t *testing.T) {
	svc, db := newTestService(t)
	chain, token := a2aChain(t, db, svc)

	resp := a2aCall(t, chain, token, "gemot/deliberation", map[string]any{
		"action":           "create",
		"topic":            "A2A principal policy",
		"principal_policy": "required",
	})
	if resp.Error != nil {
		t.Fatalf("create: %v", resp.Error.Message)
	}
	var created struct {
		ID              string `json:"deliberation_id"`
		PrincipalPolicy string `json:"principal_policy"`
	}
	if err := json.Unmarshal(resp.Result, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.PrincipalPolicy != "required" {
		t.Fatalf("principal_policy = %q, want \"required\"", created.PrincipalPolicy)
	}

	// The policy must actually bite on the A2A submit path.
	unbacked := a2aCall(t, chain, token, "gemot/participate", map[string]any{
		"action":          "submit_position",
		"deliberation_id": created.ID,
		"agent_id":        delegAgent,
		"content":         "unbacked claim",
		"on_behalf_of":    delegPrincipal,
	})
	if unbacked.Error == nil {
		t.Fatal("unbacked on_behalf_of accepted over A2A under policy=required")
	}
	if !strings.Contains(unbacked.Error.Message, "principal credential required") {
		t.Errorf("error = %q, want it to mention the missing credential", unbacked.Error.Message)
	}
}

func TestA2A_SubmitAcceptsVerifiedCredential(t *testing.T) {
	svc, db := newTestService(t)
	ctx := context.Background()
	priv := registerPrincipal(t, svc, delegPrincipal)
	chain, token := a2aChain(t, db, svc)

	d, err := svc.CreateDeliberation(ctx, "A2A credential", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	resp := a2aCall(t, chain, token, "gemot/participate", map[string]any{
		"action":               "submit_position",
		"deliberation_id":      d.ID,
		"agent_id":             delegAgent,
		"content":              "backed claim",
		"on_behalf_of":         delegPrincipal,
		"principal_credential": credentialParams(t, priv, principal.Credential{Principal: delegPrincipal, Agent: delegAgent}),
	})
	if resp.Error != nil {
		t.Fatalf("submit with valid credential: %v", resp.Error.Message)
	}
	var p struct {
		PrincipalVerified bool   `json:"principal_verified"`
		OnBehalfOf        string `json:"on_behalf_of"`
	}
	if err := json.Unmarshal(resp.Result, &p); err != nil {
		t.Fatalf("decode position: %v", err)
	}
	if !p.PrincipalVerified {
		t.Error("principal_verified = false over A2A, want true")
	}
	if p.OnBehalfOf != delegPrincipal {
		t.Errorf("on_behalf_of = %q, want %q", p.OnBehalfOf, delegPrincipal)
	}
}

// A forged credential must be rejected on the A2A path too, and under the
// default policy — policy governs whether proof is required, never whether bad
// proof passes.
func TestA2A_SubmitRejectsForgedCredential(t *testing.T) {
	svc, db := newTestService(t)
	ctx := context.Background()
	registerPrincipal(t, svc, delegPrincipal)
	_, attackerPriv := newKeypair(t)
	chain, token := a2aChain(t, db, svc)

	d, err := svc.CreateDeliberation(ctx, "A2A forged credential", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	resp := a2aCall(t, chain, token, "gemot/participate", map[string]any{
		"action":               "submit_position",
		"deliberation_id":      d.ID,
		"agent_id":             delegAgent,
		"content":              "forged",
		"on_behalf_of":         delegPrincipal,
		"principal_credential": credentialParams(t, attackerPriv, principal.Credential{Principal: delegPrincipal, Agent: delegAgent}),
	})
	if resp.Error == nil {
		t.Fatal("forged credential accepted over A2A, want rejection")
	}
	if !strings.Contains(resp.Error.Message, "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", resp.Error.Message)
	}
}

// A malformed credential must produce a clean JSON-RPC invalid-params error
// rather than a panic or an opaque internal failure.
func TestA2A_SubmitRejectsMalformedCredential(t *testing.T) {
	svc, db := newTestService(t)
	ctx := context.Background()
	chain, token := a2aChain(t, db, svc)

	d, err := svc.CreateDeliberation(ctx, "A2A malformed credential", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	resp := a2aCall(t, chain, token, "gemot/participate", map[string]any{
		"action":          "submit_position",
		"deliberation_id": d.ID,
		"agent_id":        delegAgent,
		"content":         "malformed",
		"principal_credential": map[string]any{
			"principal":  delegPrincipal,
			"agent":      delegAgent,
			"expires_at": "not-a-timestamp",
			"signature":  "!!!not-base64!!!",
		},
	})
	if resp.Error == nil {
		t.Fatal("malformed credential accepted over A2A, want rejection")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602 (invalid params)", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "expires_at") {
		t.Errorf("error = %q, want it to name the bad field", resp.Error.Message)
	}
}
