package main

import "testing"

func TestGenerateComposition(t *testing.T) {
	for _, b := range Generate(20, 2026) {
		if len(b.Words) != boardSize || len(b.Key) != boardSize {
			t.Fatalf("board %d: %d words, %d key", b.ID, len(b.Words), len(b.Key))
		}
		// distinct words
		seen := map[string]bool{}
		for _, w := range b.Words {
			if seen[w] {
				t.Fatalf("board %d: duplicate word %q", b.ID, w)
			}
			seen[w] = true
		}
		// key composition
		var team, opp, civ, assassin int
		for _, k := range b.Key {
			switch k {
			case Team:
				team++
			case Opponent:
				opp++
			case Assassin:
				assassin++
			default:
				civ++
			}
		}
		if team != teamCount || opp != opponentCount || civ != civilianCount || assassin != assassins {
			t.Fatalf("board %d: composition team=%d opp=%d civ=%d assassin=%d", b.ID, team, opp, civ, assassin)
		}
	}
}

func TestGenerateDeterministic(t *testing.T) {
	a, b := Generate(5, 99), Generate(5, 99)
	for i := range a {
		for j := range a[i].Words {
			if a[i].Words[j] != b[i].Words[j] || a[i].Key[j] != b[i].Key[j] {
				t.Fatalf("board %d differs across identical seeds", i)
			}
		}
	}
}

// TestScoreGuessTurnRules pins the calibration-aware scoring.
func TestScoreGuessTurnRules(t *testing.T) {
	// hand-built board: team = a,b,c ; neutral = n ; assassin = x
	b := Board{
		Words: []string{"a", "b", "c", "n", "x"},
		Key:   []CardType{Team, Team, Team, Neutral, Assassin},
	}
	cases := []struct {
		name     string
		guesses  []string
		score    int
		assassin bool
	}{
		{"all team then stop", []string{"a", "b"}, 2, false},
		{"team then neutral ends turn", []string{"a", "n", "b"}, 1, false},
		{"neutral first scores zero", []string{"n", "a"}, 0, false},
		{"assassin after hits => 0", []string{"a", "b", "x"}, 0, true},
		{"assassin first => 0", []string{"x"}, 0, true},
		{"off-board word ends turn", []string{"a", "zzz", "b"}, 1, false},
	}
	for _, tc := range cases {
		r := ScoreGuess(b, tc.guesses)
		if r.Score != tc.score || r.HitAssassin != tc.assassin {
			t.Errorf("%s: got score=%d assassin=%v, want score=%d assassin=%v",
				tc.name, r.Score, r.HitAssassin, tc.score, tc.assassin)
		}
	}
}

func TestIntentOverlap(t *testing.T) {
	b := Board{
		Words:       []string{"a", "b", "c", "n", "x"},
		Key:         []CardType{Team, Team, Team, Neutral, Assassin},
		IntendedIdx: []int{0, 1}, // intended a, b
	}
	if got := b.IntentOverlap([]string{"a", "n"}); got != 0.5 {
		t.Errorf("intent overlap = %v, want 0.5", got)
	}
	if got := b.IntentOverlap([]string{"a", "b"}); got != 1.0 {
		t.Errorf("intent overlap = %v, want 1.0", got)
	}
}
