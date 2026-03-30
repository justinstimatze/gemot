package tests

import (
	"testing"
	"time"
)

// TestShareTokenCreateAndLookup creates a share token and verifies lookup returns the correct group_id.
func TestShareTokenCreateAndLookup(t *testing.T) {
	db := tempDB(t)

	groupID := "test-group-123"
	token := "tok_abc123"
	expiresAt := time.Now().Add(24 * time.Hour)

	if err := db.CreateShareToken(token, groupID, expiresAt); err != nil {
		t.Fatalf("CreateShareToken: %v", err)
	}

	got, err := db.LookupShareToken(token)
	if err != nil {
		t.Fatalf("LookupShareToken: %v", err)
	}
	if got != groupID {
		t.Errorf("LookupShareToken returned group_id %q, want %q", got, groupID)
	}
}

// TestShareTokenNotFound verifies that looking up a nonexistent token returns an error.
func TestShareTokenNotFound(t *testing.T) {
	db := tempDB(t)

	_, err := db.LookupShareToken("nonexistent-token")
	if err == nil {
		t.Fatal("expected error for nonexistent token, got nil")
	}
}

// TestShareTokenExpired verifies that an expired token is rejected on lookup.
func TestShareTokenExpired(t *testing.T) {
	db := tempDB(t)

	groupID := "expired-group"
	token := "tok_expired"
	expiresAt := time.Now().Add(-1 * time.Hour) // already expired

	if err := db.CreateShareToken(token, groupID, expiresAt); err != nil {
		t.Fatalf("CreateShareToken: %v", err)
	}

	_, err := db.LookupShareToken(token)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}
