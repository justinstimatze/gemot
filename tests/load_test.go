package tests

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// TestConcurrentToolCalls verifies database concurrency under realistic load.
// Uses a local test service with mockAnalyzer, not the live server.
func TestConcurrentToolCalls(t *testing.T) {
	svc, db := newTestService(t)

	// Create 1 deliberation
	d, err := svc.CreateDeliberation("Concurrency Test", "Testing concurrent position submissions and votes.")
	if err != nil {
		t.Fatalf("creating deliberation: %v", err)
	}

	// Launch 20 goroutines, each submitting a position with a unique agent ID
	const numAgents = 20
	var wg sync.WaitGroup
	errs := make([]error, numAgents)

	for i := range numAgents {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			agentID := fmt.Sprintf("agent-%d", idx)
			content := fmt.Sprintf("Position from %s: concurrent submission #%d", agentID, idx)
			_, errs[idx] = svc.SubmitPosition(d.ID, agentID, content)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("agent-%d submit_position failed: %v", i, err)
		}
	}

	// Verify all 20 positions were created
	positions, err := svc.GetPositions(d.ID, nil, nil)
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if len(positions) != numAgents {
		t.Fatalf("expected %d positions, got %d", numAgents, len(positions))
	}
	t.Logf("All %d positions created successfully", numAgents)

	// Collect position IDs for voting
	posIDs := make([]string, len(positions))
	for i, p := range positions {
		posIDs[i] = p.ID
	}

	// Launch 20 goroutines, each voting on a random position
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	voteErrs := make([]error, numAgents)
	for i := range numAgents {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			agentID := fmt.Sprintf("voter-%d", idx)
			// Pick a random position (not from the same agent to be realistic)
			targetIdx := rng.Intn(len(posIDs))
			values := []int{-1, 0, 1}
			value := values[rng.Intn(3)]
			voteErrs[idx] = svc.Vote(d.ID, agentID, posIDs[targetIdx], value)
		}(i)
	}
	wg.Wait()

	for i, err := range voteErrs {
		if err != nil {
			t.Fatalf("voter-%d vote failed: %v", i, err)
		}
	}

	// Verify votes were recorded
	votes, err := db.GetVotes(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("GetVotes: %v", err)
	}
	if len(votes) == 0 {
		t.Fatal("expected votes to be recorded, got 0")
	}
	t.Logf("Recorded %d votes from %d concurrent goroutines", len(votes), numAgents)
	t.Log("=== CONCURRENCY TEST PASSED ===")
}

// TestLargeDeliberation creates a deliberation with 100 positions and 500
// votes, then runs analysis to verify the system handles realistic scale.
func TestLargeDeliberation(t *testing.T) {
	svc, _ := newTestService(t)

	d, err := svc.CreateDeliberation("Scale Test", "Testing large deliberation with 100 agents.")
	if err != nil {
		t.Fatalf("creating deliberation: %v", err)
	}

	// Submit 100 positions from 100 agents
	start := time.Now()
	posIDs := make([]string, 100)
	for i := range 100 {
		agentID := fmt.Sprintf("agent-%d", i)
		content := fmt.Sprintf("Agent %d's position on governance: approach #%d with varying perspectives on regulation, innovation, and safety.", i, i%7)
		p, err := svc.SubmitPosition(d.ID, agentID, content)
		if err != nil {
			t.Fatalf("submit_position agent-%d: %v", i, err)
		}
		posIDs[i] = p.ID
	}
	submitDuration := time.Since(start)
	t.Logf("Submitted 100 positions in %s", submitDuration)

	// Record 500 votes: each agent votes on 5 random positions
	rng := rand.New(rand.NewSource(42)) // deterministic for reproducibility
	start = time.Now()
	voteCount := 0
	for i := range 100 {
		agentID := fmt.Sprintf("agent-%d", i)
		// Pick 5 random positions (not own)
		voted := map[int]bool{i: true} // exclude own position
		for len(voted)-1 < 5 {
			target := rng.Intn(100)
			if voted[target] {
				continue
			}
			voted[target] = true
			values := []int{-1, 0, 1}
			value := values[rng.Intn(3)]
			if err := svc.Vote(d.ID, agentID, posIDs[target], value); err != nil {
				t.Fatalf("vote agent-%d -> position-%d: %v", i, target, err)
			}
			voteCount++
		}
	}
	voteDuration := time.Since(start)
	t.Logf("Recorded %d votes in %s", voteCount, voteDuration)

	// Analyze with mockAnalyzer
	start = time.Now()
	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	analyzeDuration := time.Since(start)

	if result.PositionCount != 100 {
		t.Fatalf("expected 100 positions in result, got %d", result.PositionCount)
	}
	if result.VoteCount != voteCount {
		t.Fatalf("expected %d votes in result, got %d", voteCount, result.VoteCount)
	}

	t.Logf("Analysis completed in %s", analyzeDuration)
	t.Logf("Result: %d agents, %d positions, %d votes, %d clusters, %d cruxes",
		result.AgentCount, result.PositionCount, result.VoteCount,
		len(result.Clusters), len(result.Cruxes))
	t.Logf("Total time: positions=%s votes=%s analyze=%s",
		submitDuration, voteDuration, analyzeDuration)
	t.Log("=== LARGE DELIBERATION TEST PASSED ===")
}
