// Package main implements compromise-eval: a key-free testbed for whether
// gemot's compromise SYNTHESIS beats mere SELECTION among proposals.
//
// The domain is hidden-constraint scheduling. Each agent privately holds a set
// of blocked slots and soft preferences. The globally-feasible slots are the
// intersection no agent blocks — which no single agent can see. Instances are
// generated so that the globally-optimal slot is feasible for everyone but is
// NOT any agent's own proposal, and so that the best PROPOSED feasible slot is
// strictly worse than that optimum. Under those conditions any procedure that
// only chooses among proposals (plurality, random dictator, best-proposal
// oracle) is capped below the optimum; only a procedure that SYNTHESISES a new
// option can close the gap. That is the whole point: it is the selection
// ceiling made concrete, so a later LLM arm can be measured against it.
//
// Unlike chess, the private information lives in what agents SAY (RenderPosition
// states each agent's blocks and prefs), so a downstream analysis layer can
// actually extract and combine it.
package main

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
)

// Slot is an index into the week grid, 0 .. Days*PerDay-1.
type Slot int

// Agent holds one participant's private constraints.
type Agent struct {
	Name    string
	Blocked map[Slot]bool // slots this agent cannot attend
	Pref    map[Slot]int  // soft preference, higher is better (0 if absent)
}

// Instance is one scheduling problem.
type Instance struct {
	ID     int
	Days   int
	PerDay int
	Agents []Agent
}

func (in Instance) slots() int { return in.Days * in.PerDay }

var dayNames = []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
var timeNames = []string{"08:00", "09:00", "10:00", "11:00", "12:00", "13:00", "14:00", "15:00", "16:00", "17:00", "18:00", "19:00"}

// Label renders a slot as e.g. "Wed 14:00".
func (in Instance) Label(s Slot) string {
	d := int(s) / in.PerDay
	t := int(s) % in.PerDay
	return fmt.Sprintf("%s %s", dayNames[d%len(dayNames)], timeNames[t%len(timeNames)])
}

// blockedByAny reports whether any agent blocks the slot.
func (in Instance) blockedByAny(s Slot) bool {
	for _, a := range in.Agents {
		if a.Blocked[s] {
			return true
		}
	}
	return false
}

// Feasible returns the globally-feasible slots (blocked by no agent), sorted.
func (in Instance) Feasible() []Slot {
	var out []Slot
	for s := Slot(0); int(s) < in.slots(); s++ {
		if !in.blockedByAny(s) {
			out = append(out, s)
		}
	}
	return out
}

// SoftScore is the sum of every agent's preference for a slot — the group's
// total welfare if that slot is chosen. Feasibility is a separate check.
func (in Instance) SoftScore(s Slot) int {
	total := 0
	for _, a := range in.Agents {
		total += a.Pref[s]
	}
	return total
}

// IsFeasible reports whether a slot works for everyone.
func (in Instance) IsFeasible(s Slot) bool { return !in.blockedByAny(s) }

// GlobalOpt returns the feasible slot with the highest soft score (ties broken
// by lowest slot index), and its score. ok is false if nothing is feasible.
func (in Instance) GlobalOpt() (Slot, int, bool) {
	best, bestScore, ok := Slot(-1), -1, false
	for _, s := range in.Feasible() {
		if sc := in.SoftScore(s); !ok || sc > bestScore {
			best, bestScore, ok = s, sc, true
		}
	}
	return best, bestScore, ok
}

// Proposal is the slot an agent would put forward from its OWN partial view:
// the slot it most prefers among the slots it personally can attend. It cannot
// see other agents' blocks, so its proposal may be globally infeasible.
func (in Instance) Proposal(i int) Slot {
	a := in.Agents[i]
	best, bestPref := Slot(0), -1
	for s := Slot(0); int(s) < in.slots(); s++ {
		if a.Blocked[s] {
			continue
		}
		if p := a.Pref[s]; p > bestPref {
			best, bestPref = s, p
		}
	}
	return best
}

// Proposals returns every agent's proposal, in agent order.
func (in Instance) Proposals() []Slot {
	out := make([]Slot, len(in.Agents))
	for i := range in.Agents {
		out[i] = in.Proposal(i)
	}
	return out
}

// RenderPosition states agent i's constraints in prose. This is what makes the
// hidden information surface in the deliberation — the analysis layer reads
// these, not the raw block sets.
func (in Instance) RenderPosition(i int) string {
	a := in.Agents[i]
	var blocked, prefs []Slot
	for s := Slot(0); int(s) < in.slots(); s++ {
		if a.Blocked[s] {
			blocked = append(blocked, s)
		}
		if a.Pref[s] > 0 {
			prefs = append(prefs, s)
		}
	}
	sort.Slice(prefs, func(x, y int) bool { return a.Pref[prefs[x]] > a.Pref[prefs[y]] })

	var b strings.Builder
	fmt.Fprintf(&b, "I am %s. ", a.Name)
	if len(blocked) > 0 {
		labels := make([]string, len(blocked))
		for j, s := range blocked {
			labels[j] = in.Label(s)
		}
		fmt.Fprintf(&b, "I cannot meet at: %s. ", strings.Join(labels, ", "))
	} else {
		b.WriteString("I have no hard conflicts. ")
	}
	if len(prefs) > 0 {
		labels := make([]string, 0, len(prefs))
		for _, s := range prefs {
			labels = append(labels, fmt.Sprintf("%s (%d)", in.Label(s), a.Pref[s]))
		}
		fmt.Fprintf(&b, "My preferred times, best first, are: %s. ", strings.Join(labels, ", "))
	}
	mine := in.Proposal(i)
	fmt.Fprintf(&b, "My proposal is %s.", in.Label(mine))
	return b.String()
}

// Generate produces `count` instances that satisfy the hidden-profile-for-
// synthesis property, deterministically from seed. Instances that don't create
// a genuine selection gap are rejected and resampled.
func Generate(count int, seed int64, agents, days, perDay int, blockRate float64) []Instance {
	rng := rand.New(rand.NewSource(seed))
	out := make([]Instance, 0, count)
	names := []string{"Ada", "Boris", "Chen", "Devi", "Ezra", "Faye", "Gita", "Hugo", "Iris", "Jamal", "Kira", "Liam"}
	for id := 0; len(out) < count; id++ {
		if id > count*10000 {
			panic("compromise-eval: generator could not meet the gap property; loosen parameters")
		}
		in := Instance{ID: len(out), Days: days, PerDay: perDay}
		total := days * perDay
		for a := 0; a < agents; a++ {
			ag := Agent{Name: names[a%len(names)], Blocked: map[Slot]bool{}, Pref: map[Slot]int{}}
			for s := Slot(0); int(s) < total; s++ {
				if rng.Float64() < blockRate {
					ag.Blocked[s] = true
				} else if rng.Float64() < 0.5 {
					ag.Pref[s] = 1 + rng.Intn(3) // 1..3
				}
			}
			in.Agents = append(in.Agents, ag)
		}
		if qualifies(in) {
			out = append(out, in)
		}
	}
	return out
}

// qualifies enforces the properties that make synthesis matter:
//  1. at least one globally-feasible slot exists,
//  2. the global optimum is NOT any agent's proposal (selection can't name it),
//  3. the best PROPOSED feasible slot scores strictly below the optimum
//     (a real gap for synthesis to close).
func qualifies(in Instance) bool {
	opt, optScore, ok := in.GlobalOpt()
	if !ok {
		return false
	}
	props := in.Proposals()
	for _, p := range props {
		if p == opt {
			return false // optimum is directly proposable — no synthesis needed
		}
	}
	bestProposed := -1
	for _, p := range props {
		if in.IsFeasible(p) {
			if sc := in.SoftScore(p); sc > bestProposed {
				bestProposed = sc
			}
		}
	}
	return optScore > bestProposed // strict selection gap
}
