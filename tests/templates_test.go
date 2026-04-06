package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestListTemplates(t *testing.T) {
	templates := deliberation.ListTemplates()
	if len(templates) != 8 {
		t.Fatalf("expected 8 templates, got %d", len(templates))
	}
	// Verify sorted alphabetically
	for i := 1; i < len(templates); i++ {
		if templates[i].Name < templates[i-1].Name {
			t.Fatalf("templates not sorted: %s before %s", templates[i-1].Name, templates[i].Name)
		}
	}
}

func TestGetTemplate(t *testing.T) {
	for _, name := range []string{"assembly", "sortition", "parliament", "jury", "consensus", "negotiation", "review", "roberts_rules"} {
		tmpl, ok := deliberation.GetTemplate(name)
		if !ok {
			t.Fatalf("template %q not found", name)
		}
		if tmpl.Name != name {
			t.Fatalf("expected name %q, got %q", name, tmpl.Name)
		}
		if tmpl.Description == "" {
			t.Fatalf("template %q has empty description", name)
		}
		if tmpl.AnalysisHint == "" {
			t.Fatalf("template %q has empty analysis hint", name)
		}
		if tmpl.SuggestedThreshold <= 0 || tmpl.SuggestedThreshold > 1.0 {
			t.Fatalf("template %q has invalid threshold %f", name, tmpl.SuggestedThreshold)
		}
	}
}

func TestGetTemplateNotFound(t *testing.T) {
	_, ok := deliberation.GetTemplate("nonexistent")
	if ok {
		t.Fatal("expected not found for nonexistent template")
	}
}

func TestCreateDeliberationWithTemplate(t *testing.T) {
	svc, _ := newTestService(t)

	d, err := svc.CreateDeliberation("Test", "Testing templates",
		deliberation.WithTemplate("jury"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if d.Template != "jury" {
		t.Fatalf("expected template 'jury', got %q", d.Template)
	}
	if d.Type != "reasoning" {
		t.Fatalf("jury template should set type to 'reasoning', got %q", d.Type)
	}
	if d.MaxParticipants != 12 {
		t.Fatalf("jury template should set max_participants to 12, got %d", d.MaxParticipants)
	}
}

func TestTemplateDefaultsOverriddenByExplicit(t *testing.T) {
	svc, _ := newTestService(t)

	d, err := svc.CreateDeliberation("Test", "Testing overrides",
		deliberation.WithTemplate("jury"),
		deliberation.WithMaxParticipants(6),
		deliberation.WithType("knowledge"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if d.Template != "jury" {
		t.Fatalf("expected template 'jury', got %q", d.Template)
	}
	// Explicit params should override template defaults
	if d.MaxParticipants != 6 {
		t.Fatalf("explicit max_participants should override template, got %d", d.MaxParticipants)
	}
	if d.Type != "knowledge" {
		t.Fatalf("explicit type should override template, got %q", d.Type)
	}
}

func TestCreateDeliberationWithUnknownTemplate(t *testing.T) {
	svc, _ := newTestService(t)

	_, err := svc.CreateDeliberation("Test", "Bad template",
		deliberation.WithTemplate("nonexistent"),
	)
	if err == nil {
		t.Fatal("expected error for unknown template")
	}
}

func TestSetTemplate(t *testing.T) {
	svc, _ := newTestService(t)

	d, err := svc.CreateDeliberation("Test", "Change template")
	if err != nil {
		t.Fatal(err)
	}

	// Creator can change template (empty keyID since no auth in test)
	err = svc.SetTemplate(d.ID, "parliament", "")
	if err != nil {
		t.Fatal(err)
	}

	d2, err := svc.GetDeliberation(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Template != "parliament" {
		t.Fatalf("expected template 'parliament', got %q", d2.Template)
	}
}

func TestTemplateRulesApplied(t *testing.T) {
	svc, _ := newTestService(t)

	d, err := svc.CreateDeliberation("Test", "Rules from template",
		deliberation.WithTemplate("parliament"),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Parliament template should set cooling_period_minutes and min_participants
	minP := deliberation.RuleInt(d, "min_participants", 0)
	if minP != 5 {
		t.Fatalf("parliament template should set min_participants=5, got %d", minP)
	}
	cooling := deliberation.RuleInt(d, "cooling_period_minutes", 0)
	if cooling != 60 {
		t.Fatalf("parliament template should set cooling_period_minutes=60, got %d", cooling)
	}
	posCost := deliberation.RuleInt(d, "position_cost", 0)
	if posCost != 5 {
		t.Fatalf("parliament template should set position_cost=5, got %d", posCost)
	}
}

func TestExplicitRulesOverrideTemplate(t *testing.T) {
	svc, _ := newTestService(t)

	d, err := svc.CreateDeliberation("Test", "Override rules",
		deliberation.WithTemplate("parliament"),
		deliberation.WithRules(map[string]any{"min_participants": 2}),
	)
	if err != nil {
		t.Fatal(err)
	}

	minP := deliberation.RuleInt(d, "min_participants", 0)
	if minP != 2 {
		t.Fatalf("explicit rule should override template, got min_participants=%d", minP)
	}
	// Non-overridden rules should still come from template
	cooling := deliberation.RuleInt(d, "cooling_period_minutes", 0)
	if cooling != 60 {
		t.Fatalf("non-overridden cooling_period should still be 60, got %d", cooling)
	}
}

func TestTemplatePersistsRoundTrip(t *testing.T) {
	svc, _ := newTestService(t)

	d, err := svc.CreateDeliberation("Test", "Round trip",
		deliberation.WithTemplate("consensus"),
	)
	if err != nil {
		t.Fatal(err)
	}

	d2, err := svc.GetDeliberation(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Template != "consensus" {
		t.Fatalf("template should persist: expected 'consensus', got %q", d2.Template)
	}
	if d2.Type != "negotiation" {
		t.Fatalf("type from template should persist: expected 'negotiation', got %q", d2.Type)
	}
}

func TestSetTemplateAppliesDefaultRules(t *testing.T) {
	svc, _ := newTestService(t)

	// Create with no template
	d, err := svc.CreateDeliberation("Test", "No template initially")
	if err != nil {
		t.Fatal(err)
	}

	// Rules should be empty
	minP := deliberation.RuleInt(d, "min_participants", 0)
	if minP != 0 {
		t.Fatalf("expected no min_participants, got %d", minP)
	}

	// Apply parliament template
	if err := svc.SetTemplate(d.ID, "parliament", ""); err != nil {
		t.Fatal(err)
	}

	// Reload and check rules
	d2, err := svc.GetDeliberation(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Template != "parliament" {
		t.Fatalf("expected template 'parliament', got %q", d2.Template)
	}
	minP = deliberation.RuleInt(d2, "min_participants", 0)
	if minP != 5 {
		t.Fatalf("set_template should apply parliament's min_participants=5, got %d", minP)
	}
	cooling := deliberation.RuleInt(d2, "cooling_period_minutes", 0)
	if cooling != 60 {
		t.Fatalf("set_template should apply parliament's cooling_period=60, got %d", cooling)
	}
}

func TestSetTemplateReplacesRulesFromOldTemplate(t *testing.T) {
	svc, _ := newTestService(t)

	// Create with explicit rule
	d, err := svc.CreateDeliberation("Test", "Template switch",
		deliberation.WithRules(map[string]any{"min_participants": 2}),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Apply parliament template — should replace ALL rules with parliament's defaults
	if err := svc.SetTemplate(d.ID, "parliament", ""); err != nil {
		t.Fatal(err)
	}

	d2, err := svc.GetDeliberation(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Parliament's min_participants=5 should replace the explicit 2
	minP := deliberation.RuleInt(d2, "min_participants", 0)
	if minP != 5 {
		t.Fatalf("parliament min_participants=5 should replace prior value, got %d", minP)
	}
	// Parliament's cooling_period should be set
	cooling := deliberation.RuleInt(d2, "cooling_period_minutes", 0)
	if cooling != 60 {
		t.Fatalf("parliament cooling_period=60 should be set, got %d", cooling)
	}
}

func TestCoolingPeriodEnforcement(t *testing.T) {
	svc, db := newTestService(t)

	d, err := svc.CreateDeliberation("Cooling Test", "Testing cooling period",
		deliberation.WithRules(map[string]any{
			"min_participants":       2,
			"cooling_period_minutes": 60,
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Submit positions from 2 agents
	svc.SubmitPosition(d.ID, "agent1", "Position A")
	svc.SubmitPosition(d.ID, "agent2", "Position B")
	positions, _ := svc.GetPositions(d.ID, nil, nil)
	for _, voter := range []string{"agent1", "agent2"} {
		for _, p := range positions {
			if p.AgentID != voter {
				svc.Vote(d.ID, voter, p.ID, 1)
			}
		}
	}

	// First analysis should work (cooling period only applies after round 1)
	_, err = svc.Analyze(context.Background(), d.ID)
	// Will fail at LLM call, but should NOT fail at cooling period
	if err != nil && strings.Contains(err.Error(), "cooling period") {
		t.Fatalf("first analysis should not be blocked by cooling period: %v", err)
	}

	// Manually set status_changed_at to 5 minutes ago (within the 60-min cooling period)
	db.RawDB().Exec(`UPDATE deliberations SET status_changed_at = NOW() - INTERVAL '5 minutes', status = 'open', round_number = 2 WHERE id = $1`, d.ID)

	// Submit more positions for round 2
	svc.SubmitPosition(d.ID, "agent1", "Position C round 2")
	svc.SubmitPosition(d.ID, "agent2", "Position D round 2")

	// Second analysis should be blocked by cooling period
	_, err = svc.Analyze(context.Background(), d.ID)
	if err == nil {
		t.Fatal("expected cooling period error")
	}
	if !strings.Contains(err.Error(), "cooling period") {
		t.Fatalf("expected 'cooling period' error, got: %v", err)
	}
}
