package main

import (
	"math/rand"
	"testing"
)

// TestRuleBotSelfPlay drives full games with the heuristic baseline on both
// sides: every game must terminate, produce a legal outcome, and both sides must
// win some games. No LLM calls, so this validates the whole runner for free.
func TestRuleBotSelfPlay(t *testing.T) {
	rng := rand.New(rand.NewSource(2026))
	goodWins, evilWins, merlinKills := 0, 0, 0
	const games = 800
	for i := 0; i < games; i++ {
		n := 5 + rng.Intn(6)
		g, err := NewGame(n, Options{Percival: true, Morgana: true}, rng)
		if err != nil {
			t.Fatalf("NewGame: %v", err)
		}
		players := make([]Player, n)
		for s := 0; s < n; s++ {
			players[s] = NewRuleBot("bot", rng)
		}
		out := RunGame(g, players, RunConfig{Arm: "bot"})
		if !g.Done {
			t.Fatalf("game %d did not finish", i)
		}
		if len(out.Results) < 3 {
			t.Fatalf("game %d ended with only %d quests", i, len(out.Results))
		}
		if out.GoodWin {
			goodWins++
		} else {
			evilWins++
		}
		if out.MerlinKilled {
			merlinKills++
		}
	}
	if goodWins == 0 || evilWins == 0 {
		t.Errorf("degenerate: good=%d evil=%d", goodWins, evilWins)
	}
	t.Logf("rule-bot self-play over %d games: good %d (%.1f%%), evil %d, merlin-kills %d",
		games, goodWins, 100*float64(goodWins)/float64(games), evilWins, merlinKills)
}
