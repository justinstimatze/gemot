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
	"time"
)

func main() {
	n := flag.Int("n", 5, "players per game (5-10)")
	games := flag.Int("games", 10, "games per arm")
	seed := flag.Int64("seed", 2026, "base seed (game i uses seed*10000+i, identical across arms)")
	startOffset := flag.Int("start-offset", 0, "first game/deal offset; advance it per batch (same --seed) to accumulate poolable games without replaying deals. The run prints the next --start-offset to use.")
	armsFlag := flag.String("arms", "bot", "comma-separated arms: bot,solo,chat,summary,structured")
	model := flag.String("model", "claude-sonnet-4-6", "Anthropic model for LLM agents")
	percival := flag.Bool("percival", true, "include Percival + Morgana (needs >=2 evil seats)")
	url := flag.String("url", "http://localhost:8080/mcp", "gemot MCP URL (chat/structured arms)")
	secret := flag.String("secret", os.Getenv("GEMOT_API_SECRET"), "gemot API secret (chat/structured arms)")
	template := flag.String("template", "review", "gemot deliberation template (structured arm)")
	analyzeTimeout := flag.Duration("analyze-timeout", 10*time.Minute, "per-deliberation analysis poll deadline (structured arm)")
	journalPath := flag.String("journal", "", "path to write the per-game journal (JSONL); default avalon-journal-seed<seed>.jsonl")
	show := flag.Bool("show", false, "print each game's outcome")
	flag.Parse()

	opts := Options{Percival: *percival, Morgana: *percival}
	journal := NewJournal()
	jpath := *journalPath
	if jpath == "" {
		jpath = fmt.Sprintf("avalon-journal-seed%d.jsonl", *seed)
	}
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
		// Warm the cached system prefix with one call so the subsequent parallel
		// per-seat batches READ the cache instead of each writing it.
		if _, err := sharedLLM.complete(avalonSystemPrompt, "Reply with the single word: ready.", 8); err != nil {
			fmt.Fprintln(os.Stderr, "avalon: cache warm-up failed:", err)
		} else {
			fmt.Println("cache warmed:", sharedLLM.Stats())
		}
	}
	var gm *GemotArm
	for _, a := range arms {
		if a == "structured" {
			client := NewGemot(*url, *secret)
			defer client.Close()
			gm = NewGemotArm(client, *template, fmt.Sprintf("avalon_%d", *seed), journal)
			gm.AnalyzeTimeout = *analyzeTimeout
			fmt.Printf("structured arm via %s (template %q, analyze-timeout %s)\n", *url, *template, *analyzeTimeout)
		}
	}

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
		results[arm] = &agg{}
		order = append(order, arm)
	}

	// Run structured first per seed so a degraded seed is caught before the other
	// arms spend LLM calls on it. If structured degrades, the whole seed is
	// discarded for ALL arms (preserving the paired deal) and a fresh seed is
	// drawn — so a contaminated data point can never reach the final dataset.
	runOrder := []string{}
	for _, a := range arms {
		if a == "structured" {
			runOrder = append(runOrder, a)
		}
	}
	for _, a := range arms {
		if a != "structured" {
			runOrder = append(runOrder, a)
		}
	}

	type pending struct {
		arm string
		out Outcome
	}
	committed, seedOffset, discarded := 0, *startOffset, 0
	maxDiscards := *games*3 + 10 // fail loudly rather than spin forever if gemot is down
	for committed < *games {
		thisOffset := seedOffset // absolute game index — unique across batches for clean pooling
		gameSeed := *seed*10000 + int64(seedOffset)
		seedOffset++
		jSnap := journal.Len()
		delibBefore, degBefore := 0, 0
		if gm != nil {
			delibBefore, degBefore = gm.Deliberations, gm.Degraded
		}
		var pend []pending
		seedOK := true
		for _, arm := range runOrder {
			g, err := NewGame(*n, opts, rand.New(rand.NewSource(gameSeed)))
			if err != nil {
				fmt.Fprintln(os.Stderr, "avalon:", err)
				os.Exit(1)
			}
			journal.Begin(arm, thisOffset)
			players, cfg, err := buildArm(arm, g, sharedLLM, gm, journal, gameSeed)
			if err != nil {
				fmt.Fprintln(os.Stderr, "avalon:", err)
				os.Exit(1)
			}
			out := RunGame(g, players, cfg)
			if arm == "structured" && gm != nil && gm.Degraded > degBefore {
				journal.Truncate(jSnap)
				gm.Deliberations, gm.Degraded = delibBefore, degBefore
				discarded++
				fmt.Fprintf(os.Stderr, "  [seed %d] structured degraded — discarding seed for all arms, redrawing (%d discarded so far)\n", gameSeed, discarded)
				seedOK = false
				break
			}
			pend = append(pend, pending{arm, out})
		}
		if !seedOK {
			if discarded > maxDiscards {
				fmt.Fprintf(os.Stderr, "avalon: too many degraded seeds (%d) — is the gemot server healthy? aborting.\n", discarded)
				os.Exit(1)
			}
			continue
		}
		for _, p := range pend {
			a := results[p.arm]
			a.count++
			a.proposals += p.out.Proposals
			if p.out.GoodWin {
				a.good++
			}
			if p.out.MerlinKilled {
				a.merlinKills++
			}
			if p.out.ThreeSuccesses {
				a.threeSucc++
			}
			if *show {
				fmt.Printf("  [%s] game %d: good=%v 3-successes=%v merlin-killed=%v quests=%v\n",
					p.arm, thisOffset, p.out.GoodWin, p.out.ThreeSuccesses, p.out.MerlinKilled, p.out.Results)
			}
		}
		committed++
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

	if gm != nil {
		if gm.Degraded > 0 {
			// Should be unreachable: degraded seeds are discarded + rolled back above.
			fmt.Printf("\nWARNING: structured arm degraded to chat on %d/%d deliberations — those data points are contaminated.\n", gm.Degraded, gm.Deliberations)
		} else if gm.Deliberations > 0 {
			fmt.Printf("\nstructured: %d/%d gemot deliberations succeeded (0 degraded); slowest aggregation %s.\n",
				gm.Deliberations, gm.Deliberations, gm.MaxAggregate.Round(time.Second))
		}
		if discarded > 0 {
			fmt.Printf("discarded %d contaminated seed(s) and redrew to reach %d clean games/arm.\n", discarded, *games)
		}
	}
	if needLLM {
		fb := journal.FallbackCounts()
		total := 0
		for _, arm := range order {
			total += fb[arm]
		}
		if total == 0 {
			fmt.Println("\nLLM->rule-bot fallbacks: 0 (no arm contaminated by bot play).")
		} else {
			fmt.Printf("\nWARNING: LLM->rule-bot fallbacks (parse/API failures inject bot play):")
			for _, arm := range order {
				if fb[arm] > 0 {
					fmt.Printf(" %s=%d", arm, fb[arm])
				}
			}
			fmt.Printf(" (total %d)\n", total)
		}
	}
	if sharedLLM != nil {
		fmt.Println(sharedLLM.Stats())
	}
	if err := journal.WriteJSONL(jpath); err != nil {
		fmt.Fprintln(os.Stderr, "avalon: journal write failed:", err)
	} else {
		fmt.Printf("journal: %s (%d entries)\n", jpath, journal.Len())
	}
	fmt.Printf("to add more poolable games later: --seed %d --start-offset %d (a new --journal), then pool the JSONL files.\n", *seed, seedOffset)
}

// buildArm constructs the seat policies and run config for one arm on game g.
func buildArm(arm string, g *Game, llm *LLM, gm *GemotArm, journal *Journal, seed int64) ([]Player, RunConfig, error) {
	rng := rand.New(rand.NewSource(seed ^ 0x5eed))
	players := make([]Player, g.NumPlayers)
	llmAgents := func() {
		for s := range players {
			players[s] = NewLLMAgent(llm, fmt.Sprintf("seat%d", s), NewRuleBot(fmt.Sprintf("bot%d", s), rng), journal)
		}
	}
	switch arm {
	case "bot":
		for s := range players {
			players[s] = NewRuleBot(fmt.Sprintf("bot%d", s), rng)
		}
		return players, RunConfig{Arm: arm, Journal: journal}, nil
	case "solo":
		llmAgents()
		return players, RunConfig{Arm: arm, Journal: journal}, nil
	case "chat":
		llmAgents()
		return players, RunConfig{Arm: arm, Discuss: chatDiscuss, Journal: journal}, nil
	case "summary":
		llmAgents()
		return players, RunConfig{Arm: arm, Discuss: NewSummaryArm(llm, journal).discuss, Journal: journal}, nil
	case "structured":
		if gm == nil {
			return nil, RunConfig{}, fmt.Errorf("structured arm needs a gemot client")
		}
		llmAgents()
		return players, RunConfig{Arm: arm, Discuss: gm.discuss, Journal: journal}, nil
	default:
		return nil, RunConfig{}, fmt.Errorf("unknown arm %q (bot, solo, chat, summary, structured)", arm)
	}
}

func pctOf(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return 100 * float64(num) / float64(den)
}
