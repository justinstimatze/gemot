package mcp

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
)

// sanitizeError maps known internal errors to user-friendly messages.
func sanitizeError(err error) string {
	msg := err.Error()
	if errors.Is(err, sql.ErrNoRows) || strings.Contains(msg, "no rows") {
		return "not found"
	}
	// Don't leak internal DB errors
	if strings.Contains(msg, "SQLITE") || strings.Contains(msg, "pq:") || strings.Contains(msg, "database") ||
		strings.Contains(msg, "disk") || strings.Contains(msg, "locked") || strings.Contains(msg, "pgx") {
		log.Printf("[gemot] internal error (sanitized): %v", err)
		return "internal error"
	}
	return msg
}

// a2aMethods is the canonical list of supported A2A methods.
var a2aMethods = []string{
	"agent/info",
	"gemot/create_deliberation",
	"gemot/submit_position",
	"gemot/vote",
	"gemot/analyze",
	"gemot/get_deliberation",
	"gemot/get_positions",
	"gemot/get_context",
	"gemot/list_deliberations",
	"gemot/list_by_group",
	"gemot/list_by_agent",
	"gemot/set_group",
	"gemot/propose_compromise",
	"gemot/dispute_crux",
	"gemot/commit",
	"gemot/invite_agent",
	"gemot/delegate",
	"gemot/generate_join_code",
	"gemot/join_deliberation",
	"gemot/list_templates",
	"gemot/set_template",
	"gemot/delete_deliberation",
	"gemot/report_abuse",
	"gemot/get_audit_log",
	"gemot/get_analysis_result",
	"gemot/get_votes",
	"gemot/get_commitments",
	"gemot/publish_position",
	"gemot/challenge_analysis",
	"gemot/reframe",
	"gemot/fulfill_commitment",
	"gemot/break_commitment",
	"gemot/agent_reputation",
	"gemot/create_share",
	"gemot/lookup_share",
	"gemot/cancel_analysis",
	"gemot/withdraw",
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

// A2AHandler provides a JSON-RPC 2.0 endpoint translating A2A task messages
// into gemot service calls. Non-MCP agents can use gemot via this endpoint.
// AuditStore logs write operations and provides audit queries.
type AuditStore interface {
	LogAuditEvent(keyID, ip, method, deliberationID, agentID string)
	GetAuditLog(deliberationID string, limit int) ([]map[string]string, error)
}

func A2AHandler(svc *deliberation.Service, creditStore *payments.CreditStore, apiSecret string, rateLimiter *payments.RateLimiter, auditLog AuditStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}

		// Auth: require bearer token
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeA2AError(w, nil, -32000, "Authorization: Bearer <api_key> required")
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")

		// Validate token
		isAdmin := apiSecret != "" && subtle.ConstantTimeCompare([]byte(token), []byte(apiSecret)) == 1
		var keyID string
		if !isAdmin {
			if creditStore == nil || !strings.HasPrefix(token, "gmt_") {
				writeA2AError(w, nil, -32000, "Invalid API key")
				return
			}
			if valid, _ := creditStore.ValidateKey(token); !valid {
				writeA2AError(w, nil, -32000, "Invalid or expired API key")
				return
			}
			keyID = payments.KeyID(token)
		}

		// Rate limit A2A requests
		if !isAdmin && keyID != "" {
			if !rateLimiter.Allow(keyID) {
				writeA2AError(w, nil, -32000, "rate limit exceeded — max 30 requests per minute")
				return
			}
		}

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
			return svc.CheckAccess(deliberationID, keyID)
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
		if auditLog != nil {
			writeOps := map[string]bool{
				"gemot/create_deliberation": true, "gemot/submit_position": true,
				"gemot/vote": true, "gemot/analyze": true, "gemot/commit": true,
				"gemot/propose_compromise": true, "gemot/delegate": true,
				"gemot/invite_agent": true, "gemot/dispute_crux": true,
				"gemot/delete_deliberation": true, "gemot/report_abuse": true,
				"gemot/set_template": true, "gemot/generate_join_code": true,
				"gemot/join_deliberation": true, "gemot/cancel_analysis": true,
				"gemot/withdraw": true,
			}
			if writeOps[req.Method] {
				ip := ClientIP(r)
				did, _ := req.Params["deliberation_id"].(string)
				aid, _ := req.Params["agent_id"].(string)
				auditLog.LogAuditEvent(keyID, strings.TrimSpace(ip), req.Method, did, aid)
			}
		}

		// Helper to get string param
		str := func(key string) string {
			if v, ok := req.Params[key]; ok {
				if s, ok := v.(string); ok {
					return s
				}
			}
			return ""
		}
		// Helper to scope agent IDs
		scope := func(agentID string) string {
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
				"tools":       len(a2aMethods) - 1, // exclude agent/info itself
			})

		case "gemot/create_deliberation":
			var dopts []deliberation.DeliberationOption
			// Template first — explicit options below override its defaults
			if t := str("template"); t != "" {
				dopts = append(dopts, deliberation.WithTemplate(t))
			}
			if t := str("type"); t != "" {
				dopts = append(dopts, deliberation.WithType(t))
			}
			if v := str("visibility"); v != "" {
				dopts = append(dopts, deliberation.WithVisibility(v))
			}
			if mp, ok := req.Params["max_participants"]; ok {
				if f, ok := mp.(float64); ok && f > 0 {
					dopts = append(dopts, deliberation.WithMaxParticipants(int(f)))
				}
			}
			if rules, ok := req.Params["rules"]; ok {
				if rulesMap, ok := rules.(map[string]any); ok {
					dopts = append(dopts, deliberation.WithRules(rulesMap))
				}
			}
			if g := str("group_id"); g != "" {
				dopts = append(dopts, deliberation.WithGroupID(g))
			}
			if dm, ok := req.Params["deadline_minutes"]; ok {
				if f, ok := dm.(float64); ok && f > 0 {
					deadline := time.Now().Add(time.Duration(f) * time.Minute)
					dopts = append(dopts, deliberation.WithDeadline(deadline))
				}
			}
			if keyID != "" {
				dopts = append(dopts, deliberation.WithCreatorKey(keyID))
			}
			d, err := svc.CreateDeliberation(str("topic"), str("description"), dopts...)
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, d)

		case "gemot/submit_position":
			if err := checkAccess(str("deliberation_id")); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			agentID := scope(str("agent_id"))
			content := str("content")
			if content == "" {
				writeA2AError(w, req.ID, -32602, "content is required")
				return
			}
			var popts []deliberation.PositionOption
			if mf := str("model_family"); mf != "" {
				popts = append(popts, deliberation.WithModelFamily(mf))
			}
			if g := str("group"); g != "" {
				popts = append(popts, deliberation.WithGroup(g))
			}
			if cv, ok := req.Params["conviction"]; ok {
				if f, ok := cv.(float64); ok && f > 0 {
					popts = append(popts, deliberation.WithConviction(f))
				}
			}
			if r := str("reservation"); r != "" {
				popts = append(popts, deliberation.WithReservation(r))
			}
			if ob := str("on_behalf_of"); ob != "" {
				popts = append(popts, deliberation.WithOnBehalfOf(ob))
			}
			if interests := str("interests"); interests != "" {
				popts = append(popts, deliberation.WithInterests(interests))
			}
			isDraft := false
			if d, ok := req.Params["draft"]; ok {
				if b, ok := d.(bool); ok && b {
					isDraft = true
					popts = append(popts, deliberation.WithDraft())
				}
			}
			// Check position cost (deduction after successful submission)
			var posCost int
			if !isDraft && !isAdmin {
				if dd, err := svc.GetDeliberation(str("deliberation_id")); err == nil {
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
			p, err := svc.SubmitPosition(str("deliberation_id"), agentID, content, popts...)
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			// Deduct after success
			if posCost > 0 && creditStore != nil && token != "" && strings.HasPrefix(token, "gmt_") {
				creditStore.Deduct(token, posCost) //nolint:errcheck
			}
			writeA2AResult(w, req.ID, p)

		case "gemot/vote":
			if err := checkAccess(str("deliberation_id")); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			agentID := scope(str("agent_id"))
			value := 0
			if v, ok := req.Params["value"]; ok {
				if f, ok := v.(float64); ok {
					value = int(f)
				}
			}
			err := svc.Vote(str("deliberation_id"), agentID, str("position_id"), value)
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, map[string]string{"status": "vote recorded"})

		case "gemot/analyze":
			deliberationID := str("deliberation_id")
			if err := checkAccess(deliberationID); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			creditCost, err := deductCredits(str("model"))
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			RunAnalysisAsync(svc, nil, creditStore, deliberationID, str("model"), keyID, creditCost)
			writeA2AResult(w, req.ID, map[string]string{
				"status":          "analysis started",
				"deliberation_id": deliberationID,
				"poll":            "Call gemot/get_deliberation to check progress",
			})

		case "gemot/get_deliberation":
			if err := checkAccess(str("deliberation_id")); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			d, err := svc.GetDeliberation(str("deliberation_id"))
			if err != nil {
				writeA2AError(w, req.ID, -32603, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, d)

		case "gemot/get_positions":
			if err := checkAccess(str("deliberation_id")); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			positions, err := svc.GetPositions(str("deliberation_id"), nil, nil)
			if err != nil {
				writeA2AError(w, req.ID, -32603, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, positions)

		case "gemot/get_context":
			if err := checkAccess(str("deliberation_id")); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			agentID := scope(str("agent_id"))
			actx, err := svc.GetContext(str("deliberation_id"), agentID)
			if err != nil {
				writeA2AError(w, req.ID, -32603, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, actx)

		case "gemot/list_deliberations":
			var pgLimit, pgOffset int
			if v, ok := req.Params["limit"].(float64); ok {
				pgLimit = int(v)
			}
			if v, ok := req.Params["offset"].(float64); ok {
				pgOffset = int(v)
			}
			allDelibs, err := svc.ListDeliberations(pgLimit, pgOffset)
			if err != nil {
				writeA2AError(w, req.ID, -32603, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, filterVisible(allDelibs, keyID, isAdmin))

		case "gemot/propose_compromise":
			if err := checkAccess(str("deliberation_id")); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			creditCost, err := deductCredits(str("model"))
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			proposal, err := svc.ProposeCompromise(context.Background(), str("deliberation_id"))
			if err != nil {
				refundCredits(creditCost)
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, map[string]string{"compromise_proposal": proposal})

		case "gemot/dispute_crux":
			if err := checkAccess(str("deliberation_id")); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			agentID := scope(str("agent_id"))
			d, err := svc.DisputeCrux(str("deliberation_id"), agentID, str("crux_claim"), str("correction"))
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, d)

		case "gemot/commit":
			if err := checkAccess(str("deliberation_id")); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			agentID := scope(str("agent_id"))
			c, err := svc.Commit(str("deliberation_id"), agentID, str("statement"), str("conditional"))
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, c)

		case "gemot/invite_agent":
			if err := checkAccess(str("deliberation_id")); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			invitedBy := scope(str("invited_by"))
			inv, err := svc.InviteAgent(str("deliberation_id"), invitedBy, str("invited_agent"), str("role"), str("reason"))
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, inv)

		case "gemot/delegate":
			if err := checkAccess(str("deliberation_id")); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			from := scope(str("from_agent"))
			to := scope(str("to_agent"))
			d, err := svc.Delegate(str("deliberation_id"), from, to, str("scope"))
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, d)

		case "gemot/generate_join_code":
			deliberationID := str("deliberation_id")
			if err := checkAccess(deliberationID); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			role := str("role")
			if role == "" {
				role = "contributor"
			}
			ttl := 24 * time.Hour // default 24h
			if v, ok := req.Params["ttl_hours"]; ok {
				if f, ok := v.(float64); ok && f > 0 {
					ttl = time.Duration(f) * time.Hour
				}
			}
			jc, err := svc.GenerateJoinCode(deliberationID, role, ttl)
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

		case "gemot/join_deliberation":
			code := str("code")
			agentID := scope(str("agent_id"))
			if code == "" || agentID == "" {
				writeA2AError(w, req.ID, -32602, "code and agent_id are required")
				return
			}
			deliberationID, role, err := svc.JoinDeliberation(code, agentID)
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

		case "gemot/get_analysis_result":
			result, err := CoreGetAnalysisResult(svc, str("deliberation_id"), keyID)
			if err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, result)

		case "gemot/get_votes":
			votes, err := CoreGetVotes(svc, str("deliberation_id"), keyID)
			if err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, votes)

		case "gemot/get_audit_log":
			deliberationID := str("deliberation_id")
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
			if result, err := svc.GetLatestAnalysisResult(deliberationID); err == nil && result != nil {
				analysisAudit = result.AuditLog
			}
			writeA2AResult(w, req.ID, map[string]any{
				"operations":         opLog,
				"analysis_decisions": analysisAudit,
			})

		case "gemot/list_templates":
			writeA2AResult(w, req.ID, deliberation.ListTemplates())

		case "gemot/delete_deliberation":
			deliberationID := str("deliberation_id")
			if deliberationID == "" {
				writeA2AError(w, req.ID, -32602, "deliberation_id is required")
				return
			}
			if err := svc.DeleteDeliberation(deliberationID, keyID, isAdmin); err != nil {
				writeA2AError(w, req.ID, -32603, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, map[string]string{"status": "deleted"})

		case "gemot/report_abuse":
			deliberationID := str("deliberation_id")
			reason := str("reason")
			if deliberationID == "" || reason == "" {
				writeA2AError(w, req.ID, -32602, "deliberation_id and reason are required")
				return
			}
			if err := svc.ReportAbuse(deliberationID, keyID, reason); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, map[string]string{"status": "report filed"})

		case "gemot/set_template":
			deliberationID := str("deliberation_id")
			template := str("template")
			if deliberationID == "" || template == "" {
				writeA2AError(w, req.ID, -32602, "deliberation_id and template are required")
				return
			}
			if err := svc.SetTemplate(deliberationID, template, keyID); err != nil {
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

		case "gemot/set_group":
			if !isAdmin {
				writeA2AError(w, req.ID, -32000, "admin only")
				return
			}
			deliberationID := str("deliberation_id")
			groupID := str("group_id")
			if deliberationID == "" || groupID == "" {
				writeA2AError(w, req.ID, -32602, "deliberation_id and group_id are required")
				return
			}
			if err := svc.SetGroupID(deliberationID, groupID); err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, map[string]string{"status": "updated", "group_id": groupID})

		case "gemot/create_share":
			if !isAdmin {
				writeA2AError(w, req.ID, -32000, "admin only")
				return
			}
			groupID := str("group_id")
			if groupID == "" {
				writeA2AError(w, req.ID, -32602, "group_id is required")
				return
			}
			shareToken, err := svc.CreateShareToken(groupID)
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, map[string]string{
				"token":    shareToken,
				"group_id": groupID,
			})

		case "gemot/lookup_share":
			shareToken := str("token")
			if shareToken == "" {
				writeA2AError(w, req.ID, -32602, "token is required")
				return
			}
			groupID, err := svc.LookupShareToken(shareToken)
			if err != nil {
				writeA2AError(w, req.ID, -32603, sanitizeError(err))
				return
			}
			delibs, err := CoreListByGroup(svc, groupID, keyID, isAdmin, 0, 0)
			if err != nil {
				writeA2AError(w, req.ID, -32603, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, map[string]any{
				"group_id":      groupID,
				"deliberations": delibs,
			})

		case "gemot/get_commitments":
			result, err := CoreGetCommitments(svc, str("deliberation_id"), keyID)
			if err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, result)

		case "gemot/publish_position":
			if err := CorePublishPosition(svc, str("position_id"), keyID); err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, map[string]string{"status": "position published"})

		case "gemot/challenge_analysis":
			result, err := CoreChallengeAnalysis(svc, str("deliberation_id"), scope(str("agent_id")), str("reason"), keyID)
			if err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, result)

		case "gemot/reframe":
			result, err := CoreReframe(svc, creditStore, str("deliberation_id"), str("position_id"), str("model"), keyID, isAdmin, token)
			if err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, result)

		case "gemot/fulfill_commitment":
			verifiedBy := str("verified_by")
			if verifiedBy == "" {
				verifiedBy = keyID
			}
			if err := CoreFulfillCommitment(svc, str("commitment_id"), verifiedBy); err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, map[string]string{"status": "commitment fulfilled"})

		case "gemot/break_commitment":
			verifiedBy := str("verified_by")
			if verifiedBy == "" {
				verifiedBy = keyID
			}
			if err := CoreBreakCommitment(svc, str("commitment_id"), str("reason"), verifiedBy); err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, map[string]string{"status": "commitment broken"})

		case "gemot/agent_reputation":
			rep, err := CoreAgentReputation(svc, str("agent_id"), str("group_id"))
			if err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, rep)

		case "gemot/list_by_group":
			var pgLimit, pgOffset int
			if v, ok := req.Params["limit"].(float64); ok {
				pgLimit = int(v)
			}
			if v, ok := req.Params["offset"].(float64); ok {
				pgOffset = int(v)
			}
			delibs, err := CoreListByGroup(svc, str("group_id"), keyID, isAdmin, pgLimit, pgOffset)
			if err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, delibs)

		case "gemot/list_by_agent":
			var pgLimit, pgOffset int
			if v, ok := req.Params["limit"].(float64); ok {
				pgLimit = int(v)
			}
			if v, ok := req.Params["offset"].(float64); ok {
				pgOffset = int(v)
			}
			delibs, err := CoreListByAgent(svc, str("agent_id"), keyID, isAdmin, pgLimit, pgOffset)
			if err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, delibs)

		case "gemot/cancel_analysis":
			deliberationID := str("deliberation_id")
			if err := CoreCancelAnalysis(svc, deliberationID, keyID); err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, map[string]string{"status": "analysis cancelled"})

		case "gemot/withdraw":
			deliberationID := str("deliberation_id")
			agentID := scope(str("agent_id"))
			if err := CoreWithdraw(svc, deliberationID, agentID, keyID); err != nil {
				writeA2AError(w, req.ID, -32000, sanitizeError(err))
				return
			}
			writeA2AResult(w, req.ID, map[string]string{"status": "agent withdrawn"})

		default:
			writeA2AError(w, req.ID, -32601,
				fmt.Sprintf("Method not found: %s. Available methods: %s", req.Method, strings.Join(a2aMethods, ", ")))
		}
	}
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
