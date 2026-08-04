package tests

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
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

// delegation bundles everything a credentialed submission now needs: the
// principal's signing key, and the agent keypair the credential is bound to.
// Proof-of-possession means the agent must have its key registered and must
// sign the position, so no test can present a credential without one.
type delegation struct {
	principalPriv ed25519.PrivateKey
	agentPub      ed25519.PublicKey
	agentPriv     ed25519.PrivateKey
}

// newDelegation registers keys for both the principal and the agent.
func newDelegation(t *testing.T, svc *deliberation.Service, principalID, agentID string) delegation {
	t.Helper()
	ppriv := registerPrincipal(t, svc, principalID)
	apub, apriv := newKeypair(t)
	if err := svc.RegisterAgentKey(context.Background(), agentID, apub, "ed25519"); err != nil {
		t.Fatalf("register agent key: %v", err)
	}
	return delegation{principalPriv: ppriv, agentPub: apub, agentPriv: apriv}
}

// cred mints a credential bound to the agent's confirmation key.
func (d delegation) cred(t *testing.T, c principal.Credential) []byte {
	t.Helper()
	c.AgentKey = d.agentPub
	return mintCredential(t, d.principalPriv, c)
}

// submitOpts returns the options a credentialed submission needs: the
// credential plus the position signature that proves control of its key.
func (d delegation) submitOpts(t *testing.T, c []byte, agentID, delibID string, round int, content string) []deliberation.PositionOption {
	t.Helper()
	return []deliberation.PositionOption{
		deliberation.WithPrincipalCredential(c),
		deliberation.WithSignature(signPosition(t, d.agentPriv, agentID, delibID, round, content)),
	}
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
	del := newDelegation(t, svc, delegPrincipal, delegAgent)

	d, err := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithPrincipalPolicy("required"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "hello"
	cred := del.cred(t, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})
	opts := append(del.submitOpts(t, cred, delegAgent, d.ID, d.Round, content),
		deliberation.WithOnBehalfOf(delegPrincipal))

	p, err := svc.SubmitPosition(ctx, d.ID, delegAgent, content, opts...)
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
	del := newDelegation(t, svc, delegPrincipal, delegAgent)

	// Signed by a key that is not the principal's registered one.
	_, attackerPriv := newKeypair(t)
	cred := mintCredential(t, attackerPriv, principal.Credential{
		Principal: delegPrincipal, Agent: delegAgent, AgentKey: del.agentPub,
	})

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "hello"
	opts := append(del.submitOpts(t, cred, delegAgent, d.ID, d.Round, content),
		deliberation.WithOnBehalfOf(delegPrincipal))

	if _, err := svc.SubmitPosition(ctx, d.ID, delegAgent, content, opts...); err == nil {
		t.Fatal("forged credential accepted under policy=none, want rejection")
	} else if !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", err)
	}
}

// A different agent presenting the credential under its own name is rejected by
// the name binding.
func TestPrincipalDelegation_RejectsReplayByOtherAgent(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	del := newDelegation(t, svc, delegPrincipal, delegAgent)
	cred := del.cred(t, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

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

// The name binding alone is not authentication: an attacker can simply claim
// the agent_id the credential names. Proof-of-possession is what actually stops
// a leaked credential, so a presenter that cannot sign with the confirmation
// key must be refused even though every name in the credential matches.
func TestPrincipalDelegation_RejectsPresenterWithoutTheKey(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	del := newDelegation(t, svc, delegPrincipal, delegAgent)
	cred := del.cred(t, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "I claim to be alice-agent"

	// No signature at all — possession never demonstrated.
	_, err = svc.SubmitPosition(ctx, d.ID, delegAgent, content,
		deliberation.WithOnBehalfOf(delegPrincipal),
		deliberation.WithPrincipalCredential(cred))
	if err == nil {
		t.Fatal("unsigned credentialed submission accepted, want rejection")
	}
	if !strings.Contains(err.Error(), "requires a signed position") {
		t.Errorf("error = %q, want it to demand a signature", err)
	}

	// Signed, but with a key that is not the credential's confirmation key.
	// This is the cross-tenant replay shape: right names, wrong keyholder.
	_, wrongPriv := newKeypair(t)
	_, err = svc.SubmitPosition(ctx, d.ID, delegAgent, content,
		deliberation.WithOnBehalfOf(delegPrincipal),
		deliberation.WithPrincipalCredential(cred),
		deliberation.WithSignature(signPosition(t, wrongPriv, delegAgent, d.ID, d.Round, content)))
	if err == nil {
		t.Fatal("credentialed submission signed by the wrong key accepted, want rejection")
	}
	if !strings.Contains(err.Error(), "SIGNATURE_VERIFY_FAIL") && !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want a verification failure", err)
	}
}

// A credential whose principal disagrees with on_behalf_of must be rejected,
// so a real credential for one principal cannot launder a claim about another.
func TestPrincipalDelegation_RejectsPrincipalMismatch(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	del := newDelegation(t, svc, delegPrincipal, delegAgent)
	cred := del.cred(t, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "hello"
	opts := append(del.submitOpts(t, cred, delegAgent, d.ID, d.Round, content),
		deliberation.WithOnBehalfOf("human:bob"))

	if _, err := svc.SubmitPosition(ctx, d.ID, delegAgent, content, opts...); err == nil {
		t.Fatal("principal/on_behalf_of mismatch accepted, want rejection")
	} else if !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", err)
	}
}

// A deliberation-scoped credential must not travel to a different deliberation.
func TestPrincipalDelegation_ScopeConfinesToDeliberation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	del := newDelegation(t, svc, delegPrincipal, delegAgent)

	scoped, err := svc.CreateDeliberation(ctx, "Scoped", "")
	if err != nil {
		t.Fatalf("create scoped: %v", err)
	}
	other, err := svc.CreateDeliberation(ctx, "Other", "")
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	cred := del.cred(t, principal.Credential{
		Principal: delegPrincipal,
		Agent:     delegAgent,
		Scope:     principal.ScopeDeliberationPrefix + scoped.ID,
	})

	const inScope = "in scope"
	opts := append(del.submitOpts(t, cred, delegAgent, scoped.ID, scoped.Round, inScope),
		deliberation.WithOnBehalfOf(delegPrincipal))
	if _, err := svc.SubmitPosition(ctx, scoped.ID, delegAgent, inScope, opts...); err != nil {
		t.Fatalf("submit inside scope: %v", err)
	}

	const outOfScope = "out of scope"
	optsOut := append(del.submitOpts(t, cred, delegAgent, other.ID, other.Round, outOfScope),
		deliberation.WithOnBehalfOf(delegPrincipal))
	if _, err := svc.SubmitPosition(ctx, other.ID, delegAgent, outOfScope, optsOut...); err == nil {
		t.Fatal("scoped credential accepted in another deliberation, want rejection")
	} else if !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", err)
	}
}

// Revoking the principal's key is the revocation path. Credentials already
// minted and not yet expired must stop working immediately.
func TestPrincipalDelegation_KeyRevocationInvalidatesCredential(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	del := newDelegation(t, svc, delegPrincipal, delegAgent)
	cred := del.cred(t, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

	d, err := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithPrincipalPolicy("required"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const before = "before revocation"
	optsBefore := append(del.submitOpts(t, cred, delegAgent, d.ID, d.Round, before),
		deliberation.WithOnBehalfOf(delegPrincipal))
	if _, err := svc.SubmitPosition(ctx, d.ID, delegAgent, before, optsBefore...); err != nil {
		t.Fatalf("submit before revocation: %v", err)
	}

	if err := svc.RevokeAgentKey(ctx, delegPrincipal); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	const after = "after revocation"
	optsAfter := append(del.submitOpts(t, cred, delegAgent, d.ID, d.Round, after),
		deliberation.WithOnBehalfOf(delegPrincipal))
	if _, err := svc.SubmitPosition(ctx, d.ID, delegAgent, after, optsAfter...); err == nil {
		t.Fatal("credential accepted after principal key revocation, want rejection")
	} else if !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", err)
	}
}

func TestPrincipalDelegation_RejectsExpiredCredential(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	del := newDelegation(t, svc, delegPrincipal, delegAgent)
	cred := del.cred(t, principal.Credential{
		Principal: delegPrincipal,
		Agent:     delegAgent,
		ExpiresAt: time.Now().Add(-time.Minute),
	})

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "hello"
	opts := append(del.submitOpts(t, cred, delegAgent, d.ID, d.Round, content),
		deliberation.WithOnBehalfOf(delegPrincipal))

	if _, err := svc.SubmitPosition(ctx, d.ID, delegAgent, content, opts...); err == nil {
		t.Fatal("expired credential accepted, want rejection")
	} else if !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", err)
	}
}

// A verified credential is stronger evidence than the free-text field, so it
// may fill an empty on_behalf_of rather than requiring the agent to restate it.
func TestPrincipalDelegation_CredentialFillsEmptyOnBehalfOf(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	del := newDelegation(t, svc, delegPrincipal, delegAgent)
	cred := del.cred(t, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "hello"
	p, err := svc.SubmitPosition(ctx, d.ID, delegAgent, content,
		del.submitOpts(t, cred, delegAgent, d.ID, d.Round, content)...)
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
	del := newDelegation(t, svc, delegPrincipal, delegAgent)
	cred := del.cred(t, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "hello"
	opts := append(del.submitOpts(t, cred, delegAgent, d.ID, d.Round, content),
		deliberation.WithOnBehalfOf(delegPrincipal))
	if _, err := svc.SubmitPosition(ctx, d.ID, delegAgent, content, opts...); err != nil {
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
	if !bytes.Equal(stored.AgentKey, del.agentPub) {
		t.Error("stored credential lost its confirmation key")
	}
}

// stubVerifier records that it was consulted and returns a fixed outcome.
type stubVerifier struct {
	calls    int
	err      error
	agentKey ed25519.PublicKey
}

func (s *stubVerifier) Verify(_ context.Context, cred *principal.Credential, agentID string, _ principal.Target) (*principal.Result, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &principal.Result{
		Principal: cred.Principal,
		Agent:     agentID,
		AgentKey:  s.agentKey,
		Issuer:    "stub",
	}, nil
}

// SetPrincipalVerifier is advertised as the HCP integration point, so there has
// to be evidence that an injected verifier actually reaches SubmitPosition. The
// credential here carries a garbage principal signature from an unregistered
// principal — the local verifier would reject it several ways over — so
// acceptance can only mean the stub was used.
func TestPrincipalDelegation_CustomVerifierIsConsulted(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	agentPub, agentPriv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, delegAgent, agentPub, "ed25519"); err != nil {
		t.Fatalf("register agent key: %v", err)
	}
	stub := &stubVerifier{agentKey: agentPub}
	svc.SetPrincipalVerifier(stub)

	garbage, err := json.Marshal(principal.Credential{
		Principal: "hcp:did:example:123",
		Agent:     delegAgent,
		AgentKey:  agentPub,
		Issuer:    "hcp",
		ExpiresAt: time.Now().Add(time.Hour),
		Signature: []byte{0x00, 0x01, 0x02},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	d, err := svc.CreateDeliberation(ctx, "Custom verifier", "", deliberation.WithPrincipalPolicy("required"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "delegated via an external authority"
	p, err := svc.SubmitPosition(ctx, d.ID, delegAgent, content,
		deliberation.WithOnBehalfOf("hcp:did:example:123"),
		deliberation.WithPrincipalCredential(garbage),
		deliberation.WithSignature(signPosition(t, agentPriv, delegAgent, d.ID, d.Round, content)))
	if err != nil {
		t.Fatalf("submit with custom verifier: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("verifier consulted %d times, want 1", stub.calls)
	}
	if !p.PrincipalVerified {
		t.Error("PrincipalVerified = false after the custom verifier accepted")
	}
}

// Proof-of-possession lives outside Verifier precisely so an external authority
// cannot waive it. A permissive verifier that vouches for a key the presenter
// does not control must still be refused.
func TestPrincipalDelegation_CustomVerifierCannotWaivePossession(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	agentPub, agentPriv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, delegAgent, agentPub, "ed25519"); err != nil {
		t.Fatalf("register agent key: %v", err)
	}
	// The verifier vouches for a key nobody registered for this agent.
	strangerPub, _ := newKeypair(t)
	svc.SetPrincipalVerifier(&stubVerifier{agentKey: strangerPub})

	cred, err := json.Marshal(principal.Credential{
		Principal: delegPrincipal, Agent: delegAgent, AgentKey: strangerPub,
		ExpiresAt: time.Now().Add(time.Hour), Signature: []byte{0x01},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	d, err := svc.CreateDeliberation(ctx, "No waiving PoP", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "vouched for by a permissive authority"
	_, err = svc.SubmitPosition(ctx, d.ID, delegAgent, content,
		deliberation.WithOnBehalfOf(delegPrincipal),
		deliberation.WithPrincipalCredential(cred),
		deliberation.WithSignature(signPosition(t, agentPriv, delegAgent, d.ID, d.Round, content)))
	if err == nil {
		t.Fatal("external verifier waived proof-of-possession, want rejection")
	}
	if !strings.Contains(err.Error(), "bound to a different key") {
		t.Errorf("error = %q, want a confirmation-key mismatch", err)
	}
}

// A rejection from the injected verifier must stop the submission, not be
// swallowed or retried against the local verifier.
func TestPrincipalDelegation_CustomVerifierRejectionPropagates(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	del := newDelegation(t, svc, delegPrincipal, delegAgent)

	stub := &stubVerifier{err: errors.New("external authority says no")}
	svc.SetPrincipalVerifier(stub)

	cred := del.cred(t, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})
	d, err := svc.CreateDeliberation(ctx, "Custom verifier rejects", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "hello"
	opts := append(del.submitOpts(t, cred, delegAgent, d.ID, d.Round, content),
		deliberation.WithOnBehalfOf(delegPrincipal))

	if _, err := svc.SubmitPosition(ctx, d.ID, delegAgent, content, opts...); err == nil {
		t.Fatal("submission succeeded despite the custom verifier rejecting")
	} else if !strings.Contains(err.Error(), "external authority says no") {
		t.Errorf("error = %q, want the verifier's own reason", err)
	}
	if stub.calls != 1 {
		t.Errorf("verifier consulted %d times, want 1", stub.calls)
	}
}

// Passing nil must restore the local verifier rather than leaving the service
// with no verifier or with the previous custom one still installed.
func TestPrincipalDelegation_NilVerifierRestoresLocal(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	del := newDelegation(t, svc, delegPrincipal, delegAgent)

	svc.SetPrincipalVerifier(&stubVerifier{err: errors.New("should not be consulted")})
	svc.SetPrincipalVerifier(nil)

	cred := del.cred(t, principal.Credential{Principal: delegPrincipal, Agent: delegAgent})
	d, err := svc.CreateDeliberation(ctx, "Nil restores local", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "hello"
	opts := append(del.submitOpts(t, cred, delegAgent, d.ID, d.Round, content),
		deliberation.WithOnBehalfOf(delegPrincipal))

	p, err := svc.SubmitPosition(ctx, d.ID, delegAgent, content, opts...)
	if err != nil {
		t.Fatalf("submit after restoring local verifier: %v", err)
	}
	if !p.PrincipalVerified {
		t.Error("PrincipalVerified = false — local verifier was not restored")
	}
}
