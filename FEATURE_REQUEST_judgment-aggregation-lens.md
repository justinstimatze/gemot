# Feature request: judgment-aggregation calibration (and direction-judgment benchmark)

Source: Anthropic, "When AI builds itself" (institute/recursive-self-improvement,
May 2026). Origin: brainstorm session 2026-06-04 reading that article against the
project portfolio.

Verbatim from the article (confirmed 2026-06-04, quoted word-for-word,
cross-verified against `../plancheck/FEATURE_REQUEST_calibration-rate.md`):

> "An area of human comparative advantage, for now, is research taste and
> judgment, including choosing which problems matter, which results to trust, and
> when an approach is a dead end." — § *What might the future of work at Anthropic
> look like?*

> "Large performance gaps persist when it comes to Claude exercising judgement in
> choosing goals in both engineering and research." — § *Evidence from within
> Anthropic*

> "Humans play a substantially diminished role in their development, likely moving
> most of our effort towards oversight, validation, and verification of an
> expanding 'virtual lab' run by AI systems." — § *Possible futures*

## Why

The article's central gap is **research judgment**: choosing which problems
matter, assessing result reliability, recognizing dead ends. Its flagship metric
is a calibration number — "model direction-choices matched humans 64% of the
time." If individual agents can't be trusted to pick the right goal, a protocol
that *aggregates judgment under disagreement and emits a reasoning-level audit
trail* is a direct mitigation: you don't need any one agent to have good taste
if the mechanism extracts collective judgment with provenance.

That is what gemot already is. The reframe worth carrying in the roadmap:

> **gemot = judgment-aggregation infrastructure for agent fleets that
> individually lack research taste.**

Anthropic publicly certified the premise (agents lack taste). gemot is a
standing mechanism that doesn't require the premise to be false.

## How gemot's surface maps to the bottleneck

Each mapping labeled **tight** (structural match), **moderate** (partial match,
scope work needed), or **loose** (useful intuition, imprecise) per the
INFLUENCES.md / plancheck-house discipline.

- **Choosing what problems matter** → compromise generation + bridging scores
  **(tight)**. `analyze action:propose_compromise` literally optimizes for
  cross-cluster endorsement of which proposal to pursue. Bridging scores surface
  positions with cross-group agreement — the mechanism for choosing *which* goal
  a divided fleet can rally around.
- **Assessing reliability** → integrity warnings, model-diversity flags, Sybil
  detection, EigenTrust cold-start cap **(tight)**. The mechanism systematically
  distrusts signals a single agent would trust: identical voting patterns flag
  Sybils, all-one-model-family triggers a diversity warning, newcomers are
  capped until they earn track record.
- **Recognizing dead ends** → crux detection + `dispute_crux` **(moderate)**.
  Crux detection surfaces the load-bearing disagreement that would let a fleet
  stop talking past itself, but doesn't directly classify approaches as
  "dead ends." Tightening this is in scope for Feature 2 below.
- **Provenance / oversight infrastructure** → tamper-evident action log +
  BLS-signed audit, replica pubkey for offline verification **(tight)**. The
  reasoning trail survives independent verification — directly answers the
  article's "oversight, validation, and verification" framing.

## Feature 1 (primary, small): judgment-aggregation calibration readout

Compute and surface a "fleet vs solo-agent" agreement rate for held-out judgment
tasks, directly comparable to the article's 64% number.

- Inputs largely exist: stored positions, votes, and accepted compromises keyed
  by deliberation_id. Ground-truth outcomes need a lightweight `record_outcome`
  hook on `decide action:commit` (mirror plancheck's). Verify current storage
  layout before wiring — the schema and `decide` surface may have moved since
  this brainstorm.
- Output: an agreement rate — "of N deliberations with known ground-truth
  outcomes, the mechanism's accepted compromise matched the retrospective right
  call M% of the time" — plus a solo-agent baseline measured on the same
  deliberations (run a single agent over the same question, take its answer,
  score vs ground truth).
- Surface as: a CLI subcommand (`gemot calibration`) and a field on
  `analyze action:get_result` (`judgment_agreement_rate` when available).
- Bucket by deliberation type (reasoning / knowledge / negotiation / policy) so
  users can see where the mechanism lifts over solo agents and where it doesn't.

Ships default-on per `feedback_darpa_always_on`: the readout is a field in
existing tool output, no opt-in flag.

Acceptance: running the readout over accumulated history prints a match rate
with n, broken out by deliberation type and a solo-agent baseline column. No new
model calls beyond the solo baseline runs.

## Feature 2 (larger): direction-judgment benchmark suite

A curated set of deliberations with later-observable ground truth, used to
measure whether gemot's mechanism lifts solo-agent direction-choosing.

- Target: reproduce or beat Anthropic's 64% on a comparable research-direction
  task. Single-agent baseline measured, then fleet deliberation measured on the
  same prompts.
- Sources for ground truth: hindsight on published research bets that did or
  didn't pan out, plancheck `record_outcome` data where decisions were logged
  with predicted-vs-actual outcomes, and prospective deliberations the project
  commits to scoring after the outcome is known.
- Sharpens the "recognizing dead ends" mapping (moderate → tight): the benchmark
  prompts include known-dead-end research directions where the right answer is
  "do not pursue."
- Becomes the demo for the bottleneck framing. Without it, the lens is rhetoric;
  with it, the lens has a number.

Scope note: Feature 1 is the high-signal, low-cost move — do it first and let
its numbers inform whether the larger benchmark investment in Feature 2 is
warranted, mirroring plancheck's calibration-rate / direction-judgment-climb
split.

## Honest seam (don't overclaim)

There is a tempting governance application — "verification infrastructure that a
credible AI-development pause would require." gemot is the **deliberation and
commitment** layer (how parties reach and record a joint decision). It is **not**
the **attestation** layer (cryptographic proof a party actually honored the
commitment, e.g. stopped training).

Attestation is the natural complement layer. Gemot's audit log carries provenance
for *decisions* (who voted what, which crux survived). Attestation would carry
provenance for *executions* (which weights produced which output) — TPM /
SEV-SNP-style hardware attestation of model runs, a Sigstore-style transparency
log for committed artifacts. The clean integration point: a commitment record
(`decide action:commit`) gets an optional `attestation_ref` field, and verifiers
chase the chain.

**Status:** Not in current roadmap. Right next layer if a counterparty ever needs
cryptographic proof of model behavior, not just a reasoning trail.

## Related

Sibling moves in the calibration family — three angles on the same Anthropic
article, one portfolio response to the research-judgment bottleneck:

- `../plancheck/FEATURE_REQUEST_calibration-rate.md` — publish verdict-vs-outcome
  agreement rate at the plan-verification layer.
- `../hindcast/FEATURE_REQUEST_outcome-calibration.md` — generalize
  predicted-vs-actual machinery from wall-clock to outcome/success calibration.
- This document — judgment-aggregation calibration + direction-judgment benchmark
  at the fleet-deliberation layer.

gemot owns *aggregation* in the family; plancheck owns *verdict calibration*;
hindcast owns *outcome prior*. Three angles, one bottleneck.

---

Quotes confirmed 2026-06-04, word-for-word. Cross-verified against
`../plancheck/FEATURE_REQUEST_calibration-rate.md`.
