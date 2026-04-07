package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type verifyResult struct {
	Total      int
	Checked    int
	Downgraded int
	Details    []verifyDetail
}

type verifyDetail struct {
	Speaker string
	Crux    string
	Reason  string
}

// verifyDisagreeStances checks all "disagree" stances in the speaker-crux matrix
// against source quotes. Ungrounded disagree stances are downgraded to "no_position".
// Modifies the data in place. Only disagree stances are checked — agree stances
// are generally well-grounded since the speaker said something supportive.
func verifyDisagreeStances(data *ReportData) *verifyResult {
	apiKey := getAnthropicKey()
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "  verify-stances: GEMOT_ANTHROPIC_KEY or ANTHROPIC_API_KEY required\n")
		os.Exit(1)
	}

	if data.AddOns == nil || data.AddOns.SpeakerCruxMatrix == nil {
		return nil
	}
	matrix := data.AddOns.SpeakerCruxMatrix

	// Count disagree stances
	total := 0
	for i := range matrix.Speakers {
		if i >= len(matrix.Matrix) {
			continue
		}
		for j := range matrix.CruxLabels {
			if j < len(matrix.Matrix[i]) && matrix.Matrix[i][j] == "disagree" {
				total++
			}
		}
	}
	if total == 0 {
		return &verifyResult{}
	}

	fmt.Fprintf(os.Stderr, "  verify-stances: checking %d disagree stances...\n", total)

	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	result := &verifyResult{Total: total}

	for i, speaker := range matrix.Speakers {
		if i >= len(matrix.Matrix) {
			continue
		}
		speakerName := parseSpeakerID(speaker)
		var quotes []string // lazy-loaded per speaker

		for j, label := range matrix.CruxLabels {
			if j >= len(matrix.Matrix[i]) || matrix.Matrix[i][j] != "disagree" {
				continue
			}
			result.Checked++

			// Lazy-load quotes for this speaker
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
					Speaker: speakerName,
					Crux:    cruxClaim[:min(80, len(cruxClaim))],
					Reason:  "no source quotes found",
				})
				updateSubtopicCrux(data, label, speaker)
				fmt.Fprintf(os.Stderr, "    ↓ %s: no quotes (downgraded)\n", speakerName)
				continue
			}

			// LLM verification
			quotesStr := strings.Join(quotes, "\n- ")
			prompt := fmt.Sprintf(
				"Speaker: %s\nClaim: \"%s\"\n\nTheir source quotes:\n- %s\n\n"+
					"T3C classified this speaker as DISAGREE on this claim. "+
					"Do the quotes provide clear evidence that this speaker opposes or rejects this claim? "+
					"Answer YES if the quotes show explicit disagreement. Answer NO if the quotes are silent on this topic, "+
					"show nuanced/partial agreement, or don't clearly oppose the claim.",
				speakerName, cruxClaim, quotesStr,
			)

			resp, err := callAnthropic(client, anthropic.MessageNewParams{
				Model:     "claude-haiku-4-5",
				MaxTokens: 150,
				System: []anthropic.TextBlockParam{
					{Text: "You verify whether source quotes support a DISAGREE stance classification. Be strict: absence of agreement is NOT disagreement. The speaker must explicitly oppose or reject the claim for DISAGREE to be justified."},
				},
				Messages: []anthropic.MessageParam{
					anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
				},
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "    ? %s: verification failed, keeping stance\n", speakerName)
				continue
			}

			answer := extractText(resp)
			if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(answer)), "YES") {
				// Downgrade to no_position
				matrix.Matrix[i][j] = "no_position"
				result.Downgraded++

				reason := answer
				if len(reason) > 120 {
					reason = reason[:117] + "..."
				}
				result.Details = append(result.Details, verifyDetail{
					Speaker: speakerName,
					Crux:    cruxClaim[:min(80, len(cruxClaim))],
					Reason:  reason,
				})
				updateSubtopicCrux(data, label, speaker)
				fmt.Fprintf(os.Stderr, "    ↓ %s: disagree not grounded (downgraded)\n", speakerName)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "  verify-stances: %d/%d disagree stances downgraded to no_position\n", result.Downgraded, result.Checked)
	return result
}

// updateSubtopicCrux moves a speaker from Disagree to NoPosition in SubtopicCruxes
// to keep the two data structures consistent after a matrix downgrade.
func updateSubtopicCrux(data *ReportData, matrixLabel, speaker string) {
	normalizedLabel := normLabel(matrixLabel)
	for i, c := range data.AddOns.SubtopicCruxes {
		if cruxLabel(c.Topic, c.Subtopic) != normalizedLabel {
			continue
		}
		// Remove from Disagree
		var newDisagree []string
		for _, d := range c.Disagree {
			if d != speaker {
				newDisagree = append(newDisagree, d)
			}
		}
		data.AddOns.SubtopicCruxes[i].Disagree = newDisagree
		// Add to NoPosition
		data.AddOns.SubtopicCruxes[i].NoPosition = append(c.NoPosition, speaker)
		return
	}
}
