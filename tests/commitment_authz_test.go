package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/mcp"
	"github.com/justinstimatze/gemot/internal/store"
)

// Commitment resolution (fulfill/break) previously accepted any caller
// holding a commitment ID: no participation check, no ownership check, at
// any layer. Because CheckAccess lets anyone read an "open" deliberation —
// the default visibility — commitment IDs were harvestable via
// get_commitments and then resolvable by a stranger, across deliberation
// boundaries. Marking a commitment broken degrades the target's
// AgentReputation trust score, so this was a remote reputation-vandalism
// vector on any agent whose deliberation ID was known.
//
// These tests run on the memory store so they execute everywhere rather
// than skipping when Postgres is unreachable.

// commitFixture builds a deliberation with two participants and returns a
// commitment made by keyA:alice. Agent IDs are keyID-scoped the way
// scopeAgentID writes them for authenticated callers.
func commitFixture(t *testing.T) (*deliberation.Service, *deliberation.Commitment, string) {
	t.Helper()
	ctx := context.Background()
	svc := deliberation.NewService(store.NewMemoryStore(), &mockAnalyzer{})

	d, err := svc.CreateDeliberation(ctx, "Commitment authorization", "")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}
	if _, err := svc.SubmitPosition(ctx, d.ID, "keyA:alice", "A"); err != nil {
		t.Fatalf("SubmitPosition alice: %v", err)
	}
	if _, err := svc.SubmitPosition(ctx, d.ID, "keyB:bob", "B"); err != nil {
		t.Fatalf("SubmitPosition bob: %v", err)
	}
	if _, err := svc.Analyze(ctx, d.ID); err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	c, err := svc.Commit(ctx, d.ID, "keyA:alice", "I will ship the interop patch", "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return svc, c, d.ID
}

// The regression test for the actual bug.
func TestBreakCommitmentRejectsNonParticipant(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	err := mcp.CoreBreakCommitment(ctx, svc, c.ID, "did not ship", "keyM:mallory", "keyM")
	if err == nil {
		t.Fatal("a non-participant broke someone else's commitment")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected an access-denied error, got: %v", err)
	}

	got, err := svc.GetCommitmentByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetCommitmentByID: %v", err)
	}
	if got.Status == "broken" {
		t.Fatal("commitment was mutated despite the rejection")
	}
}

// Holding a commitment ID from another deliberation must not confer
// standing — participation is checked against the commitment's own
// deliberation, not merely against being a participant somewhere.
func TestBreakCommitmentRejectsCrossDeliberation(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	other, err := svc.CreateDeliberation(ctx, "Unrelated deliberation", "")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}
	if _, err := svc.SubmitPosition(ctx, other.ID, "keyM:mallory", "elsewhere"); err != nil {
		t.Fatalf("SubmitPosition mallory: %v", err)
	}

	err = mcp.CoreBreakCommitment(ctx, svc, c.ID, "did not ship", "keyM:mallory", "keyM")
	if err == nil {
		t.Fatal("participation in an unrelated deliberation granted standing")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected an access-denied error, got: %v", err)
	}
}

func TestBreakCommitmentAllowsParticipant(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	if err := mcp.CoreBreakCommitment(ctx, svc, c.ID, "deadline passed", "keyB:bob", "keyB"); err != nil {
		t.Fatalf("participant could not break a commitment: %v", err)
	}
	got, err := svc.GetCommitmentByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetCommitmentByID: %v", err)
	}
	if got.Status != "broken" {
		t.Fatalf("expected broken, got %q", got.Status)
	}
}

// Admitting your own broken promise is honest reporting and costs the
// admitter, so it stays allowed.
func TestBreakCommitmentAllowsOwnAgent(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	if err := mcp.CoreBreakCommitment(ctx, svc, c.ID, "I could not deliver", "keyA:alice", "keyA"); err != nil {
		t.Fatalf("agent could not admit breaking its own commitment: %v", err)
	}
	got, err := svc.GetCommitmentByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetCommitmentByID: %v", err)
	}
	if got.Status != "broken" {
		t.Fatalf("expected broken, got %q", got.Status)
	}
}

// Fulfilment raises the committer's trust score, so it may not be
// self-asserted.
func TestFulfillCommitmentRejectsSelfAttestation(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	err := mcp.CoreFulfillCommitment(ctx, svc, c.ID, "keyA:alice", "keyA")
	if err == nil {
		t.Fatal("an agent marked its own commitment fulfilled")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected an access-denied error, got: %v", err)
	}

	got, err := svc.GetCommitmentByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetCommitmentByID: %v", err)
	}
	if got.Status == "fulfilled" {
		t.Fatal("commitment was mutated despite the rejection")
	}
}

func TestFulfillCommitmentAllowsOtherParticipant(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	if err := mcp.CoreFulfillCommitment(ctx, svc, c.ID, "keyB:bob", "keyB"); err != nil {
		t.Fatalf("participant could not verify a commitment: %v", err)
	}
	got, err := svc.GetCommitmentByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetCommitmentByID: %v", err)
	}
	if got.Status != "fulfilled" {
		t.Fatalf("expected fulfilled, got %q", got.Status)
	}
	if got.VerifiedBy != "keyB:bob" {
		t.Fatalf("expected verified_by keyB:bob, got %q", got.VerifiedBy)
	}
}

// An empty keyID is admin or dev mode, which CheckAccess already lets
// through everywhere else. Anonymous behaviour is deliberately unchanged
// by this fix.
func TestCommitmentResolutionUnauthenticatedUnchanged(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	if err := mcp.CoreBreakCommitment(ctx, svc, c.ID, "no key supplied", "system", ""); err != nil {
		t.Fatalf("unauthenticated resolution should behave as before: %v", err)
	}
}

func TestResolveMissingCommitmentIsNotFound(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := commitFixture(t)

	if err := mcp.CoreFulfillCommitment(ctx, svc, "does-not-exist", "keyB:bob", "keyB"); err == nil {
		t.Fatal("expected an error resolving an unknown commitment")
	}
}

// The checks live in the service, not the transport wrapper, so a caller
// reaching the service directly cannot skip them. Without this the
// guarantee would only hold for code that remembers to go through Core*.
func TestServiceEnforcesStandingWithoutWrapper(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	if err := svc.BreakCommitment(ctx, c.ID, "did not ship", "keyM:mallory", "keyM"); err == nil {
		t.Fatal("service accepted a break from a non-participant")
	}
	if err := svc.FulfillCommitment(ctx, c.ID, "keyA:alice", "keyA"); err == nil {
		t.Fatal("service accepted a self-attested fulfilment")
	}
	got, err := svc.GetCommitmentByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetCommitmentByID: %v", err)
	}
	if got.Status != "active" {
		t.Fatalf("commitment mutated despite both rejections, got %q", got.Status)
	}
}

// Withdrawal hides an agent's positions but leaves their commitment rows
// in place, so a fallback that accepted any commitment let a withdrawn
// agent keep resolving everyone else's commitments in a deliberation they
// had left. Standing now requires an outstanding one.
func TestWithdrawnAgentLosesStanding(t *testing.T) {
	ctx := context.Background()
	svc, _, delibID := commitFixture(t)

	// Bob commits too, so he holds standing by commitment as well as by
	// position — then withdraws, which resolves both.
	if _, err := svc.Commit(ctx, delibID, "keyB:bob", "I will review the patch", ""); err != nil {
		t.Fatalf("Commit bob: %v", err)
	}
	if err := svc.CheckParticipant(ctx, delibID, "keyB"); err != nil {
		t.Fatalf("bob should have standing before withdrawing: %v", err)
	}

	if err := svc.WithdrawAgent(ctx, delibID, "keyB:bob"); err != nil {
		t.Fatalf("WithdrawAgent: %v", err)
	}
	if err := svc.CheckParticipant(ctx, delibID, "keyB"); err == nil {
		t.Fatal("a withdrawn agent kept standing to resolve commitments")
	}
}

// The fallback still has to admit an agent who committed without ever
// submitting a position — that is the case it exists for.
func TestCommitterWithoutPositionHasStanding(t *testing.T) {
	ctx := context.Background()
	svc, _, delibID := commitFixture(t)

	if _, err := svc.Commit(ctx, delibID, "keyC:carol", "I will run the benchmark", ""); err != nil {
		t.Fatalf("Commit carol: %v", err)
	}
	if err := svc.CheckParticipant(ctx, delibID, "keyC"); err != nil {
		t.Fatalf("an agent with an outstanding commitment should have standing: %v", err)
	}
}

// Withdrawal invalidates the withdrawn agent's outstanding commitments by
// marking them broken, which counts against reputation the same way a
// caller-initiated break does — so it has to reach the ordered log too.
// This path wrote straight to the store and bypassed orderAction.
func TestWithdrawalBreaksAreBFTOrdered(t *testing.T) {
	ctx := context.Background()
	svc, c, delibID := commitFixture(t)

	before, err := svc.GetTamperEvidentLog(ctx, delibID)
	if err != nil {
		t.Fatalf("GetTamperEvidentLog: %v", err)
	}
	if err := svc.WithdrawAgent(ctx, delibID, "keyA:alice"); err != nil {
		t.Fatalf("WithdrawAgent: %v", err)
	}
	// Chained HotStuff commits an entry only once later writes extend the
	// chain, so flush before reading the log back.
	advanceChain(t, svc, delibID)

	got, err := svc.GetCommitmentByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetCommitmentByID: %v", err)
	}
	if got.Status != "broken" {
		t.Fatalf("withdrawal should invalidate outstanding commitments, got %q", got.Status)
	}

	after, err := svc.GetTamperEvidentLog(ctx, delibID)
	if err != nil {
		t.Fatalf("GetTamperEvidentLog: %v", err)
	}
	if !hasAction(after[len(before):], "break") {
		t.Fatal("withdrawal-triggered break never reached the tamper-evident log")
	}
}

// advanceChain issues throwaway ordered writes so entries already proposed
// reach the committed audit log under the two-chain rule.
func advanceChain(t *testing.T, svc *deliberation.Service, delibID string) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := svc.Commit(context.Background(), delibID, "keyB:bob", "chain advance", ""); err != nil {
			t.Fatalf("advanceChain: %v", err)
		}
	}
}

func hasAction(entries []deliberation.AuditLogEntry, action string) bool {
	for _, e := range entries {
		if e.ActionType == action {
			return true
		}
	}
	return false
}

// Commit was already BFT-ordered while fulfill and break wrote only to the
// lightweight audit callback — the verdict on a commitment escaped the
// tamper-evident log that the pledge was recorded in. Both now route
// through orderAction, so resolving a commitment advances the chain.
func TestCommitmentResolutionIsBFTOrdered(t *testing.T) {
	ctx := context.Background()
	svc, c, delibID := commitFixture(t)

	before, err := svc.GetTamperEvidentLog(ctx, delibID)
	if err != nil {
		t.Fatalf("GetTamperEvidentLog: %v", err)
	}
	if err := mcp.CoreBreakCommitment(ctx, svc, c.ID, "deadline passed", "keyB:bob", "keyB"); err != nil {
		t.Fatalf("CoreBreakCommitment: %v", err)
	}
	advanceChain(t, svc, delibID)
	after, err := svc.GetTamperEvidentLog(ctx, delibID)
	if err != nil {
		t.Fatalf("GetTamperEvidentLog: %v", err)
	}
	if !hasAction(after[len(before):], "break") {
		t.Fatal("breaking a commitment did not reach the tamper-evident log")
	}
}
