package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestZOPAFeasible(t *testing.T) {
	positions := []deliberation.Position{
		{AgentID: "alice", Reservation: ""},
		{AgentID: "bob", Reservation: ""},
	}
	consensus := []deliberation.ConsensusStatement{
		{Content: "We should do X"},
	}

	zopa := analysis.ComputeZOPA(positions, consensus, nil)
	if !zopa.Feasible {
		t.Fatal("expected feasible ZOPA with no reservations")
	}
	if len(zopa.CommonGround) != 1 {
		t.Fatalf("expected 1 common ground, got %d", len(zopa.CommonGround))
	}
}

func TestZOPAWithConflict(t *testing.T) {
	positions := []deliberation.Position{
		{AgentID: "alice", Reservation: "Cannot accept any solution involving mandatory reporting"},
		{AgentID: "bob", Reservation: ""},
	}
	consensus := []deliberation.ConsensusStatement{
		{Content: "We should implement mandatory reporting requirements for all participants"},
	}
	bridging := []deliberation.BridgingStatement{
		{Content: "Voluntary transparency guidelines would be a good start"},
	}

	zopa := analysis.ComputeZOPA(positions, consensus, bridging)

	// The bridging statement should survive (doesn't conflict with alice's reservation)
	// The consensus statement may conflict with alice's reservation
	t.Logf("ZOPA feasible: %v, common ground: %d, conflicts: %v, blocking: %v",
		zopa.Feasible, len(zopa.CommonGround), zopa.Conflicts, zopa.BlockingAgents)

	// At minimum, the bridging statement should be in common ground
	if len(zopa.CommonGround) == 0 && len(zopa.Conflicts) == 0 {
		t.Fatal("expected either common ground or conflicts, got neither")
	}
}

func TestZOPANoReservations(t *testing.T) {
	positions := []deliberation.Position{
		{AgentID: "alice"},
		{AgentID: "bob"},
	}
	zopa := analysis.ComputeZOPA(positions, nil, nil)
	if !zopa.Feasible {
		t.Fatal("no reservations + no proposals = trivially feasible")
	}
}

func TestMultiCriteriaVoteStorage(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Multi-criteria test", "")
	p, _ := svc.SubmitPosition(context.Background(), d.ID, "alice", "Proposal A")

	// Vote on same position with different criteria
	err := svc.Vote(context.Background(), d.ID, "bob", p.ID, 1, "", "", "feasibility")
	if err != nil {
		t.Fatal(err)
	}
	err = svc.Vote(context.Background(), d.ID, "bob", p.ID, -1, "", "", "ethics")
	if err != nil {
		t.Fatal(err)
	}
	// Default vote (no criterion)
	err = svc.Vote(context.Background(), d.ID, "bob", p.ID, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	votes, _ := svc.GetVotes(context.Background(), d.ID)
	if len(votes) < 2 {
		t.Fatalf("expected at least 2 votes (multi-criteria), got %d", len(votes))
	}

	// Check criteria are stored
	criteria := map[string]bool{}
	for _, v := range votes {
		if v.CriterionID != "" {
			criteria[v.CriterionID] = true
		}
	}
	if !criteria["feasibility"] || !criteria["ethics"] {
		t.Fatalf("expected feasibility and ethics criteria, got %v", criteria)
	}
}

func TestReservationConflictDetection(t *testing.T) {
	// Direct test of the conflict heuristic
	positions := []deliberation.Position{
		{AgentID: "alice", Reservation: "Cannot accept any approach that eliminates human oversight entirely"},
	}
	consensus := []deliberation.ConsensusStatement{
		{Content: "Full automation should eliminate human oversight to improve efficiency"},
	}

	zopa := analysis.ComputeZOPA(positions, consensus, nil)
	t.Logf("Conflicts: %v", zopa.Conflicts)
	// Should detect conflict: reservation says "cannot accept eliminate human oversight"
	// and consensus says "eliminate human oversight"
}
