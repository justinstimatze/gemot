package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type verifyResult struct {
	Total      int // total stances checked
	Checked    int // stances where verification was attempted
	Downgraded int
	Details    []verifyDetail
}

type verifyDetail struct {
	Speaker      string
	Crux         string
	OrigStance   string // "agree" or "disagree"
	Reason       string
}

// verifyStances checks agree and disagree stances in the speaker-crux matrix
// against source quotes. Ungrounded stances are downgraded to "no_position".
// Modifies the data in place.
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

	// Count non-neutral stances
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

	fmt.Fprintf(os.Stderr, "  verify-stances: checking %d stances...\n", total)

	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	result := &verifyResult{Total: total}

	for i, speaker := range matrix.Speakers {
		if i >= len(matrix.Matrix) {
			continue
		}
		speakerName := parseSpeakerID(speaker)
		var quotes []string // lazy-loaded per speaker

		for j, label := range matrix.CruxLabels {
			if j >= len(matrix.Matrix[i]) {
				continue
			}
			stance := matrix.Matrix[i][j]
			if stance != "agree" && stance != "disagree" {
				continue
			}
			result.Checked++

			// Lazy-load quotes
			if quotes == nil {
				quotes = findAllQuotesForSpeaker(data, speakerName)
				if len(quotes) > 12 {
					quotes = quotes[:12]
				}
			}

			// Find crux claim text
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

			// No quotes → automatic downgrade
			if len(quotes) == 0 {
				matrix.Matrix[i][j] = "no_position"
				result.Downgraded++
				result.Details = append(result.Details, verifyDetail{
					Speaker: speakerName, Crux: cruxClaim[:min(80, len(cruxClaim))],
					OrigStance: stance, Reason: "no source quotes found",
				})
				updateSubtopicCrux(data, label, speaker, stance)
				fmt.Fprintf(os.Stderr, "    ↓ %s: %s not grounded — no quotes\n", speakerName, stance)
				continue
			}

			// Build stance-specific prompt
			system, prompt := verifyPrompt(speakerName, cruxClaim, quotes, stance)

			resp, err := callAnthropic(client, anthropic.MessageNewParams{
				Model:     "claude-haiku-4-5",
				MaxTokens: 150,
				System:    []anthropic.TextBlockParam{{Text: system}},
				Messages: []anthropic.MessageParam{
					anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
				},
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "    ? %s: verification failed, keeping %s\n", speakerName, stance)
				continue
			}

			answer := extractText(resp)
			if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(answer)), "YES") {
				matrix.Matrix[i][j] = "no_position"
				result.Downgraded++

				reason := answer
				if len(reason) > 120 {
					reason = reason[:117] + "..."
				}
				result.Details = append(result.Details, verifyDetail{
					Speaker: speakerName, Crux: cruxClaim[:min(80, len(cruxClaim))],
					OrigStance: stance, Reason: reason,
				})
				updateSubtopicCrux(data, label, speaker, stance)
				fmt.Fprintf(os.Stderr, "    ↓ %s: %s not grounded (downgraded)\n", speakerName, stance)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "  verify-stances: %d/%d stances downgraded to no_position\n", result.Downgraded, result.Checked)
	return result
}

func verifyPrompt(speaker, cruxClaim string, quotes []string, stance string) (system, prompt string) {
	quotesStr := strings.Join(quotes, "\n- ")

	if stance == "disagree" {
		system = "You verify whether source quotes support a DISAGREE stance classification. " +
			"Be strict: absence of agreement is NOT disagreement. " +
			"The speaker must explicitly oppose or reject the claim for DISAGREE to be justified."
		prompt = fmt.Sprintf(
			"Speaker: %s\nClaim: \"%s\"\n\nTheir source quotes:\n- %s\n\n"+
				"T3C classified this speaker as DISAGREE on this claim. "+
				"Do the quotes provide clear evidence that this speaker opposes or rejects this claim? "+
				"Answer YES if the quotes show explicit disagreement. "+
				"Answer NO if the quotes are silent, show nuanced/partial agreement, or don't clearly oppose the claim.",
			speaker, cruxClaim, quotesStr,
		)
	} else {
		system = "You verify whether source quotes support an AGREE stance classification. " +
			"Be strict: the speaker must express views that clearly align with the claim. " +
			"Tangential mentions of the topic are NOT agreement."
		prompt = fmt.Sprintf(
			"Speaker: %s\nClaim: \"%s\"\n\nTheir source quotes:\n- %s\n\n"+
				"T3C classified this speaker as AGREE on this claim. "+
				"Do the quotes provide clear evidence that this speaker supports or endorses this claim? "+
				"Answer YES if the quotes show explicit agreement. "+
				"Answer NO if the quotes only tangentially mention the topic, address a different aspect, or don't clearly support the specific claim.",
			speaker, cruxClaim, quotesStr,
		)
	}
	return
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
