package tests

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/auth"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

// signed-submission integration tests verify the interaction between
// agent_keys persistence, signature_policy enforcement, and the
// auth-package canonicalization. These tests require Postgres (see tempDB).

func signPosition(t *testing.T, priv ed25519.PrivateKey, agentID, delibID string, round int, content string) []byte {
	t.Helper()
	return ed25519.Sign(priv, auth.PositionPayload(agentID, delibID, round, content))
}

func signVote(t *testing.T, priv ed25519.PrivateKey, agentID, delibID, positionID string, value int, qualifier, caveat, criterion string) []byte {
	t.Helper()
	return ed25519.Sign(priv, auth.VotePayload(agentID, delibID, positionID, value, qualifier, caveat, criterion))
}

func newKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return pub, priv
}

func TestSignaturePolicy_NoneAcceptsUnsigned(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, err := svc.CreateDeliberation(ctx, "Test", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if d.SignaturePolicy != "none" {
		t.Fatalf("want default policy 'none', got %q", d.SignaturePolicy)
	}

	// No key registered; policy none. Unsigned position must succeed.
	if _, err := svc.SubmitPosition(ctx, d.ID, "alice", "hello"); err != nil {
		t.Fatalf("submit unsigned: %v", err)
	}
}

func TestSignaturePolicy_ValidSignatureAccepted(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, err := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithSignaturePolicy("advisory"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register key: %v", err)
	}

	content := "my signed position"
	sig := signPosition(t, priv, "alice", d.ID, d.Round, content)
	p, err := svc.SubmitPosition(ctx, d.ID, "alice", content, deliberation.WithSignature(sig))
	if err != nil {
		t.Fatalf("submit signed: %v", err)
	}
	if len(p.Signature) == 0 {
		t.Fatal("signature should have been persisted on the returned position")
	}
}

func TestSignaturePolicy_BadSignatureRejected(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, _ := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithSignaturePolicy("advisory"))

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Sign the correct payload, then tamper with the content on submit.
	sig := signPosition(t, priv, "alice", d.ID, d.Round, "original content")
	_, err := svc.SubmitPosition(ctx, d.ID, "alice", "tampered content", deliberation.WithSignature(sig))
	if err == nil || !strings.Contains(err.Error(), "SIGNATURE_VERIFY_FAIL") {
		t.Fatalf("want SIGNATURE_VERIFY_FAIL, got %v", err)
	}
}

func TestSignaturePolicy_AdvisoryAcceptsUnsignedWhenKeyRegistered(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, _ := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithSignaturePolicy("advisory"))

	pub, _ := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Advisory policy: unsigned submission from an agent with a registered key
	// is accepted (with an audit warning); the submission should still succeed.
	if _, err := svc.SubmitPosition(ctx, d.ID, "alice", "unsigned but has key"); err != nil {
		t.Fatalf("advisory mode should accept unsigned, got: %v", err)
	}
}

func TestSignaturePolicy_RequiredRejectsUnsignedWhenKeyRegistered(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, _ := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithSignaturePolicy("required"))

	pub, _ := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err := svc.SubmitPosition(ctx, d.ID, "alice", "unsigned")
	if err == nil || !strings.Contains(err.Error(), "signature required") {
		t.Fatalf("required policy must reject unsigned, got %v", err)
	}
}

func TestSignaturePolicy_RequiredAllowsAgentsWithoutRegisteredKey(t *testing.T) {
	// If no key is registered for an agent, there's nothing to verify against —
	// even in required mode, the submission proceeds. Federation will gate access
	// at a different boundary (registration). This test pins the current behavior.
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, _ := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithSignaturePolicy("required"))

	if _, err := svc.SubmitPosition(ctx, d.ID, "newcomer", "unsigned"); err != nil {
		t.Fatalf("required policy should accept agents without a registered key (nothing to verify), got: %v", err)
	}
}

func TestSignaturePolicy_SignatureWithoutRegisteredKeyRejected(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, _ := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithSignaturePolicy("advisory"))

	_, priv := newKeypair(t)
	sig := signPosition(t, priv, "alice", d.ID, d.Round, "content")

	_, err := svc.SubmitPosition(ctx, d.ID, "alice", "content", deliberation.WithSignature(sig))
	if err == nil || !strings.Contains(err.Error(), "no public key is registered") {
		t.Fatalf("signature with no registered key must be rejected, got %v", err)
	}
}

func TestSignatureRotation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, _ := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithSignaturePolicy("advisory"))

	pub1, priv1 := newKeypair(t)
	pub2, priv2 := newKeypair(t)

	if err := svc.RegisterAgentKey(ctx, "alice", pub1, auth.AlgoEd25519); err != nil {
		t.Fatalf("register 1: %v", err)
	}
	// Rotate: register a new key — should revoke old in same transaction.
	if err := svc.RegisterAgentKey(ctx, "alice", pub2, auth.AlgoEd25519); err != nil {
		t.Fatalf("register 2: %v", err)
	}

	// Old-key signature must fail.
	oldSig := signPosition(t, priv1, "alice", d.ID, d.Round, "hello")
	if _, err := svc.SubmitPosition(ctx, d.ID, "alice", "hello", deliberation.WithSignature(oldSig)); err == nil {
		t.Fatal("old-key signature should fail after rotation")
	}

	// New-key signature must succeed.
	newSig := signPosition(t, priv2, "alice", d.ID, d.Round, "hello2")
	if _, err := svc.SubmitPosition(ctx, d.ID, "alice", "hello2", deliberation.WithSignature(newSig)); err != nil {
		t.Fatalf("new-key signature should succeed: %v", err)
	}
}

func TestSignedPosition_VerifiesAgainstRawContentBeforeSanitization(t *testing.T) {
	// Regression: the service sanitizes position content (PII redaction,
	// TrimSpace). The signature must verify against the raw client content,
	// not the sanitized form — otherwise every signed position with leading
	// whitespace or an email address would fail verification.
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, _ := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithSignaturePolicy("advisory"))
	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Content with trailing whitespace — sanitize.Position trims it.
	rawContent := "I think X because reasons.   "
	sig := signPosition(t, priv, "alice", d.ID, d.Round, rawContent)

	p, err := svc.SubmitPosition(ctx, d.ID, "alice", rawContent, deliberation.WithSignature(sig))
	if err != nil {
		t.Fatalf("submit signed (whitespace): %v", err)
	}
	if p.Content == rawContent {
		t.Fatal("sanitizer should have trimmed trailing whitespace — test premise broken")
	}
	if len(p.Signature) == 0 {
		t.Fatal("signature should be persisted despite content mutation (verified at submit)")
	}
}

func TestSignaturePolicy_UnknownValueNormalizesToNone(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, err := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithSignaturePolicy("requird-typo"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if d.SignaturePolicy != "none" {
		t.Fatalf("unknown policy should normalize to 'none', got %q", d.SignaturePolicy)
	}

	pub, _ := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}
	// With normalized policy "none", unsigned submission must succeed (not reject as required).
	if _, err := svc.SubmitPosition(ctx, d.ID, "alice", "unsigned"); err != nil {
		t.Fatalf("typo policy normalized to none should accept unsigned: %v", err)
	}
}

func TestRevokeAgentKey(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, _ := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithSignaturePolicy("required"))
	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	sig := signPosition(t, priv, "alice", d.ID, d.Round, "before revoke")
	if _, err := svc.SubmitPosition(ctx, d.ID, "alice", "before revoke", deliberation.WithSignature(sig)); err != nil {
		t.Fatalf("pre-revoke signed submit: %v", err)
	}

	if err := svc.RevokeAgentKey(ctx, "alice"); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// After revoke: signature with the revoked key presents as "no key registered"
	// which is rejected because the sig is present but unverifiable.
	sig2 := signPosition(t, priv, "alice", d.ID, d.Round, "after revoke")
	_, err := svc.SubmitPosition(ctx, d.ID, "alice", "after revoke", deliberation.WithSignature(sig2))
	if err == nil || !strings.Contains(err.Error(), "no public key is registered") {
		t.Fatalf("post-revoke signed submit should reject, got %v", err)
	}
}

func TestSignedVote(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, _ := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithSignaturePolicy("advisory"))

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, "alice", pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	// alice and bob submit so alice can vote on bob's position.
	if _, err := svc.SubmitPosition(ctx, d.ID, "alice", "alice pos"); err != nil {
		t.Fatalf("alice pos: %v", err)
	}
	bobPos, err := svc.SubmitPosition(ctx, d.ID, "bob", "bob pos")
	if err != nil {
		t.Fatalf("bob pos: %v", err)
	}

	sig := signVote(t, priv, "alice", d.ID, bobPos.ID, 1, "", "", "")
	if err := svc.SubmitSignedVote(ctx, d.ID, "alice", bobPos.ID, 1, "", "", "", sig); err != nil {
		t.Fatalf("signed vote: %v", err)
	}

	// Bad vote signature: flip one byte.
	badSig := append([]byte{}, sig...)
	badSig[0] ^= 0xFF
	if err := svc.SubmitSignedVote(ctx, d.ID, "alice", bobPos.ID, -1, "", "", "", badSig); err == nil || !strings.Contains(err.Error(), "SIGNATURE_VERIFY_FAIL") {
		t.Fatalf("bad vote sig must fail: %v", err)
	}
}

// TestHostedModeSigningID covers the scopeAgentID interaction with signatures.
// Hosted-mode MCP transports scope agent_ids as "<keyID>:<agentID>" before
// handing off to the service, but the client signs with the unscoped ID it
// knows. SubmitPositionWithSigningID/SubmitSignedVoteWithSigningID decouple
// the stored (scoped) identity from the signed (unscoped) one.
func TestHostedModeSigningID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	d, _ := svc.CreateDeliberation(ctx, "Test", "", deliberation.WithSignaturePolicy("required"))

	// The hosted MCP layer scopes both register_key and submit_position to the
	// same stored agent_id. The client sees only the unscoped form.
	const unscoped = "alice"
	const scoped = "k_abc123:alice"

	pub, priv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, scoped, pub, auth.AlgoEd25519); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Client signs with its unscoped view of its own identity.
	content := "hosted-mode position"
	sig := signPosition(t, priv, unscoped, d.ID, d.Round, content)

	// Happy path: scoped storage ID + unscoped signing ID → verify succeeds.
	p, err := svc.SubmitPositionWithSigningID(ctx, d.ID, scoped, unscoped, content, deliberation.WithSignature(sig))
	if err != nil {
		t.Fatalf("hosted signed position: %v", err)
	}
	if p.AgentID != scoped {
		t.Fatalf("stored agent_id = %q, want %q", p.AgentID, scoped)
	}

	// Regression: the old SubmitPosition path (signingAgentID == scoped agent_id)
	// must now reject this signature, since the client signed with the unscoped form.
	sigScoped := signPosition(t, priv, scoped, d.ID, d.Round, content) // re-sign with scoped to confirm the old path still works when the client does happen to know the scope
	if _, err := svc.SubmitPosition(ctx, d.ID, scoped, content, deliberation.WithSignature(sigScoped)); err != nil {
		t.Fatalf("non-hosted path with matching signing ID: %v", err)
	}
	if _, err := svc.SubmitPosition(ctx, d.ID, scoped, content, deliberation.WithSignature(sig)); err == nil || !strings.Contains(err.Error(), "SIGNATURE_VERIFY_FAIL") {
		t.Fatalf("non-hosted path with unscoped-signed sig must fail, got %v", err)
	}

	// Vote path mirrors the position path.
	bobPos, err := svc.SubmitPosition(ctx, d.ID, "k_abc123:bob", "bob pos")
	if err != nil {
		t.Fatalf("bob pos: %v", err)
	}
	// Wait — bob has no key and policy is "required". That's allowed because
	// required is gated on "agent has a registered key but did not sign."
	// Bob has no key, so he is exempt. Alice votes on bob's position.
	voteSig := signVote(t, priv, unscoped, d.ID, bobPos.ID, 1, "", "", "")
	if err := svc.SubmitSignedVoteWithSigningID(ctx, d.ID, scoped, unscoped, bobPos.ID, 1, "", "", "", voteSig); err != nil {
		t.Fatalf("hosted signed vote: %v", err)
	}
}
