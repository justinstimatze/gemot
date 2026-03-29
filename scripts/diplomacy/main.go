// diplomacy analyzes AI Diplomacy game messages through gemot's deliberation pipeline.
//
// For each power, it creates a deliberation containing that power's diplomatic messages,
// runs analysis to detect cruxes and alliance patterns, and outputs briefing files
// that can be injected into system prompts for the next game year.
//
// This replaces the T3C pipeline (CSV export → Playwright browser → report parsing)
// with direct MCP calls to gemot.
//
// Usage:
//   go run scripts/diplomacy/main.go --game /path/to/lmvsgame.json --year 1 --output /path/to/briefings
//
// Requires: GEMOT_LIVE_URL and GEMOT_API_SECRET env vars

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var powers = []string{"AUSTRIA", "ENGLAND", "FRANCE", "GERMANY", "ITALY", "RUSSIA", "TURKEY"}

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

// GameState is the top-level lmvsgame.json structure.
type GameState struct {
	Phases []Phase `json:"phases"`
}

// Phase represents a single game phase with messages and orders.
type Phase struct {
	Name     string    `json:"name"`
	Messages []Message `json:"messages"`
}

// Message is a diplomatic message between powers.
type Message struct {
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Content   string `json:"message"`
	Phase     string `json:"phase"`
}

func main() {
	gameFile := flag.String("game", "", "Path to lmvsgame.json")
	year := flag.Int("year", 1, "Game year number (1-based, e.g. 1 = 1901)")
	outputDir := flag.String("output", "", "Output directory for briefing files")
	gemotURL := flag.String("url", "", "Gemot MCP URL (default: GEMOT_LIVE_URL env)")
	flag.Parse()

	if *gameFile == "" || *outputDir == "" {
		fmt.Fprintf(os.Stderr, "Usage: diplomacy --game <path> --year <N> --output <dir>\n")
		os.Exit(1)
	}

	url := *gemotURL
	if url == "" {
		url = envOr("GEMOT_LIVE_URL", "https://gemot.dev/mcp")
	}
	secret := os.Getenv("GEMOT_API_SECRET")
	if secret == "" {
		if b, err := os.ReadFile(".env"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "GEMOT_API_SECRET=") {
					secret = strings.TrimPrefix(line, "GEMOT_API_SECRET=")
				}
			}
		}
	}
	if secret == "" {
		fatal(fmt.Errorf("GEMOT_API_SECRET not set"), "config")
	}

	// Load game state
	data, err := os.ReadFile(*gameFile)
	fatal(err, "reading game file")

	var game GameState
	fatal(json.Unmarshal(data, &game), "parsing game JSON")

	// Determine phases for this year
	gameYear := 1900 + *year
	targetPhases := map[string]bool{
		fmt.Sprintf("S%dM", gameYear): true,
		fmt.Sprintf("F%dM", gameYear): true,
	}

	// Collect messages per power (messages they sent or received)
	powerMessages := make(map[string][]Message)
	for _, phase := range game.Phases {
		if !targetPhases[phase.Name] {
			continue
		}
		for _, msg := range phase.Messages {
			sender := strings.ToUpper(msg.Sender)
			recipient := strings.ToUpper(msg.Recipient)
			powerMessages[sender] = append(powerMessages[sender], msg)
			if recipient != "GLOBAL" && recipient != sender {
				powerMessages[recipient] = append(powerMessages[recipient], msg)
			}
			if recipient == "GLOBAL" {
				for _, p := range powers {
					if p != sender {
						powerMessages[p] = append(powerMessages[p], msg)
					}
				}
			}
		}
	}

	os.MkdirAll(*outputDir, 0755) //nolint:errcheck
	ctx := context.Background()

	// Process each power with a fresh connection (SSE connections can timeout during long analyses)
	for _, power := range powers {
		msgs := powerMessages[power]
		if len(msgs) == 0 {
			fmt.Fprintf(os.Stderr, "  %s: no messages for year %d, skipping\n", power, *year)
			continue
		}

		fmt.Fprintf(os.Stderr, "  %s: %d messages — connecting...\n", power, len(msgs))

		var briefing string
		for attempt := 1; attempt <= 3; attempt++ {
			if attempt > 1 {
				fmt.Fprintf(os.Stderr, "    Retry %d/3...\n", attempt)
				time.Sleep(10 * time.Second)
			}

			session, err := connect(ctx, url, secret)
			if err != nil {
				fmt.Fprintf(os.Stderr, "    ERROR connecting: %v\n", err)
				continue
			}

			briefing, err = analyzePower(ctx, session, url, secret, power, msgs, *year)
			session.Close() //nolint:errcheck
			if err == nil {
				break
			}
			fmt.Fprintf(os.Stderr, "    ERROR: %v\n", err)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "    FAILED after 3 attempts: %v\n", err)
			continue
		}

		outFile := filepath.Join(*outputDir, fmt.Sprintf("%s_briefing.txt", strings.ToLower(power)))
		fatal(os.WriteFile(outFile, []byte(briefing), 0644), "writing briefing")
		fmt.Fprintf(os.Stderr, "    Wrote %s\n", outFile)
	}

	fmt.Fprintf(os.Stderr, "Done. Briefings written to %s\n", *outputDir)
}

func analyzePower(ctx context.Context, session *sdkmcp.ClientSession, url, secret, power string, msgs []Message, year int) (string, error) {
	// 1. Create deliberation
	topic := fmt.Sprintf("%s diplomatic intelligence — Year %d", power, 1900+year)
	desc := fmt.Sprintf("Analysis of diplomatic messages involving %s during Year %d. "+
		"Each position represents a message between powers. Analysis identifies "+
		"alliance patterns, points of disagreement, and strategic cruxes.", power, 1900+year)

	createJSON := callTool(ctx, session, "create_deliberation", map[string]any{
		"topic":       topic,
		"description": desc,
		"type":        "negotiation",
	})

	var createResp struct {
		DeliberationID string `json:"deliberation_id"`
	}
	mustParse(createJSON, &createResp)
	deliberationID := createResp.DeliberationID

	// 2. Submit each message as a position
	for i, msg := range msgs {
		sender := strings.ToUpper(msg.Sender)
		recipient := strings.ToUpper(msg.Recipient)
		positionText := fmt.Sprintf("[%s → %s, %s] %s", sender, recipient, msg.Phase, msg.Content)
		agentID := strings.ToLower(msg.Sender) + "-agent"

		callToolSoft(ctx, session, "submit_position", map[string]any{
			"deliberation_id": deliberationID,
			"agent_id":        agentID,
			"content":         positionText,
			"interests":       fmt.Sprintf("%s's strategic objectives", strings.ToLower(msg.Sender)),
		})
		_ = i
	}

	// 3. Get positions and have each power vote
	posJSON := callTool(ctx, session, "get_positions", map[string]any{
		"deliberation_id": deliberationID,
	})

	var positions []struct {
		ID      string `json:"position_id"`
		AgentID string `json:"agent_id"`
	}
	mustParse(posJSON, &positions)

	// Build a map of position ID to message metadata for voting decisions
	type posInfo struct {
		sender, recipient string
	}
	posMap := make(map[string]posInfo)
	for i, pos := range positions {
		if i < len(msgs) {
			posMap[pos.ID] = posInfo{
				sender:    strings.ToLower(msgs[i].Sender),
				recipient: strings.ToLower(msgs[i].Recipient),
			}
		}
	}

	for _, pos := range positions {
		info := posMap[pos.ID]
		for _, voterPower := range powers {
			voterAgent := strings.ToLower(voterPower) + "-agent"
			voter := strings.ToLower(voterPower)
			if voterAgent == pos.AgentID {
				continue
			}

			// Vote based on diplomatic relationship:
			// - Agree with messages addressed to you (they engaged with you)
			// - Agree with your own messages (via other positions)
			// - Disagree with messages between rivals that exclude you
			// - Pass on global broadcasts
			vote := 0
			if info.recipient == voter || info.sender == voter {
				vote = 1 // you're party to this conversation
			} else if info.recipient == "global" {
				vote = 0 // pass on broadcasts
			} else {
				vote = -1 // private message you're excluded from
			}

			callToolSoft(ctx, session, "vote", map[string]any{
				"deliberation_id": deliberationID,
				"agent_id":        voterAgent,
				"position_id":     pos.ID,
				"vote":            vote,
			})
		}
	}

	// 4. Run analysis
	fmt.Fprintf(os.Stderr, "    Analyzing %s...\n", power)
	callTool(ctx, session, "analyze", map[string]any{
		"deliberation_id": deliberationID,
	})

	// 5. Poll for completion by trying get_context directly.
	// This is the most reliable signal — if get_context returns data,
	// analysis is done. No need to interpret status transitions.
	time.Sleep(5 * time.Second)
	var contextJSON string
	for i := 0; i < 200; i++ {
		time.Sleep(3 * time.Second)

		result := callToolSoft(ctx, session, "get_context", map[string]any{
			"deliberation_id": deliberationID,
			"agent_id":        strings.ToLower(power) + "-agent",
		})

		if result == "" {
			// Session may have died — reconnect
			session.Close() //nolint:errcheck
			var reconnErr error
			session, reconnErr = connect(ctx, url, secret)
			if reconnErr != nil {
				fmt.Fprintf(os.Stderr, "    reconnect failed: %v\n", reconnErr)
			}
			// Log progress from get_deliberation
			if session != nil {
				statusJSON := callToolSoft(ctx, session, "get_deliberation", map[string]any{
					"deliberation_id": deliberationID,
				})
				if statusJSON != "" {
					var s struct { Status string `json:"status"`; SubStatus string `json:"sub_status"` }
					json.Unmarshal([]byte(strings.SplitN(statusJSON, "\n\n---\n", 2)[0]), &s)
					fmt.Fprintf(os.Stderr, "    %s/%s\n", s.Status, s.SubStatus)
				}
			}
			continue
		}

		contextJSON = result
		fmt.Fprintf(os.Stderr, "    Analysis complete\n")
		break
	}
	if contextJSON == "" {
		return "", fmt.Errorf("analysis did not produce results after 10 minutes")
	}

	// 6. Format as briefing
	return formatBriefing(power, year, contextJSON), nil
}

type crux struct {
	Claim          string   `json:"crux_claim"`
	Topic          string   `json:"topic"`
	Explanation    string   `json:"explanation"`
	Controversy    float64  `json:"controversy_score"`
	AgreeAgents    []string `json:"agree_agents"`
	DisagreeAgents []string `json:"disagree_agents"`
	CruxType       string   `json:"crux_type"`
}

func formatBriefing(power string, year int, contextJSON string) string {
	var ac struct {
		AgentID              string   `json:"agent_id"`
		ClusterID            *int     `json:"cluster_id"`
		NearestAllies        []string `json:"nearest_allies"`
		BiggestDisagreements []string `json:"biggest_disagreements_with"`
		RelevantCruxes       []crux   `json:"relevant_cruxes"`
		DiversityNudge       string   `json:"diversity_nudge"`
		IntegrityWarnings    []string `json:"integrity_warnings"`
	}
	mustParse(contextJSON, &ac)

	var b strings.Builder
	fmt.Fprintf(&b, "=== DIPLOMATIC INTELLIGENCE BRIEFING: %s — Year %d ===\n\n", power, 1900+year)

	if len(ac.NearestAllies) > 0 {
		fmt.Fprintf(&b, "ALLIANCE ALIGNMENT:\nYour closest allies based on voting patterns: %s\n\n",
			strings.Join(ac.NearestAllies, ", "))
	}

	if len(ac.BiggestDisagreements) > 0 {
		fmt.Fprintf(&b, "BIGGEST DISAGREEMENTS WITH:\n%s\n\n",
			strings.Join(ac.BiggestDisagreements, ", "))
	}

	if len(ac.RelevantCruxes) > 0 {
		fmt.Fprintf(&b, "KEY POINTS OF DISAGREEMENT (CRUXES):\n")
		for i, c := range ac.RelevantCruxes {
			fmt.Fprintf(&b, "\n%d. %s\n", i+1, c.Claim)
			if c.Topic != "" {
				fmt.Fprintf(&b, "   Topic: %s\n", c.Topic)
			}
			if c.Controversy > 0 {
				fmt.Fprintf(&b, "   Controversy: %.0f%%\n", c.Controversy*100)
			}
			if len(c.AgreeAgents) > 0 {
				fmt.Fprintf(&b, "   AGREE: %s\n", strings.Join(c.AgreeAgents, ", "))
			}
			if len(c.DisagreeAgents) > 0 {
				fmt.Fprintf(&b, "   DISAGREE: %s\n", strings.Join(c.DisagreeAgents, ", "))
			}
			if c.Explanation != "" {
				fmt.Fprintf(&b, "   %s\n", c.Explanation)
			}
		}
		fmt.Fprintln(&b)
	}

	if ac.DiversityNudge != "" {
		fmt.Fprintf(&b, "STRATEGIC CONSIDERATIONS:\n%s\n\n", ac.DiversityNudge)
	}

	fmt.Fprintf(&b, "=== END BRIEFING ===\n")
	return b.String()
}

func connect(ctx context.Context, url, secret string) (*sdkmcp.ClientSession, error) {
	transport := &sdkmcp.SSEClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: secret},
		},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "diplomacy", Version: "1.0"}, nil)
	return client.Connect(ctx, transport, nil)
}

func callTool(ctx context.Context, s *sdkmcp.ClientSession, name string, args map[string]any) string {
	res, err := s.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tool %s failed: %v\n", name, err)
		os.Exit(1)
	}
	if res.IsError {
		fmt.Fprintf(os.Stderr, "tool %s error: %s\n", name, res.Content[0].(*sdkmcp.TextContent).Text)
		os.Exit(1)
	}
	return res.Content[0].(*sdkmcp.TextContent).Text
}

func callToolSoft(ctx context.Context, s *sdkmcp.ClientSession, name string, args map[string]any) string {
	res, err := s.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return ""
	}
	if res.IsError || len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].(*sdkmcp.TextContent).Text
}

func mustParse(jsonStr string, v any) {
	if idx := strings.Index(jsonStr, "\n\n---\n"); idx != -1 {
		jsonStr = jsonStr[:idx]
	}
	if err := json.Unmarshal([]byte(jsonStr), v); err != nil {
		fmt.Fprintf(os.Stderr, "JSON parse error: %v\nRaw: %s\n", err, jsonStr[:min(200, len(jsonStr))])
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func fatal(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", msg, err)
		os.Exit(1)
	}
}
