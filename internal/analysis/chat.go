package analysis

import (
	"context"
	"fmt"
	"strings"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
)

// ChatAnalyzer is the deliberately unstructured control for measuring what
// gemot's structured pipeline (claim extraction, clustering, crux detection,
// synthesis) actually adds over plain multi-agent discussion. It bypasses all
// of that: Analyze makes a single LLM pass over the raw positions to produce a
// plain summary, and the compromise methods make a single pass to state a
// decision. No cruxes, no clusters, no Polis vote math — three agents in a
// group chat, nothing more.
//
// It is selected by config (GEMOT_ANALYZER=chat) so that a structured-vs-chat
// A/B is a one-variable swap with model, positions, and votes held identical.
// It satisfies the same deliberation.Analyzer, CompromiseGenerator, and
// ChoiceCompromiseGenerator interfaces as the Synthesizer, so it is a drop-in.
type ChatAnalyzer struct {
	structuredOutput llm.StructuredOutputFunc
}

// NewChatAnalyzer builds a ChatAnalyzer backed by a real LLM client.
func NewChatAnalyzer(client *llm.Client) *ChatAnalyzer {
	return &ChatAnalyzer{structuredOutput: client.StructuredOutput}
}

// NewChatAnalyzerWithFunc builds a ChatAnalyzer with a custom structured-output
// function (for testing).
func NewChatAnalyzerWithFunc(fn llm.StructuredOutputFunc) *ChatAnalyzer {
	return &ChatAnalyzer{structuredOutput: fn}
}

// positionsTranscript renders positions as a plain "agent: content" discussion.
func positionsTranscript(positions []deliberation.Position) string {
	var b strings.Builder
	for _, p := range positions {
		fmt.Fprintf(&b, "%s: %s\n\n", p.AgentID, p.Content)
	}
	return strings.TrimSpace(b.String())
}

// claimsTranscript reconstructs the discussion from the positions preserved as
// ExtractedClaims in a prior Analyze result — the compromise methods run after
// Analyze and only receive the persisted result, not the raw positions.
func claimsTranscript(result *deliberation.AnalysisResult) string {
	var b strings.Builder
	for _, c := range result.ExtractedClaims {
		fmt.Fprintf(&b, "%s: %s\n\n", c.AgentID, c.Claim)
	}
	return strings.TrimSpace(b.String())
}

// Analyze reads the discussion once and returns a minimal result: counts, a
// one-paragraph summary as the sole consensus statement, and the raw positions
// preserved as ExtractedClaims so the compromise methods can reconstruct the
// transcript from the persisted result. It intentionally produces no cruxes,
// clusters, or bridging statements — that absence is the whole point.
func (a *ChatAnalyzer) Analyze(ctx context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	result := &deliberation.AnalysisResult{
		AgentCount:    len(agents),
		PositionCount: len(positions),
		VoteCount:     len(votes),
		Confidence:    "unstructured",
	}
	if len(positions) == 0 {
		return result, nil
	}

	var out struct {
		Summary string `json:"summary"`
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
		},
		"required": []string{"summary"},
	}
	system := "You are summarizing an unstructured discussion among AI agents. Report plainly what was said and where the group appears to agree or disagree. Do not impose structure, procedure, or scoring."
	prompt := "Discussion:\n\n" + positionsTranscript(positions) + "\n\nSummarize the discussion in one short paragraph."
	if err := a.structuredOutput(ctx, system, prompt, schema, &out); err != nil {
		return nil, fmt.Errorf("chat analyze: %w", err)
	}

	result.ConsensusStatements = []deliberation.ConsensusStatement{{Content: out.Summary}}
	for _, p := range positions {
		result.ExtractedClaims = append(result.ExtractedClaims, deliberation.ExtractedClaim{
			AgentID:    p.AgentID,
			PositionID: p.ID,
			Claim:      p.Content,
		})
	}
	return result, nil
}

// GenerateCompromise makes a single unstructured pass: given the topic and the
// discussion (reconstructed from the preserved positions), it states the group's
// decision. This is the free-text-chat counterpart to the Synthesizer — no
// cruxes or clusters inform it, only the raw conversation.
func (a *ChatAnalyzer) GenerateCompromise(ctx context.Context, topic string, result *deliberation.AnalysisResult) (string, error) {
	var out struct {
		Decision string `json:"decision"`
	}
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"decision": map[string]any{"type": "string"}},
		"required":   []string{"decision"},
	}
	system := "You are concluding an unstructured discussion. State the single decision the group should adopt and a one-sentence reason. Impose no procedure."
	prompt := fmt.Sprintf("Topic: %s\n\nDiscussion:\n\n%s\n\nState the group's decision.", topic, claimsTranscript(result))
	if err := a.structuredOutput(ctx, system, prompt, schema, &out); err != nil {
		return "", fmt.Errorf("chat compromise: %w", err)
	}
	return out.Decision, nil
}

// GenerateCompromiseWithChoice mirrors the Synthesizer's forced-choice variant
// so ChatAnalyzer is a full drop-in. With no options it defers to
// GenerateCompromise (free synthesis); with options it picks one verbatim from
// the raw discussion, with none of the structured analysis a Synthesizer uses.
func (a *ChatAnalyzer) GenerateCompromiseWithChoice(ctx context.Context, topic string, result *deliberation.AnalysisResult, options []string, optionVotes map[string]int) (string, string, error) {
	if len(options) == 0 {
		s, err := a.GenerateCompromise(ctx, topic, result)
		return s, "", err
	}
	var out struct {
		Decision string `json:"decision"`
		Selected string `json:"selected_option"`
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"decision":        map[string]any{"type": "string"},
			"selected_option": map[string]any{"type": "string", "enum": options},
		},
		"required": []string{"decision", "selected_option"},
	}
	var opts strings.Builder
	for _, o := range options {
		fmt.Fprintf(&opts, "  - %s (chosen by %d agent(s))\n", o, optionVotes[o])
	}
	system := "You are concluding an unstructured discussion by selecting one option. Impose no procedure; choose based only on what was said."
	prompt := fmt.Sprintf("Topic: %s\n\nDiscussion:\n\n%s\n\nSelect exactly one option verbatim:\n%s", topic, claimsTranscript(result), opts.String())
	if err := a.structuredOutput(ctx, system, prompt, schema, &out); err != nil {
		return "", "", fmt.Errorf("chat compromise (choice): %w", err)
	}
	return out.Decision, out.Selected, nil
}
