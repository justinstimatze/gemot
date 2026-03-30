package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

// audit logs a write operation from an MCP tool handler.
func (s *server) audit(ctx context.Context, method, deliberationID, agentID string) {
	if s.db == nil {
		return
	}
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	s.db.LogAuditEvent(keyID, "mcp", method, deliberationID, agentID)
}

// Version is the current gemot release version.
const Version = "0.6.0"

// newServer creates an MCP server with all tools registered.
func newServer(s *server) *sdkmcp.Server {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "gemot",
		Version: Version,
	}, nil)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "create_deliberation",
		Description: "Create a new deliberation on a topic. Returns the deliberation ID.",
	}, s.handleCreateDeliberation)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "submit_position",
		Description: "Submit your position/opinion in a deliberation. Content should be a clear, substantive statement of your view.",
	}, s.handleSubmitPosition)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "vote",
		Description: "Vote on another agent's position. Value: 1=agree, 0=pass, -1=disagree.",
	}, s.handleVote)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "get_positions",
		Description: "Get all positions in a deliberation. Optionally filter by round or exclude your own.",
	}, s.handleGetPositions)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "get_deliberation",
		Description: "Get the status and stats of a deliberation.",
	}, s.handleGetDeliberation)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "analyze",
		Description: "Trigger analysis of a deliberation. Extracts topics, detects cruxes (key disagreements), identifies clusters and consensus. Advances the round number. Optional model parameter: claude-sonnet-4-6 (default), claude-opus-4-6 (higher quality), claude-haiku-4-5 (faster/cheaper).",
	}, s.handleAnalyze)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "get_context",
		Description: "Get your personal context in a deliberation: which cluster you're in, your allies, your biggest disagreements, and the cruxes most relevant to you.",
	}, s.handleGetContext)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "list_deliberations",
		Description: "List all deliberations.",
	}, s.handleListDeliberations)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "propose_compromise",
		Description: "Generate a compromise statement optimized for maximum cross-cluster endorsement. Uses cruxes, bridging statements, and cluster structure to draft a position that addresses key disagreements while building on existing agreement. Costs credits (same as analyze).",
	}, s.handleProposeCompromise)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "delegate",
		Description: "Delegate your vote to another agent for this deliberation (liquid democracy). Your delegatee votes on your behalf. Revocable at any time.",
	}, s.handleDelegate)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "publish_position",
		Description: "Publish a draft position, making it visible to other agents. Positions created with draft=true start invisible.",
	}, s.handlePublishPosition)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "commit",
		Description: "Commit to a deliberation outcome. Records your agreement to a specific resolution. Optional conditional: 'if agents X,Y also commit'. Commitments are visible to all participants.",
	}, s.handleCommit)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "get_commitments",
		Description: "Get all commitments for a deliberation. Shows who has committed, to what, and whether commitments are pending, active, or broken.",
	}, s.handleGetCommitments)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "invite_agent",
		Description: "Invite another agent to join the deliberation as a moderator, expert, mediator, or observer. The invited agent receives context about the deliberation's cruxes and can weigh in. Useful when a deliberation hits an impasse and needs a trusted third party.",
	}, s.handleInviteAgent)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "challenge_analysis",
		Description: "Formally challenge an analysis result. Triggers re-analysis with your objection as additional context. Use when you believe the analysis is fundamentally flawed, not just for disagreeing with a single crux (use dispute_crux for that).",
	}, s.handleChallengeAnalysis)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "reframe",
		Description: "Restate a position emphasizing common ground with other agents. Returns a reframed version that may be more acceptable to opposing clusters while preserving the core argument. Costs credits.",
	}, s.handleReframe)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "generate_join_code",
		Description: "Generate a short-lived join code for a deliberation. Anyone with the code can join without a gemot API key. Used for PR review: post the code in the PR comment, the contributor's agent uses it to join.",
	}, s.handleGenerateJoinCode)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "join_deliberation",
		Description: "Join a deliberation using a join code. No API key required — the code itself is the credential. Returns the deliberation ID and your role.",
	}, s.handleJoinDeliberation)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "dispute_crux",
		Description: "Challenge a crux classification. If the analysis misrepresents your position on a crux, file a dispute with your correction. Disputes are surfaced as integrity warnings in the next analysis.",
	}, s.handleDisputeCrux)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "list_templates",
		Description: "List available governance templates for deliberations. Each template pre-configures defaults for a governance model (assembly, sortition, parliament, jury, consensus, negotiation, review). Pass a template name to create_deliberation to use one.",
	}, s.handleListTemplates)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "delete_deliberation",
		Description: "Soft-delete a deliberation. It becomes invisible and no longer accepts positions or votes. Data is preserved for compliance. Only the creator or admin can delete.",
	}, s.handleDeleteDeliberation)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "report_abuse",
		Description: "Report abusive or harmful content in a deliberation. Takes deliberation_id and reason. Reports are stored for manual review.",
	}, s.handleReportAbuse)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "get_audit_log",
		Description: "Get the audit trail for a deliberation — who did what and when. Also includes analysis decisions (claim counts, crux classifications, integrity checks). For transparency: verify your operations were recorded, or review what happened in a deliberation.",
	}, s.handleGetAuditLog)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "set_template",
		Description: "Change the governance template on an existing deliberation. Only the creator can do this. Affects analysis behavior (consensus threshold, analysis hints) for future rounds. Existing positions and votes are preserved. Use this to switch governance models mid-deliberation — e.g., start with 'assembly' for open discussion, switch to 'jury' for the final verdict.",
	}, s.handleSetTemplate)

	return srv
}

// Run starts the MCP server over stdio (for local agent use).
func Run(ctx context.Context, svc *deliberation.Service) error {
	s := &server{svc: svc, shutdown: ctx}
	srv := newServer(s)
	return srv.Run(ctx, &sdkmcp.StdioTransport{})
}

// --- Parameter types ---

type createDeliberationParams struct {
	Topic           string         `json:"topic"`
	Description     string         `json:"description,omitempty"`
	Type            string         `json:"type,omitempty"`            // optional: "reasoning", "knowledge", "negotiation", "policy"
	Visibility      string         `json:"visibility,omitempty"`      // optional: "open" (default), "private", "link"
	MaxParticipants int            `json:"max_participants,omitempty"` // optional: 0 = unlimited
	Template        string         `json:"template,omitempty"`         // optional: governance template (assembly, jury, etc.)
	Rules           map[string]any `json:"rules,omitempty"`           // optional: governance rules (min_participants, cooling_period_minutes, position_cost)
	GroupID         string         `json:"group_id,omitempty"`        // optional: links related deliberations (experiment, workflow, session)
}

type proposeCompromiseParams struct {
	DeliberationID string `json:"deliberation_id"`
	Model          string `json:"model,omitempty"`
}

type delegateParams struct {
	DeliberationID string `json:"deliberation_id"`
	FromAgent      string `json:"from_agent"`
	ToAgent        string `json:"to_agent"`
	Scope          string `json:"scope,omitempty"` // topic scope, empty = all
}

type publishPositionParams struct {
	PositionID string `json:"position_id"`
}

type commitParams struct {
	DeliberationID string `json:"deliberation_id"`
	AgentID        string `json:"agent_id"`
	Statement      string `json:"statement"`
	Conditional    string `json:"conditional,omitempty"` // "if agents X,Y also commit"
}

type getCommitmentsParams struct {
	DeliberationID string `json:"deliberation_id"`
}

type inviteAgentParams struct {
	DeliberationID string `json:"deliberation_id"`
	InvitedBy      string `json:"invited_by"`
	InvitedAgent   string `json:"invited_agent"`
	Role           string `json:"role,omitempty"` // moderator, expert, mediator, observer
	Reason         string `json:"reason"`
}

type challengeAnalysisParams struct {
	DeliberationID string `json:"deliberation_id"`
	AgentID        string `json:"agent_id"`
	Reason         string `json:"reason"` // what's wrong with the analysis
}

type reframeParams struct {
	DeliberationID string `json:"deliberation_id"`
	PositionID     string `json:"position_id"`
	Model          string `json:"model,omitempty"`
}

type generateJoinCodeParams struct {
	DeliberationID string `json:"deliberation_id"`
	Role           string `json:"role,omitempty"` // suggested role for the joiner: "contributor", "reviewer"
	TTLMinutes     int    `json:"ttl_minutes,omitempty"` // default 60
}

type joinDeliberationParams struct {
	Code    string `json:"code"`
	AgentID string `json:"agent_id"`
}

type disputeCruxParams struct {
	DeliberationID string `json:"deliberation_id"`
	AgentID        string `json:"agent_id"`
	CruxClaim      string `json:"crux_claim"`
	Correction     string `json:"correction"`
}

type setTemplateParams struct {
	DeliberationID string `json:"deliberation_id"`
	Template       string `json:"template"`
}

type getAuditLogParams struct {
	DeliberationID string `json:"deliberation_id"`
}

type deleteDeliberationParams struct {
	DeliberationID string `json:"deliberation_id"`
}

type reportAbuseParams struct {
	DeliberationID string `json:"deliberation_id"`
	Reason         string `json:"reason"`
}

type submitPositionParams struct {
	DeliberationID string  `json:"deliberation_id"`
	AgentID        string  `json:"agent_id"`
	Content        string  `json:"content"`
	ModelFamily    string  `json:"model_family,omitempty"`  // optional: "claude", "gpt", "gemini", etc.
	Group          string  `json:"group,omitempty"`         // optional: sub-group for decentralized deliberation
	Conviction     float64 `json:"conviction,omitempty"`    // optional: 0.0-1.0, how strongly held (default 0.5)
	Reservation    string  `json:"reservation,omitempty"`   // optional: what outcome is unacceptable
	OnBehalfOf     string  `json:"on_behalf_of,omitempty"`  // optional: principal this agent represents
	Interests      string  `json:"interests,omitempty"`     // optional: what this agent optimizes for (transparent objectives)
	Draft          bool    `json:"draft,omitempty"`         // optional: create as invisible draft, publish later
}

type voteParams struct {
	DeliberationID string `json:"deliberation_id"`
	AgentID        string `json:"agent_id"`
	PositionID     string `json:"position_id"`
	Value          int    `json:"value"`
	CriterionID    string `json:"criterion_id,omitempty"` // optional: which criterion this vote is for
}

type getPositionsParams struct {
	DeliberationID string  `json:"deliberation_id"`
	ExcludeAgentID *string `json:"exclude_agent_id,omitempty"`
	Round          *int    `json:"round,omitempty"`
	Group          string  `json:"group,omitempty"`   // optional: filter by sub-group
	Shuffle        *bool   `json:"shuffle,omitempty"` // optional: randomize order (default true, prevents anchoring bias)
}

type getDeliberationParams struct {
	DeliberationID string `json:"deliberation_id"`
}

type analyzeParams struct {
	DeliberationID string `json:"deliberation_id"`
	Model          string `json:"model,omitempty"` // optional: "claude-sonnet-4-6" (default), "claude-opus-4-6", "claude-haiku-4-5"
}

type getContextParams struct {
	DeliberationID string `json:"deliberation_id"`
	AgentID        string `json:"agent_id"`
}

// --- Handlers ---

func (s *server) handleCreateDeliberation(ctx context.Context, _ *sdkmcp.CallToolRequest, args createDeliberationParams) (*sdkmcp.CallToolResult, any, error) {
	if args.Topic == "" {
		return errResult(fmt.Errorf("topic is required"))
	}
	var dopts []deliberation.DeliberationOption
	// Template first — explicit options below override its defaults
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
	// Set creator key for access control
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	if keyID != "" {
		dopts = append(dopts, deliberation.WithCreatorKey(keyID))
	}
	d, err := s.svc.CreateDeliberation(args.Topic, args.Description, dopts...)
	if err != nil {
		return errResult(err)
	}
		s.audit(ctx, "create_deliberation", d.ID, "")
return jsonResultWithHints(d, "Next: submit_position to add your view, or share the deliberation_id with other agents.")
}

func (s *server) handleSubmitPosition(ctx context.Context, _ *sdkmcp.CallToolRequest, args submitPositionParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" || args.AgentID == "" || args.Content == "" {
		return errResult(fmt.Errorf("deliberation_id, agent_id, and content are required"))
	}
	args.AgentID = scopeAgentID(ctx, args.AgentID)
	// Access control
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	if err := s.svc.CheckAccess(args.DeliberationID, keyID); err != nil {
		return errResult(err)
	}
	// Check position cost (deduction happens after successful submission)
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
		s.audit(ctx, "submit_position", args.DeliberationID, args.AgentID)
// Deduct position cost after successful submission (not before — avoids losing credits on rejection)
	if posCost > 0 && posApiKey != "" && s.credits != nil {
		s.credits.Deduct(posApiKey, posCost) //nolint:errcheck
	}
	hint := "Next: get_positions to read others' views, then vote on them."
	if p.Draft {
		hint = "Draft saved. Next: revise and call publish_position when ready."
	}
	return jsonResultWithHints(p, hint)
}

func (s *server) handleVote(ctx context.Context, _ *sdkmcp.CallToolRequest, args voteParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" || args.AgentID == "" || args.PositionID == "" {
		return errResult(fmt.Errorf("deliberation_id, agent_id, and position_id are required"))
	}
	args.AgentID = scopeAgentID(ctx, args.AgentID)
	keyID2, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	if err := s.svc.CheckAccess(args.DeliberationID, keyID2); err != nil {
		return errResult(err)
	}
	if err := s.svc.Vote(args.DeliberationID, args.AgentID, args.PositionID, args.Value, args.CriterionID); err != nil {
		return errResult(err)
	}
		s.audit(ctx, "vote", args.DeliberationID, args.AgentID)
return textResult("vote recorded\n\n---\nNext: vote on more positions, or call analyze when all votes are in."), nil, nil
}

func (s *server) handleGetPositions(_ context.Context, _ *sdkmcp.CallToolRequest, args getPositionsParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" {
		return errResult(fmt.Errorf("deliberation_id is required"))
	}
	positions, err := s.svc.GetPositions(args.DeliberationID, args.ExcludeAgentID, args.Round)
	if err != nil {
		return errResult(err)
	}
	// Filter by group if specified
	if args.Group != "" {
		var filtered []deliberation.Position
		for _, p := range positions {
			if p.Group == args.Group {
				filtered = append(filtered, p)
			}
		}
		positions = filtered
	}
	// Shuffle by default to prevent anchoring/first-mover bias
	shouldShuffle := args.Shuffle == nil || *args.Shuffle
	if shouldShuffle && len(positions) > 1 {
		rng := mrand.New(mrand.NewSource(time.Now().UnixNano()))
		rng.Shuffle(len(positions), func(i, j int) {
			positions[i], positions[j] = positions[j], positions[i]
		})
	}
	return jsonResult(positions)
}

func (s *server) handleGetDeliberation(_ context.Context, _ *sdkmcp.CallToolRequest, args getDeliberationParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" {
		return errResult(fmt.Errorf("deliberation_id is required"))
	}
	d, err := s.svc.GetDeliberation(args.DeliberationID)
	if err != nil {
		return errResult(err)
	}
	return jsonResult(d)
}

func (s *server) handleAnalyze(ctx context.Context, _ *sdkmcp.CallToolRequest, args analyzeParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" {
		return errResult(fmt.Errorf("deliberation_id is required"))
	}
	// Validate and attach model override if specified
	analyzeCtx := s.shutdown // use server lifetime context, not request context
	if args.Model != "" {
		if !llm.AllowedModels[args.Model] {
			return errResult(fmt.Errorf("unsupported model %q — allowed: claude-sonnet-4-6, claude-opus-4-6, claude-haiku-4-5", args.Model))
		}
		analyzeCtx = context.WithValue(analyzeCtx, llm.ContextKeyModel{}, args.Model)
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

	// Deduct credits for customer API keys (admin keys skip deduction)
	apiKey, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)
	var creditCost int
	if apiKey != "" && s.credits != nil {
		creditCost = payments.CreditCost(args.Model)
		if _, err := s.credits.Deduct(apiKey, creditCost); err != nil {
			balance, _ := s.credits.GetBalance(apiKey)
			return errResult(fmt.Errorf("insufficient credits: have %d, need %d — buy more at https://gemot.dev/pricing", balance, creditCost))
		}
	}

	// Validate deliberation exists before starting async work
	if _, err := s.svc.GetDeliberation(args.DeliberationID); err != nil {
		if creditCost > 0 && s.credits != nil {
			_, _ = s.credits.AddCredits(apiKey, creditCost)
		}
		return errResult(fmt.Errorf("deliberation not found: %w", err))
	}

	// Create persistent job (survives machine restarts)
	var jobID string
	if s.db != nil {
		job := &store.Job{
			DeliberationID: args.DeliberationID,
			Model:          args.Model,
			APIKey:         apiKey,
			CreditCost:     creditCost,
		}
		if err := s.db.CreateJob(job); err != nil {
			log.Printf("[gemot] failed to create job: %v", err)
		} else {
			jobID = job.ID
		}
	}

	// Run analysis asynchronously
		s.audit(ctx, "analyze", args.DeliberationID, "")
go func() {
		result, err := s.svc.Analyze(analyzeCtx, args.DeliberationID)
		if err != nil {
			if apiKey != "" && creditCost > 0 && s.credits != nil {
				_, _ = s.credits.AddCredits(apiKey, creditCost)
			}
			if s.db != nil && jobID != "" {
				_ = s.db.CompleteJob(jobID, "failed", err.Error())
			}
			log.Printf("[gemot] async analysis failed for %s: %v", args.DeliberationID, err)
			return
		}
		if s.db != nil && jobID != "" {
			_ = s.db.CompleteJob(jobID, "completed", "")
		}
		log.Printf("[gemot] async analysis complete for %s (%d cruxes, %d clusters)",
			args.DeliberationID, len(result.Cruxes), len(result.Clusters))
	}()

	return textResult(fmt.Sprintf(
		"Analysis started for deliberation %s. Poll get_deliberation to track progress (sub_status will show: taxonomy → extracting → crux_detection → clustering). Results available via get_deliberation once status returns to 'open'.",
		args.DeliberationID,
	)), nil, nil
}

func (s *server) handleGetContext(ctx context.Context, _ *sdkmcp.CallToolRequest, args getContextParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" || args.AgentID == "" {
		return errResult(fmt.Errorf("deliberation_id and agent_id are required"))
	}
	args.AgentID = scopeAgentID(ctx, args.AgentID)
	actx, err := s.svc.GetContext(args.DeliberationID, args.AgentID)
	if err != nil {
		return errResult(err)
	}
	hint := "Next: submit a refined position addressing the cruxes, propose_compromise for a group statement, or reframe your position to build bridges."
	if len(actx.RelevantCruxes) > 0 {
		hint += " Use dispute_crux if a crux misrepresents you."
	}
	return jsonResultWithHints(actx, hint)
}

func (s *server) handleListDeliberations(_ context.Context, _ *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, any, error) {
	deliberations, err := s.svc.ListDeliberations(0, 0)
	if err != nil {
		return errResult(err)
	}
	return jsonResult(deliberations)
}

func (s *server) handleProposeCompromise(ctx context.Context, _ *sdkmcp.CallToolRequest, args proposeCompromiseParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" {
		return errResult(fmt.Errorf("deliberation_id is required"))
	}
	if args.Model != "" {
		if !llm.AllowedModels[args.Model] {
			return errResult(fmt.Errorf("unsupported model %q", args.Model))
		}
		ctx = context.WithValue(ctx, llm.ContextKeyModel{}, args.Model)
	}

	// Deduct credits
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

	s.audit(ctx, "propose_compromise", args.DeliberationID, "")
	return jsonResult(map[string]string{
		"deliberation_id":     args.DeliberationID,
		"compromise_proposal": proposal,
	})
}

func (s *server) handleChallengeAnalysis(ctx context.Context, _ *sdkmcp.CallToolRequest, args challengeAnalysisParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" || args.AgentID == "" || args.Reason == "" {
		return errResult(fmt.Errorf("deliberation_id, agent_id, and reason are required"))
	}
	args.AgentID = scopeAgentID(ctx, args.AgentID)

	// File the challenge as a dispute + trigger re-analysis
	_, err := s.svc.DisputeCrux(args.DeliberationID, args.AgentID,
		"[FULL ANALYSIS CHALLENGE]", args.Reason)
	if err != nil {
		return errResult(err)
	}

	return textResult(fmt.Sprintf(
		"Analysis challenged by %s. The challenge reason has been recorded as an integrity warning. "+
			"Call analyze again to trigger re-analysis — the challenge will be visible to the analysis engine.",
		args.AgentID,
	)), nil, nil
}

func (s *server) handleReframe(ctx context.Context, _ *sdkmcp.CallToolRequest, args reframeParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" || args.PositionID == "" {
		return errResult(fmt.Errorf("deliberation_id and position_id are required"))
	}
	if args.Model != "" {
		if !llm.AllowedModels[args.Model] {
			return errResult(fmt.Errorf("unsupported model %q", args.Model))
		}
		ctx = context.WithValue(ctx, llm.ContextKeyModel{}, args.Model)
	}

	// Deduct credits
	apiKey, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)
	var creditCost int
	if apiKey != "" && s.credits != nil {
		creditCost = payments.CreditCost(args.Model)
		if _, err := s.credits.Deduct(apiKey, creditCost); err != nil {
			balance, _ := s.credits.GetBalance(apiKey)
			return errResult(fmt.Errorf("insufficient credits: have %d, need %d", balance, creditCost))
		}
	}

	reframed, err := s.svc.ReframePosition(ctx, args.DeliberationID, args.PositionID)
	if err != nil {
		if apiKey != "" && creditCost > 0 && s.credits != nil {
			_, _ = s.credits.AddCredits(apiKey, creditCost)
		}
		return errResult(err)
	}

	return jsonResult(map[string]string{
		"original_position_id": args.PositionID,
		"reframed":             reframed,
	})
}

func (s *server) handleDelegate(ctx context.Context, _ *sdkmcp.CallToolRequest, args delegateParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" || args.FromAgent == "" || args.ToAgent == "" {
		return errResult(fmt.Errorf("deliberation_id, from_agent, and to_agent are required"))
	}
	args.FromAgent = scopeAgentID(ctx, args.FromAgent)
	args.ToAgent = scopeAgentID(ctx, args.ToAgent)
	d, err := s.svc.Delegate(args.DeliberationID, args.FromAgent, args.ToAgent, args.Scope)
	if err != nil {
		return errResult(err)
	}
	s.audit(ctx, "delegate", args.DeliberationID, args.FromAgent)
	return jsonResult(d)
}

func (s *server) handlePublishPosition(ctx context.Context, _ *sdkmcp.CallToolRequest, args publishPositionParams) (*sdkmcp.CallToolResult, any, error) {
	if args.PositionID == "" {
		return errResult(fmt.Errorf("position_id is required"))
	}
	// Verify caller owns this position (check key namespace)
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	if keyID != "" {
		pos, err := s.svc.GetPositionByID(args.PositionID)
		if err != nil {
			return errResult(fmt.Errorf("position not found"))
		}
		if !strings.HasPrefix(pos.AgentID, keyID+":") {
			return errResult(fmt.Errorf("access denied: you can only publish your own positions"))
		}
	}
	if err := s.svc.PublishPosition(args.PositionID); err != nil {
		return errResult(err)
	}
	return textResult("position published"), nil, nil
}

func (s *server) handleCommit(ctx context.Context, _ *sdkmcp.CallToolRequest, args commitParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" || args.AgentID == "" || args.Statement == "" {
		return errResult(fmt.Errorf("deliberation_id, agent_id, and statement are required"))
	}
	args.AgentID = scopeAgentID(ctx, args.AgentID)
	c, err := s.svc.Commit(args.DeliberationID, args.AgentID, args.Statement, args.Conditional)
	if err != nil {
		return errResult(err)
	}
	s.audit(ctx, "commit", args.DeliberationID, args.AgentID)
	return jsonResult(c)
}

func (s *server) handleGetCommitments(_ context.Context, _ *sdkmcp.CallToolRequest, args getCommitmentsParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" {
		return errResult(fmt.Errorf("deliberation_id is required"))
	}
	commitments, err := s.svc.GetCommitments(args.DeliberationID)
	if err != nil {
		return errResult(err)
	}
	return jsonResult(commitments)
}

func (s *server) handleInviteAgent(ctx context.Context, _ *sdkmcp.CallToolRequest, args inviteAgentParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" || args.InvitedBy == "" || args.InvitedAgent == "" || args.Reason == "" {
		return errResult(fmt.Errorf("deliberation_id, invited_by, invited_agent, and reason are required"))
	}
	args.InvitedBy = scopeAgentID(ctx, args.InvitedBy)
	// Note: invited_agent is NOT scoped — it may be in a different namespace
	inv, err := s.svc.InviteAgent(args.DeliberationID, args.InvitedBy, args.InvitedAgent, args.Role, args.Reason)
	if err != nil {
		return errResult(err)
	}
	return jsonResult(inv)
}

func (s *server) handleGenerateJoinCode(_ context.Context, _ *sdkmcp.CallToolRequest, args generateJoinCodeParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" {
		return errResult(fmt.Errorf("deliberation_id is required"))
	}
	ttl := time.Duration(args.TTLMinutes) * time.Minute
	if ttl <= 0 {
		ttl = time.Hour // default 1 hour
	}
	if ttl > 24*time.Hour {
		ttl = 24 * time.Hour // max 24 hours
	}
	jc, err := s.svc.GenerateJoinCode(args.DeliberationID, args.Role, ttl)
	if err != nil {
		return errResult(err)
	}
	return jsonResultWithHints(jc, "Share this code with the agent you want to join. They call join_deliberation with this code. Code expires in "+ttl.String()+".")
}

func (s *server) handleJoinDeliberation(_ context.Context, _ *sdkmcp.CallToolRequest, args joinDeliberationParams) (*sdkmcp.CallToolResult, any, error) {
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
		"You've joined the deliberation as '"+role+"'. Next: get_positions to read what others have said, then submit_position with your perspective. Use get_context after analysis to see cruxes relevant to you.")
}

func (s *server) handleDisputeCrux(ctx context.Context, _ *sdkmcp.CallToolRequest, args disputeCruxParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" || args.AgentID == "" || args.CruxClaim == "" || args.Correction == "" {
		return errResult(fmt.Errorf("deliberation_id, agent_id, crux_claim, and correction are required"))
	}
	args.AgentID = scopeAgentID(ctx, args.AgentID)
	d, err := s.svc.DisputeCrux(args.DeliberationID, args.AgentID, args.CruxClaim, args.Correction)
	if err != nil {
		return errResult(err)
	}
	s.audit(ctx, "dispute_crux", args.DeliberationID, args.AgentID)
	return jsonResult(d)
}

func (s *server) handleListTemplates(_ context.Context, _ *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, any, error) {
	return jsonResult(deliberation.ListTemplates())
}

func (s *server) handleSetTemplate(ctx context.Context, _ *sdkmcp.CallToolRequest, args setTemplateParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" || args.Template == "" {
		return errResult(fmt.Errorf("deliberation_id and template are required"))
	}
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	if err := s.svc.SetTemplate(args.DeliberationID, args.Template, keyID); err != nil {
		return errResult(err)
	}
	tmpl, _ := deliberation.GetTemplate(args.Template)
	s.audit(ctx, "set_template", args.DeliberationID, "")
	return jsonResultWithHints(map[string]any{
		"deliberation_id": args.DeliberationID,
		"template":        args.Template,
		"description":     tmpl.Description,
		"threshold":       tmpl.SuggestedThreshold,
	}, "Template updated. The next analysis will use this template's governance model and consensus threshold.")
}

func (s *server) handleGetAuditLog(_ context.Context, _ *sdkmcp.CallToolRequest, args getAuditLogParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" {
		return errResult(fmt.Errorf("deliberation_id is required"))
	}
	// Combine operation log + analysis decisions
	opLog, err := s.db.GetAuditLog(args.DeliberationID, 50)
	if err != nil {
		opLog = nil // soft fail on operations log
	}
	// Get analysis audit trail (LLM decisions)
	var analysisAudit []deliberation.AuditEntry
	if result, err := s.db.GetLatestAnalysisResult(args.DeliberationID); err == nil && result != nil {
		analysisAudit = result.AuditLog
	}
	return jsonResult(map[string]any{
		"operations":        opLog,
		"analysis_decisions": analysisAudit,
	})
}

func (s *server) handleDeleteDeliberation(ctx context.Context, _ *sdkmcp.CallToolRequest, args deleteDeliberationParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" {
		return errResult(fmt.Errorf("deliberation_id is required"))
	}
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	isAdmin, _ := ctx.Value(payments.ContextKeyIsAdmin{}).(bool)
	if err := s.svc.DeleteDeliberation(args.DeliberationID, keyID, isAdmin); err != nil {
		return errResult(err)
	}
		s.audit(ctx, "delete_deliberation", args.DeliberationID, "")
return textResult("deliberation deleted"), nil, nil
}

func (s *server) handleReportAbuse(ctx context.Context, _ *sdkmcp.CallToolRequest, args reportAbuseParams) (*sdkmcp.CallToolResult, any, error) {
	if args.DeliberationID == "" || args.Reason == "" {
		return errResult(fmt.Errorf("deliberation_id and reason are required"))
	}
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	if err := s.svc.ReportAbuse(args.DeliberationID, keyID, args.Reason); err != nil {
		return errResult(err)
	}
		s.audit(ctx, "report_abuse", args.DeliberationID, "")
return textResult("abuse report filed — thank you"), nil, nil
}

// --- Helpers ---

// scopeAgentID prefixes an agent ID with the caller's key namespace.
// Admin callers (no key_id) pass through unscoped.
func scopeAgentID(ctx context.Context, agentID string) string {
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	if keyID == "" {
		return agentID // admin or dev mode — no scoping
	}
	return keyID + ":" + agentID
}

// unscopeAgentID strips the key namespace prefix, returning the agent's local name.
func unscopeAgentID(ctx context.Context, scopedID string) string {
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)
	if keyID == "" {
		return scopedID
	}
	prefix := keyID + ":"
	if strings.HasPrefix(scopedID, prefix) {
		return scopedID[len(prefix):]
	}
	return scopedID // different namespace — show full ID
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
