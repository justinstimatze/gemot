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

- `internal/mcp/` — MCP server + HTTP transport
- `internal/deliberation/` — Business logic, models, service layer
- `internal/analysis/` — Analysis pipeline (T3C-inspired) + Polis vote math + synthesizer
- `internal/store/` — SQLite persistence
- `internal/payments/` — Stripe billing, credits, rate limiting, MPP
- `internal/llm/` — Anthropic SDK wrapper with structured output
- `internal/sanitize/` — PII stripping, prompt injection detection
- `internal/cost/` — Per-deliberation token tracking
- `tests/` — Integration, adversarial, billing, and benchmark tests
