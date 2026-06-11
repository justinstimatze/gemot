package tests

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/store"
)

// testDSN returns the Postgres DSN for tests.
// Uses DATABASE_URL env var, falling back to a local default.
func testDSN() string {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
	}
	return "postgres://gemot:gemot@localhost:5432/gemot?sslmode=disable"
}

// Cached one-shot probe so the integration-test package skips cleanly
// when Postgres isn't reachable (e.g. `go test ./...` on a fresh
// machine without docker-compose up). One short ping at first call;
// subsequent calls reuse the cached verdict.
var (
	pgProbeOnce sync.Once
	pgProbeErr  error
)

// ensurePostgres probes the configured DSN once per test process. If
// Postgres is unreachable, every tempDB-backed test t.Skip()s with the
// same hint instead of t.Fatal'ing the whole package. CI provisions
// Postgres (or sets DATABASE_URL) to actually run the suite.
func ensurePostgres(t *testing.T) {
	t.Helper()
	pgProbeOnce.Do(func() {
		db, err := sql.Open("pgx", testDSN())
		if err != nil {
			pgProbeErr = err
			return
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pgProbeErr = db.PingContext(ctx)
	})
	if pgProbeErr != nil {
		t.Skipf("Postgres not reachable (%v) — set DATABASE_URL or start a local Postgres to enable integration tests", pgProbeErr)
	}
}

// tempDB creates an isolated Postgres schema for each test and returns a store.DB.
// The schema is dropped on test cleanup, giving each test a clean slate.
// Skips the calling test if Postgres isn't reachable (see ensurePostgres).
func tempDB(t *testing.T) *store.DB {
	t.Helper()
	ensurePostgres(t)

	// Create a unique schema name from the test name
	schemaName := "test_" + strings.ReplaceAll(
		strings.ReplaceAll(t.Name(), "/", "_"),
		"-", "_",
	)
	// Postgres identifiers max 63 chars; truncate if needed and add uniqueness
	if len(schemaName) > 50 {
		schemaName = schemaName[:50]
	}
	// Add a short unique suffix to avoid collisions from truncation
	schemaName = fmt.Sprintf("%s_%d", schemaName, os.Getpid()%10000)

	dsn := testDSN()

	// Connect to create/drop the schema
	rawDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}

	// Create schema and set search_path
	if _, err := rawDB.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName)); err != nil {
		rawDB.Close()
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(fmt.Sprintf("CREATE SCHEMA %s", schemaName)); err != nil {
		rawDB.Close()
		t.Fatal(err)
	}
	rawDB.Close()

	// Build DSN with search_path set to the test schema
	schemaDSN := dsn
	if strings.Contains(schemaDSN, "?") {
		schemaDSN += "&search_path=" + schemaName
	} else {
		schemaDSN += "?search_path=" + schemaName
	}

	db, err := store.Open(schemaDSN)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		db.Close()
		// Drop the schema
		cleanDB, err := sql.Open("pgx", dsn)
		if err == nil {
			cleanDB.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName)) //nolint:errcheck
			cleanDB.Close()
		}
	})
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
	if err := db.CreateDeliberation(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if d.ID == "" {
		t.Fatal("expected ID to be set")
	}

	got, err := db.GetDeliberation(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Topic != "AI Safety" {
		t.Fatalf("expected topic 'AI Safety', got %q", got.Topic)
	}

	list, err := db.ListDeliberations(context.Background(), 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 deliberation, got %d", len(list))
	}

	if err := db.UpdateDeliberationStatus(context.Background(), d.ID, "analyzing"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetDeliberation(context.Background(), d.ID)
	if got.Status != "analyzing" {
		t.Fatalf("expected status 'analyzing', got %q", got.Status)
	}

	if err := db.AdvanceRound(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetDeliberation(context.Background(), d.ID)
	if got.Round != 2 {
		t.Fatalf("expected round 2, got %d", got.Round)
	}
}

func TestPositionCRUD(t *testing.T) {
	db := tempDB(t)

	d := &deliberation.Deliberation{Topic: "Test", Round: 1, Status: "open"}
	db.CreateDeliberation(context.Background(), d)

	p := &deliberation.Position{
		DeliberationID: d.ID,
		AgentID:        "agent-1",
		Content:        "We should prioritize interpretability",
		Round:          1,
	}
	if err := db.CreatePosition(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if p.ID == "" {
		t.Fatal("expected position ID to be set")
	}

	positions, err := db.GetPositions(context.Background(), d.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 {
		t.Fatalf("expected 1 position, got %d", len(positions))
	}

	round := 1
	positions, err = db.GetPositions(context.Background(), d.ID, &round)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 {
		t.Fatalf("expected 1 position for round 1, got %d", len(positions))
	}

	round = 2
	positions, err = db.GetPositions(context.Background(), d.ID, &round)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 0 {
		t.Fatalf("expected 0 positions for round 2, got %d", len(positions))
	}

	got, err := db.GetPositionByID(context.Background(), p.ID)
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
	db.CreateDeliberation(context.Background(), d)

	p := &deliberation.Position{DeliberationID: d.ID, AgentID: "agent-1", Content: "Position 1", Round: 1}
	db.CreatePosition(context.Background(), p)

	v := &deliberation.Vote{
		DeliberationID: d.ID,
		AgentID:        "agent-2",
		PositionID:     p.ID,
		Value:          1,
	}
	if err := db.CreateVote(context.Background(), v); err != nil {
		t.Fatal(err)
	}

	votes, err := db.GetVotes(context.Background(), d.ID)
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
	if err := db.CreateVote(context.Background(), v2); err != nil {
		t.Fatal(err)
	}
	votes, _ = db.GetVotes(context.Background(), d.ID)
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
	db.CreateDeliberation(context.Background(), d)

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

	if err := db.SaveAnalysisResult(context.Background(), d.ID, 1, result); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetAnalysisResult(context.Background(), d.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cruxes) != 1 {
		t.Fatalf("expected 1 crux, got %d", len(got.Cruxes))
	}
	if got.Cruxes[0].Claim != "AI will be transformative" {
		t.Fatalf("unexpected crux claim: %q", got.Cruxes[0].Claim)
	}

	latest, err := db.GetLatestAnalysisResult(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Round != 1 {
		t.Fatalf("expected round 1, got %d", latest.Round)
	}
}
