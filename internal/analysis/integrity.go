package analysis

import (
	"fmt"
	"sort"

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

// validateLowEffortPositions flags agents whose claim count is suspiciously low
// relative to peers. A low-effort (or deliberately thin) position gets under-represented
// in downstream crux detection because it contributes fewer claims for the LLM to weigh.
//
// Two independent thresholds fire:
//   - Absolute floor: fewer than 2 claims always flags (LOW_EFFORT_ABS).
//   - Median-relative: claim count below 25% of the cohort median, when the median
//     is at least 4 (LOW_EFFORT_REL). The median-4 guard avoids spurious flags
//     in small or uniformly-thin deliberations where the signal is not meaningful.
//
// Agents with zero claims are already surfaced by validateCoverage and intentionally
// skipped here to avoid double-warning.
func validateLowEffortPositions(positions []deliberation.Position, claims []claim) []string {
	claimCount := map[string]int{}
	for _, c := range claims {
		claimCount[c.AgentID]++
	}

	// Build counts in agent order from positions (exclude agents with zero claims)
	var counts []int
	type agentCount struct {
		id string
		n  int
	}
	var perAgent []agentCount
	for _, p := range positions {
		n := claimCount[p.AgentID]
		if n == 0 {
			continue
		}
		counts = append(counts, n)
		perAgent = append(perAgent, agentCount{id: p.AgentID, n: n})
	}
	if len(counts) == 0 {
		return nil
	}

	sort.Ints(counts)
	var median float64
	mid := len(counts) / 2
	if len(counts)%2 == 0 {
		median = float64(counts[mid-1]+counts[mid]) / 2
	} else {
		median = float64(counts[mid])
	}

	var warnings []string
	for _, a := range perAgent {
		if a.n < 2 {
			warnings = append(warnings, fmt.Sprintf(
				"LOW_EFFORT_ABS: agent %q produced only %d claim(s) — position may be too thin to surface cruxes",
				a.id, a.n,
			))
			continue
		}
		if median >= 4 && float64(a.n) < 0.25*median {
			warnings = append(warnings, fmt.Sprintf(
				"LOW_EFFORT_REL: agent %q produced %d claim(s) vs cohort median %.1f — position is under-represented relative to peers",
				a.id, a.n, median,
			))
		}
	}
	return warnings
}

// validateCruxProvenance flags cruxes that lack the source-quote and source-position
// breadth needed to trust the claim. A crux derived from a single position or a single
// quote is framing-manipulable: a crafted adversarial position can drag the crux claim
// toward its preferred wording with no counterweight.
//
// This is a lightweight check — provenance is already populated at extraction time;
// we only inspect what's there.
func validateCruxProvenance(cruxes []deliberation.Crux) []string {
	var warnings []string
	for _, c := range cruxes {
		if c.Degenerate {
			continue
		}
		positionIDs := map[string]bool{}
		for _, id := range c.SourcePositionIDs {
			positionIDs[id] = true
		}
		if len(positionIDs) < 2 || len(c.SourceQuotes) < 2 {
			warnings = append(warnings, fmt.Sprintf(
				"THIN_PROVENANCE: crux %q rests on %d source position(s) and %d source quote(s) — framing is under-constrained",
				truncateClaim(c.Claim, 60), len(positionIDs), len(c.SourceQuotes),
			))
		}
	}
	return warnings
}

// validateCruxStability re-runs crux generation on a sampled subtopic N times and
// flags cruxes whose claim text disagrees across candidates. Adversarial inputs
// (AdvSumm-class attacks) can produce stable-but-biased framing that defeats
// variance-based ensemble detection at the token level, so the comparison is
// semantic (handled by the judge closure the caller supplies).
//
// This function is intentionally pure: the caller supplies a candidate generator
// and a judge. No LLM client is imported here. The check is gated at the call
// site by TextAnalyzer.StabilityCheckSamples (0 = off) so the expensive path
// stays opt-in.
//
// Returns warnings; empty slice if the check is disabled or produces no divergence.
func validateCruxStability(
	cruxes []deliberation.Crux,
	samples int,
	generateCandidates func(c deliberation.Crux, n int) ([]string, error),
	judgeSame func(a, b string) (bool, error),
) []string {
	if samples < 2 || generateCandidates == nil || judgeSame == nil {
		return nil
	}
	var warnings []string
	for _, c := range cruxes {
		if c.Degenerate {
			continue
		}
		candidates, err := generateCandidates(c, samples)
		if err != nil || len(candidates) < 2 {
			continue
		}
		agree := 0
		for _, cand := range candidates {
			same, err := judgeSame(c.Claim, cand)
			if err != nil {
				continue
			}
			if same {
				agree++
			}
		}
		if float64(agree)/float64(len(candidates)) < 2.0/3.0 {
			warnings = append(warnings, fmt.Sprintf(
				"CRUX_INSTABILITY: crux %q disagreed with %d/%d regenerated candidates — framing may be adversarially shaped",
				truncateClaim(c.Claim, 60), len(candidates)-agree, len(candidates),
			))
		}
	}
	return warnings
}

// validateAnalysisModelConsistency will re-run a sampled slice of analysis on a
// second model family and flag semantic drift. Adversarial inputs can produce
// stable-but-wrong outputs within a single model family (correlated training data);
// cross-family comparison is the defense.
//
// TODO(darpa-track1): wire a secondary LLM client (Gemini/GPT) and implement.
// Stub exists so the call site and threat-model entry are real.
func validateAnalysisModelConsistency(_ []deliberation.Crux) []string {
	return nil
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
