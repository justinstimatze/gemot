package main

import "math/rand"

// RuleBot is a deterministic heuristic policy — the no-LLM baseline that anchors
// the win-rate metric (in the spirit of AvalonBench's rule-based bots). Good bots
// avoid players they know to be evil and always pass quests; evil bots pack
// enough saboteurs onto teams and fail quests they join.
type RuleBot struct {
	name string
	rng  *rand.Rand
}

func NewRuleBot(name string, rng *rand.Rand) *RuleBot { return &RuleBot{name: name, rng: rng} }

func (b *RuleBot) Name() string { return b.name }

func knownEvilSet(v GameView) map[int]bool {
	m := make(map[int]bool, len(v.Know.SeenEvil))
	for _, e := range v.Know.SeenEvil {
		m[e] = true
	}
	return m
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (b *RuleBot) SelectTeam(v GameView) []int {
	size := v.TeamSize
	known := knownEvilSet(v)
	team := []int{v.Seat}
	if !v.Know.Good {
		// Evil leader: seat enough evil (self + known fellows) to sink the quest.
		need := v.FailsReq[v.Quest]
		evilCount := 1 // self
		for _, e := range v.Know.SeenEvil {
			if evilCount >= need {
				break
			}
			if e != v.Seat && !contains(team, e) {
				team = append(team, e)
				evilCount++
			}
		}
	}
	// Fill remaining slots with seats not believed evil.
	for p := 0; len(team) < size && p < v.NumPlayers; p++ {
		if contains(team, p) || known[p] {
			continue
		}
		team = append(team, p)
	}
	// Last resort: pad with anyone to reach the required size.
	for p := 0; len(team) < size && p < v.NumPlayers; p++ {
		if !contains(team, p) {
			team = append(team, p)
		}
	}
	return team[:size]
}

func (b *RuleBot) VoteTeam(v GameView) bool {
	team := v.ProposedTeam
	known := knownEvilSet(v)
	if !v.Know.Good {
		// Evil: approve teams they can sink; otherwise stall, but approve late to
		// avoid handing good a free auto-pass on the hammer.
		evilOn := 0
		for _, p := range team {
			if p == v.Seat || known[p] {
				evilOn++
			}
		}
		if evilOn >= v.FailsReq[v.Quest] {
			return true
		}
		return v.Proposal >= 3
	}
	// Good: reject any team containing a player known to be evil (Merlin only);
	// otherwise approve to keep quests moving.
	for _, p := range team {
		if known[p] {
			return false
		}
	}
	return true
}

func (b *RuleBot) VoteQuest(v GameView) bool {
	return v.Know.Good // good pass; evil fail
}

func (b *RuleBot) Assassinate(v GameView) int {
	known := knownEvilSet(v)
	var goods []int
	for p := 0; p < v.NumPlayers; p++ {
		if p == v.Seat || known[p] {
			continue
		}
		goods = append(goods, p)
	}
	if len(goods) == 0 {
		return (v.Seat + 1) % v.NumPlayers
	}
	return goods[b.rng.Intn(len(goods))]
}

func (b *RuleBot) Discuss(v GameView) string { return "" }
