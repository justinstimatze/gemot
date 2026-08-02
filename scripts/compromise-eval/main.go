// compromise-eval measures whether synthesising a new option beats selecting
// among proposed ones, in a hidden-constraint scheduling domain where the
// private information is stated in each agent's position.
//
// Phase 1 (this file) runs only the key-free deterministic arms and prints the
// selection ceiling — the gap a synthesis arm would have to close. The LLM arms
// (freeform-chat, gemot-synthesis) are wired in Phase 2.
//
// Usage:
//
//	go run ./scripts/compromise-eval --n 40 --seed 2026
//	go run ./scripts/compromise-eval --n 1 --seed 7 --show   # inspect one instance
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"
)

func main() {
	n := flag.Int("n", 40, "number of instances")
	seed := flag.Int64("seed", 2026, "generator seed")
	agents := flag.Int("agents", 3, "agents per instance")
	days := flag.Int("days", 5, "days in the grid")
	perDay := flag.Int("perday", 4, "slots per day")
	show := flag.Bool("show", false, "print each instance's positions and arm choices")
	url := flag.String("url", "", "gemot MCP URL; when set, adds a live gemot synthesis arm (e.g. http://localhost:8080/mcp)")
	secret := flag.String("secret", os.Getenv("GEMOT_API_SECRET"), "gemot API secret (default: GEMOT_API_SECRET)")
	template := flag.String("template", "negotiation", "deliberation template for the gemot arm")
	armLabel := flag.String("arm-label", "gemot", "scoreboard label for the gemot arm")
	gemotMode := flag.String("gemot-mode", "free", "gemot compromise mode: free (synthesis) or choice (forced-choice over all slots)")
	flag.Parse()

	if *agents < 2 || *agents > 6 {
		fmt.Fprintln(os.Stderr, "compromise-eval: --agents must be 2..6")
		os.Exit(1)
	}

	var g *Gemot
	if *url != "" {
		g = NewGemot(*url, *secret)
		defer g.Close()
		fmt.Printf("live gemot arm %q via %s (template %q)\n", *armLabel, *url, *template)
	}
	ctx := context.Background()

	suite := Generate(*n, *seed, *agents, *days, *perDay)
	all := make([][]ArmResult, 0, len(suite))
	for _, in := range suite {
		rng := rand.New(rand.NewSource(*seed + int64(in.ID) + 1))
		results := RunDeterministic(in, rng)
		if g != nil {
			start := time.Now()
			slot, _, ok := RunGemotArm(ctx, g, in, *template, fmt.Sprintf("compromise_eval_%d_%d", *seed, in.ID), *gemotMode)
			if ok {
				results = append(results, score(in, *armLabel, slot))
			} else {
				results = append(results, ArmResult{Arm: *armLabel, Chosen: -1, Feasible: false})
			}
			fmt.Printf("  instance %d/%d: %s arm done (%.0fs)\n", in.ID+1, len(suite), *armLabel, time.Since(start).Seconds())
		}
		all = append(all, results)
		if *show {
			printInstance(in, results)
		}
	}

	fmt.Printf("\ncompromise-eval: %d instances, %d agents, %dx%d grid, seed %d\n",
		len(suite), *agents, *days, *perDay, *seed)
	fmt.Println("Every instance has a feasible optimum that NO agent proposed;")
	fmt.Println("the selection ceiling (oracle:best-proposal) is capped below it.")
	fmt.Println()
	fmt.Printf("%-24s %10s %10s %10s\n", "arm", "feasible%", "mean-norm", "mean-score")
	fmt.Printf("%-24s %10s %10s %10s\n", "---", "---------", "---------", "----------")
	for _, a := range Summarize(all) {
		fmt.Printf("%-24s %9.0f%% %10.3f %10.2f\n", a.Arm, a.FeasibleRate*100, a.MeanNorm, a.MeanScore)
	}
	fmt.Println()
	fmt.Println("Reading: mean-norm is chosen soft-score / global-optimum soft-score")
	fmt.Println("(infeasible = 0). The headline Phase-2 question is whether an LLM")
	fmt.Println("synthesis arm lands ABOVE oracle:best-proposal and toward global-opt.")
}

func printInstance(in Instance, results []ArmResult) {
	fmt.Printf("\n=== instance %d ===\n", in.ID)
	for i := range in.Agents {
		fmt.Printf("  %s\n", in.RenderPosition(i))
	}
	opt, optScore, _ := in.GlobalOpt()
	fmt.Printf("  feasible slots: ")
	for _, s := range in.Feasible() {
		fmt.Printf("%s(%d) ", in.Label(s), in.SoftScore(s))
	}
	fmt.Printf("\n  global optimum: %s (score %d)\n", in.Label(opt), optScore)
	for _, r := range results {
		mark := "infeasible"
		if r.Feasible {
			mark = fmt.Sprintf("score %d (%.0f%% of opt)", r.Score, r.Norm*100)
		}
		fmt.Printf("    %-24s -> %-10s %s\n", r.Arm, in.Label(r.Chosen), mark)
	}
}
