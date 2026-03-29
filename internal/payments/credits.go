package payments

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// CreditStore manages API keys and credit balances in SQLite.
type CreditStore struct {
	db *sql.DB
	mu sync.Mutex
}

// NewCreditStore opens or creates the credits table in the given SQLite DB.
func NewCreditStore(db *sql.DB) (*CreditStore, error) {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS api_keys (
			key TEXT PRIMARY KEY,
			email TEXT NOT NULL,
			credits_remaining INTEGER DEFAULT 0,
			stripe_customer_id TEXT DEFAULT '',
			stripe_session_id TEXT DEFAULT '',
			created_at TEXT DEFAULT (datetime('now')),
			last_used_at TEXT DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_api_keys_email ON api_keys(email);
		CREATE INDEX IF NOT EXISTS idx_api_keys_stripe ON api_keys(stripe_customer_id);
	`)
	if err != nil {
		return nil, fmt.Errorf("creating api_keys table: %w", err)
	}
	// Migration: add suspended column (idempotent)
	db.Exec("ALTER TABLE api_keys ADD COLUMN suspended INTEGER DEFAULT 0") //nolint:errcheck
	return &CreditStore{db: db}, nil
}

// GenerateKey creates a new API key with the given credits.
func (s *CreditStore) GenerateKey(email, stripeCustomerID, stripeSessionID string, credits int) (string, error) {
	key, err := randomKey()
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = s.db.Exec(
		`INSERT INTO api_keys (key, email, credits_remaining, stripe_customer_id, stripe_session_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		key, email, credits, stripeCustomerID, stripeSessionID, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return "", err
	}
	return key, nil
}

// KeyID derives a stable 8-char identifier from an API key via SHA256.
// Used to scope agent identities to their API key owner.
// Hashed to avoid leaking any prefix of the actual key material.
func KeyID(key string) string {
	if key == "" {
		return ""
	}
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:4]) // 8 hex chars = 32 bits, collision-resistant for <10K keys
}

// AddCredits adds credits to an existing key. Returns new balance.
func (s *CreditStore) AddCredits(key string, amount int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`UPDATE api_keys SET credits_remaining = credits_remaining + ? WHERE key = ?`,
		amount, key,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return 0, fmt.Errorf("api key not found")
	}

	var balance int
	err = s.db.QueryRow(`SELECT credits_remaining FROM api_keys WHERE key = ?`, key).Scan(&balance)
	return balance, err
}

// AddCreditsByEmail adds credits to the most recent key for an email. Returns the key and new balance.
func (s *CreditStore) AddCreditsByEmail(email string, amount int) (string, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var key string
	err := s.db.QueryRow(`SELECT key FROM api_keys WHERE email = ? ORDER BY created_at DESC LIMIT 1`, email).Scan(&key)
	if err != nil {
		return "", 0, fmt.Errorf("no api key found for email %q", email)
	}

	_, err = s.db.Exec(
		`UPDATE api_keys SET credits_remaining = credits_remaining + ? WHERE key = ?`,
		amount, key,
	)
	if err != nil {
		return "", 0, err
	}

	var balance int
	err = s.db.QueryRow(`SELECT credits_remaining FROM api_keys WHERE key = ?`, key).Scan(&balance)
	return key, balance, err
}

// Deduct attempts to deduct credits from a key. Returns remaining balance.
// Returns error if insufficient credits.
func (s *CreditStore) Deduct(key string, amount int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var balance int
	err := s.db.QueryRow(`SELECT credits_remaining FROM api_keys WHERE key = ?`, key).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("invalid api key")
	}

	if balance < amount {
		return balance, fmt.Errorf("insufficient credits: have %d, need %d", balance, amount)
	}

	_, err = s.db.Exec(
		`UPDATE api_keys SET credits_remaining = credits_remaining - ?, last_used_at = ? WHERE key = ?`,
		amount, time.Now().UTC().Format(time.RFC3339), key,
	)
	if err != nil {
		return 0, err
	}

	return balance - amount, nil
}

// GetBalance returns the credit balance for a key.
func (s *CreditStore) GetBalance(key string) (int, error) {
	var balance int
	err := s.db.QueryRow(`SELECT credits_remaining FROM api_keys WHERE key = ?`, key).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("invalid api key")
	}
	return balance, nil
}

// ValidateKey checks if a key exists, has credits > 0, and is not suspended.
func (s *CreditStore) ValidateKey(key string) (bool, error) {
	var balance, suspended int
	err := s.db.QueryRow(`SELECT credits_remaining, COALESCE(suspended, 0) FROM api_keys WHERE key = ?`, key).Scan(&balance, &suspended)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if suspended != 0 {
		return false, nil
	}
	return balance > 0, nil
}

// SuspendKey marks an API key as suspended. Suspended keys fail validation.
func (s *CreditStore) SuspendKey(key string) error {
	_, err := s.db.Exec(`UPDATE api_keys SET suspended = 1 WHERE key = ?`, key)
	return err
}

// UnsuspendKey removes the suspension from an API key.
func (s *CreditStore) UnsuspendKey(key string) error {
	_, err := s.db.Exec(`UPDATE api_keys SET suspended = 0 WHERE key = ?`, key)
	return err
}

func randomKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gmt_" + hex.EncodeToString(b), nil
}

// Credit pack definitions
const (
	PackStarter  = 1000  // $5 = 1000 credits
	PackStandard = 4500  // $20 = 4500 credits (10% bonus)
	PackPro      = 12000 // $50 = 12000 credits (20% bonus)

	CostSonnet = 60  // credits per Sonnet analyze
	CostOpus   = 300 // credits per Opus analyze
	CostHaiku  = 20  // credits per Haiku analyze
)

// CreditCost returns the credit cost for a model.
func CreditCost(model string) int {
	switch model {
	case "claude-opus-4-6":
		return CostOpus
	case "claude-haiku-4-5":
		return CostHaiku
	default:
		return CostSonnet
	}
}
