# Gemot Protocol TLA+ Specifications

TLA+ specifications of the Gemot protocol stack, covering both the
deliberation state machine (round progression, position submission,
qualified voting, commitment lifecycle, termination under Byzantine
presence) and the Byzantine-tolerant sequence-agreement protocol that
orders those operations across replicas.

This work is a cross-cutting deliverable of DARPA-PS-26-09 Track 1
("Formal specification and auditability," abstract §3). Its purpose is
to give the protocol a precise operational meaning that can be
mechanically checked and, later, refined against the implementation.

## Files

- `Deliberation.tla` / `Deliberation.cfg` — the deliberation state
  machine module and its TLC configuration.
- `HotStuff.tla` — the chained-HotStuff sequence-agreement module.
  Session 1 of the BFT implementation; see `hotstuff-design.md` for
  the protocol design.
- `HotStuff.cfg` / `HotStuff_stress.cfg` — tight and stress TLC
  configurations for the HotStuff spec. The tight cfg runs in <1s
  and is the PR-check target; the stress cfg covers bounds high
  enough to exercise the locked-QC vote constraint under
  adversarial scenarios.
- `hotstuff-design.md` — design document for the `internal/bft/`
  HotStuff implementation: adversary model, message types, state
  machine, commit rule, storage design, deferred items.

## Running the model checker

The toolchain is [TLA+ Tools](https://github.com/tlaplus/tlaplus)
(`tla2tools.jar`). It runs on any JVM — we use OpenJDK; no Oracle JDK is
required.

```bash
# one-time setup
mkdir -p ~/.local/share/tla
curl -fsSL -o ~/.local/share/tla/tla2tools.jar \
  https://github.com/tlaplus/tlaplus/releases/latest/download/tla2tools.jar

# run from the specs/ directory
cd specs
java -XX:+UseParallelGC -Xmx2g -cp ~/.local/share/tla/tla2tools.jar \
  tlc2.TLC -deadlock -workers auto Deliberation
```

TLC writes a full report to stdout and the `states/` directory.

## What the spec covers

The spec treats the LLM analysis step as an external oracle: its only
observable effects on protocol state are (a) advance the round, (b)
close the deliberation, or (c) fail and leave the state for retry. This
matches the abstract's §3 design decision and keeps the spec agnostic
to the particular analysis pipeline that runs inside it.

The state machine has three phases — `Open`, `Analyzing`, `Closed` —
plus the following data:

- **positions**: append-only set of `(author, round, pid)` tuples.
  Honest agents submit at most one position per round; Byzantine
  agents may equivocate up to two.
- **votes**: append-only set of `(voter, pid, value)` tuples; each
  `(voter, pid)` pair appears at most once.
- **commitments**: a function `cid → state` where `state ∈
  {absent, pending, fulfilled, broken}`. Lifecycle transitions are
  forward-only; `fulfilled` and `broken` are absorbing.

### Properties checked

Safety invariants:

- `TypeOK` — all variables keep their declared types.
- `NoDoubleVote` — no duplicate `(voter, pid)` entries in `votes`.
- `HonestNoEquivocation` — honest agents submit at most one position
  per round.
- `ByzantineBound` — the `f < n/3` assumption is preserved (trivially,
  since it's on constants; checked for sanity).

Temporal safety (stuttering-insensitive `[][...]_vars` properties):

- `RoundMonotonic` — the round counter never decreases.
- `ClosedAbsorbing` — once `status = Closed`, it stays `Closed`.
- `PositionsMonotonic` / `VotesMonotonic` — positions and votes are
  append-only.
- `CommitmentFulfilledStable` / `CommitmentBrokenStable` — terminal
  commitment states are absorbing.
- `CommitmentOwnerStable` — a commitment's owner never changes once
  assigned.

Liveness (under fairness):

- `Termination` — the deliberation eventually reaches `Closed`, given
  `WF_vars(StartAnalysis)` + `SF_vars(AnalysisClose)`. Strong fairness
  on `AnalysisClose` is required because the `AnalysisFail` retry
  action can disable it arbitrarily often; weak fairness alone admits
  a lasso (demonstrated by TLC on an earlier revision).

### What the spec does *not* cover

- **The analysis pipeline.** Claim extraction, crux detection,
  clustering, synthesizer output — all collapsed into the oracle.
- **Network and persistence layers.** The envelope-signing, nonce
  cache, and Postgres persistence are verified separately
  (`internal/auth`, `internal/store`) and are orthogonal to the
  protocol-level properties here.
- **Sybil resistance and reputation.** These are defined on a
  cross-deliberation graph and are out of scope for a single-session
  spec. Track 1's EigenTrust work will produce its own specification.
- **The full Byzantine-agreement sub-protocol.** This spec captures
  the *deliberation* state machine; the HotStuff-lineage sequence
  agreement that orders position submissions and votes is in the
  companion `HotStuff.tla` module.

## The HotStuff sequence-agreement spec

`HotStuff.tla` models the chained-HotStuff core as implemented in
`internal/bft/`: block proposal, replica voting, quorum-certificate
(QC) formation at the 2f+1 threshold, and the two-chain commit rule.
Session 1 scope is the happy-path state machine; view change and
real threshold signatures are tracked for session 2. See
`hotstuff-design.md` for the design-level discussion.

### Properties checked

Safety invariants:

- `TypeInvariant` — state variables keep declared types.
- `NoDoubleVoteInView` — no replica (honest or Byzantine) casts two
  different votes in the same view.
- `AgreementAtHeight` — **the core HotStuff safety property**: no
  two honest replicas commit different blocks at the same height.
  Yin et al. PODC 2019, Theorem 1.
- `CommittedChainConsistent` — every committed block (above genesis)
  has a committed ancestor at the genesis height; the committed
  subgraph is chain-connected.
- `QCRequiresQuorum` — every QC has 2f+1 distinct voters.
- `CommitRequiresQC` — honest replicas only commit blocks with a QC.
- `ByzantineBound` — the f < N/3 constant assumption holds.

Temporal safety (stuttering-insensitive `[][...]_vars`):

- `CommittedMonotonic` — per-replica committed set is append-only.
- `QCsMonotonic`, `VotesMonotonic`, `BlocksMonotonic` — the
  observable protocol histories are append-only.

### Byzantine model

The spec's Byzantine replicas may vote for any block in any view
they have not yet voted in. This is deliberately weaker than a full
adversarial model — intra-view equivocation (two votes in the same
view), signature forgery (guarded by the threshold-sig assumption),
and adversarial view-change manipulation are out of scope for
session 1. Session 2's view-change implementation expands the
adversarial model.

### Model bounds and generalization

Two model configurations are committed:

- `HotStuff.cfg` (PR check): `|Replicas|=4`, `|Byzantine|=1`,
  `MaxView=2`, `MaxHeight=2`, `MaxBlocks=3`. TLC explores 510
  distinct reachable states in well under a second and verifies
  all safety invariants. Exercises the minimum commit pipeline:
  height-1 block QC'd in view 1, height-2 block QC'd in view 2,
  two-chain rule commits the height-1 block.

- `HotStuff_stress.cfg` (nightly / pre-release): `MaxView=4`,
  `MaxHeight=2`, `MaxBlocks=5`. TLC explores 62,017 distinct
  reachable states in ~5 seconds. These bounds admit a potentially
  conflicting height-1 block at view 3 plus a height-2 child in
  view 4 whose QC would trigger commit on the conflicting branch.
  An earlier draft of the spec — with `LockOn` modeled as an
  optional action rather than coupled atomically into `FormQC` —
  failed this configuration: TLC found an execution where honest
  replicas committed two different blocks at height 1. That trace
  motivated tying the honest-replica lock update into `FormQC`
  itself, which is the current spec. The stress cfg is retained
  as a regression harness against any future spec regression that
  loosens the lock-update rule.

N=4 with f=1 is the minimum non-trivial HotStuff configuration
(f < N/3 ⇒ N ≥ 4).

**Generalization from the small model to arbitrary N.** The same
three-step argument as `Deliberation.tla` applies:

1. **Parametric statement of properties.** `AgreementAtHeight` and
   the other safety invariants are universally quantified over
   `Honest` and `committed[r]` with no fixed-arity references.
2. **Cutoff / projection.** The safety properties are Π₁ (for-all
   over finite-history tuples); projecting an N+1-replica execution
   down to N replicas preserves the invariants, and extending to
   N+1 by leaving one replica idle is still a valid behavior.
3. **Byzantine ratio is instance-independent.** `f < N/3` is a
   constant constraint; any valid instantiation at higher N with
   the same ratio preserves it.

Liveness is *not* checked in `HotStuff.cfg` — session 1 scopes to
safety. Liveness under partial synchrony is a session-2 deliverable
once the view-change protocol is implemented. The safety argument
above is independent of liveness.

### What the HotStuff spec does *not* cover

- **View change.** Session 1 is happy-path only; no NewView message
  or leader-timeout logic.
- **Real threshold signatures.** Modeled as an abstract QC set that
  forms at 2f+1 votes; the Go implementation uses placeholder sigs
  in session 1 and wires real BLS in session 2.
- **Network model.** Messages are modeled as a set of
  state-machine transitions; no explicit network layer, no dropped
  or reordered messages. Partial-synchrony assumptions live in the
  design doc, not the spec.
- **Service-layer integration.** The spec models the protocol in
  isolation; the binding to gemot's `submit_position` / `vote` /
  `analyze` operations is a session-3 deliverable.

## Model bounds and the n-agent generalization argument

The committed cfg checks the smallest model that exercises every
non-trivial action in the spec: `|Agents| = 4`, `|Byzantine| = 1`,
`MaxRounds = 1`, `MaxPID = 1`, `MaxCID = 1`. TLC explores 12,675
distinct reachable states in ~3 seconds and verifies all invariants and
temporal properties, including liveness under strong fairness on
`AnalysisClose`.

**Known coverage limitations of the minimal model.** Two properties
are verified vacuously at these bounds and deserve honest disclosure:

- At `MaxRounds = 1`, the guard `round < MaxRounds` on
  `AnalysisAdvance` is permanently false, so no round transition is
  ever observed. `RoundMonotonic` holds by construction but carries no
  model-checking evidence; meaningful coverage needs `MaxRounds ≥ 2`.
- At `MaxPID = 1`, the global counter allows only one total position,
  so the Byzantine-equivocation branch (cap of 2 positions per round)
  is unreachable. Spec-level correctness is unchanged, but the
  equivocation path is untested at this bound.

Larger bounds (`MaxPID = 2` or `MaxCID = 2`) push the reachable-state
count above what an 8-worker, 2 GB-heap TLC run completes in a
workable window. The `Deliberation.tla` module defines a partition-
preserving `Symmetry` operator — `{p ∈ Permutations(Agents) : p(Honest)
⊆ Honest ∧ p(Byzantine) ⊆ Byzantine}` — which is sound for safety
checks and could be used with a safety-only cfg at higher `MaxPID`.
The committed cfg omits symmetry because TLC warns it can mask
liveness violations, and we want safety + liveness in one run.

**Generalization from the small model to arbitrary n.** The argument
for lifting the checked properties to any `n ≥ 4` with
`3·|Byzantine| < n` runs in three steps:

1. **Parametric statement of properties.** Every checked property is
   quantified over variables whose ranges are functions of the
   constants (`Agents`, `CIDs`, `PIDs`), with no fixed-arity
   references. E.g. `HonestNoEquivocation` is `∀a ∈ Honest, ∀r ∈
   1..MaxRounds: …`. Such properties have the same shape under any
   instantiation of `Agents`.

2. **Cutoff argument for protocol properties.** The safety properties
   checked here (no-double-vote, append-only history, commitment
   monotonicity) are local per-tuple or per-cid statements. They do
   not count agents. Any `n+1`-agent execution projects onto an
   `n`-agent sub-execution by ignoring one agent's events, and the
   properties are preserved under projection. Conversely, any
   `n`-agent execution extends to `n+1` by leaving the extra agent
   idle (stuttering) — still a valid behavior. The projection
   argument is standard for protocols whose safety properties are
   Π₁ (universally quantified over finite histories), which ours are.

3. **Byzantine ratio is instance-independent.** The `f < n/3` check
   is on `|Byzantine|` and `|Agents|` directly; TLC's symmetry
   reduction does not alter it. Larger `n` with the same ratio (or
   smaller) preserves the bound by construction.

Liveness (`Termination`) does depend on the oracle's strong-fairness
commitment, which is stated over the predicate `AnalysisClose`, not
over specific agents. The predicate is invariant under permutation.

The argument as stated covers the protocol-level properties in this
spec. It does not extend to properties of the PBFT/HotStuff sequence
agreement or verifiable-tally schemes — those will need their own
specs and their own generalization arguments.

## Provenance

- TLA+ 2024 core language ([Lamport's summary](https://lamport.azurewebsites.net/tla/summary-standalone.pdf)).
- `FiniteSets`, `Integers`, `TLC` standard modules.
- Symmetric reduction following the TLC documentation.
- Weak/strong fairness notation from Lamport's *Specifying Systems*
  (Addison-Wesley 2002).
