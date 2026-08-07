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
	handler := mcp.A2AHandler(svc, credits, nil, db, nil)
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
		"agent_key":  base64.StdEncoding.EncodeToString(c.AgentKey),
		"scope":      c.Scope,
		"issuer":     c.Issuer,
		"expires_at": c.ExpiresAt.Format(time.RFC3339),
		"signature":  base64.StdEncoding.EncodeToString(c.Signature),
	}
}

// a2aRegisterKey registers an agent key *through the transport*, the way a
// real A2A client must.
//
// Earlier revisions of these helpers called svc.RegisterAgentKey directly. That
// bypass is what hid the fact that A2A had no register_key action at all: the
// tests passed while an A2A-only client had no way to satisfy
// proof-of-possession. Registration goes through the wire here so the setup a
// test performs is setup a client can actually perform.
func a2aRegisterKey(t *testing.T, chain http.Handler, token, agentID string, pub ed25519.PublicKey) {
	t.Helper()
	resp := a2aCall(t, chain, token, "gemot/participate", map[string]any{
		"action":     "register_key",
		"agent_id":   agentID,
		"public_key": base64.StdEncoding.EncodeToString(pub),
	})
	if resp.Error != nil {
		t.Fatalf("register_key over A2A: %v", resp.Error.Message)
	}
}

// a2aDelegation sets up a delegation entirely over the wire: the agent
// registers its own key, and the principal signs a credential bound to it.
// Returns the credential params and the agent's private key for signing.
func a2aDelegation(t *testing.T, svc *deliberation.Service, chain http.Handler, token, principalID, agentID string) (map[string]any, ed25519.PrivateKey) {
	t.Helper()
	// The principal is not an A2A caller — it is a human or org that signs
	// offline — so its key is registered directly.
	ppriv := registerPrincipal(t, svc, principalID)

	apub, apriv := newKeypair(t)
	a2aRegisterKey(t, chain, token, agentID, apub)

	cred := credentialParams(t, ppriv, principal.Credential{
		Principal: principalID, Agent: agentID, AgentKey: apub,
	})
	return cred, apriv
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
	chain, token := a2aChain(t, db, svc)
	cred, agentPriv := a2aDelegation(t, svc, chain, token, delegPrincipal, delegAgent)

	d, err := svc.CreateDeliberation(ctx, "A2A credential", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The client signs with its unscoped view of its own identity.
	const content = "backed claim"
	sig := base64.StdEncoding.EncodeToString(
		signPosition(t, agentPriv, delegAgent, d.ID, d.Round, content))

	resp := a2aCall(t, chain, token, "gemot/participate", map[string]any{
		"action":               "submit_position",
		"deliberation_id":      d.ID,
		"agent_id":             delegAgent,
		"content":              content,
		"on_behalf_of":         delegPrincipal,
		"principal_credential": cred,
		"signature":            sig,
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
	chain, token := a2aChain(t, db, svc)
	_, agentPriv := a2aDelegation(t, svc, chain, token, delegPrincipal, delegAgent)
	_, attackerPriv := newKeypair(t)
	agentPub := agentPriv.Public().(ed25519.PublicKey)

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
		"principal_credential": credentialParams(t, attackerPriv, principal.Credential{Principal: delegPrincipal, Agent: delegAgent, AgentKey: agentPub}),
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

// The attack that the original name-only binding permitted, kept as a
// regression test.
//
// A credential names an agent, but the presenter chooses what to call itself.
// An attacker on a *different API key* who obtains a credential — from an
// export, from get_positions, from any log — could submit under the same
// agent_id and inherit the delegation. Hosted-mode namespacing did not help,
// because the name the credential binds is the portable unscoped one, and
// signature_policy did not help either: the attacker's scoped identity had no
// registered key, so the "required" branch treated it as an agent that never
// opted into signing and exempted it.
//
// Proof-of-possession closes it. The attacker cannot sign as the confirmation
// key, and registering that public key under their own namespace does not help
// because they lack the private half.
func TestA2A_LeakedCredentialIsUselessToAnotherTenant(t *testing.T) {
	svc, db := newTestService(t)
	ctx := context.Background()

	credits, err := payments.NewCreditStore(db.RawDB())
	if err != nil {
		t.Fatalf("credit store: %v", err)
	}
	victimTok, err := credits.GenerateKey("victim@example.com", "cus_v", "cs_v", 1000)
	if err != nil {
		t.Fatalf("victim key: %v", err)
	}
	attackerTok, err := credits.GenerateKey("attacker@example.com", "cus_a", "cs_a", 1000)
	if err != nil {
		t.Fatalf("attacker key: %v", err)
	}
	rateLim := payments.NewRateLimiter(ctx, 500, time.Minute)
	chain := mcp.A2AAuthMiddleware("test-secret", credits, rateLim, nil, nil, false)(
		mcp.A2AHandler(svc, credits, nil, db, nil))

	cred, agentPriv := a2aDelegation(t, svc, chain, victimTok, delegPrincipal, delegAgent)

	d, err := svc.CreateDeliberation(ctx, "cross-tenant replay", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The legitimate holder submits, which is also how the credential becomes
	// readable to every other participant.
	const legit = "the real alice-agent"
	ok := a2aCall(t, chain, victimTok, "gemot/participate", map[string]any{
		"action":               "submit_position",
		"deliberation_id":      d.ID,
		"agent_id":             delegAgent,
		"content":              legit,
		"on_behalf_of":         delegPrincipal,
		"principal_credential": cred,
		"signature": base64.StdEncoding.EncodeToString(
			signPosition(t, agentPriv, delegAgent, d.ID, d.Round, legit)),
	})
	if ok.Error != nil {
		t.Fatalf("legitimate submit: %v", ok.Error.Message)
	}

	// A different tenant replays the same credential under the same claimed
	// agent_id. They register their own key under that name in their own
	// namespace and sign with it — the best they can do without alice's key.
	evilPub, evilPriv := newKeypair(t)
	evilScoped := payments.KeyID(attackerTok) + ":" + delegAgent
	if err := svc.RegisterAgentKey(ctx, evilScoped, evilPub, "ed25519"); err != nil {
		t.Fatalf("attacker key registration: %v", err)
	}
	const forged = "I speak for alice too"
	evil := a2aCall(t, chain, attackerTok, "gemot/participate", map[string]any{
		"action":               "submit_position",
		"deliberation_id":      d.ID,
		"agent_id":             delegAgent,
		"content":              forged,
		"on_behalf_of":         delegPrincipal,
		"principal_credential": cred,
		"signature": base64.StdEncoding.EncodeToString(
			signPosition(t, evilPriv, delegAgent, d.ID, d.Round, forged)),
	})
	if evil.Error == nil {
		t.Fatal("a leaked credential was replayed by another tenant — proof-of-possession is not being enforced")
	}
	if !strings.Contains(evil.Error.Message, "bound to a different key") {
		t.Errorf("error = %q, want a confirmation-key mismatch", evil.Error.Message)
	}
}

// The full delegation flow must be completable over A2A alone. Adding
// principal_credential to the A2A submit path without a way to register the
// agent's key made this impossible: an A2A-only client could present a
// credential it had no route to ever satisfy. The only step not on the wire is
// the principal signing its credential, which is offline by design.
func TestA2A_DelegationIsCompletableOverA2AAlone(t *testing.T) {
	svc, db := newTestService(t)
	ctx := context.Background()
	chain, token := a2aChain(t, db, svc)

	// Step 1 (offline): the principal registers its key and signs a credential
	// bound to the agent key generated in step 2.
	principalPriv := registerPrincipal(t, svc, delegPrincipal)
	agentPub, agentPriv := newKeypair(t)

	// Step 2: the agent registers its own key — over A2A.
	a2aRegisterKey(t, chain, token, delegAgent, agentPub)

	cred := credentialParams(t, principalPriv, principal.Credential{
		Principal: delegPrincipal, Agent: delegAgent, AgentKey: agentPub,
	})

	d, err := svc.CreateDeliberation(ctx, "A2A-only delegation", "",
		deliberation.WithPrincipalPolicy("required"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Step 3: submit, proving possession — over A2A.
	const content = "delegated, set up entirely over A2A"
	resp := a2aCall(t, chain, token, "gemot/participate", map[string]any{
		"action":               "submit_position",
		"deliberation_id":      d.ID,
		"agent_id":             delegAgent,
		"content":              content,
		"on_behalf_of":         delegPrincipal,
		"principal_credential": cred,
		"signature": base64.StdEncoding.EncodeToString(
			signPosition(t, agentPriv, delegAgent, d.ID, d.Round, content)),
	})
	if resp.Error != nil {
		t.Fatalf("A2A-only delegation failed at submit: %v", resp.Error.Message)
	}
	var p struct {
		PrincipalVerified bool `json:"principal_verified"`
	}
	if err := json.Unmarshal(resp.Result, &p); err != nil {
		t.Fatalf("decode position: %v", err)
	}
	if !p.PrincipalVerified {
		t.Error("principal_verified = false after a fully A2A-driven delegation")
	}
}

// Revocation must also be reachable over A2A, otherwise an A2A client can grant
// itself signing authority but never withdraw it.
func TestA2A_RevokeKeyOverA2A(t *testing.T) {
	svc, db := newTestService(t)
	ctx := context.Background()
	chain, token := a2aChain(t, db, svc)

	pub, priv := newKeypair(t)
	a2aRegisterKey(t, chain, token, delegAgent, pub)

	d, err := svc.CreateDeliberation(ctx, "A2A revoke", "",
		deliberation.WithSignaturePolicy("required"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// While the key is live, signature_policy=required is satisfied by signing.
	const first = "signed while the key is live"
	ok := a2aCall(t, chain, token, "gemot/participate", map[string]any{
		"action": "submit_position", "deliberation_id": d.ID,
		"agent_id": delegAgent, "content": first,
		"signature": base64.StdEncoding.EncodeToString(
			signPosition(t, priv, delegAgent, d.ID, d.Round, first)),
	})
	if ok.Error != nil {
		t.Fatalf("signed submit: %v", ok.Error.Message)
	}

	resp := a2aCall(t, chain, token, "gemot/participate", map[string]any{
		"action": "revoke_key", "agent_id": delegAgent,
	})
	if resp.Error != nil {
		t.Fatalf("revoke_key over A2A: %v", resp.Error.Message)
	}

	// The same signature no longer verifies against any registered key.
	const second = "signed after revocation"
	after := a2aCall(t, chain, token, "gemot/participate", map[string]any{
		"action": "submit_position", "deliberation_id": d.ID,
		"agent_id": delegAgent, "content": second,
		"signature": base64.StdEncoding.EncodeToString(
			signPosition(t, priv, delegAgent, d.ID, d.Round, second)),
	})
	if after.Error == nil {
		t.Fatal("signed submission accepted after the key was revoked over A2A")
	}
}
