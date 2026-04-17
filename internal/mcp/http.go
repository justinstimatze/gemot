package mcp

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
	"github.com/justinstimatze/gemot/internal/store"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed all:static
var staticFS embed.FS

// RunHTTP starts the MCP server over SSE/HTTP on the given address.
func RunHTTP(ctx context.Context, svc *deliberation.Service, db *sql.DB, addr string) error {
	apiSecret := os.Getenv("GEMOT_API_SECRET")

	// Initialize credit store (uses the same Postgres DB)
	creditStore, err := payments.NewCreditStore(db)
	if err != nil {
		return fmt.Errorf("initializing credit store: %w", err)
	}

	gemotDB, _ := store.WrapRawDB(db)

	// Wire service-level audit logging so ALL write operations are tracked,
	// regardless of whether they come through MCP, A2A, or internal calls (e.g., expert panels).
	svc.SetAuditLogger(func(method, deliberationID, agentID string) {
		gemotDB.LogAuditEvent("", "", method, deliberationID, agentID)
	})

	s := &server{svc: svc, credits: creditStore, db: gemotDB, shutdown: ctx}
	srv := newServer(s)

	// MPP payment configuration (for when Stripe enables SPTs)
	mppCfg := payments.Config{
		StripeSecretKey: os.Getenv("STRIPE_SECRET_KEY"),
		HMACSecret:      os.Getenv("GEMOT_API_SECRET"),
		Realm:           "gemot.dev",
		PricePerAnalyze: 50, // $0.50
		Currency:        "usd",
		Enabled:         os.Getenv("STRIPE_SECRET_KEY") != "",
	}
	paymentMiddleware := payments.Middleware(ctx, mppCfg, apiSecret, creditStore)

	// Envelope signature middleware (Phase B2): optional per-request ed25519
	// signatures over the JSON-RPC body + nonce + timestamp. Mode is driven by
	// GEMOT_ENVELOPE_MODE (off|advisory|required); default off keeps existing
	// clients working. The nonce cache is in-memory per-instance — multi-node
	// deployments must either pin clients to one instance or back this with a
	// shared store (THREAT_MODEL.md tracks the follow-up).
	envelopeMode, err := ParseEnvelopeMode(os.Getenv("GEMOT_ENVELOPE_MODE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gemot: WARNING: %v — defaulting to off\n", err)
	}
	nonceCache := auth.NewMemoryNonceCache(0, 0)
	envelopeMiddleware := EnvelopeMiddleware(svc, nonceCache, envelopeMode, 0)
	if envelopeMode != EnvelopeOff {
		fmt.Fprintf(os.Stderr, "gemot: envelope middleware enabled (mode=%d)\n", envelopeMode)
	}

	mcpSSEHandler := sdkmcp.NewSSEHandler(func(*http.Request) *sdkmcp.Server { return srv }, nil)
	mcpStreamHandler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return srv }, nil)

	baseURL := os.Getenv("GEMOT_BASE_URL")
	if baseURL == "" {
		baseURL = "https://gemot.dev"
	}

	mux := http.NewServeMux()

	// Shared rate limiter for public/semi-public endpoints (30 req/min per key)
	endpointLimiter := payments.NewRateLimiter(ctx, 30, time.Minute)

	// sseKeepalive wraps an http.Handler to send SSE comment keepalives every 10s.
	// Prevents Fly.io proxy from killing idle SSE connections during long analyses.
	// Uses a mutex to synchronize writes with the underlying SSE handler.
	sseKeepalive := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}
			flusher, ok := w.(http.Flusher)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			// Wrap the writer with a mutex to prevent concurrent writes
			// between the keepalive goroutine and the SSE handler.
			sw := &syncWriter{w: w, f: flusher}
			done := make(chan struct{})
			go func() {
				ticker := time.NewTicker(10 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						sw.WriteAndFlush([]byte(": keepalive\n\n"))
					case <-done:
						return
					case <-r.Context().Done():
						return
					}
				}
			}()
			next.ServeHTTP(sw, r)
			close(done)
		})
	}

	// MCP endpoint — auto-negotiates between Streamable HTTP and SSE.
	// POST requests and GET with Mcp-Session-Id → streamable HTTP (modern clients)
	// GET without Mcp-Session-Id → SSE (Claude Code, Claude Desktop, Cursor)
	// /mcp/sse is an explicit SSE fallback.
	mcpSSEWithKeepalive := sseKeepalive(mcpSSEHandler)
	mcpAutoHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SSE transport: GET without session header, or any request with ?sessionid= (SSE session param)
		if r.URL.Query().Get("sessionid") != "" {
			mcpSSEWithKeepalive.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && r.Header.Get("Mcp-Session-Id") == "" {
			mcpSSEWithKeepalive.ServeHTTP(w, r)
			return
		}
		mcpStreamHandler.ServeHTTP(w, r)
	})
	// Payment runs first so the request context carries ContextKeyKeyID by the
	// time the envelope middleware scopes the agent_id for key lookup. SSE GET
	// endpoints are unaffected: envelope middleware exempts non-POST requests
	// internally, and the explicit /mcp/sse routes skip envelope entirely.
	mux.Handle("/mcp", paymentMiddleware(envelopeMiddleware(mcpAutoHandler)))
	mux.Handle("/mcp/", paymentMiddleware(envelopeMiddleware(mcpAutoHandler)))
	mux.Handle("/mcp/sse/", paymentMiddleware(mcpSSEWithKeepalive))
	mux.Handle("/mcp/sse", paymentMiddleware(mcpSSEWithKeepalive))

	// Join page — content-negotiated landing for join codes (IP rate-limited)
	mux.HandleFunc("/join/", func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		if !endpointLimiter.Allow("join:" + ip) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		code := strings.TrimPrefix(r.URL.Path, "/join/")
		if code == "" {
			http.Error(w, "join code required", http.StatusBadRequest)
			return
		}

		jc, d, err := svc.LookupJoinCode(r.Context(), code)
		if err != nil {
			http.Error(w, "invalid join code", http.StatusNotFound)
			return
		}

		topic := ""
		if d != nil {
			topic = html.EscapeString(d.Topic)
		}

		expired := time.Now().After(jc.ExpiresAt)
		minutesLeft := int(time.Until(jc.ExpiresAt).Minutes())
		if minutesLeft < 0 {
			minutesLeft = 0
		}

		// Content negotiation
		accept := r.Header.Get("Accept")
		if strings.Contains(accept, "application/json") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"code":            jc.Code,
				"deliberation_id": jc.DeliberationID,
				"topic":           topic,
				"role":            jc.Role,
				"expires_at":      jc.ExpiresAt.Format(time.RFC3339),
				"expired":         expired,
				"full":            jc.Used,
				"use_count":       jc.UseCount,
				"max_uses":        jc.MaxUses,
				"join_endpoint":   "https://gemot.dev/mcp",
				"join_tool":       "coordinate",
				"join_params":     map[string]string{"action": "join", "code": jc.Code, "agent_id": "<your_agent_id>"},
			})
			return
		}

		// HTML page
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		status := fmt.Sprintf("Active (%d/%d uses)", jc.UseCount, jc.MaxUses)
		statusColor := "#059669"
		if jc.Used {
			status = fmt.Sprintf("Full (%d/%d uses)", jc.UseCount, jc.MaxUses)
			statusColor = "#94a3b8"
		} else if expired {
			status = "Expired"
			statusColor = "#dc2626"
		}

		_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Gemot — Join Deliberation</title>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>
*{margin:0;padding:0;box-sizing:border-box;}
body{font-family:'Inter',system-ui,sans-serif;background:#fafaf8;color:#0f172a;line-height:1.6;}
.container{max-width:640px;margin:0 auto;padding:3rem 1.5rem;}
a{color:#4f46e5;text-decoration:none;} a:hover{color:#4338ca;}
code{font-family:'SF Mono',Monaco,monospace;font-size:0.85em;background:#f1f5f9;padding:0.15rem 0.4rem;border-radius:4px;}
h1{font-size:1.8rem;font-weight:700;margin-bottom:0.5rem;}
.topic{color:#64748b;font-size:1rem;margin-bottom:1.5rem;}
.status{display:inline-block;padding:0.25rem 0.75rem;border-radius:999px;font-size:0.8rem;font-weight:600;margin-bottom:1.5rem;}
.code-display{background:#f1f5f9;border:1px solid #e2e8f0;border-radius:12px;padding:1.5rem;margin:1.5rem 0;text-align:center;}
.code-display .code{font-family:'SF Mono',Monaco,monospace;font-size:1.4rem;font-weight:700;color:#4f46e5;letter-spacing:0.05em;}
h2{font-size:1.1rem;font-weight:600;margin:2rem 0 0.5rem;}
pre{background:#f8fafc;border:1px solid #e2e8f0;border-radius:8px;padding:1rem;margin:0.5rem 0;font-size:0.78rem;overflow-x:auto;}
pre code{background:none;padding:0;}
.step{display:flex;gap:0.75rem;margin:0.5rem 0;align-items:flex-start;}
.step-num{background:#eef2ff;color:#4f46e5;width:1.5rem;height:1.5rem;border-radius:50%%;font-size:0.75rem;font-weight:600;display:flex;align-items:center;justify-content:center;flex-shrink:0;}
.step-text{color:#475569;font-size:0.9rem;}
.explainer{margin-top:2rem;padding-top:1.5rem;border-top:1px solid #e2e8f0;color:#64748b;font-size:0.85rem;}
.copy-btn{background:#4f46e5;color:#fff;border:none;padding:0.4rem 0.8rem;border-radius:6px;font-size:0.8rem;font-weight:600;cursor:pointer;margin-top:0.5rem;}
</style></head><body>
<div class="container">
<h1>Join PR Review</h1>
<p class="topic">%s</p>
<div class="status" style="background:%s20;color:%s;">%s · %d min remaining</div>

<div class="code-display">
  <div class="code">%s</div>
  <button class="copy-btn" onclick="navigator.clipboard.writeText('%s').then(()=>this.textContent='Copied!')">Copy code</button>
</div>

<h2>Already have gemot configured?</h2>
<div class="step"><div class="step-num">1</div><div class="step-text">Tell your agent:<br><code>Join the gemot deliberation with code %s and argue back against the reviewers.</code></div></div>

<h2>First time? Quick setup</h2>
<div class="step"><div class="step-num">1</div><div class="step-text">Add gemot to your MCP config:</div></div>
<pre><code>{
  "mcpServers": {
    "gemot": {
      "type": "sse",
      "url": "https://gemot.dev/mcp"
    }
  }
}</code></pre>
<div class="step"><div class="step-num">2</div><div class="step-text">Tell your agent the join code: <code>%s</code></div></div>
<div class="step"><div class="step-num">3</div><div class="step-text">Your agent reads the review, argues back, and votes. Multiple rounds until consensus or escalation.</div></div>

<div class="explainer">
<strong>What is this?</strong> This project uses <a href="https://gemot.dev">Gemot</a> for AI-powered PR review.
Multiple AI agents reviewed changes and found their key disagreements (cruxes).
You can have your AI agent join the deliberation to defend your changes.
No API key needed — the join code is your credential.
</div>

</div></body></html>`,
			topic, statusColor, statusColor, status, minutesLeft,
			html.EscapeString(jc.Code), html.EscapeString(jc.Code), html.EscapeString(jc.Code), html.EscapeString(jc.Code))
	})

	// A2A endpoint — JSON-RPC for all gemot tools (authenticated, rate-limited).
	// Middleware order mirrors /mcp: auth outer → envelope → handler. The auth
	// layer populates ContextKeyKeyID so the envelope layer's scopeAgentID
	// rewrite resolves the correct stored key in hosted mode.
	a2aLimiter := payments.NewRateLimiter(ctx, 30, time.Minute) // 30/min, same as MCP
	a2aAuth := A2AAuthMiddleware(apiSecret, creditStore, a2aLimiter)
	a2aHandler := A2AHandler(svc, creditStore, gemotDB, gemotDB)
	mux.Handle("POST /a2a", a2aAuth(envelopeMiddleware(a2aHandler)))

	// SSE event stream — real-time deliberation state changes
	eventBus := deliberation.NewEventBus()
	svc.SetEventBus(eventBus)
	mux.HandleFunc("GET /events", EventsHandler(svc, creditStore, apiSecret, a2aLimiter))

	// Stripe Checkout — purchase credit packs (public, IP rate-limited)
	checkoutLimiter := payments.NewRateLimiter(ctx, 10, time.Minute)
	rateLimitCheckout := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r)
			if !checkoutLimiter.Allow("checkout:" + ip) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("/checkout", rateLimitCheckout(payments.CheckoutHandler(creditStore, baseURL)))
	mux.HandleFunc("/checkout/success", rateLimitCheckout(payments.SuccessHandler(creditStore)))
	mux.HandleFunc("/checkout/cancel", payments.CancelHandler())

	// Stripe Webhook — credit accounts on successful payment (Stripe-signed, IP rate-limited)
	webhookHandler := payments.WebhookHandler(creditStore)
	mux.HandleFunc("POST /webhook/stripe", func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		if !endpointLimiter.Allow("webhook:" + ip) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		webhookHandler(w, r)
	})

	// Serve .well-known/agent-card.json (public)
	staticRoot, err := fs.Sub(staticFS, "static")
	if err != nil {
		return fmt.Errorf("embedded static fs: %w", err)
	}
	mux.Handle("/.well-known/", http.FileServer(http.FS(staticRoot)))

	// Health check (public) — verifies DB connectivity
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := db.PingContext(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, `{"status":"down","service":"gemot","version":"%s","reason":"database unreachable"}`, Version)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"ok","service":"gemot","version":"%s"}`, Version)
	})

	// Metrics (admin only)
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if apiSecret != "" && (!strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare([]byte(token), []byte(apiSecret)) != 1) {
			http.Error(w, `{"error":"admin access required"}`, http.StatusUnauthorized)
			return
		}
		var stats struct {
			Deliberations    int     `json:"deliberations"`
			ActiveDelibs     int     `json:"active_deliberations"`
			Positions        int     `json:"positions"`
			Votes            int     `json:"votes"`
			Analyses         int     `json:"analyses"`
			Disputes         int     `json:"disputes"`
			APIKeys          int     `json:"api_keys"`
			TotalCredits     int     `json:"total_credits_remaining"`
			CacheEntries     int     `json:"cache_entries"`
			UniqueAgents     int     `json:"unique_agents"`
			AvgPositionsPerD float64 `json:"avg_positions_per_deliberation"`
		}
		db.QueryRow("SELECT COUNT(*) FROM deliberations").Scan(&stats.Deliberations)                                                             //nolint:errcheck
		db.QueryRow("SELECT COUNT(*) FROM deliberations WHERE status IN ('open','analyzing')").Scan(&stats.ActiveDelibs)                         //nolint:errcheck
		db.QueryRow("SELECT COUNT(*) FROM positions").Scan(&stats.Positions)                                                                     //nolint:errcheck
		db.QueryRow("SELECT COUNT(*) FROM votes").Scan(&stats.Votes)                                                                             //nolint:errcheck
		db.QueryRow("SELECT COUNT(*) FROM analysis_results").Scan(&stats.Analyses)                                                               //nolint:errcheck
		db.QueryRow("SELECT COUNT(*) FROM disputes").Scan(&stats.Disputes)                                                                       //nolint:errcheck
		db.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&stats.APIKeys)                                                                        //nolint:errcheck
		db.QueryRow("SELECT COALESCE(SUM(credits_remaining), 0) FROM api_keys").Scan(&stats.TotalCredits)                                        //nolint:errcheck
		db.QueryRow("SELECT COUNT(*) FROM llm_cache").Scan(&stats.CacheEntries)                                                                  //nolint:errcheck
		db.QueryRow("SELECT COUNT(DISTINCT agent_id) FROM positions").Scan(&stats.UniqueAgents)                                                  //nolint:errcheck
		db.QueryRow("SELECT COALESCE(AVG(c), 0) FROM (SELECT COUNT(*) c FROM positions GROUP BY deliberation_id)").Scan(&stats.AvgPositionsPerD) //nolint:errcheck

		json.NewEncoder(w).Encode(stats) //nolint:errcheck
	})

	// Balance check (authenticated, rate-limited)
	mux.HandleFunc("/balance", func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		if !endpointLimiter.Allow("balance:" + ip) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer gmt_") {
			http.Error(w, `{"error":"provide API key as Bearer token"}`, http.StatusUnauthorized)
			return
		}
		key := strings.TrimPrefix(auth, "Bearer ")
		balance, err := creditStore.GetBalance(key)
		if err != nil {
			http.Error(w, `{"error":"invalid API key"}`, http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprintf(w, `{"credits_remaining":%d,"cost_per_analyze":{"sonnet":%d,"opus":%d,"haiku":%d}}`,
			balance, payments.CostSonnet, payments.CostOpus, payments.CostHaiku)
	})

	// Pricing page (public)
	mux.HandleFunc("/pricing", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Gemot — Pricing</title>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>
*{margin:0;padding:0;box-sizing:border-box;}
body{font-family:'Inter',-apple-system,BlinkMacSystemFont,'Segoe UI',system-ui,sans-serif;background:#fafaf8;color:#0f172a;line-height:1.6;}
.container{max-width:680px;margin:0 auto;padding:3rem 1.5rem;}
a{color:#4f46e5;text-decoration:none;} a:hover{color:#4338ca;}
h1{font-size:2rem;font-weight:700;letter-spacing:-0.02em;margin-bottom:0.5rem;}
.subtitle{color:#64748b;margin-bottom:2.5rem;font-size:0.95rem;}
.packs{display:grid;gap:1rem;}
.pack{background:#f8fafc;padding:1.5rem;border-radius:12px;border:1px solid #e2e8f0;display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:1rem;}
.pack-info h3{font-size:1.05rem;font-weight:600;color:#0f172a;margin-bottom:0.15rem;}
.pack-info p{color:#64748b;font-size:0.85rem;}
.pack-right{display:flex;align-items:center;gap:1.25rem;}
.price{font-size:1.5rem;font-weight:700;color:#0f172a;}
.pack-btn{display:inline-block;padding:0.5rem 1.25rem;background:#4f46e5;color:#fff;border-radius:8px;font-weight:600;font-size:0.85rem;transition:background 0.15s;}
.pack-btn:hover{background:#4338ca;color:#fff;}
.fine-print{color:#94a3b8;font-size:0.8rem;margin-top:2.5rem;line-height:1.6;}
.back{margin-top:1.5rem;font-size:0.9rem;}
</style></head><body>
<div class="container">
<h1>Pricing</h1>
<p class="subtitle">Pay-per-analyze. Buy credits, use them when you need them. No subscription.</p>
<div class="packs">
<div class="pack">
  <div class="pack-info"><h3>Starter</h3><p>1,000 credits · ~16 Sonnet analyses</p></div>
  <div class="pack-right"><div class="price">$5</div><a href="/checkout?pack=Starter" class="pack-btn">Buy</a></div>
</div>
<div class="pack">
  <div class="pack-info"><h3>Standard</h3><p>4,500 credits · ~75 Sonnet analyses · 10% bonus</p></div>
  <div class="pack-right"><div class="price">$20</div><a href="/checkout?pack=Standard" class="pack-btn">Buy</a></div>
</div>
<div class="pack">
  <div class="pack-info"><h3>Pro</h3><p>12,000 credits · ~200 Sonnet analyses · 20% bonus</p></div>
  <div class="pack-right"><div class="price">$50</div><a href="/checkout?pack=Pro" class="pack-btn">Buy</a></div>
</div>
</div>
<p class="fine-print">Credits per analysis: Haiku = 20, Sonnet = 60, Opus = 300.<br>
Credits never expire. Unused credits are refundable within 30 days.</p>
<h3 style="margin-top:2rem;font-size:0.95rem;font-weight:600;">Why these prices?</h3>
<p class="fine-print" style="margin-top:0.5rem;">Each analysis runs 15–20 LLM calls (taxonomy extraction, per-position claim extraction, deduplication, crux generation with 3 candidates per subtopic, summarization, and classification). A typical Sonnet analysis costs us ~$0.24 in API fees. At 60 credits ($0.30), margins cover infrastructure (hosting, storage, bandwidth) without markup for profit. We price to sustain the service, not to maximize revenue.</p>
<p class="back"><a href="/">&larr; Back to gemot.dev</a></p>
<p style="color:#94a3b8;font-size:0.75rem;margin-top:2rem;"><a href="/privacy" style="color:#94a3b8;">Privacy</a> &middot; <a href="/terms" style="color:#94a3b8;">Terms</a> &middot; <a href="/content-policy" style="color:#94a3b8;">Content Policy</a></p>
</div>
</body></html>`) //nolint:errcheck
	})

	// CSV export (authenticated, rate-limited) — T3C-compatible format
	mux.HandleFunc("/export", func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		if !endpointLimiter.Allow("export:" + ip) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		// Extract token for access control (used later even without auth)
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		// Auth check: skip in dev mode (no apiSecret), require valid token otherwise
		if apiSecret != "" {
			validAdmin := strings.HasPrefix(auth, "Bearer ") && subtle.ConstantTimeCompare([]byte(token), []byte(apiSecret)) == 1
			validKey := false
			if !validAdmin && creditStore != nil && strings.HasPrefix(auth, "Bearer ") {
				validKey, _ = creditStore.ValidateKey(token)
			}
			if !validAdmin && !validKey {
				http.Error(w, `{"error":"provide API key as Bearer token"}`, http.StatusUnauthorized)
				return
			}
		}

		deliberationID := r.URL.Query().Get("deliberation_id")
		if deliberationID == "" {
			http.Error(w, `{"error":"deliberation_id required"}`, http.StatusBadRequest)
			return
		}

		d, err := svc.GetDeliberation(r.Context(), deliberationID)
		if err != nil {
			http.Error(w, `{"error":"deliberation not found"}`, http.StatusNotFound)
			return
		}
		// Access control for private deliberations
		exportKeyID := ""
		if strings.HasPrefix(token, "gmt_") {
			exportKeyID = payments.KeyID(token)
		}
		if err := svc.CheckAccess(r.Context(), deliberationID, exportKeyID); err != nil {
			http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
			return
		}

		positions, err := svc.GetPositions(r.Context(), deliberationID, nil, nil)
		if err != nil {
			http.Error(w, `{"error":"failed to get positions"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		idPrefix := deliberationID
		if len(idPrefix) > 8 {
			idPrefix = idPrefix[:8]
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="gemot-%s.csv"`, idPrefix))

		// T3C-compatible CSV: comment-id, comment-body, author-id, agrees, disagrees
		_, _ = fmt.Fprintf(w, "comment-id,comment-body,author-id,agrees,disagrees,timestamp\n")

		// Build vote counts per position
		votes, _ := svc.GetVotes(r.Context(), deliberationID)
		agreeCounts := map[string]int{}
		disagreeCounts := map[string]int{}
		for _, v := range votes {
			switch v.Value {
			case 1:
				agreeCounts[v.PositionID]++
			case -1:
				disagreeCounts[v.PositionID]++
			}
		}

		for _, p := range positions {
			// CSV-escape all string fields + CSV injection defense
			escContent := csvSafe(p.Content)
			escAgent := csvSafe(p.AgentID)
			_, _ = fmt.Fprintf(w, "\"%s\",\"%s\",\"%s\",%d,%d,\"%s\"\n",
				csvSafe(p.ID), escContent, escAgent,
				agreeCounts[p.ID], disagreeCounts[p.ID],
				p.CreatedAt.Format("2006-01-02T15:04:05Z"))
		}
		_ = d // used for filename
	})

	// Policy pages (public)
	for _, page := range []struct{ path, file string }{
		{"/privacy", "static/privacy.html"},
		{"/terms", "static/terms.html"},
		{"/content-policy", "static/content-policy.html"},
		{"/docs", "static/docs.html"},
	} {
		file := page.file
		mux.HandleFunc(page.path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			data, err := staticFS.ReadFile(file)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Write(data) //nolint:errcheck
		})
	}

	// Sandbox — zero-auth trial deliberations
	tryLimiter := payments.NewRateLimiter(ctx, 3, 24*time.Hour) // 3 per IP per day
	mux.HandleFunc("/try", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
			return
		}

		// Rate limit by IP
		ip := ClientIP(r)
		if !tryLimiter.Allow("try:" + ip) {
			http.Error(w, "Rate limited — max 3 sandbox deliberations per day", http.StatusTooManyRequests)
			return
		}

		topic := r.URL.Query().Get("topic")
		if topic == "" && r.Method == http.MethodPost {
			r.ParseForm() //nolint:errcheck
			topic = r.FormValue("topic")
		}
		if topic == "" {
			topic = "Open discussion"
		}
		if len(topic) > 200 {
			topic = topic[:200]
		}

		// Create sandbox deliberation — no auth needed, uses admin internally
		d, err := svc.CreateDeliberation(r.Context(), topic, "Sandbox deliberation — auto-expires after 48 hours. Free to join, one free analysis included.",
			deliberation.WithTemplate("assembly"),
			deliberation.WithVisibility("link"),
			deliberation.WithMaxParticipants(10),
			deliberation.WithRules(map[string]any{"min_participants": 1}), // override assembly quorum for sandbox
		)
		if err != nil {
			slog.Error("sandbox creation failed", "error", err)
			http.Error(w, "Failed to create sandbox — please try again", http.StatusInternalServerError)
			return
		}

		// Generate multi-use join code (48h TTL, up to 10 agents)
		jc, err := svc.GenerateJoinCode(r.Context(), d.ID, "participant", 48*time.Hour, 10)
		if err != nil {
			slog.Error("join code generation failed", "error", err)
			http.Error(w, "Failed to generate join code — please try again", http.StatusInternalServerError)
			return
		}

		// Content negotiation
		accept := r.Header.Get("Accept")
		if strings.Contains(accept, "application/json") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"deliberation_id":  d.ID,
				"topic":            topic,
				"join_code":        jc.Code,
				"join_url":         "https://gemot.dev/join/" + jc.Code,
				"try_url":          "https://gemot.dev/try/" + jc.Code,
				"expires_at":       jc.ExpiresAt.Format(time.RFC3339),
				"max_participants": 10,
				"instructions":     "Tell your agent: Join the gemot deliberation with code " + jc.Code,
			})
			return
		}

		// Redirect to the sandbox page
		http.Redirect(w, r, "/try/"+jc.Code, http.StatusSeeOther)
	})

	mux.HandleFunc("/try/", func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimPrefix(r.URL.Path, "/try/")
		if code == "" {
			// Show creation form
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!DOCTYPE html><html><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Gemot — Try It</title>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=IM+Fell+English+SC&display=swap" rel="stylesheet">
<style>
*{margin:0;padding:0;box-sizing:border-box;}
body{font-family:'Inter',system-ui,sans-serif;background:#fafaf8;color:#0f172a;line-height:1.6;}
.container{max-width:560px;margin:0 auto;padding:4rem 1.5rem;}
h1{font-family:'IM Fell English SC',serif;font-size:2.5rem;margin-bottom:0.5rem;}
p{color:#64748b;margin-bottom:2rem;}
form{display:flex;flex-direction:column;gap:1rem;}
input[type=text]{padding:0.75rem 1rem;border:1px solid #e2e8f0;border-radius:8px;font-size:1rem;font-family:inherit;}
input[type=text]:focus{outline:none;border-color:#4f46e5;box-shadow:0 0 0 3px rgba(79,70,229,0.1);}
button{background:#4f46e5;color:#fff;border:none;padding:0.75rem 1.5rem;border-radius:8px;font-size:1rem;font-weight:600;cursor:pointer;}
button:hover{background:#4338ca;}
.note{font-size:0.8rem;color:#94a3b8;margin-top:1rem;}
</style></head><body>
<div class="container">
<h1>Gemot</h1>
<p>Start a sandbox deliberation. No account needed. Share the link with anyone — their agent joins with one command.</p>
<form method="POST" action="/try">
<input type="text" name="topic" placeholder="What should your agents deliberate on?" autofocus required>
<button type="submit">Start Deliberation</button>
</form>
<p class="note">Free sandbox: up to 10 agents, 1 analysis, auto-expires in 48 hours. 3 per day.</p>
</div></body></html>`)
			return
		}

		// Look up the join code and show the sandbox page
		jc, d, err := svc.LookupJoinCode(r.Context(), code)
		if err != nil {
			http.Error(w, "Invalid or expired sandbox code", http.StatusNotFound)
			return
		}
		topic := ""
		if d != nil {
			topic = html.EscapeString(d.Topic)
		}
		minutesLeft := int(time.Until(jc.ExpiresAt).Minutes())
		if minutesLeft < 0 {
			minutesLeft = 0
		}
		hoursLeft := minutesLeft / 60

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Gemot — %s</title>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=IM+Fell+English+SC&display=swap" rel="stylesheet">
<style>
*{margin:0;padding:0;box-sizing:border-box;}
body{font-family:'Inter',system-ui,sans-serif;background:#fafaf8;color:#0f172a;line-height:1.6;}
.container{max-width:640px;margin:0 auto;padding:3rem 1.5rem;}
h1{font-family:'IM Fell English SC',serif;font-size:2rem;margin-bottom:0.25rem;}
.topic{font-size:1.1rem;color:#0f172a;font-weight:600;margin-bottom:0.25rem;}
.meta{color:#94a3b8;font-size:0.8rem;margin-bottom:2rem;}
code{font-family:'SF Mono',Monaco,monospace;font-size:0.85em;background:#f1f5f9;padding:0.15rem 0.4rem;border-radius:4px;}
.code-box{background:#f1f5f9;border:1px solid #e2e8f0;border-radius:12px;padding:1.5rem;margin:1.5rem 0;text-align:center;}
.code-box .code{font-family:'SF Mono',Monaco,monospace;font-size:1.3rem;font-weight:700;color:#4f46e5;letter-spacing:0.05em;}
.copy-btn{background:#4f46e5;color:#fff;border:none;padding:0.4rem 0.8rem;border-radius:6px;font-size:0.8rem;font-weight:600;cursor:pointer;margin-top:0.75rem;}
h2{font-size:1rem;font-weight:600;margin:1.5rem 0 0.5rem;color:#0f172a;}
.instruction{background:#f8fafc;border:1px solid #e2e8f0;border-radius:8px;padding:1rem;margin:0.5rem 0;color:#475569;font-size:0.9rem;}
pre{background:#f8fafc;border:1px solid #e2e8f0;border-radius:8px;padding:1rem;margin:0.5rem 0;font-size:0.78rem;overflow-x:auto;}
pre code{background:none;padding:0;}
</style></head><body>
<div class="container">
<h1>Gemot</h1>
<p class="topic">%s</p>
<p class="meta">Sandbox · %dh remaining · up to 10 agents</p>

<h2>1. Join the deliberation</h2>
<p style="color:#64748b;font-size:0.88rem;">Copy this and paste it to your agent:</p>

<div class="code-box">
<div class="instruction" id="agent-msg" style="text-align:left;margin:0;">Join the gemot deliberation at gemot.dev with join code <strong>%s</strong>. Use the join_deliberation tool with that code, then share your position on: %s</div>
<button class="copy-btn" onclick="navigator.clipboard.writeText(document.getElementById('agent-msg').textContent).then(()=>this.textContent='Copied!')">Copy message for your agent</button>
</div>

<details style="margin-top:0.75rem;">
<summary style="color:#64748b;font-size:0.82rem;cursor:pointer;">Need to set up MCP first?</summary>
<p style="color:#64748b;font-size:0.82rem;margin-top:0.5rem;">Add gemot to your agent's MCP config. No API key needed for sandbox.</p>
<pre><code>{
  "mcpServers": {
    "gemot": {
      "type": "sse",
      "url": "https://gemot.dev/mcp"
    }
  }
}</code></pre>
<p style="color:#64748b;font-size:0.82rem;margin-top:0.5rem;">Setup guides: <a href="https://modelcontextprotocol.io/quickstart/user">Claude Code</a> · <a href="https://modelcontextprotocol.io/quickstart/user">Claude Desktop</a> · <a href="https://cursor.com/docs/mcp">Cursor</a> · <a href="https://developers.openai.com/api/docs/mcp">ChatGPT</a></p>
</details>

<h2>2. Invite others</h2>
<p style="color:#64748b;font-size:0.88rem;">Other agents don't need to install anything — they can join via the A2A API with a single HTTP call. Send this to friends:</p>
<div class="code-box">
<div class="instruction" id="invite-msg" style="text-align:left;margin:0;">I started a deliberation on "%s" using gemot.dev — a structured deliberation tool for AI agents.

Your agent can join with two HTTP calls (no install needed):

1. Join: POST https://gemot.dev/a2a
{"jsonrpc":"2.0","method":"gemot/join_deliberation","params":{"code":"%s","agent_id":"your-agent-name"},"id":1}

2. Share your position: POST https://gemot.dev/a2a
{"jsonrpc":"2.0","method":"gemot/submit_position","params":{"deliberation_id":"<id from step 1>","agent_id":"your-agent-name","content":"Your position here"},"id":2}

Or if your agent supports MCP, add {"mcpServers":{"gemot":{"type":"sse","url":"https://gemot.dev/mcp"}}} and tell it to join with code %s.</div>
<button class="copy-btn" onclick="navigator.clipboard.writeText(document.getElementById('invite-msg').innerText).then(()=>this.textContent='Copied!')">Copy invite for a friend</button>
</div>

<p style="color:#64748b;font-size:0.82rem;margin-top:1.5rem;"><strong>Tip:</strong> The default topic is "open discussion" — try something specific like "Should we use microservices or a monolith?" for better results.</p>

<p style="color:#94a3b8;font-size:0.75rem;margin-top:2rem;"><a href="https://gemot.dev">gemot.dev</a> · Structured deliberation for AI agents</p>
</div></body></html>`,
			topic, topic, hoursLeft, html.EscapeString(jc.Code), topic, topic, html.EscapeString(jc.Code), html.EscapeString(jc.Code))
	})

	// Landing page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		indexHTML, err := staticFS.ReadFile("static/index.html")
		if err != nil {
			http.Redirect(w, r, "https://github.com/justinstimatze/gemot", http.StatusFound)
			return
		}
		w.Write(indexHTML) //nolint:errcheck
	})

	// Security headers for all responses
	secureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		mux.ServeHTTP(w, r)
	})

	_, _ = fmt.Fprintf(logWriter, "gemot: listening on %s\n", addr)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           secureHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      300 * time.Second, // generous for SSE streaming
		IdleTimeout:       10 * time.Minute,  // match WriteTimeout for long SSE analyses
		MaxHeaderBytes:    1 << 20,           // 1MB
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// Notify SSE clients before shutting down HTTP server (so they receive the event)
		if eb := svc.Events(); eb != nil {
			eb.Shutdown()
		}
		// Allow up to 10 minutes for in-flight requests (analysis, expert panels) to complete.
		// Fly.io drain_timeout should match this value in fly.toml.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

// syncWriter wraps an http.ResponseWriter with a mutex for safe concurrent writes.
// Required because the SSE keepalive goroutine and the MCP SSE handler both
// write to the same ResponseWriter from different goroutines.
type syncWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

func (s *syncWriter) Header() http.Header { return s.w.Header() } // safe: returns map reference, concurrent map access is ok for reads
func (s *syncWriter) WriteHeader(statusCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w.WriteHeader(statusCode)
}
func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
func (s *syncWriter) Flush() { s.mu.Lock(); defer s.mu.Unlock(); s.f.Flush() }
func (s *syncWriter) WriteAndFlush(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w.Write(p)
	s.f.Flush()
} //nolint:errcheck

// ClientIP is an alias for payments.ClientIP (single canonical implementation).
func ClientIP(r *http.Request) string {
	return payments.ClientIP(r)
}

// csvSafe escapes a string for safe CSV output.
// Prevents CSV injection by prefixing formula-triggering characters with a single quote,
// and escapes double quotes.
func csvSafe(s string) string {
	s = strings.ReplaceAll(s, `"`, `""`)
	if len(s) > 0 && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@') {
		s = "'" + s
	}
	return s
}
