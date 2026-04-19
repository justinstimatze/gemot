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

// TestWritesRoutedThroughBFT confirms that when a Postgres-backed
// engine is attached via SetBFTEngine, every service write
// (positions, votes, commitments, disputes) lands in bft_log —
// exercising the "one code path" guarantee that BFT routing always
// runs rather than being gated by a feature flag.
func TestWritesRoutedThroughBFT(t *testing.T) {
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

	d, err := svc.CreateDeliberation(ctx, "BFT Routing", "Verify every write orders through BFT")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}

	// One position + one vote = two BFT submits. Two-chain rule
	// commits block 1 (position) when block 2 (vote) forms its QC.
	p, err := svc.SubmitPosition(ctx, d.ID, "agent1", "we should prioritize interoperability")
	if err != nil {
		t.Fatalf("SubmitPosition: %v", err)
	}
	if err := svc.Vote(ctx, d.ID, "agent2", p.ID, 1, "", ""); err != nil {
		t.Fatalf("Vote: %v", err)
	}

	// A third write (a commitment) advances the chain far enough to
	// commit both prior actions.
	if _, err := svc.Commit(ctx, d.ID, "agent1", "I will work on the interop proposal", ""); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// bft_log must now hold at least 2 committed entries (position +
	// vote). Commitment commits on the NEXT write — so three writes
	// yields exactly two commits under the two-chain rule.
	height, err := log.HighestHeight(ctx)
	if err != nil {
		t.Fatalf("HighestHeight: %v", err)
	}
	if height < 2 {
		t.Fatalf("expected >= 2 committed entries after 3 writes; got %d — not all writes routed through BFT", height)
	}
}

// TestClientVerifiesAuditProofs is the 5d acceptance test: a client
// holding only the server's published public key can independently
// verify that each entry in the tamper-evident log was signed by the
// server — closing the self-attestation gap where the server
// previously reported its own log without anything a client could
// cross-check.
func TestClientVerifiesAuditProofs(t *testing.T) {
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

	// Client-side: fetch the replica's public key once.
	pubBytes, err := svc.ReplicaPublicKey()
	if err != nil {
		t.Fatalf("ReplicaPublicKey: %v", err)
	}
	pub, err := bft.UnmarshalBLSPublicKey(pubBytes)
	if err != nil {
		t.Fatalf("UnmarshalBLSPublicKey: %v", err)
	}
	roster := []bft.BLSPublicKey{pub}

	// Drive enough writes to commit a couple of actions.
	d, err := svc.CreateDeliberation(ctx, "Verify Test", "")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}
	p, err := svc.SubmitPosition(ctx, d.ID, "agent1", "position one")
	if err != nil {
		t.Fatalf("SubmitPosition: %v", err)
	}
	if err := svc.Vote(ctx, d.ID, "agent2", p.ID, 1, "", ""); err != nil {
		t.Fatalf("Vote: %v", err)
	}
	if _, err := svc.Commit(ctx, d.ID, "agent1", "I commit to X", ""); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Client-side: fetch audit log and verify every proof.
	entries, err := svc.GetTamperEvidentLog(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetTamperEvidentLog: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one committed entry in audit log")
	}
	for i, e := range entries {
		if len(e.Proof) == 0 {
			t.Fatalf("entry %d missing proof", i)
		}
		var qc bft.QC
		if err := json.Unmarshal(e.Proof, &qc); err != nil {
			t.Fatalf("entry %d: decode proof: %v", i, err)
		}
		if err := bft.VerifyQC(qc, roster); err != nil {
			t.Fatalf("entry %d (height %d, action %s): VerifyQC: %v — server self-attestation failed offline verification", i, e.Height, e.ActionType, err)
		}
	}

	// Negative control: a corrupted QC must fail verification.
	var corruptQC bft.QC
	_ = json.Unmarshal(entries[0].Proof, &corruptQC)
	corruptQC.View++ // tamper with the signed digest
	if err := bft.VerifyQC(corruptQC, roster); err == nil {
		t.Fatalf("tampered QC should fail verification; got nil error")
	}
}

// TestBFTEngineResumesAcrossBoot is the session-5c acceptance test:
// with the replica keypair persisted in bft_replica_keys, a fresh
// engine booted against the same stores can verify prior-boot QCs
// and extend the chain.
func TestBFTEngineResumesAcrossBoot(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	log := store.NewPostgresLogStore(db)
	voteHist := store.NewPostgresVoteHistoryStore(db, bft.ReplicaID(0))
	keys := store.NewPostgresReplicaKeyStore(db)

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

	eng2, err := bft.BootstrapSingleNode(ctx, log, voteHist, keys)
	if err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}
	qc, block, err := eng2.Submit(ctx, []byte("post-restart"))
	if err != nil {
		t.Fatalf("eng2.Submit: %v (BLS key persistence must carry across boot)", err)
	}
	if block.Height != 3 {
		t.Fatalf("post-restart block height = %d; want 3", block.Height)
	}
	if qc.View <= 3 {
		t.Fatalf("post-restart QC view = %d; want > 3", qc.View)
	}

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

	kp3, err := ks.LoadOrGenerate(ctx, bft.ReplicaID(1))
	if err != nil {
		t.Fatalf("replica-1 LoadOrGenerate: %v", err)
	}
	priv3, _ := kp3.Marshal()
	if bytes.Equal(priv1, priv3) {
		t.Fatalf("replica 0 and replica 1 produced identical private keys — not isolated by replica_id")
	}
}
