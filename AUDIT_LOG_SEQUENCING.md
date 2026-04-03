# Audit Log Sequencing

## Problem

gemotvis replays deliberation timelines by stepping through audit log operations chronologically. However, multiple operations frequently share the same timestamp (same-second granularity), making it impossible to determine the correct order from timestamps alone.

Example from a v9 diplomacy session — all at `2026-03-31 21:56:30`:
```
submit_position  england-agent
submit_position  italy-agent
analyze
```

When timestamps cluster, gemotvis cannot distinguish "england submitted first, then italy, then analysis ran" from any other ordering. This causes:
- Analysis/cruxes appearing before the conversation has played out
- Positions appearing simultaneously instead of sequentially
- Inconsistent replay behavior

## Current Workaround (gemotvis)

gemotvis uses the **array position** of operations in the audit log as an implicit sequence. It counts how many ops from each deliberation the global scrubber has stepped past, then slices the sorted ops array accordingly. This works but is fragile — it depends on the array order being meaningful, which isn't guaranteed by the API contract.

## Proposed Fix

Add a monotonically increasing `sequence` field to each audit log operation:

```json
{
  "operations": [
    {
      "sequence": 1,
      "timestamp": "2026-03-31T18:32:04Z",
      "method": "create_deliberation",
      "agent_id": ""
    },
    {
      "sequence": 2,
      "timestamp": "2026-03-31T18:32:04Z",
      "method": "set_template",
      "agent_id": ""
    },
    {
      "sequence": 3,
      "timestamp": "2026-03-31T18:32:05Z",
      "method": "submit_position",
      "agent_id": "england-agent"
    }
  ]
}
```

### Requirements

1. **Per-deliberation monotonic counter** — each deliberation's audit log has its own sequence starting at 1
2. **No gaps** — every operation gets the next integer
3. **Assigned at write time** — the sequence is set when the operation is appended, not computed later
4. **Returned by `get_audit_log`** — included in the API response
5. **Stable across reads** — the same operation always has the same sequence number

### Also consider

- **Return ops in chronological order** (oldest first) — the current API sometimes returns newest-first, which forces clients to sort. Oldest-first is the natural order for replay.
- **Sub-second timestamps** — if the storage layer supports it, using millisecond or microsecond precision would help even without a sequence field. But sequence is more robust.

## Impact

With a `sequence` field, gemotvis can:
- Sort ops unambiguously: `ops.sort((a, b) => a.sequence - b.sequence)`
- Filter precisely: "show ops with sequence <= N"
- Build global timelines by interleaving ops from multiple deliberations using `(timestamp, sequence)` as a composite sort key
- Eliminate all the array-position and op-counting workarounds
