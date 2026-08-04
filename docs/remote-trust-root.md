# Remote trust root — design & security plan

Status: **Phases 1 & 2 implemented** (`internal/principal/remote.go` + `jwks.go`, wired in
`main.go` via `GEMOT_TRUSTED_ISSUERS`); **Phase 3 designed** (§5.3 + §12, this document —
not yet built). This document specs the second
`principal.Verifier` backend: verifying delegation credentials minted by an **external
issuer** whose keys gemot does not hold. It is written security-first because this feature,
unlike everything else in `internal/principal`, deliberately **transfers trust to a third
party**.

See also: `internal/principal/remote.go` + `remote_test.go` (Phase 1),
`internal/principal/jwks.go` + `jwks_test.go` (Phase 2 JWKS resolution + SSRF guard),
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
| T11 | **Confused-deputy via `aud`** — credential minted for another resource replayed at gemot | Verify `aud` names this gemot deployment; **mandatory** on the JWT path (§4.7); native creds are gemot-shaped already |
| T12 | **cnf-stripping / bearer downgrade** — an imported JWT with no confirmation key would make the delegation a bearer token, undoing `provePossession` | JWT import **rejects any act-claim lacking a `cnf` key** (§4.8, §12.2); the mapped `Credential.Validate` also hard-requires `AgentKey`. No cnf ⇒ no import, ever |
| T13 | **PII exfiltration into the append-only log** — a JWT carries `email`/`name`/custom claims that would land, unrevocable, in the BLS-signed position log | Only the capability claims (`sub`/`act`/`cnf`/`scope`/`aud`/`iss`/`exp`/`nbf`/`iat`/`jti`) are read; the raw token and all other claims are **never persisted or logged**; import stores the translated `Credential`, which has no free-form claim bag (§4.9, §12.3) |
| T14 | **Unverified delegation chain / scope widening** — multi-hop `act` chain or attenuation gemot cannot itself check | gemot verifies the **issuer's** signature over the whole token and trusts the issuer for chain integrity + attenuation (its documented job); only the **innermost** `act.sub` is PoP-checked; the chain is recorded for audit, never re-derived as authority (§12.2) |
| T15 | **JWS-vs-native signature confusion** — a JWS-verified token treated as a native `Credential`, or vice-versa, skipping one signature check | The two paths verify different bytes (JWS signing-input vs `SigningPayload()`); the imported `Credential` carries no native signature and is **never routed through native-signature verification** — it is produced already-verified and only re-shares the non-signature checks (§12.4) |
| T16 | **Algorithm confusion on the JWT path** (`alg:none`, RS/HS swap, header-named key) — realizing T3/T3b | EdDSA-only decode; explicit method allowlist (`jwt.WithValidMethods`); `alg:none` rejected; `jku`/`jwk`/`x5u`/`x5c` header key-source params ignored; key chosen only by `(iss, kid)` against configured trust (§4.4, §12.2) |

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

### 4.6 Network hygiene (Phase 2) — *implemented*
All of this lives in `internal/principal/jwks.go`:
- JWKS URLs originate **only** from operator config (`GEMOT_TRUSTED_ISSUERS`); never fetched
  from a URL named in a credential/token.
- The HTTP client's `net.Dialer.Control` hook refuses to connect to any non-public address —
  loopback, link-local (`169.254.0.0/16`, incl. the `169.254.169.254` metadata IP), private
  ranges, CGNAT, and the documentation/benchmarking reservations. Because the check runs on
  the **resolved connect address**, it defends against DNS rebinding and redirect-to-internal,
  not just literal private URLs. Redirects are not followed at all. Relaxed only by the
  explicit `GEMOT_JWKS_ALLOW_PRIVATE` opt-in (internal-issuer / local testing).
- **TLS required** (config rejects any non-`https` `jwks_url`, and this is *not* relaxed by
  `GEMOT_JWKS_ALLOW_PRIVATE` — key material always travels over TLS). Response body is
  size-capped (1 MiB) and the keyset count is capped (32).
- Cache with a capped TTL (default 5 min, `GEMOT_JWKS_CACHE_TTL_SECONDS`). Fetches are
  single-flighted (mutex-held) and **rate-limited to at most one attempt per TTL**, so a
  flood of unverifiable credentials cannot amplify into a flood of outbound fetches. A
  transient outage serves the last-good keys; a never-reachable endpoint fails **closed**
  (`ErrKeyLookup`, reject) rather than hanging the submit path.
- Pre-warmed on startup (`RoutingVerifier.Prewarm`) so the hot path is cache-only in the
  common case; a startup fetch failure is logged, not fatal.

**Key selection without `kid`.** Native gemot credentials carry no JWT header, so there is no
`kid` to select on. The verifier tries every currently-published Ed25519 key for the issuer;
a signature either matches one of the issuer's real keys or it does not. This tolerates
rotation for free — an issuer publishes the new key alongside the old before cutting over — and
keeps the wire format unchanged. Per-`kid` cache keying (§4.6 as originally drafted) becomes
relevant only in the JWT phase (Phase 3), where tokens carry a `kid` header.

### 4.7 Audience restriction (JWT phase) — *mandatory*
Per RFC 9700 and OWASP, verify `aud` names *this* gemot deployment and reject otherwise —
this stops a token minted for another resource from being replayed at gemot (confused deputy,
T11). On the external-JWT path `aud` is **mandatory, not "when present"**: a JWT is a portable,
self-contained artifact, so a missing audience is an open replay window, not a convenience.
The expected value is operator-configured (`GEMOT_JWT_AUDIENCE`, e.g. `https://gemot.dev`);
if JWT import is enabled without an audience configured, the importer fails closed rather than
accepting unaudienced tokens. Single audience preferred; a token whose `aud` is a list is
accepted only if this deployment's identifier is a member. Native Phase-1/2 credentials are
already gemot-shaped (verified against a gemot `Target`) and carry no `aud`, so this control is
specific to the external-JWT path.

### 4.8 Confirmation-key binding survives import — *the Phase 3 control* (T12)
This is to Phase 3 what namespace binding (§4.2) is to Phase 1: the one control that, if
missed, silently collapses the whole security model. Everything already shipped is
sender-constrained — a captured credential is inert because `provePossession` requires the
agent to sign the action with the private half of the `cnf` key (§2, T8). An imported JWT
**must carry that same `cnf` key**, or the delegation it produces would be a plain bearer
token: anyone holding the JWT could submit under it.

Therefore JWT import **rejects any act-claim without a confirmation key.** The `cnf` claim
(RFC 7800, `{"cnf":{"jwk":{"kty":"OKP","crv":"Ed25519","x":"…"}}}`) maps to the credential's
`AgentKey`; the innermost `act.sub` names the agent that must hold it. Two independent gates
enforce this and both must pass: the importer refuses a token with no usable `cnf`, and the
mapped `Credential.Validate()` already hard-requires `AgentKey` ("a credential bound only to
an agent name is a bearer token", `credential.go:217`). After import, the unchanged
service-layer `provePossession` still checks the agent controls that key against its
**locally-registered** key — so, exactly as on the native path, the *agent* still registers a
gemot key; only the *principal* is federated. This also requires a `cnf` member to be added to
`docs/act-claim.schema.json` (§12.6) — the schema is `additionalProperties: false`, so today a
`cnf` would be *rejected*, which is itself a signal that the external vocabulary was never
finished for a proof-of-possession import.

### 4.9 Claim minimization & no raw-token persistence — *the privacy control* (T13)
A `Credential` is deliberately a capability, never personal context: positions land in an
append-only BLS-signed log that cannot honor a later revocation, so anything persisted there
is effectively permanent (`credential.go` package doc). JWTs routinely carry PII — `email`,
`name`, group memberships, arbitrary custom claims. Importing one therefore runs straight into
that constraint, and the rule is strict:

- **Read only the capability claims** — `iss`, `sub`, `act` (chain), `cnf`, `scope`, `aud`,
  `exp`, `nbf`, `iat`, `jti`. Every other claim is ignored.
- **Never persist the raw token.** The append-only record stores the *translated*
  `Credential` (which has no free-form claim bag), never the JWT bytes. A JWT with an `email`
  claim must leave no `email` anywhere durable.
- **Never log claim values.** Diagnostics log `(iss, kid, aud-ok, decision)` and at most a
  short hash/prefix of `sub` — never the token, never `sub`/`act` in the clear at info level.
  This extends the existing "never log secret values" house rule to PII.
- **Treat `sub`/`act.sub` as opaque strings.** gemot does not parse them for structure and
  does not need to; issuers SHOULD mint opaque/pairwise subject identifiers rather than
  emails. This is a documented expectation of a composing issuer, not something gemot can
  enforce — but because gemot never persists anything *but* the identifier it was handed, an
  issuer that uses an email is exposing its own users, not widening gemot's storage.
- **Ignore `attestation` entirely in v1.** The schema's `attestation.ref` may be a URL;
  gemot does **not** fetch it (SSRF, §4.6) and does not persist it. Chain evidence is the
  composer's to hold.
- **Reject unknown claims** rather than silently dropping them, mirroring the schema's
  `additionalProperties: false`: an unexpected claim means the token is not the shape gemot
  agreed to consume, and surfacing that is safer than quietly ingesting it.

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

### Phase 2 — JWKS key resolution ✅ *implemented*
Shipped in `internal/principal/jwks.go` (`jwksKeySource`, `fetchJWKS`, `parseJWKS`, the
SSRF-guarded client + `isBlockedIP`/`blockPrivateDial`, `validateJWKSURL`), with the routing
options (`WithJWKSAllowPrivate`, `WithJWKSCacheTTL`, `WithJWKSHTTPClient`, `WithClock`) and
`RoutingVerifier.Prewarm` in `remote.go`. Covered by `jwks_test.go` (parse, SSRF table,
cache, rotation, fail-closed, rate-limit, prewarm) and `tests/remote_trust_test.go`
(`TestRemoteTrust_JWKSBackedCredentialAccepted`).

Each issuer sets **exactly one** of `public_key` (Phase 1, pinned) or `jwks_url` (Phase 2,
resolved). A `jwks_url` issuer's keys are fetched over the SSRF-guarded client and cached;
rotation is picked up on the next post-TTL refresh with no config change. Adds the
network-hygiene controls in §4.6. Everything else from Phase 1 — namespace binding, routing,
issuer-signs-not-principal, `provePossession`, the wire format — is unchanged.

### Phase 3 — External JWT act-claim import
Accept standard RFC 8693 `act`-claim JWTs (per `docs/act-claim.schema.json`), verify the
**issuer's** JWS with the trust root Phases 1–2 already built, and translate the verified
claims into an internal `Credential`/`Result` that flows through the *unchanged* submit path
and `provePossession`. This is full external-format interop and the largest new surface (JWT
parsing, `aud`/`cnf` checks, alg pinning, PII discipline). **The full design is §12.** The
one-paragraph shape:

- **Reuses the trust root, does not rebuild it.** The verified `iss` selects an entry in
  `GEMOT_TRUSTED_ISSUERS`; the issuer's key is resolved by the *same* pinned-or-JWKS
  `keySource` from Phases 1–2; `sub` is subject to the *same* namespace binding (§4.2) and
  local-shadow rejection (T1). Phase 3 adds a decoder and a translator, not a second trust
  model.
- **Verifies a different signature.** The JWT path verifies the issuer's **JWS** over the
  compact token's signing input — not a native `SigningPayload()` signature. The translated
  `Credential` is therefore produced *already-verified* and must never be re-routed through
  native-signature verification (T15).
- **cnf and aud are mandatory** (§4.8, §4.7); PII never lands in the append-only log (§4.9).
- **Gated behind its own switch.** Trusting an issuer for native credentials does not
  auto-enable consuming that issuer's JWTs; Phase 3 requires `GEMOT_ACCEPT_ACT_CLAIM_JWT=true`
  **and** `GEMOT_JWT_AUDIENCE` set (§7).

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

## 7. Config schema

`GEMOT_TRUSTED_ISSUERS` as JSON (mirrors how richer config already travels). Each issuer sets
**exactly one** of `public_key` (Phase 1, pinned) or `jwks_url` (Phase 2, resolved):

```json
[
  {
    "name": "https://acme.example",
    "namespaces": ["acme:", "did:web:acme.example:"],
    "public_key": "base64-ed25519-raw",
    "algo": "ed25519"
  },
  {
    "name": "https://beta.example",
    "namespaces": ["beta:"],
    "jwks_url": "https://beta.example/.well-known/jwks.json",
    "algo": "ed25519"
  }
]
```

Companion env vars (JWKS only):
- `GEMOT_JWKS_ALLOW_PRIVATE` (default `false`) — permit fetches to non-public addresses
  (internal issuer / local testing). `https` is still required regardless.
- `GEMOT_JWKS_CACHE_TTL_SECONDS` (default `300`) — key cache TTL; also the fetch rate-limit
  window.

Companion env vars (Phase 3, external-JWT import only — off by default):
- `GEMOT_ACCEPT_ACT_CLAIM_JWT` (default `false`) — master switch for consuming external
  act-claim JWTs. Deliberately separate from `GEMOT_TRUSTED_ISSUERS`: trusting an issuer for
  native credentials does not imply consuming its JWTs, which is a larger surface. Both the
  trusted-issuer set and this switch must be set for import to run.
- `GEMOT_JWT_AUDIENCE` (no default) — the canonical resource identifier this deployment
  answers to (e.g. `https://gemot.dev`), checked against every imported token's `aud` (§4.7).
  If `GEMOT_ACCEPT_ACT_CLAIM_JWT=true` but this is unset, startup fails closed — an
  unaudienced JWT importer is a replay hole.
- `GEMOT_JWT_LEEWAY_SECONDS` (default `60`) — symmetric clock-skew leeway for `exp`/`nbf`/`iat`
  (T9); capped small.

Validation at load (all fail-closed, aborting startup): non-empty name; name not the reserved
`local`; ≥1 namespace; namespaces pairwise-disjoint across all issuers; exactly one of
`public_key`/`jwks_url`; pinned keys valid via `auth.ValidatePublicKey`; `jwks_url` a valid
`https` URL with a host.

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
- **JWKS (Phase 2)** — shipped. **JWT import (Phase 3)** — designed (§12), not yet built.
- **JWT-as-session-auth / full OAuth resource-server mode.** Phase 3 carries the act-claim in
  a dedicated request field, *not* the `Authorization` header, and does **not** turn gemot into
  an OAuth resource server (§12.1). Making the delegation JWT double as session auth would
  require gemot to advertise `authorization_servers`, implement DCR, and abandon the
  deliberate honesty of `/.well-known/oauth-protected-resource` (Move 1). A separate, larger
  decision — not this phase.
- **Per-hop verification of a multi-hop `act` chain.** gemot verifies the issuer's signature
  over the whole token and trusts the issuer for chain integrity and per-hop attenuation
  (T14); it does not independently verify that each actor signed the next. Revisit only with a
  concrete multi-hop partner.
- **Algorithms beyond EdDSA** (ES256, etc.). The importer is EdDSA-only, matching the native
  `Credential` and minimizing the JOSE attack surface (§12.2). Add per-issuer alg pinning for
  a second algorithm only when a partner needs it.
- **Issuer revocation lists / OAuth introspection** — expiry + kill-switch suffice for v1.
- **Preference/context resolution** for a federated principal — stays out of the signed
  payload permanently (the append-only-log constraint in `credential.go`); resolve at read
  time if ever needed.

## 10. Effort

- Phase 1: ~1 day. New types + config parse + wiring + adversarial tests. No hot-path
  network, no schema change, no changes to `provePossession` or the submit path.
- Phase 2 (JWKS): implemented; dominated by the network-hygiene controls and their tests.
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

---

## 12. Phase 3 design — external JWT act-claim import

Phase 3 lets a composer present a standard RFC 8693 act-claim **JWT** instead of a
gemot-native `Credential`, and have gemot honor it. The whole of Phases 1–2 was building the
trust root this consumes; Phase 3 adds a decoder, a claim validator, and a translator — and
runs the result through the existing, unchanged submit path. The design goal is that *nothing
downstream of translation can tell a JWT-sourced delegation from a native one* — same
`Result`, same `provePossession`, same storage, same audit record.

### 12.1 Transport placement — correcting the "bearer slot"

Earlier notes (and `COMPOSING.md`) called this "JWT act-claim import **in the bearer slot**."
That phrasing is imprecise and, taken literally, would be a design mistake. gemot's
`Authorization: Bearer …` slot already carries the **session API key** (`gmt_…`) or the admin
secret — the coarse "may this caller reach the server and who pays" gate
(`a2a.go:119`, `http.go:461+`). A delegation act-claim answers an orthogonal question — "on
whose behalf is *this* position" — and must not evict or overload session auth.

**Decision: the act-claim JWT travels in a dedicated request field, not the `Authorization`
header.** Concretely, an `act_claim` string field, sibling to the existing
`principal_credential`, on both surfaces:

- MCP: a new `submit_position` arg / `_meta` entry (`server.go:249`, alongside
  `PrincipalCredential`).
- A2A: a new `act_claim` param (`a2a.go:576`, alongside `principal_credential`).

A caller presents **exactly one** of `principal_credential` (native) or `act_claim` (JWT);
presenting both is a request error. This keeps the two authentication axes cleanly separate,
preserves the deliberate honesty of `/.well-known/oauth-protected-resource` (gemot advertises
that it is **not** an OAuth deployment — Move 1), and means Phase 3 requires no change to
session auth or the middleware.

> The alternative — accept the delegation JWT *as* the `Authorization` bearer, making gemot an
> OAuth resource server — is a genuinely different and larger product commitment (DCR,
> `authorization_servers` metadata, protected-resource semantics). It is recorded as
> out-of-scope in §9, to be decided deliberately if a partner ever needs gemot to be their
> resource server, not backed into via a header-parsing convenience.

### 12.2 Verification pipeline (fail-closed at every step)

Given `act_claim` (a compact JWS `header.payload.signature`) and the presenting `agentID` +
`Target`, in order — each step rejects on failure, none is skipped:

1. **Structural decode**, size-capped (reuse a bound analogous to `maxJWKSBytes`). Reject
   anything that is not a well-formed compact JWS with exactly three segments.
2. **Algorithm allowlist.** Decode the header `alg`; accept **only `EdDSA`**. Reject
   `alg:none` and every other value. This is enforced by construction, not inference — see the
   library note (§12.5). (T3/T16)
3. **Ignore header key-source params.** `jku`, `jwk`, `x5u`, `x5c` are never read; the
   verifying key is chosen only from configured trust. (T3b/T16)
4. **Issuer trust + key selection.** Read the **payload** `iss`; require it to name an entry
   in `GEMOT_TRUSTED_ISSUERS` (fail closed on unknown — T2). Resolve that issuer's Ed25519
   key(s) via the *same* `keySource` Phases 1–2 use (pinned or JWKS-cached, SSRF-guarded). If
   the header carries a `kid`, prefer the matching key; absent a `kid`, try all published keys
   (rotation-tolerant, as on the native path).
5. **JWS signature verification** over the token's signing input, against the issuer key(s).
   This is the crypto root of trust for the whole token. (Distinct from native-signature
   verification — T15.)
6. **Audience.** `aud` is **mandatory** and must contain `GEMOT_JWT_AUDIENCE`. (T11, §4.7)
7. **Temporal.** `exp` mandatory and in the future; `nbf`/`iat` if present must be consistent;
   `GEMOT_JWT_LEEWAY_SECONDS` symmetric leeway. (T9)
8. **Confirmation key.** Extract `cnf` → an Ed25519 public key; **reject if absent or
   unusable** (T12, §4.8). This becomes `AgentKey`.
9. **Subject / actor.** `sub` → principal; the **innermost** `act.sub` → agent, which must
   equal the presenting `agentID` (agent-mismatch = replay of another's delegation). The
   chain, if multi-hop, is recorded for audit but only the innermost is PoP-checked (T14).
10. **Scope.** Map the `scope` array to the native scope (§12.3); reject a token that names a
    different deliberation than the `Target`.
11. **Namespace binding + local-shadow** on `sub`, reusing the *exact* Phase 1 checks (§4.2,
    T1): the issuer must be authorized for `sub`'s namespace, and `sub` must not have a local
    key.
12. **Claim minimization** throughout: only the claims listed in §4.9 are ever read; unknown
    claims are rejected; nothing but the translated `Credential` is persisted or logged.

Only if all pass is a `Result` produced — identical in shape to the native path — and handed
to the unchanged service layer, where `provePossession` still requires the agent to sign the
action with the `cnf` private key against its locally-registered key.

### 12.3 Claim → `Credential` mapping

| act-claim JWT (RFC 8693/9068 + RFC 7800) | internal `Credential` | notes |
|---|---|---|
| `iss` | `Issuer` | must be a trusted issuer; selects the verifying key |
| `sub` | `Principal` | opaque string; namespace-bound; local-shadow-checked |
| innermost `act.sub` | `Agent` | must equal presenting `agentID`; PoP target |
| `cnf` (Ed25519 JWK) | `AgentKey` | **mandatory** (§4.8); the whole PoP hinge |
| `scope` (array) → the `deliberation:<id>` token | `Scope` (`delib:<id>`) | see below |
| `exp` | `ExpiresAt` | mandatory |
| *(JWS over signing input)* | *(already-verified; no native `Signature`)* | T15 |
| `aud`, `nbf`, `iat`, `jti` | *(checked, not stored)* | temporal + audience gates |
| `email`, `name`, any other claim | *(rejected / never stored)* | §4.9, T13 |

**Scope mapping is intentionally narrow and must not fail open.** The act-claim `scope` is an
array of `<tool>:<action>` and/or `deliberation:<id>` tokens; the native `Credential.Scope` is
a single `""` | `delib:<id>` | `group:<id>` string. The importer maps the `deliberation:<id>`
(or a group token) to the native scope and binds the credential to that deliberation. The
`<tool>:<action>` tokens are a *different axis* — gemot's credential authorizes "this agent
may speak for this principal within this deliberation," it is **not** the tool-authorization
mechanism (that is the API key + MPP payment scope). So dropping `<tool>:<action>` tokens does
not widen what the credential grants. This is a real semantic boundary, stated so no composer
assumes gemot enforces tool-level attenuation through the delegation layer: **if a token's
`scope` names a deliberation, gemot binds to it; tool/action scoping is honored by the
payment/session layer, not the credential.** A token whose scope cannot be represented (e.g.
two conflicting `deliberation:` tokens) is rejected, not silently coerced.

### 12.4 Code seam

Keep all JOSE/crypto inside `internal/principal`, next to the trust root it reuses:

- **New `internal/principal/jwt.go`** with an `ActClaimImporter`:
  ```
  type ActClaimImporter struct {
      issuers  map[string]issuerEntry // shared with IssuerVerifier (same keySource)
      audience string
      leeway   time.Duration
      localLookup KeyLookup            // shared local-shadow check
      now      func() time.Time
  }
  func (i *ActClaimImporter) Import(ctx, tokenString, agentID string, t Target) (*Result, error)
  ```
- **Factor the shared post-signature checks** — issuer `covers` (namespace), `CoversTarget`,
  expiry, agent-match, local-shadow — into a helper used by *both* `IssuerVerifier.Verify`
  (native signature) and `ActClaimImporter.Import` (JWS signature). Only the signature step
  differs; everything else is identical, which is what keeps the two paths from drifting.
- **Do not route the imported `Credential` back through native-signature verification** (T15).
  `Import` returns a `*Result` directly. The cleanest service-layer shape mirrors how a
  verified native credential flows today: the transport, on seeing `act_claim`, calls
  `Import`, and the service threads the resulting `*Result` into the same slot a `Verifier`
  would have produced — then `provePossession` runs unchanged. (Concretely: a small
  service-layer branch "if act-claim present, import; else verify credential", both yielding a
  `*Result` before the identical PoP + persistence tail.)
- **Wiring** (`main.go`): build the `ActClaimImporter` from the same `[]RemoteIssuer` +
  options already parsed for the `RoutingVerifier`, only when `GEMOT_ACCEPT_ACT_CLAIM_JWT` is
  set; require `GEMOT_JWT_AUDIENCE` (fail-closed startup otherwise). Share the JWKS cache
  instances so a JWKS issuer isn't fetched twice.

### 12.5 Library decision

`github.com/golang-jwt/jwt/v5 v5.3.1` is **already in the module graph** (transitively), so
Phase 3 needs no new heavyweight dependency — promote it to a direct require. It supports the
two controls that matter: an explicit method allowlist via `jwt.WithValidMethods([]string{"EdDSA"})`
(so `alg:none` and RS/HS confusion are impossible), and a custom `Keyfunc` that selects the key
purely from `(iss, kid)` against configured trust while ignoring header key-source params. Use
`WithExpirationRequired()`, `WithAudience(GEMOT_JWT_AUDIENCE)`, and `WithLeeway(…)`.

The considered alternative — a minimal in-house EdDSA-only compact-JWS verifier — has an even
smaller attack surface (the parser literally cannot do anything but EdDSA), and remains the
fallback if the dependency ever proves troublesome. Given golang-jwt/v5 is already present,
actively maintained, and its allowlist API makes the EdDSA-only stance explicit and testable,
reuse wins for v1. Either way the property is the same: **exactly one algorithm, chosen by
us, never inferred from the token.**

### 12.6 Required change to `docs/act-claim.schema.json`

The external vocabulary is currently missing the one field a proof-of-possession import cannot
work without. Add a `cnf` member (and, because the schema is `additionalProperties: false`,
without this a compliant `cnf`-bearing token would be *rejected*):

```json
"cnf": {
  "type": "object",
  "description": "RFC 7800 confirmation. Binds the act-claim to a key the innermost actor must prove control of, making the token sender-constrained rather than bearer. REQUIRED for gemot import.",
  "properties": {
    "jwk": {
      "type": "object",
      "description": "The actor's public key as a JWK. gemot accepts kty=OKP, crv=Ed25519."
    }
  },
  "required": ["jwk"],
  "additionalProperties": false
}
```

This is a schema/doc change, not code, and is the natural companion to the §4.8 control. It
should ship *with* Phase 3 so the published contract and the importer agree.

### 12.7 Test plan (adversarial-heavy — as with Phases 1–2)

Unit (`internal/principal/jwt_test.go`):
- **Happy path**: EdDSA JWT from a trusted issuer with `cnf`/`aud`/`exp`/`sub`/`act` → a
  `Result` equal to the native path's for the same delegation.
- **Algorithm attacks**: `alg:none` → reject; RS256/HS256 token (incl. the classic "public key
  as HMAC secret") → reject; unknown `alg` → reject. (T16)
- **Header key-injection**: `jku`/`jwk`/`x5u`/`x5c` pointing at an attacker key → ignored,
  verification still uses configured trust → reject. (T16)
- **cnf**: token with no `cnf` → reject; `cnf` with a non-Ed25519 / malformed key → reject;
  `cnf` present but `act.sub` ≠ presenting agent → reject. (T12)
- **aud**: missing `aud` → reject; wrong `aud` → reject; list `aud` including us → accept. (T11)
- **Temporal**: expired → reject; `nbf` in the future → reject; within-leeway skew → accept. (T9)
- **Trust root reuse**: untrusted `iss` → reject (T2); `sub` outside issuer namespace → reject;
  `sub` with a local key → reject (T1); issuer key resolved via JWKS (pinned test client) → OK.
- **Privacy**: token with an `email`/custom claim → rejected as an unknown claim; and for the
  accepted happy-path token, assert the persisted `Credential` and all log output contain no
  PII and no raw token bytes. (T13)
- **JWS-vs-native**: a valid native `Credential` presented in the `act_claim` slot → reject;
  an imported result never carries a native signature. (T15)

Integration (`tests/`): end-to-end `submit_position` with `act_claim` + a matching
locally-registered agent key → `PrincipalVerified` true, indistinguishable from native;
leaked-token replay by a second agent → rejected by `provePossession` (T8); `act_claim` +
`principal_credential` both present → request error (§12.1).

Config: `GEMOT_ACCEPT_ACT_CLAIM_JWT=true` without `GEMOT_JWT_AUDIENCE` → startup fails closed;
switch off → `act_claim` rejected as unsupported.

### 12.8 Effort

~2–3 days. The trust root, key resolution, namespace binding, shadow check, and PoP are all
reused unchanged; the genuinely new work is the JOSE decode + strict claim validation + the
mapping + the (large) adversarial test matrix, plus the two transport fields and the schema
`cnf` addition.
