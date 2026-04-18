package tests

import (
	"context"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/analysis"
)

// TestDecayPrunesBelowFloor: after decay, edges whose weight dropped
// below the floor are DELETEd. The decay UPDATE runs first, then the
// DELETE runs against the decayed weights. Seed a stale edge with
// starting weight 0.02 so that 14 days at halfLife=7 decays it to
// ~0.005 — below a 0.01 floor. Also seed a heavier stale edge that
// decays to ~0.25, above the floor, which must survive. Exercises both
// branches in one test.
func TestDecayPrunesBelowFloor(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO agent_trust_edges (from_agent, to_agent, weight, last_updated)
		 VALUES ('tiny-src', 'tiny-dst', 0.02, NOW() - INTERVAL '14 days'),
		        ('heavy-src', 'heavy-dst', 1.0, NOW() - INTERVAL '14 days')`,
	); err != nil {
		t.Fatalf("seed stale edges: %v", err)
	}

	if err := db.DecayTrustEdges(ctx, 7*24*time.Hour, 0.01); err != nil {
		t.Fatalf("DecayTrustEdges: %v", err)
	}

	loaded, err := db.LoadTrustEdges(ctx)
	if err != nil {
		t.Fatalf("LoadTrustEdges: %v", err)
	}
	byPair := map[[2]string]float64{}
	for _, e := range loaded {
		byPair[[2]string{e.From, e.To}] = e.Weight
	}

	if _, ok := byPair[[2]string{"tiny-src", "tiny-dst"}]; ok {
		t.Fatalf("edge 0.02 → 0.005 should be pruned by floor=0.01; still present")
	}
	heavyW, ok := byPair[[2]string{"heavy-src", "heavy-dst"}]
	if !ok {
		t.Fatalf("edge 1.0 → ~0.25 should survive floor=0.01; got pruned")
	}
	if heavyW < 0.24 || heavyW > 0.26 {
		t.Fatalf("heavy edge decayed to unexpected weight %f", heavyW)
	}
}

// TestDecayFloorZeroDisablesPruning: floor=0 leaves rows even at
// arbitrarily tiny weights. Regression defense — the legacy
// cumulative-forever semantics must remain available when retention is
// opt-out.
func TestDecayFloorZeroDisablesPruning(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO agent_trust_edges (from_agent, to_agent, weight, last_updated)
		 VALUES ('x', 'y', 0.001, NOW() - INTERVAL '14 days')`,
	); err != nil {
		t.Fatalf("seed tiny edge: %v", err)
	}

	if err := db.DecayTrustEdges(ctx, 7*24*time.Hour, 0); err != nil {
		t.Fatalf("DecayTrustEdges: %v", err)
	}

	loaded, err := db.LoadTrustEdges(ctx)
	if err != nil {
		t.Fatalf("LoadTrustEdges: %v", err)
	}
	found := false
	for _, e := range loaded {
		if e.From == "x" && e.To == "y" {
			found = true
			if e.Weight > 0.001 {
				t.Fatalf("edge should have decayed but not pruned; weight=%f", e.Weight)
			}
		}
	}
	if !found {
		t.Fatalf("edge pruned under floor=0; retention opt-out broken")
	}
}

// TestAccumulateCapClamps: 20 consecutive +1 endorsements end at
// weight = cap (10.0), not 20.0. Exercises the LEAST(...) branch in
// both INSERT and ON CONFLICT DO UPDATE paths.
func TestAccumulateCapClamps(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	edge := []analysis.Edge{{From: "a", To: "b", Weight: 1.0}}
	for i := 0; i < 20; i++ {
		if err := db.AccumulateTrustEdges(ctx, edge, 10.0); err != nil {
			t.Fatalf("AccumulateTrustEdges iter %d: %v", i, err)
		}
	}

	loaded, err := db.LoadTrustEdges(ctx)
	if err != nil {
		t.Fatalf("LoadTrustEdges: %v", err)
	}
	var w float64
	for _, e := range loaded {
		if e.From == "a" && e.To == "b" {
			w = e.Weight
		}
	}
	if w != 10.0 {
		t.Fatalf("20 endorsements with cap=10 should clamp at 10.0; got %f", w)
	}
}

// TestAccumulateCapZeroDisabled: cap=0 leaves cumulative weight
// uncapped. Regression defense — pre-P3 semantics must remain available.
func TestAccumulateCapZeroDisabled(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	edge := []analysis.Edge{{From: "a", To: "b", Weight: 1.0}}
	for i := 0; i < 15; i++ {
		if err := db.AccumulateTrustEdges(ctx, edge, 0); err != nil {
			t.Fatalf("AccumulateTrustEdges iter %d: %v", i, err)
		}
	}

	loaded, err := db.LoadTrustEdges(ctx)
	if err != nil {
		t.Fatalf("LoadTrustEdges: %v", err)
	}
	var w float64
	for _, e := range loaded {
		if e.From == "a" && e.To == "b" {
			w = e.Weight
		}
	}
	if w != 15.0 {
		t.Fatalf("15 endorsements with cap=0 should sum to 15.0; got %f", w)
	}
}

// TestDisputeEdgesIgnoreCap: disputes may push weight arbitrarily
// negative regardless of the accumulate-side cap. Disputes use
// ApplyDisputeEdges which has no cap parameter — encoding the design
// decision that disputes are an unbounded negative signal.
func TestDisputeEdgesIgnoreCap(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	// Seed a capped endorsement at 10.0.
	endorse := []analysis.Edge{{From: "a", To: "b", Weight: 1.0}}
	for i := 0; i < 20; i++ {
		if err := db.AccumulateTrustEdges(ctx, endorse, 10.0); err != nil {
			t.Fatalf("accumulate iter %d: %v", i, err)
		}
	}

	// Pile 50 disputes of weight 1.0 each. Final weight should be 10 - 50 = -40.
	dispute := []analysis.Edge{{From: "a", To: "b", Weight: 1.0}}
	for i := 0; i < 50; i++ {
		if err := db.ApplyDisputeEdges(ctx, dispute); err != nil {
			t.Fatalf("dispute iter %d: %v", i, err)
		}
	}

	loaded, err := db.LoadTrustEdges(ctx)
	if err != nil {
		t.Fatalf("LoadTrustEdges: %v", err)
	}
	var w float64
	for _, e := range loaded {
		if e.From == "a" && e.To == "b" {
			w = e.Weight
		}
	}
	if w != -40.0 {
		t.Fatalf("disputes should accumulate arbitrarily negative; expected -40.0, got %f", w)
	}
}
