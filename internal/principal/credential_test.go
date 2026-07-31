package principal

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
)

const (
	testPrincipal = "human:alice"
	testAgent     = "agent-1"
	testDelib     = "delib-abc"
	testGroup     = "group-xyz"
)

// mint builds a credential and signs it with priv. Mirrors what an external
// issuer would produce, so every test exercises the real canonicalization.
func mint(t *testing.T, priv ed25519.PrivateKey, c Credential) *Credential {
	t.Helper()
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = time.Now().Add(time.Hour)
	}
	c.Signature = ed25519.Sign(priv, c.SigningPayload())
	return &c
}

// lookupFor returns a KeyLookup that resolves identity to pub and fails for
// everything else — the same shape the store-backed lookup has.
func lookupFor(identity string, pub ed25519.PublicKey) KeyLookup {
	return func(_ context.Context, got string) ([]byte, string, error) {
		if got != identity {
			return nil, "", errors.New("no active key")
		}
		return pub, auth.AlgoEd25519, nil
	}
}

func newVerifier(t *testing.T) (*LocalVerifier, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return NewLocalVerifier(lookupFor(testPrincipal, pub)), priv
}

func baseCred() Credential {
	return Credential{Principal: testPrincipal, Agent: testAgent, Issuer: IssuerLocal}
}

func TestVerifyAcceptsWellFormedCredential(t *testing.T) {
	v, priv := newVerifier(t)
	cred := mint(t, priv, baseCred())

	res, err := v.Verify(context.Background(), cred, testAgent, Target{DeliberationID: testDelib})
	if err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
	if res.Principal != testPrincipal {
		t.Errorf("Principal = %q, want %q", res.Principal, testPrincipal)
	}
	if res.Issuer != IssuerLocal {
		t.Errorf("Issuer = %q, want %q", res.Issuer, IssuerLocal)
	}
}

// The credential binds a single agent. A credential captured off the wire — or
// lifted out of an exported deliberation — must not let a different agent
// speak for the principal.
func TestVerifyRejectsReplayByDifferentAgent(t *testing.T) {
	v, priv := newVerifier(t)
	cred := mint(t, priv, baseCred())

	_, err := v.Verify(context.Background(), cred, "agent-impostor", Target{DeliberationID: testDelib})
	if !errors.Is(err, ErrAgentMismatch) {
		t.Fatalf("Verify() = %v, want ErrAgentMismatch", err)
	}
}

func TestVerifyRejectsExpiredCredential(t *testing.T) {
	v, priv := newVerifier(t)
	cred := mint(t, priv, Credential{
		Principal: testPrincipal,
		Agent:     testAgent,
		ExpiresAt: time.Now().Add(-time.Minute),
	})

	_, err := v.Verify(context.Background(), cred, testAgent, Target{DeliberationID: testDelib})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("Verify() = %v, want ErrExpired", err)
	}
}

// Expiry is evaluated against the injected clock, and the boundary is
// exclusive: a credential expiring exactly now is already dead.
func TestVerifyExpiryBoundaryIsExclusive(t *testing.T) {
	v, priv := newVerifier(t)
	deadline := time.Now().Add(time.Hour)
	cred := mint(t, priv, Credential{Principal: testPrincipal, Agent: testAgent, ExpiresAt: deadline})

	v.Now = func() time.Time { return deadline }
	if _, err := v.Verify(context.Background(), cred, testAgent, Target{DeliberationID: testDelib}); !errors.Is(err, ErrExpired) {
		t.Fatalf("at expiry: Verify() = %v, want ErrExpired", err)
	}

	v.Now = func() time.Time { return deadline.Add(-time.Second) }
	if _, err := v.Verify(context.Background(), cred, testAgent, Target{DeliberationID: testDelib}); err != nil {
		t.Fatalf("one second before expiry: Verify() = %v, want nil", err)
	}
}

func TestVerifyScope(t *testing.T) {
	tests := []struct {
		name    string
		scope   string
		target  Target
		wantErr error
	}{
		{"empty scope covers any deliberation", "", Target{DeliberationID: testDelib}, nil},
		{"deliberation scope matches", ScopeDeliberationPrefix + testDelib, Target{DeliberationID: testDelib}, nil},
		{"deliberation scope rejects other deliberation", ScopeDeliberationPrefix + "delib-other", Target{DeliberationID: testDelib}, ErrScopeMismatch},
		{"group scope matches group member", ScopeGroupPrefix + testGroup, Target{DeliberationID: testDelib, GroupID: testGroup}, nil},
		{"group scope rejects other group", ScopeGroupPrefix + "group-other", Target{DeliberationID: testDelib, GroupID: testGroup}, ErrScopeMismatch},
		{"group scope rejects ungrouped deliberation", ScopeGroupPrefix + testGroup, Target{DeliberationID: testDelib}, ErrScopeMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, priv := newVerifier(t)
			c := baseCred()
			c.Scope = tc.scope
			cred := mint(t, priv, c)

			_, err := v.Verify(context.Background(), cred, testAgent, tc.target)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Verify() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Verify() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// Every signed field must be covered by the signature. Mutating any of them
// after minting has to break verification, otherwise an agent could widen its
// own scope, extend its own expiry, or claim a more trusted issuer.
func TestVerifyRejectsTamperedFields(t *testing.T) {
	tests := []struct {
		name  string
		mutfn func(*Credential)
	}{
		{"widened scope", func(c *Credential) { c.Scope = "" }},
		{"extended expiry", func(c *Credential) { c.ExpiresAt = c.ExpiresAt.Add(24 * time.Hour) }},
		{"relabelled issuer", func(c *Credential) { c.Issuer = "hcp" }},
		{"swapped principal", func(c *Credential) { c.Principal = testPrincipal }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pub, priv, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatalf("generate key: %v", err)
			}
			// Signed narrow, then widened after the fact.
			c := baseCred()
			c.Scope = ScopeDeliberationPrefix + testDelib
			c.Principal = "human:bob"
			cred := mint(t, priv, c)
			tc.mutfn(cred)

			// Resolve whichever principal the mutated credential now names, so
			// the failure is attributable to the signature and not a missing key.
			v := NewLocalVerifier(lookupFor(cred.Principal, pub))
			if _, err := v.Verify(context.Background(), cred, testAgent, Target{DeliberationID: testDelib}); !errors.Is(err, ErrVerifyFailed) {
				t.Fatalf("Verify() = %v, want ErrVerifyFailed", err)
			}
		})
	}
}

// Revoking the principal's key is the revocation path. It must invalidate
// credentials that are otherwise still valid and unexpired.
func TestVerifyRejectsRevokedPrincipalKey(t *testing.T) {
	_, priv := newVerifier(t)
	cred := mint(t, priv, baseCred())

	revoked := NewLocalVerifier(func(context.Context, string) ([]byte, string, error) {
		return nil, "", errors.New("key revoked")
	})
	if _, err := revoked.Verify(context.Background(), cred, testAgent, Target{DeliberationID: testDelib}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Verify() = %v, want ErrNoKey", err)
	}
}

// A credential signed by the wrong key must not verify even when every other
// field is correct.
func TestVerifyRejectsWrongSigningKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	_, attackerPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	cred := mint(t, attackerPriv, baseCred())

	v := NewLocalVerifier(lookupFor(testPrincipal, pub))
	if _, err := v.Verify(context.Background(), cred, testAgent, Target{DeliberationID: testDelib}); !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("Verify() = %v, want ErrVerifyFailed", err)
	}
}

// Domain separation: a position signature must not be reusable as a delegation
// credential. The leading domain tag differs, so the signed bytes differ.
func TestDelegationPayloadIsDomainSeparated(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	expiry := time.Now().Add(time.Hour)
	cred := Credential{Principal: testPrincipal, Agent: testAgent, Issuer: IssuerLocal, ExpiresAt: expiry}

	// A position signature over the same identifiers.
	positionSig := ed25519.Sign(priv, auth.PositionPayload(testAgent, testDelib, 1, "content"))
	cred.Signature = positionSig

	pub := priv.Public().(ed25519.PublicKey)
	v := NewLocalVerifier(lookupFor(testPrincipal, pub))
	if _, err := v.Verify(context.Background(), &cred, testAgent, Target{DeliberationID: testDelib}); !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("Verify() with a position signature = %v, want ErrVerifyFailed", err)
	}
}

func TestValidateRejectsMalformed(t *testing.T) {
	future := time.Now().Add(time.Hour)
	sig := []byte("sig")
	tests := []struct {
		name string
		cred Credential
	}{
		{"missing principal", Credential{Agent: testAgent, ExpiresAt: future, Signature: sig}},
		{"missing agent", Credential{Principal: testPrincipal, ExpiresAt: future, Signature: sig}},
		{"missing expiry", Credential{Principal: testPrincipal, Agent: testAgent, Signature: sig}},
		{"missing signature", Credential{Principal: testPrincipal, Agent: testAgent, ExpiresAt: future}},
		{"unknown scope prefix", Credential{Principal: testPrincipal, Agent: testAgent, Scope: "wildcard:*", ExpiresAt: future, Signature: sig}},
		{"oversized principal", Credential{Principal: string(make([]byte, MaxIdentityLen+1)), Agent: testAgent, ExpiresAt: future, Signature: sig}},
		{"oversized scope", Credential{Principal: testPrincipal, Agent: testAgent, Scope: ScopeGroupPrefix + string(make([]byte, MaxScopeLen)), ExpiresAt: future, Signature: sig}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cred.Validate(); !errors.Is(err, ErrMalformed) {
				t.Fatalf("Validate() = %v, want ErrMalformed", err)
			}
		})
	}
}

// Structural rejection must happen before any key lookup, so malformed
// credentials cannot be used to drive load against the key registry.
func TestVerifyRejectsMalformedBeforeKeyLookup(t *testing.T) {
	lookups := 0
	v := NewLocalVerifier(func(context.Context, string) ([]byte, string, error) {
		lookups++
		return nil, "", errors.New("should not be reached")
	})

	_, err := v.Verify(context.Background(), &Credential{Principal: testPrincipal}, testAgent, Target{DeliberationID: testDelib})
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("Verify() = %v, want ErrMalformed", err)
	}
	if lookups != 0 {
		t.Errorf("key lookups = %d, want 0", lookups)
	}
}

// An unset issuer must canonicalize to IssuerLocal on both the signing and the
// verifying side, so a credential minted without an explicit issuer verifies.
func TestIssuerDefaultsToLocal(t *testing.T) {
	v, priv := newVerifier(t)
	cred := mint(t, priv, Credential{Principal: testPrincipal, Agent: testAgent})

	res, err := v.Verify(context.Background(), cred, testAgent, Target{DeliberationID: testDelib})
	if err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
	if res.Issuer != IssuerLocal {
		t.Errorf("Issuer = %q, want %q", res.Issuer, IssuerLocal)
	}
}
