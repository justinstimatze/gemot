package bft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Session-4 durable log. A LogStore persists (Block, QC) pairs at
// commit time so a crashed replica can reconstruct its committed
// state on restart. Session 5 layers this into a full replay path
// that also durably tracks lastVotedView (needed for safety across
// restarts under Byzantine peers); session 4 is just the persistence
// + simple-replay primitive.

// LogEntry is one committed block plus the QC that formed on it.
// The QC is the one that proved 2f+1 honest replicas voted for
// Block in Block.View — not the child's QC that triggered the
// two-chain commit. Stored together so replay can reconstruct both
// knownBlocks and highQC without a second lookup.
type LogEntry struct {
	Block Block
	QC    QC
}

// LogStore is the BFT committed-log persistence interface. Session 4
// implementations: InMemoryLogStore (tests) and PostgresLogStore
// (internal/store/bft_log.go). Session 5 will add richer methods for
// HTTPTransport replica-sync and client-facing QC proof lookups.
type LogStore interface {
	// Append writes a committed block + its QC at the given height.
	// Implementations must enforce height-monotonic append — an
	// entry at a height < HighestHeight is an error. Idempotent at
	// the exact-entry level: appending the same (height, blockHash,
	// QC) twice is a no-op, but appending a DIFFERENT block at a
	// height already present is a violation of the log invariant
	// and must return ErrLogForkDetected.
	Append(ctx context.Context, entry LogEntry) error
	// Load returns all committed entries in height order. Used for
	// full replay on replica restart. For large logs session 5
	// will add a range/cursor variant.
	Load(ctx context.Context) ([]LogEntry, error)
	// HighestHeight returns the highest height currently persisted,
	// or 0 if the log is empty. Used by replay callers that want
	// to resume from a known point rather than re-load the whole log.
	HighestHeight(ctx context.Context) (Height, error)
}

// ErrLogForkDetected fires when an Append would overwrite a
// different block at an existing height. This is the canonical
// "the log forked" condition — safety violation — and must crash
// the replica rather than silently picking a winner.
var ErrLogForkDetected = errors.New("bft: log fork detected — two different blocks at same height")

// InMemoryLogStore is the tests-and-single-process LogStore. A
// simple slice keyed by height, no durability. Session-4 Postgres
// integration lives in internal/store/bft_log.go.
type InMemoryLogStore struct {
	entries []LogEntry // indexed by height-1 (height 1 is entries[0])
}

// NewInMemoryLogStore constructs an empty in-memory log.
func NewInMemoryLogStore() *InMemoryLogStore {
	return &InMemoryLogStore{}
}

// Append inserts entry at entry.Block.Height. Enforces monotonic
// append and fork detection (duplicate height with different block
// hash returns ErrLogForkDetected). Context is accepted for
// interface symmetry with PostgresLogStore but not consulted.
func (m *InMemoryLogStore) Append(_ context.Context, entry LogEntry) error {
	if entry.Block.Height == 0 {
		return errors.New("bft: log refuses genesis (height 0) entry — genesis is implicit")
	}
	idx := int(entry.Block.Height - 1)
	if idx < len(m.entries) {
		existing := m.entries[idx]
		if existing.Block.Hash() == entry.Block.Hash() {
			return nil // idempotent — exact-match re-append is OK
		}
		return fmt.Errorf("%w: height %d existing hash %x, new hash %x",
			ErrLogForkDetected, entry.Block.Height,
			existing.Block.Hash(), entry.Block.Hash())
	}
	if idx > len(m.entries) {
		return fmt.Errorf("bft: log append gap — height %d but next-expected %d",
			entry.Block.Height, len(m.entries)+1)
	}
	m.entries = append(m.entries, entry)
	return nil
}

// Load returns a copy of all entries in height order.
func (m *InMemoryLogStore) Load(_ context.Context) ([]LogEntry, error) {
	out := make([]LogEntry, len(m.entries))
	copy(out, m.entries)
	return out, nil
}

// HighestHeight returns the highest height in the log, or 0 if
// the log is empty (the implicit genesis height).
func (m *InMemoryLogStore) HighestHeight(_ context.Context) (Height, error) {
	if len(m.entries) == 0 {
		return 0, nil
	}
	return m.entries[len(m.entries)-1].Block.Height, nil
}

// EncodeBlock serializes a Block to JSON bytes for storage. Used by
// PostgresLogStore; lives here so the schema is defined alongside
// the consuming code. Session 5 may swap to a deterministic binary
// format (protobuf / MessagePack) — JSON was picked for MVP
// debuggability. Hash fields serialize as base64 arrays; []byte
// fields also base64-encoded.
func EncodeBlock(b Block) ([]byte, error) {
	return json.Marshal(b)
}

// DecodeBlock is the inverse of EncodeBlock.
func DecodeBlock(b []byte) (Block, error) {
	var out Block
	if err := json.Unmarshal(b, &out); err != nil {
		return Block{}, fmt.Errorf("bft: decode block: %w", err)
	}
	return out, nil
}

// EncodeQC serializes a QC to JSON bytes for storage.
func EncodeQC(q QC) ([]byte, error) {
	return json.Marshal(q)
}

// DecodeQC is the inverse of EncodeQC.
func DecodeQC(b []byte) (QC, error) {
	var out QC
	if err := json.Unmarshal(b, &out); err != nil {
		return QC{}, fmt.Errorf("bft: decode QC: %w", err)
	}
	return out, nil
}
