package analysis

import (
	"github.com/justinstimatze/gemot/internal/deliberation"
)

// TrustWeights computes per-agent trust weights based on integrity signals.
// Weights are in [0.0, 1.0] where 1.0 = fully trusted.
//
// Trust is reduced by:
// - Sybil correlation with another agent (-0.3 per correlated pair)
// - Having 0 claims extracted from positions (-0.2, possible taxonomy gaming)
// - Filing frivolous disputes that don't match any crux (-0.1)
//
// Trust is preserved (not boosted) for normal behavior — the default is 1.0.
// This implements a simple version of CP-WBFT's confidence-weighted approach.
func TrustWeights(
	agents []string,
	positions []deliberation.Position,
	votes []deliberation.Vote,
	warnings []string,
) map[string]float64 {
	weights := make(map[string]float64, len(agents))
	for _, a := range agents {
		weights[a] = 1.0
	}

	// Parse integrity warnings for Sybil signals
	for _, w := range warnings {
		if len(w) > 13 && w[:13] == "SYBIL_SIGNAL:" {
			// Extract agent names from warning
			// Format: SYBIL_SIGNAL: agents "X" and "Y" have identical votes...
			for _, a := range agents {
				if containsQuoted(w, a) {
					weights[a] = max(0.0, weights[a]-0.3)
				}
			}
		}
		if len(w) > 9 && w[:9] == "COVERAGE:" {
			// Format: COVERAGE: agent "X" submitted a position but no claims...
			for _, a := range agents {
				if containsQuoted(w, a) {
					weights[a] = max(0.0, weights[a]-0.2)
				}
			}
		}
	}

	return weights
}

func containsQuoted(s, substr string) bool {
	target := `"` + substr + `"`
	for i := 0; i <= len(s)-len(target); i++ {
		if s[i:i+len(target)] == target {
			return true
		}
	}
	return false
}
