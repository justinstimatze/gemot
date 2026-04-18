package tests

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/justinstimatze/gemot/internal/bft"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/store"
)

// TestSubmitPositionRoutedThroughBFT confirms that when a BFT engine
// is attached to the service, SubmitPosition drives the HotStuff
// state machine (proposal + self-vote + QC) and the returned Position
// carries a valid QC proof. Session 5b acceptance.
func TestSubmitPositionRoutedThroughBFT(t *testing.T) {
	db := tempDB(t)
	svc := deliberation.NewService(db, &mockAnalyzer{})

	ctx := context.Background()
	log := store.NewPostgresLogStore(db)
	voteHist := store.NewPostgresVoteHistoryStore(db, bft.ReplicaID(0))
	engine, err := bft.BootstrapSingleNode(ctx, log, voteHist)
	if err != nil {
		t.Fatalf("BootstrapSingleNode: %v", err)
	}
	svc.SetBFTEngine(engine)

	d, err := svc.CreateDeliberation(ctx, "BFT Test", "Route position through BFT")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}

	p, err := svc.SubmitPosition(ctx, d.ID, "agent1", "we should prioritize interoperability")
	if err != nil {
		t.Fatalf("SubmitPosition: %v", err)
	}
	if len(p.BFTProof) == 0 {
		t.Fatalf("Position.BFTProof is empty — engine did not attach a QC")
	}

	// The proof is a JSON-encoded QC. Unmarshal and sanity-check.
	var qc bft.QC
	if err := json.Unmarshal(p.BFTProof, &qc); err != nil {
		t.Fatalf("decode BFTProof: %v", err)
	}
	if qc.View != 1 {
		t.Fatalf("first position QC.View = %d; want 1", qc.View)
	}
	if len(qc.Signers) != 1 {
		t.Fatalf("QC.Signers = %d; want 1 (single-node)", len(qc.Signers))
	}

	// Second position advances the view and commits the first block
	// via two-chain.
	p2, err := svc.SubmitPosition(ctx, d.ID, "agent2", "but not at the cost of safety invariants")
	if err != nil {
		t.Fatalf("SubmitPosition 2: %v", err)
	}
	var qc2 bft.QC
	if err := json.Unmarshal(p2.BFTProof, &qc2); err != nil {
		t.Fatalf("decode BFTProof 2: %v", err)
	}
	if qc2.View != 2 {
		t.Fatalf("second position QC.View = %d; want 2", qc2.View)
	}

	// After two submissions, the committed log holds block 1 (block 2
	// still waits for block 3's QC under the two-chain rule).
	height, err := log.HighestHeight(ctx)
	if err != nil {
		t.Fatalf("HighestHeight: %v", err)
	}
	if height != 1 {
		t.Fatalf("after 2 SubmitPositions, highest committed height = %d; want 1", height)
	}
}

// TestBFTEngineBootIdempotent simulates a process restart: after the
// first boot writes committed blocks to the log, a fresh engine
// booted against the same stores must succeed and Replay the
// committed chain state — specifically, the new replica's committed
// log must contain every prior-boot committed block.
//
// KNOWN LIMITATION: cross-boot Submit after restart currently fails
// because Bootstrap generates a fresh BLS keypair on each call, so
// QCs from the prior boot cannot be verified under the new boot's
// roster. Persisting the BLS key across boots is session-5c scope
// (client-side QC verification + multi-node deploy both require it).
// The committed log survives; only the ability to extend the chain
// post-restart is blocked pending key persistence.
func TestBFTEngineBootIdempotent(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	log := store.NewPostgresLogStore(db)
	voteHist := store.NewPostgresVoteHistoryStore(db, bft.ReplicaID(0))

	// First boot: drive 3 submits (commits blocks 1 and 2 under the
	// two-chain rule; block 3 remains prepared but uncommitted).
	eng1, err := bft.BootstrapSingleNode(ctx, log, voteHist)
	if err != nil {
		t.Fatalf("first Bootstrap: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, _, err := eng1.Submit(ctx, []byte{byte('a' + i)}); err != nil {
			t.Fatalf("eng1.Submit %d: %v", i, err)
		}
	}
	heightBefore, _ := log.HighestHeight(ctx)
	if heightBefore != 2 {
		t.Fatalf("after 3 submits, committed height = %d; want 2", heightBefore)
	}

	// Second boot: must succeed against the same stores (Replay +
	// RestoreVoteHistory + view advance past prior proposedInView).
	if _, err := bft.BootstrapSingleNode(ctx, log, voteHist); err != nil {
		t.Fatalf("second Bootstrap: %v (log + vote history must replay cleanly)", err)
	}

	// Post-restart committed log is unchanged.
	heightAfter, _ := log.HighestHeight(ctx)
	if heightAfter != heightBefore {
		t.Fatalf("restart changed committed log height: before=%d after=%d", heightBefore, heightAfter)
	}
}
