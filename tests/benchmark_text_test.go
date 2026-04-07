package tests

import (
	"context"
	"math"
	"sort"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/config"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
)

// TestTextPipelineBenchmark runs the full LLM text analysis pipeline on a sample
// of high-engagement Polis positions and validates cruxes against actual vote data.
//
// This test makes real API calls and costs ~$0.10-0.30 per run.
// Skip with: go test -short ./tests/
func TestTextPipelineBenchmark(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping text pipeline benchmark (makes real API calls)")
	}

	cfg := config.Load()
	if cfg.AnthropicKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	// Load Polis data
	positions, votes, agents := loadPolisData(t)
	t.Logf("Full dataset: %d positions, %d votes, %d agents", len(positions), len(votes), len(agents))

	// Select top 20 positions by total engagement (agrees + disagrees)
	type posEngagement struct {
		Position deliberation.Position
		Total    int
	}

	// Count votes per position
	voteCounts := map[string]int{}
	for _, v := range votes {
		if v.Value != 0 { // only count agree/disagree, not pass
			voteCounts[v.PositionID]++
		}
	}

	var ranked []posEngagement
	for _, p := range positions {
		ranked = append(ranked, posEngagement{Position: p, Total: voteCounts[p.ID]})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].Total > ranked[j].Total })

	sampleSize := 20
	if len(ranked) < sampleSize {
		sampleSize = len(ranked)
	}
	sample := make([]deliberation.Position, sampleSize)
	for i := 0; i < sampleSize; i++ {
		sample[i] = ranked[i].Position
		sample[i].DeliberationID = "NZ Biodiversity Strategy"
	}

	t.Logf("Sample: %d positions (top by engagement)", sampleSize)
	for i, p := range sample {
		t.Logf("  %d. [%d votes] %s", i+1, voteCounts[p.ID], truncate(p.Content, 80))
	}

	// Assign synthetic agent IDs (one per unique author in sample)
	authorSet := map[string]bool{}
	for _, p := range sample {
		authorSet[p.AgentID] = true
	}
	sampleAgents := make([]string, 0, len(authorSet))
	for a := range authorSet {
		sampleAgents = append(sampleAgents, a)
	}

	// Run text analysis pipeline
	client := llm.NewClient(cfg.AnthropicKey, cfg.Model)
	analyzer := analysis.NewTextAnalyzer(client)

	t.Log("Running text analysis pipeline (real LLM calls)...")
	result, err := analyzer.Analyze(context.Background(), sample, nil, sampleAgents)
	if err != nil {
		t.Fatalf("text analysis failed: %v", err)
	}

	t.Logf("Results: %d cruxes, %d topic summaries, %d clusters",
		len(result.Cruxes), len(result.TopicSummaries), len(result.Clusters))

	// Report topic summaries
	for _, ts := range result.TopicSummaries {
		t.Logf("Topic: %s", ts.Topic)
		t.Logf("  Summary: %s", truncate(ts.Summary, 200))
	}

	// Report and validate cruxes against actual vote data
	t.Log("\nCrux validation against vote data:")

	// Build vote matrix for positions in our sample
	// votesByPosition[posID][agentID] = value
	votesByPosition := map[string]map[string]int{}
	for _, v := range votes {
		if _, ok := votesByPosition[v.PositionID]; !ok {
			votesByPosition[v.PositionID] = map[string]int{}
		}
		votesByPosition[v.PositionID][v.AgentID] = v.Value
	}

	validatedCruxes := 0
	for _, crux := range result.Cruxes {
		t.Logf("\nCrux: %s", crux.Claim)
		t.Logf("  Topic: %s > %s", crux.Topic, crux.Subtopic)
		t.Logf("  Controversy: %.2f", crux.ControversyScore)
		t.Logf("  Agree: %v", crux.AgreeAgents)
		t.Logf("  Disagree: %v", crux.DisagreeAgents)
		t.Logf("  Explanation: %s", truncate(crux.Explanation, 200))

		// Cross-reference: for each agent in agree/disagree groups,
		// check if their actual voting patterns on the sample positions
		// correlate with the crux assignment.
		// If the crux is real, agree-agents should have correlated votes
		// and disagree-agents should vote oppositely.
		if len(crux.AgreeAgents) > 0 && len(crux.DisagreeAgents) > 0 {
			agreeVoteSimilarity := meanPairwiseSimilarity(crux.AgreeAgents, sample, votesByPosition)
			disagreeVoteSimilarity := meanPairwiseSimilarity(crux.DisagreeAgents, sample, votesByPosition)
			crossGroupSimilarity := crossGroupVoteSimilarity(crux.AgreeAgents, crux.DisagreeAgents, sample, votesByPosition)

			t.Logf("  Vote correlation: agree_group=%.2f, disagree_group=%.2f, cross_group=%.2f",
				agreeVoteSimilarity, disagreeVoteSimilarity, crossGroupSimilarity)

			// A good crux: intra-group similarity > cross-group similarity
			if agreeVoteSimilarity > crossGroupSimilarity || disagreeVoteSimilarity > crossGroupSimilarity {
				t.Logf("  VALIDATED: intra-group cohesion > cross-group")
				validatedCruxes++
			} else {
				t.Logf("  WEAK: cross-group similarity is not lower than intra-group")
			}
		}
	}

	if len(result.Cruxes) > 0 {
		validationRate := float64(validatedCruxes) / float64(len(result.Cruxes))
		t.Logf("\nValidation rate: %d/%d cruxes (%.0f%%) confirmed by vote data",
			validatedCruxes, len(result.Cruxes), validationRate*100)
	}
}

// meanPairwiseSimilarity computes mean cosine similarity of voting patterns
// between all pairs of agents in the group.
func meanPairwiseSimilarity(agents []string, positions []deliberation.Position, votesByPosition map[string]map[string]int) float64 {
	if len(agents) < 2 {
		return 0
	}

	// Build vote vectors per agent
	vectors := map[string][]float64{}
	for _, agent := range agents {
		vec := make([]float64, len(positions))
		for j, p := range positions {
			if agentVotes, ok := votesByPosition[p.ID]; ok {
				if v, ok := agentVotes[agent]; ok {
					vec[j] = float64(v)
				}
			}
		}
		vectors[agent] = vec
	}

	totalSim := 0.0
	pairs := 0
	for i := 0; i < len(agents); i++ {
		for j := i + 1; j < len(agents); j++ {
			sim := cosineSimilarity(vectors[agents[i]], vectors[agents[j]])
			if !math.IsNaN(sim) {
				totalSim += sim
				pairs++
			}
		}
	}
	if pairs == 0 {
		return 0
	}
	return totalSim / float64(pairs)
}

// crossGroupVoteSimilarity computes mean cosine similarity between agents
// in different groups (should be low for a good crux).
func crossGroupVoteSimilarity(groupA, groupB []string, positions []deliberation.Position, votesByPosition map[string]map[string]int) float64 {
	vectors := map[string][]float64{}
	for _, agent := range append(groupA, groupB...) {
		vec := make([]float64, len(positions))
		for j, p := range positions {
			if agentVotes, ok := votesByPosition[p.ID]; ok {
				if v, ok := agentVotes[agent]; ok {
					vec[j] = float64(v)
				}
			}
		}
		vectors[agent] = vec
	}

	totalSim := 0.0
	pairs := 0
	for _, a := range groupA {
		for _, b := range groupB {
			sim := cosineSimilarity(vectors[a], vectors[b])
			if !math.IsNaN(sim) {
				totalSim += sim
				pairs++
			}
		}
	}
	if pairs == 0 {
		return 0
	}
	return totalSim / float64(pairs)
}

func cosineSimilarity(a, b []float64) float64 {
	dot, magA, magB := 0.0, 0.0, 0.0
	for i := range a {
		dot += a[i] * b[i]
		magA += a[i] * a[i]
		magB += b[i] * b[i]
	}
	if magA == 0 || magB == 0 {
		return 0
	}
	return dot / (math.Sqrt(magA) * math.Sqrt(magB))
}
