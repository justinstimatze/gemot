# Gemot Project

Gemot is an MCP server written in Go that exposes structured deliberation as tools for AI agent coordination.

## Quick Context

- **What**: Deliberation primitive for agents — submit positions, vote, get crux analysis
- **Why**: Moltbook proved agent societies need structural mechanisms; deliberation platforms provide that
- **How**: Go MCP server using official Go SDK, SQLite storage, Anthropic Claude for LLM analysis
- **Patterns from**: T3C for claim extraction + crux detection, Polis for vote math + clustering

## Key Commands

```bash
go build -o gemot .     # Build
go test ./...           # Test
./gemot serve           # Start MCP server (stdio)
./gemot http            # Start HTTP server (streamable-http)
```
