package main

import (
	"flag"
	"fmt"
	"strings"
)

// This CLI grows arms incrementally. For now it generates boards and prints one,
// so the key-free core (generator + scorer) is runnable and inspectable before
// the LLM arms land.
func main() {
	n := flag.Int("n", 5, "number of boards")
	seed := flag.Int64("seed", 2026, "generator seed")
	show := flag.Bool("show", false, "print each board's words and key")
	flag.Parse()

	boards := Generate(*n, *seed)
	fmt.Printf("codenames: generated %d boards (seed %d) = %d team / %d opponent / %d civilian / %d assassin\n",
		len(boards), *seed, teamCount, opponentCount, civilianCount, assassins)
	if *show {
		for _, b := range boards {
			fmt.Printf("\n=== board %d ===\n", b.ID)
			for i, w := range b.Words {
				fmt.Printf("  %-9s [%s]", w, b.Key[i])
				if (i+1)%5 == 0 {
					fmt.Println()
				}
			}
			fmt.Printf("  team: %s\n", strings.Join(b.sortedTeamWords(), ", "))
		}
	}
}
