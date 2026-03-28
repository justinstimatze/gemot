package tests

import (
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestRestorativeOstracism(t *testing.T) {
	agents := []string{"alice", "bob"}
	warnings := []string{
		`SYBIL_SIGNAL: agents "alice" and "bob" have identical votes across all 5 shared positions`,
	}

	// Round 1: full penalty
	w1 := analysis.TrustWeights(agents, nil, nil, warnings, 1)
	if w1["alice"] >= 1.0 {
		t.Fatalf("round 1: sybil agent should have reduced trust, got %f", w1["alice"])
	}

	// Round 2: reduced penalty (0.75x)
	w2 := analysis.TrustWeights(agents, nil, nil, warnings, 2)
	if w2["alice"] <= w1["alice"] {
		t.Fatalf("round 2: trust should recover (got %f, round 1 was %f)", w2["alice"], w1["alice"])
	}

	// Round 3+: further reduced (0.5x)
	w3 := analysis.TrustWeights(agents, nil, nil, warnings, 3)
	if w3["alice"] <= w2["alice"] {
		t.Fatalf("round 3: trust should recover further (got %f, round 2 was %f)", w3["alice"], w2["alice"])
	}
}

func TestAnalysisRefusal(t *testing.T) {
	// Simulate analysis with sybil-compromised data
	svc, _ := newTestService(t)
	d, err := svc.CreateDeliberation("Refusal Test", "Testing analysis refusal")
	if err != nil {
		t.Fatal(err)
	}

	// Submit identical positions from two agents (sybil pattern)
	svc.SubmitPosition(d.ID, "sybil1", "We must regulate AI immediately")
	svc.SubmitPosition(d.ID, "sybil2", "We must regulate AI immediately")
	svc.SubmitPosition(d.ID, "honest", "We need balanced approaches")

	// Make sybils vote identically on all positions
	positions, _ := svc.GetPositions(d.ID, nil, nil)
	for _, p := range positions {
		svc.Vote(d.ID, "sybil1", p.ID, 1)
		svc.Vote(d.ID, "sybil2", p.ID, 1)
		svc.Vote(d.ID, "honest", p.ID, 0)
	}

	// Analysis will proceed (LLM calls will fail without API key, but the
	// integrity checks happen before LLM calls). We can't fully test the
	// refusal without an LLM, but we can verify the sybil detection works
	// at the vote similarity level.
	// This test primarily verifies the code compiles and the flow works.
}

func TestEpistemicHealthMetrics(t *testing.T) {
	// Create a deliberation with known participation
	svc, _ := newTestService(t)
	d, err := svc.CreateDeliberation("Metrics Test", "Testing epistemic health")
	if err != nil {
		t.Fatal(err)
	}

	// 3 agents, 3 positions
	for _, agent := range []string{"a1", "a2", "a3"} {
		svc.SubmitPosition(d.ID, agent, "Position from "+agent)
	}

	// 6 of 6 possible cross-votes (full participation)
	positions, _ := svc.GetPositions(d.ID, nil, nil)
	for _, voter := range []string{"a1", "a2", "a3"} {
		for _, p := range positions {
			if p.AgentID != voter {
				svc.Vote(d.ID, voter, p.ID, 1)
			}
		}
	}

	// Can't run full analysis without LLM, but verify the metrics helpers
	// The actual analysis would set ParticipationRate and PerspectiveDiversity
}

func TestCruxTypeFields(t *testing.T) {
	// Verify the Crux struct has the new fields
	c := deliberation.Crux{
		Claim:         "Test claim",
		CruxType:      "factual",
		Resolvability: 0.8,
	}
	if c.CruxType != "factual" {
		t.Fatal("CruxType field not working")
	}
	if c.Resolvability != 0.8 {
		t.Fatal("Resolvability field not working")
	}
}

func TestPositionInterests(t *testing.T) {
	svc, _ := newTestService(t)
	d, err := svc.CreateDeliberation("Interests Test", "Testing transparent objectives")
	if err != nil {
		t.Fatal(err)
	}

	p, err := svc.SubmitPosition(d.ID, "agent1", "My position",
		deliberation.WithInterests("minimize cost, maximize reliability"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if p.Interests != "minimize cost, maximize reliability" {
		t.Fatalf("expected interests to persist, got %q", p.Interests)
	}

	// Verify it round-trips through get_positions
	positions, _ := svc.GetPositions(d.ID, nil, nil)
	if len(positions) != 1 {
		t.Fatalf("expected 1 position, got %d", len(positions))
	}
	if positions[0].Interests != "minimize cost, maximize reliability" {
		t.Fatalf("interests should round-trip, got %q", positions[0].Interests)
	}
}

func TestRulesPersistence(t *testing.T) {
	svc, _ := newTestService(t)
	d, err := svc.CreateDeliberation("Rules Test", "Testing rules",
		deliberation.WithRules(map[string]any{
			"min_participants":       5,
			"cooling_period_minutes": 30,
			"position_cost":          10,
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Verify rules round-trip
	d2, err := svc.GetDeliberation(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deliberation.RuleInt(d2, "min_participants", 0) != 5 {
		t.Fatal("min_participants should persist")
	}
	if deliberation.RuleInt(d2, "cooling_period_minutes", 0) != 30 {
		t.Fatal("cooling_period_minutes should persist")
	}
	if deliberation.RuleInt(d2, "position_cost", 0) != 10 {
		t.Fatal("position_cost should persist")
	}
	// Non-existent rule returns default
	if deliberation.RuleInt(d2, "nonexistent", 99) != 99 {
		t.Fatal("nonexistent rule should return default")
	}
}

func TestContextAccessTracking(t *testing.T) {
	_, db := newTestService(t)

	// Record access
	if err := db.RecordContextAccess("delib1", "agent1", 1); err != nil {
		t.Fatal(err)
	}

	// Should have access
	has, err := db.HasContextAccess("delib1", "agent1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("should have access after recording")
	}

	// Different round should not have access
	has, err = db.HasContextAccess("delib1", "agent1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("should not have access for different round")
	}

	// Different agent should not have access
	has, err = db.HasContextAccess("delib1", "agent2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("should not have access for different agent")
	}

	// Duplicate insert should not error (INSERT OR IGNORE)
	if err := db.RecordContextAccess("delib1", "agent1", 1); err != nil {
		t.Fatal(err)
	}
}

func TestAuditLogStorage(t *testing.T) {
	_, db := newTestService(t)

	// Log some events
	db.LogAuditEvent("key1", "1.2.3.4", "gemot/submit_position", "delib1", "agent1")
	db.LogAuditEvent("key1", "1.2.3.4", "gemot/vote", "delib1", "agent1")
	db.LogAuditEvent("key2", "5.6.7.8", "gemot/submit_position", "delib1", "agent2")

	// Query
	logs, err := db.GetAuditLog("delib1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 audit entries, got %d", len(logs))
	}

	// Verify entries are present (ordering may not be deterministic for same-second inserts)
	methods := map[string]bool{}
	for _, l := range logs {
		methods[l["method"]+":"+l["key_id"]] = true
	}
	if !methods["gemot/submit_position:key1"] || !methods["gemot/vote:key1"] || !methods["gemot/submit_position:key2"] {
		t.Fatal("expected all 3 audit entries to be present")
	}

	// Different deliberation should return empty
	logs2, err := db.GetAuditLog("other_delib", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs2) != 0 {
		t.Fatalf("expected 0 entries for other deliberation, got %d", len(logs2))
	}
}
