// Package main implements the Codenames harness: a judgment-aggregation testbed
// for gemot. A spymaster gives a one-word clue; the guessing FLEET must infer
// which board words are its team's. Ground truth (the key) scores guesses, but
// the clue->word inference is genuine judgment with no oracle — the regime where
// gemot's thesis (aggregate imperfect judgment) should pay off and where
// chess/scheduling (computable answers) could not.
//
// Substrate aligned to "Codenames as a Benchmark for Large Language Models"
// (Stephenson, Sidji, Ronval; arXiv:2412.11373) and its single-team cooperative
// protocol. The board word pool (codenames_original_wordpool.txt) is the
// official Czech Games Edition set vendored from the paper's public code
// (github.com/stepmat/Codenames_GPT, branch ToG_2025) for comparability.
// Boards are NOT seed-identical to the paper (Go's RNG differs from Python's);
// alignment is at the protocol / wordpool / metric level.
package main

import (
	_ "embed"
	"math/rand"
	"sort"
	"strings"
)

//go:embed codenames_original_wordpool.txt
var wordpoolRaw string

// CardType is the hidden identity of a board word.
type CardType int

const (
	Neutral  CardType = iota // civilian bystander: ends the turn, no points
	Team                     // your team's word: +1
	Opponent                 // the other team's word: ends the turn (a miss)
	Assassin                 // instant loss if guessed
)

func (c CardType) String() string {
	switch c {
	case Team:
		return "team"
	case Opponent:
		return "opponent"
	case Assassin:
		return "assassin"
	default:
		return "civilian"
	}
}

// Standard 25-word board: 9 team / 8 opponent / 7 civilian / 1 assassin.
const (
	boardSize     = 25
	teamCount     = 9
	opponentCount = 8
	civilianCount = 7
	assassins     = 1
)

// Board is one Codenames position: 25 words and their parallel hidden key.
type Board struct {
	ID          int        `json:"id"`
	Words       []string   `json:"words"`
	Key         []CardType `json:"key"`
	Clue        string     `json:"clue,omitempty"`
	ClueN       int        `json:"clue_n,omitempty"`
	IntendedIdx []int      `json:"intended_idx,omitempty"` // board indices the spymaster meant
}

func (b Board) keyOf(word string) (CardType, bool) {
	for i, w := range b.Words {
		if strings.EqualFold(w, word) {
			return b.Key[i], true
		}
	}
	return Neutral, false
}

func (b Board) teamWords() []int {
	var out []int
	for i, k := range b.Key {
		if k == Team {
			out = append(out, i)
		}
	}
	return out
}

// Result scores one guessing turn under Codenames rules.
type Result struct {
	Score       int  // team words hit before the turn ended (0 if assassin hit)
	TeamHits    int  // team words correctly guessed before stopping
	HitAssassin bool // guess sequence reached the assassin
	Guessed     int  // guesses played before the turn ended
}

// ScoreGuess plays guesses in confidence order: a team word scores +1 and
// continues; a civilian OR opponent word ends the turn keeping points so far;
// the assassin ends with 0. Rewards calibration — hitting team words AND knowing
// when to stop — not raw recall. Off-board words end the turn as misses.
func ScoreGuess(b Board, guesses []string) Result {
	var r Result
	for _, g := range guesses {
		r.Guessed++
		k, ok := b.keyOf(g)
		switch {
		case !ok || k == Neutral || k == Opponent:
			return r
		case k == Assassin:
			r.HitAssassin = true
			r.Score = 0
			return r
		case k == Team:
			r.TeamHits++
			r.Score++
		}
	}
	return r
}

// IntentOverlap is the fraction of the spymaster's intended words the guess
// sequence includes — recall of intent, independent of turn dynamics.
func (b Board) IntentOverlap(guesses []string) float64 {
	if len(b.IntendedIdx) == 0 {
		return 0
	}
	intended := map[string]bool{}
	for _, i := range b.IntendedIdx {
		intended[strings.ToUpper(b.Words[i])] = true
	}
	hit, seen := 0, map[string]bool{}
	for _, g := range guesses {
		u := strings.ToUpper(g)
		if intended[u] && !seen[u] {
			hit++
			seen[u] = true
		}
	}
	return float64(hit) / float64(len(b.IntendedIdx))
}

// boardWordPool is the official board vocabulary, parsed once from the embedded file.
var boardWordPool = parseWordPool(wordpoolRaw)

func parseWordPool(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		w := strings.TrimSpace(line)
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// Generate builds `count` boards deterministically from seed: 25 distinct words
// sampled from the official pool, keyed 9 team / 8 opponent / 7 civilian / 1
// assassin over shuffled positions.
func Generate(count int, seed int64) []Board {
	rng := rand.New(rand.NewSource(seed))
	out := make([]Board, 0, count)
	for id := 0; id < count; id++ {
		perm := rng.Perm(len(boardWordPool))[:boardSize]
		words := make([]string, boardSize)
		for i, p := range perm {
			words[i] = boardWordPool[p]
		}
		pos := rng.Perm(boardSize)
		key := make([]CardType, boardSize)
		for rank, p := range pos {
			switch {
			case rank < teamCount:
				key[p] = Team
			case rank < teamCount+opponentCount:
				key[p] = Opponent
			case rank < teamCount+opponentCount+civilianCount:
				key[p] = Neutral
			default:
				key[p] = Assassin
			}
		}
		out = append(out, Board{ID: id, Words: words, Key: key})
	}
	return out
}

func (b Board) sortedTeamWords() []string {
	var out []string
	for _, i := range b.teamWords() {
		out = append(out, b.Words[i])
	}
	sort.Strings(out)
	return out
}
