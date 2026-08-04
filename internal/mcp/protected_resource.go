package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// ProtectedResourceMetadata returns RFC 9728 protected-resource metadata for
// this server.
//
// Deliberate honesty: gemot is a protected resource but NOT an OAuth
// deployment. It authenticates callers with opaque bearer API keys and meters
// paid actions with per-call MPP challenges — there is no OAuth authorization
// server to delegate to. RFC 9728 makes `authorization_servers` optional, so we
// OMIT it rather than advertise a handshake gemot doesn't implement. A
// spec-compliant client reading this document therefore won't be led into a
// broken DCR / authorization-code flow; it learns how gemot's real auth works
// and where the docs are.
//
// See COMPOSING.md for the full composition contract.
func ProtectedResourceMetadata(baseURL string) map[string]any {
	baseURL = strings.TrimRight(baseURL, "/")
	return map[string]any{
		// RFC 9728 §2 — the resource identifier this metadata describes.
		"resource":      baseURL,
		"resource_name": "Gemot",
		// Bearer credentials travel in the Authorization header only.
		"bearer_methods_supported": []string{"header"},
		"resource_documentation":   baseURL + "/docs",
		"resource_policy_uri":      baseURL + "/pricing",
		// Scope tokens follow the "<tool>:<action>" convention used across the
		// grouped MCP tool surface (see COMPOSING.md and act-claim.schema.json).
		// This is the vocabulary a composed delegation/act-claim layer attenuates
		// against; it is descriptive, not an OAuth scope registry.
		"scopes_supported": []string{
			"deliberation:create",
			"participate:submit_position",
			"participate:vote",
			"analyze:run",
			"analyze:expert_panel",
			"analyze:follow_up",
			"decide:commit",
			"coordinate:join",
		},
		// Non-standard, x-prefixed to avoid colliding with any future registered
		// metadata field. Describes what gemot actually does in place of OAuth.
		"x-gemot-auth": map[string]any{
			"session_bearer": "API key (gmt_...) in Authorization; enforced when GEMOT_REQUIRE_AUTH is set. Obtain keys at " + baseURL + "/pricing.",
			"per_action_payment": "Paid actions issue Machine Payments Protocol (MPP) challenges bound to (tool, action, model, deliberation_id). See " +
				baseURL + "/pricing and mpp.dev.",
			"action_signing":  "Optional per-action ed25519 signatures (positions/votes) plus an envelope proof-of-possession layer (nonce + timestamp), gated by GEMOT_ENVELOPE_MODE. See COMPOSING.md.",
			"composition_doc": baseURL + "/docs (COMPOSING.md)",
		},
	}
}

var (
	protectedResourceOnce  sync.Once
	protectedResourceBytes []byte
	protectedResourceETag  string
	protectedResourceErr   error
)

// ProtectedResourceHandler serves RFC 9728 metadata at
// /.well-known/oauth-protected-resource. baseURL is captured once on first
// request (it is fixed per process). Mirrors AgentCardHandler: cached body,
// strong ETag, If-None-Match revalidation, GET/HEAD only.
func ProtectedResourceHandler(baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		protectedResourceOnce.Do(func() {
			protectedResourceBytes, protectedResourceErr = json.MarshalIndent(ProtectedResourceMetadata(baseURL), "", "  ")
			if protectedResourceErr == nil {
				protectedResourceBytes = append(protectedResourceBytes, '\n')
				sum := sha256.Sum256(protectedResourceBytes)
				protectedResourceETag = `"` + hex.EncodeToString(sum[:8]) + `"`
			}
		})
		if protectedResourceErr != nil {
			http.Error(w, "failed to render protected resource metadata", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("ETag", protectedResourceETag)
		if match := r.Header.Get("If-None-Match"); match != "" {
			for _, candidate := range strings.Split(match, ",") {
				candidate = strings.TrimSpace(candidate)
				if candidate == "*" || candidate == protectedResourceETag {
					w.WriteHeader(http.StatusNotModified)
					return
				}
			}
		}
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(protectedResourceBytes)
	}
}
