package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ============================================================
// V12: Coalition Declarations
// ============================================================

// coalitionDecl is a single coalition declared by a power.
type coalitionDecl struct {
	Members []string `json:"members"`
	Purpose string   `json:"purpose"`
}

// coalitionGroup is a validated coalition with mutual declarations from all members.
type coalitionGroup struct {
	Members []string
	Purpose string // merged purpose from declarations
}

// declareAllCoalitions runs coalition declarations for all powers in a single batched LLM call.
func declareAllCoalitions(ctx context.Context, apiKey string, year int, results []scopeResult, balance powerBalance) map[string][]coalitionDecl {
	// Build intelligence summary for all powers in one prompt
	var allIntel strings.Builder
	for _, power := range powers {
		intel := buildPowerIntel(power, results, balance)
		if intel == "" {
			continue
		}
		fmt.Fprintf(&allIntel, "\n======== %s's INTELLIGENCE ========\n%s\n", power, intel)
	}

	system := `You are a strategic advisor analyzing ALL powers in a Diplomacy game simultaneously. For each power, based on their diplomatic intelligence, declare coalitions they should pursue.

A coalition is a group of 2+ powers with a shared strategic purpose. Coalitions can overlap.

Rules:
- Only declare coalitions where genuine mutual interest exists
- Be specific about the purpose
- Include the declaring power in every coalition's member list

Respond with ONLY valid JSON, no other text:`

	prompt := fmt.Sprintf(`Year: %d

%s

For EACH power, declare their coalitions as JSON:
{"declarations": {"AUSTRIA": [{"members": ["AUSTRIA", "RUSSIA"], "purpose": "..."}], "ENGLAND": [...], ...}}`, 1900+year, allIntel.String())

	resp, err := llmCall(ctx, apiKey, system, prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  [coalition] batched declaration failed: %v\n", err)
		return map[string][]coalitionDecl{}
	}

	var parsed struct {
		Declarations map[string][]coalitionDecl `json:"declarations"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp)), &parsed); err != nil {
		fmt.Fprintf(os.Stderr, "  [coalition] parsing batched declarations: %v\n", err)
		return map[string][]coalitionDecl{}
	}

	// Normalize
	for power, decls := range parsed.Declarations {
		for i := range decls {
			for j := range decls[i].Members {
				decls[i].Members[j] = strings.ToUpper(decls[i].Members[j])
			}
			sort.Strings(decls[i].Members)
		}
		fmt.Fprintf(os.Stderr, "  [coalition] %s: declared %d coalition(s)\n", power, len(decls))
		for _, d := range decls {
			fmt.Fprintf(os.Stderr, "    %s — %s\n", strings.Join(d.Members, "+"), d.Purpose)
		}
		parsed.Declarations[strings.ToUpper(power)] = decls
	}

	return parsed.Declarations
}

// buildPowerIntel assembles diplomatic intelligence for a single power from scope results.
func buildPowerIntel(power string, results []scopeResult, balance powerBalance) string {
	powerLower := strings.ToLower(power)
	var intel strings.Builder

	for _, r := range results {
		if r.scope.scopeTag != "bilateral" {
			continue
		}
		ctxJSON, ok := r.contexts[powerLower]
		if !ok {
			continue
		}
		var ac agentContext
		mustParseSoft(ctxJSON, &ac)

		other := ""
		for _, p := range r.scope.powers {
			if strings.ToLower(p) != powerLower {
				other = p
			}
		}

		fmt.Fprintf(&intel, "\n--- With %s ---\n", other)
		if len(ac.BridgingStatements) > 0 {
			for _, bs := range ac.BridgingStatements {
				fmt.Fprintf(&intel, "  Shared: %s (%.0f%%)\n", truncateRunes(bs.Content, 150), bs.BridgingScore*100)
			}
		}
		if ac.CompromiseProposal != "" {
			fmt.Fprintf(&intel, "  Compromise: %s\n", truncateRunes(ac.CompromiseProposal, 200))
		}
		if len(ac.RelevantCruxes) > 0 {
			fmt.Fprintf(&intel, "  Issues: %d unresolved\n", len(ac.RelevantCruxes))
		}
	}

	if intel.Len() == 0 {
		return ""
	}

	// Add balance context
	fmt.Fprintf(&intel, "\nSC counts: ")
	for _, p := range powers {
		marker := ""
		if strings.EqualFold(p, power) {
			marker = "*"
		}
		fmt.Fprintf(&intel, "%s:%d%s ", p[:3], balance.current[p], marker)
	}
	return intel.String()
}

// matchCoalitions finds coalitions where ALL members mutually declared each other.
// Returns validated coalition groups.
func matchCoalitions(declarations map[string][]coalitionDecl) []coalitionGroup {
	// Build a map: sorted member set key → which powers declared it
	type candidateInfo struct {
		members    []string
		purposes   []string // purposes from each declaring power
		declaredBy map[string]bool
	}
	candidates := make(map[string]*candidateInfo)

	for power, decls := range declarations {
		for _, d := range decls {
			if len(d.Members) < 2 {
				continue
			}
			key := strings.Join(d.Members, "+")
			if candidates[key] == nil {
				candidates[key] = &candidateInfo{
					members:    d.Members,
					declaredBy: make(map[string]bool),
				}
			}
			candidates[key].declaredBy[strings.ToUpper(power)] = true
			candidates[key].purposes = append(candidates[key].purposes, d.Purpose)
		}
	}

	var groups []coalitionGroup
	for _, c := range candidates {
		// Check if ALL members declared this coalition
		allDeclared := true
		for _, m := range c.members {
			if !c.declaredBy[m] {
				allDeclared = false
				break
			}
		}
		if allDeclared {
			// Use first purpose (they're usually saying the same thing)
			purpose := c.purposes[0]
			if len(purpose) > 200 {
				purpose = purpose[:200] + "..."
			}
			groups = append(groups, coalitionGroup{
				Members: c.members,
				Purpose: purpose,
			})
		}
	}

	// Sort for deterministic output
	sort.Slice(groups, func(i, j int) bool {
		return strings.Join(groups[i].Members, "+") < strings.Join(groups[j].Members, "+")
	})
	return groups
}

// buildCoalitionScopes creates alliance scopes from validated coalition groups.
// Only creates scopes for 3+ member coalitions — 2-member coalitions are redundant
// with bilateral scopes and would produce duplicate analysis.
func buildCoalitionScopes(coalitions []coalitionGroup, messages []Message, year int) []scope {
	var scopes []scope
	bilateral := make(map[string][]Message)
	for _, msg := range messages {
		if !strings.EqualFold(msg.Recipient, "GLOBAL") {
			key := pairKey(msg.Sender, msg.Recipient)
			bilateral[key] = append(bilateral[key], msg)
		}
	}

	for _, c := range coalitions {
		if len(c.Members) < 3 {
			continue // 2-member coalitions are already covered by bilateral analysis
		}
		var msgs []Message
		for i := 0; i < len(c.Members); i++ {
			for j := i + 1; j < len(c.Members); j++ {
				key := pairKey(c.Members[i], c.Members[j])
				msgs = append(msgs, bilateral[key]...)
			}
		}
		if len(msgs) == 0 {
			continue
		}
		scopes = append(scopes, scope{
			name:      strings.Join(c.Members, "+"),
			scopeTag:  "coalition",
			template:  "consensus",
			delibType: "negotiation",
			powers:    c.Members,
			messages:  msgs,
		})
	}
	return scopes
}

// ============================================================
// V12: Cross-Message Consistency Checking
// ============================================================

// inconsistency represents a detected contradiction in a power's cross-bilateral statements.
type inconsistency struct {
	Power       string `json:"power"`
	SaidTo      string `json:"said_to"`
	SaidAbout   string `json:"said_about"`
	Statement1  string `json:"statement_to_one"`
	Statement2  string `json:"statement_to_other"`
	Explanation string `json:"explanation"`
}

// detectInconsistencies compares all powers' statements across bilaterals in a single batched LLM call.
func detectInconsistencies(ctx context.Context, apiKey string, messages []Message, year int) map[string][]inconsistency {
	// Build all powers' cross-bilateral messages in one prompt
	var allMsgDumps strings.Builder
	powersWithData := 0

	for _, power := range powers {
		byRecipient := make(map[string][]string)
		for _, msg := range messages {
			if !strings.EqualFold(msg.Sender, power) {
				continue
			}
			if strings.EqualFold(msg.Recipient, "GLOBAL") {
				continue
			}
			recipient := strings.ToUpper(msg.Recipient)
			byRecipient[recipient] = append(byRecipient[recipient],
				fmt.Sprintf("[to %s, %s] %s", recipient, msg.Phase, truncateRunes(msg.Content, 500)))
		}
		if len(byRecipient) < 2 {
			continue
		}
		powersWithData++
		fmt.Fprintf(&allMsgDumps, "\n======== %s's MESSAGES ========\n", power)
		for recipient, msgs := range byRecipient {
			fmt.Fprintf(&allMsgDumps, "--- To %s ---\n", recipient)
			for _, m := range msgs {
				fmt.Fprintf(&allMsgDumps, "%s\n", m)
			}
		}
	}

	if powersWithData == 0 {
		return map[string][]inconsistency{}
	}

	system := `You are analyzing ALL Diplomacy players' messages for contradictions. For each power, compare what they said to different recipients. Look for:
- Contradictory promises (same territory promised to two powers)
- Conflicting strategic commitments (told A they'd attack B, told B they'd attack A)
- Inconsistent threat assessments
- Playing both sides

Only report genuine contradictions. Respond with ONLY valid JSON.`

	prompt := fmt.Sprintf(`Year: %d

%s

For each power with contradictions, list them. Respond as JSON:
{"all_inconsistencies": {"AUSTRIA": [{"said_to": "POWER_A", "said_about": "topic", "statement_to_one": "...", "statement_to_other": "...", "explanation": "..."}], "ENGLAND": [], ...}}

Omit powers with no contradictions.`, 1900+year, allMsgDumps.String())

	resp, err := llmCall(ctx, apiKey, system, prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  [consistency] batched detection failed: %v\n", err)
		return map[string][]inconsistency{}
	}

	var parsed struct {
		AllInconsistencies map[string][]inconsistency `json:"all_inconsistencies"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp)), &parsed); err != nil {
		fmt.Fprintf(os.Stderr, "  [consistency] parse error: %v\n", err)
		return map[string][]inconsistency{}
	}

	result := make(map[string][]inconsistency)
	for power, incs := range parsed.AllInconsistencies {
		power = strings.ToUpper(power)
		for i := range incs {
			incs[i].Power = power
		}
		if len(incs) > 0 {
			result[power] = incs
			fmt.Fprintf(os.Stderr, "  [consistency] %s: %d contradiction(s) detected\n", power, len(incs))
		}
	}
	return result
}

// ============================================================
// V12: Bait Scoring (Proposal Asymmetry Detection)
// ============================================================

// baitScore measures how asymmetric a bilateral proposal is.
type baitScore struct {
	Bilateral   string  `json:"bilateral"`
	Proposal    string  `json:"proposal"`
	Asymmetry   float64 `json:"asymmetry"`    // 0-1, higher = more one-sided
	FavorsPower string  `json:"favors_power"` // which power benefits more
	Reason      string  `json:"reason"`
	Suspicious  bool    `json:"suspicious"` // true if likely bait
}

// scoreBaitForAll evaluates proposal asymmetry across all bilaterals.
func scoreBaitForAll(ctx context.Context, apiKey string, results []scopeResult, balance powerBalance, territory map[string]territorialInfo) map[string][]baitScore {
	// Collect all bilaterals with proposals
	type proposalInput struct {
		scopeName string
		powers    []string
		proposal  string
	}
	var inputs []proposalInput
	for _, r := range results {
		if r.scope.scopeTag != "bilateral" {
			continue
		}
		for _, ctxJSON := range r.contexts {
			var ac agentContext
			mustParseSoft(ctxJSON, &ac)
			if ac.CompromiseProposal != "" {
				inputs = append(inputs, proposalInput{
					scopeName: r.scope.name,
					powers:    r.scope.powers,
					proposal:  ac.CompromiseProposal,
				})
				break // only need one context per bilateral (proposal is shared)
			}
		}
	}

	if len(inputs) == 0 {
		return nil
	}

	// Build all proposals into one batch prompt
	var proposalDump strings.Builder
	for i, inp := range inputs {
		p0, p1 := strings.ToUpper(inp.powers[0]), strings.ToUpper(inp.powers[1])
		fmt.Fprintf(&proposalDump, "\n--- Proposal %d: %s ↔ %s ---\n", i+1, p0, p1)
		fmt.Fprintf(&proposalDump, "Proposal: %s\n", inp.proposal)
		// Add territorial context
		info0, info1 := territory[p0], territory[p1]
		fmt.Fprintf(&proposalDump, "%s has %d SCs, %d units; %s has %d SCs, %d units\n",
			p0, balance.current[p0], len(info0.units), p1, balance.current[p1], len(info1.units))
	}

	system := `You are evaluating Diplomacy proposals for asymmetry. For each proposal between two powers, assess:
1. Does the proposal benefit one side significantly more than the other?
2. Is the proposal a potential "bait" — offering something appealing but strategically dangerous to accept?
3. Does the proposal require one side to make irreversible concessions while the other retains flexibility?

Score asymmetry from 0.0 (perfectly balanced) to 1.0 (entirely one-sided).
Mark as suspicious if the asymmetry score is >= 0.6 OR if the proposal contains classic bait patterns.

Respond with ONLY valid JSON, no other text.`

	prompt := fmt.Sprintf(`Evaluate these Diplomacy proposals for asymmetry:
%s

Respond as JSON:
{"scores": [{"bilateral": "POWER1-POWER2", "asymmetry": 0.0-1.0, "favors_power": "POWER", "reason": "brief explanation", "suspicious": true/false}]}`, proposalDump.String())

	resp, err := llmCall(ctx, apiKey, system, prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  [bait] scoring failed: %v\n", err)
		return nil
	}

	var parsed struct {
		Scores []baitScore `json:"scores"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp)), &parsed); err != nil {
		fmt.Fprintf(os.Stderr, "  [bait] parse error: %v\n", err)
		return nil
	}

	// Index by bilateral name and by each power involved
	result := make(map[string][]baitScore)
	for _, s := range parsed.Scores {
		// Add to both powers' bait lists
		parts := strings.SplitN(s.Bilateral, "-", 2)
		for _, p := range parts {
			p = strings.ToUpper(p)
			result[p] = append(result[p], s)
		}
		if s.Suspicious {
			fmt.Fprintf(os.Stderr, "  [bait] %s: asymmetry=%.0f%% favors %s — %s\n",
				s.Bilateral, s.Asymmetry*100, s.FavorsPower, s.Reason)
		}
	}
	return result
}

// ============================================================
// V12: Relationship State Machine
// ============================================================

// relationshipState tracks the trajectory of a bilateral relationship.
type relationshipState struct {
	State    string // "hostile", "strained", "neutral", "cooperative", "allied"
	Trend    string // "deteriorating", "stable", "improving"
	Evidence []string
}

// computeRelationshipStates derives relationship states from trust, alignment, and crux data.
func computeRelationshipStates(results []scopeResult, trust *trustTracker, balance powerBalance) map[string]map[string]relationshipState {
	states := make(map[string]map[string]relationshipState)

	for _, power := range powers {
		states[power] = make(map[string]relationshipState)
		powerLower := strings.ToLower(power)

		for _, r := range results {
			if r.scope.scopeTag != "bilateral" {
				continue
			}
			ctxJSON, ok := r.contexts[powerLower]
			if !ok {
				continue
			}
			var ac agentContext
			mustParseSoft(ctxJSON, &ac)

			other := ""
			for _, p := range r.scope.powers {
				if !strings.EqualFold(p, power) {
					other = strings.ToUpper(p)
				}
			}
			if other == "" {
				continue
			}

			rs := relationshipState{State: "neutral", Trend: "stable"}
			var evidence []string

			// Factor 1: Trust (promise honor rate)
			rec := trust.get(power, other)
			recReverse := trust.get(other, power)
			totalPromises := rec.promisedSupports + recReverse.promisedSupports
			totalHonored := rec.honoredSupports + recReverse.honoredSupports
			if totalPromises > 0 {
				honorRate := float64(totalHonored) / float64(totalPromises)
				if honorRate >= 0.8 {
					evidence = append(evidence, fmt.Sprintf("high trust (%.0f%% promises honored)", honorRate*100))
				} else if honorRate < 0.4 {
					evidence = append(evidence, fmt.Sprintf("low trust (%.0f%% promises honored)", honorRate*100))
				}
			}
			totalBroken := len(rec.brokenPromises) + len(recReverse.brokenPromises)

			// Factor 2: Alignment scores (only meaningful when vote data exists)
			var alignScore float64
			hasAlignData := len(ac.AlignmentScores) > 0
			for _, a := range ac.AlignmentScores {
				alignScore = a.AlignmentScore
			}
			if hasAlignData && alignScore >= 0.67 {
				evidence = append(evidence, fmt.Sprintf("strong alignment (%.0f%%)", alignScore*100))
			} else if hasAlignData && alignScore > 0 && alignScore < 0.3 {
				evidence = append(evidence, fmt.Sprintf("weak alignment (%.0f%%)", alignScore*100))
			}
			// Note: 0% alignment in bilaterals usually means no vote data, not disagreement

			// Factor 3: Unresolved cruxes
			cruxCount := len(ac.RelevantCruxes)
			if cruxCount == 0 {
				evidence = append(evidence, "no unresolved issues")
			} else if cruxCount >= 3 {
				evidence = append(evidence, fmt.Sprintf("%d unresolved issues", cruxCount))
			}

			// Factor 4: Bridging / shared ground
			hasBridging := len(ac.BridgingStatements) > 0
			hasProposal := ac.CompromiseProposal != ""
			if hasBridging && hasProposal {
				evidence = append(evidence, "active shared ground + compromise proposal")
			} else if hasBridging {
				evidence = append(evidence, "shared ground identified")
			}

			// Factor 5: Consensus statements (strong positive signal)
			hasConsensus := len(ac.ConsensusStatements) > 0
			if hasConsensus {
				evidence = append(evidence, fmt.Sprintf("%d points of consensus", len(ac.ConsensusStatements)))
			}

			// Factor 6: Rule violations
			if len(ac.RuleViolations) > 0 {
				evidence = append(evidence, fmt.Sprintf("%d trust violations flagged", len(ac.RuleViolations)))
			}

			// Compute state from factors.
			// Start at neutral (0) and adjust based on evidence.
			// Positive: bridging, proposals, consensus, few cruxes, honored promises
			// Negative: broken promises, rule violations, many cruxes, low alignment (when data exists)
			score := 0
			if hasAlignData && alignScore >= 0.67 {
				score += 2
			} else if hasAlignData && alignScore >= 0.4 {
				score++
			} else if hasAlignData && alignScore > 0 && alignScore < 0.2 {
				score-- // only penalize alignment when we have actual data showing disagreement
			}
			if totalBroken > 0 {
				score -= totalBroken
			}
			if totalPromises > 0 && totalHonored == totalPromises {
				score += 2
			}
			if cruxCount == 0 {
				score += 2 // no disputes is a strong positive
			} else if cruxCount <= 3 {
				// some issues but manageable — neutral
			} else if cruxCount >= 6 {
				score -= 2
			} else {
				score--
			}
			if hasBridging {
				score += 2 // shared ground is a strong positive
			}
			if hasProposal {
				score++ // active compromise effort
			}
			if hasConsensus {
				score += 2 // consensus is the strongest positive signal
			}
			if len(ac.RuleViolations) > 0 {
				score -= 2
			}

			switch {
			case score >= 4:
				rs.State = "allied"
			case score >= 2:
				rs.State = "cooperative"
			case score >= 0:
				rs.State = "neutral"
			case score >= -2:
				rs.State = "strained"
			default:
				rs.State = "hostile"
			}

			// Trend: for year 1, base on promise trajectory
			if totalBroken > 0 && totalHonored > 0 {
				rs.Trend = "deteriorating"
			} else if totalPromises > 0 && totalBroken == 0 {
				rs.Trend = "improving"
			}

			rs.Evidence = evidence
			states[power][other] = rs
		}
	}
	return states
}

// ============================================================
// V12: Diplomacy Board Adjacency Graph
// ============================================================

// adjacency maps each territory to its neighbors (for threat proximity detection).
// Standard Diplomacy map — 75 territories, land and sea adjacencies.
var adjacency = map[string][]string{
	// Supply centers and key territories (abbreviated standard names)
	"ADR": {"ALB", "APU", "ION", "TRI", "VEN"},
	"AEG": {"BUL/SC", "CON", "EAS", "GRE", "ION", "SMY"},
	"ALB": {"ADR", "GRE", "ION", "SER", "TRI"},
	"ANK": {"ARM", "BLA", "CON"},
	"APU": {"ADR", "ION", "NAP", "ROM", "VEN"},
	"ARM": {"ANK", "BLA", "SEV", "SMY", "SYR"},
	"BAL": {"BER", "BOT", "DEN", "KIE", "LVN", "PRU", "SWE"},
	"BAR": {"NWG", "NWY", "STP/NC"},
	"BEL": {"BUR", "ENG", "HOL", "NTH", "PIC", "RUH"},
	"BER": {"BAL", "KIE", "MUN", "PRU", "SIL"},
	"BLA": {"ANK", "ARM", "BUL/EC", "CON", "RUM", "SEV"},
	"BOH": {"GAL", "MUN", "SIL", "TYR", "VIE"},
	"BOT": {"BAL", "FIN", "LVN", "STP/SC", "SWE"},
	"BRE": {"ENG", "GAS", "MAO", "PAR", "PIC"},
	"BUD": {"GAL", "RUM", "SER", "TRI", "VIE"},
	"BUL": {"CON", "GRE", "RUM", "SER"},
	"BUR": {"BEL", "GAS", "MAR", "MUN", "PAR", "PIC", "RUH"},
	"CLY": {"EDI", "LVP", "NAO", "NWG"},
	"CON": {"AEG", "ANK", "BLA", "BUL", "SMY"},
	"DEN": {"BAL", "HEL", "KIE", "NTH", "SKA", "SWE"},
	"EAS": {"AEG", "ION", "SMY", "SYR"},
	"EDI": {"CLY", "LVP", "NTH", "NWG", "YOR"},
	"ENG": {"BEL", "BRE", "IRI", "LON", "MAO", "NTH", "PIC", "WAL"},
	"FIN": {"BOT", "NWY", "STP/SC", "SWE"},
	"GAL": {"BOH", "BUD", "RUM", "SIL", "UKR", "VIE", "WAR"},
	"GAS": {"BRE", "BUR", "MAO", "MAR", "PAR", "SPA/NC"},
	"GRE": {"AEG", "ALB", "BUL", "ION", "SER"},
	"HEL": {"DEN", "HOL", "KIE", "NTH"},
	"HOL": {"BEL", "HEL", "KIE", "NTH", "RUH"},
	"ION": {"ADR", "AEG", "ALB", "APU", "EAS", "GRE", "NAP", "TUN", "TYS"},
	"IRI": {"ENG", "LVP", "MAO", "NAO", "WAL"},
	"KIE": {"BAL", "BER", "DEN", "HEL", "HOL", "MUN", "RUH"},
	"LON": {"ENG", "NTH", "WAL", "YOR"},
	"LVN": {"BAL", "BOT", "MOS", "PRU", "STP/SC", "WAR"},
	"LVP": {"CLY", "EDI", "IRI", "NAO", "WAL", "YOR"},
	"LYO": {"MAR", "PIE", "SPA/SC", "TUS", "TYS", "WES"},
	"MAO": {"BRE", "ENG", "GAS", "IRI", "NAF", "NAO", "POR", "SPA/NC", "SPA/SC", "WES"},
	"MAR": {"BUR", "GAS", "LYO", "PIE", "SPA/SC"},
	"MOS": {"LVN", "SEV", "STP", "UKR", "WAR"},
	"MUN": {"BER", "BOH", "BUR", "KIE", "RUH", "SIL", "TYR"},
	"NAF": {"MAO", "TUN", "WES"},
	"NAO": {"CLY", "IRI", "LVP", "MAO", "NWG"},
	"NAP": {"APU", "ION", "ROM", "TYS"},
	"NTH": {"BEL", "DEN", "EDI", "ENG", "HEL", "HOL", "LON", "NWG", "NWY", "SKA", "YOR"},
	"NWG": {"BAR", "CLY", "EDI", "NAO", "NTH", "NWY"},
	"NWY": {"BAR", "FIN", "NTH", "NWG", "SKA", "STP/NC", "SWE"},
	"PAR": {"BRE", "BUR", "GAS", "PIC"},
	"PIC": {"BEL", "BRE", "BUR", "ENG", "PAR"},
	"PIE": {"LYO", "MAR", "TUS", "TYR", "VEN"},
	"POR": {"MAO", "SPA/NC", "SPA/SC"},
	"PRU": {"BAL", "BER", "LVN", "SIL", "WAR"},
	"ROM": {"APU", "NAP", "TUS", "TYS", "VEN"},
	"RUH": {"BEL", "BUR", "HOL", "KIE", "MUN"},
	"RUM": {"BLA", "BUD", "BUL", "GAL", "SER", "SEV", "UKR"},
	"SER": {"ALB", "BUD", "BUL", "GRE", "RUM", "TRI"},
	"SEV": {"ARM", "BLA", "MOS", "RUM", "UKR"},
	"SIL": {"BER", "BOH", "GAL", "MUN", "PRU", "WAR"},
	"SKA": {"DEN", "NTH", "NWY", "SWE"},
	"SMY": {"AEG", "ARM", "CON", "EAS", "SYR"},
	"SPA": {"GAS", "MAR", "MAO", "POR", "LYO", "WES"},
	"STP": {"BAR", "BOT", "FIN", "LVN", "MOS", "NWY"},
	"SWE": {"BAL", "BOT", "DEN", "FIN", "NWY", "SKA"},
	"SYR": {"ARM", "EAS", "SMY"},
	"TRI": {"ADR", "ALB", "BUD", "SER", "TYR", "VEN", "VIE"},
	"TUN": {"ION", "NAF", "TYS", "WES"},
	"TUS": {"LYO", "PIE", "ROM", "TYS", "VEN"},
	"TYR": {"BOH", "MUN", "PIE", "TRI", "VEN", "VIE"},
	"TYS": {"ION", "LYO", "NAP", "ROM", "TUN", "TUS", "WES"},
	"UKR": {"GAL", "MOS", "RUM", "SEV", "WAR"},
	"VEN": {"ADR", "APU", "PIE", "ROM", "TRI", "TUS", "TYR"},
	"VIE": {"BOH", "BUD", "GAL", "TRI", "TYR"},
	"WAL": {"ENG", "IRI", "LON", "LVP", "YOR"},
	"WAR": {"GAL", "LVN", "MOS", "PRU", "SIL", "UKR"},
	"WES": {"LYO", "MAO", "NAF", "SPA/SC", "TUN", "TYS"},
	"YOR": {"EDI", "LON", "LVP", "NTH", "WAL"},
}

// isAdjacent checks if two territories are adjacent on the Diplomacy board.
func isAdjacent(a, b string) bool {
	a, b = strings.ToUpper(a), strings.ToUpper(b)
	for _, n := range adjacency[a] {
		// Handle coast variants (BUL/SC, STP/NC, etc.)
		if n == b || strings.HasPrefix(n, b+"/") || strings.HasPrefix(b, n+"/") || strings.Split(n, "/")[0] == b {
			return true
		}
	}
	// Also check reverse (in case of asymmetric coast entries)
	for _, n := range adjacency[b] {
		if n == a || strings.HasPrefix(n, a+"/") || strings.HasPrefix(a, n+"/") || strings.Split(n, "/")[0] == a {
			return true
		}
	}
	return false
}

// countAdjacentThreats counts how many of an enemy's units are adjacent to a power's supply centers.
func countAdjacentThreats(myPower, theirPower string, territory map[string]territorialInfo) (threats int, threatenedCenters []string) {
	myCenters := make(map[string]bool)
	// Use current SC holdings, not just home centers
	for _, c := range territory[strings.ToUpper(myPower)].centers {
		myCenters[strings.ToUpper(c)] = true
	}
	// Also include home centers (always relevant)
	for _, c := range homeCenters[strings.ToUpper(myPower)] {
		myCenters[c] = true
	}

	theirUnits := territory[strings.ToUpper(theirPower)].units
	for _, unit := range theirUnits {
		parts := strings.Fields(unit)
		if len(parts) < 2 {
			continue
		}
		unitLoc := strings.ToUpper(parts[1])
		for center := range myCenters {
			if isAdjacent(unitLoc, center) {
				threats++
				threatenedCenters = append(threatenedCenters, fmt.Sprintf("%s from %s", center, unit))
				break // count each unit once
			}
		}
	}
	sort.Strings(threatenedCenters)
	return
}

// --- Territory mapping (Diplomacy supply centers by home power) ---

var homeCenters = map[string][]string{
	"AUSTRIA": {"VIE", "BUD", "TRI"},
	"ENGLAND": {"LON", "LVP", "EDI"},
	"FRANCE":  {"PAR", "MAR", "BRE"},
	"GERMANY": {"BER", "MUN", "KIE"},
	"ITALY":   {"ROM", "NAP", "VEN"},
	"RUSSIA":  {"MOS", "WAR", "STP", "SEV"},
	"TURKEY":  {"CON", "ANK", "SMY"},
}

// ============================================================
// V12: Stab Risk Scoring
// ============================================================

// stabRisk assesses how vulnerable a power is to betrayal by a specific partner.
type stabRisk struct {
	From    string
	Risk    string // "low", "medium", "high", "critical"
	Reasons []string
}

// computeStabRisks evaluates stab vulnerability for each bilateral relationship.
func computeStabRisks(power string, results []scopeResult, trust *trustTracker, balance powerBalance, territory map[string]territorialInfo) []stabRisk {
	powerUpper := strings.ToUpper(power)
	powerLower := strings.ToLower(power)
	var risks []stabRisk

	for _, r := range results {
		if r.scope.scopeTag != "bilateral" {
			continue
		}
		_, ok := r.contexts[powerLower]
		if !ok {
			continue
		}
		other := ""
		for _, p := range r.scope.powers {
			if !strings.EqualFold(p, power) {
				other = strings.ToUpper(p)
			}
		}
		if other == "" {
			continue
		}

		var reasons []string
		riskScore := 0

		// Factor 1: Partner's SC trajectory — are they approaching solo range?
		otherSCs := balance.current[other]
		if otherSCs >= 10 {
			reasons = append(reasons, fmt.Sprintf("%s has %d SCs — approaching solo range", other, otherSCs))
			riskScore += 3
		} else if otherSCs >= 7 {
			reasons = append(reasons, fmt.Sprintf("%s has %d SCs — strong and growing", other, otherSCs))
			riskScore++
		}

		// Factor 2: SC differential — partner much stronger?
		mySCs := balance.current[powerUpper]
		if otherSCs-mySCs >= 3 {
			reasons = append(reasons, fmt.Sprintf("SC imbalance: %s has %d more SCs", other, otherSCs-mySCs))
			riskScore += 2
		}

		// Factor 3: Broken promises from partner
		recReverse := trust.get(other, power)
		if recReverse.promisedSupports > 0 && len(recReverse.brokenPromises) > 0 {
			rate := float64(recReverse.honoredSupports) / float64(recReverse.promisedSupports)
			if rate < 0.5 {
				reasons = append(reasons, fmt.Sprintf("%s has broken %.0f%% of promises to you", other, (1-rate)*100))
				riskScore += 2
			}
		}

		// Factor 4: Partner units adjacent to your supply centers (board-aware)
		threats, threatenedCenters := countAdjacentThreats(power, other, territory)
		if threats >= 3 {
			reasons = append(reasons, fmt.Sprintf("%s has %d units threatening your centers: %s", other, threats, strings.Join(threatenedCenters, ", ")))
			riskScore += 3
		} else if threats >= 1 {
			reasons = append(reasons, fmt.Sprintf("%s has units threatening: %s", other, strings.Join(threatenedCenters, ", ")))
			riskScore += threats
		}

		// Factor 5: Partner units IN your home centers
		for _, c := range homeCenters[powerUpper] {
			for _, unit := range territory[other].units {
				parts := strings.Fields(unit)
				if len(parts) >= 2 && strings.EqualFold(parts[1], c) {
					reasons = append(reasons, fmt.Sprintf("%s occupies your home center %s", other, c))
					riskScore += 4 // very high risk
				}
			}
		}

		risk := "low"
		switch {
		case riskScore >= 6:
			risk = "critical"
		case riskScore >= 4:
			risk = "high"
		case riskScore >= 2:
			risk = "medium"
		}

		if riskScore > 0 {
			risks = append(risks, stabRisk{From: other, Risk: risk, Reasons: reasons})
		}
	}

	// Sort by severity
	riskOrder := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	sort.Slice(risks, func(i, j int) bool {
		return riskOrder[risks[i].Risk] < riskOrder[risks[j].Risk]
	})
	return risks
}
