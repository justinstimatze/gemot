package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/payments"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeAccountGate is a CreditGate stub. ch/err drive the outcome; calls counts
// invocations so tests can assert the gate was (or was not) reached.
type fakeAccountGate struct {
	ch    *payments.ChallengeInfo
	err   error
	calls int
}

func (f *fakeAccountGate) RequirePayment(_ context.Context, _ int64, _, _, _ string) (*payments.ChallengeInfo, error) {
	f.calls++
	return f.ch, f.err
}

func resultText(t *testing.T, res *sdkmcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("empty result")
	}
	tc, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want *TextContent", res.Content[0])
	}
	return tc.Text
}

func authCtx() context.Context {
	return context.WithValue(context.Background(), payments.ContextKeyAPIKey{}, "gmt_test_key")
}

func TestHandleAccount_NoGate_FailsClosed(t *testing.T) {
	s := &server{credits: mustCreditStore(t)}
	res, _, _ := s.handleAccount(authCtx(), nil, accountParams{Action: "buy_credits"})
	if !res.IsError || !strings.Contains(resultText(t, res), "not configured") {
		t.Fatalf("expected not-configured error, got %q (isErr=%v)", resultText(t, res), res.IsError)
	}
}

func TestHandleAccount_NoCreditStore_FailsClosed(t *testing.T) {
	s := &server{gate: &fakeAccountGate{}}
	res, _, _ := s.handleAccount(authCtx(), nil, accountParams{Action: "buy_credits"})
	if !res.IsError || !strings.Contains(resultText(t, res), "demo mode") {
		t.Fatalf("expected demo-mode error, got %q", resultText(t, res))
	}
}

func TestHandleAccount_NoAPIKey_FailsClosed(t *testing.T) {
	gate := &fakeAccountGate{}
	s := &server{gate: gate, credits: mustCreditStore(t)}
	// No API key in context.
	res, _, _ := s.handleAccount(context.Background(), nil, accountParams{Action: "buy_credits"})
	if !res.IsError || !strings.Contains(resultText(t, res), "authenticated gemot API key") {
		t.Fatalf("expected auth-required error, got %q", resultText(t, res))
	}
	if gate.calls != 0 {
		t.Fatalf("must validate the key before hitting the gate; calls=%d", gate.calls)
	}
}

func TestHandleAccount_PaymentRequired_ReturnsChallenge_NoCredit(t *testing.T) {
	gate := &fakeAccountGate{ch: &payments.ChallengeInfo{
		Code:    -32042,
		Message: "payment required",
		Data:    map[string]any{"x402Version": 1},
	}}
	s := &server{gate: gate, credits: mustCreditStore(t)}
	res, _, _ := s.handleAccount(authCtx(), nil, accountParams{Action: "buy_credits", Pack: "Starter"})
	if res.IsError {
		t.Fatalf("payment-required is the invoice, not an error: %q", resultText(t, res))
	}
	body := resultText(t, res)
	if !strings.Contains(body, "payment_required") || !strings.Contains(body, "x402Version") {
		t.Fatalf("challenge not surfaced in result: %q", body)
	}
	if gate.calls != 1 {
		t.Fatalf("gate should have been called once, got %d", gate.calls)
	}
}

func TestHandleAccount_UnknownAction(t *testing.T) {
	s := &server{gate: &fakeAccountGate{}, credits: mustCreditStore(t)}
	res, _, _ := s.handleAccount(authCtx(), nil, accountParams{Action: "top_up"})
	if !res.IsError || !strings.Contains(resultText(t, res), "unknown action") {
		t.Fatalf("expected unknown-action error, got %q", resultText(t, res))
	}
}

// mustCreditStore returns a non-nil *CreditStore with a nil DB. It is enough for
// the tests here, none of which reach AddCredits (the challenge path returns
// before crediting; the guard paths never touch it). The settle→credit path is
// covered by payments.TestX402Gate_WithBuyCredits_EndToEnd against a fake adder.
func mustCreditStore(t *testing.T) *payments.CreditStore {
	t.Helper()
	cs, err := payments.NewCreditStore(nil)
	if err != nil {
		t.Fatalf("NewCreditStore: %v", err)
	}
	return cs
}
