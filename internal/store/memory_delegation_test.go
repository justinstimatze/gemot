package store

import (
	"context"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

// TestMemoryStoreCreateDelegationUpsertsByDeliberationAndFromAgent is the
// regression test for the tenth deferred finding: MemoryStore.CreateDelegation
// always inserted a fresh row instead of upserting by (deliberation_id,
// from_agent) like the Postgres adapter does. Re-delegating without an
// explicit revoke left both the old and new delegation Active, so
// GetDelegations returned a stale delegation indefinitely.
func TestMemoryStoreCreateDelegationUpsertsByDeliberationAndFromAgent(t *testing.T) {
	m := NewMemoryStore()
	ctx := context.Background()

	if err := m.CreateDelegation(ctx, &deliberation.Delegation{
		DeliberationID: "d1", FromAgent: "alice", ToAgent: "bob",
	}); err != nil {
		t.Fatalf("first CreateDelegation: %v", err)
	}
	if err := m.CreateDelegation(ctx, &deliberation.Delegation{
		DeliberationID: "d1", FromAgent: "alice", ToAgent: "carol",
	}); err != nil {
		t.Fatalf("second CreateDelegation: %v", err)
	}

	delegations, err := m.GetDelegations(ctx, "d1")
	if err != nil {
		t.Fatalf("GetDelegations: %v", err)
	}
	if len(delegations) != 1 {
		t.Fatalf("got %d active delegations from alice, want 1 (re-delegation should replace, not add): %+v", len(delegations), delegations)
	}
	if delegations[0].ToAgent != "carol" {
		t.Errorf("ToAgent = %q, want carol (the most recent delegation)", delegations[0].ToAgent)
	}

	// A delegation from a DIFFERENT agent in the same deliberation must be
	// unaffected.
	if err := m.CreateDelegation(ctx, &deliberation.Delegation{
		DeliberationID: "d1", FromAgent: "dave", ToAgent: "bob",
	}); err != nil {
		t.Fatalf("third CreateDelegation: %v", err)
	}
	delegations, err = m.GetDelegations(ctx, "d1")
	if err != nil {
		t.Fatalf("GetDelegations (after dave): %v", err)
	}
	if len(delegations) != 2 {
		t.Fatalf("got %d active delegations, want 2 (alice->carol, dave->bob): %+v", len(delegations), delegations)
	}
}
