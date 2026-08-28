package payments

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// CreditStore manages API keys and credit balances.
type CreditStore struct {
	db *sql.DB
}

// NewCreditStore initializes the credit store using the shared DB.
// The api_keys table is created in schema.sql; this is a no-op for table creation.
func NewCreditStore(db *sql.DB) (*CreditStore, error) {
	return &CreditStore{db: db}, nil
}

// GenerateKey creates a new API key with the given credits.
func (s *CreditStore) GenerateKey(email, stripeCustomerID, stripeSessionID string, credits int) (string, error) {
	key, err := RandomKey("gmt_")
	if err != nil {
		return "", err
	}

	_, err = s.db.Exec(
		`INSERT INTO api_keys (key, email, credits_remaining, stripe_customer_id, stripe_session_id, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		key, email, credits, stripeCustomerID, stripeSessionID, time.Now().UTC(),
	)
	if err != nil {
		return "", err
	}

	return key, nil
}

// KeyID derives a stable 16-char identifier from an API key via SHA256.
// Used to scope agent identities to their API key owner.
// Hashed to avoid leaking any prefix of the actual key material.
func KeyID(key string) string {
	if key == "" {
		return ""
	}
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:8]) // 16 hex chars = 64 bits, collision-resistant for millions of keys
}

// AddCredits adds credits to an existing key. Returns new balance.
func (s *CreditStore) AddCredits(key string, amount int) (int, error) {
	var balance int
	err := s.db.QueryRow(
		`UPDATE api_keys SET credits_remaining = credits_remaining + $1 WHERE key = $2 RETURNING credits_remaining`,
		amount, key,
	).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("api key not found")
	}
	return balance, nil
}

// AddCreditsByEmail adds credits to the most recent key for an email. Returns the key and new balance.
// Also sets stripe_session_id for idempotency (partial unique index prevents double-crediting).
func (s *CreditStore) AddCreditsByEmail(email string, amount int, sessionID ...string) (string, int, error) {
	var key string
	err := s.db.QueryRow(`SELECT key FROM api_keys WHERE email = $1 ORDER BY created_at DESC LIMIT 1`, email).Scan(&key)
	if err != nil {
		return "", 0, fmt.Errorf("no api key found for email %q", email)
	}

	var balance int
	if len(sessionID) > 0 && sessionID[0] != "" {
		// Idempotency: only credit if this session hasn't already been applied.
		// The WHERE clause rejects the UPDATE if stripe_session_id already matches,
		// preventing double-crediting from duplicate webhook deliveries.
		err = s.db.QueryRow(
			`UPDATE api_keys SET credits_remaining = credits_remaining + $1, stripe_session_id = $3 WHERE key = $2 AND (stripe_session_id IS DISTINCT FROM $3) RETURNING credits_remaining`,
			amount, key, sessionID[0],
		).Scan(&balance)
		if err != nil {
			return key, 0, fmt.Errorf("credits already applied for session %s (idempotency check)", sessionID[0])
		}
		return key, balance, nil
	} else {
		err = s.db.QueryRow(
			`UPDATE api_keys SET credits_remaining = credits_remaining + $1 WHERE key = $2 RETURNING credits_remaining`,
			amount, key,
		).Scan(&balance)
	}
	if err != nil {
		return "", 0, err
	}
	return key, balance, nil
}

// Deduct attempts to deduct credits from a key. Returns remaining balance.
// Returns error if insufficient credits.
// Uses a single atomic UPDATE with RETURNING to eliminate TOCTOU races.
func (s *CreditStore) Deduct(key string, amount int) (int, error) {
	var balance int
	err := s.db.QueryRow(
		`UPDATE api_keys SET credits_remaining = credits_remaining - $1, last_used_at = $2 WHERE key = $3 AND credits_remaining >= $1 RETURNING credits_remaining`,
		amount, time.Now().UTC(), key,
	).Scan(&balance)
	if err != nil {
		// Either key doesn't exist or insufficient credits
		var current int
		if qerr := s.db.QueryRow(`SELECT credits_remaining FROM api_keys WHERE key = $1`, key).Scan(&current); qerr != nil {
			return 0, fmt.Errorf("invalid api key")
		}
		return current, fmt.Errorf("insufficient credits: have %d, need %d", current, amount)
	}
	return balance, nil
}

// GetBalance returns the credit balance for a key.
func (s *CreditStore) GetBalance(key string) (int, error) {
	var balance int
	err := s.db.QueryRow(`SELECT credits_remaining FROM api_keys WHERE key = $1`, key).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("invalid api key")
	}
	return balance, nil
}

// ValidateKey checks if a key exists, has credits > 0, and is not suspended.
func (s *CreditStore) ValidateKey(key string) (bool, error) {
	var balance, suspended int
	err := s.db.QueryRow(`SELECT credits_remaining, COALESCE(suspended, 0) FROM api_keys WHERE key = $1`, key).Scan(&balance, &suspended)
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
	_, err := s.db.Exec(`UPDATE api_keys SET suspended = 1 WHERE key = $1`, key)
	return err
}

// UnsuspendKey removes the suspension from an API key.
func (s *CreditStore) UnsuspendKey(key string) error {
	_, err := s.db.Exec(`UPDATE api_keys SET suspended = 0 WHERE key = $1`, key)
	return err
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

// MPPSPTMinimumCents is the Stripe SPT minimum charge per the docs:
// "Stripe requires a minimum charge of 0.50 USD (or equivalent) for card
// payments via SPT." Floor MPP prices at this for any model whose
// credit-equivalent is below it — sub-50¢ charges would be rejected at
// Stripe settlement, burning the agent's credential. When the Tempo
// crypto path lands (no minimum, sub-cent micropayments supported), this
// floor can be lifted for tempo-method challenges.
const MPPSPTMinimumCents = 50

// MPPPriceForModel returns the per-call MPP price in cents for a given model.
// Kept in sync with CreditCost: same effective pricing across rails so an
// agent paying via MPP doesn't get a structural discount or markup vs a
// credit-funded user. At 1 credit = $0.005 (Starter pack 1000 credits for
// $5), the credit-equivalent prices are:
//   - Sonnet (60 credits)  = $0.30  → floored to 50¢ for Stripe SPT minimum
//   - Opus   (300 credits) = $1.50  = 150 cents
//   - Haiku  (20 credits)  = $0.10  → floored to 50¢ for Stripe SPT minimum
//
// Empty model defaults to Sonnet, matching CreditCost. The 50¢ floor
// means Haiku and Sonnet MPP users overpay vs credit-equivalent (a known
// asymmetry remaining until we wire the Tempo path). Opus matches exactly.
// If credit pricing changes (CostSonnet/Opus/Haiku constants), update both
// functions together.
func MPPPriceForModel(model string) int64 {
	credits := int64(CreditCost(model))
	// 1 credit = 0.5 cents (Starter pack: 1000 credits for $5 = 500 cents).
	// Multiply by half-cents then divide by 10 to keep integer arithmetic
	// without losing precision on credit counts that divide evenly.
	price := credits * 5 / 10
	if price < MPPSPTMinimumCents {
		return MPPSPTMinimumCents
	}
	return price
}

// CreditX402Settlement credits `amount` to `key` exactly once per settlement
// `nonce` (the EIP-3009 authorization nonce). The nonce row and the balance
// update commit in ONE transaction, so a concurrent double-present of the same
// credential cannot double-credit: the x402_settlements PRIMARY KEY serializes
// the racers and only the first INSERT wins. Returns (balance, applied);
// applied=false means this nonce was already processed and NO new credit was
// added — the returned balance is the current (unchanged) balance.
func (s *CreditStore) CreditX402Settlement(nonce, key string, amount int) (int, bool, error) {
	if nonce == "" {
		return 0, false, fmt.Errorf("x402 settlement nonce required for idempotency")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(
		`INSERT INTO x402_settlements (nonce, api_key, credits) VALUES ($1, $2, $3) ON CONFLICT (nonce) DO NOTHING`,
		nonce, key, amount,
	)
	if err != nil {
		return 0, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already processed — do NOT re-credit. Return the current balance.
		var bal int
		if err := tx.QueryRow(`SELECT credits_remaining FROM api_keys WHERE key = $1`, key).Scan(&bal); err != nil {
			return 0, false, fmt.Errorf("api key not found")
		}
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return bal, false, nil
	}

	var bal int
	if err := tx.QueryRow(
		`UPDATE api_keys SET credits_remaining = credits_remaining + $1 WHERE key = $2 RETURNING credits_remaining`,
		amount, key,
	).Scan(&bal); err != nil {
		return 0, false, fmt.Errorf("api key not found")
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return bal, true, nil
}

// KeyActive reports whether a key exists and is not suspended, IGNORING its
// balance. This is the authentication predicate — "is this a real, usable
// identity" — as distinct from ValidateKey's "does it have credits to spend".
// Auth gates use this so a key at zero balance can still authenticate to call
// free tools and, crucially, buy_credits to top itself back up; paid actions
// remain gated on balance at Deduct (or on a per-call x402/MPP payment). Without
// this split, a user who spent their credits to zero could no longer authenticate
// to buy more — a bootstrap deadlock.
func (s *CreditStore) KeyActive(key string) (bool, error) {
	var suspended int
	err := s.db.QueryRow(`SELECT COALESCE(suspended, 0) FROM api_keys WHERE key = $1`, key).Scan(&suspended)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return suspended == 0, nil
}

// RandomKey generates a high-entropy, prefixed opaque token: 32 random bytes
// via crypto/rand, hex-encoded. Exported so every part of gemot that mints a
// bearer-shaped identifier (API keys here, OAuth authorization codes in
// internal/mcp) agrees on entropy size and encoding in exactly one place.
func RandomKey(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}
