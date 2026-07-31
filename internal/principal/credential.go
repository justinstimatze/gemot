// Package principal turns Position.OnBehalfOf from an unverified free-text
// claim into a verifiable delegation.
//
// Before this package, an agent could assert `on_behalf_of: "human:alice"` on
// any position and gemot would store it, export it, and hand it to auditors
// with no evidence behind it. That is an impersonation hole in the one field an
// audit is most likely to trust — and it silently weakens the tamper-evident
// log, which faithfully records a claim it never checked.
//
// A Credential is a delegation attestation: the principal signs "agent A may
// speak for me, within scope S, until T". The agent presents it alongside the
// position; gemot verifies the signature against the principal's registered
// key, that the credential names *this* agent, that the scope covers *this*
// deliberation, and that it has not expired.
//
// # What a credential deliberately does not carry
//
// A credential is a capability, never personal context. It holds an identity
// reference, an authorization, and an expiry — no preferences, no profile, no
// personal data of any kind. This is a hard design constraint, not an
// oversight: positions land in an append-only BLS-signed log, and an
// append-only log cannot honor a later revocation. Storing a capability keeps
// revocation meaningful (revoke the principal's key and every credential it
// ever signed stops verifying) while storing context would not. Any future
// integration that resolves richer principal context — an HCP preference
// lookup, for instance — must resolve it at read time and keep it out of the
// signed payload.
//
// # Pluggability
//
// Verifier is the seam. LocalVerifier is the built-in backend: principals
// register ed25519 keys in the same identity→key registry agents already use,
// so there is no new table and no new revocation path. External authorities
// (an HCP server, an OAuth-style delegation service) implement the same
// interface without any change to the service layer.
package principal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
)

// Issuer labels identify which authority minted a credential. The label is part
// of the signed payload, so a credential cannot be relabelled as coming from a
// more trusted issuer than the principal actually used.
const (
	// IssuerLocal marks a credential signed by a principal whose key lives in
	// gemot's own identity→key registry.
	IssuerLocal = "local"
)

// Scope prefixes. An empty scope authorizes the agent across all
// deliberations; a prefixed scope narrows it.
const (
	ScopeDeliberationPrefix = "delib:"
	ScopeGroupPrefix        = "group:"
)

// Field bounds. Credentials arrive from the network, so every string is capped
// before it reaches signature verification or storage.
const (
	MaxIdentityLen = 256
	MaxScopeLen    = 320
	MaxIssuerLen   = 64
)

var (
	// ErrMalformed means the credential is structurally invalid — missing a
	// required field, over a length bound, or carrying an unusable expiry.
	ErrMalformed = errors.New("principal: malformed credential")

	// ErrExpired means the credential's expiry is in the past.
	ErrExpired = errors.New("principal: credential expired")

	// ErrAgentMismatch means the credential authorizes a different agent than
	// the one presenting it — a replay of someone else's delegation.
	ErrAgentMismatch = errors.New("principal: credential does not authorize this agent")

	// ErrScopeMismatch means the credential's scope does not cover this
	// deliberation.
	ErrScopeMismatch = errors.New("principal: credential scope does not cover this deliberation")

	// ErrPrincipalMismatch means the credential's principal differs from the
	// on_behalf_of value on the position.
	ErrPrincipalMismatch = errors.New("principal: credential principal does not match on_behalf_of")

	// ErrNoKey means the principal has no active registered key, which is also
	// what revocation looks like: revoke the key and every credential the
	// principal ever signed stops verifying.
	ErrNoKey = errors.New("principal: no active key registered for principal")

	// ErrVerifyFailed means the signature did not verify against the
	// principal's registered key.
	ErrVerifyFailed = errors.New("principal: delegation signature verification failed")
)

// Credential is a signed delegation attestation.
//
// The JSON encoding is the wire format accepted by the MCP and A2A transports.
// Signature is raw bytes here; transports that carry JSON encode it as base64.
type Credential struct {
	// Principal is the identity delegating authority, e.g. "human:alice" or a
	// DID. It must match the position's on_behalf_of.
	Principal string `json:"principal"`

	// Agent is the single agent authorized to speak for Principal. Binding the
	// agent is what stops a captured credential from being replayed by another
	// agent.
	Agent string `json:"agent"`

	// Scope narrows where the delegation applies. Empty authorizes all
	// deliberations; "delib:<id>" or "group:<id>" narrow it.
	Scope string `json:"scope,omitempty"`

	// Issuer identifies the minting authority (IssuerLocal by default).
	Issuer string `json:"issuer,omitempty"`

	// ExpiresAt is mandatory. A delegation that cannot lapse on its own would
	// outlive any verifier outage that hides a revocation.
	ExpiresAt time.Time `json:"expires_at"`

	// Signature is the principal's signature over auth.PrincipalDelegationPayload.
	Signature []byte `json:"signature"`
}

// Target is what the credential's scope is checked against: the deliberation
// the position is being submitted to, and the group that deliberation belongs
// to (empty when it belongs to none).
type Target struct {
	DeliberationID string
	GroupID        string
}

// Result is the outcome of a successful verification. It is what the service
// layer records — note that it carries provenance, not context.
type Result struct {
	Principal string    `json:"principal"`
	Agent     string    `json:"agent"`
	Scope     string    `json:"scope,omitempty"`
	Issuer    string    `json:"issuer"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SigningPayload returns the canonical bytes the principal signs. Exposed so
// credential minters (tests, CLIs, external issuers) produce byte-identical
// input to what verification reconstructs.
func (c *Credential) SigningPayload() []byte {
	return auth.PrincipalDelegationPayload(c.Principal, c.Agent, c.Scope, c.issuerOrDefault(), c.ExpiresAt.Unix())
}

func (c *Credential) issuerOrDefault() string {
	if c.Issuer == "" {
		return IssuerLocal
	}
	return c.Issuer
}

// Validate checks the credential's shape without touching any key material or
// signature. Cheap structural rejection runs before the expensive lookup so a
// flood of malformed credentials cannot drive key-registry load.
func (c *Credential) Validate() error {
	switch {
	case c == nil:
		return fmt.Errorf("%w: nil credential", ErrMalformed)
	case c.Principal == "":
		return fmt.Errorf("%w: principal is required", ErrMalformed)
	case len(c.Principal) > MaxIdentityLen:
		return fmt.Errorf("%w: principal exceeds %d characters", ErrMalformed, MaxIdentityLen)
	case c.Agent == "":
		return fmt.Errorf("%w: agent is required", ErrMalformed)
	case len(c.Agent) > MaxIdentityLen:
		return fmt.Errorf("%w: agent exceeds %d characters", ErrMalformed, MaxIdentityLen)
	case len(c.Scope) > MaxScopeLen:
		return fmt.Errorf("%w: scope exceeds %d characters", ErrMalformed, MaxScopeLen)
	case len(c.Issuer) > MaxIssuerLen:
		return fmt.Errorf("%w: issuer exceeds %d characters", ErrMalformed, MaxIssuerLen)
	case c.ExpiresAt.IsZero():
		return fmt.Errorf("%w: expires_at is required", ErrMalformed)
	case len(c.Signature) == 0:
		return fmt.Errorf("%w: signature is required", ErrMalformed)
	}
	if s := c.Scope; s != "" &&
		!strings.HasPrefix(s, ScopeDeliberationPrefix) &&
		!strings.HasPrefix(s, ScopeGroupPrefix) {
		return fmt.Errorf("%w: scope must be empty, %q-prefixed, or %q-prefixed, got %q",
			ErrMalformed, ScopeDeliberationPrefix, ScopeGroupPrefix, s)
	}
	return nil
}

// CoversTarget reports whether the credential's scope authorizes use in t.
//
// An empty scope covers everything. A "delib:" scope must name the exact
// deliberation. A "group:" scope must name the group the deliberation belongs
// to — and a deliberation with no group is covered by no group scope, so an
// ungrouped deliberation can never be reached by a group-scoped credential.
func (c *Credential) CoversTarget(t Target) bool {
	switch {
	case c.Scope == "":
		return true
	case strings.HasPrefix(c.Scope, ScopeDeliberationPrefix):
		return strings.TrimPrefix(c.Scope, ScopeDeliberationPrefix) == t.DeliberationID
	case strings.HasPrefix(c.Scope, ScopeGroupPrefix):
		want := strings.TrimPrefix(c.Scope, ScopeGroupPrefix)
		return want != "" && t.GroupID != "" && want == t.GroupID
	default:
		// Unknown prefix — Validate rejects these, so reaching here means a
		// caller skipped validation. Fail closed.
		return false
	}
}

// Verifier resolves and validates a delegation credential.
//
// Implementations must be safe for concurrent use. A non-nil error means the
// delegation is not established; callers decide what to do about that based on
// the deliberation's principal policy.
type Verifier interface {
	Verify(ctx context.Context, cred *Credential, agentID string, t Target) (*Result, error)
}

// KeyLookup resolves an identity to its active public key and algorithm.
//
// It mirrors the signature of the existing agent-key lookup so LocalVerifier
// can be wired to the same registry without this package importing the store.
// Implementations should return an error wrapping (or matching) a
// "not found" sentinel when the identity has no active key; LocalVerifier
// treats every lookup failure as ErrNoKey, which is the correct reading for
// both "never registered" and "revoked".
type KeyLookup func(ctx context.Context, identity string) (pubkey []byte, algo string, err error)

// LocalVerifier verifies credentials signed by principals whose keys live in
// gemot's own identity→key registry.
//
// Revocation is inherited rather than reimplemented: revoking the principal's
// key invalidates every credential that principal ever signed, including ones
// already handed out and not yet expired.
type LocalVerifier struct {
	// Lookup resolves a principal identity to its active key. Required.
	Lookup KeyLookup

	// Now is injectable for tests. nil uses time.Now.
	Now func() time.Time
}

// NewLocalVerifier returns a LocalVerifier backed by lookup.
func NewLocalVerifier(lookup KeyLookup) *LocalVerifier {
	return &LocalVerifier{Lookup: lookup}
}

func (v *LocalVerifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Verify establishes that cred authorizes agentID to speak for cred.Principal
// in t.
//
// Checks run cheapest-first so that malformed or plainly inapplicable
// credentials are rejected before any key lookup: shape, then agent binding,
// then expiry, then scope, and only then the signature.
func (v *LocalVerifier) Verify(ctx context.Context, cred *Credential, agentID string, t Target) (*Result, error) {
	if v.Lookup == nil {
		return nil, errors.New("principal: LocalVerifier has no key lookup configured")
	}
	if err := cred.Validate(); err != nil {
		return nil, err
	}
	if cred.Agent != agentID {
		return nil, fmt.Errorf("%w: credential authorizes %q, presented by %q",
			ErrAgentMismatch, cred.Agent, agentID)
	}
	if !cred.ExpiresAt.After(v.now()) {
		return nil, fmt.Errorf("%w: expired at %s", ErrExpired, cred.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if !cred.CoversTarget(t) {
		return nil, fmt.Errorf("%w: scope %q does not cover deliberation %q",
			ErrScopeMismatch, cred.Scope, t.DeliberationID)
	}

	pubkey, algo, err := v.Lookup(ctx, cred.Principal)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrNoKey, cred.Principal)
	}
	if err := auth.Verify(algo, pubkey, cred.SigningPayload(), cred.Signature); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVerifyFailed, err)
	}

	return &Result{
		Principal: cred.Principal,
		Agent:     cred.Agent,
		Scope:     cred.Scope,
		Issuer:    cred.issuerOrDefault(),
		ExpiresAt: cred.ExpiresAt,
	}, nil
}
