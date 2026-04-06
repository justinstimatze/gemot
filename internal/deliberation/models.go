package deliberation

import "time"

type Deliberation struct {
	ID              string         `json:"deliberation_id"`
	Topic           string         `json:"topic"`
	Description     string         `json:"description"`
	Round           int            `json:"round_number"`
	Status          string         `json:"status"`                     // open | analyzing | resolved | closed
	SubStatus       string         `json:"sub_status,omitempty"`       // taxonomy | extracting | deduplicating | crux_detection | summarizing | complete
	Type            string         `json:"type,omitempty"`             // reasoning | knowledge | negotiation | policy (affects consensus threshold)
	Criteria        []Criterion    `json:"criteria,omitempty"`         // evaluation dimensions for multi-criteria voting
	Visibility      string         `json:"visibility,omitempty"`       // open (default) | private | link
	CreatorKey      string         `json:"creator_key,omitempty"`      // key_id of the creator (for access control)
	MaxParticipants int            `json:"max_participants,omitempty"` // 0 = unlimited
	Template        string         `json:"template,omitempty"`         // governance template (assembly, jury, etc.)
	Rules           map[string]any `json:"rules,omitempty"`            // governance rules (quorum, timelock, etc.)
	GroupID         string         `json:"group_id,omitempty"`         // links related deliberations (experiment, workflow, session)
	Resolution      *Resolution    `json:"resolution,omitempty"`       // set when deliberation reaches threshold
	DeadlineAt      *time.Time     `json:"deadline_at,omitempty"`      // optional time limit for the deliberation
	CreatedAt       time.Time      `json:"created_at"`
}

// Resolution captures the outcome when a deliberation's votes meet its template threshold.
type Resolution struct {
	PositionID    string      `json:"position_id"`    // winning position
	PositionText  string      `json:"position_text"`  // content of winning position
	AgentID       string      `json:"agent_id"`       // who submitted the winning position
	Strategy      string      `json:"strategy"`       // template name that defined the threshold
	Threshold     float64     `json:"threshold"`      // threshold that was met
	Approval      float64     `json:"approval"`       // actual approval ratio
	VoteBreakdown []VoteTally `json:"vote_breakdown"` // per-position tallies
	ResolvedAt    time.Time   `json:"resolved_at"`
}

// VoteTally summarizes votes for a single position.
type VoteTally struct {
	PositionID string  `json:"position_id"`
	AgentID    string  `json:"agent_id"` // who submitted this position
	Content    string  `json:"content"`  // position text (truncated)
	Agree      int     `json:"agree"`
	Disagree   int     `json:"disagree"`
	Pass       int     `json:"pass"`
	Approval   float64 `json:"approval"` // agree / (agree + disagree), ignoring pass
}

type Position struct {
	ID               string    `json:"position_id"`
	DeliberationID   string    `json:"deliberation_id"`
	AgentID          string    `json:"agent_id"`
	Content          string    `json:"content"`
	ModelFamily      string    `json:"model_family,omitempty"`       // optional: "claude", "gpt", "gemini", etc.
	Group            string    `json:"group,omitempty"`              // optional: sub-group for decentralized deliberation
	Conviction       float64   `json:"conviction,omitempty"`         // 0.0-1.0, strength of belief (default 0.5)
	Reservation      string    `json:"reservation,omitempty"`        // what outcome is unacceptable to this agent
	OnBehalfOf       string    `json:"on_behalf_of,omitempty"`       // principal this agent represents
	Interests        string    `json:"interests,omitempty"`          // what this agent optimizes for (transparent objectives)
	Draft            bool      `json:"draft,omitempty"`              // if true, not yet visible to others
	ParentPositionID string         `json:"parent_position_id,omitempty"` // amendment to this position
	Metadata         map[string]any `json:"metadata,omitempty"`           // extensible metadata (lat, lon, label, etc.)
	Round            int            `json:"round_number"`
	CreatedAt        time.Time      `json:"created_at"`
}

// JoinCode is a short-lived code for joining a deliberation without an API key.
// Sandbox codes are multi-use (up to MaxUses); private codes default to single-use.
type JoinCode struct {
	Code           string    `json:"code"`
	DeliberationID string    `json:"deliberation_id"`
	Role           string    `json:"role,omitempty"` // suggested role: "contributor", "reviewer", etc.
	ExpiresAt      time.Time `json:"expires_at"`
	Used           bool      `json:"used"`              // true when use_count >= max_uses
	UsedBy         string    `json:"used_by,omitempty"` // last agent_id that claimed it
	UseCount       int       `json:"use_count"`
	MaxUses        int       `json:"max_uses"` // how many agents can use this code (1 = single-use)
	CreatedAt      time.Time `json:"created_at"`
}

type Delegation struct {
	ID             string    `json:"delegation_id"`
	DeliberationID string    `json:"deliberation_id"`
	FromAgent      string    `json:"from_agent"`
	ToAgent        string    `json:"to_agent"`
	Scope          string    `json:"scope,omitempty"` // topic scope, empty = all topics
	Active         bool      `json:"active"`
	CreatedAt      time.Time `json:"created_at"`
}

type Commitment struct {
	ID             string     `json:"commitment_id"`
	DeliberationID string     `json:"deliberation_id"`
	AgentID        string     `json:"agent_id"`
	AnalysisRound  int        `json:"analysis_round"`        // which round's results they're committing to
	Statement      string     `json:"statement"`             // what specifically they commit to
	Conditional    string     `json:"conditional,omitempty"` // "if agents X,Y also commit"
	Status         string     `json:"status"`                // pending | active | fulfilled | broken
	CreatedAt      time.Time  `json:"created_at"`
	FulfilledAt    *time.Time `json:"fulfilled_at,omitempty"`
	BrokenAt       *time.Time `json:"broken_at,omitempty"`
	BrokenReason   string     `json:"broken_reason,omitempty"`
	VerifiedBy     string     `json:"verified_by,omitempty"`
}

// ReputationSummary aggregates an agent's commitment track record.
type ReputationSummary struct {
	TotalCommitments int     `json:"total_commitments"`
	Fulfilled        int     `json:"fulfilled"`
	Broken           int     `json:"broken"`
	Pending          int     `json:"pending"`
	TrustScore       float64 `json:"trust_score"` // fulfilled / (fulfilled + broken), 0 if none
}

type Invitation struct {
	ID             string    `json:"invitation_id"`
	DeliberationID string    `json:"deliberation_id"`
	InvitedBy      string    `json:"invited_by"`
	InvitedAgent   string    `json:"invited_agent"`
	Role           string    `json:"role,omitempty"` // "moderator", "expert", "mediator", "observer"
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

// Criterion defines an evaluation dimension for multi-criteria voting.
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
	Value          int       `json:"value"`                  // -1, 0, 1
	CriterionID    string    `json:"criterion_id,omitempty"` // optional: which criterion this vote is for
	CreatedAt      time.Time `json:"created_at"`
}
