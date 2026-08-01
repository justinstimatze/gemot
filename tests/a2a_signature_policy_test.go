package tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/payments"
)

// signature_policy was reachable only from Go until it gained a create surface:
// every deliberation in production carried the default "none", which made the
// advisory and required modes dead code. An agent could register a key, sign
// for a while, then quietly stop, and nothing would object.
//
// The service-level behaviour of each mode is covered in signature_test.go.
// What these tests pin down is that a transport caller can actually select a
// mode, and that the selected mode reaches enforcement.

func TestA2A_CreateHonorsSignaturePolicy(t *testing.T) {
	svc, db := newTestService(t)
	chain, token := a2aChain(t, db, svc)

	resp := a2aCall(t, chain, token, "gemot/deliberation", map[string]any{
		"action":           "create",
		"topic":            "A2A signature policy",
		"signature_policy": "required",
	})
	if resp.Error != nil {
		t.Fatalf("create: %v", resp.Error.Message)
	}
	var created struct {
		ID              string `json:"deliberation_id"`
		SignaturePolicy string `json:"signature_policy"`
	}
	if err := json.Unmarshal(resp.Result, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.SignaturePolicy != "required" {
		t.Fatalf("signature_policy = %q, want \"required\"", created.SignaturePolicy)
	}

	// The policy must reach enforcement: an agent with a registered key that
	// submits unsigned is exactly the case "required" exists to reject.
	//
	// Hosted mode namespaces agents per API key, so the stored agent_id — and
	// therefore the key-registry entry the verifier looks up — is
	// "<keyID>:<agent_id>". Registering under the unscoped name would leave the
	// verifier finding no key, which reads as "agent never opted into signing"
	// and silently exempts it from the policy.
	pub, _ := newKeypair(t)
	scoped := payments.KeyID(token) + ":signing-agent"
	if err := svc.RegisterAgentKey(context.Background(), scoped, pub, "ed25519"); err != nil {
		t.Fatalf("register key: %v", err)
	}

	unsigned := a2aCall(t, chain, token, "gemot/participate", map[string]any{
		"action":          "submit_position",
		"deliberation_id": created.ID,
		"agent_id":        "signing-agent",
		"content":         "unsigned from a key-holding agent",
	})
	if unsigned.Error == nil {
		t.Fatal("unsigned position from a key-holding agent accepted under policy=required")
	}
	if !strings.Contains(unsigned.Error.Message, "signature required") {
		t.Errorf("error = %q, want it to mention the missing signature", unsigned.Error.Message)
	}
}

// An agent with no registered key must still be able to participate under
// "required" — the policy constrains agents that have opted into signing, not
// everyone. Guards against the surface turning a tightening knob into a lockout.
func TestA2A_SignaturePolicyRequiredAllowsKeylessAgent(t *testing.T) {
	svc, db := newTestService(t)
	chain, token := a2aChain(t, db, svc)

	resp := a2aCall(t, chain, token, "gemot/deliberation", map[string]any{
		"action":           "create",
		"topic":            "A2A signature policy keyless",
		"signature_policy": "required",
	})
	if resp.Error != nil {
		t.Fatalf("create: %v", resp.Error.Message)
	}
	var created struct {
		ID string `json:"deliberation_id"`
	}
	if err := json.Unmarshal(resp.Result, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	keyless := a2aCall(t, chain, token, "gemot/participate", map[string]any{
		"action":          "submit_position",
		"deliberation_id": created.ID,
		"agent_id":        "keyless-agent",
		"content":         "no key registered, so nothing to sign with",
	})
	if keyless.Error != nil {
		t.Fatalf("keyless agent rejected under policy=required: %v", keyless.Error.Message)
	}
}

// An unknown policy string must normalize to "none" rather than reaching the
// DB's CHECK constraint or falling through the verify switch as an unhandled
// value. Mirrors TestSignaturePolicy_UnknownValueNormalizesToNone at the
// transport boundary, where untrusted input actually arrives.
func TestA2A_CreateNormalizesUnknownSignaturePolicy(t *testing.T) {
	svc, db := newTestService(t)
	chain, token := a2aChain(t, db, svc)

	resp := a2aCall(t, chain, token, "gemot/deliberation", map[string]any{
		"action":           "create",
		"topic":            "A2A bogus signature policy",
		"signature_policy": "definitely-not-a-policy",
	})
	if resp.Error != nil {
		t.Fatalf("create: %v", resp.Error.Message)
	}
	var created struct {
		SignaturePolicy string `json:"signature_policy"`
	}
	if err := json.Unmarshal(resp.Result, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.SignaturePolicy != "none" {
		t.Fatalf("signature_policy = %q, want \"none\" (unknown values must fail closed)", created.SignaturePolicy)
	}
}
