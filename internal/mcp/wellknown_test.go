package mcp

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/payments"
)

// TestNotFoundHandler covers the agent-friendly 404 (item 1 of the Is
// Agentic audit): a real 404 status, never a 200 app shell, negotiated by
// Accept — JSON for agents that ask, HTML for browsers, markdown by default
// with links to the sitemap/llms.txt/docs so an agent can recover.
func TestNotFoundHandler(t *testing.T) {
	t.Run("default (no Accept) returns markdown with recovery links", func(t *testing.T) {
		rec := httptest.NewRecorder()
		notFoundHandler(rec, httptest.NewRequest(http.MethodGet, "/nonexistent-path-xyz", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/markdown") {
			t.Errorf("content-type = %q, want text/markdown", ct)
		}
		if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "Accept") {
			t.Errorf("Vary = %q, want to contain Accept", vary)
		}
		body := rec.Body.String()
		for _, want := range []string{"sitemap.xml", "llms.txt", "/docs"} {
			if !strings.Contains(body, want) {
				t.Errorf("404 markdown body missing recovery link %q", want)
			}
		}
	})

	t.Run("Accept: application/json returns structured JSON error", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/nonexistent-path-xyz", nil)
		req.Header.Set("Accept", "application/json")
		notFoundHandler(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		var body struct {
			Error string `json:"error"`
			Code  string `json:"code"`
			Hint  string `json:"hint"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("body is not valid JSON: %v", err)
		}
		if body.Code != "not_found" {
			t.Errorf("code = %q, want not_found", body.Code)
		}
	})

	t.Run("Accept: text/html returns a styled HTML page", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/nonexistent-path-xyz", nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		notFoundHandler(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Errorf("content-type = %q, want text/html", ct)
		}
	})
}

// TestNegotiateContent covers item 5 of the audit (acceptmarkdown.com): a
// request with Accept: text/markdown must get text/markdown back (never
// text/html), and every negotiated response must carry Vary: Accept so a
// CDN doesn't serve the wrong cached variant to the next requester.
func TestNegotiateContent(t *testing.T) {
	htmlBody := []byte("<html>hi</html>")
	mdBody := []byte("# hi")

	t.Run("Accept: text/markdown returns markdown, not html", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept", "text/markdown")
		negotiateContent(rec, req, htmlBody, mdBody)
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/markdown") {
			t.Errorf("content-type = %q, want text/markdown", ct)
		}
		if rec.Body.String() != string(mdBody) {
			t.Errorf("body = %q, want markdown body", rec.Body.String())
		}
	})

	t.Run("default Accept returns html", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept", "text/html")
		negotiateContent(rec, req, htmlBody, mdBody)
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Errorf("content-type = %q, want text/html", ct)
		}
	})

	t.Run("Vary: Accept is always set", func(t *testing.T) {
		for _, accept := range []string{"text/markdown", "text/html", ""} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if accept != "" {
				req.Header.Set("Accept", accept)
			}
			negotiateContent(rec, req, htmlBody, mdBody)
			if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "Accept") {
				t.Errorf("Accept=%q: Vary = %q, want to contain Accept", accept, vary)
			}
		}
	})
}

// TestOAuthAuthServerMetadataHandler covers item 3 of the audit: RFC 8414
// metadata must be published, and — the load-bearing honesty check, matching
// TestProtectedResourceOmitsAuthorizationServers's stance — must advertise
// ONLY client_credentials, never an authorization_code flow gemot can't
// service (no user-account/consent system exists).
func TestOAuthAuthServerMetadataHandler(t *testing.T) {
	h := oauthAuthServerMetadataHandler("https://gemot.dev")
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var meta struct {
		Issuer               string   `json:"issuer"`
		TokenEndpoint        string   `json:"token_endpoint"`
		GrantTypesSupported  []string `json:"grant_types_supported"`
		AuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if meta.Issuer != "https://gemot.dev" {
		t.Errorf("issuer = %q, want https://gemot.dev", meta.Issuer)
	}
	if meta.TokenEndpoint != "https://gemot.dev/oauth/token" {
		t.Errorf("token_endpoint = %q, want https://gemot.dev/oauth/token", meta.TokenEndpoint)
	}
	if len(meta.GrantTypesSupported) != 1 || meta.GrantTypesSupported[0] != "client_credentials" {
		t.Errorf("grant_types_supported = %v, want exactly [client_credentials] — gemot has no authorization_code flow to advertise", meta.GrantTypesSupported)
	}
	if len(meta.AuthMethodsSupported) != 1 || meta.AuthMethodsSupported[0] != "client_secret_post" {
		t.Errorf("token_endpoint_auth_methods_supported = %v, want exactly [client_secret_post] (must match what postOAuthToken actually implements)", meta.AuthMethodsSupported)
	}
}

// TestOAuthTokenHandler covers the client_credentials token endpoint without
// a database: with creditStore == nil (demo mode), a well-formed request must
// fail closed with an RFC 6749 §5.2 error body, never a 500 or a minted
// fake token.
func TestOAuthTokenHandler(t *testing.T) {
	limiter := payments.NewRateLimiter(context.Background(), 30, time.Minute)
	h := oauthTokenHandler(nil, limiter)

	t.Run("wrong grant_type is rejected per RFC 6749", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader("grant_type=authorization_code"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("body is not valid JSON: %v", err)
		}
		if body.Error != "unsupported_grant_type" {
			t.Errorf("error = %q, want unsupported_grant_type (RFC 6749 §5.2)", body.Error)
		}
	})

	t.Run("missing client_secret is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader("grant_type=client_credentials"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("no credit store (demo mode) fails closed, never mints a token", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader("grant_type=client_credentials&client_secret=gmt_whatever"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h(rec, req)
		if rec.Code == http.StatusOK {
			t.Fatal("demo mode (no credit store) must not issue a token")
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("content-type = %q, want application/json", ct)
		}
	})
}

// TestMachineReadableFilesAreWellFormed guards every published
// machine-readable file against syntax rot: a malformed openapi.json or
// mcp-manifest.json is worse than none for a client that tries to parse it.
func TestMachineReadableFilesAreWellFormed(t *testing.T) {
	t.Run("openapi.json is valid JSON with the documented paths", func(t *testing.T) {
		data, err := staticFS.ReadFile("static/openapi.json")
		if err != nil {
			t.Fatalf("reading openapi.json: %v", err)
		}
		var spec struct {
			OpenAPI          string                    `json:"openapi"`
			Paths            map[string]map[string]any `json:"paths"`
			VersioningPolicy struct {
				Strategy string `json:"strategy"`
				Header   string `json:"header"`
				Current  string `json:"current"`
			} `json:"x-versioning-policy"`
		}
		if err := json.Unmarshal(data, &spec); err != nil {
			t.Fatalf("openapi.json is not valid JSON: %v", err)
		}
		if spec.OpenAPI == "" {
			t.Error("missing openapi version field")
		}
		for _, want := range []string{"/health", "/a2a", "/oauth/token", "/.well-known/oauth-authorization-server"} {
			if _, ok := spec.Paths[want]; !ok {
				t.Errorf("openapi.json missing path %q", want)
			}
		}
		// Item 4 of the audit wants the versioning/deprecation policy
		// FORMALIZED in the spec, not just described in prose.
		if spec.VersioningPolicy.Strategy != "header" {
			t.Errorf("x-versioning-policy.strategy = %q, want header", spec.VersioningPolicy.Strategy)
		}
		if spec.VersioningPolicy.Header != "Gemot-Version" {
			t.Errorf("x-versioning-policy.header = %q, want Gemot-Version", spec.VersioningPolicy.Header)
		}
		if spec.VersioningPolicy.Current == "" {
			t.Error("x-versioning-policy.current is empty")
		}
		if _, ok := spec.Paths["/health"]["get"].(map[string]any)["responses"].(map[string]any)["200"].(map[string]any)["headers"].(map[string]any)["Gemot-Version"]; !ok {
			t.Error("GET /health's 200 response doesn't reference the Gemot-Version header component")
		}
		// Every operation must have a unique operationId (item 13/14: API schema
		// complexity + function-calling compatibility both key off this).
		seen := map[string]bool{}
		for path, methods := range spec.Paths {
			for method, opAny := range methods {
				op, ok := opAny.(map[string]any)
				if !ok {
					continue
				}
				id, _ := op["operationId"].(string)
				if id == "" {
					t.Errorf("%s %s missing operationId", method, path)
					continue
				}
				if seen[id] {
					t.Errorf("duplicate operationId %q", id)
				}
				seen[id] = true
				if _, ok := op["description"].(string); !ok {
					t.Errorf("%s: operation %q missing description", path, id)
				}
			}
		}
	})

	t.Run("mcp-manifest.json is valid JSON with the seven tools", func(t *testing.T) {
		data, err := staticFS.ReadFile("static/mcp-manifest.json")
		if err != nil {
			t.Fatalf("reading mcp-manifest.json: %v", err)
		}
		var manifest struct {
			Name string `json:"name"`
			MCP  struct {
				Endpoint  string `json:"endpoint"`
				Transport string `json:"transport"`
			} `json:"mcp"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			WhenToUse string `json:"when_to_use"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatalf("mcp-manifest.json is not valid JSON: %v", err)
		}
		if manifest.MCP.Endpoint != "https://gemot.dev/mcp" {
			t.Errorf("mcp.endpoint = %q", manifest.MCP.Endpoint)
		}
		if manifest.MCP.Transport != "streamable-http" {
			t.Errorf("mcp.transport = %q, want streamable-http (matches the actual /mcp handler, not the old sse-only transport)", manifest.MCP.Transport)
		}
		if len(manifest.Tools) != 7 {
			t.Errorf("tools count = %d, want 7 (deliberation, participate, analyze, decide, coordinate, admin, account)", len(manifest.Tools))
		}
		if manifest.WhenToUse == "" {
			t.Error("when_to_use is empty — item 12 of the audit wants specific when-to-use guidance")
		}
	})

	t.Run("sitemap.xml parses as XML with a urlset", func(t *testing.T) {
		data, err := staticFS.ReadFile("static/sitemap.xml")
		if err != nil {
			t.Fatalf("reading sitemap.xml: %v", err)
		}
		var doc struct {
			XMLName xml.Name `xml:"urlset"`
			URLs    []struct {
				Loc string `xml:"loc"`
			} `xml:"url"`
		}
		if err := xml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("sitemap.xml is not valid XML: %v", err)
		}
		if len(doc.URLs) == 0 {
			t.Error("sitemap.xml has no <url> entries")
		}
		foundHome := false
		for _, u := range doc.URLs {
			if u.Loc == "https://gemot.dev/" {
				foundHome = true
			}
		}
		if !foundHome {
			t.Error("sitemap.xml missing the homepage entry")
		}
	})

	t.Run("llms.txt has the required sections", func(t *testing.T) {
		data, err := staticFS.ReadFile("static/llms.txt")
		if err != nil {
			t.Fatalf("reading llms.txt: %v", err)
		}
		body := string(data)
		if !strings.HasPrefix(body, "# Gemot") {
			t.Error("llms.txt must start with an H1 site name per the llms.txt convention")
		}
		if !strings.Contains(body, "## When to use this") {
			t.Error("llms.txt missing a when-to-use section (item 12 of the audit)")
		}
	})

	t.Run("about.html and contact.html clear the 500-character trust-anchor threshold", func(t *testing.T) {
		for _, f := range []string{"static/about.html", "static/contact.html"} {
			data, err := staticFS.ReadFile(f)
			if err != nil {
				t.Fatalf("reading %s: %v", f, err)
			}
			text := stripTags(string(data))
			if len(text) < 500 {
				t.Errorf("%s: visible text is %d chars, want >= 500 (item 19 of the audit)", f, len(text))
			}
		}
	})
}

// stripTags is a crude HTML-tag stripper for the length check above — good
// enough to approximate what a text-extraction crawler sees, not a real
// HTML parser.
func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestStaticFileHandler(t *testing.T) {
	t.Run("serves embedded file with the given content type", func(t *testing.T) {
		h := staticFileHandler("static/robots.txt", "text/plain; charset=utf-8")
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("content-type = %q", ct)
		}
		if rec.Body.Len() == 0 {
			t.Error("empty body")
		}
	})

	t.Run("404s on a missing embedded file instead of panicking", func(t *testing.T) {
		h := staticFileHandler("static/does-not-exist.txt", "text/plain")
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/does-not-exist.txt", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

// TestMethodNotAllowedJSON is the regression for a real production gap: a
// request to a documented, method-restricted API path (e.g. GET /a2a, which
// is POST-only) using the wrong method didn't match ANY registered mux
// pattern and fell through to the generic 404 handler, which defaults to a
// markdown body absent an Accept header — reading as "not a JSON API" to a
// naive discovery probe. The fix registers this as the any-method fallback
// alongside the method-specific pattern (Go's enhanced ServeMux convention).
func TestMethodNotAllowedJSON(t *testing.T) {
	h := methodNotAllowedJSON("POST")
	rec := httptest.NewRecorder()
	// No Accept header at all — the exact naive-probe case that used to fall
	// through to the markdown-default 404.
	h(rec, httptest.NewRequest(http.MethodGet, "/a2a", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "POST" {
		t.Errorf("Allow = %q, want POST", allow)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json regardless of Accept header", ct)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if body.Code != "method_not_allowed" {
		t.Errorf("code = %q, want method_not_allowed", body.Code)
	}
}
