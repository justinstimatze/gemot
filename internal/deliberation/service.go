package deliberation

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/justinstimatze/gemot/internal/sanitize"
)

const (
	maxTopicLen       = 500
	maxDescriptionLen = 5000
	maxContentLen     = 10000
	maxAgentIDLen     = 200
	maxPositions      = 1000
)

// Store defines the persistence interface the service needs.
type Store interface {
	CreateDeliberation(d *Deliberation) error
	GetDeliberation(id string) (*Deliberation, error)
	ListDeliberations(limit, offset int) ([]Deliberation, error)
	ListByGroup(groupID string, limit, offset int) ([]Deliberation, error)
	ListByAgent(agentID string, limit, offset int) ([]Deliberation, error)
	SetGroupID(deliberationID, groupID string) error
	UpdateDeliberationStatus(id, status string) error
	UpdateDeliberationTemplate(id, template string) error
	DeleteDeliberation(id string) error
	CreateAbuseReport(deliberationID, reporterKey, reason string) error
	RecordContextAccess(deliberationID, agentID string, round int) error
	HasContextAccess(deliberationID, agentID string, round int) (bool, error)
	GetStatusChangedAt(deliberationID string) (time.Time, error)
	UpdateSubStatus(id, subStatus string) error
	TrySetAnalyzing(id string) (bool, error)
	AdvanceRound(id string) error

	CreatePosition(p *Position) error
	GetPositions(deliberationID string, round *int) ([]Position, error)
	GetPositionByID(id string) (*Position, error)
	CountPositions(deliberationID string) (int, error)

	CreateVote(v *Vote) error
	GetVotes(deliberationID string) ([]Vote, error)
	GetVotesByRound(deliberationID string, round int) ([]Vote, error)

	CreateDelegation(d *Delegation) error
	RevokeDelegation(deliberationID, fromAgent string) error
	GetDelegations(deliberationID string) ([]Delegation, error)
	PublishPosition(id string) error

	CreateCommitment(c *Commitment) error
	GetCommitments(deliberationID string) ([]Commitment, error)
	UpdateCommitmentStatus(id, status string) error

	CreateJoinCode(jc *JoinCode) error
	ClaimJoinCode(code, agentID string) (*JoinCode, error)
	LookupJoinCode(code string) (*JoinCode, error)

	AddToACL(deliberationID, keyID string) error
	CheckACL(deliberationID, keyID string) (bool, error)

	CreateInvitation(inv *Invitation) error
	GetInvitations(deliberationID string) ([]Invitation, error)
	UpdateInvitationStatus(id, status string) error

	CreateDispute(d *Dispute) error
	GetDisputes(deliberationID string) ([]Dispute, error)

	SaveAnalysisResult(deliberationID string, round int, result *AnalysisResult) error
	GetAnalysisResult(deliberationID string, round int) (*AnalysisResult, error)
	GetLatestAnalysisResult(deliberationID string) (*AnalysisResult, error)

	RecoverStuckAnalyzing(maxAge time.Duration) (int, error)

	CreateShareToken(token, groupID string, expiresAt time.Time) error
	LookupShareToken(token string) (groupID string, err error)
}

// ContextKeyDeliberationID is the context key for passing the deliberation ID
// through the analysis pipeline (used for per-deliberation cost tracking).
type ContextKeyDeliberationID struct{}
type ContextKeyDeliberationType struct{}
type ContextKeyPriorNorms struct{}
type ContextKeyConstitutionalRules struct{}

// ContextKeyProgressFunc is used to pass a progress callback via context.
type ContextKeyProgressFunc struct{}

// ProgressFunc is called by the analyzer to report sub-status updates.
type ProgressFunc func(subStatus string)

// Analyzer defines the analysis engine interface.
type Analyzer interface {
	Analyze(ctx context.Context, positions []Position, votes []Vote, agents []string) (*AnalysisResult, error)
}

// CompromiseGenerator generates compromise proposals from analysis results.
type CompromiseGenerator interface {
	GenerateCompromise(ctx context.Context, topic string, result *AnalysisResult) (string, error)
}

// Reframer restates positions emphasizing common ground.
type Reframer interface {
	Reframe(ctx context.Context, position, otherPositions, cruxes string) (string, error)
}

// PositionOption configures optional fields on a position.
type PositionOption func(*Position)

// WithModelFamily sets the model family for a position.
func WithModelFamily(family string) PositionOption {
	return func(p *Position) { p.ModelFamily = family }
}

// WithGroup sets the sub-group for decentralized deliberation.
func WithGroup(group string) PositionOption {
	return func(p *Position) { p.Group = group }
}

// WithConviction sets conviction weight (0.0-1.0).
func WithConviction(c float64) PositionOption {
	return func(p *Position) {
		if c < 0 {
			c = 0
		} else if c > 1 {
			c = 1
		}
		p.Conviction = c
	}
}

// WithReservation sets what outcome is unacceptable.
func WithReservation(r string) PositionOption {
	return func(p *Position) { p.Reservation = r }
}

// WithOnBehalfOf declares the principal this agent represents.
func WithOnBehalfOf(principal string) PositionOption {
	return func(p *Position) { p.OnBehalfOf = principal }
}

// WithInterests declares what this agent optimizes for (transparent objectives).
func WithInterests(interests string) PositionOption {
	return func(p *Position) { p.Interests = interests }
}

// WithDraft creates a draft position (invisible to others until published).
func WithDraft() PositionOption {
	return func(p *Position) { p.Draft = true }
}

// Service orchestrates deliberation operations.
type Service struct {
	store            Store
	analyzer         Analyzer
	compromiser      CompromiseGenerator
	reframer         Reframer
	contentClassifier sanitize.Classifier
	events           *EventBus // nil = no event emission
}

func NewService(store Store, analyzer Analyzer) *Service {
	return &Service{store: store, analyzer: analyzer}
}

// SetContentClassifier sets the LLM content screening function.
func (s *Service) SetContentClassifier(c sanitize.Classifier) {
	s.contentClassifier = c
}

// SetEventBus enables event emission for state changes.
func (s *Service) SetEventBus(eb *EventBus) {
	s.events = eb
}

// Events returns the event bus, or nil if not set.
func (s *Service) Events() *EventBus {
	return s.events
}

func (s *Service) emit(eventType, deliberationID, agentID, detail string) {
	s.emitWithData(eventType, deliberationID, agentID, detail, nil)
}

func (s *Service) emitWithData(eventType, deliberationID, agentID, detail string, data any) {
	if s.events == nil {
		return
	}
	s.events.Emit(Event{
		Type:           eventType,
		DeliberationID: deliberationID,
		AgentID:        agentID,
		Detail:         detail,
		Data:           data,
	})
}

// SetCompromiseGenerator sets the compromise generation engine.
func (s *Service) SetCompromiseGenerator(c CompromiseGenerator) {
	s.compromiser = c
}

// SetReframer sets the position reframing engine.
func (s *Service) SetReframer(r Reframer) {
	s.reframer = r
}

// ProposeCompromise generates a compromise statement from the latest analysis.
func (s *Service) ProposeCompromise(ctx context.Context, deliberationID string) (string, error) {
	if s.compromiser == nil {
		return "", fmt.Errorf("compromise generation not available")
	}

	d, err := s.store.GetDeliberation(deliberationID)
	if err != nil {
		return "", fmt.Errorf("deliberation not found: %w", err)
	}

	result, err := s.store.GetLatestAnalysisResult(deliberationID)
	if err != nil {
		return "", fmt.Errorf("no analysis results — run analyze first: %w", err)
	}

	if len(result.Cruxes) == 0 {
		return "", fmt.Errorf("no cruxes detected — nothing to compromise on")
	}

	return s.compromiser.GenerateCompromise(ctx, d.Topic, result)
}

// DeliberationOption configures optional fields on a deliberation.
type DeliberationOption func(*Deliberation)

// WithType sets the deliberation type (reasoning, knowledge, negotiation, policy).
func WithType(t string) DeliberationOption {
	return func(d *Deliberation) { d.Type = t }
}

// WithVisibility sets the deliberation visibility (open, private, link).
func WithVisibility(v string) DeliberationOption {
	return func(d *Deliberation) { d.Visibility = v }
}

// WithCreatorKey sets the creator's key_id for access control.
func WithCreatorKey(k string) DeliberationOption {
	return func(d *Deliberation) { d.CreatorKey = k }
}

// WithGroupID links this deliberation to a group (experiment, workflow, session).
func WithGroupID(g string) DeliberationOption {
	return func(d *Deliberation) { d.GroupID = g }
}

// WithMaxParticipants sets the maximum number of unique agents.
func WithMaxParticipants(n int) DeliberationOption {
	return func(d *Deliberation) { d.MaxParticipants = n }
}

// WithTemplate applies a governance template's defaults.
// Apply this BEFORE other options so explicit params override template defaults.
func WithTemplate(name string) DeliberationOption {
	return func(d *Deliberation) {
		tmpl, ok := GetTemplate(name)
		if !ok {
			d.Template = name // store it; validation happens in CreateDeliberation
			return
		}
		d.Template = name
		if d.Type == "" && tmpl.DefaultType != "" {
			d.Type = tmpl.DefaultType
		}
		if d.MaxParticipants == 0 && tmpl.DefaultMaxPart > 0 {
			d.MaxParticipants = tmpl.DefaultMaxPart
		}
		// Apply default rules (explicit rules override later)
		if d.Rules == nil && len(tmpl.DefaultRules) > 0 {
			d.Rules = make(map[string]any)
			for k, v := range tmpl.DefaultRules {
				d.Rules[k] = v
			}
		}
	}
}

// WithRules sets explicit governance rules, overriding template defaults.
func WithRules(rules map[string]any) DeliberationOption {
	return func(d *Deliberation) {
		if d.Rules == nil {
			d.Rules = make(map[string]any)
		}
		for k, v := range rules {
			d.Rules[k] = v
		}
	}
}

// ContextKeyTemplate is the context key for passing the template name through analysis.
type ContextKeyTemplate struct{}

// RuleInt reads an integer rule from a deliberation, returning the default if not set.
func RuleInt(d *Deliberation, key string, defaultVal int) int {
	if d.Rules == nil {
		return defaultVal
	}
	v, ok := d.Rules[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return defaultVal
}

func (s *Service) CreateDeliberation(topic, description string, opts ...DeliberationOption) (*Deliberation, error) {
	if len(topic) > maxTopicLen {
		return nil, fmt.Errorf("topic exceeds %d characters", maxTopicLen)
	}
	if len(description) > maxDescriptionLen {
		return nil, fmt.Errorf("description exceeds %d characters", maxDescriptionLen)
	}

	d := &Deliberation{
		Topic:       topic,
		Description: description,
		Round:       1,
		Status:      "open",
	}
	for _, opt := range opts {
		opt(d)
	}
	// Validate template
	if d.Template != "" {
		if _, ok := GetTemplate(d.Template); !ok {
			return nil, fmt.Errorf("unknown template %q — use list_templates to see available templates", d.Template)
		}
	}
	// Validate deliberation type
	if d.Type != "" {
		validTypes := map[string]bool{"reasoning": true, "knowledge": true, "negotiation": true, "policy": true}
		if !validTypes[d.Type] {
			return nil, fmt.Errorf("invalid type %q — use reasoning, knowledge, negotiation, or policy", d.Type)
		}
	}
	// Validate and default visibility
	if d.Visibility == "" {
		d.Visibility = "open"
	}
	validVis := map[string]bool{"open": true, "private": true, "link": true}
	if !validVis[d.Visibility] {
		return nil, fmt.Errorf("invalid visibility %q — use open, private, or link", d.Visibility)
	}

	if err := s.store.CreateDeliberation(d); err != nil {
		return nil, err
	}

	// Auto-add creator to ACL for private deliberations
	if d.Visibility == "private" && d.CreatorKey != "" {
		_ = s.store.AddToACL(d.ID, d.CreatorKey)
	}

	s.emit("deliberation_created", d.ID, "", d.Topic)
	return d, nil
}

func (s *Service) GetDeliberation(id string) (*Deliberation, error) {
	return s.store.GetDeliberation(id)
}

// SetTemplate changes the governance template on an existing deliberation.
// Only the creator can change the template. Only affects future analysis rounds.
func (s *Service) SetTemplate(deliberationID, template, callerKeyID string) error {
	if _, ok := GetTemplate(template); !ok {
		return fmt.Errorf("unknown template %q — use list_templates to see available templates", template)
	}
	d, err := s.store.GetDeliberation(deliberationID)
	if err != nil {
		return fmt.Errorf("deliberation not found: %w", err)
	}
	if d.CreatorKey != "" && d.CreatorKey != callerKeyID {
		return fmt.Errorf("only the deliberation creator can change the template")
	}
	return s.store.UpdateDeliberationTemplate(deliberationID, template)
}

// DeleteDeliberation removes a deliberation and all its data.
// Only the creator or an admin can delete.
func (s *Service) DeleteDeliberation(deliberationID, callerKeyID string, isAdmin bool) error {
	if !isAdmin {
		d, err := s.store.GetDeliberation(deliberationID)
		if err != nil {
			return fmt.Errorf("deliberation not found: %w", err)
		}
		if d.CreatorKey == "" || d.CreatorKey != callerKeyID {
			return fmt.Errorf("only the deliberation creator or admin can delete")
		}
	}
	return s.store.DeleteDeliberation(deliberationID)
}

// ReportAbuse files an abuse report for manual review.
func (s *Service) ReportAbuse(deliberationID, reporterKey, reason string) error {
	if _, err := s.store.GetDeliberation(deliberationID); err != nil {
		return fmt.Errorf("deliberation not found: %w", err)
	}
	return s.store.CreateAbuseReport(deliberationID, reporterKey, reason)
}

func (s *Service) GetLatestAnalysisResult(deliberationID string) (*AnalysisResult, error) {
	return s.store.GetLatestAnalysisResult(deliberationID)
}

func (s *Service) ListDeliberations(limit, offset int) ([]Deliberation, error) {
	return s.store.ListDeliberations(limit, offset)
}

func (s *Service) ListByGroup(groupID string, limit, offset int) ([]Deliberation, error) {
	return s.store.ListByGroup(groupID, limit, offset)
}

func (s *Service) ListByAgent(agentID string, limit, offset int) ([]Deliberation, error) {
	return s.store.ListByAgent(agentID, limit, offset)
}

func (s *Service) SetGroupID(deliberationID, groupID string) error {
	return s.store.SetGroupID(deliberationID, groupID)
}

func (s *Service) SubmitPosition(deliberationID, agentID, content string, opts ...PositionOption) (*Position, error) {
	if len(agentID) > maxAgentIDLen {
		return nil, fmt.Errorf("agent_id exceeds %d characters", maxAgentIDLen)
	}
	if len(content) > maxContentLen {
		return nil, fmt.Errorf("content exceeds %d characters", maxContentLen)
	}

	// PII sanitization at write time (defense in depth — also sanitized at analysis time)
	sanitized := sanitize.Position(content)
	if len(sanitized.Warnings) > 0 {
		for _, w := range sanitized.Warnings {
			fmt.Fprintf(os.Stderr, "gemot: PII sanitization warning for position in %s: %s\n", deliberationID, w)
		}
	}
	content = sanitized.Text

	// Content screening — LLM classifier (Haiku, ~200ms, ~$0.001)
	var screeningWarning string
	if s.contentClassifier != nil {
		screenCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		blocked, reason := sanitize.ScreenContent(screenCtx, s.contentClassifier, content)
		if blocked {
			return nil, fmt.Errorf("content rejected: %s", reason)
		}
		if reason != "" {
			screeningWarning = reason // "UNSCREENED: classifier unavailable"
		}
	}
	if screeningWarning != "" {
		fmt.Fprintf(os.Stderr, "gemot: %s for position in deliberation (content length: %d)\n", screeningWarning, len(content))
	}

	d, err := s.store.GetDeliberation(deliberationID)
	if err != nil {
		return nil, fmt.Errorf("deliberation not found: %w", err)
	}
	if d.Status != "open" {
		return nil, fmt.Errorf("deliberation is %s, not accepting positions", d.Status)
	}

	// Forced acknowledgment: in round 2+, agents must call get_context first
	if d.Round > 1 {
		accessed, _ := s.store.HasContextAccess(deliberationID, agentID, d.Round)
		if !accessed {
			return nil, fmt.Errorf("round %d requires reviewing cruxes first — call get_context before submitting a new position", d.Round)
		}
	}

	count, err := s.store.CountPositions(deliberationID)
	if err != nil {
		return nil, err
	}
	if count >= maxPositions {
		return nil, fmt.Errorf("deliberation has reached the maximum of %d positions", maxPositions)
	}

	// Enforce max_participants cap
	if d.MaxParticipants > 0 {
		positions, err := s.store.GetPositions(deliberationID, nil)
		if err == nil {
			uniqueAgents := map[string]bool{}
			for _, p := range positions {
				uniqueAgents[p.AgentID] = true
			}
			if !uniqueAgents[agentID] && len(uniqueAgents) >= d.MaxParticipants {
				return nil, fmt.Errorf("deliberation has reached the maximum of %d participants", d.MaxParticipants)
			}
		}
	}

	p := &Position{
		DeliberationID: deliberationID,
		AgentID:        agentID,
		Content:        content,
		Round:          d.Round,
	}
	for _, opt := range opts {
		opt(p)
	}
	if err := s.store.CreatePosition(p); err != nil {
		return nil, err
	}
	s.emitWithData("position_submitted", deliberationID, agentID, p.ID, map[string]any{
		"position_id": p.ID,
		"content":     p.Content,
		"round":       p.Round,
	})
	return p, nil
}

func (s *Service) GetPositions(deliberationID string, excludeAgentID *string, round *int) ([]Position, error) {
	positions, err := s.store.GetPositions(deliberationID, round)
	if err != nil {
		return nil, err
	}
	if excludeAgentID != nil {
		filtered := make([]Position, 0, len(positions))
		for _, p := range positions {
			if p.AgentID != *excludeAgentID {
				filtered = append(filtered, p)
			}
		}
		return filtered, nil
	}
	return positions, nil
}

func (s *Service) Vote(deliberationID, agentID, positionID string, value int, criterionID ...string) error {
	if len(agentID) > maxAgentIDLen {
		return fmt.Errorf("agent_id exceeds %d characters", maxAgentIDLen)
	}

	d, err := s.store.GetDeliberation(deliberationID)
	if err != nil {
		return fmt.Errorf("deliberation not found: %w", err)
	}
	if d.Status != "open" {
		return fmt.Errorf("deliberation is %s, not accepting votes", d.Status)
	}

	pos, err := s.store.GetPositionByID(positionID)
	if err != nil {
		return fmt.Errorf("position not found: %w", err)
	}
	if pos.DeliberationID != deliberationID {
		return fmt.Errorf("position does not belong to this deliberation")
	}

	if value < -1 || value > 1 {
		return fmt.Errorf("vote value must be -1, 0, or 1")
	}

	v := &Vote{
		DeliberationID: deliberationID,
		AgentID:        agentID,
		PositionID:     positionID,
		Value:          value,
	}
	if len(criterionID) > 0 && criterionID[0] != "" {
		v.CriterionID = criterionID[0]
	}
	if err := s.store.CreateVote(v); err != nil {
		return err
	}
	s.emitWithData("vote_cast", deliberationID, agentID, positionID, map[string]any{
		"position_id": positionID,
		"value":       value,
	})
	return nil
}

func (s *Service) Analyze(ctx context.Context, deliberationID string) (*AnalysisResult, error) {
	d, err := s.store.GetDeliberation(deliberationID)
	if err != nil {
		return nil, fmt.Errorf("deliberation not found: %w", err)
	}

	// Enforce cooling period (minimum time between analyses)
	coolingMinutes := RuleInt(d, "cooling_period_minutes", 0)
	if coolingMinutes > 0 && d.Round > 1 {
		if lastChanged, err := s.store.GetStatusChangedAt(deliberationID); err == nil && !lastChanged.IsZero() {
			elapsed := time.Since(lastChanged)
			required := time.Duration(coolingMinutes) * time.Minute
			if elapsed < required {
				remaining := required - elapsed
				return nil, fmt.Errorf("cooling period active — %d minutes remaining before next analysis", int(remaining.Minutes())+1)
			}
		}
	}

	// Enforce quorum (minimum participants before analysis)
	minParticipants := RuleInt(d, "min_participants", 0)
	if minParticipants > 0 {
		positions, err := s.store.GetPositions(deliberationID, nil)
		if err != nil {
			return nil, err
		}
		uniqueAgents := map[string]bool{}
		for _, p := range positions {
			uniqueAgents[p.AgentID] = true
		}
		if len(uniqueAgents) < minParticipants {
			return nil, fmt.Errorf("quorum not met: %d participants, need %d", len(uniqueAgents), minParticipants)
		}
	}

	// Atomic status transition: prevents concurrent analysis race condition
	ok, err := s.store.TrySetAnalyzing(deliberationID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("deliberation is not open (current status: %s)", d.Status)
	}

	// From here, we must reset status on any error
	resetStatus := func() {
		// Note: log to stderr, NOT stdout — stdout is the MCP protocol channel in stdio mode
		if err := s.store.UpdateDeliberationStatus(deliberationID, "open"); err != nil {
			fmt.Fprintf(os.Stderr, "gemot: warning: failed to reset deliberation status: %v\n", err)
		}
	}

	positions, err := s.store.GetPositions(deliberationID, nil)
	if err != nil {
		resetStatus()
		return nil, err
	}
	votes, err := s.store.GetVotes(deliberationID)
	if err != nil {
		resetStatus()
		return nil, err
	}

	// Resolve delegated votes (liquid democracy)
	delegations, _ := s.store.GetDelegations(deliberationID)
	if len(delegations) > 0 {
		votes = resolveDelegations(votes, delegations)
	}

	// Collect unique agent IDs (sorted for deterministic output)
	agentSet := map[string]bool{}
	for _, p := range positions {
		agentSet[p.AgentID] = true
	}
	for _, v := range votes {
		agentSet[v.AgentID] = true
	}
	// Include delegators in agent set even if they only delegated
	for _, d := range delegations {
		if d.Active {
			agentSet[d.FromAgent] = true
		}
	}
	agents := make([]string, 0, len(agentSet))
	for a := range agentSet {
		agents = append(agents, a)
	}
	sort.Strings(agents)

	// Set the deliberation topic on positions so the analyzer sees the real topic, not the UUID
	for i := range positions {
		positions[i].DeliberationID = d.Topic
	}

	// Attach deliberation ID, type, and progress callback to context
	analysisCtx := context.WithValue(ctx, ContextKeyDeliberationID{}, deliberationID)
	if d.Type != "" {
		analysisCtx = context.WithValue(analysisCtx, ContextKeyDeliberationType{}, d.Type)
	}
	if d.Template != "" {
		analysisCtx = context.WithValue(analysisCtx, ContextKeyTemplate{}, d.Template)
	}
	// Thread prior round's norms and constitutional rules into analysis
	if d.Round > 1 {
		prevResult, _ := s.store.GetAnalysisResult(deliberationID, d.Round-1)
		if prevResult != nil {
			if len(prevResult.EmergentNorms) > 0 {
				analysisCtx = context.WithValue(analysisCtx, ContextKeyPriorNorms{}, prevResult.EmergentNorms)
			}
			if len(prevResult.ConstitutionalRules) > 0 {
				analysisCtx = context.WithValue(analysisCtx, ContextKeyConstitutionalRules{}, prevResult.ConstitutionalRules)
			}
		}
	}

	s.emit("analysis_started", deliberationID, "", "")
	progressFn := ProgressFunc(func(subStatus string) {
		_ = s.store.UpdateSubStatus(deliberationID, subStatus)
		s.emit("analysis_progress", deliberationID, "", subStatus)
	})
	analysisCtx = context.WithValue(analysisCtx, ContextKeyProgressFunc{}, progressFn)

	result, err := s.analyzer.Analyze(analysisCtx, positions, votes, agents)
	if err != nil {
		resetStatus()
		return nil, fmt.Errorf("analysis failed: %w", err)
	}

	// Surface any pending disputes as integrity warnings
	disputes, _ := s.store.GetDisputes(deliberationID)
	for _, disp := range disputes {
		result.IntegrityWarnings = append(result.IntegrityWarnings,
			fmt.Sprintf("DISPUTED: agent %q challenges crux %q — correction: %s",
				disp.AgentID, truncate(disp.CruxClaim, 80), truncate(disp.Correction, 200)),
		)
	}

	result.DeliberationID = deliberationID
	result.Round = d.Round

	// Round drift detection: compare with previous round's analysis
	if d.Round > 0 {
		if driftWarnings := s.detectRoundDrift(deliberationID, d.Round, result, votes); len(driftWarnings) > 0 {
			result.IntegrityWarnings = append(result.IntegrityWarnings, driftWarnings...)
		}
	}

	if err := s.store.SaveAnalysisResult(deliberationID, d.Round, result); err != nil {
		resetStatus()
		return nil, err
	}

	if err := s.store.AdvanceRound(deliberationID); err != nil {
		return nil, err
	}

	if err := s.store.UpdateDeliberationStatus(deliberationID, "open"); err != nil {
		return nil, err
	}

	s.emit("analysis_complete", deliberationID, "", fmt.Sprintf("round_%d", d.Round))
	return result, nil
}

func (s *Service) GetContext(deliberationID, agentID string) (*AgentContext, error) {
	result, err := s.store.GetLatestAnalysisResult(deliberationID)
	if err != nil {
		return nil, fmt.Errorf("no analysis results found: %w", err)
	}

	// Record that this agent accessed context (for forced acknowledgment)
	if d, err := s.store.GetDeliberation(deliberationID); err == nil {
		_ = s.store.RecordContextAccess(deliberationID, agentID, d.Round)
	}

	ctx := &AgentContext{
		AgentID:              agentID,
		NearestAllies:        []string{},
		BiggestDisagreements: []string{},
		RelevantCruxes:       []Crux{},
		IntegrityWarnings:    result.IntegrityWarnings,
	}

	// Find the agent's cluster
	for _, c := range result.Clusters {
		for _, id := range c.AgentIDs {
			if id == agentID {
				clusterID := c.ID
				ctx.ClusterID = &clusterID
				for _, ally := range c.AgentIDs {
					if ally != agentID {
						ctx.NearestAllies = append(ctx.NearestAllies, ally)
					}
				}
				break
			}
		}
	}

	// Find cruxes involving this agent
	for _, crux := range result.Cruxes {
		involved := false
		isAgreer := false
		for _, a := range crux.AgreeAgents {
			if a == agentID {
				involved = true
				isAgreer = true
				break
			}
		}
		if !involved {
			for _, a := range crux.DisagreeAgents {
				if a == agentID {
					involved = true
					break
				}
			}
		}
		if involved {
			ctx.RelevantCruxes = append(ctx.RelevantCruxes, crux)
			if isAgreer {
				for _, d := range crux.DisagreeAgents {
					if !contains(ctx.BiggestDisagreements, d) {
						ctx.BiggestDisagreements = append(ctx.BiggestDisagreements, d)
					}
				}
			} else {
				for _, a := range crux.AgreeAgents {
					if !contains(ctx.BiggestDisagreements, a) {
						ctx.BiggestDisagreements = append(ctx.BiggestDisagreements, a)
					}
				}
			}
		}
	}

	// Surface topic summaries (discussion landscape overview)
	if len(result.TopicSummaries) > 0 {
		ctx.TopicSummaries = result.TopicSummaries
	}

	// Compute pairwise alignment scores from crux positions
	ctx.AlignmentScores, ctx.SwingAgents = computeAlignments(agentID, result.Cruxes)

	// Surface bridging and consensus statements
	if len(result.BridgingStatements) > 0 {
		ctx.BridgingStatements = result.BridgingStatements
	}
	if len(result.ConsensusStatements) > 0 {
		ctx.ConsensusStatements = result.ConsensusStatements
	}

	// Surface this agent's effective weight
	if w, ok := result.EffectiveWeights[agentID]; ok {
		ctx.EffectiveWeight = w
	}

	// Surface cooperation data: compromise, failure scenarios, constitutional rules
	ctx.CompromiseProposal = result.CompromiseProposal
	ctx.FailureScenarios = result.FailureScenarios
	ctx.ConstitutionalRules = result.ConstitutionalRules
	ctx.EmergentNorms = result.EmergentNorms
	ctx.RuleViolations = result.RuleViolations

	// Anti-sycophancy: generate a diversity nudge based on the agent's unique position
	ctx.DiversityNudge = buildDiversityNudge(ctx, result)

	// Strategic nudge: actionable guidance based on alignment, bridging, and swing agents
	ctx.StrategicNudge = buildStrategicNudge(ctx, result)

	// Surface pending invitations for this agent
	if invitations, err := s.store.GetInvitations(deliberationID); err == nil {
		for _, inv := range invitations {
			if inv.InvitedAgent == agentID && inv.Status == "pending" {
				ctx.PendingInvitations = append(ctx.PendingInvitations, inv)
			}
		}
	}

	return ctx, nil
}

// buildDiversityNudge generates a message encouraging agents to maintain genuine disagreement.
// Adapts FREE-MAD's anti-conformity mechanism for MCP: rather than modifying agent prompts
// directly, we provide context that agents can use to resist sycophantic convergence.
func buildDiversityNudge(ctx *AgentContext, result *AnalysisResult) string {
	if len(ctx.RelevantCruxes) == 0 {
		return ""
	}

	// Count how many cruxes this agent is in the minority on
	minorityCruxes := 0
	for _, crux := range ctx.RelevantCruxes {
		for _, a := range crux.AgreeAgents {
			if a == ctx.AgentID && len(crux.AgreeAgents) < len(crux.DisagreeAgents) {
				minorityCruxes++
			}
		}
		for _, a := range crux.DisagreeAgents {
			if a == ctx.AgentID && len(crux.DisagreeAgents) < len(crux.AgreeAgents) {
				minorityCruxes++
			}
		}
	}

	if minorityCruxes > 0 {
		return fmt.Sprintf(
			"You hold a minority position on %d crux(es). Your perspective is valuable precisely because it differs from the majority. "+
				"If you genuinely still hold these views after seeing the analysis, maintain them — minority viewpoints often surface important considerations that majorities overlook. "+
				"Only change your position if you've been genuinely persuaded by specific arguments, not because of social pressure to conform.",
			minorityCruxes,
		)
	}

	if len(ctx.BiggestDisagreements) > 0 {
		return fmt.Sprintf(
			"You have significant disagreements with %d agent(s). These disagreements drive the deliberation's crux detection. "+
				"If you refine your position, focus on addressing the specific crux claims rather than moving toward generic agreement.",
			len(ctx.BiggestDisagreements),
		)
	}

	return ""
}

// computeAlignments calculates pairwise alignment scores between this agent and
// all other agents based on crux positions, and identifies swing agents who are
// undecided on many cruxes (and thus persuadable).
func computeAlignments(agentID string, cruxes []Crux) ([]AgentAlignment, []string) {
	// Build a map of agent -> crux positions: +1 = agree, -1 = disagree, 0 = no position
	type position int8
	const agree position = 1
	const disagree position = -1

	agentPositions := map[string]map[int]position{} // agent -> crux_index -> position
	allAgents := map[string]bool{}
	noClearCounts := map[string]int{} // how many cruxes each agent has no_clear_position on

	for i, crux := range cruxes {
		for _, a := range crux.AgreeAgents {
			allAgents[a] = true
			if agentPositions[a] == nil {
				agentPositions[a] = map[int]position{}
			}
			agentPositions[a][i] = agree
		}
		for _, a := range crux.DisagreeAgents {
			allAgents[a] = true
			if agentPositions[a] == nil {
				agentPositions[a] = map[int]position{}
			}
			agentPositions[a][i] = disagree
		}
		for _, a := range crux.NoClearPosition {
			allAgents[a] = true
			noClearCounts[a]++
		}
	}

	if len(cruxes) == 0 {
		return nil, nil
	}

	// Compute alignment with each other agent
	myPositions := agentPositions[agentID]
	var alignments []AgentAlignment
	for other := range allAgents {
		if other == agentID {
			continue
		}
		otherPos := agentPositions[other]
		shared := 0
		aligned := 0
		for i := range cruxes {
			myP, myOk := myPositions[i]
			otherP, otherOk := otherPos[i]
			if myOk && otherOk {
				shared++
				if myP == otherP {
					aligned++
				}
			}
		}
		if shared > 0 {
			alignments = append(alignments, AgentAlignment{
				AgentID:        other,
				AlignmentScore: float64(aligned) / float64(shared),
				SharedCruxes:   shared,
				AgreeCruxes:    aligned,
			})
		}
	}

	// Sort by alignment score descending, tiebreak by agent name for determinism
	sort.Slice(alignments, func(i, j int) bool {
		if alignments[i].AlignmentScore != alignments[j].AlignmentScore {
			return alignments[i].AlignmentScore > alignments[j].AlignmentScore
		}
		return alignments[i].AgentID < alignments[j].AgentID
	})

	// Identify swing agents: no_clear_position on >= 40% of cruxes
	threshold := float64(len(cruxes)) * 0.4
	var swingAgents []string
	for agent, count := range noClearCounts {
		if agent != agentID && float64(count) >= threshold {
			swingAgents = append(swingAgents, agent)
		}
	}
	sort.Strings(swingAgents)

	return alignments, swingAgents
}

// buildStrategicNudge generates actionable guidance based on the agent's position
// in the deliberation landscape — which bridging statements to build on, which
// agents are persuadable, and where there's opportunity for coalition building.
func buildStrategicNudge(ctx *AgentContext, result *AnalysisResult) string {
	var parts []string

	// 1. Highlight bridging opportunities
	if len(ctx.BridgingStatements) > 0 {
		// Find bridging statements from agents outside this agent's cluster
		for _, bs := range ctx.BridgingStatements {
			if bs.AgentID != ctx.AgentID && bs.BridgingScore >= 0.5 {
				parts = append(parts, fmt.Sprintf(
					"Position by %s has cross-cluster support (%.0f%% bridging score). Consider building on it to find common ground.",
					bs.AgentID, bs.BridgingScore*100,
				))
				break // one is enough
			}
		}
	}

	// 2. Identify persuadable agents on specific cruxes
	if len(ctx.SwingAgents) > 0 {
		// Find which cruxes the swing agents are undecided on
		for _, swing := range ctx.SwingAgents {
			var undecidedCruxes []string
			for _, crux := range ctx.RelevantCruxes {
				for _, a := range crux.NoClearPosition {
					if a == swing {
						undecidedCruxes = append(undecidedCruxes, crux.Claim)
						break
					}
				}
			}
			if len(undecidedCruxes) > 0 {
				cruxDesc := undecidedCruxes[0]
				if len(cruxDesc) > 80 {
					cruxDesc = cruxDesc[:80] + "..."
				}
				parts = append(parts, fmt.Sprintf(
					"%s is undecided on %d crux(es), including: \"%s\". They may be open to persuasion.",
					swing, len(undecidedCruxes), cruxDesc,
				))
				break // one swing agent recommendation is enough
			}
		}
	}

	// 3. Note strongest alignment for coalition building
	if len(ctx.AlignmentScores) > 0 {
		top := ctx.AlignmentScores[0] // already sorted descending
		if top.AlignmentScore >= 0.67 && top.SharedCruxes >= 2 {
			parts = append(parts, fmt.Sprintf(
				"You and %s agree on %d of %d cruxes (%.0f%% aligned). A coalition could strengthen both positions.",
				top.AgentID, top.AgreeCruxes, top.SharedCruxes, top.AlignmentScore*100,
			))
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

func (s *Service) ReframePosition(ctx context.Context, deliberationID, positionID string) (string, error) {
	if s.reframer == nil {
		return "", fmt.Errorf("reframing not available")
	}

	pos, err := s.store.GetPositionByID(positionID)
	if err != nil {
		return "", fmt.Errorf("position not found: %w", err)
	}

	// Get other positions for context
	positions, err := s.store.GetPositions(deliberationID, nil)
	if err != nil {
		return "", err
	}
	var otherSummary string
	for _, p := range positions {
		if p.ID != positionID {
			otherSummary += fmt.Sprintf("- %s: %s\n", p.AgentID, p.Content)
		}
	}
	if otherSummary == "" {
		otherSummary = "No other positions submitted yet."
	}

	// Get cruxes if available
	var cruxSummary string
	result, err := s.store.GetLatestAnalysisResult(deliberationID)
	if err == nil {
		for _, c := range result.Cruxes {
			cruxSummary += fmt.Sprintf("- %s (agree: %v, disagree: %v)\n", c.Claim, c.AgreeAgents, c.DisagreeAgents)
		}
	}
	if cruxSummary == "" {
		cruxSummary = "No cruxes detected yet."
	}

	return s.reframer.Reframe(ctx, pos.Content, otherSummary, cruxSummary)
}

// resolveDelegations expands votes to include delegated votes.
// If alice delegates to bob, bob's votes count for alice too (on positions alice hasn't voted on).
func resolveDelegations(votes []Vote, delegations []Delegation) []Vote {
	if len(delegations) == 0 {
		return votes
	}

	// Build delegation graph: from -> to (active only)
	delegateOf := map[string]string{}
	for _, d := range delegations {
		if d.Active {
			delegateOf[d.FromAgent] = d.ToAgent
		}
	}

	// Resolve transitive chains (max depth 5)
	resolve := func(agent string) string {
		seen := map[string]bool{agent: true}
		current := agent
		for i := 0; i < 5; i++ {
			next, ok := delegateOf[current]
			if !ok || seen[next] {
				return current
			}
			seen[next] = true
			current = next
		}
		return current
	}

	// Build existing vote set
	voted := map[string]map[string]bool{} // agent -> position -> voted
	for _, v := range votes {
		if voted[v.AgentID] == nil {
			voted[v.AgentID] = map[string]bool{}
		}
		voted[v.AgentID][v.PositionID] = true
	}

	// For each delegator, copy delegatee's votes where delegator hasn't voted
	var expanded []Vote
	expanded = append(expanded, votes...)
	for from := range delegateOf {
		effective := resolve(from)
		if effective == from {
			continue // no actual delegation
		}
		// Copy effective delegatee's votes for positions from hasn't voted on
		for _, v := range votes {
			if v.AgentID == effective {
				if voted[from] != nil && voted[from][v.PositionID] {
					continue // direct vote overrides
				}
				expanded = append(expanded, Vote{
					ID:             "delegated-" + from + "-" + v.PositionID,
					DeliberationID: v.DeliberationID,
					AgentID:        from,
					PositionID:     v.PositionID,
					Value:          v.Value,
					CreatedAt:      v.CreatedAt,
				})
			}
		}
	}

	return expanded
}

func (s *Service) Delegate(deliberationID, fromAgent, toAgent, scope string) (*Delegation, error) {
	// Delegation cap: no agent can receive more than 3 delegations
	// Prevents power concentration (Uniswap VC-delegate pattern)
	delegations, _ := s.store.GetDelegations(deliberationID)
	count := 0
	for _, existing := range delegations {
		if existing.Active && existing.ToAgent == toAgent {
			count++
		}
	}
	if count >= 3 {
		return nil, fmt.Errorf("delegation cap reached: %s already has %d delegations (max 3)", toAgent, count)
	}

	d := &Delegation{
		DeliberationID: deliberationID,
		FromAgent:      fromAgent,
		ToAgent:        toAgent,
		Scope:          scope,
	}
	if err := s.store.CreateDelegation(d); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) RevokeDelegation(deliberationID, fromAgent string) error {
	return s.store.RevokeDelegation(deliberationID, fromAgent)
}

func (s *Service) PublishPosition(positionID string) error {
	return s.store.PublishPosition(positionID)
}

func (s *Service) Commit(deliberationID, agentID, statement, conditional string) (*Commitment, error) {
	d, err := s.store.GetDeliberation(deliberationID)
	if err != nil {
		return nil, fmt.Errorf("deliberation not found: %w", err)
	}
	c := &Commitment{
		DeliberationID: deliberationID,
		AgentID:        agentID,
		AnalysisRound:  d.Round - 1, // commit to the latest completed analysis
		Statement:      statement,
		Conditional:    conditional,
	}
	if err := s.store.CreateCommitment(c); err != nil {
		return nil, err
	}

	// Check if conditional commitments should activate
	if conditional == "" {
		c.Status = "active"
		_ = s.store.UpdateCommitmentStatus(c.ID, "active")
	}
	return c, nil
}

func (s *Service) GetCommitments(deliberationID string) ([]Commitment, error) {
	return s.store.GetCommitments(deliberationID)
}

// GenerateJoinCode creates a join code for a deliberation.
// Optional maxUses controls how many agents can use the same code (default 1 = single-use).
// Sandbox codes should use maxUses > 1 so every visitor to the /try page can join.
func (s *Service) GenerateJoinCode(deliberationID, role string, ttl time.Duration, maxUses ...int) (*JoinCode, error) {
	if _, err := s.store.GetDeliberation(deliberationID); err != nil {
		return nil, fmt.Errorf("deliberation not found: %w", err)
	}

	mu := 1
	if len(maxUses) > 0 && maxUses[0] > 0 {
		mu = maxUses[0]
	}

	// Generate a memorable, human-readable code like "bold-cedar-7291"
	code := generateMemorableCode()

	jc := &JoinCode{
		Code:           code,
		DeliberationID: deliberationID,
		Role:           role,
		ExpiresAt:      time.Now().Add(ttl),
		MaxUses:        mu,
		CreatedAt:      time.Now(),
	}
	if err := s.store.CreateJoinCode(jc); err != nil {
		return nil, err
	}
	return jc, nil
}

// JoinDeliberation claims a join code and adds the agent to the deliberation.
// Returns the deliberation ID so the agent knows where to participate.
func (s *Service) JoinDeliberation(code, agentID string) (string, string, error) {
	jc, err := s.store.ClaimJoinCode(code, agentID)
	if err != nil {
		return "", "", err
	}
	return jc.DeliberationID, jc.Role, nil
}

// LookupJoinCode returns join code metadata without claiming it.
func (s *Service) LookupJoinCode(code string) (*JoinCode, *Deliberation, error) {
	jc, err := s.store.LookupJoinCode(code)
	if err != nil {
		return nil, nil, err
	}
	d, err := s.store.GetDeliberation(jc.DeliberationID)
	if err != nil {
		return jc, nil, nil
	}
	return jc, d, nil
}

// CheckAccess verifies that the given key_id has access to the deliberation.
// Returns nil if access is allowed, error if denied.
func (s *Service) CheckAccess(deliberationID, keyID string) error {
	if keyID == "" {
		return nil // admin or dev mode
	}
	d, err := s.store.GetDeliberation(deliberationID)
	if err != nil {
		return fmt.Errorf("deliberation not found: %w", err)
	}
	switch d.Visibility {
	case "open", "":
		return nil // anyone can access
	case "link":
		return nil // UUID is the capability token
	case "private":
		if d.CreatorKey == keyID {
			return nil
		}
		allowed, err := s.store.CheckACL(deliberationID, keyID)
		if err != nil || !allowed {
			return fmt.Errorf("access denied: this is a private deliberation")
		}
		return nil
	}
	return nil
}

func (s *Service) InviteAgent(deliberationID, invitedBy, invitedAgent, role, reason string) (*Invitation, error) {
	if len(reason) > maxContentLen {
		return nil, fmt.Errorf("reason exceeds %d characters", maxContentLen)
	}
	validRoles := map[string]bool{"moderator": true, "expert": true, "mediator": true, "observer": true, "": true}
	if !validRoles[role] {
		return nil, fmt.Errorf("invalid role %q — use moderator, expert, mediator, or observer", role)
	}
	inv := &Invitation{
		DeliberationID: deliberationID,
		InvitedBy:      invitedBy,
		InvitedAgent:   invitedAgent,
		Role:           role,
		Reason:         reason,
	}
	if err := s.store.CreateInvitation(inv); err != nil {
		return nil, err
	}
	return inv, nil
}

func (s *Service) GetInvitations(deliberationID string) ([]Invitation, error) {
	return s.store.GetInvitations(deliberationID)
}

func (s *Service) AcceptInvitation(invitationID string) error {
	return s.store.UpdateInvitationStatus(invitationID, "accepted")
}

func (s *Service) GetPositionByID(id string) (*Position, error) {
	return s.store.GetPositionByID(id)
}

func (s *Service) GetVotes(deliberationID string) ([]Vote, error) {
	return s.store.GetVotes(deliberationID)
}

func (s *Service) DisputeCrux(deliberationID, agentID, cruxClaim, correction string) (*Dispute, error) {
	if len(cruxClaim) > maxContentLen {
		return nil, fmt.Errorf("crux_claim exceeds %d characters", maxContentLen)
	}
	if len(correction) > maxContentLen {
		return nil, fmt.Errorf("correction exceeds %d characters", maxContentLen)
	}
	d := &Dispute{
		DeliberationID: deliberationID,
		AgentID:        agentID,
		CruxClaim:      cruxClaim,
		Correction:      correction,
	}
	if err := s.store.CreateDispute(d); err != nil {
		return nil, err
	}
	return d, nil
}

// RecoverStuck resets deliberations stuck in "analyzing" status back to "open"
// if they have been in that state for more than 10 minutes.
func (s *Service) RecoverStuck() (int, error) {
	return s.store.RecoverStuckAnalyzing(30 * time.Minute)
}

func (s *Service) GetAnalysisResult(deliberationID string, round int) (*AnalysisResult, error) {
	return s.store.GetAnalysisResult(deliberationID, round)
}

// CreateShareToken generates a random share token for a group and stores it.
// The token is 16 random bytes, hex-encoded (32 characters).
func (s *Service) CreateShareToken(groupID string) (string, error) {
	if groupID == "" {
		return "", fmt.Errorf("group_id is required")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	token := fmt.Sprintf("%x", b)
	expiresAt := time.Now().Add(30 * 24 * time.Hour) // 30 days
	if err := s.store.CreateShareToken(token, groupID, expiresAt); err != nil {
		return "", err
	}
	return token, nil
}

// LookupShareToken returns the group ID for a valid share token.
func (s *Service) LookupShareToken(token string) (string, error) {
	return s.store.LookupShareToken(token)
}

// detectRoundDrift compares the current analysis with the previous round's analysis.
// Flags suspiciously rapid convergence that may indicate sycophantic agreement or
// coordinated manipulation rather than genuine deliberation.
func (s *Service) detectRoundDrift(deliberationID string, currentRound int, current *AnalysisResult, currentVotes []Vote) []string {
	prev, err := s.store.GetAnalysisResult(deliberationID, currentRound-1)
	if err != nil || prev == nil {
		return nil
	}

	var warnings []string

	// 1. Check if crux count dropped dramatically (may indicate convergence or silencing)
	if len(prev.Cruxes) > 0 && len(current.Cruxes) == 0 {
		warnings = append(warnings, "DRIFT: all cruxes disappeared between rounds — check for artificial consensus")
	}

	// 2. Check if cluster count collapsed (everyone moved to one cluster)
	if len(prev.Clusters) >= 2 && len(current.Clusters) <= 1 {
		warnings = append(warnings,
			fmt.Sprintf("DRIFT: clusters collapsed from %d to %d between rounds — rapid convergence may indicate sycophantic agreement",
				len(prev.Clusters), len(current.Clusters)))
	}

	// 3. Check vote pattern shift: compare how agents voted on positions that exist in both rounds
	// Build vote maps for current round
	prevVotes, err := s.store.GetVotesByRound(deliberationID, currentRound-1)
	if err != nil || len(prevVotes) == 0 {
		return warnings
	}

	// Build per-agent vote vectors for both rounds
	prevAgentVotes := map[string]map[string]int{}
	for _, v := range prevVotes {
		if prevAgentVotes[v.AgentID] == nil {
			prevAgentVotes[v.AgentID] = map[string]int{}
		}
		prevAgentVotes[v.AgentID][v.PositionID] = v.Value
	}
	currAgentVotes := map[string]map[string]int{}
	for _, v := range currentVotes {
		if currAgentVotes[v.AgentID] == nil {
			currAgentVotes[v.AgentID] = map[string]int{}
		}
		currAgentVotes[v.AgentID][v.PositionID] = v.Value
	}

	// Count agents whose votes shifted toward agreement (disagree→agree or pass→agree)
	agentsShiftedToAgree := 0
	agentsWithComparableVotes := 0
	for agent, currVotes := range currAgentVotes {
		prevV, ok := prevAgentVotes[agent]
		if !ok {
			continue
		}
		shiftedToAgree := 0
		sharedPositions := 0
		for posID, currVal := range currVotes {
			if prevVal, ok := prevV[posID]; ok {
				sharedPositions++
				if prevVal < currVal { // moved toward agreement
					shiftedToAgree++
				}
			}
		}
		if sharedPositions >= 2 {
			agentsWithComparableVotes++
			if shiftedToAgree > sharedPositions/2 {
				agentsShiftedToAgree++
			}
		}
	}

	// Flag if >50% of agents with comparable votes shifted toward agreement
	if agentsWithComparableVotes >= 3 && agentsShiftedToAgree > agentsWithComparableVotes/2 {
		warnings = append(warnings,
			fmt.Sprintf("DRIFT: %d/%d agents shifted votes toward agreement between rounds — possible sycophantic convergence",
				agentsShiftedToAgree, agentsWithComparableVotes))
	}

	return warnings
}

var joinAdjectives = []string{
	"bold", "calm", "dark", "fair", "glad", "keen", "mild", "pure", "rare", "sage",
	"warm", "wise", "blue", "cool", "deep", "firm", "gold", "iron", "just", "kind",
	"lean", "neat", "open", "pale", "rich", "safe", "tall", "vast", "wild", "free",
	"apt", "dry", "fit", "hot", "icy", "low", "new", "odd", "raw", "shy",
	"dim", "due", "lax", "red", "tan", "wet", "big", "old", "few", "real",
	"grey", "pink", "soft", "hard", "thin", "long", "fast", "slow", "high", "flat",
	"true", "full", "late", "live", "ripe", "sane", "sure", "tidy", "wary", "zesty",
}

var joinNouns = []string{
	"cedar", "delta", "ember", "forge", "grove", "haven", "jewel", "knoll", "latch", "marsh",
	"nexus", "ocean", "pearl", "quill", "ridge", "shore", "torch", "union", "vault", "woods",
	"amber", "birch", "cliff", "drift", "field", "glade", "heron", "ivory", "maple", "oasis",
	"prism", "river", "spark", "thorn", "wheel", "cloud", "flame", "grain", "light", "orbit",
	"plume", "slate", "steam", "stone", "tidal", "bloom", "comet", "frost", "lotus", "pivot",
	"quartz", "rowan", "shade", "stork", "trove", "viola", "crane", "basin", "coral", "fjord",
	"aspen", "flint", "hazel", "linen", "petal", "sable", "terra", "wren", "finch", "olive",
}

func generateMemorableCode() string {
	b := make([]byte, 6)
	rand.Read(b) //nolint:errcheck
	adj := joinAdjectives[int(b[0])%len(joinAdjectives)]
	noun := joinNouns[int(b[1])%len(joinNouns)]
	// 6-digit suffix from 4 bytes = ~4 billion possibilities for the number alone
	num := (int(b[2])<<24 | int(b[3])<<16 | int(b[4])<<8 | int(b[5])) % 1000000
	return fmt.Sprintf("%s-%s-%06d", adj, noun, num)
}

// 70 adjectives × 70 nouns × 1,000,000 numbers = ~4.9 billion combinations
// At 10 guesses/sec with rate limiting, brute force = ~15 years

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
