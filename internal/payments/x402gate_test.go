package payments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// --- test doubles -----------------------------------------------------------

type fakeX402Merchant struct {
	mode  func(req X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error)
	calls []X402PaymentRequest
}

func (f *fakeX402Merchant) RequirePayment(_ context.Context, req X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error) {
	f.calls = append(f.calls, req)
	if f.mode == nil {
		return nil, nil, errors.New("fake merchant: no mode set")
	}
	return f.mode(req)
}

const (
	testPayerAddr  = "0x16a4caa185d2eda5d63067e6e592708c79b71067"
	testOtherAddr  = "0x1111111111111111111111111111111111111111"
	testSourceAcct = "atxp_acct_source"
)

// makeCred builds a base64 X-PAYMENT settle credential naming `from`, in the
// wire shape chit confirmed (payload.authorization.from is the EIP-3009 signer).
func makeCred(t *testing.T, from, value string) string {
	t.Helper()
	body := map[string]any{
		"x402Version": 1,
		"payload": map[string]any{
			"signature": "0xdeadbeef",
			"authorization": map[string]any{
				"from":        from,
				"to":          "0x52440b7ef75b9329b84fed88061e5665767b409b",
				"value":       value,
				"validAfter":  "0",
				"validBefore": "9999999999",
				"nonce":       "0x0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal cred: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// settleMode returns a merchant that challenges when no credential is present
// and settles (as `addr`) when one is.
func settleMode(addr string) func(X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error) {
	return func(req X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error) {
		if req.Credential == "" {
			return nil, &ChallengeInfo{Code: -32042, Message: "payment required"}, nil
		}
		return &X402Settlement{PayerAddress: addr, TxHash: "0xtx"}, nil, nil
	}
}

func mustGate(t *testing.T, m x402Merchant, source string, p *PayerPolicy) *X402Gate {
	t.Helper()
	g, err := NewX402Gate(m, source, p)
	if err != nil {
		t.Fatalf("NewX402Gate: %v", err)
	}
	return g
}

func mustPolicy(t *testing.T, blocked []string, capCents int64, maxPerWindow int, window time.Duration) *PayerPolicy {
	t.Helper()
	p, err := NewPayerPolicy(blocked, capCents, maxPerWindow, window)
	if err != nil {
		t.Fatalf("NewPayerPolicy: %v", err)
	}
	return p
}

// --- first leg (challenge) --------------------------------------------------

func TestX402Gate_FirstCall_ReturnsChallenge(t *testing.T) {
	m := &fakeX402Merchant{mode: settleMode(testPayerAddr)}
	g := mustGate(t, m, testSourceAcct, nil)

	ch, err := g.RequirePayment(context.Background(), 100, "atxp:payer", "", "gemot:buy_credits")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ch == nil || ch.Code != -32042 {
		t.Fatalf("expected a challenge, got %+v", ch)
	}
	// The merchant must have been asked with gemot's own source account and no
	// credential.
	if len(m.calls) != 1 || m.calls[0].Credential != "" || m.calls[0].SourceAccountID != testSourceAcct {
		t.Fatalf("unexpected merchant call: %+v", m.calls)
	}
}

func TestX402Gate_FirstCall_MerchantError_FailsClosed(t *testing.T) {
	m := &fakeX402Merchant{mode: func(X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error) {
		return nil, nil, errors.New("rail down")
	}}
	g := mustGate(t, m, testSourceAcct, nil)

	ch, err := g.RequirePayment(context.Background(), 100, "", "", "r")
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	if ch != nil {
		t.Fatalf("must not return a challenge on infra error, got %+v", ch)
	}
}

func TestX402Gate_FirstCall_UnexpectedSettlement_FailsClosed(t *testing.T) {
	// A merchant that reports settlement with no credential is misbehaving.
	m := &fakeX402Merchant{mode: func(X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error) {
		return &X402Settlement{PayerAddress: testPayerAddr}, nil, nil
	}}
	g := mustGate(t, m, testSourceAcct, nil)

	if _, err := g.RequirePayment(context.Background(), 100, "", "", "r"); err == nil {
		t.Fatal("expected fail-closed error on settlement without a credential")
	}
}

// --- credential decoding ----------------------------------------------------

func TestX402Gate_Credential_MalformedBase64(t *testing.T) {
	m := &fakeX402Merchant{mode: settleMode(testPayerAddr)}
	g := mustGate(t, m, testSourceAcct, nil)

	if _, err := g.RequirePayment(context.Background(), 100, "", "!!!not base64!!!", "r"); err == nil {
		t.Fatal("expected error for malformed base64 credential")
	}
	if len(m.calls) != 0 {
		t.Fatalf("merchant must not be called on a malformed credential; calls=%d", len(m.calls))
	}
}

func TestX402Gate_Credential_MalformedJSON(t *testing.T) {
	m := &fakeX402Merchant{mode: settleMode(testPayerAddr)}
	g := mustGate(t, m, testSourceAcct, nil)

	bad := base64.StdEncoding.EncodeToString([]byte("{not json"))
	if _, err := g.RequirePayment(context.Background(), 100, "", bad, "r"); err == nil {
		t.Fatal("expected error for malformed JSON credential")
	}
	if len(m.calls) != 0 {
		t.Fatalf("merchant must not be called on malformed JSON; calls=%d", len(m.calls))
	}
}

func TestX402Gate_Credential_BadFromAddress(t *testing.T) {
	m := &fakeX402Merchant{mode: settleMode(testPayerAddr)}
	g := mustGate(t, m, testSourceAcct, nil)

	for _, from := range []string{"", "0x123", "notanaddress", "0xZZZZ111111111111111111111111111111111111"} {
		cred := makeCred(t, from, "10000")
		if _, err := g.RequirePayment(context.Background(), 100, "", cred, "r"); err == nil {
			t.Fatalf("expected error for bad from %q", from)
		}
	}
	if len(m.calls) != 0 {
		t.Fatalf("merchant must not be called on a bad signer address; calls=%d", len(m.calls))
	}
}

// --- happy path + spend accounting ------------------------------------------

func TestX402Gate_Settles_CreditsAndRecordsSpend(t *testing.T) {
	m := &fakeX402Merchant{mode: settleMode(testPayerAddr)}
	// cap == price, so the FIRST settle uses the whole window budget; the second
	// call proves record() committed by tripping the cap pre-settle.
	pol := mustPolicy(t, nil, 100, 0, time.Minute)
	g := mustGate(t, m, testSourceAcct, pol)
	cred := makeCred(t, testPayerAddr, "10000")

	ch, err := g.RequirePayment(context.Background(), 100, "", cred, "r")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ch != nil {
		t.Fatalf("settled call must return (nil,nil), got challenge %+v", ch)
	}
	if len(m.calls) != 1 || m.calls[0].Credential != cred {
		t.Fatalf("merchant should have been called once with the credential; calls=%+v", m.calls)
	}

	// Second charge on the same payer would exceed the cap → rejected before the
	// merchant is called again.
	_, err = g.RequirePayment(context.Background(), 100, "", cred, "r")
	if !errors.Is(err, ErrPayerSpendCap) {
		t.Fatalf("expected ErrPayerSpendCap after the window budget was spent, got %v", err)
	}
	if len(m.calls) != 1 {
		t.Fatalf("merchant must NOT be called once the cap is hit; calls=%d", len(m.calls))
	}
}

// --- pre-settle payer controls (fail before money moves) --------------------

func TestX402Gate_Blocked_RejectedBeforeSettle(t *testing.T) {
	m := &fakeX402Merchant{mode: settleMode(testPayerAddr)}
	pol := mustPolicy(t, []string{strings.ToUpper(testPayerAddr)}, 0, 0, 0) // blocklist matches case-insensitively
	g := mustGate(t, m, testSourceAcct, pol)
	cred := makeCred(t, testPayerAddr, "10000")

	_, err := g.RequirePayment(context.Background(), 100, "", cred, "r")
	if !errors.Is(err, ErrPayerBlocked) {
		t.Fatalf("expected ErrPayerBlocked, got %v", err)
	}
	if len(m.calls) != 0 {
		t.Fatalf("blocked payer must never reach the merchant; calls=%d", len(m.calls))
	}
}

func TestX402Gate_SpendCap_RejectedBeforeSettle(t *testing.T) {
	m := &fakeX402Merchant{mode: settleMode(testPayerAddr)}
	pol := mustPolicy(t, nil, 50, 0, time.Minute) // cap 50 < price 100
	g := mustGate(t, m, testSourceAcct, pol)
	cred := makeCred(t, testPayerAddr, "10000")

	_, err := g.RequirePayment(context.Background(), 100, "", cred, "r")
	if !errors.Is(err, ErrPayerSpendCap) {
		t.Fatalf("expected ErrPayerSpendCap, got %v", err)
	}
	if len(m.calls) != 0 {
		t.Fatalf("over-cap payer must never reach the merchant; calls=%d", len(m.calls))
	}
}

func TestX402Gate_RateLimit_RejectedBeforeSettle(t *testing.T) {
	m := &fakeX402Merchant{mode: settleMode(testPayerAddr)}
	pol := mustPolicy(t, nil, 0, 1, time.Minute) // one settle per window
	g := mustGate(t, m, testSourceAcct, pol)
	cred := makeCred(t, testPayerAddr, "10000")

	if _, err := g.RequirePayment(context.Background(), 10, "", cred, "r"); err != nil {
		t.Fatalf("first settle should succeed: %v", err)
	}
	_, err := g.RequirePayment(context.Background(), 10, "", cred, "r")
	if !errors.Is(err, ErrPayerRateLimit) {
		t.Fatalf("expected ErrPayerRateLimit on the second settle, got %v", err)
	}
	if len(m.calls) != 1 {
		t.Fatalf("rate-limited payer must not reach the merchant a second time; calls=%d", len(m.calls))
	}
}

// --- settle-leg outcomes ----------------------------------------------------

func TestX402Gate_MerchantRejectsCredential_ReturnsChallenge_NoSpend(t *testing.T) {
	// Merchant rejects the presented credential by returning a fresh challenge.
	m := &fakeX402Merchant{mode: func(req X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error) {
		return nil, &ChallengeInfo{Code: -32042, Message: "rejected, try again"}, nil
	}}
	pol := mustPolicy(t, nil, 100, 1, time.Minute)
	g := mustGate(t, m, testSourceAcct, pol)
	cred := makeCred(t, testPayerAddr, "10000")

	ch, err := g.RequirePayment(context.Background(), 100, "", cred, "r")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ch == nil {
		t.Fatal("expected the merchant's challenge to be surfaced")
	}
	// No spend recorded — a subsequent genuine settle for the same payer must
	// still be allowed (cap/count untouched).
	m.mode = settleMode(testPayerAddr)
	if _, err := g.RequirePayment(context.Background(), 100, "", cred, "r"); err != nil {
		t.Fatalf("a rejected attempt must not consume budget; got %v", err)
	}
}

func TestX402Gate_MerchantError_OnSettle_FailsClosed_NoSpend(t *testing.T) {
	m := &fakeX402Merchant{mode: func(req X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error) {
		if req.Credential == "" {
			return nil, &ChallengeInfo{Code: -32042}, nil
		}
		return nil, nil, errors.New("settle rail error")
	}}
	pol := mustPolicy(t, nil, 100, 1, time.Minute)
	g := mustGate(t, m, testSourceAcct, pol)
	cred := makeCred(t, testPayerAddr, "10000")

	if _, err := g.RequirePayment(context.Background(), 100, "", cred, "r"); err == nil {
		t.Fatal("expected fail-closed error on settle failure")
	}
	// Budget must be intact.
	m.mode = settleMode(testPayerAddr)
	if _, err := g.RequirePayment(context.Background(), 100, "", cred, "r"); err != nil {
		t.Fatalf("a failed settle must not consume budget; got %v", err)
	}
}

func TestX402Gate_MerchantReturnsNeitherSettlementNorChallenge(t *testing.T) {
	m := &fakeX402Merchant{mode: func(req X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error) {
		return nil, nil, nil // both nil, no error — misbehaving merchant
	}}
	g := mustGate(t, m, testSourceAcct, nil)
	cred := makeCred(t, testPayerAddr, "10000")

	if _, err := g.RequirePayment(context.Background(), 100, "", cred, "r"); err == nil {
		t.Fatal("expected error when merchant returns neither settlement nor challenge")
	}
}

// --- signer binding ---------------------------------------------------------

func TestX402Gate_SignerMismatch_Refused(t *testing.T) {
	// Merchant settles but reports a DIFFERENT signer than the credential named.
	m := &fakeX402Merchant{mode: func(req X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error) {
		return &X402Settlement{PayerAddress: testOtherAddr}, nil, nil
	}}
	g := mustGate(t, m, testSourceAcct, nil)
	cred := makeCred(t, testPayerAddr, "10000")

	if _, err := g.RequirePayment(context.Background(), 100, "", cred, "r"); err == nil {
		t.Fatal("expected refusal when settled signer != credential signer")
	}
}

func TestX402Gate_EmptyPayerAddress_FallsBackToDecodedSigner(t *testing.T) {
	// Merchant settles but does not surface the signer; the gate must still
	// account against the decoded (now verified-by-settlement) signer.
	m := &fakeX402Merchant{mode: func(req X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error) {
		return &X402Settlement{PayerAddress: ""}, nil, nil
	}}
	pol := mustPolicy(t, nil, 100, 0, time.Minute)
	g := mustGate(t, m, testSourceAcct, pol)
	cred := makeCred(t, testPayerAddr, "10000")

	if ch, err := g.RequirePayment(context.Background(), 100, "", cred, "r"); err != nil || ch != nil {
		t.Fatalf("settle should succeed, got ch=%+v err=%v", ch, err)
	}
	// Spend was recorded against the decoded signer → cap now tripped.
	if _, err := g.RequirePayment(context.Background(), 100, "", cred, "r"); !errors.Is(err, ErrPayerSpendCap) {
		t.Fatalf("expected spend to be booked to the decoded signer, got %v", err)
	}
}

// --- abuse resistance: spoofed rejected credentials -------------------------

func TestX402Gate_SpoofedRejectedCredential_DoesNotMoveVictimCounters(t *testing.T) {
	// An attacker presents credentials naming a victim's address that the
	// merchant REJECTS (bad signature → challenge). This must not consume the
	// victim's cap/count, so the victim's own later genuine settle still works.
	reject := &fakeX402Merchant{mode: func(req X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error) {
		if req.Credential == "" {
			return nil, &ChallengeInfo{Code: -32042}, nil
		}
		return nil, &ChallengeInfo{Code: -32042, Message: "bad sig"}, nil
	}}
	pol := mustPolicy(t, nil, 100, 1, time.Minute) // exactly one genuine settle allowed
	g := mustGate(t, reject, testSourceAcct, pol)
	victimCred := makeCred(t, testPayerAddr, "10000")

	// Attacker spams the victim's address; every attempt is rejected.
	for i := 0; i < 5; i++ {
		if _, err := g.RequirePayment(context.Background(), 100, "", victimCred, "r"); err != nil {
			t.Fatalf("rejected attempt %d surfaced an unexpected error: %v", i, err)
		}
	}

	// The victim's one genuine settle must still be allowed — counters untouched.
	reject.mode = settleMode(testPayerAddr)
	if ch, err := g.RequirePayment(context.Background(), 100, "", victimCred, "r"); err != nil || ch != nil {
		t.Fatalf("victim's genuine settle must succeed after spoofed rejects; ch=%+v err=%v", ch, err)
	}
}

// --- window reset -----------------------------------------------------------

func TestX402Gate_WindowReset_AllowsAgain(t *testing.T) {
	m := &fakeX402Merchant{mode: settleMode(testPayerAddr)}
	pol := mustPolicy(t, nil, 0, 1, time.Minute)
	now := time.Unix(1_000_000, 0)
	pol.now = func() time.Time { return now }
	g := mustGate(t, m, testSourceAcct, pol)
	cred := makeCred(t, testPayerAddr, "10000")

	if _, err := g.RequirePayment(context.Background(), 10, "", cred, "r"); err != nil {
		t.Fatalf("first settle failed: %v", err)
	}
	if _, err := g.RequirePayment(context.Background(), 10, "", cred, "r"); !errors.Is(err, ErrPayerRateLimit) {
		t.Fatalf("expected rate limit within the window, got %v", err)
	}
	// Advance past the window; the count resets.
	now = now.Add(2 * time.Minute)
	if _, err := g.RequirePayment(context.Background(), 10, "", cred, "r"); err != nil {
		t.Fatalf("settle after window reset should succeed, got %v", err)
	}
}

// --- nil policy -------------------------------------------------------------

func TestX402Gate_NilPolicy_NoControls(t *testing.T) {
	m := &fakeX402Merchant{mode: settleMode(testPayerAddr)}
	g := mustGate(t, m, testSourceAcct, nil)
	cred := makeCred(t, testPayerAddr, "10000")

	for i := 0; i < 3; i++ {
		if ch, err := g.RequirePayment(context.Background(), 100, "", cred, "r"); err != nil || ch != nil {
			t.Fatalf("nil policy should impose no limits; iter %d ch=%+v err=%v", i, ch, err)
		}
	}
}

// --- constructor validation -------------------------------------------------

func TestNewX402Gate_Validation(t *testing.T) {
	if _, err := NewX402Gate(nil, testSourceAcct, nil); err == nil {
		t.Fatal("nil merchant should error")
	}
	if _, err := NewX402Gate(&fakeX402Merchant{}, "  ", nil); err == nil {
		t.Fatal("empty source account should error")
	}
}

func TestNewPayerPolicy_Validation(t *testing.T) {
	if _, err := NewPayerPolicy(nil, 100, 0, 0); err == nil {
		t.Fatal("cap set with zero window should error")
	}
	if _, err := NewPayerPolicy(nil, 0, 5, 0); err == nil {
		t.Fatal("count set with zero window should error")
	}
	if _, err := NewPayerPolicy(nil, 0, 0, 0); err != nil {
		t.Fatalf("no-limit policy should be valid: %v", err)
	}
}

// --- end-to-end through BuyCredits ------------------------------------------

func TestX402Gate_WithBuyCredits_EndToEnd(t *testing.T) {
	pack := CreditPacks[0]
	cred := makeCred(t, testPayerAddr, "10000")

	// Paid path: credential present, merchant settles → credits added.
	paid := mustGate(t, &fakeX402Merchant{mode: settleMode(testPayerAddr)}, testSourceAcct, nil)
	adder := &fakeAdder{newBal: 500}
	res, err := BuyCredits(context.Background(), paid, adder, pack.Name, "gmt_key", "atxp:payer", cred)
	if err != nil {
		t.Fatalf("paid path errored: %v", err)
	}
	if adder.calls != 1 || res.NewBalance != 500 || res.Credits != pack.Credits {
		t.Fatalf("unexpected paid result: calls=%d res=%+v", adder.calls, res)
	}

	// Challenge path: no credential → gate asks the merchant, BuyCredits surfaces
	// ErrPaymentRequired and does NOT credit.
	unpaid := mustGate(t, &fakeX402Merchant{mode: settleMode(testPayerAddr)}, testSourceAcct, nil)
	adder2 := &fakeAdder{}
	_, err = BuyCredits(context.Background(), unpaid, adder2, pack.Name, "gmt_key", "atxp:payer", "")
	var pr *ErrPaymentRequired
	if !errors.As(err, &pr) {
		t.Fatalf("expected ErrPaymentRequired on the unpaid path, got %v", err)
	}
	if adder2.calls != 0 {
		t.Fatalf("must not credit on the challenge path; calls=%d", adder2.calls)
	}
}
