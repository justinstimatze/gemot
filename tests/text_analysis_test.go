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

	// Source position IDs should be populated
	if len(crux.SourcePositionIDs) == 0 {
		t.Fatal("expected source_position_ids to be populated")
	}

	// Source quotes should be populated (quote grounding)
	if len(crux.SourceQuotes) == 0 {
		t.Fatal("expected source_quotes to be populated for quote grounding")
	}

	// Verify source quotes have the right structure
	for _, sq := range crux.SourceQuotes {
		if sq.PositionID == "" {
			t.Fatal("source quote missing position_id")
		}
		if sq.AgentID == "" {
			t.Fatal("source quote missing agent_id")
		}
		if sq.Quote == "" {
			t.Fatal("source quote missing quote text")
		}
		if sq.ClaimText == "" {
			t.Fatal("source quote missing claim_text")
		}
	}

	// Verify the quotes trace back to actual positions
	posIDs := map[string]bool{"p1": true, "p2": true, "p3": true}
	for _, sq := range crux.SourceQuotes {
		if !posIDs[sq.PositionID] {
			t.Fatalf("source quote references unknown position %q", sq.PositionID)
		}
	}
}

// TestQuoteGroundingMultiAgentDedup verifies that when dedup consolidates
// claims from multiple agents into one group, all source quotes survive.
func TestQuoteGroundingMultiAgentDedup(t *testing.T) {
	// Custom mock where dedup merges claims 0 and 1 into one group
	mockFn := func(_ context.Context, system, prompt string, schema map[string]any, target any) error {
		switch {
		case strings.Contains(prompt, "break down the information"):
			return json.Unmarshal([]byte(`{
				"topics": [{"topic_name": "Policy", "topic_description": "Policy approaches",
					"subtopics": [{"subtopic_name": "Enforcement", "subtopic_description": "How to enforce"}]}]
			}`), target)
		case strings.Contains(prompt, "extract the most important concise claims"):
			if strings.Contains(prompt, "Participant 0") {
				return json.Unmarshal([]byte(`{"claims": [{"claim": "Strict penalties are needed", "quote": "we must impose heavy fines", "topic_name": "Policy", "subtopic_name": "Enforcement"}]}`), target)
			}
			if strings.Contains(prompt, "Participant 1") {
				return json.Unmarshal([]byte(`{"claims": [{"claim": "Punitive measures are essential", "quote": "without punishment there is no deterrence", "topic_name": "Policy", "subtopic_name": "Enforcement"}]}`), target)
			}
			if strings.Contains(prompt, "Participant 2") {
				return json.Unmarshal([]byte(`{"claims": [{"claim": "Education is better than punishment", "quote": "rewards work better than threats", "topic_name": "Policy", "subtopic_name": "Enforcement"}]}`), target)
			}
			return json.Unmarshal([]byte(`{"claims": []}`), target)
		case strings.Contains(prompt, "grouping claims"):
			// Merge claims 0 and 1 (both pro-punishment) into one group
			return json.Unmarshal([]byte(`{"groups": [
				{"claim_text": "Punitive enforcement is necessary", "original_claim_ids": [0, 1]},
				{"claim_text": "Educational approaches are more effective", "original_claim_ids": [2]}
			]}`), target)
		case strings.Contains(prompt, "maximally controversial statement"):
			return json.Unmarshal([]byte(`{
				"crux_claim": "Punishment is more effective than education for enforcement",
				"agree": ["0", "1"], "disagree": ["2"], "no_clear_position": [],
				"explanation": "Two participants advocate for punitive measures while one favors education."
			}`), target)
		case strings.Contains(prompt, "Generate a detailed summary"):
			return json.Unmarshal([]byte(`{"summary": "Debate over enforcement approaches."}`), target)
		default:
			return fmt.Errorf("unexpected prompt: %s", prompt[:min(100, len(prompt))])
		}
	}

	analyzer := analysis.NewTextAnalyzerWithFunc(mockFn)
	positions := []deliberation.Position{
		{ID: "p1", DeliberationID: "test", AgentID: "alice", Content: "we must impose heavy fines on violators", Round: 1},
		{ID: "p2", DeliberationID: "test", AgentID: "bob", Content: "without punishment there is no deterrence", Round: 1},
		{ID: "p3", DeliberationID: "test", AgentID: "carol", Content: "rewards work better than threats, education first", Round: 1},
	}

	result, err := analyzer.Analyze(context.Background(), positions, nil, []string{"alice", "bob", "carol"})
	if err != nil {
		t.Fatalf("analysis failed: %v", err)
	}
	if len(result.Cruxes) == 0 {
		t.Fatal("expected at least one crux")
	}

	crux := result.Cruxes[0]

	// Should have quotes from both alice AND bob (merged group)
	if len(crux.SourceQuotes) < 2 {
		t.Fatalf("expected at least 2 source quotes (from merged group), got %d", len(crux.SourceQuotes))
	}

	// Verify both positions are represented
	positionsSeen := map[string]bool{}
	for _, sq := range crux.SourceQuotes {
		positionsSeen[sq.PositionID] = true
	}
	if !positionsSeen["p1"] || !positionsSeen["p2"] {
		t.Fatalf("expected quotes from p1 and p2, got positions: %v", positionsSeen)
	}

	// Verify quotes are actual content, not empty
	for _, sq := range crux.SourceQuotes {
		if sq.Quote == "" {
			t.Fatalf("source quote for %s has empty quote text", sq.PositionID)
		}
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
