package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/reputation"
	"github.com/justinstimatze/gemot/types"
)

// TestPrivateDelibEmitsScopedEdges: a private deliberation running
// UpdateFromRound writes into agent_trust_edges with deliberation_id
// set to the delib's ID, NOT into the global partition (”).
// This is the core P4 FULL partitioning invariant — the next test
// (TestPublicDelibEmitsGlobalEdges) verifies the mirror case.
func TestPrivateDelibEmitsScopedEdges(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()
	svc := deliberation.NewService(db, repAnalyzer{})
	w := reputation.NewWeigher(db, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    20,
	})
	svc.SetReputationUpdater(w)

	d, err := svc.CreateDeliberation(ctx, "scoped edges", "private",
		deliberation.WithVisibility("private"),
		deliberation.WithCreatorKey("test-creator"),
	)
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}

	agents := []string{"alice", "bob", "carol"}
	var positionIDs []string
	for _, a := range agents {
		p, err := svc.SubmitPosition(ctx, d.ID, a, "position content from "+a)
		if err != nil {
			t.Fatalf("SubmitPosition %s: %v", a, err)
		}
		positionIDs = append(positionIDs, p.ID)
	}
	for _, voter := range agents {
		for _, pid := range positionIDs {
			if err := svc.Vote(ctx, d.ID, voter, pid, 1, "", ""); err != nil {
				t.Fatalf("Vote %s: %v", voter, err)
			}
		}
	}
	if _, err := svc.Analyze(ctx, d.ID); err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	var scoped, global int
	if err := db.RawDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_trust_edges WHERE deliberation_id = $1`, d.ID).Scan(&scoped); err != nil {
		t.Fatalf("count scoped: %v", err)
	}
	if scoped == 0 {
		t.Fatalf("private delib emitted 0 scoped edges; per-delib graph empty")
	}
	if err := db.RawDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_trust_edges WHERE deliberation_id = ''`).Scan(&global); err != nil {
		t.Fatalf("count global: %v", err)
	}
	if global != 0 {
		t.Fatalf("private delib leaked %d edges into global partition", global)
	}
}

// TestPublicDelibEmitsGlobalEdges: regression — public (open)
// deliberations still land in the global partition. Without this the
// P4 FULL partitioning could silently break the global eigenvector.
func TestPublicDelibEmitsGlobalEdges(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()
	svc := deliberation.NewService(db, repAnalyzer{})
	w := reputation.NewWeigher(db, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    20,
	})
	svc.SetReputationUpdater(w)

	d, err := svc.CreateDeliberation(ctx, "public edges", "public")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}
	agents := []string{"alice", "bob", "carol"}
	var positionIDs []string
	for _, a := range agents {
		p, err := svc.SubmitPosition(ctx, d.ID, a, "public content from "+a)
		if err != nil {
			t.Fatalf("SubmitPosition %s: %v", a, err)
		}
		positionIDs = append(positionIDs, p.ID)
	}
	for _, voter := range agents {
		for _, pid := range positionIDs {
			if err := svc.Vote(ctx, d.ID, voter, pid, 1, "", ""); err != nil {
				t.Fatalf("Vote %s: %v", voter, err)
			}
		}
	}
	if _, err := svc.Analyze(ctx, d.ID); err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	var global, scoped int
	if err := db.RawDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_trust_edges WHERE deliberation_id = ''`).Scan(&global); err != nil {
		t.Fatalf("count global: %v", err)
	}
	if global == 0 {
		t.Fatalf("public delib emitted no edges into global partition")
	}
	if err := db.RawDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_trust_edges WHERE deliberation_id = $1`, d.ID).Scan(&scoped); err != nil {
		t.Fatalf("count scoped: %v", err)
	}
	if scoped != 0 {
		t.Fatalf("public delib wrote %d edges into scoped partition (should be in global)", scoped)
	}
}

// TestPrivateDelibEigenTrustIsolated: two private delibs A and B with
// the same cohort but disjoint edge graphs. Edges emitted under delib
// A must not leak into delib B's per-delib eigenvector, and vice
// versa. Verifies LoadTrustEdges(ctx, A) returns only A's + global
// edges, not B's.
func TestPrivateDelibEigenTrustIsolated(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	// Write directly via store to control the edge content precisely —
	// we need A to have a (bob → alice) edge and B to have a (carol →
	// dave) edge, which we couldn't trigger cleanly through a mock
	// analyzer without a richer fixture.
	if err := db.AccumulateTrustEdges(ctx, []analysis.Edge{
		{From: "bob", To: "alice", Weight: 1.0},
	}, 0, "delib-A"); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if err := db.AccumulateTrustEdges(ctx, []analysis.Edge{
		{From: "carol", To: "dave", Weight: 1.0},
	}, 0, "delib-B"); err != nil {
		t.Fatalf("seed B: %v", err)
	}
	// A global edge visible to both (frank → grace).
	if err := db.AccumulateTrustEdges(ctx, []analysis.Edge{
		{From: "frank", To: "grace", Weight: 1.0},
	}, 0, ""); err != nil {
		t.Fatalf("seed global: %v", err)
	}

	aEdges, err := db.LoadTrustEdges(ctx, "delib-A")
	if err != nil {
		t.Fatalf("LoadTrustEdges A: %v", err)
	}
	have := map[[2]string]bool{}
	for _, e := range aEdges {
		have[[2]string{e.From, e.To}] = true
	}
	if !have[[2]string{"bob", "alice"}] {
		t.Fatalf("delib A should see its own edge (bob → alice)")
	}
	if !have[[2]string{"frank", "grace"}] {
		t.Fatalf("delib A should see the global edge (frank → grace)")
	}
	if have[[2]string{"carol", "dave"}] {
		t.Fatalf("delib A leaked delib B's edge (carol → dave)")
	}

	bEdges, err := db.LoadTrustEdges(ctx, "delib-B")
	if err != nil {
		t.Fatalf("LoadTrustEdges B: %v", err)
	}
	haveB := map[[2]string]bool{}
	for _, e := range bEdges {
		haveB[[2]string{e.From, e.To}] = true
	}
	if !haveB[[2]string{"carol", "dave"}] {
		t.Fatalf("delib B should see its own edge (carol → dave)")
	}
	if !haveB[[2]string{"frank", "grace"}] {
		t.Fatalf("delib B should see the global edge (frank → grace)")
	}
	if haveB[[2]string{"bob", "alice"}] {
		t.Fatalf("delib B leaked delib A's edge (bob → alice)")
	}

	// Global load excludes both scoped partitions.
	gEdges, _ := db.LoadTrustEdges(ctx, "")
	haveG := map[[2]string]bool{}
	for _, e := range gEdges {
		haveG[[2]string{e.From, e.To}] = true
	}
	if !haveG[[2]string{"frank", "grace"}] {
		t.Fatalf("global load should include global edge")
	}
	if haveG[[2]string{"bob", "alice"}] || haveG[[2]string{"carol", "dave"}] {
		t.Fatalf("global load leaked a scoped edge")
	}
}

// TestPrivateDelibInheritsGlobalReputation: a seasoned agent (high
// survived_count from past public rounds) entering a private delib
// keeps their cold-start graduation. A fresh agent in the same
// private delib is cold-capped. This is the cold-cap × per-delib
// EigenTrust interaction that defends against the private-ring
// Sybil graduation attack.
func TestPrivateDelibInheritsGlobalReputation(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	// Seed: "seasoned" is graduated (survived_count >= threshold).
	// "fresh" has no row (cold-start state).
	if err := db.IncrementSurvivedCounts(ctx, []string{idV("seasoned")}); err != nil {
		t.Fatalf("seed survived: %v", err)
	}
	for i := 0; i < 4; i++ {
		if err := db.IncrementSurvivedCounts(ctx, []string{idV("seasoned")}); err != nil {
			t.Fatalf("seed survived loop: %v", err)
		}
	}

	w := reputation.NewWeigher(db, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    20,
	})

	privCtx := types.WithDelibContext(ctx, "private-ring-delib", true)
	weights, err := w.WeightsFor(privCtx, []string{"seasoned", "fresh"}, nil)
	if err != nil {
		t.Fatalf("WeightsFor: %v", err)
	}
	if weights["fresh"] != 0.1 {
		t.Fatalf("fresh agent in private delib must be cold-capped; got %f", weights["fresh"])
	}
	if weights["seasoned"] <= 0.1 {
		t.Fatalf("seasoned agent in private delib must exceed cold cap; got %f", weights["seasoned"])
	}
}
