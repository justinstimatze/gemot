package config

import (
	"bufio"
	"os"
	"strings"
)

type Config struct {
	DBPath       string
	AnthropicKey string
	Model        string
}

func Load() *Config {
	loadDotenv(".env")

	return &Config{
		DBPath:       envOr("GEMOT_DB", ".gemot/gemot.db"),
		AnthropicKey: os.Getenv("GEMOT_ANTHROPIC_KEY"),
		Model:        envOr("GEMOT_MODEL", "claude-sonnet-4-6"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
