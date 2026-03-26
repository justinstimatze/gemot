package tests

import (
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestCorrelationDiscounting(t *testing.T) {
	agents := []string{"alice", "bob", "carol"}

	// alice and bob vote identically, carol votes differently
	votes := []deliberation.Vote{
		{AgentID: "alice", PositionID: "p1", Value: 1},
		{AgentID: "alice", PositionID: "p2", Value: -1},
		{AgentID: "alice", PositionID: "p3", Value: 1},
		{AgentID: "bob", PositionID: "p1", Value: 1},
		{AgentID: "bob", PositionID: "p2", Value: -1},
		{AgentID: "bob", PositionID: "p3", Value: 1},
		{AgentID: "carol", PositionID: "p1", Value: -1},
		{AgentID: "carol", PositionID: "p2", Value: 1},
		{AgentID: "carol", PositionID: "p3", Value: -1},
	}

	weights := analysis.CorrelationDiscountedWeights(votes, agents)

	// alice and bob are perfectly correlated — should be discounted
	if weights["alice"] >= 1.0 {
		t.Fatalf("expected alice weight < 1.0 (correlated with bob), got %f", weights["alice"])
	}
	if weights["bob"] >= 1.0 {
		t.Fatalf("expected bob weight < 1.0 (correlated with alice), got %f", weights["bob"])
	}

	// carol is anti-correlated with both — should have higher relative weight
	// (anti-correlation also reduces weight but less than perfect correlation)
	t.Logf("Weights: alice=%f bob=%f carol=%f", weights["alice"], weights["bob"], weights["carol"])
}

func TestCorrelationDiscountingIndependent(t *testing.T) {
	agents := []string{"alice", "bob", "carol"}

	// Everyone votes differently — no correlation
	votes := []deliberation.Vote{
		{AgentID: "alice", PositionID: "p1", Value: 1},
		{AgentID: "alice", PositionID: "p2", Value: -1},
		{AgentID: "bob", PositionID: "p1", Value: -1},
		{AgentID: "bob", PositionID: "p2", Value: 1},
		{AgentID: "carol", PositionID: "p1", Value: 0},
		{AgentID: "carol", PositionID: "p2", Value: -1},
	}

	weights := analysis.CorrelationDiscountedWeights(votes, agents)

	// All should be near 1.0 (low correlation)
	for _, a := range agents {
		if weights[a] < 0.5 {
			t.Fatalf("expected weight > 0.5 for independent agent %s, got %f", a, weights[a])
		}
	}
}

func TestCorrelationDiscountingMinWeight(t *testing.T) {
	agents := []string{"alice", "bob", "carol"}

	// alice, bob, carol all vote identically — perfect 3-way correlation
	votes := []deliberation.Vote{
		{AgentID: "alice", PositionID: "p1", Value: 1},
		{AgentID: "alice", PositionID: "p2", Value: 1},
		{AgentID: "alice", PositionID: "p3", Value: 1},
		{AgentID: "bob", PositionID: "p1", Value: 1},
		{AgentID: "bob", PositionID: "p2", Value: 1},
		{AgentID: "bob", PositionID: "p3", Value: 1},
		{AgentID: "carol", PositionID: "p1", Value: 1},
		{AgentID: "carol", PositionID: "p2", Value: 1},
		{AgentID: "carol", PositionID: "p3", Value: 1},
	}

	weights := analysis.CorrelationDiscountedWeights(votes, agents)

	// Should be floored at 0.1, not 0
	for _, a := range agents {
		if weights[a] < 0.1 {
			t.Fatalf("expected minimum weight 0.1 for %s, got %f", a, weights[a])
		}
		if weights[a] > 0.2 {
			t.Fatalf("expected low weight for perfectly correlated %s, got %f", a, weights[a])
		}
	}
	t.Logf("Perfect correlation weights: alice=%f bob=%f carol=%f", weights["alice"], weights["bob"], weights["carol"])
}
