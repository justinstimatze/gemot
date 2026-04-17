package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

// ErrAgentKeyNotFound is returned when the agent has no active (non-revoked) key.
// Aliased to the deliberation package's sentinel so service-layer callers can use
// errors.Is without importing the store package.
var ErrAgentKeyNotFound = deliberation.ErrAgentKeyNotFound

// RegisterAgentKey records a new public key for an agent. Pre-existing active keys
// are revoked in the same transaction so only one key is active at a time.
// Rotation = call RegisterAgentKey with a fresh key.
func (s *DB) RegisterAgentKey(ctx context.Context, agentID string, publicKey []byte, algo string) error {
	if algo == "" {
		algo = "ed25519"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE agent_keys SET revoked_at = NOW() WHERE agent_id = $1 AND revoked_at IS NULL`,
		agentID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_keys (id, agent_id, public_key, algo, registered_at) VALUES ($1, $2, $3, $4, $5)`,
		uuid.New().String(), agentID, publicKey, algo, time.Now().UTC(),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// GetActiveAgentKey returns the most-recently-registered non-revoked key for the agent.
// Returns ErrAgentKeyNotFound if none exists.
func (s *DB) GetActiveAgentKey(ctx context.Context, agentID string) ([]byte, string, error) {
	var pubkey []byte
	var algo string
	err := s.db.QueryRowContext(ctx,
		`SELECT public_key, algo FROM agent_keys
		 WHERE agent_id = $1 AND revoked_at IS NULL
		 ORDER BY registered_at DESC LIMIT 1`,
		agentID,
	).Scan(&pubkey, &algo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrAgentKeyNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return pubkey, algo, nil
}

// RevokeAgentKey marks all active keys for an agent as revoked.
// A no-op if the agent has no active key.
func (s *DB) RevokeAgentKey(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agent_keys SET revoked_at = NOW() WHERE agent_id = $1 AND revoked_at IS NULL`,
		agentID,
	)
	return err
}
