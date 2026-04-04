package tests

import (
	"testing"

	"github.com/justinstimatze/gemot/internal/payments"
)

func TestCreditStoreBasics(t *testing.T) {
	db := tempDB(t)
	store, err := payments.NewCreditStore(db.RawDB())
	if err != nil {
		t.Fatalf("creating credit store: %v", err)
	}

	// Generate a key
	key, err := store.GenerateKey("test@example.com", "cus_test", "cs_test", 1000)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	if key == "" || len(key) < 10 {
		t.Fatal("expected non-empty key")
	}
	if key[:4] != "gmt_" {
		t.Fatalf("expected key prefix 'gmt_', got %q", key[:4])
	}

	// Check balance
	balance, err := store.GetBalance(key)
	if err != nil {
		t.Fatalf("getting balance: %v", err)
	}
	if balance != 1000 {
		t.Fatalf("expected 1000 credits, got %d", balance)
	}

	// Validate key
	valid, err := store.ValidateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("expected valid key")
	}

	// Deduct credits
	remaining, err := store.Deduct(key, 50)
	if err != nil {
		t.Fatalf("deducting: %v", err)
	}
	if remaining != 950 {
		t.Fatalf("expected 950 remaining, got %d", remaining)
	}

	// Deduct too many
	_, err = store.Deduct(key, 9999)
	if err == nil {
		t.Fatal("expected error for insufficient credits")
	}

	// Add credits
	newBalance, err := store.AddCredits(key, 500)
	if err != nil {
		t.Fatalf("adding credits: %v", err)
	}
	if newBalance != 1450 {
		t.Fatalf("expected 1450, got %d", newBalance)
	}

	// Add credits by email
	_, returnedBalance, err := store.AddCreditsByEmail("test@example.com", 100)
	if err != nil {
		t.Fatalf("adding by email: %v", err)
	}
	if returnedBalance != 1550 {
		t.Fatalf("expected 1550, got %d", returnedBalance)
	}

	// Invalid key
	valid, err = store.ValidateKey("gmt_nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if valid {
		t.Fatal("expected invalid for nonexistent key")
	}

	// Zero balance = invalid
	remaining, _ = store.Deduct(key, 1550)
	if remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", remaining)
	}
	valid, _ = store.ValidateKey(key)
	if valid {
		t.Fatal("expected invalid for zero-balance key")
	}
}

func TestCreditCost(t *testing.T) {
	if payments.CreditCost("claude-sonnet-4-6") != 60 {
		t.Fatal("expected 60 for sonnet")
	}
	if payments.CreditCost("claude-opus-4-6") != 300 {
		t.Fatal("expected 300 for opus")
	}
	if payments.CreditCost("claude-haiku-4-5") != 20 {
		t.Fatal("expected 20 for haiku")
	}
	if payments.CreditCost("") != 60 {
		t.Fatal("expected 60 for default")
	}
}

func TestCreditStoreIdempotency(t *testing.T) {
	db := tempDB(t)
	store, err := payments.NewCreditStore(db.RawDB())
	if err != nil {
		t.Fatal(err)
	}

	// Simulate: same session ID should not double-credit
	key1, err := store.GenerateKey("user@test.com", "cus_1", "cs_session_123", 1000)
	if err != nil {
		t.Fatal(err)
	}

	// Try to create another key with same session ID — this would be a webhook replay
	// The schema doesn't enforce unique session IDs, but the webhook handler should check
	key2, err := store.GenerateKey("user@test.com", "cus_1", "cs_session_123", 1000)
	if err != nil {
		t.Fatal(err)
	}

	// Both keys exist — the idempotency check is in the webhook handler, not the store
	// But AddCreditsByEmail should add to the MOST RECENT key
	if key1 == key2 {
		t.Fatal("expected different keys")
	}

	// The most recent key should have 1000 credits
	balance, _ := store.GetBalance(key2)
	if balance != 1000 {
		t.Fatalf("expected 1000 for newest key, got %d", balance)
	}
}
