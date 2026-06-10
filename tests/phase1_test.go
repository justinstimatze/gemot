package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
)

func TestConvictionWeights(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Conviction test", "")
	p1, _ := svc.SubmitPosition(context.Background(), d.ID, "alice", "Strong opinion", deliberation.WithConviction(0.9))
	p2, _ := svc.SubmitPosition(context.Background(), d.ID, "bob", "Weak opinion", deliberation.WithConviction(0.2))

	positions, _ := svc.GetPositions(context.Background(), d.ID, nil, nil)
	for _, p := range positions {
		if p.ID == p1.ID && p.Conviction != 0.9 {
			t.Fatalf("expected conviction 0.9, got %f", p.Conviction)
		}
		if p.ID == p2.ID && p.Conviction != 0.2 {
			t.Fatalf("expected conviction 0.2, got %f", p.Conviction)
		}
	}
}

func TestConvictionClamped(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Clamp test", "")
	p, _ := svc.SubmitPosition(context.Background(), d.ID, "alice", "Over 9000", deliberation.WithConviction(5.0))
	if p.Conviction > 1.0 {
		t.Fatalf("conviction should be clamped to 1.0, got %f", p.Conviction)
	}

	p2, _ := svc.SubmitPosition(context.Background(), d.ID, "bob", "Negative", deliberation.WithConviction(-3.0))
	if p2.Conviction < 0.0 {
		t.Fatalf("conviction should be clamped to 0.0, got %f", p2.Conviction)
	}
}

func TestReservationValues(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Reservation test", "")
	svc.SubmitPosition(context.Background(), d.ID, "alice", "I want X",
		deliberation.WithReservation("Cannot accept anything less than 60% of budget"))

	positions, _ := svc.GetPositions(context.Background(), d.ID, nil, nil)
	if positions[0].Reservation == "" {
		t.Fatal("expected reservation to be set")
	}
	if positions[0].Reservation != "Cannot accept anything less than 60% of budget" {
		t.Fatalf("reservation mismatch: %q", positions[0].Reservation)
	}
}

func TestOnBehalfOf(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Principal test", "")
	svc.SubmitPosition(context.Background(), d.ID, "alice-agent", "Position for Alice Corp",
		deliberation.WithOnBehalfOf("Alice Corp"))

	positions, _ := svc.GetPositions(context.Background(), d.ID, nil, nil)
	if positions[0].OnBehalfOf != "Alice Corp" {
		t.Fatalf("expected on_behalf_of 'Alice Corp', got %q", positions[0].OnBehalfOf)
	}
}

func TestCommitEmptyStatement(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Commit validation test", "")
	for _, statement := range []string{"", "   "} {
		if _, err := svc.Commit(context.Background(), d.ID, "alice", statement, ""); err == nil {
			t.Fatalf("expected error for statement %q, got nil", statement)
		}
	}
}

func TestCommitBasic(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Commit test", "")
	svc.SubmitPosition(context.Background(), d.ID, "alice", "A")
	svc.SubmitPosition(context.Background(), d.ID, "bob", "B")
	svc.Analyze(context.Background(), d.ID)

	// Alice commits
	c, err := svc.Commit(context.Background(), d.ID, "alice", "I accept the compromise on safety standards", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" {
		t.Fatal("expected commitment ID")
	}
	if c.Status != "active" {
		t.Fatalf("expected active status for unconditional commitment, got %q", c.Status)
	}

	// Check commitments
	commitments, _ := svc.GetCommitments(context.Background(), d.ID)
	if len(commitments) != 1 {
		t.Fatalf("expected 1 commitment, got %d", len(commitments))
	}
}

func TestCommitConditional(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Conditional commit", "")
	svc.SubmitPosition(context.Background(), d.ID, "alice", "A")
	svc.SubmitPosition(context.Background(), d.ID, "bob", "B")
	svc.Analyze(context.Background(), d.ID)

	c, err := svc.Commit(context.Background(), d.ID, "alice", "I accept X", "if bob also commits to Y")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "pending" {
		t.Fatalf("conditional commitment should be pending, got %q", c.Status)
	}
}

func TestKeyIDDerivation(t *testing.T) {
	// Same key always produces same ID
	k1 := payments.KeyID("gmt_abc123def456")
	k2 := payments.KeyID("gmt_abc123def456")
	if k1 != k2 {
		t.Fatal("same key should produce same ID")
	}
	if len(k1) != 16 {
		t.Fatalf("expected 16-char key_id, got %d", len(k1))
	}

	// Different keys produce different IDs
	k3 := payments.KeyID("gmt_xyz789ghi012")
	if k1 == k3 {
		t.Fatal("different keys should produce different IDs")
	}

	// Empty key returns empty
	if payments.KeyID("") != "" {
		t.Fatal("empty key should return empty ID")
	}
}

func TestVisibilityValidation(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	// Valid visibilities
	for _, vis := range []string{"open", "private", "link"} {
		d, err := svc.CreateDeliberation(context.Background(), "Vis "+vis, "", deliberation.WithVisibility(vis))
		if err != nil {
			t.Fatalf("valid visibility %q rejected: %v", vis, err)
		}
		if d.Visibility != vis {
			t.Fatalf("expected %q, got %q", vis, d.Visibility)
		}
	}

	// Invalid visibility
	_, err := svc.CreateDeliberation(context.Background(), "Bad vis", "", deliberation.WithVisibility("secret"))
	if err == nil {
		t.Fatal("expected error for invalid visibility")
	}
}

func TestMaxParticipants(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Cap test", "", deliberation.WithMaxParticipants(2))

	svc.SubmitPosition(context.Background(), d.ID, "alice", "A")
	svc.SubmitPosition(context.Background(), d.ID, "bob", "B")

	// Third agent should be rejected
	_, err := svc.SubmitPosition(context.Background(), d.ID, "carol", "C")
	if err == nil {
		t.Fatal("expected error for exceeding max_participants")
	}

	// Existing agent can still submit
	_, err = svc.SubmitPosition(context.Background(), d.ID, "alice", "A revised")
	if err != nil {
		t.Fatalf("existing participant should still be able to submit: %v", err)
	}
}
