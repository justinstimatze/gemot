package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
)

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
func A2AHandler(svc *deliberation.Service, creditStore *payments.CreditStore, apiSecret string, rateLimiter *payments.RateLimiter) http.HandlerFunc {
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
		isAdmin := apiSecret != "" && token == apiSecret
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
				"description": "Structured deliberation for AI agent coordination. 19 tools.",
				"version":     Version,
				"url":         "https://gemot.dev",
				"docs":        "https://gemot.dev/docs",
				"tools":       19,
			})

		case "gemot/create_deliberation":
			var dopts []deliberation.DeliberationOption
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
				writeA2AError(w, req.ID, -32000, "content is required")
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
			if d, ok := req.Params["draft"]; ok {
				if b, ok := d.(bool); ok && b {
					popts = append(popts, deliberation.WithDraft())
				}
			}
			p, err := svc.SubmitPosition(str("deliberation_id"), agentID, content, popts...)
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
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
			go func() {
				_, err := svc.Analyze(context.Background(), deliberationID)
				if err != nil {
					refundCredits(creditCost)
				}
			}()
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
				writeA2AError(w, req.ID, -32000, err.Error())
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
				writeA2AError(w, req.ID, -32000, err.Error())
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
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, actx)

		case "gemot/list_deliberations":
			allDelibs, err := svc.ListDeliberations()
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			var deliberations []deliberation.Deliberation
			for _, d := range allDelibs {
				if d.Visibility == "private" && d.CreatorKey != keyID && !isAdmin {
					continue
				}
				deliberations = append(deliberations, d)
			}
			writeA2AResult(w, req.ID, deliberations)

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
			agentID := scope(str("agent_id"))
			d, err := svc.DisputeCrux(str("deliberation_id"), agentID, str("crux_claim"), str("correction"))
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, d)

		case "gemot/commit":
			agentID := scope(str("agent_id"))
			c, err := svc.Commit(str("deliberation_id"), agentID, str("statement"), str("conditional"))
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, c)

		case "gemot/invite_agent":
			invitedBy := scope(str("invited_by"))
			inv, err := svc.InviteAgent(str("deliberation_id"), invitedBy, str("invited_agent"), str("role"), str("reason"))
			if err != nil {
				writeA2AError(w, req.ID, -32000, err.Error())
				return
			}
			writeA2AResult(w, req.ID, inv)

		case "gemot/delegate":
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
				writeA2AError(w, req.ID, -32000, "code and agent_id are required")
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

		default:
			writeA2AError(w, req.ID, -32601,
				fmt.Sprintf("Method not found: %s. Available methods: agent/info, gemot/create_deliberation, gemot/submit_position, gemot/vote, gemot/analyze, gemot/get_deliberation, gemot/get_positions, gemot/get_context, gemot/list_deliberations, gemot/propose_compromise, gemot/dispute_crux, gemot/commit, gemot/invite_agent, gemot/delegate, gemot/generate_join_code, gemot/join_deliberation", req.Method))
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
