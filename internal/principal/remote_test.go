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
	remIssuer    = "https://acme.example"
	remNS        = "acme:"
	remPrincipal = "acme:alice"
)

// newIssuer builds a RemoteIssuer with a fresh keypair and returns both.
func newIssuer(t *testing.T, name string, namespaces ...string) (RemoteIssuer, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate issuer key: %v", err)
	}
	if len(namespaces) == 0 {
		namespaces = []string{remNS}
	}
	return RemoteIssuer{Name: name, Namespaces: namespaces, PublicKey: pub, Algo: auth.AlgoEd25519}, priv
}

// localNone is a local verifier for which no principal is registered — so the
// local-shadow check always passes (there is nothing to shadow).
func localNone() *LocalVerifier {
	return NewLocalVerifier(func(context.Context, string) ([]byte, string, error) {
		return nil, "", ErrNoKey
	})
}

// fedCred mints a credential as issuer `remIssuer` would: signed by the issuer's
// key, bound to testAgent / testAgentPub.
func fedCred(t *testing.T, issuerPriv ed25519.PrivateKey, principal string) *Credential {
	t.Helper()
	return mint(t, issuerPriv, Credential{
		Principal: principal,
		Agent:     testAgent,
		AgentKey:  testAgentPub,
		Issuer:    remIssuer,
	})
}

func TestRoutingAcceptsFederatedCredential(t *testing.T) {
	iss, priv := newIssuer(t, remIssuer)
	rv, err := NewRoutingVerifier(localNone(), []RemoteIssuer{iss})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	res, err := rv.Verify(context.Background(), fedCred(t, priv, remPrincipal), testAgent, Target{DeliberationID: testDelib})
	if err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
	if res.Principal != remPrincipal || res.Issuer != remIssuer {
		t.Fatalf("Result = {%q,%q}, want {%q,%q}", res.Principal, res.Issuer, remPrincipal, remIssuer)
	}
}

// The issuer signs, not the principal: a credential signed by any key other
// than the configured issuer key must not verify.
func TestRoutingRejectsWrongIssuerKey(t *testing.T) {
	iss, _ := newIssuer(t, remIssuer)
	_, attacker, _ := ed25519.GenerateKey(nil)
	rv, err := NewRoutingVerifier(localNone(), []RemoteIssuer{iss})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	_, err = rv.Verify(context.Background(), fedCred(t, attacker, remPrincipal), testAgent, Target{DeliberationID: testDelib})
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("Verify() = %v, want ErrVerifyFailed", err)
	}
}

// The core control (T1): an issuer may only vouch for principals in its
// declared namespace.
func TestRoutingRejectsPrincipalOutsideNamespace(t *testing.T) {
	iss, priv := newIssuer(t, remIssuer) // namespace "acme:"
	rv, err := NewRoutingVerifier(localNone(), []RemoteIssuer{iss})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	_, err = rv.Verify(context.Background(), fedCred(t, priv, "other:mallory"), testAgent, Target{DeliberationID: testDelib})
	if !errors.Is(err, ErrIssuerNamespace) {
		t.Fatalf("Verify() = %v, want ErrIssuerNamespace", err)
	}
}

// T1, second form: even within its namespace, an issuer may not speak for a
// principal that is locally registered (self-sovereign).
func TestRoutingRejectsLocalPrincipalShadowing(t *testing.T) {
	iss, priv := newIssuer(t, remIssuer)
	// remPrincipal HAS a local key, so a remote issuer must not override it.
	localPub, _, _ := ed25519.GenerateKey(nil)
	local := NewLocalVerifier(lookupFor(remPrincipal, localPub))
	rv, err := NewRoutingVerifier(local, []RemoteIssuer{iss})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	_, err = rv.Verify(context.Background(), fedCred(t, priv, remPrincipal), testAgent, Target{DeliberationID: testDelib})
	if !errors.Is(err, ErrIssuerNamespace) {
		t.Fatalf("Verify() = %v, want ErrIssuerNamespace (shadowing)", err)
	}
}

// A registry outage during the shadow check must fail closed, not skip the
// check.
func TestRoutingShadowCheckFailsClosedOnOutage(t *testing.T) {
	iss, priv := newIssuer(t, remIssuer)
	dbDown := errors.New("connection refused")
	local := NewLocalVerifier(func(context.Context, string) ([]byte, string, error) {
		return nil, "", dbDown
	})
	rv, err := NewRoutingVerifier(local, []RemoteIssuer{iss})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	_, err = rv.Verify(context.Background(), fedCred(t, priv, remPrincipal), testAgent, Target{DeliberationID: testDelib})
	if !errors.Is(err, ErrKeyLookup) {
		t.Fatalf("Verify() = %v, want ErrKeyLookup", err)
	}
}

// An issuer not in the trust set is rejected, never defaulted to local (T2).
func TestRoutingRejectsUntrustedIssuer(t *testing.T) {
	iss, _ := newIssuer(t, remIssuer)
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	rv, err := NewRoutingVerifier(localNone(), []RemoteIssuer{iss})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	cred := mint(t, otherPriv, Credential{Principal: remPrincipal, Agent: testAgent, AgentKey: testAgentPub, Issuer: "https://evil.example"})
	_, err = rv.Verify(context.Background(), cred, testAgent, Target{DeliberationID: testDelib})
	if !errors.Is(err, ErrIssuerUnknown) {
		t.Fatalf("Verify() = %v, want ErrIssuerUnknown", err)
	}
}

// Empty and "local" issuer labels route to the local verifier, unchanged.
func TestRoutingDelegatesLocalToLocalVerifier(t *testing.T) {
	iss, _ := newIssuer(t, remIssuer)
	localPub, localPriv, _ := ed25519.GenerateKey(nil)
	local := NewLocalVerifier(lookupFor(testPrincipal, localPub))
	rv, err := NewRoutingVerifier(local, []RemoteIssuer{iss})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	// A normal local credential (principal self-signs) still verifies.
	cred := mint(t, localPriv, baseCred())
	if _, err := rv.Verify(context.Background(), cred, testAgent, Target{DeliberationID: testDelib}); err != nil {
		t.Fatalf("local credential via router = %v, want nil", err)
	}
}

// The issuer label is inside the signed payload and the signature is checked
// against the *named* issuer's key. So an attacker holding issuer A's key cannot
// forge a credential for issuer B — even for a principal inside B's namespace
// (T10). This is what stops a compromised issuer from impersonating another.
func TestRoutingRejectsCrossIssuerForgery(t *testing.T) {
	issA, privA := newIssuer(t, remIssuer, "acme:")
	issB, _ := newIssuer(t, "https://beta.example", "beta:")
	rv, err := NewRoutingVerifier(localNone(), []RemoteIssuer{issA, issB})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	// A's key signs a credential labelled as issuer B, for a principal in B's
	// namespace. Namespace binding passes; the signature is checked against B's
	// key and fails.
	forged := mint(t, privA, Credential{Principal: "beta:alice", Agent: testAgent, AgentKey: testAgentPub, Issuer: "https://beta.example"})
	_, err = rv.Verify(context.Background(), forged, testAgent, Target{DeliberationID: testDelib})
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("Verify() = %v, want ErrVerifyFailed", err)
	}
}

// Expiry, scope and agent-binding are enforced identically on the remote path.
func TestRoutingEnforcesStandardChecks(t *testing.T) {
	iss, priv := newIssuer(t, remIssuer)
	rv, err := NewRoutingVerifier(localNone(), []RemoteIssuer{iss})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}

	expired := mint(t, priv, Credential{Principal: remPrincipal, Agent: testAgent, AgentKey: testAgentPub, Issuer: remIssuer, ExpiresAt: time.Now().Add(-time.Minute)})
	if _, err := rv.Verify(context.Background(), expired, testAgent, Target{DeliberationID: testDelib}); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: Verify() = %v, want ErrExpired", err)
	}

	if _, err := rv.Verify(context.Background(), fedCred(t, priv, remPrincipal), "impostor", Target{DeliberationID: testDelib}); !errors.Is(err, ErrAgentMismatch) {
		t.Fatalf("agent mismatch: Verify() = %v, want ErrAgentMismatch", err)
	}

	scoped := mint(t, priv, Credential{Principal: remPrincipal, Agent: testAgent, AgentKey: testAgentPub, Issuer: remIssuer, Scope: ScopeDeliberationPrefix + "delib-other"})
	if _, err := rv.Verify(context.Background(), scoped, testAgent, Target{DeliberationID: testDelib}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("scope: Verify() = %v, want ErrScopeMismatch", err)
	}
}

func TestNewRoutingVerifierValidation(t *testing.T) {
	goodPub, _, _ := ed25519.GenerateKey(nil)
	mk := func(name string, ns []string, key []byte) RemoteIssuer {
		return RemoteIssuer{Name: name, Namespaces: ns, PublicKey: key, Algo: auth.AlgoEd25519}
	}

	tests := []struct {
		name    string
		issuers []RemoteIssuer
		wantErr bool
	}{
		{"valid single", []RemoteIssuer{mk("a", []string{"a:"}, goodPub)}, false},
		{"valid disjoint pair", []RemoteIssuer{mk("a", []string{"a:"}, goodPub), mk("b", []string{"b:"}, goodPub)}, false},
		{"reserved local name", []RemoteIssuer{mk(IssuerLocal, []string{"x:"}, goodPub)}, true},
		{"empty name", []RemoteIssuer{mk("", []string{"x:"}, goodPub)}, true},
		{"no namespaces", []RemoteIssuer{mk("a", nil, goodPub)}, true},
		{"empty namespace matches all", []RemoteIssuer{mk("a", []string{""}, goodPub)}, true},
		{"duplicate name", []RemoteIssuer{mk("a", []string{"a:"}, goodPub), mk("a", []string{"z:"}, goodPub)}, true},
		{"overlapping namespaces", []RemoteIssuer{mk("a", []string{"acme:"}, goodPub), mk("b", []string{"acme:eu:"}, goodPub)}, true},
		{"bad key length", []RemoteIssuer{mk("a", []string{"a:"}, []byte("short"))}, true},
		{"unsupported algo", []RemoteIssuer{{Name: "a", Namespaces: []string{"a:"}, PublicKey: goodPub, Algo: "rsa"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRoutingVerifier(localNone(), tc.issuers)
			if tc.wantErr && err == nil {
				t.Fatalf("NewRoutingVerifier(%s) = nil, want error", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("NewRoutingVerifier(%s) = %v, want nil", tc.name, err)
			}
		})
	}
}

// An empty issuer set yields a router that behaves exactly like the local
// verifier alone — the backward-compatible default.
func TestNewRoutingVerifierEmptyIsLocalOnly(t *testing.T) {
	localPub, localPriv, _ := ed25519.GenerateKey(nil)
	local := NewLocalVerifier(lookupFor(testPrincipal, localPub))
	rv, err := NewRoutingVerifier(local, nil)
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	if _, err := rv.Verify(context.Background(), mint(t, localPriv, baseCred()), testAgent, Target{DeliberationID: testDelib}); err != nil {
		t.Fatalf("local credential = %v, want nil", err)
	}
	// A remote-labelled credential has no trusted issuer, so it is rejected.
	_, remotePriv, _ := ed25519.GenerateKey(nil)
	cred := mint(t, remotePriv, Credential{Principal: remPrincipal, Agent: testAgent, AgentKey: testAgentPub, Issuer: remIssuer})
	if _, err := rv.Verify(context.Background(), cred, testAgent, Target{DeliberationID: testDelib}); !errors.Is(err, ErrIssuerUnknown) {
		t.Fatalf("remote credential with no issuers = %v, want ErrIssuerUnknown", err)
	}
}

func TestParseIssuers(t *testing.T) {
	if got, err := ParseIssuers(""); err != nil || got != nil {
		t.Fatalf("ParseIssuers(empty) = (%v, %v), want (nil, nil)", got, err)
	}
	if got, err := ParseIssuers("   "); err != nil || got != nil {
		t.Fatalf("ParseIssuers(blank) = (%v, %v), want (nil, nil)", got, err)
	}
	if _, err := ParseIssuers("{not an array}"); err == nil {
		t.Fatal("ParseIssuers(malformed) = nil, want error")
	}
	// public_key is base64 in JSON (standard []byte handling).
	valid := `[{"name":"https://acme.example","namespaces":["acme:"],"public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","algo":"ed25519"}]`
	got, err := ParseIssuers(valid)
	if err != nil {
		t.Fatalf("ParseIssuers(valid) = %v", err)
	}
	if len(got) != 1 || got[0].Name != "https://acme.example" || len(got[0].PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("ParseIssuers(valid) = %+v, unexpected", got)
	}
}
