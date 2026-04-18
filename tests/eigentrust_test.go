package tests

import (
	"math"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
)

// sumScores is a convenience for the probability-mass invariant.
func sumScores(s map[string]float64) float64 {
	total := 0.0
	for _, v := range s {
		total += v
	}
	return total
}

// TestEigenTrustEmpty: no agents, no edges → empty result.
func TestEigenTrustEmpty(t *testing.T) {
	s := analysis.EigenTrust(nil, nil, analysis.EigenTrustConfig{})
	if len(s) != 0 {
		t.Fatalf("expected empty scores, got %v", s)
	}
}

// TestEigenTrustUniformNoEdges: with only an agent set and no edges,
// every agent gets uniform share and scores sum to 1.
func TestEigenTrustUniformNoEdges(t *testing.T) {
	agents := []string{"a", "b", "c", "d"}
	s := analysis.EigenTrust(nil, agents, analysis.EigenTrustConfig{})
	if math.Abs(sumScores(s)-1.0) > 1e-9 {
		t.Fatalf("scores must sum to 1, got %f", sumScores(s))
	}
	for _, a := range agents {
		if math.Abs(s[a]-0.25) > 1e-9 {
			t.Fatalf("expected uniform 0.25 for %s, got %f", a, s[a])
		}
	}
}

// TestEigenTrustConvergesOnStar: in a star graph where every peripheral
// agent trusts only the hub, the hub converges to strictly higher score
// than any peripheral, and scores sum to 1.
func TestEigenTrustConvergesOnStar(t *testing.T) {
	agents := []string{"hub", "p1", "p2", "p3", "p4"}
	edges := []analysis.Edge{
		{From: "p1", To: "hub", Weight: 1},
		{From: "p2", To: "hub", Weight: 1},
		{From: "p3", To: "hub", Weight: 1},
		{From: "p4", To: "hub", Weight: 1},
	}
	s := analysis.EigenTrust(edges, agents, analysis.EigenTrustConfig{})
	if math.Abs(sumScores(s)-1.0) > 1e-6 {
		t.Fatalf("scores must sum to ~1, got %f", sumScores(s))
	}
	if s["hub"] <= s["p1"] {
		t.Fatalf("hub score %f must exceed peripheral %f", s["hub"], s["p1"])
	}
	for _, p := range []string{"p1", "p2", "p3", "p4"} {
		if math.Abs(s[p]-s["p1"]) > 1e-6 {
			t.Fatalf("peripherals must tie by symmetry; %s=%f p1=%f", p, s[p], s["p1"])
		}
	}
}

// TestEigenTrustSybilStarvedByPreTrust: a closed Sybil ring CAN pump
// its own score under canonical EigenTrust with a uniform teleport,
// but if pre-trust is seeded on known-legit agents only, the Sybil
// ring receives no teleport mass and its scores collapse toward zero.
// This is the paper's canonical defense against closed trust cycles.
//
// In production Gemot, the cold-start cap provides an additional layer
// of defense even without pre-trusted seeds: Sybils have zero survived
// validation history, so their effective weight is clamped regardless
// of EigenTrust score.
func TestEigenTrustSybilStarvedByPreTrust(t *testing.T) {
	agents := []string{"L1", "L2", "L3", "S1", "S2", "S3"}
	edges := []analysis.Edge{
		// Legit agents trust each other.
		{From: "L1", To: "L2", Weight: 1},
		{From: "L2", To: "L3", Weight: 1},
		{From: "L3", To: "L1", Weight: 1},
		// Closed Sybil ring — trusts nobody outside itself.
		{From: "S1", To: "S2", Weight: 1},
		{From: "S2", To: "S3", Weight: 1},
		{From: "S3", To: "S1", Weight: 1},
	}
	cfg := analysis.EigenTrustConfig{
		PreTrust: map[string]float64{"L1": 1, "L2": 1, "L3": 1},
	}
	s := analysis.EigenTrust(edges, agents, cfg)

	legitTotal := s["L1"] + s["L2"] + s["L3"]
	sybilTotal := s["S1"] + s["S2"] + s["S3"]
	if legitTotal <= sybilTotal {
		t.Fatalf("pre-trusted legit ring must dominate Sybil ring; legit=%f sybil=%f",
			legitTotal, sybilTotal)
	}
	for _, sID := range []string{"S1", "S2", "S3"} {
		for _, lID := range []string{"L1", "L2", "L3"} {
			if s[sID] >= s[lID] {
				t.Fatalf("Sybil %s=%f must not reach legit %s=%f", sID, s[sID], lID, s[lID])
			}
		}
	}
}

// TestEigenTrustPreTrustSeed: when a pre-trust seed is provided, the
// seeded vertices receive the teleport-boosted share; other vertices
// get no pre-trust mass but can still accumulate via edges.
func TestEigenTrustPreTrustSeed(t *testing.T) {
	agents := []string{"seed", "other1", "other2"}
	s := analysis.EigenTrust(nil, agents, analysis.EigenTrustConfig{
		PreTrust: map[string]float64{"seed": 1.0},
	})
	if s["seed"] <= s["other1"] {
		t.Fatalf("seeded agent %f must beat non-seeded %f", s["seed"], s["other1"])
	}
}

// TestEigenTrustSinkDoesNotLeakMass: a pure sink (receives trust but
// emits none) must not cause total mass to collapse. The leaked-rank
// correction redistributes sink mass through the teleport vector each
// iteration, so scores still sum to 1.
func TestEigenTrustSinkDoesNotLeakMass(t *testing.T) {
	agents := []string{"sink", "a", "b"}
	edges := []analysis.Edge{
		{From: "a", To: "sink", Weight: 1},
		{From: "b", To: "sink", Weight: 1},
	}
	s := analysis.EigenTrust(edges, agents, analysis.EigenTrustConfig{})
	if math.Abs(sumScores(s)-1.0) > 1e-6 {
		t.Fatalf("sink must not leak mass; sum=%f", sumScores(s))
	}
}
