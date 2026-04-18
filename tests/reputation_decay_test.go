package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/reputation"
	"github.com/justinstimatze/gemot/types"
)

// Priority 1 §3 (DARPA Track 1): overt adversarial signals — Disputes —
// contribute negative-weight edges to the trust graph. The EigenTrust
// computation clamps non-positive edges to zero at input (see
// eigentrust.go:142), so a sufficiently-disputed inbound trust edge is
// effectively zeroed out rather than punching trust below zero. These
// tests drive that against the in-memory fakeReputationStore.

// TestUpdateFromRoundApplyDispute: a single dispute from an honest
// agent against a crux reduces the corresponding trust edge by
// DisputeWeight. Bob agrees with alice (edge bob→alice = 1); carol
// disputes alice's crux (edge carol→alice = -DisputeWeight).
func TestUpdateFromRoundApplyDispute(t *testing.T) {
	fs := newFakeRepStore()
	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled: true, ColdCap: 0.1, ColdThreshold: 5, Iterations: 50,
		DisputeWeight: 0.5,
	})

	cruxes := []types.Crux{
		{
			Claim:             "AI licensing is required.",
			SourcePositionIDs: []string{"p-alice"},
			AgreeAgents:       []string{"bob", "dave"},
		},
	}
	authors := map[string]string{"p-alice": "alice"}
	disputes := []types.Dispute{
		{AgentID: "carol", CruxClaim: "AI licensing is required."},
	}

	if err := w.UpdateFromRound(context.Background(), cruxes, authors, disputes); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}

	if got := fs.edges[[2]string{"bob", "alice"}]; got != 1 {
		t.Fatalf("bob→alice expected 1, got %f", got)
	}
	if got := fs.edges[[2]string{"carol", "alice"}]; got != -0.5 {
		t.Fatalf("carol→alice expected -0.5, got %f", got)
	}
}

// TestDisputeCancelsPriorEndorsement: an agent who previously endorsed
// an author and later disputes them nets out to weight 1 - DisputeWeight
// on the same edge. Confirms disputes operate on the same row as
// endorsements rather than creating a separate negative row.
func TestDisputeCancelsPriorEndorsement(t *testing.T) {
	fs := newFakeRepStore()
	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled: true, ColdCap: 0.1, ColdThreshold: 5, Iterations: 50,
		DisputeWeight: 0.6,
	})

	// Round 1: bob endorses alice (via agreement on alice's crux).
	r1 := []types.Crux{
		{
			Claim:             "Regulation is warranted.",
			SourcePositionIDs: []string{"p-alice"},
			AgreeAgents:       []string{"bob", "dave"},
		},
	}
	if err := w.UpdateFromRound(context.Background(), r1, map[string]string{"p-alice": "alice"}, nil); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	if got := fs.edges[[2]string{"bob", "alice"}]; got != 1 {
		t.Fatalf("after r1, bob→alice want 1, got %f", got)
	}

	// Round 2: bob disputes a crux authored by alice. Edge net = 1 - 0.6 = 0.4.
	r2 := []types.Crux{
		{
			Claim:             "Regulation stifles innovation.",
			SourcePositionIDs: []string{"p-alice-2"},
			AgreeAgents:       nil,
		},
	}
	disputes := []types.Dispute{
		{AgentID: "bob", CruxClaim: "Regulation stifles innovation."},
	}
	if err := w.UpdateFromRound(context.Background(), r2, map[string]string{"p-alice-2": "alice"}, disputes); err != nil {
		t.Fatalf("round 2: %v", err)
	}
	if got := fs.edges[[2]string{"bob", "alice"}]; got != 0.4 {
		t.Fatalf("net edge after dispute want 0.4, got %f", got)
	}
}

// TestDisputeSkipsSelf: an agent disputing a crux whose author is the
// same agent must not create a self-edge. Defends against a
// self-sabotage signal being recorded as trust-graph input.
func TestDisputeSkipsSelf(t *testing.T) {
	fs := newFakeRepStore()
	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled: true, ColdCap: 0.1, ColdThreshold: 5, Iterations: 50,
		DisputeWeight: 0.5,
	})

	cruxes := []types.Crux{
		{
			Claim:             "Self-critique.",
			SourcePositionIDs: []string{"p-alice"},
			AgreeAgents:       nil,
		},
	}
	disputes := []types.Dispute{
		{AgentID: "alice", CruxClaim: "Self-critique."},
	}
	if err := w.UpdateFromRound(context.Background(), cruxes, map[string]string{"p-alice": "alice"}, disputes); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}
	if _, ok := fs.edges[[2]string{"alice", "alice"}]; ok {
		t.Fatalf("unexpected self-dispute edge")
	}
}

// TestDisputeUnknownCruxIgnored: a dispute whose CruxClaim doesn't
// match any surviving crux is dropped silently. Disputes only
// contribute signal when the crux they reference actually appears in
// the round's output — protects against stale or typo'd claims
// polluting the trust graph.
func TestDisputeUnknownCruxIgnored(t *testing.T) {
	fs := newFakeRepStore()
	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled: true, ColdCap: 0.1, ColdThreshold: 5, Iterations: 50,
		DisputeWeight: 0.5,
	})

	cruxes := []types.Crux{
		{
			Claim:             "Known claim.",
			SourcePositionIDs: []string{"p-alice"},
			AgreeAgents:       []string{"bob", "dave"},
		},
	}
	disputes := []types.Dispute{
		{AgentID: "carol", CruxClaim: "Unknown claim nobody made."},
	}
	if err := w.UpdateFromRound(context.Background(), cruxes, map[string]string{"p-alice": "alice"}, disputes); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}
	// No edge should land for carol.
	if len(fs.edges) != 2 {
		t.Fatalf("expected only endorsement edges, got %d total: %+v", len(fs.edges), fs.edges)
	}
}

// TestDecayTrustEdgesCalledWhenConfigured: Weigher.recomputeGlobalScores
// invokes the store's DecayTrustEdges hook exactly when
// DecayHalfLifeDays > 0. Zero (default) must not call decay — preserves
// the "off by default" cumulative-forever semantics.
func TestDecayTrustEdgesCalledWhenConfigured(t *testing.T) {
	fs := newFakeRepStore()
	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled: true, ColdCap: 0.1, ColdThreshold: 5, Iterations: 50,
		DecayHalfLifeDays: 14,
	})
	cruxes := []types.Crux{
		{
			SourcePositionIDs: []string{"p-alice"},
			AgreeAgents:       []string{"bob", "carol"},
		},
	}
	if err := w.UpdateFromRound(context.Background(), cruxes, map[string]string{"p-alice": "alice"}, nil); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}
	if fs.decayCall != 1 {
		t.Fatalf("expected 1 DecayTrustEdges call with halflife=14, got %d", fs.decayCall)
	}
}

// TestDecayTrustEdgesSkippedByDefault: the default config (halflife=0)
// leaves decay off. A run with decay disabled must NOT touch
// DecayTrustEdges at all, because the zero-halflife case means "no
// decay configured" — even a single no-op call could mask a regression
// that ships decay in a deployment that opted out.
func TestDecayTrustEdgesSkippedByDefault(t *testing.T) {
	fs := newFakeRepStore()
	w := reputation.NewWeigher(fs, reputation.Config{
		Enabled: true, ColdCap: 0.1, ColdThreshold: 5, Iterations: 50,
		// DecayHalfLifeDays omitted → zero → disabled.
	})
	cruxes := []types.Crux{
		{
			SourcePositionIDs: []string{"p-alice"},
			AgreeAgents:       []string{"bob", "carol"},
		},
	}
	if err := w.UpdateFromRound(context.Background(), cruxes, map[string]string{"p-alice": "alice"}, nil); err != nil {
		t.Fatalf("UpdateFromRound: %v", err)
	}
	if fs.decayCall != 0 {
		t.Fatalf("expected 0 DecayTrustEdges calls when halflife=0, got %d", fs.decayCall)
	}
}

// TestDisputeAgainstSybilRingDampensScore: a 3-Sybil mutual-endorsement
// ring (s1,s2,s3) inflates EigenTrust scores when unchecked. Filing
// disputes of equal magnitude zeroes the ring's internal edges at
// EigenTrust input (non-positive weights drop), and the remaining
// external endorsement s1→honest flows ring mass out to the honest
// vertex instead of circulating inside the ring. Comparison metric:
// how much score the ring retains vs. how much leaks to honest.
//
// Both cases share vertex set {s1, s2, s3, honest} via an s1→honest
// endorsement, so EigenTrust normalization compares the same partition.
func TestDisputeAgainstSybilRingDampensScore(t *testing.T) {
	// honest's crux, endorsed by s1 — establishes the one cross-group edge
	// and pulls honest into the vertex set.
	honestCrux := types.Crux{
		Claim:             "Honest claim.",
		SourcePositionIDs: []string{"p-honest"},
		AgreeAgents:       []string{"s1"},
	}
	ringCruxes := []types.Crux{
		{
			Claim:             "Ring s1 claim.",
			SourcePositionIDs: []string{"p-s1"},
			AgreeAgents:       []string{"s2", "s3"},
		},
		{
			Claim:             "Ring s2 claim.",
			SourcePositionIDs: []string{"p-s2"},
			AgreeAgents:       []string{"s1", "s3"},
		},
		{
			Claim:             "Ring s3 claim.",
			SourcePositionIDs: []string{"p-s3"},
			AgreeAgents:       []string{"s1", "s2"},
		},
	}
	allCruxes := append([]types.Crux{honestCrux}, ringCruxes...)
	authors := map[string]string{
		"p-s1":     "s1",
		"p-s2":     "s2",
		"p-s3":     "s3",
		"p-honest": "honest",
	}

	// Pumped case: ring internal endorsement + s1→honest. Ring's inbound
	// mass dominates; honest gets a small leak.
	fsPumped := newFakeRepStore()
	wPumped := reputation.NewWeigher(fsPumped, reputation.Config{
		Enabled: true, ColdCap: 0.1, ColdThreshold: 5, Iterations: 50,
		DisputeWeight: 1.0,
	})
	if err := wPumped.UpdateFromRound(context.Background(), allCruxes, authors, nil); err != nil {
		t.Fatalf("pumped round: %v", err)
	}
	pumpedRingMin := fsPumped.scores["s1"]
	for _, a := range []string{"s2", "s3"} {
		if fsPumped.scores[a] < pumpedRingMin {
			pumpedRingMin = fsPumped.scores[a]
		}
	}
	pumpedHonest := fsPumped.scores["honest"]
	if pumpedRingMin <= pumpedHonest {
		t.Fatalf("baseline invariant broken: ring min (%f) should exceed honest (%f) without disputes",
			pumpedRingMin, pumpedHonest)
	}

	// Disputed case: six disputes matching ring endorsements cancel the
	// internal ring edges. Only s1→honest remains positive. Ring
	// becomes mostly sinks; honest gets pumped via the one surviving
	// edge.
	fsDisputed := newFakeRepStore()
	wDisputed := reputation.NewWeigher(fsDisputed, reputation.Config{
		Enabled: true, ColdCap: 0.1, ColdThreshold: 5, Iterations: 50,
		DisputeWeight: 1.0,
	})
	disputes := []types.Dispute{
		{AgentID: "s2", CruxClaim: "Ring s1 claim."},
		{AgentID: "s3", CruxClaim: "Ring s1 claim."},
		{AgentID: "s1", CruxClaim: "Ring s2 claim."},
		{AgentID: "s3", CruxClaim: "Ring s2 claim."},
		{AgentID: "s1", CruxClaim: "Ring s3 claim."},
		{AgentID: "s2", CruxClaim: "Ring s3 claim."},
	}
	if err := wDisputed.UpdateFromRound(context.Background(), allCruxes, authors, disputes); err != nil {
		t.Fatalf("disputed round: %v", err)
	}
	disputedRingMax := 0.0
	for _, a := range []string{"s1", "s2", "s3"} {
		if fsDisputed.scores[a] > disputedRingMax {
			disputedRingMax = fsDisputed.scores[a]
		}
	}
	disputedHonest := fsDisputed.scores["honest"]

	// Primary invariant: disputes flip the ordering — honest now
	// dominates the ring's best member, because the one cross-group edge
	// s1→honest is the only positive channel left.
	if disputedHonest <= disputedRingMax {
		t.Fatalf("disputes did not flip ordering: honest=%f, ring_max=%f (gap=%f)",
			disputedHonest, disputedRingMax, disputedRingMax-disputedHonest)
	}
}
