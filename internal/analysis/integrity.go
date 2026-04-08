package analysis

import (
	"fmt"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

// validateCoverage checks that every agent who submitted a position has at least
// one claim in the extracted claims. If an agent's position was silenced by taxonomy
// manipulation, this flags it.
func validateCoverage(positions []deliberation.Position, claims []claim) []string {
	var warnings []string

	agentsWithClaims := map[string]bool{}
	for _, c := range claims {
		agentsWithClaims[c.AgentID] = true
	}

	for _, p := range positions {
		if !agentsWithClaims[p.AgentID] {
			warnings = append(warnings, fmt.Sprintf(
				"COVERAGE: agent %q submitted a position but no claims were extracted from it — their voice may be missing from crux analysis",
				p.AgentID,
			))
		}
	}
	return warnings
}

// validateCruxAgents checks that every agent listed in a crux's agree/disagree/no_clear_position
// actually submitted a position. The LLM sometimes hallucinates agent assignments.
// ValidatedCruxes holds valid cruxes and degenerate ones separately.
type ValidatedCruxes struct {
	Valid      []deliberation.Crux
	Degenerate []deliberation.Crux
}

func validateCruxAgents(cruxes []deliberation.Crux, validAgents map[string]bool) (ValidatedCruxes, []string) {
	var warnings []string
	cleaned := make([]deliberation.Crux, len(cruxes))

	for i, crux := range cruxes {
		cleaned[i] = crux
		cleaned[i].AgreeAgents = filterValidAgents(crux.AgreeAgents, validAgents)
		cleaned[i].DisagreeAgents = filterValidAgents(crux.DisagreeAgents, validAgents)
		cleaned[i].NoClearPosition = filterValidAgents(crux.NoClearPosition, validAgents)

		removedAgree := len(crux.AgreeAgents) - len(cleaned[i].AgreeAgents)
		removedDisagree := len(crux.DisagreeAgents) - len(cleaned[i].DisagreeAgents)
		removedNCP := len(crux.NoClearPosition) - len(cleaned[i].NoClearPosition)

		if removedAgree+removedDisagree+removedNCP > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"HALLUCINATION: crux %q had %d agent(s) removed — they were not actual participants",
				truncateClaim(crux.Claim, 60), removedAgree+removedDisagree+removedNCP,
			))
		}

		if len(cleaned[i].AgreeAgents) == 0 || len(cleaned[i].DisagreeAgents) == 0 {
			cleaned[i].Degenerate = true
			cleaned[i].ControversyScore = 0
			emptySide := "agree"
			if len(cleaned[i].AgreeAgents) > 0 {
				emptySide = "disagree"
			}
			warnings = append(warnings, fmt.Sprintf(
				"DEGENERATE: crux %q has no agents on the %s side after validation",
				truncateClaim(crux.Claim, 60), emptySide,
			))
		}
	}

	var result ValidatedCruxes
	for _, c := range cleaned {
		if c.Degenerate {
			result.Degenerate = append(result.Degenerate, c)
		} else {
			result.Valid = append(result.Valid, c)
		}
	}

	return result, warnings
}

// validateVoteSimilarity checks for suspiciously correlated voting patterns (Sybil signal).
// Two agents with identical votes across all positions they both voted on are flagged.
func validateVoteSimilarity(votes []deliberation.Vote, agents []string) []string {
	var warnings []string

	// Build vote fingerprints per agent (value + qualifier for richer comparison)
	type votePrint struct {
		Value     int
		Qualifier string
	}
	agentVotes := map[string]map[string]votePrint{} // agent -> position -> fingerprint
	for _, v := range votes {
		if _, ok := agentVotes[v.AgentID]; !ok {
			agentVotes[v.AgentID] = map[string]votePrint{}
		}
		agentVotes[v.AgentID][v.PositionID] = votePrint{Value: v.Value, Qualifier: v.Qualifier}
	}

	// Check all pairs
	for i := 0; i < len(agents); i++ {
		for j := i + 1; j < len(agents); j++ {
			a, b := agents[i], agents[j]
			va, vb := agentVotes[a], agentVotes[b]
			if len(va) == 0 || len(vb) == 0 {
				continue
			}

			shared, identical := 0, 0
			for pos, fpA := range va {
				if fpB, ok := vb[pos]; ok {
					shared++
					if fpA.Value == fpB.Value && fpA.Qualifier == fpB.Qualifier {
						identical++
					}
				}
			}

			if shared >= 5 && identical == shared {
				warnings = append(warnings, fmt.Sprintf(
					"SYBIL_SIGNAL: agents %q and %q have identical votes across all %d shared positions",
					a, b, shared,
				))
			}
		}
	}
	return warnings
}

// validateModelDiversity checks whether all participating agents declared the same model family.
// When all agents share a model family, consensus may reflect shared training biases
// rather than genuine agreement ("Consensus is Not Verification", arXiv 2603.06612).
func validateModelDiversity(positions []deliberation.Position) []string {
	families := map[string]int{}
	declared := 0
	for _, p := range positions {
		if p.ModelFamily != "" {
			families[p.ModelFamily]++
			declared++
		}
	}
	if declared < 2 {
		return nil // not enough data to assess diversity
	}
	var warnings []string
	if len(families) == 1 {
		for family, count := range families {
			warnings = append(warnings, fmt.Sprintf(
				"MODEL_DIVERSITY: all %d agents declaring a model use %q — consensus may reflect shared training biases rather than genuine agreement",
				count, family,
			))
		}
	}
	return warnings
}

func filterValidAgents(agents []string, valid map[string]bool) []string {
	var result []string
	for _, a := range agents {
		if valid[a] {
			result = append(result, a)
		}
	}
	return result
}

func truncateClaim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
