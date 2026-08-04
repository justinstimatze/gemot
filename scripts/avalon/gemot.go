package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP client for the gemot server — same shape as scripts/codenames.

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

// Gemot is a resilient MCP client that reconnects once per call.
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
		Endpoint:   g.url,
		HTTPClient: &http.Client{Transport: &authTransport{base: http.DefaultTransport, token: g.secret}},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "avalon", Version: "1.0"}, nil)
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
		callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
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
		"template": template, "type": "reasoning", "group_id": groupID,
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
		"action": "submit_position", "deliberation_id": delibID, "agent_id": agentID, "content": content,
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
		"action": "vote", "deliberation_id": delibID, "agent_id": agentID, "position_id": positionID, "value": value,
	})
	return err
}

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
						return fmt.Errorf("analysis never started (%s)", raw.Message)
					}
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("analysis timed out after %s", timeout)
}

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

// Positioner is a player that can state a structured discussion position. LLM
// agents implement it; the deliberation arms require it and skip seats that don't.
type Positioner interface {
	Position(v GameView) (statement string, suspects []int)
}

func positionOf(p Player, v GameView) (string, []int) {
	if pn, ok := p.(Positioner); ok {
		return pn.Position(v)
	}
	return "", nil
}

// chatDiscuss is the unstructured control: every seat speaks once (seeing prior
// speakers this round), producing a public transcript. No aggregation.
func chatDiscuss(g *Game, players []Player, knows []PlayerKnowledge, log []string) []Statement {
	stmts := make([]string, g.NumPlayers)
	var wg sync.WaitGroup
	for seat := 0; seat < g.NumPlayers; seat++ {
		wg.Add(1)
		go func(seat int) {
			defer wg.Done()
			stmts[seat], _ = positionOf(players[seat], viewFor(g, seat, knows, log, nil, nil))
		}(seat)
	}
	wg.Wait()
	var transcript []Statement
	for seat, st := range stmts {
		if st != "" {
			transcript = append(transcript, Statement{Seat: seat, Text: st})
		}
	}
	return transcript
}

// seatPosition is one player's public position for a structured round.
type seatPosition struct {
	seat     int
	text     string
	suspects []int
}

// GemotArm runs the structured discussion: the same per-seat positions as the
// chat arm, but submitted to a gemot deliberation that votes, analyses, and
// synthesises a trust recommendation appended to the transcript. The delta vs
// chatDiscuss is exactly the gemot aggregation layer.
//
// It never silently degrades: aggregate() returns errors, discuss() retries once
// and counts any fallback so main can report (and flag) contamination. A run with
// Degraded > 0 has structured data points that are really chat and must not be
// trusted as-is.
type GemotArm struct {
	g              *Gemot
	template       string
	groupBase      string
	counter        int
	Deliberations  int           // aggregation attempts (one per quest that had >=2 positions)
	Degraded       int           // attempts that fell back to chat after a retry
	AnalyzeTimeout time.Duration // per-deliberation analysis poll deadline (default 3m if unset)
	MaxAggregate   time.Duration // slowest single aggregation observed (instrumentation)
	journal        *Journal
}

func NewGemotArm(g *Gemot, template, groupBase string, j *Journal) *GemotArm {
	return &GemotArm{g: g, template: template, groupBase: groupBase, journal: j}
}

func (gm *GemotArm) discuss(g *Game, players []Player, knows []PlayerKnowledge, log []string) []Statement {
	ctx := context.Background()

	slots := make([]*seatPosition, g.NumPlayers)
	var wg sync.WaitGroup
	for seat := 0; seat < g.NumPlayers; seat++ {
		wg.Add(1)
		go func(seat int) {
			defer wg.Done()
			if st, sus := positionOf(players[seat], viewFor(g, seat, knows, log, nil, nil)); st != "" {
				slots[seat] = &seatPosition{seat, st, sus}
			}
		}(seat)
	}
	wg.Wait()
	var poss []seatPosition
	for _, p := range slots {
		if p != nil {
			poss = append(poss, *p)
		}
	}
	transcript := make([]Statement, 0, len(poss)+1)
	for _, p := range poss {
		transcript = append(transcript, Statement{Seat: p.seat, Text: p.text})
	}
	if len(poss) < 2 {
		fmt.Fprintf(os.Stderr, "  [structured q%d] only %d positions; skipping aggregation\n", g.Quest+1, len(poss))
		return transcript
	}

	gm.Deliberations++
	start := time.Now()
	synth, err := gm.aggregate(ctx, g, poss)
	if err != nil {
		// One retry on a fresh connection before giving up.
		gm.g.Close()
		synth, err = gm.aggregate(ctx, g, poss)
	}
	dur := time.Since(start)
	if dur > gm.MaxAggregate {
		gm.MaxAggregate = dur
	}
	if err != nil {
		gm.Degraded++
		fmt.Fprintf(os.Stderr, "  [structured q%d] DEGRADED to chat (gemot failed twice after %s): %v\n", g.Quest+1, dur.Round(time.Second), err)
		if gm.journal != nil {
			gm.journal.Record(JournalEntry{Quest: g.Quest + 1, Phase: "structured", Seat: -1,
				Role: "gemot", Action: "degraded", Private: err.Error()})
		}
		return transcript
	}
	fmt.Fprintf(os.Stderr, "  [structured q%d] aggregated in %s\n", g.Quest+1, dur.Round(time.Second))
	if gm.journal != nil {
		gm.journal.Record(JournalEntry{Quest: g.Quest + 1, Phase: "structured", Seat: -1,
			Role: "gemot", Action: "synthesis", Choice: synth})
	}
	transcript = append(transcript, Statement{Seat: -1, Text: synth})
	return transcript
}

// aggregate runs one gemot deliberation over the seat positions and returns the
// synthesised trust recommendation, or an error. It NEVER swallows a failure —
// the caller counts degradations so the structured arm can never quietly become
// the chat arm.
func (gm *GemotArm) aggregate(ctx context.Context, g *Game, poss []seatPosition) (string, error) {
	gm.counter++
	groupID := fmt.Sprintf("%s_q%d_%d", gm.groupBase, g.Quest, gm.counter)
	topic := fmt.Sprintf("Avalon quest %d: which players are safe to send on the quest?", g.Quest+1)
	desc := fmt.Sprintf(`%s

This is a %d-player game; the seats are 0..%d. %s

Each position below is one player's public read on who is good vs evil. Some of these players are EVIL and are lying to protect their team. Weigh the claims against the voting and quest evidence, and synthesise the table's collective judgment into a recommendation of which seats are safest (most likely GOOD) to place on the quest team.

End your recommendation with a single line in exactly this form:
RECOMMEND TRUST: seat, seat, ...
listing the seats safest to send, most to least trusted.`,
		gameRules, g.NumPlayers, g.NumPlayers-1, scoreLineFor(g))

	delibID, err := gm.g.CreateDeliberation(ctx, topic, desc, gm.template, groupID)
	if err != nil {
		return "", fmt.Errorf("create: %w", err)
	}
	posID := make(map[int]string, len(poss))
	for _, p := range poss {
		content := fmt.Sprintf("Seat %d's read: %s", p.seat, p.text)
		if len(p.suspects) > 0 {
			content += fmt.Sprintf(" (most suspects seats: %s)", seatList(p.suspects))
		}
		id, serr := gm.g.SubmitPosition(ctx, delibID, fmt.Sprintf("seat%d", p.seat), content)
		if serr != nil {
			return "", fmt.Errorf("submit seat %d: %w", p.seat, serr)
		}
		posID[p.seat] = id
	}
	// Each author endorses positions of seats it trusts and opposes those it
	// suspects, giving gemot the controversy signal to find cruxes.
	for _, voter := range poss {
		sus := make(map[int]bool, len(voter.suspects))
		for _, s := range voter.suspects {
			sus[s] = true
		}
		for _, target := range poss {
			if voter.seat == target.seat {
				continue
			}
			id := posID[target.seat]
			if id == "" {
				continue
			}
			val := 1
			if sus[target.seat] {
				val = -1
			}
			_ = gm.g.Vote(ctx, delibID, fmt.Sprintf("seat%d", voter.seat), id, val)
		}
	}
	timeout := gm.AnalyzeTimeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	if err := gm.g.AnalyzeRun(ctx, delibID, timeout); err != nil {
		return "", fmt.Errorf("analyze: %w", err)
	}
	synth, err := gm.g.ProposeCompromise(ctx, delibID)
	if err != nil {
		return "", fmt.Errorf("compromise: %w", err)
	}
	if strings.TrimSpace(synth) == "" {
		return "", fmt.Errorf("compromise returned empty synthesis")
	}
	return strings.TrimSpace(synth), nil
}

func scoreLineFor(g *Game) string {
	return fmt.Sprintf("So far %d quests succeeded and %d failed.", g.Successes(), g.Failures())
}
