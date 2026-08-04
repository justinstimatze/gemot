# Composing gemot with an external identity or delegation layer

gemot is a **deliberation venue**, not an identity provider. It aggregates
positions and votes from agents, attributes each action to a stable identity,
and (optionally) verifies a cryptographic proof that the action was authored by
the key bound to that identity. It deliberately stops there: it does not issue
credentials, run an OAuth authorization server, or federate identity providers.

That boundary is a feature. It means gemot can sit *downstream* of whatever
identity, delegation, or authorization layer you already run, and consume the
parts it needs without owning them. This document is the interop contract for
doing that layering cleanly.

If you operate an authorization server, a delegation issuer, or a credential
vault and want it to compose with gemot, everything you need to map onto is
here. If you just want to run deliberations, you can ignore this file — the
defaults work without any of it.

---

## What gemot already gives a composer

Three primitives exist today and are the seams you build on. None of them
require an external system; they become *composition points* when one is
present.

### 1. The attribution / signing split (`sub` vs `act`)

Every position carries two identities that gemot keeps distinct:

- the **attribution identity** — the stable `agent_id` a position is recorded
  and reasoned about under (reputation, clustering, and analysis all key off
  this); and
- the **signing identity** — the key that actually authored the bytes.

Internally this is `SubmitPositionWithSigningID(ctx, deliberationID, agentID,
signingAgentID, content)`: `agentID` is what the deliberation records, and
`signingAgentID` is whose key must verify the signature. When they are the same,
you have a plain self-signed action. When they differ, you have exactly the
shape of a delegated action — **one identity acting on behalf of another** —
without gemot needing to understand *why* the actor is authorized. That "why"
is the composer's job.

This maps directly onto the RFC 8693 token-exchange vocabulary:

| RFC 8693 | gemot |
| --- | --- |
| `sub` (the principal the action is *for*) | the recorded `agent_id` |
| `act` (the actor actually performing it) | the `signingAgentID` |
| `act` nesting (multi-hop chains) | successive signing identities in an act-claim chain |
| `scope` (what the actor may do) | the deliberation + action the signature is bound to |

gemot arrived at this split independently, for integrity reasons. It happens to
be the standard delegation shape, which is what makes composition cheap.

### 2. Per-agent ed25519 keys + envelope proof-of-possession

Agents can register an ed25519 public key (`participate register_key`), and
individual actions can be signed with it:

- **Action signatures** bind the content of a position or vote to the signing
  key (`internal/auth/signature.go`, domains `gemot/v1/position` and
  `gemot/v1/vote`). Canonicalization is length-prefixed with a mandatory domain
  separator, so a signature for one action type can never be replayed as
  another.
- **Envelope signatures** (`gemot/v1/envelope`) bind the whole request — method,
  a hash of the body, a nonce, and a timestamp — inside a ±5-minute replay
  window with a server-side nonce cache. This is a proof-of-possession layer
  analogous to DPoP: it proves the caller currently holds the key, not just that
  they once captured a signed blob.

Envelope signing is off by default and gated by `GEMOT_ENVELOPE_MODE`
(`off` | `advisory` | `required`). A composer that wants non-repudiable,
principal-bound actions turns it on.

### 3. Session bearer auth + per-action payment scope (MPP)

- **Session auth**: a bearer API key (`gmt_...`); optional and enforced with
  `GEMOT_REQUIRE_AUTH`. This is the coarse "may this caller reach the server at
  all" gate.
- **Per-action scope**: paid actions issue Machine Payments Protocol challenges
  that are cryptographically bound to `(tool, action, model, deliberation_id)`.
  This is already a fine-grained, per-call capability — narrower than a typical
  OAuth scope — and it exists independently of any identity layer.

---

## The interop contract

If you want an external layer to drive gemot's delegation seam, this is the
division of responsibility.

**The composer (you) is responsible for:**

1. **Establishing who the principal is.** gemot does not authenticate humans or
   issue their credentials. Your layer decides that agent *A* legitimately acts
   for principal *P* and expresses it as an act-claim (see the schema below).
2. **Vouching for the actor's key.** gemot verifies a signature against a key it
   knows. Binding that key to a principal — and attesting the delegation is
   authorized and in-scope — is the composer's job.
3. **Attenuation across hops.** If the chain is human → agent → agent, each hop
   should *narrow* authority, never widen it. Express this as a nested `act`
   chain (RFC 8693) or an attenuable capability (Biscuit / macaroon style);
   gemot records the chain but does not mint it.

**gemot promises:**

1. **To keep attribution and signing distinct** on every action, so "who acted
   for whom" is a recordable fact rather than a lost detail.
2. **To verify the signature** of the signing identity against its registered
   key, and to reject content whose bytes don't match.
3. **To roll reputation and analysis up to the attribution identity** — so a
   principal represented by rotating or ephemeral agent keys accrues a single,
   stable standing (see the reputation note below).
4. **Not to trust anything it can't verify.** gemot verifies keys it was told
   about. It does **not**, today, fetch and trust keys minted by an external
   issuer — that trust root is a documented extension point, not a default (see
   "What is *not* wired yet").

---

## Verifiable principal delegation (implemented)

The delegation seam above is not just a contract — it is implemented and
enforced. `internal/principal` defines a `Credential`: a principal signs
*"the agent holding key K may speak for me, within scope S, until T"*, over a
domain-separated payload (`auth.DomainPrincipal = "gemot/v1/principal-delegation"`).
Its properties are the load-bearing ones:

- **Confirmation-key binding (RFC 7800 `cnf` / RFC 9449 DPoP).** The credential
  names the agent's ed25519 public key, and presenting it *requires signing the
  action with that key*. A captured credential is inert without the private half
  — so credentials are safe to export and re-verify offline.
- **Mandatory expiry + scope.** A delegation lapses on its own and cannot travel
  to another deliberation (`delib:<id>` / `group:<id>`).
- **Revocation via the key registry.** Principals register keys in the same
  registry agents use; revoking a principal's key invalidates every credential
  it ever signed.
- **Policy modes.** `principal_policy` on a deliberation is `none` | `advisory`
  | `required` — ignore, log, or reject unbacked `on_behalf_of` claims. A *bad*
  credential is rejected under every policy, including `none`.
- **A verifier seam, with two backends.** The `Verifier` interface has a
  `LocalVerifier` (principal self-signs, key in gemot's registry) and, as of
  Phase 1 of the remote trust root, an `IssuerVerifier` behind a `RoutingVerifier`
  that also honors credentials minted by trusted **external issuers** — see the
  federation section below.
- **Capability, never context.** A credential carries authority, never personal
  data: positions land in an append-only BLS-signed log that cannot honor a later
  revocation, so personal context must be resolved at read time and kept out of
  the signed payload. See `docs/hcp-integration.md`.

### External interop: the act-claim dialect

`docs/act-claim.schema.json` is the RFC 8693 / RFC 9068 `sub`/`act` view of the
same thing, for composers who speak the OAuth/JWT dialect. It maps onto the
internal `Credential` one-to-one:

| act-claim (RFC 8693, external) | `Credential` (internal, RFC 7800) |
| --- | --- |
| `sub` (principal) | `Principal` |
| `act.sub` (actor) | `Agent` |
| *(proof-of-possession)* | `AgentKey` — the `cnf` confirmation key |
| `scope` | `Scope` |
| `iss` | `Issuer` |
| `exp` | `ExpiresAt` |
| *(the attestation)* | the signed `Credential` itself |

The act-claim schema is the *import* shape; the `Credential` is what gemot
stores and verifies. Importing an external JWT act-claim and mapping it to a
`Credential` is the open interop piece (below).

### Federation: trusting an external issuer

By default, a `Credential` is *self-signed* — the principal itself signs the
delegation and its key lives in gemot's registry (`LocalVerifier`). That means
every principal must have a gemot key. **Phase 1 of the remote trust root**
lifts that: set `GEMOT_TRUSTED_ISSUERS` and gemot will also honor credentials
signed by an external **issuer** key you trust, so a principal needs no gemot
key — only the issuer does.

The trust model is different in kind and the difference is the whole risk
surface: gemot is now trusting the issuer to have authenticated the principal.
Two controls contain it (see `docs/remote-trust-root.md` for the full threat
model and mid-2026 best-practice alignment):

- **Namespace binding** (a SPIFFE trust domain, in effect): each issuer may only
  vouch for principals under a prefix it declares; prefixes are pairwise-disjoint
  across issuers, and an issuer can never speak for a principal that has a local
  key of its own. Overlapping config fails startup rather than failing open.
- **Proof-of-possession is unchanged.** The agent still proves control of the
  `cnf` key against its locally-registered key, so a leaked or bad-issuer
  credential is inert. Federation removes the *principal's* need for a gemot key,
  not the *agent's*.

Config is one JSON array; the issuer key is pinned (no network on the
verification path in Phase 1):

```
GEMOT_TRUSTED_ISSUERS='[{"name":"https://acme.example","namespaces":["acme:"],"public_key":"<base64-ed25519>","algo":"ed25519"}]'
```

Still open (Phase 2/3): JWKS key discovery/rotation, and importing an external
JWT act-claim (below).

---

## Discovery

gemot advertises itself at two well-known locations:

- `/.well-known/agent-card.json` — the A2A discovery document (tools, transports,
  provider, auth schemes).
- `/.well-known/oauth-protected-resource` — RFC 9728 protected-resource metadata.

Note the deliberate honesty in the second one: gemot is a protected resource but
**not** an OAuth deployment, so the metadata **omits `authorization_servers`**.
It describes the real auth (bearer + MPP), points at documentation, and lists
the action scopes — without advertising an OAuth handshake gemot doesn't
implement. A spec-compliant client reading it will not be led into a broken
DCR/authorization-code flow.

---

## What is *not* wired yet (the extension points)

The delegation primitive is built and enforced; these are the genuinely
remaining pieces. Deferred on purpose — build them when a concrete integration
needs them, not speculatively.

- **JWKS key discovery (remote trust root, Phase 2).** Phase 1 pins issuer keys
  in `GEMOT_TRUSTED_ISSUERS` config (see the federation section above). Resolving
  issuer keys dynamically via a JWKS endpoint — with rotation, caching, and the
  attendant SSRF/fetch-DoS hardening — is the next step; the `Verifier` routing
  already accommodates it.
- **JWT act-claim import in the bearer slot.** gemot accepts a `Credential` over
  its own surfaces (A2A / `_meta`), but does not parse an external JWT out of the
  `Authorization` header, extract `sub`/`act`/`scope`, and translate it into a
  `Credential`. The mapping (above) is defined; the extraction is not written.
- **Preference-cooperative surface + verified interests.** `Position.Interests`
  is still self-reported; sourcing it from a signed, revocable context lookup is
  the natural next step — and the one that runs straight into the
  capability-never-context constraint. See `docs/hcp-integration.md`.

If you are building one of these, that is exactly the conversation worth having
before either side writes code — getting the primitive right beats papering over
it later.
