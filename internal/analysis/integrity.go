package analysis

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

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

// defaultCrossFamilySampleK is the number of highest-controversy
// cruxes the cross-family consistency check re-scores on the secondary
// family. Each sampled crux costs one secondary-LLM call (stances for
// all involved agents in one structured response), so at K=5 the
// per-round overhead is ~5 Gemini-Flash-tier calls — a single-digit
// cent on current pricing.
const defaultCrossFamilySampleK = 5

// driftFlipRatio is the per-crux threshold at which CROSS_FAMILY_DRIFT
// fires. Strict majority of agents on a given crux must flip sign
// between primary and secondary — a lower bar would fire on noise
// (one agent wobbling on wording differences), a higher bar would
// miss real drift where the secondary fundamentally disagrees.
const driftFlipRatio = 0.5

// validateAnalysisModelConsistency re-scores the top-K highest-
// controversy cruxes on a secondary model family and emits
// CROSS_FAMILY_DRIFT when strict majority of agents flip sign. No-op
// when a.SecondaryLLM is nil, which keeps the feature off-by-default
// and preserves zero-cost behaviour for deployments that haven't
// configured a secondary.
//
// This is the Track 1 OOD mitigation called out in §3 of the
// DARPA-PS-26-09 abstract: adversarial inputs can produce stable-but-
// wrong outputs that defeat variance-based ensemble detection inside
// a single model family. Cross-family comparison — same structured
// question asked of a model with a different pretraining mix — is the
// intended defense. Independence between frontier labs is imperfect
// (shared benchmark corpora, convergent RLHF); the caveat is recorded
// in THREAT_MODEL.
func (a *TextAnalyzer) validateAnalysisModelConsistency(
	ctx context.Context,
	cruxes []deliberation.Crux,
	positions []deliberation.Position,
) []string {
	if a.SecondaryLLM == nil || len(cruxes) == 0 {
		return nil
	}

	k := a.SecondarySampleK
	if k <= 0 {
		k = defaultCrossFamilySampleK
	}

	// Latest position per agent: cross-family re-scoring asks the
	// secondary to judge stance against the most recent articulation,
	// matching the source-of-truth the primary would have used.
	latest := latestPositionByAgent(positions)

	sampled := selectDriftSampleCruxes(cruxes, k)

	var warnings []string
	for _, c := range sampled {
		primary := primaryStances(c)
		if len(primary) < 2 {
			continue
		}
		secondary, err := a.scoreCruxStancesSecondary(ctx, c, primary, latest)
		if err != nil {
			slog.Warn("cross-family consistency call failed",
				"provider", a.SecondaryLLM.Provider(),
				"model", a.SecondaryLLM.Model(),
				"err", err)
			continue
		}
		flips := 0
		compared := 0
		for agent, p := range primary {
			s, ok := secondary[agent]
			if !ok {
				continue
			}
			compared++
			if p != 0 && s != 0 && p != s {
				flips++
			}
		}
		if compared == 0 {
			continue
		}
		ratio := float64(flips) / float64(compared)
		if ratio > driftFlipRatio {
			warnings = append(warnings, fmt.Sprintf(
				"CROSS_FAMILY_DRIFT: secondary model %q (via %s) flipped %d/%d agent stances on crux %q — primary-family output may be an artifact of correlated training data",
				a.SecondaryLLM.Model(), a.SecondaryLLM.Provider(),
				flips, compared, truncateClaim(c.Claim, 80),
			))
		}
	}
	return warnings
}

// latestPositionByAgent picks the highest-round, most-recent position
// per agent. Earlier rounds are returned only when no later position
// exists — matches the source-of-truth the primary analysis pipeline
// uses when constructing cruxes over multi-round deliberations.
func latestPositionByAgent(positions []deliberation.Position) map[string]deliberation.Position {
	out := map[string]deliberation.Position{}
	for _, p := range positions {
		cur, ok := out[p.AgentID]
		if !ok {
			out[p.AgentID] = p
			continue
		}
		if p.Round > cur.Round || (p.Round == cur.Round && p.CreatedAt.After(cur.CreatedAt)) {
			out[p.AgentID] = p
		}
	}
	return out
}

// selectDriftSampleCruxes returns up to k cruxes ordered by descending
// controversy. Degenerate and sub-threshold cruxes are skipped — they
// carry no useful drift signal.
func selectDriftSampleCruxes(cruxes []deliberation.Crux, k int) []deliberation.Crux {
	candidates := make([]deliberation.Crux, 0, len(cruxes))
	for _, c := range cruxes {
		if c.Degenerate {
			continue
		}
		if len(c.AgreeAgents)+len(c.DisagreeAgents) < 2 {
			continue
		}
		candidates = append(candidates, c)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].ControversyScore > candidates[j].ControversyScore
	})
	if len(candidates) > k {
		candidates = candidates[:k]
	}
	return candidates
}

// primaryStances reduces a crux's AgreeAgents / DisagreeAgents into a
// per-agent sign map used as the ground-truth side of the comparison.
// NoClearPosition agents are omitted so they can't mask a real flip.
func primaryStances(c deliberation.Crux) map[string]int {
	out := map[string]int{}
	for _, a := range c.AgreeAgents {
		out[a] = 1
	}
	for _, a := range c.DisagreeAgents {
		out[a] = -1
	}
	return out
}

// crossFamilySystemPrompt and crossFamilyUserPrompt are kept short and
// T3C-style: anonymize agents to numeric IDs, ask for structured
// per-agent stance. The secondary need not reproduce the primary's
// crux taxonomy — just judge agreement against the specific claim.
const crossFamilySystemPrompt = `You are an impartial stance classifier. You will be given a single claim and a list of numbered participants with their positions. For each participant, decide whether their position agrees, disagrees, or is unclear relative to the claim. Do not invent agreement; if a participant's text does not clearly take a side, mark it as unclear.`

func (a *TextAnalyzer) scoreCruxStancesSecondary(
	ctx context.Context,
	c deliberation.Crux,
	primary map[string]int,
	latest map[string]deliberation.Position,
) (map[string]int, error) {
	// Anonymize: secondary should judge stance without any bias from
	// agent naming or ordering leakage.
	numToAgent := map[string]string{}
	var lines []string
	idx := 0
	for agent := range primary {
		pos, ok := latest[agent]
		if !ok || strings.TrimSpace(pos.Content) == "" {
			continue
		}
		num := fmt.Sprintf("%d", idx)
		numToAgent[num] = agent
		lines = append(lines, fmt.Sprintf("Participant %s:\n%s", num, strings.TrimSpace(pos.Content)))
		idx++
	}
	if len(lines) < 2 {
		return nil, fmt.Errorf("insufficient positions for cross-family stance scoring")
	}

	prompt := fmt.Sprintf("Claim:\n%s\n\n%s",
		strings.TrimSpace(c.Claim),
		strings.Join(lines, "\n\n"),
	)

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"stances": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"participant": map[string]any{"type": "string"},
						"stance":      map[string]any{"type": "string", "enum": []string{"agree", "disagree", "unclear"}},
					},
					"required": []string{"participant", "stance"},
				},
			},
		},
		"required": []string{"stances"},
	}

	var out struct {
		Stances []struct {
			Participant string `json:"participant"`
			Stance      string `json:"stance"`
		} `json:"stances"`
	}
	if err := a.SecondaryLLM.StructuredOutput(ctx, crossFamilySystemPrompt, prompt, schema, &out); err != nil {
		return nil, err
	}

	result := map[string]int{}
	for _, s := range out.Stances {
		agent, ok := numToAgent[s.Participant]
		if !ok {
			continue
		}
		switch strings.ToLower(s.Stance) {
		case "agree":
			result[agent] = 1
		case "disagree":
			result[agent] = -1
		default:
			result[agent] = 0
		}
	}
	return result, nil
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
