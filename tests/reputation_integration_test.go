package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/reputation"
	"github.com/justinstimatze/gemot/types"
)

// TestReputationStoreRoundTripPostgres exercises the batched UPSERT
// paths against a real Postgres schema. This is the integration test
// the adversarial panel flagged — unit tests use a fake store and so
// cannot validate the SQL in internal/store/reputation.go against the
// actual schema.sql definitions.
//
// Covers: IncrementSurvivedCounts batch, AccumulateTrustEdges batch,
// PersistEigenTrustScores batch, LoadReputation roundtrip,
// LoadTrustEdges roundtrip.
func TestReputationStoreRoundTripPostgres(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	agents := []string{"alice", "bob", "carol"}
	if err := db.IncrementSurvivedCounts(ctx, agents); err != nil {
		t.Fatalf("IncrementSurvivedCounts (insert): %v", err)
	}
	reps, err := db.LoadReputation(ctx, agents)
	if err != nil {
		t.Fatalf("LoadReputation: %v", err)
	}
	for _, a := range agents {
		if reps[a].SurvivedCount != 1 {
			t.Fatalf("%s survived_count=%d, want 1", a, reps[a].SurvivedCount)
		}
	}
	// Second call — ON CONFLICT DO UPDATE path.
	if err := db.IncrementSurvivedCounts(ctx, agents); err != nil {
		t.Fatalf("IncrementSurvivedCounts (update): %v", err)
	}
	reps, _ = db.LoadReputation(ctx, agents)
	for _, a := range agents {
		if reps[a].SurvivedCount != 2 {
			t.Fatalf("%s survived_count after 2nd increment=%d, want 2", a, reps[a].SurvivedCount)
		}
	}

	edges := []analysis.Edge{
		{From: "bob", To: "alice", Weight: 1},
		{From: "carol", To: "alice", Weight: 1},
		{From: "alice", To: "carol", Weight: 0.5},
	}
	if err := db.AccumulateTrustEdges(ctx, edges); err != nil {
		t.Fatalf("AccumulateTrustEdges (insert): %v", err)
	}
	// Accumulate the same edges again — should double the weights.
	if err := db.AccumulateTrustEdges(ctx, edges); err != nil {
		t.Fatalf("AccumulateTrustEdges (update): %v", err)
	}
	loaded, err := db.LoadTrustEdges(ctx)
	if err != nil {
		t.Fatalf("LoadTrustEdges: %v", err)
	}
	byPair := map[[2]string]float64{}
	for _, e := range loaded {
		byPair[[2]string{e.From, e.To}] = e.Weight
	}
	if w := byPair[[2]string{"bob", "alice"}]; w != 2 {
		t.Fatalf("bob→alice weight=%f after two accumulates, want 2", w)
	}
	if w := byPair[[2]string{"alice", "carol"}]; w != 1 {
		t.Fatalf("alice→carol weight=%f after two accumulates of 0.5, want 1", w)
	}

	scores := map[string]float64{"alice": 0.7, "bob": 0.2, "carol": 0.1}
	if err := db.PersistEigenTrustScores(ctx, scores); err != nil {
		t.Fatalf("PersistEigenTrustScores (insert): %v", err)
	}
	// Re-persist with different values — should overwrite, not add.
	scores2 := map[string]float64{"alice": 0.4, "bob": 0.4, "carol": 0.2}
	if err := db.PersistEigenTrustScores(ctx, scores2); err != nil {
		t.Fatalf("PersistEigenTrustScores (update): %v", err)
	}
	reps, _ = db.LoadReputation(ctx, agents)
	if reps["alice"].Score != 0.4 {
		t.Fatalf("alice score=%f, want 0.4 (overwrite)", reps["alice"].Score)
	}
	if reps["alice"].SurvivedCount != 2 {
		t.Fatalf("survived_count must be preserved across score upserts; got %d",
			reps["alice"].SurvivedCount)
	}
}

// TestReputationEndToEndRoundFlow exercises the full
// Weigher.UpdateFromRound → Weigher.WeightsFor path against Postgres.
// This is the integration scenario that pure unit tests miss: updates
// from one round must correctly influence WeightsFor output in the
// next. Replicates the production flow in
// internal/deliberation/service.go.
func TestReputationEndToEndRoundFlow(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	w := reputation.NewWeigher(db, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 2, // lower threshold to exercise graduation within the test
		Iterations:    50,
	})
	if w == nil {
		t.Fatal("weigher should be non-nil when enabled")
	}

	// Round 1: two agreers on one author's position. The author gets
	// a survived_count increment (meets minDistinctAgreers=2). Other
	// agents get edges out but no survived increment.
	round1 := []types.Crux{
		{SourcePositionIDs: []string{"p-alice-r1"}, AgreeAgents: []string{"bob", "carol"}},
	}
	authors1 := map[string]string{"p-alice-r1": "alice"}
	if err := w.UpdateFromRound(ctx, round1, authors1); err != nil {
		t.Fatalf("round 1 UpdateFromRound: %v", err)
	}

	// Cohort weights after round 1. Nobody has graduated (threshold=2,
	// alice has survived_count=1).
	agents := []string{"alice", "bob", "carol"}
	weights := w.WeightsFor(ctx, agents)
	for _, a := range agents {
		if weights[a] != 0.1 {
			t.Fatalf("round 1: %s should be cold-capped at 0.1, got %f", a, weights[a])
		}
	}

	// Round 2: alice's new position survives again with two agreers.
	// survived_count reaches 2 → meets the threshold, graduation.
	round2 := []types.Crux{
		{SourcePositionIDs: []string{"p-alice-r2"}, AgreeAgents: []string{"bob", "carol"}},
	}
	authors2 := map[string]string{"p-alice-r2": "alice"}
	if err := w.UpdateFromRound(ctx, round2, authors2); err != nil {
		t.Fatalf("round 2 UpdateFromRound: %v", err)
	}

	// After round 2: alice has survived_count=2 (graduated). bob and
	// carol are still cold. Alice's weight should exceed 0.1.
	weights = w.WeightsFor(ctx, agents)
	if weights["alice"] <= 0.1 {
		t.Fatalf("round 2: alice should be graduated (weight > 0.1), got %f", weights["alice"])
	}
	if weights["bob"] != 0.1 || weights["carol"] != 0.1 {
		t.Fatalf("round 2: bob/carol should remain cold-capped, got bob=%f carol=%f",
			weights["bob"], weights["carol"])
	}

	// Verify the underlying persisted state matches expectations.
	reps, err := db.LoadReputation(ctx, agents)
	if err != nil {
		t.Fatalf("LoadReputation: %v", err)
	}
	if reps["alice"].SurvivedCount != 2 {
		t.Fatalf("alice survived_count=%d, want 2", reps["alice"].SurvivedCount)
	}
	if reps["alice"].Score <= 0 {
		t.Fatalf("alice should have positive eigenvector score, got %f", reps["alice"].Score)
	}
}
