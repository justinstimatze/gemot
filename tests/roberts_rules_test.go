package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestRobertsRulesRequireSecond(t *testing.T) {
	svc, db := newTestService(t)

	// Create deliberation with roberts_rules template
	d, err := svc.CreateDeliberation("Budget Proposal", "Allocate funds for Q3",
		deliberation.WithTemplate("roberts_rules"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if d.Template != "roberts_rules" {
		t.Fatalf("expected template roberts_rules, got %s", d.Template)
	}
	if !deliberation.RuleBool(d, "require_second", false) {
		t.Fatal("expected require_second rule to be true")
	}

	// Submit a motion — should start as draft
	pos, err := svc.SubmitPosition(d.ID, "agent-proposer", "I move to allocate $50k to research")
	if err != nil {
		t.Fatal(err)
	}
	if !pos.Draft {
		t.Fatal("expected position to be a draft (motion awaiting second)")
	}

	// Verify the position is not visible in GetPositions (drafts are filtered)
	positions, err := svc.GetPositions(d.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 0 {
		t.Fatalf("expected 0 visible positions before second, got %d", len(positions))
	}

	// Vote +1 from a different agent — should "second" the motion (publish it)
	err = svc.Vote(d.ID, "agent-seconder", pos.ID, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the position is now published
	published, err := db.GetPositionByID(context.Background(), pos.ID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Draft {
		t.Fatal("expected position to be published after being seconded")
	}

	// Verify it now appears in GetPositions
	positions, err = svc.GetPositions(d.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 {
		t.Fatalf("expected 1 visible position after second, got %d", len(positions))
	}

	// Submit another motion — no second — stays draft
	pos2, err := svc.SubmitPosition(d.ID, "agent-other", "I move to table the discussion")
	if err != nil {
		t.Fatal(err)
	}
	if !pos2.Draft {
		t.Fatal("expected second position to be a draft")
	}

	// Only the first (seconded) position should be visible
	positions, err = svc.GetPositions(d.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 {
		t.Fatalf("expected 1 visible position (unseonded stays draft), got %d", len(positions))
	}

	// Self-second should NOT publish (proposer voting on their own motion)
	err = svc.Vote(d.ID, "agent-other", pos2.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	selfSecondCheck, err := db.GetPositionByID(context.Background(), pos2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !selfSecondCheck.Draft {
		t.Fatal("expected self-second to NOT publish the motion")
	}
}

func TestRobertsRulesSpeakingLimit(t *testing.T) {
	svc, _ := newTestService(t)

	d, err := svc.CreateDeliberation("Speaking Limit Test", "Test character limits",
		deliberation.WithTemplate("roberts_rules"),
	)
	if err != nil {
		t.Fatal(err)
	}

	limit := deliberation.RuleInt(d, "speaking_time_limit", 0)
	if limit != 500 {
		t.Fatalf("expected speaking_time_limit 500, got %d", limit)
	}

	// Submit position exceeding the limit
	longContent := strings.Repeat("x", 501)
	_, err = svc.SubmitPosition(d.ID, "agent-verbose", longContent)
	if err == nil {
		t.Fatal("expected error for content exceeding speaking time limit")
	}
	if !strings.Contains(err.Error(), "speaking time limit") {
		t.Fatalf("expected speaking time limit error, got: %v", err)
	}

	// Submit position within the limit
	okContent := strings.Repeat("x", 500)
	pos, err := svc.SubmitPosition(d.ID, "agent-concise", okContent)
	if err != nil {
		t.Fatalf("expected success for content within limit, got: %v", err)
	}
	if pos == nil {
		t.Fatal("expected non-nil position")
	}
}

func TestRobertsRulesAmendment(t *testing.T) {
	svc, db := newTestService(t)

	d, err := svc.CreateDeliberation("Amendment Test", "Test amendments to motions",
		deliberation.WithTemplate("roberts_rules"),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Submit original motion
	motion, err := svc.SubmitPosition(d.ID, "agent-proposer", "I move to adopt policy X")
	if err != nil {
		t.Fatal(err)
	}

	// Second the motion
	err = svc.Vote(d.ID, "agent-seconder", motion.ID, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Submit amendment referencing the original motion
	amendment, err := svc.SubmitPosition(d.ID, "agent-amender",
		"I move to amend: add clause Y to policy X",
		deliberation.WithParentPosition(motion.ID),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Amendment should also be a draft (needs seconding)
	if !amendment.Draft {
		t.Fatal("expected amendment to be a draft awaiting second")
	}

	// Verify parent_position_id is set
	if amendment.ParentPositionID != motion.ID {
		t.Fatalf("expected parent_position_id %s, got %s", motion.ID, amendment.ParentPositionID)
	}

	// Second the amendment
	err = svc.Vote(d.ID, "agent-seconder", amendment.ID, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Verify amendment is now published
	publishedAmendment, err := db.GetPositionByID(context.Background(), amendment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if publishedAmendment.Draft {
		t.Fatal("expected amendment to be published after being seconded")
	}
	if publishedAmendment.ParentPositionID != motion.ID {
		t.Fatalf("expected parent_position_id %s persisted, got %s", motion.ID, publishedAmendment.ParentPositionID)
	}

	// Get all visible positions — should be both the motion and its amendment
	positions, err := svc.GetPositions(d.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 2 {
		t.Fatalf("expected 2 visible positions (motion + amendment), got %d", len(positions))
	}

	// Verify one of them has the parent set
	foundAmendment := false
	for _, p := range positions {
		if p.ParentPositionID == motion.ID {
			foundAmendment = true
		}
	}
	if !foundAmendment {
		t.Fatal("expected to find amendment with parent_position_id in GetPositions results")
	}
}
