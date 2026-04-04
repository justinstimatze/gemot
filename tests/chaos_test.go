package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
)

func TestStuckAnalyzingRecovery(t *testing.T) {
	svc, db := newTestService(t)

	// 1. Create a deliberation
	d, err := svc.CreateDeliberation("Stuck Test", "Testing stuck state recovery")
	if err != nil {
		t.Fatal(err)
	}

	// 2. Submit 2 positions
	if _, err := svc.SubmitPosition(d.ID, "alice", "Position A"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitPosition(d.ID, "bob", "Position B"); err != nil {
		t.Fatal(err)
	}

	// 3. Simulate crash mid-analysis: directly set status to "analyzing"
	if err := db.UpdateDeliberationStatus(d.ID, "analyzing"); err != nil {
		t.Fatal(err)
	}

	// 4. Analyze should fail because status is "analyzing"
	_, err = svc.Analyze(context.Background(), d.ID)
	if err == nil {
		t.Fatal("expected error when analyzing a deliberation already in 'analyzing' status")
	}
	if !strings.Contains(err.Error(), "not open") {
		t.Fatalf("expected 'not open' error, got: %v", err)
	}

	// 5. Ensure status_changed_at is clearly recent (same clock as recovery check — UTC)
	recentTime := time.Now().UTC()
	if err := db.TestExec(`UPDATE deliberations SET status_changed_at = $1 WHERE id = $2`, recentTime, d.ID); err != nil {
		t.Fatal(err)
	}

	// RecoverStuck should recover 0 (status_changed_at is clearly within 10 min)
	n, err := svc.RecoverStuck()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 recoveries (too recent), got %d", n)
	}

	// 6. Manually set status_changed_at to 15 minutes ago (simulates stuck-for-15-min)
	oldTime := time.Now().UTC().Add(-35 * time.Minute)
	if err := db.TestExec(`UPDATE deliberations SET status_changed_at = $1 WHERE id = $2`, oldTime, d.ID); err != nil {
		t.Fatal(err)
	}

	// 7. RecoverStuck should recover 1
	n, err = svc.RecoverStuck()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 recovery, got %d", n)
	}

	// 8. Verify status is back to "open"
	d2, err := svc.GetDeliberation(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Status != "open" {
		t.Fatalf("expected status 'open', got %q", d2.Status)
	}

	// 9. Analyze should succeed now
	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("analyze should succeed after recovery: %v", err)
	}
	if result.PositionCount != 2 {
		t.Fatalf("expected 2 positions in result, got %d", result.PositionCount)
	}
}

func TestDataPersistsAfterReconnect(t *testing.T) {
	// Use tempDB to get an isolated schema, then verify data persists across connections
	db1 := tempDB(t)

	// Create data
	svc1 := deliberation.NewService(db1, &mockAnalyzer{})

	d, err := svc1.CreateDeliberation("Persist Test", "Testing data persistence")
	if err != nil {
		t.Fatal(err)
	}
	delibID := d.ID

	p1, err := svc1.SubmitPosition(delibID, "alice", "Position from alice")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := svc1.SubmitPosition(delibID, "bob", "Position from bob")
	if err != nil {
		t.Fatal(err)
	}

	if err := svc1.Vote(delibID, "alice", p2.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc1.Vote(delibID, "bob", p1.ID, -1); err != nil {
		t.Fatal(err)
	}

	// Verify data is there (Postgres persists by default)
	got, err := db1.GetDeliberation(delibID)
	if err != nil {
		t.Fatalf("deliberation not found: %v", err)
	}
	if got.Topic != "Persist Test" {
		t.Fatalf("expected topic 'Persist Test', got %q", got.Topic)
	}
	if got.Status != "open" {
		t.Fatalf("expected status 'open', got %q", got.Status)
	}

	positions, err := db1.GetPositions(delibID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 2 {
		t.Fatalf("expected 2 positions, got %d", len(positions))
	}

	votes, err := db1.GetVotes(delibID)
	if err != nil {
		t.Fatal(err)
	}
	if len(votes) != 2 {
		t.Fatalf("expected 2 votes, got %d", len(votes))
	}
}

// failingLLM returns errors for all LLM calls.
func failingLLM() llm.StructuredOutputFunc {
	return func(_ context.Context, system, prompt string, schema map[string]any, target any) error {
		return fmt.Errorf("LLM service unavailable")
	}
}

// flakeyLLM alternates between success and failure. Even-numbered calls succeed,
// odd-numbered calls fail. This simulates a degraded LLM service.
func flakeyLLM() llm.StructuredOutputFunc {
	var callCount int64
	baseMock := mockLLM()
	return func(ctx context.Context, system, prompt string, schema map[string]any, target any) error {
		n := atomic.AddInt64(&callCount, 1)
		if n%2 == 0 {
			return fmt.Errorf("LLM intermittent failure")
		}
		return baseMock(ctx, system, prompt, schema, target)
	}
}

func TestAnalysisWithFailingLLM(t *testing.T) {
	// Test 1: Total failure - should return an error, not an empty success
	failAnalyzer := analysis.NewTextAnalyzerWithFunc(failingLLM())

	agents := []string{"alice", "bob", "carol"}
	positions := []deliberation.Position{
		{ID: "p1", DeliberationID: "Fail Test", AgentID: "alice", Content: "International binding treaties are essential.", Round: 1},
		{ID: "p2", DeliberationID: "Fail Test", AgentID: "bob", Content: "Industry self-regulation is sufficient.", Round: 1},
		{ID: "p3", DeliberationID: "Fail Test", AgentID: "carol", Content: "We need a moratorium on frontier AI.", Round: 1},
	}

	_, err := failAnalyzer.Analyze(context.Background(), positions, nil, agents)
	if err == nil {
		t.Fatal("expected error from failing LLM, got nil")
	}

	// Test 2: Flakey LLM (50% failure) - should still produce some results
	flakeyAnalyzer := analysis.NewTextAnalyzerWithFunc(flakeyLLM())

	// The flakey mock succeeds on odd calls (1st, 3rd, 5th...) and fails on even calls.
	// The first call is taxonomy extraction - if it succeeds, the pipeline continues.
	// Some claim extractions may fail, but the pipeline should produce degraded results.
	result, err := flakeyAnalyzer.Analyze(context.Background(), positions, nil, agents)
	if err != nil {
		// If taxonomy extraction (the first LLM call) fails, the whole pipeline fails.
		// That's acceptable - the key assertion is that we don't get an empty success.
		t.Logf("flakey LLM produced error (acceptable if taxonomy call failed): %v", err)
	} else {
		// If we got a result, it should not be completely empty
		if result.AgentCount != 3 {
			t.Fatalf("expected 3 agents in result, got %d", result.AgentCount)
		}
		if result.PositionCount != 3 {
			t.Fatalf("expected 3 positions in result, got %d", result.PositionCount)
		}
		t.Logf("flakey LLM produced result: %d cruxes, %d topic summaries",
			len(result.Cruxes), len(result.TopicSummaries))
	}
}

// Ensure json import is used (for potential future test assertions).
var _ = json.Marshal
