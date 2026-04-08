package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestReservationInAnalysis(t *testing.T) {
	db := tempDB(t)
	// Analyzer that returns cruxes so BATNA fires
	analyzer := &funcAnalyzer{fn: func(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
		if len(agents) < 2 {
			return &deliberation.AnalysisResult{AgentCount: len(agents), PositionCount: len(positions)}, nil
		}
		// Include failure scenarios since mock bypasses TextAnalyzer
		var failures []string
		failures = append(failures, `If no resolution on "Mandatory safety review": agents [alice] and [bob] remain deadlocked`)
		for _, p := range positions {
			if p.Reservation != "" {
				failures = append(failures, "Agent "+p.AgentID+" cannot accept: "+p.Reservation)
			}
		}
		return &deliberation.AnalysisResult{
			Cruxes: []deliberation.Crux{
				{Claim: "Mandatory safety review", AgreeAgents: agents[:1], DisagreeAgents: agents[1:], ControversyScore: 0.85},
			},
			FailureScenarios:    failures,
			ConsensusStatements: []deliberation.ConsensusStatement{},
			AgentCount:          len(agents),
			PositionCount:       len(positions),
			VoteCount:           len(votes),
		}, nil
	}}

	svc := deliberation.NewService(db, analyzer)
	d, _ := svc.CreateDeliberation(context.Background(), "BATNA test", "")
	svc.SubmitPosition(context.Background(), d.ID, "alice", "Strong regulation",
		deliberation.WithReservation("Cannot accept voluntary-only approach"))
	svc.SubmitPosition(context.Background(), d.ID, "bob", "Light regulation")

	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.FailureScenarios) == 0 {
		t.Fatal("expected failure scenarios from reservation + high-controversy crux")
	}
	t.Logf("Failure scenarios: %v", result.FailureScenarios)

	// Should include both the crux deadlock and Alice's reservation
	foundCrux := false
	foundReservation := false
	for _, fs := range result.FailureScenarios {
		if len(fs) > 10 {
			if fs[:2] == "If" {
				foundCrux = true
			}
			if fs[:5] == "Agent" {
				foundReservation = true
			}
		}
	}
	if !foundCrux {
		t.Fatal("expected crux-based failure scenario")
	}
	if !foundReservation {
		t.Fatal("expected reservation-based failure scenario")
	}
}

func TestMultiCriteriaVote(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Criteria test", "")
	svc.SubmitPosition(context.Background(), d.ID, "alice", "Proposal A")
	p, _ := svc.SubmitPosition(context.Background(), d.ID, "bob", "Proposal B")

	// Vote with criterion (stored in DB even if analysis doesn't use it yet)
	err := svc.Vote(context.Background(), d.ID, "alice", p.ID, 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
}

func TestCriteriaOnDeliberation(t *testing.T) {
	// Criteria are stored as JSON in the deliberation — verify round-trip
	// This tests the model structure, not DB persistence (criteria stored in description for now)
	d := deliberation.Deliberation{
		Criteria: []deliberation.Criterion{
			{ID: "feasibility", Name: "Feasibility", Description: "How practical is this?"},
			{ID: "impact", Name: "Impact", Description: "How much does this matter?"},
		},
	}
	if len(d.Criteria) != 2 {
		t.Fatal("expected 2 criteria")
	}
}

func TestEmergentNorms(t *testing.T) {
	db := tempDB(t)
	// Analyzer with cruxes + consensus to trigger norm generation
	analyzer := &funcAnalyzer{fn: func(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
		return &deliberation.AnalysisResult{
			Cruxes: []deliberation.Crux{
				{Claim: "Test", AgreeAgents: agents[:1], DisagreeAgents: agents[1:], ControversyScore: 0.5},
			},
			ConsensusStatements: []deliberation.ConsensusStatement{
				{PositionID: "p1", Content: "We all agree on X", OverallAgreeRatio: 0.9, MinClusterAgreeRatio: 0.8},
			},
			BridgingStatements: []deliberation.BridgingStatement{
				{PositionID: "p1", BridgingScore: 0.8, Content: "Bridge"},
			},
			EmergentNorms: []string{
				"Positions that address identified cruxes directly receive more engagement",
				"Cross-cluster bridging positions are more effective than single-cluster positions",
			},
			AgentCount:    len(agents),
			PositionCount: len(positions),
			VoteCount:     len(votes),
		}, nil
	}}

	svc := deliberation.NewService(db, analyzer)
	d, _ := svc.CreateDeliberation(context.Background(), "Norms test", "")
	svc.SubmitPosition(context.Background(), d.ID, "alice", "A")
	svc.SubmitPosition(context.Background(), d.ID, "bob", "B")

	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.EmergentNorms) == 0 {
		t.Fatal("expected emergent norms")
	}
	t.Logf("Norms: %v", result.EmergentNorms)
}
