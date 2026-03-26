package analysis

import (
	"strings"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

// ZOPAResult describes the Zone of Possible Agreement.
type ZOPAResult struct {
	Feasible       bool     `json:"feasible"`
	CommonGround   []string `json:"common_ground"`     // positions all agents can accept
	BlockingAgents []string `json:"blocking_agents"`    // agents whose reservations conflict with all proposals
	Conflicts      []string `json:"conflicts,omitempty"` // specific reservation-proposal conflicts
}

// ComputeZOPA determines whether a zone of possible agreement exists
// by checking consensus/bridging positions against agent reservations.
func ComputeZOPA(
	positions []deliberation.Position,
	consensus []deliberation.ConsensusStatement,
	bridging []deliberation.BridgingStatement,
) *ZOPAResult {
	// Collect reservations
	reservations := map[string]string{} // agent -> reservation
	for _, p := range positions {
		if p.Reservation != "" {
			reservations[p.AgentID] = p.Reservation
		}
	}

	if len(reservations) == 0 {
		// No reservations = trivially feasible
		var cg []string
		for _, cs := range consensus {
			cg = append(cg, cs.Content)
		}
		for _, bs := range bridging {
			cg = append(cg, bs.Content)
		}
		return &ZOPAResult{Feasible: true, CommonGround: cg}
	}

	// Collect all proposed agreements (consensus + bridging)
	type proposal struct {
		content string
		source  string // "consensus" or "bridging"
	}
	var proposals []proposal
	for _, cs := range consensus {
		proposals = append(proposals, proposal{cs.Content, "consensus"})
	}
	for _, bs := range bridging {
		proposals = append(proposals, proposal{bs.Content, "bridging"})
	}

	// Check each proposal against each reservation
	// Heuristic: keyword overlap between reservation and proposal suggests conflict
	var commonGround []string
	var conflicts []string
	blockingAgents := map[string]bool{}

	for _, prop := range proposals {
		conflictsWithAny := false
		for agent, res := range reservations {
			if reservationConflicts(res, prop.content) {
				conflicts = append(conflicts, agent+"'s reservation conflicts with "+prop.source+": "+truncateStr(prop.content, 60))
				conflictsWithAny = true
				blockingAgents[agent] = true
			}
		}
		if !conflictsWithAny {
			commonGround = append(commonGround, prop.content)
		}
	}

	// ZOPA is feasible if at least one proposal survives all reservations
	feasible := len(commonGround) > 0

	var blockers []string
	for a := range blockingAgents {
		// Only count as blocking if ALL proposals conflict with this agent
		allConflict := true
		for _, prop := range proposals {
			if !reservationConflicts(reservations[a], prop.content) {
				allConflict = false
				break
			}
		}
		if allConflict {
			blockers = append(blockers, a)
		}
	}

	return &ZOPAResult{
		Feasible:       feasible,
		CommonGround:   commonGround,
		BlockingAgents: blockers,
		Conflicts:      conflicts,
	}
}

// reservationConflicts checks if a reservation text conflicts with a proposal.
// Uses keyword negation heuristic: if the reservation says "cannot accept X"
// and the proposal mentions X, there's likely a conflict.
func reservationConflicts(reservation, proposal string) bool {
	resLower := strings.ToLower(reservation)
	propLower := strings.ToLower(proposal)

	// Extract key phrases from reservation (after "cannot", "will not", "refuse", "unacceptable")
	negativeMarkers := []string{"cannot accept", "will not accept", "refuse", "unacceptable", "must not", "never"}
	for _, marker := range negativeMarkers {
		idx := strings.Index(resLower, marker)
		if idx >= 0 {
			// Extract the object of the negation
			remainder := resLower[idx+len(marker):]
			// Check if key words from the remainder appear in the proposal
			words := strings.Fields(remainder)
			matchCount := 0
			for _, word := range words {
				word = strings.Trim(word, ".,;:!?\"'")
				if len(word) > 3 && strings.Contains(propLower, word) {
					matchCount++
				}
			}
			// If 2+ content words overlap, likely conflict
			if matchCount >= 2 {
				return true
			}
		}
	}
	return false
}
