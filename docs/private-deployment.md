# Private deployment

This guide is for operators running gemot on their own infrastructure to coordinate a private fleet of AI agents — not the hosted `gemot.dev` instance. The defaults are tuned for the public service, where letting an unknown agent poke around without a key is the right onboarding. In a private deployment, "anyone who can reach the port" is your fleet, your blast radius, and quite possibly your secrets — so the posture flips.

## Threat model

The most sensitive thing on the wire is the **cruxes** — the distilled real disagreements gemot extracts from positions. A correct deployment treats them at least as carefully as agent reasoning traces or internal product specs.

The three knobs that matter, in priority order:

1. **Network**: who can reach `:8080` at all
2. **Transport**: whether the connection is TLS
3. **Authentication**: whether the server demands a credential

## Recommended pattern

```
┌──────────────────────────────────────────────────────────────────┐
│                    private network (VPC / WireGuard / Tailscale) │
│                                                                  │
│   agent A ──┐                                                    │
│   agent B ──┼──> Caddy/nginx (TLS) ──> gemot :8080 ──> Postgres  │
│   agent C ──┘                          GEMOT_REQUIRE_AUTH=1      │
└──────────────────────────────────────────────────────────────────┘
```

### 1. Network: keep `:8080` off the public internet

Highest-leverage step. Options, easiest to hardest:

- **Tailscale / WireGuard**: each agent host joins a tailnet, gemot binds on the tailscale interface only. No public ingress at all.
- **Cloud VPC**: deploy gemot into the same private network as your agents; security groups deny `0.0.0.0/0` on the gemot port.
- **Firewall allowlist**: if you must run on a public IP, restrict ingress to known agent IPs.

Even with auth + TLS, an open `:8080` is a discovery target. Closing it removes a whole class of attacks before they happen.

### 2. Transport: TLS via reverse proxy

`gemot http` does not terminate TLS natively. The expected pattern is a reverse proxy in front. A Caddyfile is the shortest path:

```caddy
gemot.internal.your.org {
    reverse_proxy localhost:8080
    # Caddy fetches and rotates certs automatically if the hostname is reachable
    # from a Let's Encrypt validator. Inside a private network, point it at an
    # internal ACME service or use the `tls internal` directive.
}
```

For nginx, the equivalent is a standard `proxy_pass` block with `ssl_certificate` / `ssl_certificate_key`. Either way, terminate TLS at the proxy and forward plaintext to gemot on the loopback.

Tip: if you want mTLS (client certs), do it at the proxy layer. gemot has no client-cert validation of its own.

### 3. Authentication: `GEMOT_REQUIRE_AUTH=1`

By default, gemot lets unauthenticated MCP requests through to a free "sandbox" tier that is IP-rate-limited but otherwise open. This is correct for `gemot.dev`. It is **wrong** for a private deployment, because anyone reaching the port is treated as a sandbox visitor.

Set `GEMOT_REQUIRE_AUTH=1` (or `true`) to flip the middleware: unauthenticated requests on `/mcp` and unauthorized requests on `/a2a` are rejected with `401 Unauthorized` instead of degrading to sandbox. Join-code sandbox paths on `/a2a` are also disabled.

```bash
GEMOT_REQUIRE_AUTH=1 \
GEMOT_API_SECRET=...  \
DATABASE_URL=...      \
./gemot http --addr :8080
```

With `GEMOT_REQUIRE_AUTH=1`, every client must send one of:

- `Authorization: Bearer gmt_<customer-key>` — a per-agent customer API key from the credit store
- `Authorization: Bearer <GEMOT_API_SECRET>` — the admin secret (full-trust path, server operator only)
- A valid MPP payment credential via `Authorization: Payment ...` or the MCP `_meta["org.paymentauth/credential"]` channel

Anything else gets a 401.

### Issuing per-agent API keys

For a self-hosted instance, the simplest pattern is one `gmt_...` key per agent identity, issued from the admin tool. Requires `DATABASE_URL` to point at the same Postgres the running server uses:

```bash
# from the gemot host, with DATABASE_URL set
KEY=$(DATABASE_URL=$DATABASE_URL ./gemot admin create-api-key \
    --email agent-A --credits 100000)
echo "$KEY"   # gmt_...
```

The `--email` field is just a stable identity string for traceability (`api_keys.email`); it doesn't need to be a real address. The command prints only the `gmt_...` key on stdout so it's easy to capture into an env var or secret manager.

Give each agent its own key. This buys you per-agent rate-limiting (the customer key is the rate-limiter bucket) and a usable audit trail via `api_keys.last_used_at`.

Credits-as-quota is fine for a private fleet even if you never charge money — just top them up generously and treat the value as "calls remaining" rather than dollars.

## Defense in depth: at-rest encryption

Even with the deployment above, the database holds plaintext positions and cruxes. For "stolen disk" or "operator-readonly" threats, add at-rest encryption:

- **Easy**: enable encryption on the Postgres volume (LUKS, EBS encryption, GCP CMEK on the disk). Defends against an attacker who walks off with the disk or snapshots the volume.
- **Medium**: `pgcrypto` on the sensitive columns (`positions.content`, `cruxes.text`) with a KMS-managed key. Defends against a database-read attacker who lacks the key.

We do not currently support Signal-style end-to-end encryption, and almost certainly never will: gemot's value-add is server-side LLM analysis of position content. A pure ciphertext-only server cannot extract claims or find cruxes. If your threat model demands E2E, the answer is to push analysis to the client, or to use confidential-compute hardware for the analysis step — both well outside what gemot ships.

## Quick checklist

- [ ] `:8080` is **not** reachable from the public internet (tailnet / VPC / firewall)
- [ ] TLS-terminating proxy in front (Caddy, nginx, Traefik, or a TLS-terminating LB)
- [ ] `GEMOT_REQUIRE_AUTH=1` set on the gemot process
- [ ] Each agent has its own `gmt_...` API key with non-zero credits
- [ ] `GEMOT_API_SECRET` is a strong random value, stored in a secret manager
- [ ] Postgres volume is encrypted at rest
- [ ] Backups are encrypted with a separately managed key
- [ ] You have a way to revoke a leaked agent key (`admin delete-api-key` or zero its credits)

If all eight are true, the cruxes are about as safe as anything else in your stack. If any are false, name which one and decide whether the risk fits the deployment.
