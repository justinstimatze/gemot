package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
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

// StructuredOutput sends a prompt and parses the response into the target type
// using the tool_use pattern for structured output.
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

	resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
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
	})
	if err != nil {
		return fmt.Errorf("API call failed: %w", err)
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
