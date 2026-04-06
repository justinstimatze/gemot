// expert-panel runs an adversarial expert panel against a document using gemot.
//
// Usage:
//
//	go run scripts/expert-panel/main.go --document RESULTS.md
//	go run scripts/expert-panel/main.go --document RESULTS.md --experts experts.json
//	go run scripts/expert-panel/main.go --document RESULTS.md --topic "Review our Q3 plan"
//
// Default experts: methodologist, domain expert, statistician, systems engineer, devil's advocate.
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

type expert struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	Interests   string `json:"interests"`
	Reservation string `json:"reservation"`
}

var defaultExperts = []expert{
	{ID: "methodologist", Role: "Research methodology expert (causal inference, experimental design)", Interests: "Internal validity, confound elimination, proper controls", Reservation: "Drawing causal conclusions without adequate controls"},
	{ID: "domain-expert", Role: "Domain expert with deep practical experience", Interests: "Whether claims match real-world dynamics, practical feasibility", Reservation: "Attributing outcomes to the intervention vs background noise"},
	{ID: "statistician", Role: "Statistician (small-sample analysis, effect sizes)", Interests: "Statistical rigor, appropriate claims given sample size, multiple comparisons", Reservation: "Any claim of significance without adequate replication"},
	{ID: "systems-critic", Role: "Systems engineer focused on reliability and failure modes", Interests: "Infrastructure reliability, data integrity, hidden failure modes", Reservation: "Trusting results from systems with known issues"},
	{ID: "devils-advocate", Role: "Devil's advocate — finds the strongest counterargument to every claim", Interests: "Alternative explanations, unfalsifiable claims, confirmation bias", Reservation: "Accepting conclusions when simpler explanations exist"},
}

func connect(url, secret string) *sdkmcp.ClientSession {
	transport := &sdkmcp.SSEClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: secret},
			Timeout:   5 * time.Minute,
		},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "expert-panel", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		os.Exit(1)
	}
	return session
}

func call(session *sdkmcp.ClientSession, name string, args map[string]any) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if res.IsError && len(res.Content) > 0 {
		return "", fmt.Errorf("%s: %s", name, res.Content[0].(*sdkmcp.TextContent).Text)
	}
	if len(res.Content) == 0 {
		return "", nil
	}
	text := res.Content[0].(*sdkmcp.TextContent).Text
	if idx := strings.Index(text, "\n\n---\n"); idx != -1 {
		text = text[:idx]
	}
	return text, nil
}

func main() {
	docFile := flag.String("document", "", "Path to document to review (required)")
	expertsFile := flag.String("experts", "", "Path to experts JSON file (optional, uses defaults)")
	topic := flag.String("topic", "", "Deliberation topic (default: derived from document filename)")
	url := flag.String("url", "", "Gemot MCP URL (default: GEMOT_LIVE_URL env or https://gemot.dev/mcp)")
	groupID := flag.String("group", "expert-panel", "Group ID for gemotvis")
	flag.Parse()

	if *docFile == "" {
		fmt.Fprintf(os.Stderr, "Usage: expert-panel --document <path> [--experts <path>] [--topic <topic>]\n")
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

	// Load experts
	experts := defaultExperts
	if *expertsFile != "" {
		data, err := os.ReadFile(*expertsFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reading experts: %v\n", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(data, &experts); err != nil {
			fmt.Fprintf(os.Stderr, "parsing experts JSON: %v\n", err)
			os.Exit(1)
		}
	}

	deliberationTopic := *topic
	if deliberationTopic == "" {
		deliberationTopic = "Expert review: " + *docFile
	}

	// Step 1: Create deliberation
	fmt.Fprintf(os.Stderr, "Creating deliberation...\n")
	session := connect(mcpURL, secret)
	createJSON, err := call(session, "deliberation", map[string]any{
		"action":      "create",
		"topic":       deliberationTopic,
		"description": fmt.Sprintf("Adversarial expert panel reviewing: %s", deliberationTopic),
		"type":        "reasoning",
		"template":    "assembly",
		"group_id":    *groupID,
	})
	session.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "create failed: %v\n", err)
		os.Exit(1)
	}
	var created struct {
		DeliberationID string `json:"deliberation_id"`
	}
	json.Unmarshal([]byte(createJSON), &created)
	delibID := created.DeliberationID
	fmt.Fprintf(os.Stderr, "Deliberation: %s\n", delibID)

	// Step 2: Submit expert positions (fresh session per expert for reliability)
	for _, e := range experts {
		fmt.Fprintf(os.Stderr, "  %s submitting...\n", e.ID)
		s := connect(mcpURL, secret)
		position := fmt.Sprintf(`You are a %s.

Adversarially critique the following document. Find every weakness, amateur mistake, unjustified claim, and missing control. Be specific and constructive.

Your interests: %s
Your hard constraint: %s

Provide your critique with:
1. FATAL FLAWS: issues that invalidate the conclusions
2. MAJOR CONCERNS: issues that significantly weaken the findings
3. MINOR ISSUES: things to fix but don't invalidate results
4. WHAT'S GOOD: acknowledge strengths honestly
5. RECOMMENDED FOLLOW-UPS: specific next steps ranked by value/effort

=== DOCUMENT ===
%s`, e.Role, e.Interests, e.Reservation, string(document))

		_, err := call(s, "participate", map[string]any{
			"action":          "submit_position",
			"deliberation_id": delibID,
			"agent_id":        e.ID,
			"content":         position,
			"interests":       e.Interests,
			"reservation":     e.Reservation,
		})
		s.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: submit failed: %v\n", e.ID, err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Step 3: Trigger analysis
	fmt.Fprintf(os.Stderr, "Analyzing...\n")
	s := connect(mcpURL, secret)
	_, err = call(s, "analyze", map[string]any{
		"action":          "run",
		"deliberation_id": delibID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "analyze failed: %v\n", err)
		s.Close()
		os.Exit(1)
	}

	// Step 4: Poll with reconnect
	for i := 0; i < 180; i++ { // 15 minutes max
		time.Sleep(5 * time.Second)
		statusJSON, err := call(s, "deliberation", map[string]any{
			"action":          "get",
			"deliberation_id": delibID,
		})
		if err != nil {
			// Connection died — reconnect
			s.Close()
			fmt.Fprintf(os.Stderr, "  reconnecting...\n")
			s = connect(mcpURL, secret)
			continue
		}

		var status struct {
			Status    string `json:"status"`
			SubStatus string `json:"sub_status"`
		}
		json.Unmarshal([]byte(statusJSON), &status)

		if status.Status == "open" {
			break
		}
		if i%6 == 0 {
			fmt.Fprintf(os.Stderr, "  %s/%s\n", status.Status, status.SubStatus)
		}
	}
	s.Close()

	// Step 5: Fetch and display results
	s = connect(mcpURL, secret)
	resultJSON, err := call(s, "analyze", map[string]any{
		"action":          "get_result",
		"deliberation_id": delibID,
		"round":           1,
	})
	s.Close()
	if err != nil || resultJSON == "" {
		fmt.Fprintf(os.Stderr, "no results: %v\n", err)
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
	fmt.Println("# Expert Panel Results")
	fmt.Printf("\nDeliberation: %s\n", delibID)
	fmt.Printf("Experts: %d\n", len(experts))

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
