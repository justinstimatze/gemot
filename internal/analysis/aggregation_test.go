package analysis

import (
	"math"
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestTrimmedMean_TrimsExtremes(t *testing.T) {
	// 10 samples: eight 0s + two +2 outliers. Raw mean = 0.4; trim 10%
	// drops one +2 and one 0 → seven 0s + one +2, mean = 2/8 = 0.25.
	vals := []float64{0, 0, 0, 0, 0, 0, 0, 0, 2, 2}
	if got := trimmedMean(vals, 0.1); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("trimmedMean = %v, want 0.25", got)
	}
}

func TestTrimmedMean_FallbackToMedianWhenTrimTooAggressive(t *testing.T) {
	// trim=0.5 on 3 samples would discard everything; falls back to median.
	if got := trimmedMean([]float64{-2, 0, 2}, 0.5); got != 0 {
		t.Errorf("aggressive trim = %v, want median 0", got)
	}
}

func TestTrimmedMean_FallbackToMeanWhenSmall(t *testing.T) {
	// 5 samples with trim=0.1 ⇒ k=0; returns regular mean.
	if got := trimmedMean([]float64{1, 2, 3, 4, 5}, 0.1); math.Abs(got-3.0) > 1e-9 {
		t.Errorf("small-pool mean = %v, want 3", got)
	}
}

func TestTrimmedMean_Empty(t *testing.T) {
	if got := trimmedMean(nil, 0.1); got != 0 {
		t.Errorf("empty input = %v, want 0", got)
	}
}

// validateAggregationStability only fires on genuinely extreme skew
// (|raw - trimmed| >= 0.5 on the -2..+2 scale). The bounded value
// range + low threshold means a small coordinated minority can't
// easily push the warning; it's a high-signal low-recall detector.
// These tests pin the contract: skip small cohorts, don't false-flag
// sybil-majority cases (where trimming can't rescue you from an
// actual majority vote).
func TestValidateAggregationStability_SkipsSmallCohorts(t *testing.T) {
	votes := []deliberation.Vote{
		{PositionID: "P1", AgentID: "a", Value: 2},
		{PositionID: "P1", AgentID: "b", Value: -2},
		{PositionID: "P1", AgentID: "c", Value: 0},
		{PositionID: "P1", AgentID: "d", Value: 0},
		{PositionID: "P1", AgentID: "e", Value: 0},
	}
	positions := []deliberation.Position{{ID: "P1", AgentID: "author"}}
	if warnings := validateAggregationStability(votes, positions); len(warnings) != 0 {
		t.Errorf("small cohort must not emit warnings; got %v", warnings)
	}
}

func TestValidateAggregationStability_NoWarningWhenSybilIsMajority(t *testing.T) {
	// 4 honest at 0 + 6 sybil at +2. Trimming doesn't rescue majority
	// capture — raw and trimmed means are close. Correct behavior:
	// don't flag, because the signal is a genuine vote.
	var votes []deliberation.Vote
	for i := 0; i < 4; i++ {
		votes = append(votes, deliberation.Vote{PositionID: "P", AgentID: "h", Value: 0})
	}
	for i := 0; i < 6; i++ {
		votes = append(votes, deliberation.Vote{PositionID: "P", AgentID: "s", Value: 2})
	}
	positions := []deliberation.Position{{ID: "P"}}
	if warnings := validateAggregationStability(votes, positions); len(warnings) != 0 {
		t.Errorf("sybil majority should not false-flag as drift; got %v", warnings)
	}
}

func TestValidateAggregationStability_FiresOnSingleOutlierPull(t *testing.T) {
	// 9 neutral voters + 1 Sybil at +2. Raw = 0.2, trimmed (drops +2
	// and one 0) = 0. Diff = 0.2 — at the calibrated threshold. This
	// is the canonical "coordinated minority at an extreme pulls the
	// aggregate" signal.
	var votes []deliberation.Vote
	for i := 0; i < 9; i++ {
		votes = append(votes, deliberation.Vote{PositionID: "P", AgentID: "h", Value: 0})
	}
	votes = append(votes, deliberation.Vote{PositionID: "P", AgentID: "sybil", Value: 2})
	positions := []deliberation.Position{{ID: "P"}}
	warnings := validateAggregationStability(votes, positions)
	found := false
	for _, w := range warnings {
		if strings.HasPrefix(w, "AGGREGATION_DRIFT:") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("single outlier in 10-cohort should emit AGGREGATION_DRIFT; got %v", warnings)
	}
}
