# Gemot Byzantine Adversarial Metrics

Empirical Sybil-resistance measurements from `tests/adversarial_byzantine_test.go`,
run against the real analysis pipeline with `GEMOT_EIGENTRUST_ENABLED=true`
and Postgres persistence. Metrics are deterministic (mock LLM, structured
split by `positionType`) and citable from the DARPA-PS-26-09 Track 1 bid.

All ratios reflect the reputation lever in isolation — the cold-start cap
multiplies into the effective-weight chain alongside trust × correlation
× sqrt(conviction × time). Absolute numbers therefore depend on those
other factors; ratios are the load-bearing signal.

## Measured scenarios

| Scenario | n | f (Sybil) | honest/sybil effective-weight ratio | Status |
| --- | --- | --- | --- | --- |
| `f < 1/4` | 4 | 1 (25%) | **3.00** | passes — honest dominates |
| `f = 1/3` | 6 | 2 (33%) | **2.00** | passes — theoretical limit, still majority |
| `f > 1/2` | 4 | 3 (75%) | **0.33** | documented failure — attacker majority |
| cold-start flooding | 12 | 10 newcomers | Sybil reputation sum = **1.00** (= n_sybil × ColdCap, bound holds) | passes — cap enforced |
| 3-Sybil graduation cliff | 3 | 3 (100%) | ring weight sum = **3.00** after 2 rounds at K=2 | documented limitation |

## Interpretation

- **Cold-start cap is the primary defense.** When all agents are cold, the
  ratio tracks the raw cohort split (3:1 at f=1/4, 4:2 at f=1/3, 1:3 at
  f>1/2). The mechanism adds zero Byzantine tolerance above what honest
  majority would already provide, but it *bounds the damage* during the
  cold window — the window in which a Sybil ring is indistinguishable
  from newcomers.
- **`minDistinctAgreers=2` blocks the 2-Sybil pair attack but not N≥3 rings.**
  `TestByzantineGraduationCliff` confirms a 3-agent mutual-endorsement ring
  graduates all three within 2 rounds when `ColdThreshold=2`. After
  graduation, the cold cap no longer applies and EigenTrust scores can be
  pooled by the ring.
- **Reputation does not defend the crux-extraction stage.** `TestByzantineCruxPoisoning`
  shows the cold cap only multiplies into the *synthesis* stage. Low-effort
  positions are caught by `LOW_EFFORT_ABS` / `COVERAGE` integrity warnings;
  reputation is orthogonal.
- **No decay, no negative signals.** `TestByzantineReputationLaundering`
  confirms graduated agents retain their cold-cap exemption indefinitely.
  This is the concrete empirical motivation for the next step:
  `agent_trust_edges.weight` half-life + dispute-signal ingestion (tracked
  as a Planned row in `THREAT_MODEL.md`).

## Reproducing

```bash
# Requires local Postgres (same DSN convention as the rest of the suite).
go test ./tests/ -run 'TestByzantine' -v
```

Each test logs its metric via `t.Log` so ratios can be grep'd from CI
output. Scenarios that documents rather than enforce a failure (`f>1/2`,
graduation cliff, laundering) pass unconditionally — their purpose is to
baseline the current state so regressions against future decay /
negative-signal work are visible.
