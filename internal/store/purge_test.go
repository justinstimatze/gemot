package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

// TestHardDeleteDeliberationRemovesCommitmentAccessAndJobs is the
// regression test for a purge bug: hardDeleteDeliberation's delete-table
// list omitted commitment_access (FK'd to commitments, no ON DELETE
// CASCADE) and jobs (FK'd to deliberations, same story) -- a deliberation
// with either kind of row failed to purge with a foreign key violation,
// silently rolling back the WHOLE delete for that deliberation while
// purgeByQuery's caller still counted it as cleaned up.
func TestHardDeleteDeliberationRemovesCommitmentAccessAndJobs(t *testing.T) {
	db := purgeTestDB(t)
	ctx := context.Background()

	d := &deliberation.Deliberation{Topic: "purge test", Status: "open", Round: 1}
	if err := db.CreateDeliberation(ctx, d); err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}

	c := &deliberation.Commitment{
		DeliberationID: d.ID, AgentID: "alice", AnalysisRound: 1,
		Statement: "I will do the thing", Status: "pending",
	}
	if err := db.CreateCommitment(ctx, c); err != nil {
		t.Fatalf("CreateCommitment: %v", err)
	}
	if err := db.RecordCommitmentAccess(ctx, &deliberation.CommitmentAccess{
		CommitmentID: c.ID, AccessorID: "bob", Kind: "read",
	}); err != nil {
		t.Fatalf("RecordCommitmentAccess: %v", err)
	}
	if err := db.CreateJob(&Job{DeliberationID: d.ID, Model: "claude-sonnet-4-6"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if err := db.hardDeleteDeliberation(d.ID); err != nil {
		t.Fatalf("hardDeleteDeliberation: %v (this deliberation has commitment_access and jobs rows that must be deleted first)", err)
	}

	var count int
	for _, table := range []string{"deliberations", "commitments", "commitment_access", "jobs"} {
		// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- table is drawn from the fixed literal above, not external input; id is bound via $1 below
		query := "SELECT COUNT(*) FROM " + table + " WHERE "
		if table == "commitment_access" {
			query += "commitment_id = $1"
		} else if table == "deliberations" {
			query += "id = $1"
		} else {
			query += "deliberation_id = $1"
		}
		id := d.ID
		if table == "commitment_access" {
			id = c.ID
		}
		if err := db.db.QueryRowContext(ctx, query, id).Scan(&count); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s still has %d row(s) after hardDeleteDeliberation", table, count)
		}
	}
}

// TestPurgeByQueryReturnsActualDeletedCount confirms purgeByQuery's return
// value reflects deliberations ACTUALLY deleted, not merely the number of
// candidates found -- the prior behavior counted every candidate as
// cleaned up regardless of whether hardDeleteDeliberation succeeded.
func TestPurgeByQueryReturnsActualDeletedCount(t *testing.T) {
	db := purgeTestDB(t)
	ctx := context.Background()

	old := time.Now().Add(-100 * time.Hour).UTC()
	for i := 0; i < 3; i++ {
		d := &deliberation.Deliberation{Topic: "expired", Status: "open", Round: 1, Visibility: "link", Template: "assembly"}
		if err := db.CreateDeliberation(ctx, d); err != nil {
			t.Fatalf("CreateDeliberation: %v", err)
		}
		if _, err := db.db.ExecContext(ctx, "UPDATE deliberations SET created_at = $1 WHERE id = $2", old, d.ID); err != nil {
			t.Fatalf("backdating created_at: %v", err)
		}
	}

	n, err := db.DeleteExpiredSandboxDeliberations(time.Hour)
	if err != nil {
		t.Fatalf("DeleteExpiredSandboxDeliberations: %v", err)
	}
	if n != 3 {
		t.Errorf("deleted count = %d, want 3", n)
	}
}

// purgeTestDB opens an isolated Postgres schema for this test file's own
// use, mirroring the pattern used elsewhere for Postgres-integration tests
// (this package has no import-cycle constraint stopping it from opening
// itself directly, unlike internal/deliberation or internal/mcp). Skips
// the calling test if Postgres isn't reachable.
func purgeTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://gemot:gemot@localhost:5432/gemot?sslmode=disable"
	}

	probe, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("Postgres not reachable (%v)", err)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := probe.PingContext(pingCtx); err != nil {
		probe.Close()
		t.Skipf("Postgres not reachable (%v)", err)
	}

	schemaName := "test_purge_" + strings.ReplaceAll(strings.ReplaceAll(t.Name(), "/", "_"), "-", "_")
	if len(schemaName) > 50 {
		schemaName = schemaName[:50]
	}
	schemaName = fmt.Sprintf("%s_%d", schemaName, os.Getpid()%10000)

	if _, err := probe.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName)); err != nil {
		probe.Close()
		t.Fatal(err)
	}
	if _, err := probe.Exec(fmt.Sprintf("CREATE SCHEMA %s", schemaName)); err != nil {
		probe.Close()
		t.Fatal(err)
	}
	probe.Close()

	schemaDSN := dsn
	if strings.Contains(schemaDSN, "?") {
		schemaDSN += "&search_path=" + schemaName
	} else {
		schemaDSN += "?search_path=" + schemaName
	}

	db, err := Open(schemaDSN)
	if err != nil {
		t.Fatalf("opening test schema: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		if cleanup, err := sql.Open("pgx", dsn); err == nil {
			_, _ = cleanup.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName))
			cleanup.Close()
		}
	})
	return db
}
