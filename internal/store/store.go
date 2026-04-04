package store

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schema string

type DB struct {
	db   *sql.DB
	path string
}

func Open(dsn string) (*DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close() //nolint:errcheck
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		if cerr := db.Close(); cerr != nil {
			return nil, fmt.Errorf("running migrations: %w (also failed to close: %v)", err, cerr)
		}
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return &DB{db: db, path: dsn}, nil
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
