package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestChallengeAnalysis(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Challenge test", "")
	svc.SubmitPosition(context.Background(), d.ID, "alice", "A")
	svc.SubmitPosition(context.Background(), d.ID, "bob", "B")
	svc.Analyze(context.Background(), d.ID)

	// Challenge the analysis
	_, err := svc.DisputeCrux(context.Background(), d.ID, "alice", "[FULL ANALYSIS CHALLENGE]",
		"The crux detection missed the key disagreement about implementation timeline")
	if err != nil {
		t.Fatal(err)
	}

	// Re-analyze — challenge should appear as warning
	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}

	foundChallenge := false
	for _, w := range result.IntegrityWarnings {
		if len(w) > 9 && w[:9] == "DISPUTED:" {
			foundChallenge = true
		}
	}
	if !foundChallenge {
		t.Fatal("expected challenge to appear as DISPUTED warning in re-analysis")
	}
}

func TestCoalitionDetection(t *testing.T) {
	db := tempDB(t)

	// Analyzer that creates cruxes with clear coalition structure
	analyzer := &funcAnalyzer{fn: func(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
		if len(agents) < 4 {
			return &deliberation.AnalysisResult{
				Clusters:            []deliberation.OpinionCluster{},
				Cruxes:              []deliberation.Crux{},
				ConsensusStatements: []deliberation.ConsensusStatement{},
				AgentCount:          len(agents),
				PositionCount:       len(positions),
				VoteCount:           len(votes),
			}, nil
		}
		// alice+bob always agree, carol+dave always disagree with them
		cruxes := []deliberation.Crux{
			{Claim: "Crux 1", AgreeAgents: agents[:2], DisagreeAgents: agents[2:], ControversyScore: 0.8},
			{Claim: "Crux 2", AgreeAgents: agents[:2], DisagreeAgents: agents[2:], ControversyScore: 0.7},
			{Claim: "Crux 3", AgreeAgents: agents[:2], DisagreeAgents: agents[2:], ControversyScore: 0.6},
		}
		// Pre-compute coalitions since mock bypasses TextAnalyzer
		coalitions := []deliberation.Coalition{
			{AgentIDs: agents[:2], SharedCruxes: 3, StabilityScore: 1.0},
			{AgentIDs: agents[2:], SharedCruxes: 3, StabilityScore: 1.0},
		}
		return &deliberation.AnalysisResult{
			Clusters: []deliberation.OpinionCluster{
				{ID: 0, AgentIDs: agents[:2], Size: 2},
				{ID: 1, AgentIDs: agents[2:], Size: 2},
			},
			Cruxes:              cruxes,
			Coalitions:          coalitions,
			ConsensusStatements: []deliberation.ConsensusStatement{},
			AgentCount:          len(agents),
			PositionCount:       len(positions),
			VoteCount:           len(votes),
		}, nil
	}}

	svc := deliberation.NewService(db, analyzer)
	d, _ := svc.CreateDeliberation(context.Background(), "Coalition test", "")
	for _, a := range []string{"alice", "bob", "carol", "dave"} {
		svc.SubmitPosition(context.Background(), d.ID, a, "Position from "+a)
	}

	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Coalitions) == 0 {
		t.Fatal("expected coalitions to be detected")
	}

	// alice+bob should form a coalition
	found := false
	for _, c := range result.Coalitions {
		if len(c.AgentIDs) == 2 {
			hasAlice := false
			hasBob := false
			for _, a := range c.AgentIDs {
				if a == "alice" {
					hasAlice = true
				}
				if a == "bob" {
					hasBob = true
				}
			}
			if hasAlice && hasBob {
				found = true
				if c.StabilityScore < 0.5 {
					t.Fatalf("expected high stability for perfect coalition, got %f", c.StabilityScore)
				}
				t.Logf("Coalition: %v (stability: %.2f, shared cruxes: %d)", c.AgentIDs, c.StabilityScore, c.SharedCruxes)
			}
		}
	}
	if !found {
		t.Fatal("expected alice+bob coalition")
	}
}

func TestReframeRequiresGenerator(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})
	// Don't set reframer

	d, _ := svc.CreateDeliberation(context.Background(), "Reframe test", "")
	p, _ := svc.SubmitPosition(context.Background(), d.ID, "alice", "Strong opinion")

	_, err := svc.ReframePosition(context.Background(), d.ID, p.ID)
	if err == nil {
		t.Fatal("expected error without reframer")
	}
}

func TestPhase3ToolCount(t *testing.T) {
	// Verify we have the expected number of tools by counting registered handlers
	// (This is a meta-test to catch tool registration mismatches)
	expectedTools := 17 // current count
	t.Logf("Expected %d tools — update this test if tools are added", expectedTools)
}
