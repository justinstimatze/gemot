package principal

import (
	"crypto/ed25519"
	"time"
)

// NewMinter returns a Minter that signs as issuer using priv. priv must be
// the private half of the public key registered for issuer in the deployment's
// RoutingVerifier.
func NewMinter(issuer string, priv ed25519.PrivateKey) *Minter {
	return &Minter{Issuer: issuer, priv: priv}
}

// Mint builds and signs a Credential authorizing agent (bound to agentKey)
// to speak for principalID, within scope, for ttl starting at now.
//
// ttl is caller-supplied but MUST be a fixed, server-side constant in
// practice — never derived from client input. A client-negotiable expiry
// would undo the one property Credential.ExpiresAt exists to guarantee: a
// delegation that lapses on its own even if a later revocation can't be
// seen (see the principal package doc).
//
// Signs first, then validates the fully-formed credential — Validate()
// itself requires a non-empty Signature (a credential with no signature is
// malformed by definition), so validating before signing would always fail.
// The wasted signature on a subsequently-rejected input is cheap for
// ed25519 and keeps this the single place that decides "is this shape
// acceptable", matching what LocalVerifier.Verify checks on the read side.
func (m *Minter) Mint(principalID, agent string, agentKey []byte, scope string, ttl time.Duration, now time.Time) (*Credential, error) {
	cred := &Credential{
		Principal: principalID,
		Agent:     agent,
		AgentKey:  agentKey,
		Scope:     scope,
		Issuer:    m.Issuer,
		ExpiresAt: now.Add(ttl),
	}
	cred.Signature = ed25519.Sign(m.priv, cred.SigningPayload())
	if err := cred.Validate(); err != nil {
		return nil, err
	}
	return cred, nil
}

// Minter mints signed principal.Credential values server-side — the FIRST
// place gemot ever signs a delegation rather than only verifying one. It
// exists solely to back the OAuth2 authorization_code + PKCE consent flow
// (see internal/mcp/oauth_authorize.go): a human proves control of their own
// gmt_ API key and approves a specific agent, and this mints the same
// Credential format on_behalf_of already consumes.
//
// Issuer must be registered as a RemoteIssuer with the matching public key
// in whatever RoutingVerifier the deployment installs (see main.go) —
// otherwise a credential this mints can never itself verify. Minting and
// trusting-what-was-minted are wired together at startup for exactly this
// reason; Minter itself does not enforce it.
type Minter struct {
	Issuer string
	priv   ed25519.PrivateKey
}
