package deliberation

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
	"github.com/justinstimatze/gemot/internal/bft"
	"github.com/justinstimatze/gemot/internal/sanitize"
	"github.com/justinstimatze/gemot/types"
)

// ErrAgentKeyNotFound is returned by AgentKeyStore.GetActiveAgentKey when the agent
// has no registered (non-revoked) public key. Verification helpers treat this as
// "no key" rather than a DB error and fall back to policy handling.
var ErrAgentKeyNotFound = errAgentKeyNotFound{}

type errAgentKeyNotFound struct{}

func (errAgentKeyNotFound) Error() string { return "agent_keys: no active key registered for agent" }

const (
	maxTopicLen       = 500
	maxDescriptionLen = 5000
	maxContentLen     = 50000
	maxAgentIDLen     = 200
	maxPositions      = 1000
)

// Store defines the persistence interface the service needs.
// Store sub-interfaces — split by domain for clarity and targeted mocking.

// DeliberationStore manages deliberation lifecycle.
type DeliberationStore interface {
	CreateDeliberation(ctx context.Context, d *Deliberation) error
	GetDeliberation(ctx context.Context, id string) (*Deliberation, error)
	ListDeliberations(ctx context.Context, limit, offset int, keyID string) ([]Deliberation, error)
	ListByGroup(ctx context.Context, groupID string, limit, offset int, keyID string) ([]Deliberation, error)
	ListByAgent(ctx context.Context, agentID string, limit, offset int, keyID string) ([]Deliberation, error)
	SetGroupID(ctx context.Context, deliberationID, groupID string) error
	UpdateDeliberationStatus(ctx context.Context, id, status string) error
	SaveResolution(ctx context.Context, id string, resolution *Resolution) error
	UpdateDeliberationTemplate(ctx context.Context, id, template string) error
	UpdateDeliberationRules(ctx context.Context, id string, rules map[string]any) error
	DeleteDeliberation(ctx context.Context, id string) error
	GetStatusChangedAt(ctx context.Context, deliberationID string) (time.Time, error)
	UpdateSubStatus(ctx context.Context, id, subStatus string) error
	TrySetAnalyzing(ctx context.Context, id string) (bool, error)
	AdvanceRound(ctx context.Context, id string) error
}

// PositionStore manages position CRUD.
type PositionStore interface {
	CreatePosition(ctx context.Context, p *Position) error
	GetPositions(ctx context.Context, deliberationID string, round *int) ([]Position, error)
	GetPositionByID(ctx context.Context, id string) (*Position, error)
	CountPositions(ctx context.Context, deliberationID string) (int, error)
	CheckParticipantCap(ctx context.Context, deliberationID, agentID string, maxParticipants int) (bool, bool, error)
	PublishPosition(ctx context.Context, id string) error
	WithdrawPositions(ctx context.Context, deliberationID, agentID string) error
}

// VoteStore manages votes and delegations.
type VoteStore interface {
	CreateVote(ctx context.Context, v *Vote) error
	GetVotes(ctx context.Context, deliberationID string) ([]Vote, error)
	GetVotesByRound(ctx context.Context, deliberationID string, round int) ([]Vote, error)
	DeleteVotesByAgent(ctx context.Context, deliberationID, agentID string) error
	CreateDelegation(ctx context.Context, d *Delegation) error
	RevokeDelegation(ctx context.Context, deliberationID, fromAgent string) error
	GetDelegations(ctx context.Context, deliberationID string) ([]Delegation, error)
	DeleteDelegationsByAgent(ctx context.Context, deliberationID, agentID string) error
}

// CommitmentStore manages commitments and reputation.
type CommitmentStore interface {
	CreateCommitment(ctx context.Context, c *Commitment) error
	GetCommitments(ctx context.Context, deliberationID string) ([]Commitment, error)
	GetCommitmentsByAgent(ctx context.Context, agentID string) ([]Commitment, error)
	GetCommitmentsByGroup(ctx context.Context, groupID string) ([]Commitment, error)
	UpdateCommitmentStatus(ctx context.Context, id, status string) error
	FulfillCommitment(ctx context.Context, id, verifiedBy string) error
	BreakCommitment(ctx context.Context, id, reason, verifiedBy string) error
}

// AnalysisStore manages analysis results and stuck recovery.
type AnalysisStore interface {
	SaveAnalysisResult(ctx context.Context, deliberationID string, round int, result *AnalysisResult) error
	GetAnalysisResult(ctx context.Context, deliberationID string, round int) (*AnalysisResult, error)
	GetLatestAnalysisResult(ctx context.Context, deliberationID string) (*AnalysisResult, error)
	GetAllAnalysisResults(ctx context.Context, deliberationID string) ([]AnalysisResult, error)
	GetStuckAnalyzing(ctx context.Context, maxAge time.Duration) ([]string, error)
	RecoverStuckAnalyzing(ctx context.Context, maxAge time.Duration) (int, error)
}

// AccessStore manages ACLs, join codes, invitations, and share tokens.
type AccessStore interface {
	CreateJoinCode(ctx context.Context, jc *JoinCode) error
	ClaimJoinCode(ctx context.Context, code, agentID string) (*JoinCode, error)
	LookupJoinCode(ctx context.Context, code string) (*JoinCode, error)
	AddToACL(ctx context.Context, deliberationID, keyID string) error
	CheckACL(ctx context.Context, deliberationID, keyID string) (bool, error)
	CreateInvitation(ctx context.Context, inv *Invitation) error
	GetInvitations(ctx context.Context, deliberationID string) ([]Invitation, error)
	UpdateInvitationStatus(ctx context.Context, id, status string) error
	CreateShareToken(ctx context.Context, token, groupID string, expiresAt time.Time) error
	LookupShareToken(ctx context.Context, token string) (groupID string, err error)
}

// AgentKeyStore manages per-agent public keys used for signed positions and votes.
type AgentKeyStore interface {
	RegisterAgentKey(ctx context.Context, agentID string, publicKey []byte, algo string) error
	GetActiveAgentKey(ctx context.Context, agentID string) (publicKey []byte, algo string, err error)
	RevokeAgentKey(ctx context.Context, agentID string) error
}

// ModerationStore manages disputes, abuse reports, and context access.
type ModerationStore interface {
	CreateDispute(ctx context.Context, d *Dispute) error
	GetDisputes(ctx context.Context, deliberationID string) ([]Dispute, error)
	GetUnprocessedDisputes(ctx context.Context, deliberationID string) ([]Dispute, error)
	MarkDisputesProcessed(ctx context.Context, disputeIDs []string) error
	CreateAbuseReport(ctx context.Context, deliberationID, reporterKey, reason string) error
	RecordContextAccess(ctx context.Context, deliberationID, agentID string, round int) error
	HasContextAccess(ctx context.Context, deliberationID, agentID string, round int) (bool, error)
}

// Store composes all sub-interfaces. Implementations (store.DB) satisfy
// this by implementing every method. Tests can mock individual sub-interfaces.
type Store interface {
	DeliberationStore
	PositionStore
	VoteStore
	CommitmentStore
	AnalysisStore
	AccessStore
	ModerationStore
	AgentKeyStore
}

// ContextKeyDeliberationID is the context key for passing the deliberation ID
// through the analysis pipeline (used for per-deliberation cost tracking).
type ContextKeyDeliberationID struct{}
type ContextKeyDeliberationType struct{}
type ContextKeyPriorNorms struct{}
type ContextKeyConstitutionalRules struct{}
type ContextKeyPriorClaims struct{}

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

// ChoiceCompromiseGenerator is the forced-choice variant used by the
// calibration runner. The mechanism's compromise is constrained to
// exactly one of `options` via the LLM tool_use schema enum, so the
// fleet's "answer" is deterministically extractable for scoring against
// a known-correct corpus answer key. optionVotes carries the per-option
// agent vote distribution so the compromise prompt can treat the agents'
// explicit choices as a strong prior — without it the LLM synthesizes
// from claim-level analysis alone and can override unanimous votes
// (root cause of the 2026-06-05 Haiku fleet-vs-vote-only gap).
type ChoiceCompromiseGenerator interface {
	GenerateCompromiseWithChoice(ctx context.Context, topic string, result *AnalysisResult, options []string, optionVotes map[string]int) (statement, selectedOption string, err error)
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

// WithParentPosition marks this position as an amendment to an existing motion.
func WithParentPosition(id string) PositionOption {
	return func(p *Position) { p.ParentPositionID = id }
}

// WithMetadata sets extensible metadata on a position (lat, lon, label, etc.).
func WithMetadata(m map[string]any) PositionOption {
	return func(p *Position) { p.Metadata = m }
}

// WithSignature attaches an ed25519 signature over auth.PositionPayload.
// The signature is verified against the agent's registered public key at submit time.
func WithSignature(sig []byte) PositionOption {
	return func(p *Position) { p.Signature = sig }
}

// Service orchestrates deliberation operations.
// RoundReputationUpdater is the narrow contract the service needs for
// updating the reputation layer after each round. Defined here (not in
// the reputation package) to avoid a deliberation → reputation →
// analysis → deliberation import cycle.
type RoundReputationUpdater interface {
	UpdateFromRound(ctx context.Context, delibID string, isPrivate bool, cruxes []types.Crux, positionAuthors map[string]string, disputes []types.Dispute) error
}

type Service struct {
	store             Store
	analyzer          Analyzer
	compromiser       CompromiseGenerator
	reframer          Reframer
	contentClassifier sanitize.Classifier
	reputation        RoundReputationUpdater
	events            *EventBus // nil = no event emission

	// Active analysis cancellation: deliberation_id → cancel func.
	// Used by stuck recovery to kill zombie analysis goroutines.
	analysisMu     sync.Mutex
	activeAnalyses map[string]context.CancelFunc

	// Per-deliberation lock for vote+resolution atomicity.
	// Prevents concurrent votes from computing conflicting resolutions.
	resolutionMu    sync.Mutex
	resolutionLocks map[string]*sync.Mutex

	// Optional audit logger — set via SetAuditLogger after construction.
	// Logs all write operations at the service layer so nothing bypasses the audit trail.
	auditFn func(method, deliberationID, agentID string)

	// Optional BFT engine — when set, position submissions route
	// through the HotStuff state machine and return a prepared QC
	// as proof. nil = direct writes (legacy / tests without BFT).
	bftEngine *bft.Engine
}

func NewService(store Store, analyzer Analyzer) *Service {
	// Every service gets a BFT engine so action ordering is always
	// recorded in a tamper-evident log. Tests use an in-memory engine
	// auto-constructed here; production overrides with a durable
	// Postgres-backed engine via SetBFTEngine. A single code path —
	// no "BFT disabled" branch anywhere downstream.
	engine, err := bft.BootstrapSingleNode(
		context.Background(),
		bft.NewInMemoryLogStore(),
		bft.NewInMemoryVoteHistoryStore(),
		bft.NewInMemoryReplicaKeyStore(),
	)
	if err != nil {
		// Bootstrap only fails on BLS keygen (crypto-rand failure),
		// which is unreachable in practice — panic so the
		// misconfigured build surfaces immediately.
		panic(fmt.Sprintf("bft: bootstrap in-memory engine: %v", err))
	}
	return &Service{
		store:           store,
		analyzer:        analyzer,
		activeAnalyses:  make(map[string]context.CancelFunc),
		resolutionLocks: make(map[string]*sync.Mutex),
		bftEngine:       engine,
	}
}

// SetAuditLogger sets a function that logs all service-level write operations.
func (s *Service) SetAuditLogger(fn func(method, deliberationID, agentID string)) {
	s.auditFn = fn
}

// SetBFTEngine overrides the default in-memory engine (auto-
// constructed in NewService) with a durable one — typically the
// Postgres-backed engine wired in main.go. All services always have
// an engine; this setter just swaps which storage backs it.
func (s *Service) SetBFTEngine(e *bft.Engine) {
	s.bftEngine = e
}

// AuditLogEntry is a single row in the tamper-evident action log,
// surfaced by the admin:get_audit_log tool so users can verify that
// a given deliberation's actions were recorded in an append-only log
// the server cannot retroactively edit without detection.
type AuditLogEntry struct {
	// Height is the monotonic sequence number in the BFT log.
	Height int64 `json:"height"`
	// View is the consensus round that committed this entry.
	View int64 `json:"view"`
	// ActionType: submit_position, vote, commit, dispute_crux.
	ActionType string `json:"action_type"`
	// AgentID is the agent that initiated the action.
	AgentID string `json:"agent_id"`
	// BlockHash is the cryptographic hash of the committed block —
	// stable identifier users can reference when filing disputes.
	BlockHash string `json:"block_hash"`
	// Proof is the JSON-encoded QC that witnesses this action.
	// Clients holding the replica's public key (see
	// admin:replica_pubkey) can call bft.VerifyQC to confirm the
	// signature offline without trusting the server's report of its
	// own log. Safe to ignore if you don't need that guarantee.
	Proof []byte `json:"proof,omitempty"`
}

// ReplicaPublicKey returns the BLS public key of the server's
// single-replica cluster as 96 compressed G2 bytes. Clients fetch
// this once and cache it; combined with the `proof` field on audit
// log entries, a client can verify that every recorded action carries
// a valid server signature — so the server cannot retroactively
// rewrite the log without the client detecting the mismatch.
func (s *Service) ReplicaPublicKey() ([]byte, error) {
	return s.bftEngine.PublicKey()
}

// GetTamperEvidentLog returns the committed-log entries filtered to
// a single deliberation, parsed from the canonical pipe-delimited
// payloads. Entries are in the order the server committed them.
// Actions that have been submitted but not yet committed (the most
// recent 1 action under the two-chain rule) are not included —
// they will appear after the next action lands.
func (s *Service) GetTamperEvidentLog(ctx context.Context, deliberationID string) ([]AuditLogEntry, error) {
	entries, err := s.bftEngine.AuditEntries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AuditLogEntry, 0, len(entries))
	for _, e := range entries {
		parts := strings.Split(string(e.Block.Payload), "|")
		// Canonical payloads always carry deliberation_id as the
		// second field (after action_type) except for any hypothetical
		// server-initiated actions, which have none.
		if len(parts) < 3 {
			continue
		}
		if parts[1] != deliberationID {
			continue
		}
		h := e.Block.Hash()
		proof, _ := bft.EncodeQCProof(e.QC)
		out = append(out, AuditLogEntry{
			Height:     int64(e.Block.Height),
			View:       int64(e.Block.View),
			ActionType: parts[0],
			AgentID:    parts[2],
			BlockHash:  fmt.Sprintf("%x", h[:8]),
			Proof:      proof,
		})
	}
	return out, nil
}

// orderAction submits a write action through the BFT state machine
// so it lands in the tamper-evident log before the domain-table
// write. Every service mutation that should be audit-orderable
// (positions, votes, commitments, disputes) calls this. Payload is
// a pipe-joined canonical string so a later verify path can
// regenerate and match it byte-for-byte.
//
// Returns an error that callers wrap with their own context. The
// domain write should NOT proceed if this returns an error — the
// log is the source of truth for "did this action happen."
func (s *Service) orderAction(ctx context.Context, opType string, parts ...string) error {
	payload := []byte(opType)
	for _, p := range parts {
		payload = append(payload, '|')
		payload = append(payload, p...)
	}
	if _, _, err := s.bftEngine.Submit(ctx, payload); err != nil {
		return fmt.Errorf("order %s: %w", opType, err)
	}
	return nil
}

func (s *Service) audit(method, deliberationID, agentID string) {
	if s.auditFn != nil {
		s.auditFn(method, deliberationID, agentID)
	}
}

// resolutionLock returns a per-deliberation mutex for vote+resolution atomicity.
func (s *Service) resolutionLock(deliberationID string) *sync.Mutex {
	s.resolutionMu.Lock()
	defer s.resolutionMu.Unlock()
	mu, ok := s.resolutionLocks[deliberationID]
	if !ok {
		mu = &sync.Mutex{}
		s.resolutionLocks[deliberationID] = mu
	}
	return mu
}

// SetContentClassifier sets the LLM content screening function.
func (s *Service) SetContentClassifier(c sanitize.Classifier) {
	s.contentClassifier = c
}

// SetReputationUpdater wires the persistent EigenTrust + cold-start
// reputation layer. The service calls UpdateFromRound after each
// successful round analysis. Pass nil to disable.
func (s *Service) SetReputationUpdater(r RoundReputationUpdater) {
	s.reputation = r
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

	d, err := s.store.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return "", fmt.Errorf("deliberation not found: %w", err)
	}

	result, err := s.store.GetLatestAnalysisResult(ctx, deliberationID)
	if err != nil {
		return "", fmt.Errorf("no analysis results — run analyze first: %w", err)
	}

	// Unstructured analyzers (ChatAnalyzer) produce no cruxes but preserve the
	// raw positions as ExtractedClaims; a compromise can still be synthesized
	// from those. Only refuse when there is genuinely nothing to work with.
	if len(result.Cruxes) == 0 && len(result.ExtractedClaims) == 0 {
		return "", fmt.Errorf("nothing to compromise on — analysis has no cruxes or positions")
	}

	return s.compromiser.GenerateCompromise(ctx, d.Topic, result)
}

// ProposeCompromiseWithChoiceAndVotes is the forced-choice variant used
// by the calibration runner. The configured compromiser must also
// implement ChoiceCompromiseGenerator; if not, returns an error. Returns
// the compromise statement AND the LLM's selected option (one of the
// entries in `options`). optionVotes is the per-option agent choice
// distribution, surfaced to the compromise LLM as a strong prior so
// option-level consensus isn't overridden by claim-level rationale
// noise (the 2026-06-05 Haiku failure mode).
//
// The "no cruxes detected" guard the prior signature had has been
// removed: the runner now short-circuits unanimous cases before
// calling this, so reaching here implies real disagreement worth
// asking the LLM to resolve even if cruxes haven't crystallized at
// the analysis layer.
func (s *Service) ProposeCompromiseWithChoiceAndVotes(ctx context.Context, deliberationID string, options []string, optionVotes map[string]int) (string, string, error) {
	if s.compromiser == nil {
		return "", "", fmt.Errorf("compromise generation not available")
	}
	choicer, ok := s.compromiser.(ChoiceCompromiseGenerator)
	if !ok {
		return "", "", fmt.Errorf("compromise generator does not support forced-choice mode")
	}

	d, err := s.store.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return "", "", fmt.Errorf("deliberation not found: %w", err)
	}

	result, err := s.store.GetLatestAnalysisResult(ctx, deliberationID)
	if err != nil {
		return "", "", fmt.Errorf("no analysis results — run analyze first: %w", err)
	}

	return choicer.GenerateCompromiseWithChoice(ctx, d.Topic, result, options, optionVotes)
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

// WithDeadline sets an absolute deadline for the deliberation.
func WithDeadline(d time.Time) DeliberationOption {
	return func(del *Deliberation) { del.DeadlineAt = &d }
}

// WithSignaturePolicy sets the per-deliberation signature policy:
//   - "none"     (default): signatures ignored, unsigned submissions always accepted
//   - "advisory": accept unsigned submissions from agents with a registered key,
//     but emit UNSIGNED_POSITION / UNSIGNED_VOTE to the audit log
//   - "required": reject unsigned submissions from agents with a registered key
//
// Any submission that *does* carry a signature is verified in all modes; a bad
// signature is always rejected regardless of policy.
//
// Unknown values are normalized to "none" to avoid fail-open surprises if the
// verify switch sees an unexpected string. The DB also enforces the valid set
// via a CHECK constraint so an attempted persist with a bad value errors out.
func WithSignaturePolicy(policy string) DeliberationOption {
	switch policy {
	case "none", "advisory", "required":
	default:
		policy = "none"
	}
	return func(del *Deliberation) { del.SignaturePolicy = policy }
}

// ContextKeyTemplate is the context key for passing the template name through analysis.
type ContextKeyTemplate struct{}

// ContextKeyPriorTaxonomy passes prior round topic names for taxonomy stability.
type ContextKeyPriorTaxonomy struct{}

// ContextKeyPriorTopicIDs passes prior round topic ID→name mapping for stable IDs across rounds.
type ContextKeyPriorTopicIDs struct{}
type ContextKeyPriorSummaries struct{}

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

// RuleBool reads a boolean rule from a deliberation, returning the default if not set.
func RuleBool(d *Deliberation, key string, defaultVal bool) bool {
	if d.Rules == nil {
		return defaultVal
	}
	v, ok := d.Rules[key]
	if !ok {
		return defaultVal
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return defaultVal
}

func (s *Service) CreateDeliberation(ctx context.Context, topic, description string, opts ...DeliberationOption) (*Deliberation, error) {
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

	if err := s.store.CreateDeliberation(ctx, d); err != nil {
		return nil, err
	}

	// Auto-add creator to ACL for private deliberations
	if d.Visibility == "private" && d.CreatorKey != "" {
		if err := s.store.AddToACL(ctx, d.ID, d.CreatorKey); err != nil {
			fmt.Fprintf(os.Stderr, "gemot: warning: failed to add creator to ACL: %v\n", err)
		}
	}

	s.audit("deliberation:create", d.ID, "")
	s.emit("deliberation_created", d.ID, "", d.Topic)
	return d, nil
}

func (s *Service) GetDeliberation(ctx context.Context, id string) (*Deliberation, error) {
	return s.store.GetDeliberation(ctx, id)
}

// SetTemplate changes the governance template on an existing deliberation.
// Only the creator can change the template. Applies the new template's default
// rules (without overwriting any explicitly-set rules).
func (s *Service) SetTemplate(ctx context.Context, deliberationID, template, callerKeyID string) error {
	tmpl, ok := GetTemplate(template)
	if !ok {
		return fmt.Errorf("unknown template %q — use list_templates to see available templates", template)
	}
	d, err := s.store.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return fmt.Errorf("deliberation not found: %w", err)
	}
	if d.CreatorKey != "" && d.CreatorKey != callerKeyID {
		return fmt.Errorf("only the deliberation creator can change the template")
	}
	// Note: template name and rules are updated in two separate DB calls.
	// If the rules update fails, the template name is set but rules are stale.
	// This is acceptable because the rules update is a simple UPDATE that's
	// extremely unlikely to fail after the template update succeeded.
	if err := s.store.UpdateDeliberationTemplate(ctx, deliberationID, template); err != nil {
		return err
	}
	// Replace all rules with the new template's defaults.
	// Old template's rules are discarded — they're template-specific.
	rules := make(map[string]any)
	for k, v := range tmpl.DefaultRules {
		rules[k] = v
	}
	return s.store.UpdateDeliberationRules(ctx, deliberationID, rules)
}

// CheckAnalysisPreconditions runs the canonical pre-charge validation
// chain for paid analyze actions: existence, access, and (optionally)
// quorum. Both /mcp and /a2a transports MUST call this before consuming
// any payment credential (MPP receipt or credit deduction). Keeping the
// chain in one place eliminates the drift class where one transport
// gains a guard the other forgets — same play as the service-layer
// non-empty-statement check on decide:commit (44ed91c).
//
// requireQuorum is true for analyze:run (no point starting a paid
// analysis if the minimum-participant rule isn't met). It's false for
// secondary analyze actions (propose_compromise, follow_up) which run
// against a deliberation that already passed quorum in a prior round.
//
// Error wrapping is preserved to match the legacy transport-level
// strings: "deliberation not found: <err>" for the existence path,
// "<quorum err> — submit more positions before analyzing" for quorum,
// and the CheckAccess error verbatim ("access denied: …") otherwise.
func (s *Service) CheckAnalysisPreconditions(ctx context.Context, deliberationID, keyID string, requireQuorum bool) error {
	if _, err := s.GetDeliberation(ctx, deliberationID); err != nil {
		return fmt.Errorf("deliberation not found: %w", err)
	}
	if err := s.CheckAccess(ctx, deliberationID, keyID); err != nil {
		return err
	}
	if requireQuorum {
		if err := s.CheckQuorum(ctx, deliberationID); err != nil {
			return fmt.Errorf("%w — submit more positions before analyzing", err)
		}
	}
	return nil
}

// CheckQuorum returns an error if the deliberation has a min_participants rule
// and not enough distinct agents have submitted positions.
func (s *Service) CheckQuorum(ctx context.Context, deliberationID string) error {
	d, err := s.store.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return err
	}
	minP := RuleInt(d, "min_participants", 0)
	if minP <= 0 {
		return nil
	}
	positions, err := s.store.GetPositions(ctx, deliberationID, nil)
	if err != nil {
		return err
	}
	uniqueAgents := map[string]bool{}
	for _, p := range positions {
		uniqueAgents[p.AgentID] = true
	}
	if len(uniqueAgents) < minP {
		return fmt.Errorf("quorum not met: %d participant(s), need %d", len(uniqueAgents), minP)
	}
	return nil
}

// DeleteDeliberation removes a deliberation and all its data.
// Only the creator or an admin can delete.
func (s *Service) DeleteDeliberation(ctx context.Context, deliberationID, callerKeyID string, isAdmin bool) error {
	if !isAdmin {
		d, err := s.store.GetDeliberation(ctx, deliberationID)
		if err != nil {
			return fmt.Errorf("deliberation not found: %w", err)
		}
		if d.CreatorKey == "" || d.CreatorKey != callerKeyID {
			return fmt.Errorf("only the deliberation creator or admin can delete")
		}
	}
	if err := s.store.DeleteDeliberation(ctx, deliberationID); err != nil {
		return err
	}
	s.resolutionMu.Lock()
	delete(s.resolutionLocks, deliberationID)
	s.resolutionMu.Unlock()
	s.audit("deliberation:delete", deliberationID, "")
	return nil
}

// ReportAbuse files an abuse report for manual review.
func (s *Service) ReportAbuse(ctx context.Context, deliberationID, reporterKey, reason string) error {
	if _, err := s.store.GetDeliberation(ctx, deliberationID); err != nil {
		return fmt.Errorf("deliberation not found: %w", err)
	}
	return s.store.CreateAbuseReport(ctx, deliberationID, reporterKey, reason)
}

func (s *Service) GetLatestAnalysisResult(ctx context.Context, deliberationID string) (*AnalysisResult, error) {
	return s.store.GetLatestAnalysisResult(ctx, deliberationID)
}

func (s *Service) ListDeliberations(ctx context.Context, limit, offset int, keyID string) ([]Deliberation, error) {
	return s.store.ListDeliberations(ctx, limit, offset, keyID)
}

func (s *Service) ListByGroup(ctx context.Context, groupID string, limit, offset int, keyID string) ([]Deliberation, error) {
	return s.store.ListByGroup(ctx, groupID, limit, offset, keyID)
}

func (s *Service) ListByAgent(ctx context.Context, agentID string, limit, offset int, keyID string) ([]Deliberation, error) {
	return s.store.ListByAgent(ctx, agentID, limit, offset, keyID)
}

func (s *Service) SetGroupID(ctx context.Context, deliberationID, groupID string) error {
	return s.store.SetGroupID(ctx, deliberationID, groupID)
}

func (s *Service) SubmitPosition(ctx context.Context, deliberationID, agentID, content string, opts ...PositionOption) (*Position, error) {
	return s.SubmitPositionWithSigningID(ctx, deliberationID, agentID, agentID, content, opts...)
}

// SubmitPositionWithSigningID is the hosted-mode entry point that decouples
// the stored agent_id from the identity used in the signed canonical payload.
// MCP's hosted-mode transport scopes args.agent_id to "<keyID>:<agentID>"
// before calling the service; the client, however, signs with the unscoped
// agent_id it knows. Passing the unscoped form as signingAgentID lets the
// server reconstruct the exact bytes the client signed.
//
// Callers that don't scope agent_ids should use SubmitPosition, which forwards
// here with signingAgentID == agentID.
func (s *Service) SubmitPositionWithSigningID(ctx context.Context, deliberationID, agentID, signingAgentID, content string, opts ...PositionOption) (*Position, error) {
	if len(agentID) > maxAgentIDLen {
		return nil, fmt.Errorf("agent_id exceeds %d characters", maxAgentIDLen)
	}
	if len(content) > maxContentLen {
		return nil, fmt.Errorf("content exceeds %d characters", maxContentLen)
	}

	// Capture the raw content before sanitization. Signature verification must run
	// against the exact bytes the client signed — any server-side mutation
	// (PII redaction, normalization) invalidates the signed hash. The raw content
	// is threaded to verifyPositionSignature; the sanitized form is what we store.
	rawContent := content

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
		screenCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
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

	d, err := s.store.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return nil, fmt.Errorf("deliberation not found: %w", err)
	}
	if d.Status != "open" {
		return nil, fmt.Errorf("deliberation is %s, not accepting positions", d.Status)
	}
	if d.DeadlineAt != nil && time.Now().After(*d.DeadlineAt) {
		return nil, fmt.Errorf("deliberation deadline has passed")
	}

	// Forced acknowledgment: in round 2+, agents must call get_context first
	if d.Round > 1 {
		accessed, _ := s.store.HasContextAccess(ctx, deliberationID, agentID, d.Round)
		if !accessed {
			return nil, fmt.Errorf("round %d requires reviewing cruxes first — call get_context before submitting a new position", d.Round)
		}
	}

	count, err := s.store.CountPositions(ctx, deliberationID)
	if err != nil {
		return nil, err
	}
	if count >= maxPositions {
		return nil, fmt.Errorf("deliberation has reached the maximum of %d positions", maxPositions)
	}

	// Enforce max_participants cap (single query, no position loading)
	if d.MaxParticipants > 0 {
		capped, alreadyIn, err := s.store.CheckParticipantCap(ctx, deliberationID, agentID, d.MaxParticipants)
		if err == nil && capped && !alreadyIn {
			return nil, fmt.Errorf("deliberation has reached the maximum of %d participants", d.MaxParticipants)
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

	// Enforce speaking_time_limit rule (max chars per position)
	if limit := RuleInt(d, "speaking_time_limit", 0); limit > 0 && len(content) > limit {
		return nil, fmt.Errorf("position exceeds speaking time limit of %d characters", limit)
	}

	// Enforce require_second rule: new motions start as drafts until seconded
	if RuleBool(d, "require_second", false) {
		p.Draft = true
	}

	if err := s.verifyPositionSignature(ctx, d, p, rawContent, signingAgentID); err != nil {
		return nil, err
	}
	// If sanitization mutated the content, the stored signature no longer verifies
	// against the stored content even though it was valid at submit time. The audit
	// event above records that verification succeeded; warn the operator so later
	// readers understand why post-hoc reverification would fail.
	if len(p.Signature) > 0 && rawContent != content {
		fmt.Fprintf(os.Stderr, "gemot: SIG_SANITIZED position in %s from agent %q — signature was valid at submit but content was sanitized (stored signature will not reverify against stored content)\n", deliberationID, agentID)
	}

	// Order the submission through the tamper-evident log BEFORE the
	// domain-table write. Log is the source of truth for "did this
	// happen"; if ordering fails, we must not persist the position.
	if err := s.orderAction(ctx, "submit_position", deliberationID, agentID, fmt.Sprintf("%d", p.Round), content); err != nil {
		return nil, err
	}

	if err := s.store.CreatePosition(ctx, p); err != nil {
		return nil, err
	}
	s.audit("participate:submit_position", deliberationID, agentID)
	s.emitWithData("position_submitted", deliberationID, agentID, p.ID, map[string]any{
		"position_id": p.ID,
		"content":     p.Content,
		"round":       p.Round,
	})
	return p, nil
}

func (s *Service) GetPositions(ctx context.Context, deliberationID string, excludeAgentID *string, round *int) ([]Position, error) {
	positions, err := s.store.GetPositions(ctx, deliberationID, round)
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

func (s *Service) Vote(ctx context.Context, deliberationID, agentID, positionID string, value int, qualifier, caveat string, criterionID ...string) error {
	v := &Vote{
		DeliberationID: deliberationID,
		AgentID:        agentID,
		PositionID:     positionID,
		Value:          value,
		Qualifier:      qualifier,
		Caveat:         caveat,
	}
	if len(criterionID) > 0 && criterionID[0] != "" {
		v.CriterionID = criterionID[0]
	}
	return s.castVote(ctx, v, agentID)
}

// SubmitSignedVote is the signature-aware entry point. Parallel to Vote() but
// attaches an ed25519 signature that is verified against the agent's registered
// public key before the vote is recorded.
func (s *Service) SubmitSignedVote(ctx context.Context, deliberationID, agentID, positionID string, value int, qualifier, caveat, criterionID string, signature []byte) error {
	return s.SubmitSignedVoteWithSigningID(ctx, deliberationID, agentID, agentID, positionID, value, qualifier, caveat, criterionID, signature)
}

// SubmitSignedVoteWithSigningID is the hosted-mode entry point for signed
// votes. See SubmitPositionWithSigningID — same rationale: the canonical
// payload's agent_id must match what the client signed, even when the server
// has scoped the stored agent_id for namespace isolation.
func (s *Service) SubmitSignedVoteWithSigningID(ctx context.Context, deliberationID, agentID, signingAgentID, positionID string, value int, qualifier, caveat, criterionID string, signature []byte) error {
	v := &Vote{
		DeliberationID: deliberationID,
		AgentID:        agentID,
		PositionID:     positionID,
		Value:          value,
		Qualifier:      qualifier,
		Caveat:         caveat,
		CriterionID:    criterionID,
		Signature:      signature,
	}
	return s.castVote(ctx, v, signingAgentID)
}

// castVote is the shared body for Vote and SubmitSignedVote. It does input
// validation, loads the deliberation + position, verifies any attached signature,
// persists the vote, and handles seconding.
//
// signingAgentID is the identity used to reconstruct the signed canonical
// payload. For direct callers it matches v.AgentID; hosted-mode callers pass
// the unscoped agent_id the client actually signed with.
func (s *Service) castVote(ctx context.Context, v *Vote, signingAgentID string) error {
	if len(v.AgentID) > maxAgentIDLen {
		return fmt.Errorf("agent_id exceeds %d characters", maxAgentIDLen)
	}

	d, err := s.store.GetDeliberation(ctx, v.DeliberationID)
	if err != nil {
		return fmt.Errorf("deliberation not found: %w", err)
	}
	if d.Status != "open" {
		return fmt.Errorf("deliberation is %s, not accepting votes", d.Status)
	}
	if d.DeadlineAt != nil && time.Now().After(*d.DeadlineAt) {
		return fmt.Errorf("deliberation deadline has passed")
	}

	pos, err := s.store.GetPositionByID(ctx, v.PositionID)
	if err != nil {
		return fmt.Errorf("position not found: %w", err)
	}
	if pos.DeliberationID != v.DeliberationID {
		return fmt.Errorf("position does not belong to this deliberation")
	}

	if v.Value < -2 || v.Value > 2 {
		return fmt.Errorf("vote value must be between -2 and 2")
	}

	if err := s.verifyVoteSignature(ctx, d, v, signingAgentID); err != nil {
		return err
	}

	// Lock per-deliberation to ensure vote+resolution is atomic.
	mu := s.resolutionLock(v.DeliberationID)
	mu.Lock()
	defer mu.Unlock()

	// Tamper-evident log BEFORE domain write.
	if err := s.orderAction(ctx, "vote",
		v.DeliberationID, v.AgentID, v.PositionID,
		fmt.Sprintf("%d", v.Value), v.Qualifier, v.CriterionID,
	); err != nil {
		return err
	}

	if err := s.store.CreateVote(ctx, v); err != nil {
		return err
	}
	s.audit("participate:vote", v.DeliberationID, v.AgentID)
	s.emitWithData("vote_cast", v.DeliberationID, v.AgentID, v.PositionID, map[string]any{
		"position_id": v.PositionID,
		"value":       v.Value,
	})

	// Seconding: a +1 vote from a different agent publishes a draft motion
	if v.Value == 1 && RuleBool(d, "require_second", false) && pos.Draft && pos.AgentID != v.AgentID {
		if err := s.store.PublishPosition(ctx, v.PositionID); err != nil {
			slog.Error("failed to publish seconded position", "position_id", v.PositionID, "error", err)
		} else {
			s.emit("position_seconded", v.DeliberationID, v.AgentID, v.PositionID)
		}
	}

	// Resolution deferred to Analyze() — recalculated after analysis, not per-vote.
	return nil
}

// checkResolution tallies votes and checks if any position meets the template threshold.
// Returns nil if no position meets the threshold.
func (s *Service) checkResolution(ctx context.Context, d *Deliberation) *Resolution {
	// Get template threshold
	tmpl, _ := GetTemplate(d.Template)
	threshold := tmpl.SuggestedThreshold
	if threshold == 0 {
		threshold = 0.67 // default: supermajority
	}
	// Rules can override threshold
	if t, ok := d.Rules["threshold"]; ok {
		if f, ok := t.(float64); ok && f > 0 {
			threshold = f
		}
	}

	positions, err := s.store.GetPositions(ctx, d.ID, nil)
	if err != nil || len(positions) == 0 {
		return nil
	}

	votes, err := s.store.GetVotes(ctx, d.ID)
	if err != nil {
		return nil
	}

	// Need at least 2 voters (positions are from different agents, votes are cross-agent)
	voterSet := make(map[string]bool)
	for _, v := range votes {
		voterSet[v.AgentID] = true
	}
	if len(voterSet) < 2 {
		return nil
	}

	// Tally votes per position
	type tally struct {
		agree, disagree, pass int
	}
	tallies := make(map[string]*tally)
	for _, p := range positions {
		tallies[p.ID] = &tally{}
	}
	for _, v := range votes {
		t, ok := tallies[v.PositionID]
		if !ok {
			continue
		}
		switch v.Value {
		case 1:
			t.agree++
		case -1:
			t.disagree++
		case 0:
			t.pass++
		}
	}

	// Check if any position meets threshold
	var bestPos *Position
	var bestApproval float64

	for _, p := range positions {
		t := tallies[p.ID]
		if t.agree+t.disagree == 0 {
			continue // no substantive votes
		}
		approval := float64(t.agree) / float64(t.agree+t.disagree)
		if approval >= threshold && approval > bestApproval {
			pCopy := p
			bestPos = &pCopy
			bestApproval = approval
		}
	}

	if bestPos == nil {
		return nil
	}

	// Build vote breakdown for all positions
	var breakdown []VoteTally
	for _, p := range positions {
		t := tallies[p.ID]
		app := 0.0
		if t.agree+t.disagree > 0 {
			app = float64(t.agree) / float64(t.agree+t.disagree)
		}
		content := p.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		breakdown = append(breakdown, VoteTally{
			PositionID: p.ID,
			AgentID:    p.AgentID,
			Content:    content,
			Agree:      t.agree,
			Disagree:   t.disagree,
			Pass:       t.pass,
			Approval:   app,
		})
	}

	strategy := d.Template
	if strategy == "" {
		strategy = "default"
	}

	return &Resolution{
		PositionID:    bestPos.ID,
		PositionText:  bestPos.Content,
		AgentID:       bestPos.AgentID,
		Strategy:      strategy,
		Threshold:     threshold,
		Approval:      bestApproval,
		VoteBreakdown: breakdown,
		ResolvedAt:    time.Now().UTC(),
	}
}

func (s *Service) Analyze(ctx context.Context, deliberationID string) (*AnalysisResult, error) {
	d, err := s.store.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return nil, fmt.Errorf("deliberation not found: %w", err)
	}
	if d.DeadlineAt != nil && time.Now().After(*d.DeadlineAt) {
		return nil, fmt.Errorf("deliberation deadline has passed")
	}

	// Enforce cooling period (minimum time between analyses)
	coolingMinutes := RuleInt(d, "cooling_period_minutes", 0)
	if coolingMinutes > 0 && d.Round > 1 {
		if lastChanged, err := s.store.GetStatusChangedAt(ctx, deliberationID); err == nil && !lastChanged.IsZero() {
			elapsed := time.Since(lastChanged)
			required := time.Duration(coolingMinutes) * time.Minute
			if elapsed < required {
				remaining := required - elapsed
				return nil, fmt.Errorf("cooling period active — %d minutes remaining before next analysis", int(remaining.Minutes())+1)
			}
		}
	}

	// Enforce quorum (minimum participants before analysis)
	if err := s.CheckQuorum(ctx, deliberationID); err != nil {
		return nil, err
	}

	// Atomic status transition: prevents concurrent analysis race condition
	ok, err := s.store.TrySetAnalyzing(ctx, deliberationID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("deliberation is not open (current status: %s)", d.Status)
	}

	// From here, we must reset status on any error
	resetStatus := func() {
		// Note: log to stderr, NOT stdout — stdout is the MCP protocol channel in stdio mode
		if err := s.store.UpdateDeliberationStatus(ctx, deliberationID, "open"); err != nil {
			fmt.Fprintf(os.Stderr, "gemot: warning: failed to reset deliberation status: %v\n", err)
		}
	}

	positions, err := s.store.GetPositions(ctx, deliberationID, nil)
	if err != nil {
		resetStatus()
		return nil, err
	}

	// With fewer than 2 distinct agents, crux detection can't find disagreements.
	// Return a thin result with a warning instead of erroring — single-agent scopes
	// are valid (e.g., early-round diplomacy bilaterals with only one side's messages).
	agentCheck := map[string]bool{}
	for _, p := range positions {
		agentCheck[p.AgentID] = true
	}
	if len(agentCheck) < 2 {
		resetStatus()
		thinResult := &AnalysisResult{
			DeliberationID:      deliberationID,
			Round:               d.Round,
			Clusters:            []OpinionCluster{},
			Cruxes:              []Crux{},
			ConsensusStatements: []ConsensusStatement{},
			TopicSummaries:      []TopicSummary{},
			AgentCount:          len(agentCheck),
			PositionCount:       len(positions),
			Confidence:          "low",
			IntegrityWarnings:   []string{fmt.Sprintf("INSUFFICIENT_AGENTS: %d agent(s) — crux analysis requires at least 2", len(agentCheck))},
			AnalyzedAt:          time.Now().UTC(),
			RecommendedAction:   "await_more_participants",
		}
		if err := s.store.SaveAnalysisResult(ctx, deliberationID, d.Round, thinResult); err != nil {
			return nil, fmt.Errorf("saving thin result: %w", err)
		}
		_ = s.store.AdvanceRound(ctx, deliberationID)
		return thinResult, nil
	}

	votes, err := s.store.GetVotes(ctx, deliberationID)
	if err != nil {
		resetStatus()
		return nil, err
	}

	// Resolve delegated votes (liquid democracy)
	delegations, _ := s.store.GetDelegations(ctx, deliberationID)
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
	// Thread prior round's norms, constitutional rules, and extracted claims into analysis
	if d.Round > 1 {
		prevResult, _ := s.store.GetAnalysisResult(ctx, deliberationID, d.Round-1)
		if prevResult != nil {
			if len(prevResult.EmergentNorms) > 0 {
				analysisCtx = context.WithValue(analysisCtx, ContextKeyPriorNorms{}, prevResult.EmergentNorms)
			}
			if len(prevResult.ConstitutionalRules) > 0 {
				analysisCtx = context.WithValue(analysisCtx, ContextKeyConstitutionalRules{}, prevResult.ConstitutionalRules)
			}
			if len(prevResult.ExtractedClaims) > 0 {
				analysisCtx = context.WithValue(analysisCtx, ContextKeyPriorClaims{}, prevResult.ExtractedClaims)
			}
			// Pass prior taxonomy for topic stability across rounds
			if len(prevResult.TopicSummaries) > 0 {
				var priorTopics []string
				priorTopicIDs := map[string]string{} // name → ID
				for _, ts := range prevResult.TopicSummaries {
					label := ts.Topic
					if ts.TopicID != "" {
						label = ts.TopicID + ": " + ts.Topic
						priorTopicIDs[ts.Topic] = ts.TopicID
					}
					priorTopics = append(priorTopics, "- "+label)
				}
				analysisCtx = context.WithValue(analysisCtx, ContextKeyPriorTaxonomy{}, strings.Join(priorTopics, "\n"))
				if len(priorTopicIDs) > 0 {
					analysisCtx = context.WithValue(analysisCtx, ContextKeyPriorTopicIDs{}, priorTopicIDs)
				}
				analysisCtx = context.WithValue(analysisCtx, ContextKeyPriorSummaries{}, prevResult.TopicSummaries)
			}
		}
	}

	// Register cancellable analysis context
	analysisCtx, cancelAnalysis := context.WithCancel(analysisCtx)
	s.analysisMu.Lock()
	if prev, ok := s.activeAnalyses[deliberationID]; ok {
		prev() // cancel the previous one
	}
	s.activeAnalyses[deliberationID] = cancelAnalysis
	s.analysisMu.Unlock()
	defer func() {
		s.analysisMu.Lock()
		delete(s.activeAnalyses, deliberationID)
		s.analysisMu.Unlock()
	}()

	s.emit("analysis_started", deliberationID, "", "")
	progressFn := ProgressFunc(func(subStatus string) {
		_ = s.store.UpdateSubStatus(ctx, deliberationID, subStatus)
		s.emit("analysis_progress", deliberationID, "", subStatus)
	})
	analysisCtx = context.WithValue(analysisCtx, ContextKeyProgressFunc{}, progressFn)

	// Thread delibID + isPrivate into the reputation layer's WeightsFor
	// call via ctx.Value. When the delib is private, WeightsFor loads
	// per-delib edges (union'd with global) and computes EigenTrust on
	// the fly; otherwise it falls through to the global-score path.
	analysisCtx = types.WithDelibContext(analysisCtx, deliberationID, d.Visibility == "private")

	result, err := s.analyzer.Analyze(analysisCtx, positions, votes, agents)
	if err != nil {
		resetStatus()
		return nil, fmt.Errorf("analysis failed: %w", err)
	}

	// Surface any pending disputes as integrity warnings
	disputes, _ := s.store.GetDisputes(ctx, deliberationID)
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
		if driftWarnings := s.detectRoundDrift(ctx, deliberationID, d.Round, result, votes); len(driftWarnings) > 0 {
			result.IntegrityWarnings = append(result.IntegrityWarnings, driftWarnings...)
		}
	}

	if err := s.store.SaveAnalysisResult(ctx, deliberationID, d.Round, result); err != nil {
		resetStatus()
		return nil, err
	}

	// Reputation: update edges + survived_count from this round's cruxes,
	// then recompute the global EigenTrust eigenvector. Unprocessed
	// disputes feed negative-weight edges into the graph (DARPA Track 1
	// §3 defense against whitewashing). Non-fatal on failure —
	// reputation is a signal, not a correctness property, so a DB
	// hiccup here should not abort the round.
	//
	// Private deliberations carry their edges in a partition scoped to
	// the delib_id so the agreement patterns within never leak into
	// the globally-readable agent_trust_edges. WeightsFor recomputes a
	// per-delib eigenvector over (global ∪ delib-scoped) edges when
	// the analyzer ctx carries IsPrivate=true — see WithDelibContext
	// in the reputation package. Link-visibility delibs stay global
	// (discoverable-by-token, not consent-limited). Private delibs
	// skip survived_count increments so private-ring coordination
	// cannot graduate Sybils out from under the cold-start cap.
	if s.reputation != nil {
		isPrivate := d.Visibility == "private"
		positionAuthors := make(map[string]string, len(positions))
		for _, p := range positions {
			positionAuthors[p.ID] = p.AgentID
		}
		unprocessed, dErr := s.store.GetUnprocessedDisputes(ctx, deliberationID)
		if dErr != nil {
			slog.Warn("fetch unprocessed disputes failed", "deliberation", deliberationID, "err", dErr)
		}
		if err := s.reputation.UpdateFromRound(ctx, deliberationID, isPrivate, result.Cruxes, positionAuthors, unprocessed); err != nil {
			slog.Warn("reputation update failed", "deliberation", deliberationID, "err", err)
		} else if len(unprocessed) > 0 {
			ids := make([]string, len(unprocessed))
			for i, d := range unprocessed {
				ids[i] = d.ID
			}
			if err := s.store.MarkDisputesProcessed(ctx, ids); err != nil {
				slog.Warn("mark disputes processed failed", "deliberation", deliberationID, "err", err)
			}
		}
	}

	if err := s.store.AdvanceRound(ctx, deliberationID); err != nil {
		return nil, err
	}

	if err := s.store.UpdateDeliberationStatus(ctx, deliberationID, "open"); err != nil {
		return nil, err
	}

	s.emit("analysis_complete", deliberationID, "", fmt.Sprintf("round_%d", d.Round))

	// Recalculate resolution after analysis (deferred from Vote for performance).
	if updated, _ := s.store.GetDeliberation(ctx, deliberationID); updated != nil {
		if resolution := s.checkResolution(ctx, updated); resolution != nil {
			_ = s.store.SaveResolution(ctx, deliberationID, resolution)
		} else {
			_ = s.store.SaveResolution(ctx, deliberationID, nil)
		}
	}

	return result, nil
}

func (s *Service) GetContext(ctx context.Context, deliberationID, agentID string) (*AgentContext, error) {
	result, err := s.store.GetLatestAnalysisResult(ctx, deliberationID)
	if err != nil {
		// Check if analysis is in progress
		if d, dErr := s.store.GetDeliberation(ctx, deliberationID); dErr == nil && d.Status == "analyzing" {
			return nil, fmt.Errorf("analysis is in progress (%s) — try again in a moment", d.SubStatus)
		}
		return nil, fmt.Errorf("no analysis results yet — run analyze first")
	}

	// Record that this agent accessed context (for forced acknowledgment)
	if d, err := s.store.GetDeliberation(ctx, deliberationID); err == nil {
		_ = s.store.RecordContextAccess(ctx, deliberationID, agentID, d.Round)
	}

	actx := &AgentContext{
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
				actx.ClusterID = &clusterID
				for _, ally := range c.AgentIDs {
					if ally != agentID {
						actx.NearestAllies = append(actx.NearestAllies, ally)
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
			actx.RelevantCruxes = append(actx.RelevantCruxes, crux)
			if isAgreer {
				for _, d := range crux.DisagreeAgents {
					if !contains(actx.BiggestDisagreements, d) {
						actx.BiggestDisagreements = append(actx.BiggestDisagreements, d)
					}
				}
			} else {
				for _, a := range crux.AgreeAgents {
					if !contains(actx.BiggestDisagreements, a) {
						actx.BiggestDisagreements = append(actx.BiggestDisagreements, a)
					}
				}
			}
		}
	}

	// Surface topic summaries (discussion landscape overview)
	if len(result.TopicSummaries) > 0 {
		actx.TopicSummaries = result.TopicSummaries
	}

	// Compute pairwise alignment scores from crux positions
	actx.AlignmentScores, actx.SwingAgents = computeAlignments(agentID, result.Cruxes)

	// Surface bridging and consensus statements
	if len(result.BridgingStatements) > 0 {
		actx.BridgingStatements = result.BridgingStatements
	}
	if len(result.ConsensusStatements) > 0 {
		actx.ConsensusStatements = result.ConsensusStatements
	}

	// Surface this agent's effective weight
	if w, ok := result.EffectiveWeights[agentID]; ok {
		actx.EffectiveWeight = w
	}

	// Surface cooperation data: compromise, failure scenarios, constitutional rules
	actx.CompromiseProposal = result.CompromiseProposal
	actx.FailureScenarios = result.FailureScenarios
	actx.ConstitutionalRules = result.ConstitutionalRules
	actx.EmergentNorms = result.EmergentNorms
	actx.RuleViolations = result.RuleViolations

	// Anti-sycophancy: generate a diversity nudge based on the agent's unique position
	actx.DiversityNudge = buildDiversityNudge(actx, result)

	// Strategic nudge: actionable guidance based on alignment, bridging, and swing agents
	actx.StrategicNudge = buildStrategicNudge(actx, result)

	// Surface pending invitations for this agent
	if invitations, err := s.store.GetInvitations(ctx, deliberationID); err == nil {
		for _, inv := range invitations {
			if inv.InvitedAgent == agentID && inv.Status == "pending" {
				actx.PendingInvitations = append(actx.PendingInvitations, inv)
			}
		}
	}

	return actx, nil
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

	pos, err := s.store.GetPositionByID(ctx, positionID)
	if err != nil {
		return "", fmt.Errorf("position not found: %w", err)
	}

	// Get other positions for context
	positions, err := s.store.GetPositions(ctx, deliberationID, nil)
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
	result, err := s.store.GetLatestAnalysisResult(ctx, deliberationID)
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

func (s *Service) Delegate(ctx context.Context, deliberationID, fromAgent, toAgent, scope string) (*Delegation, error) {
	delib, err := s.store.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return nil, fmt.Errorf("deliberation not found: %w", err)
	}
	if delib.DeadlineAt != nil && time.Now().After(*delib.DeadlineAt) {
		return nil, fmt.Errorf("deliberation deadline has passed")
	}
	// Delegation cap: no agent can receive more than 3 delegations
	// Prevents power concentration (Uniswap VC-delegate pattern)
	delegations, _ := s.store.GetDelegations(ctx, deliberationID)
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
	if err := s.store.CreateDelegation(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) RevokeDelegation(ctx context.Context, deliberationID, fromAgent string) error {
	return s.store.RevokeDelegation(ctx, deliberationID, fromAgent)
}

func (s *Service) PublishPosition(ctx context.Context, positionID string) error {
	return s.store.PublishPosition(ctx, positionID)
}

func (s *Service) Commit(ctx context.Context, deliberationID, agentID, statement, conditional string) (*Commitment, error) {
	if strings.TrimSpace(statement) == "" {
		return nil, fmt.Errorf("statement is required")
	}
	d, err := s.store.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return nil, fmt.Errorf("deliberation not found: %w", err)
	}
	if d.DeadlineAt != nil && time.Now().After(*d.DeadlineAt) {
		return nil, fmt.Errorf("deliberation deadline has passed")
	}
	c := &Commitment{
		DeliberationID: deliberationID,
		AgentID:        agentID,
		AnalysisRound:  d.Round - 1, // commit to the latest completed analysis
		Statement:      statement,
		Conditional:    conditional,
	}
	if err := s.orderAction(ctx, "commit", deliberationID, agentID, fmt.Sprintf("%d", c.AnalysisRound), statement, conditional); err != nil {
		return nil, err
	}
	if err := s.store.CreateCommitment(ctx, c); err != nil {
		return nil, err
	}

	// Check if conditional commitments should activate
	if conditional == "" {
		c.Status = "active"
		_ = s.store.UpdateCommitmentStatus(ctx, c.ID, "active")
	}
	s.audit("decide:commit", deliberationID, agentID)
	return c, nil
}

func (s *Service) GetCommitments(ctx context.Context, deliberationID string) ([]Commitment, error) {
	return s.store.GetCommitments(ctx, deliberationID)
}

func (s *Service) FulfillCommitment(ctx context.Context, commitmentID, verifiedBy string) error {
	err := s.store.FulfillCommitment(ctx, commitmentID, verifiedBy)
	if err == nil {
		s.audit("decide:fulfill", "", verifiedBy)
	}
	return err
}

func (s *Service) BreakCommitment(ctx context.Context, commitmentID, reason, verifiedBy string) error {
	err := s.store.BreakCommitment(ctx, commitmentID, reason, verifiedBy)
	if err == nil {
		s.audit("decide:break", "", verifiedBy)
	}
	return err
}

func (s *Service) AgentReputation(ctx context.Context, agentID, groupID string) (ReputationSummary, error) {
	var commitments []Commitment
	var err error
	if groupID != "" {
		commitments, err = s.store.GetCommitmentsByGroup(ctx, groupID)
	} else {
		commitments, err = s.store.GetCommitmentsByAgent(ctx, agentID)
	}
	if err != nil {
		return ReputationSummary{}, err
	}
	var summary ReputationSummary
	for _, c := range commitments {
		if c.AgentID != agentID {
			continue
		}
		summary.TotalCommitments++
		switch c.Status {
		case "fulfilled":
			summary.Fulfilled++
		case "broken":
			summary.Broken++
		case "pending", "active":
			summary.Pending++
		}
	}
	if summary.Fulfilled+summary.Broken > 0 {
		summary.TrustScore = float64(summary.Fulfilled) / float64(summary.Fulfilled+summary.Broken)
	}
	return summary, nil
}

// GenerateJoinCode creates a join code for a deliberation.
// Optional maxUses controls how many agents can use the same code (default 1 = single-use).
// Sandbox codes should use maxUses > 1 so every visitor to the /try page can join.
func (s *Service) GenerateJoinCode(ctx context.Context, deliberationID, role string, ttl time.Duration, maxUses ...int) (*JoinCode, error) {
	if _, err := s.store.GetDeliberation(ctx, deliberationID); err != nil {
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
	if err := s.store.CreateJoinCode(ctx, jc); err != nil {
		return nil, err
	}
	return jc, nil
}

// JoinDeliberation claims a join code and adds the agent to the deliberation.
// Returns the deliberation ID so the agent knows where to participate.
func (s *Service) JoinDeliberation(ctx context.Context, code, agentID string) (string, string, error) {
	jc, err := s.store.ClaimJoinCode(ctx, code, agentID)
	if err != nil {
		return "", "", err
	}
	return jc.DeliberationID, jc.Role, nil
}

// LookupJoinCode returns join code metadata without claiming it.
func (s *Service) LookupJoinCode(ctx context.Context, code string) (*JoinCode, *Deliberation, error) {
	jc, err := s.store.LookupJoinCode(ctx, code)
	if err != nil {
		return nil, nil, err
	}
	d, err := s.store.GetDeliberation(ctx, jc.DeliberationID)
	if err != nil {
		return jc, nil, nil
	}
	return jc, d, nil
}

// CheckAccess verifies that the given key_id has access to the deliberation.
// Returns nil if access is allowed, error if denied.
func (s *Service) CheckAccess(ctx context.Context, deliberationID, keyID string) error {
	if keyID == "" {
		return nil // admin or dev mode
	}
	d, err := s.store.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return fmt.Errorf("deliberation not found: %w", err)
	}
	if d.Status == "deleted" {
		return fmt.Errorf("deliberation not found")
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
		allowed, err := s.store.CheckACL(ctx, deliberationID, keyID)
		if err != nil || !allowed {
			return fmt.Errorf("access denied: this is a private deliberation")
		}
		return nil
	}
	return nil
}

func (s *Service) InviteAgent(ctx context.Context, deliberationID, invitedBy, invitedAgent, role, reason string) (*Invitation, error) {
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
	if err := s.store.CreateInvitation(ctx, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

func (s *Service) GetInvitations(ctx context.Context, deliberationID string) ([]Invitation, error) {
	return s.store.GetInvitations(ctx, deliberationID)
}

func (s *Service) AcceptInvitation(ctx context.Context, invitationID string) error {
	return s.store.UpdateInvitationStatus(ctx, invitationID, "accepted")
}

func (s *Service) GetPositionByID(ctx context.Context, id string) (*Position, error) {
	return s.store.GetPositionByID(ctx, id)
}

func (s *Service) GetVotes(ctx context.Context, deliberationID string) ([]Vote, error) {
	return s.store.GetVotes(ctx, deliberationID)
}

func (s *Service) DisputeCrux(ctx context.Context, deliberationID, agentID, cruxClaim, correction string) (*Dispute, error) {
	if delib, err := s.store.GetDeliberation(ctx, deliberationID); err == nil {
		if delib.DeadlineAt != nil && time.Now().After(*delib.DeadlineAt) {
			return nil, fmt.Errorf("deliberation deadline has passed")
		}
	}
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
		Correction:     correction,
	}
	if err := s.orderAction(ctx, "dispute_crux", deliberationID, agentID, cruxClaim, correction); err != nil {
		return nil, err
	}
	if err := s.store.CreateDispute(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// DrainAnalyses waits for all active analyses to complete, up to maxWait.
// Returns the number of analyses that were waited on.
// Called during shutdown to ensure analyses finish before the DB closes.
func (s *Service) DrainAnalyses(maxWait time.Duration) int {
	s.analysisMu.Lock()
	count := len(s.activeAnalyses)
	s.analysisMu.Unlock()

	if count == 0 {
		return 0
	}

	fmt.Fprintf(os.Stderr, "gemot: draining %d active analysis/analyses (max %s)...\n", count, maxWait)
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		s.analysisMu.Lock()
		remaining := len(s.activeAnalyses)
		s.analysisMu.Unlock()
		if remaining == 0 {
			return count
		}
		time.Sleep(time.Second)
	}

	// Timeout: cancel remaining analyses
	s.analysisMu.Lock()
	remaining := len(s.activeAnalyses)
	for id, cancel := range s.activeAnalyses {
		fmt.Fprintf(os.Stderr, "gemot: cancelling analysis %s (drain timeout)\n", id[:8])
		cancel()
		delete(s.activeAnalyses, id)
	}
	s.analysisMu.Unlock()

	if remaining > 0 {
		fmt.Fprintf(os.Stderr, "gemot: cancelled %d analysis/analyses after %s drain timeout\n", remaining, maxWait)
	}
	return count
}

// ResetAnalyzingStatus resets a specific deliberation from "analyzing" back to "open".
// Used by the panic recovery handler in RunAnalysisAsync.
func (s *Service) ResetAnalyzingStatus(ctx context.Context, deliberationID string) {
	if err := s.store.UpdateDeliberationStatus(ctx, deliberationID, "open"); err != nil {
		fmt.Fprintf(os.Stderr, "gemot: warning: failed to reset status after panic: %v\n", err)
	}
}

// CancelAnalysis resets a deliberation from "analyzing" back to "open".
// Returns an error if the deliberation is not currently analyzing.
func (s *Service) CancelAnalysis(ctx context.Context, deliberationID string) error {
	d, err := s.store.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return fmt.Errorf("deliberation not found: %w", err)
	}
	if d.Status != "analyzing" {
		return fmt.Errorf("deliberation is not analyzing (current status: %s)", d.Status)
	}
	// Cancel the active analysis goroutine if running
	s.analysisMu.Lock()
	if cancel, ok := s.activeAnalyses[deliberationID]; ok {
		cancel()
		delete(s.activeAnalyses, deliberationID)
	}
	s.analysisMu.Unlock()
	if err := s.store.UpdateDeliberationStatus(ctx, deliberationID, "open"); err != nil {
		return err
	}
	s.emit("analysis_cancelled", deliberationID, "", "")
	return nil
}

// WithdrawAgent removes an agent from a deliberation by hiding their positions,
// deleting their votes, and revoking their delegations.
func (s *Service) WithdrawAgent(ctx context.Context, deliberationID, agentID string) error {
	d, err := s.store.GetDeliberation(ctx, deliberationID)
	if err != nil {
		return fmt.Errorf("deliberation not found: %w", err)
	}
	if d.Status != "open" {
		return fmt.Errorf("deliberation is %s, cannot withdraw", d.Status)
	}
	// Verify agent has participated
	positions, err := s.store.GetPositions(ctx, deliberationID, nil)
	if err != nil {
		return err
	}
	votes, err := s.store.GetVotes(ctx, deliberationID)
	if err != nil {
		return err
	}
	hasPositions := false
	for _, p := range positions {
		if p.AgentID == agentID {
			hasPositions = true
			break
		}
	}
	hasVotes := false
	for _, v := range votes {
		if v.AgentID == agentID {
			hasVotes = true
			break
		}
	}
	if !hasPositions && !hasVotes {
		return fmt.Errorf("agent %s has not participated in this deliberation", agentID)
	}
	// Mark positions as withdrawn (draft = 1 makes them invisible)
	if err := s.store.WithdrawPositions(ctx, deliberationID, agentID); err != nil {
		return fmt.Errorf("failed to withdraw positions: %w", err)
	}
	// Delete votes
	if err := s.store.DeleteVotesByAgent(ctx, deliberationID, agentID); err != nil {
		return fmt.Errorf("failed to delete votes: %w", err)
	}
	// Revoke delegations from/to this agent
	if err := s.store.DeleteDelegationsByAgent(ctx, deliberationID, agentID); err != nil {
		return fmt.Errorf("failed to revoke delegations: %w", err)
	}
	// Invalidate pending commitments by the withdrawn agent
	commitments, _ := s.store.GetCommitments(ctx, deliberationID)
	for _, c := range commitments {
		if c.AgentID == agentID && c.FulfilledAt == nil && c.BrokenAt == nil {
			_ = s.store.BreakCommitment(ctx, c.ID, "agent withdrew from deliberation", "system")
		}
	}
	s.emit("agent_withdrawn", deliberationID, agentID, "")
	return nil
}

// RecoverStuck resets deliberations stuck in "analyzing" status back to "open"
// if they have been in that state for more than 30 minutes.
// Also cancels any active analysis goroutines for recovered deliberations.
func (s *Service) RecoverStuck(ctx context.Context) (int, error) {
	// Get the list of stuck deliberations before recovery (so we can cancel their goroutines)
	stuck, err := s.store.GetStuckAnalyzing(ctx, 30*time.Minute)
	if err != nil {
		return 0, err
	}

	if len(stuck) == 0 {
		return 0, nil
	}

	// Cancel active analysis goroutines for stuck deliberations
	s.analysisMu.Lock()
	for _, id := range stuck {
		if cancel, ok := s.activeAnalyses[id]; ok {
			cancel()
			delete(s.activeAnalyses, id)
		}
	}
	s.analysisMu.Unlock()

	// Reset DB status
	return s.store.RecoverStuckAnalyzing(ctx, 30*time.Minute)
}

func (s *Service) GetAnalysisResult(ctx context.Context, deliberationID string, round int) (*AnalysisResult, error) {
	return s.store.GetAnalysisResult(ctx, deliberationID, round)
}

func (s *Service) GetAllAnalysisResults(ctx context.Context, deliberationID string) ([]AnalysisResult, error) {
	return s.store.GetAllAnalysisResults(ctx, deliberationID)
}

func (s *Service) SaveAnalysisResult(ctx context.Context, deliberationID string, round int, result *AnalysisResult) error {
	return s.store.SaveAnalysisResult(ctx, deliberationID, round, result)
}

// CreateShareToken generates a random share token for a group and stores it.
// The token is 16 random bytes, hex-encoded (32 characters).
func (s *Service) CreateShareToken(ctx context.Context, groupID string) (string, error) {
	if groupID == "" {
		return "", fmt.Errorf("group_id is required")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	token := fmt.Sprintf("%x", b)
	expiresAt := time.Now().Add(30 * 24 * time.Hour) // 30 days
	if err := s.store.CreateShareToken(ctx, token, groupID, expiresAt); err != nil {
		return "", err
	}
	return token, nil
}

// LookupShareToken returns the group ID for a valid share token.
func (s *Service) LookupShareToken(ctx context.Context, token string) (string, error) {
	return s.store.LookupShareToken(ctx, token)
}

// detectRoundDrift compares the current analysis with the previous round's analysis.
// Flags suspiciously rapid convergence that may indicate sycophantic agreement or
// coordinated manipulation rather than genuine deliberation.
func (s *Service) detectRoundDrift(ctx context.Context, deliberationID string, currentRound int, current *AnalysisResult, currentVotes []Vote) []string {
	prev, err := s.store.GetAnalysisResult(ctx, deliberationID, currentRound-1)
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
	prevVotes, err := s.store.GetVotesByRound(ctx, deliberationID, currentRound-1)
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

// RegisterAgentKey records an ed25519 public key for an agent. Signed positions
// and votes from this agent will be verified against this key until it is revoked
// or replaced by a subsequent registration.
func (s *Service) RegisterAgentKey(ctx context.Context, agentID string, publicKey []byte, algo string) error {
	if len(agentID) == 0 || len(agentID) > maxAgentIDLen {
		return fmt.Errorf("agent_id must be between 1 and %d characters", maxAgentIDLen)
	}
	if err := auth.ValidatePublicKey(algo, publicKey); err != nil {
		return err
	}
	if err := s.store.RegisterAgentKey(ctx, agentID, publicKey, algo); err != nil {
		return err
	}
	s.audit("participate:register_key", "", agentID)
	return nil
}

// GetActiveAgentKey returns the most recently registered non-revoked public key
// for the agent, if any. Exposed at the service layer so HTTP middleware can
// verify envelope signatures without depending on the store package directly.
//
// Callers should use errors.Is(err, ErrAgentKeyNotFound) to distinguish "no key
// registered" from a real DB error.
func (s *Service) GetActiveAgentKey(ctx context.Context, agentID string) ([]byte, string, error) {
	return s.store.GetActiveAgentKey(ctx, agentID)
}

// RevokeAgentKey invalidates the active signing key for an agent.
// Subsequent signed submissions will fail verification until a new key is registered.
func (s *Service) RevokeAgentKey(ctx context.Context, agentID string) error {
	if err := s.store.RevokeAgentKey(ctx, agentID); err != nil {
		return err
	}
	s.audit("participate:revoke_key", "", agentID)
	return nil
}

// verifyPositionSignature enforces the deliberation's signature_policy against
// a position submission. signingContent is the raw client-supplied content
// (before any server-side PII sanitization) — the signature must verify against
// the exact bytes the client signed, not the stored form.
//
// signingAgentID is the identity used to reconstruct the canonical payload.
// It matches p.AgentID for direct callers but may differ in hosted mode where
// the transport scopes the stored agent_id.
//
// Returns an error only when the policy says to reject; advisory-mode warnings
// are emitted via the audit log and the position is accepted.
func (s *Service) verifyPositionSignature(ctx context.Context, d *Deliberation, p *Position, signingContent, signingAgentID string) error {
	policy := d.SignaturePolicy
	if policy == "" {
		policy = "none"
	}
	pubkey, algo, keyErr := s.store.GetActiveAgentKey(ctx, p.AgentID)
	hasKey := keyErr == nil
	if keyErr != nil && !errors.Is(keyErr, ErrAgentKeyNotFound) {
		// Real DB error — fail closed.
		return fmt.Errorf("signature verification: %w", keyErr)
	}

	switch {
	case len(p.Signature) > 0:
		if !hasKey {
			return fmt.Errorf("signature provided but no public key is registered for agent %q", p.AgentID)
		}
		msg := auth.PositionPayload(signingAgentID, p.DeliberationID, p.Round, signingContent)
		if err := auth.Verify(algo, pubkey, msg, p.Signature); err != nil {
			s.audit("participate:signature_verify_fail:position", d.ID, p.AgentID)
			return fmt.Errorf("SIGNATURE_VERIFY_FAIL: %w", err)
		}
	case hasKey:
		// Unsigned submission from an agent with a registered key — honor policy.
		switch policy {
		case "required":
			return fmt.Errorf("signature required: agent %q has a registered key but submission is unsigned", p.AgentID)
		case "advisory":
			s.audit("participate:unsigned_position", d.ID, p.AgentID)
			fmt.Fprintf(os.Stderr, "gemot: UNSIGNED_POSITION from agent %q (key registered, policy=advisory)\n", p.AgentID)
		}
	}
	return nil
}

// verifyVoteSignature enforces signature_policy against a vote. Parallel to
// verifyPositionSignature — the only differences are the canonical payload and
// the audit/log labels.
//
// signingAgentID has the same role as in verifyPositionSignature: the identity
// used to reconstruct the canonical payload the client signed.
func (s *Service) verifyVoteSignature(ctx context.Context, d *Deliberation, v *Vote, signingAgentID string) error {
	policy := d.SignaturePolicy
	if policy == "" {
		policy = "none"
	}
	pubkey, algo, keyErr := s.store.GetActiveAgentKey(ctx, v.AgentID)
	hasKey := keyErr == nil
	if keyErr != nil && !errors.Is(keyErr, ErrAgentKeyNotFound) {
		// Real DB error — fail closed.
		return fmt.Errorf("signature verification: %w", keyErr)
	}

	switch {
	case len(v.Signature) > 0:
		if !hasKey {
			return fmt.Errorf("signature provided but no public key is registered for agent %q", v.AgentID)
		}
		msg := auth.VotePayload(signingAgentID, v.DeliberationID, v.PositionID, v.Value, v.Qualifier, v.Caveat, v.CriterionID)
		if err := auth.Verify(algo, pubkey, msg, v.Signature); err != nil {
			s.audit("participate:signature_verify_fail:vote", d.ID, v.AgentID)
			return fmt.Errorf("SIGNATURE_VERIFY_FAIL: %w", err)
		}
	case hasKey:
		switch policy {
		case "required":
			return fmt.Errorf("signature required: agent %q has a registered key but vote is unsigned", v.AgentID)
		case "advisory":
			s.audit("participate:unsigned_vote", d.ID, v.AgentID)
			fmt.Fprintf(os.Stderr, "gemot: UNSIGNED_VOTE from agent %q (key registered, policy=advisory)\n", v.AgentID)
		}
	}
	return nil
}
