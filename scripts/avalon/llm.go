package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// LLM is a small Anthropic client with retry/backoff, mirroring the hardened
// wrappers in scripts/codenames and scripts/chess-consensus.
type LLM struct {
	apiKey      string
	baseURL     string
	model       string
	client      *http.Client
	retryBase   time.Duration
	temperature float64

	Calls      int64 // atomic
	inTok      int64 // atomic: uncached input tokens
	outTok     int64 // atomic
	cacheWrite int64 // atomic: cache_creation_input_tokens
	cacheRead  int64 // atomic: cache_read_input_tokens
}

func NewLLM(model string) (*LLM, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is required")
	}
	base := os.Getenv("ANTHROPIC_BASE_URL")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return &LLM{
		apiKey:    key,
		baseURL:   strings.TrimSuffix(base, "/"),
		model:     model,
		client:    &http.Client{Timeout: 2 * time.Minute},
		retryBase: 500 * time.Millisecond,
	}, nil
}

func (l *LLM) complete(system, user string, maxTokens int) (string, error) {
	payload := map[string]any{
		"model":      l.model,
		"max_tokens": maxTokens,
		// Uniform system prompt marked cacheable: identical bytes across every
		// call form one cached prefix for the whole run (write once, read after).
		"system": []map[string]any{
			{"type": "text", "text": system, "cache_control": map[string]string{"type": "ephemeral"}},
		},
		"messages": []map[string]string{{"role": "user", "content": user}},
	}
	if l.temperature > 0 {
		payload["temperature"] = l.temperature
	}
	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(l.retryBase << (attempt - 1))
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		req, err := http.NewRequest("POST", l.baseURL+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("x-api-key", l.apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("content-type", "application/json")
		resp, err := l.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close() //nolint:errcheck
		atomic.AddInt64(&l.Calls, 1)
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			apiErr := fmt.Errorf("anthropic API %d: %s", resp.StatusCode, truncate(string(respBody), 200))
			if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(respBody), "temperature") {
				if _, ok := payload["temperature"]; ok {
					delete(payload, "temperature")
					lastErr = apiErr
					continue
				}
			}
			if retryableStatus(resp.StatusCode) {
				lastErr = apiErr
				continue
			}
			return "", apiErr
		}
		var parsed struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Usage struct {
				InputTokens              int64 `json:"input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return "", err
		}
		atomic.AddInt64(&l.inTok, parsed.Usage.InputTokens)
		atomic.AddInt64(&l.outTok, parsed.Usage.OutputTokens)
		atomic.AddInt64(&l.cacheWrite, parsed.Usage.CacheCreationInputTokens)
		atomic.AddInt64(&l.cacheRead, parsed.Usage.CacheReadInputTokens)
		var text strings.Builder
		for _, c := range parsed.Content {
			text.WriteString(c.Text)
		}
		if text.Len() == 0 {
			return "", fmt.Errorf("empty response")
		}
		return text.String(), nil
	}
	return "", fmt.Errorf("anthropic API: giving up after %d attempts: %w", maxAttempts, lastErr)
}

// Stats summarises token usage, including cache effectiveness.
func (l *LLM) Stats() string {
	return fmt.Sprintf("llm: %d calls | input %dk (cache-read %dk, cache-write %dk) | output %dk",
		atomic.LoadInt64(&l.Calls),
		atomic.LoadInt64(&l.inTok)/1000,
		atomic.LoadInt64(&l.cacheRead)/1000,
		atomic.LoadInt64(&l.cacheWrite)/1000,
		atomic.LoadInt64(&l.outTok)/1000)
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// extractJSON returns the first complete top-level JSON object/array in s by
// brace-matching (respecting string literals and escapes).
func extractJSON(s string) string {
	start := strings.IndexAny(s, "{[")
	if start == -1 {
		return s
	}
	open := s[start]
	closer := byte('}')
	if open == '[' {
		closer = ']'
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(extractJSON(s)), v) }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

const gameRules = `The Resistance: Avalon. Players are secretly split into a GOOD team (Loyal Servants of Arthur, plus special roles like Merlin and Percival) and an EVIL team (Minions of Mordred, including the Assassin). Over up to 5 quests, a rotating leader proposes a quest team; all players publicly vote to APPROVE or REJECT it (strict majority approves; the 5th proposal of a quest auto-approves). The approved team then secretly votes the quest PASS or FAIL — one FAIL sinks most quests (a few need two). GOOD wins by passing 3 quests; EVIL wins by failing 3. If GOOD passes 3 quests, the evil Assassin gets one chance to win by correctly naming Merlin. GOOD players must deduce who is evil while protecting Merlin's identity; EVIL players must sabotage quests and blend in by lying.`
