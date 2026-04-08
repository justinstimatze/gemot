package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// getAnthropicKey reads the API key from ANTHROPIC_API_KEY (standard),
// GEMOT_ANTHROPIC_KEY (legacy), or from .env.
func getAnthropicKey() string {
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return key
	}
	if key := os.Getenv("GEMOT_ANTHROPIC_KEY"); key != "" {
		return key
	}
	if b, err := os.ReadFile(".env"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "ANTHROPIC_API_KEY=") {
				return strings.TrimPrefix(line, "ANTHROPIC_API_KEY=")
			}
			if strings.HasPrefix(line, "GEMOT_ANTHROPIC_KEY=") {
				return strings.TrimPrefix(line, "GEMOT_ANTHROPIC_KEY=")
			}
		}
	}
	return ""
}

// callAnthropic wraps an Anthropic API call with exponential backoff retries on transient errors.
func callAnthropic(client anthropic.Client, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	maxRetries := 5
	for attempt := 0; attempt <= maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		resp, err := client.Messages.New(ctx, params)
		cancel()
		if err == nil {
			return resp, nil
		}
		errMsg := err.Error()
		isRetryable := strings.Contains(errMsg, "429") || strings.Contains(errMsg, "529") ||
			strings.Contains(errMsg, "overloaded") || strings.Contains(errMsg, "rate") ||
			strings.Contains(errMsg, "capacity")
		if !isRetryable || attempt == maxRetries {
			return nil, err
		}
		delay := time.Duration(1<<uint(attempt)) * 2 * time.Second // 2s, 4s, 8s, 16s, 32s
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		fmt.Fprintf(os.Stderr, "    retry %d/%d after %v: %v\n", attempt+1, maxRetries, delay, err)
		time.Sleep(delay)
	}
	return nil, fmt.Errorf("exhausted retries")
}

// extractText gets the first text block from an Anthropic response.
func extractText(resp *anthropic.Message) string {
	for _, block := range resp.Content {
		if block.Type == "text" {
			return block.AsText().Text
		}
	}
	return ""
}

type spotCheckResult struct {
	Sampled int                `json:"sampled"`
	Passed  int                `json:"passed"`
	Failed  []spotCheckFailure `json:"failed,omitempty"`
}

type spotCheckFailure struct {
	Speaker string `json:"speaker"`
	Crux    string `json:"crux"`
	Stance  string `json:"stance"`
	Verdict string `json:"verdict"`
}

func (r *spotCheckResult) PassRate() float64 {
	if r.Sampled == 0 {
		return 0
	}
	return float64(r.Passed) / float64(r.Sampled)
}

type stanceTriple struct {
	speaker string
	crux    string
	stance  string
	quotes  []string
}

// findAllQuotesForSpeaker returns all quote texts attributed to a speaker.
func findAllQuotesForSpeaker(data *ReportData, speakerName string) []string {
	sourceIDs := map[string]bool{}
	for _, s := range data.Sources {
		if strings.EqualFold(s.Interview, speakerName) {
			sourceIDs[s.ID] = true
		}
	}
	var quotes []string
	for _, topic := range data.Topics {
		for _, sub := range topic.Subtopics {
			for _, claim := range sub.Claims {
				quotes = append(quotes, collectQuotes(claim, sourceIDs)...)
			}
		}
	}
	return quotes
}

func collectQuotes(c Claim, sourceIDs map[string]bool) []string {
	var quotes []string
	for _, q := range c.Quotes {
		if sourceIDs[q.Reference.SourceID] && q.Text != "" {
			quotes = append(quotes, q.Text)
		}
	}
	for _, sc := range c.SimilarClaims {
		quotes = append(quotes, collectQuotes(sc, sourceIDs)...)
	}
	return quotes
}

// cruxForCheck is a simplified crux for spot-checking output quality.
type cruxForCheck struct {
	Claim   string
	Stances []stanceForCheck
	Agree   []string
	Disagree []string
}

type stanceForCheck struct {
	AgentID   string
	Value     int
	Qualifier string
}

// agentIDToSpeakerName converts an agent ID like "t3c-speaker-speaker-x" or
// "t3c-steelman-speaker-x" back to a speaker name for quote lookup.
func agentIDToSpeakerName(id string) string {
	// Strip known prefixes
	for _, prefix := range []string{"t3c-speaker-", "t3c-steelman-"} {
		if strings.HasPrefix(id, prefix) {
			slug := strings.TrimPrefix(id, prefix)
			return strings.ReplaceAll(slug, "-", " ")
		}
	}
	return ""
}

// qualifiedStanceLabel returns a human-readable label for a 5-point stance value.
func qualifiedStanceLabel(value int) string {
	switch value {
	case 2:
		return "+2 (strongly agree)"
	case 1:
		return "+1 (agree with caveats)"
	case 0:
		return "0 (neutral)"
	case -1:
		return "-1 (disagree with caveats)"
	case -2:
		return "-2 (strongly disagree)"
	default:
		return fmt.Sprintf("%+d", value)
	}
}

// runCruxSpotCheck validates gemot's crux agent assignments against source quotes.
// This checks the OUTPUT quality (are our crux assignments correct?) rather than
// the INPUT quality (was the T3C matrix correct?).
func runCruxSpotCheck(cruxes []cruxForCheck, data *ReportData, sampleRate float64) *spotCheckResult {
	apiKey := getAnthropicKey()
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "  crux-spot-check: ANTHROPIC_API_KEY required\n")
		os.Exit(1)
	}

	// Collect all (speaker, crux_claim, stance) triples
	var triples []stanceTriple
	for _, crux := range cruxes {
		if len(crux.Stances) > 0 {
			// 5-point qualified stances
			for _, st := range crux.Stances {
				if isStructuralAgent(st.AgentID) {
					continue
				}
				speakerName := agentIDToSpeakerName(st.AgentID)
				if speakerName == "" {
					continue
				}
				quotes := findAllQuotesForSpeaker(data, speakerName)
				if len(quotes) == 0 {
					continue
				}
				if len(quotes) > 10 {
					quotes = quotes[:10]
				}
				label := qualifiedStanceLabel(st.Value)
				if st.Qualifier != "" {
					label += " — " + st.Qualifier
				}
				triples = append(triples, stanceTriple{
					speaker: speakerName,
					crux:    crux.Claim,
					stance:  label,
					quotes:  quotes,
				})
			}
		} else {
			// Old-style agree/disagree lists
			for _, agent := range crux.Agree {
				if isStructuralAgent(agent) {
					continue
				}
				speakerName := agentIDToSpeakerName(agent)
				if speakerName == "" {
					continue
				}
				quotes := findAllQuotesForSpeaker(data, speakerName)
				if len(quotes) == 0 {
					continue
				}
				if len(quotes) > 10 {
					quotes = quotes[:10]
				}
				triples = append(triples, stanceTriple{
					speaker: speakerName,
					crux:    crux.Claim,
					stance:  "agree",
					quotes:  quotes,
				})
			}
			for _, agent := range crux.Disagree {
				if isStructuralAgent(agent) {
					continue
				}
				speakerName := agentIDToSpeakerName(agent)
				if speakerName == "" {
					continue
				}
				quotes := findAllQuotesForSpeaker(data, speakerName)
				if len(quotes) == 0 {
					continue
				}
				if len(quotes) > 10 {
					quotes = quotes[:10]
				}
				triples = append(triples, stanceTriple{
					speaker: speakerName,
					crux:    crux.Claim,
					stance:  "disagree",
					quotes:  quotes,
				})
			}
		}
	}

	if len(triples) == 0 {
		return nil
	}

	// Sample
	nSample := max(5, int(float64(len(triples))*sampleRate))
	if nSample > len(triples) {
		nSample = len(triples)
	}
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0))
	rng.Shuffle(len(triples), func(i, j int) {
		triples[i], triples[j] = triples[j], triples[i]
	})
	sample := triples[:nSample]

	fmt.Fprintf(os.Stderr, "  crux-spot-check: verifying %d/%d crux assignments with Haiku...\n", nSample, len(triples))

	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	result := &spotCheckResult{Sampled: nSample}

	for _, t := range sample {
		quotesStr := strings.Join(t.quotes, "\n- ")
		prompt := fmt.Sprintf(
			"Speaker: %s\nClassified as: %s\nOn crux claim: \"%s\"\n\nTheir actual quotes:\n- %s\n\nDoes this classification accurately represent their position based on these quotes? Answer YES or NO, then a one-sentence reason.",
			t.speaker, t.stance, t.crux, quotesStr,
		)

		resp, err := callAnthropic(client, anthropic.MessageNewParams{
			Model:     "claude-haiku-4-5",
			MaxTokens: 150,
			System: []anthropic.TextBlockParam{
				{Text: "You verify whether a speaker's quotes support a given stance classification on a crux claim from a deliberation analysis. Be strict: if the quotes don't clearly support the classification, answer NO."},
			},
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "    skip %s: %v\n", t.speaker, err)
			result.Sampled--
			continue
		}

		answer := extractText(resp)
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(answer)), "YES") {
			result.Passed++
		} else {
			result.Failed = append(result.Failed, spotCheckFailure{
				Speaker: t.speaker,
				Crux:    t.crux,
				Stance:  t.stance,
				Verdict: answer,
			})
		}
	}

	passRate := result.PassRate() * 100
	fmt.Fprintf(os.Stderr, "  crux-spot-check: %d/%d passed (%.0f%%)\n", result.Passed, result.Sampled, passRate)
	if len(result.Failed) > 0 {
		for _, f := range result.Failed {
			fmt.Fprintf(os.Stderr, "    FAIL: %s (%s) on: %s\n", f.Speaker, f.Stance, f.Crux)
		}
	}

	return result
}

// runSpotCheck samples agent-stance assignments and verifies them against source quotes using Haiku.
func runSpotCheck(data *ReportData, sampleRate float64) *spotCheckResult {
	apiKey := getAnthropicKey()
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "  spot-check: ANTHROPIC_API_KEY required\n")
		os.Exit(1)
	}

	if data.AddOns == nil || data.AddOns.SpeakerCruxMatrix == nil {
		return nil
	}
	matrix := data.AddOns.SpeakerCruxMatrix

	// Collect all (speaker, crux, stance) triples with source quotes
	var triples []stanceTriple
	for i, speaker := range matrix.Speakers {
		speakerName := parseSpeakerID(speaker)
		quotes := findAllQuotesForSpeaker(data, speakerName)
		if len(quotes) == 0 {
			continue
		}
		// Cap quotes to avoid huge prompts
		if len(quotes) > 10 {
			quotes = quotes[:10]
		}
		for j, label := range matrix.CruxLabels {
			if i >= len(matrix.Matrix) || j >= len(matrix.Matrix[i]) {
				continue
			}
			stance := matrix.Matrix[i][j]
			if stance != "agree" && stance != "disagree" {
				continue
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
			triples = append(triples, stanceTriple{
				speaker: speakerName,
				crux:    cruxClaim,
				stance:  stance,
				quotes:  quotes,
			})
		}
	}

	if len(triples) == 0 {
		return nil
	}

	// Sample
	nSample := max(5, int(float64(len(triples))*sampleRate))
	if nSample > len(triples) {
		nSample = len(triples)
	}
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0))
	rng.Shuffle(len(triples), func(i, j int) {
		triples[i], triples[j] = triples[j], triples[i]
	})
	sample := triples[:nSample]

	fmt.Fprintf(os.Stderr, "  spot-check: verifying %d/%d stances with Haiku...\n", nSample, len(triples))

	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	result := &spotCheckResult{Sampled: nSample}

	for _, t := range sample {
		quotesStr := strings.Join(t.quotes, "\n- ")
		prompt := fmt.Sprintf(
			"Speaker: %s\nClassified as: %s\nClaim: \"%s\"\n\nTheir actual quotes:\n- %s\n\nDoes the '%s' classification accurately represent their position based on these quotes? Answer YES or NO, then a one-sentence reason.",
			t.speaker, t.stance, t.crux, quotesStr, t.stance,
		)

		resp, err := callAnthropic(client, anthropic.MessageNewParams{
			Model:     "claude-haiku-4-5",
			MaxTokens: 150,
			System: []anthropic.TextBlockParam{
				{Text: "You verify whether a speaker's quotes support a given stance classification on a claim. Be strict: if the quotes don't clearly support the classification, answer NO."},
			},
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "    skip %s: %v\n", t.speaker, err)
			result.Sampled-- // don't count failed API calls in denominator
			continue
		}

		answer := extractText(resp)

		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(answer)), "YES") {
			result.Passed++
		} else {
			result.Failed = append(result.Failed, spotCheckFailure{
				Speaker: t.speaker,
				Crux:    t.crux,
				Stance:  t.stance,
				Verdict: answer,
			})
		}
	}

	passRate := result.PassRate() * 100
	fmt.Fprintf(os.Stderr, "  spot-check: %d/%d passed (%.0f%%)\n", result.Passed, result.Sampled, passRate)
	if len(result.Failed) > 0 {
		for _, f := range result.Failed {
			fmt.Fprintf(os.Stderr, "    FAIL: %s (%s) on: %s\n", f.Speaker, f.Stance, f.Crux)
		}
	}

	return result
}
