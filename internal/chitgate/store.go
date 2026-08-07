package chitgate

import (
	"database/sql"
	"errors"
	"log/slog"

	atxp "github.com/justinstimatze/chit"
)

// pgStore is a Postgres-backed atxp.Store. It persists the one thing a merchant
// must not lose across restarts — the dynamic-client-registration credentials
// keyed by auth-server issuer — and delegates everything else (PKCE, access
// tokens, which are OAuth client-side state a bare-402 merchant never uses) to
// an embedded in-memory store.
//
// Why this matters: chit's default Store is in-memory, so a fresh process
// re-registers a DCR client on every boot. The auth server keeps the previous
// client_name claimed, but the secret died with that process — so a stable name
// then 409s (client_name_taken) forever, and the only workaround is a unique
// name per launch (which orphans a registration each time). Persisting the creds
// lets every restart and every instance reuse a single registration under a
// stable name.
type pgStore struct {
	// Embedded in-memory store provides PKCE + access-token methods (unused by a
	// bare-402 merchant) and doubles as a same-process fallback when a DB call
	// fails, so a transient DB error degrades to "works now, doesn't persist"
	// rather than breaking the charge path.
	*atxp.MemoryStore
	db *sql.DB
}

// Compile-time proof pgStore satisfies the interface chit's Config.Store wants.
var _ atxp.Store = (*pgStore)(nil)

// newPGStore returns a Postgres-backed Store. db must be non-nil.
func newPGStore(db *sql.DB) *pgStore {
	return &pgStore{MemoryStore: atxp.NewMemoryStore(), db: db}
}

// SaveClientCredentials upserts the DCR credentials for an issuer. On a DB error
// it falls back to the in-memory store so the current process still works (it
// just won't persist across restart) and logs the failure — the secret itself
// is never logged.
func (s *pgStore) SaveClientCredentials(issuer string, c atxp.ClientCredentials) {
	_, err := s.db.Exec(`
		INSERT INTO atxp_client_credentials (issuer, client_id, client_secret, redirect_uri)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (issuer) DO UPDATE
		  SET client_id     = EXCLUDED.client_id,
		      client_secret = EXCLUDED.client_secret,
		      redirect_uri  = EXCLUDED.redirect_uri`,
		issuer, c.ClientID, c.ClientSecret, c.RedirectURI)
	if err != nil {
		slog.Error("chitgate: persisting ATXP client credentials failed; using in-memory only", "issuer", issuer, "error", err)
		s.MemoryStore.SaveClientCredentials(issuer, c)
	}
}

// GetClientCredentials reads the DCR credentials for an issuer from Postgres,
// falling back to the in-memory store — for creds saved this process after a DB
// write failure — when the row is absent or the DB is unreachable.
func (s *pgStore) GetClientCredentials(issuer string) (atxp.ClientCredentials, bool) {
	var c atxp.ClientCredentials
	err := s.db.QueryRow(
		`SELECT client_id, client_secret, redirect_uri FROM atxp_client_credentials WHERE issuer = $1`,
		issuer,
	).Scan(&c.ClientID, &c.ClientSecret, &c.RedirectURI)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return s.MemoryStore.GetClientCredentials(issuer)
	case err != nil:
		slog.Error("chitgate: reading ATXP client credentials failed; using in-memory only", "issuer", issuer, "error", err)
		return s.MemoryStore.GetClientCredentials(issuer)
	default:
		return c, true
	}
}
