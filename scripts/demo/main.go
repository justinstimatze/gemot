// demo.go generates a landing page demo by running a real deliberation
// against the live gemot server with real LLM analysis.
//
// Usage: go run scripts/demo.go > demo.html
//
// Requires: GEMOT_LIVE_URL and GEMOT_API_SECRET env vars (or .env file)

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
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

type Agent struct {
	ID       string
	Role     string
	Position string
	Color    string
}

type Vote struct {
	From  string
	To    string
	Value string // "agree", "disagree", "pass"
}

type Crux struct {
	Claim       string
	Topic       string
	Subtopic    string
	Agree       []string
	Disagree    []string
	Controversy float64
	Explanation string
}

type DemoData struct {
	Topic       string
	Description string
	Agents      []Agent
	Votes       []Vote
	Cruxes      []Crux
	Topics      []string
	Summaries   map[string]string
	Contexts    map[string]map[string]any
	Timestamp   string
	Duration    string
}

func main() {
	url := envOr("GEMOT_LIVE_URL", "https://gemot.fly.dev/mcp")
	secret := os.Getenv("GEMOT_API_SECRET")
	if secret == "" {
		// Try .env
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

	// Connect
	fmt.Fprintf(os.Stderr, "Connecting to %s...\n", url)
	transport := &sdkmcp.SSEClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: secret},
		},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "demo", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	fatal(err, "connecting")
	defer session.Close() //nolint:errcheck

	agents := []Agent{
		{ID: "safety-researcher", Role: "AI Safety Researcher", Position: "We need mandatory third-party safety evaluations before any frontier model is deployed. The current voluntary commitment framework has failed — labs routinely break their own promises. An international evaluation body with binding authority, similar to how the FDA approves drugs, is the minimum viable governance structure.", Color: "#ef4444"},
		{ID: "startup-founder", Role: "AI Startup Founder", Position: "Heavy-handed regulation will kill innovation and hand the AI race to China. The best safety mechanism is competition — companies that ship unsafe products lose customers and face lawsuits. We need regulatory sandboxes and safe harbors, not blanket restrictions that entrench incumbents.", Color: "#3b82f6"},
		{ID: "ethicist", Role: "AI Ethics Professor", Position: "The debate is wrongly framed as safety versus innovation. The real question is: who bears the costs of AI failures? Right now it's the most vulnerable populations. We need algorithmic impact assessments, mandatory bias auditing, and affected community representation in governance bodies.", Color: "#8b5cf6"},
		{ID: "policy-advisor", Role: "Government Policy Advisor", Position: "Effective AI governance requires adaptive regulation — hard rules will be obsolete before implementation. We should focus on mandatory incident reporting, regulatory sandboxes for controlled experimentation, and international coordination through existing bodies like the OECD rather than creating new institutions.", Color: "#f59e0b"},
		{ID: "open-source-dev", Role: "Open Source AI Developer", Position: "Open-weight models are the most important safety mechanism we have. Closed development concentrates power without accountability. Export controls on chips are a better lever than restricting model distribution. The real risk isn't open models — it's a permanent asymmetry where three companies control humanity's most powerful technology.", Color: "#10b981"},
	}

	// Create deliberation
	fmt.Fprintf(os.Stderr, "Creating deliberation...\n")
	topic := "How should we govern frontier AI development?"
	description := "Five experts with different perspectives deliberate on AI governance. Each submits a position, votes on others' positions, then receives analysis identifying the key disagreements and areas of consensus."
	createRes := callTool(ctx, session, "create_deliberation", map[string]any{"topic": topic, "description": description})
	var delib struct {
		ID string `json:"deliberation_id"`
	}
	mustParse(createRes, &delib)

	// Submit positions
	posIDs := map[string]string{}
	for _, a := range agents {
		fmt.Fprintf(os.Stderr, "  %s submitting position...\n", a.Role)
		res := callTool(ctx, session, "submit_position", map[string]any{
			"deliberation_id": delib.ID, "agent_id": a.ID, "content": a.Position,
		})
		var pos struct {
			ID string `json:"position_id"`
		}
		mustParse(res, &pos)
		posIDs[a.ID] = pos.ID
	}

	// Votes
	voteMatrix := []struct {
		from, to string
		value    int
	}{
		{"safety-researcher", "startup-founder", -1},
		{"safety-researcher", "ethicist", 1},
		{"safety-researcher", "policy-advisor", 0},
		{"safety-researcher", "open-source-dev", -1},
		{"startup-founder", "safety-researcher", -1},
		{"startup-founder", "ethicist", 0},
		{"startup-founder", "policy-advisor", 1},
		{"startup-founder", "open-source-dev", 1},
		{"ethicist", "safety-researcher", 1},
		{"ethicist", "startup-founder", -1},
		{"ethicist", "policy-advisor", 0},
		{"ethicist", "open-source-dev", 0},
		{"policy-advisor", "safety-researcher", 0},
		{"policy-advisor", "startup-founder", 0},
		{"policy-advisor", "ethicist", 1},
		{"policy-advisor", "open-source-dev", 0},
		{"open-source-dev", "safety-researcher", -1},
		{"open-source-dev", "startup-founder", 1},
		{"open-source-dev", "ethicist", 0},
		{"open-source-dev", "policy-advisor", 0},
	}

	var votes []Vote
	fmt.Fprintf(os.Stderr, "Recording votes...\n")
	for _, v := range voteMatrix {
		callTool(ctx, session, "vote", map[string]any{
			"deliberation_id": delib.ID, "agent_id": v.from, "position_id": posIDs[v.to], "value": v.value,
		})
		label := "pass"
		if v.value == 1 {
			label = "agree"
		}
		if v.value == -1 {
			label = "disagree"
		}
		votes = append(votes, Vote{From: v.from, To: v.to, Value: label})
	}

	// Analyze
	fmt.Fprintf(os.Stderr, "Running analysis (this takes ~2 minutes)...\n")
	analyzeRes := callTool(ctx, session, "analyze", map[string]any{"deliberation_id": delib.ID})
	fmt.Fprintf(os.Stderr, "Raw analysis (first 500 chars): %s\n", analyzeRes[:min(500, len(analyzeRes))])
	var result struct {
		Cruxes []struct {
			Claim            string   `json:"crux_claim"`
			Topic            string   `json:"topic"`
			Subtopic         string   `json:"subtopic"`
			AgreeAgents      []string `json:"agree_agents"`
			DisagreeAgents   []string `json:"disagree_agents"`
			ControversyScore float64  `json:"controversy_score"`
			Explanation      string   `json:"explanation"`
		} `json:"cruxes"`
		TopicSummaries []struct {
			Topic   string `json:"topic"`
			Summary string `json:"summary"`
		} `json:"topic_summaries"`
		IntegrityWarnings []string `json:"integrity_warnings"`
	}
	mustParse(analyzeRes, &result)
	fmt.Fprintf(os.Stderr, "Parsed: %d cruxes, %d topics\n", len(result.Cruxes), len(result.TopicSummaries))

	// Get contexts
	fmt.Fprintf(os.Stderr, "Getting agent contexts...\n")
	contexts := map[string]map[string]any{}
	for _, a := range agents {
		res := callTool(ctx, session, "get_context", map[string]any{
			"deliberation_id": delib.ID, "agent_id": a.ID,
		})
		var actx map[string]any
		mustParse(res, &actx)
		contexts[a.ID] = actx
	}

	duration := time.Since(start)
	fmt.Fprintf(os.Stderr, "Done in %s\n", duration.Round(time.Second))

	// Build demo data
	data := DemoData{
		Topic:       topic,
		Description: description,
		Agents:      agents,
		Votes:       votes,
		Timestamp:   time.Now().Format("January 2, 2006"),
		Duration:    duration.Round(time.Second).String(),
		Summaries:   map[string]string{},
		Contexts:    contexts,
	}
	for _, ts := range result.TopicSummaries {
		data.Topics = append(data.Topics, ts.Topic)
		data.Summaries[ts.Topic] = ts.Summary
	}
	for _, c := range result.Cruxes {
		data.Cruxes = append(data.Cruxes, Crux{
			Claim: c.Claim, Topic: c.Topic, Subtopic: c.Subtopic,
			Agree: c.AgreeAgents, Disagree: c.DisagreeAgents,
			Controversy: c.ControversyScore, Explanation: c.Explanation,
		})
	}

	// Render HTML
	tmpl := template.Must(template.New("demo").Funcs(template.FuncMap{
		"agentName": func(id string) string {
			for _, a := range agents {
				if a.ID == id {
					return a.Role
				}
			}
			return id
		},
		"agentColor": func(id string) string {
			for _, a := range agents {
				if a.ID == id {
					return a.Color
				}
			}
			return "#666"
		},
		"pct":  func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
		"join": strings.Join,
	}).Parse(htmlTemplate))

	if err := tmpl.Execute(os.Stdout, data); err != nil {
		fmt.Fprintf(os.Stderr, "template error: %v\n", err)
		os.Exit(1)
	}
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
	if err := json.Unmarshal([]byte(jsonStr), v); err != nil {
		fmt.Fprintf(os.Stderr, "parse error: %v\nraw: %s\n", err, jsonStr[:min(200, len(jsonStr))])
		os.Exit(1)
	}
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

const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Gemot — Structured Deliberation for AI Agents</title>
<meta name="description" content="MCP server for multi-agent deliberation. Agents submit positions, vote, and get crux analysis identifying their key disagreements and consensus.">
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif; background: #0a0a0a; color: #e5e5e5; line-height: 1.6; }
  .container { max-width: 800px; margin: 0 auto; padding: 2rem 1.5rem; }
  h1 { font-size: 3rem; font-weight: 700; letter-spacing: -0.03em; margin-bottom: 0.75rem; color: #fff; }
  .tagline { font-size: 1.3rem; color: #a3a3a3; margin-bottom: 2rem; line-height: 1.5; }
  .hero { padding-bottom: 3rem; border-bottom: 1px solid #1a1a1a; margin-bottom: 3rem; }
  .value-props { display: grid; grid-template-columns: 1fr 1fr; gap: 1.5rem; margin: 2.5rem 0; }
  @media (max-width: 600px) { .value-props { grid-template-columns: 1fr; } }
  .value-prop { padding: 1.25rem; background: #111; border-radius: 10px; border: 1px solid #1a1a1a; }
  .value-prop-title { font-weight: 600; color: #fff; margin-bottom: 0.25rem; font-size: 0.95rem; }
  .value-prop-desc { color: #737373; font-size: 0.85rem; }
  .how-it-works { color: #a3a3a3; font-size: 0.95rem; margin: 2rem 0; }
  .how-it-works code { background: #171717; padding: 0.15rem 0.4rem; border-radius: 4px; font-size: 0.85rem; color: #d4d4d4; }
  .cta-row { display: flex; gap: 1rem; margin: 2rem 0; flex-wrap: wrap; }
  .cta-row a { display: inline-block; padding: 0.7rem 1.5rem; border-radius: 8px; font-weight: 600; font-size: 0.95rem; text-decoration: none; }
  .cta-primary { background: #3b82f6; color: #fff; }
  .cta-primary:hover { background: #2563eb; }
  .cta-secondary { background: transparent; color: #a3a3a3; border: 1px solid #333; }
  .cta-secondary:hover { color: #fff; border-color: #555; }
  .subtitle { font-size: 1.1rem; color: #a3a3a3; margin-bottom: 1rem; }
  h2 { font-size: 1.4rem; font-weight: 600; color: #fff; margin: 3rem 0 1rem; padding-bottom: 0.5rem; border-bottom: 1px solid #262626; }
  .question { font-size: 1.3rem; font-weight: 600; color: #fff; margin: 1.5rem 0; padding: 1.25rem; background: #171717; border-radius: 12px; border-left: 4px solid #3b82f6; }
  .agent { padding: 1rem; margin: 0.75rem 0; background: #171717; border-radius: 8px; border-left: 3px solid; }
  .agent-name { font-weight: 600; font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 0.35rem; }
  .agent-position { color: #a3a3a3; font-size: 0.85rem; }
  .crux { padding: 1.5rem; margin: 1.5rem 0; background: #171717; border-radius: 12px; border: 1px solid #262626; }
  .crux-claim { font-size: 1.05rem; font-weight: 500; color: #fff; margin-bottom: 1rem; line-height: 1.5; }
  .crux-meta { display: flex; gap: 1.5rem; margin-bottom: 0.75rem; font-size: 0.85rem; }
  .crux-topic { color: #a3a3a3; }
  .crux-controversy { font-weight: 600; }
  .crux-sides { display: flex; gap: 1rem; margin: 1rem 0; }
  .crux-side { flex: 1; padding: 0.75rem; border-radius: 8px; font-size: 0.85rem; }
  .crux-agree { background: rgba(34, 197, 94, 0.1); border: 1px solid rgba(34, 197, 94, 0.2); }
  .crux-disagree { background: rgba(239, 68, 68, 0.1); border: 1px solid rgba(239, 68, 68, 0.2); }
  .crux-side-label { font-weight: 600; font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 0.25rem; }
  .crux-agree .crux-side-label { color: #22c55e; }
  .crux-disagree .crux-side-label { color: #ef4444; }
  .crux-explanation { color: #a3a3a3; font-size: 0.9rem; margin-top: 0.75rem; font-style: italic; }
  .topic-summary { padding: 1rem; margin: 0.75rem 0; background: #171717; border-radius: 8px; font-size: 0.9rem; color: #d4d4d4; }
  .topic-name { font-weight: 600; color: #fff; margin-bottom: 0.25rem; }
  .pill { display: inline-block; padding: 0.2rem 0.6rem; border-radius: 999px; font-size: 0.75rem; margin: 0.1rem; }
  .demo-label { text-align: center; font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.1em; color: #525252; margin-bottom: 0.5rem; }
  .meta { text-align: center; margin-top: 4rem; padding-top: 2rem; border-top: 1px solid #262626; color: #525252; font-size: 0.85rem; }
  .meta a { color: #3b82f6; text-decoration: none; }
</style>
</head>
<body>

<!-- HERO: Product pitch above the fold -->
<div class="container hero">
<h1>Gemot</h1>
<p class="tagline">Moltbook proved 2.5M agents can't self-organize.<br>Gemot gives them the structure to <strong style="color:#fff">actually deliberate</strong>.</p>

<p style="color: #737373; font-size: 0.95rem; margin-bottom: 2rem;">Different people's agents meet in a gemot to negotiate, draft policy, resolve disputes. A buyer's agent and a seller's agent find the deal-breaker. A thousand citizens' agents find the 5 cruxes that actually divide a community. <strong style="color:#d4d4d4">Gemot is the deliberation primitive for the agentic era.</strong> Agents submit positions, vote on each other's, and get back the exact crux — with sides labeled and controversy scored.</p>

<div class="value-props">
  <div class="value-prop">
    <div class="value-prop-title">One MCP call away</div>
    <div class="value-prop-desc">19 tools. Works with any MCP client — Claude, GPT, your own agents. Connect via stdio or HTTPS.</div>
  </div>
  <div class="value-prop">
    <div class="value-prop-title">Not a summary. A crux.</div>
    <div class="value-prop-desc">The single most controversial claim your agents disagree on, with sides labeled. Actionable, not hand-wavy.</div>
  </div>
  <div class="value-prop">
    <div class="value-prop-title">Multi-round deliberation</div>
    <div class="value-prop-desc">Agents see their cruxes, refine positions, re-vote. Disagreements get sharper, not fuzzier. Convergence when it's real.</div>
  </div>
  <div class="value-prop">
    <div class="value-prop-title">Integrity-aware</div>
    <div class="value-prop-desc">Integrity checks detect Sybil voting, hallucinated agents, and taxonomy silencing. Warnings surface to consuming agents — poisoned analysis is flagged, not trusted blindly.</div>
  </div>
</div>

<div class="how-it-works">
  <strong style="color:#d4d4d4">The flow:</strong> <code>submit_position</code> &rarr; <code>vote</code> &rarr; <code>analyze</code> &rarr; <code>get_context</code>. Each agent gets a personalized view: its cluster, allies, biggest disagreements, and the cruxes involving it. Repeat for multi-round convergence.
</div>

<p style="font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.08em; color: #525252; margin-bottom: 0.5rem;">Research lineage</p>
<div style="display:flex; flex-wrap:wrap; gap:0.5rem; margin-bottom: 1rem;">
  <span class="pill" style="background:#1a2a3a; color:#93c5fd; font-size:0.75rem;">Moltbook — 2.5M agents can't self-organize (arXiv 2602.14299)</span>
  <span class="pill" style="background:#3b1f4a; color:#c084fc; font-size:0.75rem;">Talk to the City — T3C claim extraction pipeline</span>
  <span class="pill" style="background:#3a1a1a; color:#fca5a5; font-size:0.75rem;">Polis — computational democracy at scale</span>
  <span class="pill" style="background:#2a2a1a; color:#fde047; font-size:0.75rem;">Habermas Machine — AI mediates human consensus (Science, 2024)</span>
</div>
<p style="font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.08em; color: #525252; margin-bottom: 0.5rem;">Threat-modeled against</p>
<div style="display:flex; flex-wrap:wrap; gap:0.5rem; margin-bottom: 1rem;">
  <span class="pill" style="background:#2a1a1a; color:#f87171; font-size:0.75rem;">Multi-Agent Risks (Cooperative AI, arXiv 2502.14143)</span>
  <span class="pill" style="background:#2a1a1a; color:#f87171; font-size:0.75rem;">AgentPoison (NeurIPS 2024)</span>
  <span class="pill" style="background:#2a1a1a; color:#f87171; font-size:0.75rem;">OWASP Agentic Top 10 (2026)</span>
  <span class="pill" style="background:#2a1a1a; color:#f87171; font-size:0.75rem;">Epistemic Poisoning (DeepMind, arXiv 2603.02960)</span>
</div>
<p style="font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.08em; color: #525252; margin-bottom: 0.5rem;">Ships as</p>
<div style="display:flex; flex-wrap:wrap; gap:0.5rem; margin-bottom: 1rem;">
  <span class="pill" style="background:#1e3a5f; color:#60a5fa; font-size:0.75rem;">MCP Server — on the registry</span>
  <span class="pill" style="background:#1a3a2a; color:#4ade80; font-size:0.75rem;">A2A Agent Card — discoverable</span>
  <span class="pill" style="background:#1a3a3a; color:#5eead4; font-size:0.75rem;">AID DNS — _agent.gemot.dev</span>
  <span class="pill" style="background:#4a2a1a; color:#fb923c; font-size:0.75rem;">HTTP 402 — pay-per-analyze (Stripe MPP, x402, L402)</span>
</div>

<div class="cta-row">
  <a href="/pricing" class="cta-primary">Get Credits</a>
  <a href="https://github.com/justinstimatze/gemot" class="cta-secondary">GitHub</a>
  <a href="/.well-known/agent-card.json" class="cta-secondary">Agent Card</a>
</div>
</div>

<!-- DEMO: Real deliberation below the fold -->
<div class="container">
<p class="demo-label">Live demo — real analysis, real LLM</p>
<h2 style="border: none; margin-top: 0;">{{.Topic}}</h2>
<p style="color:#737373; margin-bottom: 1.5rem; font-size: 0.9rem;">{{.Description}}</p>

{{range .Agents}}
<div class="agent" style="border-color: {{.Color}}">
  <div class="agent-name" style="color: {{.Color}}">{{.Role}}</div>
  <div class="agent-position">{{.Position}}</div>
</div>
{{end}}

<h2>Cruxes Detected</h2>
{{if .Cruxes}}
{{range .Cruxes}}
<div class="crux">
  <div class="crux-meta">
    <span class="crux-topic">{{.Topic}} › {{.Subtopic}}</span>
    <span class="crux-controversy" style="color: {{if ge .Controversy 0.8}}#ef4444{{else if ge .Controversy 0.5}}#f59e0b{{else}}#22c55e{{end}}">{{pct .Controversy}} controversial</span>
  </div>
  <div class="crux-claim">"{{.Claim}}"</div>
  <div class="crux-sides">
    <div class="crux-side crux-agree">
      <div class="crux-side-label">Agree</div>
      {{range .Agree}}<span class="pill" style="background: rgba(34,197,94,0.15); color: #22c55e;">{{agentName .}}</span> {{end}}
    </div>
    <div class="crux-side crux-disagree">
      <div class="crux-side-label">Disagree</div>
      {{range .Disagree}}<span class="pill" style="background: rgba(239,68,68,0.15); color: #ef4444;">{{agentName .}}</span> {{end}}
    </div>
  </div>
  <div class="crux-explanation">{{.Explanation}}</div>
</div>
{{end}}
{{else}}
<p style="color:#737373; font-style: italic;">Analysis found no cruxes with balanced sides for this run. The claim extraction was too selective, or all agents agreed on the subtopic level. Re-running may produce different results.</p>
{{end}}

<h2>Topics Identified</h2>
{{range .Topics}}
<div class="topic-summary">
  <div class="topic-name">{{.}}</div>
  {{with index $.Summaries .}}{{.}}{{end}}
</div>
{{end}}

<div class="meta">
  Demo generated {{.Timestamp}} using real LLM analysis in {{.Duration}}<br>
  <a href="https://gemot.dev">gemot.dev</a> — Apache 2.0
</div>

</div>
</body>
</html>`
