# T3C Integration Feedback

Findings from building gemot's T3C structural import pipeline, tested against the public AI Manifestos report. This document is intended as constructive feedback for T3C development.

## speakerCruxMatrix Quality

**The core issue**: The `speakerCruxMatrix` agree/disagree classifications are not well-grounded in speaker source quotes.

We built an automated spot-check that verifies each stance assignment against the speaker's actual quotes using an LLM (Haiku). On the AI Manifestos report:

- **15/15 disagree stances failed verification** (100% failure rate)
- **16/29 agree stances failed verification** (55% failure rate)
- **31/44 total stances downgraded** to no_position (70% of the matrix was noise)
- After cleaning, only 13/44 stances survived — these all passed independent spot-check (100%)

### Disagree stances

T3C's LLM appears to over-classify "disagree." The failure modes:

1. **Absence as disagreement**: Speaker's quotes don't address the crux topic at all, but the matrix codes "disagree" (e.g., Speaker D coded as "disagree" on power-seeking behavior — his quotes don't mention it)
2. **Nuance as disagreement**: Speaker has a nuanced position that partially overlaps with the claim, but the matrix codes "disagree" instead of "no_position" (e.g., Speaker C coded as "disagree" on existential threat — her quotes show concern about AI risk but skepticism about specific framings)
3. **Agreement misclassified**: Speaker's quotes actually support the claim, but the matrix codes "disagree" (e.g., Speaker L team coded as "disagree" on prioritizing safety — their quotes explicitly affirm safety priority)

### Agree stances

Less systematically broken, but still noisy:

1. **Tangential agreement**: Speaker mentioned the topic but didn't clearly endorse the specific claim
2. **Attributed by association**: Speaker grouped with others who agree, but their own quotes don't clearly support the stance (common for steelman cluster members like Speaker E and Speaker B)

### Impact on downstream analysis

The ungrounded stances propagate through any system that uses the matrix:
- **Clustering**: Speakers who share "disagree" stances get grouped together even when neither actually disagrees — this creates phantom clusters
- **Crux detection**: Cruxes with artificial disagree sides appear more controversial than they are
- **Vote seeding**: When the matrix drives vote patterns (as in our pipeline), noise in the matrix becomes noise in the deliberation

### Suggestions

1. **Default to no_position**: The stance extraction prompt should treat "no_position" as the default. Only code "agree" when quotes explicitly support the claim, only code "disagree" when quotes explicitly oppose it. Silence should be silence, not disagreement.

2. **Confidence scores**: Expose a confidence score per stance (0-1) alongside the classification. Downstream consumers can threshold: high-confidence stances get used, low-confidence ones get treated as unknown.

3. **Quote attribution per stance**: For each (speaker, crux) classification, include the specific quote(s) that motivated the classification. This enables automated verification and gives consumers a paper trail.

4. **Separate extraction from classification**: Generate crux claims in one pass, then classify stances in a separate pass. This prevents the LLM from tailoring claims to fit pre-decided stances.

## Report JSON Structure

The tupled `["v0.2", {...}]` format for `data` and `metadata` is unusual and requires special handling. A flat structure would be more ergonomic:

```json
{
  "version": "0.2",
  "data": { ... },
  "metadata": { ... }
}
```

## Crux Coverage

The AI Manifestos report had 12 cruxes across 4 topics with 10 speakers. This is a good density. The crux claims themselves are well-formulated — the quality issue is in the stance assignments, not the claims.

The `addOns.subtopicCruxes` structure is clean and easy to work with. The `controversyScore` field is useful for filtering.

## Feature Requests

### 1. Stance confidence scores
As described above — expose per-stance confidence alongside the binary classification.

### 2. Quote-grounded crux stances
T3C already links quotes → claims → speakers (via sourceId references). But the crux layer (`subtopicCruxes`, `speakerCruxMatrix`) doesn't link back to which quote(s) motivated each agree/disagree classification. The `explanation` field is LLM prose, not a pointer. Adding quote IDs per stance would close the provenance chain and enable automated verification.

### 3. Stable topic/crux IDs
When re-running T3C on the same data, topic and crux IDs change. Stable IDs would enable cross-run comparison and incremental updates.

### 4. Export verified stances
After a downstream system (like gemot) verifies stances, it would be useful to feed the corrections back into T3C — an API endpoint to update stance assignments with verification results.

---

*This document will be updated as we gather more data from additional T3C report imports.*
