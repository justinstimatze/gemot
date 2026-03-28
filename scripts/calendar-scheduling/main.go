// calendar-scheduling demonstrates N agents negotiating a group meeting time
// through gemot without sharing raw calendar data — only availability windows.
//
// Five team members need a 1-hour meeting. Each agent submits available time
// slots as a position, with conviction reflecting preference strength and
// reservations for hard constraints. Gemot's analysis clusters agents by
// scheduling preference (morning vs afternoon people), finds the crux,
// proposes a compromise slot, and agents commit.
//
// Usage: go run scripts/calendar-scheduling/main.go
//
// Requires: GEMOT_LIVE_URL and GEMOT_API_SECRET env vars (or .env file)

package main

import (
	"context"
	"encoding/json"
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

type agent struct {
	id          string
	name        string
	position    string
	conviction  float64
	reservation string
}

func main() {
	url := envOr("GEMOT_LIVE_URL", "https://gemot.fly.dev/mcp")
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
		fmt.Fprintf(os.Stderr, "GEMOT_API_SECRET not set\n")
		os.Exit(1)
	}

	ctx := context.Background()
	start := time.Now()

	// Connect to gemot
	fmt.Fprintf(os.Stderr, "Connecting to %s...\n", url)
	transport := &sdkmcp.SSEClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: secret},
		},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "calendar-demo", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	fatal(err, "connecting")
	defer session.Close() //nolint:errcheck

	// ── Scenario ──────────────────────────────────────────────────────
	// Five team members need a 1-hour sync this week.
	// Nobody shares calendars — each agent reveals only availability windows.
	//
	// Alice: early bird, hard conflict Thursday PM
	// Bob: afternoon person, all-day offsite Wednesday
	// Carol: flexible but prefers Tuesday/Thursday, school pickup at 3 PM daily
	// Dave: only available Mon/Wed/Fri, remote timezone (starts late)
	// Eve: part-time, only works Mon-Wed

	nextMonday := nextWeekday(time.Monday)
	week := nextMonday.Format("Jan 2") + " – " + nextMonday.AddDate(0, 0, 4).Format("Jan 2")

	mon := nextMonday.Format("Mon Jan 2")
	tue := nextMonday.AddDate(0, 0, 1).Format("Mon Jan 2")
	wed := nextMonday.AddDate(0, 0, 2).Format("Mon Jan 2")
	thu := nextMonday.AddDate(0, 0, 3).Format("Mon Jan 2")
	fri := nextMonday.AddDate(0, 0, 4).Format("Mon Jan 2")

	agents := []agent{
		{
			id:   "alice-agent",
			name: "Alice",
			position: fmt.Sprintf(`Available for a 1-hour meeting:

PREFERRED (morning):
- %s 9:00-11:00 AM
- %s 9:00-10:00 AM
- %s 10:00-12:00 PM

ACCEPTABLE (afternoon):
- %s 2:00-4:00 PM
- %s 1:00-3:00 PM

I strongly prefer mornings — I'm most focused before lunch. Afternoons tend to fill with interruptions.`, mon, wed, fri, tue, fri),
			conviction:  0.7,
			reservation: fmt.Sprintf("Cannot meet %s after 12:00 PM — hard conflict", thu),
		},
		{
			id:   "bob-agent",
			name: "Bob",
			position: fmt.Sprintf(`Available for a 1-hour meeting:

PREFERRED (afternoon):
- %s 2:00-5:00 PM
- %s 1:00-4:00 PM
- %s 2:00-4:00 PM

ACCEPTABLE (morning):
- %s 10:00-11:00 AM
- %s 9:00-11:00 AM

I work best in the afternoon after getting through morning tasks. Wednesday is completely blocked — all-day offsite.`, mon, thu, fri, tue, fri),
			conviction:  0.6,
			reservation: fmt.Sprintf("Cannot meet any time on %s — all-day offsite", wed),
		},
		{
			id:   "carol-agent",
			name: "Carol",
			position: fmt.Sprintf(`Available for a 1-hour meeting:

PREFERRED:
- %s 10:00 AM - 2:00 PM
- %s 10:00 AM - 2:00 PM

ACCEPTABLE:
- %s 9:00 AM - 2:00 PM
- %s 9:00 AM - 2:00 PM
- %s 9:00 AM - 2:00 PM

I'm flexible on the day but I need to leave by 3 PM for school pickup, so the meeting must end by 2:00 PM at the latest. Tuesday or Thursday works best for me.`, tue, thu, mon, wed, fri),
			conviction:  0.5,
			reservation: "Meeting must end by 2:00 PM — school pickup at 3 PM, need travel time",
		},
		{
			id:   "dave-agent",
			name: "Dave",
			position: fmt.Sprintf(`Available for a 1-hour meeting:

PREFERRED:
- %s 11:00 AM - 3:00 PM
- %s 11:00 AM - 3:00 PM

ACCEPTABLE:
- %s 11:00 AM - 3:00 PM

I'm in a later timezone so I can't start before 11 AM. I only work Mon/Wed/Fri — Tue/Thu are blocked for a client engagement.`, mon, fri, wed),
			conviction:  0.8,
			reservation: fmt.Sprintf("Cannot meet before 11:00 AM any day. Cannot meet %s or %s — client engagement", tue, thu),
		},
		{
			id:   "eve-agent",
			name: "Eve",
			position: fmt.Sprintf(`Available for a 1-hour meeting:

PREFERRED:
- %s 10:00 AM - 12:00 PM
- %s 10:00 AM - 12:00 PM

ACCEPTABLE:
- %s 1:00-3:00 PM

I'm part-time and only work Monday through Wednesday. Strong preference for late morning.`, mon, tue, wed),
			conviction:  0.6,
			reservation: fmt.Sprintf("Cannot meet %s or %s — not working those days", thu, fri),
		},
	}

	fmt.Fprintf(os.Stderr, "\n📅 Calendar Scheduling Demo — %d people\n", len(agents))
	fmt.Fprintf(os.Stderr, "   Week of %s\n\n", week)

	// ── Step 1: Create a negotiation-type deliberation ─────────────────
	fmt.Fprintf(os.Stderr, "Creating deliberation...\n")
	createRes := callTool(ctx, session, "create_deliberation", map[string]any{
		"topic":       fmt.Sprintf("Schedule 1-hour team sync, week of %s", week),
		"description": fmt.Sprintf("%d team members negotiate a meeting time by sharing availability windows (not calendar details). Each proposes preferred slots with conviction scores and declares hard constraints as reservations.", len(agents)),
		"type":        "negotiation",
	})
	var delib struct {
		ID string `json:"deliberation_id"`
	}
	mustParse(createRes, &delib)
	fmt.Fprintf(os.Stderr, "  Deliberation: %s\n", delib.ID)

	// ── Step 2: Submit availability as positions ───────────────────────
	// Privacy: agents share WINDOWS, not event names or calendar contents.
	type positionInfo struct {
		agentID    string
		positionID string
	}
	var positions []positionInfo

	for _, a := range agents {
		fmt.Fprintf(os.Stderr, "  %s submitting availability...\n", a.name)
		res := callTool(ctx, session, "submit_position", map[string]any{
			"deliberation_id": delib.ID,
			"agent_id":        a.id,
			"content":         a.position,
			"on_behalf_of":    a.name,
			"conviction":      a.conviction,
			"reservation":     a.reservation,
		})
		var pos struct {
			ID string `json:"position_id"`
		}
		mustParse(res, &pos)
		positions = append(positions, positionInfo{agentID: a.id, positionID: pos.ID})
	}

	// ── Step 3: Cross-vote ────────────────────────────────────────────
	// Each agent votes on every other agent's proposal.
	// +1 = overlapping slots exist, 0 = partial overlap, -1 = no overlap
	//
	// Vote matrix based on schedule overlap analysis:
	//   Alice(AM) vs Bob(PM): partial overlap Tue AM, Fri AM → 0
	//   Alice(AM) vs Carol(mid): good overlap mornings → 1
	//   Alice(AM) vs Dave(late): overlap Mon 11-11 AM narrow → 0
	//   Alice(AM) vs Eve(AM): good overlap Mon/Tue AM → 1
	//   Bob(PM) vs Carol(mid): conflict (Carol leaves 2, Bob starts 2) → -1
	//   Bob(PM) vs Dave(late): overlap Mon 11-3 → 1
	//   Bob(PM) vs Eve(AM/Wed PM): minimal → -1
	//   Carol(mid) vs Dave(late): overlap Mon 11-2 → 1
	//   Carol(mid) vs Eve(AM): overlap Mon/Tue 10-12 → 1
	//   Dave(late) vs Eve(AM): overlap Mon 11-12 → 0

	voteMatrix := []struct {
		from, toAgent string
		value         int
	}{
		// Alice votes on others
		{"alice-agent", "bob-agent", 0},
		{"alice-agent", "carol-agent", 1},
		{"alice-agent", "dave-agent", 0},
		{"alice-agent", "eve-agent", 1},
		// Bob votes on others
		{"bob-agent", "alice-agent", 0},
		{"bob-agent", "carol-agent", -1},
		{"bob-agent", "dave-agent", 1},
		{"bob-agent", "eve-agent", -1},
		// Carol votes on others
		{"carol-agent", "alice-agent", 1},
		{"carol-agent", "bob-agent", -1},
		{"carol-agent", "dave-agent", 1},
		{"carol-agent", "eve-agent", 1},
		// Dave votes on others
		{"dave-agent", "alice-agent", 0},
		{"dave-agent", "bob-agent", 1},
		{"dave-agent", "carol-agent", 1},
		{"dave-agent", "eve-agent", 0},
		// Eve votes on others
		{"eve-agent", "alice-agent", 1},
		{"eve-agent", "bob-agent", -1},
		{"eve-agent", "carol-agent", 1},
		{"eve-agent", "dave-agent", 0},
	}

	// Resolve position IDs
	posMap := map[string]string{}
	for _, p := range positions {
		posMap[p.agentID] = p.positionID
	}

	fmt.Fprintf(os.Stderr, "  Agents voting (%d votes)...\n", len(voteMatrix))
	for _, v := range voteMatrix {
		callTool(ctx, session, "vote", map[string]any{
			"deliberation_id": delib.ID,
			"agent_id":        v.from,
			"position_id":     posMap[v.toAgent],
			"value":           v.value,
		})
	}

	// ── Step 4: Analyze to find the crux ──────────────────────────────
	fmt.Fprintf(os.Stderr, "  Running analysis (finding scheduling crux)...\n")
	callTool(ctx, session, "analyze", map[string]any{
		"deliberation_id": delib.ID,
	})

	// Poll until analysis completes (analyze runs async)
	fmt.Fprintf(os.Stderr, "  Waiting for analysis...\n")
	for i := 0; i < 120; i++ {
		time.Sleep(5 * time.Second)
		statusRes := callTool(ctx, session, "get_deliberation", map[string]any{
			"deliberation_id": delib.ID,
		})
		var d struct {
			Status    string `json:"status"`
			SubStatus string `json:"sub_status"`
		}
		mustParse(statusRes, &d)
		fmt.Fprintf(os.Stderr, "    Status: %s/%s\n", d.Status, d.SubStatus)
		if d.Status != "analyzing" {
			break
		}
	}

	// ── Step 5: Get each agent's personalized context ─────────────────
	fmt.Fprintf(os.Stderr, "  Fetching analysis results...\n")
	for _, a := range agents {
		res := callTool(ctx, session, "get_context", map[string]any{
			"deliberation_id": delib.ID,
			"agent_id":        a.id,
		})
		fmt.Fprintf(os.Stderr, "\n── %s's Context ──\n", a.name)
		prettyPrint(res)
	}

	// ── Step 6: Propose compromise ────────────────────────────────────
	fmt.Fprintf(os.Stderr, "\n  Generating compromise proposal...\n")
	compromiseRes := callTool(ctx, session, "propose_compromise", map[string]any{
		"deliberation_id": delib.ID,
	})
	fmt.Fprintf(os.Stderr, "\n── Compromise Proposal ──\n")
	prettyPrint(compromiseRes)

	// ── Step 7: Agents commit ─────────────────────────────────────────
	// In a real scenario, each agent would parse the compromise and decide.
	// Here we simulate all accepting, with conditional commitments.

	fmt.Fprintf(os.Stderr, "\n  Agents committing to the proposed time...\n")

	for i, a := range agents {
		conditional := ""
		if i == 0 {
			// First agent commits conditionally on majority
			conditional = "if at least 3 of 5 agents also commit"
		}
		callTool(ctx, session, "commit", map[string]any{
			"deliberation_id": delib.ID,
			"agent_id":        a.id,
			"statement":       fmt.Sprintf("%s accepts the meeting time proposed by the compromise for week of %s", a.name, week),
			"conditional":     conditional,
		})
		if conditional != "" {
			fmt.Fprintf(os.Stderr, "    ✓ %s committed (conditional: %s)\n", a.name, conditional)
		} else {
			fmt.Fprintf(os.Stderr, "    ✓ %s committed\n", a.name)
		}
	}

	// ── Step 8: Verify commitments ────────────────────────────────────
	commitmentsRes := callTool(ctx, session, "get_commitments", map[string]any{
		"deliberation_id": delib.ID,
	})
	fmt.Fprintf(os.Stderr, "\n── Final Commitments ──\n")
	prettyPrint(commitmentsRes)

	duration := time.Since(start)
	fmt.Fprintf(os.Stderr, "\n✅ Calendar scheduling complete in %s\n", duration.Round(time.Second))
	fmt.Fprintf(os.Stderr, "   %d agents, deliberation ID: %s\n", len(agents), delib.ID)

	// Print machine-readable summary to stdout
	agentNames := make([]string, len(agents))
	for i, a := range agents {
		agentNames[i] = fmt.Sprintf("%s (%s)", a.id, a.name)
	}
	summary := map[string]any{
		"deliberation_id": delib.ID,
		"agents":          agentNames,
		"agent_count":     len(agents),
		"week":            week,
		"duration":        duration.Round(time.Second).String(),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(summary) //nolint:errcheck
}

// nextWeekday returns the next occurrence of the given weekday.
func nextWeekday(day time.Weekday) time.Time {
	now := time.Now()
	daysUntil := int(day) - int(now.Weekday())
	if daysUntil <= 0 {
		daysUntil += 7
	}
	return time.Date(now.Year(), now.Month(), now.Day()+daysUntil, 0, 0, 0, 0, now.Location())
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

func mustParse(jsonStr string, v any) {
	// Server may append hints after "---" separator; strip them
	if idx := strings.Index(jsonStr, "\n\n---\n"); idx != -1 {
		jsonStr = jsonStr[:idx]
	}
	if err := json.Unmarshal([]byte(jsonStr), v); err != nil {
		fmt.Fprintf(os.Stderr, "parse error: %v\nraw: %s\n", err, jsonStr[:min(200, len(jsonStr))])
		os.Exit(1)
	}
}

func prettyPrint(jsonStr string) {
	var v any
	if err := json.Unmarshal([]byte(jsonStr), &v); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", jsonStr)
		return
	}
	enc := json.NewEncoder(os.Stderr)
	enc.SetIndent("", "  ")
	enc.Encode(v) //nolint:errcheck
}

func envOr(k, v string) string {
	if e := os.Getenv(k); e != "" {
		return e
	}
	return v
}

func fatal(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", msg, err)
		os.Exit(1)
	}
}
