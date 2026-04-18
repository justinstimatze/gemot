package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/reputation"
	"github.com/justinstimatze/gemot/internal/store"
	"github.com/justinstimatze/gemot/types"
)

// Schema v4 binds reputation to the agent_keys.id row rather than the
// symbolic agent_id. These tests cover the four boundary cases:
//
//  1. Rename attack: transferring an agent_id to a new key yields a
//     fresh reputation vertex, not the prior key's accumulated state.
//  2. Unsigned deployment: agents without a registered key fall back
//     to the "id:<agent>" vertex; everything works end-to-end.
//  3. Unsigned → signed transition: registering a key forks the agent
//     into a new vertex; prior symbolic reputation stays behind (is
//     orphaned on the "id:<agent>" row).
//  4. ResolveVertices ergonomics: mixed cohorts resolve per-agent.

// activeKeyID reads the current agent_keys.id for an agent directly
// from the DB. Tests use this to construct the expected vertex string
// so they can assert on LoadReputation round-trips without depending on
// reputation-layer internals.
func activeKeyID(t *testing.T, db *store.DB, agentID string) string {
	t.Helper()
	var keyID string
	err := db.RawDB().QueryRowContext(context.Background(),
		`SELECT id FROM agent_keys WHERE agent_id = $1 AND revoked_at IS NULL LIMIT 1`,
		agentID,
	).Scan(&keyID)
	if err != nil {
		t.Fatalf("activeKeyID(%s): %v", agentID, err)
	}
	return keyID
}

// TestRenameAttackFailsToTransferReputation: the load-bearing test for
// the federation-critical Sybil defense. Alice earns reputation bound
// to her current key K1. She (or an attacker post-compromise) rotates
// to K2 under the same agent_id. Subsequent WeightsFor("alice") must
// see fresh reputation, not the K1-pumped state.
func TestRenameAttackFailsToTransferReputation(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	// Register K1 for alice.
	if err := db.RegisterAgentKey(ctx, "alice", []byte("pubkey-K1"), "ed25519"); err != nil {
		t.Fatalf("register K1: %v", err)
	}
	k1 := activeKeyID(t, db, "alice")
	k1Vertex := store.VertexKeyPrefix + k1

	// Pump reputation against K1's vertex: 10 graduated survived counts
	// plus a hefty EigenTrust score.
	for i := 0; i < 10; i++ {
		if err := db.IncrementSurvivedCounts(ctx, []string{k1Vertex}); err != nil {
			t.Fatalf("increment alice@K1: %v", err)
		}
	}
	if err := db.PersistEigenTrustScores(ctx, map[string]float64{k1Vertex: 0.8}); err != nil {
		t.Fatalf("persist score alice@K1: %v", err)
	}

	// Sanity: LoadReputation for "alice" resolves to K1 and finds the pumped rep.
	before, err := db.LoadReputation(ctx, []string{"alice"})
	if err != nil {
		t.Fatalf("LoadReputation before rotation: %v", err)
	}
	if before["alice"].SurvivedCount != 10 || before["alice"].Score != 0.8 {
		t.Fatalf("pre-rotation pumped rep missing: survived=%d score=%f",
			before["alice"].SurvivedCount, before["alice"].Score)
	}

	// The attack: register K2 under the same agent_id (legit rotation
	// also follows this code path; the defense applies symmetrically).
	if err := db.RegisterAgentKey(ctx, "alice", []byte("pubkey-K2"), "ed25519"); err != nil {
		t.Fatalf("register K2: %v", err)
	}
	k2 := activeKeyID(t, db, "alice")
	if k2 == k1 {
		t.Fatalf("expected new key id after rotation; got %q twice", k1)
	}

	// Under the new key, alice's vertex is "key:<K2>" — a fresh row.
	after, err := db.LoadReputation(ctx, []string{"alice"})
	if err != nil {
		t.Fatalf("LoadReputation after rotation: %v", err)
	}
	if _, present := after["alice"]; present {
		t.Fatalf("post-rotation lookup should be empty (cold start); got %+v", after["alice"])
	}

	// And WeightsFor must cold-cap alice, not carry over the old score.
	w := reputation.NewWeigher(db, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    50,
	})
	weights, err := w.WeightsFor(ctx, []string{"alice"})
	if err != nil {
		t.Fatalf("WeightsFor after rotation: %v", err)
	}
	if weights["alice"] != 0.1 {
		t.Fatalf("post-rotation alice must cold-cap at 0.1; got %f "+
			"(rename attack transferred reputation)", weights["alice"])
	}
}

// TestUnsignedAgentResolvesToIDVertex: an agent with no registered key
// maps to the "id:<agent>" vertex. Verifies the fallback path that
// keeps unsigned deployments working.
func TestUnsignedAgentResolvesToIDVertex(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	vertices, err := db.ResolveVertices(ctx, []string{"bob", "carol"})
	if err != nil {
		t.Fatalf("ResolveVertices: %v", err)
	}
	if vertices["bob"] != store.VertexIDPrefix+"bob" {
		t.Fatalf("unsigned bob vertex=%q, want %q", vertices["bob"], store.VertexIDPrefix+"bob")
	}
	if vertices["carol"] != store.VertexIDPrefix+"carol" {
		t.Fatalf("unsigned carol vertex=%q, want %q", vertices["carol"], store.VertexIDPrefix+"carol")
	}

	// And a full round-trip: write to "id:bob", read via LoadReputation("bob").
	if err := db.IncrementSurvivedCounts(ctx, []string{vertices["bob"]}); err != nil {
		t.Fatalf("increment unsigned bob: %v", err)
	}
	reps, err := db.LoadReputation(ctx, []string{"bob"})
	if err != nil {
		t.Fatalf("LoadReputation bob: %v", err)
	}
	if reps["bob"].SurvivedCount != 1 {
		t.Fatalf("unsigned bob round-trip broken: %+v", reps["bob"])
	}
}

// TestUnsignedToSignedTransitionOrphansPriorReputation: an agent who
// accrued reputation as unsigned, then registers a key, forks into a
// new key-bound vertex. The prior "id:<agent>" row remains in the DB
// (no automatic transfer) — the transition is explicit and documented.
// This is the correct semantics because without key-binding the prior
// rep is not cryptographically attestable as belonging to the new key.
func TestUnsignedToSignedTransitionOrphansPriorReputation(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	// Accrue rep as unsigned carol.
	idVertex := store.VertexIDPrefix + "carol"
	for i := 0; i < 10; i++ {
		if err := db.IncrementSurvivedCounts(ctx, []string{idVertex}); err != nil {
			t.Fatalf("increment unsigned carol: %v", err)
		}
	}
	if err := db.PersistEigenTrustScores(ctx, map[string]float64{idVertex: 0.7}); err != nil {
		t.Fatalf("persist unsigned carol: %v", err)
	}

	before, err := db.LoadReputation(ctx, []string{"carol"})
	if err != nil {
		t.Fatalf("LoadReputation before signing: %v", err)
	}
	if before["carol"].SurvivedCount != 10 || before["carol"].Score != 0.7 {
		t.Fatalf("pre-signing rep missing: %+v", before["carol"])
	}

	// Now carol registers a key — future rep will bind to the key vertex.
	if err := db.RegisterAgentKey(ctx, "carol", []byte("pubkey"), "ed25519"); err != nil {
		t.Fatalf("register carol's key: %v", err)
	}

	after, err := db.LoadReputation(ctx, []string{"carol"})
	if err != nil {
		t.Fatalf("LoadReputation after signing: %v", err)
	}
	if _, present := after["carol"]; present {
		t.Fatalf("post-signing carol should be cold-start (key vertex is fresh); "+
			"got %+v (transition leaked unsigned rep into key-bound identity)", after["carol"])
	}
}

// TestResolveVerticesMixedCohort: a cohort where some agents have keys
// and others don't must return the right vertex for each, in one call.
func TestResolveVerticesMixedCohort(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	if err := db.RegisterAgentKey(ctx, "signed-1", []byte("pk1"), "ed25519"); err != nil {
		t.Fatalf("register signed-1: %v", err)
	}
	if err := db.RegisterAgentKey(ctx, "signed-2", []byte("pk2"), "ed25519"); err != nil {
		t.Fatalf("register signed-2: %v", err)
	}
	k1 := activeKeyID(t, db, "signed-1")
	k2 := activeKeyID(t, db, "signed-2")

	vertices, err := db.ResolveVertices(ctx, []string{"signed-1", "unsigned", "signed-2"})
	if err != nil {
		t.Fatalf("ResolveVertices: %v", err)
	}
	if vertices["signed-1"] != store.VertexKeyPrefix+k1 {
		t.Fatalf("signed-1 vertex=%q, want %q", vertices["signed-1"], store.VertexKeyPrefix+k1)
	}
	if vertices["signed-2"] != store.VertexKeyPrefix+k2 {
		t.Fatalf("signed-2 vertex=%q, want %q", vertices["signed-2"], store.VertexKeyPrefix+k2)
	}
	if vertices["unsigned"] != store.VertexIDPrefix+"unsigned" {
		t.Fatalf("unsigned vertex=%q, want %q", vertices["unsigned"], store.VertexIDPrefix+"unsigned")
	}
}

// TestRevokedKeyFallsBackToIDVertex: a revoked key is not "active", so
// the agent's current vertex reverts to "id:<agent>" until a new key
// is registered. The previously-bound rep stays on the revoked key's
// vertex (orphaned) — no silent transfer back to the symbolic form.
func TestRevokedKeyFallsBackToIDVertex(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	if err := db.RegisterAgentKey(ctx, "dave", []byte("pk"), "ed25519"); err != nil {
		t.Fatalf("register: %v", err)
	}
	keyID := activeKeyID(t, db, "dave")
	keyVertex := store.VertexKeyPrefix + keyID

	// Accrue rep under the key vertex.
	if err := db.IncrementSurvivedCounts(ctx, []string{keyVertex}); err != nil {
		t.Fatalf("increment: %v", err)
	}
	// Pre-revocation: dave resolves to keyVertex and LoadReputation finds it.
	pre, _ := db.LoadReputation(ctx, []string{"dave"})
	if pre["dave"].SurvivedCount != 1 {
		t.Fatalf("pre-revocation rep missing: %+v", pre["dave"])
	}

	if err := db.RevokeAgentKey(ctx, "dave"); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// Post-revocation: no active key, dave resolves to "id:dave" — fresh row.
	vertices, _ := db.ResolveVertices(ctx, []string{"dave"})
	if vertices["dave"] != store.VertexIDPrefix+"dave" {
		t.Fatalf("post-revoke dave vertex=%q, want %q", vertices["dave"], store.VertexIDPrefix+"dave")
	}
	post, _ := db.LoadReputation(ctx, []string{"dave"})
	if _, present := post["dave"]; present {
		t.Fatalf("post-revoke should be cold-start; got %+v", post["dave"])
	}
}

// TestUpdateFromRoundEmitsKeyedVertexEdges: through the Weigher, a
// signed agent's edges land on the "key:<id>" vertex. Other signed
// agents cite each other via keyed vertices; unsigned agents via id:
// vertices. Verifies end-to-end plumbing through UpdateFromRound.
func TestUpdateFromRoundEmitsKeyedVertexEdges(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	if err := db.RegisterAgentKey(ctx, "alice", []byte("pk-alice"), "ed25519"); err != nil {
		t.Fatalf("register alice: %v", err)
	}
	if err := db.RegisterAgentKey(ctx, "bob", []byte("pk-bob"), "ed25519"); err != nil {
		t.Fatalf("register bob: %v", err)
	}
	kAlice := activeKeyID(t, db, "alice")
	kBob := activeKeyID(t, db, "bob")

	w := reputation.NewWeigher(db, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 2,
		Iterations:    50,
	})

	// bob + unsigned carol agree with alice's crux. Weigher should emit
	// two edges: "key:<kBob>" → "key:<kAlice>" and "id:carol" → "key:<kAlice>".
	cruxes := []types.Crux{{
		Claim:             "test claim",
		SourcePositionIDs: []string{"p-alice"},
		AgreeAgents:       []string{"bob", "carol"},
	}}
	authors := map[string]string{"p-alice": "alice"}
	if err := w.UpdateFromRound(ctx, "", false, cruxes, authors, nil); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}

	loaded, err := db.LoadTrustEdges(ctx, "")
	if err != nil {
		t.Fatalf("LoadTrustEdges: %v", err)
	}
	want := map[[2]string]float64{
		{store.VertexKeyPrefix + kBob, store.VertexKeyPrefix + kAlice}:   1,
		{store.VertexIDPrefix + "carol", store.VertexKeyPrefix + kAlice}: 1,
	}
	byPair := map[[2]string]float64{}
	for _, e := range loaded {
		byPair[[2]string{e.From, e.To}] = e.Weight
	}
	for pair, expected := range want {
		if byPair[pair] != expected {
			t.Fatalf("edge %v weight=%f, want %f; full graph: %+v",
				pair, byPair[pair], expected, byPair)
		}
	}
}
