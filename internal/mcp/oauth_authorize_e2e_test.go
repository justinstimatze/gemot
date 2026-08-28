package mcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
	"github.com/justinstimatze/gemot/internal/principal"
	"github.com/justinstimatze/gemot/internal/store"
)

// TestOAuthAuthorizationCodeFlow_EndToEnd drives the full hosted consent
// flow through real HTTP handlers against a real Postgres backend: register
// an agent key, GET /oauth/authorize (rejecting an unknown client_id first,
// then rendering the consent page with an attacker-controlled client_id
// safely escaped), POST /oauth/authorize with a real API key to mint a
// code, POST /oauth/token to exchange it for a delegation_credential, then
// feed that credential into a real submit_position call and confirm
// principal_verified. Also covers the adversarial cases the plan calls
// out: a claimed-but-not-possessed key, and a replayed authorization code.
func TestOAuthAuthorizationCodeFlow_EndToEnd(t *testing.T) {
	db := oauthE2EDB(t)
	svc := deliberation.NewService(db, nil)
	ctx := context.Background()

	creditStore, err := payments.NewCreditStore(db.RawDB())
	if err != nil {
		t.Fatalf("NewCreditStore: %v", err)
	}
	apiKey, err := creditStore.GenerateKey("oauth-e2e@example.com", "", "", 100)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	const agentID = "test-agent-<script>alert(1)</script>"
	agentPub, agentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("agent keygen: %v", err)
	}
	if err := svc.RegisterAgentKey(ctx, agentID, agentPub, "ed25519"); err != nil {
		t.Fatalf("RegisterAgentKey: %v", err)
	}

	issuerPub, issuerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("issuer keygen: %v", err)
	}
	minter := principal.NewMinter("gemot-oauth", issuerPriv)
	rv, err := principal.NewRoutingVerifier(svc.PrincipalVerifier(), []principal.RemoteIssuer{
		{Name: "gemot-oauth", Namespaces: []string{"oauthkey:"}, PublicKey: issuerPub},
	})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	svc.SetPrincipalVerifier(rv)

	authorizeTmpl, err := template.ParseFS(staticFS, "static/oauth-authorize.html")
	if err != nil {
		t.Fatalf("parsing oauth-authorize.html: %v", err)
	}
	approvedTmpl, err := template.ParseFS(staticFS, "static/oauth-approved.html")
	if err != nil {
		t.Fatalf("parsing oauth-approved.html: %v", err)
	}
	limiter := payments.NewRateLimiter(ctx, 1000, time.Minute)

	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		t.Fatalf("verifier random: %v", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	challengeSum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeSum[:])

	// Unregistered client_id is rejected before any HTML renders.
	getHandler := oauthAuthorizeGetHandler(svc, authorizeTmpl, limiter)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id=nobody&code_challenge="+challenge+"&code_challenge_method=S256", nil)
	getHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unregistered client_id: status = %d, want 400", rec.Code)
	}
	var unknownClientBody struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &unknownClientBody); err != nil {
		t.Fatalf("unregistered client_id: body is not JSON: %v", err)
	}
	if unknownClientBody.Code != "invalid_client" {
		t.Errorf("unregistered client_id: code = %q, want invalid_client", unknownClientBody.Code)
	}

	// Registered agent's consent page renders with client_id auto-escaped —
	// the concrete regression test for html/template's escaping on an
	// attacker-controlled query param.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id="+url.QueryEscape(agentID)+"&code_challenge="+challenge+"&code_challenge_method=S256", nil)
	getHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /oauth/authorize: status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("client_id rendered unescaped in the consent page — XSS regression")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("client_id not found escaped in the rendered consent page")
	}

	// A real API key approves the request and mints a code.
	postHandler := oauthAuthorizePostHandler(svc, creditStore, approvedTmpl, authorizeTmpl, limiter)
	form := url.Values{
		"client_id":             {agentID},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {""},
		"state":                 {""},
		"api_key":               {apiKey},
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /oauth/authorize: status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	const codeMarker = `class="code">`
	idx := strings.Index(rec.Body.String(), codeMarker)
	if idx == -1 {
		t.Fatalf("no authorization code found in approved page: %s", rec.Body.String())
	}
	rest := rec.Body.String()[idx+len(codeMarker):]
	code := rest[:strings.Index(rest, "<")]
	if !strings.HasPrefix(code, "gac_") {
		t.Fatalf("extracted code %q doesn't look like an authorization code", code)
	}

	// Exchange the code for a delegation credential.
	tokenHandler := oauthTokenHandler(svc, creditStore, minter, limiter)
	exchange := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"code_verifier": {verifier},
			"client_id":     {agentID},
		}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		tokenHandler(rec, req)
		return rec
	}
	rec = exchange()
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /oauth/token: status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var tokenResp struct {
		TokenType           string          `json:"token_type"`
		PrincipalCredential json.RawMessage `json:"principal_credential"`
		Principal           string          `json:"principal"`
		ExpiresIn           int             `json:"expires_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tokenResp); err != nil {
		t.Fatalf("token response is not JSON: %v", err)
	}
	if tokenResp.TokenType != "delegation_credential" {
		t.Errorf("token_type = %q, want delegation_credential", tokenResp.TokenType)
	}
	if !strings.HasPrefix(tokenResp.Principal, "oauthkey:") {
		t.Errorf("principal = %q, want oauthkey: prefix", tokenResp.Principal)
	}

	// The minted credential works in a real submit_position call.
	d, err := svc.CreateDeliberation(ctx, "OAuth e2e", "")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}
	const content = "hello from an OAuth-delegated agent"
	sig := ed25519.Sign(agentPriv, auth.PositionPayload(agentID, d.ID, d.Round, content))
	p, err := svc.SubmitPosition(ctx, d.ID, agentID, content,
		deliberation.WithPrincipalCredential(tokenResp.PrincipalCredential),
		deliberation.WithSignature(sig),
	)
	if err != nil {
		t.Fatalf("SubmitPosition with minted credential: %v", err)
	}
	if !p.PrincipalVerified {
		t.Error("PrincipalVerified = false, want true")
	}
	if p.OnBehalfOf != tokenResp.Principal {
		t.Errorf("OnBehalfOf = %q, want %q", p.OnBehalfOf, tokenResp.Principal)
	}

	// Adversarial: the credential claims the agent's key, but the position
	// is signed by a different key entirely — proof-of-possession must
	// still reject this regardless of how the credential was minted.
	_, wrongPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("wrong keygen: %v", err)
	}
	wrongSig := ed25519.Sign(wrongPriv, auth.PositionPayload(agentID, d.ID, d.Round, content))
	if _, err := svc.SubmitPosition(ctx, d.ID, agentID, content,
		deliberation.WithPrincipalCredential(tokenResp.PrincipalCredential),
		deliberation.WithSignature(wrongSig),
	); err == nil {
		t.Error("submitting with a signature from an unregistered key must fail proof-of-possession")
	}

	// Adversarial: replaying the same authorization code a second time must
	// fail, even with the correct verifier.
	rec = exchange()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("replayed code: status = %d, want 400", rec.Code)
	}
	var replayBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &replayBody); err != nil {
		t.Fatalf("replay response is not JSON: %v", err)
	}
	if replayBody.Error != "invalid_grant" {
		t.Errorf("replayed code error = %q, want invalid_grant", replayBody.Error)
	}
}

// oauthE2EDB creates an isolated Postgres schema for the OAuth end-to-end
// test and returns a store.DB, mirroring tests/store_test.go's tempDB.
// Skips the test if Postgres isn't reachable.
func oauthE2EDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := oauthE2EDSN()

	probe, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("Postgres not reachable (%v) — set DATABASE_URL or start a local Postgres to enable this test", err)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := probe.PingContext(pingCtx); err != nil {
		probe.Close()
		t.Skipf("Postgres not reachable (%v) — set DATABASE_URL or start a local Postgres to enable this test", err)
	}

	schemaName := "test_oauth_e2e_" + strings.ReplaceAll(strings.ReplaceAll(t.Name(), "/", "_"), "-", "_")
	if len(schemaName) > 50 {
		schemaName = schemaName[:50]
	}
	schemaName = fmt.Sprintf("%s_%d", schemaName, os.Getpid()%10000)

	if _, err := probe.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName)); err != nil {
		probe.Close()
		t.Fatal(err)
	}
	if _, err := probe.Exec(fmt.Sprintf("CREATE SCHEMA %s", schemaName)); err != nil {
		probe.Close()
		t.Fatal(err)
	}
	probe.Close()

	schemaDSN := dsn
	if strings.Contains(schemaDSN, "?") {
		schemaDSN += "&search_path=" + schemaName
	} else {
		schemaDSN += "?search_path=" + schemaName
	}

	db, err := store.Open(schemaDSN)
	if err != nil {
		t.Fatalf("opening test schema: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		if cleanup, err := sql.Open("pgx", dsn); err == nil {
			_, _ = cleanup.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName))
			cleanup.Close()
		}
	})
	return db
}

// oauthE2EDSN returns the Postgres DSN for the OAuth end-to-end test,
// mirroring tests/store_test.go's testDSN (duplicated here since this
// package can't import that package's unexported test helpers).
func oauthE2EDSN() string {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
	}
	return "postgres://gemot:gemot@localhost:5432/gemot?sslmode=disable"
}
