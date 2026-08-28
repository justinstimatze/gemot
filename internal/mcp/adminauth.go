package mcp

import (
	"crypto/subtle"
	"strings"
)

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header value. Returns "" (never a garbage substring) if auth doesn't use
// the Bearer scheme.
func bearerToken(auth string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return ""
	}
	return strings.TrimPrefix(auth, prefix)
}

// isAdminToken reports whether token matches the configured admin secret,
// constant-time. An empty apiSecret means dev mode: every caller counts as
// admin, matching every call site's existing behavior. Shared across
// /metrics, /export, /events, and A2AAuthMiddleware so a future change to
// this comparison only needs to land in one place.
//
// Deliberately does NOT read the token from anywhere itself (callers pass
// the token they already extracted) -- see EventsHandler for why the admin
// secret specifically must only ever be accepted from the Authorization
// header, never a query parameter.
func isAdminToken(token, apiSecret string) bool {
	if apiSecret == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(apiSecret)) == 1
}
