package store

import (
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
)

// TestAggregateEdgesSumsDuplicates verifies the dedup that prevents Postgres
// SQLSTATE 21000 ("ON CONFLICT DO UPDATE command cannot affect row a second
// time"): duplicate (from, to) pairs must collapse to one row with summed
// weight, and non-positive weights must be dropped.
func TestAggregateEdgesSumsDuplicates(t *testing.T) {
	edges := []analysis.Edge{
		{From: "a", To: "b", Weight: 1.5},
		{From: "a", To: "b", Weight: 2.0}, // duplicate pair -> summed
		{From: "b", To: "c", Weight: 1.0},
		{From: "x", To: "y", Weight: 0},  // dropped (non-positive)
		{From: "x", To: "y", Weight: -3}, // dropped
	}
	froms, tos, weights := aggregateEdges(edges)
	if len(froms) != 2 || len(tos) != 2 || len(weights) != 2 {
		t.Fatalf("expected 2 aggregated rows, got froms=%d tos=%d weights=%d", len(froms), len(tos), len(weights))
	}
	got := map[[2]string]float64{}
	for i := range froms {
		key := [2]string{froms[i], tos[i]}
		if _, dup := got[key]; dup {
			t.Fatalf("duplicate pair %v in output — dedup failed, would trigger SQLSTATE 21000", key)
		}
		got[key] = weights[i]
	}
	if w := got[[2]string{"a", "b"}]; w != 3.5 {
		t.Errorf("a->b weight = %v, want 3.5 (summed)", w)
	}
	if w := got[[2]string{"b", "c"}]; w != 1.0 {
		t.Errorf("b->c weight = %v, want 1.0", w)
	}
	if _, ok := got[[2]string{"x", "y"}]; ok {
		t.Error("non-positive pair x->y should have been dropped")
	}
}

func TestAggregateEdgesEmpty(t *testing.T) {
	froms, tos, weights := aggregateEdges(nil)
	if len(froms) != 0 || len(tos) != 0 || len(weights) != 0 {
		t.Errorf("expected empty result, got %d rows", len(froms))
	}
}
