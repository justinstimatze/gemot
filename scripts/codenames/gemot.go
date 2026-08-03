package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP client for the gemot server — same shape as scripts/compromise-eval.

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
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "codenames", Version: "1.0"}, nil)
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

// parseGuesses extracts board words named in the compromise text, ordered by
// first appearance — the team's ranked guess sequence.
func parseGuesses(b Board, text string) []string {
	lower := strings.ToLower(text)
	type hit struct {
		word string
		idx  int
	}
	var hits []hit
	seen := map[string]bool{}
	for _, w := range b.Words {
		if seen[w] {
			continue
		}
		if i := strings.Index(lower, strings.ToLower(w)); i != -1 {
			hits = append(hits, hit{w, i})
			seen[w] = true
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].idx < hits[j].idx })
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.word
	}
	return out
}

// RunGemotGuess submits the guessers' positions to a live gemot deliberation and
// returns the synthesized team guess (ordered board words). ok=false on failure.
func RunGemotGuess(ctx context.Context, g *Gemot, b Board, positions []GuesserPosition, template, groupID string) ([]string, bool) {
	topic := fmt.Sprintf("Codenames: clue %q %d — which board words does it point to?", b.Clue, b.ClueN)
	desc := fmt.Sprintf("%s\n\nBoard words: %s\n\nThe spymaster's clue is %q for %d words. Decide the ordered list of board words the team should guess (most to least confident), including only words the team believes are theirs. Name the exact board words.",
		gameRules, strings.Join(b.Words, ", "), b.Clue, b.ClueN)

	delibID, err := g.CreateDeliberation(ctx, topic, desc, template, groupID)
	if err != nil {
		fmt.Printf("    [gemot] create failed: %v\n", err)
		return nil, false
	}
	posOf := make([]string, len(positions))
	for i, p := range positions {
		content := fmt.Sprintf("As the %s guesser: %s My ranked guesses: %s", p.Style, p.Reasoning, strings.Join(p.Guesses, ", "))
		pid, err := g.SubmitPosition(ctx, delibID, p.Style, content)
		if err != nil {
			fmt.Printf("    [gemot] submit %s failed: %v\n", p.Style, err)
			return nil, false
		}
		posOf[i] = pid
	}
	// each guesser up-votes positions whose top guess it shares, else neutral/down
	for vi, voter := range positions {
		for pi, prop := range positions {
			if vi == pi || posOf[pi] == "" {
				continue
			}
			val := -1
			if len(prop.Guesses) > 0 && len(voter.Guesses) > 0 && sharesTop(voter.Guesses, prop.Guesses) {
				val = 2
			}
			_ = g.Vote(ctx, delibID, voter.Style, posOf[pi], val)
		}
	}
	if err := g.AnalyzeRun(ctx, delibID, 5*time.Minute); err != nil {
		fmt.Printf("    [gemot] analyze failed: %v\n", err)
		return nil, false
	}
	compromise, err := g.ProposeCompromise(ctx, delibID)
	if err != nil {
		fmt.Printf("    [gemot] propose_compromise failed: %v\n", err)
		return nil, false
	}
	guesses := parseGuesses(b, compromise)
	if len(guesses) == 0 {
		fmt.Printf("    [gemot] compromise named no board word: %q\n", truncate(compromise, 140))
		return nil, false
	}
	return guesses, true
}

func sharesTop(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	return strings.EqualFold(a[0], b[0])
}
