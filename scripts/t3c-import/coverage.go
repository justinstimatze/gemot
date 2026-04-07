package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type coverageGap struct {
	Position        string // the unchallenged position
	MissingPerspective string // what perspective would challenge it
	SuggestedSource    string // real-world source that would likely contest it
}

type coverageResult struct {
	Gaps []coverageGap
}

// runCoverageAudit identifies missing perspectives for unchallenged positions.
// Uses Haiku to ask "what perspective is absent?" for each consensus/unchallenged item.
func runCoverageAudit(r1Analysis *analysisResult, reportTitle string) *coverageResult {
	apiKey := getAnthropicKey()
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "  coverage audit: GEMOT_ANTHROPIC_KEY or ANTHROPIC_API_KEY required\n")
		os.Exit(1)
	}

	// Collect unchallenged positions (consensus statements)
	if len(r1Analysis.ConsensusStatements) == 0 {
		return nil
	}

	// Also find cruxes where one side has very few agents
	var lopsided []string
	for _, c := range r1Analysis.Cruxes {
		nAgree := len(c.Agree)
		nDisagree := len(c.Disagree)
		if (nAgree <= 1 && nDisagree >= 3) || (nDisagree <= 1 && nAgree >= 3) {
			minority := "disagree"
			if nDisagree > nAgree {
				minority = "agree"
			}
			lopsided = append(lopsided, fmt.Sprintf("[%s side underrepresented] %s", minority, c.Claim[:min(120, len(c.Claim))]))
		}
	}

	// Build the list of positions to audit
	var positions []string
	for _, cs := range r1Analysis.ConsensusStatements[:min(5, len(r1Analysis.ConsensusStatements))] {
		content := cs.Content
		if len(content) > 150 {
			content = content[:147] + "..."
		}
		positions = append(positions, "[unchallenged] "+content)
	}
	positions = append(positions, lopsided...)

	if len(positions) == 0 {
		return nil
	}

	fmt.Fprintf(os.Stderr, "  coverage audit: checking %d positions for missing perspectives...\n", len(positions))

	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	prompt := fmt.Sprintf(
		"These positions from a deliberation about %q were either unchallenged (no agent disagreed) or had very few agents on one side:\n\n%s\n\n"+
			"For each position, identify:\n"+
			"1. What real-world perspective or stakeholder group is ABSENT that would likely challenge it?\n"+
			"2. Name a specific organization, researcher, or community that would contest this.\n\n"+
			"Be concrete. Don't say 'critics' — name the specific perspective (e.g., 'labor unions concerned about job displacement', 'Global South AI researchers', 'open-source AI advocates').\n\n"+
			"Format: one line per position, structured as:\n"+
			"POSITION: [first few words] | MISSING: [perspective] | SOURCE: [specific entity]",
		reportTitle, strings.Join(positions, "\n"),
	)

	resp, err := callAnthropic(client, anthropic.MessageNewParams{
		Model:     "claude-haiku-4-5",
		MaxTokens: 800,
		System: []anthropic.TextBlockParam{
			{Text: "You identify absent perspectives in deliberations. Be specific about who is missing and why they would disagree. Focus on real organizations and communities, not abstract categories."},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "  coverage audit: LLM call failed: %v\n", err)
		return nil
	}

	answer := extractText(resp)

	result := &coverageResult{}
	for _, line := range strings.Split(answer, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "MISSING:") {
			continue
		}

		gap := coverageGap{}

		// Parse structured response
		parts := strings.SplitN(line, "|", 3)
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "POSITION:") {
				gap.Position = strings.TrimSpace(strings.TrimPrefix(part, "POSITION:"))
			} else if strings.HasPrefix(part, "MISSING:") {
				gap.MissingPerspective = strings.TrimSpace(strings.TrimPrefix(part, "MISSING:"))
			} else if strings.HasPrefix(part, "SOURCE:") {
				gap.SuggestedSource = strings.TrimSpace(strings.TrimPrefix(part, "SOURCE:"))
			}
		}

		if gap.MissingPerspective != "" {
			result.Gaps = append(result.Gaps, gap)
		}
	}

	fmt.Fprintf(os.Stderr, "  coverage audit: found %d gaps\n", len(result.Gaps))
	return result
}
