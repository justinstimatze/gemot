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
	throughPhase := flag.String("through-phase", "", "Collect messages up through this phase (e.g. S1901M for spring only, F1901M for full year). Default: full year.")
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

	// Determine which phases to collect messages from.
	// Default: both spring and fall of the target year.
	// With --through-phase: only messages from that specific phase (for per-season mode).
	// Prior seasons' messages are already in the deliberation from earlier rounds.
	gameYear := 1900 + *year
	targetPhases := map[string]bool{}
	if *throughPhase != "" {
		// Per-season: only collect messages from the target phase
		targetPhases[*throughPhase] = true
	} else {
		// Per-year: collect both spring and fall
		targetPhases[fmt.Sprintf("S%dM", gameYear)] = true
		targetPhases[fmt.Sprintf("F%dM", gameYear)] = true
	}

	// Collect messages from target phases
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

				deliberationID, err = createOrReuseDeliberation(ctx, session, sc, year, state, experimentID, url, secret)
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
			// Dedup: fetch existing positions per deliberation to avoid re-submitting on rerun.
			existingContent := map[string]map[string]bool{} // deliberation_id -> set of content hashes
			dedupSession, dedupErr := connect(ctx, url, secret)
			if dedupErr == nil {
				for _, ar := range stageAResults {
					if ar.err != nil || ar.deliberationID == "" {
						continue
					}
					posJSON := callToolSoft(ctx, dedupSession, "participate", map[string]any{
						"action":          "get_positions",
						"deliberation_id": ar.deliberationID,
					})
					if posJSON != "" {
						var positions []struct {
							Content string `json:"content"`
						}
						if idx := strings.Index(posJSON, "\n\n---\n"); idx != -1 {
							posJSON = posJSON[:idx]
						}
						json.Unmarshal([]byte(posJSON), &positions) //nolint:errcheck
						hashes := map[string]bool{}
						for _, p := range positions {
							h := sha256Short(p.Content)
							hashes[h] = true
						}
						existingContent[ar.deliberationID] = hashes
					}
				}
				dedupSession.Close() //nolint:errcheck
			}

			skipped := 0
			for i, pp := range interleaved {
				if i > 0 {
					time.Sleep(50 * time.Millisecond)
				}
				// Skip if this exact content was already submitted to this deliberation
				if hashes, ok := existingContent[pp.deliberationID]; ok {
					if hashes[sha256Short(pp.content)] {
						skipped++
						continue
					}
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

			if skipped > 0 {
				fmt.Fprintf(os.Stderr, "  Stage B: skipped %d duplicate positions (already submitted)\n", skipped)
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
func createOrReuseDeliberation(ctx context.Context, session *sdkmcp.ClientSession, sc scope, year int, state *persistentState, experimentID, url, secret string) (string, error) {
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
		// Create new deliberation — pass template inline to avoid a separate set_template call
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
		if sc.template != "" {
			createArgs["template"] = sc.template
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

		// Save to persistent state
		state.mu.Lock()
		state.IDs[sc.name] = deliberationID
		state.mu.Unlock()

		fmt.Fprintf(os.Stderr, "  [%s] %s: created deliberation %s\n",
			sc.scopeTag, sc.name, deliberationID[:8])
	}

	// Satisfy forced acknowledgment: call get_context for all agents before submitting
	// (required for round 2+ deliberations — agents must review cruxes before contributing).
	// Uses a resilient session — the original session may have died after the create call.
	if existingID != "" && year > 1 {
		rs := newResilientSession(url, secret)
		defer rs.close()
		for _, p := range sc.powers {
			agentID := strings.ToLower(p) + "-agent"
			rs.callSoft(ctx, "participate", map[string]any{
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

		// Poll by checking deliberation status first (cheap, doesn't trigger
		// forced-acknowledgment errors). Only try get_context once analysis is done.
		statusJSON := callToolSoft(ctx, session, "deliberation", map[string]any{
			"action":          "get",
			"deliberation_id": deliberationID,
		})

		if statusJSON == "" {
			// Connection likely dead — reconnect
			reconnect()
			continue
		}

		var s struct {
			Status    string `json:"status"`
			SubStatus string `json:"sub_status"`
		}
		json.Unmarshal([]byte(strings.SplitN(statusJSON, "\n\n---\n", 2)[0]), &s) //nolint:errcheck

		if s.Status == "analyzing" {
			if i%10 == 0 {
				fmt.Fprintf(os.Stderr, "%s %s/%s\n", prefix, s.Status, s.SubStatus)
			}
			continue
		}

		if s.Status == "open" {
			// Analysis done — try to get context to confirm results exist
			result := callToolSoft(ctx, session, "participate", map[string]any{
				"action":          "get_context",
				"deliberation_id": deliberationID,
				"agent_id":        firstPower,
			})
			if result != "" {
				completed = true
				fmt.Fprintf(os.Stderr, "%s analysis complete\n", prefix)
				break
			}
			// Status is open but no results — analysis may have failed, retry
			if analyzeRetries < maxAnalyzeRetries {
				analyzeRetries++
				fmt.Fprintf(os.Stderr, "%s analysis failed (no results), retrying (%d/%d)...\n", prefix, analyzeRetries, maxAnalyzeRetries)
				time.Sleep(5 * time.Second)
				retryResult := callToolSoft(ctx, session, "analyze", map[string]any{
					"action":          "run",
					"deliberation_id": deliberationID,
				})
				if retryResult == "" {
					reconnect()
				}
				continue
			}
			return nil, fmt.Errorf("analysis failed after %d retries", maxAnalyzeRetries)
		}

		// Unexpected status — log and continue
		fmt.Fprintf(os.Stderr, "%s unexpected status: %s/%s\n", prefix, s.Status, s.SubStatus)
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

func countByTag(scopes []scope, tag string) int {
	n := 0
	for _, s := range scopes {
		if s.scopeTag == tag {
			n++
		}
	}
	return n
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
