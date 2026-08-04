package tests

import (
	"context"
	"testing"
	"time"

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

	// Write-side methods take pre-resolved vertex-form strings (the
	// Weigher emits in this format after v4 schema). Read-side
	// LoadReputation takes symbolic agent_ids and internally resolves
	// via agent_keys — the three agents here have no registered keys so
	// they all resolve to "id:<name>".
	symbolic := []string{"alice", "bob", "carol"}
	vertices := []string{idV("alice"), idV("bob"), idV("carol")}

	if err := db.IncrementSurvivedCounts(ctx, vertices); err != nil {
		t.Fatalf("IncrementSurvivedCounts (insert): %v", err)
	}
	reps, err := db.LoadReputation(ctx, symbolic)
	if err != nil {
		t.Fatalf("LoadReputation: %v", err)
	}
	for _, a := range symbolic {
		if reps[a].SurvivedCount != 1 {
			t.Fatalf("%s survived_count=%d, want 1", a, reps[a].SurvivedCount)
		}
	}
	// Second call — ON CONFLICT DO UPDATE path.
	if err := db.IncrementSurvivedCounts(ctx, vertices); err != nil {
		t.Fatalf("IncrementSurvivedCounts (update): %v", err)
	}
	reps, _ = db.LoadReputation(ctx, symbolic)
	for _, a := range symbolic {
		if reps[a].SurvivedCount != 2 {
			t.Fatalf("%s survived_count after 2nd increment=%d, want 2", a, reps[a].SurvivedCount)
		}
	}

	edges := []analysis.Edge{
		{From: idV("bob"), To: idV("alice"), Weight: 1},
		{From: idV("carol"), To: idV("alice"), Weight: 1},
		{From: idV("alice"), To: idV("carol"), Weight: 0.5},
	}
	if err := db.AccumulateTrustEdges(ctx, edges, 0, ""); err != nil {
		t.Fatalf("AccumulateTrustEdges (insert): %v", err)
	}
	// Accumulate the same edges again — should double the weights.
	if err := db.AccumulateTrustEdges(ctx, edges, 0, ""); err != nil {
		t.Fatalf("AccumulateTrustEdges (update): %v", err)
	}
	loaded, err := db.LoadTrustEdges(ctx, "")
	if err != nil {
		t.Fatalf("LoadTrustEdges: %v", err)
	}
	byPair := map[[2]string]float64{}
	for _, e := range loaded {
		byPair[[2]string{e.From, e.To}] = e.Weight
	}
	if w := byPair[[2]string{idV("bob"), idV("alice")}]; w != 2 {
		t.Fatalf("bob→alice weight=%f after two accumulates, want 2", w)
	}
	if w := byPair[[2]string{idV("alice"), idV("carol")}]; w != 1 {
		t.Fatalf("alice→carol weight=%f after two accumulates of 0.5, want 1", w)
	}

	scores := map[string]float64{idV("alice"): 0.7, idV("bob"): 0.2, idV("carol"): 0.1}
	if err := db.PersistEigenTrustScores(ctx, scores); err != nil {
		t.Fatalf("PersistEigenTrustScores (insert): %v", err)
	}
	// Re-persist with different values — should overwrite, not add.
	scores2 := map[string]float64{idV("alice"): 0.4, idV("bob"): 0.4, idV("carol"): 0.2}
	if err := db.PersistEigenTrustScores(ctx, scores2); err != nil {
		t.Fatalf("PersistEigenTrustScores (update): %v", err)
	}
	reps, _ = db.LoadReputation(ctx, symbolic)
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
	if err := w.UpdateFromRound(ctx, "", false, round1, authors1, nil, nil); err != nil {
		t.Fatalf("round 1 UpdateFromRound: %v", err)
	}

	// Cohort weights after round 1. Nobody has graduated (threshold=2,
	// alice has survived_count=1).
	agents := []string{"alice", "bob", "carol"}
	weights, err := w.WeightsFor(ctx, agents)
	if err != nil {
		t.Fatalf("round 1 WeightsFor: %v", err)
	}
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
	if err := w.UpdateFromRound(ctx, "", false, round2, authors2, nil, nil); err != nil {
		t.Fatalf("round 2 UpdateFromRound: %v", err)
	}

	// After round 2: alice has survived_count=2 (graduated). bob and
	// carol are still cold. Alice's weight should exceed 0.1.
	weights, err = w.WeightsFor(ctx, agents)
	if err != nil {
		t.Fatalf("round 2 WeightsFor: %v", err)
	}
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

// TestDecayTrustEdgesPostgres exercises the decay SQL against real
// Postgres. An edge aged 14 days at halfLife=7 days should decay to
// 0.25× its original weight (two half-lives). Edges fresher than 1
// hour are skipped by the WHERE clause — the test seeds a stale edge
// and a fresh edge so both branches are exercised in one UPDATE.
func TestDecayTrustEdgesPostgres(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	// Seed a fresh edge (will be skipped by the 1-hour-age guard).
	if err := db.AccumulateTrustEdges(ctx, []analysis.Edge{
		{From: "fresh-src", To: "fresh-dst", Weight: 1.0},
	}, 0, ""); err != nil {
		t.Fatalf("seed fresh edge: %v", err)
	}
	// Seed a stale edge by back-dating last_updated 14 days. Postgres
	// INTERVAL + raw SQL keeps the test hermetic (no wall-clock wait).
	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO agent_trust_edges (from_agent, to_agent, weight, last_updated) VALUES ('stale-src', 'stale-dst', 1.0, NOW() - INTERVAL '14 days')`,
	); err != nil {
		t.Fatalf("seed stale edge: %v", err)
	}

	// halfLife = 7 days. Stale edge aged 14d → 2 half-lives → factor 0.25.
	if err := db.DecayTrustEdges(ctx, 7*24*time.Hour, 0); err != nil {
		t.Fatalf("DecayTrustEdges: %v", err)
	}
	loaded, err := db.LoadTrustEdges(ctx, "")
	if err != nil {
		t.Fatalf("LoadTrustEdges: %v", err)
	}
	byPair := map[[2]string]float64{}
	for _, e := range loaded {
		byPair[[2]string{e.From, e.To}] = e.Weight
	}
	// Fresh edge untouched.
	if w := byPair[[2]string{"fresh-src", "fresh-dst"}]; w != 1.0 {
		t.Fatalf("fresh edge should not decay within 1h window; got weight=%f", w)
	}
	// Stale edge decayed. Allow small floating slack — the EXTRACT(EPOCH)
	// math won't land at exactly 0.25 due to the tiny time elapsed
	// during the test itself.
	staleW := byPair[[2]string{"stale-src", "stale-dst"}]
	if staleW < 0.24 || staleW > 0.26 {
		t.Fatalf("stale edge aged 14d at halfLife=7d should decay to ~0.25; got weight=%f", staleW)
	}
}

// TestApplyDisputeEdgesPostgres exercises the dispute-edge SQL path.
// A dispute against an agent who has no prior edge inserts a row with
// negative weight; a dispute against an agent with an existing
// positive edge subtracts. Exercises both the INSERT and ON CONFLICT
// DO UPDATE branches.
func TestApplyDisputeEdgesPostgres(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	// Seed a positive edge: bob endorsed alice.
	if err := db.AccumulateTrustEdges(ctx, []analysis.Edge{
		{From: "bob", To: "alice", Weight: 1.0},
	}, 0, ""); err != nil {
		t.Fatalf("seed endorsement: %v", err)
	}

	// Apply two disputes: carol→alice (new row, negative), bob→alice
	// (existing row, subtracts).
	if err := db.ApplyDisputeEdges(ctx, []analysis.Edge{
		{From: "carol", To: "alice", Weight: 0.5},
		{From: "bob", To: "alice", Weight: 0.4},
	}, ""); err != nil {
		t.Fatalf("ApplyDisputeEdges: %v", err)
	}

	loaded, err := db.LoadTrustEdges(ctx, "")
	if err != nil {
		t.Fatalf("LoadTrustEdges: %v", err)
	}
	byPair := map[[2]string]float64{}
	for _, e := range loaded {
		byPair[[2]string{e.From, e.To}] = e.Weight
	}
	// Existing edge: 1.0 - 0.4 = 0.6
	if w := byPair[[2]string{"bob", "alice"}]; w < 0.5999 || w > 0.6001 {
		t.Fatalf("bob→alice after dispute want 0.6, got %f", w)
	}
	// New negative row: -0.5
	if w := byPair[[2]string{"carol", "alice"}]; w < -0.5001 || w > -0.4999 {
		t.Fatalf("carol→alice after dispute want -0.5, got %f", w)
	}
}

// TestUnprocessedDisputesGatingPostgres verifies the idempotency path:
// GetUnprocessedDisputes returns only disputes with rep_processed_at
// IS NULL, and MarkDisputesProcessed flips the flag so subsequent
// rounds skip already-ingested disputes.
func TestUnprocessedDisputesGatingPostgres(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	// Create a deliberation so the FK on disputes is satisfied.
	_, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO deliberations (id, topic, round_number, status) VALUES ('d-test', 'decay test', 0, 'open')`,
	)
	if err != nil {
		t.Fatalf("seed deliberation: %v", err)
	}

	// Two disputes on the same deliberation.
	for _, id := range []string{"disp-1", "disp-2"} {
		if _, err := db.RawDB().ExecContext(ctx,
			`INSERT INTO disputes (id, deliberation_id, agent_id, crux_claim, correction, created_at) VALUES ($1, 'd-test', 'carol', 'some claim', 'why', NOW())`,
			id,
		); err != nil {
			t.Fatalf("seed dispute %s: %v", id, err)
		}
	}

	unprocessed, err := db.GetUnprocessedDisputes(ctx, "d-test")
	if err != nil {
		t.Fatalf("GetUnprocessedDisputes: %v", err)
	}
	if len(unprocessed) != 2 {
		t.Fatalf("expected 2 unprocessed, got %d", len(unprocessed))
	}

	// Mark one processed — subsequent query should return just the other.
	if err := db.MarkDisputesProcessed(ctx, []string{"disp-1"}); err != nil {
		t.Fatalf("MarkDisputesProcessed: %v", err)
	}
	unprocessed, _ = db.GetUnprocessedDisputes(ctx, "d-test")
	if len(unprocessed) != 1 || unprocessed[0].ID != "disp-2" {
		t.Fatalf("expected only disp-2 unprocessed, got %+v", unprocessed)
	}
}
