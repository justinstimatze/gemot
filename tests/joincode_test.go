package tests

import (
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestJoinCodeLifecycle(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation("Join code test", "")

	// Generate a join code
	jc, err := svc.GenerateJoinCode(d.ID, "contributor", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if jc.Code == "" || len(jc.Code) < 10 {
		t.Fatalf("expected memorable code like 'bold-cedar-123456', got %q", jc.Code)
	}
	if jc.Role != "contributor" {
		t.Fatalf("expected role contributor, got %q", jc.Role)
	}
	t.Logf("Join code: %s (expires: %s)", jc.Code, jc.ExpiresAt.Format(time.RFC3339))

	// Claim the code
	deliberationID, role, err := svc.JoinDeliberation(jc.Code, "pr-author-agent")
	if err != nil {
		t.Fatal(err)
	}
	if deliberationID != d.ID {
		t.Fatalf("expected deliberation %s, got %s", d.ID, deliberationID)
	}
	if role != "contributor" {
		t.Fatalf("expected role contributor, got %q", role)
	}

	// Can't reuse the code
	_, _, err = svc.JoinDeliberation(jc.Code, "another-agent")
	if err == nil {
		t.Fatal("expected error on code reuse")
	}
}

func TestJoinCodeExpiry(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation("Expiry test", "")

	// Generate a code that expires immediately
	jc, _ := svc.GenerateJoinCode(d.ID, "reviewer", 1*time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	_, _, err := svc.JoinDeliberation(jc.Code, "late-agent")
	if err == nil {
		t.Fatal("expected error on expired code")
	}
}

func TestJoinCodeInvalidCode(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	_, _, err := svc.JoinDeliberation("DEADBEEF", "agent")
	if err == nil {
		t.Fatal("expected error on invalid code")
	}
}

func TestJoinCodeContributorCanParticipate(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation("PR review test", "", deliberation.WithVisibility("private"))

	// Project agent submits review
	svc.SubmitPosition(d.ID, "review-agent", "This PR has a potential SQL injection risk in the query builder")

	// Generate join code for contributor
	jc, _ := svc.GenerateJoinCode(d.ID, "contributor", time.Hour)

	// Contributor joins and argues back
	svc.JoinDeliberation(jc.Code, "pr-author")

	// Contributor submits their response
	_, err := svc.SubmitPosition(d.ID, "pr-author", "The query builder uses parameterized queries — see line 42. The input is validated at the handler level before reaching the builder.")
	if err != nil {
		t.Fatalf("contributor should be able to submit position after joining: %v", err)
	}

	// Both can vote
	positions, _ := svc.GetPositions(d.ID, nil, nil)
	if len(positions) != 2 {
		t.Fatalf("expected 2 positions, got %d", len(positions))
	}
}
