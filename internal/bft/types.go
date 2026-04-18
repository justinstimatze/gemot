// Package bft implements chained-HotStuff Byzantine fault-tolerant
// sequence agreement for the gemot server. Session 1 scope is the
// happy-path state machine and in-memory transport; view change and
// real threshold signatures are session 2 deliverables. See
// specs/hotstuff-design.md for the protocol design.
package bft

import "crypto/sha256"

// Hash is a SHA-256 digest. Used to identify blocks and as the payload
// of votes and QCs. Fixed-size so struct values stay comparable.
type Hash [32]byte

// Signature is an opaque signature byte slice. Session 1 uses the
// placeholder signer (signature.go); session 2 wires a real threshold
// signature scheme via gnark-crypto or equivalent pure-Go BLS.
type Signature []byte

// ReplicaID identifies one replica in the replica set. Session 1
// assumes a fixed roster known at startup; dynamic reconfiguration is
// out of scope.
type ReplicaID uint32

// View is a logical round number. Chained HotStuff pipelines views so
// that view v+1's proposal carries the QC for view v, and the two-
// chain commit rule fires when v+1's QC forms.
type View uint64

// Height is the block's position in the committed chain. Monotonic
// across the honest majority.
type Height uint64

// Block is the unit of sequence. Gemot operations (submit_position,
// submit_vote, file_dispute, etc.) are serialised into the Payload;
// session 1 treats the payload as opaque bytes.
type Block struct {
	// Height is the block's position in the chain.
	Height Height
	// Parent is the hash of the parent block. For the genesis block this
	// is the zero Hash.
	Parent Hash
	// View is the view in which the leader proposed this block.
	View View
	// Payload is the serialised batch of gemot operations. Opaque to
	// the BFT layer in session 1.
	Payload []byte
	// Justify is the QC for the Parent block. This is the chained
	// HotStuff optimization: every proposal carries its parent's QC,
	// so a single round simultaneously advances prepare/pre-commit/
	// commit for different blocks on the chain.
	Justify QC
}

// Hash returns the block's SHA-256 digest. Computed deterministically
// from (Height, Parent, View, Payload) — Justify is excluded because
// it references this block's parent (already covered by Parent) and
// including it would create a hash dependency cycle in the chain.
func (b *Block) Hash() Hash {
	h := sha256.New()
	var buf [8]byte
	bigEndian8(buf[:], uint64(b.Height))
	h.Write(buf[:])
	h.Write(b.Parent[:])
	bigEndian8(buf[:], uint64(b.View))
	h.Write(buf[:])
	h.Write(b.Payload)
	var out Hash
	sum := h.Sum(nil)
	copy(out[:], sum)
	return out
}

// QC is a quorum certificate: a threshold-aggregated signature proving
// 2f+1 replicas voted for BlockHash in View. Session 1 stores both the
// aggregate signature and the explicit Signers list so placeholder
// verification can count votes; session 2's real threshold scheme may
// drop the Signers field once the aggregate signature encodes it.
type QC struct {
	// View is the view in which the votes were cast.
	View View
	// BlockHash is the hash of the block being voted on.
	BlockHash Hash
	// Signers is the set of replicas whose votes formed this QC.
	// Session 1: explicit list for placeholder verification.
	Signers []ReplicaID
	// AggSig is the threshold-aggregated signature.
	AggSig Signature
}

// IsGenesis reports whether this QC is the genesis QC — the distinguished
// no-op certificate every replica starts with.
func (q *QC) IsGenesis() bool {
	return q.View == 0 && q.BlockHash == (Hash{})
}

// Proposal is a leader's broadcast of a new block with the justifying
// QC of the parent block.
type Proposal struct {
	View    View
	Block   Block
	Justify QC
	Sender  ReplicaID
	Sig     Signature
}

// Vote is a replica's signed endorsement of a proposed block.
type Vote struct {
	View      View
	BlockHash Hash
	Voter     ReplicaID
	Sig       Signature
}

// NewView is sent by a replica on leader timeout, carrying the
// replica's highest-known QC so the next leader can extend it.
// Session 1 defines the type; view-change protocol that uses it is
// session 2.
type NewView struct {
	View   View
	HighQC QC
	Sender ReplicaID
	Sig    Signature
}

// bigEndian8 writes v into buf[:8] in big-endian order. Inline to avoid
// dragging in encoding/binary for one use.
func bigEndian8(buf []byte, v uint64) {
	buf[0] = byte(v >> 56)
	buf[1] = byte(v >> 48)
	buf[2] = byte(v >> 40)
	buf[3] = byte(v >> 32)
	buf[4] = byte(v >> 24)
	buf[5] = byte(v >> 16)
	buf[6] = byte(v >> 8)
	buf[7] = byte(v)
}
