package bft

import (
	"bytes"
	"testing"
)

// Session-3 BLS signer unit tests. Exercise sign/verify, aggregate,
// and negative cases (tampered sig, wrong signer, duplicate in
// aggregate) independently of the HotStuff protocol layer.

func TestBLSSignVerifyRoundtrip(t *testing.T) {
	keys, roster, err := GenerateBLSKeyset(4)
	if err != nil {
		t.Fatalf("GenerateBLSKeyset: %v", err)
	}
	signer, err := NewBLSSigner(0, keys[0], roster)
	if err != nil {
		t.Fatalf("NewBLSSigner: %v", err)
	}

	msg := []byte("hotstuff vote (view=7, blockhash=0xdeadbeef)")
	sig := signer.Sign(msg)
	if len(sig) == 0 {
		t.Fatal("Sign produced empty signature")
	}
	if err := signer.Verify(0, msg, sig); err != nil {
		t.Fatalf("Verify good sig: %v", err)
	}
}

func TestBLSVerifyRejectsTamperedSig(t *testing.T) {
	keys, roster, err := GenerateBLSKeyset(4)
	if err != nil {
		t.Fatalf("keyset: %v", err)
	}
	signer, _ := NewBLSSigner(0, keys[0], roster)
	msg := []byte("vote msg")
	sig := signer.Sign(msg)
	tampered := bytes.Clone(sig)
	tampered[0] ^= 0x01
	if err := signer.Verify(0, msg, tampered); err == nil {
		t.Fatal("Verify accepted a tampered signature")
	}
}

func TestBLSVerifyRejectsWrongSigner(t *testing.T) {
	keys, roster, err := GenerateBLSKeyset(4)
	if err != nil {
		t.Fatalf("keyset: %v", err)
	}
	signer0, _ := NewBLSSigner(0, keys[0], roster)
	msg := []byte("vote msg")
	sig := signer0.Sign(msg)
	// Verify the sig against replica 1's public key — should fail.
	if err := signer0.Verify(1, msg, sig); err == nil {
		t.Fatal("Verify accepted sig under wrong signer's pubkey")
	}
}

func TestBLSAggregateRoundtrip(t *testing.T) {
	keys, roster, err := GenerateBLSKeyset(4)
	if err != nil {
		t.Fatalf("keyset: %v", err)
	}
	verifier, _ := NewBLSSigner(0, keys[0], roster)
	msg := []byte("vote msg")

	// Three replicas (0, 1, 2) sign the same msg — the 2f+1 case for
	// N=4 f=1.
	var sigs []Signature
	signers := []ReplicaID{0, 1, 2}
	for _, id := range signers {
		s, _ := NewBLSSigner(id, keys[id], roster)
		sigs = append(sigs, s.Sign(msg))
	}
	agg := verifier.Aggregate(sigs)
	if err := verifier.VerifyAggregate(signers, msg, agg); err != nil {
		t.Fatalf("VerifyAggregate: %v", err)
	}
}

func TestBLSAggregateRejectsDuplicateSigner(t *testing.T) {
	keys, roster, err := GenerateBLSKeyset(4)
	if err != nil {
		t.Fatalf("keyset: %v", err)
	}
	verifier, _ := NewBLSSigner(0, keys[0], roster)
	msg := []byte("vote msg")
	s0, _ := NewBLSSigner(0, keys[0], roster)
	s1, _ := NewBLSSigner(1, keys[1], roster)
	sigs := []Signature{s0.Sign(msg), s0.Sign(msg), s1.Sign(msg)}
	// Intentionally pass signer 0 twice — VerifyAggregate must reject.
	// (The Aggregate call will still produce a sum, but VerifyAggregate
	// with duplicate signer IDs must catch the double-weight attack.)
	agg := verifier.Aggregate(sigs)
	err = verifier.VerifyAggregate([]ReplicaID{0, 0, 1}, msg, agg)
	if err == nil {
		t.Fatal("VerifyAggregate accepted duplicate signer in list")
	}
}

func TestBLSAggregateVerifyRejectsWrongMessage(t *testing.T) {
	keys, roster, err := GenerateBLSKeyset(4)
	if err != nil {
		t.Fatalf("keyset: %v", err)
	}
	verifier, _ := NewBLSSigner(0, keys[0], roster)
	msgA := []byte("block A")
	msgB := []byte("block B")
	signers := []ReplicaID{0, 1, 2}
	var sigs []Signature
	for _, id := range signers {
		s, _ := NewBLSSigner(id, keys[id], roster)
		sigs = append(sigs, s.Sign(msgA))
	}
	agg := verifier.Aggregate(sigs)
	if err := verifier.VerifyAggregate(signers, msgB, agg); err == nil {
		t.Fatal("VerifyAggregate accepted aggregate under a different message")
	}
}

func TestBLSNewSignerRejectsMismatchedKey(t *testing.T) {
	keys, roster, err := GenerateBLSKeyset(4)
	if err != nil {
		t.Fatalf("keyset: %v", err)
	}
	// Try to instantiate replica 0's signer with replica 1's keypair —
	// the pubkey won't match roster[0].
	if _, err := NewBLSSigner(0, keys[1], roster); err == nil {
		t.Fatal("NewBLSSigner accepted keypair that doesn't match roster[id]")
	}
}
