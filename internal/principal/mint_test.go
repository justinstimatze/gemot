package principal

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
)

func TestMintProducesValidCredential(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate issuer key: %v", err)
	}
	m := NewMinter("gemot-oauth", priv)
	cred, err := m.Mint("oauthkey:deadbeef", testAgent, testAgentPub, "", time.Hour, time.Now())
	if err != nil {
		t.Fatalf("Mint() = %v, want nil", err)
	}
	if err := cred.Validate(); err != nil {
		t.Fatalf("minted credential fails Validate(): %v", err)
	}
	if cred.Issuer != "gemot-oauth" {
		t.Errorf("Issuer = %q, want gemot-oauth", cred.Issuer)
	}
	if len(cred.Signature) == 0 {
		t.Error("Signature is empty")
	}
}

// TestMintedCredentialRejectedByLocalVerifier proves a self-issued credential
// (Issuer: "gemot-oauth") is never silently treated as locally-signed: bare
// LocalVerifier.Verify doesn't inspect cred.Issuer at all (that dispatch is
// RoutingVerifier's job), so the real guarantee to test is that
// RoutingVerifier fails closed when "gemot-oauth" isn't a configured issuer,
// rather than falling through to the local verifier.
func TestMintedCredentialRejectedByLocalVerifier(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate issuer key: %v", err)
	}
	m := NewMinter("gemot-oauth", priv)
	cred, err := m.Mint("oauthkey:deadbeef", testAgent, testAgentPub, "", time.Hour, time.Now())
	if err != nil {
		t.Fatalf("Mint() = %v, want nil", err)
	}

	// No issuers configured at all — "gemot-oauth" is unknown, and must be
	// rejected, never routed to the local verifier just because Principal
	// happens to parse as a normal string.
	rv, err := NewRoutingVerifier(localNone(), nil)
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	if _, err := rv.Verify(context.Background(), cred, testAgent, Target{}); !errors.Is(err, ErrIssuerUnknown) {
		t.Fatalf("Verify() = %v, want ErrIssuerUnknown", err)
	}
}

// TestMintedCredentialRejectedByWrongIssuerKey proves the issuer label is
// bound into the signed payload, not just routing metadata: a credential
// minted by one key does not verify against a RemoteIssuer entry sharing
// the same Name but a DIFFERENT PublicKey.
func TestMintedCredentialRejectedByWrongIssuerKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate issuer key: %v", err)
	}
	wrongPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate wrong key: %v", err)
	}
	m := NewMinter("gemot-oauth", priv)
	cred, err := m.Mint("oauthkey:deadbeef", testAgent, testAgentPub, "", time.Hour, time.Now())
	if err != nil {
		t.Fatalf("Mint() = %v, want nil", err)
	}

	selfIssuer := RemoteIssuer{Name: "gemot-oauth", Namespaces: []string{"oauthkey:"}, PublicKey: wrongPub, Algo: auth.AlgoEd25519}
	rv, err := NewRoutingVerifier(localNone(), []RemoteIssuer{selfIssuer})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}

	if _, err := rv.Verify(context.Background(), cred, testAgent, Target{}); !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("Verify() = %v, want ErrVerifyFailed", err)
	}
}

// TestMintedCredentialVerifiesViaMatchingIssuer proves the intended reuse:
// a credential this package mints verifies through the exact same
// RoutingVerifier/IssuerVerifier machinery an external federated issuer
// uses — no new verification code needed for gemot-as-its-own-issuer.
func TestMintedCredentialVerifiesViaMatchingIssuer(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate issuer key: %v", err)
	}
	m := NewMinter("gemot-oauth", priv)
	cred, err := m.Mint("oauthkey:deadbeef", testAgent, testAgentPub, "", time.Hour, time.Now())
	if err != nil {
		t.Fatalf("Mint() = %v, want nil", err)
	}

	selfIssuer := RemoteIssuer{Name: "gemot-oauth", Namespaces: []string{"oauthkey:"}, PublicKey: pub, Algo: auth.AlgoEd25519}
	rv, err := NewRoutingVerifier(localNone(), []RemoteIssuer{selfIssuer})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}

	res, err := rv.Verify(context.Background(), cred, testAgent, Target{})
	if err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
	if res.Principal != "oauthkey:deadbeef" || res.Issuer != "gemot-oauth" {
		t.Errorf("Result = {%q,%q}, want {oauthkey:deadbeef,gemot-oauth}", res.Principal, res.Issuer)
	}
}
