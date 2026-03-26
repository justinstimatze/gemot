package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/store"
)

// mockAnalyzer provides deterministic analysis results for testing.
type mockAnalyzer struct{}

func (m *mockAnalyzer) Analyze(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	if len(agents) == 0 {
		return &deliberation.AnalysisResult{
			Clusters:            []deliberation.OpinionCluster{},
			Cruxes:              []deliberation.Crux{},
			ConsensusStatements: []deliberation.ConsensusStatement{},
			TopicSummaries:      []deliberation.TopicSummary{},
			PositionCount:       len(positions),
			VoteCount:           len(votes),
		}, nil
	}

	// Build simple clusters: first half vs second half
	mid := len(agents) / 2
	if mid == 0 {
		mid = 1
	}

	cluster1 := deliberation.OpinionCluster{
		ID:       0,
		AgentIDs: agents[:mid],
		Size:     mid,
	}
	cluster2 := deliberation.OpinionCluster{
		ID:       1,
		AgentIDs: agents[mid:],
		Size:     len(agents) - mid,
	}

	crux := deliberation.Crux{
		Claim:            "The approach should prioritize safety over capability",
		Topic:            "Strategy",
		Subtopic:         "Priorities",
		AgreeAgents:      agents[:mid],
		DisagreeAgents:   agents[mid:],
		NoClearPosition:  []string{},
		ControversyScore: 1.0,
		Explanation:      "Agents are divided on whether safety or capability should be the primary focus.",
	}

	return &deliberation.AnalysisResult{
		Clusters:            []deliberation.OpinionCluster{cluster1, cluster2},
		Cruxes:              []deliberation.Crux{crux},
		ConsensusStatements: []deliberation.ConsensusStatement{},
		TopicSummaries: []deliberation.TopicSummary{
			{Topic: "Strategy", Summary: "Agents discussed priorities for the approach."},
		},
		AgentCount:    len(agents),
		PositionCount: len(positions),
		VoteCount:     len(votes),
	}, nil
}

func newTestService(t *testing.T) (*deliberation.Service, *store.DB) {
	t.Helper()
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})
	return svc, db
}

func TestFullDeliberationLoop(t *testing.T) {
	svc, _ := newTestService(t)

	// Step 1: Create deliberation
	d, err := svc.CreateDeliberation("AI Governance", "How should we govern AI development?")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == "" || d.Status != "open" || d.Round != 1 {
		t.Fatalf("unexpected deliberation state: %+v", d)
	}

	// Step 2: Submit positions from 4 agents
	agents := []string{"alice", "bob", "carol", "dave"}
	contents := []string{
		"We need strong international regulation with binding treaties.",
		"Self-regulation by AI labs is sufficient if we maintain transparency.",
		"A moratorium on frontier AI development is necessary until we understand the risks.",
		"Market forces and competition will naturally drive safety improvements.",
	}

	var positionIDs []string
	for i, agent := range agents {
		p, err := svc.SubmitPosition(d.ID, agent, contents[i])
		if err != nil {
			t.Fatalf("submitting position for %s: %v", agent, err)
		}
		positionIDs = append(positionIDs, p.ID)
	}

	// Step 3: Verify positions
	positions, err := svc.GetPositions(d.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 4 {
		t.Fatalf("expected 4 positions, got %d", len(positions))
	}

	// Test exclude_agent_id filter
	excludeAlice := "alice"
	filtered, err := svc.GetPositions(d.ID, &excludeAlice, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 3 {
		t.Fatalf("expected 3 positions (excluding alice), got %d", len(filtered))
	}

	// Step 4: Vote
	// Alice agrees with Bob, disagrees with Dave
	if err := svc.Vote(d.ID, "alice", positionIDs[1], 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.Vote(d.ID, "alice", positionIDs[3], -1); err != nil {
		t.Fatal(err)
	}
	// Bob agrees with Dave, disagrees with Carol
	if err := svc.Vote(d.ID, "bob", positionIDs[3], 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.Vote(d.ID, "bob", positionIDs[2], -1); err != nil {
		t.Fatal(err)
	}

	// Step 5: Analyze
	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentCount != 4 {
		t.Fatalf("expected 4 agents, got %d", result.AgentCount)
	}
	if result.PositionCount != 4 {
		t.Fatalf("expected 4 positions, got %d", result.PositionCount)
	}
	if len(result.Cruxes) != 1 {
		t.Fatalf("expected 1 crux, got %d", len(result.Cruxes))
	}
	if len(result.Clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(result.Clusters))
	}

	// Verify round advanced
	d2, _ := svc.GetDeliberation(d.ID)
	if d2.Round != 2 {
		t.Fatalf("expected round 2 after analysis, got %d", d2.Round)
	}

	// Step 6: Get context for alice
	ctx, err := svc.GetContext(d.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.AgentID != "alice" {
		t.Fatalf("expected agent_id 'alice', got %q", ctx.AgentID)
	}
	if ctx.ClusterID == nil {
		t.Fatal("expected alice to be in a cluster")
	}
	if len(ctx.RelevantCruxes) != 1 {
		t.Fatalf("expected 1 relevant crux for alice, got %d", len(ctx.RelevantCruxes))
	}

	// Step 7: List deliberations
	list, err := svc.ListDeliberations()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 deliberation, got %d", len(list))
	}
}

func TestVoteValidation(t *testing.T) {
	svc, _ := newTestService(t)

	d, _ := svc.CreateDeliberation("Test", "")
	p, _ := svc.SubmitPosition(d.ID, "agent-1", "A position")

	// Invalid vote value
	if err := svc.Vote(d.ID, "agent-2", p.ID, 5); err == nil {
		t.Fatal("expected error for invalid vote value")
	}

	// Valid votes
	if err := svc.Vote(d.ID, "agent-2", p.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.Vote(d.ID, "agent-2", p.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := svc.Vote(d.ID, "agent-2", p.ID, -1); err != nil {
		t.Fatal(err)
	}
}

func TestClosedDeliberationRejectsInput(t *testing.T) {
	svc, db := newTestService(t)

	d, _ := svc.CreateDeliberation("Test", "")
	db.UpdateDeliberationStatus(d.ID, "closed")

	if _, err := svc.SubmitPosition(d.ID, "agent-1", "Late position"); err == nil {
		t.Fatal("expected error submitting to closed deliberation")
	}
}
