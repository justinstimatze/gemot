package main

import (
	"math/rand"
	"strings"
	"testing"
)

// TestGenerateGapProperty is the central invariant: every generated instance
// must have a feasible optimum that no agent proposed, and the selection
// ceiling (best feasible proposal) must score strictly below that optimum.
// If this ever fails, selection could reach the optimum and the experiment
// would be measuring nothing.
func TestGenerateGapProperty(t *testing.T) {
	suite := Generate(50, 2026, 3, 5, 4)
	if len(suite) != 50 {
		t.Fatalf("got %d instances, want 50", len(suite))
	}
	for _, in := range suite {
		opt, optScore, ok := in.GlobalOpt()
		if !ok {
			t.Fatalf("instance %d has no feasible slot", in.ID)
		}
		for i, p := range in.Proposals() {
			if p == opt {
				t.Fatalf("instance %d: agent %d proposed the optimum %s — no synthesis gap", in.ID, i, in.Label(opt))
			}
		}
		ceiling := score(in, "oracle", armOracleBestProposal(in))
		if ceiling.Feasible && ceiling.Score >= optScore {
			t.Fatalf("instance %d: selection ceiling %d >= optimum %d — no gap", in.ID, ceiling.Score, optScore)
		}
		// The interesting claim, stated positively:
		if ceiling.Norm >= 1.0 {
			t.Fatalf("instance %d: oracle norm %.2f — selection already optimal", in.ID, ceiling.Norm)
		}
	}
}

func TestCheckerFeasibilityAndOpt(t *testing.T) {
	// 1 day, 3 slots. A blocks slot0; B blocks slot2. Only slot1 works for both.
	in := Instance{
		ID: 0, Days: 1, PerDay: 3,
		Agents: []Agent{
			{Name: "A", Blocked: map[Slot]bool{0: true}, Pref: map[Slot]int{1: 2, 2: 1}},
			{Name: "B", Blocked: map[Slot]bool{2: true}, Pref: map[Slot]int{1: 3, 0: 1}},
		},
	}
	if in.IsFeasible(0) || in.IsFeasible(2) {
		t.Error("slots 0 and 2 should be infeasible")
	}
	if !in.IsFeasible(1) {
		t.Error("slot 1 should be feasible for both")
	}
	if got := in.SoftScore(1); got != 5 {
		t.Errorf("SoftScore(1) = %d, want 5", got)
	}
	opt, sc, ok := in.GlobalOpt()
	if !ok || opt != 1 || sc != 5 {
		t.Errorf("GlobalOpt = (%v, %d, %v), want (1, 5, true)", opt, sc, ok)
	}
}

func TestRenderPositionSurfacesConstraints(t *testing.T) {
	suite := Generate(1, 99, 3, 5, 4)
	in := suite[0]
	pos := in.RenderPosition(0)
	if !strings.Contains(pos, "proposal") {
		t.Errorf("position should state a proposal: %q", pos)
	}
	if !strings.Contains(pos, "cannot meet") && !strings.Contains(pos, "no hard conflicts") {
		t.Errorf("position should state hard constraints: %q", pos)
	}
}

func TestGenerateDeterministic(t *testing.T) {
	a := Generate(10, 4242, 3, 5, 4)
	b := Generate(10, 4242, 3, 5, 4)
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		pa, pb := a[i].Proposals(), b[i].Proposals()
		for j := range pa {
			if pa[j] != pb[j] {
				t.Fatalf("instance %d agent %d differs across identical seeds", i, j)
			}
		}
	}
}

func TestRandomDictatorPicksAProposal(t *testing.T) {
	in := Generate(1, 1, 3, 5, 4)[0]
	rng := rand.New(rand.NewSource(1))
	chosen := armRandomDictator(in, rng)
	found := false
	for _, p := range in.Proposals() {
		if p == chosen {
			found = true
		}
	}
	if !found {
		t.Errorf("random dictator chose %v which is not a proposal", chosen)
	}
}
