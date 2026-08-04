# Verifiable principal delegation, and the Human Context Protocol

What gemot built — **verifiable principal delegation**, a self-contained
primitive — where that sits relative to the Human Context Protocol (HCP), and
what it deliberately has not built.

**On HCP's status:** HCP is an aspiration, not a fixed target — there is no
normative wire spec today (see "Deliberately not done"). gemot's delegation
credential stands on its own as a generic primitive; HCP is one integration it
is *shaped to accept*, not a dependency it is built around. Read the delegation
sections for what ships now; read the HCP sections for where it could go.

## What HCP is

Two related efforts share the name:

- **"Robust AI Personalization Controls: The Human Context Protocol"** — Anand
  Shah, Tobin South, Talfan Evans, Hannah Rose Kirk, Jiaxin Pei, Andrew Trask,
  E. Glen Weyl, Michiel Bakker (July 2025). The origin paper.
- **"The Future of Personal AI: Portable and Persistent Personal Memory through
  a Unified Human Context Protocol"** — Stanford Digital Economy Lab (June
  2026), with Alex "Sandy" Pentland, Tobin South, Dazza Greenwood, Jiaxin Pei,
  Zexue He, and Ben Moskowitz. Part of the **Loyal Agents** collaboration
  between Stanford's Digital Economy Lab and the Consumer Reports Innovation
  Lab.

The proposal treats user preferences as a **portable, user-governed layer**
rather than something each platform silos: scoped access, revocation, and data
minimization at inference time, so an agent can act on a person's behalf from
context that person actually controls.

**HCP is not an extension of the MCP specification.** It is a separate protocol
that *ships as* an MCP server, exposing roughly three tools — schema definition,
preference search, and preference update — between LLM clients and memory
managers. This matters architecturally: gemot is in the same posture. Both are
domain protocols delivered over MCP, so they compose as sibling servers in one
client with no spec changes on either side.

## Why the two fit together

HCP is a **read** protocol for one principal's context. Gemot is an
**aggregation** protocol across many principals.

> HCP answers *what does this person want*.
> Gemot answers *what should this group do, given what everyone wants*.

Neither does the other's job, and each names the other's job as future work.
The origin paper proposes aggregating compatible preferences for "coordination
problems ranging from scheduling to collaborative project planning," and
identifying "complementary patterns and potential compromises" — which is
`analyze:propose_compromise` and the [calendar scheduling
demo](calendar-scheduling.md), described from the outside. Its notion of
**preference cooperatives** needs exactly the instrument gemot already has:
PCA clustering, repness, and bridging scores tell a would-be cooperative
whether it is a coherent bloc or an average that represents nobody.

The intellectual lineage already overlaps. Michiel Bakker co-authored the
Habermas Machine; E. Glen Weyl is Plurality. Both are in gemot's
acknowledgments.

## What gemot built: verifiable delegation

`Position.OnBehalfOf` has existed since early on as the slot for "which
principal does this agent speak for." It was a free-text string: unverified,
self-asserted, non-portable. Any agent could claim to represent anyone, and
gemot would store that claim, export it, and hand it to auditors with nothing
behind it — including writing it to the tamper-evident log, which faithfully
recorded a claim it never checked.

`internal/principal` closes that. A **credential** is a delegation attestation:

```
principal signs → "the agent holding key K may speak for me,
                   within scope S, until T"
```

Verification binds five things:

| Bound | Attack it stops |
|---|---|
| `agent_key` | **The load-bearing one.** A captured credential replayed by anyone who does not hold the private key |
| `agent` | Casual misuse under a different name (a name is not authentication — see below) |
| `scope` | A credential minted for one deliberation presented in another |
| `expires_at` | A delegation outliving a revocation the verifier cannot observe |
| signature over all fields | Widening scope, extending expiry, swapping the confirmation key, or relabelling the issuer after minting |

### Why the key binding, not just the name

An earlier revision bound only `agent`, and that was a real vulnerability. The
credential names an agent, but *the presenter chooses what to call itself*.
Anyone who obtained a credential — from an export, from `get_positions`, from a
log — could submit under the same `agent_id` and inherit the delegation.
Hosted-mode namespacing did not help, because the name a credential binds is
the portable unscoped one. `signature_policy` did not help either: the
attacker's scoped identity had no registered key, so the `required` branch
treated it as an agent that never opted into signing and exempted it. The two
mechanisms had a gap exactly between them, and the result was Sybil position
stuffing carrying `principal_verified: true`.

The fix is proof-of-possession, the move that
[RFC 7800](https://www.rfc-editor.org/rfc/rfc7800) (`cnf`),
[RFC 9449](https://www.rfc-editor.org/rfc/rfc9449) (DPoP),
[RFC 8705](https://www.rfc-editor.org/rfc/rfc8705) (mTLS-bound tokens), and
W3C Verifiable Credentials holder binding all make. The credential commits to a
confirmation key, and presenting it requires signing the position with that
key:

- the key registered for the submitter's **stored** (namespaced) identity must
  equal the credential's `agent_key`, so another tenant's registration cannot
  satisfy it; and
- the position must carry a signature that verifies under that key.

Presenting a credential is therefore the opt-in to signing, regardless of
`signature_policy`. The cost is real — delegation now requires the agent to
have a registered key — but `principal_verified` previously meant "someone
typed the right name" and now means "the delegated key signed this position".

Enforcement deliberately lives in the service layer, **outside** `Verifier`.
Whether the principal issued a credential is a question an external authority
can answer; whether the presenter controls the key is a local fact about this
request, and must not become waivable by installing a permissive verifier.
There is a test for exactly that.

### Credentials are safe to disclose

Because the binding is to a key rather than a secret, a credential is inert to
anyone who captures it. That is what makes it safe for `export` and
`get_positions` to carry it, and it is the same property that lets DPoP-bound
tokens and verifiable credentials be logged and audited freely. Independent
offline re-verification becomes a feature rather than a leak.

Principals register keys in the **same identity→key registry agents already
use**, so there is no new table and revocation is inherited rather than
reimplemented: revoke the principal's key and every credential it ever signed
stops verifying, including ones already handed out and not yet expired.

### Policy

`principal_policy` on a deliberation mirrors `signature_policy`:

| Policy | Behavior |
|---|---|
| `none` (default) | `on_behalf_of` accepted as an unverified claim — every pre-existing deliberation is unaffected |
| `advisory` | Unbacked claims accepted, logged as `UNVERIFIED_PRINCIPAL` |
| `required` | `on_behalf_of` must carry a verified credential |

The two axes are independent. Policy governs whether proof must be **present**,
never whether bad proof passes: a forged, expired, replayed, or out-of-scope
credential is rejected under every policy including `none`.

### Using it

Both transports carry the full surface — `principal_policy` on create and
`principal_credential` on submit — and enforcement is transport-independent
regardless, since MCP and A2A funnel through the same
`SubmitPositionWithSigningID`.

Create a deliberation that demands backed claims:

```json
{"action": "create", "topic": "Q3 roadmap", "principal_policy": "required"}
```

Submit a position with a credential:

```json
{
  "action": "submit_position",
  "deliberation_id": "...",
  "agent_id": "alice-agent",
  "content": "...",
  "on_behalf_of": "human:alice",
  "signature": "<base64 — required whenever a credential is presented>",
  "principal_credential": {
    "principal": "human:alice",
    "agent": "alice-agent",
    "agent_key": "<base64 ed25519 public key>",
    "scope": "delib:...",
    "issuer": "local",
    "expires_at": "2026-08-01T00:00:00Z",
    "signature": "<base64>"
  }
}
```

Minting one (the principal's side):

```go
cred := principal.Credential{
    Principal: "human:alice",
    Agent:     "alice-agent",
    AgentKey:  agentPublicKey, // the key the agent will sign positions with
    Scope:     principal.ScopeDeliberationPrefix + delibID, // or "" for all
    Issuer:    principal.IssuerLocal,
    ExpiresAt: time.Now().Add(24 * time.Hour),
}
cred.Signature = ed25519.Sign(principalPrivKey, cred.SigningPayload())
```

Signing covers a fixed-shape length-prefixed record, never the JSON, so field
order and whitespace in transit cannot change what verifies.

### The full setup

Four steps, and both transports carry all of them:

1. The **principal** registers its key — `participate:register_key` for
   `human:alice`.
2. The **agent** generates a keypair and registers it —
   `participate:register_key` for `alice-agent`. Both transports namespace the
   stored identity per API key, so a client passes its own plain `agent_id` and
   the server scopes it; there is nothing for the caller to compute.
3. The principal signs a credential naming the agent's public key. This is the
   only offline step, and deliberately so — the principal is a human or org,
   not an API caller.
4. The agent submits with `principal_credential` **and** a `signature` over
   `auth.PositionPayload(agent_id, deliberation_id, round, content)`, signed
   with the confirmation key.

Steps 2 and 4 are what proof-of-possession costs. Step 4 is the one that
actually constrains clients: the signature is ed25519 over gemot's canonical
payload, which a model driving MCP tool calls cannot compute on its own. A
credentialed agent needs a client-side helper that holds a private key.

That is the security property rather than incidental friction — a credential
usable without holding a key is a password anyone who reads the deliberation
can copy. Casual use is not taxed: leave `principal_policy` at `none` and
`on_behalf_of` remains the one-call unverified hint it always was.

Note that the signature binds `round`, so a client that fetches state and then
submits across a round boundary must re-sign.

## The constraint that shapes everything

**A credential carries a capability, never personal context.**

This is a hard design rule, not an oversight. Positions land in an append-only
BLS-signed log, and *an append-only log cannot honor a later revocation*. Those
two guarantees — tamper-evidence and revocable consent — are in direct conflict
the moment personal context enters a signed payload.

Storing a capability keeps both intact: the log records that a delegation was
verified, revocation still works because it operates on the key, and no
preference data was ever written to something that cannot forget.

Any future integration that resolves richer principal context — an HCP
preference lookup being the obvious one — must **resolve it at read time and
keep it out of the signed payload**. Reference by scoped credential; never
inline.

## Plugging in an HCP server

`principal.Verifier` is the seam:

```go
type Verifier interface {
    Verify(ctx context.Context, cred *Credential, agentID string, t Target) (*Result, error)
}
```

`LocalVerifier` is the built-in backend. An HCP-backed verifier implements the
same interface and is installed with `svc.SetPrincipalVerifier(...)` — no
change to the service layer, the transports, or the storage schema. The
`issuer` field is part of the signed payload precisely so credentials from
different authorities stay distinguishable and a credential cannot be
relabelled as coming from a more trusted issuer than the principal used.

## Deliberately not done

- **No HCP client.** The name currently covers two overlapping papers, a
  research demo from one of the authors ([tobinsouth/hcp-demo](https://github.com/tobinsouth/hcp-demo),
  tied to a NeurIPS submission), a [Loyal Agents GitHub org](https://github.com/loyalagents),
  and at least four sites claiming it — plus a separate near-identical effort
  in [context-schema](https://github.com/vidursharma202-del/context-schema).
  What is missing is a normative wire spec, which is the expected state for a
  protocol at this stage rather than an oversight. The `Verifier` interface is
  here; a concrete backend should wait for a stable target.
- **`interests` is still self-reported.** `Position.Interests` is spliced into
  the analysis prompt (`internal/analysis/text.go`), so an agent's unverified
  claim about its own objectives is weighted by crux detection. Sourcing it
  from a signed, revocable context lookup is the natural next step — and it is
  the step that runs straight into the append-only constraint above.
- **No preference-cooperative surface.** The clustering math exists; nothing
  yet exposes it as cooperative formation.
- **The MCP create surface has no in-process test.** `internal/mcp` exposes
  only `Run` (stdio) and `RunHTTP` (binds a port), so `principal_policy` and
  `signature_policy` are covered at the A2A surface and the service layer but
  verified on the MCP path by hand. That gap predates these policies — every
  MCP create param is in the same position.

## Where it does not fit

- **Population mismatch.** Most gemot participants are agents with no human
  principal at all — Diplomacy fleets, PR reviewers, T3C synthetic personas.
  Delegation only helps the subset of deliberations where positions represent
  people.
- **`internal/sanitize` runs the other way.** It strips PII; HCP imports
  personal context deliberately. Any integration needs an explicit boundary
  between the two.
- **Data minimization degrades crux detection.** HCP returns a "relevant
  subset" by design; crux detection wants full context. A real tradeoff with no
  free lunch.

## References

- [Robust AI Personalization Controls: The Human Context Protocol (SSRN)](https://papers.ssrn.com/sol3/papers.cfm?abstract_id=5403981)
- [Human Context Protocol — Stanford Digital Economy Lab](https://digitaleconomy.stanford.edu/project/loyal-agents/hcp-human-context-protocol/)
- [The Future of Personal AI: Portable and Persistent Personal Memory through a Unified Human Context Protocol](https://digitaleconomy.stanford.edu/publication/the-future-of-personal-ai-portable-and-persistent-personal-memory-through-a-unified-human-context-protocol/)
- [Loyal Agents — Stanford Digital Economy Lab](https://digitaleconomy.stanford.edu/project/loyal-agents/)
- [Model Context Protocol specification](https://modelcontextprotocol.io/specification/2026-07-28)
