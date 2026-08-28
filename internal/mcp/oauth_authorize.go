package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
	"github.com/justinstimatze/gemot/internal/principal"
)

// oauthCodeTTL is how long an authorization code (minted at /oauth/authorize,
// redeemed at /oauth/token) stays valid. Short and fixed — the code is meant
// to be handed to the agent immediately.
const oauthCodeTTL = 10 * time.Minute

// oauthCredentialTTL is the fixed lifetime of a minted principal.Credential.
// Server-side and non-negotiable: see principal.Minter.Mint's doc comment for
// why a client-chosen expiry would undo the point of Credential.ExpiresAt.
const oauthCredentialTTL = time.Hour

// oauthScopeDescription renders a plain-English description of an OAuth
// consent scope. hasScope is false only for the empty (unscoped) case, which
// the consent page uses to decide whether to show the loud unscoped warning.
func oauthScopeDescription(scope string) (desc string, hasScope bool) {
	switch {
	case scope == "":
		return "Full access — all deliberations", false
	case strings.HasPrefix(scope, principal.ScopeDeliberationPrefix):
		return "This deliberation only: " + strings.TrimPrefix(scope, principal.ScopeDeliberationPrefix), true
	case strings.HasPrefix(scope, principal.ScopeGroupPrefix):
		return "This group only: " + strings.TrimPrefix(scope, principal.ScopeGroupPrefix), true
	default:
		return "Unrecognized scope: " + scope, true
	}
}

// oauthApprovedData is the render context for oauth-approved.html.
type oauthApprovedData struct {
	ClientID           string
	Code               string
	CodeTTLDescription string
}

// oauthAuthorizeData is the render context for oauth-authorize.html. Named
// so a typo'd template field fails loudly at startup parse time instead of
// silently rendering <no value> — same discipline as tryCodeData.
type oauthAuthorizeData struct {
	ClientID            string
	ScopeDescription    string
	HasScope            bool
	TTLDescription      string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	State               string
	ErrorMessage        string
}

// oauthAuthorizeDataFrom builds the consent-page render context from a
// request's parameters, computing the scope description once so both the
// GET handler and the POST handler's re-render-on-error path stay in sync.
func oauthAuthorizeDataFrom(clientID, scope, codeChallenge, codeChallengeMethod, state, errMsg string) oauthAuthorizeData {
	desc, hasScope := oauthScopeDescription(scope)
	return oauthAuthorizeData{
		ClientID:            clientID,
		ScopeDescription:    desc,
		HasScope:            hasScope,
		TTLDescription:      oauthCredentialTTL.String(),
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		Scope:               scope,
		State:               state,
		ErrorMessage:        errMsg,
	}
}

// oauthAuthorizeGetHandler renders the consent page for an OAuth2
// authorization_code + PKCE request. Validates the request shape and that
// client_id already has a registered key BEFORE rendering anything — an
// agent that hasn't called register_key yet gets a JSON error, not a
// consent page for a request that could never succeed.
func oauthAuthorizeGetHandler(svc *deliberation.Service, tmpl *template.Template, limiter *payments.RateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		q := r.URL.Query()
		clientID := q.Get("client_id")

		// Rate-limit by IP AND by client_id independently — either an IP
		// hammering many client_ids, or many IPs hammering one victim
		// client_id, must be bounded. Neither check mints anything, so no
		// SetRateLimitHeaders bookkeeping is needed for the common case;
		// only the rejection path needs a response at all.
		allowedIP := limiter.Allow("ip:" + ip)
		allowedClient := clientID == "" || limiter.Allow("client:"+clientID)
		limiter.SetRateLimitHeaders(w, "ip:"+ip, allowedIP)
		if !allowedIP || !allowedClient {
			jsonError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded", "retry after a short delay")
			return
		}

		codeChallenge := q.Get("code_challenge")
		codeChallengeMethod := q.Get("code_challenge_method")
		scope := q.Get("scope")
		state := q.Get("state")

		if clientID == "" || codeChallenge == "" {
			jsonError(w, http.StatusBadRequest, "invalid_request", "client_id and code_challenge are required", "")
			return
		}
		if codeChallengeMethod != "S256" {
			jsonError(w, http.StatusBadRequest, "invalid_request", "code_challenge_method must be S256", "plain is not supported")
			return
		}

		if _, _, err := svc.GetActiveAgentKey(r.Context(), clientID); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid_client", "unknown client_id", "this agent must call participate action:register_key before requesting authorization")
			return
		}

		renderOAuthAuthorize(w, tmpl, oauthAuthorizeDataFrom(clientID, scope, codeChallenge, codeChallengeMethod, state, ""))
	}
}

// renderOAuthAuthorize executes tmpl into a buffer first, so a template
// error becomes a clean 500 instead of a truncated page — same discipline
// RunHTTP's /try/&lt;code&gt; handler uses for tryCodeTmpl.
func renderOAuthAuthorize(w http.ResponseWriter, tmpl *template.Template, data oauthAuthorizeData) {
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		slog.Error("oauth-authorize template render failed", "client_id", data.ClientID, "error", err)
		http.Error(w, "authorize page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-cache")
	_, _ = w.Write([]byte(buf.String()))
}

// oauthAuthorizePostHandler processes the human's consent-page submission:
// validates their gmt_ API key, derives the principal identity from it
// (never the associated email — see the principal package's "capability,
// never personal context" constraint), mints a short-lived authorization
// code, and shows it for the human to copy back to the agent.
func oauthAuthorizePostHandler(svc *deliberation.Service, creditStore *payments.CreditStore, approvedTmpl *template.Template, authorizeTmpl *template.Template, limiter *payments.RateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if creditStore == nil {
			jsonError(w, http.StatusBadRequest, "unavailable", "OAuth consent is unavailable in demo mode", "")
			return
		}
		ip := ClientIP(r)
		if !limiter.Allow("ip:" + ip) {
			jsonError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded", "retry after a short delay")
			return
		}
		if err := r.ParseForm(); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid_request", "could not parse form body", "")
			return
		}

		clientID := r.FormValue("client_id")
		codeChallenge := r.FormValue("code_challenge")
		codeChallengeMethod := r.FormValue("code_challenge_method")
		scope := r.FormValue("scope")
		state := r.FormValue("state")
		apiKey := r.FormValue("api_key")

		if clientID == "" || codeChallenge == "" || codeChallengeMethod != "S256" {
			jsonError(w, http.StatusBadRequest, "invalid_request", "missing or invalid authorization parameters", "")
			return
		}

		// Defense in depth: the agent's key could have been revoked between
		// the GET and this POST.
		if _, _, err := svc.GetActiveAgentKey(r.Context(), clientID); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid_client", "unknown client_id", "this agent must call participate action:register_key before requesting authorization")
			return
		}

		// Never distinguish "wrong format" from "inactive" from "nonexistent"
		// — all three re-render the same generic error, no oracle.
		active, err := creditStore.KeyActive(apiKey)
		if err != nil || !active {
			data := oauthAuthorizeDataFrom(clientID, scope, codeChallenge, codeChallengeMethod, state,
				"Invalid API key. Get one at gemot.dev/pricing, or check you copied it correctly.")
			renderOAuthAuthorize(w, authorizeTmpl, data)
			return
		}

		principalID := oauthPrincipalFromKey(apiKey)

		code, err := generateOAuthCode()
		if err != nil {
			slog.Error("oauth authorization code generation failed", "error", err)
			http.Error(w, "could not generate authorization code", http.StatusInternalServerError)
			return
		}
		oc := &deliberation.OAuthAuthorizationCode{
			Code:                code,
			AgentID:             clientID,
			Principal:           principalID,
			Scope:               scope,
			CodeChallenge:       codeChallenge,
			CodeChallengeMethod: codeChallengeMethod,
			ExpiresAt:           time.Now().Add(oauthCodeTTL),
			CreatedAt:           time.Now(),
		}
		if err := svc.CreateOAuthAuthorizationCode(r.Context(), oc); err != nil {
			slog.Error("oauth authorization code persist failed", "client_id", clientID, "error", err)
			http.Error(w, "could not create authorization code", http.StatusInternalServerError)
			return
		}

		var buf strings.Builder
		if err := approvedTmpl.Execute(&buf, oauthApprovedData{
			ClientID: clientID, Code: code, CodeTTLDescription: oauthCodeTTL.String(),
		}); err != nil {
			slog.Error("oauth-approved template render failed", "client_id", clientID, "error", err)
			http.Error(w, "approval page unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "private, no-cache")
		_, _ = w.Write([]byte(buf.String()))
	}
}

// generateOAuthCode mints a high-entropy authorization code. Prefixed
// distinctly from gmt_ API keys (payments.randomKey's shape) so the two are
// never confusable in logs or if a human pastes one into the wrong field.
func generateOAuthCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gac_" + hex.EncodeToString(b), nil
}

// oauthPrincipalFromKey derives a stable, non-reversible principal identity
// from a gmt_ API key -- never the key's associated email, per the principal
// package's hard "no personal data" constraint. "oauthkey:" is distinct from
// schema.sql's reputation-graph "key:<agent_keys.id>" vertex-naming
// convention, which is a different table with no functional collision today,
// but a distinct prefix avoids a future one.
func oauthPrincipalFromKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return "oauthkey:" + hex.EncodeToString(sum[:8])
}
