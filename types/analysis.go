package types

import "time"

type SourceQuote struct {
	PositionID string `json:"position_id"`
	AgentID    string `json:"agent_id"`
	Quote      string `json:"quote"`
	ClaimText  string `json:"claim_text"`
}

type AgentStance struct {
	AgentID   string `json:"agent_id"`
	Value     int    `json:"value"`     // -2 to +2
	Qualifier string `json:"qualifier"` // one-line reason for this specific stance
}

type Crux struct {
	Claim             string        `json:"crux_claim"`
	Topic             string        `json:"topic"`
	Subtopic          string        `json:"subtopic"`
	AgreeAgents       []string      `json:"agree_agents"`
	DisagreeAgents    []string      `json:"disagree_agents"`
	NoClearPosition   []string      `json:"no_clear_position"`
	ControversyScore  float64       `json:"controversy_score"`
	Explanation       string        `json:"explanation"`
	SourcePositionIDs []string      `json:"source_position_ids,omitempty"`
	SourceQuotes      []SourceQuote `json:"source_quotes,omitempty"`
	CruxType          string        `json:"crux_type,omitempty"`
	Resolvability     float64       `json:"resolvability,omitempty"`
	Degenerate        bool          `json:"degenerate,omitempty"`
	Stances           []AgentStance `json:"stances,omitempty"`
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

type BridgingStatement struct {
	PositionID       string             `json:"position_id"`
	AgentID          string             `json:"agent_id"`
	Content          string             `json:"content"`
	BridgingScore    float64            `json:"bridging_score"`
	OverallAgreeRate float64            `json:"overall_agree_rate"`
	ClusterAgreeRate map[string]float64 `json:"cluster_agree_rate"`
}

type Coalition struct {
	AgentIDs       []string `json:"agent_ids"`
	SharedCruxes   int      `json:"shared_cruxes"`
	StabilityScore float64  `json:"stability_score"`
}

type TopicSummary struct {
	TopicID string `json:"topic_id"`
	Topic   string `json:"topic"`
	Summary string `json:"summary"`
}

type AgentAlignment struct {
	AgentID        string  `json:"agent_id"`
	AlignmentScore float64 `json:"alignment_score"`
	SharedCruxes   int     `json:"shared_cruxes"`
	AgreeCruxes    int     `json:"agree_cruxes"`
}

type AgentContext struct {
	AgentID              string               `json:"agent_id"`
	ClusterID            *int                 `json:"cluster_id"`
	NearestAllies        []string             `json:"nearest_allies"`
	BiggestDisagreements []string             `json:"biggest_disagreements_with"`
	RelevantCruxes       []Crux               `json:"relevant_cruxes"`
	TopicSummaries       []TopicSummary       `json:"topic_summaries,omitempty"`
	AlignmentScores      []AgentAlignment     `json:"alignment_scores,omitempty"`
	SwingAgents          []string             `json:"swing_agents,omitempty"`
	BridgingStatements   []BridgingStatement  `json:"bridging_statements,omitempty"`
	ConsensusStatements  []ConsensusStatement `json:"consensus_statements,omitempty"`
	EffectiveWeight      float64              `json:"effective_weight,omitempty"`
	CompromiseProposal   string               `json:"compromise_proposal,omitempty"`
	FailureScenarios     []string             `json:"failure_scenarios,omitempty"`
	ConstitutionalRules  []string             `json:"constitutional_rules,omitempty"`
	EmergentNorms        []string             `json:"emergent_norms,omitempty"`
	RuleViolations       []string             `json:"rule_violations,omitempty"`
	StrategicNudge       string               `json:"strategic_nudge,omitempty"`
	DiversityNudge       string               `json:"diversity_nudge,omitempty"`
	PendingInvitations   []Invitation         `json:"pending_invitations,omitempty"`
	IntegrityWarnings    []string             `json:"integrity_warnings,omitempty"`
}

type AuditEntry struct {
	Stage  string `json:"stage"`
	Detail string `json:"detail"`
	Count  int    `json:"count,omitempty"`
}

type ExtractedClaim struct {
	AgentID      string `json:"agent_id"`
	PositionID   string `json:"position_id"`
	Claim        string `json:"claim"`
	Quote        string `json:"quote"`
	TopicName    string `json:"topic_name"`
	SubtopicName string `json:"subtopic_name"`
}

type AnalysisResult struct {
	DeliberationID       string               `json:"deliberation_id"`
	Round                int                  `json:"round_number"`
	Clusters             []OpinionCluster     `json:"clusters"`
	Cruxes               []Crux               `json:"cruxes"`
	DiscardedCruxes      []Crux               `json:"discarded_cruxes,omitempty"`
	ConsensusStatements  []ConsensusStatement `json:"consensus_statements"`
	BridgingStatements   []BridgingStatement  `json:"bridging_statements,omitempty"`
	TopicSummaries       []TopicSummary       `json:"topic_summaries"`
	AgentCount           int                  `json:"agent_count"`
	PositionCount        int                  `json:"position_count"`
	VoteCount            int                  `json:"vote_count"`
	Confidence           string               `json:"confidence"`
	Coalitions           []Coalition          `json:"coalitions,omitempty"`
	CompromiseProposal   string               `json:"compromise_proposal,omitempty"`
	ConstitutionalRules  []string             `json:"constitutional_rules,omitempty"`
	FailureScenarios     []string             `json:"failure_scenarios,omitempty"`
	ZOPA                 any                  `json:"zopa,omitempty"`
	CriteriaResults      map[string]any       `json:"criteria_results,omitempty"`
	EmergentNorms        []string             `json:"emergent_norms,omitempty"`
	RuleViolations       []string             `json:"rule_violations,omitempty"`
	TrustWeights         map[string]float64   `json:"trust_weights,omitempty"`
	CorrelationWeights   map[string]float64   `json:"correlation_weights,omitempty"`
	EffectiveWeights     map[string]float64   `json:"effective_weights,omitempty"`
	IntegrityWarnings    []string             `json:"integrity_warnings,omitempty"`
	AuditLog             []AuditEntry         `json:"audit_log,omitempty"`
	ParticipationRate    float64              `json:"participation_rate,omitempty"`
	PerspectiveDiversity float64              `json:"perspective_diversity,omitempty"`
	ParetoEfficient      []string             `json:"pareto_efficient,omitempty"`
	DominatedProposals   []string             `json:"dominated_proposals,omitempty"`
	AnalyzedAt           time.Time            `json:"analyzed_at,omitempty"`
	RecommendedAction    string               `json:"recommended_action,omitempty"`
	ExtractedClaims      []ExtractedClaim     `json:"extracted_claims,omitempty"`
	NullControl          *NullControlResult   `json:"null_control,omitempty"`
	Verification         *VerificationResult  `json:"verification,omitempty"`
	Replication          *ReplicationResult   `json:"replication,omitempty"`
	CoverageGaps         []CoverageGap        `json:"coverage_gaps,omitempty"`
}

type NullControlResult struct {
	NullDelibID   string          `json:"null_delib_id"`
	RealMetrics   PipelineMetrics `json:"real_metrics"`
	NullMetrics   PipelineMetrics `json:"null_metrics"`
	FailedMetrics []string        `json:"failed_metrics,omitempty"`
	Pass          bool            `json:"pass"`
}

type PipelineMetrics struct {
	CruxCount      int     `json:"crux_count"`
	AvgControversy float64 `json:"avg_controversy"`
	ConsensusCount int     `json:"consensus_count"`
	BridgingCount  int     `json:"bridging_count"`
	ClusterCount   int     `json:"cluster_count"`
	Confidence     string  `json:"confidence"`
}

type VerificationResult struct {
	Total      int            `json:"total"`
	Checked    int            `json:"checked"`
	Downgraded int            `json:"downgraded"`
	Threshold  int            `json:"threshold"`
	ScoreDist  []int          `json:"score_dist"` // index 0 unused, 1-5 count stances at each score
	Details    []VerifyDetail `json:"details,omitempty"`
}

type VerifyDetail struct {
	Speaker    string `json:"speaker"`
	Crux       string `json:"crux"`
	OrigStance string `json:"orig_stance"`
	Score      int    `json:"score"`
	Reason     string `json:"reason"`
}

type ReplicationResult struct {
	NumRuns   int               `json:"num_runs"`
	DelibIDs  []string          `json:"delib_ids,omitempty"`
	Runs      []PipelineMetrics `json:"runs,omitempty"`
	Stability StabilityReport   `json:"stability"`
}

type StabilityReport struct {
	Tier        int     `json:"tier"`
	CruxCV      float64 `json:"crux_cv"`
	ControvCV   float64 `json:"controv_cv"`
	ConsensusCV float64 `json:"consensus_cv"`
	AllStable   bool    `json:"all_stable"`
}

type CoverageGap struct {
	Position           string `json:"position"`
	MissingPerspective string `json:"missing_perspective"`
	SuggestedSource    string `json:"suggested_source,omitempty"`
}
