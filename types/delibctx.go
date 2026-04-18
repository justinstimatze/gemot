package types

import "context"

// delibContextKey is the private key for plumbing (deliberationID,
// isPrivate) into the reputation layer's WeightsFor call. Kept in
// the types package because both the service layer (which sets it)
// and the reputation layer (which reads it) need access without
// creating an import cycle: reputation → analysis → deliberation,
// so deliberation cannot import reputation.
type delibContextKey struct{}

// DelibContext carries per-round information WeightsFor needs to
// decide between the global-reputation path and the per-delib
// private-EigenTrust path. IsPrivate=true with a non-empty ID
// triggers the private path: load the delib's scoped edges union'd
// with the global graph, compute EigenTrust fresh, apply the cold-
// start cap over that.
type DelibContext struct {
	ID        string
	IsPrivate bool
}

// WithDelibContext returns a context carrying the current delib's
// id and privacy flag. The service layer wraps the analyzer context
// before invoking synthesis so downstream reputation calls can
// switch between global and per-delib paths without any signature
// change on the analysis.ReputationWeigher interface.
//
// Absence of the key is the zero-value case — callers that never
// opt in (tests, scripts, tools) get global semantics, preserving
// legacy behaviour.
func WithDelibContext(ctx context.Context, delibID string, isPrivate bool) context.Context {
	return context.WithValue(ctx, delibContextKey{}, DelibContext{ID: delibID, IsPrivate: isPrivate})
}

// DelibFromContext returns the (id, isPrivate) pair stored by
// WithDelibContext, or the zero DelibContext if none was attached.
func DelibFromContext(ctx context.Context) DelibContext {
	if v, ok := ctx.Value(delibContextKey{}).(DelibContext); ok {
		return v
	}
	return DelibContext{}
}
