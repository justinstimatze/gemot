package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SecondaryStructuredOutput is the minimal interface used by the
// cross-family OOD consistency check. It intentionally mirrors the
// shape of Client.StructuredOutput so callers can treat primary and
// secondary identically for re-stance-scoring sampled cruxes.
//
// The cross-family defense re-poses the same structured question to a
// model trained on a different corpus (Gemini vs. Anthropic). When the
// two families disagree on agent stance for a high-controversy crux,
// that's evidence the primary's output is an artifact of its training
// distribution rather than a genuine signal from the positions — the
// adversarial condition §3 of the DARPA-PS-26-09 abstract calls out
// ("adversarial inputs can produce stable-but-wrong outputs that
// defeat variance-based ensemble detection").
//
// Independence between frontier labs is imperfect (shared benchmark
// corpora, RLHF drift toward similar refusals) — see THREAT_MODEL row
// "Cross-family consistency — correlated-training caveat".
type SecondaryStructuredOutput interface {
	StructuredOutput(ctx context.Context, system, prompt string, schema map[string]any, target any) error
	Model() string
	Provider() string
}

// SecondaryFunc adapts a plain function to the interface. Used in
// tests so the full cross-family pipeline can be exercised without
// standing up a real Gemini endpoint.
type SecondaryFunc struct {
	Fn           StructuredOutputFunc
	ModelName    string
	ProviderName string
}

func (s SecondaryFunc) StructuredOutput(ctx context.Context, system, prompt string, schema map[string]any, target any) error {
	return s.Fn(ctx, system, prompt, schema, target)
}

func (s SecondaryFunc) Model() string    { return s.ModelName }
func (s SecondaryFunc) Provider() string { return s.ProviderName }

// NewSecondary returns a Gemini-backed secondary client. Empty apiKey
// or model yields (nil, nil) so callers can treat the feature as
// off-by-default without a separate error branch.
//
// Only Gemini is supported today; Google's pretraining mix is further
// from Anthropic's than OpenAI's is, so it's the best default for the
// cross-family independence argument. Adding another provider is a
// matter of implementing SecondaryStructuredOutput and extending this
// constructor — not a forward-compat switch.
func NewSecondary(model, apiKey string) (SecondaryStructuredOutput, error) {
	if apiKey == "" || model == "" {
		return nil, nil
	}
	return &geminiSecondary{
		model:  model,
		apiKey: apiKey,
		http:   &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// geminiSecondary calls generativelanguage.googleapis.com with the
// responseSchema structured-output knob. The JSONSchema flavour Gemini
// accepts is a subset of the schema strings produced by analysis.go —
// good enough for the classify-stance shape we use, but callers should
// keep schemas flat (no $ref).
type geminiSecondary struct {
	model  string
	apiKey string
	http   *http.Client
}

func (g *geminiSecondary) Model() string    { return g.model }
func (g *geminiSecondary) Provider() string { return "gemini" }

func (g *geminiSecondary) StructuredOutput(ctx context.Context, system, prompt string, schema map[string]any, target any) error {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", g.model, g.apiKey)

	body := map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]any{{"text": prompt}}},
		},
		"generationConfig": map[string]any{
			"responseMimeType": "application/json",
			"responseSchema":   schema,
			"temperature":      0,
		},
	}
	if system != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": system}},
		}
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("gemini marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("gemini do: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("gemini read: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("gemini status %d: %s", resp.StatusCode, truncate(string(respBody), 400))
	}

	var decoded struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return fmt.Errorf("gemini unmarshal envelope: %w", err)
	}
	if len(decoded.Candidates) == 0 || len(decoded.Candidates[0].Content.Parts) == 0 {
		return fmt.Errorf("gemini empty response")
	}
	text := decoded.Candidates[0].Content.Parts[0].Text
	if err := json.Unmarshal([]byte(text), target); err != nil {
		return fmt.Errorf("gemini unmarshal target: %w (body: %s)", err, truncate(text, 200))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
