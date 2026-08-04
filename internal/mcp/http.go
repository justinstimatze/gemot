package mcp

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
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

// tryCodeData is the render context for static/try-code.html. Named
// so adding a template field without a matching struct field surfaces
// at the call site instead of rendering <no value>.
type tryCodeData struct {
	Topic     string
	Code      string
	HoursLeft int
}

// RunHTTP starts the MCP server over SSE/HTTP on the given address.
//
// backend is the gemot persistence layer (Postgres in production, in-memory
// for `docker run gemot/gemot` style demos). db is the underlying *sql.DB
// for components that genuinely need raw SQL access (credit store, Postgres
// nonce cache, /metrics counters); pass nil in demo mode to skip those
// components. backend itself is always non-nil.
func RunHTTP(ctx context.Context, svc *deliberation.Service, backend store.Backend, db *sql.DB, addr string) error {
	// Fail-fast template + static-asset checks. If an embed is missing
	// or a template has a syntax error, surface here at startup rather
	// than on the first /try/<code> request in production.
	tryCodeTmpl, err := template.ParseFS(staticFS, "static/try-code.html")
	if err != nil {
		return fmt.Errorf("parsing try-code.html template: %w", err)
	}
	tryFormBody, err := staticFS.ReadFile("static/try-form.html")
	if err != nil {
		return fmt.Errorf("reading try-form.html: %w", err)
	}

	apiSecret := os.Getenv("GEMOT_API_SECRET")
	demoMode := db == nil

	// Initialize credit store. In demo mode (no SQL DB) it stays nil and
	// every paid path no-ops via the existing `if creditStore != nil`
	// guards in the handlers — anonymous users effectively get
	// unrestricted analysis runs.
	var creditStore *payments.CreditStore
	if !demoMode {
		creditStore, err = payments.NewCreditStore(db)
		if err != nil {
			return fmt.Errorf("initializing credit store: %w", err)
		}
	}

	// Wire service-level audit logging so ALL write operations are tracked,
	// regardless of whether they come through MCP, A2A, or internal calls (e.g., expert panels).
	svc.SetAuditLogger(func(method, deliberationID, agentID string) {
		backend.LogAuditEvent("", "", method, deliberationID, agentID)
	})

	// Per-key analyze rate limit: 10 concurrent analyses per minute
	// per API key. Credits bound the dollar spend; this limiter bounds
	// the burst rate so a funded account can't monopolize the upstream
	// Anthropic quota. Anonymous (no-key) requests share a pool keyed
	// by IP via a separate limiter downstream.
	analyzeLimiter := payments.NewRateLimiter(ctx, 10, time.Minute)

	// MPP payment configuration. All three secrets are required: STRIPE_SECRET_KEY
	// to settle the PaymentIntent, STRIPE_PROFILE_ID as the networkId routing the
	// SPT, and GEMOT_API_SECRET as the HMAC secret that binds challenge IDs to
	// their parameters. With any one missing we'd advertise challenges that can
	// never validate, so Enabled requires all three.
	mppCfg := payments.Config{
		StripeSecretKey: os.Getenv("STRIPE_SECRET_KEY"),
		StripeProfileID: os.Getenv("STRIPE_PROFILE_ID"),
		HMACSecret:      os.Getenv("GEMOT_API_SECRET"),
		Realm:           "gemot.dev",
		PricePerAnalyze: 50, // $0.50
		Currency:        "usd",
		Enabled: os.Getenv("STRIPE_SECRET_KEY") != "" &&
			os.Getenv("STRIPE_PROFILE_ID") != "" &&
			os.Getenv("GEMOT_API_SECRET") != "",
		RequireAuth: os.Getenv("GEMOT_REQUIRE_AUTH") == "1" ||
			strings.EqualFold(os.Getenv("GEMOT_REQUIRE_AUTH"), "true"),
	}

	// Sandbox unified daily quota across paid analyze actions. 20 calls per
	// 24h per IP is generous for sandbox exploration while bounding cost
	// exposure if MPP/credits funding isn't engaged. Tune via env later if
	// abuse signal emerges.
	sandboxQuota := payments.NewSandboxQuota(20, 24*time.Hour)

	s := &server{svc: svc, credits: creditStore, db: backend, shutdown: ctx, analyzeLimiter: analyzeLimiter, mppCfg: mppCfg, sandboxQuota: sandboxQuota}
	srv := newServer(s)

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
	var nonceCache auth.NonceCache
	// Default is postgres so multi-instance Fly deploys have replay
	// protection out of the box. Explicit GEMOT_NONCE_STORE=memory is
	// still honored for single-process local dev where avoiding the
	// Postgres round-trip per request matters more than durability.
	// Demo mode (no SQL DB) always uses the memory cache — there's no
	// other option, and there's only one instance anyway.
	if demoMode {
		nonceCache = auth.NewMemoryNonceCache(0, 0)
	} else {
		switch store := strings.TrimSpace(os.Getenv("GEMOT_NONCE_STORE")); store {
		case "", "postgres":
			pg := auth.NewPostgresNonceCache(db, 0)
			pg.StartJanitor(ctx, 0)
			nonceCache = pg
			fmt.Fprintf(os.Stderr, "gemot: envelope nonce cache: postgres (durable, multi-instance safe)\n")
		case "memory":
			nonceCache = auth.NewMemoryNonceCache(0, 0)
			fmt.Fprintf(os.Stderr, "gemot: envelope nonce cache: memory (single-instance only)\n")
		default:
			fmt.Fprintf(os.Stderr, "gemot: WARNING: unknown GEMOT_NONCE_STORE=%q — defaulting to postgres\n", store)
			pg := auth.NewPostgresNonceCache(db, 0)
			pg.StartJanitor(ctx, 0)
			nonceCache = pg
		}
	}
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
	//
	// Also primes the response so the SDK's first event (endpoint URL) reaches
	// the client without being held in Fly's proxy buffer. Sends headers + a
	// short comment line + immediate flush before delegating, so subsequent SDK
	// writes flow through. Without priming, the endpoint event sits in the
	// proxy until enough additional bytes accumulate and SSE clients see only
	// headers (manifests as Fly proxy "could not finish reading HTTP body"
	// noise on connection close).
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

			// Set SSE response headers before the SDK runs so the proxy sees
			// them on the first byte and treats the response as a stream.
			// X-Accel-Buffering: no is honored by nginx-style proxies; Fly's
			// behavior is best-effort, so we also pre-flush a comment line to
			// force the proxy to commit to streaming mode. We deliberately do
			// NOT set Connection: keep-alive — it's the default on HTTP/1.1
			// and forbidden as a connection-specific header on HTTP/2 (RFC
			// 7540 §8.1.2.2), so Go's stack would just strip it on h2.
			h := sw.Header()
			h.Set("Content-Type", "text/event-stream")
			h.Set("Cache-Control", "no-cache")
			h.Set("X-Accel-Buffering", "no")
			sw.WriteAndFlush([]byte(": gemot-sse-init\n\n"))

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
	// Separate tight rate limit for bearer-less sandbox A2A callers.
	// 10/min per IP — enough for a human pacing through the invite
	// block, tight enough that a shared join code can't flood prod.
	a2aSandboxLimiter := payments.NewRateLimiter(ctx, 10, time.Minute)
	a2aAuth := A2AAuthMiddleware(apiSecret, creditStore, a2aLimiter, svc, a2aSandboxLimiter, mppCfg.RequireAuth)
	a2aHandler := A2AHandler(svc, creditStore, backend, backend)
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
	if !demoMode {
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
	}

	// Agent card (A2A discovery) — generated from the Version constant so it
	// can never drift from the released binary. See agent_card.go.
	mux.HandleFunc("/.well-known/agent-card.json", AgentCardHandler)

	// Protected-resource metadata (RFC 9728). Deliberately omits
	// authorization_servers: gemot uses bearer API keys + MPP, not OAuth, and
	// won't advertise a handshake it doesn't implement. See protected_resource.go
	// and COMPOSING.md.
	mux.HandleFunc("/.well-known/oauth-protected-resource", ProtectedResourceHandler(baseURL))

	// Health check (public) — verifies DB connectivity in production,
	// always reports ok in demo mode (the in-memory backend is part of
	// the process, so liveness == health).
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if demoMode {
			_, _ = fmt.Fprintf(w, `{"status":"ok","service":"gemot","version":"%s","mode":"demo"}`, Version)
			return
		}
		if err := db.PingContext(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, `{"status":"down","service":"gemot","version":"%s","reason":"database unreachable"}`, Version)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"ok","service":"gemot","version":"%s"}`, Version)
	})

	// Metrics (admin only) — Postgres-only because it queries the
	// physical schema. In demo mode the in-memory backend has no
	// equivalent counters and the route stays unregistered.
	if !demoMode {
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
	} // end !demoMode block (skips Postgres-only routes)

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
		// Collapse whitespace so a multi-line paste doesn't produce
		// mid-sentence newlines in the invite-a-friend clipboard text.
		topic = strings.Join(strings.Fields(topic), " ")
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
		// Rate-limit reads: a bogus-code lookup is a DB round-trip, and
		// an empty-code request is a template render. 30/min per IP
		// (same bucket as other read endpoints) is plenty for a real
		// user and blocks enumeration.
		ip := ClientIP(r)
		if !endpointLimiter.Allow("try-get:" + ip) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		code := strings.TrimPrefix(r.URL.Path, "/try/")
		if code == "" {
			// Show creation form. Pre-read at startup — no disk hit per request.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "private, no-cache")
			w.Write(tryFormBody) //nolint:errcheck
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
			topic = d.Topic
		}
		minutesLeft := int(time.Until(jc.ExpiresAt).Minutes())
		if minutesLeft < 0 {
			minutesLeft = 0
		}
		hoursLeft := minutesLeft / 60

		// Render to a buffer first so a mid-stream template error turns
		// into a clean 500 instead of a truncated page to the client.
		var buf strings.Builder
		if err := tryCodeTmpl.Execute(&buf, tryCodeData{
			Topic: topic, Code: jc.Code, HoursLeft: hoursLeft,
		}); err != nil {
			slog.Error("try-code template render failed", "code", jc.Code, "error", err)
			http.Error(w, "sandbox page unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Time-sensitive ("Xh remaining") — don't let intermediaries cache.
		w.Header().Set("Cache-Control", "private, no-cache")
		_, _ = w.Write([]byte(buf.String()))
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

	// Security headers for all responses. CSP uses 'unsafe-inline' for
	// both scripts and styles because the landing page + /try HTML have
	// inline onclick handlers and inline <style>. Fonts.googleapis /
	// gstatic are the only external origins any page reaches; everything
	// else is same-origin or explicit via a redirect the user initiates.
	// Stricter nonces are a future tightening once the static pages are
	// rewritten to use external script/style refs.
	const csp = "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline'; " +
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
		"font-src 'self' https://fonts.gstatic.com; " +
		"img-src 'self' data:; " +
		"connect-src 'self'; " +
		"frame-ancestors 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'"
	secureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", csp)
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
