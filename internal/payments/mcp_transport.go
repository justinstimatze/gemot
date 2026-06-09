// Package payments — MPP-over-MCP transport.
//
// Per mpp.dev/protocol/transports/mcp: when a paid tool is invoked without
// a valid credential, the server returns JSON-RPC error code -32042 with a
// challenges array under error.data. Credentials arrive in the tool call
// params under _meta["org.paymentauth/credential"]; receipts are returned
// in the tool result under _meta["org.paymentauth/receipt"].
package payments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// JSON-RPC error code for "402 Payment Required" per MPP-MCP transport spec.
const MPPErrorCode = -32042

// _meta keys per MPP spec — lowercase per the canonical key form in
// org.paymentauth/<verb>; do NOT capitalize the wire string even though
// the Go Receipt type is exported.
const (
	MetaCredentialKey = "org.paymentauth/credential"
	MetaReceiptKey    = "org.paymentauth/receipt"
)

// PaymentRequiredError builds the -32042 JSON-RPC error advertising the
// payment methods this server accepts. Stripe SPT is included whenever
// StripeProfileID is set; the Tempo charge method is included unconditionally
// once we wire on-chain settlement (TODO: add when Tempo path lands).
func PaymentRequiredError(cfg Config, description string) *jsonrpc.Error {
	now := time.Now().UTC()
	expires := now.Add(5 * time.Minute).Format(time.RFC3339)

	var challenges []challenge

	if cfg.StripeProfileID != "" {
		c, err := buildChallenge(cfg, "stripe", description, expires, map[string]any{
			"amount":             fmt.Sprintf("%d", cfg.PricePerAnalyze),
			"currency":           cfg.Currency,
			"decimals":           2,
			"description":        description,
			"paymentMethodTypes": []string{"card", "link"},
			"networkId":          cfg.StripeProfileID,
		})
		if err == nil {
			challenges = append(challenges, c)
		}
	}

	return &jsonrpc.Error{
		Code:    MPPErrorCode,
		Message: "Payment Required",
		Data: mustMarshalRaw(map[string]any{
			"httpStatus": 402,
			"type":       "https://paymentauth.org/problems/payment-required",
			"title":      "Payment Required",
			"detail":     description,
			"challenges": challenges,
		}),
	}
}

// buildChallenge constructs an MPP challenge for one payment method.
// The id is HMAC-bound to the canonical sequence per mpp.dev/protocol/challenges:
// realm | method | intent | request | expires | digest | opaque
func buildChallenge(cfg Config, method, description, expires string, request map[string]any) (challenge, error) {
	reqJSON, err := json.Marshal(request)
	if err != nil {
		return challenge{}, err
	}
	requestB64 := base64.RawURLEncoding.EncodeToString(reqJSON)
	id := generateChallengeID(cfg.HMACSecret, cfg.Realm, method, "charge", requestB64, expires)
	return challenge{
		ID:      id,
		Realm:   cfg.Realm,
		Method:  method,
		Intent:  "charge",
		Request: requestB64,
		Expires: expires,
	}, nil
}

// VerifyMCPCredential extracts org.paymentauth/credential from tool call _meta,
// verifies it (HMAC bind + Stripe SPT settlement), and returns the Receipt.
// Returns (nil, nil) when no credential present — caller decides whether to
// 402 or fall through. Returns (nil, error) when credential is malformed or
// fails verification.
func VerifyMCPCredential(ctx context.Context, cfg Config, meta map[string]any) (*Receipt, error) {
	if meta == nil {
		return nil, nil
	}
	raw, ok := meta[MetaCredentialKey]
	if !ok {
		return nil, nil
	}

	// _meta values arrive as decoded JSON — could be a string (base64url-
	// encoded credential, matching the HTTP Authorization: Payment shape)
	// or an object (the credential decoded inline). Support both.
	var credB64 string
	switch v := raw.(type) {
	case string:
		credB64 = v
	case map[string]any:
		credJSON, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("re-marshal credential: %w", err)
		}
		credB64 = base64.RawURLEncoding.EncodeToString(credJSON)
	default:
		return nil, fmt.Errorf("credential must be string or object, got %T", raw)
	}

	return verifyCredential(ctx, cfg, credB64)
}

// ReceiptMeta returns a _meta map with the Receipt under the canonical key,
// suitable for assignment to CallToolResult.Meta after a successful paid call.
func ReceiptMeta(r *Receipt) map[string]any {
	return map[string]any{
		MetaReceiptKey: r,
	}
}

func mustMarshalRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(b)
}
