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
	err := PaymentRequiredError(cfg, "test charge")
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
	err := PaymentRequiredError(cfg, "test charge")
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
	err1 := PaymentRequiredError(cfg, "first")
	err2 := PaymentRequiredError(cfg, "second")

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
	r, err := VerifyMCPCredential(context.Background(), cfg, nil)
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
	r, err := VerifyMCPCredential(context.Background(), cfg, map[string]any{})
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
	})
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
			})
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
	})
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
	})
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
	})
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
	})
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
	}
	for _, msg := range preStripeFailures {
		if strings.Contains(err.Error(), msg) {
			t.Errorf("string-form round-trip failed pre-Stripe with %q — happy path is broken", msg)
		}
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
	})
	if err == nil {
		t.Fatal("amount-substitution attack should be rejected")
	}
	if !strings.Contains(err.Error(), "invalid challenge ID") {
		t.Errorf("expected HMAC rejection on amount substitution, got: %v", err)
	}
}
