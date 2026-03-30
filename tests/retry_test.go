package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

// TestRetryEnrichedContext creates a 3-agent deliberation with clear factions,
// runs analysis, and verifies GetContext returns enriched fields.
func TestRetryEnrichedContext(t *testing.T) {
	db := tempDB(t)
	// Use factionAnalyzer for clear alice+bob vs carol split with 3 cruxes
	analyzer := &factionAnalyzer{
		agreeAgents:    []string{"alice", "bob"},
		disagreeAgents: []string{"carol"},
		numCruxes:      3,
	}
	svc := deliberation.NewService(db, analyzer)

	d, err := svc.CreateDeliberation("Enriched context test", "Testing full pipeline enriched context")
	if err != nil {
		t.Fatal(err)
	}

	// Submit positions from 3 agents with clear factions
	positions := map[string]string{
		"alice": "We should prioritize safety measures above all else",
		"bob":   "Safety is paramount, capability can wait",
		"carol": "Capability advancement drives progress, safety is secondary",
	}
	for agent, content := range positions {
		if _, err := svc.SubmitPosition(d.ID, agent, content); err != nil {
			t.Fatal(err)
		}
	}

	// Run analysis
	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}

	if result == nil {
		t.Fatal("analysis result is nil")
	}

	// Verify GetContext returns enriched fields for alice
	ctx, err := svc.GetContext(d.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}

	// TopicSummaries should be populated (from factionAnalyzer)
	if len(ctx.TopicSummaries) == 0 {
		t.Error("expected TopicSummaries to be populated")
	}

	// AlignmentScores should be populated (computed from cruxes)
	if len(ctx.AlignmentScores) == 0 {
		t.Error("expected AlignmentScores to be populated")
	}

	// Verify alice-bob alignment is high and alice-carol is low
	for _, a := range ctx.AlignmentScores {
		if a.AgentID == "bob" && a.AlignmentScore != 1.0 {
			t.Errorf("alice-bob alignment: got %f, want 1.0", a.AlignmentScore)
		}
		if a.AgentID == "carol" && a.AlignmentScore != 0.0 {
			t.Errorf("alice-carol alignment: got %f, want 0.0", a.AlignmentScore)
		}
	}

	// Cooperation data fields should not panic even if empty
	// (the mock analyzer doesn't set them, but accessing them should be safe)
	_ = ctx.CompromiseProposal
	_ = ctx.FailureScenarios
	_ = ctx.ConstitutionalRules
	_ = ctx.EmergentNorms
	_ = ctx.RuleViolations
	_ = ctx.StrategicNudge
	_ = ctx.DiversityNudge

	// Verify for carol too (the "other" faction)
	ctxCarol, err := svc.GetContext(d.ID, "carol")
	if err != nil {
		t.Fatal(err)
	}

	if len(ctxCarol.AlignmentScores) == 0 {
		t.Error("expected carol's AlignmentScores to be populated")
	}

	// Carol should see bob as low-alignment and alice as low-alignment
	for _, a := range ctxCarol.AlignmentScores {
		if a.AgentID == "alice" && a.AlignmentScore != 0.0 {
			t.Errorf("carol-alice alignment: got %f, want 0.0", a.AlignmentScore)
		}
		if a.AgentID == "bob" && a.AlignmentScore != 0.0 {
			t.Errorf("carol-bob alignment: got %f, want 0.0", a.AlignmentScore)
		}
	}

	// RelevantCruxes should have entries for both agents
	if len(ctx.RelevantCruxes) == 0 {
		t.Error("expected alice's RelevantCruxes to be populated")
	}
	if len(ctxCarol.RelevantCruxes) == 0 {
		t.Error("expected carol's RelevantCruxes to be populated")
	}
}
