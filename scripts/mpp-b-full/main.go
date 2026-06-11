// Manual B-full harness: drives a real Stripe SPT round-trip against the
// MPP code path used by gemot's MCP server. Issues a real HMAC-bound
// challenge via PaymentRequiredError, pauses for an SPT minted by
// @stripe/link-cli (or any other source), and settles via the same
// VerifyMCPCredential entry point production traffic hits. Prints the
// Stripe PaymentIntent ID on success.
//
// Usage:
//
//	STRIPE_SECRET_KEY=sk_test_... \
//	STRIPE_PROFILE_ID=profile_test_... \
//	GEMOT_API_SECRET=<random> \
//	go run ./scripts/mpp-b-full
//
// Standalone — does NOT need the gemot HTTP server running. Bypasses the
// quota-exhaustion gate because here we're verifying the settlement path,
// not the challenge-issuance UX (which is unit-tested).
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/justinstimatze/gemot/internal/payments"
	"github.com/stripe/stripe-go/v82"
)

func main() {
	cfg := payments.Config{
		StripeSecretKey: os.Getenv("STRIPE_SECRET_KEY"),
		StripeProfileID: os.Getenv("STRIPE_PROFILE_ID"),
		HMACSecret:      os.Getenv("GEMOT_API_SECRET"),
		Realm:           "gemot.dev",
		Currency:        "usd",
		Enabled:         true,
	}
	if cfg.StripeSecretKey == "" || cfg.StripeProfileID == "" || cfg.HMACSecret == "" {
		fmt.Fprintln(os.Stderr, "ERROR: set STRIPE_SECRET_KEY, STRIPE_PROFILE_ID, GEMOT_API_SECRET")
		os.Exit(1)
	}
	if !strings.HasPrefix(cfg.StripeSecretKey, "sk_test_") {
		fmt.Fprintln(os.Stderr, "ERROR: refusing to run against a live Stripe key — use sk_test_...")
		os.Exit(1)
	}
	if !strings.HasPrefix(cfg.StripeProfileID, "profile_test_") {
		fmt.Fprintln(os.Stderr, "ERROR: refusing to run against a live MPP profile — use profile_test_...")
		os.Exit(1)
	}
	stripe.Key = cfg.StripeSecretKey

	scope := payments.ChallengeScope{
		Tool:           "analyze",
		Action:         "run",
		Model:          "claude-sonnet-4-6",
		DeliberationID: "delib_bfull_test",
	}
	rpcErr := payments.PaymentRequiredError(cfg, scope, "B-full real SPT round-trip test")

	var data struct {
		Challenges []map[string]any `json:"challenges"`
	}
	if err := json.Unmarshal(rpcErr.Data, &data); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: decode -32042 data:", err)
		os.Exit(1)
	}
	if len(data.Challenges) == 0 {
		fmt.Fprintln(os.Stderr, "ERROR: no challenges issued — STRIPE_PROFILE_ID missing?")
		os.Exit(1)
	}
	ch := data.Challenges[0]
	fmt.Println("=== Challenge issued (HMAC-bound) ===")
	fmt.Println("  method:    ", ch["method"])
	fmt.Println("  realm:     ", ch["realm"])
	fmt.Println("  expires:   ", ch["expires"])

	reqB64, _ := ch["request"].(string)
	reqJSON, err := base64.RawURLEncoding.DecodeString(reqB64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: decode challenge request:", err)
		os.Exit(1)
	}
	var req map[string]any
	_ = json.Unmarshal(reqJSON, &req)
	fmt.Println()
	fmt.Println("=== Mint an SPT against this profile + amount ===")
	fmt.Printf("  amount:    %s %s (= $%s.%s, decimals=%v)\n",
		req["amount"], req["currency"],
		amountDollars(req["amount"]), amountCents(req["amount"]),
		req["decimals"])
	fmt.Printf("  networkId: %s\n", req["networkId"])
	fmt.Println()
	fmt.Println("To mint via @stripe/link-cli (one-time setup: `link-cli auth login`):")
	fmt.Println("  link-cli payment-methods list   # find a payment-method-id to charge")
	fmt.Printf("  link-cli spend-request create \\\n")
	fmt.Printf("    --payment-method-id csmrpd_... \\\n")
	fmt.Printf("    --credential-type shared_payment_token \\\n")
	fmt.Printf("    --network-id %s \\\n", req["networkId"])
	fmt.Printf("    --amount %s --currency %s \\\n", req["amount"], req["currency"])
	fmt.Printf("    --context \"<≥100 chars describing the purchase>\" \\\n")
	fmt.Printf("    --test --request-approval\n")
	fmt.Println("  # approve in Link app, then:")
	fmt.Println("  link-cli spend-request retrieve <lsrq_...> --include shared_payment_token")
	fmt.Println("  # the spt_... value is under shared_payment_token.id")
	fmt.Println()
	fmt.Print("Paste the SPT (spt_...) and press Enter: ")
	reader := bufio.NewReader(os.Stdin)
	spt, _ := reader.ReadString('\n')
	spt = strings.TrimSpace(spt)
	if !strings.HasPrefix(spt, "spt_") {
		fmt.Fprintln(os.Stderr, "ERROR: expected SPT starting with 'spt_'")
		os.Exit(1)
	}

	cred := map[string]any{
		"challenge": ch,
		"payload":   map[string]any{"spt": spt},
	}
	credJSON, _ := json.Marshal(cred)
	credB64 := base64.RawURLEncoding.EncodeToString(credJSON)

	meta := map[string]any{
		payments.MetaCredentialKey: credB64,
	}

	fmt.Println()
	fmt.Println("=== Settling via VerifyMCPCredential (preview API, real SPT) ===")
	rcpt, err := payments.VerifyMCPCredential(context.Background(), cfg, meta, scope)
	if err != nil {
		fmt.Fprintln(os.Stderr, "SETTLEMENT FAILED:", err)
		os.Exit(2)
	}
	fmt.Println("  status:    ", rcpt.Status)
	fmt.Println("  method:    ", rcpt.Method)
	fmt.Println("  reference: ", rcpt.Reference)
	fmt.Println("  timestamp: ", rcpt.Timestamp)
	fmt.Println()
	fmt.Println("B-full PASS — real SPT settled via preview API. Check Stripe dashboard for PaymentIntent.")
}

func amountDollars(amount any) string {
	s, _ := amount.(string)
	if len(s) <= 2 {
		return "0"
	}
	return s[:len(s)-2]
}

func amountCents(amount any) string {
	s, _ := amount.(string)
	if len(s) == 0 {
		return "00"
	}
	if len(s) == 1 {
		return "0" + s
	}
	return s[len(s)-2:]
}
