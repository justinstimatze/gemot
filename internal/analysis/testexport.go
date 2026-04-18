package analysis

import (
	"context"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

// CheckCrossFamilyConsistencyForTest exposes the unexported
// validateAnalysisModelConsistency method for tests in other packages
// (tests/cross_family_consistency_test.go). Production code calls the
// method indirectly via Analyze — this wrapper exists solely so the
// integration-style test can drive the drift check end-to-end without
// standing up a real primary LLM + Postgres.
func (a *TextAnalyzer) CheckCrossFamilyConsistencyForTest(
	ctx context.Context,
	cruxes []deliberation.Crux,
	positions []deliberation.Position,
) []string {
	return a.validateAnalysisModelConsistency(ctx, cruxes, positions)
}
