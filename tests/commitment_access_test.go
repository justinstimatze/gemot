package tests

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/mcp"
)

// The keystone's core invariant: a commitment's own author cannot record
// downstream access to it. If it could, a seller could manufacture its own
// immunity clock and never be "late". "Downstream" means someone else touched
// the artifact.
func TestRecordAccessRejectsSelfAccess(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	// By the author's own key.
	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyA:alice", "read", "", "", "keyA"); err == nil {
		t.Fatal("a committer recorded downstream access to its own commitment (by key)")
	}
	// And by matching the author's agent id even if the key check is skipped
	// (keyID empty = admin/dev): the agent-id predicate must still reject it.
	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, c.AgentID, "read", "", "", ""); err == nil {
		t.Fatal("a committer recorded downstream access to its own commitment (by agent id)")
	}

	sig, err := mcp.CoreCommitmentSignals(ctx, svc, c.ID, "keyB")
	if err != nil {
		t.Fatalf("CoreCommitmentSignals: %v", err)
	}
	if sig.AccessCount != 0 {
		t.Fatalf("self-access should not have been recorded, got %d accesses", sig.AccessCount)
	}
}

// A stranger cannot write to the ledger: the signals only mean something if
// the accesses in them are by real mesh participants, not anyone who harvested
// a commitment ID.
func TestRecordAccessRejectsNonParticipant(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	_, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyM:mallory", "read", "", "", "keyM")
	if err == nil {
		t.Fatal("a non-participant recorded access")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected an access-denied error, got: %v", err)
	}
}

// A downstream participant may record access, and gemot stamps the id and
// time server-side — the clock is the store's, never the caller's. That first
// access is the immunity cutoff, not a deadline.
func TestRecordAccessAllowsDownstreamAndStampsServerClock(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	before := time.Now().UTC()
	a, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyB:bob", "read", "read the artifact", "", "keyB")
	if err != nil {
		t.Fatalf("downstream participant could not record access: %v", err)
	}
	if a.ID == "" {
		t.Fatal("access record has no server-assigned id")
	}
	if a.AccessorID != "keyB:bob" || a.Kind != "read" {
		t.Fatalf("unexpected access record: %+v", a)
	}
	if a.CreatedAt.Before(before) {
		t.Fatalf("access timestamp %v predates the call at %v — not server-stamped", a.CreatedAt, before)
	}

	sig, err := mcp.CoreCommitmentSignals(ctx, svc, c.ID, "keyB")
	if err != nil {
		t.Fatalf("CoreCommitmentSignals: %v", err)
	}
	if sig.AccessCount != 1 || sig.DistinctAccessors != 1 {
		t.Fatalf("expected 1 access by 1 accessor, got count=%d distinct=%d", sig.AccessCount, sig.DistinctAccessors)
	}
	// First downstream access is the immunity cutoff, stamped by the server on
	// the access we just recorded.
	if sig.FirstDownstreamAccessAt == nil {
		t.Fatal("immunity cutoff (first downstream access) not populated")
	}
	if !sig.FirstDownstreamAccessAt.Equal(a.CreatedAt) {
		t.Fatalf("immunity cutoff %v != first access stamp %v", *sig.FirstDownstreamAccessAt, a.CreatedAt)
	}
	// A single read is not reliance, so stakes stay normal.
	if sig.StakesLevel != "normal" {
		t.Fatalf("a single read should be normal stakes, got %q", sig.StakesLevel)
	}
}

// A dependent commitment flags high stakes, and a question sets the question
// clock independently of the access clock. The dependency must reference a real
// commitment the accessor authored.
func TestCommitmentSignalsDependencyFlagsHighAndQuestionClock(t *testing.T) {
	ctx := context.Background()
	svc, c, delibID := commitFixture(t)

	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyB:bob", "question", "does this hold under X?", "", "keyB"); err != nil {
		t.Fatalf("record question: %v", err)
	}
	// bob makes a real commitment that relies on alice's, then declares it.
	dep, err := svc.Commit(ctx, delibID, "keyB:bob", "I will build on alice's artifact", "")
	if err != nil {
		t.Fatalf("Commit bob dependent: %v", err)
	}
	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyB:bob", "dependency", "built my result on it", dep.ID, "keyB"); err != nil {
		t.Fatalf("record dependency: %v", err)
	}

	sig, err := mcp.CoreCommitmentSignals(ctx, svc, c.ID, "keyB")
	if err != nil {
		t.Fatalf("CoreCommitmentSignals: %v", err)
	}
	if sig.DependentCount != 1 {
		t.Fatalf("expected 1 dependent, got %d", sig.DependentCount)
	}
	if sig.StakesLevel != "high" {
		t.Fatalf("a dependent commitment should flag high stakes, got %q", sig.StakesLevel)
	}
	if sig.FirstQuestionAt == nil {
		t.Fatal("a question should populate the question clock")
	}
	if sig.DistinctAccessors != 1 || sig.AccessCount != 2 {
		t.Fatalf("expected 1 accessor / 2 accesses, got distinct=%d count=%d", sig.DistinctAccessors, sig.AccessCount)
	}
}

// The anti-grief property: reads never fire high stakes, no matter how many
// distinct accessors. Otherwise an adversary points N sock puppets at an
// artifact to force the seller into a costly mandatory tier. Only a dependent
// commitment — a costly, key-attributable act — raises stakes.
func TestCommitmentSignalsReadersNeverFireHighStakes(t *testing.T) {
	ctx := context.Background()
	svc, c, delibID := commitFixture(t)

	// carol gains standing by committing in the same deliberation, then reads.
	depCarol, err := svc.Commit(ctx, delibID, "keyC:carol", "I will run the benchmark", "")
	if err != nil {
		t.Fatalf("Commit carol: %v", err)
	}
	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyB:bob", "read", "", "", "keyB"); err != nil {
		t.Fatalf("record bob: %v", err)
	}
	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyC:carol", "read", "", "", "keyC"); err != nil {
		t.Fatalf("record carol: %v", err)
	}
	sig, err := mcp.CoreCommitmentSignals(ctx, svc, c.ID, "keyB")
	if err != nil {
		t.Fatalf("signals: %v", err)
	}
	if sig.DistinctAccessors != 2 {
		t.Fatalf("expected 2 distinct accessors, got %d", sig.DistinctAccessors)
	}
	if sig.StakesLevel != "normal" {
		t.Fatalf("distinct readers must not fire high stakes, got %q", sig.StakesLevel)
	}

	// A real dependent commitment flips it.
	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyC:carol", "dependency", "relied on it", depCarol.ID, "keyC"); err != nil {
		t.Fatalf("record dependency: %v", err)
	}
	sig, err = mcp.CoreCommitmentSignals(ctx, svc, c.ID, "keyB")
	if err != nil {
		t.Fatalf("signals after dependency: %v", err)
	}
	if sig.StakesLevel != "high" {
		t.Fatalf("a dependent commitment should fire high stakes, got %q", sig.StakesLevel)
	}
}

// A dependency must cite a real commitment authored by the accessor. A missing
// reference, a self-reference, or someone else's commitment are all rejected —
// this is what stops a bare, forgeable "dependency" row from raising stakes.
func TestRecordAccessDependencyRequiresRealOwnedCommitment(t *testing.T) {
	ctx := context.Background()
	svc, c, delibID := commitFixture(t)

	// No dependent id.
	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyB:bob", "dependency", "", "", "keyB"); err == nil {
		t.Fatal("dependency without dependent_commitment_id was accepted")
	}
	// Nonexistent dependent.
	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyB:bob", "dependency", "", "does-not-exist", "keyB"); err == nil {
		t.Fatal("dependency citing a nonexistent commitment was accepted")
	}
	// Self-reference (the depended-on commitment itself).
	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyB:bob", "dependency", "", c.ID, "keyB"); err == nil {
		t.Fatal("dependency citing the target commitment itself was accepted")
	}
	// Someone else's commitment (alice's own commitment c is authored by alice,
	// not bob) — bob cannot pass it off as his dependency.
	other, err := svc.Commit(ctx, delibID, "keyC:carol", "carol's commitment", "")
	if err != nil {
		t.Fatalf("Commit carol: %v", err)
	}
	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyB:bob", "dependency", "", other.ID, "keyB"); err == nil {
		t.Fatal("bob declared a dependency citing carol's commitment")
	}
	// No forged dependency landed, so stakes stay normal.
	sig, err := mcp.CoreCommitmentSignals(ctx, svc, c.ID, "keyB")
	if err != nil {
		t.Fatalf("signals: %v", err)
	}
	if sig.StakesLevel != "normal" || sig.DependentCount != 0 {
		t.Fatalf("forged dependencies must not raise stakes, got level=%q count=%d", sig.StakesLevel, sig.DependentCount)
	}
}

// A dependent whose commitment is later broken stops counting: the stakes
// marker is not a ratchet that reads high forever after reliance has ended.
func TestCommitmentSignalsBrokenDependentDropsOut(t *testing.T) {
	ctx := context.Background()
	svc, c, delibID := commitFixture(t)

	dep, err := svc.Commit(ctx, delibID, "keyB:bob", "I depend on alice's artifact", "")
	if err != nil {
		t.Fatalf("Commit bob dependent: %v", err)
	}
	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyB:bob", "dependency", "", dep.ID, "keyB"); err != nil {
		t.Fatalf("record dependency: %v", err)
	}
	sig, err := mcp.CoreCommitmentSignals(ctx, svc, c.ID, "keyB")
	if err != nil {
		t.Fatalf("signals: %v", err)
	}
	if sig.StakesLevel != "high" || sig.DependentCount != 1 {
		t.Fatalf("live dependent should count, got level=%q count=%d", sig.StakesLevel, sig.DependentCount)
	}

	// Bob breaks his dependent commitment (admits he no longer relies).
	if err := svc.BreakCommitment(ctx, dep.ID, "no longer building on it", "keyB:bob", "keyB"); err != nil {
		t.Fatalf("BreakCommitment: %v", err)
	}
	sig, err = mcp.CoreCommitmentSignals(ctx, svc, c.ID, "keyB")
	if err != nil {
		t.Fatalf("signals after break: %v", err)
	}
	if sig.StakesLevel != "normal" || sig.DependentCount != 0 {
		t.Fatalf("a broken dependent must drop out, got level=%q count=%d", sig.StakesLevel, sig.DependentCount)
	}
}

// The same dependent commitment cited twice counts once (distinct, not raw).
func TestCommitmentSignalsDependentCountedOnce(t *testing.T) {
	ctx := context.Background()
	svc, c, delibID := commitFixture(t)

	dep, err := svc.Commit(ctx, delibID, "keyB:bob", "I depend on it", "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyB:bob", "dependency", "", dep.ID, "keyB"); err != nil {
			t.Fatalf("record dependency %d: %v", i, err)
		}
	}
	sig, err := mcp.CoreCommitmentSignals(ctx, svc, c.ID, "keyB")
	if err != nil {
		t.Fatalf("signals: %v", err)
	}
	if sig.DependentCount != 1 {
		t.Fatalf("one dependent cited thrice should count once, got %d", sig.DependentCount)
	}
}

func TestRecordAccessRejectsInvalidKind(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyB:bob", "bogus", "", "", "keyB"); err == nil {
		t.Fatal("an invalid access kind was accepted")
	}
}

// The access ledger is only trustworthy if it lands in the same tamper-evident
// log as commit/fulfill/break — otherwise a signal a third party relies on
// could be quietly rewritten. Recording access must advance the chain with an
// "access" entry.
func TestRecordAccessIsBFTOrdered(t *testing.T) {
	ctx := context.Background()
	svc, c, delibID := commitFixture(t)

	before, err := svc.GetTamperEvidentLog(ctx, delibID)
	if err != nil {
		t.Fatalf("GetTamperEvidentLog: %v", err)
	}
	if _, err := mcp.CoreRecordAccess(ctx, svc, c.ID, "keyB:bob", "read", "", "", "keyB"); err != nil {
		t.Fatalf("CoreRecordAccess: %v", err)
	}
	advanceChain(t, svc, delibID)
	after, err := svc.GetTamperEvidentLog(ctx, delibID)
	if err != nil {
		t.Fatalf("GetTamperEvidentLog: %v", err)
	}
	if !hasAction(after[len(before):], "access") {
		t.Fatal("recording downstream access did not reach the tamper-evident log")
	}
}

// Signals inherit the deliberation's access control — they call the same
// CheckAccess as get_commitments. On an OPEN deliberation that means the clock
// and stakes are third-party-readable by design, which is the whole point of
// the keystone. A missing commitment is a clean not-found.
func TestCommitmentSignalsAccessAndNotFound(t *testing.T) {
	ctx := context.Background()
	svc, c, _ := commitFixture(t)

	if _, err := mcp.CoreCommitmentSignals(ctx, svc, c.ID, "keyM"); err != nil {
		t.Fatalf("open-deliberation signals should be externally readable, got: %v", err)
	}
	if _, err := mcp.CoreCommitmentSignals(ctx, svc, "does-not-exist", "keyB"); err == nil {
		t.Fatal("expected not-found for unknown commitment")
	}
}
