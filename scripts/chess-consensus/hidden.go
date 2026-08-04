package main

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
)

// InfoSets is a hidden-profile deal: who can see which moves.
//
// The structure is taken from the hidden-profile paradigm in group decision
// research. Everyone shares a pool of decent-but-not-best options. The genuinely
// best option — the gem — sits with exactly one agent. No individual can reach
// the right answer alone, and a group that only aggregates first preferences
// gets it wrong systematically, because two of three agents are choosing from
// the shared pool. Only a group that actually pools private information wins.
//
// That failure mode is the point. Human groups reliably flunk it: they
// re-discuss what everyone already knows and discount what only one member
// brought. If gemot's crux detection corrects that, it is a result about the
// mechanism rather than about hand-tuned personality weights.
type InfoSets struct {
	GemUCI    string              `json:"gem_uci"`
	GemSAN    string              `json:"gem_san"`
	GemHolder string              `json:"gem_holder"`
	Shared    []string            `json:"shared"`  // visible to every agent
	Private   map[string][]string `json:"private"` // agent -> moves only it can see
	Sets      map[string][]string `json:"sets"`    // agent -> everything it may search
	Discarded []string            `json:"discarded"`
}

// HiddenConfig controls the deal.
type HiddenConfig struct {
	SharedStart int // 1-indexed reference rank where the shared pool begins
	SharedCount int // how many ranks form the shared pool
}

// DefaultHiddenConfig leaves ranks 2 and 3 out of the deal entirely. That gap
// guarantees the shared pool is meaningfully worse than the gem, so a group
// that fails to pool information cannot accidentally land on a near-equivalent
// move and look successful.
func DefaultHiddenConfig() HiddenConfig {
	return HiddenConfig{SharedStart: 4, SharedCount: 4}
}

// Partition deals the reference-ranked candidates into information sets.
//
// The gem holder is chosen by hashing the position ID rather than by a running
// RNG, so a position always deals the same way no matter which arm is running
// or what order positions are visited in — otherwise arms would not be
// comparable.
func Partition(positionID string, candidates []Line, sans map[string]string, agentIDs []string, cfg HiddenConfig) (InfoSets, error) {
	if len(agentIDs) < 2 {
		return InfoSets{}, fmt.Errorf("hidden profile needs at least 2 agents, got %d", len(agentIDs))
	}
	sharedIdx := cfg.SharedStart - 1
	if sharedIdx < 1 {
		return InfoSets{}, fmt.Errorf("shared pool must start below rank 2")
	}
	if len(candidates) < sharedIdx+cfg.SharedCount {
		return InfoSets{}, fmt.Errorf("need at least %d candidates for this deal, got %d",
			sharedIdx+cfg.SharedCount, len(candidates))
	}

	sets := InfoSets{
		GemUCI:  candidates[0].UCI,
		GemSAN:  sans[candidates[0].UCI],
		Private: map[string][]string{},
		Sets:    map[string][]string{},
	}

	// Ranks between the gem and the shared pool go to nobody.
	for _, l := range candidates[1:sharedIdx] {
		sets.Discarded = append(sets.Discarded, l.UCI)
	}
	for _, l := range candidates[sharedIdx:min(sharedIdx+cfg.SharedCount, len(candidates))] {
		sets.Shared = append(sets.Shared, l.UCI)
	}

	holders := append([]string(nil), agentIDs...)
	sort.Strings(holders)
	sets.GemHolder = holders[hashIndex(positionID, len(holders))]

	// Everything below the shared pool is dealt round-robin as private
	// distractors. They are worse than the shared moves, so they never become
	// an agent's first choice — they exist so the gem holder's information set
	// is not conspicuously different in size from anyone else's.
	rest := candidates[min(sharedIdx+cfg.SharedCount, len(candidates)):]
	for i, l := range rest {
		agent := holders[i%len(holders)]
		sets.Private[agent] = append(sets.Private[agent], l.UCI)
	}

	for _, agent := range holders {
		set := append([]string(nil), sets.Shared...)
		set = append(set, sets.Private[agent]...)
		if agent == sets.GemHolder {
			set = append(set, sets.GemUCI)
		}
		sort.Strings(set)
		sets.Sets[agent] = set
	}
	return sets, nil
}

// Sees reports whether an agent may search a move as part of its own survey.
func (s InfoSets) Sees(agentID, uciMove string) bool {
	for _, m := range s.Sets[agentID] {
		if m == uciMove {
			return true
		}
	}
	return false
}

// Summary renders the deal for the deliberation description, so the record
// shows what each agent could and could not see.
func (s InfoSets) Summary(sans map[string]string) string {
	var b strings.Builder
	b.WriteString("Each agent searched only the moves in its own information set. Nobody saw every option.\n")
	fmt.Fprintf(&b, "Shared by all agents: %s\n", strings.Join(sansOf(s.Shared, sans), ", "))
	for _, agent := range sortedKeys(s.Sets) {
		fmt.Fprintf(&b, "  %s: %s\n", agent, strings.Join(sansOf(s.Sets[agent], sans), ", "))
	}
	return b.String()
}

func sansOf(moves []string, sans map[string]string) []string {
	out := make([]string, 0, len(moves))
	for _, m := range moves {
		if san, ok := sans[m]; ok {
			out = append(out, san)
		} else {
			out = append(out, m)
		}
	}
	return out
}

func hashIndex(key string, n int) int {
	h := fnv.New32a()
	h.Write([]byte(key)) //nolint:errcheck
	return int(h.Sum32() % uint32(n))
}
