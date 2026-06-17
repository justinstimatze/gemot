package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/bft"
	"github.com/justinstimatze/gemot/internal/calibration"
	"github.com/justinstimatze/gemot/internal/config"
	"github.com/justinstimatze/gemot/internal/cost"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
	"github.com/justinstimatze/gemot/internal/mcp"
	"github.com/justinstimatze/gemot/internal/payments"
	"github.com/justinstimatze/gemot/internal/reputation"
	"github.com/justinstimatze/gemot/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: gemot <serve|http|admin|calibration> [args...]\n")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		cmdServe(false, "")
	case "http":
		httpFlags := flag.NewFlagSet("http", flag.ExitOnError)
		var addr string
		httpFlags.StringVar(&addr, "addr", ":8080", "HTTP listen address")
		httpFlags.Parse(os.Args[2:]) //nolint:errcheck
		cmdServe(true, addr)
	case "admin":
		cmdAdmin(os.Args[2:])
	case "calibration":
		os.Exit(calibration.CLI(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

// cmdServe starts gemot in stdio (httpMode=false) or HTTP mode. With
// DATABASE_URL set, gemot runs in production mode against Postgres. With
// DATABASE_URL unset, gemot boots into demo mode against an in-memory
// store — fully functional for kicking the tires, but ephemeral. The
// boot banner makes the difference obvious.
func cmdServe(httpMode bool, addr string) {
	cfg := config.Load()

	// Detect mode from the raw env var, not cfg.DatabaseURL — the latter
	// defaults to a localhost Postgres URL when DATABASE_URL is unset
	// (kept for backwards-compat with the dev workflow), so checking it
	// would never see "no database configured." The env-var check is
	// the only signal that distinguishes "operator wants Postgres" from
	// "operator just wants gemot to run."
	demoMode := os.Getenv("DATABASE_URL") == ""

	// rawDB is the *sql.DB handle that genuinely needs Postgres semantics
	// (credit store, BFT-Postgres stores, /metrics counters). nil in demo
	// mode; consumers downstream branch on that.
	var rawDB *sql.DB
	var backend store.Backend
	if demoMode {
		announceDemoMode()
		mem := store.NewMemoryStore()
		backend = mem
	} else {
		pg, err := store.Open(cfg.DatabaseURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
			os.Exit(1)
		}
		defer pg.Close() //nolint:errcheck
		backend = pg
		rawDB = pg.RawDB()
	}

	// Cost tracker (mode-independent)
	tracker := cost.NewTracker()

	// EigenTrust reputation weigher — nil when feature disabled OR in
	// demo mode (the in-memory backend's reputation surface is no-op so
	// there's nothing for the weigher to read).
	var repWeigher *reputation.Weigher
	if !demoMode {
		repWeigher = reputation.NewWeigher(backend.(*store.DB), reputation.Config{
			Enabled:           cfg.EigenTrustEnabled,
			ColdCap:           cfg.EigenTrustColdCap,
			ColdThreshold:     cfg.EigenTrustColdThreshold,
			Iterations:        cfg.EigenTrustIterations,
			DBFail:            cfg.EigenTrustDBFail,
			DecayHalfLifeDays: cfg.EigenTrustDecayHalfLifeDays,
			DisputeWeight:     cfg.EigenTrustDisputeWeight,
			EdgeFloor:         cfg.EigenTrustEdgeFloor,
			EdgeCap:           cfg.EigenTrustEdgeCap,
		})
	}

	var analyzer deliberation.Analyzer
	if cfg.AnthropicKey != "" {
		client := llm.NewClient(cfg.AnthropicKey, cfg.Model)
		// Wire per-deliberation cost tracking into LLM client.
		// The deliberation ID is propagated via context from Service.Analyze.
		client.OnUsage = func(ctx context.Context, input, output int) {
			delibID := "_global"
			if id, ok := ctx.Value(deliberation.ContextKeyDeliberationID{}).(string); ok {
				delibID = id
			}
			model := cfg.Model
			if m, ok := ctx.Value(llm.ContextKeyModel{}).(string); ok && m != "" {
				model = m
			}
			tracker.Record(delibID, model, input, output)
		}
		synth := analysis.NewSynthesizer(client)
		// LLM response cache (24h TTL). Both Postgres and in-memory
		// backends satisfy store.CacheBackend, so caching works in
		// demo mode too — same query twice is free.
		synth.SetCache(store.NewLLMCache(backend, 24*time.Hour))
		if cfg.StabilitySamples > 1 {
			synth.SetStabilityCheckSamples(cfg.StabilitySamples)
			fmt.Fprintf(os.Stderr, "gemot: crux-stability re-sampling enabled (N=%d) — each generated crux will incur ~%d extra LLM calls\n", cfg.StabilitySamples, cfg.StabilitySamples*2)
		}
		if repWeigher != nil {
			synth.SetReputation(repWeigher)
		}
		if cfg.ConsistencyModel != "" && cfg.ConsistencyKey != "" {
			secondary, err := llm.NewSecondary(cfg.ConsistencyModel, cfg.ConsistencyKey)
			if err != nil {
				fmt.Fprintf(os.Stderr, "gemot: WARNING: cross-family secondary init failed: %v\n", err)
			} else if secondary != nil {
				synth.SetSecondary(secondary, cfg.ConsistencySampleK)
			}
		}
		analyzer = synth
	} else {
		// config.Load already prints a one-line warning when the key is
		// missing — no need to repeat. Just install the no-op analyzer
		// so /analyze tool calls return a clear error rather than crash.
		analyzer = &noopAnalyzer{}
	}

	// HotStuff BFT engine. In production we wire in the Postgres-backed
	// stores so committed actions persist across restarts. In demo mode
	// we let NewService keep its auto-constructed in-memory engine —
	// the tamper-evident log is a per-process artifact, same as the
	// rest of the in-memory state.
	svc := deliberation.NewService(backend, analyzer)
	if !demoMode {
		pg := backend.(*store.DB)
		bftLog := store.NewPostgresLogStore(pg)
		bftVoteHist := store.NewPostgresVoteHistoryStore(pg, bft.ReplicaID(0))
		bftKeys := store.NewPostgresReplicaKeyStore(pg)
		bftEngine, err := bft.BootstrapSingleNode(context.Background(), bftLog, bftVoteHist, bftKeys)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error bootstrapping BFT engine: %v\n", err)
			os.Exit(1)
		}
		svc.SetBFTEngine(bftEngine)
	}
	if synth, ok := analyzer.(*analysis.Synthesizer); ok {
		svc.SetCompromiseGenerator(synth)
		svc.SetReframer(synth)
	}
	if repWeigher != nil {
		svc.SetReputationUpdater(repWeigher)
	}
	// LLM content screening (Haiku classifier for position moderation)
	if cfg.AnthropicKey != "" {
		screeningClient := llm.NewClient(cfg.AnthropicKey, "claude-haiku-4-5")
		svc.SetContentClassifier(screeningClient.Classify)
	}

	// Signal-aware context for graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Background janitor: recover stuck deliberations and jobs. The
	// methods we call (RecoverStuck on the service, RecoverStuckJobs etc.
	// on the backend) are defined on both backends, so this loop runs
	// identically in demo mode and production.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							fmt.Fprintf(os.Stderr, "gemot: PANIC in janitor (recovered): %v\n", r)
						}
					}()
					if n, err := svc.RecoverStuck(ctx); err != nil {
						fmt.Fprintf(os.Stderr, "gemot: stuck recovery error: %v\n", err)
					} else if n > 0 {
						fmt.Fprintf(os.Stderr, "gemot: recovered %d stuck deliberation(s)\n", n)
					}
					if n, err := backend.RecoverStuckJobs(10 * time.Minute); err != nil {
						fmt.Fprintf(os.Stderr, "gemot: stuck job recovery error: %v\n", err)
					} else if n > 0 {
						fmt.Fprintf(os.Stderr, "gemot: recovered %d stuck job(s)\n", n)
					}
					if n, err := backend.DeleteExpiredSandboxDeliberations(48 * time.Hour); err != nil {
						fmt.Fprintf(os.Stderr, "gemot: sandbox cleanup error: %v\n", err)
					} else if n > 0 {
						fmt.Fprintf(os.Stderr, "gemot: cleaned up %d expired sandbox deliberation(s)\n", n)
					}
					if n, err := backend.PurgeSoftDeleted(60 * 24 * time.Hour); err != nil {
						fmt.Fprintf(os.Stderr, "gemot: purge error: %v\n", err)
					} else if n > 0 {
						fmt.Fprintf(os.Stderr, "gemot: purged %d soft-deleted deliberation(s)\n", n)
					}
					tracker.Cleanup(24 * time.Hour)
				}()
			}
		}
	}()

	if httpMode {
		if err := mcp.RunHTTP(ctx, svc, backend, rawDB, addr); err != nil {
			fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := mcp.Run(ctx, svc); err != nil {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
			os.Exit(1)
		}
	}

	// Wait for active analyses to finish before closing DB.
	// SSE shutdown already happened inside RunHTTP before httpServer.Shutdown.
	// Without this, db.Close() (deferred above) kills in-flight analyses.
	if n := svc.DrainAnalyses(10 * time.Minute); n > 0 {
		fmt.Fprintf(os.Stderr, "gemot: waited for %d active analysis/analyses to complete\n", n)
	}
}

// announceDemoMode prints a friendly stderr banner so the operator knows
// they're in ephemeral mode. Two lines: what's happening, and how to
// upgrade. Plain text — works in any terminal, doesn't assume colors.
func announceDemoMode() {
	const banner = `gemot: running in DEMO MODE — no DATABASE_URL set, all state lives in
gemot: process memory and is lost on restart. To run with persistent
gemot: storage, set DATABASE_URL to a Postgres connection string and
gemot: run the schema in internal/store/schema.sql.
`
	fmt.Fprint(os.Stderr, banner)
}

type noopAnalyzer struct{}

func (n *noopAnalyzer) Analyze(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	return nil, fmt.Errorf("analysis not available: ANTHROPIC_API_KEY not configured")
}

// cmdAdmin handles `gemot admin <action>`. Operator-facing tools for
// private/self-hosted deployments — currently just key issuance, but
// scoped so we can add audit/export/key-revoke siblings without
// rewiring main.
func cmdAdmin(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: gemot admin <create-api-key> [args...]")
		os.Exit(1)
	}
	switch args[0] {
	case "create-api-key":
		fs := flag.NewFlagSet("create-api-key", flag.ExitOnError)
		var email string
		var credits int
		fs.StringVar(&email, "email", "", "identity for the key (email or label; stored for traceability)")
		fs.IntVar(&credits, "credits", 0, "credits to issue with the new key")
		fs.Parse(args[1:]) //nolint:errcheck
		if email == "" || credits <= 0 {
			fmt.Fprintln(os.Stderr, "Usage: gemot admin create-api-key --email <id> --credits <n>")
			os.Exit(1)
		}
		cmdAdminCreateAPIKey(email, credits)
	default:
		fmt.Fprintf(os.Stderr, "Unknown admin action: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "Supported: create-api-key")
		os.Exit(1)
	}
}

// cmdAdminCreateAPIKey mints a fresh `gmt_...` customer key and writes
// it to api_keys with the given email/identity and credit balance. The
// key is the only thing printed to stdout so callers can capture it via
// `KEY=$(gemot admin create-api-key --email agent-A --credits 100000)`.
// Requires DATABASE_URL — admin actions are Postgres-only by design.
func cmdAdminCreateAPIKey(email string, credits int) {
	if os.Getenv("DATABASE_URL") == "" {
		fmt.Fprintln(os.Stderr, "gemot admin requires DATABASE_URL (private-deployment Postgres)")
		os.Exit(1)
	}
	cfg := config.Load()
	pg, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer pg.Close()
	credStore, err := payments.NewCreditStore(pg.RawDB())
	if err != nil {
		fmt.Fprintf(os.Stderr, "init credit store: %v\n", err)
		os.Exit(1)
	}
	key, err := credStore.GenerateKey(email, "", "", credits)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate key: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(key)
}
