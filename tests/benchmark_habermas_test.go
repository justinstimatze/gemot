package tests

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/config"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
)

type habermasData struct {
	Question       string             `json:"question"`
	TotalResponses int                `json:"total_responses"`
	SampleSize     int                `json:"sample_size"`
	Positions      []habermasPosition `json:"positions"`
}

type habermasPosition struct {
	AgentID        string `json:"agent_id"`
	ParticipantID  string `json:"participant_id"`
	Content        string `json:"content"`
	ViewChange     string `json:"view_change"`
	ViewMovement   string `json:"view_movement"`
	BestExpression string `json:"best_expression"`
}

// TestHabermasBenchmark runs gemot's text pipeline on real human deliberation data
// from Google Speaker L's Habermas Machine experiment.
//
// Ground truth: which participants changed their views during deliberation.
// Hypothesis: cruxes should capture the claims that view-changers moved on.
//
// Skip with: go test -short ./tests/
func TestHabermasBenchmark(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Habermas benchmark (makes real API calls)")
	}

	cfg := config.Load()
	if cfg.AnthropicKey == "" {
		t.Skip("GEMOT_ANTHROPIC_KEY not set")
	}

	// Load test data
	f, err := os.Open("../testdata/habermas_prison.json")
	if err != nil {
		t.Skipf("Habermas data not available: %v", err)
	}
	defer f.Close()

	var data habermasData
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		t.Fatalf("parsing Habermas data: %v", err)
	}

	t.Logf("Question: %s", data.Question)
	t.Logf("Sample: %d positions (from %d total responses)", data.SampleSize, data.TotalResponses)

	// Build positions and agents
	var positions []deliberation.Position
	var agents []string
	changerSet := map[string]bool{}
	for _, hp := range data.Positions {
		positions = append(positions, deliberation.Position{
			ID:             hp.AgentID,
			DeliberationID: data.Question,
			AgentID:        hp.AgentID,
			Content:        hp.Content,
			Round:          1,
		})
		agents = append(agents, hp.AgentID)
		if hp.ViewChange == "YES" {
			changerSet[hp.AgentID] = true
		}
	}

	t.Logf("View-changers: %d/%d", len(changerSet), len(agents))

	// Run text analysis
	client := llm.NewClient(cfg.AnthropicKey, cfg.Model)
	analyzer := analysis.NewTextAnalyzer(client)

	t.Log("Running text analysis pipeline (real LLM calls)...")
	result, err := analyzer.Analyze(context.Background(), positions, nil, agents)
	if err != nil {
		t.Fatalf("analysis failed: %v", err)
	}

	t.Logf("\n=== RESULTS ===")
	t.Logf("Topics: %d, Cruxes: %d", len(result.TopicSummaries), len(result.Cruxes))

	for _, ts := range result.TopicSummaries {
		t.Logf("\nTopic: %s", ts.Topic)
		t.Logf("  %s", truncate(ts.Summary, 200))
	}

	// Validate cruxes against view-change ground truth
	t.Logf("\n=== CRUX VALIDATION ===")
	for _, crux := range result.Cruxes {
		t.Logf("\nCrux: %s", truncate(crux.Claim, 120))
		t.Logf("  Topic: %s > %s", crux.Topic, crux.Subtopic)
		t.Logf("  Controversy: %.2f", crux.ControversyScore)
		t.Logf("  Agree: %v", crux.AgreeAgents)
		t.Logf("  Disagree: %v", crux.DisagreeAgents)

		// Key metric: do cruxes involve view-changers?
		// If our crux captures a real disagreement, view-changers should appear
		// on the crux (they're the ones whose position was movable).
		agreeChangers, disagreeChangers := 0, 0
		for _, a := range crux.AgreeAgents {
			if changerSet[a] {
				agreeChangers++
			}
		}
		for _, a := range crux.DisagreeAgents {
			if changerSet[a] {
				disagreeChangers++
			}
		}
		totalInvolved := len(crux.AgreeAgents) + len(crux.DisagreeAgents)
		changersInvolved := agreeChangers + disagreeChangers
		if totalInvolved > 0 {
			changerRate := float64(changersInvolved) / float64(totalInvolved)
			baseRate := float64(len(changerSet)) / float64(len(agents))
			t.Logf("  View-changers involved: %d/%d (%.0f%% vs base rate %.0f%%)",
				changersInvolved, totalInvolved, changerRate*100, baseRate*100)
			if changerRate > baseRate {
				t.Logf("  SIGNAL: crux over-represents view-changers (good — captures movable positions)")
			}
		}
	}

	// Overall: did any crux involve a view-changer?
	anyChangerInCrux := false
	for _, crux := range result.Cruxes {
		for _, a := range append(crux.AgreeAgents, crux.DisagreeAgents...) {
			if changerSet[a] {
				anyChangerInCrux = true
			}
		}
	}
	if len(result.Cruxes) > 0 {
		t.Logf("\n=== SUMMARY ===")
		t.Logf("Cruxes involve view-changers: %v", anyChangerInCrux)
		t.Logf("(View-changers are people whose minds were changed during the Habermas Machine deliberation)")
	}
}
