package calibration

import (
	"context"
	"fmt"

	"github.com/justinstimatze/gemot/internal/llm"
)

// SoloPrompt is the instruction template used by the single-agent baseline.
// It mirrors what a fleet participant sees when they submit a position on a
// calibration corpus question: same framing, same options, same answer
// space. The only difference is the absence of any deliberation context —
// no other positions, no votes, no compromise generation. This isolates
// the bottleneck the design note targets ("Claude exercising judgement in
// choosing goals") to a single-call comparison.
const SoloPrompt = `You are answering a direction-judgment question. Consider the options carefully and select exactly one.

Question:
%s

Options:
%s

Pick the option you believe is most correct. Provide brief reasoning in the rationale field.`

const soloSystemPrompt = "You are a careful judgment-making assistant. When asked to choose among options, weigh each option's merits and select the single best answer."

// SoloCoTPrompt is the chain-of-thought variant used to validate the bare
// SoloPrompt baseline. Literature reports Sonnet 4.6 ~70% on GPQA Diamond
// with proper CoT; the shipped v2 baseline used bare prompting and got 32%.
// If SoloCoT lands materially above the shipped baseline, the published
// fleet-vs-solo lift is misleading and needs revision.
const SoloCoTPrompt = `You are answering a graduate-level science question. Think carefully before answering.

Question:
%s

Options:
%s

Reason step-by-step:
1. Identify what the question is actually asking.
2. List the relevant principles, formulas, or facts.
3. Work through the analysis explicitly, including any numerical computation.
4. Evaluate each option against your analysis.
5. State your final answer.

Put the full step-by-step reasoning in the rationale field, then set selected_option to your final answer (verbatim, one of the listed options).`

const soloCoTSystemPrompt = "You are an expert solving a multiple-choice graduate-level science problem. Always reason step-by-step before committing to an answer. Show your work in the rationale; never guess."

// Solo runs the single-agent baseline for one calibration question. Returns
// the verbatim option string the LLM selected (one of `options`). Uses the
// LLM client's structured-output path with a JSON-schema enum constraint, so
// extraction is near-deterministic at the tool_use layer — same reliability
// guarantee as GenerateCompromiseWithChoice.
//
// Cost tracking flows through llm.Client.OnUsage automatically when the
// runner passes a context with a deliberation_id key set; calibration runs
// tag their solo calls under a "_calibration_solo" namespace so the
// tracker can separate them from fleet costs.
func Solo(ctx context.Context, client *llm.Client, question string, options []string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("solo: nil llm client")
	}
	if len(options) == 0 {
		return "", fmt.Errorf("solo: options required")
	}

	var optionsText string
	for _, o := range options {
		optionsText += "  - " + o + "\n"
	}
	prompt := fmt.Sprintf(SoloPrompt, question, optionsText)

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selected_option": map[string]any{"type": "string", "enum": options},
			"rationale":       map[string]any{"type": "string"},
		},
		"required": []string{"selected_option", "rationale"},
	}

	var output struct {
		SelectedOption string `json:"selected_option"`
		Rationale      string `json:"rationale"`
	}
	if err := client.StructuredOutput(ctx, soloSystemPrompt, prompt, schema, &output); err != nil {
		return "", fmt.Errorf("solo structured output: %w", err)
	}
	return output.SelectedOption, nil
}

// SoloCoT is the chain-of-thought baseline. Same enum-constrained schema as
// Solo, but the prompt and system message explicitly elicit step-by-step
// reasoning before the final answer. Used by `gemot calibration
// validate-solo` to test whether the shipped 32% solo rate is an artifact
// of bare prompting.
func SoloCoT(ctx context.Context, client *llm.Client, question string, options []string) (string, string, error) {
	if client == nil {
		return "", "", fmt.Errorf("solo_cot: nil llm client")
	}
	if len(options) == 0 {
		return "", "", fmt.Errorf("solo_cot: options required")
	}

	var optionsText string
	for _, o := range options {
		optionsText += "  - " + o + "\n"
	}
	prompt := fmt.Sprintf(SoloCoTPrompt, question, optionsText)

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selected_option": map[string]any{"type": "string", "enum": options},
			"rationale":       map[string]any{"type": "string"},
		},
		"required": []string{"selected_option", "rationale"},
	}

	var output struct {
		SelectedOption string `json:"selected_option"`
		Rationale      string `json:"rationale"`
	}
	if err := client.StructuredOutput(ctx, soloCoTSystemPrompt, prompt, schema, &output); err != nil {
		return "", "", fmt.Errorf("solo_cot structured output: %w", err)
	}
	return output.SelectedOption, output.Rationale, nil
}
