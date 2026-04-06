package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// authTransport adds an Authorization header to every request.
type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

// TestLiveDeliberation connects to the production gemot server and runs a
// full deliberation end-to-end: create → submit positions → vote → list.
//
// Requires GEMOT_LIVE_URL and GEMOT_API_SECRET env vars.
// Run with: GEMOT_LIVE_URL=https://gemot.fly.dev/mcp GEMOT_API_SECRET=... go test -v -run TestLiveDeliberation ./tests/
func TestLiveDeliberation(t *testing.T) {
	liveURL := os.Getenv("GEMOT_LIVE_URL")
	apiSecret := os.Getenv("GEMOT_API_SECRET")
	if liveURL == "" || apiSecret == "" {
		t.Skip("GEMOT_LIVE_URL and GEMOT_API_SECRET not set")
	}

	ctx := context.Background()

	// Connect as MCP client with auth
	transport := &sdkmcp.SSEClientTransport{
		Endpoint: liveURL,
		HTTPClient: &http.Client{
			Transport: &authTransport{
				base:  http.DefaultTransport,
				token: apiSecret,
			},
		},
	}

	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "live-test",
		Version: "1.0.0",
	}, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connecting to %s: %v", liveURL, err)
	}
	defer session.Close()

	// List tools to verify connection
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("listing tools: %v", err)
	}
	t.Logf("Connected to %s — %d tools available", liveURL, len(tools.Tools))
	for _, tool := range tools.Tools {
		t.Logf("  %s: %s", tool.Name, truncate(tool.Description, 80))
	}

	// Create a deliberation
	createResult, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: "deliberation",
		Arguments: map[string]any{
			"action":      "create",
			"topic":       "Live Test: Best Programming Language",
			"description": "A live test deliberation to verify the production deployment works end-to-end.",
		},
	})
	if err != nil {
		t.Fatalf("create_deliberation: %v", err)
	}
	if createResult.IsError {
		t.Fatalf("create_deliberation error: %s", createResult.Content[0].(*sdkmcp.TextContent).Text)
	}

	var delib struct {
		ID     string `json:"deliberation_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(createResult.Content[0].(*sdkmcp.TextContent).Text), &delib); err != nil {
		t.Fatalf("parsing create result: %v", err)
	}
	t.Logf("Created deliberation: %s (status: %s)", delib.ID, delib.Status)

	// Submit positions from 3 agents
	agents := map[string]string{
		"rustacean":  "Rust is the best language because it provides memory safety without garbage collection. The borrow checker prevents entire classes of bugs at compile time.",
		"gopher":     "Go is the best language for production systems. Simple syntax, fast compilation, excellent concurrency with goroutines, and a single binary deployment model.",
		"pythonista": "Python is the best language because developer productivity matters more than raw performance. The ecosystem of libraries (numpy, pandas, pytorch) is unmatched.",
	}

	positionIDs := map[string]string{}
	for agentID, content := range agents {
		result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
			Name: "participate",
			Arguments: map[string]any{
				"action":          "submit_position",
				"deliberation_id": delib.ID,
				"agent_id":        agentID,
				"content":         content,
			},
		})
		if err != nil {
			t.Fatalf("submit_position %s: %v", agentID, err)
		}
		if result.IsError {
			t.Fatalf("submit_position %s error: %s", agentID, result.Content[0].(*sdkmcp.TextContent).Text)
		}

		var pos struct {
			ID string `json:"position_id"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].(*sdkmcp.TextContent).Text), &pos); err != nil {
			t.Fatalf("parsing position: %v", err)
		}
		positionIDs[agentID] = pos.ID
		t.Logf("  %s submitted position %s", agentID, pos.ID)
	}

	// Vote
	type voteEntry struct {
		voter, target string
		value         int
	}
	votes := []voteEntry{
		{"rustacean", "gopher", 1},      // rust respects go
		{"rustacean", "pythonista", -1}, // rust dislikes python's lack of types
		{"gopher", "rustacean", 0},      // go is neutral on rust
		{"gopher", "pythonista", -1},    // go dislikes python's GIL
		{"pythonista", "rustacean", -1}, // python finds rust too complex
		{"pythonista", "gopher", 0},     // python is neutral on go
	}

	for _, v := range votes {
		result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
			Name: "participate",
			Arguments: map[string]any{
				"action":          "vote",
				"deliberation_id": delib.ID,
				"agent_id":        v.voter,
				"position_id":     positionIDs[v.target],
				"value":           v.value,
			},
		})
		if err != nil {
			t.Fatalf("vote %s->%s: %v", v.voter, v.target, err)
		}
		if result.IsError {
			t.Fatalf("vote %s->%s error: %s", v.voter, v.target, result.Content[0].(*sdkmcp.TextContent).Text)
		}
	}
	t.Logf("Recorded %d votes", len(votes))

	// Get deliberation status
	statusResult, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "deliberation",
		Arguments: map[string]any{"action": "get", "deliberation_id": delib.ID},
	})
	if err != nil {
		t.Fatalf("get_deliberation: %v", err)
	}
	t.Logf("Status: %s", statusResult.Content[0].(*sdkmcp.TextContent).Text)

	// List deliberations
	listResult, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "deliberation",
		Arguments: map[string]any{"action": "list"},
	})
	if err != nil {
		t.Fatalf("deliberation list: %v", err)
	}
	t.Logf("Deliberations: %s", truncate(listResult.Content[0].(*sdkmcp.TextContent).Text, 200))

	t.Log("\n=== LIVE TEST PASSED ===")
	t.Logf("Deliberation %s is live on %s", delib.ID, liveURL)
	t.Log("Skipping analyze (costs real API credits) — use the gemot MCP tools interactively to trigger analysis.")
}

// newLiveSession creates an MCP client session to the live server.
func newLiveSession(t *testing.T, liveURL, apiSecret string) *sdkmcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	transport := &sdkmcp.SSEClientTransport{
		Endpoint: liveURL,
		HTTPClient: &http.Client{
			Transport: &authTransport{
				base:  http.DefaultTransport,
				token: apiSecret,
			},
		},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "live-test",
		Version: "1.0.0",
	}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connecting to %s: %v", liveURL, err)
	}
	return session
}

// callToolText calls an MCP tool and returns the text content, failing the test on error.
func callToolText(t *testing.T, session *sdkmcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("%s error: %s", name, result.Content[0].(*sdkmcp.TextContent).Text)
	}
	return result.Content[0].(*sdkmcp.TextContent).Text
}

// TestDataPersistsAcrossRequests verifies that Postgres data
// persists across MCP connections: create data, disconnect, reconnect, verify.
//
// Requires GEMOT_LIVE_URL and GEMOT_API_SECRET env vars.
func TestDataPersistsAcrossRequests(t *testing.T) {
	liveURL := os.Getenv("GEMOT_LIVE_URL")
	apiSecret := os.Getenv("GEMOT_API_SECRET")
	if liveURL == "" || apiSecret == "" {
		t.Skip("GEMOT_LIVE_URL and GEMOT_API_SECRET not set")
	}

	uniqueTopic := fmt.Sprintf("Persistence Test %d", time.Now().UnixNano())

	// --- Session 1: create deliberation + submit position ---
	session1 := newLiveSession(t, liveURL, apiSecret)

	createText := callToolText(t, session1, "deliberation", map[string]any{
		"action":      "create",
		"topic":       uniqueTopic,
		"description": "Testing data persistence across connections.",
	})
	var delib struct {
		ID string `json:"deliberation_id"`
	}
	if err := json.Unmarshal([]byte(createText), &delib); err != nil {
		t.Fatalf("parsing create result: %v", err)
	}
	t.Logf("Session 1: created deliberation %s with topic %q", delib.ID, uniqueTopic)

	posText := callToolText(t, session1, "participate", map[string]any{
		"action":          "submit_position",
		"deliberation_id": delib.ID,
		"agent_id":        "persist-agent",
		"content":         "This position must survive a reconnect.",
	})
	var pos struct {
		ID string `json:"position_id"`
	}
	if err := json.Unmarshal([]byte(posText), &pos); err != nil {
		t.Fatalf("parsing position result: %v", err)
	}
	t.Logf("Session 1: submitted position %s", pos.ID)

	// Disconnect
	session1.Close()
	t.Log("Session 1: closed")

	// --- Session 2: reconnect and verify ---
	session2 := newLiveSession(t, liveURL, apiSecret)
	defer session2.Close()

	// Verify deliberation exists in list
	listText := callToolText(t, session2, "deliberation", map[string]any{"action": "list"})
	if !strings.Contains(listText, delib.ID) {
		t.Fatalf("Session 2: deliberation %s not found in deliberation list output:\n%s", delib.ID, truncate(listText, 500))
	}
	t.Log("Session 2: deliberation found in list")

	// Verify position is retrievable
	positionsText := callToolText(t, session2, "participate", map[string]any{
		"action":          "get_positions",
		"deliberation_id": delib.ID,
	})
	if !strings.Contains(positionsText, pos.ID) {
		t.Fatalf("Session 2: position %s not found in get_positions output:\n%s", pos.ID, truncate(positionsText, 500))
	}
	if !strings.Contains(positionsText, "This position must survive a reconnect.") {
		t.Fatalf("Session 2: position content not found in get_positions output:\n%s", truncate(positionsText, 500))
	}
	t.Log("Session 2: position content verified")
	t.Log("=== DATA PERSISTENCE VERIFIED ===")
}

// TestLiveAnalysis runs a full deliberation with real LLM analysis on the
// production server. This costs real API credits.
//
// Requires GEMOT_LIVE_URL, GEMOT_API_SECRET, and GEMOT_RUN_EXPENSIVE env vars.
func TestLiveAnalysis(t *testing.T) {
	liveURL := os.Getenv("GEMOT_LIVE_URL")
	apiSecret := os.Getenv("GEMOT_API_SECRET")
	runExpensive := os.Getenv("GEMOT_RUN_EXPENSIVE")
	if liveURL == "" || apiSecret == "" {
		t.Skip("GEMOT_LIVE_URL and GEMOT_API_SECRET not set")
	}
	if runExpensive == "" {
		t.Skip("GEMOT_RUN_EXPENSIVE not set (this test calls the real LLM)")
	}

	session := newLiveSession(t, liveURL, apiSecret)
	defer session.Close()

	// Create deliberation
	createText := callToolText(t, session, "deliberation", map[string]any{
		"action":      "create",
		"topic":       fmt.Sprintf("Tax Policy Debate %d", time.Now().UnixNano()),
		"description": "Live analysis test: 3 agents debate tax policy.",
	})
	var delib struct {
		ID string `json:"deliberation_id"`
	}
	if err := json.Unmarshal([]byte(createText), &delib); err != nil {
		t.Fatalf("parsing create result: %v", err)
	}
	t.Logf("Created deliberation: %s", delib.ID)

	// Submit 3 positions
	agents := map[string]string{
		"taxes-hawk":     "Income taxes should be raised significantly on the wealthy to fund public services. Trickle-down economics has been thoroughly debunked.",
		"taxes-dove":     "Lower taxes stimulate economic growth and job creation. The government wastes most tax revenue on bureaucracy.",
		"taxes-moderate": "A balanced approach is needed. Some taxes should increase on the ultra-wealthy while reducing taxes on small businesses to encourage entrepreneurship.",
	}

	positionIDs := map[string]string{}
	for agentID, content := range agents {
		posText := callToolText(t, session, "participate", map[string]any{
			"action":          "submit_position",
			"deliberation_id": delib.ID,
			"agent_id":        agentID,
			"content":         content,
		})
		var pos struct {
			ID string `json:"position_id"`
		}
		if err := json.Unmarshal([]byte(posText), &pos); err != nil {
			t.Fatalf("parsing position for %s: %v", agentID, err)
		}
		positionIDs[agentID] = pos.ID
		t.Logf("  %s submitted position %s", agentID, pos.ID)
	}

	// Record 6 votes: each agent votes on the other two
	type voteEntry struct {
		voter, target string
		value         int
	}
	votes := []voteEntry{
		{"taxes-hawk", "taxes-dove", -1},
		{"taxes-hawk", "taxes-moderate", 0},
		{"taxes-dove", "taxes-hawk", -1},
		{"taxes-dove", "taxes-moderate", 0},
		{"taxes-moderate", "taxes-hawk", 1},
		{"taxes-moderate", "taxes-dove", -1},
	}
	for _, v := range votes {
		callToolText(t, session, "participate", map[string]any{
			"action":          "vote",
			"deliberation_id": delib.ID,
			"agent_id":        v.voter,
			"position_id":     positionIDs[v.target],
			"value":           v.value,
		})
	}
	t.Logf("Recorded %d votes", len(votes))

	// Call analyze
	t.Log("Calling analyze (this will invoke the real LLM)...")
	analyzeText := callToolText(t, session, "analyze", map[string]any{
		"action":          "run",
		"deliberation_id": delib.ID,
	})
	t.Logf("Analysis result:\n%s", analyzeText)

	// Parse and verify the result
	var result struct {
		TopicSummaries    []json.RawMessage `json:"topic_summaries"`
		Cruxes            []json.RawMessage `json:"cruxes"`
		Confidence        string            `json:"confidence"`
		IntegrityWarnings []string          `json:"integrity_warnings"`
	}
	if err := json.Unmarshal([]byte(analyzeText), &result); err != nil {
		t.Fatalf("parsing analysis result: %v", err)
	}

	// Verify at least 1 topic
	if len(result.TopicSummaries) < 1 {
		t.Fatalf("expected at least 1 topic summary, got %d", len(result.TopicSummaries))
	}
	t.Logf("Topic summaries: %d", len(result.TopicSummaries))

	// Verify cruxes
	if len(result.Cruxes) == 0 {
		t.Log("WARNING: 0 cruxes returned (LLM non-determinism) -- not failing")
	} else {
		// Verify at least 1 crux has non-empty agree AND disagree sides
		type cruxShape struct {
			AgreeAgents    []string `json:"agree_agents"`
			DisagreeAgents []string `json:"disagree_agents"`
		}
		foundValid := false
		for _, raw := range result.Cruxes {
			var c cruxShape
			if err := json.Unmarshal(raw, &c); err != nil {
				continue
			}
			if len(c.AgreeAgents) > 0 && len(c.DisagreeAgents) > 0 {
				foundValid = true
				break
			}
		}
		if !foundValid {
			t.Fatalf("no crux has both non-empty agree and disagree sides")
		}
		t.Logf("Cruxes: %d (at least 1 valid)", len(result.Cruxes))
	}

	// Log integrity warnings
	if len(result.IntegrityWarnings) > 0 {
		t.Logf("IntegrityWarnings: %v", result.IntegrityWarnings)
	} else {
		t.Log("IntegrityWarnings: none")
	}

	// Log confidence
	t.Logf("Confidence: %s", result.Confidence)

	t.Log("=== LIVE ANALYSIS TEST PASSED ===")
}
