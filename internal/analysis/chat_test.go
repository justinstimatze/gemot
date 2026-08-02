package analysis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
)

// fakeStructured returns a StructuredOutputFunc that decodes a fixed response
// map into whatever target struct the caller passes. It also records the last
// prompt so tests can assert the transcript reached the model.
func fakeStructured(resp map[string]any, lastPrompt *string) llm.StructuredOutputFunc {
	return func(ctx context.Context, system, prompt string, schema map[string]any, target any) error {
		if lastPrompt != nil {
			*lastPrompt = prompt
		}
		b, _ := json.Marshal(resp)
		return json.Unmarshal(b, target)
	}
}

func TestChatAnalyzerAnalyzeIsUnstructured(t *testing.T) {
	var prompt string
	a := NewChatAnalyzerWithFunc(fakeStructured(map[string]any{"summary": "they mostly agree on e4"}, &prompt))
	positions := []deliberation.Position{
		{ID: "p1", AgentID: "aggressor", Content: "play e4, seize the center"},
		{ID: "p2", AgentID: "defender", Content: "d4 is safer"},
	}
	votes := []deliberation.Vote{{AgentID: "defender", PositionID: "p1", Value: 1}}
	res, err := a.Analyze(context.Background(), positions, votes, []string{"aggressor", "defender"})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.PositionCount != 2 || res.AgentCount != 2 || res.VoteCount != 1 {
		t.Errorf("counts wrong: pos=%d agents=%d votes=%d", res.PositionCount, res.AgentCount, res.VoteCount)
	}
	// The whole point: no structured output.
	if len(res.Cruxes) != 0 || len(res.Clusters) != 0 || len(res.BridgingStatements) != 0 {
		t.Errorf("chat analyzer must produce no cruxes/clusters/bridging; got %d/%d/%d",
			len(res.Cruxes), len(res.Clusters), len(res.BridgingStatements))
	}
	if len(res.ConsensusStatements) != 1 || res.ConsensusStatements[0].Content != "they mostly agree on e4" {
		t.Errorf("expected the summary as the sole consensus statement, got %+v", res.ConsensusStatements)
	}
	// Positions preserved as claims so the compromise step can see them.
	if len(res.ExtractedClaims) != 2 || res.ExtractedClaims[0].AgentID != "aggressor" {
		t.Errorf("expected 2 preserved claims carrying positions, got %+v", res.ExtractedClaims)
	}
	if !strings.Contains(prompt, "seize the center") {
		t.Errorf("transcript did not reach the model; prompt = %q", prompt)
	}
}

func TestChatAnalyzerGenerateCompromise(t *testing.T) {
	a := NewChatAnalyzerWithFunc(fakeStructured(map[string]any{"decision": "play e4"}, nil))
	res := &deliberation.AnalysisResult{
		ExtractedClaims: []deliberation.ExtractedClaim{
			{AgentID: "aggressor", Claim: "e4"},
			{AgentID: "defender", Claim: "d4"},
		},
	}
	got, err := a.GenerateCompromise(context.Background(), "which move?", res)
	if err != nil {
		t.Fatalf("GenerateCompromise: %v", err)
	}
	if got != "play e4" {
		t.Errorf("decision = %q, want %q", got, "play e4")
	}
}

func TestChatAnalyzerGenerateCompromiseWithChoice(t *testing.T) {
	a := NewChatAnalyzerWithFunc(fakeStructured(map[string]any{"decision": "e4 is best", "selected_option": "e4"}, nil))
	res := &deliberation.AnalysisResult{ExtractedClaims: []deliberation.ExtractedClaim{{AgentID: "x", Claim: "e4"}}}

	// With options: returns the selected option.
	stmt, sel, err := a.GenerateCompromiseWithChoice(context.Background(), "move?", res, []string{"e4", "d4"}, map[string]int{"e4": 2})
	if err != nil {
		t.Fatalf("choice: %v", err)
	}
	if sel != "e4" || stmt != "e4 is best" {
		t.Errorf("got (%q, %q), want (e4 is best, e4)", stmt, sel)
	}

	// Empty options: falls back to free synthesis (empty selected option).
	a2 := NewChatAnalyzerWithFunc(fakeStructured(map[string]any{"decision": "synthesized plan"}, nil))
	stmt2, sel2, err := a2.GenerateCompromiseWithChoice(context.Background(), "move?", res, nil, nil)
	if err != nil {
		t.Fatalf("choice empty: %v", err)
	}
	if sel2 != "" || stmt2 != "synthesized plan" {
		t.Errorf("empty-options fallback got (%q, %q), want (synthesized plan, \"\")", stmt2, sel2)
	}
}

// TestChatAnalyzerSatisfiesInterfaces pins the drop-in contract.
func TestChatAnalyzerSatisfiesInterfaces(t *testing.T) {
	var _ deliberation.Analyzer = (*ChatAnalyzer)(nil)
	var _ deliberation.CompromiseGenerator = (*ChatAnalyzer)(nil)
	var _ deliberation.ChoiceCompromiseGenerator = (*ChatAnalyzer)(nil)
}
