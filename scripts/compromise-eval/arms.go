package main

import (
	"math/rand"
	"sort"
)

// ArmResult is one arm's choice on one instance, scored against ground truth.
type ArmResult struct {
	Arm      string
	Chosen   Slot
	Feasible bool
	Score    int     // soft score if feasible, else 0
	OptScore int     // the instance's global optimum score
	Norm     float64 // Score/OptScore when feasible, else 0
}

// score evaluates a chosen slot against the instance's ground truth.
func score(in Instance, arm string, chosen Slot) ArmResult {
	_, optScore, _ := in.GlobalOpt()
	feasible := in.IsFeasible(chosen)
	sc := 0
	norm := 0.0
	if feasible {
		sc = in.SoftScore(chosen)
		if optScore > 0 {
			norm = float64(sc) / float64(optScore)
		}
	}
	return ArmResult{Arm: arm, Chosen: chosen, Feasible: feasible, Score: sc, OptScore: optScore, Norm: norm}
}

func sortedSlots(s []Slot) []Slot {
	out := append([]Slot{}, s...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// armPlurality picks the most-proposed slot; ties break to the lowest index.
func armPlurality(in Instance) Slot {
	counts := map[Slot]int{}
	for _, p := range in.Proposals() {
		counts[p]++
	}
	best, bestN := Slot(-1), -1
	for _, k := range sortedSlots(in.Proposals()) {
		if counts[k] > bestN {
			best, bestN = k, counts[k]
		}
	}
	return best
}

// armRandomDictator returns a uniformly random agent's proposal (seeded).
func armRandomDictator(in Instance, rng *rand.Rand) Slot {
	props := in.Proposals()
	return props[rng.Intn(len(props))]
}

// armOracleBestProposal is the SELECTION CEILING: the best feasible slot anyone
// actually proposed. No procedure that only chooses among proposals can beat
// it. By construction (see qualifies) it scores strictly below the global
// optimum — the gap only a synthesised option can close.
func armOracleBestProposal(in Instance) Slot {
	best, bestScore := Slot(-1), -1
	for _, p := range sortedSlots(in.Proposals()) {
		if in.IsFeasible(p) && in.SoftScore(p) > bestScore {
			best, bestScore = p, in.SoftScore(p)
		}
	}
	if best == -1 {
		return sortedSlots(in.Proposals())[0] // no feasible proposal; ceiling still fails
	}
	return best
}

// RunDeterministic evaluates every key-free arm on one instance.
func RunDeterministic(in Instance, rng *rand.Rand) []ArmResult {
	var out []ArmResult
	for i := range in.Agents {
		out = append(out, score(in, "solo:"+in.Agents[i].Name, in.Proposal(i)))
	}
	out = append(out, score(in, "plurality", armPlurality(in)))
	out = append(out, score(in, "random-dictator", armRandomDictator(in, rng)))
	out = append(out, score(in, "oracle:best-proposal", armOracleBestProposal(in)))
	// global optimum: the synthesis target / upper bound (norm = 1 by definition)
	opt, _, _ := in.GlobalOpt()
	out = append(out, score(in, "global-opt (ceiling)", opt))
	return out
}

// Aggregate is a per-arm summary across the suite.
type Aggregate struct {
	Arm          string
	N            int
	FeasibleRate float64
	MeanNorm     float64 // mean of per-instance Norm (infeasible counts as 0)
	MeanScore    float64
}

// Summarize rolls per-instance results up per arm, preserving first-seen order.
func Summarize(all [][]ArmResult) []Aggregate {
	order := []string{}
	acc := map[string]*Aggregate{}
	for _, inst := range all {
		for _, r := range inst {
			a, ok := acc[r.Arm]
			if !ok {
				a = &Aggregate{Arm: r.Arm}
				acc[r.Arm] = a
				order = append(order, r.Arm)
			}
			a.N++
			if r.Feasible {
				a.FeasibleRate++
			}
			a.MeanNorm += r.Norm
			a.MeanScore += float64(r.Score)
		}
	}
	out := make([]Aggregate, 0, len(order))
	for _, name := range order {
		a := acc[name]
		if a.N > 0 {
			a.FeasibleRate /= float64(a.N)
			a.MeanNorm /= float64(a.N)
			a.MeanScore /= float64(a.N)
		}
		out = append(out, *a)
	}
	return out
}
