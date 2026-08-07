package tests

import (
	"context"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/justinstimatze/gemot/internal/auth"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/mcp"
	"github.com/justinstimatze/gemot/internal/store"
)

// A vote with no verified signature is marked relayed (unattributable); a
// self-signed one is marked direct. An unfalsifiable side-channel vote must be
// visibly distinct in the record from a vote the voter actually signed.
func TestVoteRelayedMarking(t *testing.T) {
	ctx := context.Background()
	svc := deliberation.NewService(store.NewMemoryStore(), &mockAnalyzer{})

	d, err := svc.CreateDeliberation(ctx, "relayed marking", "")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}
	pos, err := svc.SubmitPosition(ctx, d.ID, "alice", "a claim to vote on")
	if err != nil {
		t.Fatalf("SubmitPosition: %v", err)
	}

	// bob votes unsigned — cannot be attributed to bob → relayed.
	if err := svc.Vote(ctx, d.ID, "bob", pos.ID, 1, "", ""); err != nil {
		t.Fatalf("unsigned vote: %v", err)
	}
	// carol registers a key and signs her vote → direct.
	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "carol", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("RegisterAgentKey: %v", err)
	}
	sig := signVote(t, priv, "carol", d.ID, pos.ID, 2, "", "", "")
	if err := svc.SubmitSignedVote(ctx, d.ID, "carol", pos.ID, 2, "", "", "", sig); err != nil {
		t.Fatalf("signed vote: %v", err)
	}

	votes, err := svc.GetVotes(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetVotes: %v", err)
	}
	byAgent := map[string]deliberation.Vote{}
	for _, v := range votes {
		byAgent[v.AgentID] = v
	}
	if !byAgent["bob"].Relayed {
		t.Fatal("unsigned vote should be marked relayed")
	}
	if byAgent["carol"].Relayed {
		t.Fatal("self-signed vote must not be marked relayed")
	}
}

// get_vote_state lets an agent confirm its own vote landed and see whether the
// record marks it relayed — the "did my vote land, and how?" surface the BFT
// fork-wedge left unanswerable. It returns only the queried agent's votes.
func TestGetVoteStateSelfScopedWithRelayedFlag(t *testing.T) {
	ctx := context.Background()
	svc := deliberation.NewService(store.NewMemoryStore(), &mockAnalyzer{})

	d, err := svc.CreateDeliberation(ctx, "vote state", "")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}
	pos, err := svc.SubmitPosition(ctx, d.ID, "alice", "a claim")
	if err != nil {
		t.Fatalf("SubmitPosition: %v", err)
	}
	if err := svc.Vote(ctx, d.ID, "bob", pos.ID, 1, "", ""); err != nil {
		t.Fatalf("bob vote: %v", err)
	}
	if err := svc.Vote(ctx, d.ID, "carol", pos.ID, -1, "", ""); err != nil {
		t.Fatalf("carol vote: %v", err)
	}

	// keyID "" is admin/dev, so CheckAccess passes; agent scoping is a no-op
	// here since votes were stored under bare ids.
	bob, err := mcp.CoreGetVoteState(ctx, svc, d.ID, "bob", "")
	if err != nil {
		t.Fatalf("CoreGetVoteState: %v", err)
	}
	if len(bob) != 1 {
		t.Fatalf("expected only bob's 1 vote, got %d", len(bob))
	}
	if bob[0].AgentID != "bob" {
		t.Fatalf("vote state leaked another agent's vote: %+v", bob[0])
	}
	if !bob[0].Relayed {
		t.Fatal("bob's unsigned vote should show relayed=true in vote state")
	}
}
