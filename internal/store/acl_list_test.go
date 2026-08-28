package store

import (
	"context"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

// TestListDeliberationsIncludesACLGrantedPrivateDeliberations is the
// regression test for a visibility mismatch: ListDeliberations,
// ListByGroup, and ListByAgent all filtered private deliberations by
// `visibility != 'private' OR creator_key = $N` only, never consulting the
// deliberation_acl table CheckAccess independently checks. An agent
// granted ACL access (not as creator) could successfully access the
// deliberation directly, but it never appeared in any list result.
func TestListDeliberationsIncludesACLGrantedPrivateDeliberations(t *testing.T) {
	db := purgeTestDB(t)
	ctx := context.Background()

	d := &deliberation.Deliberation{
		Topic: "acl list test", Status: "open", Round: 1,
		Visibility: "private", CreatorKey: "owner-key",
	}
	if err := db.CreateDeliberation(ctx, d); err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}

	const granteeKey = "grantee-key"

	// Before granting ACL access, the grantee must not see this private
	// deliberation in any list.
	before, err := db.ListDeliberations(ctx, 100, 0, granteeKey)
	if err != nil {
		t.Fatalf("ListDeliberations (before grant): %v", err)
	}
	for _, item := range before {
		if item.ID == d.ID {
			t.Fatal("private deliberation visible before any ACL grant")
		}
	}

	if err := db.AddToACL(ctx, d.ID, granteeKey); err != nil {
		t.Fatalf("AddToACL: %v", err)
	}

	after, err := db.ListDeliberations(ctx, 100, 0, granteeKey)
	if err != nil {
		t.Fatalf("ListDeliberations (after grant): %v", err)
	}
	found := false
	for _, item := range after {
		if item.ID == d.ID {
			found = true
		}
	}
	if !found {
		t.Error("ACL-granted private deliberation missing from ListDeliberations — access and listing disagree")
	}

	// The creator must still see it too (unaffected by this fix).
	ownerList, err := db.ListDeliberations(ctx, 100, 0, "owner-key")
	if err != nil {
		t.Fatalf("ListDeliberations (owner): %v", err)
	}
	ownerFound := false
	for _, item := range ownerList {
		if item.ID == d.ID {
			ownerFound = true
		}
	}
	if !ownerFound {
		t.Error("creator no longer sees their own private deliberation")
	}

	// An unrelated third party must still be denied.
	strangerList, err := db.ListDeliberations(ctx, 100, 0, "stranger-key")
	if err != nil {
		t.Fatalf("ListDeliberations (stranger): %v", err)
	}
	for _, item := range strangerList {
		if item.ID == d.ID {
			t.Error("an uninvolved key can see a private deliberation it has no access to")
		}
	}
}
