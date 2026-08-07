package payments

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/checkout/session"
	"github.com/stripe/stripe-go/v82/webhook"
)

// CreditPack defines a purchasable credit pack.
type CreditPack struct {
	Name     string
	Credits  int
	PriceUSD int64 // in cents
}

var CreditPacks = []CreditPack{
	{Name: "Starter", Credits: PackStarter, PriceUSD: 500},
	{Name: "Standard", Credits: PackStandard, PriceUSD: 2000},
	{Name: "Pro", Credits: PackPro, PriceUSD: 5000},
	// Micro: a sub-dollar top-up ($0.10 → 100000 USDC atomic units) for agents
	// paying over the x402/USDC rail. It sits below Stripe's 50c SPT floor, so
	// CheckoutHandler rejects it for card checkout — it is x402/buy_credits only.
	{Name: "Micro", Credits: 10, PriceUSD: 10},
}

// CheckoutHandler creates a Stripe Checkout session for purchasing credits.
// GET /checkout?pack=starter|standard|pro
func CheckoutHandler(store *CreditStore, baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		packName := r.URL.Query().Get("pack")
		email := r.URL.Query().Get("email") // optional — Stripe Checkout collects it if empty

		var pack *CreditPack
		for i := range CreditPacks {
			if CreditPacks[i].Name == packName || (packName == "" && i == 0) {
				pack = &CreditPacks[i]
				break
			}
		}
		// Case-insensitive fallback
		if pack == nil {
			for i := range CreditPacks {
				if strings.EqualFold(packName, CreditPacks[i].Name) {
					pack = &CreditPacks[i]
					break
				}
			}
		}
		if pack == nil {
			http.Error(w, `{"error":"invalid pack — use starter, standard, or pro"}`, http.StatusBadRequest)
			return
		}
		// Sub-floor packs (below Stripe's card minimum) are x402/USDC-only: they
		// exist for buy_credits over the ATXP rail, and Stripe would reject a
		// session below its 50c minimum anyway.
		if pack.PriceUSD < MPPSPTMinimumCents {
			http.Error(w, `{"error":"this pack is available only via buy_credits (x402/USDC), not card checkout"}`, http.StatusBadRequest)
			return
		}

		params := &stripe.CheckoutSessionParams{
			Mode: stripe.String(string(stripe.CheckoutSessionModePayment)),
			LineItems: []*stripe.CheckoutSessionLineItemParams{
				{
					PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
						Currency:   stripe.String("usd"),
						UnitAmount: stripe.Int64(pack.PriceUSD),
						ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
							Name:        stripe.String(fmt.Sprintf("Gemot %s — %d credits", pack.Name, pack.Credits)),
							Description: stripe.String(fmt.Sprintf("%d credits for Gemot deliberation analysis", pack.Credits)),
						},
					},
					Quantity: stripe.Int64(1),
				},
			},
			SuccessURL: stripe.String(baseURL + "/checkout/success?session_id={CHECKOUT_SESSION_ID}"),
			CancelURL:  stripe.String(baseURL + "/checkout/cancel"),
			ConsentCollection: &stripe.CheckoutSessionConsentCollectionParams{
				TermsOfService: stripe.String("required"),
			},
			CustomText: &stripe.CheckoutSessionCustomTextParams{
				TermsOfServiceAcceptance: &stripe.CheckoutSessionCustomTextTermsOfServiceAcceptanceParams{
					Message: stripe.String("I agree to the [Terms of Service](https://gemot.dev/terms) and [Privacy Policy](https://gemot.dev/privacy)"),
				},
			},
		}
		// Let Stripe Checkout collect the email; only pre-fill if provided
		if email != "" {
			params.CustomerEmail = stripe.String(email)
		}
		params.AddMetadata("credits", fmt.Sprintf("%d", pack.Credits))
		params.AddMetadata("pack", pack.Name)
		if email != "" {
			params.AddMetadata("email", email)
		}

		s, err := session.New(params)
		if err != nil {
			slog.Error("stripe checkout error", "error", err)
			http.Error(w, `{"error":"failed to create checkout session"}`, http.StatusInternalServerError)
			return
		}

		// nosemgrep: go.lang.security.injection.open-redirect.open-redirect -- s.URL is the Stripe-issued Checkout Session URL from the Stripe API, not user input
		http.Redirect(w, r, s.URL, http.StatusSeeOther)
	}
}

// WebhookHandler processes Stripe webhook events.
// POST /webhook/stripe
func WebhookHandler(store *CreditStore) http.HandlerFunc {
	webhookSecret := os.Getenv("STRIPE_WEBHOOK_SECRET")

	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 65536))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		var event stripe.Event

		if webhookSecret == "" {
			slog.Error("webhook: STRIPE_WEBHOOK_SECRET not set, rejecting event")
			http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
			return
		}
		event, err = webhook.ConstructEvent(body, r.Header.Get("Stripe-Signature"), webhookSecret)
		if err != nil {
			slog.Error("webhook signature verification failed", "error", err)
			http.Error(w, "invalid signature", http.StatusBadRequest)
			return
		}

		if event.Type == "checkout.session.completed" {
			var sess stripe.CheckoutSession
			if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
				slog.Error("webhook parse error", "error", err)
				http.Error(w, "parse error", http.StatusBadRequest)
				return
			}

			email := sess.CustomerEmail
			if email == "" && sess.CustomerDetails != nil {
				email = sess.CustomerDetails.Email
			}
			credits := 0
			if c, ok := sess.Metadata["credits"]; ok {
				_, _ = fmt.Sscanf(c, "%d", &credits)
			}
			customerID := ""
			if sess.Customer != nil {
				customerID = sess.Customer.ID
			}

			// Verify payment amount matches claimed credits
			expectedAmount := int64(0)
			for _, p := range CreditPacks {
				if fmt.Sprintf("%d", p.Credits) == sess.Metadata["credits"] {
					expectedAmount = p.PriceUSD
					break
				}
			}
			if expectedAmount > 0 && sess.AmountTotal != expectedAmount {
				slog.Warn("webhook: amount mismatch", "session_id", sess.ID, "got", sess.AmountTotal, "expected", expectedAmount)
				w.WriteHeader(http.StatusOK) // ACK to Stripe but don't provision
				return
			}

			if email != "" && credits > 0 {
				// Check idempotency — skip if this session was already processed
				var existingKey string
				err := store.db.QueryRow("SELECT key FROM api_keys WHERE stripe_session_id = $1", sess.ID).Scan(&existingKey)
				if err == nil {
					// Already processed
					keyPreview := existingKey
					if len(keyPreview) > 12 {
						keyPreview = keyPreview[:12]
					}
					slog.Info("webhook: session already processed", "session_id", sess.ID, "key_preview", keyPreview)
					w.WriteHeader(http.StatusOK)
					return
				}

				// Try to add to existing key, or create new one
				key, balance, err := store.AddCreditsByEmail(email, credits, sess.ID)
				if err != nil {
					// No existing key — create one
					key, err = store.GenerateKey(email, customerID, sess.ID, credits)
					if err != nil {
						slog.Error("failed to create api key", "error", err)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					_ = credits // balance tracked in DB
					slog.Info("new api key created", "email", email, "key_prefix", key[:12]+"...", "credits", credits)
				} else {
					slog.Info("credits added", "email", email, "added", credits, "balance", balance, "key_prefix", key[:12]+"...")
				}
			}
		}

		w.WriteHeader(http.StatusOK)
	}
}

// SuccessHandler shows the API key after successful purchase.
// It provisions the key inline if the webhook hasn't fired yet (race condition fix).
// GET /checkout/success?session_id=cs_...
func SuccessHandler(store *CreditStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			http.Error(w, "missing session_id", http.StatusBadRequest)
			return
		}

		// Look up the session to get the email and payment status
		slog.Info("success page accessed", "session_prefix", sessionID[:min(len(sessionID), 20)]+"...", "remote_addr", r.RemoteAddr)
		s, err := session.Get(sessionID, nil)
		if err != nil {
			slog.Warn("success page: invalid session", "session_prefix", sessionID[:min(len(sessionID), 20)]+"...", "remote_addr", r.RemoteAddr)
			http.Error(w, "invalid session", http.StatusBadRequest)
			return
		}

		email := s.CustomerEmail
		if email == "" && s.CustomerDetails != nil {
			email = s.CustomerDetails.Email
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		// If session is not paid yet, show processing message
		if s.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
			_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Gemot — Processing</title>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
</head>
<body style="font-family:'Inter',system-ui,sans-serif;max-width:600px;margin:4rem auto;padding:1.5rem;background:#fafaf8;color:#0f172a;">
<h1 style="font-size:1.5rem;font-weight:700;margin-bottom:1rem;">Payment processing...</h1>
<p style="color:#64748b;margin-bottom:1rem;">Your payment is still being processed. Please refresh this page in a few seconds.</p>
<p><a href="https://gemot.dev" style="color:#4f46e5;">Return to gemot.dev</a></p>
</body></html>`)
			return
		}

		// Check for existing key for this email
		var key string
		var balance int
		err = store.db.QueryRow(
			`SELECT key, credits_remaining FROM api_keys WHERE email = $1 ORDER BY created_at DESC LIMIT 1`,
			email,
		).Scan(&key, &balance)

		if err != nil && email != "" {
			// Webhook hasn't fired yet — provision the key inline
			credits := 0
			if c, ok := s.Metadata["credits"]; ok {
				_, _ = fmt.Sscanf(c, "%d", &credits)
			}
			if credits > 0 {
				customerID := ""
				if s.Customer != nil {
					customerID = s.Customer.ID
				}
				key, err = store.GenerateKey(email, customerID, s.ID, credits)
				if err != nil {
					slog.Error("success handler: failed to create key", "email", email, "error", err)
				} else {
					balance = credits
					slog.Info("success handler: provisioned key inline", "email", email)
				}
			}
		}

		if key == "" {
			_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Gemot — Purchase Complete</title>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
</head>
<body style="font-family:'Inter',system-ui,sans-serif;max-width:600px;margin:4rem auto;padding:1.5rem;background:#fafaf8;color:#0f172a;">
<h1 style="font-size:1.5rem;font-weight:700;color:#059669;margin-bottom:1rem;">Payment received!</h1>
<p style="color:#64748b;margin-bottom:1rem;">Your API key is being provisioned. If you don't receive it within a few minutes, email <a href="mailto:justin@gemot.dev" style="color:#4f46e5;">justin@gemot.dev</a>.</p>
<p><a href="https://gemot.dev" style="color:#4f46e5;">Return to gemot.dev</a></p>
</body></html>`)
			return
		}

		_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Gemot — Your API Key</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>
  *{margin:0;padding:0;box-sizing:border-box;}
  body{font-family:'Inter',-apple-system,BlinkMacSystemFont,'Segoe UI',system-ui,sans-serif;background:#fafaf8;color:#0f172a;line-height:1.6;}
  .container{max-width:640px;margin:0 auto;padding:3rem 1.5rem;}
  a{color:#4f46e5;text-decoration:none;} a:hover{color:#4338ca;}
  code{font-family:'SF Mono',Monaco,'Cascadia Code',monospace;font-size:0.85em;background:#f1f5f9;padding:0.15rem 0.4rem;border-radius:4px;color:#334155;}
  h1{font-size:1.8rem;font-weight:700;color:#059669;margin-bottom:1.5rem;}
  .key-box{background:#f8fafc;border:1px solid #e2e8f0;border-radius:12px;padding:1.25rem;margin:1.5rem 0;position:relative;}
  .key-label{font-size:0.7rem;text-transform:uppercase;letter-spacing:0.08em;color:#94a3b8;margin-bottom:0.5rem;font-weight:600;}
  .key-value{font-family:'SF Mono',Monaco,'Cascadia Code',monospace;font-size:0.85rem;color:#0f172a;word-break:break-all;line-height:1.4;}
  .copy-btn{position:absolute;top:1rem;right:1rem;background:#4f46e5;color:#fff;border:none;padding:0.4rem 0.8rem;border-radius:6px;font-size:0.8rem;font-weight:600;cursor:pointer;transition:background 0.15s;}
  .copy-btn:hover{background:#4338ca;}
  .copy-btn.copied{background:#059669;}
  .credits{display:inline-block;background:#ecfdf5;color:#047857;padding:0.3rem 0.8rem;border-radius:999px;font-size:0.85rem;font-weight:600;margin:0.5rem 0;border:1px solid #bbf7d0;}
  .snippet{background:#f8fafc;border:1px solid #e2e8f0;border-radius:8px;padding:1rem;margin:0.75rem 0;font-size:0.8rem;overflow-x:auto;}
  .snippet-label{font-size:0.65rem;text-transform:uppercase;letter-spacing:0.06em;color:#94a3b8;margin-bottom:0.25rem;font-weight:600;}
  .snippet pre{font-family:'SF Mono',Monaco,monospace;color:#334155;white-space:pre-wrap;word-break:break-all;}
  h2{font-size:1.1rem;font-weight:600;color:#0f172a;margin:2rem 0 0.75rem;}
  .step{display:flex;gap:0.75rem;margin:0.5rem 0;align-items:flex-start;}
  .step-num{background:#eef2ff;color:#4f46e5;width:1.5rem;height:1.5rem;border-radius:50%%;font-size:0.75rem;font-weight:600;display:flex;align-items:center;justify-content:center;flex-shrink:0;margin-top:0.1rem;}
  .step-text{color:#475569;font-size:0.9rem;}
  .footer{margin-top:2.5rem;padding-top:1.5rem;border-top:1px solid #e2e8f0;color:#94a3b8;font-size:0.8rem;}
</style>
</head><body>
<div class="container">

<h1>You're in.</h1>

<div class="key-box">
  <div class="key-label">Your API Key</div>
  <div class="key-value" id="api-key">%s</div>
  <button class="copy-btn" onclick="copyKey()">Copy</button>
</div>

<div class="credits">%d credits</div>

<h2>Quick start</h2>

<div class="step"><div class="step-num">1</div><div class="step-text">Add to your agent's MCP config:</div></div>
<div class="snippet"><div class="snippet-label">Claude Code .mcp.json</div><pre>{
  "mcpServers": {
    "gemot": {
      "type": "sse",
      "url": "https://gemot.dev/mcp",
      "headers": {
        "Authorization": "Bearer %s"
      }
    }
  }
}</pre></div>

<div class="step"><div class="step-num">2</div><div class="step-text">Your agent can now call gemot's tools: <code>create_deliberation</code>, <code>submit_position</code>, <code>vote</code>, <code>analyze</code>, and more.</div></div>

<div class="step"><div class="step-num">3</div><div class="step-text">Each <code>analyze</code> call costs 50 credits (Sonnet) or 200 credits (Opus). Check your balance anytime:</div></div>
<div class="snippet"><div class="snippet-label">curl</div><pre>curl https://gemot.dev/balance -H "Authorization: Bearer %s"</pre></div>

<div class="footer">
  <p>Need more credits? <a href="/pricing">Buy more</a></p>
  <p>Questions? <a href="mailto:justin@gemot.dev">justin@gemot.dev</a> · <a href="https://github.com/justinstimatze/gemot">GitHub</a></p>
</div>

</div>
<script>
function copyKey(){
  const key=document.getElementById('api-key').textContent;
  navigator.clipboard.writeText(key).then(()=>{
    const btn=document.querySelector('.copy-btn');
    btn.textContent='Copied!';btn.classList.add('copied');
    setTimeout(()=>{btn.textContent='Copy';btn.classList.remove('copied');},2000);
  });
}
</script>
</body></html>`, html.EscapeString(key), balance, html.EscapeString(key), html.EscapeString(key))
	}
}

// CancelHandler shows cancellation page.
func CancelHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Gemot — Cancelled</title>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
</head>
<body style="font-family:'Inter',system-ui,sans-serif;max-width:600px;margin:4rem auto;padding:1.5rem;background:#fafaf8;color:#0f172a;">
<h1 style="font-size:1.5rem;font-weight:700;margin-bottom:1rem;">Purchase cancelled</h1>
<p style="color:#64748b;margin-bottom:1rem;">No charges were made.</p>
<p><a href="https://gemot.dev" style="color:#4f46e5;">Return to gemot.dev</a></p>
</body></html>`)
	}
}
