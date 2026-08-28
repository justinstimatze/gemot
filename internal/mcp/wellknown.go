package mcp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
	"github.com/justinstimatze/gemot/internal/principal"
)

const notFoundHTML = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Gemot — 404 Not Found</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>*{margin:0;padding:0;box-sizing:border-box;}body{font-family:'Inter',system-ui,sans-serif;background:#fafaf8;color:#0f172a;line-height:1.6;}
.container{max-width:560px;margin:0 auto;padding:4rem 1.5rem;}a{color:#4f46e5;text-decoration:none;}a:hover{color:#4338ca;}
h1{font-size:1.6rem;font-weight:700;margin-bottom:0.5rem;}p{color:#64748b;margin-bottom:1.5rem;}
ul{padding-left:1.25rem;color:#475569;}li{margin-bottom:0.4rem;}</style></head><body>
<div class="container"><h1>404 — not found</h1><p>The path you requested doesn't exist on gemot.dev.</p>
<ul>
<li><a href="/sitemap.xml">Sitemap</a></li>
<li><a href="/llms.txt">llms.txt</a></li>
<li><a href="/docs">Documentation</a></li>
<li><a href="/openapi.json">OpenAPI spec</a></li>
<li><a href="/">Home</a></li>
</ul></div></body></html>`

const notFoundMarkdown = `# 404 Not Found

The path you requested doesn't exist on gemot.dev.

## Where to look next

- [Sitemap](https://gemot.dev/sitemap.xml) — every indexable page
- [llms.txt](https://gemot.dev/llms.txt) — agent-facing site map and when-to-use guidance
- [Documentation](https://gemot.dev/docs) — full tool/method/endpoint reference
- [OpenAPI spec](https://gemot.dev/openapi.json) — machine-readable HTTP surface
- [MCP manifest](https://gemot.dev/.well-known/mcp.json)
- [Home](https://gemot.dev/)
`

// negotiateContent serves either the markdown or HTML variant of a page
// based on the Accept header, and always sets Vary: Accept so a CDN or
// shared cache doesn't serve the wrong variant to the next requester with a
// different Accept header (acceptmarkdown.com compliance).
func negotiateContent(w http.ResponseWriter, r *http.Request, htmlBody, markdownBody []byte) {
	w.Header().Set("Vary", "Accept")
	if strings.Contains(r.Header.Get("Accept"), "text/markdown") {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write(markdownBody) //nolint:errcheck
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(htmlBody) //nolint:errcheck
}

// notFoundHandler is the agent-friendly 404: a real HTTP 404 status (never a
// 200 app shell) with a body negotiated by Accept — JSON for agents that ask
// for it, a short styled page for browsers, and a short markdown body with
// sitemap/llms.txt/docs links otherwise (the default, since an agent hitting
// an unknown path rarely sends a specific Accept header).
func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Vary", "Accept")
	accept := r.Header.Get("Accept")
	switch {
	case strings.Contains(accept, "application/json"):
		jsonError(w, http.StatusNotFound, "not_found", "the requested path does not exist", "see /sitemap.xml, /llms.txt, or /docs")
	case strings.Contains(accept, "text/html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(notFoundHTML)) //nolint:errcheck
	default:
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(notFoundMarkdown)) //nolint:errcheck
	}
}

// staticFileHandler serves one embedded static file verbatim with a fixed
// Content-Type. Used for the plain machine-readable files (llms.txt,
// robots.txt, sitemap.xml, openapi.json, the MCP manifest, the OG image)
// that have no HTML/negotiation variant.
func staticFileHandler(file, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFS.ReadFile(file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Write(data) //nolint:errcheck
	}
}

// oauthAuthServerMetadataHandler publishes RFC 8414 authorization server
// metadata. client_credentials (a standards-compliant PRESENTATION of the
// existing gmt_ bearer-key model, not a new credential-issuance path — see
// oauthClientCredentialsGrant) is always advertised.
//
// authorization_code + PKCE is advertised only when minter is non-nil (the
// GEMOT_OAUTH_CONSENT-gated hosted consent flow is enabled) — this is NOT a
// general user-account system: it's a narrow flow where a human proves
// control of their own gmt_ API key and approves a specific agent. minter
// being the single gate here means metadata can never advertise a grant
// type the server doesn't actually serve, and vice versa — see main.go for
// why minting and trusting-what-was-minted are wired together the same way.
func oauthAuthServerMetadataHandler(baseURL string, minter *principal.Minter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		grantTypes := []string{"client_credentials"}
		responseTypes := []string{}
		// "deliberate" is the coarse, always-valid full-access scope the
		// client_credentials facade grants (it just hands back the API key
		// itself). The two entries added below when minter is enabled are
		// PATTERNS, not literal scope strings — RFC 8414 has no notation for a
		// parameterized scope, so these are illustrative of the syntax
		// oauthScopeDescription actually parses (ScopeDeliberationPrefix /
		// ScopeGroupPrefix), not an exhaustive enumeration.
		scopes := []string{"deliberate"}
		meta := map[string]any{
			"issuer":                                baseURL,
			"token_endpoint":                        baseURL + "/oauth/token",
			"token_endpoint_auth_methods_supported": []string{"client_secret_post"},
			"service_documentation":                 baseURL + "/docs",
		}
		if minter != nil {
			grantTypes = append(grantTypes, "authorization_code")
			responseTypes = append(responseTypes, "code")
			scopes = append(scopes,
				principal.ScopeDeliberationPrefix+"<deliberation_id>",
				principal.ScopeGroupPrefix+"<group_id>",
			)
			meta["authorization_endpoint"] = baseURL + "/oauth/authorize"
			meta["code_challenge_methods_supported"] = []string{"S256"}
		}
		meta["grant_types_supported"] = grantTypes
		meta["response_types_supported"] = responseTypes
		meta["scopes_supported"] = scopes
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(meta)
	}
}

// oauthTokenHandler dispatches the RFC 6749 token endpoint by grant_type:
// client_credentials (always available, see oauthClientCredentialsGrant) or
// authorization_code (only when minter is configured, see
// oauthAuthorizationCodeGrant). Shared rate limiting and form parsing happen
// once here regardless of which grant is requested.
func oauthTokenHandler(svc *deliberation.Service, creditStore *payments.CreditStore, minter *principal.Minter, limiter *payments.RateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		allowed := limiter.Allow("oauth:" + ip)
		limiter.SetRateLimitHeaders(w, "oauth:"+ip, allowed)
		if !allowed {
			jsonError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded", "retry after a short delay")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "could not parse application/x-www-form-urlencoded body")
			return
		}
		switch r.FormValue("grant_type") {
		case "client_credentials":
			oauthClientCredentialsGrant(w, r, creditStore)
		case "authorization_code":
			oauthAuthorizationCodeGrant(w, r, svc, minter)
		default:
			writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "supported grant types: client_credentials, authorization_code")
		}
	}
}

// writeOAuthError writes an RFC 6749 §5.2 token-endpoint error body — a
// DIFFERENT shape than jsonError's {error,code,hint}, because this is a
// protocol gemot must match exactly for OAuth-aware clients to parse it.
func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}

// methodNotAllowedJSON returns a handler for the any-method fallback pattern
// registered alongside a method-specific one (Go's enhanced ServeMux: e.g.
// "POST /a2a" plus a bare "/a2a" — the method-specific pattern wins for its
// method, and everything else falls through to the bare one). Without this,
// a request to a documented API endpoint using the wrong method doesn't
// match ANY registered pattern and falls through to the generic 404
// (markdown by default) instead of a 405 that correctly signals "this
// endpoint exists, wrong method" — the exact naive-crawler probe (GET on a
// POST-only API path, no Accept header) an "is this a real JSON API"
// check is likely to run.
func methodNotAllowedJSON(allowed ...string) http.HandlerFunc {
	allow := strings.Join(allowed, ", ")
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		jsonError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "use "+allow)
	}
}

// oauthAuthorizationCodeGrant implements RFC 6749 §4.1.3 for the hosted
// consent flow: exchanges a code minted by /oauth/authorize (after PKCE and
// client_id verification) for a freshly signed principal.Credential.
//
// minter == nil (feature disabled) rejects with unsupported_grant_type,
// matching what oauthAuthServerMetadataHandler advertises for the same
// condition — a deployment that doesn't advertise this grant never serves
// it either.
func oauthAuthorizationCodeGrant(w http.ResponseWriter, r *http.Request, svc *deliberation.Service, minter *principal.Minter) {
	if minter == nil {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "authorization_code is not enabled on this deployment")
		return
	}
	code := r.FormValue("code")
	verifier := r.FormValue("code_verifier")
	clientID := r.FormValue("client_id")
	if code == "" || verifier == "" || clientID == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code, code_verifier, and client_id are required")
		return
	}

	// Re-resolve the agent's CURRENT key BEFORE consuming the code (rather
	// than trusting a snapshot from authorize time, and never accepting a
	// client-submitted pubkey here). Checking this first — instead of after
	// consuming — means a key revoked in the narrow window between
	// /oauth/authorize and this exchange leaves the code intact: the agent
	// can re-register a key and retry with the SAME code rather than
	// restarting the whole human-consent flow. A real infra error (not just
	// "no key registered") is logged, since the response to the caller must
	// collapse both into the same invalid_grant either way.
	agentKey, _, err := svc.GetActiveAgentKey(r.Context(), clientID)
	if err != nil {
		if !errors.Is(err, deliberation.ErrAgentKeyNotFound) {
			slog.Error("oauth authorization_code: GetActiveAgentKey failed", "client_id", clientID, "error", err)
		}
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client_id has no registered key")
		return
	}

	// Atomically consumes the code exactly once, and only for the matching
	// client_id (RFC 6749 client-confusion guard, folded into the same
	// atomic UPDATE — see AccessStore.ConsumeOAuthAuthorizationCode).
	// "unknown", "expired", "already used", and "wrong client" all collapse
	// into this one invalid_grant — distinguishing them would be an oracle.
	oc, err := svc.ConsumeOAuthAuthorizationCode(r.Context(), code, clientID)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "invalid, expired, or already-used authorization code")
		return
	}

	// PKCE S256: base64url(sha256(verifier)) must match the challenge stored
	// at /oauth/authorize time. Constant-time compare — same pattern http.go
	// already uses for bearer-token comparisons. A wrong verifier burns the
	// code (already consumed above) rather than leaving it retryable — that
	// is deliberate, unlike the key-check above: PKCE failing is a signal the
	// code may have been intercepted, so immediately invalidating it (rather
	// than letting an attacker keep guessing verifiers against a live code)
	// is the actual anti-replay property PKCE exists for.
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(computed), []byte(oc.CodeChallenge)) != 1 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match code_challenge")
		return
	}

	cred, err := minter.Mint(oc.Principal, clientID, agentKey, oc.Scope, oauthCredentialTTL, time.Now())
	if err != nil {
		// Not a client mistake — RFC 6749 §5.2 has no "server_error" code for
		// the token endpoint (that's an authorization-endpoint-only code), so
		// this is a plain internal error, not an OAuth-shaped one.
		slog.Error("oauth authorization_code: mint failed", "client_id", clientID, "error", err)
		jsonError(w, http.StatusInternalServerError, "internal_error", "failed to mint delegation credential", "")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token_type":           "delegation_credential",
		"principal_credential": cred,
		"principal":            cred.Principal,
		"expires_in":           int(oauthCredentialTTL.Seconds()),
	})
}

// oauthClientCredentialsGrant implements RFC 6749 §4.4 as a thin facade
// over gemot's existing API keys: client_secret must be an existing, active
// gmt_ key (checked the same way /export and /balance check Bearer tokens),
// and the returned access_token is that SAME key. This path itself mints
// nothing new — gemot's only *key*-minting path stays Stripe checkout
// (anti-abuse: no agent-facing API key mint). oauthAuthorizationCodeGrant
// above IS a second minting path, but it mints a scoped, time-limited
// delegation Credential from an already-registered agent key, never a new
// bearer API key — a materially different capability from this one.
func oauthClientCredentialsGrant(w http.ResponseWriter, r *http.Request, creditStore *payments.CreditStore) {
	secret := r.FormValue("client_secret")
	if secret == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "client_secret is required (your existing gmt_ API key)")
		return
	}
	if creditStore == nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", "OAuth token issuance is unavailable in demo mode")
		return
	}
	active, err := creditStore.KeyActive(secret)
	if err != nil || !active {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client_secret is not a valid, active API key")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"access_token": secret,
		"token_type":   "Bearer",
		"scope":        "deliberate",
	})
}
