package analysis

import (
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestValidateLowEffortPositions_AbsoluteFloor(t *testing.T) {
	positions := []deliberation.Position{
		{AgentID: "alice"}, {AgentID: "bob"}, {AgentID: "carol"},
	}
	claims := []claim{
		{AgentID: "alice", Claim: "a1"},
		{AgentID: "bob", Claim: "b1"},
		{AgentID: "bob", Claim: "b2"},
		{AgentID: "bob", Claim: "b3"},
		{AgentID: "carol", Claim: "c1"},
		{AgentID: "carol", Claim: "c2"},
	}
	warnings := validateLowEffortPositions(positions, claims)
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning (alice), got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "LOW_EFFORT_ABS") || !strings.Contains(warnings[0], "alice") {
		t.Errorf("unexpected warning: %q", warnings[0])
	}
}

func TestValidateLowEffortPositions_MedianRelative(t *testing.T) {
	positions := []deliberation.Position{
		{AgentID: "alice"}, {AgentID: "bob"}, {AgentID: "carol"},
		{AgentID: "dave"}, {AgentID: "eve"},
	}
	var claims []claim
	// bob/carol/dave/eve each have 9 claims; alice has 2 (< 0.25 * median 9 = 2.25).
	for _, a := range []string{"bob", "carol", "dave", "eve"} {
		for i := 0; i < 9; i++ {
			claims = append(claims, claim{AgentID: a, Claim: "c"})
		}
	}
	claims = append(claims, claim{AgentID: "alice", Claim: "c1"}, claim{AgentID: "alice", Claim: "c2"})
	warnings := validateLowEffortPositions(positions, claims)
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "LOW_EFFORT_REL") || !strings.Contains(warnings[0], "alice") {
		t.Errorf("unexpected warning: %q", warnings[0])
	}
}

func TestValidateLowEffortPositions_SmallCohortNoRelative(t *testing.T) {
	// Median below 4 — relative rule must not fire even though alice has fewer claims.
	positions := []deliberation.Position{
		{AgentID: "alice"}, {AgentID: "bob"}, {AgentID: "carol"},
	}
	claims := []claim{
		{AgentID: "alice", Claim: "a1"}, {AgentID: "alice", Claim: "a2"},
		{AgentID: "bob", Claim: "b1"}, {AgentID: "bob", Claim: "b2"}, {AgentID: "bob", Claim: "b3"},
		{AgentID: "carol", Claim: "c1"}, {AgentID: "carol", Claim: "c2"}, {AgentID: "carol", Claim: "c3"},
	}
	if w := validateLowEffortPositions(positions, claims); len(w) != 0 {
		t.Errorf("expected no warnings for small cohort, got %v", w)
	}
}

func TestValidateLowEffortPositions_ZeroSkipped(t *testing.T) {
	// Agents with zero claims are covered by validateCoverage; they must not appear here.
	positions := []deliberation.Position{{AgentID: "ghost"}, {AgentID: "bob"}}
	claims := []claim{{AgentID: "bob", Claim: "b1"}, {AgentID: "bob", Claim: "b2"}}
	for _, w := range validateLowEffortPositions(positions, claims) {
		if strings.Contains(w, "ghost") {
			t.Errorf("ghost agent (zero claims) should not be flagged by low-effort: %q", w)
		}
	}
}

func TestValidateCruxProvenance_ThinSinglePosition(t *testing.T) {
	cruxes := []deliberation.Crux{
		{
			Claim:             "thin crux",
			SourcePositionIDs: []string{"p1"},
			SourceQuotes: []deliberation.SourceQuote{
				{PositionID: "p1", AgentID: "alice", Quote: "q1"},
			},
		},
	}
	warnings := validateCruxProvenance(cruxes)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "THIN_PROVENANCE") {
		t.Fatalf("want THIN_PROVENANCE warning, got %v", warnings)
	}
}

func TestValidateCruxProvenance_HealthyPasses(t *testing.T) {
	cruxes := []deliberation.Crux{
		{
			Claim:             "healthy crux",
			SourcePositionIDs: []string{"p1", "p2", "p3"},
			SourceQuotes: []deliberation.SourceQuote{
				{PositionID: "p1", AgentID: "alice", Quote: "q1"},
				{PositionID: "p2", AgentID: "bob", Quote: "q2"},
				{PositionID: "p3", AgentID: "carol", Quote: "q3"},
			},
		},
	}
	if w := validateCruxProvenance(cruxes); len(w) != 0 {
		t.Errorf("healthy crux should not warn, got %v", w)
	}
}

func TestValidateCruxProvenance_DegenerateIgnored(t *testing.T) {
	cruxes := []deliberation.Crux{
		{
			Claim:             "degenerate",
			Degenerate:        true,
			SourcePositionIDs: []string{"p1"},
		},
	}
	if w := validateCruxProvenance(cruxes); len(w) != 0 {
		t.Errorf("degenerate cruxes should be skipped (already surfaced elsewhere), got %v", w)
	}
}

func TestValidateCruxStability_Disabled(t *testing.T) {
	cruxes := []deliberation.Crux{{Claim: "c"}}
	if w := validateCruxStability(cruxes, 0, nil, nil); len(w) != 0 {
		t.Errorf("samples=0 must short-circuit, got %v", w)
	}
}

func TestValidateCruxStability_FlagsDivergence(t *testing.T) {
	cruxes := []deliberation.Crux{{Claim: "original"}}
	gen := func(_ deliberation.Crux, n int) ([]string, error) {
		out := make([]string, n)
		for i := range out {
			out[i] = "different"
		}
		return out, nil
	}
	judge := func(a, b string) (bool, error) { return a == b, nil }
	warnings := validateCruxStability(cruxes, 3, gen, judge)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "CRUX_INSTABILITY") {
		t.Fatalf("want CRUX_INSTABILITY warning, got %v", warnings)
	}
}

func TestValidateCruxStability_StablePasses(t *testing.T) {
	cruxes := []deliberation.Crux{{Claim: "original"}}
	gen := func(_ deliberation.Crux, n int) ([]string, error) {
		out := make([]string, n)
		for i := range out {
			out[i] = "original"
		}
		return out, nil
	}
	judge := func(a, b string) (bool, error) { return a == b, nil }
	if w := validateCruxStability(cruxes, 3, gen, judge); len(w) != 0 {
		t.Errorf("stable crux should not warn, got %v", w)
	}
}
