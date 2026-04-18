package bft

import (
	"context"
	"fmt"
	"sync"
)

// ReplicaKeyStore persists a replica's BLS keypair across restarts.
// Without this, BootstrapSingleNode regenerates a fresh keypair on
// every boot, which breaks cross-boot QC verification — QCs signed
// under boot N's key cannot be verified under boot N+1's roster.
// Session 5c closes that gap.
//
// LoadOrGenerate is the atomic primitive: the first call for a given
// replica ID generates and persists a new keypair; subsequent calls
// return the persisted one. Concurrent callers for the same ID
// converge on a single keypair (enforced via ON CONFLICT DO NOTHING
// + re-read in the Postgres implementation).
type ReplicaKeyStore interface {
	LoadOrGenerate(ctx context.Context, replicaID ReplicaID) (BLSKeypair, error)
}

// InMemoryReplicaKeyStore is a test reference implementation. Keeps
// keys in memory only — useful for tests that want the same keypair
// across multiple Bootstrap calls within one process.
type InMemoryReplicaKeyStore struct {
	mu   sync.Mutex
	keys map[ReplicaID]BLSKeypair
}

// NewInMemoryReplicaKeyStore constructs an empty store.
func NewInMemoryReplicaKeyStore() *InMemoryReplicaKeyStore {
	return &InMemoryReplicaKeyStore{keys: make(map[ReplicaID]BLSKeypair)}
}

// LoadOrGenerate returns the stored keypair for replicaID, generating
// + persisting if absent.
func (s *InMemoryReplicaKeyStore) LoadOrGenerate(_ context.Context, id ReplicaID) (BLSKeypair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kp, ok := s.keys[id]; ok {
		return kp, nil
	}
	kp, err := GenerateBLSKeypair()
	if err != nil {
		return BLSKeypair{}, fmt.Errorf("bft: in-memory keystore generate: %w", err)
	}
	s.keys[id] = kp
	return kp, nil
}
