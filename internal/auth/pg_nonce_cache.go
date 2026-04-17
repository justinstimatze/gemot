package auth

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PostgresNonceCache is a durable, multi-instance-safe NonceCache backed by
// the envelope_nonces table. Observe uses INSERT ... ON CONFLICT DO NOTHING
// so concurrent observations across replicas converge on the same winner
// without a distributed lock. Expired rows are removed by a background
// janitor (see StartJanitor), which also protects the table from unbounded
// growth under adversarial load — the PRIMARY KEY on nonce bounds per-entry
// cost and the expires_at index keeps the janitor's DELETE cheap.
type PostgresNonceCache struct {
	db  *sql.DB
	ttl time.Duration
}

// NewPostgresNonceCache returns a NonceCache that persists nonces to Postgres.
// ttl is how long a nonce remains "seen" after first observation. Pass ttl=0
// to use the default 2*ReplayWindow (matches MemoryNonceCache default).
// The caller is responsible for ensuring the envelope_nonces table exists
// (the store package's schema bootstrap handles this on server startup).
func NewPostgresNonceCache(db *sql.DB, ttl time.Duration) *PostgresNonceCache {
	if ttl <= 0 {
		ttl = 2 * ReplayWindow
	}
	return &PostgresNonceCache{db: db, ttl: ttl}
}

// Observe records the nonce. Returns ErrReplay if a non-expired row with the
// same nonce already exists. Thread-safe and replica-safe: the UNIQUE primary
// key + INSERT...ON CONFLICT DO NOTHING gives us at-most-once-winner semantics
// across concurrent observers.
//
// Important: an expired-but-not-yet-swept row will still collide on INSERT
// and be treated as a replay. That's conservative but strictly safer than
// racing the janitor — the false-replay window closes as soon as the next
// sweep runs. See StartJanitor for sweep cadence.
func (p *PostgresNonceCache) Observe(nonce string, now time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	expires := now.Add(p.ttl)
	res, err := p.db.ExecContext(ctx,
		`INSERT INTO envelope_nonces (nonce, expires_at) VALUES ($1, $2)
		 ON CONFLICT (nonce) DO NOTHING`,
		nonce, expires)
	if err != nil {
		return fmt.Errorf("pg nonce observe: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("pg nonce rows affected: %w", err)
	}
	if n == 0 {
		return ErrReplay
	}
	return nil
}

// StartJanitor launches a background goroutine that periodically deletes
// expired nonces. The goroutine exits when ctx is canceled. Safe to run on
// multiple replicas simultaneously — DELETE is idempotent and the query is
// bounded by the expires_at index so the duplicate work is cheap.
//
// interval=0 uses the default of ReplayWindow (so we sweep at least twice
// per TTL period, bounding the false-replay window on expired rows).
func (p *PostgresNonceCache) StartJanitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = ReplayWindow
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				_, _ = p.db.ExecContext(sweepCtx,
					`DELETE FROM envelope_nonces WHERE expires_at < NOW()`)
				cancel()
			}
		}
	}()
}
