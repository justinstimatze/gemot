// Package types contains the public data types for gemot's deliberation API.
// These types are the canonical definitions — internal packages use type aliases
// to reference them, ensuring a single source of truth for JSON serialization.
package types

import "time"

type Deliberation struct {
	ID              string         `json:"deliberation_id"`
	Topic           string         `json:"topic"`
	Description     string         `json:"description"`
	Round           int            `json:"round_number"`
	Status          string         `json:"status"`                     // open | analyzing | resolved | closed
	SubStatus       string         `json:"sub_status,omitempty"`       // taxonomy | extracting | deduplicating | crux_detection | summarizing | complete
	Type            string         `json:"type,omitempty"`             // reasoning | knowledge | negotiation | policy
	Criteria        []Criterion    `json:"criteria,omitempty"`         // evaluation dimensions for multi-criteria voting
	Visibility      string         `json:"visibility,omitempty"`       // open (default) | private | link
	CreatorKey      string         `json:"creator_key,omitempty"`      // key_id of the creator
	MaxParticipants int            `json:"max_participants,omitempty"` // 0 = unlimited
	Template        string         `json:"template,omitempty"`         // governance template (assembly, jury, etc.)
	Rules           map[string]any `json:"rules,omitempty"`            // governance rules (quorum, timelock, etc.)
	GroupID         string         `json:"group_id,omitempty"`         // links related deliberations
	Resolution      *Resolution    `json:"resolution,omitempty"`       // set when deliberation reaches threshold
	SignaturePolicy string         `json:"signature_policy,omitempty"` // none | advisory | required
	PrincipalPolicy string         `json:"principal_policy,omitempty"` // none | advisory | required
	DeadlineAt      *time.Time     `json:"deadline_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

type Resolution struct {
	PositionID    string      `json:"position_id"`
	PositionText  string      `json:"position_text"`
	AgentID       string      `json:"agent_id"`
	Strategy      string      `json:"strategy"`
	Threshold     float64     `json:"threshold"`
	Approval      float64     `json:"approval"`
	VoteBreakdown []VoteTally `json:"vote_breakdown"`
	ResolvedAt    time.Time   `json:"resolved_at"`
}

type VoteTally struct {
	PositionID string  `json:"position_id"`
	AgentID    string  `json:"agent_id"`
	Content    string  `json:"content"`
	Agree      int     `json:"agree"`
	Disagree   int     `json:"disagree"`
	Pass       int     `json:"pass"`
	Approval   float64 `json:"approval"`
}

type Position struct {
	ID             string  `json:"position_id"`
	DeliberationID string  `json:"deliberation_id"`
	AgentID        string  `json:"agent_id"`
	Content        string  `json:"content"`
	ModelFamily    string  `json:"model_family,omitempty"`
	Group          string  `json:"group,omitempty"`
	Conviction     float64 `json:"conviction,omitempty"`
	Reservation    string  `json:"reservation,omitempty"`
	OnBehalfOf     string  `json:"on_behalf_of,omitempty"`
	Interests      string  `json:"interests,omitempty"`
	// PrincipalCredential is the JSON-encoded delegation attestation backing
	// OnBehalfOf (see internal/principal). Stored so the claim can be
	// re-verified offline rather than taken on the server's word.
	PrincipalCredential []byte `json:"principal_credential,omitempty"`
	// PrincipalVerified records that the server verified the delegation at
	// submit time. Kept alongside the credential because key rotation and
	// revocation can make a credential that was genuinely valid at submit
	// time fail to re-verify later.
	PrincipalVerified bool           `json:"principal_verified,omitempty"`
	Draft             bool           `json:"draft,omitempty"`
	ParentPositionID  string         `json:"parent_position_id,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	Round             int            `json:"round_number"`
	Signature         []byte         `json:"signature,omitempty"` // ed25519 signature over auth.PositionPayload
	CreatedAt         time.Time      `json:"created_at"`
}

type JoinCode struct {
	Code           string    `json:"code"`
	DeliberationID string    `json:"deliberation_id"`
	Role           string    `json:"role,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
	Used           bool      `json:"used"`
	UsedBy         string    `json:"used_by,omitempty"`
	UseCount       int       `json:"use_count"`
	MaxUses        int       `json:"max_uses"`
	CreatedAt      time.Time `json:"created_at"`
}

type Delegation struct {
	ID             string    `json:"delegation_id"`
	DeliberationID string    `json:"deliberation_id"`
	FromAgent      string    `json:"from_agent"`
	ToAgent        string    `json:"to_agent"`
	Scope          string    `json:"scope,omitempty"`
	Active         bool      `json:"active"`
	CreatedAt      time.Time `json:"created_at"`
}

type Commitment struct {
	ID             string     `json:"commitment_id"`
	DeliberationID string     `json:"deliberation_id"`
	AgentID        string     `json:"agent_id"`
	AnalysisRound  int        `json:"analysis_round"`
	Statement      string     `json:"statement"`
	Conditional    string     `json:"conditional,omitempty"`
	Status         string     `json:"status"` // pending | active | fulfilled | broken
	CreatedAt      time.Time  `json:"created_at"`
	FulfilledAt    *time.Time `json:"fulfilled_at,omitempty"`
	BrokenAt       *time.Time `json:"broken_at,omitempty"`
	BrokenReason   string     `json:"broken_reason,omitempty"`
	VerifiedBy     string     `json:"verified_by,omitempty"`
}

type ReputationSummary struct {
	TotalCommitments int     `json:"total_commitments"`
	Fulfilled        int     `json:"fulfilled"`
	Broken           int     `json:"broken"`
	Pending          int     `json:"pending"`
	TrustScore       float64 `json:"trust_score"`
}

type Invitation struct {
	ID             string    `json:"invitation_id"`
	DeliberationID string    `json:"deliberation_id"`
	InvitedBy      string    `json:"invited_by"`
	InvitedAgent   string    `json:"invited_agent"`
	Role           string    `json:"role,omitempty"`
	Reason         string    `json:"reason"`
	Status         string    `json:"status"` // pending | accepted | declined
	CreatedAt      time.Time `json:"created_at"`
}

type Dispute struct {
	ID             string    `json:"dispute_id"`
	DeliberationID string    `json:"deliberation_id"`
	AgentID        string    `json:"agent_id"`
	CruxClaim      string    `json:"crux_claim"`
	Correction     string    `json:"correction"`
	CreatedAt      time.Time `json:"created_at"`
}

type Criterion struct {
	ID          string `json:"criterion_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type Vote struct {
	ID             string    `json:"vote_id"`
	DeliberationID string    `json:"deliberation_id"`
	AgentID        string    `json:"agent_id"`
	PositionID     string    `json:"position_id"`
	Value          int       `json:"value"` // -2 (strongly disagree) to +2 (strongly agree)
	CriterionID    string    `json:"criterion_id,omitempty"`
	Qualifier      string    `json:"qualifier,omitempty"` // brief reason for this stance
	Caveat         string    `json:"caveat,omitempty"`    // specific condition or objection
	Signature      []byte    `json:"signature,omitempty"` // ed25519 signature over auth.VotePayload
	CreatedAt      time.Time `json:"created_at"`
}

// CommitmentAccess is a server-stamped record that an agent OTHER than the
// committer touched a commitment's artifact. It is the externally-visible
// signal the payment-mesh disclosure-window and stakes-gate both key to:
// gemot stamps CreatedAt and takes AccessorID from the authenticated caller,
// so neither party can backdate or suppress it. The same ledger yields both
// the third-party-readable clock (first/last downstream access) and the
// party-independent stakes marker (distinct accessors, dependent commitments).
type CommitmentAccess struct {
	ID           string    `json:"access_id"`
	CommitmentID string    `json:"commitment_id"`
	AccessorID   string    `json:"accessor_id"`
	Kind         string    `json:"kind"` // read | question | dependency
	Note         string    `json:"note,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// CommitmentSignals is the externally-visible clock + stakes marker derived
// from a commitment's access ledger. Every field is computed from
// server-stamped facts, so a third party can read them and neither the buyer
// nor the seller can rewrite them.
//
// Design note (from design review): the clock supplies IMMUNITY and EVIDENCE, not an
// automatic verdict. The real trigger for a late disclosure is seller
// knowledge, which gemot cannot observe — so gemot deliberately does NOT emit
// a "seller is late" deadline (that would be a cliff: the moment consequence
// becomes real is the same moment protection would start). Adjudication weighs
// seller knowledge against the evidence here. Stakes key to DEPENDENTS ONLY: a
// raw reader count is gameable upward (N sock-puppet readers must not force a
// seller into a costly mandatory tier) and reading is not relying.
type CommitmentSignals struct {
	CommitmentID string `json:"commitment_id"`

	// FirstDownstreamAccessAt is the IMMUNITY CUTOFF: a correction the seller
	// self-reports (timestamped) strictly before this — i.e. before anyone was
	// exposed — earns immunity. nil = nobody downstream has accessed yet, so a
	// self-report now is fully immune. It is NOT a deadline.
	FirstDownstreamAccessAt *time.Time `json:"first_downstream_access_at,omitempty"`
	// FirstQuestionAt is the on-notice evidentiary floor: the earliest external
	// question put to the artifact, the latest point at which a seller can be
	// argued to have been on notice. Evidence for adjudication, not a verdict.
	FirstQuestionAt *time.Time `json:"first_question_at,omitempty"`
	// LastDownstreamAccessAt is informational only — deliberately NOT a
	// deadline: any accessor would reset it, so a popular artifact's window
	// would never close (unbounded, buyer-gameable seller liability).
	LastDownstreamAccessAt *time.Time `json:"last_downstream_access_at,omitempty"`

	// DistinctAccessors and AccessCount are reported but do NOT set stakes.
	DistinctAccessors int `json:"distinct_accessors"`
	AccessCount       int `json:"access_count"`
	// DependentCount is the stakes signal: a dependent commitment is a costly,
	// logged act by someone other than the seller — expensive to fake and hard
	// to suppress.
	DependentCount int    `json:"dependent_count"`
	StakesLevel    string `json:"stakes_level"` // normal | high (high iff DependentCount >= 1)
}
