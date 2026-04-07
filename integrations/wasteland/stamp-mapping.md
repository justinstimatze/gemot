# Gemot ↔ Wasteland Stamp Mapping

How gemot analysis results map to Wasteland stamp dimensions.

## Stamp Schema

The Wasteland `stamps` table stores:

```sql
-- From wl_commons schema
stamps (
  author      TEXT,       -- validator rig handle
  subject     TEXT,       -- contributor rig handle
  valence     JSON,       -- {quality, reliability, creativity}
  confidence  REAL,       -- 0.0–1.0
  severity    TEXT,       -- leaf/branch/root
  CHECK (NOT(author = subject))  -- yearbook rule
)
```

## Stamp Dimension → Gemot Signal

| Stamp Field | Gemot Source | Mapping |
|---|---|---|
| `valence.quality` | `relevant_cruxes` resolution | All cruxes resolved → 0.9+. Unresolved cruxes reduce proportionally. |
| `valence.reliability` | `integrity_warnings` count | 0 warnings → 1.0. Each warning −0.1. Sybil warning → 0.0. Analysis refusal → 0.0. |
| `valence.creativity` | `topic_summaries` count | Normalize: topics / max(topics across recent deliberations). |
| `confidence` | Analysis `confidence` field | Direct: "high" → 0.9, "medium" → 0.7, "low" → 0.5, "refused" → 0.0. |
| `severity` | Deliberation context | Wanted item priority: P0-P1 → "root", P2 → "branch", P3-P4 → "leaf". |

## Deriving a Stamp from Analysis

```bash
#!/usr/bin/env bash
# After deliberation completes, extract stamp scores

GEMOT_URL="${GEMOT_URL:-https://gemot.dev/a2a}"

rpc() {
  curl -s "$GEMOT_URL" -X POST \
    -H "Authorization: Bearer $GEMOT_KEY" \
    -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"gemot/$1\",\"params\":$2}"
}

DELIB_ID="$1"

# Get analysis result for confidence + cruxes
ANALYSIS=$(rpc "analyze" "{\"action\":\"get_result\",\"deliberation_id\":\"$DELIB_ID\"}")

# Get context for integrity warnings
CONTEXT=$(rpc "participate" "{\"action\":\"get_context\",\"deliberation_id\":\"$DELIB_ID\",\"agent_id\":\"validator-rig\"}")

# Extract stamp components
CONFIDENCE=$(echo "$ANALYSIS" | jq -r '.result.confidence // "low"')
CRUX_COUNT=$(echo "$ANALYSIS" | jq '[.result.cruxes // [] | .[] | select(.resolved != true)] | length')
TOTAL_CRUXES=$(echo "$ANALYSIS" | jq '.result.cruxes // [] | length')
WARNINGS=$(echo "$CONTEXT" | jq '.result.integrity_warnings // [] | length')
TOPICS=$(echo "$ANALYSIS" | jq '.result.topic_summaries // [] | length')
SYBIL=$(echo "$CONTEXT" | jq '[.result.integrity_warnings // [] | .[] | select(. == "SYBIL_SIGNAL")] | length')

# Calculate quality (crux resolution rate)
if [ "$TOTAL_CRUXES" -gt 0 ]; then
  RESOLVED=$((TOTAL_CRUXES - CRUX_COUNT))
  QUALITY=$(echo "scale=2; $RESOLVED / $TOTAL_CRUXES" | bc)
else
  QUALITY="0.90"  # no cruxes = agreement
fi

# Calculate reliability (integrity)
if [ "$SYBIL" -gt 0 ]; then
  RELIABILITY="0.0"
else
  RELIABILITY=$(echo "scale=2; 1.0 - ($WARNINGS * 0.1)" | bc)
  # Floor at 0
  if (( $(echo "$RELIABILITY < 0" | bc -l) )); then RELIABILITY="0.0"; fi
fi

# Map confidence
case "$CONFIDENCE" in
  high)   CONF_SCORE="0.9" ;;
  medium) CONF_SCORE="0.7" ;;
  low)    CONF_SCORE="0.5" ;;
  *)      CONF_SCORE="0.0" ;;
esac

echo "Stamp: quality=$QUALITY reliability=$RELIABILITY creativity=$TOPICS confidence=$CONF_SCORE"
```

## Trust Tier → Conviction Weight

When submitting positions, map the rig's Wasteland trust tier to gemot conviction:

| Trust Tier | Level | Conviction |
|---|---|---|
| Imperator | 3+ | 0.9 |
| Warrior | 3 | 0.8 |
| Settler | 2 | 0.7 |
| Scavenger | 1 | 0.5 |
| Drifter | 0 | 0.3 |

```bash
# Submit position with conviction derived from trust tier
rpc "participate" "{
  \"action\": \"submit_position\",
  \"deliberation_id\": \"$DELIB_ID\",
  \"agent_id\": \"validator-rig-42\",
  \"content\": \"The error handling in this PR is insufficient...\",
  \"conviction\": 0.8,
  \"reservation\": \"Cannot accept without error handling for the database timeout case\"
}"
```

## When to Create a Deliberation

| Wasteland Event | Gemot Template | Threshold |
|---|---|---|
| Completion rejected, contributor disputes | `negotiation` | 60% (ZOPA-aware) |
| Multiple validators disagree on a stamp | `jury` | 92% (near-unanimous) |
| Community policy dispute | `parliament` | 51% (majority) |
| Novel architecture question | `review` | 75% |
| Cross-wasteland governance | `consensus` | 100% (unanimous) |

## Audit Trail

Every operation is logged. Rigs call `admin action:get_audit_log` to verify the process was fair. The audit log maps to the Wasteland's evidence-traced reputation model — every stamp can reference a deliberation ID as provenance.

```bash
rpc "admin" "{\"action\":\"get_audit_log\",\"deliberation_id\":\"$DELIB_ID\"}"
```
