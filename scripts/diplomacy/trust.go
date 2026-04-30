package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- Trust Tracking (2a) ---

type trustRecord struct {
	promisedSupports int
	honoredSupports  int
	brokenPromises   []string // "promised support in Burgundy F1902, ordered hold instead"
}

type trustTracker struct {
	records map[string]map[string]*trustRecord // power -> counterpart -> record
}

func newTrustTracker() *trustTracker {
	return &trustTracker{records: make(map[string]map[string]*trustRecord)}
}

func (t *trustTracker) get(power, counterpart string) *trustRecord {
	power = strings.ToUpper(power)
	counterpart = strings.ToUpper(counterpart)
	if t.records[power] == nil {
		t.records[power] = make(map[string]*trustRecord)
	}
	if t.records[power][counterpart] == nil {
		t.records[power][counterpart] = &trustRecord{}
	}
	return t.records[power][counterpart]
}

// extractPromises parses diplomatic messages for military support promises.
// Only matches promises that reference specific units or territories — diplomatic
// language like "I support your position" is NOT a military support promise.
// Returns: promiser (uppercase) -> beneficiary (uppercase) -> []promise descriptions
func extractPromises(messages []Message) map[string]map[string][]string {
	promises := make(map[string]map[string][]string)

	// Patterns that indicate a specific military support order promise.
	// Must reference a unit type (A/F) or a territory abbreviation.
	supportPatterns := []string{
		"support a ", "support f ", // "I will support A VIE", "support F TRI"
		"s a ", "s f ", // shorthand: "A GAL S A VIE"
		"order support",                      // "I'll order support for your unit"
		"support your a ", "support your f ", // "support your A BUD"
		"support into", "support move", "support hold", // tactical support language
	}

	// Territory names that confirm a military context (3-letter abbreviations)
	territoryPattern := func(s string) bool {
		// Check if the message contains a 3-letter territory abbreviation near "support"
		upper := strings.ToUpper(s)
		supportIdx := strings.Index(strings.ToLower(s), "support")
		if supportIdx == -1 {
			return false
		}
		// Look for territory abbreviation within 30 chars of "support"
		window := upper
		if supportIdx+40 < len(upper) {
			window = upper[supportIdx : supportIdx+40]
		} else if supportIdx < len(upper) {
			window = upper[supportIdx:]
		}
		for territory := range adjacency {
			base := strings.Split(territory, "/")[0]
			if len(base) == 3 && strings.Contains(window, " "+base+" ") {
				return true
			}
			if len(base) == 3 && strings.HasSuffix(window, " "+base) {
				return true
			}
		}
		return false
	}

	for _, msg := range messages {
		if strings.ToUpper(msg.Recipient) == "GLOBAL" {
			continue
		}
		content := strings.ToLower(msg.Content)
		hasPromise := false

		// Check for specific military support patterns
		for _, pat := range supportPatterns {
			if strings.Contains(content, pat) {
				hasPromise = true
				break
			}
		}

		// Fallback: "will support" or "promise to support" + territory reference
		if !hasPromise {
			if (strings.Contains(content, "will support") || strings.Contains(content, "promise to support") ||
				strings.Contains(content, "i'll support")) && territoryPattern(msg.Content) {
				hasPromise = true
			}
		}

		if !hasPromise {
			continue
		}
		sender := strings.ToUpper(msg.Sender)
		recipient := strings.ToUpper(msg.Recipient)
		if promises[sender] == nil {
			promises[sender] = make(map[string][]string)
		}
		desc := truncateRunes(msg.Content, 120)
		promises[sender][recipient] = append(promises[sender][recipient],
			fmt.Sprintf("[%s] %s", msg.Phase, desc))
	}
	return promises
}

// checkPromiseFollowThrough cross-references promises against actual support orders.
func checkPromiseFollowThrough(game *GameState, year int, promises map[string]map[string][]string) *trustTracker {
	tracker := newTrustTracker()

	// Build unit ownership map for the year
	gameYear := 1900 + year
	targetPhases := map[string]bool{
		fmt.Sprintf("S%dM", gameYear): true,
		fmt.Sprintf("F%dM", gameYear): true,
	}

	unitOwner := make(map[string]string)
	for _, phase := range game.Phases {
		if !targetPhases[phase.Name] {
			continue
		}
		for power, units := range phase.State.Units {
			for _, unit := range units {
				unitOwner[unit] = strings.ToUpper(power)
			}
		}
	}

	// Find actual cross-power support orders: who supported whom
	actualSupports := make(map[string]map[string]bool) // supporter -> beneficiary -> true
	for _, phase := range game.Phases {
		if !targetPhases[phase.Name] {
			continue
		}
		for power, orders := range phase.Orders {
			power = strings.ToUpper(power)
			for _, order := range orders {
				if order == nil {
					continue
				}
				orderStr, ok := order.(string)
				if !ok {
					continue
				}
				if !strings.Contains(orderStr, " S ") {
					continue
				}
				parts := strings.SplitN(orderStr, " S ", 2)
				if len(parts) != 2 {
					continue
				}
				supportTarget := strings.TrimSpace(parts[1])
				supportedUnit := supportTarget
				if dashIdx := strings.Index(supportTarget, " - "); dashIdx != -1 {
					supportedUnit = supportTarget[:dashIdx]
				}
				supportedUnit = strings.TrimSpace(supportedUnit)

				if owner, ok := unitOwner[supportedUnit]; ok && owner != power {
					if actualSupports[power] == nil {
						actualSupports[power] = make(map[string]bool)
					}
					actualSupports[power][owner] = true
				}
			}
		}
	}

	// Cross-reference promises against actual supports
	for promiser, beneficiaries := range promises {
		for beneficiary, promiseDescs := range beneficiaries {
			rec := tracker.get(promiser, beneficiary)
			rec.promisedSupports += len(promiseDescs)

			if actualSupports[promiser] != nil && actualSupports[promiser][beneficiary] {
				rec.honoredSupports += len(promiseDescs)
			} else {
				for _, desc := range promiseDescs {
					rec.brokenPromises = append(rec.brokenPromises,
						fmt.Sprintf("promised %s but no matching support order found", desc))
				}
			}
		}
	}

	return tracker
}

// reputation holds cross-deliberation trust data for a power.
type reputation struct {
	Total     int     `json:"total_commitments"`
	Fulfilled int     `json:"fulfilled"`
	Broken    int     `json:"broken"`
	Pending   int     `json:"pending"`
	Score     float64 `json:"trust_score"` // fulfilled / (fulfilled + broken)
}

// auditCommitments cross-references existing commitments with trust data and
// calls fulfill/break on the gemot server. Returns reputation per power.
func auditCommitments(ctx context.Context, url, secret string, results []scopeResult, trust *trustTracker) map[string]reputation {
	reputations := make(map[string]reputation)

	for _, r := range results {
		if r.scope.scopeTag != "bilateral" || len(r.commitments) == 0 {
			continue
		}
		powers := r.scope.powers
		if len(powers) != 2 {
			continue
		}

		for _, c := range r.commitments {
			if c.Status != "pending" && c.Status != "active" && c.Status != "" {
				continue // already resolved
			}
			if c.ID == "" {
				continue // can't fulfill/break without ID
			}

			// Determine which power made this commitment
			agentPower := strings.TrimSuffix(c.AgentID, "-agent")
			agentPower = strings.ToUpper(agentPower)

			// Find the counterpart
			var counterpart string
			for _, p := range powers {
				if strings.ToUpper(p) != agentPower {
					counterpart = strings.ToUpper(p)
				}
			}
			if counterpart == "" {
				continue
			}

			// Check trust record for this power → counterpart
			rec := trust.get(agentPower, counterpart)
			if rec.promisedSupports == 0 {
				continue // no promises to evaluate
			}

			// Determine outcome: fulfilled if they honored all promises, broken if any broken
			rs := connectForCall(ctx, url, secret)
			if rs == nil {
				continue
			}
			if rec.honoredSupports > 0 && len(rec.brokenPromises) == 0 {
				callToolSoft(ctx, rs, "decide", map[string]any{
					"action":        "fulfill",
					"commitment_id": c.ID,
					"verified_by":   "diplomacy-script",
				})
				fmt.Fprintf(os.Stderr, "  commitment %s: FULFILLED (%s honored %d/%d supports to %s)\n",
					c.ID[:8], agentPower, rec.honoredSupports, rec.promisedSupports, counterpart)
			} else if len(rec.brokenPromises) > 0 {
				reason := fmt.Sprintf("broken promises: %s", strings.Join(rec.brokenPromises, "; "))
				if len(reason) > 500 {
					reason = reason[:497] + "..."
				}
				callToolSoft(ctx, rs, "decide", map[string]any{
					"action":        "break",
					"commitment_id": c.ID,
					"reason":        reason,
					"verified_by":   "diplomacy-script",
				})
				fmt.Fprintf(os.Stderr, "  commitment %s: BROKEN (%s broke %d promise(s) to %s)\n",
					c.ID[:8], agentPower, len(rec.brokenPromises), counterpart)
			}
			rs.Close() //nolint:errcheck
		}
	}

	// Fetch reputation for each power
	for _, power := range []string{"AUSTRIA", "ENGLAND", "FRANCE", "GERMANY", "ITALY", "RUSSIA", "TURKEY"} {
		rs := connectForCall(ctx, url, secret)
		if rs == nil {
			continue
		}
		agentID := strings.ToLower(power) + "-agent"
		repJSON := callToolSoft(ctx, rs, "decide", map[string]any{
			"action":   "reputation",
			"agent_id": agentID,
		})
		rs.Close() //nolint:errcheck
		if repJSON != "" {
			var rep reputation
			mustParseSoft(repJSON, &rep)
			if rep.Total > 0 {
				reputations[strings.ToLower(power)] = rep
			}
		}
	}

	return reputations
}

// connectForCall creates a short-lived session for a single tool call.
func connectForCall(ctx context.Context, url, secret string) *sdkmcp.ClientSession {
	s, err := connect(ctx, url, secret)
	if err != nil {
		return nil
	}
	return s
}
