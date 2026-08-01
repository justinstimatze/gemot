package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// LLM writes the agents' arguments and casts their votes when --llm=on.
// With --llm=off the same decisions are made by the deterministic heuristics in
// agent.go, which keeps a full run free and reproducible; the LLM path trades
// that for arguments that can actually persuade.
type LLM struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
	Calls   int
}

func NewLLM(model string) (*LLM, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is required for --llm=on")
	}
	base := os.Getenv("ANTHROPIC_BASE_URL")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return &LLM{
		apiKey:  key,
		baseURL: strings.TrimSuffix(base, "/"),
		model:   model,
		client:  &http.Client{Timeout: 2 * time.Minute},
	}, nil
}

func (l *LLM) complete(system, user string, maxTokens int) (string, error) {
	payload := map[string]any{
		"model":      l.model,
		"max_tokens": maxTokens,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": user}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", l.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", l.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := l.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	l.Calls++
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic API %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	var parsed struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Content) == 0 {
		return "", fmt.Errorf("empty response")
	}
	return parsed.Content[0].Text, nil
}

func (l *LLM) systemFor(p Personality) string {
	return fmt.Sprintf(`You are %s, one of three chess agents who must agree on a single move for your side.

Your temperament: %s

Your stated interests: %s
Your hard constraint: %s

You have your own engine analysis, shown as a table of candidate moves. Each row gives the engine's evaluation and the centipawn adjustment your temperament applies on top of it. You may argue against the engine when your temperament justifies it, but you must be honest that you are doing so.

Write like a strong player explaining a decision to two peers who will push back. Be concrete: cite variations, not adjectives. Never invent evaluations or moves that are not in your table.`,
		p.Name, p.Style, p.Interests, p.Reservation)
}

func candidateTable(shortlist []Candidate) string {
	var b strings.Builder
	b.WriteString("| move | engine eval | my adjustment | my total | principal variation |\n")
	b.WriteString("|------|-------------|---------------|----------|---------------------|\n")
	for _, c := range shortlist {
		why := "—"
		if len(c.Why) > 0 {
			why = fmt.Sprintf("%+dcp (%s)", c.Bias, strings.Join(c.Why, ", "))
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %+dcp | %s |\n",
			c.SAN, c.Eval, why, c.Utility, strings.Join(c.PV, " "))
	}
	return b.String()
}

// Argue writes the case for the agent's preferred move.
func (l *LLM) Argue(p Personality, moveLabel, fen string, shortlist []Candidate) (string, error) {
	if len(shortlist) == 0 {
		return "", fmt.Errorf("no candidates")
	}
	user := fmt.Sprintf(`Position (FEN): %s
It is %s.

Your engine analysis, already ranked by your own preferences:

%s

Argue for %s — the move at the top of your table. In under 200 words:
1. State the move.
2. Give the concrete line you are relying on.
3. Say plainly where your temperament is overriding the engine, if it is, and why that trade is worth it.
4. Name the strongest objection you expect from the other two agents.

Do not hedge. You are trying to convince them.`, fen, moveLabel, candidateTable(shortlist), shortlist[0].SAN)

	return l.complete(l.systemFor(p), user, 1024)
}

// Vote returns a gemot vote value (-2..2) and a caveat for a peer's proposal.
func (l *LLM) Vote(p Personality, moveLabel string, own Candidate, peer Proposal, peerAsSeenByMe Candidate) (int, string, error) {
	user := fmt.Sprintf(`It is %s. You proposed %s (engine %s, your total %+dcp).

%s proposed %s and argued:

---
%s
---

Your own engine's verdict on %s: %s, which after your adjustment comes to %+dcp — %dcp against your own choice.

Reply with JSON only, no prose around it:
{"value": <-2|-1|0|1|2>, "caveat": "<one sentence, max 30 words>"}

The scale is: 2 strongly agree, 1 agree with caveats, 0 mixed, -1 disagree with caveats, -2 strongly disagree.
Vote on the merits of the move and the argument, not on whose idea it was. If the argument genuinely addresses your concern, let it move you.`,
		moveLabel, own.SAN, own.Eval, own.Utility,
		peer.AgentID, peer.Move.SAN, peer.Argument,
		peer.Move.SAN, peerAsSeenByMe.Eval, peerAsSeenByMe.Utility, peerAsSeenByMe.Utility-own.Utility)

	out, err := l.complete(l.systemFor(p), user, 400)
	if err != nil {
		return 0, "", err
	}
	var parsed struct {
		Value  int    `json:"value"`
		Caveat string `json:"caveat"`
	}
	if err := json.Unmarshal([]byte(extractJSON(out)), &parsed); err != nil {
		return 0, "", fmt.Errorf("parsing vote: %w (raw: %s)", err, truncate(out, 120))
	}
	if parsed.Value < -2 {
		parsed.Value = -2
	}
	if parsed.Value > 2 {
		parsed.Value = 2
	}
	return parsed.Value, parsed.Caveat, nil
}

// Reconsider asks the agent, having seen gemot's analysis, which move it now
// backs. Returning a move other than its own is the persuasion event this whole
// experiment is built to measure.
func (l *LLM) Reconsider(p Personality, moveLabel string, own Candidate, options map[string]Candidate, analysis *Analysis, agentContext string) (string, string, error) {
	var opts strings.Builder
	for uciMove, c := range options {
		fmt.Fprintf(&opts, "- %s (uci %s): engine %s, my adjustment %+dcp, my total %+dcp\n",
			c.SAN, uciMove, c.Eval, c.Bias, c.Utility)
	}

	var analysisText strings.Builder
	for _, c := range analysis.Cruxes {
		fmt.Fprintf(&analysisText, "CRUX (%.0f%% controversy): %s\n  agree: %s | disagree: %s\n  %s\n",
			c.Score*100, c.Claim, strings.Join(c.Agree, ", "), strings.Join(c.Disagree, ", "), c.Explanation)
	}
	for _, s := range analysis.Consensus {
		fmt.Fprintf(&analysisText, "CONSENSUS: %s\n", s)
	}
	if analysis.Compromise != "" {
		fmt.Fprintf(&analysisText, "PROPOSED COMPROMISE: %s\n", analysis.Compromise)
	}
	if analysisText.Len() == 0 {
		analysisText.WriteString("(the analysis produced no cruxes or consensus statements)\n")
	}

	user := fmt.Sprintf(`It is %s. You originally proposed %s.

The deliberation analysis:

%s
%s
Every move on the table:

%s
Pick the one move your side should play. You may keep your own or switch. Switching because the other agents made a better case is the correct outcome, not a loss.

Reply with JSON only:
{"uci": "<uci move from the list>", "reason": "<one sentence, max 25 words>"}`,
		moveLabel, own.SAN, analysisText.String(), agentContext, opts.String())

	out, err := l.complete(l.systemFor(p), user, 400)
	if err != nil {
		return "", "", err
	}
	var parsed struct {
		UCI    string `json:"uci"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(extractJSON(out)), &parsed); err != nil {
		return "", "", fmt.Errorf("parsing reconsider: %w (raw: %s)", err, truncate(out, 120))
	}
	if _, ok := options[parsed.UCI]; !ok {
		return "", "", fmt.Errorf("agent picked %q which is not on the table", parsed.UCI)
	}
	return parsed.UCI, parsed.Reason, nil
}

// extractJSON pulls the first JSON object out of a response that may be wrapped
// in markdown fences or prose.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	}
	start := strings.IndexAny(s, "{[")
	if start == -1 {
		return s
	}
	end := strings.LastIndexAny(s, "}]")
	if end < start {
		return s[start:]
	}
	return s[start : end+1]
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
