package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(ctx context.Context, url, secret string) (*sdkmcp.ClientSession, error) {
	transport := &sdkmcp.SSEClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: secret},
		},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "diplomacy", Version: "2.0"}, nil)
	return client.Connect(ctx, transport, nil)
}

func callTool(ctx context.Context, s *sdkmcp.ClientSession, name string, args map[string]any) string {
	if s == nil {
		fmt.Fprintf(os.Stderr, "tool %s: nil session (gemot server down?)\n", name)
		os.Exit(1)
	}
	res, err := s.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tool %s failed: %v\n", name, err)
		os.Exit(1)
	}
	if res.IsError {
		fmt.Fprintf(os.Stderr, "tool %s error: %s\n", name, res.Content[0].(*sdkmcp.TextContent).Text)
		os.Exit(1)
	}
	return res.Content[0].(*sdkmcp.TextContent).Text
}

func callToolSoft(ctx context.Context, s *sdkmcp.ClientSession, name string, args map[string]any) string {
	if s == nil {
		fmt.Fprintf(os.Stderr, "  [soft] %s: nil session\n", name)
		return ""
	}
	ctx2, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res, err := s.CallTool(ctx2, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fmt.Fprintf(os.Stderr, "  [soft] %s failed: %v\n", name, err)
		return ""
	}
	if res.IsError || len(res.Content) == 0 {
		errMsg := ""
		if res.IsError && len(res.Content) > 0 {
			errMsg = res.Content[0].(*sdkmcp.TextContent).Text
		}
		fmt.Fprintf(os.Stderr, "  [soft] %s error: %s\n", name, errMsg)
		return ""
	}
	return res.Content[0].(*sdkmcp.TextContent).Text
}

// resilientSession wraps a session with automatic reconnection on failure.
type resilientSession struct {
	url, secret string
	session     *sdkmcp.ClientSession
}

func newResilientSession(url, secret string) *resilientSession {
	return &resilientSession{url: url, secret: secret}
}

func (rs *resilientSession) ensure(ctx context.Context) error {
	if rs.session != nil {
		return nil
	}
	s, err := connect(ctx, rs.url, rs.secret)
	if err != nil {
		return err
	}
	rs.session = s
	return nil
}

func (rs *resilientSession) callSoft(ctx context.Context, name string, args map[string]any) string {
	if err := rs.ensure(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "  [soft] connect failed: %v\n", err)
		return ""
	}
	result := callToolSoft(ctx, rs.session, name, args)
	if result != "" {
		return result
	}
	// Retry once with a fresh connection (the session may have died)
	rs.close()
	if err := rs.ensure(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "  [soft] reconnect failed: %v\n", err)
		return ""
	}
	return callToolSoft(ctx, rs.session, name, args)
}

func (rs *resilientSession) close() {
	if rs.session != nil {
		rs.session.Close() //nolint:errcheck
		rs.session = nil
	}
}

func sha256Short(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

func mustParse(jsonStr string, v any) {
	if idx := strings.Index(jsonStr, "\n\n---\n"); idx != -1 {
		jsonStr = jsonStr[:idx]
	}
	if err := json.Unmarshal([]byte(jsonStr), v); err != nil {
		fmt.Fprintf(os.Stderr, "JSON parse error: %v\nRaw: %s\n", err, jsonStr[:min(200, len(jsonStr))])
		os.Exit(1)
	}
}

func mustParseSoft(jsonStr string, v any) {
	if idx := strings.Index(jsonStr, "\n\n---\n"); idx != -1 {
		jsonStr = jsonStr[:idx]
	}
	json.Unmarshal([]byte(jsonStr), v) //nolint:errcheck
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func fatal(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", msg, err)
		os.Exit(1)
	}
}

// truncateRunes truncates a string to at most maxRunes runes, appending "..." if truncated.
// Unlike byte slicing (s[:n]), this never splits a multibyte UTF-8 character.
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// ============================================================
// V12: Anthropic API Client
// ============================================================

// llmCall makes a direct Anthropic API call for analysis enrichments.
func llmCall(ctx context.Context, apiKey, system, user string) (string, error) {
	body := map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 4096,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": user}},
	}
	bodyJSON, _ := json.Marshal(body) //nolint:errcheck
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(bodyJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	respBody, _ := io.ReadAll(resp.Body) //nolint:errcheck
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("anthropic API %d: %s", resp.StatusCode, string(respBody[:min(200, len(respBody))]))
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}
	if len(result.Content) == 0 {
		return "", fmt.Errorf("empty response from anthropic API")
	}
	return result.Content[0].Text, nil
}

// extractJSON pulls the first JSON object or array from a string (handles markdown fences).
func extractJSON(s string) string {
	// Strip markdown code fences
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	}
	// Find first { or [
	start := -1
	for i, c := range s {
		if c == '{' || c == '[' {
			start = i
			break
		}
	}
	if start == -1 {
		return s
	}
	return strings.TrimSpace(s[start:])
}
