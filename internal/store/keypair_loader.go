package store

import (
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// loadOrGenerateKeypair implements the shared atomic first-boot pattern used
// by every server-held signing key this package persists (BFT replica keys,
// the OAuth issuer key): try load, generate a candidate on a miss, attempt
// to persist it, then re-load so every concurrent caller returns the same
// winning keypair.
//
// insert must itself be an INSERT ... ON CONFLICT DO NOTHING so concurrent
// first-boots serialize to exactly one winner rather than erroring or
// overwriting each other. Callers supply their own table-specific SQL via
// the closures — this shares only the control flow, deliberately never
// builds SQL from a string.
func loadOrGenerateKeypair[T any](tryLoad func() (T, bool, error), generate func() (T, error), insert func(T) error) (T, error) {
	var zero T
	if v, ok, err := tryLoad(); err != nil {
		return zero, err
	} else if ok {
		return v, nil
	}

	candidate, err := generate()
	if err != nil {
		return zero, fmt.Errorf("generate candidate keypair: %w", err)
	}
	if err := insert(candidate); err != nil {
		return zero, fmt.Errorf("insert keypair: %w", err)
	}

	// Re-read to get whichever keypair won the race. Guaranteed to exist
	// after the insert above.
	v, ok, err := tryLoad()
	if err != nil {
		return zero, err
	}
	if !ok {
		return zero, fmt.Errorf("keypair vanished after insert")
	}
	return v, nil
}
