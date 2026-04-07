# Contributing to Gemot

Thanks for your interest in contributing.

## Getting Started

```bash
git clone https://github.com/justinstimatze/gemot.git
cd gemot
go build ./...
go test ./...
```

You'll need Go 1.25+, Postgres (see `docker-compose.yml`), and an Anthropic API key for the LLM-dependent tests.

## Development

```bash
# Build
go build -o gemot .

# Run tests
go test ./...

# Start local HTTP server
ANTHROPIC_API_KEY=sk-ant-... ./gemot http

# Run the calendar scheduling demo against your local server
GEMOT_LIVE_URL=http://localhost:8080/mcp GEMOT_API_SECRET=your-secret go run ./scripts/calendar-scheduling
```

## Pull Requests

1. Fork the repo and create a branch from `main`
2. Add tests for new functionality
3. Run `go test ./...` and ensure all tests pass
4. Run `go vet ./...` for static analysis
5. Keep PRs focused — one feature or fix per PR

## What to Contribute

- Bug fixes with regression tests
- New integration examples (see `integrations/` and `scripts/`)
- Documentation improvements
- Test coverage for untested paths
- Performance improvements with benchmarks

## Code Style

- Standard Go formatting (`gofmt`)
- No unnecessary abstractions — three similar lines beat a premature helper
- Error messages should include context: `fmt.Errorf("creating deliberation: %w", err)`
- Tests go in `tests/` (integration) or alongside the package (unit)

## Reporting Issues

Use GitHub Issues. Include:
- What you expected
- What happened
- Steps to reproduce
- Go version and OS

## License

By contributing, you agree that your contributions will be licensed under the Apache 2.0 License.
