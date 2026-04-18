package bft

// Signer is the cryptographic interface the protocol layer depends
// on. Session 3 uses BLSSigner (bls_signer.go) — BLS12-381 multi-
// signatures on top of gnark-crypto primitives. Earlier sessions
// used a non-cryptographic placeholder; the interface was shaped
// around the eventual BLS scheme so the switch was a pure signer-
// implementation swap, not a protocol change.
type Signer interface {
	// Sign produces a per-replica signature over msg.
	Sign(msg []byte) Signature
	// Verify checks a per-replica signature against the claimed signer.
	Verify(signer ReplicaID, msg []byte, sig Signature) error
	// Aggregate combines per-replica signatures into a threshold
	// signature. For BLS multi-sig this is the sum of per-replica
	// G1 points — one 48-byte signature regardless of signer count.
	Aggregate(sigs []Signature) Signature
	// VerifyAggregate validates a threshold signature against the set
	// of signers who contributed. Duplicate signers are rejected so a
	// single party can't silently double-weight itself.
	VerifyAggregate(signers []ReplicaID, msg []byte, agg Signature) error
}

// newViewDigest is the byte sequence a replica signs when emitting a
// NewView message during a view change. Domain byte 0x03 separates
// it from vote (0x01) and proposal (0x02) signatures; under BLS a
// shared digest bit-pattern across domains would let a vote signature
// be replayed as a NewView. Signs over
// (newViewTargetView, highQC.View, highQC.BlockHash) — the view the
// sender is advancing INTO plus the highQC it carries.
func newViewDigest(targetView View, highQCView View, highQCHash Hash) []byte {
	out := make([]byte, 0, 1+8+8+len(highQCHash))
	out = append(out, domainNewView)
	var buf [8]byte
	bigEndian8(buf[:], uint64(targetView))
	out = append(out, buf[:]...)
	bigEndian8(buf[:], uint64(highQCView))
	out = append(out, buf[:]...)
	out = append(out, highQCHash[:]...)
	return out
}
