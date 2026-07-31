# Human Context Protocol integration

Where gemot sits relative to the Human Context Protocol (HCP), what gemot has
built toward it, and what it deliberately has not.

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
principal signs → "agent A may speak for me, within scope S, until T"
```

Verification binds four things:

| Bound | Attack it stops |
|---|---|
| `agent` | A captured credential replayed by a different agent |
| `scope` | A credential minted for one deliberation presented in another |
| `expires_at` | A delegation outliving a revocation the verifier cannot observe |
| signature over all fields | Widening scope, extending expiry, or relabelling the issuer after minting |

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
  "principal_credential": {
    "principal": "human:alice",
    "agent": "alice-agent",
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
    Scope:     principal.ScopeDeliberationPrefix + delibID, // or "" for all
    Issuer:    principal.IssuerLocal,
    ExpiresAt: time.Now().Add(24 * time.Hour),
}
cred.Signature = ed25519.Sign(principalPrivKey, cred.SigningPayload())
```

The principal's public key is registered under its identity via
`RegisterAgentKey`. Signing covers a fixed-shape length-prefixed record, never
the JSON, so field order and whitespace in transit cannot change what verifies.

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

- **No HCP client.** HCP has two overlapping papers, a reference
  implementation, and no adopted spec. The interface is here; a concrete
  backend should wait for a stable target.
- **`interests` is still self-reported.** `Position.Interests` is spliced into
  the analysis prompt (`internal/analysis/text.go`), so an agent's unverified
  claim about its own objectives is weighted by crux detection. Sourcing it
  from a signed, revocable context lookup is the natural next step — and it is
  the step that runs straight into the append-only constraint above.
- **No preference-cooperative surface.** The clustering math exists; nothing
  yet exposes it as cooperative formation.
- **`signature_policy` still has no MCP surface**, so the two policies are
  inconsistent in reachability: `principal_policy` is settable from an MCP
  client, `signature_policy` only from Go.

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
