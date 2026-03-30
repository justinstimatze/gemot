package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

// apiSemaphore limits concurrent Anthropic API calls across all analyses.
// Prevents rate limit hits when multiple deliberations analyze simultaneously.
var apiSemaphore = make(chan struct{}, 10)

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
	delay := baseRetryDelay * time.Duration(math.Pow(2, float64(attempt)))
	// Cap at 30 seconds
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return delay
}

// StructuredOutput sends a prompt and parses the response into the target type
// using the tool_use pattern for structured output. Retries on rate limit errors.
func (c *Client) StructuredOutput(ctx context.Context, system, prompt string, schema map[string]any, target any) error {
	// Acquire API slot (blocks if 10 concurrent calls are in flight)
	select {
	case apiSemaphore <- struct{}{}:
		defer func() { <-apiSemaphore }()
	case <-ctx.Done():
		return ctx.Err()
	}
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
			{Text: system},
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

	var resp *anthropic.Message
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := retryDelay(attempt - 1)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		resp, lastErr = c.client.Messages.New(ctx, params)
		if lastErr == nil {
			break
		}
		if !isRetryable(lastErr) {
			return fmt.Errorf("API call failed: %w", lastErr)
		}
		// Retryable error — log and continue
		log.Printf("[gemot] LLM retry %d/%d: %v", attempt+1, maxRetries, lastErr)
	}
	if lastErr != nil {
		log.Printf("[gemot] LLM call failed after %d retries: %v", maxRetries, lastErr)
		return fmt.Errorf("API call failed after %d retries: %w", maxRetries, lastErr)
	}

	// Report token usage if callback is set
	if c.OnUsage != nil {
		c.OnUsage(ctx, int(resp.Usage.InputTokens), int(resp.Usage.OutputTokens))
	}

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
func (c *Client) Classify(ctx context.Context, system, prompt string) (string, error) {
	select {
	case apiSemaphore <- struct{}{}:
		defer func() { <-apiSemaphore }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	params := anthropic.MessageNewParams{
		Model:     "claude-haiku-4-5",
		MaxTokens: 100,
		System: []anthropic.TextBlockParam{
			{Text: system},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	}

	var resp *anthropic.Message
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := retryDelay(attempt - 1)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		resp, lastErr = c.client.Messages.New(ctx, params)
		if lastErr == nil {
			break
		}
		if !isRetryable(lastErr) {
			return "", fmt.Errorf("classify API call failed: %w", lastErr)
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("classify API call failed after %d retries: %w", maxRetries, lastErr)
	}

	if c.OnUsage != nil {
		c.OnUsage(ctx, int(resp.Usage.InputTokens), int(resp.Usage.OutputTokens))
	}

	for _, block := range resp.Content {
		if block.Type == "text" {
			return block.AsText().Text, nil
		}
	}
	return "", fmt.Errorf("no text block in response")
}
