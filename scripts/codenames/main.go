// codenames measures whether structured multi-agent deliberation beats simpler
// aggregation at a no-oracle judgment task: given a spymaster's clue, which board
// words are the team's? Single-turn evaluation (the paired, clean-isolation
// measurement) — all aggregation arms consume the same guesser judgments, so the
// only varying factor is how those judgments are combined. Multi-turn
// turns-to-clear (comparable to arXiv:2412.11373) is a planned follow-on.
//
// Usage:
//
//	set -a; . ./.env; set +a   # ANTHROPIC_API_KEY for codemaster+guessers
//	go run ./scripts/codenames --n 3 --seed 2026                       # solo + majority + oracle
//	go run ./scripts/codenames --n 3 --seed 2026 --url http://localhost:8080/mcp --arm-label gemot-structured
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	n := flag.Int("n", 5, "number of boards")
	seed := flag.Int64("seed", 2026, "generator seed")
	cmModel := flag.String("cm-model", "claude-haiku-4-5", "codemaster (spymaster) model")
	gModel := flag.String("guesser-model", "claude-haiku-4-5", "guesser model")
	url := flag.String("url", "", "gemot MCP URL; when set, adds a live gemot deliberation arm")
	secret := flag.String("secret", os.Getenv("GEMOT_API_SECRET"), "gemot API secret / key")
	template := flag.String("template", "review", "deliberation template for the gemot arm (min_participants <= guesser count)")
	armLabel := flag.String("arm-label", "gemot", "scoreboard label for the gemot arm")
	show := flag.Bool("show", false, "print each board's clue and per-arm guesses")
	flag.Parse()

	cm, err := NewLLM(*cmModel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "codenames:", err)
		os.Exit(1)
	}
	guesser, err := NewLLM(*gModel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "codenames:", err)
		os.Exit(1)
	}
	var g *Gemot
	if *url != "" {
		g = NewGemot(*url, *secret)
		defer g.Close()
		fmt.Printf("live gemot arm %q via %s (template %q)\n", *armLabel, *url, *template)
	}
	ctx := context.Background()

	boards := Generate(*n, *seed)
	fmt.Printf("codenames: %d boards, codemaster=%s guesser=%s\n", len(boards), *cmModel, *gModel)

	var all [][]ArmResult
	for i := range boards {
		b := &boards[i]
		clue, num, intended, err := cm.Clue(*b)
		if err != nil {
			fmt.Printf("board %d: codemaster failed: %v\n", b.ID, err)
			continue
		}
		b.Clue, b.ClueN, b.IntendedIdx = clue, num, intended

		var positions []GuesserPosition
		for _, st := range guessStyles {
			gs, reason, err := guesser.Guess(st, b.Words, clue, num)
			if err != nil {
				fmt.Printf("  board %d guesser %s failed: %v\n", b.ID, st.Name, err)
				continue
			}
			positions = append(positions, GuesserPosition{Style: st.Name, Reasoning: reason, Guesses: gs})
		}
		if len(positions) == 0 {
			continue
		}

		var results []ArmResult
		for _, p := range positions {
			results = append(results, scoreArm(*b, "solo:"+p.Style, p.Guesses))
		}
		results = append(results, scoreArm(*b, "majority-vote", majorityAggregate(positions)))
		var intendedWords []string
		for _, idx := range intended {
			intendedWords = append(intendedWords, b.Words[idx])
		}
		results = append(results, scoreArm(*b, "oracle:intent", intendedWords))

		if g != nil {
			if guesses, ok := RunGemotGuess(ctx, g, *b, positions, *template, fmt.Sprintf("codenames_%d_%d", *seed, b.ID)); ok {
				results = append(results, scoreArm(*b, *armLabel, guesses))
			} else {
				results = append(results, ArmResult{Arm: *armLabel})
			}
			fmt.Printf("  board %d/%d: %s arm done\n", b.ID+1, len(boards), *armLabel)
		}

		all = append(all, results)
		if *show {
			printBoardResult(*b, results)
		}
	}

	fmt.Printf("\ncodenames: %d scored boards, seed %d\n", len(all), *seed)
	fmt.Printf("%-22s %11s %11s %11s\n", "arm", "mean-score", "assassin%", "intent")
	fmt.Printf("%-22s %11s %11s %11s\n", "---", "----------", "---------", "------")
	for _, a := range Summarize(all) {
		fmt.Printf("%-22s %11.2f %10.0f%% %11.2f\n", a.Arm, a.MeanScore, a.AssassinRate*100, a.MeanIntent)
	}
	fmt.Println("\nmean-score = team words secured before the turn ends (higher better);")
	fmt.Println("intent = fraction of the spymaster's intended words recovered.")
}

func printBoardResult(b Board, results []ArmResult) {
	fmt.Printf("\n=== board %d — clue %q %d — intended: %s ===\n", b.ID, b.Clue, b.ClueN, strings.Join(intendedWords(b), ", "))
	fmt.Printf("  team: %s\n", strings.Join(b.sortedTeamWords(), ", "))
	for _, r := range results {
		mark := ""
		if r.HitAssassin {
			mark = " ASSASSIN"
		}
		fmt.Printf("    %-22s score %d  [%s]%s\n", r.Arm, r.Score, strings.Join(r.Guesses, ", "), mark)
	}
}

func intendedWords(b Board) []string {
	var out []string
	for _, idx := range b.IntendedIdx {
		out = append(out, b.Words[idx])
	}
	return out
}
