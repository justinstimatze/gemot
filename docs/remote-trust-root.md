# Remote trust root — design & security plan

Status: **Phase 1 implemented** (`internal/principal/remote.go`, wired in `main.go` via
`GEMOT_TRUSTED_ISSUERS`); Phases 2–3 proposed. This document specs the second
`principal.Verifier` backend: verifying delegation credentials minted by an **external
issuer** whose keys gemot does not hold. It is written security-first because this feature,
unlike everything else in `internal/principal`, deliberately **transfers trust to a third
party**.

See also: `internal/principal/remote.go` + `remote_test.go` (Phase 1 implementation),
`internal/principal/credential.go` (the seam), `tests/remote_trust_test.go` (end-to-end),
`COMPOSING.md` (interop contract), `docs/act-claim.schema.json` (the external act-claim
vocabulary this ultimately consumes).

---

## 1. The trust-model shift (read this first)

Everything already shipped is *self-authenticating*:

- **`LocalVerifier`** resolves the **principal's** key from gemot's own registry and
  verifies that the principal *itself* signed "agent A may act for me." No third party is
  trusted. Revocation is inherited for free (revoke the principal's key, every credential
  it ever signed dies).

The remote backend is different in kind:

- A **third-party issuer** signs an attestation asserting "principal P delegates to agent A,
  scope S, until T." gemot does **not** have P's key. gemot verifies the **issuer's**
  signature and *trusts the issuer to have authenticated P.*

That single sentence is the whole risk surface. Consequences that must drive the design:

1. **A trusted issuer can vouch for principals it does not own.** If issuer `evil.example`
   is trusted and asserts `principal: "human:alice"` — where alice is a locally-registered
   principal, or one another issuer owns — then adding `evil.example` to the allowlist has
   silently granted it the power to impersonate alice. **The allowlist alone is not enough;
   each issuer must be confined to a principal namespace it provably owns.** This is the
   single most important control in the design (§4.2).
2. **Revocation cannot be inherited.** The issuer's signing key is outside gemot. We fall
   back to mandatory short expiry (already enforced) plus a hard config kill-switch, and —
   only in the JWKS phase — optional issuer revocation checks (§4.5).
3. **Key resolution may cross the network.** That drags in SSRF, fetch-DoS, cache poisoning,
   and algorithm confusion — none of which exist in the current codebase. Phase 1
   deliberately keeps keys in config to sidestep all of it while we prove the trust model
   (§5).

## 2. What changes, and what pointedly does not

The seam already exists and is correctly shaped. `Service.principalVerifier` is a
`principal.Verifier` interface; `SetPrincipalVerifier` swaps the implementation
(`internal/deliberation/service.go:351`). Nothing in the submit-position path, storage,
wire format, or `provePossession` needs to change to add a backend.

**Unchanged, and load-bearing:**

- **`provePossession` stays exactly as-is** (`service.go:2924`). It runs in the service
  layer *outside* `Verifier` on purpose, and it is what keeps a remote credential from
  becoming a bearer token. Even a fully-malicious trusted issuer can only mint credentials
  binding a confirmation key; to actually submit, the attacker must (a) present an agent
  whose **locally-registered** key equals the credential's `cnf` key, and (b) produce a
  position signature under that key. A leaked or forged-by-a-bad-issuer credential is inert
  without the agent's private key.
  - **Deliberate consequence:** the *agent* must still `register_key` with gemot. The remote
    issuer removes the need for the **principal** (the human/org) to have a gemot key — not
    the agent. This is the right tradeoff for v1: agents already make the API calls, so they
    already have a gemot relationship; principals are the ones we want to federate. Relaxing
    this (accepting an unregistered `cnf` key on the issuer's word) is explicitly out of
    scope — see §9.
- **`Credential.Validate`, `CoversTarget`, `Result`** — unchanged. Scope attenuation,
  length caps, mandatory expiry, ed25519 key validation all still apply.
- **The `issuer` label is inside the signed payload** (`SigningPayload`,
  `credential.go:180`). A credential therefore cannot be relabelled to a more-trusted issuer
  than the one that actually signed it. The remote path relies on this.

**New:**

- A **routing verifier** that dispatches on `cred.Issuer`.
- A **remote issuer backend** behind that route.
- **Config** (`GEMOT_TRUSTED_ISSUERS`) and **startup wiring**.

## 3. Threat model

| # | Threat | Mitigation |
|---|--------|------------|
| T1 | **Issuer impersonates a principal it doesn't own** (incl. shadowing a local principal) | Per-issuer **principal-namespace binding** (§4.2); local principals reserved; fail closed on overlap |
| T2 | **Untrusted issuer** presents a credential | Strict allowlist; unknown/empty-in-remote-slot issuer → reject (§4.1) |
| T3 | **Algorithm confusion** (`alg:none`, RS/HS swap, key-as-HMAC-secret) | ed25519/EdDSA only in Phase 1; explicit per-issuer alg allowlist in the JWT phase; `none` always rejected (§4.4) |
| T3b | **Header key-injection** (`jku`/`jwk`/`x5u`/`x5c` name attacker's key) | Header key-source params ignored; key selected only by `(iss, kid)` against configured trust (§4.4) — JWT phase |
| T4 | **SSRF** via a JWKS URL | JWKS URLs come **only** from operator config, never from the token; block link-local/private/metadata ranges (§4.6). *Absent entirely in Phase 1.* |
| T5 | **Fetch-DoS / hot-path stall** on key resolution | Phase 1: no network. Phase 2: pre-warmed cache, tight timeout, bounded concurrency, fail-closed to `ErrKeyLookup` (§4.6) |
| T6 | **Key-rotation / cache poisoning** | Cache keyed by `(iss, kid)`; capped TTL; rate-limited refresh-on-unknown-kid; signed JWKS over TLS only (§4.6) |
| T7 | **Stale trust after de-trusting an issuer** | Config is the source of truth; removing an issuer takes effect on reload and purges its cache; short expiry bounds the window (§4.5) |
| T8 | **Replay of a captured credential by another agent** | `provePossession` (unchanged) — cnf-key + position signature; leaked credential is inert |
| T9 | **Clock skew** accepting expired / not-yet-valid creds | `exp` enforced (existing); small fixed leeway; `nbf`/`iat` checked in JWT phase |
| T10 | **Downgrade** — remote cred masquerading as `local`, or vice-versa | Issuer label is signed; router dispatches deterministically; a `local`-labelled cred never reaches a remote key and vice-versa (§4.3) |
| T11 | **Confused-deputy via `aud`** — credential minted for another resource replayed at gemot | Verify `aud` names this gemot deployment when present (JWT phase); native creds are gemot-shaped already |

## 4. Non-negotiable security controls

### 4.1 Strict issuer allowlist, fail-closed
Only issuers named in `GEMOT_TRUSTED_ISSUERS` are honored. An issuer not in the set, or an
empty issuer arriving at the remote route, is rejected — never defaulted to `local`, never
"trust on first use." No config ⇒ remote verification is **off** and only `LocalVerifier`
runs (100% backward compatible).

### 4.2 Per-issuer principal-namespace binding — *the* control
This is gemot's form of a **SPIFFE trust domain**: a trust domain is "the administrative and
cryptographic boundary for identity issuance," and confining each issuer to its own boundary
is the named industry mechanism for preventing cross-tenant impersonation. Concretely:

Each trusted issuer declares the principal prefix(es) it is authorized to vouch for. A
credential from issuer `I` for principal `P` is rejected unless `P` falls under one of `I`'s
declared namespaces. Rules:

- Namespaces must be **disjoint across issuers**; overlapping config is a startup error
  (fail to boot, not fail open).
- The `local` namespace (any principal with a locally-registered key) is **reserved** — no
  remote issuer may claim a prefix that could shadow a local principal. A remote credential
  whose principal also has a local key is rejected.
- Recommended convention: bind an issuer to principals under its own identity, e.g. issuer
  `https://acme.example` ⇒ principals `acme:*` or `did:web:acme.example:*`.

Without this, adding one trusted issuer grants it impersonation power over *every* principal
in the system. This check is mandatory in Phase 1.

### 4.3 Deterministic routing on the signed issuer label
A `RoutingVerifier` reads `cred.Issuer`:
- `""` or `"local"` → `LocalVerifier` (today's behavior, principal self-signed).
- an allowlisted issuer → that issuer's remote backend.
- anything else → reject (`ErrKeyLookup`-style, fail closed).

Because the label is inside the signed bytes, it cannot be forged to cross routes.

### 4.4 Algorithm discipline
Phase 1 is ed25519-only (matches the existing `Credential`). The JWT phase pins an explicit
per-issuer algorithm allowlist (EdDSA/ES256), rejects `alg:none`, and never feeds an
asymmetric public key into a symmetric verifier. No algorithm is inferred from the token.
Additionally (JWT phase), **reject any token whose header tries to name its own key source** —
`jku`, `jwk`, `x5u`, `x5c` are ignored; the verifying key is selected only by `(iss, kid)`
against operator-configured trust. This is a standing OWASP JWT control and the header-side
twin of the SSRF rule in §4.6.

### 4.5 Revocation & kill-switch
- Mandatory short expiry (already enforced) is the primary bound; document a recommended
  ceiling (e.g. ≤ 1h) for delegated authority.
- Removing an issuer from config and reloading immediately stops honoring its credentials
  and purges any cached keys. This is the operator's emergency stop.
- JWKS phase *may* add optional issuer revocation-list / status checks; not required for v1.

### 4.6 Network hygiene (Phase 2+ only)
- JWKS URLs originate **only** from operator config; never fetched from a URL named in a
  credential/token.
- Resolve and refuse link-local (`169.254.0.0/16`, incl. `169.254.169.254`), loopback, and
  private ranges unless explicitly opted in for local testing.
- TLS required; cache keyed by `(iss, kid)` with a capped TTL; refresh-on-unknown-kid is
  rate-limited so a `kid`-flood cannot drive unbounded fetches; fetch has a tight timeout
  and fails **closed** (`ErrKeyLookup`, reject) rather than hanging the submit path.
- Pre-warm on startup so the hot path is cache-only in the common case.

### 4.7 Audience restriction (JWT phase)
Per RFC 9700 and OWASP, when a credential/token carries `aud`, verify it names *this* gemot
deployment and reject otherwise — this stops a token minted for another resource from being
replayed at gemot (confused deputy, T11). Single audience preferred over a list. Native
Phase-1 credentials are already gemot-shaped (verified against a gemot `Target`), so this
control is specific to the external-JWT path.

## 5. Phased design

Phasing is a security decision, not just scheduling: each phase adds exactly one new risk
class only after the previous foundation is proven.

### Phase 1 — Federated native credentials, keys pinned in config ✅ *implemented*
Shipped in `internal/principal/remote.go` (`RemoteIssuer`, `IssuerVerifier`,
`RoutingVerifier`, `ParseIssuers`, `NewRoutingVerifier`), wired in `main.go` via
`GEMOT_TRUSTED_ISSUERS`, covered by `remote_test.go` and `tests/remote_trust_test.go`.

External issuer mints a gemot-native `principal.Credential`, but signs it with the
**issuer's** ed25519 key instead of the principal's. Issuer public keys live directly in
`GEMOT_TRUSTED_ISSUERS`. **No network on the hot path at all** — this eliminates T4/T5/T6
entirely for the first cut and forces us to get the trust model (allowlist + namespace
binding + issuer-signs-not-principal + routing) correct in isolation.

Reuses `SigningPayload`, `Validate`, `CoversTarget`, `Result` verbatim. The only new logic:
resolve the signing key from the issuer (not the principal) and enforce namespace binding.

### Phase 2 — JWKS key resolution
Replace config-pinned issuer keys with a JWKS endpoint per issuer: dynamic key discovery and
rotation. Adds the network-hygiene controls in §4.6. Everything else from Phase 1 is
unchanged.

### Phase 3 — External JWT act-claim import
Accept standard RFC 8693 `act`-claim JWTs (per `docs/act-claim.schema.json`) in the bearer
slot, mapping `sub`→principal, innermost `act.sub`→signing agent, `scope`, `iss`, `exp` into
a `Result`. This is full external-format interop and the largest surface (JWT parsing, `aud`
checks, per-issuer alg pinning). Deferred until Phases 1–2 are in production.

## 6. Implementation surface

New package `internal/principal` additions (keep the store out of it, as today):

```
// RemoteIssuer describes one trusted external authority.
type RemoteIssuer struct {
    Name        string   // matches Credential.Issuer
    Namespaces  []string // principal prefixes this issuer may vouch for (§4.2)
    PublicKey   []byte   // Phase 1: pinned ed25519 key
    Algo        string   // "ed25519" in Phase 1
    // Phase 2: JWKSURL string + cache
}

// IssuerVerifier verifies credentials signed by a configured RemoteIssuer.
// Implements principal.Verifier. Looks up the ISSUER's key (not the principal's),
// enforces namespace binding, then reuses the same expiry/scope/signature checks.
type IssuerVerifier struct { issuers map[string]RemoteIssuer }

// RoutingVerifier dispatches on cred.Issuer to LocalVerifier or an IssuerVerifier.
// Implements principal.Verifier. This is what SetPrincipalVerifier installs.
type RoutingVerifier struct {
    local  principal.Verifier
    remote map[string]principal.Verifier // by issuer name
}
```

Wiring:
- `internal/config/config.go` — parse `GEMOT_TRUSTED_ISSUERS`; reject overlapping namespaces
  at load (fail-closed boot). Follow the existing `envOr/envBool/envInt` helper style.
- `main.go:180` and `internal/mcp/http.go` (and `internal/calibration/cli.go:99` if it needs
  it) — after `NewService`, if trusted issuers are configured, build a `RoutingVerifier`
  wrapping the default `LocalVerifier` and call `svc.SetPrincipalVerifier(...)`.
- **No schema change.** `principal_credential` already stores the JSON credential; `Result`
  already carries `Issuer` for the audit trail.
- `internal/mcp/protected_resource.go` / `COMPOSING.md` — document the federation surface
  once shipped (which issuers a deployment trusts is operator-visible metadata).

## 7. Config schema (Phase 1)

`GEMOT_TRUSTED_ISSUERS` as JSON (mirrors how richer config already travels), e.g.:

```json
[
  {
    "name": "https://acme.example",
    "namespaces": ["acme:", "did:web:acme.example:"],
    "public_key": "base64-ed25519-spki-or-raw",
    "algo": "ed25519"
  }
]
```

Validation at load: non-empty name; ≥1 namespace; namespaces pairwise-disjoint across all
issuers **and** disjoint from the reserved `local` space; valid key via
`auth.ValidatePublicKey`. Any failure aborts startup.

## 8. Test plan (adversarial-heavy — this is the point)

Unit (`internal/principal`):
- issuer key verifies a well-formed federated credential; wrong issuer key → `ErrVerifyFailed`.
- **namespace-binding**: issuer vouches for a principal outside its namespace → reject; issuer
  vouches for a principal that has a local key → reject (T1).
- untrusted / empty-in-remote-slot / unknown issuer → reject, never defaulted (T2).
- relabelled issuer (label ≠ signing key) → `ErrVerifyFailed` because label is signed (T10).
- expiry, scope attenuation, agent mismatch still enforced on the remote path.
- routing: `""`/`local` → LocalVerifier; allowlisted → IssuerVerifier; garbage → reject.

Config: overlapping namespaces → boot fails; malformed key → boot fails; empty config →
remote off, LocalVerifier only (backward-compat).

Integration (`tests/`, adversarial suite):
- end-to-end submit with a federated credential + `provePossession` (agent must have a
  locally-registered key matching cnf).
- **leaked-credential replay**: a second agent presents a captured federated credential →
  rejected by `provePossession` (T8).
- de-trust: remove issuer, reload → previously-valid credential now rejected (T7).

## 9. Out of scope / deferred (explicit)

- **Relaxing `provePossession`** to accept an unregistered cnf key on the issuer's word
  (true issuer-asserted holder binding). Weakens tenant isolation; only revisit with a
  concrete federation partner and its own threat review.
- **JWKS (Phase 2)** and **JWT import (Phase 3)** — sequenced above, not v1.
- **Issuer revocation lists / OAuth introspection** — expiry + kill-switch suffice for v1.
- **Preference/context resolution** for a federated principal — stays out of the signed
  payload permanently (the append-only-log constraint in `credential.go`); resolve at read
  time if ever needed.

## 10. Effort

- Phase 1: ~1 day. New types + config parse + wiring + adversarial tests. No hot-path
  network, no schema change, no changes to `provePossession` or the submit path.
- Phase 2 (JWKS): ~1–2 days, dominated by the network-hygiene controls and their tests.
- Phase 3 (JWT import): ~2–3 days; separate design pass on `aud`/alg pinning before starting.

## 11. Alignment with current best practice (mid-2026)

This design was cross-checked against the current standards and guidance. Each maps to a
control above; nothing here contradicts the shipped `internal/principal` design — in several
places the newest specs have converged *toward* choices gemot already made.

| External practice (mid-2026) | Where it lands here |
|---|---|
| **SPIFFE trust domains** — issuance boundary prevents cross-tenant impersonation; **federation** validates identity across domains | §4.2 namespace binding *is* a trust domain; the whole feature is trust-domain federation |
| **WIT-SVID (SPIFFE, 2026)** makes **`cnf` proof-of-possession mandatory**, structurally closing JWT bearer-token replay | gemot's `provePossession` (cnf key + signature) already does exactly this and stays unchanged (§2, T8) — we are *ahead of*, not behind, the curve here |
| **RFC 9449 DPoP** / **RFC 9700** (OAuth Security BCP, Jan 2025) — sender-constrain tokens so theft is a non-event | Same property `provePossession` provides; a captured credential is inert |
| **RFC 9700 / OWASP** — verify `aud` deliberately, single audience preferred | §4.7 / T11 (JWT phase); native creds are gemot-shaped already |
| **OWASP JWT** — explicit algorithm allowlist, reject `alg:none`, never infer alg | §4.4, T3 |
| **OWASP JWT** — reject header-supplied key sources (`jku`/`jwk`/`x5u`/`x5c`) | §4.4, T3b |
| **JWKS as a dynamic dependency** — select by `kid`, cache, refresh-on-unknown-kid, expect overlapping keys during rotation | §4.6 (Phase 2) |
| **AIP: Agent Identity Protocol for Verifiable Delegation across MCP and A2A** (2026) — emerging agent-delegation work in gemot's exact domain | Track as prior art / possible Phase 3 interop target alongside the act-claim schema |

Sources: SPIFFE/SPIRE trust-domain & federation docs and the 2026 WIT-SVID PoP update;
RFC 9700 (OAuth 2.0 Security BCP, 2025); RFC 9449 (DPoP); OWASP JWT guidance on algorithm
allowlisting and header key-injection; RFC 8693 (`act`-claim vocabulary, already reflected in
`docs/act-claim.schema.json`); and the 2026 AIP verifiable-delegation paper.
