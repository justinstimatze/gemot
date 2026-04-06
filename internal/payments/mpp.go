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
	"time"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/paymentintent"
)

// Config holds payment configuration.
type Config struct {
	StripeSecretKey string // Stripe secret key (sk_live_... or sk_test_...)
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
				if bearerSecret == "" {
					next.ServeHTTP(w, r)
					return
				}
				// Allow unauthenticated MCP connections for sandbox mode
				// Rate-limit by IP to prevent abuse (10 req/min for sandbox)
				ip := clientIP(r)
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
				receipt, err := verifyCredential(r.Context(), cfg, credB64)
				if err != nil {
					writePaymentError(w, cfg, "https://paymentauth.org/problems/verification-failed", err.Error())
					return
				}
				// Payment verified — set receipt header and pass through
				receiptJSON, _ := json.Marshal(receipt)
				w.Header().Set("Payment-Receipt", base64.RawURLEncoding.EncodeToString(receiptJSON))
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

type receipt struct {
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

func verifyCredential(ctx context.Context, cfg Config, credB64 string) (*receipt, error) {
	// Decode credential
	credJSON, err := base64.RawURLEncoding.DecodeString(credB64)
	if err != nil {
		return nil, fmt.Errorf("malformed credential: %w", err)
	}

	var cred credential
	if err := json.Unmarshal(credJSON, &cred); err != nil {
		return nil, fmt.Errorf("invalid credential JSON: %w", err)
	}

	// Verify challenge HMAC (stateless verification)
	expectedID := generateChallengeID(
		cfg.HMACSecret,
		cred.Challenge.Realm,
		cred.Challenge.Method,
		cred.Challenge.Intent,
		cred.Challenge.Request,
		cred.Challenge.Expires,
	)

	if subtle.ConstantTimeCompare([]byte(cred.Challenge.ID), []byte(expectedID)) != 1 {
		return nil, fmt.Errorf("invalid challenge ID")
	}

	// Check expiry
	if cred.Challenge.Expires != "" {
		expires, err := time.Parse(time.RFC3339, cred.Challenge.Expires)
		if err == nil && time.Now().After(expires) {
			return nil, fmt.Errorf("challenge expired")
		}
	}

	// Verify realm matches
	if cred.Challenge.Realm != cfg.Realm {
		return nil, fmt.Errorf("realm mismatch")
	}

	// Extract SPT from payload
	sptRaw, ok := cred.Payload["spt"]
	if !ok {
		return nil, fmt.Errorf("missing spt in payload")
	}
	spt, ok := sptRaw.(string)
	if !ok {
		return nil, fmt.Errorf("spt must be a string")
	}

	// Decode the payment request to get amount/currency
	reqJSON, err := base64.RawURLEncoding.DecodeString(cred.Challenge.Request)
	if err != nil {
		return nil, fmt.Errorf("malformed request in challenge: %w", err)
	}
	var req paymentRequest
	if err := json.Unmarshal(reqJSON, &req); err != nil {
		return nil, fmt.Errorf("invalid request JSON: %w", err)
	}

	// Create PaymentIntent with the SPT
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

	return &receipt{
		Status:    "success",
		Method:    "stripe",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Reference: pi.ID,
	}, nil
}

// clientIP extracts the real client IP, preferring Fly-Client-IP (unforgeable).
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("Fly-Client-IP"); ip != "" {
		return strings.TrimSpace(ip)
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return r.RemoteAddr
}
