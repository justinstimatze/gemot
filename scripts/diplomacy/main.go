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
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	Name     string           `json:"name"`
	Messages []Message        `json:"messages"`
	Orders   map[string][]any `json:"orders,omitempty"`
	State    PhaseState       `json:"state,omitempty"`
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
	fmt.Fprintf(os.Stderr, "Year %d: %d messages → %d scopes (%d global, %d bilateral, %d alliance, %d coalition)\n",
		*year, len(yearMessages), len(scopes),
		countByTag(scopes, "global"), countByTag(scopes, "bilateral"), countByTag(scopes, "alliance"), countByTag(scopes, "coalition"))
	for _, a := range alliances {
		fmt.Fprintf(os.Stderr, "  Alliance detected: %s\n", strings.Join(a, "+"))
	}

	// Load persistent deliberation state (reuse deliberations across years)
	state := loadState(*stateFile)

	os.MkdirAll(*outputDir, 0755) //nolint:errcheck
	ctx := context.Background()

	// Get Anthropic API key for v12 LLM enrichments
	anthropicKey := os.Getenv("GEMOT_ANTHROPIC_KEY")
	if anthropicKey == "" {
		if b, err := os.ReadFile(".env"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "GEMOT_ANTHROPIC_KEY=") {
					anthropicKey = strings.TrimPrefix(line, "GEMOT_ANTHROPIC_KEY=")
				}
			}
		}
	}

	// ========================================
	// Phase 1: Bilateral + Global Analysis
	// ========================================
	fmt.Fprintf(os.Stderr, "\n=== Phase 1: Bilateral + Global Analysis ===\n")
	results := analyzeScopes(ctx, scopes, url, secret, *year, state, *experiment)
	saveState(*stateFile, state)

	// Extract power balance, trust, and territory data
	balance := extractPowerBalance(&game, *year)
	promises := extractPromises(yearMessages)
	trust := checkPromiseFollowThrough(&game, *year, promises)
	territory := buildTerritorialContext(&game, *year)

	// ========================================
	// Phase 2: V12 Enrichments (parallel LLM calls)
	// ========================================
	fmt.Fprintf(os.Stderr, "\n=== Phase 2: V12 Enrichments ===\n")

	var (
		coalitionDeclarations map[string][]coalitionDecl
		inconsistencies       map[string][]inconsistency
		baitScores            map[string][]baitScore
		relStates             map[string]map[string]relationshipState
	)

	if anthropicKey != "" {
		// Run enrichments in parallel
		var enrichWg sync.WaitGroup

		// 2a: Coalition declarations
		enrichWg.Add(1)
		go func() {
			defer enrichWg.Done()
			fmt.Fprintf(os.Stderr, "  [v12] Running coalition declarations...\n")
			coalitionDeclarations = declareAllCoalitions(ctx, anthropicKey, *year, results, balance)
		}()

		// 2b: Cross-message consistency
		enrichWg.Add(1)
		go func() {
			defer enrichWg.Done()
			fmt.Fprintf(os.Stderr, "  [v12] Running cross-message consistency check...\n")
			inconsistencies = detectInconsistencies(ctx, anthropicKey, yearMessages, *year)
		}()

		// 2c: Bait scoring
		enrichWg.Add(1)
		go func() {
			defer enrichWg.Done()
			fmt.Fprintf(os.Stderr, "  [v12] Running bait scoring on proposals...\n")
			baitScores = scoreBaitForAll(ctx, anthropicKey, results, balance, territory)
		}()

		enrichWg.Wait()
	} else {
		fmt.Fprintf(os.Stderr, "  [v12] GEMOT_ANTHROPIC_KEY not set — skipping LLM enrichments\n")
	}

	// 2d: Relationship state machine (no LLM needed)
	relStates = computeRelationshipStates(results, trust, balance)

	// ========================================
	// Phase 3: Coalition Analysis (through gemot)
	// ========================================
	validCoalitions := matchCoalitions(coalitionDeclarations)
	if len(validCoalitions) > 0 {
		fmt.Fprintf(os.Stderr, "\n=== Phase 3: Coalition Analysis (%d validated coalitions) ===\n", len(validCoalitions))
		for _, c := range validCoalitions {
			fmt.Fprintf(os.Stderr, "  Coalition: %s — %s\n", strings.Join(c.Members, "+"), truncateRunes(c.Purpose, 80))
		}

		coalitionScopes := buildCoalitionScopes(validCoalitions, yearMessages, *year)
		if len(coalitionScopes) > 0 {
			coalitionResults := analyzeScopes(ctx, coalitionScopes, url, secret, *year, state, *experiment)
			results = append(results, coalitionResults...)
			saveState(*stateFile, state)
		}
	} else {
		fmt.Fprintf(os.Stderr, "\n=== Phase 3: No mutual coalition declarations — skipping ===\n")
	}

	// ========================================
	// Phase 4: Synthesize Enriched Briefings
	// ========================================
	fmt.Fprintf(os.Stderr, "\n=== Phase 4: Briefing Synthesis ===\n")

	// Build v12 enrichment bundle for briefing synthesis
	v12 := v12Enrichments{
		coalitionDeclarations: coalitionDeclarations,
		validCoalitions:       validCoalitions,
		inconsistencies:       inconsistencies,
		baitScores:            baitScores,
		relStates:             relStates,
	}

	var wg sync.WaitGroup
	for _, power := range powers {
		wg.Add(1)
		go func(power string) {
			defer wg.Done()
			briefing := synthesizeBriefing(power, *year, results, balance, trust, territory, results, v12)
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
			name:      key,
			scopeTag:  "bilateral",
			template:  "negotiation",
			delibType: "negotiation",
			powers:    parts,
			messages:  msgs,
		})
	}

	// Alliance scopes → negotiation type
	// 3+ members use consensus template; 2-member alliances use negotiation template
	// (consensus template requires min 3 participants)
	for _, alliance := range alliances {
		var msgs []Message
		for i := 0; i < len(alliance); i++ {
			for j := i + 1; j < len(alliance); j++ {
				key := pairKey(alliance[i], alliance[j])
				msgs = append(msgs, bilateral[key]...)
			}
		}
		if len(msgs) > 0 {
			tmpl := "consensus"
			if len(alliance) < 3 {
				tmpl = "negotiation"
			}
			scopes = append(scopes, scope{
				name:      strings.Join(alliance, "+"),
				scopeTag:  "alliance",
				template:  tmpl,
				delibType: "negotiation",
				powers:    alliance,
				messages:  msgs,
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
				fmt.Sscanf(p.Name[j:], "%d", &phaseYear) //nolint:errcheck
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
				if strings.EqualFold(rp, power) {
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
	mu  sync.Mutex
	IDs map[string]string `json:"deliberation_ids"` // scope name -> deliberation ID
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
		// Submit positions with reconnection on failure.
		submitSession, err := connect(ctx, url, secret)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Stage B: ERROR connecting for submissions: %v\n", err)
		} else {
			for i, pp := range interleaved {
				if i > 0 {
					time.Sleep(200 * time.Millisecond)
				}
				result := callToolSoft(ctx, submitSession, "participate", map[string]any{
					"action":          "submit_position",
					"deliberation_id": pp.deliberationID,
					"agent_id":        pp.agentID,
					"content":         pp.content,
					"interests":       pp.interests,
				})
				if result == "" {
					// Connection may have died — reconnect and retry
					submitSession.Close() //nolint:errcheck
					submitSession, err = connect(ctx, url, secret)
					if err != nil {
						fmt.Fprintf(os.Stderr, "  Stage B: reconnect failed: %v\n", err)
						break
					}
					callToolSoft(ctx, submitSession, "participate", map[string]any{
						"action":          "submit_position",
						"deliberation_id": pp.deliberationID,
						"agent_id":        pp.agentID,
						"content":         pp.content,
						"interests":       pp.interests,
					})
				}
			}

			// Alliance voting — fresh session per alliance to avoid connection issues.
			submitSession.Close() //nolint:errcheck
			for _, ar := range stageAResults {
				if ar.err != nil || ar.sc.scopeTag != "alliance" || len(ar.sc.messages) == 0 {
					continue
				}
				voteSession, voteErr := connect(ctx, url, secret)
				if voteErr != nil {
					fmt.Fprintf(os.Stderr, "  Stage B: ERROR connecting for alliance votes: %v\n", voteErr)
					continue
				}
				posJSON := callToolSoft(ctx, voteSession, "participate", map[string]any{
					"action":          "get_positions",
					"deliberation_id": ar.deliberationID,
				})
				if posJSON == "" {
					voteSession.Close() //nolint:errcheck
					continue
				}
				var positions []struct {
					ID      string `json:"position_id"`
					AgentID string `json:"agent_id"`
				}
				mustParseSoft(posJSON, &positions)

				for _, pos := range positions {
					for _, p := range ar.sc.powers {
						voterAgent := strings.ToLower(p) + "-agent"
						if voterAgent == pos.AgentID {
							continue
						}
						callToolSoft(ctx, voteSession, "participate", map[string]any{
							"action":          "vote",
							"deliberation_id": ar.deliberationID,
							"agent_id":        voterAgent,
							"position_id":     pos.ID,
							"value":           1,
						})
					}
				}
				voteSession.Close() //nolint:errcheck
			}
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
		// Skip analysis for scopes with fewer than 2 distinct agents.
		// Single-agent deliberations have nothing to analyze (no cruxes possible).
		distinctAgents := make(map[string]bool)
		for _, msg := range ar.sc.messages {
			distinctAgents[strings.ToUpper(msg.Sender)] = true
		}
		if len(distinctAgents) < 2 {
			if len(ar.sc.messages) > 0 {
				fmt.Fprintf(os.Stderr, "  [%s] %s: skipping analysis (only %d agent(s) with messages)\n",
					ar.sc.scopeTag, ar.sc.name, len(distinctAgents))
			}
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

		// Re-set the template in case the scope's template requirements changed
		// (e.g., alliance grew from 2 to 3 members, switching negotiation → consensus)
		if sc.template != "" {
			callToolSoft(ctx, session, "deliberation", map[string]any{
				"action":          "set_template",
				"deliberation_id": deliberationID,
				"template":        sc.template,
			})
		}
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
		case "coalition":
			topic = fmt.Sprintf("%s coalition deliberation", sc.name)
			desc = fmt.Sprintf("Coalition deliberation among %s. Members mutually declared this coalition.", strings.Join(sc.powers, ", "))
		}

		createArgs := map[string]any{
			"topic":       topic,
			"description": desc,
			"type":        sc.delibType,
		}
		if experimentID != "" {
			createArgs["group_id"] = experimentID
		}
		createArgs["action"] = "create"
		createJSON := callTool(ctx, session, "deliberation", createArgs)

		var createResp struct {
			DeliberationID string `json:"deliberation_id"`
		}
		mustParse(createJSON, &createResp)
		deliberationID = createResp.DeliberationID

		// Set template for scope-appropriate rules and analysis framing
		if sc.template != "" {
			callToolSoft(ctx, session, "deliberation", map[string]any{
				"action":          "set_template",
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
			callToolSoft(ctx, session, "participate", map[string]any{
				"action":          "get_context",
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
	analyzeResult := callToolSoft(ctx, session, "analyze", map[string]any{
		"action":          "run",
		"deliberation_id": deliberationID,
	})
	if analyzeResult == "" {
		return nil, fmt.Errorf("analyze call failed")
	}

	// Poll for completion using first power's context.
	// If the analysis fails (status resets to "open"), re-trigger up to 2 more times.
	// If the connection drops, reconnect and keep polling.
	firstPower := strings.ToLower(sc.powers[0]) + "-agent"
	time.Sleep(5 * time.Second)
	completed := false
	analyzeRetries := 0
	reconnects := 0
	const maxAnalyzeRetries = 2
	const maxReconnects = 10

	reconnect := func() {
		session.Close() //nolint:errcheck
		reconnects++
		if reconnects > maxReconnects {
			fmt.Fprintf(os.Stderr, "%s too many reconnects (%d), giving up\n", prefix, reconnects)
			return
		}
		fmt.Fprintf(os.Stderr, "%s reconnecting (%d)...\n", prefix, reconnects)
		time.Sleep(3 * time.Second)
		var reconnErr error
		session, reconnErr = connect(ctx, url, secret)
		if reconnErr != nil {
			fmt.Fprintf(os.Stderr, "%s reconnect failed: %v\n", prefix, reconnErr)
		}
	}

	for i := 0; i < 300; i++ {
		time.Sleep(3 * time.Second)
		if reconnects > maxReconnects {
			break
		}

		result := callToolSoft(ctx, session, "participate", map[string]any{
			"action":          "get_context",
			"deliberation_id": deliberationID,
			"agent_id":        firstPower,
		})

		if result == "" {
			// First: try to get status on a fresh connection (the current one may be dead)
			reconnect()
			if session == nil {
				continue
			}

			statusJSON := callToolSoft(ctx, session, "deliberation", map[string]any{
				"action":          "get",
				"deliberation_id": deliberationID,
			})
			if statusJSON == "" {
				// Still can't connect — keep retrying
				continue
			}

			var s struct {
				Status    string `json:"status"`
				SubStatus string `json:"sub_status"`
			}
			json.Unmarshal([]byte(strings.SplitN(statusJSON, "\n\n---\n", 2)[0]), &s) //nolint:errcheck
			fmt.Fprintf(os.Stderr, "%s %s/%s\n", prefix, s.Status, s.SubStatus)

			if s.Status == "analyzing" {
				// Analysis still running on the server — just keep polling
				continue
			}

			// If status reset to "open", analysis failed — retry
			if s.Status == "open" && analyzeRetries < maxAnalyzeRetries {
				analyzeRetries++
				fmt.Fprintf(os.Stderr, "%s analysis failed, retrying (%d/%d)...\n", prefix, analyzeRetries, maxAnalyzeRetries)
				time.Sleep(5 * time.Second)
				retryResult := callToolSoft(ctx, session, "analyze", map[string]any{
					"action":          "run",
					"deliberation_id": deliberationID,
				})
				if retryResult == "" {
					return nil, fmt.Errorf("analyze retry failed")
				}
				continue
			}
			if s.Status == "open" {
				return nil, fmt.Errorf("analysis failed after %d retries", maxAnalyzeRetries)
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
		result := callToolSoft(ctx, session, "participate", map[string]any{
			"action":          "get_context",
			"deliberation_id": deliberationID,
			"agent_id":        agentID,
		})
		if result != "" {
			contexts[strings.ToLower(p)] = result
		}
	}

	// Fetch commitments for this deliberation
	var commitments []commitment
	commJSON := callToolSoft(ctx, session, "decide", map[string]any{
		"action":          "get_commitments",
		"deliberation_id": deliberationID,
	})
	if commJSON != "" {
		mustParseSoft(commJSON, &commitments)
	}

	return &scopeResult{scope: sc, contexts: contexts, commitments: commitments}, nil
}

// v12Enrichments bundles all v12 analysis additions for briefing synthesis.
type v12Enrichments struct {
	coalitionDeclarations map[string][]coalitionDecl
	validCoalitions       []coalitionGroup
	inconsistencies       map[string][]inconsistency
	baitScores            map[string][]baitScore
	relStates             map[string]map[string]relationshipState
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
				fmt.Sscanf(p.Name[j:], "%d", &phaseYear) //nolint:errcheck
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

func synthesizeBriefing(power string, year int, results []scopeResult, balance powerBalance, trust *trustTracker, territory map[string]territorialInfo, allResults []scopeResult, v12 v12Enrichments) string {
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
		case "alliance", "coalition":
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

		type powerSC struct {
			name  string
			scs   int
			delta int
		}
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

			// 10. V12: RELATIONSHIP TRAJECTORY
			if v12.relStates != nil {
				if rs, ok := v12.relStates[power][otherUpper]; ok {
					stateEmoji := map[string]string{
						"allied": "🤝", "cooperative": "👍", "neutral": "—",
						"strained": "⚡", "hostile": "⚔",
					}
					trendArrow := map[string]string{
						"improving": "↑", "stable": "→", "deteriorating": "↓",
					}
					fmt.Fprintf(&b, "  RELATIONSHIP: %s %s (trend: %s %s)\n",
						rs.State, stateEmoji[rs.State], rs.Trend, trendArrow[rs.Trend])
					if len(rs.Evidence) > 0 {
						for _, e := range rs.Evidence {
							fmt.Fprintf(&b, "    • %s\n", e)
						}
					}
				}
			}

			// 11. V12: BAIT WARNING (proposal asymmetry)
			if v12.baitScores != nil {
				for _, bs := range v12.baitScores[powerUpper] {
					if bs.Bilateral == pairKey(power, other) || bs.Bilateral == pairKey(other, power) {
						if bs.Suspicious {
							fmt.Fprintf(&b, "  ⚠ BAIT WARNING: Proposal is %.0f%% asymmetric, favors %s\n",
								bs.Asymmetry*100, bs.FavorsPower)
							fmt.Fprintf(&b, "    %s\n", bs.Reason)
						} else if bs.Asymmetry >= 0.3 {
							fmt.Fprintf(&b, "  NOTE: Proposal slightly favors %s (%.0f%% asymmetric)\n",
								bs.FavorsPower, bs.Asymmetry*100)
						}
					}
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

	// ========================================
	// V12: COALITION MEMBERSHIPS
	// ========================================

	if len(v12.validCoalitions) > 0 {
		var myCoalitions []coalitionGroup
		for _, c := range v12.validCoalitions {
			for _, m := range c.Members {
				if strings.EqualFold(m, power) {
					myCoalitions = append(myCoalitions, c)
					break
				}
			}
		}
		if len(myCoalitions) > 0 {
			fmt.Fprintf(&b, "YOUR COALITION MEMBERSHIPS:\n")
			for _, c := range myCoalitions {
				fmt.Fprintf(&b, "  %s — %s\n", strings.Join(c.Members, "+"), c.Purpose)
			}
			fmt.Fprintln(&b)
		}

		// Coalitions you're NOT in
		var otherCoalitions []coalitionGroup
		for _, c := range v12.validCoalitions {
			isMember := false
			for _, m := range c.Members {
				if strings.EqualFold(m, power) {
					isMember = true
					break
				}
			}
			if !isMember {
				otherCoalitions = append(otherCoalitions, c)
			}
		}
		if len(otherCoalitions) > 0 {
			fmt.Fprintf(&b, "COALITIONS YOU'RE EXCLUDED FROM:\n")
			for _, c := range otherCoalitions {
				fmt.Fprintf(&b, "  ⚠ %s — %s\n", strings.Join(c.Members, "+"), c.Purpose)
			}
			fmt.Fprintln(&b)
		}
	}

	// Your own declarations (for transparency on what you committed to)
	if v12.coalitionDeclarations != nil {
		if decls, ok := v12.coalitionDeclarations[power]; ok && len(decls) > 0 {
			fmt.Fprintf(&b, "YOUR DECLARED COALITIONS:\n")
			for _, d := range decls {
				mutual := "⚠ NOT MUTUAL"
				for _, c := range v12.validCoalitions {
					if strings.Join(c.Members, "+") == strings.Join(d.Members, "+") {
						mutual = "✓ mutual"
						break
					}
				}
				fmt.Fprintf(&b, "  %s — %s [%s]\n", strings.Join(d.Members, "+"), d.Purpose, mutual)
			}
			fmt.Fprintln(&b)
		}
	}

	// ========================================
	// V12: DECEPTION INTELLIGENCE
	// ========================================

	if v12.inconsistencies != nil {
		// What OTHER powers are saying inconsistently (intelligence for you)
		var deceptionIntel []string
		for otherPower, incons := range v12.inconsistencies {
			if strings.EqualFold(otherPower, power) {
				continue
			}
			for _, inc := range incons {
				// Only show if relevant to this power
				if strings.EqualFold(inc.SaidTo, power) || strings.Contains(strings.ToUpper(inc.SaidAbout), powerUpper) {
					deceptionIntel = append(deceptionIntel,
						fmt.Sprintf("%s: %s (told %s something different — %s)",
							otherPower, truncateRunes(inc.Explanation, 200), inc.SaidTo, truncateRunes(inc.Statement2, 150)))
				}
			}
		}
		if len(deceptionIntel) > 0 {
			fmt.Fprintf(&b, "DECEPTION INTELLIGENCE:\n")
			fmt.Fprintf(&b, "Cross-referencing reveals other powers may be playing both sides:\n")
			for _, d := range deceptionIntel {
				fmt.Fprintf(&b, "  ⚠ %s\n", d)
			}
			fmt.Fprintln(&b)
		}

		// Your own consistency issues (self-awareness warning)
		if myIncons, ok := v12.inconsistencies[power]; ok && len(myIncons) > 0 {
			fmt.Fprintf(&b, "CONSISTENCY WARNING (your own messages):\n")
			fmt.Fprintf(&b, "Your bilateral messages contain apparent contradictions that others may detect:\n")
			for _, inc := range myIncons {
				fmt.Fprintf(&b, "  ⚠ Re: %s — %s\n", inc.SaidAbout, truncateRunes(inc.Explanation, 200))
			}
			fmt.Fprintln(&b)
		}
	}

	// ========================================
	// V12: STAB RISK ASSESSMENT
	// ========================================

	stabRisks := computeStabRisks(power, allResults, trust, balance, territory)
	if len(stabRisks) > 0 {
		hasSignificant := false
		for _, sr := range stabRisks {
			if sr.Risk != "low" {
				hasSignificant = true
				break
			}
		}
		if hasSignificant {
			fmt.Fprintf(&b, "STAB RISK ASSESSMENT:\n")
			for _, sr := range stabRisks {
				if sr.Risk == "low" {
					continue
				}
				riskLabel := strings.ToUpper(sr.Risk)
				fmt.Fprintf(&b, "  [%s] %s:\n", riskLabel, sr.From)
				for _, r := range sr.Reasons {
					fmt.Fprintf(&b, "    - %s\n", r)
				}
			}
			fmt.Fprintln(&b)
		}
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
		Content           string  `json:"content"`
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

// ============================================================
// V12: Anthropic API Client
// ============================================================

// llmCall makes a direct Anthropic API call for analysis enrichments.
func llmCall(ctx context.Context, apiKey, system, user string) (string, error) {
	body := map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 4096,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": user}},
	}
	bodyJSON, _ := json.Marshal(body) //nolint:errcheck
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(bodyJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	respBody, _ := io.ReadAll(resp.Body) //nolint:errcheck
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("anthropic API %d: %s", resp.StatusCode, string(respBody[:min(200, len(respBody))]))
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}
	if len(result.Content) == 0 {
		return "", fmt.Errorf("empty response from anthropic API")
	}
	return result.Content[0].Text, nil
}

// extractJSON pulls the first JSON object or array from a string (handles markdown fences).
func extractJSON(s string) string {
	// Strip markdown code fences
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	}
	// Find first { or [
	start := -1
	for i, c := range s {
		if c == '{' || c == '[' {
			start = i
			break
		}
	}
	if start == -1 {
		return s
	}
	return strings.TrimSpace(s[start:])
}

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

// declareCoalitionsForPower asks one power to declare their coalitions based on bilateral intelligence.
func declareCoalitionsForPower(ctx context.Context, apiKey string, power string, year int, results []scopeResult, balance powerBalance) ([]coalitionDecl, error) {
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
			fmt.Fprintf(&intel, "Shared ground:\n")
			for _, bs := range ac.BridgingStatements {
				fmt.Fprintf(&intel, "  - %s (%.0f%% support)\n", truncateRunes(bs.Content, 200), bs.BridgingScore*100)
			}
		}
		if ac.CompromiseProposal != "" {
			fmt.Fprintf(&intel, "Compromise proposal: %s\n", truncateRunes(ac.CompromiseProposal, 300))
		}
		if len(ac.RelevantCruxes) > 0 {
			fmt.Fprintf(&intel, "Unresolved issues: %d\n", len(ac.RelevantCruxes))
			for _, c := range ac.RelevantCruxes {
				fmt.Fprintf(&intel, "  - %s\n", truncateRunes(c.Claim, 150))
			}
		}
		if len(ac.AlignmentScores) > 0 {
			for _, a := range ac.AlignmentScores {
				fmt.Fprintf(&intel, "Alignment: %.0f%%\n", a.AlignmentScore*100)
			}
		}
	}

	// Add global context
	for _, r := range results {
		if r.scope.scopeTag != "global" {
			continue
		}
		ctxJSON, ok := r.contexts[powerLower]
		if !ok {
			continue
		}
		var ac agentContext
		mustParseSoft(ctxJSON, &ac)
		if len(ac.AlignmentScores) > 0 {
			fmt.Fprintf(&intel, "\n--- Global Alignment ---\n")
			for _, a := range ac.AlignmentScores {
				fmt.Fprintf(&intel, "  %s: %.0f%% aligned\n", a.AgentID, a.AlignmentScore*100)
			}
		}
	}

	// Add power balance
	fmt.Fprintf(&intel, "\n--- Power Balance ---\n")
	for p, scs := range balance.current {
		marker := ""
		if strings.EqualFold(p, power) {
			marker = " (you)"
		}
		fmt.Fprintf(&intel, "  %s: %d SCs%s\n", p, scs, marker)
	}

	system := `You are a strategic advisor for a power in Diplomacy. Based on the diplomatic intelligence provided, declare coalitions this power should pursue.

A coalition is a group of 2+ powers (including this power) with a shared strategic purpose. Coalitions can overlap — a power can be in multiple coalitions simultaneously with different (even contradictory) purposes. This is normal in Diplomacy.

Rules:
- Only declare coalitions where genuine mutual interest exists based on the evidence
- Be specific about the purpose — vague coalitions are worthless
- Include this power in every coalition's member list
- A power can be in coalitions that have competing loyalties (e.g., allied with both sides of a future conflict)

Respond with ONLY valid JSON, no other text:`

	prompt := fmt.Sprintf(`Power: %s
Year: %d

Diplomatic Intelligence:
%s

Declare coalitions as JSON:
{"coalitions": [{"members": ["POWER1", "POWER2"], "purpose": "specific shared objective"}]}`, power, 1900+year, intel.String())

	resp, err := llmCall(ctx, apiKey, system, prompt)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Coalitions []coalitionDecl `json:"coalitions"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp)), &parsed); err != nil {
		return nil, fmt.Errorf("parsing coalition declarations: %w", err)
	}

	// Normalize member names
	for i := range parsed.Coalitions {
		for j := range parsed.Coalitions[i].Members {
			parsed.Coalitions[i].Members[j] = strings.ToUpper(parsed.Coalitions[i].Members[j])
		}
		sort.Strings(parsed.Coalitions[i].Members)
	}

	return parsed.Coalitions, nil
}

// declareAllCoalitions runs coalition declarations for all powers in parallel.
func declareAllCoalitions(ctx context.Context, apiKey string, year int, results []scopeResult, balance powerBalance) map[string][]coalitionDecl {
	declarations := make(map[string][]coalitionDecl)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, power := range powers {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			decls, err := declareCoalitionsForPower(ctx, apiKey, p, year, results, balance)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  [coalition] %s: declaration failed: %v\n", p, err)
				return
			}
			mu.Lock()
			declarations[p] = decls
			mu.Unlock()
			fmt.Fprintf(os.Stderr, "  [coalition] %s: declared %d coalition(s)\n", p, len(decls))
			for _, d := range decls {
				fmt.Fprintf(os.Stderr, "    %s — %s\n", strings.Join(d.Members, "+"), d.Purpose)
			}
		}(power)
	}
	wg.Wait()
	return declarations
}

// matchCoalitions finds coalitions where ALL members mutually declared each other.
// Returns validated coalition groups.
func matchCoalitions(declarations map[string][]coalitionDecl) []coalitionGroup {
	// Build a map: sorted member set key → which powers declared it
	type candidateInfo struct {
		members  []string
		purposes []string // purposes from each declaring power
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

// detectInconsistencies compares each power's statements across bilaterals.
func detectInconsistencies(ctx context.Context, apiKey string, messages []Message, year int) map[string][]inconsistency {
	result := make(map[string][]inconsistency)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, power := range powers {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			// Gather messages this power sent, grouped by recipient
			byRecipient := make(map[string][]string)
			for _, msg := range messages {
				if !strings.EqualFold(msg.Sender, p) {
					continue
				}
				if strings.EqualFold(msg.Recipient, "GLOBAL") {
					continue
				}
				recipient := strings.ToUpper(msg.Recipient)
				byRecipient[recipient] = append(byRecipient[recipient],
					fmt.Sprintf("[to %s, %s] %s", recipient, msg.Phase, msg.Content))
			}

			if len(byRecipient) < 2 {
				return // need at least 2 bilateral conversations to find contradictions
			}

			var msgDump strings.Builder
			for recipient, msgs := range byRecipient {
				fmt.Fprintf(&msgDump, "\n=== Messages to %s ===\n", recipient)
				for _, m := range msgs {
					fmt.Fprintf(&msgDump, "%s\n", m)
				}
			}

			system := `You are analyzing a Diplomacy player's messages for contradictions. Compare what this power said to different powers. Look for:
- Contradictory promises (promised the same territory to two different powers)
- Conflicting strategic commitments (told A they'd attack B, told B they'd attack A)
- Inconsistent threat assessments (told A that C is dangerous, told C that A is dangerous)
- Playing both sides of a dispute

Only report genuine contradictions, not normal diplomatic ambiguity. False positives waste the analyst's time.

Respond with ONLY valid JSON, no other text.`

			prompt := fmt.Sprintf(`Power: %s
Year: %d

Messages sent by %s to different powers:
%s

Find contradictions. Respond as JSON:
{"inconsistencies": [{"said_to": "POWER_A", "said_about": "topic or POWER_B", "statement_to_one": "what they said to A", "statement_to_other": "contradicting statement to another power", "explanation": "why this is contradictory"}]}

If no contradictions found, respond: {"inconsistencies": []}`, p, 1900+year, p, msgDump.String())

			resp, err := llmCall(ctx, apiKey, system, prompt)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  [consistency] %s: failed: %v\n", p, err)
				return
			}

			var parsed struct {
				Inconsistencies []inconsistency `json:"inconsistencies"`
			}
			if err := json.Unmarshal([]byte(extractJSON(resp)), &parsed); err != nil {
				fmt.Fprintf(os.Stderr, "  [consistency] %s: parse error: %v\n", p, err)
				return
			}

			// Tag with power name
			for i := range parsed.Inconsistencies {
				parsed.Inconsistencies[i].Power = p
			}

			if len(parsed.Inconsistencies) > 0 {
				mu.Lock()
				result[p] = parsed.Inconsistencies
				mu.Unlock()
				fmt.Fprintf(os.Stderr, "  [consistency] %s: %d contradiction(s) detected\n", p, len(parsed.Inconsistencies))
			} else {
				fmt.Fprintf(os.Stderr, "  [consistency] %s: no contradictions\n", p)
			}
		}(power)
	}
	wg.Wait()
	return result
}

// ============================================================
// V12: Bait Scoring (Proposal Asymmetry Detection)
// ============================================================

// baitScore measures how asymmetric a bilateral proposal is.
type baitScore struct {
	Bilateral   string  `json:"bilateral"`
	Proposal    string  `json:"proposal"`
	Asymmetry   float64 `json:"asymmetry"`     // 0-1, higher = more one-sided
	FavorsPower string  `json:"favors_power"`  // which power benefits more
	Reason      string  `json:"reason"`
	Suspicious  bool    `json:"suspicious"`    // true if likely bait
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

// ============================================================
// V12: Stab Risk Scoring
// ============================================================

// stabRisk assesses how vulnerable a power is to betrayal by a specific partner.
type stabRisk struct {
	From        string
	Risk        string // "low", "medium", "high", "critical"
	Reasons     []string
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
