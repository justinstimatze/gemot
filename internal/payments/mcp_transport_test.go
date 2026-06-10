package payments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPaymentRequiredError_WithProfileID(t *testing.T) {
	cfg := testCfg()
	err := PaymentRequiredError(cfg, testScope(), "test charge")
	if err == nil {
		t.Fatal("expected non-nil jsonrpc.Error")
	}
	if err.Code != MPPErrorCode {
		t.Errorf("Code = %d, want %d (-32042)", err.Code, MPPErrorCode)
	}
	if err.Message != "Payment Required" {
		t.Errorf("Message = %q, want %q", err.Message, "Payment Required")
	}

	// Decode the Data field and assert shape.
	var data map[string]any
	if jsonErr := json.Unmarshal(err.Data, &data); jsonErr != nil {
		t.Fatalf("error.Data should be valid JSON: %v", jsonErr)
	}
	if got := data["httpStatus"]; got != float64(402) {
		t.Errorf("httpStatus = %v, want 402", got)
	}
	if got := data["title"]; got != "Payment Required" {
		t.Errorf("title = %v, want Payment Required", got)
	}
	challenges, ok := data["challenges"].([]any)
	if !ok {
		t.Fatalf("challenges should be an array, got %T", data["challenges"])
	}
	if len(challenges) != 1 {
		t.Fatalf("expected 1 challenge (stripe SPT), got %d", len(challenges))
	}
	chal := challenges[0].(map[string]any)
	if chal["method"] != "stripe" {
		t.Errorf("challenge method = %v, want stripe", chal["method"])
	}
	if chal["intent"] != "charge" {
		t.Errorf("challenge intent = %v, want charge", chal["intent"])
	}
	if chal["realm"] != cfg.Realm {
		t.Errorf("challenge realm = %v, want %v", chal["realm"], cfg.Realm)
	}
	// Verify the embedded request encodes networkId.
	reqB64 := chal["request"].(string)
	reqJSON, decErr := base64.RawURLEncoding.DecodeString(reqB64)
	if decErr != nil {
		t.Fatalf("challenge request should be base64url-decodable: %v", decErr)
	}
	var req map[string]any
	if jsonErr := json.Unmarshal(reqJSON, &req); jsonErr != nil {
		t.Fatalf("decoded request should be JSON: %v", jsonErr)
	}
	if req["networkId"] != cfg.StripeProfileID {
		t.Errorf("networkId in request = %v, want %v", req["networkId"], cfg.StripeProfileID)
	}
}

func TestPaymentRequiredError_NoProfileID(t *testing.T) {
	cfg := testCfg()
	cfg.StripeProfileID = ""
	err := PaymentRequiredError(cfg, testScope(), "test charge")
	if err == nil {
		t.Fatal("expected non-nil jsonrpc.Error")
	}
	var data map[string]any
	if jsonErr := json.Unmarshal(err.Data, &data); jsonErr != nil {
		t.Fatalf("error.Data should be valid JSON: %v", jsonErr)
	}
	challenges, _ := data["challenges"].([]any)
	if len(challenges) != 0 {
		t.Errorf("expected 0 challenges when no method is configured, got %d", len(challenges))
	}
}

func TestPaymentRequiredError_ChallengeIDHMACBound(t *testing.T) {
	cfg := testCfg()
	err1 := PaymentRequiredError(cfg, testScope(), "first")
	err2 := PaymentRequiredError(cfg, testScope(), "second")

	id1 := extractChallengeID(t, err1)
	id2 := extractChallengeID(t, err2)
	// Different descriptions go into the request body, so the HMAC-bound
	// IDs must differ — defense against an attacker substituting a
	// challenge from a different paid action.
	if id1 == id2 {
		t.Error("challenge IDs should differ for different request bodies (HMAC bind)")
	}
}

func extractChallengeID(t *testing.T, jrErr interface{}) string {
	t.Helper()
	type errLike interface {
		Error() string
	}
	_ = jrErr.(errLike) // sanity
	// Re-marshal via the Data field — the simplest path that avoids
	// cross-package type wrangling in tests.
	raw, _ := json.Marshal(jrErr)
	var wrapper struct {
		Data struct {
			Challenges []struct {
				ID string `json:"id"`
			} `json:"challenges"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if len(wrapper.Data.Challenges) == 0 {
		t.Fatal("no challenges in error")
	}
	return wrapper.Data.Challenges[0].ID
}

func TestVerifyMCPCredential_NilMeta(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	r, err := VerifyMCPCredential(context.Background(), cfg, nil, testScope())
	if err != nil {
		t.Errorf("nil meta should not error, got %v", err)
	}
	if r != nil {
		t.Errorf("nil meta should return nil receipt, got %+v", r)
	}
}

func TestVerifyMCPCredential_EmptyMeta(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	r, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{}, testScope())
	if err != nil {
		t.Errorf("empty meta should not error, got %v", err)
	}
	if r != nil {
		t.Errorf("empty meta should return nil receipt, got %+v", r)
	}
}

func TestVerifyMCPCredential_NoCredentialKey(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	r, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{
		"some.other/key": "irrelevant",
	}, testScope())
	if err != nil {
		t.Errorf("meta without credential key should not error, got %v", err)
	}
	if r != nil {
		t.Errorf("meta without credential key should return nil receipt, got %+v", r)
	}
}

func TestVerifyMCPCredential_WrongType(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	cases := []struct {
		name string
		val  any
	}{
		{"number", 42},
		{"float", 3.14},
		{"array", []any{"a", "b"}},
		{"bool", true},
		{"nil", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{
				MetaCredentialKey: tc.val,
			}, testScope())
			if err == nil {
				t.Errorf("expected error for credential value of type %s, got nil", tc.name)
			}
		})
	}
}

func TestVerifyMCPCredential_MalformedString(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	_, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{
		MetaCredentialKey: "!!!not-valid-base64!!!",
	}, testScope())
	if err == nil {
		t.Fatal("expected error for malformed base64 credential string")
	}
}

func TestVerifyMCPCredential_TamperedViaObject(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	// Tamper realm AFTER building the (valid) challenge — must reject.
	chal.Realm = "attacker.dev"
	// Send as object form so the test exercises the map[string]any branch.
	credObj := map[string]any{
		"challenge": chal,
		"source":    "did:pkh:eip155:4217:0xtest",
		"payload":   validPayload(),
	}
	_, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{
		MetaCredentialKey: credObj,
	}, testScope())
	if err == nil {
		t.Fatal("expected error for tampered credential delivered as object")
	}
}

// TestVerifyMCPCredential_ObjectFormParsesEquivalently confirms the object
// branch reaches the same validation path as the string branch — a valid
// credential delivered as an object reaches parseAndValidateCredential and
// only fails at the Stripe settle layer (which we don't exercise here).
// The "success" we assert is reaching the post-validation SPT extraction
// without an HMAC/realm/expiry error.
func TestVerifyMCPCredential_ObjectFormReachesSettlement(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	credObj := map[string]any{
		"challenge": chal,
		"source":    "did:pkh:eip155:4217:0xtest",
		"payload":   validPayload(),
	}
	_, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{
		MetaCredentialKey: credObj,
	}, testScope())
	// Stripe call will fail (no API key, no test backend), but the error
	// must be a stripe error, NOT one of our pre-Stripe rejection errors.
	if err == nil {
		// Unexpected — would mean Stripe actually succeeded somehow. Not a
		// failure of the test, but flag it.
		t.Log("Stripe call unexpectedly succeeded — likely test env has STRIPE_SECRET_KEY set")
		return
	}
	preStripeFailures := []string{
		"invalid challenge ID",
		"realm mismatch",
		"expired",
		"unsupported payment method",
		"malformed credential",
		"invalid credential JSON",
		"missing spt",
		"replay",
	}
	for _, msg := range preStripeFailures {
		if strings.Contains(err.Error(), msg) {
			t.Errorf("object-form credential failed pre-Stripe validation with %q — object branch is broken", msg)
		}
	}
}

func TestReceiptMeta_Shape(t *testing.T) {
	r := &Receipt{
		Status:    "success",
		Method:    "stripe",
		Timestamp: "2026-06-09T12:00:00Z",
		Reference: "pi_test_abc123",
	}
	m := ReceiptMeta(r)
	got, ok := m[MetaReceiptKey]
	if !ok {
		t.Fatalf("ReceiptMeta missing key %q", MetaReceiptKey)
	}
	if got != r {
		t.Errorf("ReceiptMeta should store the receipt under %q", MetaReceiptKey)
	}
}

// TestVerifyMCPCredential_RoundTrip exercises the full string-form path:
// build a challenge → encode as agent would → verify reaches Stripe (and
// fails there, since we don't have a test backend). What we assert: no
// pre-Stripe rejection.
func TestVerifyMCPCredential_RoundTripReachesSettlement(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	_, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{
		MetaCredentialKey: credB64,
	}, testScope())
	if err == nil {
		t.Log("Stripe call unexpectedly succeeded — test env likely has STRIPE_SECRET_KEY set")
		return
	}
	preStripeFailures := []string{
		"invalid challenge ID",
		"realm mismatch",
		"expired",
		"unsupported payment method",
		"malformed credential",
		"invalid credential JSON",
		"missing spt",
		"replay",
		"scope mismatch",
	}
	for _, msg := range preStripeFailures {
		if strings.Contains(err.Error(), msg) {
			t.Errorf("string-form round-trip failed pre-Stripe with %q — happy path is broken", msg)
		}
	}
}

// TestPaymentRequiredError_PerModelAmount verifies that the challenge
// emitted by PaymentRequiredError carries the per-model price in its
// request body. This is the foundation of per-model billing: a Haiku
// challenge advertises Haiku's price (10¢), Sonnet 30¢, Opus 150¢.
func TestPaymentRequiredError_PerModelAmount(t *testing.T) {
	cfg := testCfg()
	cases := []struct {
		model      string
		wantAmount string
	}{
		// Haiku (10¢ credit-equivalent) and Sonnet (30¢) get floored to
		// the 50¢ Stripe SPT minimum — sub-50¢ charges are rejected at
		// Stripe settlement. Opus (150¢) is above the floor, exact price.
		{"claude-haiku-4-5", "50"},
		{"claude-sonnet-4-6", "50"},
		{"claude-opus-4-6", "150"},
		// Empty model is defensively resolved inside PaymentRequiredError
		// to "claude-sonnet-4-6" — credential must always be model-bound.
		{"", "50"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			scope := testScope()
			scope.Model = tc.model
			err := PaymentRequiredError(cfg, scope, "test")
			id := extractChallengeID(t, err)
			_ = id
			// Decode the challenge to confirm amount
			raw, _ := json.Marshal(err)
			var wrapper struct {
				Data struct {
					Challenges []struct {
						Request string `json:"request"`
					} `json:"challenges"`
				} `json:"data"`
			}
			if err := json.Unmarshal(raw, &wrapper); err != nil {
				t.Fatalf("re-marshal: %v", err)
			}
			reqJSON, decErr := base64.RawURLEncoding.DecodeString(wrapper.Data.Challenges[0].Request)
			if decErr != nil {
				t.Fatalf("decode request: %v", decErr)
			}
			var reqMap map[string]any
			if err := json.Unmarshal(reqJSON, &reqMap); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			if reqMap["amount"] != tc.wantAmount {
				t.Errorf("model %s: got amount %v, want %v", tc.model, reqMap["amount"], tc.wantAmount)
			}
			// Scope should also be encoded
			scopeMap, ok := reqMap["scope"].(map[string]any)
			if !ok {
				t.Fatalf("request body missing scope sub-object")
			}
			// Empty input model is defensively resolved to Sonnet.
			wantModel := tc.model
			if wantModel == "" {
				wantModel = "claude-sonnet-4-6"
			}
			if scopeMap["model"] != wantModel {
				t.Errorf("scope.model = %v, want %v", scopeMap["model"], wantModel)
			}
		})
	}
}

// TestVerifyMCPCredential_ScopeMismatchTool — credential bound to one tool,
// caller redeems against a different tool. Must reject before Stripe.
func TestVerifyMCPCredential_ScopeMismatchTool(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	credScope := testScope() // tool=analyze
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBodyWithScope(cfg, credScope), 5*time.Minute)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	// Caller expects tool=deliberation (different tool — attempt at reuse)
	expected := credScope
	expected.Tool = "deliberation"
	_, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{MetaCredentialKey: credB64}, expected)
	if err == nil {
		t.Fatal("expected scope-mismatch rejection on tool")
	}
	if !strings.Contains(err.Error(), "scope mismatch") {
		t.Errorf("expected scope mismatch error, got: %v", err)
	}
}

func TestVerifyMCPCredential_ScopeMismatchAction(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	credScope := testScope() // action=run
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBodyWithScope(cfg, credScope), 5*time.Minute)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	expected := credScope
	expected.Action = "expert_panel" // cross-action reuse attempt
	_, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{MetaCredentialKey: credB64}, expected)
	if err == nil {
		t.Fatal("expected scope-mismatch rejection on action")
	}
	if !strings.Contains(err.Error(), "scope mismatch") {
		t.Errorf("expected scope mismatch error, got: %v", err)
	}
}

func TestVerifyMCPCredential_ScopeMismatchModel(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	credScope := testScope() // model=sonnet
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBodyWithScope(cfg, credScope), 5*time.Minute)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	expected := credScope
	expected.Model = "claude-opus-4-6" // model-upgrade attack: pay Sonnet, get Opus
	_, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{MetaCredentialKey: credB64}, expected)
	if err == nil {
		t.Fatal("expected scope-mismatch rejection on model — this is the model-upgrade attack")
	}
	if !strings.Contains(err.Error(), "scope mismatch") {
		t.Errorf("expected scope mismatch error, got: %v", err)
	}
}

func TestVerifyMCPCredential_ScopeMismatchDeliberation(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	credScope := testScope() // deliberation_id=test-delib-12345
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBodyWithScope(cfg, credScope), 5*time.Minute)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	expected := credScope
	expected.DeliberationID = "different-delib-99999"
	_, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{MetaCredentialKey: credB64}, expected)
	if err == nil {
		t.Fatal("expected scope-mismatch rejection on deliberation_id")
	}
	if !strings.Contains(err.Error(), "scope mismatch") {
		t.Errorf("expected scope mismatch error, got: %v", err)
	}
}

// TestChallengeScope_Matches_EmptyFieldsSkipChecks documents that an empty
// field in the CREDENTIAL's scope means "unbound on this dimension" — the
// caller's actual value for that field is accepted without comparison. Used
// when a dimension isn't knowable at challenge-issue time.
func TestChallengeScope_Matches_EmptyFieldsSkipChecks(t *testing.T) {
	// Credential scope with empty model — caller can use any model.
	cred := ChallengeScope{Tool: "analyze", Action: "run"}
	actual := ChallengeScope{Tool: "analyze", Action: "run", Model: "claude-opus-4-6", DeliberationID: "x"}
	if err := cred.Matches(actual); err != nil {
		t.Errorf("empty fields in credential scope should skip check, got: %v", err)
	}

	// Credential scope with non-empty tool but caller's tool is empty — REJECT.
	cred2 := ChallengeScope{Tool: "analyze"}
	actual2 := ChallengeScope{Tool: ""}
	if err := cred2.Matches(actual2); err == nil {
		t.Error("non-empty cred.Tool vs empty actual.Tool should reject")
	}
}

// TestVerifyMCPCredential_AmountSubstitution proves that even if an attacker
// crafts a credential claiming a tiny amount, our server-side amount is
// what gets charged. The amount field lives inside the HMAC-bound request,
// so changing it invalidates the HMAC. This test confirms the rejection.
func TestVerifyMCPCredential_AmountSubstitution(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	// Build with the legitimate amount...
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	// ...then swap the request bytes for a 1-cent charge while keeping the
	// original challenge ID. HMAC must reject.
	cheapReq, _ := json.Marshal(map[string]any{
		"amount":    "1",
		"currency":  "usd",
		"networkId": cfg.StripeProfileID,
	})
	chal.Request = base64.RawURLEncoding.EncodeToString(cheapReq)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	_, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{
		MetaCredentialKey: credB64,
	}, testScope())
	if err == nil {
		t.Fatal("amount-substitution attack should be rejected")
	}
	if !strings.Contains(err.Error(), "invalid challenge ID") {
		t.Errorf("expected HMAC rejection on amount substitution, got: %v", err)
	}
}
