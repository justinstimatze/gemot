package main

// pipelineConfig holds all configuration for a structural mode run.
type pipelineConfig struct {
	MCPURL        string
	Template      string
	Threshold     float64
	AllCruxes     bool
	DryRun        bool
	GroupID       string
	Rounds        int
	ReportPath    string
	NullControl   bool
	SpotCheck     bool
	ReplicateN    int
	CoverageAudit bool
	ResumeID      string // deliberation ID to resume (skip creation/submission)
	Named         bool   // use real speaker names (default: anonymized to prevent false attribution)
}

// reportInput bundles all data needed to generate a markdown report.
type reportInput struct {
	Data         *ReportData
	R1JSON       string
	R2JSON       string
	R3JSON       string
	R1Compromise string
	R2Compromise string
	R3Compromise string
	R1Agents     []agentPlan
	R2Agents     []agentPlan
	R3Agents     []agentPlan
	Template     string
	DelibID      string
	JoinCode     string
	NullControl  *nullControlResult
	SpotCheck      *spotCheckResult
	CruxSpotCheck  *spotCheckResult
	Named          bool
	Replication    *replicationResult
	Coverage       *coverageResult
}
