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

	// Connect to gemot
	fmt.Fprintf(os.Stderr, "Connecting to %s...\n", url)
	ctx := context.Background()
	transport := &sdkmcp.SSEClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: secret},
		},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "diplomacy", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	fatal(err, "connecting to gemot")
	defer session.Close() //nolint:errcheck

	os.MkdirAll(*outputDir, 0755) //nolint:errcheck

	// Process each power
	for _, power := range powers {
		msgs := powerMessages[power]
		if len(msgs) == 0 {
			fmt.Fprintf(os.Stderr, "  %s: no messages for year %d, skipping\n", power, *year)
			continue
		}

		fmt.Fprintf(os.Stderr, "  %s: %d messages\n", power, len(msgs))

		briefing, err := analyzePower(ctx, session, power, msgs, *year)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ERROR: %v\n", err)
			continue
		}

		outFile := filepath.Join(*outputDir, fmt.Sprintf("%s_briefing.txt", strings.ToLower(power)))
		fatal(os.WriteFile(outFile, []byte(briefing), 0644), "writing briefing")
		fmt.Fprintf(os.Stderr, "    Wrote %s\n", outFile)
	}

	fmt.Fprintf(os.Stderr, "Done. Briefings written to %s\n", *outputDir)
}

func analyzePower(ctx context.Context, session *sdkmcp.ClientSession, power string, msgs []Message, year int) (string, error) {
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

	var positions struct {
		Positions []struct {
			ID      string `json:"id"`
			AgentID string `json:"agent_id"`
		} `json:"positions"`
	}
	mustParse(posJSON, &positions)

	for _, pos := range positions.Positions {
		for _, voterPower := range powers {
			voterAgent := strings.ToLower(voterPower) + "-agent"
			if voterAgent == pos.AgentID {
				continue
			}
			// Powers agree with their own messages, pass on others
			vote := 0
			senderPower := strings.TrimSuffix(pos.AgentID, "-agent")
			if strings.EqualFold(senderPower, voterPower) {
				vote = 1
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

	// 5. Poll for completion
	for i := 0; i < 60; i++ {
		time.Sleep(3 * time.Second)
		statusJSON := callTool(ctx, session, "get_deliberation", map[string]any{
			"deliberation_id": deliberationID,
		})
		var status struct {
			Status    string `json:"status"`
			SubStatus string `json:"sub_status"`
		}
		mustParse(statusJSON, &status)
		if status.Status == "open" && status.SubStatus == "" {
			break
		}
		fmt.Fprintf(os.Stderr, "    Status: %s/%s\n", status.Status, status.SubStatus)
	}

	// 6. Get context for this power
	contextJSON := callTool(ctx, session, "get_context", map[string]any{
		"deliberation_id": deliberationID,
		"agent_id":        strings.ToLower(power) + "-agent",
	})

	// 7. Format as briefing
	return formatBriefing(power, year, contextJSON), nil
}

func formatBriefing(power string, year int, contextJSON string) string {
	var ac struct {
		ClusterSummary      string `json:"cluster_summary"`
		CruxesInvolvingYou  string `json:"cruxes_involving_you"`
		Allies              string `json:"allies"`
		BiggestDisagreements string `json:"biggest_disagreements"`
		Consensus           string `json:"consensus"`
		Bridging            string `json:"bridging"`
		DiversityNudge      string `json:"diversity_nudge"`
	}
	mustParse(contextJSON, &ac)

	var b strings.Builder
	fmt.Fprintf(&b, "=== DIPLOMATIC INTELLIGENCE BRIEFING: %s — Year %d ===\n\n", power, 1900+year)

	sections := []struct {
		title, content string
	}{
		{"YOUR DIPLOMATIC POSITION", ac.ClusterSummary},
		{"KEY POINTS OF DISAGREEMENT (CRUXES)", ac.CruxesInvolvingYou},
		{"ALLIANCE ALIGNMENT", ac.Allies},
		{"BIGGEST DISAGREEMENTS", ac.BiggestDisagreements},
		{"AREAS OF CONSENSUS", ac.Consensus},
		{"BRIDGING OPPORTUNITIES", ac.Bridging},
		{"STRATEGIC CONSIDERATIONS", ac.DiversityNudge},
	}
	for _, s := range sections {
		if s.content != "" {
			fmt.Fprintf(&b, "%s:\n%s\n\n", s.title, s.content)
		}
	}

	fmt.Fprintf(&b, "=== END BRIEFING ===\n")
	return b.String()
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
