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
	scope    scope
	contexts map[string]string // power (lowercase) -> context JSON
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

	// Synthesize per-power briefings from all relevant scopes
	var wg sync.WaitGroup
	for _, power := range powers {
		wg.Add(1)
		go func(power string) {
			defer wg.Done()
			briefing := synthesizeBriefing(power, *year, results, balance)
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
	targetPhases := map[string]bool{
		fmt.Sprintf("S%dM", gameYear): true,
		fmt.Sprintf("F%dM", gameYear): true,
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

// analyzeScopes processes all scopes in parallel and returns results.
func analyzeScopes(ctx context.Context, scopes []scope, url, secret string, year int, state *persistentState, experimentID string) []scopeResult {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var results []scopeResult

	for i, sc := range scopes {
		wg.Add(1)
		stagger := time.Duration(i) * 2 * time.Second
		go func(sc scope, delay time.Duration) {
			defer wg.Done()
			if delay > 0 {
				time.Sleep(delay)
			}

			fmt.Fprintf(os.Stderr, "  [%s] %s: %d messages, %d powers\n",
				sc.scopeTag, sc.name, len(sc.messages), len(sc.powers))

			var result *scopeResult
			var lastErr error
			for attempt := 1; attempt <= 3; attempt++ {
				if attempt > 1 {
					fmt.Fprintf(os.Stderr, "  [%s] %s: retry %d/3...\n", sc.scopeTag, sc.name, attempt)
					time.Sleep(10 * time.Second)
				}

				session, err := connect(ctx, url, secret)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  [%s] %s: ERROR connecting: %v\n", sc.scopeTag, sc.name, err)
					lastErr = err
					continue
				}

				result, err = analyzeScope(ctx, session, url, secret, sc, year, state, experimentID)
				session.Close() //nolint:errcheck
				if err == nil {
					lastErr = nil
					break
				}
				fmt.Fprintf(os.Stderr, "  [%s] %s: ERROR: %v\n", sc.scopeTag, sc.name, err)
				lastErr = err
			}

			if lastErr != nil {
				fmt.Fprintf(os.Stderr, "  [%s] %s: FAILED: %v\n", sc.scopeTag, sc.name, lastErr)
				return
			}

			mu.Lock()
			results = append(results, *result)
			mu.Unlock()
			fmt.Fprintf(os.Stderr, "  [%s] %s: complete (%d contexts)\n", sc.scopeTag, sc.name, len(result.contexts))
		}(sc, stagger)
	}

	wg.Wait()
	return results
}

// analyzeScope creates or reuses a deliberation for one scope, submits messages,
// runs analysis, and returns contexts for each participating power.
// When state is provided, deliberations persist across years as multi-round deliberations.
func analyzeScope(ctx context.Context, session *sdkmcp.ClientSession, url, secret string, sc scope, year int, state *persistentState, experimentID string) (*scopeResult, error) {
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

	// 2. Satisfy forced acknowledgment: call get_context for all agents before submitting
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

	// 3. Submit each message as a position
	if len(sc.messages) == 0 {
		// No messages for this scope (e.g. global assembly with no broadcasts).
		// Deliberation exists but has no data to analyze yet.
		return &scopeResult{scope: sc, contexts: make(map[string]string)}, nil
	}
	for _, msg := range sc.messages {
		sender := strings.ToUpper(msg.Sender)
		recipient := strings.ToUpper(msg.Recipient)
		positionText := fmt.Sprintf("[%s → %s, %s] %s", sender, recipient, msg.Phase, msg.Content)
		agentID := strings.ToLower(msg.Sender) + "-agent"

		callToolSoft(ctx, session, "submit_position", map[string]any{
			"deliberation_id": deliberationID,
			"agent_id":        agentID,
			"content":         positionText,
			"interests":       fmt.Sprintf("%s's strategic objectives", strings.ToLower(msg.Sender)),
		})
	}

	// 4. Voting: only for alliance scopes where members vote on proposals.
	// Global and bilateral scopes rely on content analysis for crux detection —
	// artificial votes conflate "I heard this" with "I agree with this."
	// Alliance consensus deliberations benefit from votes: members agree/disagree
	// with each other's strategic proposals, which feeds clustering and bridging.
	if sc.scopeTag == "alliance" {
		posJSON := callTool(ctx, session, "get_positions", map[string]any{
			"deliberation_id": deliberationID,
		})
		var positions []struct {
			ID      string `json:"position_id"`
			AgentID string `json:"agent_id"`
		}
		mustParse(posJSON, &positions)

		// Each alliance member votes on every other member's messages.
		// +1 for messages from their bilateral partner (shared negotiation context),
		// 0 (skip) for messages from bilateral channels they're not part of.
		for _, pos := range positions {
			for _, p := range sc.powers {
				voterAgent := strings.ToLower(p) + "-agent"
				if voterAgent == pos.AgentID {
					continue // don't vote on own positions
				}
				// In alliance scope, vote +1 on all other members' positions.
				// Alliance members are cooperating — votes signal internal agreement/disagreement.
				callToolSoft(ctx, session, "vote", map[string]any{
					"deliberation_id": deliberationID,
					"agent_id":        voterAgent,
					"position_id":     pos.ID,
					"vote":            1,
				})
			}
		}
	}

	// 5. Analyze
	prefix := fmt.Sprintf("  [%s] %s:", sc.scopeTag, sc.name)
	fmt.Fprintf(os.Stderr, "%s analyzing...\n", prefix)
	callTool(ctx, session, "analyze", map[string]any{
		"deliberation_id": deliberationID,
	})

	// 6. Poll for completion using first power's context
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

	// 7. Collect contexts for each participating power
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

	return &scopeResult{scope: sc, contexts: contexts}, nil
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

func synthesizeBriefing(power string, year int, results []scopeResult, balance powerBalance) string {
	powerLower := strings.ToLower(power)
	gameYear := 1900 + year

	// Collect relevant contexts by scope type
	var globalCtx *agentContext
	var bilateralCtxs []namedContext
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
			// Name the bilateral by the OTHER power
			other := ""
			for _, p := range r.scope.powers {
				if strings.ToLower(p) != powerLower {
					other = p
				}
			}
			bilateralCtxs = append(bilateralCtxs, namedContext{name: other, ctx: ac})
		case "alliance":
			allianceCtxs = append(allianceCtxs, namedContext{name: r.scope.name, ctx: ac})
		}
	}

	if globalCtx == nil && len(bilateralCtxs) == 0 && len(allianceCtxs) == 0 {
		return ""
	}

	// Sort bilateral by number of cruxes (most informative first)
	sort.Slice(bilateralCtxs, func(i, j int) bool {
		return len(bilateralCtxs[i].ctx.RelevantCruxes) > len(bilateralCtxs[j].ctx.RelevantCruxes)
	})

	var b strings.Builder
	fmt.Fprintf(&b, "=== DIPLOMATIC INTELLIGENCE BRIEFING: %s — Year %d ===\n", power, gameYear)
	fmt.Fprintf(&b, "This briefing identifies opportunities for diplomatic cooperation.\n")
	fmt.Fprintf(&b, "Military conflict is costly — the analysis below highlights where\n")
	fmt.Fprintf(&b, "mutual agreements serve your interests better than unilateral action.\n\n")

	// ========================================
	// POWER BALANCE (situational awareness)
	// ========================================

	if len(balance.current) > 0 {
		fmt.Fprintf(&b, "CURRENT POWER BALANCE:\n")

		// Sort by SC count descending
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
			if ps.name == strings.ToUpper(power) {
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
		yourSCs := balance.current[strings.ToUpper(power)]
		for _, ps := range sorted {
			if ps.name == strings.ToUpper(power) {
				continue
			}
			if ps.scs >= 7 {
				fmt.Fprintf(&b, "  ⚠ %s is approaching victory (18 SCs needed). Coalition response may be warranted.\n", ps.name)
			} else if ps.delta >= 2 {
				fmt.Fprintf(&b, "  ⚠ %s gained %d SCs this year — rapid expansion.\n", ps.name, ps.delta)
			}
		}
		_ = yourSCs
		fmt.Fprintln(&b)
	}

	// ========================================
	// SECTION 1: WHAT YOU AGREE ON (cooperation baseline)
	// ========================================

	// Collect all consensus and constitutional rules across scopes
	var agreements []string
	var constitutionalRules []string
	for _, bc := range bilateralCtxs {
		for _, cs := range bc.ctx.ConsensusStatements {
			agreements = append(agreements, fmt.Sprintf("[with %s] %s", bc.name, cs.Content))
		}
		constitutionalRules = append(constitutionalRules, bc.ctx.ConstitutionalRules...)
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
	// SECTION 2: BILATERAL RELATIONS (cooperation-first per counterpart)
	// ========================================

	if len(bilateralCtxs) > 0 {
		fmt.Fprintf(&b, "BILATERAL RELATIONS:\n\n")
		for _, bc := range bilateralCtxs {
			fmt.Fprintf(&b, "  === With %s ===\n", bc.name)

			// Lead with compromise/ZOPA (what's possible)
			if bc.ctx.CompromiseProposal != "" {
				proposal := bc.ctx.CompromiseProposal
				proposal = truncateRunes(proposal, 1000)
				fmt.Fprintf(&b, "  AVAILABLE COMPROMISE: %s\n", proposal)
			}

			// Bridging positions (already have bilateral support)
			if len(bc.ctx.BridgingStatements) > 0 {
				for _, bs := range bc.ctx.BridgingStatements {
					content := bs.Content
					content = truncateRunes(content, 500)
					fmt.Fprintf(&b, "  SHARED GROUND: %s (%.0f%% support)\n", content, bs.BridgingScore*100)
				}
			}

			// Cost of non-cooperation
			if len(bc.ctx.FailureScenarios) > 0 {
				fmt.Fprintf(&b, "  IF COOPERATION FAILS:\n")
				for _, f := range bc.ctx.FailureScenarios {
					fmt.Fprintf(&b, "    - %s\n", f)
				}
			}

			// Then remaining issues (framed as resolution opportunities)
			if len(bc.ctx.RelevantCruxes) > 0 {
				fmt.Fprintf(&b, "  ISSUES TO RESOLVE (resolving these creates mutual benefit):\n")
				for _, c := range bc.ctx.RelevantCruxes {
					fmt.Fprintf(&b, "    • %s\n", c.Claim)
					if c.Explanation != "" {
						expl := c.Explanation
						expl = truncateRunes(expl, 500)
						fmt.Fprintf(&b, "      %s\n", expl)
					}
				}
			} else {
				fmt.Fprintf(&b, "  No unresolved issues — this relationship is in good standing.\n")
			}

			// Rule violations (trust damage)
			if len(bc.ctx.RuleViolations) > 0 {
				fmt.Fprintf(&b, "  WARNING — TRUST CONCERN: Prior agreements may have been violated:\n")
				for _, v := range bc.ctx.RuleViolations {
					fmt.Fprintf(&b, "    - %s\n", v)
				}
			}

			fmt.Fprintln(&b)
		}
	}

	// ========================================
	// SECTION 3: PUBLIC LANDSCAPE (from global assembly)
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
	// SECTION 4: ALLIANCE COORDINATION
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
	// SECTION 5: COOPERATIVE PATTERNS AND GUIDANCE
	// ========================================

	// Emergent norms (cooperative behaviors that worked)
	var norms []string
	for _, bc := range bilateralCtxs {
		norms = append(norms, bc.ctx.EmergentNorms...)
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

	// Strategic nudge
	var stratParts []string
	if globalCtx != nil && globalCtx.StrategicNudge != "" {
		stratParts = append(stratParts, globalCtx.StrategicNudge)
	}
	for _, bc := range bilateralCtxs {
		if bc.ctx.StrategicNudge != "" {
			stratParts = append(stratParts, fmt.Sprintf("[%s] %s", bc.name, bc.ctx.StrategicNudge))
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
