package chitgate

import (
	"context"
	"testing"
	"time"

	chit "github.com/justinstimatze/chit/server"
	"github.com/justinstimatze/gemot/internal/payments"
)

func TestCentsToAmount(t *testing.T) {
	for cents, want := range map[int64]string{100: "1.00", 4: "0.04", 5: "0.05", 1: "0.01", 250: "2.50", 12345: "123.45"} {
		got, err := centsToAmount(cents)
		if err != nil {
			t.Fatalf("centsToAmount(%d): %v", cents, err)
		}
		wantAmt, err := chit.ParseAmount(want)
		if err != nil {
			t.Fatalf("ParseAmount(%q): %v", want, err)
		}
		if got.String() != wantAmt.String() {
			t.Errorf("centsToAmount(%d) = %q, want %q", cents, got.String(), wantAmt.String())
		}
	}
	if _, err := centsToAmount(0); err == nil {
		t.Error("expected error for 0 cents")
	}
	if _, err := centsToAmount(-5); err == nil {
		t.Error("expected error for negative cents")
	}
}

func TestCorrelationKey(t *testing.T) {
	c := &chitMerchant{}
	keyed := context.WithValue(context.Background(), payments.ContextKeyKeyID{}, "k123")
	if got := c.correlationKey(keyed, payments.X402PaymentRequest{PayerAccountID: "atxp:payer"}); got != "k123" {
		t.Errorf("ctx keyID should win, got %q", got)
	}
	if got := c.correlationKey(context.Background(), payments.X402PaymentRequest{PayerAccountID: "atxp:payer"}); got != "atxp:payer" {
		t.Errorf("should fall back to payer account, got %q", got)
	}
	if got := c.correlationKey(context.Background(), payments.X402PaymentRequest{}); got != "default" {
		t.Errorf("should fall back to default, got %q", got)
	}
}

func TestChallengeCache_RememberRecallForget(t *testing.T) {
	orig := nowFn
	defer func() { nowFn = orig }()
	base := time.Unix(1_700_000_000, 0)
	nowFn = func() time.Time { return base }

	c := &chitMerchant{pending: map[string]pendingChallenge{}}
	ch := &chit.Challenge{Data: map[string]any{"paymentRequestId": "pr-abc"}}

	c.remember("corr1", ch)
	p, ok := c.recall("corr1")
	if !ok || p.paymentID != "pr-abc" {
		t.Fatalf("recall after remember: ok=%v paymentID=%q", ok, p.paymentID)
	}
	c.forget("corr1")
	if _, ok := c.recall("corr1"); ok {
		t.Fatal("recall after forget should miss")
	}
}

func TestChallengeCache_TTLExpiry(t *testing.T) {
	orig := nowFn
	defer func() { nowFn = orig }()
	base := time.Unix(1_700_000_000, 0)
	nowFn = func() time.Time { return base }

	c := &chitMerchant{pending: map[string]pendingChallenge{}}
	c.remember("corr", &chit.Challenge{Data: map[string]any{"paymentRequestId": "pr"}})

	// Just inside the window → still present.
	nowFn = func() time.Time { return base.Add(pendingTTL - time.Second) }
	if _, ok := c.recall("corr"); !ok {
		t.Fatal("should still be present just inside TTL")
	}
	// Past the window → pruned on read.
	nowFn = func() time.Time { return base.Add(pendingTTL + time.Second) }
	if _, ok := c.recall("corr"); ok {
		t.Fatal("should be expired past TTL")
	}
}

func TestToChallengeInfo(t *testing.T) {
	ch := &chit.Challenge{Code: -30402, Message: "pay up", Data: map[string]any{"x402": "reqs"}}
	ci := toChallengeInfo(ch)
	if ci.Code != -30402 || ci.Message != "pay up" || ci.Data["x402"] != "reqs" {
		t.Fatalf("toChallengeInfo mismapped: %+v", ci)
	}
}

func TestNew_RequiresConfig(t *testing.T) {
	if _, err := New(Config{ConnectionToken: "tok"}); err == nil {
		t.Error("expected error when MerchantID is empty")
	}
	// ConnectionToken is optional — the bare-402 x402 path needs only a
	// Destination, so a MerchantID alone builds a valid gate.
	if _, err := New(Config{MerchantID: "base:0x948eB1Bc3fb960D97EA7AFc0FAca9F6625352594"}); err != nil {
		t.Errorf("MerchantID alone should build a gate (token optional): %v", err)
	}
}
