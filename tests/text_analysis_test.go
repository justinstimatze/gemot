package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
)

// mockLLM returns canned responses based on prompt content.
func mockLLM() llm.StructuredOutputFunc {
	callCount := 0
	return func(_ context.Context, system, prompt string, schema map[string]any, target any) error {
		callCount++

		// Detect which prompt type based on content
		switch {
		case strings.Contains(prompt, "break down the information"):
			// Taxonomy prompt
			return json.Unmarshal([]byte(`{
				"topics": [
					{
						"topic_name": "Regulation Approach",
						"topic_description": "Different approaches to regulating AI development and deployment.",
						"subtopics": [
							{
								"subtopic_name": "Government vs Self-Regulation",
								"subtopic_description": "Whether AI should be regulated by governments through legislation and treaties, or through voluntary industry self-regulation with transparency requirements."
							},
							{
								"subtopic_name": "Speed of Action",
								"subtopic_description": "The urgency of regulatory action, ranging from immediate moratoriums to gradual phased approaches that allow the field to develop."
							}
						]
					}
				]
			}`), target)

		case strings.Contains(prompt, "extract the most important concise claims"):
			// Claim extraction prompt - vary by participant
			if strings.Contains(prompt, "Participant 0") {
				return json.Unmarshal([]byte(`{
					"claims": [
						{
							"claim": "International binding treaties are necessary to prevent regulatory arbitrage",
							"quote": "No single nation can regulate AI effectively because labs will relocate",
							"topic_name": "Regulation Approach",
							"subtopic_name": "Government vs Self-Regulation"
						}
					]
				}`), target)
			}
			if strings.Contains(prompt, "Participant 1") {
				return json.Unmarshal([]byte(`{
					"claims": [
						{
							"claim": "Government regulators lack technical expertise to write effective AI rules",
							"quote": "Government regulators lack the technical expertise to write good rules",
							"topic_name": "Regulation Approach",
							"subtopic_name": "Government vs Self-Regulation"
						}
					]
				}`), target)
			}
			if strings.Contains(prompt, "Participant 2") {
				return json.Unmarshal([]byte(`{
					"claims": [
						{
							"claim": "Frontier AI development should be paused until safety evaluations catch up",
							"quote": "moratorium on frontier model training above a compute threshold",
							"topic_name": "Regulation Approach",
							"subtopic_name": "Speed of Action"
						}
					]
				}`), target)
			}
			// Default: no claims
			return json.Unmarshal([]byte(`{"claims": []}`), target)

		case strings.Contains(prompt, "grouping claims"):
			// Dedup prompt
			return json.Unmarshal([]byte(`{
				"groups": [
					{
						"claim_text": "Government regulation is necessary because industry cannot self-regulate AI effectively",
						"original_claim_ids": [0]
					},
					{
						"claim_text": "Industry self-regulation is preferable because regulators lack technical competence",
						"original_claim_ids": [1]
					}
				]
			}`), target)

		case strings.Contains(prompt, "maximally controversial statement"):
			// Crux prompt
			return json.Unmarshal([]byte(`{
				"crux_claim": "Governments, not industry, should be the primary regulators of AI development",
				"agree": ["0"],
				"disagree": ["1"],
				"no_clear_position": [],
				"explanation": "One group believes that international government regulation is essential to prevent a race to the bottom, while the other argues that the technical complexity of AI makes industry self-regulation more effective."
			}`), target)

		case strings.Contains(prompt, "Generate a detailed summary"):
			// Summary prompt
			return json.Unmarshal([]byte(`{
				"summary": "Participants are divided on the optimal approach to AI regulation. Some advocate for international binding treaties with enforcement mechanisms, arguing that without coordination, AI labs will simply relocate to permissive jurisdictions. Others contend that government regulators lack the technical expertise to write effective rules, preferring industry self-regulation with transparency requirements. A third perspective calls for immediate moratoriums on frontier AI development, viewing both treaties and self-regulation as insufficient given the urgency of the risks."
			}`), target)

		default:
			return fmt.Errorf("unexpected prompt: %s", prompt[:min(100, len(prompt))])
		}
	}
}

func TestTextAnalyzerFullPipeline(t *testing.T) {
	analyzer := analysis.NewTextAnalyzerWithFunc(mockLLM())

	agents := []string{"alice", "bob", "carol"}
	positions := []deliberation.Position{
		{ID: "p1", DeliberationID: "AI Governance", AgentID: "alice", Content: "International binding treaties are essential. No single nation can regulate AI effectively because labs will relocate to permissive jurisdictions.", Round: 1},
		{ID: "p2", DeliberationID: "AI Governance", AgentID: "bob", Content: "Industry self-regulation with mandatory transparency is the right approach. Government regulators lack the technical expertise to write good rules.", Round: 1},
		{ID: "p3", DeliberationID: "AI Governance", AgentID: "carol", Content: "We need a moratorium on frontier model training above a compute threshold until safety evaluations catch up.", Round: 1},
	}

	result, err := analyzer.Analyze(context.Background(), positions, nil, agents)
	if err != nil {
		t.Fatalf("analysis failed: %v", err)
	}

	if result.AgentCount != 3 {
		t.Fatalf("expected 3 agents, got %d", result.AgentCount)
	}
	if result.PositionCount != 3 {
		t.Fatalf("expected 3 positions, got %d", result.PositionCount)
	}

	// Should have at least one crux
	if len(result.Cruxes) == 0 {
		t.Fatal("expected at least one crux")
	}

	// Crux should have the claim from mock
	crux := result.Cruxes[0]
	if crux.Claim != "Governments, not industry, should be the primary regulators of AI development" {
		t.Fatalf("unexpected crux claim: %q", crux.Claim)
	}

	// Verify de-anonymization: "0" -> "alice", "1" -> "bob"
	if len(crux.AgreeAgents) != 1 || crux.AgreeAgents[0] != "alice" {
		t.Fatalf("expected alice to agree, got %v", crux.AgreeAgents)
	}
	if len(crux.DisagreeAgents) != 1 || crux.DisagreeAgents[0] != "bob" {
		t.Fatalf("expected bob to disagree, got %v", crux.DisagreeAgents)
	}

	// Should have topic summaries
	if len(result.TopicSummaries) == 0 {
		t.Fatal("expected at least one topic summary")
	}

	// Controversy score should be calculated
	if crux.ControversyScore <= 0 {
		t.Fatalf("expected positive controversy score, got %f", crux.ControversyScore)
	}
}

func TestTextAnalyzerEmptyPositions(t *testing.T) {
	analyzer := analysis.NewTextAnalyzerWithFunc(mockLLM())

	result, err := analyzer.Analyze(context.Background(), nil, nil, []string{"agent-1"})
	if err != nil {
		t.Fatalf("expected no error for empty positions, got: %v", err)
	}
	if result.PositionCount != 0 {
		t.Fatalf("expected 0 positions, got %d", result.PositionCount)
	}
}
