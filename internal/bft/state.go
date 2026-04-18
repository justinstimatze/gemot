package bft

import (
	"fmt"
	"sync"
)

// Replica is the per-node state of a HotStuff replica. Session 1
// holds this state in memory only; session 2 backs it with a durable
// append-only log in Postgres and replays on restart.
//
// All state mutation happens under mu. The public Handle* methods
// acquire mu internally; callers should not hold mu across calls.
type Replica struct {
	// ID is this replica's identifier in the roster.
	ID ReplicaID
	// N is the total replica count.
	N int
	// F is the Byzantine tolerance bound, must satisfy 3*F < N.
	F int

	// view is the current logical view number. Advances on QC formation
	// (session 1) or on view-change timeout (session 2).
	view View
	// highQC is the highest-view QC this replica has observed. Seeded
	// to genesis at construction.
	highQC QC
	// lockedQC is the safety lock: a replica will only vote for a block
	// that extends lockedQC's block OR has a justify QC in a view
	// strictly greater than lockedQC.View. See HotStuff paper §4.
	lockedQC QC
	// preparedQC is the QC of the last block this replica voted for.
	preparedQC QC
	// lastVotedView is the anti-equivocation guard: a replica votes at
	// most once per view.
	lastVotedView View

	// knownBlocks indexes all blocks this replica has seen, by hash.
	// Needed so the two-chain commit rule can look up a block's parent.
	knownBlocks map[Hash]Block
	// committed records the hashes of blocks this replica has committed.
	// The committed log is extracted by walking the parent chain from
	// the newest committed block; for session 1 we keep the set for
	// simple existence checks and add an ordered slice for projection.
	committed    map[Hash]bool
	committedLog []Hash
	// pendingVotes groups incoming votes by (view, blockhash) so the
	// leader can detect when 2f+1 has been collected and form a QC.
	pendingVotes map[voteKey]map[ReplicaID]Signature

	signer    Signer
	transport Transport
	roster    []ReplicaID

	mu sync.Mutex
}

// voteKey identifies a vote tally bucket.
type voteKey struct {
	view View
	hash Hash
}

// NewReplica constructs a replica with the given roster, placeholder
// signer, and transport. Genesis is installed as the initial high QC
// so the first proposal has a valid justify chain.
func NewReplica(id ReplicaID, n, f int, signer Signer, transport Transport, roster []ReplicaID) (*Replica, error) {
	if n <= 0 {
		return nil, fmt.Errorf("bft: replica count must be positive, got %d", n)
	}
	if 3*f >= n {
		return nil, fmt.Errorf("bft: f<N/3 required, got N=%d, F=%d", n, f)
	}
	if len(roster) != n {
		return nil, fmt.Errorf("bft: roster size %d != N %d", len(roster), n)
	}
	genesis := QC{} // View=0, BlockHash=zero, no signers, no sig.
	r := &Replica{
		ID:            id,
		N:             n,
		F:             f,
		view:          1,
		highQC:        genesis,
		lockedQC:      genesis,
		preparedQC:    genesis,
		lastVotedView: 0,
		knownBlocks:   make(map[Hash]Block),
		committed:     make(map[Hash]bool),
		committedLog:  []Hash{{}},
		pendingVotes:  make(map[voteKey]map[ReplicaID]Signature),
		signer:        signer,
		transport:     transport,
		roster:        append([]ReplicaID{}, roster...),
	}
	// Genesis is committed by construction — every replica agrees on it.
	var zero Hash
	r.committed[zero] = true
	return r, nil
}

// Quorum is the 2f+1 threshold required to form a QC.
func (r *Replica) Quorum() int { return 2*r.F + 1 }

// View returns the current view. Exported for tests; do not call from
// hot paths under the lock.
func (r *Replica) View() View {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.view
}

// Committed returns a copy of the ordered committed log. Exported for
// tests; the session-3 service-layer wire-up will use a streaming API
// instead of copying the whole slice.
func (r *Replica) Committed() []Hash {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Hash, len(r.committedLog))
	copy(out, r.committedLog)
	return out
}

// leader returns the designated leader for a given view. Deterministic
// rotation: view mod N over the roster.
func (r *Replica) leader(v View) ReplicaID {
	return r.roster[int(v)%r.N]
}

// extends reports whether descendant's chain (walking Parent pointers
// through knownBlocks) reaches ancestor. Genesis (zero hash) is an
// ancestor of every block by convention.
func (r *Replica) extends(descendant, ancestor Hash) bool {
	var zero Hash
	if ancestor == zero {
		return true
	}
	cur := descendant
	for cur != zero {
		if cur == ancestor {
			return true
		}
		b, ok := r.knownBlocks[cur]
		if !ok {
			return false
		}
		cur = b.Parent
	}
	return false
}
