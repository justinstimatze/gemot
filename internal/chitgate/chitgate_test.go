package chitgate

import (
	"context"
	"testing"

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

func TestPayToAddress(t *testing.T) {
	cases := map[string]string{
		"base:0x948eB1Bc3fb960D97EA7AFc0FAca9F6625352594": "0x948eB1Bc3fb960D97EA7AFc0FAca9F6625352594",
		"0xabc":              "0xabc",  // no prefix → unchanged
		"base:":              "",       // prefix only → empty
		"eip155:8453:0xdead": "0xdead", // strips to the last colon (CAIP-2 style)
	}
	for in, want := range cases {
		if got := payToAddress(in); got != want {
			t.Errorf("payToAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMintRequirements(t *testing.T) {
	c := &chitMerchant{payTo: "0x948eB1Bc3fb960D97EA7AFc0FAca9F6625352594"}
	reqs, err := c.mintRequirements(500, "gemot:buy_credits")
	if err != nil {
		t.Fatalf("mintRequirements: %v", err)
	}
	if reqs.X402Version != x402Version {
		t.Errorf("X402Version = %d, want %d", reqs.X402Version, x402Version)
	}
	if len(reqs.Accepts) != 1 {
		t.Fatalf("Accepts len = %d, want 1", len(reqs.Accepts))
	}
	a := reqs.Accepts[0]
	// $5.00 = 500 cents = 5.00 USDC = 5_000_000 atomic units.
	if a.Amount != "5000000" {
		t.Errorf("Amount = %q, want %q", a.Amount, "5000000")
	}
	if a.Scheme != x402Scheme || a.Network != x402Network {
		t.Errorf("scheme/network = %q/%q, want %q/%q", a.Scheme, a.Network, x402Scheme, x402Network)
	}
	if a.PayTo != c.payTo {
		t.Errorf("PayTo = %q, want %q", a.PayTo, c.payTo)
	}
	if a.Asset != usdcAssetBase {
		t.Errorf("Asset = %q, want %q", a.Asset, usdcAssetBase)
	}
	if a.MaxTimeoutSeconds != x402MaxTimeoutSeconds {
		t.Errorf("MaxTimeoutSeconds = %d, want %d", a.MaxTimeoutSeconds, x402MaxTimeoutSeconds)
	}
	if a.Extra["name"] != usdcEIP712Name || a.Extra["version"] != usdcEIP712Version {
		t.Errorf("Extra = %v, want name=%q version=%q", a.Extra, usdcEIP712Name, usdcEIP712Version)
	}
	if a.Resource != "gemot:buy_credits" {
		t.Errorf("Resource = %q, want %q", a.Resource, "gemot:buy_credits")
	}
	// Guardrails: non-positive price and missing payout address must error.
	if _, err := c.mintRequirements(0, "r"); err == nil {
		t.Error("expected error for 0 cents")
	}
	empty := &chitMerchant{}
	if _, err := empty.mintRequirements(500, "r"); err == nil {
		t.Error("expected error when payTo is empty")
	}
}

func TestChallengeFrom(t *testing.T) {
	c := &chitMerchant{payTo: "0xabc"}
	reqs, err := c.mintRequirements(100, "r")
	if err != nil {
		t.Fatalf("mintRequirements: %v", err)
	}
	ci := challengeFrom(reqs)
	if ci.Code != x402PaymentRequiredCode {
		t.Errorf("Code = %d, want %d", ci.Code, x402PaymentRequiredCode)
	}
	if ci.Message == "" {
		t.Error("Message should be non-empty")
	}
	got, ok := ci.Data["x402"].(chit.X402PaymentRequirements)
	if !ok {
		t.Fatalf("Data[\"x402\"] type = %T, want chit.X402PaymentRequirements", ci.Data["x402"])
	}
	if len(got.Accepts) != 1 || got.Accepts[0].Amount != "1000000" {
		t.Errorf("challenge requirements not carried through: %+v", got)
	}
}

// TestRequirePayment_Call1SelfMint proves call 1 emits a challenge WITHOUT
// touching chit: the merchant's *chit.Merchant is nil, so any attempt to call it
// would panic. A clean challenge return is the proof the decouple holds.
func TestRequirePayment_Call1SelfMint(t *testing.T) {
	c := &chitMerchant{payTo: "0x948eB1Bc3fb960D97EA7AFc0FAca9F6625352594", resource: "gemot:buy_credits"}
	settled, ch, err := c.RequirePayment(context.Background(), payments.X402PaymentRequest{PriceCents: 500})
	if err != nil {
		t.Fatalf("call-1 RequirePayment: %v", err)
	}
	if settled != nil {
		t.Errorf("call-1 must not settle, got %+v", settled)
	}
	if ch == nil {
		t.Fatal("call-1 must return a challenge")
	}
	reqs, ok := ch.Data["x402"].(chit.X402PaymentRequirements)
	if !ok || len(reqs.Accepts) != 1 || reqs.Accepts[0].Amount != "5000000" {
		t.Fatalf("call-1 challenge shape wrong: %+v", ch.Data)
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
