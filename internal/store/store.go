package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type DB struct {
	db   *sql.DB
	path string
}

func Open(path string) (*DB, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// SQLite is single-writer; limit connections to prevent SQLITE_BUSY under concurrency
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		if cerr := db.Close(); cerr != nil {
			return nil, fmt.Errorf("running migrations: %w (also failed to close: %v)", err, cerr)
		}
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	// Migrations (idempotent — ALTER TABLE errors if column exists, which is fine)
	db.Exec("ALTER TABLE deliberations ADD COLUMN sub_status TEXT DEFAULT ''")              //nolint:errcheck
	db.Exec("ALTER TABLE deliberations ADD COLUMN status_changed_at TEXT DEFAULT ''")       //nolint:errcheck
	db.Exec("ALTER TABLE positions ADD COLUMN model_family TEXT DEFAULT ''")                //nolint:errcheck
	db.Exec("ALTER TABLE deliberations ADD COLUMN type TEXT DEFAULT ''")                   //nolint:errcheck
	db.Exec("ALTER TABLE deliberations ADD COLUMN visibility TEXT DEFAULT 'open'")         //nolint:errcheck
	db.Exec("ALTER TABLE deliberations ADD COLUMN creator_key TEXT DEFAULT ''")             //nolint:errcheck
	db.Exec("ALTER TABLE deliberations ADD COLUMN max_participants INTEGER DEFAULT 0")     //nolint:errcheck
	db.Exec("ALTER TABLE positions ADD COLUMN group_name TEXT DEFAULT ''")                  //nolint:errcheck
	db.Exec("ALTER TABLE positions ADD COLUMN conviction REAL DEFAULT 0.5")                //nolint:errcheck
	db.Exec("ALTER TABLE positions ADD COLUMN reservation TEXT DEFAULT ''")                //nolint:errcheck
	db.Exec("ALTER TABLE positions ADD COLUMN on_behalf_of TEXT DEFAULT ''")               //nolint:errcheck
	db.Exec("ALTER TABLE positions ADD COLUMN draft INTEGER DEFAULT 0")                    //nolint:errcheck
	db.Exec("ALTER TABLE votes ADD COLUMN criterion_id TEXT DEFAULT ''")                   //nolint:errcheck
	db.Exec("ALTER TABLE deliberations ADD COLUMN template TEXT DEFAULT ''")               //nolint:errcheck

	// Security tables
	db.Exec(`CREATE TABLE IF NOT EXISTS abuse_reports (
		id TEXT PRIMARY KEY,
		deliberation_id TEXT NOT NULL,
		reporter_key TEXT DEFAULT '',
		reason TEXT NOT NULL,
		created_at TEXT DEFAULT (datetime('now'))
	)`) //nolint:errcheck

	db.Exec(`CREATE TABLE IF NOT EXISTS audit_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT DEFAULT (datetime('now')),
		key_id TEXT DEFAULT '',
		ip TEXT DEFAULT '',
		method TEXT NOT NULL,
		deliberation_id TEXT DEFAULT '',
		agent_id TEXT DEFAULT ''
	)`) //nolint:errcheck

	// Note: api_keys.suspended column is added in payments.NewCreditStore (where the table is created)
	db.Exec("ALTER TABLE deliberations ADD COLUMN rules TEXT DEFAULT '{}'") //nolint:errcheck
	db.Exec("ALTER TABLE positions ADD COLUMN interests TEXT DEFAULT ''")  //nolint:errcheck

	// Multi-use join codes for sandbox deliberations
	db.Exec("ALTER TABLE join_codes ADD COLUMN max_uses INTEGER DEFAULT 1")  //nolint:errcheck
	db.Exec("ALTER TABLE join_codes ADD COLUMN use_count INTEGER DEFAULT 0") //nolint:errcheck
	db.Exec("UPDATE join_codes SET max_uses = 1 WHERE max_uses = 0")        //nolint:errcheck

	// Forced acknowledgment: track which agents have read context before round 2+
	db.Exec(`CREATE TABLE IF NOT EXISTS context_access (
		deliberation_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		round INTEGER NOT NULL,
		accessed_at TEXT DEFAULT (datetime('now')),
		PRIMARY KEY (deliberation_id, agent_id, round)
	)`) //nolint:errcheck

	return &DB{db: db, path: path}, nil
}

func (s *DB) Close() error {
	return s.db.Close()
}

// RawDB returns the underlying *sql.DB for shared use (e.g., credit store).
func (s *DB) RawDB() *sql.DB {
	return s.db
}

// WrapRawDB wraps an existing *sql.DB as a store.DB (for when you already have the raw DB).
func WrapRawDB(rawDB *sql.DB) (*DB, error) {
	return &DB{db: rawDB}, nil
}
