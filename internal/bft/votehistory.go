package bft

import (
	"context"
	"fmt"
	"sync"
)

// VoteHistoryStore persists a replica's anti-equivocation counters
// across restarts. Without this, a crashed replica on restart would
// reset lastVotedView/proposedInView to 0 and could vote a second
// time in a view it already voted in — a Byzantine peer racing the
// restart can exploit that to manufacture equivocating quorums.
//
// Two independent counters:
//
//   - last-voted view: highest view in which this replica emitted a
//     vote (HandleProposal path). Persisted BEFORE the vote is sent.
//
//   - last-proposed view: highest view in which this replica emitted
//     a proposal as leader (Propose path). Persisted BEFORE the
//     proposal is returned to the caller for broadcast.
//
// Both counters are monotonic — writes may only advance the stored
// value, never regress it. Implementations MUST enforce monotonicity
// so a stale in-flight write cannot clobber a newer value on retry.
type VoteHistoryStore interface {
	// SaveVote records that the replica is about to vote in view v.
	// Returns on successful durability; the caller proceeds to emit
	// the vote only after this returns nil.
	SaveVote(ctx context.Context, v View) error
	// SaveProposal records that the replica is about to emit a
	// proposal in view v. Caller broadcasts only after nil return.
	SaveProposal(ctx context.Context, v View) error
	// Load returns the highest-ever-saved values for (lastVoted,
	// lastProposed). A fresh store returns (0, 0, nil).
	Load(ctx context.Context) (lastVoted, lastProposed View, err error)
}

// InMemoryVoteHistoryStore is a reference implementation for tests.
// Enforces monotonic writes and thread-safe access.
type InMemoryVoteHistoryStore struct {
	mu            sync.Mutex
	lastVoted    View
	lastProposed View
}

// NewInMemoryVoteHistoryStore constructs an empty store.
func NewInMemoryVoteHistoryStore() *InMemoryVoteHistoryStore {
	return &InMemoryVoteHistoryStore{}
}

func (s *InMemoryVoteHistoryStore) SaveVote(_ context.Context, v View) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v < s.lastVoted {
		return fmt.Errorf("bft: vote history regression: save %d < stored %d", v, s.lastVoted)
	}
	s.lastVoted = v
	return nil
}

func (s *InMemoryVoteHistoryStore) SaveProposal(_ context.Context, v View) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v < s.lastProposed {
		return fmt.Errorf("bft: proposal history regression: save %d < stored %d", v, s.lastProposed)
	}
	s.lastProposed = v
	return nil
}

func (s *InMemoryVoteHistoryStore) Load(_ context.Context) (View, View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastVoted, s.lastProposed, nil
}
