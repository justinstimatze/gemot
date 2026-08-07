package payments

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// testCredential builds a base64 X-PAYMENT credential carrying an EIP-3009
// authorization with the given nonce — the minimum decodeX402Nonce needs.
func testCredential(nonce string) string {
	j := `{"payload":{"authorization":{"from":"0x1111111111111111111111111111111111111111","nonce":"` + nonce + `"}}}`
	return base64.StdEncoding.EncodeToString([]byte(j))
}

func TestBuyCredits_ChallengeReturned(t *testing.T) {
	gate := &fakeGate{ch: &ChallengeInfo{Code: -32042, Message: "pay up"}}
	adder := &fakeAdder{}
	_, err := BuyCredits(context.Background(), gate, adder, "Starter", "gmt_key", "atxp:payer", "")
	var pr *ErrPaymentRequired
	if !errors.As(err, &pr) {
		t.Fatalf("expected *ErrPaymentRequired, got %v", err)
	}
	if pr.Challenge == nil || pr.Challenge.Code != -32042 {
		t.Fatalf("challenge not carried through: %+v", pr.Challenge)
	}
	if adder.calls != 0 {
		t.Fatalf("must NOT credit on challenge; settlement called %d times", adder.calls)
	}
}

func TestBuyCredits_DefaultPackStarter(t *testing.T) {
	gate := &fakeGate{}
	adder := &fakeAdder{}
	res, err := BuyCredits(context.Background(), gate, adder, "", "gmt_key", "atxp:payer", testCredential("0xn1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Pack != CreditPacks[0].Name {
		t.Fatalf("empty pack should default to %q, got %q", CreditPacks[0].Name, res.Pack)
	}
}

func TestBuyCredits_EmptyKey(t *testing.T) {
	gate := &fakeGate{}
	adder := &fakeAdder{}
	_, err := BuyCredits(context.Background(), gate, adder, "Starter", "", "atxp:payer", testCredential("0xn1"))
	if err == nil {
		t.Fatal("expected error for empty gemot key")
	}
	if gate.calls != 0 {
		t.Fatalf("must validate before charging; gate calls=%d", gate.calls)
	}
}

func TestBuyCredits_GateErrorFailsClosed(t *testing.T) {
	gate := &fakeGate{err: errors.New("boom")}
	adder := &fakeAdder{}
	_, err := BuyCredits(context.Background(), gate, adder, "Starter", "gmt_key", "atxp:payer", testCredential("0xn1"))
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	var pr *ErrPaymentRequired
	if errors.As(err, &pr) {
		t.Fatal("gate error must not be surfaced as ErrPaymentRequired")
	}
	if adder.calls != 0 {
		t.Fatalf("must NOT credit on gate error; settlement called %d times", adder.calls)
	}
}

func TestBuyCredits_Paid(t *testing.T) {
	gate := &fakeGate{}
	adder := &fakeAdder{}
	res, err := BuyCredits(context.Background(), gate, adder, "Starter", "gmt_key", "atxp:payer", testCredential("0xn1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adder.calls != 1 || adder.key != "gmt_key" {
		t.Fatalf("expected one settlement credit to gmt_key, got calls=%d key=%q", adder.calls, adder.key)
	}
	if res.Credits != CreditPacks[0].Credits || res.NewBalance != CreditPacks[0].Credits {
		t.Fatalf("unexpected result: %+v", res)
	}
	if gate.gotPrice != CreditPacks[0].PriceUSD {
		t.Fatalf("charged %d, want pack price %d", gate.gotPrice, CreditPacks[0].PriceUSD)
	}
	if gate.gotResource != "gemot:buy_credits" {
		t.Fatalf("resource = %q", gate.gotResource)
	}
}

// TestBuyCredits_IdempotentReplay presents the SAME credential twice: the
// settlement is attempted both times, but the credit lands exactly once.
func TestBuyCredits_IdempotentReplay(t *testing.T) {
	gate := &fakeGate{}
	adder := &fakeAdder{}
	cred := testCredential("0xnonce-abc")
	r1, err := BuyCredits(context.Background(), gate, adder, "Starter", "gmt_key", "atxp:payer", cred)
	if err != nil {
		t.Fatalf("first buy: %v", err)
	}
	r2, err := BuyCredits(context.Background(), gate, adder, "Starter", "gmt_key", "atxp:payer", cred)
	if err != nil {
		t.Fatalf("replay buy: %v", err)
	}
	if adder.calls != 2 {
		t.Fatalf("expected 2 settlement attempts, got %d", adder.calls)
	}
	if adder.bal != CreditPacks[0].Credits {
		t.Fatalf("replay double-credited: balance=%d want %d", adder.bal, CreditPacks[0].Credits)
	}
	if r1.NewBalance != CreditPacks[0].Credits || r2.NewBalance != CreditPacks[0].Credits {
		t.Fatalf("balances not stable across replay: r1=%d r2=%d", r1.NewBalance, r2.NewBalance)
	}
}

func TestBuyCredits_PaidButCreditFails(t *testing.T) {
	gate := &fakeGate{}
	adder := &fakeAdder{err: errors.New("db down")}
	_, err := BuyCredits(context.Background(), gate, adder, "Starter", "gmt_key", "atxp:payer", testCredential("0xn1"))
	if err == nil {
		t.Fatal("expected error when settled charge cannot be credited")
	}
	var pr *ErrPaymentRequired
	if errors.As(err, &pr) {
		t.Fatal("credit failure must not look like payment-required")
	}
	if !strings.Contains(err.Error(), "SETTLED") {
		t.Fatalf("error should flag the reconcile case, got %v", err)
	}
}

// TestBuyCredits_SettledButNoNonce covers the fail-closed path when a settled
// charge's credential carries no extractable nonce — we must NOT credit.
func TestBuyCredits_SettledButNoNonce(t *testing.T) {
	gate := &fakeGate{}
	adder := &fakeAdder{}
	_, err := BuyCredits(context.Background(), gate, adder, "Starter", "gmt_key", "atxp:payer", "not-a-credential")
	if err == nil {
		t.Fatal("expected error when settled credential has no nonce")
	}
	if !strings.Contains(err.Error(), "SETTLED") {
		t.Fatalf("should flag the reconcile case, got %v", err)
	}
	if adder.calls != 0 {
		t.Fatalf("must NOT credit when nonce missing; settlement called %d times", adder.calls)
	}
}

func TestBuyCredits_UnknownPack(t *testing.T) {
	gate := &fakeGate{}
	adder := &fakeAdder{}
	_, err := BuyCredits(context.Background(), gate, adder, "nope", "gmt_key", "atxp:payer", testCredential("0xn1"))
	if err == nil {
		t.Fatal("expected error for unknown pack")
	}
	if gate.calls != 0 {
		t.Fatalf("must not hit the gate for an unknown pack; calls=%d", gate.calls)
	}
}

func TestLookupPack(t *testing.T) {
	if _, ok := lookupPack("bogus"); ok {
		t.Fatal("bogus pack should not resolve")
	}
	p, ok := lookupPack("")
	if !ok || p.Name != CreditPacks[0].Name {
		t.Fatalf("empty should default to Starter, got %q ok=%v", p.Name, ok)
	}
	ci, ok := lookupPack("sTaRtEr")
	if !ok || ci.Name != "Starter" {
		t.Fatalf("case-insensitive match failed: %q ok=%v", ci.Name, ok)
	}
}

// fakeAdder is an in-memory creditAdder that accumulates a balance and dedupes
// on the settlement nonce, mirroring CreditX402Settlement's contract.
type fakeAdder struct {
	err   error
	bal   int
	key   string
	added int
	calls int
	seen  map[string]bool
}

func (f *fakeAdder) CreditX402Settlement(nonce, key string, amount int) (int, bool, error) {
	f.calls++
	f.key = key
	f.added = amount
	if f.err != nil {
		return 0, false, f.err
	}
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if f.seen[nonce] {
		return f.bal, false, nil // replay — no new credit
	}
	f.seen[nonce] = true
	f.bal += amount
	return f.bal, true, nil
}

type fakeGate struct {
	ch          *ChallengeInfo
	err         error
	gotPrice    int64
	gotAccount  string
	gotToken    string
	gotResource string
	calls       int
}

func (f *fakeGate) RequirePayment(ctx context.Context, priceCents int64, accountID, sourceToken, resource string) (*ChallengeInfo, error) {
	f.calls++
	f.gotPrice = priceCents
	f.gotAccount = accountID
	f.gotToken = sourceToken
	f.gotResource = resource
	return f.ch, f.err
}
