package llm

import (
	"errors"
	"testing"
	"time"
)

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("connection refused"), false},
		{errors.New("HTTP 429 Too Many Requests"), true},
		{errors.New("HTTP 529 Service Overloaded"), true},
		{errors.New("rate limit exceeded"), true},
		{errors.New("server overloaded"), true},
		{errors.New("insufficient capacity"), true},
		{errors.New("permission denied"), false},
		{errors.New("invalid API key"), false},
	}

	for _, tt := range tests {
		name := "nil"
		if tt.err != nil {
			name = tt.err.Error()
		}
		t.Run(name, func(t *testing.T) {
			got := isRetryable(tt.err)
			if got != tt.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	// Attempt 0: 2s * 2^0 = 2s
	if d := retryDelay(0); d != 2*time.Second {
		t.Errorf("attempt 0: got %v, want 2s", d)
	}

	// Attempt 1: 2s * 2^1 = 4s
	if d := retryDelay(1); d != 4*time.Second {
		t.Errorf("attempt 1: got %v, want 4s", d)
	}

	// Attempt 3: 2s * 2^3 = 16s
	if d := retryDelay(3); d != 16*time.Second {
		t.Errorf("attempt 3: got %v, want 16s", d)
	}

	// Attempt 10: should be capped at 30s (2s * 2^10 = 2048s >> 30s)
	if d := retryDelay(10); d != 30*time.Second {
		t.Errorf("attempt 10: got %v, want 30s (capped)", d)
	}

	// Very high attempt: still capped at 30s
	if d := retryDelay(100); d != 30*time.Second {
		t.Errorf("attempt 100: got %v, want 30s (capped)", d)
	}
}

func TestAllowedModels(t *testing.T) {
	expected := []string{"claude-sonnet-4-6", "claude-opus-4-6", "claude-haiku-4-5"}
	for _, m := range expected {
		if !AllowedModels[m] {
			t.Errorf("%s should be allowed", m)
		}
	}

	if AllowedModels["gpt-4"] {
		t.Error("gpt-4 should not be allowed")
	}
	if AllowedModels[""] {
		t.Error("empty string should not be allowed")
	}
}
