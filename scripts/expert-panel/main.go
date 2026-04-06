// expert-panel runs an adversarial expert panel against a document using gemot.
//
// Usage:
//
//	go run scripts/expert-panel/ --document RESULTS.md
//	go run scripts/expert-panel/ --document RESULTS.md --experts experts.json
//	go run scripts/expert-panel/ --document RESULTS.md --topic "Review our Q3 plan" --source-type proposal
//	go run scripts/expert-panel/ --document RESULTS.md --depth quick
//
// Source types: code_review, architecture, experiment (default), proposal.
// Depth: "quick" (~2min, 3 experts) or "thorough" (~7min, 5 experts, default).
// Custom experts via --experts JSON file:
//
//	[{"id": "economist", "role": "Behavioral economist", "interests": "incentive design", "reservation": "ignoring second-order effects"}]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

func connect(url, secret string) (*sdkmcp.ClientSession, error) {
	transport := &sdkmcp.SSEClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: secret},
		},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "expert-panel", Version: "2.0"}, nil)
	return client.Connect(context.Background(), transport, nil)
}

// callSoft calls a tool and returns "" on error instead of exiting.
func callSoft(session *sdkmcp.ClientSession, name string, args map[string]any) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fmt.Fprintf(os.Stderr, "  [soft] %s: %v\n", name, err)
		return ""
	}
	if res.IsError || len(res.Content) == 0 {
		return ""
	}
	text := res.Content[0].(*sdkmcp.TextContent).Text
	if idx := strings.Index(text, "\n\n---\n"); idx != -1 {
		text = text[:idx]
	}
	return text
}

// call calls a tool and exits on error.
func call(session *sdkmcp.ClientSession, name string, args map[string]any) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", name, err)
		os.Exit(1)
	}
	if res.IsError && len(res.Content) > 0 {
		fmt.Fprintf(os.Stderr, "%s error: %s\n", name, res.Content[0].(*sdkmcp.TextContent).Text)
		os.Exit(1)
	}
	if len(res.Content) == 0 {
		return ""
	}
	text := res.Content[0].(*sdkmcp.TextContent).Text
	if idx := strings.Index(text, "\n\n---\n"); idx != -1 {
		text = text[:idx]
	}
	return text
}

func main() {
	docFile := flag.String("document", "", "Path to document to review (required)")
	expertsFile := flag.String("experts", "", "Path to experts JSON file (optional, uses defaults)")
	topic := flag.String("topic", "", "Deliberation topic (default: derived from document filename)")
	sourceType := flag.String("source-type", "", "Panel type: code_review, architecture, experiment, proposal")
	depth := flag.String("depth", "", "Panel depth: quick (~2min, 3 experts) or thorough (~7min, 5 experts, default)")
	url := flag.String("url", "", "Gemot MCP URL (default: GEMOT_LIVE_URL env or https://gemot.dev/mcp)")
	groupID := flag.String("group", "expert-panel", "Group ID for gemotvis")
	flag.Parse()

	if *docFile == "" {
		fmt.Fprintf(os.Stderr, "Usage: expert-panel --document <path> [--experts <path>] [--topic <topic>] [--source-type <type>] [--depth quick|thorough]\n")
		os.Exit(1)
	}

	document, err := os.ReadFile(*docFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading document: %v\n", err)
		os.Exit(1)
	}

	mcpURL := *url
	if mcpURL == "" {
		mcpURL = os.Getenv("GEMOT_LIVE_URL")
	}
	if mcpURL == "" {
		mcpURL = "https://gemot.dev/mcp"
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

	deliberationTopic := *topic
	if deliberationTopic == "" {
		deliberationTopic = "Expert review: " + *docFile
	}

	var expertsJSON string
	if *expertsFile != "" {
		data, err := os.ReadFile(*expertsFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reading experts: %v\n", err)
			os.Exit(1)
		}
		expertsJSON = string(data)
	}

	// Step 1: Create panel (returns immediately with deliberation_id)
	fmt.Fprintf(os.Stderr, "Creating expert panel...\n")
	start := time.Now()
	session, err := connect(mcpURL, secret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		os.Exit(1)
	}
	args := map[string]any{
		"action":   "expert_panel",
		"document": string(document),
		"topic":    deliberationTopic,
		"group_id": *groupID,
	}
	if *sourceType != "" {
		args["source_type"] = *sourceType
	}
	if *depth != "" {
		args["depth"] = *depth
	}
	if expertsJSON != "" {
		args["experts"] = expertsJSON
	}
	panelJSON := call(session, "analyze", args)
	session.Close()
	var panel struct {
		DeliberationID string `json:"deliberation_id"`
		ExpertCount    int    `json:"expert_count"`
	}
	json.Unmarshal([]byte(panelJSON), &panel)
	fmt.Fprintf(os.Stderr, "Panel: %s (%d experts, analysis started)\n", panel.DeliberationID, panel.ExpertCount)

	// Step 2: Poll for completion with robust reconnect
	const maxReconnects = 10
	reconnects := 0
	s, err := connect(mcpURL, secret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "poll connect failed: %v\n", err)
		os.Exit(1)
	}

	reconnect := func() bool {
		s.Close()
		reconnects++
		if reconnects > maxReconnects {
			fmt.Fprintf(os.Stderr, "  max reconnects (%d) exceeded\n", maxReconnects)
			return false
		}
		fmt.Fprintf(os.Stderr, "  reconnecting (%d/%d)...\n", reconnects, maxReconnects)
		time.Sleep(3 * time.Second)
		var err error
		s, err = connect(mcpURL, secret)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  reconnect failed: %v\n", err)
			return false
		}
		return true
	}

	analysisComplete := false
	for i := 0; i < 300; i++ { // 25 minutes max (300 * 5s)
		time.Sleep(5 * time.Second)
		statusJSON := callSoft(s, "deliberation", map[string]any{
			"action":          "get",
			"deliberation_id": panel.DeliberationID,
		})
		if statusJSON == "" {
			if !reconnect() {
				break
			}
			continue
		}
		var status struct {
			Status    string `json:"status"`
			SubStatus string `json:"sub_status"`
		}
		json.Unmarshal([]byte(statusJSON), &status)
		if status.Status == "open" {
			analysisComplete = true
			break
		}
		if i%6 == 0 {
			fmt.Fprintf(os.Stderr, "  %s/%s (%ds)\n", status.Status, status.SubStatus, int(time.Since(start).Seconds()))
		}
	}
	s.Close()

	if !analysisComplete {
		fmt.Fprintf(os.Stderr, "analysis did not complete within timeout\n")
		fmt.Fprintf(os.Stderr, "deliberation_id: %s (check manually)\n", panel.DeliberationID)
		os.Exit(1)
	}

	// Step 3: Fetch and display results
	s, err = connect(mcpURL, secret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "result connect failed: %v\n", err)
		os.Exit(1)
	}
	resultJSON := call(s, "analyze", map[string]any{
		"action":          "get_result",
		"deliberation_id": panel.DeliberationID,
		"round":           1,
	})
	s.Close()
	if resultJSON == "" {
		fmt.Fprintf(os.Stderr, "no results returned\n")
		os.Exit(1)
	}

	var result struct {
		Cruxes []struct {
			Claim       string   `json:"crux_claim"`
			Agree       []string `json:"agree_agents"`
			Disagree    []string `json:"disagree_agents"`
			Score       float64  `json:"controversy_score"`
			Explanation string   `json:"explanation"`
			Topic       string   `json:"topic"`
		} `json:"cruxes"`
		ConsensusStatements []struct{ Content string } `json:"consensus_statements"`
		BridgingStatements  []struct {
			Content string
			Score   float64 `json:"bridging_score"`
		} `json:"bridging_statements"`
		CompromiseProposal string `json:"compromise_proposal"`
		TopicSummaries     []struct{ Topic, Summary string } `json:"topic_summaries"`
	}
	json.Unmarshal([]byte(resultJSON), &result)

	// Output
	elapsed := time.Since(start)
	fmt.Println("# Expert Panel Results")
	fmt.Printf("\nDeliberation: %s\n", panel.DeliberationID)
	fmt.Printf("Experts: %d | Time: %ds\n", panel.ExpertCount, int(elapsed.Seconds()))

	if len(result.TopicSummaries) > 0 {
		fmt.Println("\n## Summaries\n")
		for _, ts := range result.TopicSummaries {
			fmt.Printf("### %s\n\n%s\n\n", ts.Topic, ts.Summary)
		}
	}

	if len(result.Cruxes) > 0 {
		fmt.Println("## Key Disagreements\n")
		for i, c := range result.Cruxes {
			fmt.Printf("%d. **%s** (%.0f%% controversy)\n", i+1, c.Claim, c.Score*100)
			fmt.Printf("   - Agree: %s\n", strings.Join(c.Agree, ", "))
			fmt.Printf("   - Disagree: %s\n", strings.Join(c.Disagree, ", "))
			fmt.Printf("   - %s\n\n", c.Explanation)
		}
	}

	if len(result.ConsensusStatements) > 0 {
		fmt.Println("## Consensus\n")
		for _, cs := range result.ConsensusStatements {
			fmt.Printf("- %s\n", cs.Content)
		}
		fmt.Println()
	}

	if len(result.BridgingStatements) > 0 {
		fmt.Println("## Bridging Statements\n")
		for _, bs := range result.BridgingStatements {
			fmt.Printf("- [%.0f%%] %s\n", bs.Score*100, bs.Content)
		}
		fmt.Println()
	}

	if result.CompromiseProposal != "" {
		fmt.Printf("## Recommended Follow-up Plan\n\n%s\n", result.CompromiseProposal)
	}
}
