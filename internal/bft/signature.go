package bft

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
)

// Signer is the cryptographic interface the protocol layer depends on.
// Session 1 uses PlaceholderSigner, which encodes replica ID in the
// signature bytes so tests can validate protocol mechanics without a
// real crypto library. Session 2 replaces this with a pure-Go BLS
// threshold scheme (gnark-crypto is the preliminary choice — see
// specs/hotstuff-design.md).
type Signer interface {
	// Sign produces a per-replica signature over msg.
	Sign(msg []byte) Signature
	// Verify checks a per-replica signature against the claimed signer.
	Verify(signer ReplicaID, msg []byte, sig Signature) error
	// Aggregate combines per-replica signatures into a threshold
	// signature. The real scheme will produce a single O(|sig|) blob;
	// the placeholder concatenates sorted inputs.
	Aggregate(sigs []Signature) Signature
	// VerifyAggregate validates a threshold signature against the set
	// of signers who contributed.
	VerifyAggregate(signers []ReplicaID, msg []byte, agg Signature) error
}

// PlaceholderSigner is a NON-CRYPTOGRAPHIC signer for session 1 tests.
// It produces deterministic, forgeable "signatures" encoding the
// replica ID so protocol tests can reason about vote provenance
// without a threshold-sig library. Any code path that instantiates
// this signer in a non-test build must be caught in code review —
// see the constructor's loud comment.
type PlaceholderSigner struct {
	// ID of the replica this signer acts for. Real BLS would store a
	// private key share here.
	ID ReplicaID
}

// NewPlaceholderSigner constructs the session-1 placeholder signer.
//
// DO NOT SHIP THIS TO PRODUCTION. The placeholder provides no
// unforgeability guarantees. Session 2 replaces this with a real
// pure-Go BLS threshold scheme. A TODO at the top of this file
// (and a grep for "PlaceholderSigner" in CI) is how we catch
// accidental reuse.
func NewPlaceholderSigner(id ReplicaID) *PlaceholderSigner {
	return &PlaceholderSigner{ID: id}
}

// Sign returns a deterministic byte slice derived from (replica, msg).
// First byte is the replica ID (truncated to uint8 — fine for session
// 1's small replica sets) followed by the SHA-256 of msg. This is
// forgeable by anyone who knows the replica ID; the placeholder
// exists only to let protocol tests distinguish votes by source.
func (s *PlaceholderSigner) Sign(msg []byte) Signature {
	h := sha256.Sum256(msg)
	out := make([]byte, 0, 1+len(h))
	out = append(out, byte(s.ID))
	out = append(out, h[:]...)
	return out
}

// Verify checks that the first byte matches the claimed signer and the
// SHA-256 matches msg. Unforgeable only in the sense that the placeholder
// encodes the signer's ID — not a cryptographic property.
func (s *PlaceholderSigner) Verify(signer ReplicaID, msg []byte, sig Signature) error {
	if len(sig) != 1+sha256.Size {
		return fmt.Errorf("placeholder sig wrong length: got %d, want %d", len(sig), 1+sha256.Size)
	}
	if sig[0] != byte(signer) {
		return fmt.Errorf("placeholder sig replica-id mismatch: got %d, want %d", sig[0], signer)
	}
	h := sha256.Sum256(msg)
	if !bytes.Equal(sig[1:], h[:]) {
		return errors.New("placeholder sig message digest mismatch")
	}
	return nil
}

// Aggregate concatenates per-replica placeholder signatures sorted by
// replica ID. Real BLS produces a single constant-size aggregate; the
// placeholder is intentionally O(n) in replica count to avoid masking
// the size gap in session-2 benchmarks.
func (s *PlaceholderSigner) Aggregate(sigs []Signature) Signature {
	copied := make([]Signature, len(sigs))
	copy(copied, sigs)
	sort.Slice(copied, func(i, j int) bool {
		if len(copied[i]) == 0 || len(copied[j]) == 0 {
			return len(copied[i]) < len(copied[j])
		}
		return copied[i][0] < copied[j][0]
	})
	var out Signature
	for _, sig := range copied {
		out = append(out, sig...)
	}
	return out
}

// VerifyAggregate splits the concatenated signature back into per-replica
// chunks (each of length 1+sha256.Size) and verifies each. Session 2's
// real scheme replaces this with a single threshold-signature check.
func (s *PlaceholderSigner) VerifyAggregate(signers []ReplicaID, msg []byte, agg Signature) error {
	chunkLen := 1 + sha256.Size
	if len(agg) != len(signers)*chunkLen {
		return fmt.Errorf("placeholder agg sig wrong length: got %d, want %d", len(agg), len(signers)*chunkLen)
	}
	sorted := make([]ReplicaID, len(signers))
	copy(sorted, signers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for i, signer := range sorted {
		chunk := agg[i*chunkLen : (i+1)*chunkLen]
		if err := s.Verify(signer, msg, chunk); err != nil {
			return fmt.Errorf("placeholder agg sig chunk %d (replica %d): %w", i, signer, err)
		}
	}
	return nil
}
