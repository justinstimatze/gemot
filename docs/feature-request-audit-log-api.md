# Feature Request: Audit Log API Endpoint

**Status: Endpoint already exists. Gemotvis needs to call it.**

## Current State

Gemot has a working audit log API:
- **MCP**: `admin action:get_audit_log deliberation_id:...` (server.go:894)
- **A2A**: `gemot/admin` with `action: "get_audit_log"` (a2a.go:823)
- **Store**: `GetAuditLog(deliberationID, limit)` returns `[]map[string]string` with id, timestamp, key_id, method, agent_id (repository.go:932)
- **Limit**: 50 entries per call (hardcoded)

The audit_log table tracks: submit_position, vote, analyze:run, deliberation:create, deliberation:delete, decide:commit, decide:fulfill, decide:break, expert_panel, follow_up, and more.

## What gemotvis needs to do

1. Call `gemot/admin action:get_audit_log` for each deliberation during polling
2. Use the returned operations to populate the scrubber timeline
3. Map method names to display events:
   - `participate:submit_position` → "X submits position"
   - `participate:vote` → "X votes"  
   - `analyze:run` → "Analysis started"
   - `decide:commit` → "X commits to..."
   - `deliberation:create` → "Deliberation created"

## Optional gemot enhancement

Include `audit_log` in the `deliberation action:export` response (Option B from original request). This would let gemotvis get everything in one call instead of N+1 calls (export + audit per deliberation).

The export function is at `internal/mcp/core.go:120` (CoreExportDeliberation). Would need the store.DB passed through or a Service method wrapping GetAuditLog.

## Impact

The scrubber currently only shows position/vote events synthesized from timestamps. Adding audit log events would show the complete deliberation lifecycle: when analysis ran, when commitments were made, when rounds changed.
