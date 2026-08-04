package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestProtectedResourceOmitsAuthorizationServers is the load-bearing assertion:
// gemot is NOT an OAuth deployment, so its RFC 9728 metadata must never
// advertise an authorization server. Doing so would push a spec-compliant MCP
// client into a DCR / authorization-code flow that gemot can't service. See
// COMPOSING.md.
func TestProtectedResourceOmitsAuthorizationServers(t *testing.T) {
	meta := ProtectedResourceMetadata("https://gemot.dev")
	if _, present := meta["authorization_servers"]; present {
		t.Fatal("protected-resource metadata advertises authorization_servers — gemot does not run OAuth; this misleads clients")
	}
	if got, _ := meta["resource"].(string); got != "https://gemot.dev" {
		t.Errorf("resource = %q, want https://gemot.dev", got)
	}
	methods, ok := meta["bearer_methods_supported"].([]string)
	if !ok || len(methods) == 0 || methods[0] != "header" {
		t.Errorf("bearer_methods_supported = %v, want [header]", meta["bearer_methods_supported"])
	}
	// The honest description of what gemot does instead of OAuth must be present.
	if _, ok := meta["x-gemot-auth"].(map[string]any); !ok {
		t.Error("x-gemot-auth block missing — clients have no description of gemot's real auth")
	}
}

// TestProtectedResourceBaseURLNormalization ensures a trailing slash on the
// base URL doesn't produce doubled slashes in derived links.
func TestProtectedResourceBaseURLNormalization(t *testing.T) {
	meta := ProtectedResourceMetadata("https://gemot.dev/")
	if got, _ := meta["resource"].(string); got != "https://gemot.dev" {
		t.Errorf("resource = %q, want trailing slash trimmed", got)
	}
	if got, _ := meta["resource_documentation"].(string); got != "https://gemot.dev/docs" {
		t.Errorf("resource_documentation = %q, want https://gemot.dev/docs", got)
	}
}

// TestProtectedResourceHandler covers the wire-protocol bits — method gating,
// ETag, If-None-Match — mirroring TestAgentCardHandler.
func TestProtectedResourceHandler(t *testing.T) {
	h := ProtectedResourceHandler("https://gemot.dev")

	t.Run("GET returns 200 with JSON body and ETag", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		if etag := rec.Header().Get("ETag"); etag == "" || etag[0] != '"' {
			t.Errorf("ETag missing or unquoted: %q", etag)
		}
		var doc map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("body is not valid JSON: %v", err)
		}
		if _, present := doc["authorization_servers"]; present {
			t.Error("served body advertises authorization_servers")
		}
	})

	t.Run("HEAD returns 200 with no body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodHead, "/.well-known/oauth-protected-resource", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("HEAD leaked %d bytes of body", rec.Body.Len())
		}
	})

	t.Run("If-None-Match returns 304", func(t *testing.T) {
		warm := httptest.NewRecorder()
		h(warm, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))
		etag := warm.Header().Get("ETag")
		if etag == "" {
			t.Fatal("no ETag on warm-up call")
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
		req.Header.Set("If-None-Match", etag)
		h(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Errorf("status = %d, want 304", rec.Code)
		}
	})

	t.Run("non-GET/HEAD returns 405 with Allow header", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/.well-known/oauth-protected-resource", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", rec.Code)
		}
		if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
			t.Errorf("Allow = %q, want GET, HEAD", allow)
		}
	})
}

// TestActClaimSchemaIsValidJSON guards the published interop contract against
// syntax rot — a malformed schema is worse than none for a composer relying on
// it. Asserts it parses and declares the fields COMPOSING.md promises.
func TestActClaimSchemaIsValidJSON(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locating repo root: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "docs/act-claim.schema.json"))
	if err != nil {
		t.Fatalf("reading act-claim.schema.json: %v", err)
	}
	var schema struct {
		Schema   string         `json:"$schema"`
		Required []string       `json:"required"`
		Props    map[string]any `json:"properties"`
		Defs     map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatalf("act-claim.schema.json is not valid JSON: %v", err)
	}
	if schema.Schema == "" {
		t.Error("$schema missing — schema should declare its draft")
	}
	for _, field := range []string{"sub", "act"} {
		if _, ok := schema.Props[field]; !ok {
			t.Errorf("schema properties missing %q", field)
		}
	}
	if _, ok := schema.Defs["actor"]; !ok {
		t.Error("schema $defs missing recursive actor definition")
	}
}
