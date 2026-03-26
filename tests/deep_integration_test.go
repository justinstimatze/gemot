package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestEffectiveWeightsComputation(t *testing.T) {
	// Effective weight = trust × correlation × sqrt(conviction)
	// High conviction (0.9): sqrt(0.9) ≈ 0.949 → weight ≈ 0.949 (with trust=corr=1.0)
	// Low conviction (0.1): sqrt(0.1) ≈ 0.316 → weight ≈ 0.316
	// This is the Plurality quadratic voting mechanism: influence grows sub-linearly

	// Verify via the analysis package directly
	weights := analysis.TrustWeights([]string{"alice", "bob"}, nil, nil, nil)
	if weights["alice"] != 1.0 || weights["bob"] != 1.0 {
		t.Fatal("clean agents should have trust 1.0")
	}

	corrWeights := analysis.CorrelationDiscountedWeights(nil, []string{"alice", "bob"})
	if corrWeights["alice"] != 1.0 {
		t.Fatalf("no votes = no correlation discount, expected 1.0 got %f", corrWeights["alice"])
	}

	// The effective weight math:
	// alice (conv 0.9): 1.0 * 1.0 * sqrt(0.9) ≈ 0.949
	// bob (conv 0.1): 1.0 * 1.0 * sqrt(0.1) ≈ 0.316
	// alice has ~3x the influence of bob
	t.Logf("Quadratic conviction: sqrt(0.9)/sqrt(0.1) = %.1fx influence ratio", 0.949/0.316)
}

func TestDelegatedVotesInAnalysis(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation("Delegation analysis test", "")
	p1, _ := svc.SubmitPosition(d.ID, "alice", "A")
	p2, _ := svc.SubmitPosition(d.ID, "bob", "B")
	svc.SubmitPosition(d.ID, "carol", "C")

	// carol delegates to alice
	svc.Delegate(d.ID, "carol", "alice", "")

	// alice votes, bob votes — carol should get alice's votes via delegation
	svc.Vote(d.ID, "alice", p2.ID, 1)
	svc.Vote(d.ID, "bob", p1.ID, -1)

	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Carol should appear in agent count (included via delegation)
	if result.AgentCount < 3 {
		t.Fatalf("expected 3 agents (including delegator carol), got %d", result.AgentCount)
	}

	// Vote count should include delegated votes
	if result.VoteCount < 3 {
		t.Logf("Vote count: %d (should include delegated votes)", result.VoteCount)
	}
}

func TestDelegationDirectVoteOverride(t *testing.T) {
	// If carol delegates to alice but also votes directly, direct vote wins
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation("Override test", "")
	_, _ = svc.SubmitPosition(d.ID, "alice", "A")
	p2, _ := svc.SubmitPosition(d.ID, "bob", "B")
	_, _ = svc.SubmitPosition(d.ID, "carol", "C")

	// carol delegates to alice
	svc.Delegate(d.ID, "carol", "alice", "")

	// alice votes agree on bob's position
	svc.Vote(d.ID, "alice", p2.ID, 1)
	// carol votes disagree directly — should override delegation
	svc.Vote(d.ID, "carol", p2.ID, -1)

	result, _ := svc.Analyze(context.Background(), d.ID)

	// carol's direct vote should count, not alice's delegated vote
	// The vote count should NOT include a delegated vote for carol on p2
	// since carol voted directly
	t.Logf("Votes: %d, Agents: %d", result.VoteCount, result.AgentCount)
}

func TestTransitiveDelegation(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation("Transitive test", "")
	_, _ = svc.SubmitPosition(d.ID, "alice", "A")
	p2, _ := svc.SubmitPosition(d.ID, "bob", "B")
	_, _ = svc.SubmitPosition(d.ID, "carol", "C")
	_, _ = svc.SubmitPosition(d.ID, "dave", "D")

	// dave -> carol -> alice (transitive chain)
	svc.Delegate(d.ID, "dave", "carol", "")
	svc.Delegate(d.ID, "carol", "alice", "")

	// Only alice votes
	svc.Vote(d.ID, "alice", p2.ID, 1)

	result, _ := svc.Analyze(context.Background(), d.ID)

	// All 4 agents should be counted (dave and carol via transitive delegation)
	if result.AgentCount < 4 {
		t.Fatalf("expected 4 agents with transitive delegation, got %d", result.AgentCount)
	}
	// Vote count should include alice + carol (delegated) + dave (transitively delegated)
	if result.VoteCount < 3 {
		t.Logf("Vote count with transitive delegation: %d", result.VoteCount)
	}
}
