package payments

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestKeyActive_IgnoresBalance guards the bootstrap-deadlock fix: authentication
// must key on "real, unsuspended key", NOT on balance. A zero-balance key must
// be active (so it can authenticate to call free tools and buy_credits to top
// up), while a suspended or missing key must not. It also pins the contrast that
// ValidateKey (the balance predicate) still rejects a zero-balance key. Skips
// without DATABASE_URL.
func TestKeyActive_IgnoresBalance(t *testing.T) {
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
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
		key TEXT PRIMARY KEY, email TEXT NOT NULL, credits_remaining INTEGER DEFAULT 0,
		stripe_customer_id TEXT DEFAULT '', stripe_session_id TEXT DEFAULT '',
		suspended INTEGER DEFAULT 0, created_at TIMESTAMPTZ DEFAULT NOW(), last_used_at TIMESTAMPTZ)`); err != nil {
		t.Fatalf("ensure table: %v", err)
	}
	cs, err := NewCreditStore(db)
	if err != nil {
		t.Fatalf("new credit store: %v", err)
	}

	zeroKey := "gmt_keyactive_zero_" + t.Name()
	suspKey := "gmt_keyactive_susp_" + t.Name()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM api_keys WHERE key IN ($1,$2)`, zeroKey, suspKey) })
	_, _ = db.Exec(`DELETE FROM api_keys WHERE key IN ($1,$2)`, zeroKey, suspKey) // clean slate

	if _, err := db.Exec(`INSERT INTO api_keys (key, email, credits_remaining, suspended) VALUES ($1,$2,0,0)`, zeroKey, "zero@test"); err != nil {
		t.Fatalf("insert zero-balance key: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO api_keys (key, email, credits_remaining, suspended) VALUES ($1,$2,0,1)`, suspKey, "susp@test"); err != nil {
		t.Fatalf("insert suspended key: %v", err)
	}

	// Active predicate ignores balance: a zero-credit, non-suspended key IS active.
	if ok, err := cs.KeyActive(zeroKey); err != nil || !ok {
		t.Fatalf("KeyActive(zero-balance) = %v, %v; want true (bootstrap must authenticate)", ok, err)
	}
	// Suspended key is not active.
	if ok, _ := cs.KeyActive(suspKey); ok {
		t.Fatal("KeyActive(suspended) = true; want false")
	}
	// Missing key is not active.
	if ok, _ := cs.KeyActive("gmt_does_not_exist_" + t.Name()); ok {
		t.Fatal("KeyActive(missing) = true; want false")
	}
	// Contrast: ValidateKey (the balance predicate) still rejects the zero key.
	if ok, _ := cs.ValidateKey(zeroKey); ok {
		t.Fatal("ValidateKey(zero-balance) = true; want false — balance predicate unchanged")
	}
}
