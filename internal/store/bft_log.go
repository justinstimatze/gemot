package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/justinstimatze/gemot/internal/bft"
)

// PostgresLogStore persists BFT committed blocks to the bft_log
// table (schema v6). Implements bft.LogStore. Used by session-4
// durable-log wiring; session 5 extends with range queries + a
// per-replica cursor for HTTPTransport block sync.
type PostgresLogStore struct {
	db *sql.DB
}

// NewPostgresLogStore wraps the store.DB's raw handle in a LogStore
// interface. The caller owns the DB lifecycle; the returned store
// holds only a reference.
func NewPostgresLogStore(db *DB) *PostgresLogStore {
	return &PostgresLogStore{db: db.RawDB()}
}

// Append persists a committed-block entry. Enforces height-monotonic
// append via the bft_log table's PRIMARY KEY on (height). Duplicate
// exact-hash entries at the same height are idempotent. Different
// hash at existing height returns ErrLogForkDetected — the caller
// must treat this as a protocol safety violation.
func (s *PostgresLogStore) Append(ctx context.Context, entry bft.LogEntry) error {
	if entry.Block.Height == 0 {
		return errors.New("bft: log refuses genesis (height 0) entry — genesis is implicit")
	}
	blockBytes, err := bft.EncodeBlock(entry.Block)
	if err != nil {
		return fmt.Errorf("bft: encode block: %w", err)
	}
	qcBytes, err := bft.EncodeQC(entry.QC)
	if err != nil {
		return fmt.Errorf("bft: encode QC: %w", err)
	}
	blockHash := entry.Block.Hash()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO bft_log (height, block_hash, block_bytes, qc_bytes)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (height) DO NOTHING
	`, int64(entry.Block.Height), blockHash[:], blockBytes, qcBytes)
	if err != nil {
		return fmt.Errorf("bft: insert bft_log: %w", err)
	}
	// ON CONFLICT DO NOTHING swallows both "same-hash reinsert"
	// (idempotent — intended) and "different-hash same-height"
	// (fork — must escalate). Distinguish with an explicit read.
	var existingHash []byte
	err = s.db.QueryRowContext(ctx,
		`SELECT block_hash FROM bft_log WHERE height = $1`,
		int64(entry.Block.Height),
	).Scan(&existingHash)
	if err != nil {
		return fmt.Errorf("bft: verify bft_log insert: %w", err)
	}
	if string(existingHash) != string(blockHash[:]) {
		return fmt.Errorf("%w: height %d existing hash %x, new hash %x",
			bft.ErrLogForkDetected, entry.Block.Height, existingHash, blockHash[:])
	}
	return nil
}

// Load returns every committed block+QC in height-ascending order.
// Session 5 adds a range variant for replica-sync streaming.
func (s *PostgresLogStore) Load(ctx context.Context) ([]bft.LogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT block_bytes, qc_bytes FROM bft_log ORDER BY height ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("bft: query bft_log: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []bft.LogEntry
	for rows.Next() {
		var blockBytes, qcBytes []byte
		if err := rows.Scan(&blockBytes, &qcBytes); err != nil {
			return nil, fmt.Errorf("bft: scan bft_log: %w", err)
		}
		block, err := bft.DecodeBlock(blockBytes)
		if err != nil {
			return nil, err
		}
		qc, err := bft.DecodeQC(qcBytes)
		if err != nil {
			return nil, err
		}
		out = append(out, bft.LogEntry{Block: block, QC: qc})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bft: iterate bft_log: %w", err)
	}
	return out, nil
}

// HighestHeight returns the max height in bft_log, or 0 if empty.
func (s *PostgresLogStore) HighestHeight(ctx context.Context) (bft.Height, error) {
	var h sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(height) FROM bft_log`,
	).Scan(&h)
	if err != nil {
		return 0, fmt.Errorf("bft: query max height: %w", err)
	}
	if !h.Valid {
		return 0, nil
	}
	return bft.Height(h.Int64), nil
}

// WithLock runs fn while holding the cluster-wide BFT append advisory lock,
// serializing the propose→append→commit round across every gemot machine
// sharing this Postgres log. Implements bft.ClusterLocker.
//
// The lock is acquired and released on a single dedicated pooled connection
// (session-level pg_advisory_lock is per-connection, so acquire and release
// MUST use the same one), and that connection returns to the pool only after
// the unlock. If the holding process dies mid-round, Postgres drops the
// session and releases the lock automatically — a crash can never wedge the
// cluster. The unlock runs on context.Background so a canceled request ctx
// still releases the lock before the connection is reused.
func (s *PostgresLogStore) WithLock(ctx context.Context, key int64, fn func() error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("bft: acquire advisory-lock conn: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		return fmt.Errorf("bft: pg_advisory_lock: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", key)
	}()
	return fn()
}
