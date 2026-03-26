package tests

import (
	"os"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/store"
)

func tempDB(t *testing.T) *store.DB {
	t.Helper()
	f, err := os.CreateTemp("", "gemot-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	db, err := store.Open(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestDeliberationCRUD(t *testing.T) {
	db := tempDB(t)

	d := &deliberation.Deliberation{
		Topic:       "AI Safety",
		Description: "Discuss approaches to AI alignment",
		Round:       1,
		Status:      "open",
	}
	if err := db.CreateDeliberation(d); err != nil {
		t.Fatal(err)
	}
	if d.ID == "" {
		t.Fatal("expected ID to be set")
	}

	got, err := db.GetDeliberation(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Topic != "AI Safety" {
		t.Fatalf("expected topic 'AI Safety', got %q", got.Topic)
	}

	list, err := db.ListDeliberations()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 deliberation, got %d", len(list))
	}

	if err := db.UpdateDeliberationStatus(d.ID, "analyzing"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetDeliberation(d.ID)
	if got.Status != "analyzing" {
		t.Fatalf("expected status 'analyzing', got %q", got.Status)
	}

	if err := db.AdvanceRound(d.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetDeliberation(d.ID)
	if got.Round != 2 {
		t.Fatalf("expected round 2, got %d", got.Round)
	}
}

func TestPositionCRUD(t *testing.T) {
	db := tempDB(t)

	d := &deliberation.Deliberation{Topic: "Test", Round: 1, Status: "open"}
	db.CreateDeliberation(d)

	p := &deliberation.Position{
		DeliberationID: d.ID,
		AgentID:        "agent-1",
		Content:        "We should prioritize interpretability",
		Round:          1,
	}
	if err := db.CreatePosition(p); err != nil {
		t.Fatal(err)
	}
	if p.ID == "" {
		t.Fatal("expected position ID to be set")
	}

	positions, err := db.GetPositions(d.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 {
		t.Fatalf("expected 1 position, got %d", len(positions))
	}

	round := 1
	positions, err = db.GetPositions(d.ID, &round)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 {
		t.Fatalf("expected 1 position for round 1, got %d", len(positions))
	}

	round = 2
	positions, err = db.GetPositions(d.ID, &round)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 0 {
		t.Fatalf("expected 0 positions for round 2, got %d", len(positions))
	}

	got, err := db.GetPositionByID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "We should prioritize interpretability" {
		t.Fatalf("unexpected content: %q", got.Content)
	}
}

func TestVoteCRUD(t *testing.T) {
	db := tempDB(t)

	d := &deliberation.Deliberation{Topic: "Test", Round: 1, Status: "open"}
	db.CreateDeliberation(d)

	p := &deliberation.Position{DeliberationID: d.ID, AgentID: "agent-1", Content: "Position 1", Round: 1}
	db.CreatePosition(p)

	v := &deliberation.Vote{
		DeliberationID: d.ID,
		AgentID:        "agent-2",
		PositionID:     p.ID,
		Value:          1,
	}
	if err := db.CreateVote(v); err != nil {
		t.Fatal(err)
	}

	votes, err := db.GetVotes(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(votes) != 1 {
		t.Fatalf("expected 1 vote, got %d", len(votes))
	}
	if votes[0].Value != 1 {
		t.Fatalf("expected vote value 1, got %d", votes[0].Value)
	}

	// Test upsert (same agent, same position = replace)
	v2 := &deliberation.Vote{
		DeliberationID: d.ID,
		AgentID:        "agent-2",
		PositionID:     p.ID,
		Value:          -1,
	}
	if err := db.CreateVote(v2); err != nil {
		t.Fatal(err)
	}
	votes, _ = db.GetVotes(d.ID)
	if len(votes) != 1 {
		t.Fatalf("expected 1 vote after upsert, got %d", len(votes))
	}
	if votes[0].Value != -1 {
		t.Fatalf("expected updated vote value -1, got %d", votes[0].Value)
	}
}

func TestAnalysisResultCRUD(t *testing.T) {
	db := tempDB(t)

	d := &deliberation.Deliberation{Topic: "Test", Round: 1, Status: "open"}
	db.CreateDeliberation(d)

	result := &deliberation.AnalysisResult{
		DeliberationID: d.ID,
		Round:          1,
		Cruxes: []deliberation.Crux{
			{
				Claim:            "AI will be transformative",
				Topic:            "Impact",
				AgreeAgents:      []string{"agent-1"},
				DisagreeAgents:   []string{"agent-2"},
				ControversyScore: 1.0,
			},
		},
		AgentCount:    2,
		PositionCount: 2,
		VoteCount:     0,
	}

	if err := db.SaveAnalysisResult(d.ID, 1, result); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetAnalysisResult(d.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cruxes) != 1 {
		t.Fatalf("expected 1 crux, got %d", len(got.Cruxes))
	}
	if got.Cruxes[0].Claim != "AI will be transformative" {
		t.Fatalf("unexpected crux claim: %q", got.Cruxes[0].Claim)
	}

	latest, err := db.GetLatestAnalysisResult(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Round != 1 {
		t.Fatalf("expected round 1, got %d", latest.Round)
	}
}
