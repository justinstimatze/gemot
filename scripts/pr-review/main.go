// pr-review-demo.go demonstrates multi-agent PR review deliberation.
//
// Three agents review gemot's latest commit:
// 1. Contributor's agent — defends the changes
// 2. Maintainer's review agent — evaluates quality and fit
// 3. Architecture agent — assesses consistency with project direction
//
// They deliberate through gemot, find cruxes, then attempt to resolve
// them autonomously. Humans only see the outcome.
//
// Usage: go run scripts/pr-review-demo.go
//
// Requires: GEMOT_ANTHROPIC_KEY (or .env), GEMOT_API_SECRET

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

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

func main() {
	// Load .env
	if b, err := os.ReadFile(".env"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				k, v = strings.TrimSpace(k), strings.TrimSpace(v)
				if os.Getenv(k) == "" {
					_ = os.Setenv(k, v)
				}
			}
		}
	}

	secret := os.Getenv("GEMOT_API_SECRET")
	url := "https://gemot.fly.dev/mcp"
	if secret == "" {
		fmt.Fprintln(os.Stderr, "GEMOT_API_SECRET not set")
		os.Exit(1)
	}

	// Get the latest commit diff
	fmt.Fprintln(os.Stderr, "Getting latest commit...")
	commitMsg, _ := exec.Command("git", "log", "--oneline", "-1").Output()
	diff, _ := exec.Command("git", "diff", "HEAD~1", "--stat").Output()
	diffContent, _ := exec.Command("git", "diff", "HEAD~1").Output()

	// Truncate diff to avoid token limits
	diffStr := string(diffContent)
	if len(diffStr) > 8000 {
		diffStr = diffStr[:8000] + "\n... (truncated)"
	}

	fmt.Fprintf(os.Stderr, "Commit: %s", commitMsg)
	fmt.Fprintf(os.Stderr, "Files changed:\n%s\n", diff)

	// Connect to gemot
	ctx := context.Background()
	transport := &sdkmcp.StreamableClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: secret},
		},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "pr-review-demo", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer session.Close() //nolint:errcheck

	// Create deliberation
	topic := fmt.Sprintf("PR Review: %s", strings.TrimSpace(string(commitMsg)))
	createRes := call(ctx, session, "create_deliberation", map[string]any{
		"topic":       topic,
		"description": "Three agents review a pull request and attempt to reach consensus autonomously.",
	})
	var delib struct {
		ID string `json:"deliberation_id"`
	}
	parse(createRes, &delib)
	fmt.Fprintf(os.Stderr, "Created deliberation: %s\n", delib.ID)

	// Agent positions
	contributorPosition := fmt.Sprintf(`I'm the contributor who wrote this commit. Here's what I changed and why:

Commit: %s
Files changed:
%s

Diff (partial):
%s

I believe these changes are well-motivated, correctly implemented, and ready to merge. The code follows the project's existing patterns and improves the codebase.`, strings.TrimSpace(string(commitMsg)), string(diff), diffStr)

	reviewerPosition := fmt.Sprintf(`I'm the code reviewer evaluating this PR for quality, correctness, and maintainability.

Commit: %s
Files changed:
%s

Diff (partial):
%s

I'm looking for: bugs, security issues, missing error handling, unclear naming, insufficient tests, unnecessary complexity, and deviations from project conventions. I will raise concerns if I find them.`, strings.TrimSpace(string(commitMsg)), string(diff), diffStr)

	architectPosition := fmt.Sprintf(`I'm the project architect evaluating whether this change is consistent with gemot's design philosophy and roadmap.

Commit: %s
Files changed:
%s

Gemot is a deliberation primitive for multi-agent systems. Key design principles: text-first analysis (T3C pipeline), vote-second (Polis math), integrity-aware (epistemic poisoning defense), MCP-native, single Go binary with SQLite.

I'm evaluating: does this change maintain architectural coherence? Does it add complexity proportional to value? Does it follow the existing module boundaries?`, strings.TrimSpace(string(commitMsg)), string(diff))

	// Submit positions
	agents := map[string]string{
		"contributor": contributorPosition,
		"reviewer":    reviewerPosition,
		"architect":   architectPosition,
	}
	posIDs := map[string]string{}
	for id, content := range agents {
		fmt.Fprintf(os.Stderr, "  %s submitting position...\n", id)
		res := call(ctx, session, "submit_position", map[string]any{
			"deliberation_id": delib.ID, "agent_id": id, "content": content,
		})
		var pos struct {
			ID string `json:"position_id"`
		}
		parse(res, &pos)
		posIDs[id] = pos.ID
	}

	// Voting: each agent votes on the other two
	votes := []struct {
		from, to string
		value    int
	}{
		{"contributor", "reviewer", 0},  // contributor is neutral on reviewer (waits for feedback)
		{"contributor", "architect", 1}, // contributor agrees with architecture assessment
		{"reviewer", "contributor", 0},  // reviewer is neutral (evaluating)
		{"reviewer", "architect", 1},    // reviewer agrees with architecture principles
		{"architect", "contributor", 0}, // architect is neutral (evaluating)
		{"architect", "reviewer", 1},    // architect agrees with quality focus
	}
	for _, v := range votes {
		call(ctx, session, "vote", map[string]any{
			"deliberation_id": delib.ID, "agent_id": v.from, "position_id": posIDs[v.to], "value": v.value,
		})
	}
	fmt.Fprintf(os.Stderr, "Recorded %d votes\n", len(votes))

	// Analyze
	fmt.Fprintln(os.Stderr, "Running analysis (this takes ~2 minutes)...")
	analyzeRes := call(ctx, session, "analyze", map[string]any{"deliberation_id": delib.ID})

	var result struct {
		Cruxes []struct {
			Claim       string   `json:"crux_claim"`
			Topic       string   `json:"topic"`
			Subtopic    string   `json:"subtopic"`
			Agree       []string `json:"agree_agents"`
			Disagree    []string `json:"disagree_agents"`
			Controversy float64  `json:"controversy_score"`
			Explanation string   `json:"explanation"`
		} `json:"cruxes"`
		TopicSummaries    []struct{ Topic, Summary string } `json:"topic_summaries"`
		Confidence        string                            `json:"confidence"`
		IntegrityWarnings []string                          `json:"integrity_warnings"`
	}
	parse(analyzeRes, &result)

	// Output
	fmt.Println()
	fmt.Printf("## PR Review Deliberation: %s\n\n", strings.TrimSpace(string(commitMsg)))
	fmt.Printf("**Confidence:** %s\n", result.Confidence)
	fmt.Printf("**Topics:** %d | **Cruxes:** %d\n\n", len(result.TopicSummaries), len(result.Cruxes))

	if len(result.Cruxes) == 0 {
		fmt.Println("**No cruxes detected** — the agents found no substantive disagreements.")
		fmt.Println("**Recommendation: AUTO-MERGE** — all agents are aligned.")
	} else {
		for i, crux := range result.Cruxes {
			fmt.Printf("### Crux %d: %s > %s (%.0f%% controversial)\n\n", i+1, crux.Topic, crux.Subtopic, crux.Controversy*100)
			fmt.Printf("> %s\n\n", crux.Claim)
			fmt.Printf("**Agree:** %s\n", strings.Join(crux.Agree, ", "))
			fmt.Printf("**Disagree:** %s\n\n", strings.Join(crux.Disagree, ", "))
			fmt.Printf("*%s*\n\n", crux.Explanation)
		}

		if len(result.Cruxes) == 1 && result.Cruxes[0].Controversy < 0.5 {
			fmt.Println("**Recommendation: APPROVE WITH COMMENT** — one minor disagreement, not blocking.")
		} else {
			fmt.Println("**Recommendation: HUMAN REVIEW NEEDED** — unresolved cruxes require maintainer decision.")
		}
	}

	fmt.Println()
	for _, ts := range result.TopicSummaries {
		fmt.Printf("**%s:** %s\n\n", ts.Topic, ts.Summary)
	}

	if len(result.IntegrityWarnings) > 0 {
		fmt.Println("**Integrity Warnings:**")
		for _, w := range result.IntegrityWarnings {
			fmt.Printf("- %s\n", w)
		}
	}

	fmt.Fprintf(os.Stderr, "\nDone.\n")
}

func call(ctx context.Context, s *sdkmcp.ClientSession, name string, args map[string]any) string {
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

func parse(jsonStr string, v any) {
	if err := json.Unmarshal([]byte(jsonStr), v); err != nil {
		fmt.Fprintf(os.Stderr, "parse error: %v\n", err)
		os.Exit(1)
	}
}
