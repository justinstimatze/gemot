package mcp

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
	"github.com/justinstimatze/gemot/internal/principal"
	"github.com/justinstimatze/gemot/internal/store"
)

// SandboxResolver lets A2AAuthMiddleware authorize anonymous requests
// that carry a valid sandbox join code. Kept as an interface (rather
// than a *deliberation.Service dep) so tests can stub it without
// pulling the full service wiring.
type SandboxResolver interface {
	LookupJoinCode(ctx context.Context, code string) (*deliberation.JoinCode, *deliberation.Deliberation, error)
}

// sandboxAllowedOp is a (grouped-method, action) pair a bearer-less
// sandbox caller may invoke. Keep tight — each entry is an avenue for
// spam via a leaked join code. Rate-limited per IP downstream.
type sandboxAllowedOp struct{ method, action string }

var sandboxAllowedOps = map[sandboxAllowedOp]bool{
	{"gemot/coordinate", "join"}:             true,
	{"gemot/participate", "submit_position"}: true,
	{"gemot/participate", "vote"}:            true,
	{"gemot/participate", "get_positions"}:   true,
}

// peekA2ARequest reads the JSON-RPC request body, returns method and
// raw params, and restores r.Body so the downstream handler can read
// it again. Returns empty strings on any parse failure (not fatal —
// we fall through to Bearer auth).
func peekA2ARequest(r *http.Request) (method string, params map[string]any) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", nil
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var msg struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return "", nil
	}
	return msg.Method, msg.Params
}

// sandboxCodeFromParams extracts the join code from a sandbox-allowed
// call. For join (coordinate/join) the code field is `code`; for
// submit/vote/get_positions it's `join_code` so the two don't
// collide with any existing field.
func sandboxCodeFromParams(op sandboxAllowedOp, params map[string]any) string {
	if params == nil {
		return ""
	}
	if op.method == "gemot/coordinate" && op.action == "join" {
		if c, ok := params["code"].(string); ok {
			return c
		}
	}
	if c, ok := params["join_code"].(string); ok {
		return c
	}
	return ""
}

// A2AAuthMiddleware verifies A2A bearer-token auth and populates the request
// context with the same keys payments.Middleware uses on /mcp:
//
//   - payments.ContextKeyIsAdmin — true when the caller presents the admin secret
//   - payments.ContextKeyAPIKey  — the full customer token (gmt_...) for credit ops
//   - payments.ContextKeyKeyID   — the 8-char agent namespace derived from that token
//
// Lifting auth into a middleware is what lets EnvelopeMiddleware wrap the A2A
// handler: envelope verification calls scopeAgentID(ctx), which reads
// ContextKeyKeyID to rewrite "alice" → "k_test:alice" for key lookup in hosted
// mode. Previously A2AHandler parsed Authorization itself, so ContextKeyKeyID
// was never set by the time an outer middleware could see it.
//
// Responses use the JSON-RPC error envelope to match existing A2A clients.
// Unlike payments.Middleware, this middleware never falls through to sandbox
// mode — unauthenticated A2A requests are rejected outright, preserving the
// strict posture agents expect from /a2a.
func A2AAuthMiddleware(
	apiSecret string,
	creditStore *payments.CreditStore,
	rateLimiter *payments.RateLimiter,
	sandboxResolver SandboxResolver,
	sandboxLimiter *payments.RateLimiter,
	requireAuth bool,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Dev mode: no apiSecret configured → admin on every request.
			if apiSecret == "" {
				ctx = context.WithValue(ctx, payments.ContextKeyIsAdmin{}, true)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			authHdr := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHdr, "Bearer ") {
				// Bearer-less sandbox path: if the request is one of the
				// sandbox-allowed methods and carries a valid join code,
				// allow it through with ContextKeySandbox=true. Matches
				// the MCP-with-join-code flow so the /try invite block
				// is actually usable over /a2a.
				if !requireAuth && sandboxResolver != nil {
					method, params := peekA2ARequest(r)
					action, _ := params["action"].(string)
					op := sandboxAllowedOp{method: method, action: action}
					if sandboxAllowedOps[op] {
						code := sandboxCodeFromParams(op, params)
						if code != "" {
							jc, _, err := sandboxResolver.LookupJoinCode(ctx, code)
							if err == nil && jc != nil && time.Now().Before(jc.ExpiresAt) {
								ip := ClientIP(r)
								if sandboxLimiter != nil && !sandboxLimiter.Allow("sbx-a2a:"+ip) {
									writeA2AError(w, nil, -32000, "sandbox rate limit exceeded — slow down or get an API key")
									return
								}
								// Set a distinct keyID so downstream
								// CheckAccess runs the visibility check
								// instead of treating the caller as
								// admin (empty keyID). Format is
								// "sbx:<first-8-chars-of-code>" so logs
								// remain legible and the value is stable
								// across a sandbox session.
								codeBucket := code
								if len(codeBucket) > 8 {
									codeBucket = codeBucket[:8]
								}
								ctx = context.WithValue(ctx, payments.ContextKeySandbox{}, true)
								ctx = context.WithValue(ctx, payments.ContextKeyKeyID{}, "sbx:"+codeBucket)
								next.ServeHTTP(w, r.WithContext(ctx))
								return
							}
						}
					}
				}
				writeA2AError(w, nil, -32000, "Authorization: Bearer <api_key> required (or a valid sandbox join_code on sandbox-allowed methods)")
				return
			}
			token := strings.TrimPrefix(authHdr, "Bearer ")

			// Admin secret — unlimited access.
			if subtle.ConstantTimeCompare([]byte(token), []byte(apiSecret)) == 1 {
				ctx = context.WithValue(ctx, payments.ContextKeyIsAdmin{}, true)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Customer API key — must be gmt_-prefixed and exist in credit store.
			if creditStore == nil || !strings.HasPrefix(token, "gmt_") {
				writeA2AError(w, nil, -32000, "Invalid API key")
				return
			}
			valid, _ := creditStore.ValidateKey(token)
			if !valid {
				writeA2AError(w, nil, -32000, "Invalid or expired API key")
				return
			}

			keyID := payments.KeyID(token)
			if rateLimiter != nil && !rateLimiter.Allow(keyID) {
				writeA2AError(w, nil, -32000, "rate limit exceeded — max 30 requests per minute")
				return
			}

			ctx = context.WithValue(ctx, payments.ContextKeyAPIKey{}, token)
			ctx = context.WithValue(ctx, payments.ContextKeyKeyID{}, keyID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// sanitizeError maps known internal errors to user-friendly messages.
func sanitizeError(err error) string {
	if errors.Is(err, sql.ErrNoRows) {
		return "not found"
	}
	msg := err.Error()
	// Don't leak internal DB errors — catch by driver prefix or known patterns
	if strings.Contains(msg, "pq:") || strings.Contains(msg, "pgx") ||
		strings.Contains(msg, "database") || strings.Contains(msg, "disk") ||
		strings.Contains(msg, "locked") || strings.Contains(msg, "no rows") {
		slog.Error("internal error (sanitized)", "error", err)
		return "internal error"
	}
	return msg
}

// a2aMethods is the canonical list of supported A2A methods.
var a2aMethods = []string{
	"agent/info",
	"gemot/deliberation",
	"gemot/participate",
	"gemot/analyze",
	"gemot/decide",
	"gemot/coordinate",
	"gemot/admin",
}

// A2ARequest is an A2A JSON-RPC 2.0 request.
type A2ARequest struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	ID      any            `json:"id"`
	Params  map[string]any `json:"params"`
}

// A2AResponse is an A2A JSON-RPC 2.0 response.
type A2AResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

// AuditStore logs write operations and provides audit queries.
type AuditStore interface {
	LogAuditEvent(keyID, ip, method, deliberationID, agentID string)
	GetAuditLog(deliberationID string, limit int) ([]map[string]string, error)
}

func A2AHandler(svc *deliberation.Service, creditStore *payments.CreditStore, auditLog AuditStore, jobDB store.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Auth, admin detection, and rate limiting are all handled by
		// A2AAuthMiddleware. The handler just consumes the context it populates.
		ctx := r.Context()
		isAdmin, _ := ctx.Value(payments.ContextKeyIsAdmin{}).(bool)
		keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
		token, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)

		var req A2ARequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req); err != nil {
			writeA2AError(w, nil, -32700, "Parse error")
			return
		}
		if req.JSONRPC != "2.0" {
			writeA2AError(w, req.ID, -32600, "Invalid Request: jsonrpc must be 2.0")
			return
		}

		// Access control helper
		checkAccess := func(deliberationID string) error {
			return svc.CheckAccess(ctx, deliberationID, keyID)
		}
		// Credit deduction helper for LLM calls
		deductCredits := func(model string) (int, error) {
			if isAdmin || creditStore == nil || token == "" || !strings.HasPrefix(token, "gmt_") {
				return 0, nil
			}
			cost := payments.CreditCost(model)
			if _, err := creditStore.Deduct(token, cost); err != nil {
				balance, _ := creditStore.GetBalance(token)
				return 0, fmt.Errorf("insufficient credits: have %d, need %d", balance, cost)
			}
			return cost, nil
		}
		refundCredits := func(cost int) {
			if cost > 0 && creditStore != nil && token != "" {
				creditStore.AddCredits(token, cost) //nolint:errcheck
			}
		}

		// Audit log write operations
		action := str(req.Params, "action")
		if auditLog != nil {
			writeOps := map[string]map[string]bool{
				"gemot/deliberation": {"create": true, "delete": true, "set_template": true},
				"gemot/participate":  {"submit_position": true, "vote": true, "withdraw": true},
				"gemot/analyze":      {"run": true, "propose_compromise": true, "dispute_crux": true, "challenge": true, "expert_panel": true},
				"gemot/decide":       {"commit": true, "fulfill": true, "break": true},
				"gemot/coordinate":   {"delegate": true, "invite": true, "generate_join_code": true, "join": true},
				"gemot/admin":        {"report_abuse": true},
			}
			if actions, ok := writeOps[req.Method]; ok && actions[action] {
				ip := ClientIP(r)
				did := str(req.Params, "deliberation_id")
				aid := str(req.Params, "agent_id")
				auditLog.LogAuditEvent(keyID, strings.TrimSpace(ip), req.Method+":"+action, did, aid)
			}
		}

		// Helper to scope agent IDs. Colons stripped to prevent namespace impersonation.
		scope := func(agentID string) string {
			agentID = strings.ReplaceAll(agentID, ":", "_")
			if keyID == "" || agentID == "" {
				return agentID
			}
			return keyID + ":" + agentID
		}

		w.Header().Set("Content-Type", "application/json")

		switch req.Method {
		case "agent/info":
			writeA2AResult(w, req.ID, map[string]any{
				"name":        "Gemot",
				"description": "Structured deliberation for AI agent coordination.",
				"version":     Version,
				"url":         "https://gemot.dev",
				"docs":        "https://gemot.dev/docs",
				"methods":     len(a2aMethods) - 1,
			})

		case "gemot/deliberation":
			s := req.Params
			switch action {
			case "create":
				var dopts []deliberation.DeliberationOption
				if t := str(s, "template"); t != "" {
					dopts = append(dopts, deliberation.WithTemplate(t))
				}
				if t := str(s, "type"); t != "" {
					dopts = append(dopts, deliberation.WithType(t))
				}
				if v := str(s, "visibility"); v != "" {
					dopts = append(dopts, deliberation.WithVisibility(v))
				}
				if mp, ok := s["max_participants"]; ok {
					if f, ok := mp.(float64); ok && f > 0 {
						dopts = append(dopts, deliberation.WithMaxParticipants(int(f)))
					}
				}
				if rules, ok := s["rules"]; ok {
					if rulesMap, ok := rules.(map[string]any); ok {
						dopts = append(dopts, deliberation.WithRules(rulesMap))
					}
				}
				if g := str(s, "group_id"); g != "" {
					dopts = append(dopts, deliberation.WithGroupID(g))
				}
				if pp := str(s, "principal_policy"); pp != "" {
					dopts = append(dopts, deliberation.WithPrincipalPolicy(pp))
				}
				if sp := str(s, "signature_policy"); sp != "" {
					dopts = append(dopts, deliberation.WithSignaturePolicy(sp))
				}
				if dm, ok := s["deadline_minutes"]; ok {
					if f, ok := dm.(float64); ok && f > 0 {
						deadline := time.Now().Add(time.Duration(f) * time.Minute)
						dopts = append(dopts, deliberation.WithDeadline(deadline))
					}
				}
				if keyID != "" {
					dopts = append(dopts, deliberation.WithCreatorKey(keyID))
				}
				d, err := svc.CreateDeliberation(ctx, str(s, "topic"), str(s, "description"), dopts...)
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				writeA2AResult(w, req.ID, d)

			case "get":
				if err := checkAccess(str(s, "deliberation_id")); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				d, err := svc.GetDeliberation(ctx, str(s, "deliberation_id"))
				if err != nil {
					writeA2AError(w, req.ID, -32603, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, d)

			case "list":
				var pgLimit, pgOffset int
				if v, ok := s["limit"].(float64); ok {
					pgLimit = int(v)
				}
				if v, ok := s["offset"].(float64); ok {
					pgOffset = int(v)
				}
				delibs, err := svc.ListDeliberations(ctx, pgLimit, pgOffset, keyID)
				if err != nil {
					writeA2AError(w, req.ID, -32603, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, delibs)

			case "list_by_group":
				var pgLimit, pgOffset int
				if v, ok := s["limit"].(float64); ok {
					pgLimit = int(v)
				}
				if v, ok := s["offset"].(float64); ok {
					pgOffset = int(v)
				}
				delibs, err := CoreListByGroup(ctx, svc, str(s, "group_id"), keyID, isAdmin, pgLimit, pgOffset)
				if err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, delibs)

			case "list_by_agent":
				var pgLimit, pgOffset int
				if v, ok := s["limit"].(float64); ok {
					pgLimit = int(v)
				}
				if v, ok := s["offset"].(float64); ok {
					pgOffset = int(v)
				}
				delibs, err := CoreListByAgent(ctx, svc, str(s, "agent_id"), keyID, isAdmin, pgLimit, pgOffset)
				if err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, delibs)

			case "delete":
				deliberationID := str(s, "deliberation_id")
				if deliberationID == "" {
					writeA2AError(w, req.ID, -32602, "deliberation_id is required")
					return
				}
				if err := svc.DeleteDeliberation(ctx, deliberationID, keyID, isAdmin); err != nil {
					writeA2AError(w, req.ID, -32603, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, map[string]string{"status": "deleted"})

			case "set_template":
				deliberationID := str(s, "deliberation_id")
				template := str(s, "template")
				if deliberationID == "" || template == "" {
					writeA2AError(w, req.ID, -32602, "deliberation_id and template are required")
					return
				}
				if err := svc.SetTemplate(ctx, deliberationID, template, keyID); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				tmpl, _ := deliberation.GetTemplate(template)
				writeA2AResult(w, req.ID, map[string]any{
					"deliberation_id": deliberationID,
					"template":        template,
					"description":     tmpl.Description,
					"threshold":       tmpl.SuggestedThreshold,
				})

			case "export":
				export, err := CoreExportDeliberation(ctx, svc, str(s, "deliberation_id"), keyID, auditLog)
				if err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, export)

			case "set_group":
				if !isAdmin {
					writeA2AError(w, req.ID, -32000, "admin only")
					return
				}
				deliberationID := str(s, "deliberation_id")
				groupID := str(s, "group_id")
				if deliberationID == "" || groupID == "" {
					writeA2AError(w, req.ID, -32602, "deliberation_id and group_id are required")
					return
				}
				if err := svc.SetGroupID(ctx, deliberationID, groupID); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				writeA2AResult(w, req.ID, map[string]string{"status": "updated", "group_id": groupID})

			case "create_share":
				if !isAdmin {
					writeA2AError(w, req.ID, -32000, "admin only")
					return
				}
				groupID := str(s, "group_id")
				if groupID == "" {
					writeA2AError(w, req.ID, -32602, "group_id is required")
					return
				}
				shareToken, err := svc.CreateShareToken(ctx, groupID)
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				writeA2AResult(w, req.ID, map[string]string{
					"token":    shareToken,
					"group_id": groupID,
				})

			case "lookup_share":
				shareToken := str(s, "token")
				if shareToken == "" {
					writeA2AError(w, req.ID, -32602, "token is required")
					return
				}
				groupID, err := svc.LookupShareToken(ctx, shareToken)
				if err != nil {
					writeA2AError(w, req.ID, -32603, sanitizeError(err))
					return
				}
				delibs, err := CoreListByGroup(ctx, svc, groupID, keyID, isAdmin, 0, 0)
				if err != nil {
					writeA2AError(w, req.ID, -32603, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, map[string]any{
					"group_id":      groupID,
					"deliberations": delibs,
				})

			default:
				writeA2AError(w, req.ID, -32602, fmt.Sprintf("unknown action %q for gemot/deliberation", action))
			}

		case "gemot/participate":
			s := req.Params
			switch action {
			case "submit_position":
				if err := checkAccess(str(s, "deliberation_id")); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				unscopedAgentID := str(s, "agent_id")
				agentID := scope(unscopedAgentID)
				content := str(s, "content")
				if content == "" {
					writeA2AError(w, req.ID, -32602, "content is required")
					return
				}
				if len(content) > 65536 {
					writeA2AError(w, req.ID, -32602, "content exceeds maximum length of 65536 bytes")
					return
				}
				var popts []deliberation.PositionOption
				if mf := str(s, "model_family"); mf != "" {
					popts = append(popts, deliberation.WithModelFamily(mf))
				}
				if g := str(s, "group"); g != "" {
					popts = append(popts, deliberation.WithGroup(g))
				}
				if cv, ok := s["conviction"]; ok {
					if f, ok := cv.(float64); ok && f > 0 {
						popts = append(popts, deliberation.WithConviction(f))
					}
				}
				if r := str(s, "reservation"); r != "" {
					popts = append(popts, deliberation.WithReservation(r))
				}
				if ob := str(s, "on_behalf_of"); ob != "" {
					popts = append(popts, deliberation.WithOnBehalfOf(ob))
				}
				if interests := str(s, "interests"); interests != "" {
					popts = append(popts, deliberation.WithInterests(interests))
				}
				// A2A accepts on_behalf_of, so it must also accept the
				// credential backing it — otherwise A2A callers could never
				// satisfy a deliberation with principal_policy=required.
				if pc, ok := s["principal_credential"]; ok {
					raw, err := parseA2ACredential(pc)
					if err != nil {
						writeA2AError(w, req.ID, -32602, err.Error())
						return
					}
					popts = append(popts, deliberation.WithPrincipalCredential(raw))
				}
				isDraft := false
				if d, ok := s["draft"]; ok {
					if b, ok := d.(bool); ok && b {
						isDraft = true
						popts = append(popts, deliberation.WithDraft())
					}
				}
				if md, ok := s["metadata"]; ok {
					if m, ok := md.(map[string]any); ok && len(m) > 0 {
						popts = append(popts, deliberation.WithMetadata(m))
					}
				}
				// Per-action ed25519 signature (optional). The client signs with
				// the unscoped agent_id; SubmitPositionWithSigningID reconstructs
				// the canonical payload using that same form while the stored
				// record remains scoped.
				if sigB64 := str(s, "signature"); sigB64 != "" {
					sigBytes, err := base64.StdEncoding.DecodeString(sigB64)
					if err != nil {
						writeA2AError(w, req.ID, -32602, fmt.Sprintf("signature must be base64-encoded: %v", err))
						return
					}
					popts = append(popts, deliberation.WithSignature(sigBytes))
				}
				var posCost int
				if !isDraft && !isAdmin {
					if dd, err := svc.GetDeliberation(ctx, str(s, "deliberation_id")); err == nil {
						posCost = deliberation.RuleInt(dd, "position_cost", 0)
						if posCost > 0 && creditStore != nil && token != "" && strings.HasPrefix(token, "gmt_") {
							balance, _ := creditStore.GetBalance(token)
							if balance < posCost {
								writeA2AError(w, req.ID, -32000, fmt.Sprintf("position cost: insufficient credits: have %d, need %d", balance, posCost))
								return
							}
						}
					}
				}
				p, err := svc.SubmitPositionWithSigningID(ctx, str(s, "deliberation_id"), agentID, unscopedAgentID, content, popts...)
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				if posCost > 0 && creditStore != nil && token != "" && strings.HasPrefix(token, "gmt_") {
					if _, err := creditStore.Deduct(token, posCost); err != nil {
						slog.Error("position cost deduction failed", "key_prefix", token[:12], "cost", posCost, "error", err)
					}
				}
				writeA2AResult(w, req.ID, p)

			case "publish_position":
				if err := CorePublishPosition(ctx, svc, str(s, "position_id"), keyID); err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, map[string]string{"status": "position published"})

			case "vote":
				if err := checkAccess(str(s, "deliberation_id")); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				unscopedVoteAgentID := str(s, "agent_id")
				agentID := scope(unscopedVoteAgentID)
				value, err := coerceVoteValue(s["value"])
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				if sigB64 := str(s, "signature"); sigB64 != "" {
					sigBytes, err := base64.StdEncoding.DecodeString(sigB64)
					if err != nil {
						writeA2AError(w, req.ID, -32602, fmt.Sprintf("signature must be base64-encoded: %v", err))
						return
					}
					if err := svc.SubmitSignedVoteWithSigningID(ctx, str(s, "deliberation_id"), agentID, unscopedVoteAgentID, str(s, "position_id"), value, str(s, "qualifier"), str(s, "caveat"), str(s, "criterion_id"), sigBytes); err != nil {
						writeA2AError(w, req.ID, -32000, err.Error())
						return
					}
				} else if err := svc.Vote(ctx, str(s, "deliberation_id"), agentID, str(s, "position_id"), value, str(s, "qualifier"), str(s, "caveat"), str(s, "criterion_id")); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				writeA2AResult(w, req.ID, map[string]string{"status": "vote recorded"})

			case "get_positions":
				if err := checkAccess(str(s, "deliberation_id")); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				var excludeAgent *string
				if ea := str(s, "exclude_agent_id"); ea != "" {
					excludeAgent = &ea
				}
				var round *int
				if v, ok := s["round"]; ok {
					if f, ok := v.(float64); ok {
						r := int(f)
						round = &r
					}
				}
				positions, err := svc.GetPositions(ctx, str(s, "deliberation_id"), excludeAgent, round)
				if err != nil {
					writeA2AError(w, req.ID, -32603, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, positions)

			case "get_context":
				if err := checkAccess(str(s, "deliberation_id")); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				agentID := scope(str(s, "agent_id"))
				actx, err := svc.GetContext(ctx, str(s, "deliberation_id"), agentID)
				if err != nil {
					writeA2AError(w, req.ID, -32603, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, actx)

			case "withdraw":
				deliberationID := str(s, "deliberation_id")
				agentID := scope(str(s, "agent_id"))
				if err := CoreWithdraw(ctx, svc, deliberationID, agentID, keyID); err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, map[string]string{"status": "agent withdrawn"})

			default:
				writeA2AError(w, req.ID, -32602, fmt.Sprintf("unknown action %q for gemot/participate", action))
			}

		case "gemot/analyze":
			s := req.Params
			switch action {
			case "run":
				deliberationID := str(s, "deliberation_id")
				// Existence + access + quorum BEFORE deducting credits.
				// Single service-layer precondition function shared with
				// MCP — a guard added on one transport applies to both.
				if err := svc.CheckAnalysisPreconditions(ctx, deliberationID, keyID, true); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				creditCost, err := deductCredits(str(s, "model"))
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				RunAnalysisAsync(svc, jobDB, creditStore, deliberationID, str(s, "model"), keyID, creditCost)
				writeA2AResult(w, req.ID, map[string]string{
					"status":          "analysis started",
					"deliberation_id": deliberationID,
					"poll":            "Call gemot/deliberation action:get to check progress",
				})

			case "get_result":
				var round *int
				if v, ok := s["round"]; ok {
					if f, ok := v.(float64); ok {
						r := int(f)
						round = &r
					}
				}
				deliberationID := str(s, "deliberation_id")
				// round:-1 returns all rounds
				if round != nil && *round == -1 {
					results, err := CoreGetAllAnalysisResults(ctx, svc, deliberationID, keyID)
					if err != nil {
						writeA2AError(w, req.ID, -32000, sanitizeError(err))
						return
					}
					if len(results) == 0 {
						status, err := CoreGetAnalysisStatus(ctx, svc, deliberationID)
						if err != nil {
							writeA2AError(w, req.ID, -32000, sanitizeError(err))
							return
						}
						writeA2AResult(w, req.ID, status)
						return
					}
					writeA2AResult(w, req.ID, results)
					return
				}
				result, err := CoreGetAnalysisResult(ctx, svc, deliberationID, keyID, round)
				if err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				if result == nil {
					status, err := CoreGetAnalysisStatus(ctx, svc, deliberationID)
					if err != nil {
						writeA2AError(w, req.ID, -32000, sanitizeError(err))
						return
					}
					writeA2AResult(w, req.ID, status)
					return
				}
				writeA2AResult(w, req.ID, result)

			case "update_result":
				deliberationID := str(s, "deliberation_id")
				if err := checkAccess(deliberationID); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				resultJSON := str(s, "result_json")
				if resultJSON == "" {
					writeA2AError(w, req.ID, -32602, "result_json is required")
					return
				}
				roundVal := 0
				if v, ok := s["round"]; ok {
					if f, ok := v.(float64); ok {
						roundVal = int(f)
					}
				}
				if roundVal == 0 {
					writeA2AError(w, req.ID, -32602, "round is required")
					return
				}
				var updated deliberation.AnalysisResult
				if err := json.Unmarshal([]byte(resultJSON), &updated); err != nil {
					writeA2AError(w, req.ID, -32602, fmt.Sprintf("invalid result_json: %v", err))
					return
				}
				if err := svc.SaveAnalysisResult(ctx, deliberationID, roundVal, &updated); err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, map[string]string{"status": fmt.Sprintf("analysis result updated for round %d", roundVal)})

			case "cancel":
				deliberationID := str(s, "deliberation_id")
				if err := CoreCancelAnalysis(ctx, svc, deliberationID, keyID); err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, map[string]string{"status": "analysis cancelled"})

			case "propose_compromise":
				// Existence + access (no quorum on secondary action)
				// BEFORE deducting credits. Shared precondition with MCP.
				if err := svc.CheckAnalysisPreconditions(ctx, str(s, "deliberation_id"), keyID, false); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				creditCost, err := deductCredits(str(s, "model"))
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				proposal, err := svc.ProposeCompromise(ctx, str(s, "deliberation_id"))
				if err != nil {
					refundCredits(creditCost)
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				writeA2AResult(w, req.ID, map[string]string{"compromise_proposal": proposal})

			case "reframe":
				result, err := CoreReframe(ctx, svc, creditStore, str(s, "deliberation_id"), str(s, "position_id"), str(s, "model"), keyID, isAdmin, token)
				if err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, result)

			case "challenge":
				result, err := CoreChallengeAnalysis(ctx, svc, str(s, "deliberation_id"), scope(str(s, "agent_id")), str(s, "reason"), keyID)
				if err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, result)

			case "dispute_crux":
				if err := checkAccess(str(s, "deliberation_id")); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				agentID := scope(str(s, "agent_id"))
				d, err := svc.DisputeCrux(ctx, str(s, "deliberation_id"), agentID, str(s, "crux_claim"), str(s, "correction"))
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				writeA2AResult(w, req.ID, d)

			case "expert_panel":
				model := str(s, "model")
				creditCost, err := deductCredits(model)
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				result, err := CoreRunExpertPanel(ctx, svc, str(s, "document"), str(s, "topic"), str(s, "experts"), str(s, "group_id"), model, keyID, str(s, "source_type"), str(s, "depth"))
				if err != nil {
					// Refund must use the full gmt_ token — AddCredits looks up
					// by `WHERE key = $2` and silently no-ops on the 8-char keyID.
					if creditCost > 0 && creditStore != nil && strings.HasPrefix(token, "gmt_") {
						_, _ = creditStore.AddCredits(token, creditCost)
					}
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				RunAnalysisAsync(svc, jobDB, creditStore, result.DeliberationID, result.Model, keyID, creditCost)
				writeA2AResult(w, req.ID, result)

			case "follow_up":
				// Existence + access BEFORE deducting credits. Previously
				// follow_up on A2A skipped both — a customer key (or
				// guessed UUID) could trigger a paid follow_up against a
				// deliberation they had no access to. Shared precondition
				// with MCP closes the drift.
				if err := svc.CheckAnalysisPreconditions(ctx, str(s, "deliberation_id"), keyID, false); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				model := str(s, "model")
				creditCost, err := deductCredits(model)
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				result, err := CoreFollowUpExpertPanel(ctx, svc, str(s, "deliberation_id"), model, keyID)
				if err != nil {
					// Refund must use the full gmt_ token — AddCredits looks up
					// by `WHERE key = $2` and silently no-ops on the 8-char keyID.
					if creditCost > 0 && creditStore != nil && strings.HasPrefix(token, "gmt_") {
						_, _ = creditStore.AddCredits(token, creditCost)
					}
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				RunAnalysisAsync(svc, jobDB, creditStore, result.DeliberationID, result.Model, keyID, creditCost)
				writeA2AResult(w, req.ID, result)

			default:
				writeA2AError(w, req.ID, -32602, fmt.Sprintf("unknown action %q for gemot/analyze", action))
			}

		case "gemot/decide":
			s := req.Params
			switch action {
			case "commit":
				if err := checkAccess(str(s, "deliberation_id")); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				agentID := scope(str(s, "agent_id"))
				c, err := svc.Commit(ctx, str(s, "deliberation_id"), agentID, str(s, "statement"), str(s, "conditional"))
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				writeA2AResult(w, req.ID, c)

			case "get_commitments":
				result, err := CoreGetCommitments(ctx, svc, str(s, "deliberation_id"), keyID)
				if err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, result)

			case "fulfill":
				verifiedBy := str(s, "verified_by")
				if verifiedBy == "" {
					verifiedBy = keyID
				}
				if err := CoreFulfillCommitment(ctx, svc, str(s, "commitment_id"), verifiedBy); err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, map[string]string{"status": "commitment fulfilled"})

			case "break":
				verifiedBy := str(s, "verified_by")
				if verifiedBy == "" {
					verifiedBy = keyID
				}
				if err := CoreBreakCommitment(ctx, svc, str(s, "commitment_id"), str(s, "reason"), verifiedBy); err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, map[string]string{"status": "commitment broken"})

			case "reputation":
				rep, err := CoreAgentReputation(ctx, svc, str(s, "agent_id"), str(s, "group_id"))
				if err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, rep)

			default:
				writeA2AError(w, req.ID, -32602, fmt.Sprintf("unknown action %q for gemot/decide", action))
			}

		case "gemot/coordinate":
			s := req.Params
			switch action {
			case "delegate":
				if err := checkAccess(str(s, "deliberation_id")); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				from := scope(str(s, "from_agent"))
				to := scope(str(s, "to_agent"))
				d, err := svc.Delegate(ctx, str(s, "deliberation_id"), from, to, str(s, "scope"))
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				writeA2AResult(w, req.ID, d)

			case "invite":
				if err := checkAccess(str(s, "deliberation_id")); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				invitedBy := scope(str(s, "invited_by"))
				inv, err := svc.InviteAgent(ctx, str(s, "deliberation_id"), invitedBy, str(s, "invited_agent"), str(s, "role"), str(s, "reason"))
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				writeA2AResult(w, req.ID, inv)

			case "generate_join_code":
				deliberationID := str(s, "deliberation_id")
				if err := checkAccess(deliberationID); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				role := str(s, "role")
				if role == "" {
					role = "contributor"
				}
				ttl := 24 * time.Hour
				if v, ok := s["ttl_hours"]; ok {
					if f, ok := v.(float64); ok && f > 0 {
						ttl = time.Duration(f) * time.Hour
					}
				}
				jc, err := svc.GenerateJoinCode(ctx, deliberationID, role, ttl)
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				writeA2AResult(w, req.ID, map[string]any{
					"code":            jc.Code,
					"deliberation_id": jc.DeliberationID,
					"role":            jc.Role,
					"expires_at":      jc.ExpiresAt.Format(time.RFC3339),
					"join_url":        "https://gemot.dev/join/" + jc.Code,
				})

			case "join":
				code := str(s, "code")
				agentID := scope(str(s, "agent_id"))
				if code == "" || agentID == "" {
					writeA2AError(w, req.ID, -32602, "code and agent_id are required")
					return
				}
				deliberationID, role, err := svc.JoinDeliberation(ctx, code, agentID)
				if err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				writeA2AResult(w, req.ID, map[string]string{
					"deliberation_id": deliberationID,
					"role":            role,
					"agent_id":        agentID,
					"status":          "joined",
				})

			default:
				writeA2AError(w, req.ID, -32602, fmt.Sprintf("unknown action %q for gemot/coordinate", action))
			}

		case "gemot/admin":
			s := req.Params
			switch action {
			case "report_abuse":
				deliberationID := str(s, "deliberation_id")
				reason := str(s, "reason")
				if deliberationID == "" || reason == "" {
					writeA2AError(w, req.ID, -32602, "deliberation_id and reason are required")
					return
				}
				if err := svc.ReportAbuse(ctx, deliberationID, keyID, reason); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				writeA2AResult(w, req.ID, map[string]string{"status": "report filed"})

			case "get_audit_log":
				deliberationID := str(s, "deliberation_id")
				if deliberationID == "" {
					writeA2AError(w, req.ID, -32602, "deliberation_id is required")
					return
				}
				if err := checkAccess(deliberationID); err != nil {
					writeA2AError(w, req.ID, -32000, err.Error())
					return
				}
				var opLog []map[string]string
				if auditLog != nil {
					opLog, _ = auditLog.GetAuditLog(deliberationID, 50)
				}
				var analysisAudit []deliberation.AuditEntry
				if result, err := svc.GetLatestAnalysisResult(ctx, deliberationID); err == nil && result != nil {
					analysisAudit = result.AuditLog
				}
				writeA2AResult(w, req.ID, map[string]any{
					"operations":         opLog,
					"analysis_decisions": analysisAudit,
				})

			case "list_templates":
				writeA2AResult(w, req.ID, deliberation.ListTemplates())

			case "get_votes":
				votes, err := CoreGetVotes(ctx, svc, str(s, "deliberation_id"), keyID)
				if err != nil {
					writeA2AError(w, req.ID, -32000, sanitizeError(err))
					return
				}
				writeA2AResult(w, req.ID, votes)

			default:
				writeA2AError(w, req.ID, -32602, fmt.Sprintf("unknown action %q for gemot/admin", action))
			}

		default:
			writeA2AError(w, req.ID, -32601,
				fmt.Sprintf("Method not found: %s. Available methods: %s", req.Method, strings.Join(a2aMethods, ", ")))
		}
	}
}

// str extracts a string param from a JSON-RPC params map.
func str(params map[string]any, key string) string {
	if v, ok := params[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func writeA2AResult(w http.ResponseWriter, id any, result any) {
	json.NewEncoder(w).Encode(A2AResponse{ //nolint:errcheck
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func writeA2AError(w http.ResponseWriter, id any, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(A2AResponse{ //nolint:errcheck
		JSONRPC: "2.0",
		ID:      id,
		Error: map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

// parseA2ACredential converts an A2A principal_credential object into the
// stored credential encoding. Mirrors principalCredentialParam on the MCP side;
// A2A params arrive as untyped maps, so the fields are pulled out by hand.
func parseA2ACredential(v any) ([]byte, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("principal_credential must be an object")
	}
	expires, err := time.Parse(time.RFC3339, str(m, "expires_at"))
	if err != nil {
		return nil, fmt.Errorf("principal_credential.expires_at must be RFC3339: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(str(m, "signature"))
	if err != nil {
		return nil, fmt.Errorf("principal_credential.signature must be base64-encoded: %w", err)
	}
	return json.Marshal(principal.Credential{
		Principal: str(m, "principal"),
		Agent:     str(m, "agent"),
		Scope:     str(m, "scope"),
		Issuer:    str(m, "issuer"),
		ExpiresAt: expires,
		Signature: sig,
	})
}
