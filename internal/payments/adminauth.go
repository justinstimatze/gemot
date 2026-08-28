package payments

import (
	"crypto/subtle"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// BearerToken extracts the token from an "Authorization: Bearer <token>"
// header value. Returns "" (never a garbage substring) if auth doesn't use
// the Bearer scheme.
func BearerToken(auth string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return ""
	}
	return strings.TrimPrefix(auth, prefix)
}

// IsAdminToken reports whether token matches the configured admin secret,
// constant-time. An empty apiSecret means dev mode: every caller counts as
// admin, matching every call site's existing behavior. Shared across every
// admin-gated surface in gemot (/metrics, /export, /events,
// A2AAuthMiddleware, and this package's own MPP Middleware) so a future
// change to this comparison only needs to land in one place.
//
// Deliberately does NOT read the token from anywhere itself (callers pass
// the token they already extracted) -- see internal/mcp's EventsHandler for
// why the admin secret specifically must only ever be accepted from the
// Authorization header, never a query parameter.
func IsAdminToken(token, apiSecret string) bool {
	if apiSecret == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(apiSecret)) == 1
}
