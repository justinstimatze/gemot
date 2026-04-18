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

	// EigenTrust controls the persistent reputation layer. Off-by-default.
	// When enabled, positions/votes contribute to a cross-deliberation
	// trust graph (see internal/analysis/eigentrust.go + internal/store/
	// reputation.go), and agent effective-weights in consensus synthesis
	// are modulated by (a) the EigenTrust score and (b) a cold-start cap
	// until the agent has accumulated ColdStartThreshold survived-round
	// validations. The cold-start cap is the primary Sybil defense
	// because canonical EigenTrust under a uniform teleport cannot
	// defeat closed trust cycles — see THREAT_MODEL.md.
	EigenTrustEnabled       bool
	EigenTrustColdCap       float64 // default 0.1
	EigenTrustColdThreshold int     // default 5
	EigenTrustIterations    int     // default 50
	// EigenTrustDBFail is "open" (default) or "closed". Under
	// "closed", a reputation DB read failure aborts the analysis round
	// rather than silently degrading to unit weights. Recommended for
	// hosted / Byzantine-context deployments; see THREAT_MODEL.md.
	EigenTrustDBFail string
	// EigenTrustDecayHalfLifeDays exponentially decays trust-edge weights
	// with the given half-life (0 disables). Defends against the
	// whitewashing attack where a graduated Sybil ring retains pumped-up
	// weight indefinitely — with decay, edges that stop being reinforced
	// fade toward zero. Recommended: 14 (two-week half-life).
	EigenTrustDecayHalfLifeDays int
	// EigenTrustDisputeWeight is the negative edge weight added per
	// Dispute against an agent's crux. Defaults to 0.5 — half a full
	// endorsement. Stored weight can go negative; EigenTrust ignores
	// non-positive edges so inbound trust mass is effectively clamped.
	EigenTrustDisputeWeight float64
	// EigenTrustEdgeFloor prunes trust-edge rows whose decayed weight
	// falls below this value. 0 (default) disables pruning. Bounds the
	// trust graph's row count so open-federation scale can't blow up
	// agent_trust_edges with edges that will never contribute
	// meaningfully to EigenTrust. Recommended: 0.01.
	EigenTrustEdgeFloor float64
	// EigenTrustEdgeCap clamps the cumulative weight of any single
	// (from, to) edge. 0 (default) disables — unbounded legacy
	// accumulation. Recommended: 10.0. A Sybil pair that mutually
	// endorses across many deliberations can push an edge to infinity
	// without the cap, leaking into post-graduation EigenTrust mass.
	EigenTrustEdgeCap float64

	// ConsistencyModel + ConsistencyKey enable the cross-family OOD
	// consistency check (DARPA-PS-26-09 Track 1, abstract §3). When
	// both are set at startup, a secondary Gemini client re-scores the
	// top-K highest-controversy cruxes per analysis and emits
	// CROSS_FAMILY_DRIFT warnings on strict-majority sign flip.
	// Off-by-default; empty values disable the feature at zero cost.
	ConsistencyModel   string
	ConsistencyKey     string
	ConsistencySampleK int // default 5
}

// Load reads configuration from environment variables (and optional .env file).
// Validates required settings and warns about misconfigurations.
func Load() *Config {
	loadDotenv(".env")

	cfg := &Config{
		DatabaseURL:                 envOr("DATABASE_URL", "postgres://gemot:gemot@localhost:5432/gemot?sslmode=disable"),
		AnthropicKey:                envOr("ANTHROPIC_API_KEY", os.Getenv("GEMOT_ANTHROPIC_KEY")),
		Model:                       envOr("GEMOT_MODEL", "claude-sonnet-4-6"),
		StabilitySamples:            envInt("GEMOT_STABILITY_SAMPLES", 0),
		EigenTrustEnabled:           envBool("GEMOT_EIGENTRUST_ENABLED", false),
		EigenTrustColdCap:           envFloat("GEMOT_EIGENTRUST_COLD_CAP", 0.1),
		EigenTrustColdThreshold:     envInt("GEMOT_EIGENTRUST_COLD_THRESHOLD", 5),
		EigenTrustIterations:        envInt("GEMOT_EIGENTRUST_ITERATIONS", 50),
		EigenTrustDBFail:            envDBFailMode("GEMOT_EIGENTRUST_DB_FAIL", "open"),
		EigenTrustDecayHalfLifeDays: envInt("GEMOT_EIGENTRUST_DECAY_HALFLIFE_DAYS", 0),
		EigenTrustDisputeWeight:     envFloat("GEMOT_EIGENTRUST_DISPUTE_WEIGHT", 0.5),
		EigenTrustEdgeFloor:         envFloat("GEMOT_EIGENTRUST_EDGE_FLOOR", 0),
		EigenTrustEdgeCap:           envFloat("GEMOT_EIGENTRUST_EDGE_CAP", 0),
		ConsistencyModel:            os.Getenv("GEMOT_CONSISTENCY_MODEL"),
		ConsistencyKey:              envOr("GEMOT_CONSISTENCY_KEY", os.Getenv("GEMINI_API_KEY")),
		ConsistencySampleK:          envInt("GEMOT_CONSISTENCY_SAMPLE_K", 5),
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

	if cfg.EigenTrustEnabled {
		fmt.Fprintf(os.Stderr,
			"gemot: EigenTrust reputation enabled (cold_cap=%.2f, cold_threshold=%d, iterations=%d, db_fail=%s, decay_halflife_days=%d, dispute_weight=%.2f, edge_floor=%.4f, edge_cap=%.2f)\n",
			cfg.EigenTrustColdCap, cfg.EigenTrustColdThreshold, cfg.EigenTrustIterations, cfg.EigenTrustDBFail, cfg.EigenTrustDecayHalfLifeDays, cfg.EigenTrustDisputeWeight, cfg.EigenTrustEdgeFloor, cfg.EigenTrustEdgeCap)
	}

	if cfg.ConsistencyModel != "" && cfg.ConsistencyKey != "" {
		fmt.Fprintf(os.Stderr,
			"gemot: cross-family OOD consistency ENABLED (model=%s, sample_k=%d) — expect ~%d extra LLM calls per analysis\n",
			cfg.ConsistencyModel, cfg.ConsistencySampleK, cfg.ConsistencySampleK)
	}

	return cfg
}

// envBool reads a boolean env var. Accepts "1"/"true"/"yes" (case-
// insensitive) as true, "0"/"false"/"no"/"" as false. Unrecognized
// values warn on stderr and fall back to the default — same pattern
// as envInt.
func envBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		fmt.Fprintf(os.Stderr, "gemot: WARNING: %s=%q is not a boolean, using default %v\n", key, raw, fallback)
		return fallback
	}
}

// envDBFailMode reads a fail-mode env var. Accepts "open" or "closed".
// Unrecognized values warn and fall back to the default.
func envDBFailMode(key, fallback string) string {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "open", "closed":
		return raw
	default:
		fmt.Fprintf(os.Stderr, "gemot: WARNING: %s=%q must be open|closed, using default %q\n", key, raw, fallback)
		return fallback
	}
}

// envFloat mirrors envInt for float64 values.
func envFloat(key string, fallback float64) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gemot: WARNING: %s=%q is not a float, using default %f\n", key, raw, fallback)
		return fallback
	}
	return f
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
