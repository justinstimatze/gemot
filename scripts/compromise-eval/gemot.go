package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This is the Phase 2 client: it drives a live gemot server so the LLM arms
// (structured synthesis vs the freeform/chat analyzer) submit the SAME prose
// positions and are scored by the SAME checker as the deterministic arms.

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(req)
}

// Gemot is a small MCP client for the gemot server, reconnecting once per call
// because the SSE transport drops idle sessions.
type Gemot struct {
	url     string
	secret  string
	session *sdkmcp.ClientSession
	Calls   int
}

func NewGemot(url, secret string) *Gemot { return &Gemot{url: url, secret: secret} }

func (g *Gemot) connect(ctx context.Context) error {
	if g.session != nil {
		return nil
	}
	transport := &sdkmcp.SSEClientTransport{
		Endpoint: g.url,
		HTTPClient: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: g.secret},
		},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "compromise-eval", Version: "1.0"}, nil)
	s, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return err
	}
	g.session = s
	return nil
}

func (g *Gemot) Close() {
	if g.session != nil {
		g.session.Close() //nolint:errcheck
		g.session = nil
	}
}

func (g *Gemot) Call(ctx context.Context, tool string, args map[string]any) (string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := g.connect(ctx); err != nil {
			g.Close()
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		res, err := g.session.CallTool(callCtx, &sdkmcp.CallToolParams{Name: tool, Arguments: args})
		cancel()
		g.Calls++
		if err != nil {
			g.Close()
			continue
		}
		if len(res.Content) == 0 {
			return "", fmt.Errorf("%s: empty response", tool)
		}
		text := res.Content[0].(*sdkmcp.TextContent).Text
		if res.IsError {
			return "", fmt.Errorf("%s: %s", tool, text)
		}
		if idx := strings.Index(text, "\n\n---\n"); idx != -1 {
			text = text[:idx]
		}
		return text, nil
	}
	return "", fmt.Errorf("%s: connection failed after retry", tool)
}

func (g *Gemot) CreateDeliberation(ctx context.Context, topic, description, template, groupID string) (string, error) {
	out, err := g.Call(ctx, "deliberation", map[string]any{
		"action": "create", "topic": topic, "description": description,
		"template": template, "type": "negotiation", "group_id": groupID,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		DeliberationID string `json:"deliberation_id"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", fmt.Errorf("parse create: %w", err)
	}
	if resp.DeliberationID == "" {
		return "", fmt.Errorf("create returned no deliberation_id")
	}
	return resp.DeliberationID, nil
}

func (g *Gemot) SubmitPosition(ctx context.Context, delibID, agentID, content string) (string, error) {
	out, err := g.Call(ctx, "participate", map[string]any{
		"action": "submit_position", "deliberation_id": delibID,
		"agent_id": agentID, "content": content,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		PositionID string `json:"position_id"`
		ID         string `json:"id"`
	}
	json.Unmarshal([]byte(out), &resp) //nolint:errcheck
	if resp.PositionID != "" {
		return resp.PositionID, nil
	}
	return resp.ID, nil
}

func (g *Gemot) Vote(ctx context.Context, delibID, agentID, positionID string, value int) error {
	_, err := g.Call(ctx, "participate", map[string]any{
		"action": "vote", "deliberation_id": delibID,
		"agent_id": agentID, "position_id": positionID, "value": value,
	})
	return err
}

// AnalyzeRun triggers analysis and polls until it completes (bare result, no
// "status" field), matching the chess harness's status semantics.
func (g *Gemot) AnalyzeRun(ctx context.Context, delibID string, timeout time.Duration) error {
	if _, err := g.Call(ctx, "analyze", map[string]any{"action": "run", "deliberation_id": delibID}); err != nil {
		return fmt.Errorf("analyze run: %w", err)
	}
	start := time.Now()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := g.Call(ctx, "analyze", map[string]any{"action": "get_result", "deliberation_id": delibID})
		if err == nil && out != "" {
			var raw struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(out), &raw) == nil {
				switch raw.Status {
				case "":
					return nil
				case "not_started":
					if time.Since(start) > 20*time.Second {
						return fmt.Errorf("analysis never started (%s — is ANTHROPIC_API_KEY set on the server?)", raw.Message)
					}
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("analysis timed out after %s", timeout)
}

// ProposeCompromise triggers synthesis (options-empty GenerateCompromise) and
// returns the free-text compromise statement.
func (g *Gemot) ProposeCompromise(ctx context.Context, delibID string) (string, error) {
	out, err := g.Call(ctx, "analyze", map[string]any{"action": "propose_compromise", "deliberation_id": delibID})
	if err != nil {
		return "", err
	}
	var resp struct {
		CompromiseProposal string `json:"compromise_proposal"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", fmt.Errorf("parse compromise: %w", err)
	}
	return resp.CompromiseProposal, nil
}

// dayFull maps full weekday names to the abbreviations Label uses, so a
// compromise that writes "Friday at 14:00" still resolves to "Fri 14:00".
var dayFull = map[string]string{
	"Monday": "Mon", "Tuesday": "Tue", "Wednesday": "Wed", "Thursday": "Thu",
	"Friday": "Fri", "Saturday": "Sat", "Sunday": "Sun",
}

// normalizeForMatch loosens slot phrasing: full day names -> abbreviations and
// "Day at HH:MM" -> "Day HH:MM", so free-text compromises match Label().
func normalizeForMatch(s string) string {
	for full, ab := range dayFull {
		s = strings.ReplaceAll(s, full, ab)
	}
	return strings.ReplaceAll(s, " at ", " ")
}

// parseSlot finds the earliest-occurring slot label in the compromise text —
// the decision the synthesis names. Returns ok=false if no label appears. It
// normalizes both text and labels so day-name and "at" variants still resolve.
func parseSlot(in Instance, text string) (Slot, bool) {
	text = normalizeForMatch(text)
	best, bestIdx := Slot(-1), -1
	for s := Slot(0); int(s) < in.slots(); s++ {
		if idx := strings.Index(text, normalizeForMatch(in.Label(s))); idx != -1 {
			if bestIdx == -1 || idx < bestIdx {
				best, bestIdx = s, idx
			}
		}
	}
	return best, bestIdx != -1
}

// voteValue encodes the voter's true view of the proposer's slot into a gemot
// vote (-2..+2), so the structured analyzer has real conflict signal: a slot
// the voter is blocked on is a hard -2; otherwise it scales with preference.
func voteValue(in Instance, voter int, proposerSlot Slot) int {
	if in.Agents[voter].Blocked[proposerSlot] {
		return -2
	}
	if in.Agents[voter].Pref[proposerSlot] > 0 {
		return 2
	}
	return 1
}

// RunGemotArm runs one instance through a live gemot server and returns the
// slot its synthesized compromise names. ok is false if any step fails or the
// compromise names no slot (scored as infeasible by the caller).
func RunGemotArm(ctx context.Context, g *Gemot, in Instance, template, groupID string) (Slot, string, bool) {
	var slotList []string
	for s := Slot(0); int(s) < in.slots(); s++ {
		slotList = append(slotList, in.Label(s))
	}
	topic := "Schedule one meeting everyone can attend"
	desc := fmt.Sprintf("Choose exactly one time slot for a meeting all participants can attend. "+
		"Available slots: %s. Each participant states their hard conflicts and preferences. "+
		"State the single chosen slot using its exact label (e.g. \"%s\").",
		strings.Join(slotList, ", "), in.Label(0))

	delibID, err := g.CreateDeliberation(ctx, topic, desc, template, groupID)
	if err != nil {
		fmt.Printf("    [gemot] create failed: %v\n", err)
		return -1, "", false
	}
	posOf := make([]string, len(in.Agents))
	for i := range in.Agents {
		pid, err := g.SubmitPosition(ctx, delibID, in.Agents[i].Name, in.RenderPosition(i))
		if err != nil {
			fmt.Printf("    [gemot] submit %s failed: %v\n", in.Agents[i].Name, err)
			return -1, "", false
		}
		posOf[i] = pid
	}
	for voter := range in.Agents {
		for proposer := range in.Agents {
			if voter == proposer || posOf[proposer] == "" {
				continue
			}
			_ = g.Vote(ctx, delibID, in.Agents[voter].Name, posOf[proposer], voteValue(in, voter, in.Proposal(proposer)))
		}
	}
	if err := g.AnalyzeRun(ctx, delibID, 5*time.Minute); err != nil {
		fmt.Printf("    [gemot] analyze failed: %v\n", err)
		return -1, "", false
	}
	compromise, err := g.ProposeCompromise(ctx, delibID)
	if err != nil {
		fmt.Printf("    [gemot] propose_compromise failed: %v\n", err)
		return -1, "", false
	}
	slot, ok := parseSlot(in, compromise)
	if !ok {
		fmt.Printf("    [gemot] compromise named no slot: %q\n", truncate(compromise, 160))
		return -1, compromise, false
	}
	return slot, compromise, true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
