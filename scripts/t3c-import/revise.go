package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// buildR3Agents generates revised speaker positions informed by R2 analysis.
// Only speaker-derived agents (speaker, steelman) get revised — structural agents don't.
// Returns forked agents with "-r3" suffix.
func buildR3Agents(r1Agents []agentPlan, r2Analysis *analysisResult, data *ReportData) []agentPlan {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "  R3: ANTHROPIC_API_KEY required for position revision\n")
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

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     "claude-sonnet-4-6",
			MaxTokens: 800,
			System: []anthropic.TextBlockParam{
				{Text: "You simulate how a real speaker would revise their position after seeing deliberation results. Be faithful to their stated views — revise where the evidence warrants, but don't manufacture consensus."},
			},
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
			},
		})
		cancel()

		if err != nil {
			fmt.Fprintf(os.Stderr, "    revision failed for %s: %v\n", agent.ID, err)
			continue
		}

		revised := ""
		for _, block := range resp.Content {
			if block.Type == "text" {
				revised = block.AsText().Text
				break
			}
		}
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

	// R3 agents vote on all positions
	for _, voter := range r3Agents {
		originalID := r3ToR1[voter.ID]
		for _, pos := range positions {
			if pos.AgentID == voter.ID {
				continue
			}

			vote := 0
			switch {
			case pos.AgentID == originalID:
				// R3 partially agrees with its own R1 version
				vote = 1
			case strings.HasPrefix(pos.AgentID, "t3c-bridge"):
				// Revised agents are generally open to bridging
				vote = 1
			case strings.HasPrefix(pos.AgentID, "t3c-dissent"):
				// Mixed on dissent — some revisions address dissent concerns
				vote = 0
			case r3IDs[pos.AgentID]:
				// R3 agents are generally sympathetic to other revised agents
				vote = 1
			default:
				// Vote on other R1 agents based on cluster similarity
				if voter.Cluster != nil {
					for _, other := range r1Agents {
						if other.ID == pos.AgentID && other.Cluster != nil {
							sim := patternSimilarity(voter.Cluster.Pattern, other.Cluster.Pattern)
							if sim >= 0.5 {
								vote = 1
							} else if sim <= 0.3 {
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

	// R1 + R2 agents vote on R3 positions
	allPrior := append(r1Agents, r2Agents...)
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
				// Bridge likes revised positions (they represent convergence)
				vote = 1
			case voter.Kind == "dissent":
				// Dissent is skeptical of revisions (might be false consensus)
				vote = -1
			case voter.Kind == "empty-chair":
				vote = 0
			default:
				// Other speakers vote based on cluster similarity to the original
				if voter.Cluster != nil {
					for _, r1a := range r1Agents {
						if r1a.ID == r3Original && r1a.Cluster != nil {
							sim := patternSimilarity(voter.Cluster.Pattern, r1a.Cluster.Pattern)
							if sim >= 0.5 {
								vote = 1
							} else if sim <= 0.3 {
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
