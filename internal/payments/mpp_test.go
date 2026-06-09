package payments

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// testCfg returns a Config suitable for crypto-layer tests. No Stripe
// secrets — tests that reach paymentintent.New are integration territory
// and live elsewhere.
func testCfg() Config {
	return Config{
		HMACSecret:      "test-hmac-secret-do-not-use-in-prod",
		Realm:           "test.gemot.dev",
		PricePerAnalyze: 50,
		Currency:        "usd",
		StripeProfileID: "profile_test_abc123",
	}
}

// buildChallengeForTest constructs a server-signed challenge with the given
// method, intent, request body, and expiry offset from now. Used by every
// crypto test as the baseline a credential is built from.
func buildChallengeForTest(t *testing.T, cfg Config, method, intent string, requestBody map[string]any, expiresIn time.Duration) challenge {
	t.Helper()
	reqJSON, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	requestB64 := base64.RawURLEncoding.EncodeToString(reqJSON)
	expires := time.Now().Add(expiresIn).UTC().Format(time.RFC3339)
	id := generateChallengeID(cfg.HMACSecret, cfg.Realm, method, intent, requestB64, expires)
	return challenge{
		ID:      id,
		Realm:   cfg.Realm,
		Method:  method,
		Intent:  intent,
		Request: requestB64,
		Expires: expires,
	}
}

// buildCredentialForTest packs a challenge + payload into the base64url
// credential format an agent would send back.
func buildCredentialForTest(t *testing.T, chal challenge, payload map[string]any) string {
	t.Helper()
	cred := credential{
		Challenge: chal,
		Source:    "did:pkh:eip155:4217:0xtest",
		Payload:   payload,
	}
	credJSON, err := json.Marshal(cred)
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(credJSON)
}

// validPayload is the minimal payload an SPT credential must carry.
func validPayload() map[string]any {
	return map[string]any{"spt": "spt_test_xyz789"}
}

// validRequestBody is the standard charge request encoded in a challenge.
func validRequestBody(cfg Config) map[string]any {
	return map[string]any{
		"amount":             fmt.Sprintf("%d", cfg.PricePerAnalyze),
		"currency":           cfg.Currency,
		"decimals":           2,
		"description":        "test charge",
		"paymentMethodTypes": []string{"card", "link"},
		"networkId":          cfg.StripeProfileID,
	}
}

// resetReplayCache clears the package-level replay cache so tests don't
// interfere with each other. The cache is a singleton, so test isolation
// requires explicit reset.
func resetReplayCache() {
	usedChallengesMu.Lock()
	defer usedChallengesMu.Unlock()
	usedChallenges = make(map[string]time.Time)
}

func TestParseAndValidateCredential_HappyPath(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	cred, req, err := parseAndValidateCredential(cfg, credB64)
	if err != nil {
		t.Fatalf("expected validation to succeed, got error: %v", err)
	}
	if cred == nil || req == nil {
		t.Fatal("expected non-nil cred and req on success")
	}
	if req.Currency != cfg.Currency {
		t.Errorf("decoded request.Currency = %q, want %q", req.Currency, cfg.Currency)
	}
	if cred.Challenge.Realm != cfg.Realm {
		t.Errorf("cred.Challenge.Realm = %q, want %q", cred.Challenge.Realm, cfg.Realm)
	}
}

func TestParseAndValidateCredential_EmptyHMACSecret(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	cfg.HMACSecret = ""
	chal := buildChallengeForTest(t, testCfg(), "stripe", "charge", validRequestBody(testCfg()), 5*time.Minute)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	_, _, err := parseAndValidateCredential(cfg, credB64)
	if err == nil {
		t.Fatal("expected error when HMACSecret is empty (server misconfiguration)")
	}
	if !strings.Contains(err.Error(), "HMACSecret") {
		t.Errorf("error should mention HMACSecret, got: %v", err)
	}
}

func TestParseAndValidateCredential_MalformedBase64(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	_, _, err := parseAndValidateCredential(cfg, "!!!not-base64!!!")
	if err == nil {
		t.Fatal("expected error for malformed base64 credential")
	}
	if !strings.Contains(err.Error(), "malformed credential") {
		t.Errorf("error should say malformed credential, got: %v", err)
	}
}

func TestParseAndValidateCredential_InvalidJSON(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	// Valid base64 but not valid JSON
	notJSON := base64.RawURLEncoding.EncodeToString([]byte("this is not json"))
	_, _, err := parseAndValidateCredential(cfg, notJSON)
	if err == nil {
		t.Fatal("expected error for credential body that isn't JSON")
	}
	if !strings.Contains(err.Error(), "invalid credential JSON") {
		t.Errorf("error should say invalid credential JSON, got: %v", err)
	}
}

func TestParseAndValidateCredential_UnsupportedMethod(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	// Build a challenge with an unknown method but a valid HMAC bind for that
	// method (so the rejection is on the method whitelist, not the HMAC).
	chal := buildChallengeForTest(t, cfg, "evil_rail", "charge", validRequestBody(cfg), 5*time.Minute)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	_, _, err := parseAndValidateCredential(cfg, credB64)
	if err == nil {
		t.Fatal("expected error for unsupported method")
	}
	if !strings.Contains(err.Error(), "unsupported payment method") {
		t.Errorf("error should mention unsupported method, got: %v", err)
	}
}

func TestParseAndValidateCredential_TamperedChallengeID(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	// Flip a byte in the challenge ID — HMAC bind must reject.
	chal.ID = strings.Repeat("A", len(chal.ID))
	credB64 := buildCredentialForTest(t, chal, validPayload())

	_, _, err := parseAndValidateCredential(cfg, credB64)
	if err == nil {
		t.Fatal("expected error for tampered challenge ID")
	}
	if !strings.Contains(err.Error(), "invalid challenge ID") {
		t.Errorf("error should say invalid challenge ID, got: %v", err)
	}
}

func TestParseAndValidateCredential_TamperedRealm(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	// Change realm after signing — HMAC bind must reject because the recomputed
	// expected ID won't match the original.
	chal.Realm = "attacker.example.com"
	credB64 := buildCredentialForTest(t, chal, validPayload())

	_, _, err := parseAndValidateCredential(cfg, credB64)
	if err == nil {
		t.Fatal("expected error for tampered realm")
	}
}

func TestParseAndValidateCredential_TamperedRequestBytes(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	// Replace the request body with one for a smaller amount but keep the
	// original challenge ID — HMAC bind must reject.
	cheaperReq := map[string]any{"amount": "1", "currency": "usd"}
	reqJSON, _ := json.Marshal(cheaperReq)
	chal.Request = base64.RawURLEncoding.EncodeToString(reqJSON)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	_, _, err := parseAndValidateCredential(cfg, credB64)
	if err == nil {
		t.Fatal("expected error for tampered request bytes (amount substitution attack)")
	}
	if !strings.Contains(err.Error(), "invalid challenge ID") {
		t.Errorf("error should be invalid challenge ID (HMAC mismatch), got: %v", err)
	}
}

func TestParseAndValidateCredential_RealmMismatch(t *testing.T) {
	resetReplayCache()
	// Two configs: one builds a valid credential, the other verifies it.
	// Same HMAC secret (attacker reuses a credential across servers) but
	// different realms — must reject.
	signingCfg := testCfg()
	verifyingCfg := testCfg()
	verifyingCfg.Realm = "production.gemot.dev"

	chal := buildChallengeForTest(t, signingCfg, "stripe", "charge", validRequestBody(signingCfg), 5*time.Minute)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	_, _, err := parseAndValidateCredential(verifyingCfg, credB64)
	if err == nil {
		t.Fatal("expected error for realm mismatch")
	}
	// HMAC bind covers realm — so the first failure will actually be invalid
	// challenge ID (the verifying server recomputes with its own realm).
	// Both messages are acceptable; what matters is that we reject.
}

func TestParseAndValidateCredential_ExpiredChallenge(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	// Expired 1 minute ago.
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), -1*time.Minute)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	_, _, err := parseAndValidateCredential(cfg, credB64)
	if err == nil {
		t.Fatal("expected error for expired challenge")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error should mention expired, got: %v", err)
	}
}

func TestParseAndValidateCredential_MissingExpires(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	// Strip the expires field but DON'T re-sign — the HMAC will catch this.
	// To test the empty-expires rejection specifically, recompute the ID
	// with empty expires so HMAC passes but our empty-check fires.
	chal.Expires = ""
	chal.ID = generateChallengeID(cfg.HMACSecret, cfg.Realm, chal.Method, chal.Intent, chal.Request, "")
	credB64 := buildCredentialForTest(t, chal, validPayload())

	_, _, err := parseAndValidateCredential(cfg, credB64)
	if err == nil {
		t.Fatal("expected error for missing expires field (indefinite credentials defeat replay window)")
	}
	if !strings.Contains(err.Error(), "expires") {
		t.Errorf("error should mention expires, got: %v", err)
	}
}

func TestParseAndValidateCredential_MalformedExpires(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	chal.Expires = "yesterday"
	chal.ID = generateChallengeID(cfg.HMACSecret, cfg.Realm, chal.Method, chal.Intent, chal.Request, "yesterday")
	credB64 := buildCredentialForTest(t, chal, validPayload())

	_, _, err := parseAndValidateCredential(cfg, credB64)
	if err == nil {
		t.Fatal("expected error for malformed expires field")
	}
	if !strings.Contains(err.Error(), "expires") {
		t.Errorf("error should mention expires, got: %v", err)
	}
}

func TestParseAndValidateCredential_Replay(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	// First call succeeds.
	if _, _, err := parseAndValidateCredential(cfg, credB64); err != nil {
		t.Fatalf("first call should succeed, got: %v", err)
	}
	// Second call must reject as replay.
	_, _, err := parseAndValidateCredential(cfg, credB64)
	if err == nil {
		t.Fatal("expected replay rejection on second call")
	}
	if !strings.Contains(err.Error(), "replay") {
		t.Errorf("error should mention replay, got: %v", err)
	}
}

func TestParseAndValidateCredential_ReplayConcurrent(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	// Two goroutines race to redeem the same credential. Exactly one must win.
	const N = 8
	var wg sync.WaitGroup
	results := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := parseAndValidateCredential(cfg, credB64)
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	successes, failures := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 success under concurrent replay, got %d successes / %d failures", successes, failures)
	}
}

// TestParseAndValidateCredential_BadPayloadDoesNotBurnChallenge verifies
// that a structurally-valid challenge with a malformed payload is rejected
// WITHOUT consuming the replay-cache slot — so a client bug that submits a
// payload missing `spt` doesn't permanently lock out the challenge ID. The
// agent can fix the payload and retry with the same credential. This is
// payment fidelity: structural client bugs must not burn the agent's
// challenge.
func TestParseAndValidateCredential_BadPayloadDoesNotBurnChallenge(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)

	// First attempt: missing spt in payload — must reject WITHOUT reserving.
	badCred := buildCredentialForTest(t, chal, map[string]any{"not_spt": "wrong"})
	if _, _, err := parseAndValidateCredential(cfg, badCred); err == nil {
		t.Fatal("expected error for missing spt")
	}

	// Second attempt: same challenge, correct payload — MUST succeed.
	// If it fails with "replay", the bad-payload attempt incorrectly burned
	// the slot.
	goodCred := buildCredentialForTest(t, chal, validPayload())
	if _, _, err := parseAndValidateCredential(cfg, goodCred); err != nil {
		t.Fatalf("retry with corrected payload should succeed, got: %v", err)
	}
}

// TestParseAndValidateCredential_NonStringSPTDoesNotBurnChallenge same as
// above but for a payload where spt is present but not a string.
func TestParseAndValidateCredential_NonStringSPTDoesNotBurnChallenge(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)

	badCred := buildCredentialForTest(t, chal, map[string]any{"spt": 42})
	if _, _, err := parseAndValidateCredential(cfg, badCred); err == nil {
		t.Fatal("expected error for non-string spt")
	}

	goodCred := buildCredentialForTest(t, chal, validPayload())
	if _, _, err := parseAndValidateCredential(cfg, goodCred); err != nil {
		t.Fatalf("retry with corrected payload should succeed, got: %v", err)
	}
}

func TestParseAndValidateCredential_MalformedRequestField(t *testing.T) {
	resetReplayCache()
	cfg := testCfg()
	chal := buildChallengeForTest(t, cfg, "stripe", "charge", validRequestBody(cfg), 5*time.Minute)
	// Replace request with garbage base64. HMAC was bound to the original
	// request, so the tamper will fail HMAC bind FIRST (good — the
	// malformed-request path only fires on a server-signed challenge with
	// a corrupted request payload, which shouldn't happen in practice but
	// is worth covering defensively).
	chal.Request = "!!!notbase64!!!"
	chal.ID = generateChallengeID(cfg.HMACSecret, cfg.Realm, chal.Method, chal.Intent, "!!!notbase64!!!", chal.Expires)
	credB64 := buildCredentialForTest(t, chal, validPayload())

	_, _, err := parseAndValidateCredential(cfg, credB64)
	if err == nil {
		t.Fatal("expected error for malformed request field")
	}
}

// TestGenerateChallengeID_Stability verifies that the HMAC bind is
// deterministic for the same inputs — needed for stateless verification.
func TestGenerateChallengeID_Stability(t *testing.T) {
	a := generateChallengeID("secret", "realm", "stripe", "charge", "req", "2026-06-09T12:00:00Z")
	b := generateChallengeID("secret", "realm", "stripe", "charge", "req", "2026-06-09T12:00:00Z")
	if a != b {
		t.Errorf("same inputs produced different IDs: %s vs %s", a, b)
	}
}

func TestGenerateChallengeID_Sensitivity(t *testing.T) {
	base := generateChallengeID("secret", "realm", "stripe", "charge", "req", "2026-06-09T12:00:00Z")
	cases := []struct {
		name string
		got  string
	}{
		{"different secret", generateChallengeID("OTHER", "realm", "stripe", "charge", "req", "2026-06-09T12:00:00Z")},
		{"different realm", generateChallengeID("secret", "OTHER", "stripe", "charge", "req", "2026-06-09T12:00:00Z")},
		{"different method", generateChallengeID("secret", "realm", "tempo", "charge", "req", "2026-06-09T12:00:00Z")},
		{"different intent", generateChallengeID("secret", "realm", "stripe", "session", "req", "2026-06-09T12:00:00Z")},
		{"different request", generateChallengeID("secret", "realm", "stripe", "charge", "REQ_DIFFERENT", "2026-06-09T12:00:00Z")},
		{"different expires", generateChallengeID("secret", "realm", "stripe", "charge", "req", "2026-06-09T13:00:00Z")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got == base {
				t.Errorf("%s should produce a different ID; both produced %s", tc.name, tc.got)
			}
		})
	}
}

func TestReserveChallengeID_FirstUseSucceeds(t *testing.T) {
	resetReplayCache()
	if !reserveChallengeID("test-id-1", time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339)) {
		t.Fatal("first reservation should succeed")
	}
}

func TestReserveChallengeID_ReplayFails(t *testing.T) {
	resetReplayCache()
	exp := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	reserveChallengeID("test-id-2", exp)
	if reserveChallengeID("test-id-2", exp) {
		t.Fatal("second reservation of same ID should fail")
	}
}

func TestReserveChallengeID_EmptyIDRejected(t *testing.T) {
	resetReplayCache()
	if reserveChallengeID("", time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339)) {
		t.Fatal("empty ID should never reserve (would allow trivial DoS by polluting cache key)")
	}
}

func TestReserveChallengeID_PrunesExpired(t *testing.T) {
	resetReplayCache()
	// Insert an entry that's already expired (the reservation expiry, which
	// is parsed-expires + 1 minute, must be in the past).
	expiredExp := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	if !reserveChallengeID("aged-id", expiredExp) {
		t.Fatal("first reservation should succeed even with backdated expiry")
	}
	// Reserve a new ID — the prune sweep should remove the aged entry,
	// allowing it to be re-reserved.
	if !reserveChallengeID("new-id", time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339)) {
		t.Fatal("new reservation should succeed")
	}
	// Now the aged ID should be re-reservable (it was pruned).
	if !reserveChallengeID("aged-id", time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339)) {
		t.Fatal("aged ID should be re-reservable after prune sweep")
	}
}
