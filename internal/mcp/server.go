package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	// nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used -- non-crypto: only used for display-order shuffling of positions (get_positions), never for secrets/codes
	mrand "math/rand"
	"os"
	"strings"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
	"github.com/justinstimatze/gemot/internal/payments"
	"github.com/justinstimatze/gemot/internal/principal"
	"github.com/justinstimatze/gemot/internal/store"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var logWriter io.Writer = os.Stderr

type server struct {
	svc      *deliberation.Service
	credits  *payments.CreditStore // nil in demo mode (no DB) — handlers must nil-check before use
	db       store.Backend         // *store.DB in production, *store.MemoryStore in demo mode
	shutdown context.Context       // server lifetime context — cancelled on shutdown
	// analyzeLimiter caps analyze:run invocations per API key to bound
	// LLM-call rate independent of the credit system. A well-funded
	// account could otherwise burn through its credits in seconds and
	// saturate the upstream Anthropic quota for every other user.
	// Set by RunHTTP; nil when running via `serve` (stdio, trusted
	// single agent) where rate limiting adds no value.
	analyzeLimiter *payments.RateLimiter
	// mppCfg is the MPP payment config used by the MCP transport layer
	// to emit -32042 challenges and verify _meta credentials. Enabled
	// when both STRIPE_SECRET_KEY and STRIPE_PROFILE_ID are set.
	mppCfg payments.Config
	// sandboxQuota throttles unauthenticated calls across all paid analyze
	// actions (run, propose_compromise, expert_panel, follow_up). When a
	// sandbox identity (IP) exceeds the daily limit, handlers respond with
	// -32042 challenges so the agent can fund via credits or MPP.
	sandboxQuota *payments.SandboxQuota
	// gate funds account:buy_credits over the ATXP/x402 rail (the CreditGate
	// seam). nil ⇒ buy_credits is not configured and refuses. Set at the
	// composition root (RunHTTP) when a merchant is available.
	gate payments.CreditGate
}

// RunAnalysisAsync starts an analysis in a background goroutine with proper
// context management, credit refunding on failure, and job tracking.
// Shared between MCP and A2A handlers to avoid divergent code paths.
func RunAnalysisAsync(svc *deliberation.Service, db store.Backend, credits *payments.CreditStore, deliberationID, model, apiKey string, creditCost int, opts ...func(context.Context) context.Context) {
	analyzeCtx, analyzeCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	if model != "" {
		analyzeCtx = context.WithValue(analyzeCtx, llm.ContextKeyModel{}, model)
	}
	for _, opt := range opts {
		analyzeCtx = opt(analyzeCtx)
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
				svc.ResetAnalyzingStatus(context.Background(), deliberationID)
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
const Version = "0.13.1"

// newServer creates an MCP server with 7 grouped tools.
func newServer(s *server) *sdkmcp.Server {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "gemot",
		Version: Version,
	}, &sdkmcp.ServerOptions{
		Instructions: serverInstructions,
	})

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: "deliberation",
		Description: `Manage deliberations. Actions:
- create: Create a new deliberation (topic, description, template, group_id, deadline_minutes, rules, visibility, max_participants, type, principal_policy, signature_policy)
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
- submit_position: Submit your position (deliberation_id, agent_id, content; optional: model_family, group, conviction, reservation, on_behalf_of, interests, draft, metadata, signature, principal_credential)
- publish_position: Publish a draft position (position_id)
- vote: Vote on a position — value: -2=strongly_disagree, -1=disagree_with_caveats, 0=mixed, 1=agree_with_caveats, 2=strongly_agree (deliberation_id, agent_id, position_id, value; optional: qualifier, caveat, criterion_id, signature)
- get_positions: Get all positions (deliberation_id; optional: round, exclude_agent_id, group, shuffle)
- get_context: Get your personal context — cluster, allies, cruxes (deliberation_id, agent_id)
- withdraw: Withdraw from a deliberation (deliberation_id, agent_id)
- register_key: Register a base64 ed25519 public key for this agent (agent_id, public_key; optional: algo)
- revoke_key: Revoke this agent's active signing key (agent_id)`,
	}, s.handleParticipate)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: "analyze",
		Description: `Analyze disagreements and find common ground. Actions:
- run: Trigger analysis — extracts cruxes, clusters, consensus (deliberation_id; optional: model)
- get_result: Get analysis result (deliberation_id; optional: round). While the analysis is running, returns {status:"pending", analysis_status: <stage>} with the current pipeline stage (taxonomy / extracting / crux_detection / clustering) so a single poll loop on this tool replaces calling deliberation action:get for progress.
- cancel: Cancel in-progress analysis (deliberation_id)
- propose_compromise: Generate a compromise statement (deliberation_id; optional: model)
- reframe: Restate a position emphasizing common ground (deliberation_id, position_id; optional: model)
- challenge: Challenge an analysis result (deliberation_id, agent_id, reason)
- dispute_crux: Dispute a crux classification (deliberation_id, agent_id, crux_claim, correction)
- expert_panel: Run an adversarial expert panel review (document; optional: topic, source_type, depth, experts, group_id, model). Creates a deliberation, submits expert critiques, triggers analysis. Returns deliberation_id immediately — poll with deliberation action:get for status, then analyze action:get_result. depth: "quick" (~2 min, 3 experts, tight taxonomy) or "thorough" (~7 min, 5 experts, full taxonomy). source_type selects specialized experts: "code_review", "architecture", "experiment", "proposal".
- follow_up: Submit follow-up expert positions responding to round 1 cruxes, then trigger round 2 analysis (deliberation_id; optional: model). Experts review the cruxes and consensus, flag misclassifications, and identify missed issues. Requires round 1 to be complete.`,
	}, s.handleAnalyzeTool)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: "decide",
		Description: `Commitments and reputation tracking. Actions:
- commit: Commit to a deliberation outcome (deliberation_id, agent_id, statement; optional: conditional)
- get_commitments: Get all commitments (deliberation_id)
- fulfill: Mark a commitment as fulfilled (commitment_id; optional: verified_by) — caller must be a participant in the commitment's deliberation, and cannot be the agent that made it
- break: Mark a commitment as broken (commitment_id, reason; optional: verified_by) — caller must be a participant in the commitment's deliberation; the committing agent may break its own
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
- get_audit_log: Get audit trail incl. tamper-evident log with proofs (deliberation_id)
- list_templates: List available governance templates
- get_votes: Get all votes (deliberation_id)
- get_vote_state: Get your own agent's recorded votes to confirm they landed and whether each is marked relayed vs direct (deliberation_id, agent_id)
- replica_pubkey: Get the server's BLS public key for offline proof verification`,
	}, s.handleAdmin)

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: "account",
		Description: `Fund your gemot credit balance. Actions:
- buy_credits: Top up THIS API key's balance over the ATXP/x402 rail (optional: pack, atxp_account_id, payment_credential). Call once with no payment_credential to receive a payment-required challenge (the x402 accepts to pay against), then call again with payment_credential set to the base64 X-PAYMENT settle credential. Credits are added only on a settled, on-chain-confirmed charge.`,
	}, s.handleAccount)

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
	// PrincipalPolicy governs whether on_behalf_of claims must be backed by a
	// verified delegation credential: none | advisory | required.
	PrincipalPolicy string `json:"principal_policy,omitempty"`
	// SignaturePolicy governs whether agents with a registered key must sign
	// their positions and votes: none | advisory | required.
	SignaturePolicy string `json:"signature_policy,omitempty"`
	DeadlineMinutes int    `json:"deadline_minutes,omitempty"`
	DeliberationID  string `json:"deliberation_id,omitempty"`
	AgentID         string `json:"agent_id,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	Offset          int    `json:"offset,omitempty"`
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
	// PrincipalCredential is the signed delegation attestation backing
	// on_behalf_of. Spelled as an explicit struct rather than a free-form
	// object so the derived JSON schema is concrete — strict validators
	// reject the wildcard `{}` an `any`-typed field would produce.
	PrincipalCredential *principalCredentialParam `json:"principal_credential,omitempty"`
	Draft               bool                      `json:"draft,omitempty"`
	Metadata            map[string]any            `json:"metadata,omitempty"`
	PositionID          string                    `json:"position_id,omitempty"`
	// Value is intentionally typed `int` so the auto-derived JSON schema
	// emits {"type":"integer"} and not {} (the wildcard that strict zod
	// validators — Glama's, for one — reject as `Invalid input`). The A2A
	// path keeps `coerceVoteValue(any)` for backwards compatibility with
	// callers sending the string forms ("strongly_agree" etc.); MCP
	// callers use the documented integer form -2..+2.
	Value          int     `json:"value,omitempty"`
	Qualifier      string  `json:"qualifier,omitempty"`
	Caveat         string  `json:"caveat,omitempty"`
	CriterionID    string  `json:"criterion_id,omitempty"`
	ExcludeAgentID *string `json:"exclude_agent_id,omitempty"`
	Round          *int    `json:"round,omitempty"`
	Shuffle        *bool   `json:"shuffle,omitempty"`
	// Signature is a base64-encoded ed25519 signature over the canonical
	// position/vote payload (see internal/auth). Verified against the agent's
	// registered public key when signature_policy != "none".
	Signature string `json:"signature,omitempty"`
	// PublicKey (base64, ed25519, 32 bytes) is used by action:register_key.
	PublicKey string `json:"public_key,omitempty"`
	Algo      string `json:"algo,omitempty"`
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
	ResultJSON     string `json:"result_json,omitempty"` // for update_result: full JSON of modified analysis result
	// expert_panel fields
	Document   string `json:"document,omitempty"`
	Experts    string `json:"experts,omitempty"`
	Topic      string `json:"topic,omitempty"`
	GroupID    string `json:"group_id,omitempty"`
	SourceType string `json:"source_type,omitempty"`
	Depth      string `json:"depth,omitempty"` // "quick" or "thorough" (default: thorough)
	// forced-choice compromise: when Options is non-empty, propose_compromise
	// selects exactly one of them instead of synthesizing free text.
	Options     []string       `json:"options,omitempty"`
	OptionVotes map[string]int `json:"option_votes,omitempty"`
}

type decideParams struct {
	Action                string `json:"action"`
	DeliberationID        string `json:"deliberation_id,omitempty"`
	AgentID               string `json:"agent_id,omitempty"`
	Statement             string `json:"statement,omitempty"`
	Conditional           string `json:"conditional,omitempty"`
	CommitmentID          string `json:"commitment_id,omitempty"`
	Reason                string `json:"reason,omitempty"`
	VerifiedBy            string `json:"verified_by,omitempty"`
	GroupID               string `json:"group_id,omitempty"`
	AccessorID            string `json:"accessor_id,omitempty"`
	Kind                  string `json:"kind,omitempty"`
	Note                  string `json:"note,omitempty"`
	DependentCommitmentID string `json:"dependent_commitment_id,omitempty"`
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
	AgentID        string `json:"agent_id,omitempty"`
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
		if args.PrincipalPolicy != "" {
			dopts = append(dopts, deliberation.WithPrincipalPolicy(args.PrincipalPolicy))
		}
		if args.SignaturePolicy != "" {
			dopts = append(dopts, deliberation.WithSignaturePolicy(args.SignaturePolicy))
		}
		if args.DeadlineMinutes > 0 {
			deadline := time.Now().Add(time.Duration(args.DeadlineMinutes) * time.Minute)
			dopts = append(dopts, deliberation.WithDeadline(deadline))
		}
		if keyID != "" {
			dopts = append(dopts, deliberation.WithCreatorKey(keyID))
		}
		d, err := s.svc.CreateDeliberation(ctx, args.Topic, args.Description, dopts...)
		if err != nil {
			return errResult(err)
		}
		s.audit(ctx, "deliberation:create", d.ID, "")
		return jsonResultWithHints(d, "Next: participate action:submit_position to add your view, or share the deliberation_id with other agents.")

	case "get":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if err := s.svc.CheckAccess(ctx, args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		d, err := s.svc.GetDeliberation(ctx, args.DeliberationID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(d)

	case "list":
		deliberations, err := s.svc.ListDeliberations(ctx, args.Limit, args.Offset, keyID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(deliberations)

	case "list_by_group":
		delibs, err := CoreListByGroup(ctx, s.svc, args.GroupID, keyID, isAdmin, args.Limit, args.Offset)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(delibs)

	case "list_by_agent":
		delibs, err := CoreListByAgent(ctx, s.svc, args.AgentID, keyID, isAdmin, args.Limit, args.Offset)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(delibs)

	case "delete":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if err := s.svc.DeleteDeliberation(ctx, args.DeliberationID, keyID, isAdmin); err != nil {
			return errResult(err)
		}
		s.audit(ctx, "deliberation:delete", args.DeliberationID, "")
		return textResult("deliberation deleted"), nil, nil

	case "set_template":
		if args.DeliberationID == "" || args.Template == "" {
			return errResult(fmt.Errorf("deliberation_id and template are required"))
		}
		if err := s.svc.SetTemplate(ctx, args.DeliberationID, args.Template, keyID); err != nil {
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
		export, err := CoreExportDeliberation(ctx, s.svc, args.DeliberationID, keyID, s.db)
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
		// Capture the client's unscoped agent_id before scoping. The signature
		// was computed over this form; the server stores the scoped form but
		// must reconstruct the canonical payload with the unscoped one.
		unscopedAgentID := args.AgentID
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		if err := s.svc.CheckAccess(ctx, args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		// Check position cost
		var posCost int
		var posApiKey string
		if !args.Draft {
			if d, err := s.svc.GetDeliberation(ctx, args.DeliberationID); err == nil {
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
		if args.PrincipalCredential != nil {
			credJSON, err := args.PrincipalCredential.toCredential()
			if err != nil {
				return errResult(err)
			}
			opts = append(opts, deliberation.WithPrincipalCredential(credJSON))
		}
		if args.Draft {
			opts = append(opts, deliberation.WithDraft())
		}
		if len(args.Metadata) > 0 {
			opts = append(opts, deliberation.WithMetadata(args.Metadata))
		}
		if args.Signature != "" {
			sigBytes, err := base64.StdEncoding.DecodeString(args.Signature)
			if err != nil {
				return errResult(fmt.Errorf("signature must be base64-encoded: %w", err))
			}
			opts = append(opts, deliberation.WithSignature(sigBytes))
		}
		p, err := s.svc.SubmitPositionWithSigningID(ctx, args.DeliberationID, args.AgentID, unscopedAgentID, args.Content, opts...)
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
		// Quorum hint: tell the agent if more participants are needed
		if err := s.svc.CheckQuorum(ctx, args.DeliberationID); err != nil {
			hint += " Note: " + err.Error() + "."
		}
		return jsonResultWithHints(p, hint)

	case "publish_position":
		if err := CorePublishPosition(ctx, s.svc, args.PositionID, keyID); err != nil {
			return errResult(err)
		}
		return textResult("position published"), nil, nil

	case "vote":
		if args.DeliberationID == "" || args.AgentID == "" || args.PositionID == "" {
			return errResult(fmt.Errorf("deliberation_id, agent_id, and position_id are required"))
		}
		unscopedVoteAgentID := args.AgentID
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		if err := s.svc.CheckAccess(ctx, args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		if args.Value < -2 || args.Value > 2 {
			return errResult(fmt.Errorf("vote value must be -2, -1, 0, 1, or 2 (got %d)", args.Value))
		}
		if args.Signature != "" {
			sigBytes, err := base64.StdEncoding.DecodeString(args.Signature)
			if err != nil {
				return errResult(fmt.Errorf("signature must be base64-encoded: %w", err))
			}
			if err := s.svc.SubmitSignedVoteWithSigningID(ctx, args.DeliberationID, args.AgentID, unscopedVoteAgentID, args.PositionID, args.Value, args.Qualifier, args.Caveat, args.CriterionID, sigBytes); err != nil {
				return errResult(err)
			}
		} else if err := s.svc.Vote(ctx, args.DeliberationID, args.AgentID, args.PositionID, args.Value, args.Qualifier, args.Caveat, args.CriterionID); err != nil {
			return errResult(err)
		}
		s.audit(ctx, "participate:vote", args.DeliberationID, args.AgentID)
		return textResult("vote recorded\n\n---\nNext: vote on more positions, or call analyze action:run when all votes are in."), nil, nil

	case "register_key":
		if args.AgentID == "" || args.PublicKey == "" {
			return errResult(fmt.Errorf("agent_id and public_key (base64) are required"))
		}
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		pubBytes, err := base64.StdEncoding.DecodeString(args.PublicKey)
		if err != nil {
			return errResult(fmt.Errorf("public_key must be base64-encoded: %w", err))
		}
		algo := args.Algo
		if algo == "" {
			algo = "ed25519"
		}
		if err := s.svc.RegisterAgentKey(ctx, args.AgentID, pubBytes, algo); err != nil {
			return errResult(err)
		}
		s.audit(ctx, "participate:register_key", "", args.AgentID)
		return textResult("public key registered — future signed positions and votes will be verified against it"), nil, nil

	case "revoke_key":
		if args.AgentID == "" {
			return errResult(fmt.Errorf("agent_id is required"))
		}
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		if err := s.svc.RevokeAgentKey(ctx, args.AgentID); err != nil {
			return errResult(err)
		}
		s.audit(ctx, "participate:revoke_key", "", args.AgentID)
		return textResult("public key revoked"), nil, nil

	case "get_positions":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if err := s.svc.CheckAccess(ctx, args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		positions, err := s.svc.GetPositions(ctx, args.DeliberationID, args.ExcludeAgentID, args.Round)
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
		if err := s.svc.CheckAccess(ctx, args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		actx, err := s.svc.GetContext(ctx, args.DeliberationID, args.AgentID)
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
		if err := CoreWithdraw(ctx, s.svc, args.DeliberationID, args.AgentID, keyID); err != nil {
			return errResult(err)
		}
		s.audit(ctx, "participate:withdraw", args.DeliberationID, args.AgentID)
		return textResult("agent withdrawn from deliberation"), nil, nil

	default:
		return errResult(fmt.Errorf("unknown action %q — use: submit_position, publish_position, vote, get_positions, get_context, withdraw", args.Action))
	}
}

func (s *server) handleAnalyzeTool(ctx context.Context, req *sdkmcp.CallToolRequest, args analyzeToolParams) (*sdkmcp.CallToolResult, any, error) {
	keyID, _ := ctx.Value(payments.ContextKeyKeyID{}).(string)

	switch args.Action {
	case "run":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if args.Model != "" && !llm.AllowedModels[args.Model] {
			return errResult(fmt.Errorf("unsupported model %q — allowed: claude-sonnet-4-6, claude-opus-4-6, claude-haiku-4-5", args.Model))
		}
		// Per-key rate limit BEFORE any funding consumption (credits OR
		// MPP credential). A rejected request must not charge the caller.
		// Nil limiter (stdio / trusted mode) is a pass-through.
		if s.analyzeLimiter != nil {
			bucket := keyID
			if bucket == "" {
				bucket = "anonymous"
			}
			if !s.analyzeLimiter.Allow("analyze:" + bucket) {
				return errResult(fmt.Errorf("rate limit exceeded: max 10 analyses per minute per API key — slow down or batch requests"))
			}
		}
		// Validate preconditions (existence, access, quorum) BEFORE any
		// charge. MPP-paid requests consume a one-time Stripe SPT;
		// refunding it requires a Stripe Refund API call we don't yet
		// wire. Doing the structural validation first means we never
		// consume a credential for a service we can't render. Both /mcp
		// and /a2a go through the same service-layer precondition
		// function so a guard added to one transport applies to the
		// other automatically.
		if err := s.svc.CheckAnalysisPreconditions(ctx, args.DeliberationID, keyID, true); err != nil {
			return errResult(err)
		}

		// MPP-over-MCP credential check. Scope binds the credential to this
		// specific call (tool, action, model, deliberation_id). A valid
		// credential bypasses the sandbox quota AND the credit deduction.
		runScope := payments.ChallengeScope{
			Tool:           "analyze",
			Action:         "run",
			Model:          resolveModel(args.Model),
			DeliberationID: args.DeliberationID,
		}
		mppReceipt, err := s.checkMPPCredential(ctx, req, runScope)
		if err != nil {
			return nil, nil, fmt.Errorf("MPP credential verification failed: %w", err)
		}

		// Unified sandbox quota: when no apiKey AND no MPP receipt, the
		// caller has 20 free calls/day across all paid analyze actions.
		// Replaces the prior "1 free analysis per deliberation" gate —
		// more generous on volume across deliberations, bounded per IP.
		apiKey, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)
		if mppReceipt == nil && apiKey == "" {
			if err := s.gateSandbox(ctx, runScope, fmt.Sprintf("analyze:run for deliberation %s (model %s)", args.DeliberationID, runScope.Model)); err != nil {
				return nil, nil, err
			}
		}
		var creditCost int
		// MPP receipt substitutes for credit deduction — the agent already
		// paid via Stripe SPT (or Tempo, when wired). No credits to deduct.
		if mppReceipt == nil && apiKey != "" && s.credits != nil {
			creditCost = payments.CreditCost(args.Model)
			if _, err := s.credits.Deduct(apiKey, creditCost); err != nil {
				balance, _ := s.credits.GetBalance(apiKey)
				return errResult(fmt.Errorf("insufficient credits: have %d, need %d — buy more at https://gemot.dev/pricing", balance, creditCost))
			}
		}
		s.audit(ctx, "analyze:run", args.DeliberationID, "")
		// Log MPP-paid runs with the Stripe PaymentIntent reference so accounting
		// and dispute resolution can correlate analyses to settlements — the
		// audit-log schema doesn't carry payment metadata.
		if mppReceipt != nil {
			slog.Info("MPP-paid analysis", "deliberation_id", args.DeliberationID, "payment_method", mppReceipt.Method, "payment_ref", mppReceipt.Reference)
		}
		RunAnalysisAsync(s.svc, s.db, s.credits, args.DeliberationID, args.Model, apiKey, creditCost)
		result := textResult(fmt.Sprintf(
			"Analysis started for deliberation %s. Poll analyze action:get_result — while in-progress it returns {status:\"pending\", analysis_status: taxonomy|extracting|crux_detection|clustering}; when done it returns the full result object.",
			args.DeliberationID,
		))
		// Attach MPP receipt to tool result per mpp.dev/protocol/transports/mcp.
		if mppReceipt != nil {
			result.Meta = payments.ReceiptMeta(mppReceipt)
		}
		return result, nil, nil

	case "get_result":
		// round:-1 returns all rounds as an array
		if args.Round != nil && *args.Round == -1 {
			results, err := CoreGetAllAnalysisResults(ctx, s.svc, args.DeliberationID, keyID)
			if err != nil {
				return errResult(err)
			}
			if len(results) == 0 {
				status, err := CoreGetAnalysisStatus(ctx, s.svc, args.DeliberationID)
				if err != nil {
					return errResult(err)
				}
				return jsonResult(status)
			}
			return jsonResult(results)
		}
		result, err := CoreGetAnalysisResult(ctx, s.svc, args.DeliberationID, keyID, args.Round)
		if err != nil {
			return errResult(err)
		}
		if result == nil {
			status, err := CoreGetAnalysisStatus(ctx, s.svc, args.DeliberationID)
			if err != nil {
				return errResult(err)
			}
			return jsonResult(status)
		}
		return jsonResult(result)

	case "update_result":
		if args.DeliberationID == "" || args.Round == nil {
			return errResult(fmt.Errorf("deliberation_id and round are required"))
		}
		if args.ResultJSON == "" {
			return errResult(fmt.Errorf("result_json is required"))
		}
		if err := s.svc.CheckAccess(ctx, args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		var updated deliberation.AnalysisResult
		if err := json.Unmarshal([]byte(args.ResultJSON), &updated); err != nil {
			return errResult(fmt.Errorf("invalid result_json: %w", err))
		}
		if err := s.svc.SaveAnalysisResult(ctx, args.DeliberationID, *args.Round, &updated); err != nil {
			return errResult(err)
		}
		return textResult(fmt.Sprintf("analysis result updated for round %d", *args.Round)), nil, nil

	case "cancel":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if err := CoreCancelAnalysis(ctx, s.svc, args.DeliberationID, keyID); err != nil {
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
		// Validate preconditions (existence + access; no quorum for
		// secondary actions) BEFORE MPP credential consumption — never
		// consume a credential for a service we can't render.
		// (Stripe SPT refund is not wired; mirrors analyze:run pattern.)
		if err := s.svc.CheckAnalysisPreconditions(ctx, args.DeliberationID, keyID, false); err != nil {
			return errResult(err)
		}
		proposeScope := payments.ChallengeScope{
			Tool:           "analyze",
			Action:         "propose_compromise",
			Model:          resolveModel(args.Model),
			DeliberationID: args.DeliberationID,
		}
		mppReceipt, err := s.checkMPPCredential(ctx, req, proposeScope)
		if err != nil {
			return nil, nil, fmt.Errorf("MPP credential verification failed: %w", err)
		}
		apiKey, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)
		if mppReceipt == nil && apiKey == "" {
			if err := s.gateSandbox(ctx, proposeScope, fmt.Sprintf("analyze:propose_compromise for deliberation %s (model %s)", args.DeliberationID, proposeScope.Model)); err != nil {
				return nil, nil, err
			}
		}
		var creditCost int
		if mppReceipt == nil && apiKey != "" && s.credits != nil {
			creditCost = payments.CreditCost(args.Model)
			if _, err := s.credits.Deduct(apiKey, creditCost); err != nil {
				balance, _ := s.credits.GetBalance(apiKey)
				return errResult(fmt.Errorf("insufficient credits: have %d, need %d", balance, creditCost))
			}
		}
		var proposal, selected string
		if len(args.Options) > 0 {
			proposal, selected, err = s.svc.ProposeCompromiseWithChoiceAndVotes(ctx, args.DeliberationID, args.Options, args.OptionVotes)
		} else {
			proposal, err = s.svc.ProposeCompromise(ctx, args.DeliberationID)
		}
		if err != nil {
			if apiKey != "" && creditCost > 0 && s.credits != nil {
				_, _ = s.credits.AddCredits(apiKey, creditCost)
			}
			s.refundSandboxQuota(ctx, mppReceipt, apiKey)
			return errResult(err)
		}
		s.audit(ctx, "analyze:propose_compromise", args.DeliberationID, "")
		if mppReceipt != nil {
			slog.Info("MPP-paid propose_compromise", "deliberation_id", args.DeliberationID, "payment_ref", mppReceipt.Reference)
		}
		result, _, _ := jsonResult(map[string]string{
			"deliberation_id":     args.DeliberationID,
			"compromise_proposal": proposal,
			"selected_option":     selected,
		})
		if mppReceipt != nil {
			result.Meta = payments.ReceiptMeta(mppReceipt)
		}
		return result, nil, nil

	case "reframe":
		// Same MPP/sandbox gate every other paid analyze action gets — this
		// one used to skip straight to CoreReframe, letting an
		// unauthenticated caller (no apiKey, no MPP credential) trigger a
		// real LLM call for free, bounded only by the generic IP rate
		// limit instead of the 20/day sandbox quota.
		reframeScope := payments.ChallengeScope{
			Tool:           "analyze",
			Action:         "reframe",
			Model:          resolveModel(args.Model),
			DeliberationID: args.DeliberationID,
		}
		mppReceipt, err := s.checkMPPCredential(ctx, req, reframeScope)
		if err != nil {
			return nil, nil, fmt.Errorf("MPP credential verification failed: %w", err)
		}
		apiKey, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)
		if mppReceipt == nil && apiKey == "" {
			if err := s.gateSandbox(ctx, reframeScope, fmt.Sprintf("analyze:reframe for deliberation %s (model %s)", args.DeliberationID, reframeScope.Model)); err != nil {
				return nil, nil, err
			}
		}
		result, err := CoreReframe(ctx, s.svc, s.credits, args.DeliberationID, args.PositionID, args.Model, keyID, false, apiKey)
		if err != nil {
			s.refundSandboxQuota(ctx, mppReceipt, apiKey)
			return errResult(err)
		}
		res, _, resErr := jsonResult(result)
		if mppReceipt != nil && res != nil {
			res.Meta = payments.ReceiptMeta(mppReceipt)
		}
		return res, nil, resErr

	case "challenge":
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		result, err := CoreChallengeAnalysis(ctx, s.svc, args.DeliberationID, args.AgentID, args.Reason, keyID)
		if err != nil {
			return errResult(err)
		}
		return textResult(result["status"] + ". " + result["detail"]), nil, nil

	case "dispute_crux":
		if args.DeliberationID == "" || args.AgentID == "" || args.CruxClaim == "" || args.Correction == "" {
			return errResult(fmt.Errorf("deliberation_id, agent_id, crux_claim, and correction are required"))
		}
		args.AgentID = scopeAgentID(ctx, args.AgentID)
		d, err := s.svc.DisputeCrux(ctx, args.DeliberationID, args.AgentID, args.CruxClaim, args.Correction)
		if err != nil {
			return errResult(err)
		}
		s.audit(ctx, "analyze:dispute_crux", args.DeliberationID, args.AgentID)
		return jsonResult(d)

	case "expert_panel":
		if args.Document == "" {
			return errResult(fmt.Errorf("document is required — pass the content to review"))
		}
		if args.Model != "" && !llm.AllowedModels[args.Model] {
			return errResult(fmt.Errorf("unsupported model %q — allowed: claude-sonnet-4-6, claude-opus-4-6, claude-haiku-4-5", args.Model))
		}
		// expert_panel doesn't have a deliberation_id at issue time — the
		// panel CREATES the deliberation. Scope binds tool/action/model
		// only; deliberation_id stays unbound (empty).
		panelScope := payments.ChallengeScope{
			Tool:   "analyze",
			Action: "expert_panel",
			Model:  resolveModel(args.Model),
		}
		mppReceipt, err := s.checkMPPCredential(ctx, req, panelScope)
		if err != nil {
			return nil, nil, fmt.Errorf("MPP credential verification failed: %w", err)
		}
		apiKey, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)
		if mppReceipt == nil && apiKey == "" {
			if err := s.gateSandbox(ctx, panelScope, fmt.Sprintf("analyze:expert_panel (model %s)", panelScope.Model)); err != nil {
				return nil, nil, err
			}
		}
		var creditCost int
		if mppReceipt == nil && apiKey != "" && s.credits != nil {
			creditCost = payments.CreditCost(args.Model)
			if _, err := s.credits.Deduct(apiKey, creditCost); err != nil {
				balance, _ := s.credits.GetBalance(apiKey)
				return errResult(fmt.Errorf("insufficient credits: have %d, need %d — buy more at https://gemot.dev/pricing", balance, creditCost))
			}
		}
		s.audit(ctx, "analyze:expert_panel", "", "")
		result, err := CoreRunExpertPanel(ctx, s.svc, args.Document, args.Topic, args.Experts, args.GroupID, args.Model, keyID, args.SourceType, args.Depth)
		if err != nil {
			if creditCost > 0 && s.credits != nil {
				_, _ = s.credits.AddCredits(apiKey, creditCost)
			}
			s.refundSandboxQuota(ctx, mppReceipt, apiKey)
			return errResult(err)
		}
		// Trigger analysis async — panel creation and position submission are already done
		// Expert panels use interactive priority (reserved API slots)
		s.audit(ctx, "analyze:expert_panel", result.DeliberationID, "")
		if mppReceipt != nil {
			slog.Info("MPP-paid expert_panel", "deliberation_id", result.DeliberationID, "payment_ref", mppReceipt.Reference)
		}
		RunAnalysisAsync(s.svc, s.db, s.credits, result.DeliberationID, result.Model, apiKey, creditCost,
			func(c context.Context) context.Context {
				return context.WithValue(c, llm.ContextKeyInteractive{}, true)
			})
		hintResult, _, _ := jsonResultWithHints(result,
			fmt.Sprintf("Panel created with %d experts. Analysis started — poll deliberation action:get (deliberation_id: %s) for status, then analyze action:get_result for results.",
				result.ExpertCount, result.DeliberationID))
		if mppReceipt != nil {
			hintResult.Meta = payments.ReceiptMeta(mppReceipt)
		}
		return hintResult, nil, nil

	case "follow_up":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required — pass the deliberation from a previous expert_panel"))
		}
		if args.Model != "" && !llm.AllowedModels[args.Model] {
			return errResult(fmt.Errorf("unsupported model %q", args.Model))
		}
		// Validate preconditions (existence + access; no quorum for
		// secondary actions) BEFORE MPP credential consumption — never
		// consume a credential for a service we can't render.
		if err := s.svc.CheckAnalysisPreconditions(ctx, args.DeliberationID, keyID, false); err != nil {
			return errResult(err)
		}
		followUpScope := payments.ChallengeScope{
			Tool:           "analyze",
			Action:         "follow_up",
			Model:          resolveModel(args.Model),
			DeliberationID: args.DeliberationID,
		}
		mppReceipt, err := s.checkMPPCredential(ctx, req, followUpScope)
		if err != nil {
			return nil, nil, fmt.Errorf("MPP credential verification failed: %w", err)
		}
		apiKey, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)
		if mppReceipt == nil && apiKey == "" {
			if err := s.gateSandbox(ctx, followUpScope, fmt.Sprintf("analyze:follow_up for deliberation %s (model %s)", args.DeliberationID, followUpScope.Model)); err != nil {
				return nil, nil, err
			}
		}
		var creditCost int
		if mppReceipt == nil && apiKey != "" && s.credits != nil {
			creditCost = payments.CreditCost(args.Model)
			if _, err := s.credits.Deduct(apiKey, creditCost); err != nil {
				balance, _ := s.credits.GetBalance(apiKey)
				return errResult(fmt.Errorf("insufficient credits: have %d, need %d", balance, creditCost))
			}
		}
		result, err := CoreFollowUpExpertPanel(ctx, s.svc, args.DeliberationID, args.Model, keyID)
		if err != nil {
			if creditCost > 0 && s.credits != nil {
				_, _ = s.credits.AddCredits(apiKey, creditCost)
			}
			s.refundSandboxQuota(ctx, mppReceipt, apiKey)
			return errResult(err)
		}
		s.audit(ctx, "analyze:follow_up", result.DeliberationID, "")
		if mppReceipt != nil {
			slog.Info("MPP-paid follow_up", "deliberation_id", result.DeliberationID, "payment_ref", mppReceipt.Reference)
		}
		RunAnalysisAsync(s.svc, s.db, s.credits, result.DeliberationID, result.Model, apiKey, creditCost,
			func(c context.Context) context.Context {
				return context.WithValue(c, llm.ContextKeyInteractive{}, true)
			})
		hintResult, _, _ := jsonResultWithHints(result,
			fmt.Sprintf("Follow-up round started with %d experts. Poll deliberation action:get for status, then analyze action:get_result.",
				result.ExpertCount))
		if mppReceipt != nil {
			hintResult.Meta = payments.ReceiptMeta(mppReceipt)
		}
		return hintResult, nil, nil

	default:
		return errResult(fmt.Errorf("unknown action %q — use: run, get_result, cancel, propose_compromise, reframe, challenge, dispute_crux, expert_panel, follow_up", args.Action))
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
		c, err := s.svc.Commit(ctx, args.DeliberationID, args.AgentID, args.Statement, args.Conditional)
		if err != nil {
			return errResult(err)
		}
		s.audit(ctx, "decide:commit", args.DeliberationID, args.AgentID)
		return jsonResult(c)

	case "get_commitments":
		commitments, err := CoreGetCommitments(ctx, s.svc, args.DeliberationID, keyID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(commitments)

	case "fulfill":
		verifiedBy := args.VerifiedBy
		if verifiedBy == "" {
			verifiedBy = keyID
		}
		if err := CoreFulfillCommitment(ctx, s.svc, args.CommitmentID, verifiedBy, keyID); err != nil {
			return errResult(err)
		}
		return textResult("commitment fulfilled"), nil, nil

	case "break":
		verifiedBy := args.VerifiedBy
		if verifiedBy == "" {
			verifiedBy = keyID
		}
		if err := CoreBreakCommitment(ctx, s.svc, args.CommitmentID, args.Reason, verifiedBy, keyID); err != nil {
			return errResult(err)
		}
		return textResult("commitment marked as broken"), nil, nil

	case "reputation":
		rep, err := CoreAgentReputation(ctx, s.svc, args.AgentID, args.GroupID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(rep)

	case "record_access":
		accessor := args.AccessorID
		if accessor == "" {
			accessor = args.AgentID
		}
		accessor = scopeAgentID(ctx, accessor)
		a, err := CoreRecordAccess(ctx, s.svc, args.CommitmentID, accessor, args.Kind, args.Note, args.DependentCommitmentID, keyID)
		if err != nil {
			return errResult(err)
		}
		s.audit(ctx, "decide:access", "", accessor)
		return jsonResult(a)

	case "get_signals":
		sig, err := CoreCommitmentSignals(ctx, s.svc, args.CommitmentID, keyID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(sig)

	default:
		return errResult(fmt.Errorf("unknown action %q — use: commit, get_commitments, fulfill, break, reputation, record_access, get_signals", args.Action))
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
		d, err := s.svc.Delegate(ctx, args.DeliberationID, args.FromAgent, args.ToAgent, args.Scope)
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
		inv, err := s.svc.InviteAgent(ctx, args.DeliberationID, args.InvitedBy, args.InvitedAgent, args.Role, args.Reason)
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
		jc, err := s.svc.GenerateJoinCode(ctx, args.DeliberationID, args.Role, ttl)
		if err != nil {
			return errResult(err)
		}
		return jsonResultWithHints(jc, "Share this code with the agent you want to join. They call coordinate action:join with this code. Code expires in "+ttl.String()+".")

	case "join":
		if args.Code == "" || args.AgentID == "" {
			return errResult(fmt.Errorf("code and agent_id are required"))
		}
		deliberationID, role, err := s.svc.JoinDeliberation(ctx, args.Code, args.AgentID)
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
		if err := s.svc.ReportAbuse(ctx, args.DeliberationID, keyID, args.Reason); err != nil {
			return errResult(err)
		}
		s.audit(ctx, "admin:report_abuse", args.DeliberationID, "")
		return textResult("abuse report filed — thank you"), nil, nil

	case "get_audit_log":
		if args.DeliberationID == "" {
			return errResult(fmt.Errorf("deliberation_id is required"))
		}
		if err := s.svc.CheckAccess(ctx, args.DeliberationID, keyID); err != nil {
			return errResult(err)
		}
		// s.db is nil only in stdio mode (mcp.Run), which doesn't wire a
		// backend. The other two log surfaces fall back to nil/empty so
		// the response shape stays consistent.
		var opLog []map[string]string
		var analysisAudit []deliberation.AuditEntry
		if s.db != nil {
			if log, err := s.db.GetAuditLog(args.DeliberationID, 50); err == nil {
				opLog = log
			}
			if result, err := s.db.GetLatestAnalysisResult(ctx, args.DeliberationID); err == nil && result != nil {
				analysisAudit = result.AuditLog
			}
		}
		// Tamper-evident log: every write action is ordered through
		// a BFT state machine, so a client holding this log can
		// verify the server didn't retroactively edit history.
		tamperEvident, tErr := s.svc.GetTamperEvidentLog(ctx, args.DeliberationID)
		if tErr != nil {
			tamperEvident = nil
		}
		return jsonResult(map[string]any{
			"operations":         opLog,
			"analysis_decisions": analysisAudit,
			"tamper_evident_log": tamperEvident,
		})

	case "list_templates":
		return jsonResult(deliberation.ListTemplates())

	case "replica_pubkey":
		pub, err := s.svc.ReplicaPublicKey()
		if err != nil {
			return errResult(err)
		}
		return jsonResult(map[string]any{
			"public_key_hex": fmt.Sprintf("%x", pub),
			"algorithm":      "bls12-381-g2",
			"usage":          "verify `proof` fields in get_audit_log's tamper_evident_log",
		})

	case "get_votes":
		votes, err := CoreGetVotes(ctx, s.svc, args.DeliberationID, keyID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(votes)

	case "get_vote_state":
		agentID := scopeAgentID(ctx, args.AgentID)
		votes, err := CoreGetVoteState(ctx, s.svc, args.DeliberationID, agentID, keyID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(votes)

	default:
		return errResult(fmt.Errorf("unknown action %q — use: report_abuse, get_audit_log, list_templates, get_votes, get_vote_state, replica_pubkey", args.Action))
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
		case "strongly_agree", "+2", "2":
			return 2, nil
		case "agree_with_caveats", "agree", "1", "+1":
			return 1, nil
		case "mixed", "0":
			return 0, nil
		case "disagree_with_caveats", "disagree", "-1":
			return -1, nil
		case "strongly_disagree", "-2":
			return -2, nil
		default:
			return 0, fmt.Errorf("invalid vote value %q: must be -2..2 or a stance string (strongly_agree, agree_with_caveats, mixed, disagree_with_caveats, strongly_disagree)", val)
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

// checkMPPCredential verifies an MPP credential from the tool call _meta
// against the given scope. Returns (nil, nil) when MPP is disabled or no
// credential present — caller should fall through to credits/sandbox path.
// Returns (receipt, nil) on success — caller skips credit deduction.
// Returns (nil, err) on verification failure — caller surfaces the error.
func (s *server) checkMPPCredential(ctx context.Context, req *sdkmcp.CallToolRequest, scope payments.ChallengeScope) (*payments.Receipt, error) {
	if !s.mppCfg.Enabled || req == nil || req.Params == nil {
		return nil, nil
	}
	return payments.VerifyMCPCredential(ctx, s.mppCfg, req.Params.GetMeta(), scope)
}

// gateSandbox is the unified daily quota gate for paid analyze actions.
// Pre-condition: caller has determined this is a sandbox call (no apiKey,
// no valid MPP credential). Returns nil if the call may proceed (quota
// counter is incremented as a side effect). Returns *jsonrpc.Error with
// -32042 + scope-bound challenges when MPP is enabled and quota is
// exhausted; returns a plain error otherwise.
func (s *server) gateSandbox(ctx context.Context, scope payments.ChallengeScope, description string) error {
	// No quota configured (a bare/local instance with no MPP/payments
	// subsystem): there is nothing to meter, so the call proceeds free. This
	// guards a nil-pointer panic that otherwise crashes the server on every
	// paid action when running locally without payments wired.
	if s.sandboxQuota == nil {
		return nil
	}
	ip, _ := ctx.Value(payments.ContextKeyClientIP{}).(string)
	if allowed, _ := s.sandboxQuota.Allow(ip); allowed {
		return nil
	}
	if s.mppCfg.Enabled {
		return payments.PaymentRequiredError(s.mppCfg, scope, description)
	}
	return fmt.Errorf("sandbox daily quota exceeded (20 calls/day) — fund credits at https://gemot.dev/pricing or pay per-call via MPP")
}

// resolveModel returns the model name to use for scope binding and pricing.
// Empty args.Model means "server default" — at the LLM call layer this
// becomes Sonnet (or whatever GEMOT_MODEL is set to). For MPP scope binding
// we resolve to "claude-sonnet-4-6" so the credential is bound to a
// concrete model rather than the empty string. The actual downstream LLM
// call honors args.Model verbatim; we don't propagate the resolution.
func resolveModel(model string) string {
	if model == "" {
		return "claude-sonnet-4-6"
	}
	return model
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

// principalCredentialParam is the wire form of a delegation attestation
// (see internal/principal). Timestamps are RFC3339 and the signature is
// base64 so the whole credential survives JSON transport.
//
// Re-encoding this into the canonical signed bytes is safe: the principal
// signs a fixed-shape length-prefixed record, never the JSON, so field order,
// whitespace, and Unicode normalization in transit cannot change what verifies.
type principalCredentialParam struct {
	Principal string `json:"principal"`
	Agent     string `json:"agent"`
	// AgentKey is the base64 ed25519 public key the credential is bound to.
	// The submitting agent must have this key registered and must sign the
	// position with it, or the credential is refused.
	AgentKey  string `json:"agent_key"`
	Scope     string `json:"scope,omitempty"`
	Issuer    string `json:"issuer,omitempty"`
	ExpiresAt string `json:"expires_at"`
	Signature string `json:"signature"`
}

// toCredential converts the wire form into a storable credential, returning the
// JSON encoding the service layer persists and re-verifies against.
func (p *principalCredentialParam) toCredential() ([]byte, error) {
	expires, err := time.Parse(time.RFC3339, p.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("principal_credential.expires_at must be RFC3339: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(p.Signature)
	if err != nil {
		return nil, fmt.Errorf("principal_credential.signature must be base64-encoded: %w", err)
	}
	agentKey, err := base64.StdEncoding.DecodeString(p.AgentKey)
	if err != nil {
		return nil, fmt.Errorf("principal_credential.agent_key must be base64-encoded: %w", err)
	}
	return json.Marshal(principal.Credential{
		Principal: p.Principal,
		Agent:     p.Agent,
		AgentKey:  agentKey,
		Scope:     p.Scope,
		Issuer:    p.Issuer,
		ExpiresAt: expires,
		Signature: sig,
	})
}

// APIVersion is gemot's REST/JSON-RPC API contract version, date-stamped
// Stripe-style — independent of Version (the software release, which bumps
// on every deploy). APIVersion only advances when the request/response
// shape of a documented endpoint changes in a way a client might need to
// react to. Sent on every response via the Gemot-Version header, giving
// agents a versioning signal without URL-path versioning (/v1/, /v2/),
// which would break every already-deployed MCP config pointed at /mcp.
// Deprecation policy: a breaking change is announced in CHANGELOG.md and
// the old shape stays live for at least 90 days, during which responses
// from the deprecated path carry Deprecation: true and a Sunset header
// (RFC 8594) naming the removal date.
const APIVersion = "2026-08-25"

// refundSandboxQuota returns the daily sandbox call gateSandbox already
// consumed for this request, but only when this was actually a sandbox
// call (no apiKey, no MPP receipt) -- matching gateSandbox's own
// precondition exactly, so a paying caller's credit/MPP accounting is
// never touched here.
//
// Call this from the same downstream-failure branch that already refunds
// credits via AddCredits: a failed paid action must not permanently cost
// an unauthenticated caller one of their 20 free calls/day any more than it
// should permanently cost a paying caller their credits.
func (s *server) refundSandboxQuota(ctx context.Context, mppReceipt *payments.Receipt, apiKey string) {
	if s.sandboxQuota == nil || mppReceipt != nil || apiKey != "" {
		return
	}
	ip, _ := ctx.Value(payments.ContextKeyClientIP{}).(string)
	s.sandboxQuota.Refund(ip)
}
