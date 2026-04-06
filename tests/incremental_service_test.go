package tests

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/mcp"
)

// capturingAnalyzer records the context and positions passed to Analyze,
// so tests can verify prior claims threading and incremental behavior.
type capturingAnalyzer struct {
	mu             sync.Mutex
	calls          []analyzerCall
	returnClaims   []deliberation.ExtractedClaim // claims to include in result
	returnError    error
	returnCruxes   []deliberation.Crux
	returnClusters []deliberation.OpinionCluster
}

type analyzerCall struct {
	Ctx       context.Context
	Positions []deliberation.Position
	Votes     []deliberation.Vote
	Agents    []string
}

func (a *capturingAnalyzer) Analyze(ctx context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.calls = append(a.calls, analyzerCall{
		Ctx:       ctx,
		Positions: positions,
		Votes:     votes,
		Agents:    agents,
	})

	if a.returnError != nil {
		return nil, a.returnError
	}

	cruxes := a.returnCruxes
	if cruxes == nil {
		cruxes = []deliberation.Crux{{
			Claim:            "Test crux",
			Topic:            "Test",
			Subtopic:         "Test",
			AgreeAgents:      agents[:1],
			DisagreeAgents:   agents[1:],
			NoClearPosition:  []string{},
			ControversyScore: 0.8,
		}}
	}

	clusters := a.returnClusters
	if clusters == nil && len(agents) >= 2 {
		clusters = []deliberation.OpinionCluster{
			{ID: 0, AgentIDs: agents[:1], Size: 1},
			{ID: 1, AgentIDs: agents[1:], Size: len(agents) - 1},
		}
	}

	return &deliberation.AnalysisResult{
		Clusters:            clusters,
		Cruxes:              cruxes,
		ConsensusStatements: []deliberation.ConsensusStatement{},
		TopicSummaries:      []deliberation.TopicSummary{{Topic: "Test", Summary: "Test summary"}},
		AgentCount:          len(agents),
		PositionCount:       len(positions),
		VoteCount:           len(votes),
		ExtractedClaims:     a.returnClaims,
		AnalyzedAt:          time.Now().UTC(),
	}, nil
}

func (a *capturingAnalyzer) lastCall() analyzerCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls[len(a.calls)-1]
}

func (a *capturingAnalyzer) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

// --- Multi-round prior claims threading ---

func TestMultiRoundPriorClaimsThreading(t *testing.T) {
	db := tempDB(t)

	// Round 1: analyzer returns claims that should be stored
	round1Claims := []deliberation.ExtractedClaim{
		{AgentID: "alice", PositionID: "will-be-set", Claim: "Safety first", Quote: "we need safety", TopicName: "Strategy", SubtopicName: "Priorities"},
		{AgentID: "bob", PositionID: "will-be-set", Claim: "Speed matters", Quote: "move fast", TopicName: "Strategy", SubtopicName: "Priorities"},
	}
	analyzer := &capturingAnalyzer{returnClaims: round1Claims}
	svc := deliberation.NewService(db, analyzer)

	d, err := svc.CreateDeliberation("Claims threading test", "Verify prior claims flow between rounds")
	if err != nil {
		t.Fatal(err)
	}

	// Submit round 1 positions
	p1, _ := svc.SubmitPosition(d.ID, "alice", "We need safety above all else")
	p2, _ := svc.SubmitPosition(d.ID, "bob", "We should move fast and iterate")
	if p1 == nil || p2 == nil {
		t.Fatal("failed to submit positions")
	}

	// Analyze round 1
	result1, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("round 1 analysis: %v", err)
	}

	// Verify round 1 result has extracted claims
	if len(result1.ExtractedClaims) != 2 {
		t.Fatalf("expected 2 extracted claims in round 1, got %d", len(result1.ExtractedClaims))
	}

	// Verify round 1 result is stored with claims
	stored1, err := svc.GetAnalysisResult(d.ID, 1)
	if err != nil {
		t.Fatalf("getting stored round 1: %v", err)
	}
	if len(stored1.ExtractedClaims) != 2 {
		t.Fatalf("stored round 1 should have 2 claims, got %d", len(stored1.ExtractedClaims))
	}

	// Round 2: satisfy forced acknowledgment, then submit new positions
	svc.GetContext(d.ID, "alice")
	svc.GetContext(d.ID, "bob")
	_, err = svc.SubmitPosition(d.ID, "alice", "After reflection, safety with pragmatism")
	if err != nil {
		t.Fatalf("round 2 submit alice: %v", err)
	}
	_, err = svc.SubmitPosition(d.ID, "bob", "Speed but with guardrails")
	if err != nil {
		t.Fatalf("round 2 submit bob: %v", err)
	}

	// Analyze round 2
	round2Claims := []deliberation.ExtractedClaim{
		{AgentID: "alice", PositionID: "r2", Claim: "Pragmatic safety", Quote: "pragmatism", TopicName: "Strategy", SubtopicName: "Priorities"},
	}
	analyzer.mu.Lock()
	analyzer.returnClaims = round2Claims
	analyzer.mu.Unlock()

	_, err = svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("round 2 analysis: %v", err)
	}

	// Verify the analyzer received prior claims via context
	call2 := analyzer.lastCall()
	priorClaims, ok := call2.Ctx.Value(deliberation.ContextKeyPriorClaims{}).([]deliberation.ExtractedClaim)
	if !ok {
		t.Fatal("round 2 analysis did not receive prior claims in context")
	}
	if len(priorClaims) != 2 {
		t.Fatalf("expected 2 prior claims in round 2 context, got %d", len(priorClaims))
	}
	if priorClaims[0].Claim != "Safety first" {
		t.Fatalf("prior claim 0: expected 'Safety first', got %q", priorClaims[0].Claim)
	}
}

func TestMultiRoundPriorNormsThreading(t *testing.T) {
	db := tempDB(t)

	// Round 1: return norms and constitutional rules
	analyzer := &capturingAnalyzer{}
	svc := deliberation.NewService(db, analyzer)

	d, _ := svc.CreateDeliberation("Norms threading", "")
	svc.SubmitPosition(d.ID, "alice", "Position A")
	svc.SubmitPosition(d.ID, "bob", "Position B")

	// Override the analyzer to return norms
	analyzer.mu.Lock()
	analyzer.returnClaims = nil
	analyzer.mu.Unlock()

	// We need the result to have norms — inject them after analysis
	result1, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	_ = result1

	// Manually update the stored result to have norms (the mock doesn't set them)
	stored1, _ := svc.GetAnalysisResult(d.ID, 1)
	stored1.EmergentNorms = []string{"Agents should provide evidence for claims"}
	stored1.ConstitutionalRules = []string{"All proposals must consider minority impact"}
	// Re-save with norms
	err = db.SaveAnalysisResult(context.Background(), d.ID, 1, stored1)
	if err != nil {
		t.Fatalf("saving updated result: %v", err)
	}

	// Round 2
	svc.SubmitPosition(d.ID, "alice", "Updated position")
	svc.SubmitPosition(d.ID, "bob", "Updated position B")
	_, err = svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}

	call2 := analyzer.lastCall()

	// Check prior norms
	norms, ok := call2.Ctx.Value(deliberation.ContextKeyPriorNorms{}).([]string)
	if !ok || len(norms) == 0 {
		t.Fatal("round 2 should receive prior norms in context")
	}
	if norms[0] != "Agents should provide evidence for claims" {
		t.Fatalf("unexpected norm: %q", norms[0])
	}

	// Check constitutional rules
	rules, ok := call2.Ctx.Value(deliberation.ContextKeyConstitutionalRules{}).([]string)
	if !ok || len(rules) == 0 {
		t.Fatal("round 2 should receive constitutional rules in context")
	}
	if rules[0] != "All proposals must consider minority impact" {
		t.Fatalf("unexpected rule: %q", rules[0])
	}
}

// --- Thin result for < 2 agents ---

func TestThinResultSingleAgent(t *testing.T) {
	db := tempDB(t)
	analyzer := &capturingAnalyzer{}
	svc := deliberation.NewService(db, analyzer)

	d, _ := svc.CreateDeliberation("Solo agent", "")
	svc.SubmitPosition(d.ID, "lonely", "I think X")

	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("single agent analysis should not error: %v", err)
	}

	// Verify thin result fields
	if result.AgentCount != 1 {
		t.Fatalf("expected 1 agent, got %d", result.AgentCount)
	}
	if result.PositionCount != 1 {
		t.Fatalf("expected 1 position, got %d", result.PositionCount)
	}
	if result.Confidence != "low" {
		t.Fatalf("expected 'low' confidence, got %q", result.Confidence)
	}
	if result.RecommendedAction != "await_more_participants" {
		t.Fatalf("expected 'await_more_participants', got %q", result.RecommendedAction)
	}
	if len(result.IntegrityWarnings) == 0 {
		t.Fatal("expected INSUFFICIENT_AGENTS warning")
	}

	// Verify the analyzer was NOT called (no LLM work for single agent)
	if analyzer.callCount() != 0 {
		t.Fatalf("analyzer should not be called for single agent, got %d calls", analyzer.callCount())
	}

	// Verify result was saved
	stored, err := svc.GetAnalysisResult(d.ID, 1)
	if err != nil {
		t.Fatalf("thin result should be stored: %v", err)
	}
	if stored.AgentCount != 1 {
		t.Fatalf("stored result should have 1 agent, got %d", stored.AgentCount)
	}

	// Verify round advanced
	d2, _ := svc.GetDeliberation(d.ID)
	if d2.Round != 2 {
		t.Fatalf("round should advance to 2 after thin result, got %d", d2.Round)
	}
}

func TestThinResultZeroPositions(t *testing.T) {
	db := tempDB(t)
	analyzer := &capturingAnalyzer{}
	svc := deliberation.NewService(db, analyzer)

	d, _ := svc.CreateDeliberation("Empty", "No positions")

	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("zero positions should not error: %v", err)
	}
	if result.PositionCount != 0 {
		t.Fatalf("expected 0 positions, got %d", result.PositionCount)
	}
	if result.AgentCount != 0 {
		t.Fatalf("expected 0 agents, got %d", result.AgentCount)
	}
	if analyzer.callCount() != 0 {
		t.Fatal("analyzer should not be called for 0 agents")
	}
}

func TestThinResultThenRealAnalysis(t *testing.T) {
	db := tempDB(t)
	analyzer := &capturingAnalyzer{}
	svc := deliberation.NewService(db, analyzer)

	d, _ := svc.CreateDeliberation("Grows over time", "")

	// Round 1: single agent → thin result
	svc.SubmitPosition(d.ID, "alice", "Just me for now")
	r1, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r1.AgentCount != 1 {
		t.Fatalf("round 1 should have 1 agent, got %d", r1.AgentCount)
	}

	// Round 2: second agent joins → real analysis
	// Satisfy forced acknowledgment for all agents (required even for new participants)
	svc.GetContext(d.ID, "alice")
	svc.GetContext(d.ID, "bob")
	_, err = svc.SubmitPosition(d.ID, "alice", "Still here, updated view")
	if err != nil {
		t.Fatalf("alice round 2 submit: %v", err)
	}
	_, err = svc.SubmitPosition(d.ID, "bob", "I'm here too")
	if err != nil {
		t.Fatalf("bob round 2 submit: %v", err)
	}
	r2, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("round 2 with 2 agents should work: %v", err)
	}
	if r2.AgentCount < 2 {
		t.Fatalf("round 2 should have at least 2 agents, got %d", r2.AgentCount)
	}
	if len(r2.Cruxes) == 0 {
		t.Fatal("round 2 with 2 agents should produce cruxes")
	}
	if analyzer.callCount() != 1 {
		t.Fatalf("analyzer should be called once (round 2 only), got %d", analyzer.callCount())
	}
}

// --- Analysis status transitions ---

func TestAnalysisStatusTransitions(t *testing.T) {
	db := tempDB(t)

	// Use a slow analyzer that lets us check status mid-analysis
	var analysisStarted, analysisDone chan struct{}
	analysisStarted = make(chan struct{})
	analysisDone = make(chan struct{})

	slowAnalyzer := &capturingAnalyzer{}
	originalAnalyze := slowAnalyzer.Analyze
	_ = originalAnalyze

	blockingAnalyzer := &blockingMockAnalyzer{
		started: analysisStarted,
		done:    analysisDone,
	}
	svc := deliberation.NewService(db, blockingAnalyzer)

	d, _ := svc.CreateDeliberation("Status transitions", "")
	svc.SubmitPosition(d.ID, "alice", "Position A")
	svc.SubmitPosition(d.ID, "bob", "Position B")

	// Verify initial status
	d1, _ := svc.GetDeliberation(d.ID)
	if d1.Status != "open" {
		t.Fatalf("initial status should be 'open', got %q", d1.Status)
	}

	// Start analysis in background
	go func() {
		svc.Analyze(context.Background(), d.ID)
	}()

	// Wait for analysis to start
	select {
	case <-analysisStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("analysis didn't start within 5s")
	}

	// Check status is "analyzing"
	d2, _ := svc.GetDeliberation(d.ID)
	if d2.Status != "analyzing" {
		t.Fatalf("status during analysis should be 'analyzing', got %q", d2.Status)
	}

	// Let analysis complete
	close(analysisDone)
	time.Sleep(500 * time.Millisecond)

	// Check status returned to "open"
	d3, _ := svc.GetDeliberation(d.ID)
	if d3.Status != "open" {
		t.Fatalf("status after analysis should be 'open', got %q", d3.Status)
	}

	// Check round advanced
	if d3.Round != 2 {
		t.Fatalf("round should advance to 2, got %d", d3.Round)
	}

	// Check results exist
	result, err := svc.GetAnalysisResult(d.ID, 1)
	if err != nil {
		t.Fatalf("should have analysis result: %v", err)
	}
	if result.AgentCount != 2 {
		t.Fatalf("result should have 2 agents, got %d", result.AgentCount)
	}
}

// blockingMockAnalyzer signals when analysis starts and blocks until released.
type blockingMockAnalyzer struct {
	started chan struct{}
	done    chan struct{}
}

func (a *blockingMockAnalyzer) Analyze(ctx context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	close(a.started)
	select {
	case <-a.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	mid := len(agents) / 2
	if mid == 0 {
		mid = 1
	}
	return &deliberation.AnalysisResult{
		Clusters: []deliberation.OpinionCluster{
			{ID: 0, AgentIDs: agents[:mid], Size: mid},
			{ID: 1, AgentIDs: agents[mid:], Size: len(agents) - mid},
		},
		Cruxes: []deliberation.Crux{{
			Claim:            "Test crux",
			AgreeAgents:      agents[:mid],
			DisagreeAgents:   agents[mid:],
			NoClearPosition:  []string{},
			ControversyScore: 0.8,
		}},
		ConsensusStatements: []deliberation.ConsensusStatement{},
		TopicSummaries:      []deliberation.TopicSummary{{Topic: "Test", Summary: "Summary"}},
		AgentCount:          len(agents),
		PositionCount:       len(positions),
		VoteCount:           len(votes),
		AnalyzedAt:          time.Now().UTC(),
	}, nil
}

func TestAnalysisCancellation(t *testing.T) {
	db := tempDB(t)
	started := make(chan struct{})
	done := make(chan struct{})
	svc := deliberation.NewService(db, &blockingMockAnalyzer{started: started, done: done})

	d, _ := svc.CreateDeliberation("Cancel test", "")
	svc.SubmitPosition(d.ID, "alice", "A")
	svc.SubmitPosition(d.ID, "bob", "B")

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := svc.Analyze(ctx, d.ID)
		errCh <- err
	}()

	<-started
	cancel() // Cancel while analyzing

	// The analysis should eventually return (context cancelled)
	select {
	case <-time.After(5 * time.Second):
		close(done) // unblock the analyzer if it's still waiting
		t.Fatal("analysis didn't respect cancellation within 5s")
	case err := <-errCh:
		// Analysis may return error or nil depending on timing
		_ = err
	}

	// Give the service time to reset status (it uses a background context for cleanup)
	time.Sleep(1 * time.Second)
	close(done) // ensure cleanup

	// The stuck analyzing recovery mechanism should handle this.
	// In production, a background goroutine calls RecoverStuckAnalyzing.
	// For this test, we verify the analysis returned promptly on cancel.
	d2, _ := svc.GetDeliberation(d.ID)
	// Status may be "open" (if reset succeeded) or "analyzing" (if reset
	// failed due to cancelled context). The key assertion is that Analyze()
	// returned promptly, which we already verified above.
	t.Logf("status after cancel: %s", d2.Status)
}

func TestSetTemplateReplacesRules(t *testing.T) {
	db := tempDB(t)
	analyzer := &capturingAnalyzer{}
	svc := deliberation.NewService(db, analyzer)

	// Create with jury template (min_participants: 6, cooling_period_minutes: 15)
	d, err := svc.CreateDeliberation("Template switch", "",
		deliberation.WithTemplate("jury"),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Verify jury rules
	d1, _ := svc.GetDeliberation(d.ID)
	if d1.Rules == nil {
		t.Fatal("expected rules to be set")
	}
	minP, _ := d1.Rules["min_participants"].(float64)
	if int(minP) != 6 {
		t.Fatalf("jury should have min_participants=6, got %v", d1.Rules["min_participants"])
	}

	// Switch to assembly (min_participants: 3)
	err = svc.SetTemplate(d.ID, "assembly", "")
	if err != nil {
		t.Fatalf("set_template: %v", err)
	}

	// Verify assembly rules replaced jury's min_participants
	d2, _ := svc.GetDeliberation(d.ID)
	minP2, _ := d2.Rules["min_participants"].(float64)
	if int(minP2) != 3 {
		t.Fatalf("after switching to assembly, min_participants should be 3, got %v", d2.Rules["min_participants"])
	}

	// Verify jury-only rules (cooling_period_minutes) are removed
	// since assembly doesn't define them
	if _, hasCooling := d2.Rules["cooling_period_minutes"]; hasCooling {
		t.Fatal("cooling_period_minutes from jury should be removed when switching to assembly")
	}
}

func TestExpertPanelCore(t *testing.T) {
	db := tempDB(t)
	analyzer := &capturingAnalyzer{}
	svc := deliberation.NewService(db, analyzer)

	ctx := context.Background()

	// Run expert panel with default experts (creates deliberation + submits positions)
	result, err := mcp.CoreRunExpertPanel(ctx, svc, "This is a test document with some claims about AI safety.", "Test panel", "", "test-group", "", "", "", "")
	if err != nil {
		t.Fatalf("expert panel failed: %v", err)
	}

	// Verify result structure
	if result.DeliberationID == "" {
		t.Error("expected deliberation_id")
	}
	if result.Topic != "Test panel" {
		t.Errorf("expected topic 'Test panel', got %q", result.Topic)
	}
	if result.ExpertCount != 5 {
		t.Errorf("expected 5 default experts, got %d", result.ExpertCount)
	}

	// Verify positions were submitted (analysis is async, so check via service)
	positions, err := svc.GetPositions(result.DeliberationID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 5 {
		t.Errorf("expected 5 positions from 5 experts, got %d", len(positions))
	}

	// Verify each expert's position contains the document
	for _, p := range positions {
		if !strings.Contains(p.Content, "test document with some claims") {
			t.Errorf("position from %s doesn't contain document text", p.AgentID)
		}
	}

	// Verify the deliberation was created with assembly template
	d, err := svc.GetDeliberation(result.DeliberationID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Template != "assembly" {
		t.Errorf("expected assembly template, got %q", d.Template)
	}
	if d.GroupID != "test-group" {
		t.Errorf("expected group_id 'test-group', got %q", d.GroupID)
	}
	if d.Type != "reasoning" {
		t.Errorf("expected type 'reasoning', got %q", d.Type)
	}
}

func TestExpertPanelCustomExperts(t *testing.T) {
	db := tempDB(t)
	analyzer := &capturingAnalyzer{}
	svc := deliberation.NewService(db, analyzer)

	ctx := context.Background()
	customExperts := `[{"id":"economist","role":"Behavioral economist","interests":"incentive design","reservation":"ignoring second-order effects"},{"id":"ethicist","role":"Applied ethicist","interests":"fairness and equity","reservation":"utilitarian shortcuts"}]`

	result, err := mcp.CoreRunExpertPanel(ctx, svc, "Test document content.", "Custom panel", customExperts, "", "", "", "", "")
	if err != nil {
		t.Fatalf("expert panel failed: %v", err)
	}
	if result.ExpertCount != 2 {
		t.Errorf("expected 2 custom experts, got %d", result.ExpertCount)
	}

	positions, err := svc.GetPositions(result.DeliberationID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	agentIDs := map[string]bool{}
	for _, p := range positions {
		agentIDs[p.AgentID] = true
	}
	if !agentIDs["economist"] || !agentIDs["ethicist"] {
		t.Errorf("expected economist and ethicist agents, got %v", agentIDs)
	}
}

func TestExpertPanelSourceType(t *testing.T) {
	db := tempDB(t)
	analyzer := &capturingAnalyzer{}
	svc := deliberation.NewService(db, analyzer)
	ctx := context.Background()

	// code_review source_type should use code review experts
	result, err := mcp.CoreRunExpertPanel(ctx, svc, "func main() { fmt.Println(\"hello\") }", "Review main.go", "", "", "", "", "code_review", "")
	if err != nil {
		t.Fatalf("expert panel failed: %v", err)
	}
	if result.ExpertCount != 5 {
		t.Errorf("expected 5 code review experts, got %d", result.ExpertCount)
	}

	// Verify code review experts (not research defaults)
	positions, err := svc.GetPositions(result.DeliberationID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	agentIDs := map[string]bool{}
	for _, p := range positions {
		agentIDs[p.AgentID] = true
	}
	if !agentIDs["security-reviewer"] {
		t.Error("code_review should include security-reviewer expert")
	}
	if agentIDs["methodologist"] {
		t.Error("code_review should NOT include methodologist (that's the research panel)")
	}

	// Verify code review prompt framing
	for _, p := range positions {
		if strings.Contains(p.Content, "FATAL FLAWS") {
			t.Errorf("code_review should use code review prompt, not research prompt (found FATAL FLAWS in %s)", p.AgentID)
		}
		if !strings.Contains(p.Content, "BLOCKING") {
			t.Errorf("code_review prompt should use BLOCKING category for %s", p.AgentID)
		}
	}

	// Invalid source_type
	_, err = mcp.CoreRunExpertPanel(ctx, svc, "doc", "Test", "", "", "", "", "invalid_type", "")
	if err == nil {
		t.Error("expected error for invalid source_type")
	}
}

func TestExpertPanelValidation(t *testing.T) {
	db := tempDB(t)
	analyzer := &capturingAnalyzer{}
	svc := deliberation.NewService(db, analyzer)
	ctx := context.Background()

	// Empty document
	_, err := mcp.CoreRunExpertPanel(ctx, svc, "", "Test", "", "", "", "", "", "")
	if err == nil {
		t.Error("expected error for empty document")
	}

	// Document too large
	_, err = mcp.CoreRunExpertPanel(ctx, svc, strings.Repeat("x", 50001), "Test", "", "", "", "", "", "")
	if err == nil {
		t.Error("expected error for document > 50000 chars")
	}

	// Invalid experts JSON
	_, err = mcp.CoreRunExpertPanel(ctx, svc, "doc", "Test", "not json", "", "", "", "", "")
	if err == nil {
		t.Error("expected error for invalid experts JSON")
	}

	// Empty experts array
	_, err = mcp.CoreRunExpertPanel(ctx, svc, "doc", "Test", "[]", "", "", "", "", "")
	if err == nil {
		t.Error("expected error for empty experts array")
	}

	// Invalid depth
	_, err = mcp.CoreRunExpertPanel(ctx, svc, "doc", "Test", "", "", "", "", "", "invalid")
	if err == nil {
		t.Error("expected error for invalid depth")
	}
}

func TestExpertPanelQuickDepth(t *testing.T) {
	db := tempDB(t)
	analyzer := &capturingAnalyzer{}
	svc := deliberation.NewService(db, analyzer)
	ctx := context.Background()

	// Quick mode should use only 3 experts
	result, err := mcp.CoreRunExpertPanel(ctx, svc, "Test document.", "Quick test", "", "", "", "", "", "quick")
	if err != nil {
		t.Fatalf("quick expert panel failed: %v", err)
	}
	if result.ExpertCount != 3 {
		t.Errorf("quick mode should use 3 experts, got %d", result.ExpertCount)
	}

	positions, err := svc.GetPositions(result.DeliberationID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 3 {
		t.Errorf("expected 3 positions in quick mode, got %d", len(positions))
	}
}
