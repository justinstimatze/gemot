package payments

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ChallengeInfo is a gemot-native view of an ATXP payment challenge — enough to
// re-emit as a JSON-RPC -32042 / -30402 error without importing the chit SDK.
type ChallengeInfo struct {
	Code    int
	Message string
	Data    map[string]any
}

// BuyCredits meters a credit purchase through the ATXP gate and, ONLY on a
// settled charge, tops up the caller's gemot credit key. The gemot key is the
// balance funded; the ATXP account is who gets charged — ATXP is the funding
// rail, the gemot key is the account. Mirrors the Stripe checkout path
// (pay → AddCredits) with ATXP swapped in for Stripe.
//
// Crediting is idempotent on the credential's EIP-3009 nonce: a concurrent
// double-present, or an AlreadySettled retry of the same authorization, credits
// the key exactly once (see CreditStore.CreditX402Settlement).
//
//	gemotKey        — the gemot API key whose balance is topped up (required).
//	atxpAccountID   — the caller's ATXP account (sub); who gets charged.
//	atxpSourceToken — the base64 X-PAYMENT settle credential (empty on call 1).
func BuyCredits(ctx context.Context, gate CreditGate, adder creditAdder, packName, gemotKey, atxpAccountID, atxpSourceToken string) (*BuyCreditsResult, error) {
	if gate == nil || adder == nil {
		return nil, errors.New("buy_credits: gate and credit store are required")
	}
	if gemotKey == "" {
		return nil, errors.New("buy_credits: gemot API key required (the balance to top up)")
	}
	pack, ok := lookupPack(packName)
	if !ok {
		return nil, fmt.Errorf("buy_credits: unknown pack %q", packName)
	}

	ch, err := gate.RequirePayment(ctx, pack.PriceUSD, atxpAccountID, atxpSourceToken, "gemot:buy_credits")
	if err != nil {
		return nil, fmt.Errorf("buy_credits: payment gate error: %w", err)
	}
	if ch != nil {
		return nil, &ErrPaymentRequired{Challenge: ch}
	}

	// Settled. Key the credit on the credential's EIP-3009 nonce so a concurrent
	// double-present or an AlreadySettled retry credits exactly once. A settled
	// charge we cannot key is not safe to credit blindly — fail closed and flag
	// for reconcile rather than risk a double-credit.
	nonce, err := decodeX402Nonce(atxpSourceToken)
	if err != nil {
		return nil, fmt.Errorf("buy_credits: charge SETTLED but could not extract settlement nonce (reconcile, do not retry): %w", err)
	}
	bal, _, err := adder.CreditX402Settlement(nonce, gemotKey, pack.Credits)
	if err != nil {
		return nil, fmt.Errorf("buy_credits: charge SETTLED but crediting key failed (reconcile): %w", err)
	}
	// applied=false (nonce already processed) is not an error: the returned
	// balance is authoritative and the credit was applied by the first settlement.
	return &BuyCreditsResult{Pack: pack.Name, Credits: pack.Credits, NewBalance: bal}, nil
}

// BuyCreditsResult is returned only on the settled (paid) path.
type BuyCreditsResult struct {
	Pack       string
	Credits    int
	NewBalance int
}

// CreditGate is the narrow seam gemot depends on for ATXP-metered purchases;
// chit's *server.Merchant is adapted onto it at the composition root. Semantics
// mirror chit's RequirePayment and are fail-closed:
//
//	(nil, nil)            → the charge settled; proceed (credit the ledger).
//	(*ChallengeInfo, nil) → payment required; emit as an MCP error, do NOT credit.
//	(nil, non-nil error)  → infrastructure error; do NOT credit.
type CreditGate interface {
	RequirePayment(ctx context.Context, priceCents int64, accountID, sourceToken, resource string) (*ChallengeInfo, error)
}

// ErrPaymentRequired wraps a challenge the caller must re-emit to the agent as a
// -32042 payment-required error — the (*ChallengeInfo, nil) case surfaced as a
// typed error so callers can branch on it.
type ErrPaymentRequired struct{ Challenge *ChallengeInfo }

// creditAdder is the slice of *CreditStore the buy_credits handler needs, kept
// as an interface so the handler is unit-testable without a database.
type creditAdder interface {
	// CreditX402Settlement credits the pack once per settlement nonce, returning
	// (balance, applied). applied=false means the nonce was already processed and
	// no new credit was added.
	CreditX402Settlement(nonce, key string, amount int) (int, bool, error)
}

// lookupPack resolves a credit pack by name (case-insensitive); empty name
// defaults to the first (Starter) pack, matching the Stripe checkout default.
func lookupPack(name string) (CreditPack, bool) {
	if len(CreditPacks) == 0 {
		return CreditPack{}, false
	}
	if name == "" {
		return CreditPacks[0], true
	}
	for _, p := range CreditPacks {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return CreditPack{}, false
}

// Error implements the error interface for ErrPaymentRequired.
func (e *ErrPaymentRequired) Error() string {
	if e.Challenge == nil {
		return "payment required"
	}
	return fmt.Sprintf("payment required: %s", e.Challenge.Message)
}
