package tests

import (
	"encoding/json"
	"strings"
	"testing"
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
	// Registration goes over the wire with the plain agent_id. The transport
	// namespaces it per API key, so the key lands under the identity the
	// verifier looks up without the caller computing anything. Registering
	// through the service instead would let this test pass against a transport
	// that offers no way to register at all.
	pub, _ := newKeypair(t)
	a2aRegisterKey(t, chain, token, "signing-agent", pub)

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

// A2A serves get_audit_log, whose entries carry BLS proofs. Without
// replica_pubkey on the same transport, an A2A-only client can read those
// proofs but has no way to verify them — leaving it trusting the server's
// report of its own log, which is what the signed log exists to avoid.
func TestA2A_ReplicaPubkeyIsAvailableForAuditVerification(t *testing.T) {
	svc, db := newTestService(t)
	chain, token := a2aChain(t, db, svc)

	resp := a2aCall(t, chain, token, "gemot/admin", map[string]any{
		"action": "replica_pubkey",
	})
	if resp.Error != nil {
		t.Fatalf("replica_pubkey over A2A: %v", resp.Error.Message)
	}
	var got struct {
		PublicKeyHex string `json:"public_key_hex"`
		Algorithm    string `json:"algorithm"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.PublicKeyHex == "" {
		t.Error("public_key_hex is empty — proofs cannot be verified offline")
	}
	if got.Algorithm != "bls12-381-g2" {
		t.Errorf("algorithm = %q, want bls12-381-g2", got.Algorithm)
	}
	_ = svc
}
