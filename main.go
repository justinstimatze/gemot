package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/config"
	"github.com/justinstimatze/gemot/internal/cost"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
	"github.com/justinstimatze/gemot/internal/mcp"
	"github.com/justinstimatze/gemot/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: gemot <serve|http> [--addr :8080]\n")
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
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func cmdServe(httpMode bool, addr string) {
	cfg := config.Load()

	db, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close() //nolint:errcheck

	// Cost tracker
	tracker := cost.NewTracker()

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
		// Enable LLM response caching (24h TTL, T3C pattern)
		synth.SetCache(store.NewLLMCache(db, 24*time.Hour))
		analyzer = synth
	} else {
		fmt.Fprintf(os.Stderr, "Warning: ANTHROPIC_API_KEY not set, analysis will not be available\n")
		analyzer = &noopAnalyzer{}
	}

	svc := deliberation.NewService(db, analyzer)
	if synth, ok := analyzer.(*analysis.Synthesizer); ok {
		svc.SetCompromiseGenerator(synth)
		svc.SetReframer(synth)
	}
	// LLM content screening (Haiku classifier for position moderation)
	if cfg.AnthropicKey != "" {
		screeningClient := llm.NewClient(cfg.AnthropicKey, "claude-haiku-4-5")
		svc.SetContentClassifier(screeningClient.Classify)
	}

	// Signal-aware context for graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Background janitor: recover stuck deliberations and jobs
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
					if n, err := db.RecoverStuckJobs(10 * time.Minute); err != nil {
						fmt.Fprintf(os.Stderr, "gemot: stuck job recovery error: %v\n", err)
					} else if n > 0 {
						fmt.Fprintf(os.Stderr, "gemot: recovered %d stuck job(s)\n", n)
					}
					if n, err := db.DeleteExpiredSandboxDeliberations(48 * time.Hour); err != nil {
						fmt.Fprintf(os.Stderr, "gemot: sandbox cleanup error: %v\n", err)
					} else if n > 0 {
						fmt.Fprintf(os.Stderr, "gemot: cleaned up %d expired sandbox deliberation(s)\n", n)
					}
					if n, err := db.PurgeSoftDeleted(60 * 24 * time.Hour); err != nil {
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
		if err := mcp.RunHTTP(ctx, svc, db.RawDB(), addr); err != nil {
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

type noopAnalyzer struct{}

func (n *noopAnalyzer) Analyze(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	return nil, fmt.Errorf("analysis not available: ANTHROPIC_API_KEY not configured")
}
