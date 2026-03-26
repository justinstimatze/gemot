# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in gemot, please report it responsibly.

**Email:** justin@gemot.dev

Please include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

We will acknowledge your report within 48 hours and aim to release a fix within 7 days for critical issues.

## Scope

This policy covers:
- The gemot MCP server and HTTP/A2A endpoints
- The credit/billing system
- Access control and authentication
- The analysis pipeline (prompt injection, data leakage)

## Threat Model

See [THREAT_MODEL.md](THREAT_MODEL.md) for our documented threat model, including:
- Sybil voting detection
- Prompt injection defense
- Taxonomy silencing
- Epistemic poisoning resistance

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest  | Yes       |

## Security Features

- API key scoping (SHA256-derived key_id namespaces all agent identities)
- Private deliberation ACLs with max participant caps
- Rate limiting (30 req/min per key)
- Request body size limits (64KB on A2A)
- PII stripping on position content
- Stripe webhook signature verification
- CSV export injection defense
