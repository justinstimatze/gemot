package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/justinstimatze/gemot/internal/bft"
)

// PostgresVoteHistoryStore persists a replica's anti-equivocation
// counters to the bft_vote_history table (schema v7). Implements
// bft.VoteHistoryStore. Each replica scopes its writes by replica_id
// so multiple replicas in the same database (tests, single-process
// multi-replica deploys) stay isolated.
//
// Writes are monotonic: SaveVote / SaveProposal UPSERT using GREATEST
// against the existing value, so a stale retry that arrives after a
// newer write cannot regress the persisted view.
type PostgresVoteHistoryStore struct {
	db        *sql.DB
	replicaID string
}

// NewPostgresVoteHistoryStore binds a store.DB to a specific replica
// identifier. The caller owns the DB lifecycle; the returned store
// holds only a reference.
func NewPostgresVoteHistoryStore(db *DB, replicaID bft.ReplicaID) *PostgresVoteHistoryStore {
	return &PostgresVoteHistoryStore{db: db.RawDB(), replicaID: replicaIDString(replicaID)}
}

func replicaIDString(id bft.ReplicaID) string {
	return fmt.Sprintf("%d", id)
}

// SaveVote records that the replica is voting in view v. Monotonic:
// an in-flight retry with a stale view cannot clobber a newer stored
// value because the UPSERT takes GREATEST of (existing, new).
func (s *PostgresVoteHistoryStore) SaveVote(ctx context.Context, v bft.View) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO bft_vote_history (replica_id, last_voted_view, last_proposed_view, updated_at)
		VALUES ($1, $2, 0, NOW())
		ON CONFLICT (replica_id) DO UPDATE SET
			last_voted_view = GREATEST(bft_vote_history.last_voted_view, EXCLUDED.last_voted_view),
			updated_at = NOW()
	`, s.replicaID, int64(v))
	if err != nil {
		return fmt.Errorf("bft: upsert vote history: %w", err)
	}
	return nil
}

// SaveProposal records that the replica is proposing in view v.
// Same monotonic UPSERT semantics as SaveVote.
func (s *PostgresVoteHistoryStore) SaveProposal(ctx context.Context, v bft.View) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO bft_vote_history (replica_id, last_voted_view, last_proposed_view, updated_at)
		VALUES ($1, 0, $2, NOW())
		ON CONFLICT (replica_id) DO UPDATE SET
			last_proposed_view = GREATEST(bft_vote_history.last_proposed_view, EXCLUDED.last_proposed_view),
			updated_at = NOW()
	`, s.replicaID, int64(v))
	if err != nil {
		return fmt.Errorf("bft: upsert proposal history: %w", err)
	}
	return nil
}

// Load returns the persisted (lastVoted, lastProposed) for this
// replica. Returns (0, 0, nil) when no row exists yet.
func (s *PostgresVoteHistoryStore) Load(ctx context.Context) (bft.View, bft.View, error) {
	var lastVoted, lastProposed sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT last_voted_view, last_proposed_view
		FROM bft_vote_history WHERE replica_id = $1
	`, s.replicaID).Scan(&lastVoted, &lastProposed)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("bft: query vote history: %w", err)
	}
	return bft.View(lastVoted.Int64), bft.View(lastProposed.Int64), nil
}
