// Package chitgate is the composition-root adapter binding gemot's CreditGate
// to a chit bare-402 x402 merchant. It lives outside internal/payments on
// purpose: payments must stay free of the chit import so its gate/policy stay
// unit-testable against a fake merchant, and the on-chain seam — which can only
// be validated live — is isolated here.
//
// It orchestrates chit's bare-402 x402 flow behind the single-call x402Merchant
// shape payments.X402Gate expects:
//
//	call 1 (no credential): gemot SELF-MINTS the x402 challenge to emit.
//	call 2 (credential):    DetectProtocol → OpenPaymentSession → RequirePayment
//	                        (charges the session) → CloseSession (settles
//	                        on-chain) → the verified payer address.
//
// Why self-mint call 1 (instead of asking chit): on the bare-402 path a
// credential-less call sets User=merchantID with no OAuth, so chit's pull
// /charge probe sees source==destination — a self-charge that the AS can report
// as already-settled right after the merchant settled anything, wedging strangers
// out of a challenge at prod scale. call 2 takes chit's Session branch, which
// skips /charge entirely and was always safe. So the fix is isolated to call 1:
// don't call chit there at all. Since the price is a static per-pack lookup, both
// calls recompute the SAME X402PaymentRequirements deterministically — no cached
// challenge state, no RemoteAddr correlation. The settle path (call 2) needs the
// same requirements that were advertised in call 1, and recomputing gives exactly
// that.
package chitgate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	chit "github.com/justinstimatze/chit/server"
	"github.com/justinstimatze/gemot/internal/payments"
)

// x402 challenge constants for the Base / USDC "exact" scheme. gemot's merchant
// Destination is a Base address and it is paid in USDC, so these are fixed for
// this deployment. If a second chain/asset is ever added, lift them into Config.
const (
	x402Version           = 2
	x402Network           = "eip155:8453"                                // Base mainnet (CAIP-2)
	x402Scheme            = "exact"                                      // EIP-3009 transferWithAuthorization
	usdcAssetBase         = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913" // USD Coin on Base
	usdcEIP712Name        = "USD Coin"                                   // EIP-712 domain name — MUST match USDC's real domain
	usdcEIP712Version     = "2"                                          // EIP-712 domain version on Base
	x402MaxTimeoutSeconds = 300
	// usdcAtomicPerCent scales integer US cents to USDC atomic units (6 decimals):
	// 1 cent = $0.01 = 0.01 USDC = 10_000 atomic units. So 500 cents → 5_000_000.
	usdcAtomicPerCent = 10_000
)

// x402PaymentRequiredCode is the code surfaced to the agent in the
// payment-required result. Informational: the x402 payer reads data.x402.accepts,
// not this code.
const x402PaymentRequiredCode = 402

// Config configures the chit-backed merchant gate. Only MerchantID is required.
// ConnectionToken is optional (see the field) and, when set, is a wallet-grade
// secret that must come from the environment, never source.
type Config struct {
	// MerchantID is chit's "network:address" account id, e.g.
	// "base:0x948eB1Bc3fb960D97EA7AFc0FAca9F6625352594". It doubles as the
	// destination and the nominal sourceAccountId (never checked against the
	// real payer — abuse controls key on the credential's signer instead). The
	// bare address (after the "network:" prefix) is the x402 payTo.
	MerchantID string

	// ConnectionToken is the merchant's ATXP connection token. OPTIONAL: needed
	// only for DCR-bound registration on OAuth-gated resources, NOT the bare-402
	// x402 path buy_credits uses (a plain payout-address Destination suffices).
	// When set it is a wallet-grade secret — env only.
	ConnectionToken string

	// PayeeName labels the merchant in challenges. Optional.
	PayeeName string

	// Resource labels what is being paid for in challenges. Optional.
	Resource string

	// Policy is gemot's per-payer-address abuse policy (blocklist / spend cap /
	// settle rate), keyed on the verified EIP-3009 signer. Optional (nil = none).
	Policy *payments.PayerPolicy

	// DB, when non-nil, backs a persistent atxp.Store so DCR client credentials
	// survive restarts/instances (chit's default store is in-memory, which forces
	// a fresh client_name each launch). Optional; nil keeps the in-memory default.
	DB *sql.DB
}

// New builds a payments.CreditGate backed by a chit bare-402 x402 merchant.
func New(cfg Config) (payments.CreditGate, error) {
	if cfg.MerchantID == "" {
		return nil, errors.New("chitgate: MerchantID is required (network:address)")
	}
	// ConnectionToken is NOT required for the bare-402 x402 path buy_credits
	// uses: chit's server.Config only strictly needs a Destination. The token is
	// for DCR-bound registration on OAuth-gated resources, which this flow is
	// not — so pass it through only when set (a plain payout address suffices).
	chitCfg := chit.Config{
		Destination:     chit.StaticDestination{ID: cfg.MerchantID},
		ConnectionToken: cfg.ConnectionToken,
		PayeeName:       cfg.PayeeName,
	}
	// Persist DCR client credentials across restarts/instances when a DB is
	// provided. Without it, chit's default in-memory store re-registers a client
	// every boot and a stable PayeeName 409s. See internal/chitgate/store.go.
	if cfg.DB != nil {
		chitCfg.Store = newPGStore(cfg.DB)
	}
	m, err := chit.New(chitCfg)
	if err != nil {
		return nil, fmt.Errorf("chitgate: building merchant: %w", err)
	}
	adapter := &chitMerchant{
		m:          m,
		merchantID: cfg.MerchantID,
		payTo:      payToAddress(cfg.MerchantID),
		resource:   cfg.Resource,
	}
	// merchantID is passed as the gate's nominal source account (bare-402
	// requirement). The X402Gate layers on settle-before-serve + payer-signer
	// accounting; this adapter owns the chit session dance below.
	return payments.NewX402Gate(adapter, cfg.MerchantID, cfg.Policy)
}

// chitMerchant adapts a *chit.Merchant onto payments' x402Merchant shape.
type chitMerchant struct {
	m          *chit.Merchant
	merchantID string
	payTo      string // bare on-chain address (merchantID minus the "network:" prefix)
	resource   string
}

// RequirePayment implements the (unexported) payments.x402Merchant contract:
//
//	(*X402Settlement, nil, nil) → settled on-chain.
//	(nil, *ChallengeInfo, nil)  → payment required; emit the challenge.
//	(nil, nil, non-nil)         → infrastructure/settle error; fail closed.
func (c *chitMerchant) RequirePayment(ctx context.Context, req payments.X402PaymentRequest) (*payments.X402Settlement, *payments.ChallengeInfo, error) {
	resource := c.resource
	if req.Resource != "" {
		resource = req.Resource
	}
	reqs, err := c.mintRequirements(req.PriceCents, resource)
	if err != nil {
		return nil, nil, err
	}

	// Call 1: no credential yet — emit a self-minted challenge. chit is not
	// consulted here (see the package doc: this is the whole point of the fix).
	if req.Credential == "" {
		return nil, challengeFrom(reqs), nil
	}

	// Call 2: settle the presented credential. chit's Session branch skips the
	// pull /charge entirely; the requirements it settles against are the same
	// ones advertised in call 1 (recomputed above, deterministically).
	hdr := http.Header{}
	hdr.Set("X-Payment", req.Credential)
	detected := chit.DetectProtocol(hdr)
	if detected == nil {
		return nil, nil, errors.New("chitgate: presented credential is not a recognized x402 payment credential")
	}

	price, err := centsToAmount(req.PriceCents)
	if err != nil {
		return nil, nil, err
	}
	sctx := chit.SettlementContext{
		PaymentRequirements:  &reqs,
		SourceAccountID:      c.merchantID,
		DestinationAccountID: c.merchantID,
	}
	session := c.m.OpenPaymentSession(*detected, sctx)

	ch, err := c.m.RequirePayment(ctx, chit.PaymentRequest{Price: price, User: c.merchantID, Resource: resource, Session: session})
	if err != nil {
		return nil, nil, err
	}
	if ch != nil {
		// Still needs payment (credential rejected / insufficient). Re-challenge
		// with our own minted requirements — never trust a chit-built one here.
		return nil, challengeFrom(reqs), nil
	}

	// The charge is recorded against the session — settle it on-chain BEFORE we
	// report success. A failed settle is not a paid charge: fail closed. As of
	// chit's UnderpaymentError change, CloseSession also fails here when the
	// signer settled less than advertised, so this single check covers the
	// amount-safety gate without a separate SettledAmount comparison.
	if err := c.m.CloseSession(ctx, session); err != nil {
		return nil, nil, fmt.Errorf("chitgate: settlement failed: %w", err)
	}

	// The verified EIP-3009 signer — the one identity the gate accounts on.
	payer, err := chit.ExtractX402PayerAddress(req.Credential)
	if err != nil {
		// Settled, but we could not extract the signer. The gate falls back to
		// the credential's decoded signer when PayerAddress is empty, so return
		// a settlement rather than failing a charge that already landed.
		payer = ""
	}
	return &payments.X402Settlement{PayerAddress: payer}, nil, nil
}

// mintRequirements builds the x402 "exact" requirements for a pack price. Both
// call 1 (challenge) and call 2 (settle) call this with the same PriceCents, so
// the settle path presents exactly what was advertised.
func (c *chitMerchant) mintRequirements(priceCents int64, resource string) (chit.X402PaymentRequirements, error) {
	if priceCents <= 0 {
		return chit.X402PaymentRequirements{}, fmt.Errorf("chitgate: price must be positive, got %d cents", priceCents)
	}
	if c.payTo == "" {
		return chit.X402PaymentRequirements{}, errors.New("chitgate: merchant has no payout address")
	}
	return chit.X402PaymentRequirements{
		X402Version: x402Version,
		Accepts: []chit.X402PaymentOption{{
			Scheme:            x402Scheme,
			Network:           x402Network,
			Amount:            fmt.Sprintf("%d", priceCents*usdcAtomicPerCent),
			Resource:          resource,
			Description:       "gemot credit purchase",
			PayTo:             c.payTo,
			MaxTimeoutSeconds: x402MaxTimeoutSeconds,
			Asset:             usdcAssetBase,
			Extra:             map[string]any{"name": usdcEIP712Name, "version": usdcEIP712Version},
		}},
	}, nil
}

// challengeFrom wraps minted requirements in the ChallengeInfo shape the gate
// re-emits to the agent. Data mirrors chit's challenge under the "x402" key
// (paymentRequestId/chargeAmount are ATXP-native and intentionally omitted).
func challengeFrom(reqs chit.X402PaymentRequirements) *payments.ChallengeInfo {
	return &payments.ChallengeInfo{
		Code:    x402PaymentRequiredCode,
		Message: "Payment required. Sign the x402 challenge and retry with the credential.",
		Data:    map[string]any{"x402": reqs},
	}
}

// payToAddress strips the "network:" prefix from a chit MerchantID to get the
// bare on-chain payout address the x402 accepts array carries.
func payToAddress(merchantID string) string {
	if i := strings.LastIndexByte(merchantID, ':'); i >= 0 {
		return merchantID[i+1:]
	}
	return merchantID
}

// centsToAmount converts integer US cents to a chit Amount via its decimal
// string parser (0.01 USDC granularity), so 100 → "1.00", 4 → "0.04".
func centsToAmount(cents int64) (chit.Amount, error) {
	if cents <= 0 {
		return chit.Amount{}, fmt.Errorf("chitgate: price must be positive, got %d cents", cents)
	}
	return chit.ParseAmount(fmt.Sprintf("%d.%02d", cents/100, cents%100))
}
