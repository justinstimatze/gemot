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
	var transcript []Statement
	for seat := 0; seat < g.NumPlayers; seat++ {
		v := viewFor(g, seat, knows, log, nil, transcript)
		if st, _ := positionOf(players[seat], v); st != "" {
			transcript = append(transcript, Statement{Seat: seat, Text: st})
		}
	}
	return transcript
}

// GemotArm runs the structured discussion: the same per-seat positions as the
// chat arm, but submitted to a gemot deliberation that votes, analyses, and
// synthesises a trust recommendation appended to the transcript. The delta vs
// chatDiscuss is exactly the gemot aggregation layer.
type GemotArm struct {
	g         *Gemot
	template  string
	groupBase string
	counter   int
}

func NewGemotArm(g *Gemot, template, groupBase string) *GemotArm {
	return &GemotArm{g: g, template: template, groupBase: groupBase}
}

func (gm *GemotArm) discuss(g *Game, players []Player, knows []PlayerKnowledge, log []string) []Statement {
	ctx := context.Background()

	type pos struct {
		seat     int
		text     string
		suspects []int
	}
	var poss []pos
	for seat := 0; seat < g.NumPlayers; seat++ {
		v := viewFor(g, seat, knows, log, nil, nil)
		if st, sus := positionOf(players[seat], v); st != "" {
			poss = append(poss, pos{seat, st, sus})
		}
	}
	transcript := make([]Statement, 0, len(poss)+1)
	for _, p := range poss {
		transcript = append(transcript, Statement{Seat: p.seat, Text: p.text})
	}
	if len(poss) < 2 {
		return transcript // nothing to aggregate; degrade to plain talk
	}

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
		return transcript
	}
	posID := make(map[int]string, len(poss))
	for _, p := range poss {
		content := fmt.Sprintf("Seat %d's read: %s", p.seat, p.text)
		if len(p.suspects) > 0 {
			content += fmt.Sprintf(" (most suspects seats: %s)", seatList(p.suspects))
		}
		if id, err := gm.g.SubmitPosition(ctx, delibID, fmt.Sprintf("seat%d", p.seat), content); err == nil {
			posID[p.seat] = id
		}
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
	if err := gm.g.AnalyzeRun(ctx, delibID, 3*time.Minute); err != nil {
		return transcript
	}
	synth, err := gm.g.ProposeCompromise(ctx, delibID)
	if err != nil || strings.TrimSpace(synth) == "" {
		return transcript
	}
	transcript = append(transcript, Statement{Seat: -1, Text: strings.TrimSpace(synth)})
	return transcript
}

func scoreLineFor(g *Game) string {
	return fmt.Sprintf("So far %d quests succeeded and %d failed.", g.Successes(), g.Failures())
}
