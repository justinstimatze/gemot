package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
)

func newKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return pub, priv
}

func TestPositionPayload_DomainSeparation(t *testing.T) {
	// Same agent, deliberation, round, and content — but position vs vote must produce
	// different payloads so a position signature cannot be replayed as a vote signature.
	pos := PositionPayload("alice", "delib1", 1, "content")
	vote := VotePayload("alice", "delib1", "content", 0, "", "", "")
	if bytes.Equal(pos, vote) {
		t.Fatal("position and vote payloads must differ by domain tag")
	}
}

func TestPositionPayload_Deterministic(t *testing.T) {
	a := PositionPayload("alice", "d1", 2, "hello")
	b := PositionPayload("alice", "d1", 2, "hello")
	if !bytes.Equal(a, b) {
		t.Fatal("payload must be deterministic")
	}
}

func TestPositionPayload_FieldsMatter(t *testing.T) {
	base := PositionPayload("alice", "d1", 1, "content")
	cases := map[string][]byte{
		"agent":   PositionPayload("bob", "d1", 1, "content"),
		"delib":   PositionPayload("alice", "d2", 1, "content"),
		"round":   PositionPayload("alice", "d1", 2, "content"),
		"content": PositionPayload("alice", "d1", 1, "different"),
	}
	for name, other := range cases {
		if bytes.Equal(base, other) {
			t.Errorf("changing %s should change payload", name)
		}
	}
}

func TestVotePayload_FieldsMatter(t *testing.T) {
	base := VotePayload("alice", "d1", "p1", 1, "because", "unless x", "c1")
	cases := map[string][]byte{
		"agent":     VotePayload("bob", "d1", "p1", 1, "because", "unless x", "c1"),
		"delib":     VotePayload("alice", "d2", "p1", 1, "because", "unless x", "c1"),
		"position":  VotePayload("alice", "d1", "p2", 1, "because", "unless x", "c1"),
		"value":     VotePayload("alice", "d1", "p1", 2, "because", "unless x", "c1"),
		"qualifier": VotePayload("alice", "d1", "p1", 1, "other", "unless x", "c1"),
		"caveat":    VotePayload("alice", "d1", "p1", 1, "because", "different", "c1"),
		"criterion": VotePayload("alice", "d1", "p1", 1, "because", "unless x", "c2"),
	}
	for name, other := range cases {
		if bytes.Equal(base, other) {
			t.Errorf("changing %s should change payload", name)
		}
	}
}

func TestSignAndVerify_Position(t *testing.T) {
	pub, priv := newKeypair(t)
	payload := PositionPayload("alice", "d1", 1, "my position")
	sig := ed25519.Sign(priv, payload)

	if err := Verify(AlgoEd25519, pub, payload, sig); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerify_WrongKey(t *testing.T) {
	_, priv := newKeypair(t)
	otherPub, _ := newKeypair(t)

	payload := PositionPayload("alice", "d1", 1, "my position")
	sig := ed25519.Sign(priv, payload)

	err := Verify(AlgoEd25519, otherPub, payload, sig)
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("want ErrVerifyFailed, got %v", err)
	}
}

func TestVerify_TamperedPayload(t *testing.T) {
	pub, priv := newKeypair(t)
	payload := PositionPayload("alice", "d1", 1, "my position")
	sig := ed25519.Sign(priv, payload)

	// Simulate content tampering — attacker keeps signature but changes content
	tampered := PositionPayload("alice", "d1", 1, "evil content")
	err := Verify(AlgoEd25519, pub, tampered, sig)
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("tampered payload must fail verify, got %v", err)
	}
}

func TestVerify_CrossDomainReplay(t *testing.T) {
	// A position signature must not verify when presented as a vote signature
	// even if the attacker controls all the fields — the domain tag prevents it.
	pub, priv := newKeypair(t)
	posPayload := PositionPayload("alice", "d1", 1, "content")
	sig := ed25519.Sign(priv, posPayload)

	votePayload := VotePayload("alice", "d1", "content", 0, "", "", "")
	if err := Verify(AlgoEd25519, pub, votePayload, sig); !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("cross-domain replay must fail, got %v", err)
	}
}

func TestEnvelopePayload_DomainAndFields(t *testing.T) {
	body := []byte(`{"method":"participate","params":{"action":"vote"}}`)
	h := [32]byte{}
	copy(h[:], body)
	base := EnvelopePayload("alice", "participate", h[:], "nonce-1", 1000)
	// Must differ from position / vote payloads sharing the same surface fields.
	if bytes.Equal(base, PositionPayload("alice", "participate", 1, string(body))) {
		t.Fatal("envelope must not collide with position payload")
	}
	// Each field contributes — changing any one changes the bytes.
	cases := map[string][]byte{
		"agent":     EnvelopePayload("bob", "participate", h[:], "nonce-1", 1000),
		"method":    EnvelopePayload("alice", "analyze", h[:], "nonce-1", 1000),
		"body":      EnvelopePayload("alice", "participate", []byte("different-hash"), "nonce-1", 1000),
		"nonce":     EnvelopePayload("alice", "participate", h[:], "nonce-2", 1000),
		"timestamp": EnvelopePayload("alice", "participate", h[:], "nonce-1", 1001),
	}
	for name, other := range cases {
		if bytes.Equal(base, other) {
			t.Errorf("changing %s should change envelope payload", name)
		}
	}
}

func TestEnvelopePayload_SignVerify(t *testing.T) {
	pub, priv := newKeypair(t)
	h := [32]byte{1, 2, 3}
	msg := EnvelopePayload("alice", "participate", h[:], "n", 1000)
	sig := ed25519.Sign(priv, msg)
	if err := Verify(AlgoEd25519, pub, msg, sig); err != nil {
		t.Fatalf("verify envelope: %v", err)
	}
	// Tampering with the timestamp should fail.
	tampered := EnvelopePayload("alice", "participate", h[:], "n", 1001)
	if err := Verify(AlgoEd25519, pub, tampered, sig); !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("tampered timestamp must fail verify, got %v", err)
	}
}

func TestValidatePublicKey(t *testing.T) {
	pub, _ := newKeypair(t)
	if err := ValidatePublicKey(AlgoEd25519, pub); err != nil {
		t.Errorf("valid ed25519 key rejected: %v", err)
	}
	if err := ValidatePublicKey(AlgoEd25519, []byte("too short")); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("want ErrInvalidKey, got %v", err)
	}
	if err := ValidatePublicKey("rsa-2048", pub); !errors.Is(err, ErrUnsupportedAlgo) {
		t.Errorf("want ErrUnsupportedAlgo, got %v", err)
	}
}
