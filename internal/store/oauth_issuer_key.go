package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// NewPostgresOAuthIssuerKeyStore wraps the store.DB's raw handle. Caller
// owns the DB lifecycle.
func NewPostgresOAuthIssuerKeyStore(db *DB) *PostgresOAuthIssuerKeyStore {
	return &PostgresOAuthIssuerKeyStore{db: db.RawDB()}
}

// LoadOrGenerate returns the persisted ed25519 keypair for issuerName,
// generating + persisting if absent. The private key lives in the
// database — treat access to this table like any other secret: it is
// equivalent to gemot's authority to mint a delegation credential for
// anyone who ever proves control of a gmt_ key.
func (s *PostgresOAuthIssuerKeyStore) LoadOrGenerate(ctx context.Context, issuerName string) (pub, priv []byte, err error) {
	// Fast path: the row already exists.
	if pub, priv, ok, err := s.tryLoad(ctx, issuerName); err != nil {
		return nil, nil, err
	} else if ok {
		return pub, priv, nil
	}

	// Generate a candidate and attempt to persist. ON CONFLICT DO NOTHING
	// means concurrent callers race to be the one whose candidate gets
	// stored; the winner's row is then read back by every caller.
	candidatePub, candidatePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("oauth: generate candidate issuer keypair: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO oauth_issuer_keys (issuer_name, private_key, public_key)
		VALUES ($1, $2, $3)
		ON CONFLICT (issuer_name) DO NOTHING
	`, issuerName, []byte(candidatePriv), []byte(candidatePub))
	if err != nil {
		return nil, nil, fmt.Errorf("oauth: insert issuer keypair: %w", err)
	}

	// Re-read to get whichever keypair won the race. Guaranteed to exist
	// after the insert above.
	pub, priv, ok, err := s.tryLoad(ctx, issuerName)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("oauth: issuer key for %q vanished after insert", issuerName)
	}
	return pub, priv, nil
}

func (s *PostgresOAuthIssuerKeyStore) tryLoad(ctx context.Context, issuerName string) (pub, priv []byte, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT public_key, private_key FROM oauth_issuer_keys WHERE issuer_name = $1
	`, issuerName).Scan(&pub, &priv)
	if err == sql.ErrNoRows {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("oauth: query issuer keypair: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize || len(priv) != ed25519.PrivateKeySize {
		return nil, nil, false, fmt.Errorf("oauth: stored issuer keypair for %q has wrong length (pub=%d priv=%d)", issuerName, len(pub), len(priv))
	}
	return pub, priv, true, nil
}

// PostgresOAuthIssuerKeyStore persists the ed25519 signing keypair gemot
// uses as the "gemot-oauth" principal.RemoteIssuer (schema v11) — see
// internal/principal/mint.go. Keys are row-scoped by issuer_name so more
// than one self-issued name could share the table, though only
// "gemot-oauth" exists today.
//
// LoadOrGenerate is atomic: the INSERT ... ON CONFLICT DO NOTHING
// serializes concurrent first-boots — at most one generated keypair
// persists, and every concurrent caller returns that same pair after
// re-reading. Mirrors PostgresReplicaKeyStore's race-safe shape exactly;
// same problem (a server-held signing key must survive restarts or every
// credential it ever signed becomes unverifiable), same fix.
type PostgresOAuthIssuerKeyStore struct {
	db *sql.DB
}
