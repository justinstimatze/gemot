// migrate-demos copies deliberation data between gemot Postgres instances.
//
// Usage:
//
//	go run scripts/migrate-demos/ --from "postgres://..." --to "postgres://..." --group gemot_v15a
//	go run scripts/migrate-demos/ --from "postgres://..." --to "postgres://..." --deliberation <id>
//
// Copies: deliberations, positions, votes, analysis_results, commitments, audit_log.
// Skips rows that already exist in the target (idempotent).
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	fromURL := flag.String("from", os.Getenv("DATABASE_URL"), "Source database URL")
	toURL := flag.String("to", "", "Target database URL (required)")
	groupID := flag.String("group", "", "Migrate all deliberations in this group")
	delibID := flag.String("deliberation", "", "Migrate a single deliberation")
	dryRun := flag.Bool("dry-run", false, "Show what would be copied without writing")
	flag.Parse()

	if *toURL == "" {
		log.Fatal("--to is required")
	}
	if *groupID == "" && *delibID == "" {
		log.Fatal("--group or --deliberation is required")
	}

	ctx := context.Background()

	src, err := sql.Open("pgx", *fromURL)
	if err != nil {
		log.Fatalf("connecting to source: %v", err)
	}
	defer src.Close()

	dst, err := sql.Open("pgx", *toURL)
	if err != nil {
		log.Fatalf("connecting to target: %v", err)
	}
	defer dst.Close()

	// Collect deliberation IDs to migrate
	var delibIDs []string
	if *delibID != "" {
		delibIDs = []string{*delibID}
	} else {
		rows, err := src.QueryContext(ctx, "SELECT id FROM deliberations WHERE group_id = $1", *groupID)
		if err != nil {
			log.Fatalf("listing deliberations: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			rows.Scan(&id)
			delibIDs = append(delibIDs, id)
		}
	}

	if len(delibIDs) == 0 {
		log.Fatal("no deliberations found")
	}
	fmt.Printf("Migrating %d deliberation(s)\n", len(delibIDs))

	for _, did := range delibIDs {
		if *dryRun {
			fmt.Printf("  [dry-run] %s\n", did)
			continue
		}
		if err := migrateDeliberation(ctx, src, dst, did); err != nil {
			fmt.Fprintf(os.Stderr, "  ERROR %s: %v\n", did, err)
		}
	}

	fmt.Println("Done.")
}

func migrateDeliberation(ctx context.Context, src, dst *sql.DB, delibID string) error {
	tables := []struct {
		name      string
		filterCol string
	}{
		{"deliberations", "id"},
		{"positions", "deliberation_id"},
		{"votes", "deliberation_id"},
		{"analysis_results", "deliberation_id"},
		{"commitments", "deliberation_id"},
		{"audit_log", "deliberation_id"},
	}

	for i, t := range tables {
		n, err := copyRows(ctx, src, dst, t.name, t.filterCol, delibID)
		if err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
		if i == 0 {
			fmt.Printf("  %s: %d %s\n", delibID[:8], n, t.name)
		} else if n > 0 {
			fmt.Printf("           %d %s\n", n, t.name)
		}
	}
	return nil
}

// sharedColumns discovers columns that exist in both source and target for a table.
func sharedColumns(ctx context.Context, src, dst *sql.DB, table string) ([]string, error) {
	getCols := func(db *sql.DB) (map[string]bool, error) {
		rows, err := db.QueryContext(ctx,
			"SELECT column_name FROM information_schema.columns WHERE table_name = $1", table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		cols := map[string]bool{}
		for rows.Next() {
			var col string
			rows.Scan(&col)
			cols[col] = true
		}
		return cols, rows.Err()
	}

	srcCols, err := getCols(src)
	if err != nil {
		return nil, fmt.Errorf("source schema: %w", err)
	}
	dstCols, err := getCols(dst)
	if err != nil {
		return nil, fmt.Errorf("target schema: %w", err)
	}

	// Use source column order, filtered to shared
	rows, err := src.QueryContext(ctx,
		"SELECT column_name FROM information_schema.columns WHERE table_name = $1 ORDER BY ordinal_position", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shared []string
	for rows.Next() {
		var col string
		rows.Scan(&col)
		if srcCols[col] && dstCols[col] {
			shared = append(shared, col)
		}
	}
	return shared, rows.Err()
}

func copyRows(ctx context.Context, src, dst *sql.DB, table, filterCol, filterVal string) (int, error) {
	cols, err := sharedColumns(ctx, src, dst, table)
	if err != nil {
		return 0, fmt.Errorf("schema discovery: %w", err)
	}
	if len(cols) == 0 {
		return 0, fmt.Errorf("no shared columns for %s", table)
	}

	colList := strings.Join(cols, ", ")
	placeholders := make([]string, len(cols))
	for i := range cols {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	selectSQL := fmt.Sprintf("SELECT %s FROM %s WHERE %s = $1", colList, table, filterCol)
	conflict := "ON CONFLICT DO NOTHING"
	// analysis_results: upsert so validation data updates propagate
	if table == "analysis_results" {
		conflict = "ON CONFLICT (deliberation_id, round_number) DO UPDATE SET result_json = EXCLUDED.result_json, analyzed_at = EXCLUDED.analyzed_at"
	}
	insertSQL := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) %s",
		table, colList, strings.Join(placeholders, ", "), conflict)

	rows, err := src.QueryContext(ctx, selectSQL, filterVal)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return count, fmt.Errorf("scan: %w", err)
		}
		if _, err := dst.ExecContext(ctx, insertSQL, vals...); err != nil {
			return count, fmt.Errorf("insert: %w", err)
		}
		count++
	}
	return count, rows.Err()
}
