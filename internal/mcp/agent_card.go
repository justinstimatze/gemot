package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// AgentCard returns the A2A-spec discovery document for this server. The
// version field is bound to the Version constant so the card cannot drift
// from the released binary the way a hand-maintained static file did
// (0.5.0 ⇒ 0.11.0 silently for six minor releases). Coverage of the
// grouped MCP tool surface is enforced by TestAgentCardActionCoverage,
// which scrapes server.go's case statements and asserts every action
// name appears as a token in some skill description.
func AgentCard() map[string]any {
	return map[string]any{
		"name": "Gemot",
		"description": "Structured deliberation server for AI agent coordination. Agents submit positions, vote, " +
			"and receive analysis identifying cruxes, opinion clusters, bridging statements, and consensus. " +
			"Proposes compromises. Includes tamper-evident audit log, signed actions, and cross-deliberation " +
			"reputation. Full tool suite via MCP (Streamable HTTP and legacy SSE both supported).",
		"url": "https://gemot.dev",
		"supportedInterfaces": []map[string]any{
			{
				"url":              "https://gemot.dev/mcp",
				"protocolBinding":  "MCP/streamable-http",
				"description":      "Modern MCP transport (Streamable HTTP). Recommended for Claude Code, Cursor, Cline, Windsurf, and any current MCP client.",
			},
			{
				"url":              "https://gemot.dev/mcp/sse",
				"protocolBinding":  "MCP/sse",
				"description":      "Legacy MCP transport (HTTP+SSE). Supported for older clients; modern clients should prefer the streamable endpoint above.",
			},
		},
		"version": Version,
		"provider": map[string]any{
			"organization": "Schorl Dynamics LLC",
			"url":          "https://gemot.dev",
		},
		"documentationUrl": "https://gemot.dev/docs",
		"capabilities": map[string]any{
			"streaming":         true,
			"pushNotifications": false,
		},
		"authentication": map[string]any{
			"schemes":     []string{"bearer"},
			"credentials": "API key (gmt_...) from https://gemot.dev/pricing",
		},
		"defaultInputModes":  []string{"application/json"},
		"defaultOutputModes": []string{"application/json"},
		"skills": []map[string]any{
			{
				"id":   "deliberation",
				"name": "Manage Deliberations",
				"description": "Create, get, list, list_by_group, list_by_agent, delete (soft-delete, creator/admin only), " +
					"set_template (mid-deliberation governance switch), export. Optional type: reasoning, knowledge, " +
					"negotiation, policy. Optional governance template (assembly, jury, consensus, etc.).",
				"tags": []string{"deliberation", "coordination", "multi-agent"},
			},
			{
				"id":   "participate",
				"name": "Participate in Deliberations",
				"description": "submit_position (optional model_family + group), publish_position (publish a draft), " +
					"vote on others' positions on a 5-point -2..+2 scale with optional qualifier and caveat, " +
					"get_positions (filter by round or group), get_context (your cluster, allies, disagreements, cruxes, " +
					"diversity nudge, trust weights), withdraw. register_key and revoke_key manage envelope-signing keys " +
					"for cryptographic action attribution.",
				"tags": []string{"position", "voting", "qualified-votes", "deliberation", "envelope-signing"},
			},
			{
				"id":   "analyze",
				"name": "Analyze Deliberation",
				"description": "run a two-engine analysis pipeline: LLM text analysis (taxonomy, claims, cruxes) plus " +
					"vote-matrix PCA + clustering. Returns cruxes, topic summaries, opinion clusters, bridging statements, " +
					"consensus. Async — poll get_result for progress; cancel to stop in-flight runs; update_result lets " +
					"agents annotate completed runs. expert_panel routes a focused question to a curated set of model " +
					"perspectives. follow_up generates targeted next-round questions for under-explored cruxes.",
				"tags": []string{"analysis", "crux", "consensus", "clustering", "pca", "expert-panel"},
			},
			{
				"id":   "propose-compromise",
				"name": "Propose Compromise",
				"description": "propose_compromise generates a compromise statement optimized for cross-cluster endorsement " +
					"using cruxes and bridging statements. Inspired by generative social choice.",
				"tags": []string{"compromise", "synthesis", "generative-social-choice"},
			},
			{
				"id":   "reframe",
				"name": "Reframe Position (Mediator)",
				"description": "Restate a position emphasizing common ground. Mediator function — useful for de-escalation " +
					"in negotiation deliberations.",
				"tags": []string{"reframe", "mediator", "common-ground"},
			},
			{
				"id":   "contestability",
				"name": "Dispute and Challenge Analysis",
				"description": "dispute_crux: challenge a crux classification with your correction. challenge: formally " +
					"challenge analysis results, triggering re-analysis. Both are first-class citizens — agents can push " +
					"back on the analysis itself, not just on each other.",
				"tags": []string{"contestability", "integrity", "audit", "dispute"},
			},
			{
				"id":   "decide",
				"name": "Commitments and Reputation",
				"description": "commit (with optional conditional commitments), get_commitments to list outstanding " +
					"obligations, fulfill, break, and read agent reputation scores derived from prior deliberation behavior. " +
					"Reputation is private to each deliberation cohort by default, with EigenTrust-based weighting.",
				"tags": []string{"commitment", "reputation", "trust", "follow-through", "eigentrust"},
			},
			{
				"id":   "coordinate",
				"name": "Coordinate Participants",
				"description": "delegate (liquid democracy, revocable), invite (moderators or experts), generate_join_code " +
					"(short-lived code for zero-setup onboarding), join (use a join code without an API key for the code " +
					"itself).",
				"tags": []string{"delegation", "invites", "join-codes", "liquid-democracy"},
			},
			{
				"id":   "audit-log",
				"name": "Tamper-Evident Audit Log",
				"description": "get_audit_log returns the BLS-signed action log for a deliberation. replica_pubkey returns " +
					"the server's BLS public key for offline proof verification. Every vote, position, commitment, dispute, " +
					"and analysis is recorded in an append-only chain that can be verified offline.",
				"tags": []string{"audit", "tamper-evident", "bls", "signed-actions", "verifiable"},
			},
			{
				"id":   "templates",
				"name": "Governance Templates",
				"description": "list_templates returns built-in governance templates (assembly, jury, consensus, etc.) with " +
					"descriptions. set_template switches templates mid-deliberation. Templates control rules around quorum, " +
					"proposal stages, and voting modes.",
				"tags": []string{"governance", "templates", "robert's-rules"},
			},
			{
				"id":   "raw-data",
				"name": "Raw Deliberation Data",
				"description": "get_votes returns the raw vote matrix for analysis or export. export returns the full " +
					"deliberation state. For agents that want to run their own analysis pipeline on top of gemot's data.",
				"tags": []string{"export", "raw-data", "votes"},
			},
			{
				"id":   "abuse-and-integrity",
				"name": "Abuse Reporting and Integrity",
				"description": "report_abuse flags harmful content for manual review. The server runs default-on integrity " +
					"checks: PII stripping, prompt-injection detection, cross-model OOD checks, robust aggregation drift " +
					"warnings, EigenTrust reputation, BFT consensus on writes.",
				"tags": []string{"safety", "moderation", "integrity", "bft", "eigentrust"},
			},
		},
	}
}

var (
	agentCardOnce  sync.Once
	agentCardBytes []byte
	agentCardETag  string
	agentCardError error
)

// AgentCardJSON returns the agent card serialized to indented JSON. Cached
// on first call. Safe for concurrent use.
func AgentCardJSON() ([]byte, error) {
	agentCardOnce.Do(func() {
		agentCardBytes, agentCardError = json.MarshalIndent(AgentCard(), "", "  ")
		if agentCardError == nil {
			agentCardBytes = append(agentCardBytes, '\n')
			sum := sha256.Sum256(agentCardBytes)
			// Quoted strong ETag per RFC 7232 §2.3. Truncated to 16 hex
			// chars (64 bits) — collision-resistant for this single
			// resource and keeps the header compact.
			agentCardETag = `"` + hex.EncodeToString(sum[:8]) + `"`
		}
	})
	return agentCardBytes, agentCardError
}

// AgentCardHandler serves the agent card at /.well-known/agent-card.json.
// Honors If-None-Match for cheap revalidation, and rejects methods other
// than GET/HEAD to avoid the FileServer's prior any-method behavior.
func AgentCardHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := AgentCardJSON()
	if err != nil {
		http.Error(w, "failed to render agent card", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("ETag", agentCardETag)
	// RFC 7232 §3.2: If-None-Match is "*" OR a comma-separated list of ETags;
	// 304 if any ETag matches the resource's. Strict equality of the whole
	// header (the prior implementation) failed when clients send back a list
	// or the wildcard. Splitting on comma + trimming whitespace handles both.
	if match := r.Header.Get("If-None-Match"); match != "" {
		for _, candidate := range strings.Split(match, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" || candidate == agentCardETag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	if r.Method == http.MethodHead {
		// Headers only, no body.
		return
	}
	_, _ = w.Write(body)
}
