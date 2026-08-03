// avalon measures whether structured multi-agent deliberation helps a team win at
// Avalon — a hidden-role, adversarial, deception-heavy game — the conflict axis
// that Codenames (cooperative, shared-info) could not probe. All arms play the
// SAME per-game role deals (seeded per game), so any win-rate gap is attributable
// to the policy, not the deal.
//
// Arms: bot (rule baseline, no LLM) | solo (LLM agents, no discussion) |
// chat / structured (LLM agents + gemot deliberation — added with gemot.go).
//
// Usage:
//
//	set -a; . ./.env; set +a
//	go run ./scripts/avalon --arms bot                       # free baseline
//	go run ./scripts/avalon --arms bot,solo --games 10 --model claude-haiku-4-5
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
)

func main() {
	n := flag.Int("n", 5, "players per game (5-10)")
	games := flag.Int("games", 10, "games per arm")
	seed := flag.Int64("seed", 2026, "base seed (game i uses seed*10000+i, identical across arms)")
	armsFlag := flag.String("arms", "bot", "comma-separated arms: bot,solo,chat,structured")
	model := flag.String("model", "claude-haiku-4-5", "Anthropic model for LLM agents")
	percival := flag.Bool("percival", true, "include Percival + Morgana (needs >=2 evil seats)")
	url := flag.String("url", "http://localhost:8080/mcp", "gemot MCP URL (chat/structured arms)")
	secret := flag.String("secret", os.Getenv("GEMOT_API_SECRET"), "gemot API secret (chat/structured arms)")
	show := flag.Bool("show", false, "print each game's outcome")
	flag.Parse()

	opts := Options{Percival: *percival, Morgana: *percival}
	arms := strings.Split(*armsFlag, ",")
	for i := range arms {
		arms[i] = strings.TrimSpace(arms[i])
	}

	// One shared LLM per model — stateless, safe across seats.
	var sharedLLM *LLM
	needLLM := false
	for _, a := range arms {
		if a != "bot" {
			needLLM = true
		}
	}
	if needLLM {
		l, err := NewLLM(*model)
		if err != nil {
			fmt.Fprintln(os.Stderr, "avalon:", err)
			os.Exit(1)
		}
		sharedLLM = l
	}
	_ = url
	_ = secret

	fmt.Printf("avalon: %d players, %d games/arm, seed %d, arms=%s", *n, *games, *seed, *armsFlag)
	if needLLM {
		fmt.Printf(", model=%s", *model)
	}
	fmt.Println()

	type agg struct {
		good, merlinKills, threeSucc, proposals, count int
	}
	results := map[string]*agg{}
	order := []string{}

	for _, arm := range arms {
		a := &agg{}
		results[arm] = a
		order = append(order, arm)
		for i := 0; i < *games; i++ {
			gameSeed := *seed*10000 + int64(i)
			g, err := NewGame(*n, opts, rand.New(rand.NewSource(gameSeed)))
			if err != nil {
				fmt.Fprintln(os.Stderr, "avalon:", err)
				os.Exit(1)
			}
			players, cfg, err := buildArm(arm, g, sharedLLM, gameSeed)
			if err != nil {
				fmt.Fprintln(os.Stderr, "avalon:", err)
				os.Exit(1)
			}
			out := RunGame(g, players, cfg)
			a.count++
			a.proposals += out.Proposals
			if out.GoodWin {
				a.good++
			}
			if out.MerlinKilled {
				a.merlinKills++
			}
			if out.ThreeSuccesses {
				a.threeSucc++
			}
			if *show {
				fmt.Printf("  [%s] game %d: good=%v 3-successes=%v merlin-killed=%v quests=%v\n",
					arm, i, out.GoodWin, out.ThreeSuccesses, out.MerlinKilled, out.Results)
			}
		}
	}

	fmt.Printf("\n%-14s %10s %12s %14s %10s\n", "arm", "good-win%", "3-success%", "merlin-kill%", "proposals")
	fmt.Printf("%-14s %10s %12s %14s %10s\n", "---", "--------", "----------", "-----------", "---------")
	for _, arm := range order {
		a := results[arm]
		if a.count == 0 {
			continue
		}
		fmt.Printf("%-14s %9.1f%% %11.1f%% %13.1f%% %10.2f\n",
			arm,
			100*float64(a.good)/float64(a.count),
			100*float64(a.threeSucc)/float64(a.count),
			pctOf(a.merlinKills, a.threeSucc),
			float64(a.proposals)/float64(a.count))
	}
	fmt.Println("\ngood-win% = good team victories; 3-success% = good passed 3 quests (reached assassination);")
	fmt.Println("merlin-kill% = of those, how often the assassin found Merlin.")
}

// buildArm constructs the seat policies and run config for one arm on game g.
func buildArm(arm string, g *Game, llm *LLM, seed int64) ([]Player, RunConfig, error) {
	rng := rand.New(rand.NewSource(seed ^ 0x5eed))
	players := make([]Player, g.NumPlayers)
	switch arm {
	case "bot":
		for s := range players {
			players[s] = NewRuleBot(fmt.Sprintf("bot%d", s), rng)
		}
		return players, RunConfig{Arm: arm}, nil
	case "solo":
		for s := range players {
			players[s] = NewLLMAgent(llm, fmt.Sprintf("seat%d", s), NewRuleBot(fmt.Sprintf("bot%d", s), rng))
		}
		return players, RunConfig{Arm: arm}, nil
	default:
		return nil, RunConfig{}, fmt.Errorf("arm %q not implemented yet (bot, solo available; chat/structured need gemot.go)", arm)
	}
}

func pctOf(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return 100 * float64(num) / float64(den)
}
