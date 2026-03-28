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
//
// Restorative ostracism: penalties decay in later rounds. By round 3+,
// first-round warnings carry ~50% of their original penalty. This encourages
// reform — agents who stop bad behavior see their trust recover.
//
// This implements CP-WBFT's confidence-weighted approach with Hortator-inspired decay.
func TrustWeights(
	agents []string,
	positions []deliberation.Position,
	votes []deliberation.Vote,
	warnings []string,
	round int,
) map[string]float64 {
	weights := make(map[string]float64, len(agents))
	for _, a := range agents {
		weights[a] = 1.0
	}

	// Restorative decay: penalties are softer in later rounds
	// Round 1: full penalty. Round 2: 75%. Round 3+: 50%.
	decayFactor := 1.0
	if round >= 3 {
		decayFactor = 0.5
	} else if round == 2 {
		decayFactor = 0.75
	}

	// Parse integrity warnings for Sybil signals
	for _, w := range warnings {
		if len(w) > 13 && w[:13] == "SYBIL_SIGNAL:" {
			for _, a := range agents {
				if containsQuoted(w, a) {
					weights[a] = max(0.0, weights[a]-0.3*decayFactor)
				}
			}
		}
		if len(w) > 9 && w[:9] == "COVERAGE:" {
			for _, a := range agents {
				if containsQuoted(w, a) {
					weights[a] = max(0.0, weights[a]-0.2*decayFactor)
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
