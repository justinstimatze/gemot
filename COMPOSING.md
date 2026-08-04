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

## The act-claim shape

`docs/act-claim.schema.json` is the concrete JSON Schema (draft 2020-12) for an
act-claim: the object a composer produces to assert that an actor is authorized
to act for a principal, within a scope, with an optional nested delegation
chain. It is a strict subset of the RFC 8693 / RFC 9068 vocabulary so it can be
carried inside a JWT `act` claim, a verifiable credential, or a capability token
without translation.

It is published as a **contract**, not as something gemot's write path enforces
end-to-end today. The `sub`/`act` split it maps onto is real and in use; the
external verification of the claim itself is the extension point below.

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

These are the honest gaps. They are deferred on purpose — none should be built
speculatively, only when a concrete integration needs them.

- **External trust root.** There is no `GEMOT_TRUSTED_ISSUERS` / JWKS fetch
  today. gemot verifies keys registered directly with it, not keys minted by an
  issuer it was merely told to trust. This is the real functional unlock for
  full delegation and is the first thing to add when a concrete composer exists.
- **JWT/act-claim in the bearer slot.** gemot does not currently parse a JWT out
  of the `Authorization` header, extract `sub`/`act`/`scope`, and map it to a
  service identity. The seam (the `sub`/`act` split) is ready for it; the
  extraction is not written.
- **End-to-end act-claim enforcement.** The schema is published and the storage
  split exists, but the write path does not yet *require* a verified act-claim to
  accept a delegated action. It records the chain; it does not gate on it.

If you are building one of these, that is exactly the conversation worth having
before either side writes code — getting the primitive right beats papering over
it later.
