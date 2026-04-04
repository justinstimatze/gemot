// diplomacy analyzes AI Diplomacy game messages through gemot's deliberation pipeline.
//
// Multi-scope architecture with scope-appropriate deliberation types:
//   - 1 global deliberation (assembly — public broadcasts, all 7 powers)
//   - N bilateral deliberations (negotiation — private pair, ZOPA/BATNA analysis)
//   - M alliance deliberations (consensus — mutual allies, internal coordination)
//
// Alliance detection via cross-power support orders and optional --alliances flag.
// Each power's briefing synthesizes intelligence from all deliberations they're party to.
//
// Usage:
//
//	go run scripts/diplomacy/main.go --game /path/to/lmvsgame.json --year 1 --output /path/to/briefings
//	go run scripts/diplomacy/main.go --game game.json --year 3 --output out/ --alliances "ENGLAND+FRANCE+RUSSIA,AUSTRIA+TURKEY"
//
// Requires: GEMOT_LIVE_URL and GEMOT_API_SECRET env vars
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var powers = []string{"AUSTRIA", "ENGLAND", "FRANCE", "GERMANY", "ITALY", "RUSSIA", "TURKEY"}

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

// GameState is the top-level lmvsgame.json structure.
type GameState struct {
	Phases []Phase `json:"phases"`
}

// Phase represents a single game phase with messages and orders.
type Phase struct {
	Name     string         `json:"name"`
	Messages []Message      `json:"messages"`
	Orders   map[string][]any `json:"orders,omitempty"`
	State    PhaseState     `json:"state,omitempty"`
}

// PhaseState holds the game state at the end of a phase.
type PhaseState struct {
	Units   map[string][]string `json:"units,omitempty"`   // power -> ["A VIE", "F TRI", ...]
	Centers map[string][]string `json:"centers,omitempty"` // power -> ["VIE", "TRI", ...]
}

// Message is a diplomatic message between powers.
type Message struct {
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Content   string `json:"message"`
	Phase     string `json:"phase"`
}

// scope describes a deliberation at a particular scope level.
type scope struct {
	name      string   // e.g., "global", "ENGLAND-FRANCE", "ENGLAND-FRANCE-RUSSIA"
	scopeTag  string   // "global", "bilateral", "alliance"
	template  string   // gemot template: "assembly", "negotiation", "consensus"
	delibType string   // gemot type: "reasoning", "knowledge", "negotiation", "policy"
	powers    []string // which powers participate
	messages  []Message
}

// scopeResult holds the analysis context for each power in a deliberation.
type scopeResult struct {
	scope       scope
	contexts    map[string]string // power (lowercase) -> context JSON
	commitments []commitment      // commitments from this deliberation
}

func main() {
	gameFile := flag.String("game", "", "Path to lmvsgame.json")
	year := flag.Int("year", 1, "Game year number (1-based, e.g. 1 = 1901)")
	outputDir := flag.String("output", "", "Output directory for briefing files")
	gemotURL := flag.String("url", "", "Gemot MCP URL (default: GEMOT_LIVE_URL env)")
	alliancesFlag := flag.String("alliances", "", "Explicit alliances: ENGLAND+FRANCE+RUSSIA,AUSTRIA+TURKEY (comma-separated groups)")
	stateFile := flag.String("state", "", "State file for persistent deliberation IDs across years (JSON)")
	experiment := flag.String("experiment", "", "Experiment name (used as group_id to link deliberations for visualization)")
	flag.Parse()

	if *gameFile == "" || *outputDir == "" {
		fmt.Fprintf(os.Stderr, "Usage: diplomacy --game <path> --year <N> --output <dir>\n")
		os.Exit(1)
	}

	url := *gemotURL
	if url == "" {
		url = envOr("GEMOT_LIVE_URL", "https://gemot.dev/mcp")
	}
	secret := os.Getenv("GEMOT_API_SECRET")
	if secret == "" {
		if b, err := os.ReadFile(".env"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "GEMOT_API_SECRET=") {
					secret = strings.TrimPrefix(line, "GEMOT_API_SECRET=")
				}
			}
		}
	}
	if secret == "" {
		fatal(fmt.Errorf("GEMOT_API_SECRET not set"), "config")
	}

	// Load game state
	data, err := os.ReadFile(*gameFile)
	fatal(err, "reading game file")

	var game GameState
	fatal(json.Unmarshal(data, &game), "parsing game JSON")

	// Determine phases for this year
	gameYear := 1900 + *year
	targetPhases := map[string]bool{
		fmt.Sprintf("S%dM", gameYear): true,
		fmt.Sprintf("F%dM", gameYear): true,
	}

	// Collect all messages for this year
	var yearMessages []Message
	for _, phase := range game.Phases {
		if !targetPhases[phase.Name] {
			continue
		}
		yearMessages = append(yearMessages, phase.Messages...)
	}

	if len(yearMessages) == 0 {
		fmt.Fprintf(os.Stderr, "No messages for year %d\n", *year)
		os.Exit(0)
	}

	// Detect alliances: explicit flag, or inferred from cross-power support orders
	alliances := parseAllianceFlag(*alliancesFlag)
	if len(alliances) == 0 {
		alliances = detectAlliancesFromOrders(game, gameYear)
	}

	// Categorize messages by scope
	scopes := buildScopes(yearMessages, alliances, *year)
	fmt.Fprintf(os.Stderr, "Year %d: %d messages → %d scopes (%d global, %d bilateral, %d alliance)\n",
		*year, len(yearMessages), len(scopes),
		countByTag(scopes, "global"), countByTag(scopes, "bilateral"), countByTag(scopes, "alliance"))
	for _, a := range alliances {
		fmt.Fprintf(os.Stderr, "  Alliance detected: %s\n", strings.Join(a, "+"))
	}

	// Load persistent deliberation state (reuse deliberations across years)
	state := loadState(*stateFile)

	os.MkdirAll(*outputDir, 0755) //nolint:errcheck
	ctx := context.Background()

	// Analyze all scopes in parallel, reusing existing deliberations where possible
	results := analyzeScopes(ctx, scopes, url, secret, *year, state, *experiment)

	// Save updated state
	saveState(*stateFile, state)

	// Extract power balance for briefing headers
	balance := extractPowerBalance(&game, *year)

	// Build trust and territory data for enriched briefings
	promises := extractPromises(yearMessages)
	trust := checkPromiseFollowThrough(&game, *year, promises)
	territory := buildTerritorialContext(&game, *year)

	// Synthesize per-power briefings from all relevant scopes
	var wg sync.WaitGroup
	for _, power := range powers {
		wg.Add(1)
		go func(power string) {
			defer wg.Done()
			briefing := synthesizeBriefing(power, *year, results, balance, trust, territory, results)
			if briefing == "" {
				fmt.Fprintf(os.Stderr, "  %s: no data for briefing\n", power)
				return
			}
			outFile := filepath.Join(*outputDir, fmt.Sprintf("%s_briefing.txt", strings.ToLower(power)))
			if err := os.WriteFile(outFile, []byte(briefing), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "  %s: ERROR writing briefing: %v\n", power, err)
				return
			}
			fmt.Fprintf(os.Stderr, "  %s: wrote %s\n", power, outFile)
		}(power)
	}
	wg.Wait()

	// Print metrics
	maxSCs, minSCs := 0, 99
	for _, scs := range balance.current {
		if scs > maxSCs {
			maxSCs = scs
		}
		if scs < minSCs {
			minSCs = scs
		}
	}
	fmt.Fprintf(os.Stderr, "Metrics: spread=%d gini=%.2f survival=%d/7\n",
		maxSCs-minSCs, computeGini(balance.current), computeSurvivalCount(balance.current))

	fmt.Fprintf(os.Stderr, "Done. Briefings written to %s\n", *outputDir)
}

// buildScopes categorizes messages into global, bilateral, and alliance scopes.
// Each scope gets the appropriate gemot deliberation template.
func buildScopes(messages []Message, alliances [][]string, year int) []scope {
	var globalMsgs []Message
	bilateral := make(map[string][]Message) // "AUSTRIA-ENGLAND" -> messages

	for _, msg := range messages {
		if strings.ToUpper(msg.Recipient) == "GLOBAL" {
			globalMsgs = append(globalMsgs, msg)
		} else {
			key := pairKey(msg.Sender, msg.Recipient)
			bilateral[key] = append(bilateral[key], msg)
		}
	}

	var scopes []scope

	// Global scope → policy type + assembly template
	// Always create the global assembly even without explicit GLOBAL messages —
	// it provides the 7-power deliberation for cross-cutting analysis.
	scopes = append(scopes, scope{
		name:      "global",
		scopeTag:  "global",
		template:  "assembly",
		delibType: "policy",
		powers:    powers,
		messages:  globalMsgs, // may be empty
	})

	// Bilateral scopes → negotiation type + negotiation template
	for key, msgs := range bilateral {
		parts := strings.SplitN(key, "-", 2)
		scopes = append(scopes, scope{
			name:     key,
			scopeTag: "bilateral",
			template: "negotiation",
			delibType: "negotiation",
			powers:   parts,
			messages: msgs,
		})
	}

	// Alliance scopes → negotiation type + consensus template
	for _, alliance := range alliances {
		var msgs []Message
		for i := 0; i < len(alliance); i++ {
			for j := i + 1; j < len(alliance); j++ {
				key := pairKey(alliance[i], alliance[j])
				msgs = append(msgs, bilateral[key]...)
			}
		}
		if len(msgs) > 0 {
			scopes = append(scopes, scope{
				name:     strings.Join(alliance, "+"),
				scopeTag: "alliance",
				template: "consensus",
				delibType: "negotiation",
				powers:   alliance,
				messages: msgs,
			})
		}
	}

	return scopes
}

// pairKey returns a canonical key for a pair of powers (sorted alphabetically).
func pairKey(a, b string) string {
	a, b = strings.ToUpper(a), strings.ToUpper(b)
	if a > b {
		a, b = b, a
	}
	return a + "-" + b
}


// parseAllianceFlag parses "ENGLAND+FRANCE+RUSSIA,AUSTRIA+TURKEY" into alliance groups.
func parseAllianceFlag(flag string) [][]string {
	if flag == "" {
		return nil
	}
	var alliances [][]string
	for _, group := range strings.Split(flag, ",") {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		members := strings.Split(group, "+")
		for i := range members {
			members[i] = strings.ToUpper(strings.TrimSpace(members[i]))
		}
		if len(members) >= 2 {
			sort.Strings(members)
			alliances = append(alliances, members)
		}
	}
	return alliances
}

// detectAlliancesFromOrders finds alliances by detecting cross-power support orders.
// If Power A orders a unit to support Power B's unit, that's an alliance signal.
// Mutual support (A supports B AND B supports A) = strong alliance.
func detectAlliancesFromOrders(game GameState, gameYear int) [][]string {
	// Look at current year plus previous year — alliances form across years,
	// not just within a single year's movement phases.
	targetPhases := map[string]bool{
		fmt.Sprintf("S%dM", gameYear):   true,
		fmt.Sprintf("F%dM", gameYear):   true,
		fmt.Sprintf("S%dM", gameYear-1): true,
		fmt.Sprintf("F%dM", gameYear-1): true,
	}

	// Build unit ownership map from game state
	unitOwner := make(map[string]string) // "A VIE" -> "AUSTRIA"
	for _, phase := range game.Phases {
		if !targetPhases[phase.Name] {
			continue
		}
		if phase.State.Units == nil {
			continue
		}
		for power, units := range phase.State.Units {
			for _, unit := range units {
				unitOwner[unit] = strings.ToUpper(power)
			}
		}
	}

	// Find cross-power support orders
	supportPairs := make(map[string]bool) // "ENGLAND->FRANCE" = England supports French unit
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
				// Support orders contain " S " (e.g., "F NTH S A YOR - WAL")
				if !strings.Contains(orderStr, " S ") {
					continue
				}
				// Parse the supported unit: everything after " S "
				parts := strings.SplitN(orderStr, " S ", 2)
				if len(parts) != 2 {
					continue
				}
				// The supported unit is the first token(s) of the support target
				// e.g., "A YOR - WAL" → supported unit is "A YOR"
				supportTarget := strings.TrimSpace(parts[1])
				supportedUnit := supportTarget
				if dashIdx := strings.Index(supportTarget, " - "); dashIdx != -1 {
					supportedUnit = supportTarget[:dashIdx]
				}
				supportedUnit = strings.TrimSpace(supportedUnit)

				// Check who owns the supported unit
				if owner, ok := unitOwner[supportedUnit]; ok && owner != power {
					key := power + "->" + owner
					supportPairs[key] = true
				}
			}
		}
	}

	// Find mutual support pairs → alliances
	mutualPairs := make(map[string]bool)
	for pair := range supportPairs {
		parts := strings.SplitN(pair, "->", 2)
		reverse := parts[1] + "->" + parts[0]
		if supportPairs[reverse] {
			key := pairKey(parts[0], parts[1])
			mutualPairs[key] = true
		}
	}

	// Build alliance groups from mutual support graph
	// For now: each mutual pair is a 2-power alliance.
	// Also check for 3-power alliances (triangles in mutual support graph).
	edges := make(map[string]map[string]bool)
	for key := range mutualPairs {
		parts := strings.SplitN(key, "-", 2)
		if edges[parts[0]] == nil {
			edges[parts[0]] = make(map[string]bool)
		}
		if edges[parts[1]] == nil {
			edges[parts[1]] = make(map[string]bool)
		}
		edges[parts[0]][parts[1]] = true
		edges[parts[1]][parts[0]] = true
	}

	// Find triangles (3-power alliances)
	var alliances [][]string
	seen := make(map[string]bool)
	sortedPowers := make([]string, 0, len(edges))
	for p := range edges {
		sortedPowers = append(sortedPowers, p)
	}
	sort.Strings(sortedPowers)

	for _, a := range sortedPowers {
		for _, b := range sortedPowers {
			if b <= a || !edges[a][b] {
				continue
			}
			for _, c := range sortedPowers {
				if c <= b || !edges[b][c] || !edges[a][c] {
					continue
				}
				key := a + "+" + b + "+" + c
				if !seen[key] {
					seen[key] = true
					alliances = append(alliances, []string{a, b, c})
				}
			}
		}
	}

	// Also include 2-power mutual support pairs not part of a triangle
	for key := range mutualPairs {
		parts := strings.SplitN(key, "-", 2)
		inTriangle := false
		for _, a := range alliances {
			if containsStr(a, parts[0]) && containsStr(a, parts[1]) {
				inTriangle = true
				break
			}
		}
		if !inTriangle {
			alliances = append(alliances, []string{parts[0], parts[1]})
		}
	}

	return alliances
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
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

// territoryOwner maps a supply center to its home power.
func territoryOwner(sc string) string {
	sc = strings.ToUpper(sc)
	for power, centers := range homeCenters {
		for _, c := range centers {
			if c == sc {
				return power
			}
		}
	}
	return ""
}

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

// extractPromises parses diplomatic messages for support promises.
// Returns: promiser (uppercase) -> beneficiary (uppercase) -> []promise descriptions
func extractPromises(messages []Message) map[string]map[string][]string {
	promises := make(map[string]map[string][]string)

	supportPatterns := []string{
		"support", "i will support", "i'll order", "agreed to support",
		"i'll support", "will support your", "promise to support",
		"order support", "i can support", "my support",
	}

	for _, msg := range messages {
		if strings.ToUpper(msg.Recipient) == "GLOBAL" {
			continue
		}
		content := strings.ToLower(msg.Content)
		hasPromise := false
		for _, pat := range supportPatterns {
			if strings.Contains(content, pat) {
				hasPromise = true
				break
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
		// Extract a brief description from the message
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

// --- Territorial Context (2c) ---

type territorialInfo struct {
	units   []string // "A VIE", "F TRI"
	centers []string // "VIE", "TRI"
}

func buildTerritorialContext(game *GameState, year int) map[string]territorialInfo {
	gameYear := 1900 + year
	result := make(map[string]territorialInfo)

	// Walk phases backwards to find the latest state for this year
	for i := len(game.Phases) - 1; i >= 0; i-- {
		p := game.Phases[i]
		phaseYear := 0
		for j := 0; j < len(p.Name); j++ {
			if p.Name[j] >= '0' && p.Name[j] <= '9' {
				fmt.Sscanf(p.Name[j:], "%d", &phaseYear)
				break
			}
		}
		if phaseYear != gameYear {
			continue
		}
		if len(p.State.Units) > 0 && len(result) == 0 {
			for power, units := range p.State.Units {
				power = strings.ToUpper(power)
				info := result[power]
				info.units = append(info.units, units...)
				if centers, ok := p.State.Centers[power]; ok {
					info.centers = append(info.centers, centers...)
				}
				result[power] = info
			}
			break
		}
	}

	return result
}

func formatTerritorialReality(myInfo, theirInfo territorialInfo, myPower, theirPower string) string {
	var parts []string
	if len(myInfo.units) > 0 {
		parts = append(parts, fmt.Sprintf("%s units: %s", myPower, strings.Join(myInfo.units, ", ")))
	}
	if len(myInfo.centers) > 0 {
		parts = append(parts, fmt.Sprintf("%s centers: %s", myPower, strings.Join(myInfo.centers, ", ")))
	}
	if len(theirInfo.units) > 0 {
		parts = append(parts, fmt.Sprintf("%s units: %s", theirPower, strings.Join(theirInfo.units, ", ")))
	}
	if len(theirInfo.centers) > 0 {
		parts = append(parts, fmt.Sprintf("%s centers: %s", theirPower, strings.Join(theirInfo.centers, ", ")))
	}
	return strings.Join(parts, "; ")
}

// --- Commitments Integration (2d) ---

type commitment struct {
	AgentID     string `json:"agent_id"`
	Statement   string `json:"statement"`
	Conditional string `json:"conditional"`
	Status      string `json:"status"`
}

// --- Elimination Warnings (2b) ---

func detectEliminationRisk(balance powerBalance, allResults []scopeResult, power string) []string {
	var warnings []string
	for p, scs := range balance.current {
		if scs > 1 {
			continue
		}
		label := "ELIMINATED"
		if scs == 1 {
			label = "1 SC remaining"
		}
		warnings = append(warnings, fmt.Sprintf("%s is at risk of elimination (%s)", p, label))

		// Check if any bilateral proposals reference this at-risk power's territories
		for _, r := range allResults {
			if r.scope.scopeTag != "bilateral" {
				continue
			}
			// Only check bilaterals involving the current briefing power
			involves := false
			for _, rp := range r.scope.powers {
				if strings.ToUpper(rp) == strings.ToUpper(power) {
					involves = true
					break
				}
			}
			if !involves {
				continue
			}
			for _, ctxJSON := range r.contexts {
				var ac agentContext
				mustParseSoft(ctxJSON, &ac)
				for _, center := range homeCenters[p] {
					if strings.Contains(strings.ToUpper(ac.CompromiseProposal), center) {
						warnings = append(warnings, fmt.Sprintf("  Your negotiations reference %s's territory %s — they may have no leverage to resist", p, center))
					}
					for _, bs := range ac.BridgingStatements {
						if strings.Contains(strings.ToUpper(bs.Content), center) {
							warnings = append(warnings, fmt.Sprintf("  Shared ground with counterpart references %s's territory %s", p, center))
						}
					}
				}
			}
		}
	}
	sort.Strings(warnings)
	return warnings
}

// --- Coalition Threats (2e) ---

func detectCoalitionThreats(power string, allResults []scopeResult, balance powerBalance) []string {
	power = strings.ToUpper(power)
	var warnings []string

	// Get this power's home centers
	myCenters := homeCenters[power]

	for _, r := range allResults {
		if r.scope.scopeTag != "bilateral" {
			continue
		}
		// Skip bilaterals that include this power — we want OTHER powers' bilaterals
		involves := false
		for _, rp := range r.scope.powers {
			if strings.ToUpper(rp) == power {
				involves = true
				break
			}
		}
		if involves {
			continue
		}

		// Check if proposals/bridging in this bilateral reference our territories
		for _, ctxJSON := range r.contexts {
			var ac agentContext
			mustParseSoft(ctxJSON, &ac)

			for _, center := range myCenters {
				if strings.Contains(strings.ToUpper(ac.CompromiseProposal), center) {
					warnings = append(warnings, fmt.Sprintf("%s share agreement referencing your territory %s", r.scope.name, center))
				}
				for _, bs := range ac.BridgingStatements {
					if strings.Contains(strings.ToUpper(bs.Content), center) {
						warnings = append(warnings, fmt.Sprintf("%s have shared ground affecting your territory %s", r.scope.name, center))
					}
				}
			}
			// Also check if they mention this power by name in proposals
			if strings.Contains(strings.ToUpper(ac.CompromiseProposal), power) {
				warnings = append(warnings, fmt.Sprintf("%s have a compromise proposal that mentions %s", r.scope.name, power))
			}
		}
	}

	// Deduplicate
	seen := make(map[string]bool)
	var unique []string
	for _, w := range warnings {
		if !seen[w] {
			seen[w] = true
			unique = append(unique, w)
		}
	}
	return unique
}

// --- Metrics (2e-iv) ---

func computeGini(scCounts map[string]int) float64 {
	n := len(scCounts)
	if n == 0 {
		return 0
	}
	values := make([]float64, 0, n)
	sum := 0.0
	for _, v := range scCounts {
		values = append(values, float64(v))
		sum += float64(v)
	}
	if sum == 0 {
		return 0
	}
	sort.Float64s(values)
	var numerator float64
	for _, vi := range values {
		for _, vj := range values {
			numerator += math.Abs(vi - vj)
		}
	}
	return numerator / (2 * float64(n) * sum)
}

func computeSurvivalCount(scCounts map[string]int) int {
	count := 0
	for _, scs := range scCounts {
		if scs > 0 {
			count++
		}
	}
	return count
}

// persistentState tracks deliberation IDs across years for multi-round deliberations.
type persistentState struct {
	mu    sync.Mutex
	IDs   map[string]string `json:"deliberation_ids"` // scope name -> deliberation ID
}

func loadState(path string) *persistentState {
	state := &persistentState{IDs: make(map[string]string)}
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return state // file doesn't exist yet, that's fine
	}
	json.Unmarshal(data, state) //nolint:errcheck
	if state.IDs == nil {
		state.IDs = make(map[string]string)
	}
	return state
}

func saveState(path string, state *persistentState) {
	if path == "" {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0644) //nolint:errcheck
}

// pendingPosition holds a position ready for submission, collected before interleaving.
type pendingPosition struct {
	scopeName      string
	deliberationID string
	agentID        string
	content        string
	interests      string
}

// analyzeScopes processes all scopes through a 3-stage pipeline:
//   - Stage A: Create/reuse deliberations + forced acknowledgment (parallel)
//   - Stage B: Collect, interleave, and submit positions across scopes (sequential)
//   - Stage C: Analyze and poll for results (parallel)
func analyzeScopes(ctx context.Context, scopes []scope, url, secret string, year int, state *persistentState, experimentID string) []scopeResult {
	// ========================================
	// Stage A: Create deliberations (parallel)
	// ========================================
	type stageAResult struct {
		sc             scope
		deliberationID string
		err            error
	}
	stageAResults := make([]stageAResult, len(scopes))

	var wgA sync.WaitGroup
	for i, sc := range scopes {
		wgA.Add(1)
		stagger := time.Duration(i) * 2 * time.Second
		go func(idx int, sc scope, delay time.Duration) {
			defer wgA.Done()
			if delay > 0 {
				time.Sleep(delay)
			}

			fmt.Fprintf(os.Stderr, "  [%s] %s: %d messages, %d powers\n",
				sc.scopeTag, sc.name, len(sc.messages), len(sc.powers))

			var deliberationID string
			var lastErr error
			for attempt := 1; attempt <= 3; attempt++ {
				if attempt > 1 {
					fmt.Fprintf(os.Stderr, "  [%s] %s: retry %d/3 (stage A)...\n", sc.scopeTag, sc.name, attempt)
					time.Sleep(10 * time.Second)
				}

				session, err := connect(ctx, url, secret)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  [%s] %s: ERROR connecting: %v\n", sc.scopeTag, sc.name, err)
					lastErr = err
					continue
				}

				deliberationID, err = createOrReuseDeliberation(ctx, session, sc, year, state, experimentID)
				session.Close() //nolint:errcheck
				if err == nil {
					lastErr = nil
					break
				}
				fmt.Fprintf(os.Stderr, "  [%s] %s: ERROR (stage A): %v\n", sc.scopeTag, sc.name, err)
				lastErr = err
			}

			stageAResults[idx] = stageAResult{sc: sc, deliberationID: deliberationID, err: lastErr}
			if lastErr != nil {
				fmt.Fprintf(os.Stderr, "  [%s] %s: FAILED (stage A): %v\n", sc.scopeTag, sc.name, lastErr)
			}
		}(i, sc, stagger)
	}
	wgA.Wait()

	// ========================================
	// Stage B: Interleave and submit positions (sequential)
	// ========================================

	// Collect positions from all scopes, grouped by scope name.
	grouped := make(map[string][]pendingPosition)
	activeScopesByName := make(map[string]stageAResult)
	for _, ar := range stageAResults {
		if ar.err != nil {
			continue
		}
		activeScopesByName[ar.sc.name] = ar
		if len(ar.sc.messages) == 0 {
			continue // no positions to submit
		}
		grouped[ar.sc.name] = collectPositions(ar.sc, ar.deliberationID)
	}

	interleaved := interleaveByScope(grouped)

	if len(interleaved) > 0 {
		// Single session for all position submissions.
		submitSession, err := connect(ctx, url, secret)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Stage B: ERROR connecting for submissions: %v\n", err)
		} else {
			for i, pp := range interleaved {
				if i > 0 {
					time.Sleep(200 * time.Millisecond)
				}
				callToolSoft(ctx, submitSession, "submit_position", map[string]any{
					"deliberation_id": pp.deliberationID,
					"agent_id":        pp.agentID,
					"content":         pp.content,
					"interests":       pp.interests,
				})
			}

			// Alliance voting (sequential, same session).
			for _, ar := range stageAResults {
				if ar.err != nil || ar.sc.scopeTag != "alliance" || len(ar.sc.messages) == 0 {
					continue
				}
				posJSON := callTool(ctx, submitSession, "get_positions", map[string]any{
					"deliberation_id": ar.deliberationID,
				})
				var positions []struct {
					ID      string `json:"position_id"`
					AgentID string `json:"agent_id"`
				}
				mustParse(posJSON, &positions)

				for _, pos := range positions {
					for _, p := range ar.sc.powers {
						voterAgent := strings.ToLower(p) + "-agent"
						if voterAgent == pos.AgentID {
							continue
						}
						callToolSoft(ctx, submitSession, "vote", map[string]any{
							"deliberation_id": ar.deliberationID,
							"agent_id":        voterAgent,
							"position_id":     pos.ID,
							"value":           1,
						})
					}
				}
			}

			submitSession.Close() //nolint:errcheck
		}
	}

	// ========================================
	// Stage C: Analyze and poll (parallel)
	// ========================================
	var mu sync.Mutex
	var wgC sync.WaitGroup
	var results []scopeResult

	for _, ar := range stageAResults {
		if ar.err != nil {
			continue
		}
		if len(ar.sc.messages) == 0 {
			// No messages — deliberation exists but nothing to analyze.
			mu.Lock()
			results = append(results, scopeResult{scope: ar.sc, contexts: make(map[string]string)})
			mu.Unlock()
			continue
		}

		wgC.Add(1)
		go func(sc scope, deliberationID string) {
			defer wgC.Done()

			var result *scopeResult
			var lastErr error
			for attempt := 1; attempt <= 3; attempt++ {
				if attempt > 1 {
					fmt.Fprintf(os.Stderr, "  [%s] %s: retry %d/3 (stage C)...\n", sc.scopeTag, sc.name, attempt)
					time.Sleep(10 * time.Second)
				}

				var err error
				result, err = analyzeAndPoll(ctx, url, secret, sc, deliberationID)
				if err == nil {
					lastErr = nil
					break
				}
				fmt.Fprintf(os.Stderr, "  [%s] %s: ERROR (stage C): %v\n", sc.scopeTag, sc.name, err)
				lastErr = err
			}

			if lastErr != nil {
				fmt.Fprintf(os.Stderr, "  [%s] %s: FAILED (stage C): %v\n", sc.scopeTag, sc.name, lastErr)
				return
			}

			mu.Lock()
			results = append(results, *result)
			mu.Unlock()
			fmt.Fprintf(os.Stderr, "  [%s] %s: complete (%d contexts)\n", sc.scopeTag, sc.name, len(result.contexts))
		}(ar.sc, ar.deliberationID)
	}

	wgC.Wait()
	return results
}

// createOrReuseDeliberation creates a new deliberation or reuses an existing one from state.
// It also handles forced acknowledgment for round 2+ deliberations.
// Returns the deliberation ID.
func createOrReuseDeliberation(ctx context.Context, session *sdkmcp.ClientSession, sc scope, year int, state *persistentState, experimentID string) (string, error) {
	// Check if we have a persistent deliberation for this scope
	state.mu.Lock()
	existingID := state.IDs[sc.name]
	state.mu.Unlock()

	var deliberationID string

	if existingID != "" {
		// Reuse existing deliberation — this year's messages become a new round
		deliberationID = existingID
		fmt.Fprintf(os.Stderr, "  [%s] %s: reusing deliberation %s (round %d)\n",
			sc.scopeTag, sc.name, deliberationID[:8], year)
	} else {
		// Create new deliberation
		var topic, desc string
		switch sc.scopeTag {
		case "global":
			topic = "Public diplomacy"
			desc = "Ongoing analysis of public broadcast messages between all powers. Each round represents one game year."
		case "bilateral":
			topic = fmt.Sprintf("%s bilateral negotiations", sc.name)
			desc = fmt.Sprintf("Ongoing private diplomatic negotiations between %s. Each round represents one game year.", strings.Join(sc.powers, " and "))
		case "alliance":
			topic = fmt.Sprintf("%s alliance coordination", sc.name)
			desc = fmt.Sprintf("Multi-party coordination within the %s alliance. Each round represents one game year.", sc.name)
		}

		createArgs := map[string]any{
			"topic":       topic,
			"description": desc,
			"type":        sc.delibType,
		}
		if experimentID != "" {
			createArgs["group_id"] = experimentID
		}
		createJSON := callTool(ctx, session, "create_deliberation", createArgs)

		var createResp struct {
			DeliberationID string `json:"deliberation_id"`
		}
		mustParse(createJSON, &createResp)
		deliberationID = createResp.DeliberationID

		// Set template for scope-appropriate rules and analysis framing
		if sc.template != "" {
			callToolSoft(ctx, session, "set_template", map[string]any{
				"deliberation_id": deliberationID,
				"template":        sc.template,
			})
		}

		// Save to persistent state
		state.mu.Lock()
		state.IDs[sc.name] = deliberationID
		state.mu.Unlock()

		fmt.Fprintf(os.Stderr, "  [%s] %s: created deliberation %s\n",
			sc.scopeTag, sc.name, deliberationID[:8])
	}

	// Satisfy forced acknowledgment: call get_context for all agents before submitting
	// (required for round 2+ deliberations — agents must review cruxes before contributing)
	if existingID != "" && year > 1 {
		for _, p := range sc.powers {
			agentID := strings.ToLower(p) + "-agent"
			callToolSoft(ctx, session, "get_context", map[string]any{
				"deliberation_id": deliberationID,
				"agent_id":        agentID,
			})
		}
	}

	return deliberationID, nil
}

// collectPositions builds the position list for a scope without submitting them.
func collectPositions(sc scope, deliberationID string) []pendingPosition {
	var positions []pendingPosition
	for _, msg := range sc.messages {
		sender := strings.ToUpper(msg.Sender)
		recipient := strings.ToUpper(msg.Recipient)
		positions = append(positions, pendingPosition{
			scopeName:      sc.name,
			deliberationID: deliberationID,
			agentID:        strings.ToLower(msg.Sender) + "-agent",
			content:        fmt.Sprintf("[%s → %s, %s] %s", sender, recipient, msg.Phase, msg.Content),
			interests:      fmt.Sprintf("%s's strategic objectives", strings.ToLower(msg.Sender)),
		})
	}
	return positions
}

// interleaveByScope round-robins positions across scopes: takes 1 from each scope queue
// in turn, repeating until all queues are empty. Preserves order within each scope.
func interleaveByScope(grouped map[string][]pendingPosition) []pendingPosition {
	// Stable iteration order: sort scope names.
	scopeNames := make([]string, 0, len(grouped))
	for name := range grouped {
		scopeNames = append(scopeNames, name)
	}
	sort.Strings(scopeNames)

	// Track current index per scope.
	indices := make(map[string]int, len(scopeNames))
	var result []pendingPosition

	for {
		advanced := false
		for _, name := range scopeNames {
			idx := indices[name]
			if idx < len(grouped[name]) {
				result = append(result, grouped[name][idx])
				indices[name] = idx + 1
				advanced = true
			}
		}
		if !advanced {
			break
		}
	}
	return result
}

// analyzeAndPoll triggers analysis for a scope and polls until results are available.
// Creates its own MCP session for parallel execution.
func analyzeAndPoll(ctx context.Context, url, secret string, sc scope, deliberationID string) (*scopeResult, error) {
	session, err := connect(ctx, url, secret)
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", err)
	}
	defer session.Close() //nolint:errcheck

	prefix := fmt.Sprintf("  [%s] %s:", sc.scopeTag, sc.name)
	fmt.Fprintf(os.Stderr, "%s analyzing...\n", prefix)
	callTool(ctx, session, "analyze", map[string]any{
		"deliberation_id": deliberationID,
	})

	// Poll for completion using first power's context
	firstPower := strings.ToLower(sc.powers[0]) + "-agent"
	time.Sleep(5 * time.Second)
	completed := false
	for i := 0; i < 200; i++ {
		time.Sleep(3 * time.Second)

		result := callToolSoft(ctx, session, "get_context", map[string]any{
			"deliberation_id": deliberationID,
			"agent_id":        firstPower,
		})

		if result == "" {
			session.Close() //nolint:errcheck
			var reconnErr error
			session, reconnErr = connect(ctx, url, secret)
			if reconnErr != nil {
				fmt.Fprintf(os.Stderr, "%s reconnect failed: %v\n", prefix, reconnErr)
			}
			if session != nil {
				statusJSON := callToolSoft(ctx, session, "get_deliberation", map[string]any{
					"deliberation_id": deliberationID,
				})
				if statusJSON != "" {
					var s struct {
						Status    string `json:"status"`
						SubStatus string `json:"sub_status"`
					}
					json.Unmarshal([]byte(strings.SplitN(statusJSON, "\n\n---\n", 2)[0]), &s)
					fmt.Fprintf(os.Stderr, "%s %s/%s\n", prefix, s.Status, s.SubStatus)
				}
			}
			continue
		}

		completed = true
		fmt.Fprintf(os.Stderr, "%s analysis complete\n", prefix)
		break
	}
	if !completed {
		return nil, fmt.Errorf("analysis did not produce results after 10 minutes")
	}

	// Collect contexts for each participating power
	contexts := make(map[string]string)
	for _, p := range sc.powers {
		agentID := strings.ToLower(p) + "-agent"
		result := callToolSoft(ctx, session, "get_context", map[string]any{
			"deliberation_id": deliberationID,
			"agent_id":        agentID,
		})
		if result != "" {
			contexts[strings.ToLower(p)] = result
		}
	}

	// Fetch commitments for this deliberation
	var commitments []commitment
	commJSON := callToolSoft(ctx, session, "get_commitments", map[string]any{
		"deliberation_id": deliberationID,
	})
	if commJSON != "" {
		mustParseSoft(commJSON, &commitments)
	}

	return &scopeResult{scope: sc, contexts: contexts, commitments: commitments}, nil
}

// synthesizeBriefing merges intelligence from all scopes into one briefing per power.
// powerBalance holds SC counts for the briefing header.
type powerBalance struct {
	current  map[string]int // power -> SC count this year
	previous map[string]int // power -> SC count last year (nil if year 1)
}

func extractPowerBalance(game *GameState, year int) powerBalance {
	pb := powerBalance{
		current:  make(map[string]int),
		previous: make(map[string]int),
	}
	gameYear := 1900 + year
	prevYear := gameYear - 1

	// Walk phases backwards to find the latest state for each year
	for i := len(game.Phases) - 1; i >= 0; i-- {
		p := game.Phases[i]
		if len(p.State.Centers) == 0 {
			continue
		}
		// Parse year from phase name (e.g., "W1904A" -> 1904)
		phaseYear := 0
		for j := 0; j < len(p.Name); j++ {
			if p.Name[j] >= '0' && p.Name[j] <= '9' {
				fmt.Sscanf(p.Name[j:], "%d", &phaseYear)
				break
			}
		}
		if phaseYear == gameYear && len(pb.current) == 0 {
			for power, centers := range p.State.Centers {
				pb.current[power] = len(centers)
			}
		}
		if phaseYear == prevYear && len(pb.previous) == 0 {
			for power, centers := range p.State.Centers {
				pb.previous[power] = len(centers)
			}
		}
		if len(pb.current) > 0 && len(pb.previous) > 0 {
			break
		}
	}
	return pb
}

func synthesizeBriefing(power string, year int, results []scopeResult, balance powerBalance, trust *trustTracker, territory map[string]territorialInfo, allResults []scopeResult) string {
	powerLower := strings.ToLower(power)
	powerUpper := strings.ToUpper(power)
	gameYear := 1900 + year

	// Collect relevant contexts by scope type
	var globalCtx *agentContext
	type bilateralResult struct {
		named       namedContext
		commitments []commitment
	}
	var bilateralCtxs []bilateralResult
	var allianceCtxs []namedContext

	for _, r := range results {
		ctxJSON, ok := r.contexts[powerLower]
		if !ok {
			continue
		}
		var ac agentContext
		mustParseSoft(ctxJSON, &ac)

		switch r.scope.scopeTag {
		case "global":
			globalCtx = &ac
		case "bilateral":
			other := ""
			for _, p := range r.scope.powers {
				if strings.ToLower(p) != powerLower {
					other = p
				}
			}
			bilateralCtxs = append(bilateralCtxs, bilateralResult{
				named:       namedContext{name: other, ctx: ac},
				commitments: r.commitments,
			})
		case "alliance":
			allianceCtxs = append(allianceCtxs, namedContext{name: r.scope.name, ctx: ac})
		}
	}

	if globalCtx == nil && len(bilateralCtxs) == 0 && len(allianceCtxs) == 0 {
		return ""
	}

	// Sort bilateral by number of cruxes (most informative first)
	sort.Slice(bilateralCtxs, func(i, j int) bool {
		return len(bilateralCtxs[i].named.ctx.RelevantCruxes) > len(bilateralCtxs[j].named.ctx.RelevantCruxes)
	})

	var b strings.Builder
	fmt.Fprintf(&b, "=== DIPLOMATIC INTELLIGENCE BRIEFING: %s — Year %d ===\n", power, gameYear)
	fmt.Fprintf(&b, "This briefing identifies opportunities for diplomatic cooperation.\n")
	fmt.Fprintf(&b, "Military conflict is costly — the analysis below highlights where\n")
	fmt.Fprintf(&b, "mutual agreements serve your interests better than unilateral action.\n\n")

	// ========================================
	// 1. POWER BALANCE (situational awareness)
	// ========================================

	if len(balance.current) > 0 {
		fmt.Fprintf(&b, "CURRENT POWER BALANCE:\n")

		type powerSC struct{ name string; scs int; delta int }
		var sorted []powerSC
		maxSCs := 0
		for p, scs := range balance.current {
			delta := 0
			if prev, ok := balance.previous[p]; ok {
				delta = scs - prev
			}
			sorted = append(sorted, powerSC{p, scs, delta})
			if scs > maxSCs {
				maxSCs = scs
			}
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].scs > sorted[j].scs })

		for _, ps := range sorted {
			marker := ""
			if ps.name == powerUpper {
				marker = " ← YOU"
			}
			trend := ""
			if ps.delta > 0 {
				trend = fmt.Sprintf(" (+%d)", ps.delta)
			} else if ps.delta < 0 {
				trend = fmt.Sprintf(" (%d)", ps.delta)
			}
			fmt.Fprintf(&b, "  %s: %d SCs%s%s\n", ps.name, ps.scs, trend, marker)
		}

		// Flag runaway leaders
		for _, ps := range sorted {
			if ps.name == powerUpper {
				continue
			}
			if ps.scs >= 7 {
				fmt.Fprintf(&b, "  ⚠ %s is approaching victory (18 SCs needed). Coalition response may be warranted.\n", ps.name)
			} else if ps.delta >= 2 {
				fmt.Fprintf(&b, "  ⚠ %s gained %d SCs this year — rapid expansion.\n", ps.name, ps.delta)
			}
		}
		fmt.Fprintln(&b)
	}

	// ========================================
	// 2. ELIMINATION WARNINGS (2b)
	// ========================================

	elimWarnings := detectEliminationRisk(balance, allResults, power)
	if len(elimWarnings) > 0 {
		fmt.Fprintf(&b, "ELIMINATION WARNINGS:\n")
		for _, w := range elimWarnings {
			fmt.Fprintf(&b, "  ⚠ %s\n", w)
		}
		fmt.Fprintln(&b)
	}

	// ========================================
	// 3. COALITION THREATS (2e)
	// ========================================

	coalitionThreats := detectCoalitionThreats(power, allResults, balance)
	if len(coalitionThreats) > 0 {
		fmt.Fprintf(&b, "COALITION THREATS:\n")
		fmt.Fprintf(&b, "Other powers' negotiations reference your interests:\n")
		for _, t := range coalitionThreats {
			fmt.Fprintf(&b, "  ⚠ %s\n", t)
		}
		fmt.Fprintln(&b)
	}

	// ========================================
	// 4. DECLINING POSITION WARNING (2e-ii)
	// ========================================

	if len(balance.previous) > 0 {
		prevSCs := balance.previous[powerUpper]
		curSCs := balance.current[powerUpper]
		if prevSCs-curSCs >= 2 {
			fmt.Fprintf(&b, "DECLINING POSITION WARNING:\n")
			fmt.Fprintf(&b, "  You lost %d SCs this year (%d → %d). Diplomatic realignment is urgent.\n",
				prevSCs-curSCs, prevSCs, curSCs)

			// Find most aligned powers from global alignment scores
			if globalCtx != nil && len(globalCtx.AlignmentScores) > 0 {
				var allies []string
				for _, a := range globalCtx.AlignmentScores {
					if a.AlignmentScore >= 0.4 {
						allies = append(allies, a.AgentID)
					}
				}
				if len(allies) > 0 {
					fmt.Fprintf(&b, "  Most aligned powers: %s\n", strings.Join(allies, ", "))
				}
			}

			// Suggest counter-alliance with powers who also lost SCs
			var fellowDecliners []string
			for p, prev := range balance.previous {
				if p == powerUpper {
					continue
				}
				cur := balance.current[p]
				if prev-cur >= 1 && cur > 0 {
					fellowDecliners = append(fellowDecliners, p)
				}
			}
			if len(fellowDecliners) > 0 {
				sort.Strings(fellowDecliners)
				fmt.Fprintf(&b, "  Potential counter-alliance partners (also losing ground): %s\n",
					strings.Join(fellowDecliners, ", "))
			}
			fmt.Fprintln(&b)
		}
	}

	// ========================================
	// 5. ESTABLISHED AGREEMENTS (existing)
	// ========================================

	var agreements []string
	var constitutionalRules []string
	for _, bc := range bilateralCtxs {
		for _, cs := range bc.named.ctx.ConsensusStatements {
			agreements = append(agreements, fmt.Sprintf("[with %s] %s", bc.named.name, cs.Content))
		}
		constitutionalRules = append(constitutionalRules, bc.named.ctx.ConstitutionalRules...)
	}
	if globalCtx != nil {
		for _, cs := range globalCtx.ConsensusStatements {
			agreements = append(agreements, fmt.Sprintf("[public] %s", cs.Content))
		}
		constitutionalRules = append(constitutionalRules, globalCtx.ConstitutionalRules...)
	}

	if len(agreements) > 0 || len(constitutionalRules) > 0 {
		fmt.Fprintf(&b, "ESTABLISHED AGREEMENTS:\n")
		if len(constitutionalRules) > 0 {
			fmt.Fprintf(&b, "Settled principles from prior rounds (breaking these damages trust):\n")
			for _, r := range constitutionalRules {
				fmt.Fprintf(&b, "  • %s\n", r)
			}
		}
		if len(agreements) > 0 {
			fmt.Fprintf(&b, "Positions with broad support:\n")
			for _, a := range agreements {
				fmt.Fprintf(&b, "  • %s\n", a)
			}
		}
		fmt.Fprintln(&b)
	}

	// ========================================
	// BILATERAL RELATIONS (restructured per 2f)
	// ========================================

	if len(bilateralCtxs) > 0 {
		fmt.Fprintf(&b, "BILATERAL RELATIONS:\n\n")
		for _, bc := range bilateralCtxs {
			other := bc.named.name
			otherUpper := strings.ToUpper(other)
			fmt.Fprintf(&b, "  === With %s ===\n", other)

			// 1. TRUST STATUS (2a)
			rec := trust.get(power, other)
			recReverse := trust.get(other, power)
			if rec.promisedSupports > 0 || recReverse.promisedSupports > 0 {
				fmt.Fprintf(&b, "  TRUST STATUS:\n")
				if rec.promisedSupports > 0 {
					fmt.Fprintf(&b, "    Your promises to %s: %d/%d honored\n",
						other, rec.honoredSupports, rec.promisedSupports)
					for _, bp := range rec.brokenPromises {
						fmt.Fprintf(&b, "      ⚠ %s\n", bp)
					}
				}
				if recReverse.promisedSupports > 0 {
					fmt.Fprintf(&b, "    %s's promises to you: %d/%d honored\n",
						other, recReverse.honoredSupports, recReverse.promisedSupports)
					for _, bp := range recReverse.brokenPromises {
						fmt.Fprintf(&b, "      ⚠ %s\n", bp)
					}
				}
			}

			// 2. ACTIVE COMMITMENTS (2d)
			if len(bc.commitments) > 0 {
				fmt.Fprintf(&b, "  ACTIVE COMMITMENTS:\n")
				for _, c := range bc.commitments {
					status := c.Status
					if status == "" {
						status = "active"
					}
					line := fmt.Sprintf("    [%s] %s: %s", status, c.AgentID, c.Statement)
					if c.Conditional != "" {
						line += fmt.Sprintf(" (if %s)", c.Conditional)
					}
					fmt.Fprintln(&b, line)
				}
			}

			// 3. SHARED GROUND (existing bridging)
			if len(bc.named.ctx.BridgingStatements) > 0 {
				for _, bs := range bc.named.ctx.BridgingStatements {
					content := truncateRunes(bs.Content, 500)
					fmt.Fprintf(&b, "  SHARED GROUND: %s (%.0f%% support)\n", content, bs.BridgingScore*100)
				}
			}

			// 4. AVAILABLE COMPROMISE (with 2e-iii no-elimination annotation)
			if bc.named.ctx.CompromiseProposal != "" {
				proposal := truncateRunes(bc.named.ctx.CompromiseProposal, 1000)
				fmt.Fprintf(&b, "  AVAILABLE COMPROMISE: %s\n", proposal)

				// 2e-iii: Minimum viability check
				proposalUpper := strings.ToUpper(bc.named.ctx.CompromiseProposal)
				for _, p := range powers {
					if strings.Contains(proposalUpper, p) {
						// Check if this references eliminating a power
						elimPhrases := []string{
							"ELIMINATE " + p, "ELIMINATION OF " + p,
							p + " ELIMINATED", "REMOVE " + p,
							"ALL OF " + p + "'S", "LOSS OF ALL",
						}
						for _, phrase := range elimPhrases {
							if strings.Contains(proposalUpper, phrase) {
								fmt.Fprintf(&b, "    ⚠ NOTE: This proposal may involve eliminating %s. Eliminated powers cannot honor future agreements.\n", p)
								break
							}
						}
					}
				}
			}

			// 5. TERRITORIAL REALITY (2c)
			myInfo := territory[powerUpper]
			theirInfo := territory[otherUpper]
			if len(myInfo.units) > 0 || len(theirInfo.units) > 0 {
				fmt.Fprintf(&b, "  TERRITORIAL REALITY: %s\n",
					formatTerritorialReality(myInfo, theirInfo, powerUpper, otherUpper))
			}

			// 6. AFFECTED PARTIES (2e — if this bilateral's proposals affect a third party)
			if bc.named.ctx.CompromiseProposal != "" || len(bc.named.ctx.BridgingStatements) > 0 {
				var affectedParties []string
				proposalUpper := strings.ToUpper(bc.named.ctx.CompromiseProposal)
				for _, p := range powers {
					if p == powerUpper || p == otherUpper {
						continue
					}
					affected := false
					for _, center := range homeCenters[p] {
						if strings.Contains(proposalUpper, center) {
							affected = true
							break
						}
						for _, bs := range bc.named.ctx.BridgingStatements {
							if strings.Contains(strings.ToUpper(bs.Content), center) {
								affected = true
								break
							}
						}
						if affected {
							break
						}
					}
					if affected {
						affectedParties = append(affectedParties, p)
					}
				}
				if len(affectedParties) > 0 {
					fmt.Fprintf(&b, "  AFFECTED PARTIES: Proposals in this bilateral reference territory of %s\n",
						strings.Join(affectedParties, ", "))
				}
			}

			// 7. ISSUES TO RESOLVE (existing cruxes)
			if len(bc.named.ctx.RelevantCruxes) > 0 {
				fmt.Fprintf(&b, "  ISSUES TO RESOLVE (resolving these creates mutual benefit):\n")
				for _, c := range bc.named.ctx.RelevantCruxes {
					fmt.Fprintf(&b, "    • %s\n", c.Claim)
					if c.Explanation != "" {
						expl := truncateRunes(c.Explanation, 500)
						fmt.Fprintf(&b, "      %s\n", expl)
					}
				}
			} else {
				fmt.Fprintf(&b, "  No unresolved issues — this relationship is in good standing.\n")
			}

			// 8. IF COOPERATION FAILS (existing failure scenarios)
			if len(bc.named.ctx.FailureScenarios) > 0 {
				fmt.Fprintf(&b, "  IF COOPERATION FAILS:\n")
				for _, f := range bc.named.ctx.FailureScenarios {
					fmt.Fprintf(&b, "    - %s\n", f)
				}
			}

			// 9. TRUST CONCERNS (existing rule violations)
			if len(bc.named.ctx.RuleViolations) > 0 {
				fmt.Fprintf(&b, "  TRUST CONCERNS: Prior agreements may have been violated:\n")
				for _, v := range bc.named.ctx.RuleViolations {
					fmt.Fprintf(&b, "    - %s\n", v)
				}
			}

			fmt.Fprintln(&b)
		}
	}

	// ========================================
	// PUBLIC LANDSCAPE (from global assembly)
	// ========================================

	if globalCtx != nil {
		if len(globalCtx.TopicSummaries) > 0 {
			fmt.Fprintf(&b, "PUBLIC DIPLOMATIC LANDSCAPE:\n")
			for i, ts := range globalCtx.TopicSummaries {
				fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, ts.Topic, ts.Summary)
			}
			fmt.Fprintln(&b)
		}

		if len(globalCtx.AlignmentScores) > 0 {
			fmt.Fprintf(&b, "ALIGNMENT WITH OTHER POWERS:\n")
			for _, a := range globalCtx.AlignmentScores {
				label := "OPPOSED"
				if a.AlignmentScore >= 0.67 {
					label = "STRONG ALLY"
				} else if a.AlignmentScore >= 0.4 {
					label = "PARTIAL ALLY"
				} else if a.AlignmentScore > 0 {
					label = "WEAK"
				}
				fmt.Fprintf(&b, "  %s: %.0f%% aligned (%d/%d issues) — %s\n",
					a.AgentID, a.AlignmentScore*100, a.AgreeCruxes, a.SharedCruxes, label)
			}
			fmt.Fprintln(&b)
		}

		if len(globalCtx.RelevantCruxes) > 0 {
			fmt.Fprintf(&b, "PUBLIC ISSUES UNDER DISCUSSION:\n")
			writeCruxes(&b, globalCtx.RelevantCruxes)
			fmt.Fprintln(&b)
		}
	}

	// ========================================
	// ALLIANCE COORDINATION
	// ========================================

	if len(allianceCtxs) > 0 {
		fmt.Fprintf(&b, "ALLIANCE COORDINATION:\n\n")
		for _, ac := range allianceCtxs {
			fmt.Fprintf(&b, "  === %s Alliance ===\n", ac.name)
			if ac.ctx.CompromiseProposal != "" {
				fmt.Fprintf(&b, "  Alliance proposal: %s\n", ac.ctx.CompromiseProposal)
			}
			if len(ac.ctx.AlignmentScores) > 0 {
				fmt.Fprintf(&b, "  Internal alignment:\n")
				for _, a := range ac.ctx.AlignmentScores {
					fmt.Fprintf(&b, "    %s: %.0f%% aligned\n", a.AgentID, a.AlignmentScore*100)
				}
			}
			if len(ac.ctx.RelevantCruxes) > 0 {
				fmt.Fprintf(&b, "  Issues to align on:\n")
				for _, c := range ac.ctx.RelevantCruxes {
					fmt.Fprintf(&b, "    • %s\n", c.Claim)
				}
			}
			fmt.Fprintln(&b)
		}
	}

	// ========================================
	// COOPERATIVE PATTERNS AND GUIDANCE
	// ========================================

	var norms []string
	for _, bc := range bilateralCtxs {
		norms = append(norms, bc.named.ctx.EmergentNorms...)
	}
	if globalCtx != nil {
		norms = append(norms, globalCtx.EmergentNorms...)
	}
	if len(norms) > 0 {
		fmt.Fprintf(&b, "EFFECTIVE DIPLOMATIC PATTERNS:\n")
		for _, n := range norms {
			fmt.Fprintf(&b, "  • %s\n", n)
		}
		fmt.Fprintln(&b)
	}

	var stratParts []string
	if globalCtx != nil && globalCtx.StrategicNudge != "" {
		stratParts = append(stratParts, globalCtx.StrategicNudge)
	}
	for _, bc := range bilateralCtxs {
		if bc.named.ctx.StrategicNudge != "" {
			stratParts = append(stratParts, fmt.Sprintf("[%s] %s", bc.named.name, bc.named.ctx.StrategicNudge))
		}
	}
	if len(stratParts) > 0 {
		fmt.Fprintf(&b, "DIPLOMATIC OPPORTUNITIES:\n")
		for _, s := range stratParts {
			fmt.Fprintf(&b, "  %s\n", s)
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintf(&b, "=== END BRIEFING ===\n")
	return b.String()
}

// writeCruxes renders cruxes in the standard format.
func writeCruxes(b *strings.Builder, cruxes []crux) {
	for i, c := range cruxes {
		fmt.Fprintf(b, "\n%d. %s\n", i+1, c.Claim)
		if c.Topic != "" {
			fmt.Fprintf(b, "   Topic: %s\n", c.Topic)
		}
		if c.Controversy > 0 {
			fmt.Fprintf(b, "   Controversy: %.0f%%\n", c.Controversy*100)
		}
		if len(c.AgreeAgents) > 0 {
			fmt.Fprintf(b, "   AGREE: %s\n", strings.Join(c.AgreeAgents, ", "))
		}
		if len(c.DisagreeAgents) > 0 {
			fmt.Fprintf(b, "   DISAGREE: %s\n", strings.Join(c.DisagreeAgents, ", "))
		}
		if len(c.NoClearPosition) > 0 {
			fmt.Fprintf(b, "   NO CLEAR POSITION: %s\n", strings.Join(c.NoClearPosition, ", "))
		}
		if c.Explanation != "" {
			fmt.Fprintf(b, "   %s\n", c.Explanation)
		}
	}
}

// --- Types for context parsing ---

type crux struct {
	Claim           string   `json:"crux_claim"`
	Topic           string   `json:"topic"`
	Explanation     string   `json:"explanation"`
	Controversy     float64  `json:"controversy_score"`
	AgreeAgents     []string `json:"agree_agents"`
	DisagreeAgents  []string `json:"disagree_agents"`
	NoClearPosition []string `json:"no_clear_position"`
	CruxType        string   `json:"crux_type"`
}

type topicSummary struct {
	Topic   string `json:"topic"`
	Summary string `json:"summary"`
}

type alignment struct {
	AgentID        string  `json:"agent_id"`
	AlignmentScore float64 `json:"alignment_score"`
	SharedCruxes   int     `json:"shared_cruxes"`
	AgreeCruxes    int     `json:"agree_cruxes"`
}

type bridging struct {
	AgentID       string  `json:"agent_id"`
	Content       string  `json:"content"`
	BridgingScore float64 `json:"bridging_score"`
}

type agentContext struct {
	AgentID              string         `json:"agent_id"`
	ClusterID            *int           `json:"cluster_id"`
	NearestAllies        []string       `json:"nearest_allies"`
	BiggestDisagreements []string       `json:"biggest_disagreements_with"`
	RelevantCruxes       []crux         `json:"relevant_cruxes"`
	TopicSummaries       []topicSummary `json:"topic_summaries"`
	AlignmentScores      []alignment    `json:"alignment_scores"`
	SwingAgents          []string       `json:"swing_agents"`
	BridgingStatements   []bridging     `json:"bridging_statements"`
	ConsensusStatements  []struct {
		Content          string  `json:"content"`
		OverallAgreeRatio float64 `json:"overall_agree_ratio"`
	} `json:"consensus_statements"`
	CompromiseProposal  string   `json:"compromise_proposal"`
	FailureScenarios    []string `json:"failure_scenarios"`
	ConstitutionalRules []string `json:"constitutional_rules"`
	EmergentNorms       []string `json:"emergent_norms"`
	RuleViolations      []string `json:"rule_violations"`
	StrategicNudge      string   `json:"strategic_nudge"`
	DiversityNudge      string   `json:"diversity_nudge"`
	IntegrityWarnings   []string `json:"integrity_warnings"`
}

type namedContext struct {
	name string
	ctx  agentContext
}

// --- Helpers ---

// truncateRunes truncates a string to at most maxRunes runes, appending "..." if truncated.
// Unlike byte slicing (s[:n]), this never splits a multibyte UTF-8 character.
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

func countByTag(scopes []scope, tag string) int {
	n := 0
	for _, s := range scopes {
		if s.scopeTag == tag {
			n++
		}
	}
	return n
}

func connect(ctx context.Context, url, secret string) (*sdkmcp.ClientSession, error) {
	transport := &sdkmcp.SSEClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: secret},
		},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "diplomacy", Version: "2.0"}, nil)
	return client.Connect(ctx, transport, nil)
}

func callTool(ctx context.Context, s *sdkmcp.ClientSession, name string, args map[string]any) string {
	res, err := s.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tool %s failed: %v\n", name, err)
		os.Exit(1)
	}
	if res.IsError {
		fmt.Fprintf(os.Stderr, "tool %s error: %s\n", name, res.Content[0].(*sdkmcp.TextContent).Text)
		os.Exit(1)
	}
	return res.Content[0].(*sdkmcp.TextContent).Text
}

func callToolSoft(ctx context.Context, s *sdkmcp.ClientSession, name string, args map[string]any) string {
	res, err := s.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fmt.Fprintf(os.Stderr, "  [soft] %s failed: %v\n", name, err)
		return ""
	}
	if res.IsError || len(res.Content) == 0 {
		errMsg := ""
		if res.IsError && len(res.Content) > 0 {
			errMsg = res.Content[0].(*sdkmcp.TextContent).Text
		}
		fmt.Fprintf(os.Stderr, "  [soft] %s error: %s\n", name, errMsg)
		return ""
	}
	return res.Content[0].(*sdkmcp.TextContent).Text
}

func mustParse(jsonStr string, v any) {
	if idx := strings.Index(jsonStr, "\n\n---\n"); idx != -1 {
		jsonStr = jsonStr[:idx]
	}
	if err := json.Unmarshal([]byte(jsonStr), v); err != nil {
		fmt.Fprintf(os.Stderr, "JSON parse error: %v\nRaw: %s\n", err, jsonStr[:min(200, len(jsonStr))])
		os.Exit(1)
	}
}

func mustParseSoft(jsonStr string, v any) {
	if idx := strings.Index(jsonStr, "\n\n---\n"); idx != -1 {
		jsonStr = jsonStr[:idx]
	}
	json.Unmarshal([]byte(jsonStr), v) //nolint:errcheck
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func fatal(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", msg, err)
		os.Exit(1)
	}
}
