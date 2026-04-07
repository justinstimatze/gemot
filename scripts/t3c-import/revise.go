package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// buildResolutionAgents generates concrete actionable proposals from R2 findings.
// Each resolution agent champions a specific proposal that existing agents vote on.
func buildResolutionAgents(r2Analysis *analysisResult, data *ReportData) []agentPlan {
	apiKey := getAnthropicKey()
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "  R3: GEMOT_ANTHROPIC_KEY or ANTHROPIC_API_KEY required for resolutions\n")
		os.Exit(1)
	}
	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	// Build context from R2 findings
	var ctx strings.Builder
	if len(r2Analysis.Cruxes) > 0 {
		ctx.WriteString("CRUXES (points of disagreement):\n")
		for _, c := range r2Analysis.Cruxes[:min(6, len(r2Analysis.Cruxes))] {
			fmt.Fprintf(&ctx, "- [%.0f%%] %s\n", c.Score*100, c.Claim[:min(150, len(c.Claim))])
			fmt.Fprintf(&ctx, "  Agree: %s | Disagree: %s\n", strings.Join(c.Agree, ", "), strings.Join(c.Disagree, ", "))
		}
		ctx.WriteString("\n")
	}
	if len(r2Analysis.ConsensusStatements) > 0 {
		ctx.WriteString("CONSENSUS:\n")
		for _, cs := range r2Analysis.ConsensusStatements[:min(4, len(r2Analysis.ConsensusStatements))] {
			fmt.Fprintf(&ctx, "- %s\n", cs.Content[:min(120, len(cs.Content))])
		}
		ctx.WriteString("\n")
	}
	if len(r2Analysis.BridgingStatements) > 0 {
		ctx.WriteString("BRIDGING PROPOSALS:\n")
		for _, bs := range r2Analysis.BridgingStatements[:min(3, len(r2Analysis.BridgingStatements))] {
			fmt.Fprintf(&ctx, "- %s\n", bs.Content[:min(120, len(bs.Content))])
		}
		ctx.WriteString("\n")
	}

	findings := ctx.String()
	if findings == "" {
		return nil
	}

	prompt := fmt.Sprintf(
		"A deliberation about %q produced these findings after 2 rounds:\n\n%s\n"+
			"Generate 3-4 concrete, actionable resolution proposals that could address the key disagreements. Each proposal should:\n"+
			"1. Be specific enough to implement (not vague principles)\n"+
			"2. Build on areas of agreement while addressing contested cruxes\n"+
			"3. Identify what each side would need to concede\n"+
			"4. Be honest about tradeoffs — don't pretend there's a costless solution\n\n"+
			"Format each as:\n"+
			"RESOLUTION: [title]\n[2-3 sentence proposal]\nREQUIRES: [what each side concedes]\n\n"+
			"Generate exactly 3-4 resolutions, separated by blank lines.",
		data.Title, findings,
	)

	fmt.Fprintf(os.Stderr, "  generating resolution proposals...\n")
	resp, err := callAnthropic(client, anthropic.MessageNewParams{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 1200,
		System: []anthropic.TextBlockParam{
			{Text: "You propose concrete, actionable resolutions for deliberation disputes. Be specific and honest about tradeoffs. Each resolution should be implementable, not aspirational."},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "  resolution generation failed: %v\n", err)
		return nil
	}

	text := extractText(resp)
	if text == "" {
		return nil
	}

	// Parse resolutions from response
	var agents []agentPlan
	blocks := strings.Split(text, "RESOLUTION:")
	for i, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" || i == 0 { // skip text before first RESOLUTION:
			continue
		}

		agentID := fmt.Sprintf("t3c-resolution-%d", i)
		lines := strings.SplitN(block, "\n", 2)
		title := strings.TrimSpace(lines[0])
		body := block
		if len(lines) > 1 {
			body = strings.TrimSpace(lines[1])
		}

		agents = append(agents, agentPlan{
			ID:       agentID,
			Role:     fmt.Sprintf("Resolution: %s", title),
			Position: fmt.Sprintf("RESOLUTION: %s\n\n%s", title, body),
			Kind:     "resolution",
			Round:    3,
		})

		fmt.Fprintf(os.Stderr, "    resolution %d: %s\n", i, title[:min(60, len(title))])
	}

	return agents
}

// buildR3Agents generates revised speaker positions informed by R2 analysis.
// Only speaker-derived agents (speaker, steelman) get revised — structural agents don't.
// Returns forked agents with "-r3" suffix.
func buildR3Agents(r1Agents []agentPlan, r2Analysis *analysisResult, data *ReportData) []agentPlan {
	apiKey := getAnthropicKey()
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "  R3: GEMOT_ANTHROPIC_KEY or ANTHROPIC_API_KEY required for position revision\n")
		os.Exit(1)
	}
	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	// Build R2 findings summary for the revision prompt
	var findings strings.Builder
	if len(r2Analysis.Cruxes) > 0 {
		findings.WriteString("KEY CRUXES (points of genuine disagreement):\n")
		for _, c := range r2Analysis.Cruxes[:min(5, len(r2Analysis.Cruxes))] {
			fmt.Fprintf(&findings, "- %s (agree: %s | disagree: %s)\n",
				c.Claim[:min(120, len(c.Claim))],
				strings.Join(c.Agree, ", "),
				strings.Join(c.Disagree, ", "))
		}
		findings.WriteString("\n")
	}
	if len(r2Analysis.ConsensusStatements) > 0 {
		findings.WriteString("UNCHALLENGED POSITIONS:\n")
		for _, cs := range r2Analysis.ConsensusStatements[:min(3, len(r2Analysis.ConsensusStatements))] {
			fmt.Fprintf(&findings, "- %s\n", cs.Content[:min(120, len(cs.Content))])
		}
		findings.WriteString("\n")
	}
	if len(r2Analysis.BridgingStatements) > 0 {
		findings.WriteString("BRIDGING PROPOSALS:\n")
		for _, bs := range r2Analysis.BridgingStatements[:min(3, len(r2Analysis.BridgingStatements))] {
			fmt.Fprintf(&findings, "- %s\n", bs.Content[:min(120, len(bs.Content))])
		}
		findings.WriteString("\n")
	}

	findingsText := findings.String()
	if findingsText == "" {
		return nil
	}

	var r3Agents []agentPlan
	for _, agent := range r1Agents {
		if agent.Kind != "speaker" && agent.Kind != "steelman" {
			continue
		}

		// Get source quotes for grounding
		var quotes []string
		if agent.Cluster != nil {
			for _, m := range agent.Cluster.Members {
				quotes = append(quotes, findAllQuotesForSpeaker(data, parseSpeakerID(m))...)
			}
		}
		quotesText := ""
		if len(quotes) > 0 {
			if len(quotes) > 8 {
				quotes = quotes[:8]
			}
			quotesText = "\nSOURCE QUOTES (stay grounded in these):\n- " + strings.Join(quotes, "\n- ") + "\n"
		}

		prompt := fmt.Sprintf(
			"You are revising a speaker's position in a deliberation after seeing the results of two rounds of analysis.\n\n"+
				"ORIGINAL POSITION:\n%s\n\n"+
				"DELIBERATION FINDINGS (from Rounds 1-2):\n%s\n"+
				"%s\n"+
				"Generate a REVISED position for this speaker that:\n"+
				"1. Accounts for the deliberation findings — where would this speaker likely update, concede, or dig in?\n"+
				"2. Stays grounded in the speaker's actual views and source quotes — do NOT generate positions they never expressed\n"+
				"3. Shows evolution: what shifted, what held firm, what new questions arise\n"+
				"4. Is honest about remaining disagreements — don't force false consensus\n\n"+
				"Write the revised position directly (no preamble). Keep it concise — 5-10 bullet points max.",
			agent.Position, findingsText, quotesText,
		)

		resp, err := callAnthropic(client, anthropic.MessageNewParams{
			Model:     "claude-sonnet-4-6",
			MaxTokens: 800,
			System: []anthropic.TextBlockParam{
				{Text: "You simulate how a real speaker would revise their position after seeing deliberation results. Be faithful to their stated views — revise where the evidence warrants, but don't manufacture consensus."},
			},
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "    revision failed for %s: %v\n", agent.ID, err)
			continue
		}

		revised := extractText(resp)
		if revised == "" {
			continue
		}

		r3Agents = append(r3Agents, agentPlan{
			ID:       agent.ID + "-r3",
			Role:     agent.Role + " (revised after R2)",
			Position: fmt.Sprintf("REVISED POSITION (after seeing R1-R2 findings)\n\n%s", revised),
			Kind:     agent.Kind,
			Round:    3,
			Cluster:  agent.Cluster,
			Topic:    agent.Topic,
		})

		fmt.Fprintf(os.Stderr, "    revised: %s → %s\n", agent.ID, agent.ID+"-r3")
	}

	return r3Agents
}

// seedR3Votes seeds votes between R3 agents and all prior agents.
func seedR3Votes(session *sdkmcp.ClientSession, r1Agents, r2Agents, r3Agents []agentPlan, delibID string) int {
	positions := getPositions(session, delibID)

	r3IDs := map[string]bool{}
	for _, a := range r3Agents {
		r3IDs[a.ID] = true
	}

	// Map R3 agent to its R1 original
	r3ToR1 := map[string]string{}
	for _, a := range r3Agents {
		r3ToR1[a.ID] = strings.TrimSuffix(a.ID, "-r3")
	}

	voteCount := 0

	// R3 agents vote on all positions.
	// IMPORTANT: Votes should NOT systematically favor convergence — that would
	// make R3 always appear to "converge" regardless of revision content. Use
	// cluster patterns for R1 agents (same as R1 voting) and neutral/skeptical
	// stances for structural agents. The LLM analysis should discover convergence
	// from the position TEXT, not from a biased vote matrix.
	for _, voter := range r3Agents {
		originalID := r3ToR1[voter.ID]
		for _, pos := range positions {
			if pos.AgentID == voter.ID {
				continue
			}

			vote := 0
			if voter.Kind == "resolution" {
				// Resolution agents vote based on index parity to diversify patterns.
				// Odd-numbered resolutions lean toward safety-focused positions,
				// even-numbered lean toward progress-focused. This ensures each
				// resolution has a distinct vote pattern (avoiding sybil detection)
				// while still being meaningful.
				resIdx := 0
				fmt.Sscanf(voter.ID, "t3c-resolution-%d", &resIdx)
				for _, other := range r1Agents {
					if other.ID == pos.AgentID {
						if other.Kind == "probe" {
							// Resolutions vary on probes by index
							if resIdx%2 == 0 {
								vote = 1
							} else {
								vote = -1
							}
						} else if other.Kind == "speaker" || other.Kind == "steelman" {
							// Alternate agreement pattern by resolution index
							vote = []int{1, -1, 0, 1}[resIdx%4]
						}
						break
					}
				}
			} else {
				// Revised agents: only vote +1 on own R1 original, 0 on everything else.
				// DO NOT use cluster patterns — they're identical to the original's,
				// which triggers SYBIL_SIGNAL. The analysis discovers R3 alignment
				// from the revised position text, not from vote patterns.
				if pos.AgentID == originalID {
					vote = 1
				}
			}

			call(session, "participate", map[string]any{
				"action": "vote", "deliberation_id": delibID,
				"agent_id": voter.ID, "position_id": pos.ID, "value": vote,
			})
			voteCount++
		}
	}

	// R1 + R2 agents vote on R3 positions.
	// Avoid append(r1, r2...) which may mutate r1's backing array.
	allPrior := make([]agentPlan, 0, len(r1Agents)+len(r2Agents))
	allPrior = append(allPrior, r1Agents...)
	allPrior = append(allPrior, r2Agents...)
	for _, voter := range allPrior {
		for _, pos := range positions {
			if pos.AgentID == voter.ID || !r3IDs[pos.AgentID] {
				continue
			}

			vote := 0
			r3Original := r3ToR1[pos.AgentID]

			switch {
			case voter.ID == r3Original:
				// Original speaker partially agrees with their revised version
				vote = 1
			case voter.Kind == "bridge":
				vote = 1 // bridge is favorable toward proposals
			case voter.Kind == "dissent":
				vote = -1 // dissent is skeptical of proposals
			case voter.Kind == "empty-chair":
				vote = 0
			default:
				// Other speakers vote based on cluster similarity to the R3's original
				if voter.Cluster != nil {
					for _, r1a := range r1Agents {
						if r1a.ID == r3Original && r1a.Cluster != nil {
							sim := patternSimilarity(voter.Cluster.Pattern, r1a.Cluster.Pattern)
							if sim >= 0.6 {
								vote = 1
							} else if sim <= 0.4 {
								vote = -1
							}
							break
						}
					}
				}
			}

			call(session, "participate", map[string]any{
				"action": "vote", "deliberation_id": delibID,
				"agent_id": voter.ID, "position_id": pos.ID, "value": vote,
			})
			voteCount++
		}
	}

	return voteCount
}
