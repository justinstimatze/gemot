package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestInviteAgent(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation("Invite test", "")
	svc.SubmitPosition(d.ID, "alice", "Position A")
	svc.SubmitPosition(d.ID, "bob", "Position B")

	// Alice invites an expert
	inv, err := svc.InviteAgent(d.ID, "alice", "expert-agent", "expert", "Need domain expertise on safety evaluation")
	if err != nil {
		t.Fatal(err)
	}
	if inv.ID == "" || inv.Status != "pending" {
		t.Fatalf("unexpected invitation: %+v", inv)
	}
	if inv.Role != "expert" {
		t.Fatalf("expected role 'expert', got %q", inv.Role)
	}

	// Expert sees the invitation in their context (need analysis first)
	svc.Vote(d.ID, "alice", "pos1", 1) // need some votes for analysis
	svc.Analyze(context.Background(), d.ID)

	ctx, err := svc.GetContext(d.ID, "expert-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.PendingInvitations) != 1 {
		t.Fatalf("expected 1 pending invitation, got %d", len(ctx.PendingInvitations))
	}
	if ctx.PendingInvitations[0].InvitedBy != "alice" {
		t.Fatalf("expected invited_by alice, got %q", ctx.PendingInvitations[0].InvitedBy)
	}

	// Accept invitation
	err = svc.AcceptInvitation(inv.ID)
	if err != nil {
		t.Fatal(err)
	}

	// No longer pending after acceptance
	ctx2, _ := svc.GetContext(d.ID, "expert-agent")
	if len(ctx2.PendingInvitations) != 0 {
		t.Fatal("expected 0 pending invitations after acceptance")
	}
}

func TestInviteAgentInvalidRole(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation("Role test", "")
	_, err := svc.InviteAgent(d.ID, "alice", "bob", "dictator", "I want power")
	if err == nil {
		t.Fatal("expected error for invalid role")
	}
}

func TestDeliberationTypeValidation(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	// Valid types
	for _, typ := range []string{"reasoning", "knowledge", "negotiation", "policy"} {
		d, err := svc.CreateDeliberation("Test "+typ, "", deliberation.WithType(typ))
		if err != nil {
			t.Fatalf("valid type %q rejected: %v", typ, err)
		}
		if d.Type != typ {
			t.Fatalf("expected type %q, got %q", typ, d.Type)
		}
	}

	// Invalid type
	_, err := svc.CreateDeliberation("Bad type", "", deliberation.WithType("garbage"))
	if err == nil {
		t.Fatal("expected error for invalid type")
	}

	// Empty type is fine
	d, err := svc.CreateDeliberation("No type", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Type != "" {
		t.Fatalf("expected empty type, got %q", d.Type)
	}
}

func TestProposeCompromiseRequiresAnalysis(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation("Compromise test", "")
	svc.SubmitPosition(d.ID, "alice", "Position A")

	// No analysis yet — should fail
	_, err := svc.ProposeCompromise(context.Background(), d.ID)
	if err == nil {
		t.Fatal("expected error without analysis")
	}
}

func TestProposeCompromiseRequiresGenerator(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})
	// Don't set compromise generator

	d, _ := svc.CreateDeliberation("No generator", "")
	svc.SubmitPosition(d.ID, "alice", "A")
	svc.SubmitPosition(d.ID, "bob", "B")
	svc.Vote(d.ID, "alice", "bogus", 1) // ignored but needed
	svc.Analyze(context.Background(), d.ID)

	_, err := svc.ProposeCompromise(context.Background(), d.ID)
	if err == nil {
		t.Fatal("expected error without compromise generator")
	}
}

func TestDiversityNudgeMinority(t *testing.T) {
	db := tempDB(t)
	// Analyzer that puts alice alone in disagree
	analyzer := &funcAnalyzer{fn: func(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
		return &deliberation.AnalysisResult{
			Clusters: []deliberation.OpinionCluster{
				{ID: 0, AgentIDs: agents, Size: len(agents)},
			},
			Cruxes: []deliberation.Crux{
				{
					Claim:            "Test crux",
					AgreeAgents:      agents[1:], // everyone except first
					DisagreeAgents:   agents[:1], // first agent alone
					ControversyScore: 0.8,
				},
			},
			ConsensusStatements: []deliberation.ConsensusStatement{},
			AgentCount:          len(agents),
			PositionCount:       len(positions),
			VoteCount:           len(votes),
		}, nil
	}}

	svc := deliberation.NewService(db, analyzer)
	d, _ := svc.CreateDeliberation("Nudge test", "")
	svc.SubmitPosition(d.ID, "alice", "Minority view")
	svc.SubmitPosition(d.ID, "bob", "Majority view")
	svc.SubmitPosition(d.ID, "carol", "Majority view too")
	svc.Analyze(context.Background(), d.ID)

	ctx, err := svc.GetContext(d.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.DiversityNudge == "" {
		t.Fatal("expected diversity nudge for minority agent")
	}
	t.Logf("Nudge: %s", ctx.DiversityNudge)

	// Majority agent should get a different nudge or none
	ctx2, _ := svc.GetContext(d.ID, "bob")
	if ctx2.DiversityNudge == ctx.DiversityNudge {
		t.Fatal("majority agent should get different nudge than minority")
	}
}

func TestGetInvitationsEmpty(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation("Empty invites", "")
	invs, err := svc.GetInvitations(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 0 {
		t.Fatalf("expected 0 invitations, got %d", len(invs))
	}
}
