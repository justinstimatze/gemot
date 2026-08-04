package main

import (
	"math/rand"
	"testing"
)

func newTestGame(t *testing.T, n int, opts Options, seed int64) *Game {
	t.Helper()
	g, err := NewGame(n, opts, rand.New(rand.NewSource(seed)))
	if err != nil {
		t.Fatalf("NewGame(%d): %v", n, err)
	}
	return g
}

// TestPresets pins the config tables against AvalonBench's QUEST_PRESET.
func TestPresets(t *testing.T) {
	cases := []struct {
		n          int
		good, evil int
		teams      [5]int
		fails      [5]int
	}{
		{5, 3, 2, [5]int{2, 3, 2, 3, 3}, [5]int{1, 1, 1, 1, 1}},
		{7, 4, 3, [5]int{2, 3, 3, 4, 4}, [5]int{1, 1, 1, 2, 1}},
		{10, 6, 4, [5]int{3, 4, 4, 5, 5}, [5]int{1, 1, 1, 2, 1}},
	}
	for _, c := range cases {
		p := questPresets[c.n]
		if p.numGood != c.good || p.numEvil != c.evil || p.teamSizes != c.teams || p.failsRequired != c.fails {
			t.Errorf("preset %d = %+v, want good=%d evil=%d teams=%v fails=%v", c.n, p, c.good, c.evil, c.teams, c.fails)
		}
	}
}

// TestRoleComposition checks each seat gets exactly the right side counts, always
// exactly one Assassin and one Merlin, and enabled special roles present.
func TestRoleComposition(t *testing.T) {
	for n := 5; n <= 10; n++ {
		// Enable only as many special evil roles as there are evil seats
		// (Assassin is always forced, so specials must leave room for it).
		opts := Options{Percival: true, Morgana: true}
		if questPresets[n].numEvil >= 4 {
			opts.Mordred = true
			opts.Oberon = true
		}
		wantSpecial := []Role{Percival, Morgana}
		if opts.Mordred {
			wantSpecial = append(wantSpecial, Mordred, Oberon)
		}
		for seed := int64(0); seed < 50; seed++ {
			g := newTestGame(t, n, opts, seed)
			if len(g.Roles) != n {
				t.Fatalf("n=%d: %d roles", n, len(g.Roles))
			}
			var good, evil int
			has := map[Role]int{}
			for _, r := range g.Roles {
				has[r]++
				if r.Evil() {
					evil++
				} else {
					good++
				}
			}
			p := questPresets[n]
			if good != p.numGood || evil != p.numEvil {
				t.Errorf("n=%d seed=%d: good=%d evil=%d want %d/%d", n, seed, good, evil, p.numGood, p.numEvil)
			}
			if has[Assassin] != 1 || has[Merlin] != 1 {
				t.Errorf("n=%d seed=%d: assassins=%d merlins=%d (want 1/1)", n, seed, has[Assassin], has[Merlin])
			}
			for _, sp := range wantSpecial {
				if has[sp] != 1 {
					t.Errorf("n=%d seed=%d: role %s count=%d (want 1)", n, seed, sp, has[sp])
				}
			}
		}
	}
}

// TestKnowledge verifies the corrected night information: Merlin sees evil except
// Mordred; Oberon is hidden from other evil and sees none; Percival sees exactly
// Merlin + Morgana.
func TestKnowledge(t *testing.T) {
	// All four special evil roles (Assassin + Morgana + Mordred + Oberon) need
	// four evil seats, which only 10 players provide.
	g := newTestGame(t, 10, Options{Percival: true, Morgana: true, Mordred: true, Oberon: true}, 3)
	roleAt := map[Role]int{}
	for p, r := range g.Roles {
		roleAt[r] = p
	}
	merlin := g.Knowledge(roleAt[Merlin])
	for _, p := range merlin.SeenEvil {
		if g.Roles[p] == Mordred {
			t.Errorf("Merlin should not see Mordred")
		}
		if !g.Roles[p].Evil() {
			t.Errorf("Merlin saw a good player %d as evil", p)
		}
	}
	// Merlin sees all evil except Mordred (Oberon IS visible to Merlin).
	wantMerlin := g.preset.numEvil - 1 // minus Mordred
	if len(merlin.SeenEvil) != wantMerlin {
		t.Errorf("Merlin SeenEvil=%d, want %d", len(merlin.SeenEvil), wantMerlin)
	}

	oberon := g.Knowledge(roleAt[Oberon])
	if len(oberon.SeenEvil) != 0 {
		t.Errorf("Oberon should see no evil, got %v", oberon.SeenEvil)
	}
	// A plain Minion/Assassin should not see Oberon among fellow evil.
	assassin := g.Knowledge(roleAt[Assassin])
	for _, p := range assassin.SeenEvil {
		if g.Roles[p] == Oberon {
			t.Errorf("Assassin should not see Oberon")
		}
	}

	perc := g.Knowledge(roleAt[Percival])
	if len(perc.MerlinAndMorgana) != 2 {
		t.Fatalf("Percival should see 2 magical players, got %v", perc.MerlinAndMorgana)
	}
	sawMerlin, sawMorgana := false, false
	for _, p := range perc.MerlinAndMorgana {
		switch g.Roles[p] {
		case Merlin:
			sawMerlin = true
		case Morgana:
			sawMorgana = true
		default:
			t.Errorf("Percival saw non-magical role %s", g.Roles[p])
		}
	}
	if !sawMerlin || !sawMorgana {
		t.Errorf("Percival must see both Merlin and Morgana")
	}
}

// TestGoodWinsThreeQuests drives a full game where three quests succeed and the
// assassin misses Merlin -> good win.
func TestGoodWinsThreeQuests(t *testing.T) {
	g := newTestGame(t, 5, Options{}, 1)
	for q := 0; q < 3; q++ {
		if g.Phase != TeamSelection {
			t.Fatalf("quest %d: phase %s, want Team Selection", q, g.Phase)
		}
		leader := g.Leader
		team := firstN(g.TeamSize(), g.NumPlayers)
		if err := g.ChooseTeam(leader, team); err != nil {
			t.Fatalf("ChooseTeam: %v", err)
		}
		accepted, err := g.GatherTeamVotes(allTrue(g.NumPlayers))
		if err != nil || !accepted {
			t.Fatalf("team vote: accepted=%v err=%v", accepted, err)
		}
		success, _, err := g.GatherQuestVotes(allTrue(g.TeamSize()))
		if err != nil || !success {
			t.Fatalf("quest vote: success=%v err=%v", success, err)
		}
	}
	if g.Phase != Assassination {
		t.Fatalf("after 3 successes phase=%s, want Assassination", g.Phase)
	}
	assassin := g.AssassinPlayer()
	// Target a non-Merlin good player.
	var target int = -1
	for p := range g.Roles {
		if g.Roles[p] != Merlin && g.IsGood(p) {
			target = p
			break
		}
	}
	if target == -1 {
		target = assassin // degenerate; still non-Merlin
	}
	goodWins, err := g.Assassinate(assassin, target)
	if err != nil {
		t.Fatalf("Assassinate: %v", err)
	}
	if !goodWins || !g.GoodVictory {
		t.Errorf("expected good win when assassin misses Merlin")
	}
}

// TestAssassinKillsMerlin: 3 successes but assassin finds Merlin -> evil win.
func TestAssassinKillsMerlin(t *testing.T) {
	g := newTestGame(t, 5, Options{}, 2)
	for q := 0; q < 3; q++ {
		g.ChooseTeam(g.Leader, firstN(g.TeamSize(), g.NumPlayers)) //nolint:errcheck
		g.GatherTeamVotes(allTrue(g.NumPlayers))                   //nolint:errcheck
		g.GatherQuestVotes(allTrue(g.TeamSize()))                  //nolint:errcheck
	}
	merlin := -1
	for p, r := range g.Roles {
		if r == Merlin {
			merlin = p
		}
	}
	goodWins, err := g.Assassinate(g.AssassinPlayer(), merlin)
	if err != nil {
		t.Fatalf("Assassinate: %v", err)
	}
	if goodWins || g.GoodVictory {
		t.Errorf("expected evil win when assassin hits Merlin")
	}
}

// TestEvilWinsThreeFails: three failed quests end the game for evil, no
// assassination.
func TestEvilWinsThreeFails(t *testing.T) {
	g := newTestGame(t, 5, Options{}, 3)
	for q := 0; q < 3; q++ {
		g.ChooseTeam(g.Leader, firstN(g.TeamSize(), g.NumPlayers)) //nolint:errcheck
		g.GatherTeamVotes(allTrue(g.NumPlayers))                   //nolint:errcheck
		success, _, err := g.GatherQuestVotes(allFalse(g.TeamSize()))
		if err != nil {
			t.Fatalf("quest %d: %v", q, err)
		}
		if success {
			t.Fatalf("quest %d unexpectedly succeeded on all-fail votes", q)
		}
	}
	if !g.Done || g.GoodVictory {
		t.Errorf("expected evil win + done after 3 fails; done=%v goodVictory=%v", g.Done, g.GoodVictory)
	}
	if g.Phase == Assassination {
		t.Errorf("3 failed quests must not reach assassination")
	}
}

// TestFifthProposalAutoPasses: four rejections then a fifth proposal passes
// regardless of votes.
func TestFifthProposalAutoPasses(t *testing.T) {
	g := newTestGame(t, 5, Options{}, 4)
	for i := 0; i < 4; i++ {
		if g.Phase != TeamSelection {
			t.Fatalf("proposal %d: phase %s", i, g.Phase)
		}
		if err := g.ChooseTeam(g.Leader, firstN(g.TeamSize(), g.NumPlayers)); err != nil {
			t.Fatalf("ChooseTeam %d: %v", i, err)
		}
		accepted, err := g.GatherTeamVotes(allFalse(g.NumPlayers)) // everyone rejects
		if err != nil {
			t.Fatalf("vote %d: %v", i, err)
		}
		if accepted {
			t.Fatalf("proposal %d should have been rejected", i)
		}
	}
	if g.Proposal != 4 {
		t.Fatalf("after 4 rejections Proposal=%d, want 4", g.Proposal)
	}
	if err := g.ChooseTeam(g.Leader, firstN(g.TeamSize(), g.NumPlayers)); err != nil {
		t.Fatalf("5th ChooseTeam: %v", err)
	}
	accepted, err := g.GatherTeamVotes(allFalse(g.NumPlayers)) // still all reject
	if err != nil {
		t.Fatalf("5th vote: %v", err)
	}
	if !accepted || g.Phase != QuestVoting {
		t.Errorf("fifth proposal must auto-pass: accepted=%v phase=%s", accepted, g.Phase)
	}
}

// TestSelfPlayRandomSanity runs many random-agent games end to end: no panics,
// no illegal-state errors, every game terminates, and both sides win sometimes.
func TestSelfPlayRandomSanity(t *testing.T) {
	rng := rand.New(rand.NewSource(2026))
	goodWins, evilWins := 0, 0
	const games = 3000
	for i := 0; i < games; i++ {
		n := 5 + rng.Intn(6) // 5..10
		g, err := NewGame(n, Options{Percival: true, Morgana: true}, rng)
		if err != nil {
			t.Fatalf("NewGame: %v", err)
		}
		playRandomGame(t, g, rng)
		if !g.Done {
			t.Fatalf("game %d did not terminate", i)
		}
		if g.GoodVictory {
			goodWins++
		} else {
			evilWins++
		}
	}
	if goodWins == 0 || evilWins == 0 {
		t.Errorf("degenerate outcomes: good=%d evil=%d over %d games", goodWins, evilWins, games)
	}
	t.Logf("random self-play: good %d (%.1f%%) / evil %d over %d games",
		goodWins, 100*float64(goodWins)/float64(games), evilWins, games)
}

func playRandomGame(t *testing.T, g *Game, rng *rand.Rand) {
	t.Helper()
	guard := 0
	for !g.Done {
		guard++
		if guard > 1000 {
			t.Fatalf("game failed to terminate within 1000 steps")
		}
		switch g.Phase {
		case TeamSelection:
			team := randomTeam(g.TeamSize(), g.NumPlayers, rng)
			if err := g.ChooseTeam(g.Leader, team); err != nil {
				t.Fatalf("ChooseTeam: %v", err)
			}
		case TeamVoting:
			votes := make([]bool, g.NumPlayers)
			for i := range votes {
				votes[i] = rng.Float64() < 0.6
			}
			if _, err := g.GatherTeamVotes(votes); err != nil {
				t.Fatalf("GatherTeamVotes: %v", err)
			}
		case QuestVoting:
			votes := make([]bool, len(g.Team))
			for i, p := range g.Team {
				// Good always pass; evil fail with prob 0.7.
				votes[i] = g.IsGood(p) || rng.Float64() >= 0.7
			}
			if _, _, err := g.GatherQuestVotes(votes); err != nil {
				t.Fatalf("GatherQuestVotes: %v", err)
			}
		case Assassination:
			target := rng.Intn(g.NumPlayers)
			if _, err := g.Assassinate(g.AssassinPlayer(), target); err != nil {
				t.Fatalf("Assassinate: %v", err)
			}
		}
	}
}

// helpers
func firstN(k, n int) []int {
	if k > n {
		k = n
	}
	out := make([]int, k)
	for i := range out {
		out[i] = i
	}
	return out
}

func randomTeam(k, n int, rng *rand.Rand) []int {
	perm := rng.Perm(n)
	return perm[:k]
}

func allTrue(n int) []bool  { return fill(n, true) }
func allFalse(n int) []bool { return fill(n, false) }
func fill(n int, v bool) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = v
	}
	return out
}
