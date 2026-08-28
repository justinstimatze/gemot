package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/justinstimatze/gemot/internal/bft"
)

// PostgresReplicaKeyStore persists a replica's BLS keypair to the
// bft_replica_keys table (schema v8). Implements bft.ReplicaKeyStore.
// Keys are row-scoped by replica_id so multiple replicas sharing the
// same database (tests, single-process multi-replica deploys) stay
// independent.
//
// LoadOrGenerate is atomic: the INSERT ... ON CONFLICT DO NOTHING
// serializes concurrent first-boots — at most one generated keypair
// persists, and every concurrent caller returns that same pair after
// re-reading.
type PostgresReplicaKeyStore struct {
	db *sql.DB
}

// NewPostgresReplicaKeyStore wraps the store.DB's raw handle. Caller
// owns the DB lifecycle.
func NewPostgresReplicaKeyStore(db *DB) *PostgresReplicaKeyStore {
	return &PostgresReplicaKeyStore{db: db.RawDB()}
}

// LoadOrGenerate returns the persisted keypair for replicaID,
// generating + persisting if absent. The private key lives in the
// database — treat access to this table like any other secret.
func (s *PostgresReplicaKeyStore) LoadOrGenerate(ctx context.Context, replicaID bft.ReplicaID) (bft.BLSKeypair, error) {
	replicaStr := fmt.Sprintf("%d", replicaID)
	return loadOrGenerateKeypair(
		func() (bft.BLSKeypair, bool, error) { return s.tryLoad(ctx, replicaStr) },
		func() (bft.BLSKeypair, error) {
			kp, err := bft.GenerateBLSKeypair()
			if err != nil {
				return bft.BLSKeypair{}, fmt.Errorf("bft: generate candidate keypair: %w", err)
			}
			return kp, nil
		},
		func(kp bft.BLSKeypair) error {
			privBytes, pubBytes := kp.Marshal()
			_, err := s.db.ExecContext(ctx, `
				INSERT INTO bft_replica_keys (replica_id, private_key, public_key)
				VALUES ($1, $2, $3)
				ON CONFLICT (replica_id) DO NOTHING
			`, replicaStr, privBytes, pubBytes)
			if err != nil {
				return fmt.Errorf("bft: insert replica keypair: %w", err)
			}
			return nil
		},
	)
}

func (s *PostgresReplicaKeyStore) tryLoad(ctx context.Context, replicaStr string) (bft.BLSKeypair, bool, error) {
	var priv, pub []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT private_key, public_key FROM bft_replica_keys WHERE replica_id = $1
	`, replicaStr).Scan(&priv, &pub)
	if err == sql.ErrNoRows {
		return bft.BLSKeypair{}, false, nil
	}
	if err != nil {
		return bft.BLSKeypair{}, false, fmt.Errorf("bft: query replica keypair: %w", err)
	}
	kp, err := bft.UnmarshalBLSKeypair(priv, pub)
	if err != nil {
		return bft.BLSKeypair{}, false, fmt.Errorf("bft: unmarshal stored keypair for %s: %w", replicaStr, err)
	}
	return kp, true, nil
}
