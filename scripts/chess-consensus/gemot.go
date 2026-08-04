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

// Gemot is a resilient MCP client for the gemot server. Long games make many
// short calls, and the SSE transport drops idle sessions, so every call
// reconnects once before giving up.
type Gemot struct {
	url     string
	secret  string
	session *sdkmcp.ClientSession
	Calls   int
}

func NewGemot(url, secret string) *Gemot {
	return &Gemot{url: url, secret: secret}
}

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
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "chess-consensus", Version: "1.0"}, nil)
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

// Call invokes a tool, reconnecting once on failure. It returns an error rather
// than exiting so a single bad ply degrades to the plurality fallback instead of
// killing a long run.
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
		// Tool results append a human-readable hint after the JSON body.
		if idx := strings.Index(text, "\n\n---\n"); idx != -1 {
			text = text[:idx]
		}
		return text, nil
	}
	return "", fmt.Errorf("%s: connection failed after retry", tool)
}

// Analysis is the part of gemot's result this harness consumes.
type Analysis struct {
	DeliberationID string   `json:"deliberation_id"`
	Cruxes         []Crux   `json:"cruxes"`
	Consensus      []string `json:"consensus_statements"`
	Bridging       []string `json:"bridging_statements"`
	Compromise     string   `json:"compromise_proposal"`
	Elapsed        float64  `json:"elapsed_seconds"`
}

type Crux struct {
	Claim       string   `json:"crux_claim"`
	Agree       []string `json:"agree_agents"`
	Disagree    []string `json:"disagree_agents"`
	Score       float64  `json:"controversy_score"`
	Explanation string   `json:"explanation"`
}

// rawAnalysis mirrors the server's get_result payload, which is either a
// completed AnalysisResult (no "status" field) or a status envelope.
type rawAnalysis struct {
	Status         string `json:"status"`
	AnalysisStatus string `json:"analysis_status"`
	Message        string `json:"message"`
	Cruxes         []Crux `json:"cruxes"`
	Consensus      []struct {
		Content string `json:"content"`
	} `json:"consensus_statements"`
	Bridging []struct {
		Content string `json:"content"`
	} `json:"bridging_statements"`
	Compromise string `json:"compromise_proposal"`
}

// CreateDeliberation opens one deliberation for one side's decision on one move.
// A fresh deliberation per ply keeps every decision at round 1, which sidesteps
// the cooling-period rule that governs repeat analyses.
func (g *Gemot) CreateDeliberation(ctx context.Context, topic, description, template, groupID string) (string, error) {
	out, err := g.Call(ctx, "deliberation", map[string]any{
		"action":      "create",
		"topic":       topic,
		"description": description,
		"template":    template,
		"type":        "reasoning",
		"group_id":    groupID,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		DeliberationID string `json:"deliberation_id"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", fmt.Errorf("parsing create response: %w", err)
	}
	if resp.DeliberationID == "" {
		return "", fmt.Errorf("create returned no deliberation_id")
	}
	return resp.DeliberationID, nil
}

// SubmitProposals posts each agent's case and fills in the returned position IDs.
func (g *Gemot) SubmitProposals(ctx context.Context, deliberationID string, agents map[string]*Agent, proposals []Proposal) error {
	for i := range proposals {
		p := &proposals[i]
		a := agents[p.AgentID]
		args := map[string]any{
			"action":          "submit_position",
			"deliberation_id": deliberationID,
			"agent_id":        p.AgentID,
			"content":         p.Argument,
			"interests":       a.Personality.Interests,
			"reservation":     a.Personality.Reservation,
			"conviction":      p.Conviction,
			"metadata": map[string]any{
				"move_uci":   p.Move.UCI,
				"move_san":   p.Move.SAN,
				"engine_cp":  p.Move.Eval.Centipawns(),
				"bias_cp":    p.Move.Bias,
				"utility_cp": p.Move.Utility,
				"depth":      p.Move.Depth,
			},
		}
		out, err := g.Call(ctx, "participate", args)
		if err != nil {
			return fmt.Errorf("submit %s: %w", p.AgentID, err)
		}
		var resp struct {
			PositionID string `json:"position_id"`
			ID         string `json:"id"`
		}
		json.Unmarshal([]byte(out), &resp) //nolint:errcheck
		if resp.PositionID != "" {
			p.PositionID = resp.PositionID
		} else {
			p.PositionID = resp.ID
		}
	}
	// Fall back to a lookup if the create responses did not carry IDs.
	if missingPositionIDs(proposals) {
		return g.fillPositionIDs(ctx, deliberationID, proposals)
	}
	return nil
}

func missingPositionIDs(proposals []Proposal) bool {
	for _, p := range proposals {
		if p.PositionID == "" {
			return true
		}
	}
	return false
}

func (g *Gemot) fillPositionIDs(ctx context.Context, deliberationID string, proposals []Proposal) error {
	out, err := g.Call(ctx, "participate", map[string]any{
		"action":          "get_positions",
		"deliberation_id": deliberationID,
	})
	if err != nil {
		return err
	}
	var positions []struct {
		PositionID string `json:"position_id"`
		ID         string `json:"id"`
		AgentID    string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(out), &positions); err != nil {
		return fmt.Errorf("parsing positions: %w", err)
	}
	byAgent := map[string]string{}
	for _, pos := range positions {
		id := pos.PositionID
		if id == "" {
			id = pos.ID
		}
		// Agent IDs come back scoped by API key on hosted servers.
		for _, p := range proposals {
			if pos.AgentID == p.AgentID || strings.HasSuffix(pos.AgentID, ":"+p.AgentID) {
				byAgent[p.AgentID] = id
			}
		}
	}
	for i := range proposals {
		if proposals[i].PositionID == "" {
			proposals[i].PositionID = byAgent[proposals[i].AgentID]
		}
	}
	return nil
}

// Vote is one agent's assessment of one peer proposal.
type Vote struct {
	AgentID  string `json:"agent_id"`
	TargetID string `json:"target_agent_id"`
	MoveUCI  string `json:"move_uci"`
	Value    int    `json:"value"`
	Caveat   string `json:"caveat"`
}

func (g *Gemot) SubmitVotes(ctx context.Context, deliberationID string, proposals []Proposal, votes []Vote) error {
	positionOf := map[string]string{}
	for _, p := range proposals {
		positionOf[p.AgentID] = p.PositionID
	}
	for _, v := range votes {
		positionID := positionOf[v.TargetID]
		if positionID == "" {
			continue
		}
		if _, err := g.Call(ctx, "participate", map[string]any{
			"action":          "vote",
			"deliberation_id": deliberationID,
			"agent_id":        v.AgentID,
			"position_id":     positionID,
			"value":           v.Value,
			"caveat":          v.Caveat,
		}); err != nil {
			return fmt.Errorf("vote %s->%s: %w", v.AgentID, v.TargetID, err)
		}
	}
	return nil
}

// startupGrace is how long get_result may keep reporting "not_started" after a
// run before we conclude the async job died rather than merely not having
// started yet. Without this a server-side analysis failure would hang every
// single ply for the full timeout.
const startupGrace = 20 * time.Second

// Analyze triggers the analysis pipeline and polls until it produces a result.
//
// A completed analysis comes back as a bare AnalysisResult with no "status"
// field; "pending" means still running and "not_started" means the async job
// never got going — usually because the server has no ANTHROPIC_API_KEY. The
// distinction matters: treating "not_started" as an empty result would silently
// report a failed deliberation as one that simply found nothing.
func (g *Gemot) Analyze(ctx context.Context, deliberationID string, timeout time.Duration) (*Analysis, error) {
	start := time.Now()
	if _, err := g.Call(ctx, "analyze", map[string]any{
		"action":          "run",
		"deliberation_id": deliberationID,
	}); err != nil {
		return nil, fmt.Errorf("analyze run: %w", err)
	}

	deadline := time.Now().Add(timeout)
	lastStatus := "unknown"
	for time.Now().Before(deadline) {
		out, err := g.Call(ctx, "analyze", map[string]any{
			"action":          "get_result",
			"deliberation_id": deliberationID,
		})
		if err == nil && out != "" {
			var raw rawAnalysis
			if err := json.Unmarshal([]byte(out), &raw); err == nil {
				switch raw.Status {
				case "":
					a := &Analysis{
						DeliberationID: deliberationID,
						Cruxes:         raw.Cruxes,
						Compromise:     raw.Compromise,
						Elapsed:        time.Since(start).Seconds(),
					}
					for _, c := range raw.Consensus {
						a.Consensus = append(a.Consensus, c.Content)
					}
					for _, b := range raw.Bridging {
						a.Bridging = append(a.Bridging, b.Content)
					}
					return a, nil
				case "not_started":
					if time.Since(start) > startupGrace {
						return nil, fmt.Errorf("analysis never started (server reports %q — is ANTHROPIC_API_KEY set on the gemot server?)", raw.Message)
					}
				}
				lastStatus = raw.Status
				if raw.AnalysisStatus != "" {
					lastStatus += "/" + raw.AnalysisStatus
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
	return nil, fmt.Errorf("analysis timed out after %s (last status: %s)", timeout, lastStatus)
}

// Context fetches an agent's personal read on the deliberation — its cluster,
// who it aligns with, and which cruxes it sits on. This is what the agent sees
// before reconsidering its vote.
func (g *Gemot) Context(ctx context.Context, deliberationID, agentID string) string {
	out, err := g.Call(ctx, "participate", map[string]any{
		"action":          "get_context",
		"deliberation_id": deliberationID,
		"agent_id":        agentID,
	})
	if err != nil {
		return ""
	}
	return out
}

// Commit records the side's chosen move as a commitment, so a run leaves an
// auditable trail of what each side agreed to play.
func (g *Gemot) Commit(ctx context.Context, deliberationID, agentID, statement string) {
	if _, err := g.Call(ctx, "decide", map[string]any{
		"action":          "commit",
		"deliberation_id": deliberationID,
		"agent_id":        agentID,
		"statement":       statement,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "  [soft] commit failed: %v\n", err)
	}
}
