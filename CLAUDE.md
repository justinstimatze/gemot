# Gemot

Structured deliberation MCP server for AI agent coordination.

## Key Commands

```bash
go build -o gemot .     # Build
go test ./...           # Test
./gemot serve           # Start MCP server (stdio)
./gemot http            # Start HTTP/SSE server
```

## Architecture

- `internal/mcp/` — MCP server, HTTP transport, A2A JSON-RPC, SSE events
- `internal/deliberation/` — Business logic, models, service layer, event bus
- `internal/analysis/` — Analysis pipeline (T3C-inspired) + Polis vote math + synthesizer
- `internal/store/` — Postgres persistence (pgx)
- `internal/payments/` — Stripe billing, credits, rate limiting, MPP
- `internal/llm/` — Anthropic SDK wrapper with structured output
- `internal/sanitize/` — PII stripping, prompt injection detection
- `internal/cost/` — Per-deliberation token tracking
- `internal/config/` — Runtime configuration
- `tests/` — 161 tests (integration, adversarial, billing, benchmarks)
