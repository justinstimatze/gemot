package analysis

import (
	"math"
	"strings"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

// QualityScore holds evaluation metrics for an analysis result.
type QualityScore struct {
	CruxStructureScore float64  `json:"crux_structure_score"` // 0-1: are cruxes well-formed?
	ExplanationScore   float64  `json:"explanation_score"`    // 0-1: explanation quality
	CoverageScore      float64  `json:"coverage_score"`       // 0-1: what % of agents appear in cruxes?
	BalanceScore       float64  `json:"balance_score"`        // 0-1: are crux sides balanced?
	OverallScore       float64  `json:"overall_score"`        // 0-1: weighted composite
	Issues             []string `json:"issues,omitempty"`
}

// EvaluateResult scores the quality of an analysis result.
// This is a deterministic, fast evaluation — no LLM calls.
// Adapted from T3C's evaluation suite (common/evaluations/).
func EvaluateResult(result *deliberation.AnalysisResult) QualityScore {
	score := QualityScore{}
	if result == nil {
		return score
	}

	// 1. Crux structure: are cruxes well-formed?
	score.CruxStructureScore = scoreCruxStructure(result.Cruxes, &score.Issues)

	// 2. Explanation quality
	score.ExplanationScore = scoreExplanations(result.Cruxes, &score.Issues)

	// 3. Coverage: what % of agents appear in at least one crux?
	score.CoverageScore = scoreCoverage(result.Cruxes, result.AgentCount, &score.Issues)

	// 4. Balance: are crux sides reasonably balanced?
	score.BalanceScore = scoreBalance(result.Cruxes, &score.Issues)

	// Weighted composite
	score.OverallScore = 0.25*score.CruxStructureScore +
		0.25*score.ExplanationScore +
		0.25*score.CoverageScore +
		0.25*score.BalanceScore

	return score
}

func scoreCruxStructure(cruxes []deliberation.Crux, issues *[]string) float64 {
	if len(cruxes) == 0 {
		*issues = append(*issues, "no cruxes found")
		return 0
	}

	validCount := 0
	for _, c := range cruxes {
		valid := true
		if strings.TrimSpace(c.Claim) == "" {
			*issues = append(*issues, "crux has empty claim")
			valid = false
		}
		if len(c.AgreeAgents) == 0 {
			*issues = append(*issues, "crux has no agree agents: "+truncateClaim(c.Claim, 40))
			valid = false
		}
		if len(c.DisagreeAgents) == 0 {
			*issues = append(*issues, "crux has no disagree agents: "+truncateClaim(c.Claim, 40))
			valid = false
		}
		if valid {
			validCount++
		}
	}
	return float64(validCount) / float64(len(cruxes))
}

func scoreExplanations(cruxes []deliberation.Crux, issues *[]string) float64 {
	if len(cruxes) == 0 {
		return 0
	}

	totalScore := 0.0
	for _, c := range cruxes {
		s := 1.0

		// Length checks (adapted from T3C explanationQualityScorer)
		if len(c.Explanation) < 30 {
			*issues = append(*issues, "explanation too brief: "+truncateClaim(c.Claim, 40))
			s -= 0.4
		}
		if len(c.Explanation) > 500 {
			s -= 0.2
		}

		// Contrastive reasoning (does the explanation explain WHY sides differ?)
		contrastiveKeywords := []string{"while", "whereas", "however", "although", "but", "in contrast", "on the other hand", "tension", "divided"}
		hasContrast := false
		lower := strings.ToLower(c.Explanation)
		for _, kw := range contrastiveKeywords {
			if strings.Contains(lower, kw) {
				hasContrast = true
				break
			}
		}
		if !hasContrast && len(c.DisagreeAgents) > 0 {
			*issues = append(*issues, "explanation lacks contrastive reasoning: "+truncateClaim(c.Claim, 40))
			s -= 0.3
		}

		if s < 0 {
			s = 0
		}
		totalScore += s
	}
	return totalScore / float64(len(cruxes))
}

func scoreCoverage(cruxes []deliberation.Crux, agentCount int, issues *[]string) float64 {
	if agentCount == 0 {
		return 0
	}

	agentsInCruxes := map[string]bool{}
	for _, c := range cruxes {
		for _, a := range c.AgreeAgents {
			agentsInCruxes[a] = true
		}
		for _, a := range c.DisagreeAgents {
			agentsInCruxes[a] = true
		}
		for _, a := range c.NoClearPosition {
			agentsInCruxes[a] = true
		}
	}

	coverage := float64(len(agentsInCruxes)) / float64(agentCount)
	if coverage < 0.5 {
		*issues = append(*issues, "less than 50% of agents appear in cruxes")
	}
	return coverage
}

func scoreBalance(cruxes []deliberation.Crux, issues *[]string) float64 {
	if len(cruxes) == 0 {
		return 0
	}

	totalScore := 0.0
	for _, c := range cruxes {
		agree := float64(len(c.AgreeAgents))
		disagree := float64(len(c.DisagreeAgents))
		total := agree + disagree
		if total == 0 {
			continue
		}
		// Perfect balance = 1.0, completely one-sided = 0.0
		// min(agree, disagree) * 2 / total
		balance := 2.0 * math.Min(agree, disagree) / total
		if balance < 0.3 {
			*issues = append(*issues, "crux is heavily one-sided: "+truncateClaim(c.Claim, 40))
		}
		totalScore += balance
	}
	return totalScore / float64(len(cruxes))
}
