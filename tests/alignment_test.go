package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

// TestAlignmentThreeAgents tests pairwise alignment via GetContext.
// alice and bob agree on all 3 cruxes, carol disagrees on all 3.
func TestAlignmentThreeAgents(t *testing.T) {
	svc, _ := newTestService(t)

	// Create deliberation and submit 3 positions
	d, err := svc.CreateDeliberation("Alignment test", "Testing pairwise alignment computation")
	if err != nil {
		t.Fatal(err)
	}

	for _, agent := range []string{"alice", "bob", "carol"} {
		content := "Safety should be the top priority in all cases"
		if agent == "carol" {
			content = "Capability advancement is more important than safety constraints"
		}
		if _, err := svc.SubmitPosition(d.ID, agent, content); err != nil {
			t.Fatal(err)
		}
	}

	// Use a custom analyzer that returns 3 cruxes with alice+bob agreeing, carol disagreeing
	db := tempDB(t)
	analyzer := &factionAnalyzer{
		agreeAgents:    []string{"alice", "bob"},
		disagreeAgents: []string{"carol"},
		numCruxes:      3,
	}
	svc2 := deliberation.NewService(db, analyzer)

	d2, err := svc2.CreateDeliberation("Alignment test 2", "desc")
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"alice", "bob", "carol"} {
		if _, err := svc2.SubmitPosition(d2.ID, agent, "position from "+agent); err != nil {
			t.Fatal(err)
		}
	}

	// Run analysis
	if _, err := svc2.Analyze(context.Background(), d2.ID); err != nil {
		t.Fatal(err)
	}

	// Check alice's context: alignment with bob should be 1.0, with carol should be 0.0
	ctx, err := svc2.GetContext(d2.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}

	if len(ctx.AlignmentScores) != 2 {
		t.Fatalf("expected 2 alignment scores, got %d", len(ctx.AlignmentScores))
	}

	var bobAlignment, carolAlignment *deliberation.AgentAlignment
	for i := range ctx.AlignmentScores {
		switch ctx.AlignmentScores[i].AgentID {
		case "bob":
			bobAlignment = &ctx.AlignmentScores[i]
		case "carol":
			carolAlignment = &ctx.AlignmentScores[i]
		}
	}

	if bobAlignment == nil {
		t.Fatal("missing alignment score for bob")
	}
	if carolAlignment == nil {
		t.Fatal("missing alignment score for carol")
	}

	if bobAlignment.AlignmentScore != 1.0 {
		t.Errorf("alice-bob alignment: got %f, want 1.0", bobAlignment.AlignmentScore)
	}
	if carolAlignment.AlignmentScore != 0.0 {
		t.Errorf("alice-carol alignment: got %f, want 0.0", carolAlignment.AlignmentScore)
	}
	if bobAlignment.SharedCruxes != 3 {
		t.Errorf("alice-bob shared cruxes: got %d, want 3", bobAlignment.SharedCruxes)
	}
}

// TestAlignmentZeroCruxes verifies that 0 cruxes returns nil alignment scores.
func TestAlignmentZeroCruxes(t *testing.T) {
	db := tempDB(t)
	analyzer := &factionAnalyzer{
		agreeAgents:    []string{},
		disagreeAgents: []string{},
		numCruxes:      0,
	}
	svc := deliberation.NewService(db, analyzer)

	d, err := svc.CreateDeliberation("No cruxes", "desc")
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"alice", "bob"} {
		if _, err := svc.SubmitPosition(d.ID, agent, "position from "+agent); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := svc.Analyze(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}

	ctx, err := svc.GetContext(d.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}

	if ctx.AlignmentScores != nil {
		t.Errorf("expected nil alignment scores with 0 cruxes, got %v", ctx.AlignmentScores)
	}
}

// TestAlignmentSwingAgent verifies that an agent with no_clear_position on >= 40% of cruxes is swing.
func TestAlignmentSwingAgent(t *testing.T) {
	db := tempDB(t)
	// 5 cruxes: swinger has no_clear_position on 3 of them (60% >= 40% threshold)
	analyzer := &swingAnalyzer{}
	svc := deliberation.NewService(db, analyzer)

	d, err := svc.CreateDeliberation("Swing test", "desc")
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"alice", "bob", "swinger"} {
		if _, err := svc.SubmitPosition(d.ID, agent, "position from "+agent); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := svc.Analyze(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}

	ctx, err := svc.GetContext(d.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}

	if len(ctx.SwingAgents) != 1 || ctx.SwingAgents[0] != "swinger" {
		t.Errorf("expected swing agents [swinger], got %v", ctx.SwingAgents)
	}
}

// TestAlignmentDeterministicSort verifies sort order: descending score, tiebreak by name.
func TestAlignmentDeterministicSort(t *testing.T) {
	db := tempDB(t)
	// 2 cruxes: bob and carol both agree with alice on exactly 1 crux each → same score
	analyzer := &tiedAnalyzer{}
	svc := deliberation.NewService(db, analyzer)

	d, err := svc.CreateDeliberation("Sort test", "desc")
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"alice", "bob", "carol"} {
		if _, err := svc.SubmitPosition(d.ID, agent, "position from "+agent); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := svc.Analyze(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}

	ctx, err := svc.GetContext(d.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}

	if len(ctx.AlignmentScores) < 2 {
		t.Fatalf("expected at least 2 alignment scores, got %d", len(ctx.AlignmentScores))
	}

	// Same score → sorted by name ascending: bob before carol
	if ctx.AlignmentScores[0].AgentID != "bob" {
		t.Errorf("expected first aligned agent to be bob (alphabetical tiebreak), got %s", ctx.AlignmentScores[0].AgentID)
	}
	if ctx.AlignmentScores[1].AgentID != "carol" {
		t.Errorf("expected second aligned agent to be carol, got %s", ctx.AlignmentScores[1].AgentID)
	}
}

// --- Custom analyzers for alignment tests ---

// factionAnalyzer returns N cruxes where agreeAgents and disagreeAgents are split.
type factionAnalyzer struct {
	agreeAgents    []string
	disagreeAgents []string
	numCruxes      int
}

func (a *factionAnalyzer) Analyze(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	var cruxes []deliberation.Crux
	for i := 0; i < a.numCruxes; i++ {
		cruxes = append(cruxes, deliberation.Crux{
			Claim:            "Test crux " + string(rune('A'+i)),
			AgreeAgents:      a.agreeAgents,
			DisagreeAgents:   a.disagreeAgents,
			NoClearPosition:  []string{},
			ControversyScore: 1.0,
		})
	}
	return &deliberation.AnalysisResult{
		Clusters:            []deliberation.OpinionCluster{},
		Cruxes:              cruxes,
		ConsensusStatements: []deliberation.ConsensusStatement{},
		TopicSummaries:      []deliberation.TopicSummary{{Topic: "Test", Summary: "Test topic"}},
		AgentCount:          len(agents),
		PositionCount:       len(positions),
		VoteCount:           len(votes),
	}, nil
}

// swingAnalyzer returns 5 cruxes: alice+bob agree on all, swinger has no_clear_position on 3.
type swingAnalyzer struct{}

func (a *swingAnalyzer) Analyze(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	cruxes := make([]deliberation.Crux, 5)
	for i := range cruxes {
		cruxes[i] = deliberation.Crux{
			Claim:          "Crux " + string(rune('A'+i)),
			AgreeAgents:    []string{"alice", "bob"},
			DisagreeAgents: []string{},
		}
		if i < 3 {
			// swinger undecided on first 3
			cruxes[i].NoClearPosition = []string{"swinger"}
		} else {
			// swinger agrees on last 2
			cruxes[i].AgreeAgents = []string{"alice", "bob", "swinger"}
		}
	}
	return &deliberation.AnalysisResult{
		Clusters:            []deliberation.OpinionCluster{},
		Cruxes:              cruxes,
		ConsensusStatements: []deliberation.ConsensusStatement{},
		TopicSummaries:      []deliberation.TopicSummary{{Topic: "Test", Summary: "Swing test"}},
		AgentCount:          len(agents),
		PositionCount:       len(positions),
		VoteCount:           len(votes),
	}, nil
}

// tiedAnalyzer returns 2 cruxes where bob agrees on crux 1 but disagrees on crux 2,
// and carol disagrees on crux 1 but agrees on crux 2 — both end up with 0.5 alignment to alice.
type tiedAnalyzer struct{}

func (a *tiedAnalyzer) Analyze(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	cruxes := []deliberation.Crux{
		{
			Claim:          "Crux A",
			AgreeAgents:    []string{"alice", "bob"},
			DisagreeAgents: []string{"carol"},
		},
		{
			Claim:          "Crux B",
			AgreeAgents:    []string{"alice", "carol"},
			DisagreeAgents: []string{"bob"},
		},
	}
	return &deliberation.AnalysisResult{
		Clusters:            []deliberation.OpinionCluster{},
		Cruxes:              cruxes,
		ConsensusStatements: []deliberation.ConsensusStatement{},
		TopicSummaries:      []deliberation.TopicSummary{{Topic: "Test", Summary: "Tied test"}},
		AgentCount:          len(agents),
		PositionCount:       len(positions),
		VoteCount:           len(votes),
	}, nil
}
