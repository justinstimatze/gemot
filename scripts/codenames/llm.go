package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// LLM is a small Anthropic client with retry/backoff, mirroring the hardened
// wrapper in scripts/chess-consensus. Used for the codemaster and the guessers.
type LLM struct {
	apiKey      string
	baseURL     string
	model       string
	client      *http.Client
	retryBase   time.Duration
	temperature float64 // >0 raises sampling diversity (for guessers)
	Calls       int
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
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": user}},
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
		l.Calls++
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			apiErr := fmt.Errorf("anthropic API %d: %s", resp.StatusCode, truncate(string(respBody), 200))
			// Some models reject `temperature` (deprecated); drop it and retry.
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
		}
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return "", err
		}
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

func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// extractJSON returns the first complete top-level JSON object/array in s by
// brace-matching (respecting string literals and escapes), so it is robust to
// markdown fences, reasoning preambles, and trailing prose around the JSON.
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

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

const gameRules = `Codenames: a 5x5 board of 25 words. Each word is secretly one of your team's (good), the opponent's, a neutral civilian, or the single assassin (instant loss). A spymaster gives a one-word clue and a number N meaning "N board words relate to this clue". Guessers pick words they believe are their team's; a correct pick lets them continue, any wrong pick ends the turn, and the assassin loses the game.`
