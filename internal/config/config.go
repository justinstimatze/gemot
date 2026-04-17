package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL  string
	AnthropicKey string
	Model        string

	// StabilitySamples enables opt-in CRUX_INSTABILITY re-sampling at analysis
	// time. 0 or 1 disables the check; 2+ triggers N extra same-prompt crux
	// calls per subtopic plus a semantic-judge call per sample. Expect cost
	// scaling roughly linear in N × subtopic count. Suggested dev/research
	// value: 3. Leave unset in production unless the cost profile is understood.
	StabilitySamples int
}

// Load reads configuration from environment variables (and optional .env file).
// Validates required settings and warns about misconfigurations.
func Load() *Config {
	loadDotenv(".env")

	cfg := &Config{
		DatabaseURL:      envOr("DATABASE_URL", "postgres://gemot:gemot@localhost:5432/gemot?sslmode=disable"),
		AnthropicKey:     envOr("ANTHROPIC_API_KEY", os.Getenv("GEMOT_ANTHROPIC_KEY")),
		Model:            envOr("GEMOT_MODEL", "claude-sonnet-4-6"),
		StabilitySamples: envInt("GEMOT_STABILITY_SAMPLES", 0),
	}

	// Validate model is in known set
	validModels := map[string]bool{
		"claude-sonnet-4-6": true,
		"claude-opus-4-6":   true,
		"claude-haiku-4-5":  true,
	}
	if !validModels[cfg.Model] {
		fmt.Fprintf(os.Stderr, "gemot: WARNING: unknown GEMOT_MODEL %q — analysis may fail\n", cfg.Model)
	}

	// Warn if analysis won't work
	if cfg.AnthropicKey == "" {
		fmt.Fprintf(os.Stderr, "gemot: WARNING: ANTHROPIC_API_KEY not set — analysis and content screening disabled\n")
	}

	return cfg
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envInt reads an integer env var; unset or unparseable falls back to the default.
// Malformed values emit a warning on stderr so misconfiguration is visible.
func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gemot: WARNING: %s=%q is not an integer, using default %d\n", key, raw, fallback)
		return fallback
	}
	return n
}

// loadDotenv reads a .env file and sets any vars not already in the environment.
func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // missing .env is fine
	}
	defer f.Close() //nolint:errcheck

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		// Strip surrounding quotes (KEY="value" or KEY='value')
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		// Don't override existing env vars
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
}
