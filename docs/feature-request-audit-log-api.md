# Feature Request: Audit Log API Endpoint

**Status: Done. Shipped 2026-04-06.**

## What shipped

The `deliberation action:export` response now includes the full `audit_log` — all operations with timestamps, agent IDs, and method names. No limit, no extra API call needed.

```json
{
  "deliberation": { ... },
  "rounds": [ ... ],
  "commitments": [ ... ],
  "resolution": { ... },
  "audit_log": [
    {"id": "1", "timestamp": "2026-04-06T10:30:00Z", "method": "participate:submit_position", "agent_id": "safety-researcher", "key_id": ""},
    {"id": "2", "timestamp": "2026-04-06T10:30:05Z", "method": "analyze:run", "agent_id": "", "key_id": ""},
    ...
  ]
}
```

Available on both MCP (`deliberation action:export`) and A2A (`gemot/deliberation` with `action: "export"`).

The standalone `admin action:get_audit_log` endpoint also still works (capped at 50 entries by default, pass higher limit to get more).

## What gemotvis needs to do

1. In the poller/exporter, read `audit_log` from the export response (it's already there)
2. Map method names to scrubber timeline events:
   - `participate:submit_position` → "X submits position"
   - `participate:vote` → "X votes"
   - `analyze:run` → "Analysis started"
   - `analyze:expert_panel` → "Expert panel created"
   - `decide:commit` → "X commits"
   - `decide:fulfill` → "X fulfills commitment"
   - `decide:break` → "X breaks commitment"
   - `deliberation:create` → "Deliberation created"
   - `deliberation:delete` → "Deliberation deleted"
   - `coordinate:join` → "X joins via code"
3. Use timestamps for scrubber positioning instead of synthesizing from position/vote created_at
