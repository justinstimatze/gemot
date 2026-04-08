package tests

import (
	"context"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
)

// bridgingAnalyzer returns results with two clusters so bridging can be tested.
type bridgingAnalyzer struct{}

func (b *bridgingAnalyzer) Analyze(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	if len(agents) < 2 {
		return &deliberation.AnalysisResult{
			Clusters:            []deliberation.OpinionCluster{},
			Cruxes:              []deliberation.Crux{},
			ConsensusStatements: []deliberation.ConsensusStatement{},
			AgentCount:          len(agents),
			PositionCount:       len(positions),
			VoteCount:           len(votes),
		}, nil
	}
	mid := len(agents) / 2
	return &deliberation.AnalysisResult{
		Clusters: []deliberation.OpinionCluster{
			{ID: 0, AgentIDs: agents[:mid], Size: mid},
			{ID: 1, AgentIDs: agents[mid:], Size: len(agents) - mid},
		},
		Cruxes: []deliberation.Crux{
			{
				Claim:            "Test crux",
				Topic:            "Test",
				AgreeAgents:      agents[:mid],
				DisagreeAgents:   agents[mid:],
				ControversyScore: 0.8,
				SourcePositionIDs: func() []string {
					ids := make([]string, len(positions))
					for i, p := range positions {
						ids[i] = p.ID
					}
					return ids
				}(),
			},
		},
		ConsensusStatements: []deliberation.ConsensusStatement{},
		TopicSummaries:      []deliberation.TopicSummary{{Topic: "Test", Summary: "Test summary"}},
		AgentCount:          len(agents),
		PositionCount:       len(positions),
		VoteCount:           len(votes),
	}, nil
}

func TestBridgingStatements(t *testing.T) {
	// Test bridging score computation directly via the analysis package.
	// Bridging is computed inside TextAnalyzer.Analyze() from votes + clusters,
	// so we test the logic by constructing the scenario through the service
	// with a mock that includes bridging results.

	db := tempDB(t)

	// Mock that pre-computes bridging based on the votes it receives
	analyzer := &funcAnalyzer{fn: func(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
		mid := len(agents) / 2
		if mid == 0 {
			mid = 1
		}

		// Build vote map per position
		voteMap := map[string]map[string]int{}
		for _, v := range votes {
			if voteMap[v.PositionID] == nil {
				voteMap[v.PositionID] = map[string]int{}
			}
			voteMap[v.PositionID][v.AgentID] = v.Value
		}

		// Compute bridging inline
		agentCluster := map[string]int{}
		for i, a := range agents {
			if i < mid {
				agentCluster[a] = 0
			} else {
				agentCluster[a] = 1
			}
		}

		var bridging []deliberation.BridgingStatement
		for _, p := range positions {
			av := voteMap[p.ID]
			if len(av) == 0 {
				continue
			}
			agrees := 0
			for _, v := range av {
				if v == 1 {
					agrees++
				}
			}
			overallRatio := float64(agrees) / float64(len(av))
			clusterAgrees := map[int]int{}
			clusterTotal := map[int]int{}
			for agent, v := range av {
				cid := agentCluster[agent]
				clusterTotal[cid]++
				if v == 1 {
					clusterAgrees[cid]++
				}
			}
			if len(clusterTotal) < 2 {
				continue
			}
			minRatio := 1.0
			for cid, total := range clusterTotal {
				ratio := float64(clusterAgrees[cid]) / float64(total)
				if ratio < minRatio {
					minRatio = ratio
				}
			}
			if minRatio >= 0.4 && overallRatio >= 0.5 {
				bridging = append(bridging, deliberation.BridgingStatement{
					PositionID:       p.ID,
					AgentID:          p.AgentID,
					Content:          p.Content,
					BridgingScore:    minRatio,
					OverallAgreeRate: overallRatio,
				})
			}
		}

		return &deliberation.AnalysisResult{
			Clusters: []deliberation.OpinionCluster{
				{ID: 0, AgentIDs: agents[:mid], Size: mid},
				{ID: 1, AgentIDs: agents[mid:], Size: len(agents) - mid},
			},
			Cruxes:              []deliberation.Crux{{Claim: "Test crux", AgreeAgents: agents[:mid], DisagreeAgents: agents[mid:], ControversyScore: 0.8}},
			ConsensusStatements: []deliberation.ConsensusStatement{},
			BridgingStatements:  bridging,
			AgentCount:          len(agents),
			PositionCount:       len(positions),
			VoteCount:           len(votes),
		}, nil
	}}

	svc := deliberation.NewService(db, analyzer)

	d, err := svc.CreateDeliberation(context.Background(), "Bridging test", "")
	if err != nil {
		t.Fatal(err)
	}

	// 4 agents, 2 clusters (0: alice,bob  1: carol,dave)
	agents := []string{"alice", "bob", "carol", "dave"}
	posIDs := make([]string, 4)
	for i, a := range agents {
		p, err := svc.SubmitPosition(context.Background(), d.ID, a, "Position from "+a)
		if err != nil {
			t.Fatal(err)
		}
		posIDs[i] = p.ID
	}

	// alice's position: everyone agrees (bridging)
	for _, voter := range agents {
		if voter == "alice" {
			continue
		}
		_ = svc.Vote(context.Background(), d.ID, voter, posIDs[0], 1, "", "")
	}

	// bob's position: only cluster 0 agrees (not bridging)
	_ = svc.Vote(context.Background(), d.ID, "alice", posIDs[1], 1, "", "")
	_ = svc.Vote(context.Background(), d.ID, "carol", posIDs[1], -1, "", "")
	_ = svc.Vote(context.Background(), d.ID, "dave", posIDs[1], -1, "", "")

	// carol's position: mixed but cross-cluster (bridging)
	_ = svc.Vote(context.Background(), d.ID, "alice", posIDs[2], 1, "", "")
	_ = svc.Vote(context.Background(), d.ID, "bob", posIDs[2], 0, "", "")
	_ = svc.Vote(context.Background(), d.ID, "dave", posIDs[2], 1, "", "")

	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.BridgingStatements) == 0 {
		t.Fatal("expected bridging statements")
	}

	// alice's position should be a bridge (100% in both clusters)
	found := false
	for _, bs := range result.BridgingStatements {
		if bs.PositionID == posIDs[0] {
			found = true
			if bs.BridgingScore < 0.9 {
				t.Fatalf("expected high bridging score for universally agreed position, got %f", bs.BridgingScore)
			}
		}
	}
	if !found {
		t.Fatal("alice's universally-agreed position should be a bridging statement")
	}

	// bob's position should NOT be a bridge (cluster 1 disagrees)
	for _, bs := range result.BridgingStatements {
		if bs.PositionID == posIDs[1] {
			t.Fatal("bob's divisive position should not be a bridging statement")
		}
	}
}

func TestBridgingRequiresTwoClusters(t *testing.T) {
	db := tempDB(t)
	// Use mock that returns 2 clusters but we'll only have votes from 1 agent
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Single cluster", "")
	p, _ := svc.SubmitPosition(context.Background(), d.ID, "alice", "Solo position")
	_ = svc.Vote(context.Background(), d.ID, "alice", p.ID, 1, "", "")

	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}

	// With only 1 agent, bridging is meaningless
	if len(result.BridgingStatements) > 0 {
		t.Fatal("should not have bridging statements with single agent")
	}
}

func TestAnalysisProvenance(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &bridgingAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Provenance test", "")
	p1, _ := svc.SubmitPosition(context.Background(), d.ID, "alice", "Position A")
	p2, _ := svc.SubmitPosition(context.Background(), d.ID, "bob", "Position B")
	_ = svc.Vote(context.Background(), d.ID, "alice", p2.ID, -1, "", "")
	_ = svc.Vote(context.Background(), d.ID, "bob", p1.ID, -1, "", "")

	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Cruxes) == 0 {
		t.Fatal("expected cruxes")
	}

	// Cruxes should have source position IDs
	crux := result.Cruxes[0]
	if len(crux.SourcePositionIDs) == 0 {
		t.Fatal("expected source_position_ids on crux")
	}

	// Source IDs should reference actual positions
	validIDs := map[string]bool{p1.ID: true, p2.ID: true}
	for _, id := range crux.SourcePositionIDs {
		if !validIDs[id] {
			t.Fatalf("source_position_id %q does not match any submitted position", id)
		}
	}
}

func TestRoundDriftDetection(t *testing.T) {
	db := tempDB(t)

	// driftAnalyzer: round 1 has 2 clusters, round 2 collapses to 1
	round := 0
	analyzer := &funcAnalyzer{fn: func(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
		round++
		if round == 1 {
			mid := len(agents) / 2
			return &deliberation.AnalysisResult{
				Clusters: []deliberation.OpinionCluster{
					{ID: 0, AgentIDs: agents[:mid], Size: mid},
					{ID: 1, AgentIDs: agents[mid:], Size: len(agents) - mid},
				},
				Cruxes: []deliberation.Crux{
					{Claim: "Test crux", AgreeAgents: agents[:mid], DisagreeAgents: agents[mid:], ControversyScore: 0.8},
				},
				ConsensusStatements: []deliberation.ConsensusStatement{},
				AgentCount:          len(agents),
				PositionCount:       len(positions),
				VoteCount:           len(votes),
			}, nil
		}
		// Round 2: everything collapsed
		return &deliberation.AnalysisResult{
			Clusters: []deliberation.OpinionCluster{
				{ID: 0, AgentIDs: agents, Size: len(agents)},
			},
			Cruxes:              []deliberation.Crux{},
			ConsensusStatements: []deliberation.ConsensusStatement{},
			AgentCount:          len(agents),
			PositionCount:       len(positions),
			VoteCount:           len(votes),
		}, nil
	}}

	svc := deliberation.NewService(db, analyzer)
	d, _ := svc.CreateDeliberation(context.Background(), "Drift test", "")

	// Round 1: divergent positions
	agents := []string{"alice", "bob", "carol", "dave"}
	for _, a := range agents {
		svc.SubmitPosition(context.Background(), d.ID, a, "Position from "+a)
	}

	result1, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result1.Clusters) != 2 {
		t.Fatalf("round 1 expected 2 clusters, got %d", len(result1.Clusters))
	}

	// Round 2: same agents, converged positions
	for _, a := range agents {
		svc.SubmitPosition(context.Background(), d.ID, a, "We all agree now")
	}

	result2, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Should detect cluster collapse and crux disappearance
	driftWarnings := 0
	for _, w := range result2.IntegrityWarnings {
		if len(w) > 5 && w[:5] == "DRIFT" {
			driftWarnings++
			t.Logf("Drift warning: %s", w)
		}
	}
	if driftWarnings == 0 {
		t.Fatal("expected DRIFT warnings when clusters collapse and cruxes disappear")
	}
}

func TestRateLimiter(t *testing.T) {
	rl := payments.NewRateLimiter(context.Background(), 3, 100*time.Millisecond)

	// First 3 should pass
	for i := 0; i < 3; i++ {
		if !rl.Allow("key1") {
			t.Fatalf("request %d should be allowed", i)
		}
	}

	// 4th should be rejected
	if rl.Allow("key1") {
		t.Fatal("4th request should be rate limited")
	}

	// Different key should still work
	if !rl.Allow("key2") {
		t.Fatal("different key should not be rate limited")
	}

	// Wait for window to expire
	time.Sleep(150 * time.Millisecond)

	// Should be allowed again
	if !rl.Allow("key1") {
		t.Fatal("should be allowed after window expires")
	}
}

func TestRateLimiterConcurrent(t *testing.T) {
	rl := payments.NewRateLimiter(context.Background(), 100, time.Second)
	done := make(chan bool, 200)

	// Fire 200 concurrent requests
	for i := 0; i < 200; i++ {
		go func() {
			done <- rl.Allow("key")
		}()
	}

	allowed := 0
	for i := 0; i < 200; i++ {
		if <-done {
			allowed++
		}
	}

	if allowed != 100 {
		t.Fatalf("expected exactly 100 allowed, got %d", allowed)
	}
}

// funcAnalyzer wraps a function as an Analyzer for testing.
type funcAnalyzer struct {
	fn func(context.Context, []deliberation.Position, []deliberation.Vote, []string) (*deliberation.AnalysisResult, error)
}

func (f *funcAnalyzer) Analyze(ctx context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	return f.fn(ctx, positions, votes, agents)
}
