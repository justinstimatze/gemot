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
	ID               string         `json:"position_id"`
	DeliberationID   string         `json:"deliberation_id"`
	AgentID          string         `json:"agent_id"`
	Content          string         `json:"content"`
	ModelFamily      string         `json:"model_family,omitempty"`
	Group            string         `json:"group,omitempty"`
	Conviction       float64        `json:"conviction,omitempty"`
	Reservation      string         `json:"reservation,omitempty"`
	OnBehalfOf       string         `json:"on_behalf_of,omitempty"`
	Interests        string         `json:"interests,omitempty"`
	Draft            bool           `json:"draft,omitempty"`
	ParentPositionID string         `json:"parent_position_id,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	Round            int            `json:"round_number"`
	CreatedAt        time.Time      `json:"created_at"`
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
	Value          int       `json:"value"`
	CriterionID    string    `json:"criterion_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}
