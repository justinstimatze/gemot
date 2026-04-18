package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/reputation"
)

// repAnalyzer produces a crux with SourcePositionIDs + AgreeAgents
// populated so the reputation layer has enough data to emit edges at
// round close. The default mockAnalyzer leaves SourcePositionIDs empty,
// which would make these privacy tests vacuously true (no edges
// regardless of visibility).
type repAnalyzer struct{}

func (repAnalyzer) Analyze(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	posIDs := make([]string, 0, len(positions))
	for _, p := range positions {
		posIDs = append(posIDs, p.ID)
	}
	crux := deliberation.Crux{
		Claim:             "reputation test crux",
		Topic:             "test",
		Subtopic:          "test",
		SourcePositionIDs: posIDs,
		AgreeAgents:       agents,
		ControversyScore:  0.5,
		Explanation:       "deterministic crux with all agents agreeing",
	}
	return &deliberation.AnalysisResult{
		Clusters:       []deliberation.OpinionCluster{{ID: 0, AgentIDs: agents, Size: len(agents)}},
		Cruxes:         []deliberation.Crux{crux},
		TopicSummaries: []deliberation.TopicSummary{{Topic: "test", Summary: "test"}},
		AgentCount:     len(agents),
		PositionCount:  len(positions),
		VoteCount:      len(votes),
	}, nil
}

// TestPrivateDelibEmitsNoTrustEdges: a deliberation created with
// WithVisibility("private") must not emit into the GLOBAL partition
// (delib_id=”) of agent_trust_edges at round close. Edges scoped to
// the delib's own partition (delib_id=<delib.ID>) are expected — the
// P4 FULL design partitions the trust graph so private delibs can
// have per-delib EigenTrust without leaking into the globally-
// readable eigenvector.
//
// Mirrors TestFullDeliberationLoop in flow but (a) flips visibility
// private, (b) wires reputation, and (c) asserts the GLOBAL partition
// is untouched while the private partition carries this delib's edges.
func TestPrivateDelibEmitsNoTrustEdges(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, repAnalyzer{})
	w := reputation.NewWeigher(db, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    20,
	})
	svc.SetReputationUpdater(w)

	ctx := context.Background()
	d, err := svc.CreateDeliberation(ctx, "private topic", "private",
		deliberation.WithVisibility("private"),
		deliberation.WithCreatorKey("test-creator"),
	)
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}

	agents := []string{"alice", "bob", "carol", "dave"}
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

	var globalCount int
	if err := db.RawDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_trust_edges WHERE deliberation_id = ''`).Scan(&globalCount); err != nil {
		t.Fatalf("count global edges: %v", err)
	}
	if globalCount != 0 {
		t.Fatalf("private delib leaked %d trust edges into global graph", globalCount)
	}

	var scopedCount int
	if err := db.RawDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_trust_edges WHERE deliberation_id = $1`, d.ID).Scan(&scopedCount); err != nil {
		t.Fatalf("count scoped edges: %v", err)
	}
	if scopedCount == 0 {
		t.Fatalf("private delib emitted no scoped edges; per-delib EigenTrust graph is empty")
	}

	// survived_count must also not increment — leaking survival on private
	// delibs would let attackers graduate their Sybils out of the cold cap
	// by running a private ring delib.
	var repCount int
	if err := db.RawDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_reputation WHERE survived_count > 0`).Scan(&repCount); err != nil {
		t.Fatalf("count reputation: %v", err)
	}
	if repCount != 0 {
		t.Fatalf("private delib leaked %d survived_count increments", repCount)
	}
}

// TestPublicDelibEmitsTrustEdgesAsBefore: regression — the default
// visibility ("open") still populates agent_trust_edges. The P4 MVP
// branch must not accidentally disable reputation for public delibs.
func TestPublicDelibEmitsTrustEdgesAsBefore(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, repAnalyzer{})
	w := reputation.NewWeigher(db, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    20,
	})
	svc.SetReputationUpdater(w)

	ctx := context.Background()
	d, err := svc.CreateDeliberation(ctx, "public topic", "public")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}

	agents := []string{"alice", "bob", "carol", "dave"}
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

	var count int
	if err := db.RawDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_trust_edges`).Scan(&count); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if count == 0 {
		t.Fatalf("public delib emitted no trust edges; P4 branch over-skipped")
	}
}

// TestLinkVisibilityEmitsTrustEdges: "link" visibility is treated as
// public for reputation purposes (discoverable-by-token, not consent-
// limited). A link-shared deliberation still contributes to the global
// trust graph.
func TestLinkVisibilityEmitsTrustEdges(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, repAnalyzer{})
	w := reputation.NewWeigher(db, reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: 5,
		Iterations:    20,
	})
	svc.SetReputationUpdater(w)

	ctx := context.Background()
	d, err := svc.CreateDeliberation(ctx, "link topic", "link",
		deliberation.WithVisibility("link"),
	)
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}

	agents := []string{"alice", "bob", "carol", "dave"}
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

	var count int
	if err := db.RawDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_trust_edges`).Scan(&count); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if count == 0 {
		t.Fatalf("link-visibility delib emitted no trust edges; should behave like open")
	}
}
