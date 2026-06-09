// Package payments implements Stripe MPP (Machine Payments Protocol) for gemot.
// Agents pay per-analyze autonomously via HTTP 402 challenges.
//
// Protocol: draft-ryan-httpauth-payment-01 (Stripe + Tempo, March 2026)
// Spec: https://mpp.dev/overview
package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/paymentintent"
)

// Config holds payment configuration.
type Config struct {
	StripeSecretKey string // Stripe secret key (sk_live_... or sk_test_...)
	StripeProfileID string // Stripe profile ID used as networkId for SPT (profile_... live, profile_test_... sandbox)
	HMACSecret      string // Secret for challenge ID generation
	Realm           string // Protection space (e.g., "gemot.dev")
	PricePerAnalyze int64  // Price in cents (e.g., 50 = $0.50)
	Currency        string // Currency code (default "usd")
	Enabled         bool   // If false, all requests pass through (dev mode)
}

// ContextKeyAPIKey is the context key for the customer API key set by middleware.
type ContextKeyAPIKey struct{}

// ContextKeyKeyID is the context key for the 8-char key identifier (agent namespace).
type ContextKeyKeyID struct{}

// ContextKeyIsAdmin is set to true when the request uses the admin secret.
type ContextKeyIsAdmin struct{}

// ContextKeySandbox is set to true for unauthenticated sandbox connections.
type ContextKeySandbox struct{}

// Middleware returns HTTP middleware that implements MPP 402 payment gating.
// It also supports traditional bearer token auth as a fallback.
// If a CreditStore is provided, customer API keys (gmt_...) are validated against it.
func Middleware(ctx context.Context, cfg Config, bearerSecret string, creditStore ...*CreditStore) func(http.Handler) http.Handler {
	var cs *CreditStore
	if len(creditStore) > 0 {
		cs = creditStore[0]
	}
	if cfg.Currency == "" {
		cfg.Currency = "usd"
	}
	if cfg.StripeSecretKey != "" {
		stripe.Key = cfg.StripeSecretKey
	}

	// Rate limit: 30 requests per minute per API key
	limiter := NewRateLimiter(ctx, 30, time.Minute)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If payments not enabled, fall back to bearer-only auth
			if !cfg.Enabled {
				// Check bearer token
				if bearerSecret != "" {
					auth := r.Header.Get("Authorization")
					token := strings.TrimPrefix(auth, "Bearer ")
					if strings.HasPrefix(auth, "Bearer ") && subtle.ConstantTimeCompare([]byte(token), []byte(bearerSecret)) == 1 {
						ctx := context.WithValue(r.Context(), ContextKeyIsAdmin{}, true)
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
					// Check customer API key
					if strings.HasPrefix(auth, "Bearer ") && cs != nil && strings.HasPrefix(token, "gmt_") {
						if valid, _ := cs.ValidateKey(token); valid {
							if !limiter.Allow(token) {
								http.Error(w, `{"error":"rate limit exceeded — max 30 requests per minute"}`, http.StatusTooManyRequests)
								return
							}
							ctx := context.WithValue(r.Context(), ContextKeyAPIKey{}, token)
							ctx = context.WithValue(ctx, ContextKeyKeyID{}, KeyID(token))
							next.ServeHTTP(w, r.WithContext(ctx))
							return
						}
					}
				}
				// No auth required in dev mode without bearer secret
				// No rate limit in dev mode — pipelines send 100+ calls/min
				if bearerSecret == "" {
					ctx := context.WithValue(r.Context(), ContextKeyIsAdmin{}, true)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// Allow unauthenticated MCP connections for sandbox mode
				// Rate-limit by IP to prevent abuse (30 req/min for sandbox)
				ip := ClientIP(r)
				if !limiter.Allow("sandbox:" + ip) {
					http.Error(w, `{"error":"rate limit exceeded for sandbox mode"}`, http.StatusTooManyRequests)
					return
				}
				ctx := context.WithValue(r.Context(), ContextKeySandbox{}, true)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Check for bearer token first (API key + credits path)
			auth := r.Header.Get("Authorization")
			if strings.HasPrefix(auth, "Bearer ") {
				token := strings.TrimPrefix(auth, "Bearer ")
				// Admin secret — unlimited access
				if bearerSecret != "" && subtle.ConstantTimeCompare([]byte(token), []byte(bearerSecret)) == 1 {
					ctx := context.WithValue(r.Context(), ContextKeyIsAdmin{}, true)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// Customer API key — validate and set in context
				if cs != nil && strings.HasPrefix(token, "gmt_") {
					if valid, _ := cs.ValidateKey(token); valid {
						if !limiter.Allow(token) {
							http.Error(w, `{"error":"rate limit exceeded — max 30 requests per minute"}`, http.StatusTooManyRequests)
							return
						}
						ctx := context.WithValue(r.Context(), ContextKeyAPIKey{}, token)
						ctx = context.WithValue(ctx, ContextKeyKeyID{}, KeyID(token))
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}

			// Check for MPP payment credential
			if strings.HasPrefix(auth, "Payment ") {
				credB64 := strings.TrimPrefix(auth, "Payment ")
				rcpt, err := verifyCredential(r.Context(), cfg, credB64)
				if err != nil {
					writePaymentError(w, cfg, "https://paymentauth.org/problems/verification-failed", err.Error())
					return
				}
				// Payment verified — set receipt header and pass through
				rcptJSON, _ := json.Marshal(rcpt)
				w.Header().Set("Payment-Receipt", base64.RawURLEncoding.EncodeToString(rcptJSON))
				next.ServeHTTP(w, r)
				return
			}

			// No valid auth — allow sandbox mode for free tools
			// Rate-limit by IP (10 req/min for sandbox)
			ip := r.RemoteAddr
			if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
				ip = strings.Split(fwd, ",")[0]
			}
			if !limiter.Allow("sandbox:" + strings.TrimSpace(ip)) {
				http.Error(w, `{"error":"rate limit exceeded for sandbox mode"}`, http.StatusTooManyRequests)
				return
			}
			ctx := context.WithValue(r.Context(), ContextKeySandbox{}, true)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// challenge is the 402 response asking the agent to pay.
type challenge struct {
	ID      string `json:"id"`
	Realm   string `json:"realm"`
	Method  string `json:"method"`
	Intent  string `json:"intent"`
	Request string `json:"request"` // base64url-encoded payment request
	Expires string `json:"expires,omitempty"`
}

type paymentRequest struct {
	Amount             string   `json:"amount"`
	Currency           string   `json:"currency"`
	Decimals           int      `json:"decimals"`
	Description        string   `json:"description"`
	PaymentMethodTypes []string `json:"paymentMethodTypes"`
}

type credential struct {
	Challenge challenge      `json:"challenge"`
	Source    string         `json:"source,omitempty"`
	Payload   map[string]any `json:"payload"`
}

type Receipt struct {
	Status    string `json:"status"`
	Method    string `json:"method"`
	Timestamp string `json:"timestamp"`
	Reference string `json:"reference"`
}

func writePaymentError(w http.ResponseWriter, cfg Config, errType, detail string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusPaymentRequired)
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"type":   errType,
		"title":  "Payment Verification Failed",
		"status": 402,
		"detail": detail,
	})
}

func generateChallengeID(secret, realm, method, intent, requestB64, expires string) string {
	msg := strings.Join([]string{realm, method, intent, requestB64, expires, "", ""}, "|")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// supportedMethods are the payment methods this server will accept on a
// credential. A credential claiming any other method is rejected even if
// the HMAC binds, since we have no way to settle it. Adding "tempo" here
// requires the Tempo charge settlement path to be implemented first.
var supportedMethods = map[string]bool{
	"stripe": true,
}

// usedChallenges tracks recently-redeemed challenge IDs to reject replays.
// Defense in depth — Stripe SPT one-time-use semantics also enforce this at
// settlement, but rejecting at our layer prevents wasted Stripe API calls
// and catches replays against other rails (e.g. future Tempo path) that
// might not provide token-level replay protection.
var (
	usedChallengesMu sync.Mutex
	usedChallenges   = make(map[string]time.Time)
)

// reserveChallengeID returns false if the challenge ID has been seen before
// within its expiry window. It records the ID and the expiry so we can prune
// the map as challenges age out (challenge IDs have a 5-minute TTL by default).
func reserveChallengeID(id, expires string) bool {
	if id == "" {
		return false
	}
	usedChallengesMu.Lock()
	defer usedChallengesMu.Unlock()

	// Prune expired entries opportunistically — bounds map growth without
	// a background goroutine.
	now := time.Now()
	for k, exp := range usedChallenges {
		if now.After(exp) {
			delete(usedChallenges, k)
		}
	}

	if _, seen := usedChallenges[id]; seen {
		return false
	}
	// Default expiry: 10 minutes from now (covers any reasonable challenge
	// TTL plus clock skew). Override with parsed expiry when available.
	exp := now.Add(10 * time.Minute)
	if expires != "" {
		if parsed, err := time.Parse(time.RFC3339, expires); err == nil {
			// Keep entry for an extra minute past expiry to defend against
			// clients retrying right at the boundary.
			exp = parsed.Add(1 * time.Minute)
		}
	}
	usedChallenges[id] = exp
	return true
}

// parseAndValidateCredential decodes a base64url credential, validates its
// HMAC bind, realm, expiry, and method, and returns the parsed credential
// + decoded payment request. This is the security-critical surface — all
// rejection logic lives here. The Stripe settlement is a separate step.
//
// Replay protection: this function reserves the challenge ID against
// reuse — callers must NOT call it twice for the same credential without
// expecting the second call to fail.
func parseAndValidateCredential(cfg Config, credB64 string) (*credential, *paymentRequest, error) {
	if cfg.HMACSecret == "" {
		return nil, nil, fmt.Errorf("server misconfigured: HMACSecret is empty")
	}

	// Decode credential
	credJSON, err := base64.RawURLEncoding.DecodeString(credB64)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed credential: %w", err)
	}

	var cred credential
	if err := json.Unmarshal(credJSON, &cred); err != nil {
		return nil, nil, fmt.Errorf("invalid credential JSON: %w", err)
	}

	// Whitelist method BEFORE HMAC check — a credential claiming an unknown
	// method has no settlement path even if the HMAC could bind, so reject
	// early with a specific error.
	if !supportedMethods[cred.Challenge.Method] {
		return nil, nil, fmt.Errorf("unsupported payment method %q", cred.Challenge.Method)
	}

	// Verify challenge HMAC (stateless verification). Per spec the canonical
	// bind sequence is: realm | method | intent | request | expires | digest | opaque.
	// digest and opaque are currently always empty for our challenges.
	expectedID := generateChallengeID(
		cfg.HMACSecret,
		cred.Challenge.Realm,
		cred.Challenge.Method,
		cred.Challenge.Intent,
		cred.Challenge.Request,
		cred.Challenge.Expires,
	)

	if subtle.ConstantTimeCompare([]byte(cred.Challenge.ID), []byte(expectedID)) != 1 {
		return nil, nil, fmt.Errorf("invalid challenge ID")
	}

	// Verify realm matches — defense in depth (HMAC bind already covers
	// realm, but an attacker reusing a credential against a server with the
	// same HMAC secret across realms would slip through without this).
	if cred.Challenge.Realm != cfg.Realm {
		return nil, nil, fmt.Errorf("realm mismatch")
	}

	// Check expiry. Per spec the expires field is optional; we treat an
	// empty expires as a misconfiguration on our challenge side and reject,
	// because indefinite credentials defeat the replay-window assumption.
	if cred.Challenge.Expires == "" {
		return nil, nil, fmt.Errorf("challenge missing expires field")
	}
	expires, err := time.Parse(time.RFC3339, cred.Challenge.Expires)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed expires field: %w", err)
	}
	if time.Now().After(expires) {
		return nil, nil, fmt.Errorf("challenge expired")
	}

	// Decode the embedded payment request — the HMAC binds the base64url
	// request bytes, so we can trust the decoded amount/currency/networkId.
	reqJSON, err := base64.RawURLEncoding.DecodeString(cred.Challenge.Request)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed request in challenge: %w", err)
	}
	var req paymentRequest
	if err := json.Unmarshal(reqJSON, &req); err != nil {
		return nil, nil, fmt.Errorf("invalid request JSON: %w", err)
	}

	// Method-specific payload validation BEFORE replay reservation. Any
	// structural failure here is a client bug, not a redemption attempt —
	// we don't want to burn the agent's challenge ID for a malformed payload.
	switch cred.Challenge.Method {
	case "stripe":
		sptRaw, ok := cred.Payload["spt"]
		if !ok {
			return nil, nil, fmt.Errorf("missing spt in payload")
		}
		if _, ok := sptRaw.(string); !ok {
			return nil, nil, fmt.Errorf("spt must be a string")
		}
	}

	// Replay protection: reserve the challenge ID. This is the LAST step
	// in validation — every check above is non-network and structural, so
	// reaching this point means the credential is well-formed and we're
	// about to attempt settlement. Two concurrent redemptions race here;
	// one wins, the other gets a replay rejection (saves a Stripe call).
	if !reserveChallengeID(cred.Challenge.ID, cred.Challenge.Expires) {
		return nil, nil, fmt.Errorf("credential already used (replay)")
	}

	return &cred, &req, nil
}

func verifyCredential(ctx context.Context, cfg Config, credB64 string) (*Receipt, error) {
	cred, _, err := parseAndValidateCredential(cfg, credB64)
	if err != nil {
		return nil, err
	}

	// SPT presence + type already validated in parseAndValidateCredential
	// (method-specific payload check before replay reservation). Type
	// assertion here is infallible.
	spt := cred.Payload["spt"].(string)

	// Create PaymentIntent with the SPT. Amount/currency come from cfg, not
	// from the credential — the HMAC binds these to the challenge we issued,
	// so they MUST match cfg.PricePerAnalyze/cfg.Currency, but we use the
	// server-side values as canonical to defend against any future drift.
	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(cfg.PricePerAnalyze),
		Currency: stripe.String(cfg.Currency),
	}
	params.Context = ctx
	params.AddExtra("shared_payment_granted_token", spt)
	params.AddExtra("confirm", "true")

	pi, err := paymentintent.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe payment failed: %w", err)
	}

	if pi.Status != stripe.PaymentIntentStatusSucceeded {
		return nil, fmt.Errorf("payment not succeeded: status=%s", pi.Status)
	}

	return &Receipt{
		Status:    "success",
		Method:    "stripe",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Reference: pi.ID,
	}, nil
}

// ClientIP extracts the real client IP, preferring Fly-Client-IP (unforgeable).
func ClientIP(r *http.Request) string {
	if ip := r.Header.Get("Fly-Client-IP"); ip != "" {
		return strings.TrimSpace(ip)
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return r.RemoteAddr
}
