# HotStuff SMR for Gemot: Design (Sessions 1–4)

## Status

**Session 4 of N.** Session 1 shipped the happy-path state machine,
the TLA+ spec, and the protocol-core Go skeleton. Session 2 added view
change and a Byzantine adversarial test suite. Session 3 replaced
the placeholder signer with real BLS12-381 multi-signatures via
`gnark-crypto`. **Session 4 adds the durable commit log**: a
`LogStore` interface with in-memory + Postgres implementations, a
`bft_log` schema-v6 table, a `Replay` helper that reconstructs
committed state on replica restart, and commit-path persistence
(`commitBlock` writes log-first-then-memory so the in-memory state
never advances past the persisted tail). Service-layer integration
(routing `submit_position`/`vote`/`analyze` through the BFT state
machine), multi-node deploy, and client-side proof verification
remain deferred — see "Not in Session 4" below.

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

## Signatures

### BLS12-381 multi-signature (Session 3, current)

Session 3 replaces the sessions-1/2 placeholder with real BLS12-381
multi-signatures via `gnark-crypto/ecc/bls12-381`. Each replica owns
a keypair; the public-key roster is distributed to every replica at
startup; aggregation sums per-replica G1 signature points.

Scheme (min-sig variant, RFC 9380 hash-to-curve):

```
Private key:  scalar s ∈ Fr (the scalar field of BLS12-381)
Public key:   s · g₂  ∈ G2  (96 bytes compressed)
Sign(msg):    s · H(msg)  ∈ G1  (48 bytes compressed)
Verify:       e(sig, g₂) == e(H(msg), pk)
              (implemented via PairingCheck([-H(msg), sig], [pk, g₂]))
Aggregate:    Σᵢ sigᵢ = Σᵢ (sᵢ · H(msg)) = (Σᵢ sᵢ) · H(msg)
VerifyAgg:    e(agg, g₂) == e(H(msg), Σᵢ pkᵢ)
```

`H(msg)` is hash-to-G1 with DST
`BLS_SIG_BLS12381G1_XMD:SHA-256_SSWU_RO_NUL_` per the RFC 9380
BLS signature profile. Changing this DST invalidates every signature
ever produced — it is schema-breaking.

### Multi-signature vs. threshold BLS

Session 3 ships multi-signature, not threshold BLS. The distinction:

- **Multi-sig**: each replica owns an independent keypair; aggregation
  is the sum of per-replica signatures; the verifier knows which
  replicas contributed (the QC's `Signers` list).
- **Threshold BLS**: a master secret is Shamir-split across replicas;
  any 2f+1 partial signatures can Lagrange-interpolate to the master
  signature; the verifier uses a single master public key and does
  not need to know which replicas contributed.

For HotStuff, either works. Multi-sig matches the QC data model
exactly — the QC carries `Signers []ReplicaID` and `AggSig` already,
so the verifier deriving the aggregate public key as `Σᵢ pk_{Signers[i]}`
is a natural read. Threshold BLS requires a distributed key
generation (DKG) ceremony at protocol bootstrap, which is session-5
work (multi-node deploy needs key distribution anyway). Shipping
multi-sig in session 3 gets real crypto into the protocol
immediately without the DKG dependency.

The design doc reserves the word "threshold" for the HotStuff
protocol-layer threshold (2f+1 quorum). The cryptographic scheme
underneath that threshold is multi-signature.

### Message-layer domain separation

Domain separation is enforced independently of the signer. Vote
digests are prefixed with `0x01`, proposal digests with `0x02`,
NewView digests with `0x03`. Under BLS a shared digest bit-pattern
across domains would let one domain's signature be replayed as
another — a vote signature over `(view, blockhash)` could become a
NewView signature over `(view, 0, zerohash)` if the byte strings
matched. Domain bytes prevent this at the message layer before the
signer ever sees the bytes.

### Key distribution (Session 3: test-only)

`GenerateBLSKeyset(n int)` produces n independent keypairs via
`crypto/rand` and returns the shared roster. This is test-only —
all keys exist in a single process. Session 5 replaces this with
either:

- **Trusted setup**: one-time offline ceremony that emits per-replica
  keypairs over a secure channel. Simple, suitable for closed-
  federation deployments.
- **DKG**: Pedersen- or GG20-style distributed key generation
  ceremony at cluster bootstrap. Harder but lets the roster evolve.

The choice deferred to session 5 when actual inter-replica key
distribution is needed.

### Library choice

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

**Decision (finalized in session 3):** `github.com/consensys/gnark-crypto`
@ v0.20.1. ConsenSys maintenance, broad Ethereum-ecosystem adoption,
pure-Go (verified `CGO_ENABLED=0 go build ./...` passes). gnark-crypto
provides the BLS12-381 curve primitives — G1/G2 arithmetic, pairing,
RFC 9380 hash-to-G1 — but not a pre-built BLS signature scheme, so
the sign/verify/aggregate/verify-aggregate layer is implemented
directly in `internal/bft/bls_signer.go` on top of the primitives.
Roughly 200 lines of Go.

Alternatives considered:

- `github.com/cloudflare/circl/sign/bls` provides a full BLS
  signature API (Sign/Verify/Aggregate/VerifyAggregate) with
  KeyGen from IKM, but its VerifyAggregate takes distinct-message
  pairs — suitable for BLS multi-sig across different messages but
  suboptimal for same-message aggregation (N pairings instead of 2).
  Writing our own sign/verify on top of gnark-crypto's primitives
  was cleaner than adding a second crypto dependency just for this
  path.
- `github.com/drand/kyber-bls12381` (drand randomness beacon) has
  threshold BLS primitives including Lagrange interpolation —
  suitable if we decide to move to threshold BLS in a later session,
  but a heavier dependency tree than gnark-crypto for today's
  multi-signature scope.

### Replica roster changes

Not in scope for any of sessions 1–5. The roster is fixed at cluster
bootstrap; adding or removing a replica requires a separate
reconfiguration protocol (cf. Raft's joint-consensus pattern adapted
for BFT, or BLS DKG resharing). Gemot's current operational model is
small-N closed federation where a ~monthly roster rotation via
full-cluster restart is acceptable.

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

## What's Not in Session 4

Explicitly deferred. Each item is tracked for session 5+ with
acceptance criteria written down here so scope creep is visible.

1. ~~**View change.**~~ **Delivered in session 2.** `Timeout()` +
   `HandleNewView()` drive view change under Byzantine leader failure;
   `TestLeaderStall` exercises the canonical case.
2. ~~**Real threshold signatures.**~~ **Delivered in session 3.**
   BLS12-381 multi-signatures via gnark-crypto. Key distribution is
   test-only (`GenerateBLSKeyset`); session 5 replaces with either
   trusted setup or DKG when multi-node deploy needs inter-replica
   key distribution.
3. ~~**Durable commit log.**~~ **Delivered in session 4 (persistence
   layer only).** `LogStore` interface + `InMemoryLogStore` +
   `PostgresLogStore` (schema v6 `bft_log` table). `Replica.SetLog`
   attaches a log; `commitBlock` persists (Block, QC) before
   advancing in-memory state. `Replay` reconstructs knownBlocks,
   committed, committedLog, view, highQC, lockedQC from the log.
   **Known gap**: `lastVotedView` and `proposedInView` are NOT
   persisted — a crashed-and-restarted replica could equivocate
   under a Byzantine peer racing the restart. Safe for single-
   honest-replica crash recovery (which is the current deployment
   posture); session 5 adds a durable vote-history side-table before
   the multi-node deploy.
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

## Session 4 Acceptance

Session 4 ships iff:

1. `internal/bft/log.go` defines the `LogStore` interface
   (Append/Load/HighestHeight) and `InMemoryLogStore` reference
   implementation with height-monotonic append, fork detection via
   `ErrLogForkDetected`, and idempotent exact-match re-append.
2. `internal/bft/replay.go` reconstructs a fresh `Replica`'s
   committed state from a `LogStore`. Restores knownBlocks,
   committed, committedLog, view (= last logged view + 1), highQC,
   and lockedQC.
3. `internal/bft/state.go` gains a `log LogStore` field and
   `SetLog(log LogStore)` setter. `internal/bft/protocol.go`
   `commitBlock` is refactored to take `(Block, QC)` and persist
   log-first-then-memory so the in-memory state never advances past
   the persisted tail. Error propagation threads through
   `processJustify` → `HandleProposal` / `HandleVote`.
4. `internal/store/bft_log.go` ships `PostgresLogStore` implementing
   the `LogStore` interface against the `bft_log` schema-v6 table.
   Fork detection uses `INSERT ... ON CONFLICT DO NOTHING` + a
   post-insert hash compare.
5. `internal/store/schema.sql` bumps to version 6 with the
   `bft_log` table definition (height PK, block_hash, block_bytes,
   qc_bytes, committed_at). Migration is additive — empty bft_log
   has no runtime cost on existing deployments.
6. `internal/bft/log_test.go` ships 5 unit tests (in-memory log
   append/load roundtrip, fork detection, gap rejection, log
   writes on protocol commit, Replay reconstructs committed state
   after a 4-round run).
7. `tests/bft_log_integration_test.go` ships 2 Postgres integration
   tests (append/load roundtrip with HighestHeight, fork detection).
8. `specs/hotstuff-design.md` (this doc) promotes durable log from
   deferred to delivered; `THREAT_MODEL.md` sub-caveat updated;
   `CHANGELOG.md` records session 4.
9. Commit + push. **No production deploy** — the BFT package is
   still not wired into `internal/deliberation/service.go`. Schema
   migration runs on boot; `bft_log` starts empty and stays empty
   in prod until session 5 wires the service layer.

## Session 3 Acceptance

Session 3 ships iff:

1. `internal/bft/bls_signer.go` implements the `Signer` interface
   using BLS12-381 multi-signatures via gnark-crypto primitives.
   Sign/Verify/Aggregate/VerifyAggregate all pass the RFC 9380 hash-
   to-G1 + pairing-check tests in `bls_signer_test.go`.
2. `PlaceholderSigner` removed from the codebase. `signature.go`
   retains only the `Signer` interface and the `newViewDigest`
   helper.
3. `newCluster` in tests generates a fresh BLS keyset per cluster and
   distributes (keypair, roster) pairs to each replica. All 13
   pre-existing bft tests pass under real BLS verification.
4. CGO_ENABLED=0 build passes (`CGO_ENABLED=0 go build ./...`) —
   gnark-crypto is pure Go; the Fly Dockerfile's no-cgo posture is
   preserved.
5. `specs/hotstuff-design.md` (this doc) promotes real-threshold-sigs
   from deferred to delivered, documents the library choice, and
   explains the multi-sig-vs-threshold-BLS distinction.
6. `THREAT_MODEL.md` "Byzantine-tolerant sequence agreement" sub-
   caveat updated to reflect real sigs shipping (remaining deferrals:
   service integration, multi-node deploy, client proof).
7. `CHANGELOG.md` Unreleased entry documents the session-3 additions.
8. Commit + push. **No production deploy** — the BFT package is
   still not wired into `internal/deliberation/service.go`.

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
