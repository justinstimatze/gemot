package main

import "sort"

// GuesserPosition is one guesser's judgment for a clue: its ranked words + why.
// Every aggregation arm consumes the SAME positions, so the comparison isolates
// the aggregation method, not the underlying guesses.
type GuesserPosition struct {
	Style     string
	Reasoning string
	Guesses   []string
}

// majorityAggregate ranks words by cross-guesser agreement (no deliberation):
// words at least ceil(N/2) guessers listed, ordered by agreement then mean rank.
// If none reach a majority, fall back to the single most-agreed word. This is
// the "wisdom of crowds without discussion" baseline.
func majorityAggregate(positions []GuesserPosition) []string {
	count := map[string]int{}
	rankSum := map[string]float64{}
	for _, p := range positions {
		for r, w := range p.Guesses {
			count[w]++
			rankSum[w] += float64(r)
		}
	}
	majority := (len(positions) + 1) / 2
	type wc struct {
		word    string
		c       int
		avgRank float64
	}
	var picks []wc
	for w, c := range count {
		if c >= majority {
			picks = append(picks, wc{w, c, rankSum[w] / float64(c)})
		}
	}
	if len(picks) == 0 { // nobody agreed; take the most-frequent single word
		for w, c := range count {
			picks = append(picks, wc{w, c, rankSum[w] / float64(c)})
		}
		sort.Slice(picks, func(i, j int) bool {
			if picks[i].c != picks[j].c {
				return picks[i].c > picks[j].c
			}
			return picks[i].avgRank < picks[j].avgRank
		})
		if len(picks) > 0 {
			return []string{picks[0].word}
		}
		return nil
	}
	sort.Slice(picks, func(i, j int) bool {
		if picks[i].c != picks[j].c {
			return picks[i].c > picks[j].c
		}
		if picks[i].avgRank != picks[j].avgRank {
			return picks[i].avgRank < picks[j].avgRank
		}
		return picks[i].word < picks[j].word
	})
	out := make([]string, len(picks))
	for i, p := range picks {
		out[i] = p.word
	}
	return out
}

// ArmResult scores one arm's guess on one board.
type ArmResult struct {
	Arm           string
	Guesses       []string
	Score         int
	HitAssassin   bool
	IntentOverlap float64
}

func scoreArm(b Board, arm string, guesses []string) ArmResult {
	r := ScoreGuess(b, guesses)
	return ArmResult{
		Arm: arm, Guesses: guesses, Score: r.Score,
		HitAssassin: r.HitAssassin, IntentOverlap: b.IntentOverlap(guesses),
	}
}

// Aggregate is a per-arm summary across boards.
type Aggregate struct {
	Arm          string
	N            int
	MeanScore    float64 // team words secured per turn (higher better)
	AssassinRate float64
	MeanIntent   float64
}

func Summarize(all [][]ArmResult) []Aggregate {
	order := []string{}
	acc := map[string]*Aggregate{}
	for _, board := range all {
		for _, r := range board {
			a, ok := acc[r.Arm]
			if !ok {
				a = &Aggregate{Arm: r.Arm}
				acc[r.Arm] = a
				order = append(order, r.Arm)
			}
			a.N++
			a.MeanScore += float64(r.Score)
			a.MeanIntent += r.IntentOverlap
			if r.HitAssassin {
				a.AssassinRate++
			}
		}
	}
	out := make([]Aggregate, 0, len(order))
	for _, name := range order {
		a := acc[name]
		if a.N > 0 {
			a.MeanScore /= float64(a.N)
			a.MeanIntent /= float64(a.N)
			a.AssassinRate /= float64(a.N)
		}
		out = append(out, *a)
	}
	return out
}
