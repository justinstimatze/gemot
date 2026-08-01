package mcp

// core.go contains shared business logic called by both MCP and A2A handlers.
// This prevents bugs from diverging code paths — the transport layers are thin
// wrappers that extract params and format responses.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/calibration"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
	"github.com/justinstimatze/gemot/internal/payments"
)

// CoreGetCommitments returns all commitments for a deliberation after access check.
func CoreGetCommitments(ctx context.Context, svc *deliberation.Service, deliberationID, keyID string) ([]deliberation.Commitment, error) {
	if deliberationID == "" {
		return nil, fmt.Errorf("deliberation_id is required")
	}
	if err := svc.CheckAccess(ctx, deliberationID, keyID); err != nil {
		return nil, err
	}
	return svc.GetCommitments(ctx, deliberationID)
}

// CorePublishPosition publishes a draft position after verifying ownership.
func CorePublishPosition(ctx context.Context, svc *deliberation.Service, positionID, keyID string) error {
	if positionID == "" {
		return fmt.Errorf("position_id is required")
	}
	if keyID != "" {
		pos, err := svc.GetPositionByID(ctx, positionID)
		if err != nil {
			return fmt.Errorf("position not found")
		}
		if !strings.HasPrefix(pos.AgentID, keyID+":") {
			return fmt.Errorf("access denied: you can only publish your own positions")
		}
	}
	return svc.PublishPosition(ctx, positionID)
}

// CoreChallengeAnalysis files a full analysis challenge as a dispute.
func CoreChallengeAnalysis(ctx context.Context, svc *deliberation.Service, deliberationID, agentID, reason, keyID string) (map[string]string, error) {
	if deliberationID == "" || agentID == "" || reason == "" {
		return nil, fmt.Errorf("deliberation_id, agent_id, and reason are required")
	}
	if err := svc.CheckAccess(ctx, deliberationID, keyID); err != nil {
		return nil, err
	}
	if _, err := svc.DisputeCrux(ctx, deliberationID, agentID, "[FULL ANALYSIS CHALLENGE]", reason); err != nil {
		return nil, err
	}
	return map[string]string{
		"status": "analysis challenged by " + agentID,
		"detail": "Challenge recorded as integrity warning. Call analyze to trigger re-analysis.",
	}, nil
}

// CoreReframe reframes a position with credit handling.
func CoreReframe(ctx context.Context, svc *deliberation.Service, credits *payments.CreditStore, deliberationID, positionID, model, keyID string, isAdmin bool, apiKey string) (map[string]string, error) {
	if deliberationID == "" || positionID == "" {
		return nil, fmt.Errorf("deliberation_id and position_id are required")
	}
	if err := svc.CheckAccess(ctx, deliberationID, keyID); err != nil {
		return nil, err
	}
	if model != "" && !llm.AllowedModels[model] {
		return nil, fmt.Errorf("unsupported model %q", model)
	}

	// Deduct credits
	var creditCost int
	if !isAdmin && credits != nil && apiKey != "" && strings.HasPrefix(apiKey, "gmt_") {
		creditCost = payments.CreditCost(model)
		if _, err := credits.Deduct(apiKey, creditCost); err != nil {
			balance, _ := credits.GetBalance(apiKey)
			return nil, fmt.Errorf("insufficient credits: have %d, need %d", balance, creditCost)
		}
	}

	if model != "" {
		ctx = context.WithValue(ctx, llm.ContextKeyModel{}, model)
	}
	reframed, err := svc.ReframePosition(ctx, deliberationID, positionID)
	if err != nil {
		if creditCost > 0 && credits != nil && apiKey != "" {
			credits.AddCredits(apiKey, creditCost) //nolint:errcheck
		}
		return nil, err
	}
	return map[string]string{
		"original_position_id": positionID,
		"reframed":             reframed,
	}, nil
}

// CoreGetAnalysisResult returns an analysis result for a deliberation.
// If round is non-nil, returns that specific round; otherwise returns the latest.
//
// The result includes a `calibration` field when the deliberation's type
// matches a reference class in the embedded calibration run with
// sufficient n. The field is absent (nil pointer, json omitempty) when
// no match exists — the mechanism never claims accuracy it can't back.
// See internal/calibration/lookup.go.
func CoreGetAnalysisResult(ctx context.Context, svc *deliberation.Service, deliberationID, keyID string, round *int) (*deliberation.AnalysisResult, error) {
	if deliberationID == "" {
		return nil, fmt.Errorf("deliberation_id is required")
	}
	if err := svc.CheckAccess(ctx, deliberationID, keyID); err != nil {
		return nil, err
	}
	var result *deliberation.AnalysisResult
	var err error
	if round != nil {
		result, err = svc.GetAnalysisResult(ctx, deliberationID, *round)
	} else {
		result, err = svc.GetLatestAnalysisResult(ctx, deliberationID)
	}
	// Distinguish "no result yet" (sql.ErrNoRows) from a real error so
	// transport handlers can fall through to CoreGetAnalysisStatus
	// rather than surfacing a raw DB error to the caller.
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil || result == nil {
		return result, err
	}
	attachCalibration(ctx, svc, deliberationID, result)
	return result, nil
}

// AnalysisStatusResponse is returned by get_result when no analysis
// result is available for the requested round. It distinguishes
// "in-progress, poll again" from "never started, run analyze first"
// without forcing callers to make a second deliberation:get call.
// Shape is intentionally JSON-serializable — both transports emit it
// directly in their native response envelope.
type AnalysisStatusResponse struct {
	Status             string `json:"status"` // "pending" | "not_started"
	DeliberationID     string `json:"deliberation_id"`
	DeliberationStatus string `json:"deliberation_status"`       // open / analyzing / closed / ...
	AnalysisStatus     string `json:"analysis_status,omitempty"` // taxonomy / extracting / crux_detection / clustering
	Message            string `json:"message,omitempty"`         // human-readable hint
}

// CoreGetAnalysisStatus describes the current analyze state for a
// deliberation that has no result for the requested round. Both
// transports call this from their get_result no-result branch so
// callers can tell "still working, poll again" from "you forgot to
// call analyze:run" in one round-trip.
func CoreGetAnalysisStatus(ctx context.Context, svc *deliberation.Service, deliberationID string) (*AnalysisStatusResponse, error) {
	d, err := svc.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return nil, err
	}
	resp := &AnalysisStatusResponse{
		DeliberationID:     deliberationID,
		DeliberationStatus: d.Status,
	}
	if d.Status == "analyzing" {
		resp.Status = "pending"
		resp.AnalysisStatus = d.SubStatus
		resp.Message = "analysis in progress — poll get_result again in a few seconds"
	} else {
		resp.Status = "not_started"
		resp.Message = "no analysis has run yet — call analyze action:run to start"
	}
	return resp, nil
}

// CoreGetAllAnalysisResults returns all rounds of analysis for a deliberation.
// Each round's calibration field is populated the same way as
// CoreGetAnalysisResult.
func CoreGetAllAnalysisResults(ctx context.Context, svc *deliberation.Service, deliberationID, keyID string) ([]deliberation.AnalysisResult, error) {
	if deliberationID == "" {
		return nil, fmt.Errorf("deliberation_id is required")
	}
	if err := svc.CheckAccess(ctx, deliberationID, keyID); err != nil {
		return nil, err
	}
	results, err := svc.GetAllAnalysisResults(ctx, deliberationID)
	if err != nil {
		return results, err
	}
	for i := range results {
		attachCalibration(ctx, svc, deliberationID, &results[i])
	}
	return results, nil
}

// attachCalibration looks up the deliberation's type and populates the
// result's Calibration field if a matching reference class exists in the
// embedded calibration run. Failures (missing deliberation, missing
// embed, etc.) are silently swallowed — the field is absent rather than
// erroring the whole get_result call.
func attachCalibration(ctx context.Context, svc *deliberation.Service, deliberationID string, result *deliberation.AnalysisResult) {
	if result == nil {
		return
	}
	d, err := svc.GetDeliberation(ctx, deliberationID)
	if err != nil || d == nil {
		return
	}
	const minN = 8
	if cf := calibration.Lookup(d.Type, minN); cf != nil {
		result.Calibration = cf
	}
}

// CoreExportDeliberation returns the complete multi-round history of a deliberation.
func CoreExportDeliberation(ctx context.Context, svc *deliberation.Service, deliberationID, keyID string, auditLog ...AuditStore) (map[string]any, error) {
	if deliberationID == "" {
		return nil, fmt.Errorf("deliberation_id is required")
	}
	if err := svc.CheckAccess(ctx, deliberationID, keyID); err != nil {
		return nil, err
	}

	d, err := svc.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return nil, err
	}

	// Get all positions (no round filter)
	positions, err := svc.GetPositions(ctx, deliberationID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("getting positions: %w", err)
	}

	// Group positions by round
	positionsByRound := make(map[int][]deliberation.Position)
	for _, p := range positions {
		positionsByRound[p.Round] = append(positionsByRound[p.Round], p)
	}

	// Build rounds array
	rounds := make([]map[string]any, 0, d.Round)
	for r := 1; r <= d.Round; r++ {
		roundData := map[string]any{
			"round":     r,
			"positions": positionsByRound[r],
		}
		// Get analysis for this round (may not exist)
		analysis, err := svc.GetAnalysisResult(ctx, deliberationID, r)
		if err == nil && analysis != nil {
			roundData["analysis"] = analysis
		} else {
			roundData["analysis"] = nil
		}
		rounds = append(rounds, roundData)
	}

	// Votes are not per-round — attach to first round for backwards compat
	votes, err := svc.GetVotes(ctx, deliberationID)
	if err != nil {
		return nil, fmt.Errorf("getting votes: %w", err)
	}
	if len(rounds) > 0 {
		rounds[0]["votes"] = votes
	}

	// Commitments
	commitments, err := svc.GetCommitments(ctx, deliberationID)
	if err != nil {
		return nil, fmt.Errorf("getting commitments: %w", err)
	}

	export := map[string]any{
		"deliberation": d,
		"rounds":       rounds,
		"commitments":  commitments,
		"resolution":   d.Resolution,
	}

	// Include audit log if store is available
	if len(auditLog) > 0 && auditLog[0] != nil {
		if ops, err := auditLog[0].GetAuditLog(deliberationID, -1); err == nil {
			export["audit_log"] = ops
		}
	}

	return export, nil
}

// CoreGetVotes returns all votes for a deliberation.
func CoreGetVotes(ctx context.Context, svc *deliberation.Service, deliberationID, keyID string) ([]deliberation.Vote, error) {
	if deliberationID == "" {
		return nil, fmt.Errorf("deliberation_id is required")
	}
	if err := svc.CheckAccess(ctx, deliberationID, keyID); err != nil {
		return nil, err
	}
	return svc.GetVotes(ctx, deliberationID)
}

// CoreListByGroup lists deliberations in a group.
func CoreListByGroup(ctx context.Context, svc *deliberation.Service, groupID, keyID string, isAdmin bool, limit, offset int) ([]deliberation.Deliberation, error) {
	if groupID == "" {
		return nil, fmt.Errorf("group_id is required")
	}
	effectiveKeyID := keyID
	if isAdmin {
		effectiveKeyID = "" // admins see all, empty keyID matches the OR condition
	}
	return svc.ListByGroup(ctx, groupID, limit, offset, effectiveKeyID)
}

// CoreListByAgent lists deliberations an agent has participated in.
func CoreListByAgent(ctx context.Context, svc *deliberation.Service, agentID, keyID string, isAdmin bool, limit, offset int) ([]deliberation.Deliberation, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	effectiveKeyID := keyID
	if isAdmin {
		effectiveKeyID = ""
	}
	return svc.ListByAgent(ctx, agentID, limit, offset, effectiveKeyID)
}

// CoreFulfillCommitment marks a commitment as fulfilled. Standing is
// enforced in the service so it cannot be bypassed by a caller that skips
// this wrapper; see Service.FulfillCommitment.
func CoreFulfillCommitment(ctx context.Context, svc *deliberation.Service, commitmentID, verifiedBy, keyID string) error {
	if commitmentID == "" {
		return fmt.Errorf("commitment_id is required")
	}
	return svc.FulfillCommitment(ctx, commitmentID, verifiedBy, keyID)
}

// CoreBreakCommitment marks a commitment as broken with a reason. Standing
// is enforced in the service; see Service.BreakCommitment.
func CoreBreakCommitment(ctx context.Context, svc *deliberation.Service, commitmentID, reason, verifiedBy, keyID string) error {
	if commitmentID == "" || reason == "" {
		return fmt.Errorf("commitment_id and reason are required")
	}
	return svc.BreakCommitment(ctx, commitmentID, reason, verifiedBy, keyID)
}

// CoreAgentReputation returns an agent's commitment track record.
func CoreAgentReputation(ctx context.Context, svc *deliberation.Service, agentID, groupID string) (deliberation.ReputationSummary, error) {
	if agentID == "" {
		return deliberation.ReputationSummary{}, fmt.Errorf("agent_id is required")
	}
	return svc.AgentReputation(ctx, agentID, groupID)
}

// CoreCancelAnalysis cancels an in-progress analysis after access check.
func CoreCancelAnalysis(ctx context.Context, svc *deliberation.Service, deliberationID, keyID string) error {
	if deliberationID == "" {
		return fmt.Errorf("deliberation_id is required")
	}
	if err := svc.CheckAccess(ctx, deliberationID, keyID); err != nil {
		return err
	}
	return svc.CancelAnalysis(ctx, deliberationID)
}

// CoreWithdraw removes an agent from a deliberation after access check and agent scoping.
func CoreWithdraw(ctx context.Context, svc *deliberation.Service, deliberationID, agentID, keyID string) error {
	if deliberationID == "" || agentID == "" {
		return fmt.Errorf("deliberation_id and agent_id are required")
	}
	if err := svc.CheckAccess(ctx, deliberationID, keyID); err != nil {
		return err
	}
	return svc.WithdrawAgent(ctx, deliberationID, agentID)
}

// PanelExpert defines a single expert for the adversarial panel.
type PanelExpert struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	Interests   string `json:"interests"`
	Reservation string `json:"reservation"`
}

// DefaultPanelExperts is the standard 5-expert adversarial panel for research/general use.
var DefaultPanelExperts = []PanelExpert{
	{ID: "methodologist", Role: "Research methodology expert (causal inference, experimental design)", Interests: "Internal validity, confound elimination, proper controls", Reservation: "Drawing causal conclusions without adequate controls"},
	{ID: "domain-expert", Role: "Domain expert with deep practical experience", Interests: "Whether claims match real-world dynamics, practical feasibility", Reservation: "Attributing outcomes to the intervention vs background noise"},
	{ID: "statistician", Role: "Statistician (small-sample analysis, effect sizes)", Interests: "Statistical rigor, appropriate claims given sample size, multiple comparisons", Reservation: "Any claim of significance without adequate replication"},
	{ID: "systems-critic", Role: "Systems engineer focused on reliability and failure modes", Interests: "Infrastructure reliability, data integrity, hidden failure modes", Reservation: "Trusting results from systems with known issues"},
	{ID: "devils-advocate", Role: "Devil's advocate — finds the strongest counterargument to every claim", Interests: "Alternative explanations, unfalsifiable claims, confirmation bias", Reservation: "Accepting conclusions when simpler explanations exist"},
}

// sourceTypeExperts maps source_type to specialized expert panels.
var sourceTypeExperts = map[string][]PanelExpert{
	"code_review": {
		{ID: "security-reviewer", Role: "Application security engineer (OWASP, supply chain, authz)", Interests: "Injection vectors, auth/authz gaps, secret handling, dependency risk", Reservation: "Shipping code with unreviewed attack surface"},
		{ID: "api-designer", Role: "API design expert (REST, gRPC, backward compatibility)", Interests: "Interface stability, naming consistency, versioning, error contracts", Reservation: "Breaking changes disguised as minor updates"},
		{ID: "reliability-eng", Role: "Site reliability engineer (failure modes, observability, capacity)", Interests: "Error handling, timeout behavior, resource leaks, monitoring gaps", Reservation: "Optimistic happy-path code with no failure handling"},
		{ID: "maintainability", Role: "Senior engineer focused on long-term maintainability", Interests: "Complexity budget, test coverage, abstractions that pay for themselves", Reservation: "Clever code that only the author can debug"},
		{ID: "devils-advocate", Role: "Devil's advocate — argues the change shouldn't be made at all", Interests: "Whether the problem is real, simpler alternatives, hidden costs", Reservation: "Accepting the premise without questioning it"},
	},
	"architecture": {
		{ID: "scalability", Role: "Distributed systems architect (CAP, consistency, partitioning)", Interests: "Scaling bottlenecks, data consistency, failure domains", Reservation: "Designs that work at demo scale but break at production load"},
		{ID: "security-arch", Role: "Security architect (threat modeling, zero trust, data boundaries)", Interests: "Trust boundaries, data flow, attack surface, compliance", Reservation: "Security as an afterthought bolted onto an insecure design"},
		{ID: "operational", Role: "Platform engineer (deployment, observability, incident response)", Interests: "Operability, debugging in production, deployment complexity, rollback", Reservation: "Architectures that can't be operated by a small team"},
		{ID: "simplicity", Role: "Engineering leader who values simplicity over elegance", Interests: "Accidental complexity, premature abstraction, build-vs-buy", Reservation: "Over-engineering when a simpler solution exists"},
		{ID: "devils-advocate", Role: "Devil's advocate — challenges whether the architecture needs to change", Interests: "Whether current system actually can't handle requirements, migration cost", Reservation: "Rebuilding what works for theoretical future needs"},
	},
	"experiment": {
		{ID: "methodologist", Role: "Research methodology expert (causal inference, experimental design)", Interests: "Internal validity, confound elimination, proper controls", Reservation: "Drawing causal conclusions without adequate controls"},
		{ID: "domain-expert", Role: "Domain expert with deep practical experience", Interests: "Whether claims match real-world dynamics, practical feasibility", Reservation: "Attributing outcomes to the intervention vs background noise"},
		{ID: "statistician", Role: "Statistician (small-sample analysis, effect sizes)", Interests: "Statistical rigor, appropriate claims given sample size, multiple comparisons", Reservation: "Any claim of significance without adequate replication"},
		{ID: "systems-critic", Role: "Systems engineer focused on reliability and failure modes", Interests: "Infrastructure reliability, data integrity, hidden failure modes", Reservation: "Trusting results from systems with known issues"},
		{ID: "devils-advocate", Role: "Devil's advocate — finds the strongest counterargument to every claim", Interests: "Alternative explanations, unfalsifiable claims, confirmation bias", Reservation: "Accepting conclusions when simpler explanations exist"},
	},
	"proposal": {
		{ID: "feasibility", Role: "Engineering manager who ships on time and on budget", Interests: "Scope realism, hidden dependencies, team capacity, timeline risk", Reservation: "Plans that assume everything goes right"},
		{ID: "customer-advocate", Role: "Product manager representing end users", Interests: "User value, adoption friction, whether anyone actually wants this", Reservation: "Building features nobody asked for"},
		{ID: "technical-debt", Role: "Staff engineer focused on long-term codebase health", Interests: "Maintenance burden, reversibility, migration paths", Reservation: "Short-term wins that create long-term liabilities"},
		{ID: "business-case", Role: "Business analyst focused on ROI and opportunity cost", Interests: "Cost-benefit, competitive landscape, what we're NOT doing instead", Reservation: "Investing without a clear return thesis"},
		{ID: "devils-advocate", Role: "Devil's advocate — argues for doing nothing", Interests: "Status quo viability, change cost, whether the problem is urgent", Reservation: "Acting on urgency that isn't backed by evidence"},
	},
}

// sourceTypePrompts maps source_type to customized critique instructions.
var sourceTypePrompts = map[string]string{
	"code_review": `You are a %s.

Review the following code changes. Focus on what matters for production: security, correctness, maintainability, and operational risk. Don't nitpick style.

Your interests: %s
Your hard constraint: %s

Structure your review:
1. BLOCKING: Issues that must be fixed before merge
2. SERIOUS: Issues that should be addressed but aren't blockers
3. SUGGESTIONS: Improvements worth considering
4. LOOKS GOOD: What's well done — be specific
5. QUESTIONS: Things you'd ask the author in review

=== CODE ===
%s`,

	"architecture": `You are a %s.

Evaluate the following architecture design or proposal. Consider both the immediate design and its long-term implications.

Your interests: %s
Your hard constraint: %s

Structure your evaluation:
1. FUNDAMENTAL CONCERNS: Issues with the core approach
2. RISK AREAS: Things likely to cause problems at scale or over time
3. MISSING CONSIDERATIONS: What the design doesn't address
4. STRENGTHS: What's well-designed — be specific
5. ALTERNATIVES: Other approaches worth considering, with tradeoffs

=== ARCHITECTURE ===
%s`,

	"proposal": `You are a %s.

Evaluate the following proposal or plan. Be constructive but honest about feasibility and value.

Your interests: %s
Your hard constraint: %s

Structure your evaluation:
1. DEAL-BREAKERS: Why this might fail or shouldn't be done
2. RISKS: Things that could go wrong and aren't addressed
3. GAPS: What's missing from the proposal
4. STRENGTHS: What's compelling — be specific
5. RECOMMENDATIONS: How to improve the proposal, ranked by impact

=== PROPOSAL ===
%s`,
}

// validSourceTypes lists accepted source_type values.
var validSourceTypes = map[string]bool{
	"":             true, // default/general
	"code_review":  true,
	"architecture": true,
	"experiment":   true,
	"proposal":     true,
}

// ExpertPanelResult is returned immediately after creating the panel and submitting positions.
// Analysis runs async — poll deliberation action:get for status, then analyze action:get_result.
type ExpertPanelResult struct {
	DeliberationID string `json:"deliberation_id"`
	Topic          string `json:"topic"`
	ExpertCount    int    `json:"expert_count"`
	Model          string `json:"model,omitempty"`
}

// CoreRunExpertPanel creates a deliberation, submits expert positions, runs
// analysis synchronously, and returns structured results. This is the
// single-call expert panel workflow — no polling needed.
//
// sourceType selects specialized experts and prompts: "code_review",
// "architecture", "experiment", "proposal", or "" (general/research).
// Custom experts via expertsJSON override the source_type defaults.
// depth: "quick" (3 experts, tight taxonomy, ~2 min) or "thorough" (5 experts, full taxonomy, ~7 min).
func CoreRunExpertPanel(ctx context.Context, svc *deliberation.Service, document, topic, expertsJSON, groupID, model, keyID, sourceType, depth string) (*ExpertPanelResult, error) {
	if document == "" {
		return nil, fmt.Errorf("document is required")
	}
	if len(document) > 50000 {
		return nil, fmt.Errorf("document exceeds 50000 characters — excerpt or summarize for panel review")
	}
	if !validSourceTypes[sourceType] {
		return nil, fmt.Errorf("unknown source_type %q — use: code_review, architecture, experiment, proposal (or omit for general)", sourceType)
	}
	if depth == "" {
		depth = "thorough"
	}
	if depth != "quick" && depth != "thorough" {
		return nil, fmt.Errorf("unknown depth %q — use: quick (~2 min, 3 experts) or thorough (~7 min, 5 experts)", depth)
	}

	// Set taxonomy limits based on depth
	if depth == "quick" {
		ctx = context.WithValue(ctx, analysis.ContextKeyMaxTopics{}, 3)
		ctx = context.WithValue(ctx, analysis.ContextKeyMaxSubtopics{}, 2)
	}

	// Select experts: custom JSON > source_type defaults > general defaults
	experts := DefaultPanelExperts
	if expertsJSON != "" {
		var custom []PanelExpert
		if err := json.Unmarshal([]byte(expertsJSON), &custom); err != nil {
			return nil, fmt.Errorf("invalid experts JSON: %w", err)
		}
		if len(custom) == 0 {
			return nil, fmt.Errorf("experts array must not be empty")
		}
		if len(custom) > 20 {
			return nil, fmt.Errorf("maximum 20 experts allowed")
		}
		experts = custom
	} else if sourceType != "" {
		if typed, ok := sourceTypeExperts[sourceType]; ok {
			experts = typed
		}
	}

	// Quick mode: use 3 experts (first, last, and middle) for faster analysis
	if depth == "quick" && expertsJSON == "" && len(experts) > 3 {
		experts = []PanelExpert{experts[0], experts[len(experts)/2], experts[len(experts)-1]}
	}

	// Select prompt template: source_type-specific or general
	promptTemplate := `You are a %s.

Adversarially critique the following document. Find every weakness, amateur mistake, unjustified claim, and missing control. Be specific and constructive.

Your interests: %s
Your hard constraint: %s

Provide your critique with:
1. FATAL FLAWS: issues that invalidate the conclusions
2. MAJOR CONCERNS: issues that significantly weaken the findings
3. MINOR ISSUES: things to fix but don't invalidate results
4. WHAT'S GOOD: acknowledge strengths honestly
5. RECOMMENDED FOLLOW-UPS: specific next steps ranked by value/effort

=== DOCUMENT ===
%s`
	if sourceType != "" {
		if typed, ok := sourceTypePrompts[sourceType]; ok {
			promptTemplate = typed
		}
	}

	if topic == "" {
		topic = "Expert panel review"
	}

	// Create deliberation with assembly template, quorum set to match expert count
	opts := []deliberation.DeliberationOption{
		deliberation.WithTemplate("assembly"),
		deliberation.WithType("reasoning"),
		deliberation.WithRules(map[string]any{"min_participants": len(experts)}),
	}
	if groupID != "" {
		opts = append(opts, deliberation.WithGroupID(groupID))
	}
	if keyID != "" {
		opts = append(opts, deliberation.WithCreatorKey(keyID))
	}
	d, err := svc.CreateDeliberation(ctx, topic, fmt.Sprintf("Adversarial expert panel (%s): %s", sourceType, topic), opts...)
	if err != nil {
		return nil, fmt.Errorf("creating deliberation: %w", err)
	}
	slog.Info("expert panel created", "deliberation_id", d.ID, "experts", len(experts), "source_type", sourceType)

	// Submit expert positions
	for _, e := range experts {
		content := fmt.Sprintf(promptTemplate, e.Role, e.Interests, e.Reservation, document)

		_, err := svc.SubmitPosition(ctx, d.ID, e.ID, content,
			deliberation.WithInterests(e.Interests),
			deliberation.WithReservation(e.Reservation),
		)
		if err != nil {
			return nil, fmt.Errorf("submitting %s position: %w", e.ID, err)
		}
	}

	return &ExpertPanelResult{
		DeliberationID: d.ID,
		Topic:          topic,
		ExpertCount:    len(experts),
		Model:          model,
	}, nil
}

// CoreFollowUpExpertPanel submits follow-up expert positions that respond to
// round 1 analysis results, then triggers round 2 analysis. The deliberation
// must be in "open" status with round > 1 (i.e., round 1 analysis completed).
func CoreFollowUpExpertPanel(ctx context.Context, svc *deliberation.Service, deliberationID, model, keyID string) (*ExpertPanelResult, error) {
	if deliberationID == "" {
		return nil, fmt.Errorf("deliberation_id is required")
	}

	d, err := svc.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return nil, fmt.Errorf("deliberation not found: %w", err)
	}
	if d.Status != "open" {
		return nil, fmt.Errorf("deliberation is %s, not open — wait for round 1 to complete", d.Status)
	}
	if d.Round < 2 {
		return nil, fmt.Errorf("round 1 has not completed yet (current round: %d)", d.Round)
	}

	// Get round 1 results
	prevRound := d.Round - 1
	result, err := svc.GetAnalysisResult(ctx, deliberationID, prevRound)
	if err != nil || result == nil {
		return nil, fmt.Errorf("no round %d results found", prevRound)
	}

	// Get round 1 positions to identify expert agents and their roles
	round1 := 1
	positions, err := svc.GetPositions(ctx, deliberationID, nil, &round1)
	if err != nil {
		return nil, fmt.Errorf("getting round 1 positions: %w", err)
	}

	// Build crux summary for follow-up prompt
	var cruxSummary strings.Builder
	for i, c := range result.Cruxes {
		fmt.Fprintf(&cruxSummary, "%d. %s (agree: %s, disagree: %s)\n",
			i+1, c.Claim,
			strings.Join(c.AgreeAgents, ", "),
			strings.Join(c.DisagreeAgents, ", "))
	}
	var consensusSummary strings.Builder
	for _, cs := range result.ConsensusStatements {
		fmt.Fprintf(&consensusSummary, "- %s\n", cs.Content)
	}

	// Submit follow-up position for each expert from round 1
	expertCount := 0
	for _, p := range positions {
		if p.Round != 1 {
			continue
		}
		content := fmt.Sprintf(`You previously submitted a critique of a document. The panel has now been analyzed.

KEY DISAGREEMENTS among the panel:
%s
CONSENSUS the panel reached:
%s
Your previous position was attributed to the following sides of these cruxes. Review them:
- Do you still hold your position on each crux, or has the panel's analysis changed your view?
- Are there cruxes where you were misclassified?
- What NEW issues did the panel miss entirely?
- Which recommended follow-ups are highest priority given the disagreements?

Be specific and constructive. Focus on what changed or what was missed.`, cruxSummary.String(), consensusSummary.String())

		_, err := svc.SubmitPosition(ctx, deliberationID, p.AgentID, content,
			deliberation.WithInterests(p.Interests),
			deliberation.WithReservation(p.Reservation),
		)
		if err != nil {
			slog.Warn("follow-up submission failed", "agent", p.AgentID, "error", err)
			continue
		}
		expertCount++
	}

	if expertCount == 0 {
		return nil, fmt.Errorf("no follow-up positions submitted")
	}

	return &ExpertPanelResult{
		DeliberationID: deliberationID,
		Topic:          d.Topic,
		ExpertCount:    expertCount,
		Model:          model,
	}, nil
}
