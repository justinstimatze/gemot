// Package chitgate is the composition-root adapter binding gemot's CreditGate
// to a chit bare-402 x402 merchant. It lives outside internal/payments on
// purpose: payments must stay free of the chit import so its gate/policy stay
// unit-testable against a fake merchant, and the on-chain seam — which can only
// be validated live — is isolated here.
//
// It orchestrates chit's two-phase bare-402 flow behind the single-call
// x402Merchant shape payments.X402Gate expects:
//
//	call 1 (no credential): Merchant.RequirePayment → a 402 Challenge to emit.
//	call 2 (credential):    DetectProtocol → OpenPaymentSession → RequirePayment
//	                        (charges the session) → CloseSession (settles
//	                        on-chain) → the verified payer address.
//
// The subtlety chit's own x402stranger example calls out: a session's settle
// needs the SAME X402PaymentRequirements advertised in call 1's challenge, and
// chit has no exported way to rebuild it. So call 1's requirements are cached
// and replayed on call 2 — keyed by the authenticated gemot caller (ctx), which
// is stable across the two-call exchange, rather than the example's RemoteAddr.
package chitgate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	chit "github.com/justinstimatze/chit/server"
	"github.com/justinstimatze/gemot/internal/payments"
)

// pendingTTL bounds how long a call-1 challenge waits for its call-2 credential
// before it is pruned. Generous enough for a human-paced pay-and-retry, tight
// enough that abandoned challenges don't accumulate.
const pendingTTL = 15 * time.Minute

// Config configures the chit-backed merchant gate. Only MerchantID is required.
// ConnectionToken is optional (see the field) and, when set, is a wallet-grade
// secret that must come from the environment, never source.
type Config struct {
	// MerchantID is chit's "network:address" account id, e.g.
	// "base:0x52440B7EF75B9329b84Fed88061e5665767b409B". It doubles as the
	// destination and the nominal sourceAccountId (never checked against the
	// real payer — abuse controls key on the credential's signer instead).
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
		resource:   cfg.Resource,
		pending:    map[string]pendingChallenge{},
	}
	// merchantID is passed as the gate's nominal source account (bare-402
	// requirement). The X402Gate layers on settle-before-serve + payer-signer
	// accounting; this adapter owns the chit session dance below.
	return payments.NewX402Gate(adapter, cfg.MerchantID, cfg.Policy)
}

// pendingChallenge is a call-1 challenge awaiting its call-2 credential.
type pendingChallenge struct {
	reqs      chit.X402PaymentRequirements
	paymentID string
	at        time.Time
}

// chitMerchant adapts a *chit.Merchant onto payments' x402Merchant shape.
type chitMerchant struct {
	m          *chit.Merchant
	merchantID string
	resource   string

	mu      sync.Mutex
	pending map[string]pendingChallenge
}

// RequirePayment implements the (unexported) payments.x402Merchant contract:
//
//	(*X402Settlement, nil, nil) → settled on-chain.
//	(nil, *ChallengeInfo, nil)  → payment required; emit the challenge.
//	(nil, nil, non-nil)         → infrastructure/settle error; fail closed.
func (c *chitMerchant) RequirePayment(ctx context.Context, req payments.X402PaymentRequest) (*payments.X402Settlement, *payments.ChallengeInfo, error) {
	price, err := centsToAmount(req.PriceCents)
	if err != nil {
		return nil, nil, err
	}
	corr := c.correlationKey(ctx, req)
	resource := c.resource
	if req.Resource != "" {
		resource = req.Resource
	}

	// Call 1: no credential yet — ask chit for a challenge to emit.
	if req.Credential == "" {
		ch, err := c.m.RequirePayment(ctx, chit.PaymentRequest{Price: price, User: c.merchantID, Resource: resource})
		if err != nil {
			return nil, nil, err
		}
		if ch == nil {
			// chit reported paid with no credential presented. On the bare-402
			// x402 path this should not happen; do not fabricate a settlement.
			return nil, nil, errors.New("chitgate: merchant reported settlement with no credential presented")
		}
		c.remember(corr, ch)
		return nil, toChallengeInfo(ch), nil
	}

	// Call 2: settle the presented credential.
	hdr := http.Header{}
	hdr.Set("X-Payment", req.Credential)
	detected := chit.DetectProtocol(hdr)
	if detected == nil {
		return nil, nil, errors.New("chitgate: presented credential is not a recognized x402 payment credential")
	}

	sctx := chit.SettlementContext{SourceAccountID: c.merchantID, DestinationAccountID: c.merchantID}
	if pend, ok := c.recall(corr); ok {
		sctx.PaymentRequirements = &pend.reqs
		sctx.PaymentRequestID = pend.paymentID
	}
	session := c.m.OpenPaymentSession(*detected, sctx)

	ch, err := c.m.RequirePayment(ctx, chit.PaymentRequest{Price: price, User: c.merchantID, Resource: resource, Session: session})
	if err != nil {
		return nil, nil, err
	}
	if ch != nil {
		// Still needs payment (credential rejected / insufficient). Re-challenge.
		c.remember(corr, ch)
		return nil, toChallengeInfo(ch), nil
	}

	// The charge is recorded against the session — settle it on-chain BEFORE we
	// report success. A failed settle is not a paid charge: fail closed.
	if err := c.m.CloseSession(ctx, session); err != nil {
		return nil, nil, fmt.Errorf("chitgate: settlement failed: %w", err)
	}
	c.forget(corr)

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

// correlationKey ties call 1 (challenge) to call 2 (credential). The
// authenticated gemot caller is stable across both; fall back to the payer's
// ATXP account, then a single shared slot for the unauthenticated stdio case.
func (c *chitMerchant) correlationKey(ctx context.Context, req payments.X402PaymentRequest) string {
	if k, _ := ctx.Value(payments.ContextKeyKeyID{}).(string); k != "" {
		return k
	}
	if req.PayerAccountID != "" {
		return req.PayerAccountID
	}
	return "default"
}

func (c *chitMerchant) remember(corr string, ch *chit.Challenge) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked()
	c.pending[corr] = pendingChallenge{
		reqs:      ch.X402,
		paymentID: fmt.Sprint(ch.Data["paymentRequestId"]),
		at:        nowFn(),
	}
}

func (c *chitMerchant) recall(corr string) (pendingChallenge, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pending[corr]
	if ok && nowFn().Sub(p.at) >= pendingTTL {
		delete(c.pending, corr)
		return pendingChallenge{}, false
	}
	return p, ok
}

func (c *chitMerchant) forget(corr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, corr)
}

// pruneLocked drops expired pending challenges. Caller holds c.mu.
func (c *chitMerchant) pruneLocked() {
	now := nowFn()
	for k, p := range c.pending {
		if now.Sub(p.at) >= pendingTTL {
			delete(c.pending, k)
		}
	}
}

// nowFn is time.Now, indirected for tests.
var nowFn = time.Now

func toChallengeInfo(ch *chit.Challenge) *payments.ChallengeInfo {
	return &payments.ChallengeInfo{Code: ch.Code, Message: ch.Message, Data: ch.Data}
}

// centsToAmount converts integer US cents to a chit Amount via its decimal
// string parser (0.01 USDC granularity), so 100 → "1.00", 4 → "0.04".
func centsToAmount(cents int64) (chit.Amount, error) {
	if cents <= 0 {
		return chit.Amount{}, fmt.Errorf("chitgate: price must be positive, got %d cents", cents)
	}
	return chit.ParseAmount(fmt.Sprintf("%d.%02d", cents/100, cents%100))
}
