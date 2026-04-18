package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/reputation"
	"github.com/justinstimatze/gemot/internal/store"
	"github.com/justinstimatze/gemot/types"
)

// fakeReputationStore is an in-memory ReputationStore for unit tests.
// Mirrors the Postgres semantics: LoadReputation returns only rows that
// exist (missing agents are absent from the map, not present with
// zero values), AccumulateTrustEdges sums weights, PersistEigenTrustScores
// upserts.
type fakeReputationStore struct {
	reps   map[string]store.Reputation
	edges  map[[2]string]float64
	scores map[string]float64
}

func newFakeRepStore() *fakeReputationStore {
	return &fakeReputationStore{
		reps:   map[string]store.Reputation{},
		edges:  map[[2]string]float64{},
		scores: map[string]float64{},
	}
}

func (f *fakeReputationStore) LoadReputation(_ context.Context, agents []string) (map[string]store.Reputation, error) {
	out := map[string]store.Reputation{}
	for _, a := range agents {
		if r, ok := f.reps[a]; ok {
			out[a] = r
		}
	}
	return out, nil
}

func (f *fakeReputationStore) LoadTrustEdges(_ context.Context) ([]analysis.Edge, error) {
	out := make([]analysis.Edge, 0, len(f.edges))
	for k, w := range f.edges {
		out = append(out, analysis.Edge{From: k[0], To: k[1], Weight: w})
	}
	return out, nil
}

func (f *fakeReputationStore) AccumulateTrustEdges(_ context.Context, edges []analysis.Edge) error {
	for _, e := range edges {
		if e.Weight <= 0 {
			continue
		}
		f.edges[[2]string{e.From, e.To}] += e.Weight
	}
	return nil
}

func (f *fakeReputationStore) IncrementSurvivedCounts(_ context.Context, agents []string) error {
	for _, a := range agents {
		r := f.reps[a]
		r.AgentID = a
		r.SurvivedCount++
		f.reps[a] = r
	}
	return nil
}

func (f *fakeReputationStore) PersistEigenTrustScores(_ context.Context, scores map[string]float64) error {
	for a, s := range scores {
		f.scores[a] = s
		r := f.reps[a]
		r.AgentID = a
		r.Score = s
		f.reps[a] = r
	}
	return nil
}

// TestColdStartCapClampsNewAgents: agents below the cold-start threshold
// receive exactly the cold cap as their weight, regardless of score.
func TestColdStartCapClampsNewAgents(t *testing.T) {
	fs := newFakeRepStore()
	// Seed: "seasoned" has high score and graduated survived_count.
	// "newcomer" has the same score but zero survived_count.
	fs.reps["seasoned"] = store.Reputation{AgentID: "seasoned", Score: 0.5, SurvivedCount: 10}
	fs.reps["newcomer"] = store.Reputation{AgentID: "newcomer", Score: 0.5, SurvivedCount: 0}

	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    50,
	})
	weights := w.WeightsFor(context.Background(), []string{"seasoned", "newcomer"})

	if weights["newcomer"] != 0.1 {
		t.Fatalf("cold-start agent must receive exactly cold_cap=0.1, got %f", weights["newcomer"])
	}
	if weights["seasoned"] <= 0.1 {
		t.Fatalf("seasoned agent must receive > cold_cap, got %f", weights["seasoned"])
	}
	if weights["seasoned"] < weights["newcomer"] {
		t.Fatalf("seasoned weight (%f) must exceed newcomer weight (%f)",
			weights["seasoned"], weights["newcomer"])
	}
}

// TestGraduationIsMonotonicWithinCohort: within a single WeightsFor
// call, an agent who just crosses the cold-start threshold must not
// see their weight drop relative to a peer one increment behind. The
// [ColdCap, 1] scaling anchors the minimum at the cap so this is
// guaranteed by construction. Graduation is NOT globally monotone
// across calls — cohort composition shifts the normalization
// denominator.
func TestGraduationIsMonotonicWithinCohort(t *testing.T) {
	fs := newFakeRepStore()
	// Two agents both at survived_count = threshold - 1 and = threshold,
	// with the same score. The second graduated, the first did not.
	fs.reps["pre"] = store.Reputation{AgentID: "pre", Score: 0.0, SurvivedCount: 4}
	fs.reps["post"] = store.Reputation{AgentID: "post", Score: 0.0, SurvivedCount: 5}

	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    50,
	})
	weights := w.WeightsFor(context.Background(), []string{"pre", "post"})

	if weights["post"] < weights["pre"] {
		t.Fatalf("graduation must be non-decreasing: pre=%f post=%f",
			weights["pre"], weights["post"])
	}
}

// TestUpdateFromRoundAccumulatesEdgesAndSurvived: a round with two
// distinct non-author agreers on an author's surviving crux meets the
// minDistinctAgreers=2 threshold: survived_count increments and both
// agree edges land.
func TestUpdateFromRoundAccumulatesEdgesAndSurvived(t *testing.T) {
	fs := newFakeRepStore()
	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    50,
	})

	cruxes := []types.Crux{
		{
			SourcePositionIDs: []string{"p-alice"},
			AgreeAgents:       []string{"bob", "carol"},
		},
	}
	authors := map[string]string{
		"p-alice": "alice",
	}

	if err := w.UpdateFromRound(context.Background(), cruxes, authors); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}

	if fs.reps["alice"].SurvivedCount != 1 {
		t.Fatalf("alice survived_count=%d, want 1", fs.reps["alice"].SurvivedCount)
	}
	if fs.edges[[2]string{"bob", "alice"}] != 1 {
		t.Fatalf("expected bob→alice edge weight 1, got %f", fs.edges[[2]string{"bob", "alice"}])
	}
	if fs.edges[[2]string{"carol", "alice"}] != 1 {
		t.Fatalf("expected carol→alice edge weight 1, got %f", fs.edges[[2]string{"carol", "alice"}])
	}
	if _, ok := fs.edges[[2]string{"alice", "alice"}]; ok {
		t.Fatalf("unexpected self-endorsement edge")
	}
	if fs.scores["alice"] <= 0 {
		t.Fatalf("alice should have positive score after round, got %f", fs.scores["alice"])
	}
}

// TestUpdateFromRoundDedupAgreers: a duplicate AgreeAgents entry
// within a single crux must not double-count. A malformed upstream
// that lists "bob" twice should still produce exactly one bob→alice
// edge of weight 1.
func TestUpdateFromRoundDedupAgreers(t *testing.T) {
	fs := newFakeRepStore()
	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled: true, ColdCap: 0.1, ColdThreshold: 5, Iterations: 50,
	})
	cruxes := []types.Crux{
		{
			SourcePositionIDs: []string{"p-alice"},
			AgreeAgents:       []string{"bob", "bob", "carol"},
		},
	}
	authors := map[string]string{"p-alice": "alice"}

	if err := w.UpdateFromRound(context.Background(), cruxes, authors); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}
	if fs.edges[[2]string{"bob", "alice"}] != 1 {
		t.Fatalf("dup agreers must not double-count; bob→alice weight=%f, want 1",
			fs.edges[[2]string{"bob", "alice"}])
	}
}

// TestUpdateFromRoundBlocksSybilPair: the simplest Sybil attack is a
// 2-agent ring where A and B submit positions and agree with each
// other. With minDistinctAgreers=2, neither crosses the threshold
// because each has only ONE non-self agreer (the other Sybil). Both
// edges still land (they're informational), but survived_count does
// not increment for either. Rings of size N ≥ 3 are NOT blocked by
// this rule — documented in THREAT_MODEL.
func TestUpdateFromRoundBlocksSybilPair(t *testing.T) {
	fs := newFakeRepStore()
	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled: true, ColdCap: 0.1, ColdThreshold: 5, Iterations: 50,
	})
	cruxes := []types.Crux{
		{SourcePositionIDs: []string{"p-A"}, AgreeAgents: []string{"B"}},
		{SourcePositionIDs: []string{"p-B"}, AgreeAgents: []string{"A"}},
	}
	authors := map[string]string{"p-A": "A", "p-B": "B"}

	if err := w.UpdateFromRound(context.Background(), cruxes, authors); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}
	if fs.reps["A"].SurvivedCount != 0 {
		t.Fatalf("Sybil A must not graduate on a pair ring; survived_count=%d",
			fs.reps["A"].SurvivedCount)
	}
	if fs.reps["B"].SurvivedCount != 0 {
		t.Fatalf("Sybil B must not graduate on a pair ring; survived_count=%d",
			fs.reps["B"].SurvivedCount)
	}
}

// TestSybilRingStarvedByColdCap: the cold-start cap is the primary
// defense against Sybils. A fresh 3-agent Sybil ring (zero survived
// validations) all clamps to ColdCap regardless of whatever score
// they have manufactured through mutual edges. A single seasoned
// legitimate agent dominates.
func TestSybilRingStarvedByColdCap(t *testing.T) {
	fs := newFakeRepStore()
	// Sybils have pumped their score to 0.9 via internal edges, but
	// their survived_count is 0 because no external agent has validated
	// any of their positions.
	fs.reps["S1"] = store.Reputation{AgentID: "S1", Score: 0.9, SurvivedCount: 0}
	fs.reps["S2"] = store.Reputation{AgentID: "S2", Score: 0.9, SurvivedCount: 0}
	fs.reps["S3"] = store.Reputation{AgentID: "S3", Score: 0.9, SurvivedCount: 0}
	// Legit agent has lower raw score but has accumulated validations
	// across many deliberations.
	fs.reps["legit"] = store.Reputation{AgentID: "legit", Score: 0.3, SurvivedCount: 20}

	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    50,
	})
	weights := w.WeightsFor(context.Background(), []string{"S1", "S2", "S3", "legit"})

	// All Sybils clamped to ColdCap.
	for _, s := range []string{"S1", "S2", "S3"} {
		if weights[s] != 0.1 {
			t.Fatalf("Sybil %s must be clamped to cold_cap=0.1, got %f", s, weights[s])
		}
	}
	// Combined Sybil weight (3 * 0.1 = 0.3) must not dominate legit.
	// The cold-start cap ensures the total Sybil contribution is
	// bounded by ring_size * cold_cap, which is small for any
	// realistic threshold.
	if weights["legit"] < weights["S1"] {
		t.Fatalf("legit (%f) must dominate individual Sybil (%f)", weights["legit"], weights["S1"])
	}
}

// TestDisabledReputationReturnsNil: when the feature is disabled,
// NewReputationWeigher returns nil so callers can skip wiring.
func TestDisabledReputationReturnsNil(t *testing.T) {
	fs := newFakeRepStore()
	w := reputation.NewWeigher(fs, reputation.Config{Enabled: false})
	if w != nil {
		t.Fatalf("disabled config must yield nil weigher, got %+v", w)
	}
}
