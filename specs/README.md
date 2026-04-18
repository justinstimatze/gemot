# Gemot Protocol TLA+ Specification

TLA+ specification of the Gemot deliberation protocol state machine,
covering round progression, position submission, qualified voting,
commitment lifecycle, and termination under Byzantine presence.

This work is a cross-cutting deliverable of DARPA-PS-26-09 Track 1
("Formal specification and auditability," abstract §3). Its purpose is
to give the protocol a precise operational meaning that can be
mechanically checked and, later, refined against the implementation.

## Files

- `Deliberation.tla` — the specification module.
- `Deliberation.cfg` — TLC model-checker configuration with
  representative constants.

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
  the *deliberation* state machine; the PBFT/HotStuff-lineage sequence
  agreement that will eventually order position submissions and votes
  is a separate module (future work — abstract §5 M16 item).

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
