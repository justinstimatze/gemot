package main

import (
	"fmt"
	"strings"
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
		"s a ", "s f ",             // shorthand: "A GAL S A VIE"
		"order support",            // "I'll order support for your unit"
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
