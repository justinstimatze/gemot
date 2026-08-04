package tests

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/reputation"
	"github.com/justinstimatze/gemot/internal/store"
	"github.com/justinstimatze/gemot/types"
)

// fakeReputationStore is an in-memory ReputationStore for unit tests.
// Mirrors the Postgres semantics after schema v4: the reps/edges/scores
// maps are keyed on *vertex strings* ("id:<agent>" for unsigned agents,
// "key:<keyID>" for agents with an active key). The LoadReputation
// surface takes symbolic agent names and resolves them through the
// ResolveVertices helper so callers see the same key-agnostic API as
// in production. Missing rows are absent from LoadReputation's output
// (cold-start state), AccumulateTrustEdges sums weights,
// PersistEigenTrustScores upserts.
type fakeReputationStore struct {
	reps map[string]store.Reputation
	// edges: key [from, to, delibID] → cumulative weight. delibID=""
	// is the global partition; non-empty is a private-delib partition.
	edges  map[[3]string]float64
	scores map[string]float64
	// keys pins symbolic agent → active agent_keys.id for the pubkey-
	// binding tests. Empty for tests that don't care about key identity;
	// in that case every agent resolves to its "id:<agent>" form.
	keys      map[string]string
	decayCall int
}

func newFakeRepStore() *fakeReputationStore {
	return &fakeReputationStore{
		reps:   map[string]store.Reputation{},
		edges:  map[[3]string]float64{},
		scores: map[string]float64{},
		keys:   map[string]string{},
	}
}

// idV is the vertex string for an agent with no active key. Used by
// tests to assert against stored rows without re-deriving the prefix.
func idV(agent string) string { return store.VertexIDPrefix + agent }

// keyV is the vertex string for an agent bound to a specific key id.
func keyV(keyID string) string { return store.VertexKeyPrefix + keyID }

func (f *fakeReputationStore) ResolveVertices(_ context.Context, agents []string) (map[string]string, error) {
	out := make(map[string]string, len(agents))
	for _, a := range agents {
		if keyID, ok := f.keys[a]; ok && keyID != "" {
			out[a] = keyV(keyID)
		} else {
			out[a] = idV(a)
		}
	}
	return out, nil
}

func (f *fakeReputationStore) LoadReputation(ctx context.Context, agents []string) (map[string]store.Reputation, error) {
	vertices, _ := f.ResolveVertices(ctx, agents)
	out := map[string]store.Reputation{}
	for _, a := range agents {
		if r, ok := f.reps[vertices[a]]; ok {
			r.AgentID = a
			out[a] = r
		}
	}
	return out, nil
}

// LoadTrustEdges in the fake honors the delib-partition convention:
// delibID="" returns edges whose scope is ""; delibID=<id> returns
// edges scoped to "" OR that id (mirrors the real store's private-
// delib union semantics).
func (f *fakeReputationStore) LoadTrustEdges(_ context.Context, delibID string) ([]analysis.Edge, error) {
	out := make([]analysis.Edge, 0, len(f.edges))
	for k, w := range f.edges {
		if delibID == "" {
			if k[2] != "" {
				continue
			}
		} else {
			if k[2] != "" && k[2] != delibID {
				continue
			}
		}
		out = append(out, analysis.Edge{From: k[0], To: k[1], Weight: w})
	}
	return out, nil
}

// LoadTrustEdgesForCohort returns the private partition plus global
// edges where either endpoint is in cohort. Mirrors PostgresReputation
// store's SQL semantics.
func (f *fakeReputationStore) LoadTrustEdgesForCohort(_ context.Context, delibID string, cohort []string) ([]analysis.Edge, error) {
	if delibID == "" && len(cohort) == 0 {
		return f.LoadTrustEdges(context.Background(), "")
	}
	inCohort := make(map[string]bool, len(cohort))
	for _, v := range cohort {
		inCohort[v] = true
	}
	out := make([]analysis.Edge, 0, len(f.edges))
	for k, w := range f.edges {
		scope := k[2]
		if scope != "" {
			if scope == delibID {
				out = append(out, analysis.Edge{From: k[0], To: k[1], Weight: w})
			}
			continue
		}
		// Global edge: include only if either endpoint is in cohort.
		if len(cohort) > 0 && (inCohort[k[0]] || inCohort[k[1]]) {
			out = append(out, analysis.Edge{From: k[0], To: k[1], Weight: w})
		}
	}
	return out, nil
}

func (f *fakeReputationStore) AccumulateTrustEdges(_ context.Context, edges []analysis.Edge, cap float64, delibID string) error {
	for _, e := range edges {
		if e.Weight <= 0 {
			continue
		}
		key := [3]string{e.From, e.To, delibID}
		sum := f.edges[key] + e.Weight
		if cap > 0 && sum > cap {
			sum = cap
		}
		f.edges[key] = sum
	}
	return nil
}

// ApplyDisputeEdges subtracts weight (mirroring Postgres semantics:
// negatives allowed; EigenTrust clamps non-positive at input).
func (f *fakeReputationStore) ApplyDisputeEdges(_ context.Context, edges []analysis.Edge, delibID string) error {
	for _, e := range edges {
		if e.Weight <= 0 {
			continue
		}
		f.edges[[3]string{e.From, e.To, delibID}] -= e.Weight
	}
	return nil
}

// DecayTrustEdges is a no-op in the fake store — decay math is tested
// against the real Postgres store in reputation_decay_test.go, and
// in-memory tests assert edge accumulation invariants that decay would
// obscure. Call count is recorded so we can assert the weigher
// invokes the hook exactly when configured.
func (f *fakeReputationStore) DecayTrustEdges(_ context.Context, _ time.Duration, floor float64) error {
	f.decayCall++
	if floor > 0 {
		for k, w := range f.edges {
			if w > 0 && w < floor {
				delete(f.edges, k)
			}
		}
	}
	return nil
}

// edgeKey is a test helper: fabricate the [from, to, delibID] tuple
// used to index the fake store's edge map. delibID defaults to the
// global partition ("") for tests that pre-date the v5 partitioning.
func edgeKey(from, to string) [3]string { return [3]string{from, to, ""} }

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
	fs.reps[idV("seasoned")] = store.Reputation{AgentID: idV("seasoned"), Score: 0.5, SurvivedCount: 10}
	fs.reps[idV("newcomer")] = store.Reputation{AgentID: idV("newcomer"), Score: 0.5, SurvivedCount: 0}

	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    50,
	})
	weights, err := w.WeightsFor(context.Background(), []string{"seasoned", "newcomer"})
	if err != nil {
		t.Fatalf("WeightsFor: %v", err)
	}

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
	fs.reps[idV("pre")] = store.Reputation{AgentID: idV("pre"), Score: 0.0, SurvivedCount: 4}
	fs.reps[idV("post")] = store.Reputation{AgentID: idV("post"), Score: 0.0, SurvivedCount: 5}

	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    50,
	})
	weights, err := w.WeightsFor(context.Background(), []string{"pre", "post"})
	if err != nil {
		t.Fatalf("WeightsFor: %v", err)
	}

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

	if err := w.UpdateFromRound(context.Background(), "", false, cruxes, authors, nil, nil); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}

	if fs.reps[idV("alice")].SurvivedCount != 1 {
		t.Fatalf("alice survived_count=%d, want 1", fs.reps[idV("alice")].SurvivedCount)
	}
	if fs.edges[edgeKey(idV("bob"), idV("alice"))] != 1 {
		t.Fatalf("expected bob→alice edge weight 1, got %f", fs.edges[edgeKey(idV("bob"), idV("alice"))])
	}
	if fs.edges[edgeKey(idV("carol"), idV("alice"))] != 1 {
		t.Fatalf("expected carol→alice edge weight 1, got %f", fs.edges[edgeKey(idV("carol"), idV("alice"))])
	}
	if _, ok := fs.edges[edgeKey(idV("alice"), idV("alice"))]; ok {
		t.Fatalf("unexpected self-endorsement edge")
	}
	if fs.scores[idV("alice")] <= 0 {
		t.Fatalf("alice should have positive score after round, got %f", fs.scores[idV("alice")])
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

	if err := w.UpdateFromRound(context.Background(), "", false, cruxes, authors, nil, nil); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}
	if fs.edges[edgeKey(idV("bob"), idV("alice"))] != 1 {
		t.Fatalf("dup agreers must not double-count; bob→alice weight=%f, want 1",
			fs.edges[edgeKey(idV("bob"), idV("alice"))])
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

	if err := w.UpdateFromRound(context.Background(), "", false, cruxes, authors, nil, nil); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}
	if fs.reps[idV("A")].SurvivedCount != 0 {
		t.Fatalf("Sybil A must not graduate on a pair ring; survived_count=%d",
			fs.reps[idV("A")].SurvivedCount)
	}
	if fs.reps[idV("B")].SurvivedCount != 0 {
		t.Fatalf("Sybil B must not graduate on a pair ring; survived_count=%d",
			fs.reps[idV("B")].SurvivedCount)
	}
}

// TestUpdateFromRoundRollsUpToPrincipal: a position carried by an ephemeral
// agent under a verified delegation credits the *principal* vertex, not the
// agent (Move 5). Edges point at the principal, survived_count accrues to
// the principal, and the throwaway agent vertex stays absent — so a
// human-org's standing follows it across the agents it fields.
func TestUpdateFromRoundRollsUpToPrincipal(t *testing.T) {
	fs := newFakeRepStore()
	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled: true, ColdCap: 0.1, ColdThreshold: 5, Iterations: 50,
	})
	cruxes := []types.Crux{
		{
			SourcePositionIDs: []string{"p-1"},
			AgreeAgents:       []string{"bob", "carol"},
		},
	}
	authors := map[string]string{"p-1": "acme-agent"}
	principalOf := map[string]string{"acme-agent": "acme:alice"}

	if err := w.UpdateFromRound(context.Background(), "", false, cruxes, authors, nil, principalOf); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}

	// Credit landed on the principal.
	if fs.reps[idV("acme:alice")].SurvivedCount != 1 {
		t.Fatalf("principal survived_count=%d, want 1", fs.reps[idV("acme:alice")].SurvivedCount)
	}
	if fs.edges[edgeKey(idV("bob"), idV("acme:alice"))] != 1 {
		t.Fatalf("expected bob→principal edge weight 1, got %f", fs.edges[edgeKey(idV("bob"), idV("acme:alice"))])
	}
	// Nothing landed on the ephemeral agent.
	if _, ok := fs.reps[idV("acme-agent")]; ok {
		t.Fatalf("ephemeral agent vertex should carry no reputation after rollup")
	}
	if _, ok := fs.edges[edgeKey(idV("bob"), idV("acme-agent"))]; ok {
		t.Fatalf("no edge should point at the ephemeral agent after rollup")
	}
}

// TestUpdateFromRoundSamePrincipalCollapses: two distinct agents acting for
// the SAME verified principal, agreeing with each other, must not graduate
// the principal — after rollup both endpoints are one vertex, so the mutual
// agreement is a self-endorsement and is dropped. Without uniform rollup of
// agreers (not just authors) this would look like two distinct agreers and
// spuriously cross minDistinctAgreers.
func TestUpdateFromRoundSamePrincipalCollapses(t *testing.T) {
	fs := newFakeRepStore()
	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled: true, ColdCap: 0.1, ColdThreshold: 5, Iterations: 50,
	})
	cruxes := []types.Crux{
		{SourcePositionIDs: []string{"p-1"}, AgreeAgents: []string{"agent-2"}},
		{SourcePositionIDs: []string{"p-2"}, AgreeAgents: []string{"agent-1"}},
	}
	authors := map[string]string{"p-1": "agent-1", "p-2": "agent-2"}
	principalOf := map[string]string{"agent-1": "acme:alice", "agent-2": "acme:alice"}

	if err := w.UpdateFromRound(context.Background(), "", false, cruxes, authors, nil, principalOf); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}
	if _, ok := fs.edges[edgeKey(idV("acme:alice"), idV("acme:alice"))]; ok {
		t.Fatalf("self-endorsement edge from same-principal agreement must be dropped")
	}
	if fs.reps[idV("acme:alice")].SurvivedCount != 0 {
		t.Fatalf("principal must not graduate off its own agents agreeing; survived_count=%d",
			fs.reps[idV("acme:alice")].SurvivedCount)
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
	fs.reps[idV("S1")] = store.Reputation{AgentID: idV("S1"), Score: 0.9, SurvivedCount: 0}
	fs.reps[idV("S2")] = store.Reputation{AgentID: idV("S2"), Score: 0.9, SurvivedCount: 0}
	fs.reps[idV("S3")] = store.Reputation{AgentID: idV("S3"), Score: 0.9, SurvivedCount: 0}
	// Legit agent has lower raw score but has accumulated validations
	// across many deliberations.
	fs.reps[idV("legit")] = store.Reputation{AgentID: idV("legit"), Score: 0.3, SurvivedCount: 20}

	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    50,
	})
	weights, err := w.WeightsFor(context.Background(), []string{"S1", "S2", "S3", "legit"})
	if err != nil {
		t.Fatalf("WeightsFor: %v", err)
	}

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

// failingRepStore wraps fakeReputationStore and forces LoadReputation
// to return an error. Other methods delegate so tests can still
// exercise the non-load paths if needed.
type failingRepStore struct {
	*fakeReputationStore
	err error
}

func (f *failingRepStore) LoadReputation(_ context.Context, _ []string) (map[string]store.Reputation, error) {
	return nil, f.err
}

// TestWeightsForFailsClosedOnDBError: under DBFail="closed" a store
// read failure must propagate as an error so the caller aborts the
// analysis round rather than silently neutralizing the cold-start
// defense. This is the Byzantine-context fix the adversarial test
// suite surfaced — an attacker who can DoS Postgres would otherwise
// strip all reputation weighting.
func TestWeightsForFailsClosedOnDBError(t *testing.T) {
	stubErr := errors.New("simulated postgres outage")
	fs := &failingRepStore{fakeReputationStore: newFakeRepStore(), err: stubErr}

	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    50,
		DBFail:        reputation.DBFailClosed,
	})

	weights, err := w.WeightsFor(context.Background(), []string{"alice", "bob"})
	if err == nil {
		t.Fatalf("DBFail=closed must propagate store error, got weights=%v err=nil", weights)
	}
	if weights != nil {
		t.Fatalf("DBFail=closed must return nil weights on error, got %v", weights)
	}
	if !errors.Is(err, stubErr) {
		t.Fatalf("returned error must wrap underlying store error, got %v", err)
	}
	if !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("error message should mention fail-closed for operator diagnosis, got %q", err.Error())
	}
}

// TestWeightsForFailsOpenOnDBError: default (DBFail unset / "open")
// preserves legacy behaviour — a store read failure degrades to unit
// weights with a slog.Warn. Keeps availability as the default when
// operators have not opted into fail-closed.
func TestWeightsForFailsOpenOnDBError(t *testing.T) {
	fs := &failingRepStore{fakeReputationStore: newFakeRepStore(), err: errors.New("db down")}

	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    50,
		// DBFail left empty → default fail-open.
	})

	agents := []string{"alice", "bob"}
	weights, err := w.WeightsFor(context.Background(), agents)
	if err != nil {
		t.Fatalf("DBFail=open must swallow store error, got %v", err)
	}
	for _, a := range agents {
		if weights[a] != 1.0 {
			t.Fatalf("fail-open must return unit weights, got %s=%f", a, weights[a])
		}
	}
}
