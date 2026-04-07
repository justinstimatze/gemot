# T3C Integration Notes

Observations from building gemot's T3C structural import pipeline, tested against the public AI Manifestos report. Feature requests tracked in Linear.

## What Works Well

- **Crux claims are high quality** — the 12 cruxes across 4 topics were well-formulated and captured genuine fault lines in the discourse
- **`addOns.subtopicCruxes` structure** is clean and easy to consume. `controversyScore` is useful for filtering
- **Quote → claim → speaker provenance chain** is solid via sourceId references
- **Report density** (12 cruxes, 10 speakers, 49 claims) provides good material for downstream analysis

## speakerCruxMatrix Observations

When we verified matrix stance assignments against source quotes (using an LLM to check whether a speaker's quotes actually support the assigned agree/disagree), we found the matrix is more confident than the underlying data warrants. On the AI Manifestos report, about 70% of stances didn't survive strict verification against source quotes.

The pattern: "disagree" classifications are especially fragile. Common cases where the verifier disagreed with the matrix:

- Speaker's quotes don't address the crux topic (coded as "disagree" rather than "no_position")
- Speaker has a nuanced position that partially overlaps (coded as binary "disagree" when reality is more complex)
- Speaker grouped by association with a cluster rather than individual quote evidence

This may be inherent to the task — mapping continuous nuance to a three-way classification is hard, and the matrix is useful even as an approximation. Our pipeline now includes a `--verify-stances` pre-processing step that checks each stance against source quotes and downgrades ungrounded ones to "no_position" before analysis.

## Feature Requests (Linear)

1. **Stance confidence scores** — expose per-stance confidence (0-1) alongside the classification, so downstream consumers can threshold
2. **Quote-grounded crux stances** — the crux layer (`subtopicCruxes`, `speakerCruxMatrix`) doesn't link back to which quote(s) motivated each classification. The `explanation` field is prose, not a pointer. Adding quote IDs per stance would close the provenance chain
3. **Stable topic/crux IDs** — re-running T3C on the same data produces different IDs, making cross-run comparison difficult
4. **Stance correction feedback** — after a downstream system verifies stances, a way to feed corrections back

## JSON Structure Note

The tupled `["v0.2", {...}]` format for `data` and `metadata` requires special parsing. A flat `{"version": "0.2", "data": {...}}` structure would be more ergonomic for consumers.

---

*Updated as we test additional T3C reports.*
