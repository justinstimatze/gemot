package deliberation

// SourceQuote traces a crux back to a specific quote from an agent's position.
type SourceQuote struct {
	PositionID string `json:"position_id"`
	AgentID    string `json:"agent_id"`
	Quote      string `json:"quote"`
	ClaimText  string `json:"claim_text"` // the extracted claim this quote supports
}

type Crux struct {
	Claim             string        `json:"crux_claim"`
	Topic             string        `json:"topic"`
	Subtopic          string        `json:"subtopic"`
	AgreeAgents       []string      `json:"agree_agents"`
	DisagreeAgents    []string      `json:"disagree_agents"`
	NoClearPosition   []string      `json:"no_clear_position"`
	ControversyScore  float64       `json:"controversy_score"`              // 0-1
	Explanation       string        `json:"explanation"`
	SourcePositionIDs []string      `json:"source_position_ids,omitempty"` // positions that contributed claims to this crux
	SourceQuotes      []SourceQuote `json:"source_quotes,omitempty"`       // verbatim quotes grounding this crux
	CruxType          string        `json:"crux_type,omitempty"`           // "factual", "value", "mixed"
	Resolvability     float64       `json:"resolvability,omitempty"`       // 0-1, how likely evidence could resolve it
}

type OpinionCluster struct {
	ID                      int      `json:"cluster_id"`
	AgentIDs                []string `json:"agent_ids"`
	RepresentativePositions []string `json:"representative_positions"`
	Size                    int      `json:"size"`
}

type ConsensusStatement struct {
	PositionID           string  `json:"position_id"`
	Content              string  `json:"content"`
	OverallAgreeRatio    float64 `json:"overall_agree_ratio"`
	MinClusterAgreeRatio float64 `json:"min_cluster_agree_ratio"`
}

// BridgingStatement is a position that gets agreement across opposing clusters.
// Bridging score = minimum per-cluster agree ratio (higher = more cross-cutting).
type BridgingStatement struct {
	PositionID       string             `json:"position_id"`
	AgentID          string             `json:"agent_id"`
	Content          string             `json:"content"`
	BridgingScore    float64            `json:"bridging_score"`    // min per-cluster agree ratio
	OverallAgreeRate float64            `json:"overall_agree_rate"`
	ClusterAgreeRate map[string]float64 `json:"cluster_agree_rate"` // cluster_id -> agree ratio
}

// Coalition represents a subset of agents that agree on most cruxes.
type Coalition struct {
	AgentIDs       []string `json:"agent_ids"`
	SharedCruxes   int      `json:"shared_cruxes"`   // cruxes where all coalition members agree
	StabilityScore float64  `json:"stability_score"` // 0-1, how consistent their agreement is
}

type TopicSummary struct {
	Topic   string `json:"topic"`
	Summary string `json:"summary"`
}

// AgentAlignment shows how closely aligned two agents are based on crux positions.
type AgentAlignment struct {
	AgentID        string  `json:"agent_id"`
	AlignmentScore float64 `json:"alignment_score"` // 0-1, fraction of cruxes where both agree or both disagree
	SharedCruxes   int     `json:"shared_cruxes"`   // cruxes where both took a position
	AgreeCruxes    int     `json:"agree_cruxes"`    // cruxes where both are on the same side
}

type AgentContext struct {
	AgentID              string   `json:"agent_id"`
	ClusterID            *int     `json:"cluster_id"`
	NearestAllies        []string `json:"nearest_allies"`
	BiggestDisagreements []string `json:"biggest_disagreements_with"`
	RelevantCruxes       []Crux   `json:"relevant_cruxes"`
	// Enriched context from analysis
	TopicSummaries       []TopicSummary       `json:"topic_summaries,omitempty"`        // what's being discussed (landscape overview)
	AlignmentScores      []AgentAlignment     `json:"alignment_scores,omitempty"`       // pairwise alignment with all other agents
	SwingAgents          []string             `json:"swing_agents,omitempty"`           // agents with no_clear_position on many cruxes (persuadable)
	BridgingStatements   []BridgingStatement  `json:"bridging_statements,omitempty"`    // positions with cross-cluster agreement
	ConsensusStatements  []ConsensusStatement `json:"consensus_statements,omitempty"`   // positions with 67%+ weighted agreement
	EffectiveWeight      float64              `json:"effective_weight,omitempty"`        // this agent's weight in consensus (trust × correlation × conviction)
	StrategicNudge       string               `json:"strategic_nudge,omitempty"`         // actionable guidance based on position
	DiversityNudge       string               `json:"diversity_nudge,omitempty"`         // anti-sycophancy: encourages maintaining genuine disagreement
	PendingInvitations   []Invitation         `json:"pending_invitations,omitempty"`
	IntegrityWarnings    []string             `json:"integrity_warnings,omitempty"`
}

// AuditEntry records a pipeline decision for transparency and debugging.
type AuditEntry struct {
	Stage   string `json:"stage"`
	Detail  string `json:"detail"`
	Count   int    `json:"count,omitempty"`
}

type AnalysisResult struct {
	DeliberationID      string               `json:"deliberation_id"`
	Round               int                  `json:"round_number"`
	Clusters            []OpinionCluster     `json:"clusters"`
	Cruxes              []Crux               `json:"cruxes"`
	ConsensusStatements []ConsensusStatement `json:"consensus_statements"`
	BridgingStatements  []BridgingStatement  `json:"bridging_statements,omitempty"`
	TopicSummaries      []TopicSummary       `json:"topic_summaries"`
	AgentCount          int                  `json:"agent_count"`
	PositionCount       int                  `json:"position_count"`
	VoteCount           int                  `json:"vote_count"`
	Confidence          string               `json:"confidence"`
	Coalitions          []Coalition          `json:"coalitions,omitempty"`
	CompromiseProposal  string               `json:"compromise_proposal,omitempty"`
	ConstitutionalRules []string             `json:"constitutional_rules,omitempty"` // consensus principles expressible as constraints
	FailureScenarios    []string             `json:"failure_scenarios,omitempty"`    // BATNA: what happens if deliberation fails to resolve
	ZOPA                any                  `json:"zopa,omitempty"`                // Zone of Possible Agreement analysis
	CriteriaResults     map[string]any       `json:"criteria_results,omitempty"`    // per-criterion consensus/bridging
	EmergentNorms       []string             `json:"emergent_norms,omitempty"`      // behavioral patterns achieving consensus, promotable to rules
	RuleViolations      []string             `json:"rule_violations,omitempty"`     // positions that may violate constitutional rules from prior rounds
	TrustWeights        map[string]float64   `json:"trust_weights,omitempty"`
	CorrelationWeights  map[string]float64   `json:"correlation_weights,omitempty"` // Plurality: degressive proportionality for correlated agents
	EffectiveWeights    map[string]float64   `json:"effective_weights,omitempty"`   // trust × correlation × sqrt(conviction) — actually used in consensus/bridging
	IntegrityWarnings   []string             `json:"integrity_warnings,omitempty"`
	AuditLog            []AuditEntry         `json:"audit_log,omitempty"`
	// Epistemic health metrics
	ParticipationRate    float64  `json:"participation_rate,omitempty"`    // votes cast / (agents × positions)
	PerspectiveDiversity float64  `json:"perspective_diversity,omitempty"` // clusters / agents (0-1, higher = more diverse)
	// Pareto analysis
	ParetoEfficient  []string `json:"pareto_efficient,omitempty"`  // proposals on the Pareto frontier
	DominatedProposals []string `json:"dominated_proposals,omitempty"` // proposals beaten on all criteria
}
