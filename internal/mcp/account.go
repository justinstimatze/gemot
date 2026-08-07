package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/justinstimatze/gemot/internal/payments"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// accountParams is the argument shape for the `account` tool.
type accountParams struct {
	Action string `json:"action"`

	// Pack is the credit pack to buy (case-insensitive); empty defaults to the
	// Starter pack, matching the Stripe checkout default.
	Pack string `json:"pack,omitempty"`

	// ATXPAccountID is the payer's ATXP account, if any. Informational only —
	// on the bare-402 x402 rail the payer may have no ATXP relationship, and
	// gemot never gates on it (it is not the cryptographic signer).
	ATXPAccountID string `json:"atxp_account_id,omitempty"`

	// PaymentCredential is the base64 X-PAYMENT settle credential (the signed
	// EIP-3009 authorization). Empty on the first call — which returns a
	// payment-required challenge to pay against — and present on the paying call.
	PaymentCredential string `json:"payment_credential,omitempty"`
}

// handleAccount serves the `account` tool: self-service credit funding over the
// ATXP / x402 rail. It is the MCP-native counterpart to the Stripe web
// checkout — pay in-band, and the caller's OWN gemot key is topped up.
//
// It is fail-closed at every step: no gate configured, no credit store, or no
// authenticated key all refuse before any charge. A required payment is NOT an
// error to the agent — it is the invoice: the handler returns the challenge as a
// structured result (status:"payment_required") for the agent to satisfy and
// retry with the credential. Credits are added ONLY on a settled charge.
func (s *server) handleAccount(ctx context.Context, _ *sdkmcp.CallToolRequest, args accountParams) (*sdkmcp.CallToolResult, any, error) {
	switch args.Action {
	case "buy_credits":
		if s.gate == nil {
			return errResult(fmt.Errorf("buy_credits is not configured on this server (no payment gate)"))
		}
		if s.credits == nil {
			return errResult(fmt.Errorf("buy_credits is unavailable in demo mode (no credit store)"))
		}
		apiKey, _ := ctx.Value(payments.ContextKeyAPIKey{}).(string)
		if apiKey == "" {
			return errResult(fmt.Errorf("buy_credits requires an authenticated gemot API key — that key's balance is what gets topped up"))
		}

		res, err := payments.BuyCredits(ctx, s.gate, s.credits, args.Pack, apiKey, args.ATXPAccountID, args.PaymentCredential)
		if err != nil {
			var pr *payments.ErrPaymentRequired
			if errors.As(err, &pr) && pr.Challenge != nil {
				// The invoice, not a failure — surface it so the agent can pay
				// and retry with payment_credential set.
				return jsonResult(map[string]any{
					"status":    "payment_required",
					"code":      pr.Challenge.Code,
					"message":   pr.Challenge.Message,
					"challenge": pr.Challenge.Data,
				})
			}
			return errResult(err)
		}

		s.audit(ctx, "account:buy_credits", "", "")
		return jsonResultWithHints(res, "Credits added. Spend them with analyze:run (Sonnet / Opus / Haiku).")

	default:
		return errResult(fmt.Errorf("unknown action %q — use: buy_credits", args.Action))
	}
}
