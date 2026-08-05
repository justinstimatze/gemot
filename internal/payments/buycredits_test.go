package payments

import (
	"context"
	"errors"
	"strings"
	"testing"
)

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
		t.Fatalf("must NOT credit on challenge; AddCredits called %d times", adder.calls)
	}
}

func TestBuyCredits_DefaultPackStarter(t *testing.T) {
	gate := &fakeGate{}
	adder := &fakeAdder{newBal: 42}
	res, err := BuyCredits(context.Background(), gate, adder, "", "gmt_key", "atxp:payer", "tok")
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
	_, err := BuyCredits(context.Background(), gate, adder, "Starter", "", "atxp:payer", "tok")
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
	_, err := BuyCredits(context.Background(), gate, adder, "Starter", "gmt_key", "atxp:payer", "tok")
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	var pr *ErrPaymentRequired
	if errors.As(err, &pr) {
		t.Fatal("gate error must not be surfaced as ErrPaymentRequired")
	}
	if adder.calls != 0 {
		t.Fatalf("must NOT credit on gate error; AddCredits called %d times", adder.calls)
	}
}

func TestBuyCredits_Paid(t *testing.T) {
	gate := &fakeGate{}
	adder := &fakeAdder{newBal: 1000}
	res, err := BuyCredits(context.Background(), gate, adder, "Starter", "gmt_key", "atxp:payer", "tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adder.calls != 1 || adder.key != "gmt_key" {
		t.Fatalf("expected AddCredits(gmt_key) once, got calls=%d key=%q", adder.calls, adder.key)
	}
	if res.Credits != CreditPacks[0].Credits || res.NewBalance != 1000 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if gate.gotPrice != CreditPacks[0].PriceUSD {
		t.Fatalf("charged %d, want pack price %d", gate.gotPrice, CreditPacks[0].PriceUSD)
	}
	if gate.gotResource != "gemot:buy_credits" {
		t.Fatalf("resource = %q", gate.gotResource)
	}
}

func TestBuyCredits_PaidButCreditFails(t *testing.T) {
	gate := &fakeGate{}
	adder := &fakeAdder{err: errors.New("db down")}
	_, err := BuyCredits(context.Background(), gate, adder, "Starter", "gmt_key", "atxp:payer", "tok")
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

func TestBuyCredits_UnknownPack(t *testing.T) {
	gate := &fakeGate{}
	adder := &fakeAdder{}
	_, err := BuyCredits(context.Background(), gate, adder, "nope", "gmt_key", "atxp:payer", "tok")
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

type fakeAdder struct {
	err    error
	newBal int
	key    string
	added  int
	calls  int
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

func (f *fakeAdder) AddCredits(key string, amount int) (int, error) {
	f.calls++
	f.key = key
	f.added = amount
	if f.err != nil {
		return 0, f.err
	}
	return f.newBal, nil
}
