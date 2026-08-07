package chitgate

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	atxp "github.com/justinstimatze/chit"
)

// TestPGStore_ClientCredentialsRoundTrip exercises the persistence that makes a
// stable DCR client_name viable across restarts. Skips without DATABASE_URL.
func TestPGStore_ClientCredentialsRoundTrip(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping Postgres-backed store test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Skipf("DATABASE_URL set but DB unreachable (%v) — skipping", err)
	}
	// Self-contained: production applies this via schema.sql; ensure it here too.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS atxp_client_credentials (
		issuer TEXT PRIMARY KEY, client_id TEXT NOT NULL, client_secret TEXT NOT NULL DEFAULT '',
		redirect_uri TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`); err != nil {
		t.Fatalf("ensure table: %v", err)
	}

	s := newPGStore(db)
	issuer := "https://test.example/" + t.Name()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM atxp_client_credentials WHERE issuer=$1`, issuer) })
	_, _ = db.Exec(`DELETE FROM atxp_client_credentials WHERE issuer=$1`, issuer) // clean slate

	if _, ok := s.GetClientCredentials(issuer); ok {
		t.Fatal("expected miss before save")
	}

	want := atxp.ClientCredentials{ClientID: "cid-123", ClientSecret: "secret-xyz", RedirectURI: "https://cb"}
	s.SaveClientCredentials(issuer, want)
	if got, ok := s.GetClientCredentials(issuer); !ok || got != want {
		t.Fatalf("round-trip mismatch: got %+v ok=%v, want %+v", got, ok, want)
	}

	// Upsert overwrites in place.
	want2 := atxp.ClientCredentials{ClientID: "cid-456", ClientSecret: "secret-new"}
	s.SaveClientCredentials(issuer, want2)
	if got, ok := s.GetClientCredentials(issuer); !ok || got != want2 {
		t.Fatalf("upsert mismatch: got %+v, want %+v", got, want2)
	}

	// The point of the whole exercise: a FRESH store over the same DB (i.e. a
	// restarted process) sees the persisted creds — no re-registration needed.
	fresh := newPGStore(db)
	if got, ok := fresh.GetClientCredentials(issuer); !ok || got != want2 {
		t.Fatalf("persistence across store instances failed: got %+v ok=%v", got, ok)
	}
}
