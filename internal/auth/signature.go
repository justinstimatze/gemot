// Package auth implements per-agent ed25519 signatures for position and vote
// integrity. Bearer tokens remain the outer session auth; signatures here are
// per-action integrity proofs verified at submit time.
//
// Canonicalization uses length-prefixed concatenation with a mandatory domain
// separator — the SSH/Noise/TUF pattern. We sign fixed-shape action records,
// not free-form JSON, which sidesteps every canonicalization-ambiguity pitfall
// catalogued by the Matrix and Ethereum EIP-712 teams (duplicate keys, Unicode
// normalization, number representation).
//
// Format for each field: uint32 big-endian length followed by raw bytes.
// Integer fields are int64 big-endian. The domain tag prefix (e.g.
// "gemot/v1/position") prevents cross-protocol replay: a valid position
// signature cannot be reused as a vote signature because the signed bytes differ
// in their leading tag.
package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	DomainPosition = "gemot/v1/position"
	DomainVote     = "gemot/v1/vote"

	AlgoEd25519 = "ed25519"
)

// ErrInvalidKey is returned when a public key is the wrong shape for its algorithm.
var ErrInvalidKey = errors.New("auth: invalid public key")

// ErrVerifyFailed is returned when a signature does not verify against the payload + key.
var ErrVerifyFailed = errors.New("auth: signature verification failed")

// ErrUnsupportedAlgo is returned when a registered key's algorithm is not implemented.
var ErrUnsupportedAlgo = errors.New("auth: unsupported signature algorithm")

// writeLenPrefixed appends a uint32-BE length followed by b to buf.
func writeLenPrefixed(buf []byte, b []byte) []byte {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))
	buf = append(buf, lenBuf[:]...)
	return append(buf, b...)
}

// writeInt64 appends an int64-BE to buf.
func writeInt64(buf []byte, v int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return append(buf, b[:]...)
}

// PositionPayload returns the canonical bytes to sign for a position submission.
// The payload commits to agent, deliberation, round, and a hash of the content.
// Signing the content hash (SHA-256) keeps signatures cheap to log and small to
// store, while still binding every byte of the submitted content.
func PositionPayload(agentID, deliberationID string, round int, content string) []byte {
	contentHash := sha256.Sum256([]byte(content))
	buf := make([]byte, 0, 64+len(agentID)+len(deliberationID))
	buf = writeLenPrefixed(buf, []byte(DomainPosition))
	buf = writeLenPrefixed(buf, []byte(agentID))
	buf = writeLenPrefixed(buf, []byte(deliberationID))
	buf = writeInt64(buf, int64(round))
	buf = writeLenPrefixed(buf, contentHash[:])
	return buf
}

// VotePayload returns the canonical bytes to sign for a vote.
// The payload commits to agent, deliberation, position, value, qualifier, caveat,
// and criterion (multi-criterion votes reuse the same agent/position tuple so the
// criterion must be in the signed bytes).
func VotePayload(agentID, deliberationID, positionID string, value int, qualifier, caveat, criterionID string) []byte {
	buf := make([]byte, 0, 96)
	buf = writeLenPrefixed(buf, []byte(DomainVote))
	buf = writeLenPrefixed(buf, []byte(agentID))
	buf = writeLenPrefixed(buf, []byte(deliberationID))
	buf = writeLenPrefixed(buf, []byte(positionID))
	buf = writeInt64(buf, int64(value))
	buf = writeLenPrefixed(buf, []byte(qualifier))
	buf = writeLenPrefixed(buf, []byte(caveat))
	buf = writeLenPrefixed(buf, []byte(criterionID))
	return buf
}

// Verify checks sig against msg using the given algorithm and public key.
// Only ed25519 is currently implemented; other algorithms return ErrUnsupportedAlgo.
func Verify(algo string, pubkey, msg, sig []byte) error {
	switch algo {
	case AlgoEd25519, "":
		if len(pubkey) != ed25519.PublicKeySize {
			return fmt.Errorf("%w: ed25519 public key must be %d bytes, got %d", ErrInvalidKey, ed25519.PublicKeySize, len(pubkey))
		}
		if !ed25519.Verify(ed25519.PublicKey(pubkey), msg, sig) {
			return ErrVerifyFailed
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedAlgo, algo)
	}
}

// ValidatePublicKey returns an error if the key is not well-formed for its algorithm.
// Used at registration time to reject obviously bad keys before they reach verification.
func ValidatePublicKey(algo string, pubkey []byte) error {
	switch algo {
	case AlgoEd25519, "":
		if len(pubkey) != ed25519.PublicKeySize {
			return fmt.Errorf("%w: ed25519 public key must be %d bytes, got %d", ErrInvalidKey, ed25519.PublicKeySize, len(pubkey))
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedAlgo, algo)
	}
}
