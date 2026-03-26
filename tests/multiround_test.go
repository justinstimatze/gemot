package tests

import (
	"context"
	"testing"
)

func TestMultiRoundConvergence(t *testing.T) {
	svc, _ := newTestService(t)

	// Step 1: Create a deliberation
	d, err := svc.CreateDeliberation("Multi-round convergence", "Testing that multi-round deliberation works end to end")
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: Submit positions from 4 agents (round 1)
	agents := []string{"alpha", "beta", "gamma", "delta"}
	round1Positions := []string{
		"We should prioritize safety above all else.",
		"Capability advances are what drive safety research forward.",
		"A balanced approach considering both safety and capability is best.",
		"Open development is the strongest safety mechanism we have.",
	}
	for i, agent := range agents {
		_, err := svc.SubmitPosition(d.ID, agent, round1Positions[i])
		if err != nil {
			t.Fatalf("round 1: submitting position for %s: %v", agent, err)
		}
	}

	// Step 3: Analyze (round 1)
	result1, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("round 1 analysis: %v", err)
	}
	if result1.Round != 1 {
		t.Fatalf("expected round 1, got %d", result1.Round)
	}
	if result1.AgentCount != 4 {
		t.Fatalf("expected 4 agents, got %d", result1.AgentCount)
	}

	// Step 4: Get context for each agent and verify
	for _, agent := range agents {
		actx, err := svc.GetContext(d.ID, agent)
		if err != nil {
			t.Fatalf("get_context for %s: %v", agent, err)
		}
		if actx.AgentID != agent {
			t.Fatalf("expected agent_id %q, got %q", agent, actx.AgentID)
		}
		if actx.ClusterID == nil {
			t.Fatalf("expected %s to be in a cluster", agent)
		}
		// Each agent should have allies (other agents in same cluster)
		// With the mock analyzer splitting first half vs second half,
		// each cluster has 2 agents so each has 1 ally
		if len(actx.NearestAllies) == 0 {
			t.Fatalf("expected %s to have allies", agent)
		}
		// Each agent should have disagreements (from the crux)
		if len(actx.BiggestDisagreements) == 0 {
			t.Fatalf("expected %s to have disagreements", agent)
		}
	}

	// Step 5: Verify round advanced to 2
	d2, err := svc.GetDeliberation(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Round != 2 {
		t.Fatalf("expected round 2 after first analysis, got %d", d2.Round)
	}

	// Step 6: Submit updated positions for round 2 (simulate agents refining based on context)
	round2Positions := []string{
		"After considering others' views, safety should be primary but not exclusive.",
		"I still believe capability drives safety, but acknowledge the need for guardrails.",
		"The balanced approach is validated — we need both safety research and capability work.",
		"Open development with safety guidelines is the path forward.",
	}
	for i, agent := range agents {
		_, err := svc.SubmitPosition(d.ID, agent, round2Positions[i])
		if err != nil {
			t.Fatalf("round 2: submitting position for %s: %v", agent, err)
		}
	}

	// Step 7: Analyze (round 2)
	result2, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("round 2 analysis: %v", err)
	}
	if result2.Round != 2 {
		t.Fatalf("expected round 2, got %d", result2.Round)
	}

	// Step 8: Verify round advanced to 3
	d3, err := svc.GetDeliberation(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d3.Round != 3 {
		t.Fatalf("expected round 3 after second analysis, got %d", d3.Round)
	}

	// Step 9: Verify both rounds' analysis results are stored and retrievable
	stored1, err := svc.GetAnalysisResult(d.ID, 1)
	if err != nil {
		t.Fatalf("retrieving round 1 result: %v", err)
	}
	if stored1.Round != 1 {
		t.Fatalf("stored round 1 result has round %d", stored1.Round)
	}
	if stored1.PositionCount != 4 {
		t.Fatalf("round 1 should have 4 positions, got %d", stored1.PositionCount)
	}

	stored2, err := svc.GetAnalysisResult(d.ID, 2)
	if err != nil {
		t.Fatalf("retrieving round 2 result: %v", err)
	}
	if stored2.Round != 2 {
		t.Fatalf("stored round 2 result has round %d", stored2.Round)
	}
	// Round 2 should have 8 positions total (4 from round 1 + 4 from round 2)
	if stored2.PositionCount != 8 {
		t.Fatalf("round 2 should have 8 positions, got %d", stored2.PositionCount)
	}
}
