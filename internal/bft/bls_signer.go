package bft

import (
	"errors"
	"fmt"
	"math/big"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
)

// Session-3 BLS12-381 signer. Replaces the PlaceholderSigner from
// sessions 1-2 with pairing-based signatures. This is *BLS multi-
// signature*, not threshold BLS: each replica owns an independent
// keypair, and aggregation is the sum of per-replica signatures
// verifiable against the sum of the signers' public keys. HotStuff
// QCs carry the explicit Signers list so the verifier knows which
// public keys to aggregate — matching the multi-signature model
// exactly and letting us defer the distributed-key-generation
// ceremony to session 5 (when multi-node deploy needs key
// distribution anyway).
//
// Scheme (min-sig variant, RFC 9380 hash-to-curve):
//   Private key: scalar s in fr (the scalar field of BLS12-381)
//   Public key:  s * g2 in G2
//   Sign(msg):   s * H(msg) in G1 (48 bytes compressed)
//   Verify:      e(sig, g2) == e(H(msg), pk), via PairingCheck
//                (negate H(msg) so product-equals-1 semantic matches)
//   Aggregate:   sum of per-replica G1 signatures (same msg only)
//   VerifyAgg:   sum signers' G2 public keys, do PairingCheck against
//                the summed key
//
// Message-layer domain separation (domainVote/Proposal/NewView bytes)
// is unchanged from session 2; the BLS layer hashes whatever bytes
// the message-layer digest functions emit.

// blsHashDST is the RFC 9380 domain-separation tag for BLS-signature
// hash-to-G1 on the BLS12-381 curve under SHA-256. Changing this
// invalidates every signature ever produced — schema-breaking.
var blsHashDST = []byte("BLS_SIG_BLS12381G1_XMD:SHA-256_SSWU_RO_NUL_")

// generatorG2 caches the BLS12-381 G2 generator so we don't re-fetch
// it on every Sign/Verify call.
var generatorG2 bls12381.G2Affine

func init() {
	_, _, _, g2 := bls12381.Generators()
	generatorG2 = g2
}

// BLSPublicKey is a replica's BLS public key — a G2 curve point.
type BLSPublicKey struct {
	point bls12381.G2Affine
}

// Marshal returns the 96-byte compressed G2 encoding of the key.
// Used for roster serialization when session-5 multi-node deploy
// distributes public keys across replicas.
func (pk BLSPublicKey) Marshal() []byte {
	b := pk.point.Bytes()
	return b[:]
}

// UnmarshalBLSPublicKey parses a 96-byte compressed G2 encoding
// back into a BLSPublicKey. Validates the point is on-curve and in
// the correct subgroup.
func UnmarshalBLSPublicKey(b []byte) (BLSPublicKey, error) {
	var pk BLSPublicKey
	if _, err := pk.point.SetBytes(b); err != nil {
		return BLSPublicKey{}, fmt.Errorf("bft: BLS public key unmarshal: %w", err)
	}
	return pk, nil
}

// BLSKeypair is a (private, public) BLS key pair. Generated locally
// in tests via GenerateBLSKeypair; distributed via a DKG ceremony in
// the session-5 multi-node deploy.
type BLSKeypair struct {
	Private fr.Element
	Public  BLSPublicKey
}

// GenerateBLSKeypair produces a fresh BLS12-381 keypair. Uses
// gnark-crypto's fr.Element.SetRandom, which draws from crypto/rand.
// Test-only: session 5 swaps in the DKG protocol.
func GenerateBLSKeypair() (BLSKeypair, error) {
	var priv fr.Element
	if _, err := priv.SetRandom(); err != nil {
		return BLSKeypair{}, fmt.Errorf("bft: BLS private key: %w", err)
	}
	var privBI big.Int
	priv.BigInt(&privBI)
	var pub bls12381.G2Affine
	pub.ScalarMultiplicationBase(&privBI)
	return BLSKeypair{
		Private: priv,
		Public:  BLSPublicKey{point: pub},
	}, nil
}

// GenerateBLSKeyset generates n independent keypairs and returns the
// per-replica keypairs plus a shared roster of their public keys.
// The roster is a slice indexed by ReplicaID — i.e., roster[i] is
// the public key for the replica with ID i. Test-only helper.
func GenerateBLSKeyset(n int) ([]BLSKeypair, []BLSPublicKey, error) {
	keys := make([]BLSKeypair, n)
	roster := make([]BLSPublicKey, n)
	for i := 0; i < n; i++ {
		kp, err := GenerateBLSKeypair()
		if err != nil {
			return nil, nil, fmt.Errorf("bft: keygen at index %d: %w", i, err)
		}
		keys[i] = kp
		roster[i] = kp.Public
	}
	return keys, roster, nil
}

// BLSSigner implements Signer using BLS12-381 multi-signatures.
// Holds this replica's keypair plus the roster of public keys so
// Verify/VerifyAggregate can look up signers by ReplicaID.
type BLSSigner struct {
	id     ReplicaID
	myKey  BLSKeypair
	roster []BLSPublicKey
}

// NewBLSSigner constructs a BLSSigner for replica id. The roster
// must contain the BLS public key of every replica in the cluster,
// indexed by ReplicaID (a uint32). The constructor verifies that
// myKey's public component matches roster[id] so a mis-keyed replica
// fails loudly at startup rather than producing signatures that no
// one can verify.
func NewBLSSigner(id ReplicaID, myKey BLSKeypair, roster []BLSPublicKey) (*BLSSigner, error) {
	if int(id) >= len(roster) {
		return nil, fmt.Errorf("bft: BLS signer id %d out of roster range %d", id, len(roster))
	}
	if !myKey.Public.point.Equal(&roster[id].point) {
		return nil, errors.New("bft: BLS signer keypair does not match roster entry at id")
	}
	rosterCopy := make([]BLSPublicKey, len(roster))
	copy(rosterCopy, roster)
	return &BLSSigner{
		id:     id,
		myKey:  myKey,
		roster: rosterCopy,
	}, nil
}

// Sign returns sig = priv * H(msg). Output is 48 bytes (compressed
// G1 point).
func (s *BLSSigner) Sign(msg []byte) Signature {
	h, err := bls12381.HashToG1(msg, blsHashDST)
	if err != nil {
		// HashToG1 errors only on malformed DST. The package-level DST
		// is a fixed valid string, so this is unreachable in production
		// — panic to surface a misconfigured build immediately.
		panic(fmt.Sprintf("bft: HashToG1 failed: %v", err))
	}
	var privBI big.Int
	s.myKey.Private.BigInt(&privBI)
	var sig bls12381.G1Affine
	sig.ScalarMultiplication(&h, &privBI)
	b := sig.Bytes()
	return b[:]
}

// Verify checks a per-replica signature.
// Pairing equation: e(sig, g2) == e(H(msg), pk).
// Implemented via PairingCheck( [-H(msg), sig], [pk, g2] ) which
// verifies the product of pairings equals 1.
func (s *BLSSigner) Verify(signer ReplicaID, msg []byte, sig Signature) error {
	if int(signer) >= len(s.roster) {
		return fmt.Errorf("bft: BLS verify unknown signer %d", signer)
	}
	var sigPoint bls12381.G1Affine
	if _, err := sigPoint.SetBytes(sig); err != nil {
		return fmt.Errorf("bft: BLS sig unmarshal: %w", err)
	}
	h, err := bls12381.HashToG1(msg, blsHashDST)
	if err != nil {
		return fmt.Errorf("bft: BLS HashToG1: %w", err)
	}
	var negH bls12381.G1Affine
	negH.Neg(&h)
	ok, err := bls12381.PairingCheck(
		[]bls12381.G1Affine{negH, sigPoint},
		[]bls12381.G2Affine{s.roster[signer].point, generatorG2},
	)
	if err != nil {
		return fmt.Errorf("bft: BLS pairing check: %w", err)
	}
	if !ok {
		return errors.New("bft: BLS signature invalid")
	}
	return nil
}

// Aggregate sums per-replica BLS signatures into a single 48-byte
// aggregate. For same-message inputs
//
//	agg = sum_i sig_i = sum_i (priv_i * H(msg)) = (sum_i priv_i) * H(msg)
//
// which is verifiable under the sum of signers' public keys — that
// is the multi-signature identity.
//
// Caller must ensure all input sigs are over the same msg; QC
// formation in HandleVote pins (view, blockhash) as the only signed
// digest so this is guaranteed at the protocol layer.
func (s *BLSSigner) Aggregate(sigs []Signature) Signature {
	if len(sigs) == 0 {
		return nil
	}
	var agg bls12381.G1Jac
	for i, sig := range sigs {
		var p bls12381.G1Affine
		if _, err := p.SetBytes(sig); err != nil {
			// The caller (HandleVote) has already verified each sig
			// individually before this point. A malformed sig reaching
			// Aggregate is a programming error, not an adversary
			// signal — panic so it surfaces in the call stack.
			panic(fmt.Sprintf("bft: BLS aggregate sig %d malformed: %v", i, err))
		}
		agg.AddMixed(&p)
	}
	var aggAff bls12381.G1Affine
	aggAff.FromJacobian(&agg)
	b := aggAff.Bytes()
	return b[:]
}

// VerifyAggregate validates the aggregate signature against the sum
// of the listed signers' public keys. Duplicates rejected — a
// duplicate signer would silently double-weight one party.
func (s *BLSSigner) VerifyAggregate(signers []ReplicaID, msg []byte, agg Signature) error {
	if len(signers) == 0 {
		return errors.New("bft: BLS verify-aggregate requires at least one signer")
	}
	seen := make(map[ReplicaID]struct{}, len(signers))
	var aggPK bls12381.G2Jac
	for _, id := range signers {
		if int(id) >= len(s.roster) {
			return fmt.Errorf("bft: BLS verify-aggregate unknown signer %d", id)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("bft: BLS verify-aggregate duplicate signer %d", id)
		}
		seen[id] = struct{}{}
		aggPK.AddMixed(&s.roster[id].point)
	}
	var aggPKAff bls12381.G2Affine
	aggPKAff.FromJacobian(&aggPK)

	var sigPoint bls12381.G1Affine
	if _, err := sigPoint.SetBytes(agg); err != nil {
		return fmt.Errorf("bft: BLS aggregate unmarshal: %w", err)
	}
	h, err := bls12381.HashToG1(msg, blsHashDST)
	if err != nil {
		return fmt.Errorf("bft: BLS HashToG1: %w", err)
	}
	var negH bls12381.G1Affine
	negH.Neg(&h)
	ok, err := bls12381.PairingCheck(
		[]bls12381.G1Affine{negH, sigPoint},
		[]bls12381.G2Affine{aggPKAff, generatorG2},
	)
	if err != nil {
		return fmt.Errorf("bft: BLS aggregate pairing check: %w", err)
	}
	if !ok {
		return errors.New("bft: BLS aggregate signature invalid")
	}
	return nil
}
