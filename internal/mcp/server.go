package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand"
	"os"
	"strings"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
	"github.com/justinstimatze/gemot/internal/payments"
	"github.com/justinstimatze/gemot/internal/store"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var logWriter io.Writer = os.Stderr

type server struct {
	svc      *deliberation.Service
	credits  *payments.CreditStore
	db       *store.DB
	shutdown context.Context // server lifetime context — cancelled on shutdown
}

// RunAnalysisAsync starts an analysis in a background goroutine with proper
// context management, credit refunding on failure, and job tracking.
// Shared between MCP and A2A handlers to avoid divergent code paths.
func RunAnalysisAsync(svc *deliberation.Service, db *store.DB, credits *payments.CreditStore, deliberationID, model, apiKey string, creditCost int) {
	analyzeCtx, analyzeCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	if model != "" {
		analyzeCtx = context.WithValue(analyzeCtx, llm.ContextKeyModel{}, model)
	}

	// Create persistent job if DB available
	var jobID string
	if db != nil {
		job := &store.Job{
			DeliberationID: deliberationID,
			Model:          model,
			APIKey:         apiKey,
			CreditCost:     creditCost,
		}
		if err := db.CreateJob(job); err != nil {
			slog.Error("failed to create job", "error", err)
		} else {
			jobID = job.ID
		}
	}

	go func() {
		defer analyzeCancel()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("PANIC in analysis", "deliberation_id", deliberationID, "panic", r)
				// Reset status so deliberation isn't stuck in "analyzing"
				svc.ResetAnalyzingStatus(deliberationID)
				if apiKey != "" && creditCost > 0 && credits != nil {
					_, _ = credits.AddCredits(apiKey, creditCost)
				}
				if db != nil && jobID != "" {
					_ = db.CompleteJob(jobID, "failed", fmt.Sprintf("panic: %v", r))
				}
			}
		}()
		result, err := svc.Analyze(analyzeCtx, deliberationID)
		if err != nil {
			if apiKey != "" && creditCost > 0 && credits != nil {
				_, _ = credits.AddCredits(apiKey, creditCost)
			}
			if db != nil && jobID != "" {
				if jerr := db.CompleteJob(jobID, "failed", err.Error()); jerr != nil {
					slog.Error("failed to mark job failed", "error", jerr)
				}
			}
			slog.Error("async analysis failed", "deliberation_id", deliberationID, "error", err)
			return
		}
		if db != nil && jobID != "" {
			if jerr := db.CompleteJob(jobID, "completed", ""); jerr != nil {
				slog.Error("failed to mark job completed", "error", jerr)
			}
		}
		slog.Info("async analysis complete", "deliberation_id", deliberationID, "cruxes", len(result.Cruxes), "clusters", len(result.Clusters))
	}()
}

// audit logs a write operation from an MCP tool handler.
func (s *server) audit(ctx context.Context, method, deliberationID, agentID string) {
	if s.db == nil {
		return
	}
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	s.db.LogAuditEvent(keyID, "mcp", method, deliberationID, agentID)
}

// Version is the current gemot release version.
const Version = "0.8.0"

// newServer creates an MCP server with 6 grouped tools.
func newServer(s *server) *sdkmcp.Server {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "gemot",
		Version: Version,
	}, nil)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: "deliberation",
		Description: `Manage deliberations. Actions:
- create: Create a new deliberation (topic, description, template, group_id, deadline_minutes, rules, visibility, max_participants, type)
- get: Get status/stats of a deliberation (deliberation_id)
- list: List all deliberations (limit, offset)
- list_by_group: List deliberations in a group (group_id, limit, offset)
- list_by_agent: List deliberations an agent participated in (agent_id, limit, offset)
- delete: Soft-delete a deliberation (deliberation_id)
- set_template: Change governance template (deliberation_id, template)
- export: Export complete multi-round history (deliberation_id)`,
	}, s.handleDeliberation)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: "participate",
		Description: `Participate in a deliberation. Actions:
- submit_position: Submit your position (deliberation_id, agent_id, content; optional: model_family, group, conviction, reservation, on_behalf_of, interests, draft)
- publish_position: Publish a draft position (position_id)
- vote: Vote on a position — 1=agree, 0=pass, -1=disagree (deliberation_id, agent_id, position_id, value)
- get_positions: Get all positions (deliberation_id; optional: round, exclude_agent_id, group, shuffle)
- get_context: Get your personal context — cluster, allies, cruxes (deliberation_id, agent_id)
- withdraw: Withdraw from a deliberation (deliberation_id, agent_id)`,
	}, s.handleParticipate)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: "analyze",
		Description: `Analyze disagreements and find common ground. Actions:
- run: Trigger analysis — extracts cruxes, clusters, consensus (deliberation_id; optional: model)
- get_result: Get analysis result (deliberation_id; optional: round)
- cancel: Cancel in-progress analysis (deliberation_id)
- propose_compromise: Generate a compromise statement (deliberation_id; optional: model)
- reframe: Restate a position emphasizing common ground (deliberation_id, position_id; optional: model)
- challenge: Challenge an analysis result (deliberation_id, agent_id, reason)
- dispute_crux: Dispute a crux classification (deliberation_id, agent_id, crux_claim, correction)`,
	}, s.handleAnalyzeTool)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: "decide",
		Description: `Commitments and reputation tracking. Actions:
- commit: Commit to a deliberation outcome (deliberation_id, agent_id, statement; optional: conditional)
- get_commitments: Get all commitments (deliberation_id)
- fulfill: Mark a commitment as fulfilled (commitment_id; optional: verified_by)
- break: Mark a commitment as broken (commitment_id, reason; optional: verified_by)
- reputation: Get an agent's commitment track record (agent_id; optional: group_id)`,
	}, s.handleDecide)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: "coordinate",
		Description: `Multi-agent coordination. Actions:
- delegate: Delegate your vote to another agent (deliberation_id, from_agent, to_agent; optional: scope)
- invite: Invite an agent to join (deliberation_id, invited_by, invited_agent, reason; optional: role)
- generate_join_code: Generate a short-lived join code (deliberation_id; optional: role, ttl_minutes)
- join: Join a deliberation using a code (code, agent_id)`,
	}, s.handleCoordinate)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: "admin",
		Description: `Admin and audit tools. Actions:
- report_abuse: Report abusive content (deliberation_id, reason)
- get_audit_log: Get audit trail (deliberation_id)
- list_templates: List available governance templates
- get_votes: Get all votes (deliberation_id)`,
	}, s.handleAdmin)

	return srv
}

// Run starts the MCP server over stdio (for local agent use).
func Run(ctx context.Context, svc *deliberation.Service) error {
	s := &server{svc: svc, shutdown: ctx}
	srv := newServer(s)
	return srv.Run(ctx, &sdkmcp.StdioTransport{})
}

// --- Grouped parameter types ---

type deliberationParams struct {
	Action          string         `json:"action"`
	Topic           string         `json:"topic,omitempty"`
	Description     string         `json:"description,omitempty"`
	Type            string         `json:"type,omitempty"`
	Visibility      string         `json:"visibility,omitempty"`
	MaxParticipants int            `json:"max_participants,omitempty"`
	Template        string         `json:"template,omitempty"`
	Rules           map[string]any `json:"rules,omitempty"`
	GroupID         string         `json:"group_id,omitempty"`
	DeadlineMinutes int            `json:"deadline_minutes,omitempty"`
	DeliberationID  string         `json:"deliberation_id,omitempty"`
	AgentID         string         `json:"agent_id,omitempty"`
	Limit           int            `json:"limit,omitempty"`
	Offset          int            `json:"offset,omitempty"`
}

type participateParams struct {
	Action         string  `json:"action"`
	DeliberationID string  `json:"deliberation_id,omitempty"`
	AgentID        string  `json:"agent_id,omitempty"`
	Content        string  `json:"content,omitempty"`
	ModelFamily    string  `json:"model_family,omitempty"`
	Group          string  `json:"group,omitempty"`
	Conviction     float64 `json:"conviction,omitempty"`
	Reservation    string  `json:"reservation,omitempty"`
	OnBehalfOf     string  `json:"on_behalf_of,omitempty"`
	Interests      string  `json:"interests,omitempty"`
	Draft          bool    `json:"draft,omitempty"`
	PositionID     string  `json:"position_id,omitempty"`
	Value          any     `json:"value,omitempty"`
	CriterionID    string  `json:"criterion_id,omitempty"`
	ExcludeAgentID *string `json:"exclude_agent_id,omitempty"`
	Round          *int    `json:"round,omitempty"`
	Shuffle        *bool   `json:"shuffle,omitempty"`
}

type analyzeToolParams struct {
	Action         string `json:"action"`
	DeliberationID string `json:"deliberation_id,omitempty"`
	Model          string `json:"model,omitempty"`
	Round          *int   `json:"round,omitempty"`
	PositionID     string `json:"position_id,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	Reason         string `json:"reason,omitempty"`
	CruxClaim      string `json:"crux_claim,omitempty"`
	Correction     string `json:"correction,omitempty"`
}

type decideParams struct {
	Action         string `json:"action"`
	DeliberationID string `json:"deliberation_id,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	Statement      string `json:"statement,omitempty"`
	Conditional    string `json:"conditional,omitempty"`
	CommitmentID   string `json:"commitment_id,omitempty"`
	Reason         string `json:"reason,omitempty"`
	VerifiedBy     string `json:"verified_by,omitempty"`
	GroupID        string `json:"group_id,omitempty"`
}

type coordinateParams struct {
	Action         string `json:"action"`
	DeliberationID string `json:"deliberation_id,omitempty"`
	FromAgent      string `json:"from_agent,omitempty"`
	ToAgent        string `json:"to_agent,omitempty"`
	Scope          string `json:"scope,omitempty"`
	InvitedBy      string `json:"invited_by,omitempty"`
	InvitedAgent   string `json:"invited_agent,omitempty"`
	Role           string `json:"role,omitempty"`
	Reason         string `json:"reason,omitempty"`
	TTLMinutes     int    `json:"ttl_minutes,omitempty"`
	Code           string `json:"code,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
}

type adminParams struct {
	Action         string `json:"action"`
	DeliberationID string `json:"deliberation_id,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// --- Grouped handlers ---

func (s *server) handleDeliberation(ctx context.Context, _ *sdkmcp.CallToolRequest, args deliberationParams) (*sdkmcp.CallToolResult, any, error) {
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	isAdmin, _ := ctx.Value(payments.ContextKeyIsAdmin{}).(bool)

	switch args.Action {
	case "create":
		if args.Topic == "" {
			return errResult(fmt.Errorf("topic is required"))
		}
		var dopts []deliberation.DeliberationOption
		if args.Template != "" {
			dopts = append(dopts, deliberation.WithTemplate(args.Template))
		}
		if args.Type != "" {
			dopts = append(dopts, deliberation.WithType(args.Type))
		}
		if args.Visibility != "" {
			dopts = append(dopts, deliberation.WithVisibility(args.Visibility))
		}
		if args.MaxParticipants > 0 {
			dopts = append(dopts, deliberation.WithMaxParticipants(args.MaxParticipants))
		}
		if len(args.Rules) > 0 {
			dopts = append(dopts, deliberation.WithRules(args.Rules))
		}
		if args.GroupID != "" {
			dopts = append(dopts, deliberation.WithGroupID(args.GroupID))
		}
		if args.DeadlineMinutes > 0 {
			deadline := time.Now().Add(time.Duration(args.DeadlineMinutes) * time.Minute)
			dopts = append(dopts, deliberation.WithDeadline(deadline))
		}
		if keyID != "" {
			dopts = append(dopts, deliberation.WithCreatorKey(keyID))
		}
		d, err := s.svc.CreateDeliberation(args.Topic, args.Description, dopts...)
		if err != nil {
			return errResult(err)
		}
		s.audit(ctx, "deliberation:create", d.ID, "")
		return jsonResultWithHints(d, "Next: participate action:submit_position to add your view, or share the deliberation_id with other agents.")

	case "get":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if err := s.svc.CheckAccess(args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		d, err := s.svc.GetDeliberation(args.DeliberationID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(d)

	case "list":
		deliberations, err := s.svc.ListDeliberations(args.Limit, args.Offset, keyID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(deliberations)

	case "list_by_group":
		delibs, err := CoreListByGroup(s.svc, args.GroupID, keyID, isAdmin, args.Limit, args.Offset)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(delibs)

	case "list_by_agent":
		delibs, err := CoreListByAgent(s.svc, args.AgentID, keyID, isAdmin, args.Limit, args.Offset)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(delibs)

	case "delete":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if err := s.svc.DeleteDeliberation(args.DeliberationID, keyID, isAdmin); err != nil {
			return errResult(err)
		}
		s.audit(ctx, "deliberation:delete", args.DeliberationID, "")
		return textResult("deliberation deleted"), nil, nil

	case "set_template":
		if args.DeliberationID == "" || args.Template == "" {
			return errResult(fmt.Errorf("deliberation_id and template are required"))
		}
		if err := s.svc.SetTemplate(args.DeliberationID, args.Template, keyID); err != nil {
			return errResult(err)
		}
		tmpl, _ := deliberation.GetTemplate(args.Template)
		s.audit(ctx, "deliberation:set_template", args.DeliberationID, "")
		return jsonResultWithHints(map[string]any{
			"deliberation_id": args.DeliberationID,
			"template":        args.Template,
			"description":     tmpl.Description,
			"threshold":       tmpl.SuggestedThreshold,
		}, "Template updated. The next analysis will use this template's governance model and consensus threshold.")

	case "export":
		export, err := CoreExportDeliberation(s.svc, args.DeliberationID, keyID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(export)

	default:
		return errResult(fmt.Errorf("unknown action %q — use: create, get, list, list_by_group, list_by_agent, delete, set_template, export", args.Action))
	}
}

func (s *server) handleParticipate(ctx context.Context, _ *sdkmcp.CallToolRequest, args participateParams) (*sdkmcp.CallToolResult, any, error) {
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)

	switch args.Action {
	case "submit_position":
		if args.DeliberationID == "" || args.AgentID == "" || args.Content == "" {
			return errResult(fmt.Errorf("deliberation_id, agent_id, and content are required"))
		}
		if len(args.Content) > 65536 {
			return errResult(fmt.Errorf("content exceeds maximum length of 65536 bytes"))
		}
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		if err := s.svc.CheckAccess(args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		// Check position cost
		var posCost int
		var posApiKey string
		if !args.Draft {
			if d, err := s.svc.GetDeliberation(args.DeliberationID); err == nil {
				posCost = deliberation.RuleInt(d, "position_cost", 0)
				if posCost > 0 {
					posApiKey, _ = ctx.Value(payments.ContextKeyAPIKey{}).(string)
					if posApiKey != "" && s.credits != nil {
						balance, _ := s.credits.GetBalance(posApiKey)
						if balance < posCost {
							return errResult(fmt.Errorf("position cost: insufficient credits: have %d, need %d", balance, posCost))
						}
					}
				}
			}
		}
		var opts []deliberation.PositionOption
		if args.ModelFamily != "" {
			opts = append(opts, deliberation.WithModelFamily(args.ModelFamily))
		}
		if args.Group != "" {
			opts = append(opts, deliberation.WithGroup(args.Group))
		}
		if args.Conviction > 0 {
			opts = append(opts, deliberation.WithConviction(args.Conviction))
		}
		if args.Reservation != "" {
			opts = append(opts, deliberation.WithReservation(args.Reservation))
		}
		if args.OnBehalfOf != "" {
			opts = append(opts, deliberation.WithOnBehalfOf(args.OnBehalfOf))
		}
		if args.Interests != "" {
			opts = append(opts, deliberation.WithInterests(args.Interests))
		}
		if args.Draft {
			opts = append(opts, deliberation.WithDraft())
		}
		p, err := s.svc.SubmitPosition(args.DeliberationID, args.AgentID, args.Content, opts...)
		if err != nil {
			return errResult(err)
		}
		s.audit(ctx, "participate:submit_position", args.DeliberationID, args.AgentID)
		if posCost > 0 && posApiKey != "" && s.credits != nil {
			if _, err := s.credits.Deduct(posApiKey, posCost); err != nil {
				slog.Error("position cost deduction failed", "key_prefix", posApiKey[:8], "cost", posCost, "error", err)
			}
		}
		hint := "Next: participate action:get_positions to read others' views, then participate action:vote on them."
		if p.Draft {
			hint = "Draft saved. Next: participate action:publish_position when ready."
		}
		return jsonResultWithHints(p, hint)

	case "publish_position":
		if err := CorePublishPosition(s.svc, args.PositionID, keyID); err != nil {
			return errResult(err)
		}
		return textResult("position published"), nil, nil

	case "vote":
		if args.DeliberationID == "" || args.AgentID == "" || args.PositionID == "" {
			return errResult(fmt.Errorf("deliberation_id, agent_id, and position_id are required"))
		}
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		if err := s.svc.CheckAccess(args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		value, err := coerceVoteValue(args.Value)
		if err != nil {
			return errResult(err)
		}
		if err := s.svc.Vote(args.DeliberationID, args.AgentID, args.PositionID, value, args.CriterionID); err != nil {
			return errResult(err)
		}
		s.audit(ctx, "participate:vote", args.DeliberationID, args.AgentID)
		return textResult("vote recorded\n\n---\nNext: vote on more positions, or call analyze action:run when all votes are in."), nil, nil

	case "get_positions":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if err := s.svc.CheckAccess(args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		positions, err := s.svc.GetPositions(args.DeliberationID, args.ExcludeAgentID, args.Round)
		if err != nil {
			return errResult(err)
		}
		if args.Group != "" {
			var filtered []deliberation.Position
			for _, p := range positions {
				if p.Group == args.Group {
					filtered = append(filtered, p)
				}
			}
			positions = filtered
		}
		shouldShuffle := args.Shuffle == nil || *args.Shuffle
		if shouldShuffle && len(positions) > 1 {
			rng := mrand.New(mrand.NewSource(time.Now().UnixNano()))
			rng.Shuffle(len(positions), func(i, j int) {
				positions[i], positions[j] = positions[j], positions[i]
			})
		}
		return jsonResult(positions)

	case "get_context":
		if args.DeliberationID == "" || args.AgentID == "" {
			return errResult(fmt.Errorf("deliberation_id and agent_id are required"))
		}
		if err := s.svc.CheckAccess(args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		actx, err := s.svc.GetContext(args.DeliberationID, args.AgentID)
		if err != nil {
			return errResult(err)
		}
		hint := "Next: submit a refined position addressing the cruxes, analyze action:propose_compromise for a group statement, or analyze action:reframe your position to build bridges."
		if len(actx.RelevantCruxes) > 0 {
			hint += " Use analyze action:dispute_crux if a crux misrepresents you."
		}
		return jsonResultWithHints(actx, hint)

	case "withdraw":
		if args.DeliberationID == "" || args.AgentID == "" {
			return errResult(fmt.Errorf("deliberation_id and agent_id are required"))
		}
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		if err := CoreWithdraw(s.svc, args.DeliberationID, args.AgentID, keyID); err != nil {
			return errResult(err)
		}
		s.audit(ctx, "participate:withdraw", args.DeliberationID, args.AgentID)
		return textResult("agent withdrawn from deliberation"), nil, nil

	default:
		return errResult(fmt.Errorf("unknown action %q — use: submit_position, publish_position, vote, get_positions, get_context, withdraw", args.Action))
	}
}

func (s *server) handleAnalyzeTool(ctx context.Context, _ *sdkmcp.CallToolRequest, args analyzeToolParams) (*sdkmcp.CallToolResult, any, error) {
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)

	switch args.Action {
	case "run":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if args.Model != "" && !llm.AllowedModels[args.Model] {
			return errResult(fmt.Errorf("unsupported model %q — allowed: claude-sonnet-4-6, claude-opus-4-6, claude-haiku-4-5", args.Model))
		}
		// Sandbox users get 1 free analysis per deliberation
		if sandbox, _ := ctx.Value(payments.ContextKeySandbox{}).(bool); sandbox {
			apiKey, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)
			if apiKey == "" {
				existing, _ := s.svc.GetLatestAnalysisResult(args.DeliberationID)
				if existing != nil {
					return errResult(fmt.Errorf("sandbox deliberations get 1 free analysis — get an API key at https://gemot.dev/pricing for more"))
				}
			}
		}
		apiKey, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)
		var creditCost int
		if apiKey != "" && s.credits != nil {
			creditCost = payments.CreditCost(args.Model)
			if _, err := s.credits.Deduct(apiKey, creditCost); err != nil {
				balance, _ := s.credits.GetBalance(apiKey)
				return errResult(fmt.Errorf("insufficient credits: have %d, need %d — buy more at https://gemot.dev/pricing", balance, creditCost))
			}
		}
		if _, err := s.svc.GetDeliberation(args.DeliberationID); err != nil {
			if creditCost > 0 && s.credits != nil {
				_, _ = s.credits.AddCredits(apiKey, creditCost)
			}
			return errResult(fmt.Errorf("deliberation not found: %w", err))
		}
		s.audit(ctx, "analyze:run", args.DeliberationID, "")
		RunAnalysisAsync(s.svc, s.db, s.credits, args.DeliberationID, args.Model, apiKey, creditCost)
		return textResult(fmt.Sprintf(
			"Analysis started for deliberation %s. Poll deliberation action:get to track progress (sub_status will show: taxonomy → extracting → crux_detection → clustering). Results available via analyze action:get_result once status returns to 'open'.",
			args.DeliberationID,
		)), nil, nil

	case "get_result":
		result, err := CoreGetAnalysisResult(s.svc, args.DeliberationID, keyID, args.Round)
		if err != nil {
			return errResult(err)
		}
		if result == nil {
			return textResult("no analysis results yet"), nil, nil
		}
		return jsonResult(result)

	case "cancel":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if err := CoreCancelAnalysis(s.svc, args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		s.audit(ctx, "analyze:cancel", args.DeliberationID, "")
		return textResult("analysis cancelled — deliberation is open again"), nil, nil

	case "propose_compromise":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if args.Model != "" {
			if !llm.AllowedModels[args.Model] {
				return errResult(fmt.Errorf("unsupported model %q", args.Model))
			}
			ctx = context.WithValue(ctx, llm.ContextKeyModel{}, args.Model)
		}
		apiKey, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)
		var creditCost int
		if apiKey != "" && s.credits != nil {
			creditCost = payments.CreditCost(args.Model)
			if _, err := s.credits.Deduct(apiKey, creditCost); err != nil {
				balance, _ := s.credits.GetBalance(apiKey)
				return errResult(fmt.Errorf("insufficient credits: have %d, need %d", balance, creditCost))
			}
		}
		proposal, err := s.svc.ProposeCompromise(ctx, args.DeliberationID)
		if err != nil {
			if apiKey != "" && creditCost > 0 && s.credits != nil {
				_, _ = s.credits.AddCredits(apiKey, creditCost)
			}
			return errResult(err)
		}
		s.audit(ctx, "analyze:propose_compromise", args.DeliberationID, "")
		return jsonResult(map[string]string{
			"deliberation_id":     args.DeliberationID,
			"compromise_proposal": proposal,
		})

	case "reframe":
		apiKey, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)
		result, err := CoreReframe(s.svc, s.credits, args.DeliberationID, args.PositionID, args.Model, keyID, false, apiKey)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)

	case "challenge":
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		result, err := CoreChallengeAnalysis(s.svc, args.DeliberationID, args.AgentID, args.Reason, keyID)
		if err != nil {
			return errResult(err)
		}
		return textResult(result["status"] + ". " + result["detail"]), nil, nil

	case "dispute_crux":
		if args.DeliberationID == "" || args.AgentID == "" || args.CruxClaim == "" || args.Correction == "" {
			return errResult(fmt.Errorf("deliberation_id, agent_id, crux_claim, and correction are required"))
		}
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		d, err := s.svc.DisputeCrux(args.DeliberationID, args.AgentID, args.CruxClaim, args.Correction)
		if err != nil {
			return errResult(err)
		}
		s.audit(ctx, "analyze:dispute_crux", args.DeliberationID, args.AgentID)
		return jsonResult(d)

	default:
		return errResult(fmt.Errorf("unknown action %q — use: run, get_result, cancel, propose_compromise, reframe, challenge, dispute_crux", args.Action))
	}
}

func (s *server) handleDecide(ctx context.Context, _ *sdkmcp.CallToolRequest, args decideParams) (*sdkmcp.CallToolResult, any, error) {
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)

	switch args.Action {
	case "commit":
		if args.DeliberationID == "" || args.AgentID == "" || args.Statement == "" {
			return errResult(fmt.Errorf("deliberation_id, agent_id, and statement are required"))
		}
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		c, err := s.svc.Commit(args.DeliberationID, args.AgentID, args.Statement, args.Conditional)
		if err != nil {
			return errResult(err)
		}
		s.audit(ctx, "decide:commit", args.DeliberationID, args.AgentID)
		return jsonResult(c)

	case "get_commitments":
		commitments, err := CoreGetCommitments(s.svc, args.DeliberationID, keyID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(commitments)

	case "fulfill":
		verifiedBy := args.VerifiedBy
		if verifiedBy == "" {
			verifiedBy = keyID
		}
		if err := CoreFulfillCommitment(s.svc, args.CommitmentID, verifiedBy); err != nil {
			return errResult(err)
		}
		return textResult("commitment fulfilled"), nil, nil

	case "break":
		verifiedBy := args.VerifiedBy
		if verifiedBy == "" {
			verifiedBy = keyID
		}
		if err := CoreBreakCommitment(s.svc, args.CommitmentID, args.Reason, verifiedBy); err != nil {
			return errResult(err)
		}
		return textResult("commitment marked as broken"), nil, nil

	case "reputation":
		rep, err := CoreAgentReputation(s.svc, args.AgentID, args.GroupID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(rep)

	default:
		return errResult(fmt.Errorf("unknown action %q — use: commit, get_commitments, fulfill, break, reputation", args.Action))
	}
}

func (s *server) handleCoordinate(ctx context.Context, _ *sdkmcp.CallToolRequest, args coordinateParams) (*sdkmcp.CallToolResult, any, error) {
	switch args.Action {
	case "delegate":
		if args.DeliberationID == "" || args.FromAgent == "" || args.ToAgent == "" {
			return errResult(fmt.Errorf("deliberation_id, from_agent, and to_agent are required"))
		}
		args.FromAgent = scopeAgentID(ctx, args.FromAgent)
		args.ToAgent = scopeAgentID(ctx, args.ToAgent)
		d, err := s.svc.Delegate(args.DeliberationID, args.FromAgent, args.ToAgent, args.Scope)
		if err != nil {
			return errResult(err)
		}
		s.audit(ctx, "coordinate:delegate", args.DeliberationID, args.FromAgent)
		return jsonResult(d)

	case "invite":
		if args.DeliberationID == "" || args.InvitedBy == "" || args.InvitedAgent == "" || args.Reason == "" {
			return errResult(fmt.Errorf("deliberation_id, invited_by, invited_agent, and reason are required"))
		}
		args.InvitedBy = scopeAgentID(ctx, args.InvitedBy)
		inv, err := s.svc.InviteAgent(args.DeliberationID, args.InvitedBy, args.InvitedAgent, args.Role, args.Reason)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(inv)

	case "generate_join_code":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		ttl := time.Duration(args.TTLMinutes) * time.Minute
		if ttl <= 0 {
			ttl = time.Hour
		}
		if ttl > 24*time.Hour {
			ttl = 24 * time.Hour
		}
		jc, err := s.svc.GenerateJoinCode(args.DeliberationID, args.Role, ttl)
		if err != nil {
			return errResult(err)
		}
		return jsonResultWithHints(jc, "Share this code with the agent you want to join. They call coordinate action:join with this code. Code expires in "+ttl.String()+".")

	case "join":
		if args.Code == "" || args.AgentID == "" {
			return errResult(fmt.Errorf("code and agent_id are required"))
		}
		deliberationID, role, err := s.svc.JoinDeliberation(args.Code, args.AgentID)
		if err != nil {
			return errResult(err)
		}
		result := map[string]string{
			"deliberation_id": deliberationID,
			"agent_id":        args.AgentID,
			"role":            role,
			"status":          "joined",
		}
		return jsonResultWithHints(result,
			"You've joined the deliberation as '"+role+"'. Next: participate action:get_positions to read what others have said, then participate action:submit_position with your perspective.")

	default:
		return errResult(fmt.Errorf("unknown action %q — use: delegate, invite, generate_join_code, join", args.Action))
	}
}

func (s *server) handleAdmin(ctx context.Context, _ *sdkmcp.CallToolRequest, args adminParams) (*sdkmcp.CallToolResult, any, error) {
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)

	switch args.Action {
	case "report_abuse":
		if args.DeliberationID == "" || args.Reason == "" {
			return errResult(fmt.Errorf("deliberation_id and reason are required"))
		}
		if err := s.svc.ReportAbuse(args.DeliberationID, keyID, args.Reason); err != nil {
			return errResult(err)
		}
		s.audit(ctx, "admin:report_abuse", args.DeliberationID, "")
		return textResult("abuse report filed — thank you"), nil, nil

	case "get_audit_log":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if err := s.svc.CheckAccess(args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		opLog, err := s.db.GetAuditLog(args.DeliberationID, 50)
		if err != nil {
			opLog = nil
		}
		var analysisAudit []deliberation.AuditEntry
		if result, err := s.db.GetLatestAnalysisResult(ctx, args.DeliberationID); err == nil && result != nil {
			analysisAudit = result.AuditLog
		}
		return jsonResult(map[string]any{
			"operations":         opLog,
			"analysis_decisions": analysisAudit,
		})

	case "list_templates":
		return jsonResult(deliberation.ListTemplates())

	case "get_votes":
		votes, err := CoreGetVotes(s.svc, args.DeliberationID, keyID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(votes)

	default:
		return errResult(fmt.Errorf("unknown action %q — use: report_abuse, get_audit_log, list_templates, get_votes", args.Action))
	}
}

// coerceVoteValue accepts int, float64, or string representations of a vote value.
// MCP clients may send "1" (string) instead of 1 (integer).
func coerceVoteValue(v any) (int, error) {
	switch val := v.(type) {
	case float64:
		return int(val), nil
	case int:
		return val, nil
	case string:
		switch val {
		case "1", "+1":
			return 1, nil
		case "0":
			return 0, nil
		case "-1":
			return -1, nil
		default:
			return 0, fmt.Errorf("invalid vote value %q: must be -1, 0, or 1", val)
		}
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("invalid vote value type: %T", v)
	}
}

// --- Helpers ---

// scopeAgentID prefixes an agent ID with the caller's key namespace.
// Admin callers (no key_id) pass through unscoped.
// Colons are stripped from agentID to prevent impersonation of other key namespaces.
func scopeAgentID(ctx context.Context, agentID string) string {
	agentID = strings.ReplaceAll(agentID, ":", "_")
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	if keyID == "" {
		return agentID // admin or dev mode — no scoping
	}
	return keyID + ":" + agentID
}

func textResult(text string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}},
	}
}

func jsonResult(v any) (*sdkmcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResult(err)
	}
	return textResult(string(b)), nil, nil
}

// jsonResultWithHints adds progressive disclosure: suggests what tools to call next.
func jsonResultWithHints(v any, hints string) (*sdkmcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResult(err)
	}
	return textResult(string(b) + "\n\n---\n" + hints), nil, nil
}

func errResult(err error) (*sdkmcp.CallToolResult, any, error) {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
		IsError: true,
	}, nil, nil
}
