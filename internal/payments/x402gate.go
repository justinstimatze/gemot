package payments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// x402gate.go — the concrete CreditGate backed by a self-custodial x402
// (bare-402) merchant. It adapts an x402 merchant (chit's *server.Merchant,
// wired at the composition root) onto the narrow CreditGate seam buy_credits
// depends on, and layers gemot's own payer controls on top.
//
// Why the abuse controls live HERE and not on the rail: on the proven bare-402
// x402 path, ATXP enforces none of its account-level protections — a
// fraud_blocked, unfunded account still settles real USDC on-chain, and the
// merchant-supplied sourceAccountId is never checked against the actual signer.
// So every control gemot wants (blocklist, spend cap, settle-rate) must key on
// the ONE unforgeable identity in the flow: payload.authorization.from, the
// EIP-3009 signer the merchant verifies at settlement. This file is where gemot
// reads that address and decides.
//
// Two ordering rules this file enforces, both learned the hard way:
//
//   - Settle before serving. The gate returns (nil, nil) — "the charge settled,
//     credit the ledger" — only after the merchant confirms settlement, never
//     optimistically.
//   - Account on the VERIFIED signer, never the decoded one. The blocklist plus
//     a read-only cap/rate projection run pre-settle on the decoded `from`
//     (safe: a blocked payer cannot forge a valid signature for a different
//     address, so the pre-check can only ever deny the attacker). But spend is
//     COMMITTED only post-settle, against the signer the merchant actually
//     verified — so presenting a credential that names a victim's address but
//     fails signature verification can never move the victim's counters.

// X402PaymentRequest is what the gate hands the x402 merchant. It is the
// gemot-native mirror of chit's server.PaymentRequest for the
// one-charge-per-call case, so this package never imports the (private) chit
// SDK — the adapter at the composition root maps between the two shapes.
type X402PaymentRequest struct {
	// PriceCents is the amount to charge, in US cents.
	PriceCents int64

	// Resource labels what is being paid for (e.g. "gemot:buy_credits").
	Resource string

	// Credential is the base64 X-PAYMENT settle credential presented by the
	// payer. Empty on the first (unpaid) call, which asks the merchant for a
	// challenge to emit rather than settling anything.
	Credential string

	// PayerAccountID is the payer's ATXP account, if any. Informational only:
	// on bare-402 x402 the payer may have no ATXP relationship at all, and the
	// value is never used for any gate — it is not the cryptographic signer.
	PayerAccountID string

	// SourceAccountID is gemot's OWN ATXP account id, passed as the nominal
	// source the bare-402 flow requires (a placeholder value 500s on chit's
	// merchant; the merchant's own id satisfies it). It is not the payer.
	SourceAccountID string
}

// X402Settlement is what the merchant returns on a confirmed, on-chain charge.
type X402Settlement struct {
	// PayerAddress is the verified EIP-3009 signer — payload.authorization.from
	// — trustworthy precisely because the merchant verified the signature and
	// settled against it. This is the identity gemot commits spend to. A
	// merchant that cannot surface it may leave it empty, in which case the gate
	// falls back to the decoded signer of the credential it just settled.
	PayerAddress string

	// TxHash is the on-chain Transfer the charge settled to, when available. It
	// makes the payment publicly verifiable with zero custody; empty if the
	// merchant does not surface it.
	TxHash string
}

// x402Merchant is the narrow slice of an x402 merchant the gate depends on.
// chit's *server.Merchant is adapted onto this at the composition root.
//
// Exactly one of the first two results is non-nil on a nil error:
//
//	(*X402Settlement, nil, nil) → settled; the charge is on-chain.
//	(nil, *ChallengeInfo, nil)  → payment required; emit the challenge.
//	(nil, nil, non-nil)         → infrastructure error; fail closed.
type x402Merchant interface {
	RequirePayment(ctx context.Context, req X402PaymentRequest) (*X402Settlement, *ChallengeInfo, error)
}

// Payer-control failures. All are fail-closed: they return before any money
// moves, and never credit.
var (
	// ErrPayerBlocked means the credential's signer is on gemot's blocklist.
	ErrPayerBlocked = errors.New("x402: payer address is blocked")

	// ErrPayerSpendCap means settling this charge would push the signer over its
	// per-window spend cap.
	ErrPayerSpendCap = errors.New("x402: payer would exceed spend cap for the window")

	// ErrPayerRateLimit means the signer has already settled the maximum number
	// of charges allowed this window.
	ErrPayerRateLimit = errors.New("x402: payer exceeded settle count for the window")
)

// PayerPolicy holds gemot's per-payer-address abuse controls for the x402 rail.
// Every limit keys on the verified EIP-3009 signer. A nil *PayerPolicy applies
// no controls — every non-blocked, well-formed credential is allowed to settle.
//
// Safe for concurrent use.
type PayerPolicy struct {
	mu           sync.Mutex
	blocked      map[string]struct{}
	capCents     int64         // 0 = no spend cap
	maxPerWindow int           // 0 = no settle-count limit
	window       time.Duration // rolling window for capCents / maxPerWindow
	buckets      map[string]*payerBucket
	now          func() time.Time // nil ⇒ time.Now
}

// payerBucket is one address's spend + count within the current window.
type payerBucket struct {
	start time.Time
	cents int64
	count int
}

// NewPayerPolicy builds a policy. blocked addresses are matched
// case-insensitively (checksummed and all-lowercase hex are the same account).
// A capCents or maxPerWindow of 0 disables that limit; window is required
// whenever either limit is set.
func NewPayerPolicy(blocked []string, capCents int64, maxPerWindow int, window time.Duration) (*PayerPolicy, error) {
	if (capCents > 0 || maxPerWindow > 0) && window <= 0 {
		return nil, errors.New("x402: PayerPolicy window must be > 0 when a spend cap or count limit is set")
	}
	set := make(map[string]struct{}, len(blocked))
	for _, a := range blocked {
		if a = normalizeAddr(a); a != "" {
			set[a] = struct{}{}
		}
	}
	return &PayerPolicy{
		blocked:      set,
		capCents:     capCents,
		maxPerWindow: maxPerWindow,
		window:       window,
		buckets:      make(map[string]*payerBucket),
	}, nil
}

func (p *PayerPolicy) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// allow runs the pre-settle, READ-ONLY checks against a decoded (not-yet
// -verified) address: blocklist, plus a projection that this charge would keep
// the address under both its spend cap and its settle count. It records
// nothing and never mutates the window state, so presenting a credential that
// names a victim's address but fails signature verification cannot move — or
// reset — the victim's counters.
func (p *PayerPolicy) allow(addr string, priceCents int64) error {
	if p == nil {
		return nil
	}
	addr = normalizeAddr(addr)
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.blocked[addr]; ok {
		return fmt.Errorf("%w: %s", ErrPayerBlocked, addr)
	}
	cents, count := p.snapshot(addr, p.clock())
	if p.capCents > 0 && cents+priceCents > p.capCents {
		return fmt.Errorf("%w: %s (%d + %d > %d cents)", ErrPayerSpendCap, addr, cents, priceCents, p.capCents)
	}
	if p.maxPerWindow > 0 && count+1 > p.maxPerWindow {
		return fmt.Errorf("%w: %s (%d settles this window, max %d)", ErrPayerRateLimit, addr, count, p.maxPerWindow)
	}
	return nil
}

// record commits a settled charge to the VERIFIED signer's window bucket. It is
// the only mutator of window state and runs only after the merchant confirms
// settlement. Caller must NOT hold p.mu.
func (p *PayerPolicy) record(addr string, priceCents int64) {
	if p == nil {
		return
	}
	addr = normalizeAddr(addr)
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.liveBucket(addr)
	b.cents += priceCents
	b.count++
}

// snapshot returns addr's current spend + count, treating an elapsed window as
// zeroed WITHOUT writing (so a read cannot reset a victim's window). Caller
// holds p.mu.
func (p *PayerPolicy) snapshot(addr string, now time.Time) (cents int64, count int) {
	b := p.buckets[addr]
	if b == nil || (p.window > 0 && now.Sub(b.start) >= p.window) {
		return 0, 0
	}
	return b.cents, b.count
}

// liveBucket returns addr's bucket, resetting it in place if its window has
// elapsed. Caller holds p.mu. Used only by record (the write path).
func (p *PayerPolicy) liveBucket(addr string) *payerBucket {
	now := p.clock()
	b := p.buckets[addr]
	if b == nil || (p.window > 0 && now.Sub(b.start) >= p.window) {
		b = &payerBucket{start: now}
		p.buckets[addr] = b
	}
	return b
}

// x402Credential is the minimal shape the gate decodes out of the base64
// X-PAYMENT settle credential. Only the signer is read here; amount, signature,
// and on-chain verification are the merchant's responsibility.
type x402Credential struct {
	Payload struct {
		Authorization struct {
			From  string `json:"from"`
			Value string `json:"value"`
		} `json:"authorization"`
	} `json:"payload"`
}

// decodeX402Signer extracts payload.authorization.from from a base64 X-PAYMENT
// credential. It validates only that the address is well-formed (0x + 40 hex);
// whether the SIGNATURE over it is valid is the merchant's job at settlement.
func decodeX402Signer(credential string) (from string, err error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(credential))
	if err != nil {
		return "", fmt.Errorf("x402: credential is not valid base64: %w", err)
	}
	var c x402Credential
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", fmt.Errorf("x402: credential is not valid JSON: %w", err)
	}
	from = strings.TrimSpace(c.Payload.Authorization.From)
	if !isHexAddress(from) {
		return "", fmt.Errorf("x402: credential payload.authorization.from is not a valid address: %q", from)
	}
	return from, nil
}

// isHexAddress reports whether s is a 20-byte 0x-prefixed hex address.
func isHexAddress(s string) bool {
	if len(s) != 42 || !strings.HasPrefix(s, "0x") {
		return false
	}
	for _, r := range s[2:] {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// normalizeAddr lowercases + trims an EIP-155 hex address for case-insensitive
// matching (checksummed and all-lowercase forms name the same account).
func normalizeAddr(a string) string {
	return strings.ToLower(strings.TrimSpace(a))
}

// X402Gate implements CreditGate over a self-custodial bare-402 x402 merchant.
type X402Gate struct {
	merchant        x402Merchant
	sourceAccountID string
	policy          *PayerPolicy // nil ⇒ no payer controls
}

// Compile-time proof the gate satisfies the seam buy_credits depends on.
var _ CreditGate = (*X402Gate)(nil)

// NewX402Gate builds the gate. sourceAccountID is gemot's OWN ATXP account id
// (the nominal source the bare-402 flow requires); policy may be nil.
func NewX402Gate(merchant x402Merchant, sourceAccountID string, policy *PayerPolicy) (*X402Gate, error) {
	if merchant == nil {
		return nil, errors.New("x402: NewX402Gate requires a merchant")
	}
	if strings.TrimSpace(sourceAccountID) == "" {
		return nil, errors.New("x402: NewX402Gate requires gemot's own source account id (bare-402 nominal source)")
	}
	return &X402Gate{merchant: merchant, sourceAccountID: sourceAccountID, policy: policy}, nil
}

// RequirePayment implements CreditGate.
//
// sourceToken carries the base64 X-PAYMENT settle credential: empty on the
// first call (⇒ return a challenge to emit), present on the paying call (⇒
// gate, settle, credit). accountID is the payer's optional ATXP account,
// informational only — it is never gated on, since it is not the cryptographic
// signer.
func (g *X402Gate) RequirePayment(ctx context.Context, priceCents int64, accountID, sourceToken, resource string) (*ChallengeInfo, error) {
	if priceCents <= 0 {
		return nil, fmt.Errorf("x402: price must be positive, got %d", priceCents)
	}
	credential := strings.TrimSpace(sourceToken)

	// First leg: no credential yet — ask the merchant for a challenge to emit.
	if credential == "" {
		settled, ch, err := g.merchant.RequirePayment(ctx, X402PaymentRequest{
			PriceCents:      priceCents,
			Resource:        resource,
			PayerAccountID:  accountID,
			SourceAccountID: g.sourceAccountID,
		})
		if err != nil {
			return nil, err
		}
		if settled != nil {
			// A merchant that claims settlement with no credential presented is
			// misbehaving; do not silently credit on it. Fail closed.
			return nil, errors.New("x402: merchant reported settlement with no credential presented")
		}
		return ch, nil
	}

	// Second leg: a credential is present. Decode the signer and run the
	// pre-settle, read-only payer checks BEFORE spending anything to settle.
	from, err := decodeX402Signer(credential)
	if err != nil {
		return nil, err
	}
	if err := g.policy.allow(from, priceCents); err != nil {
		return nil, err
	}

	// Settle before serving — the merchant verifies the signature, the amount,
	// and the on-chain Transfer. Only a confirmed settlement lets us credit.
	settled, ch, err := g.merchant.RequirePayment(ctx, X402PaymentRequest{
		PriceCents:      priceCents,
		Resource:        resource,
		Credential:      credential,
		PayerAccountID:  accountID,
		SourceAccountID: g.sourceAccountID,
	})
	if err != nil {
		return nil, err
	}
	if ch != nil {
		// Merchant still wants payment (e.g. it rejected the credential). Emit
		// the challenge; do not credit.
		return ch, nil
	}
	if settled == nil {
		return nil, errors.New("x402: merchant returned neither settlement nor challenge")
	}

	// The address gated on must be the one the merchant actually verified. When
	// the merchant surfaces the signer, a mismatch means the decoded and settled
	// signers disagree — refuse rather than credit an unvetted payer. When it
	// does not, the credential we decoded is the one that just settled, so its
	// decoded signer is the verified signer.
	verified := normalizeAddr(settled.PayerAddress)
	switch {
	case verified == "":
		verified = normalizeAddr(from)
	case verified != normalizeAddr(from):
		return nil, fmt.Errorf("x402: settled signer %q does not match credential signer %q",
			settled.PayerAddress, from)
	}

	// Commit spend to the verified signer, then signal "settled — credit".
	g.policy.record(verified, priceCents)
	return nil, nil
}

// decodeX402Nonce extracts payload.authorization.nonce from a base64 X-PAYMENT
// credential — the EIP-3009 authorization nonce, unique per signed authorization
// and consumed on-chain at settlement. buy_credits keys its once-only credit on
// this so a concurrent double-present, an AlreadySettled retry, or a re-encoded
// copy of the same authorization all credit exactly once.
func decodeX402Nonce(credential string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(credential))
	if err != nil {
		return "", fmt.Errorf("x402: credential is not valid base64: %w", err)
	}
	var c struct {
		Payload struct {
			Authorization struct {
				Nonce string `json:"nonce"`
			} `json:"authorization"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", fmt.Errorf("x402: credential is not valid JSON: %w", err)
	}
	nonce := strings.TrimSpace(c.Payload.Authorization.Nonce)
	if nonce == "" {
		return "", errors.New("x402: credential authorization has no nonce")
	}
	return nonce, nil
}
