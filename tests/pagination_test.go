package tests

import (
	"context"
	"fmt"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

// TestPaginationLimit creates 5 deliberations and verifies limit=2 returns exactly 2.
func TestPaginationLimit(t *testing.T) {
	svc, _ := newTestService(t)

	for i := 0; i < 5; i++ {
		if _, err := svc.CreateDeliberation(context.Background(), fmt.Sprintf("Topic %d", i), "desc"); err != nil {
			t.Fatal(err)
		}
	}

	results, err := svc.ListDeliberations(context.Background(), 2, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("ListDeliberations(limit=2): got %d results, want 2", len(results))
	}
}

// TestPaginationOffset creates 5 deliberations and verifies offset=3 returns the last 2.
func TestPaginationOffset(t *testing.T) {
	svc, _ := newTestService(t)

	for i := 0; i < 5; i++ {
		if _, err := svc.CreateDeliberation(context.Background(), fmt.Sprintf("Topic %d", i), "desc"); err != nil {
			t.Fatal(err)
		}
	}

	results, err := svc.ListDeliberations(context.Background(), 100, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("ListDeliberations(offset=3): got %d results, want 2", len(results))
	}
}

// TestPaginationDefaultLimit verifies that limit=0 defaults to 100 (returns all 5).
func TestPaginationDefaultLimit(t *testing.T) {
	svc, _ := newTestService(t)

	for i := 0; i < 5; i++ {
		if _, err := svc.CreateDeliberation(context.Background(), fmt.Sprintf("Topic %d", i), "desc"); err != nil {
			t.Fatal(err)
		}
	}

	results, err := svc.ListDeliberations(context.Background(), 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Errorf("ListDeliberations(limit=0): got %d results, want 5 (default limit should be 100)", len(results))
	}
}

// TestPaginationListByGroup verifies pagination works for group-scoped listing.
func TestPaginationListByGroup(t *testing.T) {
	svc, _ := newTestService(t)

	groupID := "test-group-pagination"
	for i := 0; i < 5; i++ {
		d, err := svc.CreateDeliberation(context.Background(), fmt.Sprintf("Group topic %d", i), "desc", deliberation.WithGroupID(groupID))
		if err != nil {
			t.Fatal(err)
		}
		_ = d
	}

	// Also create one outside the group
	if _, err := svc.CreateDeliberation(context.Background(), "Other topic", "desc"); err != nil {
		t.Fatal(err)
	}

	// All in group
	all, err := svc.ListByGroup(context.Background(), groupID, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Errorf("ListByGroup(all): got %d, want 5", len(all))
	}

	// Paginated
	page, err := svc.ListByGroup(context.Background(), groupID, 2, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 {
		t.Errorf("ListByGroup(limit=2): got %d, want 2", len(page))
	}

	// Offset past most
	tail, err := svc.ListByGroup(context.Background(), groupID, 100, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 1 {
		t.Errorf("ListByGroup(offset=4): got %d, want 1", len(tail))
	}
}
