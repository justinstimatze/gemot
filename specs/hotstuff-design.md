# HotStuff SMR for Gemot: Design (Sessions 1–2)

## Status

**Session 2 of N.** Session 1 shipped the happy-path state machine,
the TLA+ spec, and the protocol-core Go skeleton. Session 2 adds view
change — `Timeout()` and `HandleNewView()` methods, pending-NewView
tallies, domain-separated NewView signatures — and extends the TLA+
spec with `TriggerTimeout`/`ViewChange` actions, the liveness branch
of the honest vote rule, and a `ViewMonotonic` property. A dedicated
adversarial test suite (`internal/bft/adversarial_test.go`) covers
Byzantine leader stall, cross-view equivocation, partitioned-minority
liveness, and stale-NewView selection. Real threshold signatures,
service-layer integration, durable log, multi-node deploy, and
client-side proof verification remain deferred — see "Not in Session 2"
below.

Deliverable of DARPA-PS-26-09 Track 1 M8 ("Byzantine-tolerant sequence
agreement," abstract §3).

## Problem

Gemot today runs as a single Fly machine with a warm standby. Agents
sign their positions and votes end-to-end (M8 envelope protocol), but
the **server** is authoritative for ordering. "Position X was
submitted before position Y" and "crux C was computed from this
specific input set" are server-asserted facts. A compromised server
— or a server operator with a conflict of interest — can:

- **Equivocate.** Show agent A a different commit ordering than agent
  B. Each agent independently verifies individual message signatures
  and is satisfied, but the two view histories are inconsistent.
- **Censor.** Silently drop a position, claiming it never arrived.
- **Rewrite history.** Replay analysis over a fabricated input set,
  then discard the evidence of the original inputs.

Signatures on individual messages do not close this gap: they prove
an agent authored a message, not that the server honestly included
it in the canonical sequence.

The fix is replicated Byzantine-tolerant sequence agreement. Gemot
runs as N replicas; any subset of f < N/3 may be Byzantine. Honest
clients receive threshold-signed certificates proving their writes
committed into a canonical log. No single compromised replica can
rewrite, censor, or equivocate.

## Protocol Choice: HotStuff over PBFT

Two mature protocol families could fit:

| Property                        | PBFT (Castro-Liskov 1999) | HotStuff (Yin et al. 2019) |
|---------------------------------|---------------------------|----------------------------|
| Message complexity per decision | O(n²) all-to-all          | O(n) via threshold sigs    |
| Pipelining                      | No (three sequential phases) | Yes (chained variant)  |
| View change cost                | O(n²) messages            | O(n) via view synchronizer |
| Threshold sig infra required    | No                        | Yes (BLS or similar)       |
| Reference implementations       | Many academic             | LibraBFT, Aptos, HotStuff-rs |

**We choose HotStuff (chained variant).** Rationale:

1. **Linear message complexity.** The right asymptotic posture for a
   system that may eventually run at tens of replicas.
2. **Forward-compatible with verifiable tally.** The companion Track 1
   priority (verifiable tally, abstract §3) uses threshold signatures
   too (Helios/ElectionGuard-lineage schemes rely on threshold
   decryption). Shared infrastructure reduces total complexity.
3. **Active reference implementations.** LibraBFT (now DiemBFT / Aptos)
   and HotStuff-rs give us two independent implementations to
   cross-check against, which PBFT does not at comparable recency.
4. **Cleaner view change.** PBFT view change is the historically
   hardest part of a PBFT implementation to get right; HotStuff's
   two-chain rule makes view change an ordinary case of the same
   voting protocol.

PBFT remains the academic comparison baseline in this document and
in future publication, but not a Go implementation target.

**Chained vs. basic vs. event-driven HotStuff.** The 2019 paper
defines three variants. The *chained* variant is the pipelined
three-phase version used by LibraBFT, Aptos, and HotStuff-rs, and is
what we implement. "Basic" HotStuff (no pipelining) is simpler to
reason about but burns three rounds per decision; "event-driven" is
an optimization that triggers new view entry on QC receipt rather
than on a timer. Chained is the right balance of operational
maturity and throughput.

## Adversary Model

- **N replicas.** At most f < N/3 may be Byzantine (arbitrary
  behavior: equivocation, stalling, collusion, sending malformed
  messages, colluding with the network). Tolerance is tight — N=4,
  f=1 is the minimum non-trivial configuration.
- **Clients are not replicas.** Agents submit writes to any replica
  and receive a threshold-signed QC proving commitment. A malicious
  client can submit nonsense inputs, but cannot rewrite the log; the
  worst an f-bounded Byzantine replica coalition can do is stall
  progress during view change, not corrupt committed state.
- **Partially synchronous network.** There is a Global Stabilization
  Time (GST) after which message delays are bounded by some Δ. Before
  GST, the network may arbitrarily delay, drop, or reorder. Safety
  holds under any network; liveness is conditional on eventual
  synchrony. This is the standard partial-synchrony model (Dwork,
  Lynch, Stockmeyer 1988).
- **Replicas have local storage** (Postgres per node, or shared
  Postgres with per-replica append-only log — see "Storage Design"
  below). Storage is assumed durable; a crashed honest replica that
  recovers from its log still counts as honest.

Not in scope: active network adversaries who can forge message
signatures (handled by envelope crypto), denial-of-service against
the network layer (handled by rate limiting), or cryptographic
breaks of BLS (out of scope for any practical BFT protocol).

## Message Types and State Machine

### Blocks

A **block** is the unit of sequence. A block contains:

- `height: uint64` — monotonic sequence number.
- `parent: Hash` — hash of the previous block (chain pointer).
- `view: uint64` — view (round) in which the leader proposed it.
- `payload: []Operation` — gemot operations to apply (see below).
- `qc: QC` — quorum certificate for the parent block.
- `hash: Hash` — SHA-256 over the preceding fields.

### Messages

- **Proposal(view, block, justify)**: leader broadcasts a new block.
  `justify` is the QC of the high QC the leader extends.
- **Vote(view, block_hash, sig)**: replica's signed vote on a
  proposal. Aggregated by the leader into a QC.
- **QC(view, block_hash, agg_sig)**: quorum certificate — 2f+1
  threshold-aggregated signatures proving a block was voted on.
- **NewView(view, high_qc)**: sent by a replica that times out,
  carrying its highest-known QC so the next leader can extend it.

### Per-replica state

```
view: uint64          — current view number
locked_qc: QC         — highest QC seen so far that could commit
prepared_qc: QC       — QC justifying the block we most recently voted for
high_qc: QC           — highest-view QC observed (not necessarily committed)
last_voted_view: uint64 — anti-equivocation guard
log: []Block          — committed blocks (projection of decided chain)
```

`locked_qc` is the HotStuff safety invariant: a replica only votes
for a block that either extends `locked_qc`'s block or has a QC in a
strictly higher view than `locked_qc.view`. This is the
"locked-liveness" rule from Yin et al. §4.

### Happy path (one decision)

1. **Leader proposes.** Leader for view v broadcasts `Proposal(v,
   block_v, justify=high_qc)` where `block_v` extends
   `high_qc.block`.
2. **Replicas vote.** On valid proposal, each honest replica updates
   `last_voted_view = v`, updates `prepared_qc`, and sends
   `Vote(v, block_v.hash, sig)` to the leader.
3. **Leader forms QC.** On receipt of 2f+1 votes, leader aggregates
   into `QC(v, block_v.hash, agg_sig)` and broadcasts the next
   proposal `Proposal(v+1, block_{v+1}, justify=QC_v)`.
4. **Commit rule (two-chain).** Replica commits `block_k` when it
   sees two consecutive QCs `QC_k, QC_{k+1}` forming a direct parent
   chain (i.e., `QC_{k+1}` justifies a block whose parent is
   `block_k`). In chained HotStuff this is called the "two-chain
   commit rule."

Because each proposal carries the QC for its parent, the three
HotStuff phases (prepare, pre-commit, commit) are pipelined: a single
proposal round simultaneously prepares block_v and pre-commits
block_{v-1} and commits block_{v-2}. Over 3 consecutive honest
rounds, all three blocks commit; over T rounds, T-2 blocks commit.

### Safety invariant

No two honest replicas commit different blocks at the same height.
Formal statement and TLC proof: `HotStuff.tla` §Safety.

### Liveness

Under partial synchrony, after GST, an honest leader will eventually
drive a decision. Before GST (or under a Byzantine leader that
stalls), the view synchronizer times out and triggers NewView, which
drives view change.

### View Change (Session 2)

A replica that observes no QC in its current view calls `Timeout()`,
which broadcasts `NewView(view+1, highQC)` signed with domain byte
`0x03` and advances the replica's local view. Any replica that
receives 2f+1 NewView messages for a target view `v'` records the
**highest-view QC among the collected set** as the authoritative
"view-change justify" for `v'`. When the leader of `v'` subsequently
calls `Propose`, the protocol transparently substitutes the
caller-supplied `(parent, justify)` with the collected
highest-view QC.

This selection rule is the core safety argument for view change (Yin
et al. §5): the 2f+1 NewView set intersects with any prior 2f+1 vote
quorum in at least f+1 replicas, so the selected highQC is at least
as recent as any QC an honest replica had locked. Extending it
preserves the locked-QC invariant. A Byzantine sender who contributes
a stale NewView cannot regress the selection — the highest-view QC
among 2f+1 wins, and the honest majority's NewViews dominate.

The TLA+ spec models this as `TriggerTimeout(r, claimedQC)` and
`ViewChange(v)`, plus the liveness branch of `HonestCanVote` (vote
for `b` if `b`'s parent was proposed in a view strictly newer than
the locked QC's view, even if `b` does not extend the locked block).
Without the liveness branch, a view change would leave honest
replicas locked on the pre-timeout branch forever.

## Threshold Signatures

### Placeholder in Session 1

Session 1 uses a `PlaceholderSigner` that encodes the replica ID in
the first four bytes of the signature followed by `sha256(msg)`.
`Verify` validates the claimed replica ID and the digest match;
`Aggregate` concatenates (sorted by replica ID) so `VerifyAggregate`
can split back into per-replica chunks. All signatures are forgeable
by anyone who knows the replica ID — the placeholder exists only so
protocol tests can distinguish votes by source and validate the
proposal/vote signature paths end-to-end.

Message-layer domain separation is enforced independently of the
signer: vote digests are prefixed with `0x01`, proposal digests with
`0x02`, NewView digests with `0x03` (session-2 addition). Under a
real threshold-sig scheme these prefixes prevent any one domain's
signature over `(view, blockhash)` from being replayed as another —
a vote cannot become a proposal, a NewView cannot impersonate a
vote. The placeholder follows the same domain boundaries so session
3's swap to real BLS is a pure signer-implementation replacement,
not a protocol change.

This is deliberate: we want session-1 protocol tests to fail loudly
when protocol logic is wrong, not when a crypto library is missing.
The `PlaceholderSigner` constructor is loudly labeled
"DO NOT SHIP THIS TO PRODUCTION" and a CI grep for the type name in
non-test builds catches accidental reuse.

### Real scheme (Session 3+)

**Constraint: no cgo.** The gemot Dockerfile builds with
`CGO_ENABLED=0`. Adding cgo would break the Fly container build.
This eliminates the two most popular Go BLS libraries:

- ~~`github.com/herumi/bls-eth-go-binary`~~ — wraps the herumi C
  library via cgo. Excluded.
- ~~`github.com/supranational/blst`~~ — C library with Go bindings
  via cgo. Excluded.

**Pure-Go candidates:**

- `github.com/consensys/gnark-crypto` — ConsenSys. Well-maintained
  (used by gnark zk library, Geth fallback). BLS12-381 + BN254.
- `github.com/cloudflare/circl` — CloudFlare's research crypto
  library. Pure Go, reviewed. BLS12-381.
- `github.com/drand/kyber-bls12381` — drand randomness beacon.
  Pure Go wrapper over go.dedis.ch/kyber/v3.

**Decision (preliminary):** `gnark-crypto`. ConsenSys maintenance,
broad adoption in Ethereum ecosystem, minimal additional dependency
graph beyond what we already pull. Finalize in session 3.

### Scheme

2f+1-of-N BLS threshold signatures. Each replica holds a secret key
share; aggregated signatures verify against a single group public
key. Key distribution is out-of-band at protocol bootstrap (not
dynamic); replica roster changes require a separate reconfiguration
protocol (out of scope for this work).

## Storage Design

**Local append-only log per replica.** Each replica maintains a
Postgres table `bft_log(height, block_hash, block_bytes, qc_bytes,
committed_at)` indexed by `height`. Blocks append on commit;
`committed_at` is wall-clock for operational telemetry only — the QC
is the cryptographic proof.

Gemot's existing Postgres tables (`deliberations`, `positions`,
`votes`, `cruxes`, etc.) become a **projection** of the log, not the
source of truth. On replica crash + recovery, replay the log from
the last snapshot; on fresh replica join, catch up via log sync
(implementation deferred to session 2).

This inversion is the key architectural shift. Today, Postgres is
the source of truth and the application state. After HotStuff, the
log is the source of truth and Postgres is a cache.

**Shared Postgres vs per-replica Postgres** is a session-2+
deployment question. The protocol layer doesn't care: each replica's
`bft_log` writes are independent.

## Which Gemot Operations Need Ordering

The protocol orders **writes** to gemot state. Reads are served from
any replica's local projection and do not go through consensus. The
operations that go through BFT:

- `submit_position` — write to `positions`, affects cohort.
- `submit_vote` / `submit_qualified_vote` — write to `votes`,
  affects aggregation.
- `file_dispute` — write to `disputes`, affects reputation.
- `register_key`, `revoke_key` — write to `agent_keys`, affects auth.
- `analyze` round close — writes `analysis_results`, `cruxes`, and
  triggers reputation `UpdateFromRound`. Must be ordered so replicas
  agree on the input set.

Operations that do **not** go through BFT:

- `list_deliberations`, `get_positions`, `get_analysis`, etc. — all
  reads, served from local projection.
- Internal timer-driven analysis triggers — each replica runs its
  own timer but only the designated leader's trigger wins consensus.
- Envelope nonce cache writes — orthogonal; handled by the existing
  `envelope_nonces` shared-Postgres path.

## What's Not in Session 2

Explicitly deferred. Each item is tracked for session 3+ with
acceptance criteria written down here so scope creep is visible.

1. ~~**View change.**~~ **Delivered in session 2.** `Timeout()` +
   `HandleNewView()` drive view change under Byzantine leader failure;
   `TestLeaderStall` exercises the canonical case (replica 0 silent,
   replica 1 becomes leader of view 2, protocol resumes).
2. **Real threshold signatures.** Placeholder in sessions 1–2. Session
   3 wires `gnark-crypto` (or the finalized scheme), implements
   distributed key generation or out-of-band key distribution, and
   re-runs protocol tests under real verification.
3. **Service-layer integration.** Sessions 1–2 ship `internal/bft/`
   as an independent package. Session 4 routes
   `internal/deliberation/service.go` writes through the BFT state
   machine. Acceptance: `SubmitPosition` returns a QC proof to the
   client; `Analyze` round close is driven by committed log order,
   not wall-clock.
4. **Log replay from Postgres.** Sessions 1–2 tests use in-memory
   replicas. Session 4 implements `bft_log` table + replay on
   replica restart. Acceptance: crash replica 0 mid-view, restart,
   verify it catches up to log head and participates correctly.
5. **Multi-node deployment.** Sessions 1–2 use `InMemoryTransport`
   in tests only. Session 5 implements `HTTPTransport`, configures
   Fly to run N machines as BFT replicas (or uses a separate replica
   fleet), adds health checks and replica-roster config, and wires a
   real wall-clock `time.Timer` for view-change timeout (session 2
   tests drive `Timeout()` manually via direct call).
6. **Client-side proof verification.** Sessions 1–2: replicas form
   QCs but don't expose them to clients. Session 5: `SubmitPosition`
   response includes a threshold-signed QC; clients verify
   independently using the replica group's public key. Acceptance:
   unit test where a client with the group public key and a QC
   accepts a committed submission and rejects a forged one.
7. **Temporal liveness property in TLA+.** Session 2's spec adds
   `ViewMonotonic` (a safety-like temporal property stating the view
   counter never regresses) and models timeout / view-change actions,
   but does not encode an explicit `<>[]progress` liveness formula.
   Under fairness assumptions this would express "eventually some
   honest replica commits," but TLC liveness checking at the current
   bounds expands state space beyond the 10-second PR-check budget.
   The session-2 test suite covers liveness empirically; a formal
   liveness check lands with session 3 or later, guarded by a
   separate cfg.

## Session 2 Acceptance

Session 2 ships iff:

1. `specs/HotStuff.tla` extends session 1 with `newViews`/`timedOut`
   state, `TriggerTimeout`/`ViewChange` actions, the liveness branch
   of `HonestCanVote`, a `ViewMonotonic` temporal property, and
   `NewViewQCIsReal` invariant. TLC passes both tight
   (`HotStuff.cfg`: `MaxView=2, MaxHeight=2, MaxBlocks=3` —
   ~14k states, <5s) and stress
   (`HotStuff_stress.cfg`: `MaxView=3, MaxHeight=2, MaxBlocks=3` —
   ~540k states, ~10s) with no safety violations.
2. `internal/bft/` adds `Replica.Timeout()` and
   `Replica.HandleNewView()`; `pendingNewViews` and
   `viewChangeHighQC` state; `domainNewView = 0x03` domain byte;
   `Propose` transparently extends the view-change-selected highQC
   when acting as post-timeout leader; `HandleVote` integrates the
   formed QC into the leader's own state so a post-vote timeout
   carries the correct highQC.
3. `internal/bft/adversarial_test.go` ships four tests:
   `TestLeaderStall` (view-change canonical case),
   `TestLeaderEquivocatesAcrossViews` (safety under Byzantine
   leader), `TestPartitionedMinority` (no progress without quorum,
   progress on heal), `TestStaleNewView` (Byzantine stale-highQC
   loses selection to honest real-highQC). Existing 9 session-1
   tests continue to pass.
4. `specs/hotstuff-design.md` (this doc) promotes view change from
   deferred to delivered and updates the deferred list to
   sessions 3+.
5. `specs/README.md` documents the session-2 spec additions.
6. `THREAT_MODEL.md` row "Byzantine-tolerant sequence agreement"
   remains Partially Implemented, with the sub-caveat updated to
   reflect view-change + adversarial tests shipping and the
   remaining deferrals (real sigs, service integration, multi-node
   deploy, client proof).
7. `CHANGELOG.md` Unreleased entry documents the session-2 additions.
8. Commit + push to main. **No production deploy** — `internal/bft/`
   is still not wired into the service layer, so nothing changes
   for prod.

## Session 1 Acceptance (historical)

Session 1 shipped at commit `e2a557a` + review fixes at `99b8021`
with: design doc, TLA+ spec at minimal bounds, Go protocol-core
happy-path, 9 unit tests, placeholder signer, in-memory transport,
THREAT\_MODEL and CHANGELOG updates. See the git log for the detailed
review-fix record (critical: TLA+ spec under-constrained re.
locked-advance; authentication gaps on proposals; digest domain
separation).

## References

- Castro, Liskov. *Practical Byzantine Fault Tolerance.* OSDI 1999.
- Yin, Malkhi, Reiter, Gueta, Abraham. *HotStuff: BFT Consensus with
  Linearity and Responsiveness.* PODC 2019.
- Dwork, Lynch, Stockmeyer. *Consensus in the Presence of Partial
  Synchrony.* JACM 1988.
- Lamport. *Specifying Systems.* Addison-Wesley 2002.
- LibraBFT / DiemBFT / Aptos reference implementation.
- HotStuff-rs reference implementation.
