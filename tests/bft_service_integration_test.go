package tests

import (
	"bytes"
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
	keys := store.NewPostgresReplicaKeyStore(db)
	engine, err := bft.BootstrapSingleNode(ctx, log, voteHist, keys)
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

// TestBFTEngineResumesAcrossBoot is the session-5c acceptance test:
// with the replica keypair persisted in bft_replica_keys, a fresh
// engine booted against the same stores can verify prior-boot QCs
// and extend the chain — the limitation documented in session 5b
// is now closed.
func TestBFTEngineResumesAcrossBoot(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	log := store.NewPostgresLogStore(db)
	voteHist := store.NewPostgresVoteHistoryStore(db, bft.ReplicaID(0))
	keys := store.NewPostgresReplicaKeyStore(db)

	// First boot: drive 3 submits (commits blocks 1 and 2 under the
	// two-chain rule; block 3 remains prepared but uncommitted).
	eng1, err := bft.BootstrapSingleNode(ctx, log, voteHist, keys)
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

	// Second boot: fresh engine against the same stores. Extending
	// the chain requires verifying the prior boot's QC — which only
	// works if the BLS keypair persisted from boot 1.
	eng2, err := bft.BootstrapSingleNode(ctx, log, voteHist, keys)
	if err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}
	qc, block, err := eng2.Submit(ctx, []byte("post-restart"))
	if err != nil {
		t.Fatalf("eng2.Submit: %v (BLS key persistence must carry across boot)", err)
	}
	// Next height extends the last committed (= block 2); prepared-
	// but-uncommitted block 3 is not recovered by Replay, so the new
	// submission lands at height 3 and view > 3.
	if block.Height != 3 {
		t.Fatalf("post-restart block height = %d; want 3", block.Height)
	}
	if qc.View <= 3 {
		t.Fatalf("post-restart QC view = %d; want > 3", qc.View)
	}

	// Committed log continues to grow across restarts: the new
	// submission's successor would commit this block, but a third
	// submission under the second engine is enough to confirm the
	// chain is live — commit block 3 via two-chain.
	if _, _, err := eng2.Submit(ctx, []byte("post-restart-2")); err != nil {
		t.Fatalf("eng2.Submit 2: %v", err)
	}
	heightAfter, _ := log.HighestHeight(ctx)
	if heightAfter <= heightBefore {
		t.Fatalf("after restart + 2 submits, committed height = %d; want > %d", heightAfter, heightBefore)
	}
}

// TestReplicaKeyStoreReturnsSameKey confirms the atomic LoadOrGenerate
// contract: the first call persists a new keypair; subsequent calls
// return the identical one.
func TestReplicaKeyStoreReturnsSameKey(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()
	ks := store.NewPostgresReplicaKeyStore(db)

	kp1, err := ks.LoadOrGenerate(ctx, bft.ReplicaID(0))
	if err != nil {
		t.Fatalf("first LoadOrGenerate: %v", err)
	}
	kp2, err := ks.LoadOrGenerate(ctx, bft.ReplicaID(0))
	if err != nil {
		t.Fatalf("second LoadOrGenerate: %v", err)
	}
	priv1, pub1 := kp1.Marshal()
	priv2, pub2 := kp2.Marshal()
	if !bytes.Equal(priv1, priv2) {
		t.Fatalf("second load returned different private key — persistence failed")
	}
	if !bytes.Equal(pub1, pub2) {
		t.Fatalf("second load returned different public key — persistence failed")
	}

	// A different replica ID yields a different keypair.
	kp3, err := ks.LoadOrGenerate(ctx, bft.ReplicaID(1))
	if err != nil {
		t.Fatalf("replica-1 LoadOrGenerate: %v", err)
	}
	priv3, _ := kp3.Marshal()
	if bytes.Equal(priv1, priv3) {
		t.Fatalf("replica 0 and replica 1 produced identical private keys — not isolated by replica_id")
	}
}
