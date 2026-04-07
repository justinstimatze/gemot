package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type verifyResult struct {
	Total      int
	Checked    int
	Downgraded int
	Details    []verifyDetail
	ScoreDist  [6]int // index 0 unused, 1-5 count stances at each score
}

type verifyDetail struct {
	Speaker    string
	Crux       string
	OrigStance string
	Score      int
	Reason     string
}

// verifyStances checks agree and disagree stances in the speaker-crux matrix
// against source quotes using a 1-5 grounding score:
//
//	5 = quotes explicitly support this stance
//	4 = quotes clearly align but don't address exact claim
//	3 = quotes relevant but stance is an interpretation
//	2 = quotes tangentially related, stance is a stretch
//	1 = quotes don't address this topic
//
// Stances scoring 1-3 are downgraded to "no_position". Modifies data in place.
func verifyStances(data *ReportData) *verifyResult {
	apiKey := getAnthropicKey()
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "  verify-stances: GEMOT_ANTHROPIC_KEY or ANTHROPIC_API_KEY required\n")
		os.Exit(1)
	}

	if data.AddOns == nil || data.AddOns.SpeakerCruxMatrix == nil {
		return nil
	}
	matrix := data.AddOns.SpeakerCruxMatrix

	total := 0
	for i := range matrix.Speakers {
		if i >= len(matrix.Matrix) {
			continue
		}
		for j := range matrix.CruxLabels {
			if j < len(matrix.Matrix[i]) {
				s := matrix.Matrix[i][j]
				if s == "agree" || s == "disagree" {
					total++
				}
			}
		}
	}
	if total == 0 {
		return &verifyResult{}
	}

	fmt.Fprintf(os.Stderr, "  verify-stances: scoring %d stances (1-5 grounding scale)...\n", total)

	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	result := &verifyResult{Total: total}

	for i, speaker := range matrix.Speakers {
		if i >= len(matrix.Matrix) {
			continue
		}
		speakerName := parseSpeakerID(speaker)
		var quotes []string

		for j, label := range matrix.CruxLabels {
			if j >= len(matrix.Matrix[i]) {
				continue
			}
			stance := matrix.Matrix[i][j]
			if stance != "agree" && stance != "disagree" {
				continue
			}
			result.Checked++

			if quotes == nil {
				quotes = findAllQuotesForSpeaker(data, speakerName)
				if len(quotes) > 12 {
					quotes = quotes[:12]
				}
			}

			normalizedLabel := normLabel(label)
			cruxClaim := ""
			for _, c := range data.AddOns.SubtopicCruxes {
				if cruxLabel(c.Topic, c.Subtopic) == normalizedLabel {
					cruxClaim = c.CruxClaim
					break
				}
			}
			if cruxClaim == "" {
				continue
			}

			// No quotes → score 1
			if len(quotes) == 0 {
				score := 1
				result.ScoreDist[score]++
				matrix.Matrix[i][j] = "no_position"
				result.Downgraded++
				result.Details = append(result.Details, verifyDetail{
					Speaker: speakerName, Crux: cruxClaim[:min(80, len(cruxClaim))],
					OrigStance: stance, Score: score, Reason: "no source quotes found",
				})
				updateSubtopicCrux(data, label, speaker, stance)
				fmt.Fprintf(os.Stderr, "    [1] %s: %s — no quotes\n", speakerName, stance)
				continue
			}

			score, reason := scoreStance(client, speakerName, cruxClaim, quotes, stance)
			result.ScoreDist[score]++

			if score <= 3 {
				matrix.Matrix[i][j] = "no_position"
				result.Downgraded++
				result.Details = append(result.Details, verifyDetail{
					Speaker: speakerName, Crux: cruxClaim[:min(80, len(cruxClaim))],
					OrigStance: stance, Score: score, Reason: reason,
				})
				updateSubtopicCrux(data, label, speaker, stance)
				fmt.Fprintf(os.Stderr, "    [%d] %s: %s — downgraded\n", score, speakerName, stance)
			} else {
				fmt.Fprintf(os.Stderr, "    [%d] %s: %s — kept\n", score, speakerName, stance)
			}
		}
	}

	kept := result.Checked - result.Downgraded
	fmt.Fprintf(os.Stderr, "  verify-stances: %d kept (score 4-5), %d downgraded (score 1-3)\n", kept, result.Downgraded)
	return result
}

func scoreStance(client anthropic.Client, speaker, cruxClaim string, quotes []string, stance string) (int, string) {
	quotesStr := strings.Join(quotes, "\n- ")

	stanceVerb := "supports or endorses"
	if stance == "disagree" {
		stanceVerb = "opposes or rejects"
	}

	prompt := fmt.Sprintf(
		"Speaker: %s\nClaim: \"%s\"\nClassified as: %s\n\nSource quotes:\n- %s\n\n"+
			"Rate how well the quotes support the %s classification (1-5):\n"+
			"5 = quotes explicitly %s this claim\n"+
			"4 = quotes clearly align but don't address the exact wording\n"+
			"3 = quotes are relevant but the stance is an interpretation\n"+
			"2 = quotes tangentially related, stance is a stretch\n"+
			"1 = quotes don't address this topic at all\n\n"+
			"Answer with the number (1-5), then a one-sentence reason.",
		speaker, cruxClaim, stance, quotesStr, stance, stanceVerb,
	)

	resp, err := callAnthropic(client, anthropic.MessageNewParams{
		Model:     "claude-haiku-4-5",
		MaxTokens: 150,
		System: []anthropic.TextBlockParam{
			{Text: "You score how well source quotes support a stance classification. Be strict but fair. Score the actual evidence, not what you think the speaker believes."},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return 3, "verification failed — treated as uncertain"
	}

	answer := strings.TrimSpace(extractText(resp))

	// Parse score: look for leading digit 1-5
	score := 3 // default: uncertain
	if len(answer) > 0 {
		if n, err := strconv.Atoi(string(answer[0])); err == nil && n >= 1 && n <= 5 {
			score = n
		}
	}

	reason := answer
	// Strip the leading score digit and punctuation
	if len(reason) > 2 {
		reason = strings.TrimLeft(reason[1:], " .-–:,")
		reason = strings.TrimSpace(reason)
	}
	if len(reason) > 120 {
		reason = reason[:117] + "..."
	}

	return score, reason
}

// updateSubtopicCrux moves a speaker from Agree/Disagree to NoPosition in SubtopicCruxes.
func updateSubtopicCrux(data *ReportData, matrixLabel, speaker, origStance string) {
	normalizedLabel := normLabel(matrixLabel)
	for i, c := range data.AddOns.SubtopicCruxes {
		if cruxLabel(c.Topic, c.Subtopic) != normalizedLabel {
			continue
		}
		if origStance == "disagree" {
			var filtered []string
			for _, d := range c.Disagree {
				if d != speaker {
					filtered = append(filtered, d)
				}
			}
			data.AddOns.SubtopicCruxes[i].Disagree = filtered
		} else {
			var filtered []string
			for _, a := range c.Agree {
				if a != speaker {
					filtered = append(filtered, a)
				}
			}
			data.AddOns.SubtopicCruxes[i].Agree = filtered
		}
		data.AddOns.SubtopicCruxes[i].NoPosition = append(c.NoPosition, speaker)
		return
	}
}
