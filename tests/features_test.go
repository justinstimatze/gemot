package tests

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/store"
)

func TestDisputeCrux(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Dispute test", "")
	svc.SubmitPosition(context.Background(), d.ID, "alice", "Position A")
	svc.SubmitPosition(context.Background(), d.ID, "bob", "Position B")

	// File a dispute
	disp, err := svc.DisputeCrux(context.Background(), d.ID, "alice", "The approach should prioritize safety", "I actually agree with this — my position was misclassified")
	if err != nil {
		t.Fatal(err)
	}
	if disp.ID == "" {
		t.Fatal("expected dispute ID")
	}
	if disp.AgentID != "alice" {
		t.Fatalf("expected alice, got %s", disp.AgentID)
	}

	// Analyze — dispute should appear as integrity warning
	result, err := svc.Analyze(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}

	foundDisputed := false
	for _, w := range result.IntegrityWarnings {
		if len(w) > 9 && w[:9] == "DISPUTED:" {
			foundDisputed = true
			t.Logf("Dispute warning: %s", w)
		}
	}
	if !foundDisputed {
		t.Fatal("expected DISPUTED integrity warning")
	}
}

func TestDeliberationType(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, err := svc.CreateDeliberation(context.Background(), "Type test", "Testing types", deliberation.WithType("reasoning"))
	if err != nil {
		t.Fatal(err)
	}

	got, _ := svc.GetDeliberation(context.Background(), d.ID)
	if got.Type != "reasoning" {
		t.Fatalf("expected type 'reasoning', got %q", got.Type)
	}
}

func TestSubGroupPositions(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Group test", "")

	// Submit positions in different groups
	svc.SubmitPosition(context.Background(), d.ID, "alice", "Group A position", deliberation.WithGroup("team-a"))
	svc.SubmitPosition(context.Background(), d.ID, "bob", "Group B position", deliberation.WithGroup("team-b"))
	svc.SubmitPosition(context.Background(), d.ID, "carol", "Group A position 2", deliberation.WithGroup("team-a"))

	// Get all positions
	all, _ := svc.GetPositions(context.Background(), d.ID, nil, nil)
	if len(all) != 3 {
		t.Fatalf("expected 3 positions, got %d", len(all))
	}

	// Verify group field persisted
	groupA := 0
	for _, p := range all {
		if p.Group == "team-a" {
			groupA++
		}
	}
	if groupA != 2 {
		t.Fatalf("expected 2 team-a positions, got %d", groupA)
	}
}

func TestModelFamily(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "Model test", "")
	svc.SubmitPosition(context.Background(), d.ID, "alice", "Claude position", deliberation.WithModelFamily("claude"))
	svc.SubmitPosition(context.Background(), d.ID, "bob", "GPT position", deliberation.WithModelFamily("gpt"))

	positions, _ := svc.GetPositions(context.Background(), d.ID, nil, nil)
	families := map[string]bool{}
	for _, p := range positions {
		if p.ModelFamily != "" {
			families[p.ModelFamily] = true
		}
	}
	if len(families) != 2 {
		t.Fatalf("expected 2 model families, got %d", len(families))
	}
}

func TestTrustWeights(t *testing.T) {
	agents := []string{"alice", "bob", "carol"}
	warnings := []string{
		`SYBIL_SIGNAL: agents "alice" and "bob" have identical votes across all 5 shared positions`,
		`COVERAGE: agent "carol" submitted a position but no claims were extracted from it`,
	}

	weights := analysis.TrustWeights(agents, nil, nil, warnings, 1)

	if weights["alice"] >= 1.0 {
		t.Fatalf("expected alice trust < 1.0 (sybil), got %f", weights["alice"])
	}
	if weights["bob"] >= 1.0 {
		t.Fatalf("expected bob trust < 1.0 (sybil), got %f", weights["bob"])
	}
	if weights["carol"] >= 1.0 {
		t.Fatalf("expected carol trust < 1.0 (coverage), got %f", weights["carol"])
	}
}

func TestTrustWeightsClean(t *testing.T) {
	agents := []string{"alice", "bob"}
	weights := analysis.TrustWeights(agents, nil, nil, nil, 1)

	if weights["alice"] != 1.0 {
		t.Fatalf("expected 1.0 for clean agent, got %f", weights["alice"])
	}
}

func TestCSVExportFormat(t *testing.T) {
	// Verify the store can handle positions with special chars in content
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	d, _ := svc.CreateDeliberation(context.Background(), "CSV test", "")
	svc.SubmitPosition(context.Background(), d.ID, "alice", `Position with "quotes" and, commas`)

	positions, _ := svc.GetPositions(context.Background(), d.ID, nil, nil)
	if len(positions) != 1 {
		t.Fatal("expected 1 position")
	}
	if positions[0].Content != `Position with "quotes" and, commas` {
		t.Fatalf("content mangled: %q", positions[0].Content)
	}
}

func TestLLMCache(t *testing.T) {
	db := tempDB(t)
	cache := store.NewLLMCache(db, 24*60*60*1e9) // 24h in nanoseconds

	// Miss
	if got := cache.Get("test-key"); got != "" {
		t.Fatalf("expected cache miss, got %q", got)
	}

	// Put + Hit
	cache.Put("test-key", `{"claims":[]}`, "claude-sonnet-4-6")
	if got := cache.Get("test-key"); got != `{"claims":[]}` {
		t.Fatalf("expected cache hit, got %q", got)
	}

	// Different key = miss
	if got := cache.Get("other-key"); got != "" {
		t.Fatal("expected miss for different key")
	}
}

func TestJobQueue(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})
	d, _ := svc.CreateDeliberation(context.Background(), "Job test", "")

	job := &store.Job{
		DeliberationID: d.ID,
		Model:          "claude-sonnet-4-6",
		APIKey:         "gmt_test",
		CreditCost:     50,
	}
	if err := db.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	if job.ID == "" {
		t.Fatal("expected job ID")
	}

	// Claim the job
	claimed, err := db.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if claimed.DeliberationID != d.ID {
		t.Fatalf("expected %s, got %s", d.ID, claimed.DeliberationID)
	}
	if claimed.Status != "running" {
		t.Fatalf("expected running, got %s", claimed.Status)
	}

	// No more pending jobs
	count, _ := db.GetPendingJobs()
	if count != 0 {
		t.Fatalf("expected 0 pending, got %d", count)
	}

	// Complete the job
	if err := db.CompleteJob(claimed.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
}
