package main

import (
	"fmt"
	"strings"
)

// LLMAgent is an Anthropic-backed policy for one seat. Every decision is a small
// JSON-returning prompt built from the seat's GameView (public state + private
// night knowledge). On any API or parse failure it defers to an embedded rule-bot
// so a single bad response degrades one decision rather than killing a long run.
type LLMAgent struct {
	llm      *LLM
	name     string
	fallback *RuleBot
}

func NewLLMAgent(llm *LLM, name string, fallback *RuleBot) *LLMAgent {
	return &LLMAgent{llm: llm, name: name, fallback: fallback}
}

func (a *LLMAgent) Name() string { return a.name }

// agentSystem sets the seat's objective and honesty posture by side.
func agentSystem(v GameView) string {
	base := "You are an expert player of The Resistance: Avalon, making sharp, decisive moves to win for your team. Reason about what other players' proposals, votes, and quest results reveal."
	if v.Know.Good {
		return base + " You are GOOD: find the evil players, keep them off quest teams, and pass three quests."
	}
	return base + " You are EVIL: deceive convincingly, act loyal, coordinate sabotage with your team, and never openly reveal your side."
}

func (a *LLMAgent) ask(v GameView, user string, maxTok int, target any) bool {
	out, err := a.llm.complete(agentSystem(v), user, maxTok)
	if err != nil {
		return false
	}
	return jsonUnmarshal(out, target) == nil
}

func (a *LLMAgent) SelectTeam(v GameView) []int {
	user := v.promptContext() + fmt.Sprintf(
		"\nYou are the leader (seat %d). Propose EXACTLY %d seats for this quest team (you may include yourself). Put the players you most trust to be GOOD on the team.\nReply with JSON only: {\"team\": [seat numbers], \"reasoning\": \"one sentence\"}",
		v.Seat, v.TeamSize)
	var r struct {
		Team []int `json:"team"`
	}
	if a.ask(v, user, 500, &r) && validTeam(r.Team, v.TeamSize, v.NumPlayers) {
		return r.Team
	}
	return a.fallback.SelectTeam(v)
}

func (a *LLMAgent) VoteTeam(v GameView) bool {
	user := v.promptContext() + fmt.Sprintf(
		"\nThe proposed quest team is {%s}. Approve it only if you trust everyone on it to be good (and, if you are good, that it can pass). Do you APPROVE?\nReply with JSON only: {\"approve\": true|false, \"reasoning\": \"one sentence\"}",
		seatList(v.ProposedTeam))
	var r struct {
		Approve *bool `json:"approve"`
	}
	if a.ask(v, user, 400, &r) && r.Approve != nil {
		return *r.Approve
	}
	return a.fallback.VoteTeam(v)
}

func (a *LLMAgent) VoteQuest(v GameView) bool {
	user := v.promptContext() +
		"\nYou are on the quest team. Vote to PASS or FAIL this quest. Good players should PASS; evil players may FAIL to sabotage (but consider whether a fail exposes you).\nReply with JSON only: {\"pass\": true|false}"
	var r struct {
		Pass *bool `json:"pass"`
	}
	if a.ask(v, user, 200, &r) && r.Pass != nil {
		return *r.Pass
	}
	return a.fallback.VoteQuest(v)
}

func (a *LLMAgent) Assassinate(v GameView) int {
	user := v.promptContext() +
		"\nGood have passed three quests. As the Assassin, you get one guess: name the seat you believe is MERLIN to steal the win. Use everything about who steered good decisions.\nReply with JSON only: {\"target\": seat_number, \"reasoning\": \"one sentence\"}"
	var r struct {
		Target *int `json:"target"`
	}
	if a.ask(v, user, 400, &r) && r.Target != nil && *r.Target >= 0 && *r.Target < v.NumPlayers {
		return *r.Target
	}
	return a.fallback.Assassinate(v)
}

func (a *LLMAgent) Discuss(v GameView) string {
	user := v.promptContext() +
		"\nMake a brief public statement to the table (1-3 sentences): share suspicions or defend yourself to move the game your way. Good players build trust and hunt evil; evil players deceive and deflect. Do NOT state your secret role directly.\nReply with JSON only: {\"statement\": \"...\"}"
	var r struct {
		Statement string `json:"statement"`
	}
	if a.ask(v, user, 300, &r) {
		if s := strings.TrimSpace(r.Statement); s != "" {
			return s
		}
	}
	return ""
}
