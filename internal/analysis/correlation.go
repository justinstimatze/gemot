package analysis

import (
	"math"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

// CorrelationDiscountedWeights computes vote weights that discount correlated agents.
// Agents that consistently vote together get their combined influence reduced via
// square-root scaling (degressive proportionality).
//
// This implements the core mechanism from Plurality (Weyl/Tang) Chapter 5-6:
// "correlation discounting" prevents Sybil-like behavior and ensures genuinely
// independent perspectives carry proportionally more weight.
//
// Reference: Miller, Weyl, Erichsen, "Beyond Collusion Resistance: Leveraging
// Social Information for Plural Funding and Voting" (2023), SSRN 4311507.
func CorrelationDiscountedWeights(votes []deliberation.Vote, agents []string) map[string]float64 {
	weights := make(map[string]float64, len(agents))
	for _, a := range agents {
		weights[a] = 1.0
	}

	if len(votes) < 3 || len(agents) < 3 {
		return weights
	}

	// Build vote vectors per agent
	agentVotes := map[string]map[string]int{}
	for _, v := range votes {
		if agentVotes[v.AgentID] == nil {
			agentVotes[v.AgentID] = map[string]int{}
		}
		agentVotes[v.AgentID][v.PositionID] = v.Value
	}

	// Compute pairwise correlation (fraction of shared votes that are identical)
	type pair struct{ a, b string }
	correlation := map[pair]float64{}

	for i := 0; i < len(agents); i++ {
		for j := i + 1; j < len(agents); j++ {
			a, b := agents[i], agents[j]
			va, vb := agentVotes[a], agentVotes[b]
			shared, identical := 0, 0
			for pos, valA := range va {
				if valB, ok := vb[pos]; ok {
					shared++
					if valA == valB {
						identical++
					}
				}
			}
			if shared >= 2 {
				correlation[pair{a, b}] = float64(identical) / float64(shared)
			}
		}
	}

	// For each agent, compute average correlation with all other agents
	// High correlation → reduced weight (square-root discounting)
	for _, agent := range agents {
		totalCorr := 0.0
		corrCount := 0
		for _, other := range agents {
			if other == agent {
				continue
			}
			p := pair{agent, other}
			if agent > other {
				p = pair{other, agent}
			}
			if c, ok := correlation[p]; ok {
				totalCorr += c
				corrCount++
			}
		}
		if corrCount > 0 {
			avgCorr := totalCorr / float64(corrCount)
			// Square-root discounting: weight = sqrt(1 - avgCorrelation)
			// Perfectly independent (avgCorr=0) → weight 1.0
			// Perfectly correlated (avgCorr=1) → weight 0.0
			// Moderately correlated (avgCorr=0.5) → weight ~0.71
			weights[agent] = math.Sqrt(1.0 - avgCorr)
			if weights[agent] < 0.1 {
				weights[agent] = 0.1 // floor to prevent complete silencing
			}
		}
	}

	return weights
}
