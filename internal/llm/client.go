package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const (
	maxRetries     = 10
	baseRetryDelay = 2 * time.Second
)

// StructuredOutputFunc is the signature for structured LLM calls.
// Used by analysis code and can be replaced for testing.
type StructuredOutputFunc func(ctx context.Context, system, prompt string, schema map[string]any, target any) error

// UsageCallback is called after each LLM call with the request context and token counts.
type UsageCallback func(ctx context.Context, inputTokens, outputTokens int)

// ContextKeyModel overrides the model for a specific analysis run.
// If set in the context, it takes precedence over the client's default model.
type ContextKeyModel struct{}

// ContextKeyTemperature overrides the sampling temperature for a specific
// request. Anthropic's default is 1.0; calibration runs set 0 so per-call
// variance doesn't drown out the rates being measured (60→28 swings on
// the same 25 questions, 2026-06-05).
type ContextKeyTemperature struct{}

// AllowedModels is the set of models agents can request.
var AllowedModels = map[string]bool{
	"claude-sonnet-4-6": true,
	"claude-opus-4-6":   true,
	"claude-haiku-4-5":  true,
}

type Client struct {
	client  anthropic.Client
	model   string
	OnUsage UsageCallback // optional: called after each API call
}

func NewClient(apiKey, model string) *Client {
	return &Client{
		client: anthropic.NewClient(option.WithAPIKey(apiKey)),
		model:  model,
	}
}

// API semaphores: background (batch/experiment) and interactive (user-facing panels).
// Background gets 7 slots; interactive gets 3 reserved + can use background slots.
// Total concurrent calls stays at 10; interactive work won't starve behind batch jobs.
var bgSemaphore = make(chan struct{}, 7)
var interactiveSemaphore = make(chan struct{}, 3)

// ContextKeyInteractive marks a request as user-facing (expert panel, sandbox).
type ContextKeyInteractive struct{}

// acquireSemaphore blocks until a slot is available. Interactive callers try their
// reserved pool first, then fall back to the background pool. Background callers
// only use the background pool.
func acquireSemaphore(ctx context.Context) (release func(), err error) {
	if interactive, _ := ctx.Value(ContextKeyInteractive{}).(bool); interactive {
		// Try interactive pool first (non-blocking), then background pool
		select {
		case interactiveSemaphore <- struct{}{}:
			return func() { <-interactiveSemaphore }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			// Interactive pool full, fall back to background
		}
	}
	select {
	case bgSemaphore <- struct{}{}:
		return func() { <-bgSemaphore }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// isRetryable returns true for errors that should be retried with backoff.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "429") ||
		strings.Contains(s, "529") ||
		strings.Contains(s, "rate") ||
		strings.Contains(s, "overloaded") ||
		strings.Contains(s, "capacity")
}

// retryDelay returns the delay before the nth retry (exponential backoff, capped at 30s).
func retryDelay(attempt int) time.Duration {
	const maxDelay = 30 * time.Second
	if attempt > 4 { // 2s * 2^4 = 32s > 30s cap, so anything above 4 is always capped
		return maxDelay
	}
	delay := baseRetryDelay * time.Duration(math.Pow(2, float64(attempt)))
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

// StructuredOutput sends a prompt and parses the response into the target type
// using the tool_use pattern for structured output. Retries on rate limit errors.
// The API semaphore is released before retry sleeps to avoid starvation.
func (c *Client) StructuredOutput(ctx context.Context, system, prompt string, schema map[string]any, target any) error {
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshaling schema: %w", err)
	}

	var inputSchema anthropic.ToolInputSchemaParam
	if err := json.Unmarshal(schemaJSON, &inputSchema); err != nil {
		return fmt.Errorf("unmarshaling schema to param: %w", err)
	}

	// Allow per-request model override via context
	model := c.model
	if override, ok := ctx.Value(ContextKeyModel{}).(string); ok && override != "" {
		model = override
	}

	params := anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			// Cache the tools+system prefix. The analysis pipeline fires many
			// StructuredOutput calls that share the same (large) instruction +
			// tool-schema prefix and vary only in the user prompt, so a cache
			// breakpoint here turns repeated full-price input into 0.1x cache
			// reads — a cost + latency win on every deliberation, not just runs.
			{Text: system, CacheControl: anthropic.CacheControlEphemeralParam{}},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
		Tools: []anthropic.ToolUnionParam{
			{
				OfTool: &anthropic.ToolParam{
					Name:        "output",
					Description: anthropic.String("Output structured result"),
					InputSchema: inputSchema,
				},
			},
		},
		ToolChoice: anthropic.ToolChoiceParamOfTool("output"),
	}
	if temp, ok := ctx.Value(ContextKeyTemperature{}).(float64); ok {
		params.Temperature = anthropic.Float(temp)
	}

	var resp *anthropic.Message
	var lastErr error
	var attempts int
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attempts = attempt

		// Acquire API slot (blocks if all slots are in flight).
		// Interactive callers get priority via reserved slots.
		lastErr = func() error {
			release, err := acquireSemaphore(ctx)
			if err != nil {
				return err
			}
			defer release()
			resp, lastErr = c.client.Messages.New(ctx, params)
			return lastErr
		}()

		if lastErr == nil {
			break
		}
		if !isRetryable(lastErr) {
			return fmt.Errorf("API call failed: %w", lastErr)
		}
		// Retryable error — log and sleep without holding the semaphore
		slog.Warn("LLM retry", "attempt", attempt+1, "max_retries", maxRetries, "error", lastErr)

		delay := retryDelay(attempt)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if lastErr != nil {
		slog.Error("LLM call failed", "retries", maxRetries, "error", lastErr)
		return fmt.Errorf("API call failed after %d retries: %w", maxRetries, lastErr)
	}
	if attempts > 0 {
		slog.Info("LLM call succeeded after retries", "retries", attempts)
	}

	// Report token usage if callback is set
	if c.OnUsage != nil {
		c.OnUsage(ctx, int(resp.Usage.InputTokens), int(resp.Usage.OutputTokens))
	}
	slog.Debug("llm_usage", "kind", "structured",
		"input", resp.Usage.InputTokens, "output", resp.Usage.OutputTokens,
		"cache_read", resp.Usage.CacheReadInputTokens, "cache_write", resp.Usage.CacheCreationInputTokens)

	for _, block := range resp.Content {
		if block.Type == "tool_use" {
			tu := block.AsToolUse()
			if tu.Name == "output" {
				return json.Unmarshal(tu.Input, target)
			}
		}
	}

	return fmt.Errorf("no tool_use block in response")
}

// Classify sends a short prompt and returns the text response.
// Uses Haiku for speed and cost (~$0.001 per call). Retries on rate limit errors.
// The API semaphore is released before retry sleeps to avoid starvation.
func (c *Client) Classify(ctx context.Context, system, prompt string) (string, error) {
	params := anthropic.MessageNewParams{
		Model:     "claude-haiku-4-5",
		MaxTokens: 100,
		System: []anthropic.TextBlockParam{
			{Text: system, CacheControl: anthropic.CacheControlEphemeralParam{}},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	}

	var resp *anthropic.Message
	var lastErr error
	var attempts int
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attempts = attempt

		// Acquire API slot — interactive callers get priority via reserved slots
		lastErr = func() error {
			release, err := acquireSemaphore(ctx)
			if err != nil {
				return err
			}
			defer release()
			resp, lastErr = c.client.Messages.New(ctx, params)
			return lastErr
		}()

		if lastErr == nil {
			break
		}
		if !isRetryable(lastErr) {
			return "", fmt.Errorf("classify API call failed: %w", lastErr)
		}
		// Retryable error — log and sleep without holding the semaphore
		slog.Warn("LLM classify retry", "attempt", attempt+1, "max_retries", maxRetries, "error", lastErr)

		delay := retryDelay(attempt)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if lastErr != nil {
		slog.Error("LLM classify failed", "retries", maxRetries, "error", lastErr)
		return "", fmt.Errorf("classify API call failed after %d retries: %w", maxRetries, lastErr)
	}
	if attempts > 0 {
		slog.Info("LLM classify succeeded after retries", "retries", attempts)
	}

	if c.OnUsage != nil {
		c.OnUsage(ctx, int(resp.Usage.InputTokens), int(resp.Usage.OutputTokens))
	}
	slog.Debug("llm_usage", "kind", "classify",
		"input", resp.Usage.InputTokens, "output", resp.Usage.OutputTokens,
		"cache_read", resp.Usage.CacheReadInputTokens, "cache_write", resp.Usage.CacheCreationInputTokens)

	for _, block := range resp.Content {
		if block.Type == "text" {
			return block.AsText().Text, nil
		}
	}
	return "", fmt.Errorf("no text block in response")
}
