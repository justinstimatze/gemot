# Report Visualization: gemotvis Integration Guide

## Speaker Identity Anonymization

**Legal requirement**: Reports MUST NOT attribute AI-synthesized stances to named real individuals without consent. See *Angwin v. Superhuman Platform* (S.D.N.Y. 2026).

**Anonymization is end-to-end.** The pipeline anonymizes at every layer:
- **Agent IDs**: `t3c-speaker-a`, `t3c-steelman-b` (no real names)
- **Position content**: real names replaced with pseudonyms before submission to the server
- **Analysis results**: clean because the LLM only sees anonymized input
- **Report markdown + JSON export**: post-processed with name replacement as a final layer

**gemotvis does not need any anonymization logic.** The API data is safe to display directly. The `--named` flag exists for internal/authorized use only.

If gemotvis stores or caches data from before this change (deliberations with IDs like `t3c-speaker-speaker-e`), **scrub that data and its git history**.

## Live Test Data

**Deliberation**: `3b820d7b-a70d-4e4a-a48d-c259c6bd28d6`
- 3 rounds (R1: initial analysis, R2: bridge + empty chair, R3: revised positions + 4 resolution proposals)
- 8 speaker-derived agents + 4 probes + 3 structural R2 agents + 12 R3 agents (8 revised + 4 resolutions)
- Agent kinds: steelman, speaker, probe, bridge, dissent, empty-chair, resolution
- Agent IDs: `t3c-speaker-a` through `t3c-speaker-h`, `t3c-steelman-a`/`t3c-steelman-b`

**Static exports** (in repo):
- `integrations/t3c/ai-manifestos-report.md` — anonymized markdown with TOC
- `integrations/t3c/ai-manifestos-report.json` — anonymized structured JSON (all 3 rounds)

## API Endpoints

### Get all rounds
```
gemot/analyze action:get_result deliberation_id:3b820d7b-... round:-1
-> [AnalysisResult, AnalysisResult, AnalysisResult]  (R1, R2, R3)
```

### Get specific round
```
gemot/analyze action:get_result deliberation_id:3b820d7b-... round:1
-> AnalysisResult
```

### Get positions with kind metadata
```
gemot/participate action:get_positions deliberation_id:3b820d7b-...
-> [{position_id, agent_id, content, metadata: {kind: "speaker"}, ...}, ...]
```

### Export full deliberation (what gemotvis uses)
```
gemot/deliberation action:export deliberation_id:3b820d7b-...
-> {deliberation, positions, votes, analysis_results, audit_log}
```

## Shared Types Package

Replace gemotvis's mirrored `internal/gemot/types.go`:

```go
import "github.com/justinstimatze/gemot/types"
// types.AnalysisResult, types.Crux, types.Position, etc.
```

Delete `internal/gemot/types.go`. The `types/` package (`types/types.go` + `types/analysis.go`) is the single source of truth. Imports only `"time"`.

## Data Available on AnalysisResult

### Always present
- `cruxes` — with `agree_agents`, `disagree_agents`, `controversy_score`, `stances` (5-point qualified), `explanation`
- `discarded_cruxes` — degenerate cruxes with empty sides
- `consensus_statements`, `bridging_statements`
- `topic_summaries` — each has `topic_id` (T1-T6) for cross-round tracking
- `integrity_warnings` — parse for `ANALYSIS_REFUSED:`, `HALLUCINATION:`, `SYBIL_SIGNAL:`
- `compromise_proposal` — suppress when `ANALYSIS_REFUSED` is present
- `audit_log` — pipeline stage details

### Qualified stances on cruxes (new)

Cruxes now carry a `stances` array with 5-point values and per-agent qualifiers:

```json
{
  "crux_claim": "Building superintelligent AI without solved alignment...",
  "stances": [
    {"agent_id": "t3c-speaker-d", "value": 2, "qualifier": "Explicitly frames extinction as the central expected outcome"},
    {"agent_id": "t3c-speaker-e", "value": 2, "qualifier": "Mirrors the same load-bearing claim"},
    {"agent_id": "t3c-steelman-a", "value": -2, "qualifier": "Argues treating any AI pressure as arbitrarily intense is a systematic bias"}
  ],
  "agree_agents": ["t3c-speaker-d", "t3c-speaker-e"],
  "disagree_agents": ["t3c-steelman-a"],
  "explanation": "The core tension here is between those who treat extinction-level catastrophe as..."
}
```

Values: +2 (strongly agree), +1 (agree with caveats), 0 (genuinely torn), -1 (disagree with caveats), -2 (strongly disagree). The `agree_agents`/`disagree_agents` lists are derived from stances for backwards compatibility.

Rendering suggestion: stance values as colored chips (+2 green, +1 light green, 0 gray, -1 light red, -2 red); qualifiers as tooltips or expandable text.

### Present when validation was run (check for nil/missing)
```json
{
  "verification": {
    "total": 44,
    "checked": 44,
    "downgraded": 20,
    "threshold": 2,
    "score_dist": [0, 4, 14, 8, 14, 4],
    "details": [
      {
        "speaker": "speaker-d",
        "crux": "The design of AGI systems inherently encourages power-seeking...",
        "orig_stance": "disagree",
        "score": 2,
        "reason": "The quotes show this speaker acknowledges both AI risks and benefits..."
      }
    ]
  },
  "null_control": { "pass": true, "real_metrics": {...}, "null_metrics": {...} },
  "replication": { "stability": {"tier": 2, "all_stable": true}, "runs": [...] },
  "coverage_gaps": [{"position": "...", "missing_perspective": "...", "suggested_source": "..."}]
}
```

Note: The live test deliberation may not have all validation fields populated (null_control, replication, and coverage_gaps require extra flags during pipeline runs). Handle null/absent gracefully.

## Agent Kind Values

Read from `position.metadata.kind`:

| Kind | Category | Description |
|---|---|---|
| `speaker` | speaker-derived | Singleton from T3C source |
| `steelman` | speaker-derived | Multi-member cluster |
| `probe` | structural | Topic investigator |
| `bridge` | structural | Cross-cluster common ground (R2) |
| `dissent` | structural | Challenges consensus (R2) |
| `empty-chair` | structural | Amplifies minority side (R2) |
| `resolution` | structural | Actionable proposal (R3) |

Use for:
- Two-track crux display (speaker-derived vs structural agree/disagree)
- Participant categorization
- Graph view node differentiation

## Rendering Guide

### Crux Stances
When `stances` array is present on a crux, render per-agent stance lines:
- Value as colored label: `+2`, `+1`, `0`, `-1`, `-2`
- Agent name via `shortAgentID()` (will show "A", "B", etc. for anonymous IDs)
- Qualifier as secondary text

When `stances` is absent, fall back to `agree_agents` / `disagree_agents` flat lists.

### Reliability Table
Check each field's presence and render conditionally:

| Dimension | How to compute status |
|---|---|
| Internal coherence | `discarded_cruxes.length / (cruxes.length + discarded_cruxes.length)` — pass if <20% |
| Agent hallucinations | Count `HALLUCINATION:` in `integrity_warnings` — none/minor(1-3)/moderate(4-9)/high(10+) |
| Stance grounding | `verification.downgraded` / `verification.checked` — show "cleaned" with counts, or "untested" if null |
| Null control | `null_control.pass` — show pass/fail with failed metrics, or "untested" if null |
| Replication | `replication.stability.all_stable` — show tier + CV, or "untested" if null |

### Common Ground
May be empty. When empty, the pipeline outputs a "no consensus" note rather than omitting the section. Render as an informational banner.

### Position Evolution (R3)
Match R3 agents by stripping `-r3` suffix to find R1 original. R3 position text may contain `[HELD]`, `[UPDATED]`, `[NEW]` tags — render with distinct styling if present.

### Cross-Round Topic Evolution
Fetch all rounds via `round:-1`. Compare:
- **New cruxes**: claims in R2 not in R1 (exact string match)
- **Topic renames**: same `topic_id`, different `topic` name

### Report Sections (Minto pyramid order)
1. **Proposed Actions** — resolution proposals with support/opposition
2. **Key Disagreements** — cruxes with qualified stances
3. **Common Ground** — consensus items or "no consensus" note
4. **Position Evolution** — R1→R3 comparison per speaker
5. **Synthesis** — LLM-generated compromise
6. **Participants** — grouped by kind
7. **Confidence & Caveats** — reliability table
8. **Appendix** — per-round detail (collapsible)
