package tests

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/principal"
)

// Principal-delegation integration tests cover the interaction between the
// agent_keys registry (which principals share with agents), principal_policy
// enforcement in the service layer, and the credential canonicalization in
// internal/principal.

const (
	delegPrincipal = "human:alice"
	delegAgent     = "alice-agent"
)

// mintCredential produces the JSON-encoded credential an agent would present.
func mintCredential(t *testing.T, priv ed25519.PrivateKey, c principal.Credential) []byte {
	t.Helper()
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = time.Now().Add(time.Hour)
	}
	c.Signature = ed25519.Sign(priv, c.SigningPayload())
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	return raw
}

// registerPrincipal generates a keypair and registers it under the principal's
// identity, which is what makes LocalVerifier able to resolve it.
func registerPrincipal(t *testing.T, svc *deliberation.Service, identity string) ed25519.PrivateKey {
	t.Helper()
	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(context.Background(), identity, pub, "ed25519"); err != nil {
		t.Fatalf("register principal key: %v", err)
	}
	return priv
}

// Deliberations created before principal credentials existed must keep working:
// the default policy leaves on_behalf_of as a free-text claim.
func TestPrincipalPolicy_DefaultAcceptsUnbackedClaim(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if d.PrincipalPolicy != "none" {
		t.Fatalf("default principal policy = %q, want \"none\"", d.PrincipalPolicy)
	}

	p, err := svc.SubmitPosition(ctx, d.ID, delegAgent, "hello",
		deliberation.WithOnBehalfOf(delegPrincipal))
	if err != nil {
		t.Fatalf("submit with unbacked on_behalf_of: %v", err)
	}
	if p.PrincipalVerified {
		t.Error("PrincipalVerified = true for an unbacked claim, want false")
	}
}

func TestPrincipalPolicy_RequiredRejectsUnbackedClaim(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, err := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithPrincipalPolicy("required"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = svc.SubmitPosition(ctx, d.ID, delegAgent, "hello",
		deliberation.WithOnBehalfOf(delegPrincipal))
	if err == nil {
		t.Fatal("submit with unbacked on_behalf_of succeeded, want rejection")
	}
	if !strings.Contains(err.Error(), "principal credential required") {
		t.Errorf("error = %q, want it to mention the missing credential", err)
	}
}

// A position that claims nothing needs no credential, even under "required".
func TestPrincipalPolicy_RequiredAllowsPositionWithNoClaim(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, err := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithPrincipalPolicy("required"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.SubmitPosition(ctx, d.ID, delegAgent, "speaking only for myself"); err != nil {
		t.Fatalf("submit without on_behalf_of: %v", err)
	}
}

func TestPrincipalPolicy_RequiredAcceptsVerifiedCredential(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	priv := registerPrincipal(t, svc, delegPrincipal)

	d, err := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithPrincipalPolicy("required"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cred := mintCredential(t, priv, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

	p, err := svc.SubmitPosition(ctx, d.ID, delegAgent, "hello",
		deliberation.WithOnBehalfOf(delegPrincipal),
		deliberation.WithPrincipalCredential(cred))
	if err != nil {
		t.Fatalf("submit with valid credential: %v", err)
	}
	if !p.PrincipalVerified {
		t.Error("PrincipalVerified = false for a verified credential, want true")
	}
	if len(p.PrincipalCredential) == 0 {
		t.Error("credential was not retained on the position")
	}
}

// Policy governs whether proof is *required*, never whether bad proof passes.
// A forged credential must be rejected even under the most permissive policy.
func TestPrincipalPolicy_NoneStillRejectsBadCredential(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	registerPrincipal(t, svc, delegPrincipal)

	// Signed by a key that is not the principal's registered one.
	_, attackerPriv := newKeypair(t)
	cred := mintCredential(t, attackerPriv, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = svc.SubmitPosition(ctx, d.ID, delegAgent, "hello",
		deliberation.WithOnBehalfOf(delegPrincipal),
		deliberation.WithPrincipalCredential(cred))
	if err == nil {
		t.Fatal("forged credential accepted under policy=none, want rejection")
	}
	if !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", err)
	}
}

// The credential names an agent. Another agent presenting it — the exact replay
// the binding exists to stop — must fail.
func TestPrincipalDelegation_RejectsReplayByOtherAgent(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	priv := registerPrincipal(t, svc, delegPrincipal)

	cred := mintCredential(t, priv, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = svc.SubmitPosition(ctx, d.ID, "impostor-agent", "hello",
		deliberation.WithOnBehalfOf(delegPrincipal),
		deliberation.WithPrincipalCredential(cred))
	if err == nil {
		t.Fatal("credential replayed by another agent was accepted, want rejection")
	}
	if !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", err)
	}
}

// A credential whose principal disagrees with on_behalf_of must be rejected,
// so a real credential for one principal cannot launder a claim about another.
func TestPrincipalDelegation_RejectsPrincipalMismatch(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	priv := registerPrincipal(t, svc, delegPrincipal)

	cred := mintCredential(t, priv, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = svc.SubmitPosition(ctx, d.ID, delegAgent, "hello",
		deliberation.WithOnBehalfOf("human:bob"),
		deliberation.WithPrincipalCredential(cred))
	if err == nil {
		t.Fatal("principal/on_behalf_of mismatch accepted, want rejection")
	}
	if !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", err)
	}
}

// A deliberation-scoped credential must not travel to a different deliberation.
func TestPrincipalDelegation_ScopeConfinesToDeliberation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	priv := registerPrincipal(t, svc, delegPrincipal)

	scoped, err := svc.CreateDeliberation(ctx, "Scoped", "")
	if err != nil {
		t.Fatalf("create scoped: %v", err)
	}
	other, err := svc.CreateDeliberation(ctx, "Other", "")
	if err != nil {
		t.Fatalf("create other: %v", err)
	}

	cred := mintCredential(t, priv, principal.Credential{
		Principal: delegPrincipal,
		Agent:     delegAgent,
		Scope:     principal.ScopeDeliberationPrefix + scoped.ID,
	})

	if _, err := svc.SubmitPosition(ctx, scoped.ID, delegAgent, "in scope",
		deliberation.WithOnBehalfOf(delegPrincipal),
		deliberation.WithPrincipalCredential(cred)); err != nil {
		t.Fatalf("submit inside scope: %v", err)
	}

	_, err = svc.SubmitPosition(ctx, other.ID, delegAgent, "out of scope",
		deliberation.WithOnBehalfOf(delegPrincipal),
		deliberation.WithPrincipalCredential(cred))
	if err == nil {
		t.Fatal("scoped credential accepted in another deliberation, want rejection")
	}
	if !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", err)
	}
}

// Revoking the principal's key is the revocation path. Credentials already
// minted and not yet expired must stop working immediately.
func TestPrincipalDelegation_KeyRevocationInvalidatesCredential(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	priv := registerPrincipal(t, svc, delegPrincipal)

	cred := mintCredential(t, priv, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

	d, err := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithPrincipalPolicy("required"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.SubmitPosition(ctx, d.ID, delegAgent, "before revocation",
		deliberation.WithOnBehalfOf(delegPrincipal),
		deliberation.WithPrincipalCredential(cred)); err != nil {
		t.Fatalf("submit before revocation: %v", err)
	}

	if err := svc.RevokeAgentKey(ctx, delegPrincipal); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	_, err = svc.SubmitPosition(ctx, d.ID, delegAgent, "after revocation",
		deliberation.WithOnBehalfOf(delegPrincipal),
		deliberation.WithPrincipalCredential(cred))
	if err == nil {
		t.Fatal("credential accepted after principal key revocation, want rejection")
	}
	if !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", err)
	}
}

func TestPrincipalDelegation_RejectsExpiredCredential(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	priv := registerPrincipal(t, svc, delegPrincipal)

	cred := mintCredential(t, priv, principal.Credential{
		Principal: delegPrincipal,
		Agent:     delegAgent,
		ExpiresAt: time.Now().Add(-time.Minute),
	})

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = svc.SubmitPosition(ctx, d.ID, delegAgent, "hello",
		deliberation.WithOnBehalfOf(delegPrincipal),
		deliberation.WithPrincipalCredential(cred))
	if err == nil {
		t.Fatal("expired credential accepted, want rejection")
	}
	if !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", err)
	}
}

// A verified credential is stronger evidence than the free-text field, so it
// may fill an empty on_behalf_of rather than requiring the agent to restate it.
func TestPrincipalDelegation_CredentialFillsEmptyOnBehalfOf(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	priv := registerPrincipal(t, svc, delegPrincipal)

	cred := mintCredential(t, priv, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	p, err := svc.SubmitPosition(ctx, d.ID, delegAgent, "hello",
		deliberation.WithPrincipalCredential(cred))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if p.OnBehalfOf != delegPrincipal {
		t.Errorf("OnBehalfOf = %q, want %q filled from the credential", p.OnBehalfOf, delegPrincipal)
	}
	if !p.PrincipalVerified {
		t.Error("PrincipalVerified = false, want true")
	}
}

// Verification state must survive the round trip through storage, otherwise an
// audit reading positions back cannot tell verified claims from unbacked ones.
func TestPrincipalDelegation_VerificationPersists(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	priv := registerPrincipal(t, svc, delegPrincipal)

	cred := mintCredential(t, priv, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.SubmitPosition(ctx, d.ID, delegAgent, "hello",
		deliberation.WithOnBehalfOf(delegPrincipal),
		deliberation.WithPrincipalCredential(cred)); err != nil {
		t.Fatalf("submit: %v", err)
	}

	positions, err := svc.GetPositions(ctx, d.ID, nil, nil)
	if err != nil {
		t.Fatalf("get positions: %v", err)
	}
	if len(positions) != 1 {
		t.Fatalf("got %d positions, want 1", len(positions))
	}
	got := positions[0]
	if !got.PrincipalVerified {
		t.Error("PrincipalVerified did not survive storage round trip")
	}
	if got.OnBehalfOf != delegPrincipal {
		t.Errorf("OnBehalfOf = %q, want %q", got.OnBehalfOf, delegPrincipal)
	}

	// The stored credential must be re-verifiable independently of the
	// server's own PrincipalVerified flag.
	var stored principal.Credential
	if err := json.Unmarshal(got.PrincipalCredential, &stored); err != nil {
		t.Fatalf("stored credential is not decodable: %v", err)
	}
	if stored.Principal != delegPrincipal || stored.Agent != delegAgent {
		t.Errorf("stored credential = %+v, want principal %q agent %q", stored, delegPrincipal, delegAgent)
	}
}
