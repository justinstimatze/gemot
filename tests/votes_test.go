package tests

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

func makePositions(n int, deliberationID string) []deliberation.Position {
	positions := make([]deliberation.Position, n)
	for i := range positions {
		positions[i] = deliberation.Position{
			ID:             fmt.Sprintf("pos-%d", i),
			DeliberationID: deliberationID,
			AgentID:        fmt.Sprintf("agent-%d", i%5),
			Content:        fmt.Sprintf("Position %d", i),
			Round:          1,
		}
	}
	return positions
}

func makeAgents(n int) []string {
	agents := make([]string, n)
	for i := range agents {
		agents[i] = fmt.Sprintf("agent-%d", i)
	}
	return agents
}

func TestVoteAnalyzerInsufficientData(t *testing.T) {
	va := analysis.NewVoteAnalyzer()

	// Too few agents (default MinAgents is now 10)
	positions := makePositions(5, "d1")
	agents := makeAgents(3)
	result := va.Analyze(context.Background(), positions, nil, agents)
	if result != nil {
		t.Fatal("expected nil result with too few agents")
	}

	// Too few positions
	agents = makeAgents(11)
	positions = makePositions(2, "d1")
	result = va.Analyze(context.Background(), positions, nil, agents)
	if result != nil {
		t.Fatal("expected nil result with too few positions")
	}
}

func TestVoteAnalyzerClustering(t *testing.T) {
	va := analysis.NewVoteAnalyzer()
	va.MinAgents = 4
	va.MinPositions = 3
	va.MinCoverage = 0.3

	agents := makeAgents(6)
	positions := makePositions(4, "d1")

	// Create two clear voting blocs:
	// agents 0,1,2 agree with positions 0,1 and disagree with 2,3
	// agents 3,4,5 disagree with positions 0,1 and agree with 2,3
	var votes []deliberation.Vote
	voteID := 0
	for _, ai := range []int{0, 1, 2} {
		for _, pi := range []int{0, 1} {
			votes = append(votes, deliberation.Vote{
				ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
				AgentID: agents[ai], PositionID: positions[pi].ID, Value: 1,
			})
			voteID++
		}
		for _, pi := range []int{2, 3} {
			votes = append(votes, deliberation.Vote{
				ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
				AgentID: agents[ai], PositionID: positions[pi].ID, Value: -1,
			})
			voteID++
		}
	}
	for _, ai := range []int{3, 4, 5} {
		for _, pi := range []int{0, 1} {
			votes = append(votes, deliberation.Vote{
				ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
				AgentID: agents[ai], PositionID: positions[pi].ID, Value: -1,
			})
			voteID++
		}
		for _, pi := range []int{2, 3} {
			votes = append(votes, deliberation.Vote{
				ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
				AgentID: agents[ai], PositionID: positions[pi].ID, Value: 1,
			})
			voteID++
		}
	}

	result := va.Analyze(context.Background(), positions, votes, agents)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if len(result.Clusters) < 2 {
		t.Fatalf("expected at least 2 clusters, got %d", len(result.Clusters))
	}

	// Verify agents are separated into opposing clusters
	cluster0Agents := map[string]bool{}
	for _, a := range result.Clusters[0].AgentIDs {
		cluster0Agents[a] = true
	}

	// Agents 0,1,2 should be in the same cluster, agents 3,4,5 in another
	sameCluster012 := cluster0Agents["agent-0"] == cluster0Agents["agent-1"] && cluster0Agents["agent-1"] == cluster0Agents["agent-2"]
	sameCluster345 := cluster0Agents["agent-3"] == cluster0Agents["agent-4"] && cluster0Agents["agent-4"] == cluster0Agents["agent-5"]

	if !sameCluster012 || !sameCluster345 {
		t.Fatalf("agents not properly separated into clusters: %+v", result.Clusters)
	}

	// Verify PCA coords exist for all agents
	if len(result.PCACoords) != 6 {
		t.Fatalf("expected 6 PCA coord entries, got %d", len(result.PCACoords))
	}
}

func TestVoteAnalyzerConsensus(t *testing.T) {
	va := analysis.NewVoteAnalyzer()
	va.MinAgents = 4
	va.MinPositions = 3
	va.MinCoverage = 0.3

	agents := makeAgents(6)
	positions := makePositions(4, "d1")

	// Position 0: everyone agrees (consensus)
	// Position 1: split (not consensus)
	// Position 2: everyone agrees
	// Position 3: mostly agrees
	var votes []deliberation.Vote
	voteID := 0
	for _, ai := range []int{0, 1, 2, 3, 4, 5} {
		// Everyone agrees with position 0
		votes = append(votes, deliberation.Vote{
			ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
			AgentID: agents[ai], PositionID: positions[0].ID, Value: 1,
		})
		voteID++
		// Everyone agrees with position 2
		votes = append(votes, deliberation.Vote{
			ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
			AgentID: agents[ai], PositionID: positions[2].ID, Value: 1,
		})
		voteID++
	}
	// Position 1: split
	for _, ai := range []int{0, 1, 2} {
		votes = append(votes, deliberation.Vote{
			ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
			AgentID: agents[ai], PositionID: positions[1].ID, Value: 1,
		})
		voteID++
	}
	for _, ai := range []int{3, 4, 5} {
		votes = append(votes, deliberation.Vote{
			ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
			AgentID: agents[ai], PositionID: positions[1].ID, Value: -1,
		})
		voteID++
	}

	result := va.Analyze(context.Background(), positions, votes, agents)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Positions 0 and 2 should be consensus
	consensusIDs := map[string]bool{}
	for _, c := range result.Consensus {
		consensusIDs[c.PositionID] = true
	}

	if !consensusIDs[positions[0].ID] {
		t.Fatal("expected position 0 to be consensus")
	}
	if !consensusIDs[positions[2].ID] {
		t.Fatal("expected position 2 to be consensus")
	}
	// Position 1 should NOT be consensus (50/50 split)
	if consensusIDs[positions[1].ID] {
		t.Fatal("expected position 1 NOT to be consensus")
	}
}

func TestVoteAnalyzerRepness(t *testing.T) {
	va := analysis.NewVoteAnalyzer()
	va.MinAgents = 4
	va.MinPositions = 3
	va.MinCoverage = 0.3

	agents := makeAgents(6)
	positions := makePositions(4, "d1")

	// Two blocs with clearly different position preferences
	var votes []deliberation.Vote
	voteID := 0
	// Bloc A (agents 0,1,2): love position 0, hate position 2
	for _, ai := range []int{0, 1, 2} {
		votes = append(votes, deliberation.Vote{
			ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
			AgentID: agents[ai], PositionID: positions[0].ID, Value: 1,
		})
		voteID++
		votes = append(votes, deliberation.Vote{
			ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
			AgentID: agents[ai], PositionID: positions[2].ID, Value: -1,
		})
		voteID++
		// Neutral on 1, 3
		votes = append(votes, deliberation.Vote{
			ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
			AgentID: agents[ai], PositionID: positions[1].ID, Value: 0,
		})
		voteID++
		votes = append(votes, deliberation.Vote{
			ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
			AgentID: agents[ai], PositionID: positions[3].ID, Value: 0,
		})
		voteID++
	}
	// Bloc B (agents 3,4,5): love position 2, hate position 0
	for _, ai := range []int{3, 4, 5} {
		votes = append(votes, deliberation.Vote{
			ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
			AgentID: agents[ai], PositionID: positions[0].ID, Value: -1,
		})
		voteID++
		votes = append(votes, deliberation.Vote{
			ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
			AgentID: agents[ai], PositionID: positions[2].ID, Value: 1,
		})
		voteID++
		votes = append(votes, deliberation.Vote{
			ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
			AgentID: agents[ai], PositionID: positions[1].ID, Value: 0,
		})
		voteID++
		votes = append(votes, deliberation.Vote{
			ID: fmt.Sprintf("v-%d", voteID), DeliberationID: "d1",
			AgentID: agents[ai], PositionID: positions[3].ID, Value: 0,
		})
		voteID++
	}

	result := va.Analyze(context.Background(), positions, votes, agents)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Each cluster should have representative positions
	if len(result.Repness) == 0 {
		t.Fatal("expected repness results")
	}

	// At least one cluster should have repness entries
	hasReps := false
	for _, reps := range result.Repness {
		if len(reps) > 0 {
			hasReps = true
			// Scores should be non-zero
			for _, r := range reps {
				if math.Abs(r.Score) < 0.01 {
					t.Fatalf("expected non-trivial repness score, got %f", r.Score)
				}
			}
		}
	}
	if !hasReps {
		t.Fatal("expected at least one cluster to have representative positions")
	}
}
