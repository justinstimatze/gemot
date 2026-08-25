package mcp

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/justinstimatze/gemot/internal/payments"
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
// metadata advertising ONLY the client_credentials grant — gemot has no
// user-account/consent system to back an authorization_code flow, so it
// doesn't advertise one (same honesty-over-checkbox stance as
// ProtectedResourceHandler's deliberate omission of authorization_servers).
// The client_secret in the token exchange is an existing gmt_ API key: this
// is a standards-compliant PRESENTATION of the existing bearer-key model,
// not a new credential-issuance path. See postOAuthToken.
func oauthAuthServerMetadataHandler(baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                baseURL,
			"token_endpoint":                        baseURL + "/oauth/token",
			"grant_types_supported":                 []string{"client_credentials"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_post"},
			"scopes_supported":                      []string{"deliberate"},
			"response_types_supported":              []string{},
			"service_documentation":                 baseURL + "/docs",
		})
	}
}

// oauthTokenHandler implements RFC 6749 §4.4 (client_credentials grant)
// as a thin facade over gemot's existing API keys: client_secret must be an
// existing, active gmt_ key (checked the same way /export and /balance
// check Bearer tokens), and the returned access_token is that SAME key. No
// new credential is minted — gemot's only key-minting path stays Stripe
// checkout (anti-abuse: no agent-facing key mint), matching what
// oauthAuthServerMetadataHandler advertises.
func oauthTokenHandler(creditStore *payments.CreditStore, limiter *payments.RateLimiter) http.HandlerFunc {
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
		if r.FormValue("grant_type") != "client_credentials" {
			writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "only grant_type=client_credentials is supported")
			return
		}
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
