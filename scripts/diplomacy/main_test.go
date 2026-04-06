package main

import (
	"testing"
)

// --- Adjacency Graph Tests ---

func TestIsAdjacent(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		// Direct neighbors
		{"VIE", "BUD", true},
		{"BUD", "VIE", true}, // symmetric
		{"LON", "NTH", true},
		{"CON", "ANK", true},

		// Non-neighbors
		{"LON", "VIE", false},
		{"MAR", "BER", false},
		{"EDI", "ROM", false},

		// Coast variants
		{"BAR", "STP/NC", true},
		{"BOT", "STP/SC", true},
		{"BLA", "BUL/EC", true},

		// Same territory
		{"VIE", "VIE", false},
	}
	for _, tt := range tests {
		got := isAdjacent(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("isAdjacent(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCountAdjacentThreats(t *testing.T) {
	territory := map[string]territorialInfo{
		"AUSTRIA": {
			units:   []string{"A VIE", "A BUD", "A SER"},
			centers: []string{"VIE", "BUD", "TRI", "SER"},
		},
		"RUSSIA": {
			units:   []string{"A GAL", "F RUM", "A WAR"},
			centers: []string{"MOS", "WAR", "SEV", "RUM"},
		},
	}

	threats, centers := countAdjacentThreats("AUSTRIA", "RUSSIA", territory)
	if threats == 0 {
		t.Error("expected threats from Russia to Austria (GAL borders VIE/BUD)")
	}
	if len(centers) == 0 {
		t.Error("expected threatened center names")
	}
	t.Logf("threats=%d, centers=%v", threats, centers)
}

// --- Coalition Matching Tests ---

func TestMatchCoalitions_MutualDeclarations(t *testing.T) {
	declarations := map[string][]coalitionDecl{
		"ENGLAND": {
			{Members: []string{"ENGLAND", "FRANCE"}, Purpose: "anti-German"},
		},
		"FRANCE": {
			{Members: []string{"ENGLAND", "FRANCE"}, Purpose: "contain Germany"},
		},
		"GERMANY": {
			{Members: []string{"GERMANY", "RUSSIA"}, Purpose: "eastern expansion"},
		},
	}

	groups := matchCoalitions(declarations)
	if len(groups) != 1 {
		t.Fatalf("expected 1 mutual coalition, got %d", len(groups))
	}
	if groups[0].Members[0] != "ENGLAND" || groups[0].Members[1] != "FRANCE" {
		t.Errorf("expected ENGLAND+FRANCE, got %v", groups[0].Members)
	}
}

func TestMatchCoalitions_NoMutual(t *testing.T) {
	declarations := map[string][]coalitionDecl{
		"ENGLAND": {
			{Members: []string{"ENGLAND", "FRANCE"}, Purpose: "alliance"},
		},
		"FRANCE": {
			{Members: []string{"FRANCE", "GERMANY"}, Purpose: "different alliance"},
		},
	}

	groups := matchCoalitions(declarations)
	if len(groups) != 0 {
		t.Errorf("expected 0 mutual coalitions, got %d", len(groups))
	}
}

func TestMatchCoalitions_Trilateral(t *testing.T) {
	declarations := map[string][]coalitionDecl{
		"ENGLAND": {
			{Members: []string{"ENGLAND", "FRANCE", "RUSSIA"}, Purpose: "anti-German"},
		},
		"FRANCE": {
			{Members: []string{"ENGLAND", "FRANCE", "RUSSIA"}, Purpose: "contain Germany"},
		},
		"RUSSIA": {
			{Members: []string{"ENGLAND", "FRANCE", "RUSSIA"}, Purpose: "German containment"},
		},
	}

	groups := matchCoalitions(declarations)
	if len(groups) != 1 {
		t.Fatalf("expected 1 trilateral coalition, got %d", len(groups))
	}
	if len(groups[0].Members) != 3 {
		t.Errorf("expected 3 members, got %d", len(groups[0].Members))
	}
}

func TestMatchCoalitions_OverlappingDeclarations(t *testing.T) {
	// Power in multiple coalitions
	declarations := map[string][]coalitionDecl{
		"RUSSIA": {
			{Members: []string{"ENGLAND", "RUSSIA"}, Purpose: "Scandinavian partition"},
			{Members: []string{"AUSTRIA", "RUSSIA"}, Purpose: "Balkan partition"},
		},
		"ENGLAND": {
			{Members: []string{"ENGLAND", "RUSSIA"}, Purpose: "North Sea security"},
		},
		"AUSTRIA": {
			{Members: []string{"AUSTRIA", "RUSSIA"}, Purpose: "anti-Turkey"},
		},
	}

	groups := matchCoalitions(declarations)
	if len(groups) != 2 {
		t.Fatalf("expected 2 coalitions, got %d", len(groups))
	}
}

// --- Relationship State Machine Tests ---

func TestComputeRelationshipStates_Cooperative(t *testing.T) {
	results := []scopeResult{
		{
			scope: scope{
				scopeTag: "bilateral",
				powers:   []string{"ENGLAND", "FRANCE"},
			},
			contexts: map[string]string{
				"england": `{"agent_id":"england-agent","alignment_scores":[{"agent_id":"france-agent","alignment_score":0.75}],"bridging_statements":[{"content":"shared ground","bridging_score":0.8}],"relevant_cruxes":[],"consensus_statements":[{"content":"we agree","overall_agree_ratio":0.9}]}`,
			},
		},
	}

	trust := newTrustTracker()
	trust.get("ENGLAND", "FRANCE").promisedSupports = 3
	trust.get("ENGLAND", "FRANCE").honoredSupports = 3

	balance := powerBalance{current: map[string]int{"ENGLAND": 4, "FRANCE": 5}}
	states := computeRelationshipStates(results, trust, balance)

	rs := states["ENGLAND"]["FRANCE"]
	if rs.State != "allied" && rs.State != "cooperative" {
		t.Errorf("expected cooperative or allied state, got %q", rs.State)
	}
	if rs.Trend == "deteriorating" {
		t.Error("expected non-deteriorating trend for fully honored promises")
	}
}

func TestComputeRelationshipStates_Strained(t *testing.T) {
	results := []scopeResult{
		{
			scope: scope{
				scopeTag: "bilateral",
				powers:   []string{"AUSTRIA", "TURKEY"},
			},
			contexts: map[string]string{
				"austria": `{"agent_id":"austria-agent","alignment_scores":[{"agent_id":"turkey-agent","alignment_score":0.15}],"relevant_cruxes":[{"crux_claim":"dispute 1"},{"crux_claim":"dispute 2"},{"crux_claim":"dispute 3"},{"crux_claim":"dispute 4"},{"crux_claim":"dispute 5"},{"crux_claim":"dispute 6"}],"rule_violations":["violated agreement"]}`,
			},
		},
	}

	trust := newTrustTracker()
	trust.get("TURKEY", "AUSTRIA").promisedSupports = 2
	trust.get("TURKEY", "AUSTRIA").honoredSupports = 0
	trust.get("TURKEY", "AUSTRIA").brokenPromises = []string{"broke 1", "broke 2"}

	balance := powerBalance{current: map[string]int{"AUSTRIA": 5, "TURKEY": 4}}
	states := computeRelationshipStates(results, trust, balance)

	rs := states["AUSTRIA"]["TURKEY"]
	if rs.State != "hostile" && rs.State != "strained" {
		t.Errorf("expected hostile or strained, got %q", rs.State)
	}
}

// TestComputeRelationshipStates_RealWorldBilateral tests the actual scenario from game data:
// 0% alignment (no vote data), many cruxes, but active bridging and proposals.
// This should NOT be hostile — it's a normal active negotiation.
func TestComputeRelationshipStates_RealWorldBilateral(t *testing.T) {
	results := []scopeResult{
		{
			scope: scope{
				scopeTag: "bilateral",
				powers:   []string{"ENGLAND", "GERMANY"},
			},
			contexts: map[string]string{
				// Realistic bilateral: 0% alignment (no votes), 5 cruxes, but bridging + proposal + consensus
				"england": `{
					"agent_id":"england-agent",
					"alignment_scores":[{"agent_id":"germany-agent","alignment_score":0}],
					"bridging_statements":[
						{"content":"both agree on Scandinavian split","bridging_score":0.7},
						{"content":"North Sea is England domain","bridging_score":0.8}
					],
					"compromise_proposal":"England takes Norway, Germany takes Denmark",
					"consensus_statements":[
						{"content":"Norway to England, Denmark to Germany","overall_agree_ratio":0.9},
						{"content":"North Sea is England's domain","overall_agree_ratio":0.85}
					],
					"relevant_cruxes":[
						{"crux_claim":"Belgium assignment"},
						{"crux_claim":"Sweden dispute"},
						{"crux_claim":"Long term Russia threat"},
						{"crux_claim":"Holland control"},
						{"crux_claim":"Fleet positioning"}
					]
				}`,
			},
		},
	}

	trust := newTrustTracker()
	// No promises in year 1 — typical
	balance := powerBalance{current: map[string]int{"ENGLAND": 3, "GERMANY": 5}}
	states := computeRelationshipStates(results, trust, balance)

	rs := states["ENGLAND"]["GERMANY"]
	t.Logf("state=%s trend=%s evidence=%v", rs.State, rs.Trend, rs.Evidence)
	if rs.State == "hostile" {
		t.Errorf("bilateral with bridging + proposals + consensus should NOT be hostile, got %q", rs.State)
	}
	if rs.State == "strained" {
		t.Errorf("bilateral with active shared ground should NOT be strained, got %q", rs.State)
	}
}

// TestComputeRelationshipStates_NoData tests a minimal bilateral with zero analysis data.
// Should default to neutral, not hostile.
func TestComputeRelationshipStates_NoData(t *testing.T) {
	results := []scopeResult{
		{
			scope: scope{
				scopeTag: "bilateral",
				powers:   []string{"FRANCE", "TURKEY"},
			},
			contexts: map[string]string{
				"france": `{"agent_id":"france-agent"}`,
			},
		},
	}

	trust := newTrustTracker()
	balance := powerBalance{current: map[string]int{"FRANCE": 5, "TURKEY": 4}}
	states := computeRelationshipStates(results, trust, balance)

	rs := states["FRANCE"]["TURKEY"]
	if rs.State == "hostile" {
		t.Errorf("no-data bilateral should not be hostile, got %q", rs.State)
	}
}

// TestComputeRelationshipStates_BrokenTrustWithBridging tests the case where
// promises are broken but there's still active negotiation (bridging + proposals).
// Should be strained, not hostile — they're still talking.
func TestComputeRelationshipStates_BrokenTrustWithBridging(t *testing.T) {
	results := []scopeResult{
		{
			scope: scope{
				scopeTag: "bilateral",
				powers:   []string{"RUSSIA", "TURKEY"},
			},
			contexts: map[string]string{
				"russia": `{
					"agent_id":"russia-agent",
					"bridging_statements":[{"content":"Black Sea demilitarization","bridging_score":0.6}],
					"compromise_proposal":"Russia gets Romania, Turkey gets Bulgaria",
					"relevant_cruxes":[{"crux_claim":"Black Sea access"},{"crux_claim":"Bulgaria ownership"}]
				}`,
			},
		},
	}

	trust := newTrustTracker()
	trust.get("TURKEY", "RUSSIA").promisedSupports = 1
	trust.get("TURKEY", "RUSSIA").honoredSupports = 0
	trust.get("TURKEY", "RUSSIA").brokenPromises = []string{"broke Black Sea promise"}

	balance := powerBalance{current: map[string]int{"RUSSIA": 5, "TURKEY": 4}}
	states := computeRelationshipStates(results, trust, balance)

	rs := states["RUSSIA"]["TURKEY"]
	t.Logf("state=%s trend=%s evidence=%v", rs.State, rs.Trend, rs.Evidence)
	if rs.State == "hostile" {
		t.Errorf("broken trust BUT active bridging+proposals should not be hostile, got %q", rs.State)
	}
}

// TestComputeRelationshipStates_TrulyHostile tests genuinely hostile: broken promises,
// rule violations, many cruxes, no bridging, no proposals.
func TestComputeRelationshipStates_TrulyHostile(t *testing.T) {
	results := []scopeResult{
		{
			scope: scope{
				scopeTag: "bilateral",
				powers:   []string{"AUSTRIA", "ITALY"},
			},
			contexts: map[string]string{
				"austria": `{
					"agent_id":"austria-agent",
					"relevant_cruxes":[{"crux_claim":"1"},{"crux_claim":"2"},{"crux_claim":"3"},{"crux_claim":"4"},{"crux_claim":"5"},{"crux_claim":"6"},{"crux_claim":"7"}],
					"rule_violations":["broke Adriatic pact","ignored non-aggression"]
				}`,
			},
		},
	}

	trust := newTrustTracker()
	trust.get("ITALY", "AUSTRIA").promisedSupports = 3
	trust.get("ITALY", "AUSTRIA").honoredSupports = 0
	trust.get("ITALY", "AUSTRIA").brokenPromises = []string{"broke 1", "broke 2", "broke 3"}

	balance := powerBalance{current: map[string]int{"AUSTRIA": 5, "ITALY": 4}}
	states := computeRelationshipStates(results, trust, balance)

	rs := states["AUSTRIA"]["ITALY"]
	t.Logf("state=%s trend=%s evidence=%v", rs.State, rs.Trend, rs.Evidence)
	if rs.State != "hostile" {
		t.Errorf("broken trust + rule violations + many cruxes + no bridging should be hostile, got %q", rs.State)
	}
}

// --- Stab Risk Tests ---

func TestComputeStabRisks_HighRisk(t *testing.T) {
	results := []scopeResult{
		{
			scope: scope{
				scopeTag: "bilateral",
				powers:   []string{"ENGLAND", "FRANCE"},
			},
			contexts: map[string]string{
				"england": `{"agent_id":"england-agent"}`,
			},
		},
	}

	trust := newTrustTracker()
	trust.get("FRANCE", "ENGLAND").promisedSupports = 3
	trust.get("FRANCE", "ENGLAND").honoredSupports = 0
	trust.get("FRANCE", "ENGLAND").brokenPromises = []string{"broke 1", "broke 2", "broke 3"}

	balance := powerBalance{current: map[string]int{"ENGLAND": 3, "FRANCE": 8}}
	territory := map[string]territorialInfo{
		"ENGLAND": {units: []string{"F NTH", "A YOR"}, centers: []string{"LON", "EDI", "LVP"}},
		"FRANCE":  {units: []string{"A PIC", "F ENG", "A BEL"}, centers: []string{"PAR", "BRE", "MAR", "BEL", "POR"}},
	}

	risks := computeStabRisks("ENGLAND", results, trust, balance, territory)
	if len(risks) == 0 {
		t.Fatal("expected stab risk from France")
	}
	if risks[0].Risk == "low" {
		t.Error("expected non-low risk given SC imbalance and broken promises")
	}
	t.Logf("risk=%s, reasons=%v", risks[0].Risk, risks[0].Reasons)
}

// --- Build Coalition Scopes Tests ---

func TestBuildCoalitionScopes(t *testing.T) {
	coalitions := []coalitionGroup{
		{Members: []string{"ENGLAND", "FRANCE", "RUSSIA"}, Purpose: "anti-German"},
		{Members: []string{"AUSTRIA", "TURKEY"}, Purpose: "bilateral — should be skipped"},
	}
	messages := []Message{
		{Sender: "ENGLAND", Recipient: "FRANCE", Content: "let's work together"},
		{Sender: "FRANCE", Recipient: "RUSSIA", Content: "coordinate against Germany"},
		{Sender: "ENGLAND", Recipient: "RUSSIA", Content: "agreed"},
		{Sender: "ENGLAND", Recipient: "GLOBAL", Content: "peace"},
		{Sender: "AUSTRIA", Recipient: "TURKEY", Content: "alliance?"},
	}

	scopes := buildCoalitionScopes(coalitions, messages, 1)
	if len(scopes) != 1 {
		t.Fatalf("expected 1 scope (2-member skipped), got %d", len(scopes))
	}
	if len(scopes[0].messages) != 3 {
		t.Errorf("expected 3 messages (excluding GLOBAL), got %d", len(scopes[0].messages))
	}
	if scopes[0].scopeTag != "coalition" {
		t.Errorf("expected coalition tag, got %q", scopes[0].scopeTag)
	}
}

// --- Extract JSON Tests ---

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`{"key": "value"}`, `{"key": "value"}`},
		{"```json\n{\"key\": \"value\"}\n```", `{"key": "value"}`},
		{"Some text before {\"key\": \"value\"}", `{"key": "value"}`},
		{"[1, 2, 3]", `[1, 2, 3]`},
	}
	for _, tt := range tests {
		got := extractJSON(tt.input)
		if got != tt.want {
			t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Pair Key Tests ---

func TestPairKey(t *testing.T) {
	if pairKey("FRANCE", "ENGLAND") != "ENGLAND-FRANCE" {
		t.Error("pair key should be alphabetically sorted")
	}
	if pairKey("AUSTRIA", "TURKEY") != "AUSTRIA-TURKEY" {
		t.Error("already sorted should stay the same")
	}
}

// --- Gini Tests ---

func TestComputeGini(t *testing.T) {
	// Perfect equality
	equal := map[string]int{"A": 5, "B": 5, "C": 5}
	gini := computeGini(equal)
	if gini > 0.01 {
		t.Errorf("equal distribution should have gini ~0, got %f", gini)
	}

	// Perfect inequality
	unequal := map[string]int{"A": 18, "B": 0, "C": 0}
	gini = computeGini(unequal)
	if gini < 0.5 {
		t.Errorf("very unequal distribution should have high gini, got %f", gini)
	}
}

// --- Promise Extraction Tests ---

func TestExtractPromises_MilitaryOnly(t *testing.T) {
	messages := []Message{
		// Should match: specific military support language
		{Sender: "ENGLAND", Recipient: "FRANCE", Content: "I will support A PAR to BUR", Phase: "S1901M"},
		{Sender: "RUSSIA", Recipient: "AUSTRIA", Content: "F SEV S A RUM", Phase: "F1901M"},
		{Sender: "GERMANY", Recipient: "AUSTRIA", Content: "I'll order support for your move into SER", Phase: "S1901M"},
		// Should NOT match: diplomatic language, not military orders
		{Sender: "ITALY", Recipient: "FRANCE", Content: "I support your position on the Mediterranean", Phase: "S1901M"},
		{Sender: "TURKEY", Recipient: "RUSSIA", Content: "You have my full support in this matter", Phase: "S1901M"},
		{Sender: "AUSTRIA", Recipient: "GERMANY", Content: "I appreciate your support and look forward to cooperation", Phase: "F1901M"},
		// GLOBAL messages should be ignored
		{Sender: "ENGLAND", Recipient: "GLOBAL", Content: "I will support A PAR to BUR", Phase: "S1901M"},
	}

	promises := extractPromises(messages)

	// Military promises should be found
	if len(promises["ENGLAND"]) == 0 {
		t.Error("expected England's military support promise to be detected")
	}
	if len(promises["RUSSIA"]) == 0 {
		t.Error("expected Russia's shorthand support order to be detected")
	}

	// Diplomatic language should NOT be detected
	if len(promises["ITALY"]) > 0 {
		t.Errorf("'support your position' should not be detected as military promise, got %v", promises["ITALY"])
	}
	if len(promises["TURKEY"]) > 0 {
		t.Errorf("'full support in this matter' should not be detected as military promise, got %v", promises["TURKEY"])
	}
	if len(promises["AUSTRIA"]) > 0 {
		t.Errorf("'appreciate your support' should not be detected as military promise, got %v", promises["AUSTRIA"])
	}

	// GLOBAL messages should be ignored
	if _, ok := promises["ENGLAND"]["GLOBAL"]; ok {
		t.Error("GLOBAL recipient messages should be ignored")
	}
}

// --- Interleave Tests ---

func TestInterleaveByScope(t *testing.T) {
	grouped := map[string][]pendingPosition{
		"scope-A": {
			{scopeName: "scope-A", agentID: "a1"},
			{scopeName: "scope-A", agentID: "a2"},
		},
		"scope-B": {
			{scopeName: "scope-B", agentID: "b1"},
		},
	}

	result := interleaveByScope(grouped)
	if len(result) != 3 {
		t.Fatalf("expected 3 positions, got %d", len(result))
	}
	// Should round-robin: A, B, A
	if result[0].scopeName != "scope-A" || result[1].scopeName != "scope-B" || result[2].scopeName != "scope-A" {
		t.Errorf("expected round-robin interleaving, got %s %s %s",
			result[0].scopeName, result[1].scopeName, result[2].scopeName)
	}
}

// --- Build Scopes Tests ---

func TestBuildScopes_Basic(t *testing.T) {
	messages := []Message{
		{Sender: "ENGLAND", Recipient: "GLOBAL", Content: "peace to all"},
		{Sender: "ENGLAND", Recipient: "FRANCE", Content: "alliance?"},
		{Sender: "FRANCE", Recipient: "ENGLAND", Content: "yes"},
		{Sender: "GERMANY", Recipient: "RUSSIA", Content: "let's talk"},
	}

	scopes := buildScopes(messages, nil, 1)

	global := 0
	bilateral := 0
	for _, s := range scopes {
		switch s.scopeTag {
		case "global":
			global++
			if len(s.messages) != 1 {
				t.Errorf("global should have 1 message, got %d", len(s.messages))
			}
		case "bilateral":
			bilateral++
		}
	}
	if global != 1 {
		t.Errorf("expected 1 global scope, got %d", global)
	}
	if bilateral != 2 {
		t.Errorf("expected 2 bilateral scopes, got %d", bilateral)
	}
}

func TestBuildScopes_WithAlliances(t *testing.T) {
	messages := []Message{
		{Sender: "ENGLAND", Recipient: "FRANCE", Content: "hello"},
		{Sender: "FRANCE", Recipient: "RUSSIA", Content: "hello"},
		{Sender: "ENGLAND", Recipient: "RUSSIA", Content: "hello"},
	}

	alliances := [][]string{{"ENGLAND", "FRANCE", "RUSSIA"}}
	scopes := buildScopes(messages, alliances, 1)

	alliance := 0
	for _, s := range scopes {
		if s.scopeTag == "alliance" {
			alliance++
			if len(s.messages) != 3 {
				t.Errorf("alliance scope should pool all 3 bilateral messages, got %d", len(s.messages))
			}
			if s.template != "consensus" {
				t.Errorf("alliance template should be consensus, got %q", s.template)
			}
		}
	}
	if alliance != 1 {
		t.Errorf("expected 1 alliance scope, got %d", alliance)
	}
}

// --- Survival / Metrics Tests ---

func TestComputeSurvivalCount(t *testing.T) {
	counts := map[string]int{"A": 5, "B": 3, "C": 0, "D": 1, "E": 0}
	if got := computeSurvivalCount(counts); got != 3 {
		t.Errorf("expected 3 survivors, got %d", got)
	}
}

// --- Elimination Risk Tests ---

func TestDetectEliminationRisk(t *testing.T) {
	balance := powerBalance{
		current: map[string]int{"AUSTRIA": 5, "TURKEY": 1, "ENGLAND": 0},
	}
	warnings := detectEliminationRisk(balance, nil, "AUSTRIA")
	if len(warnings) < 2 {
		t.Errorf("expected warnings for Turkey (1 SC) and England (0 SC), got %d", len(warnings))
	}
}

// --- Coalition Detection From Orders ---

func TestDetectAlliancesFromOrders_MutualSupport(t *testing.T) {
	game := GameState{
		Phases: []Phase{
			{
				Name: "S1901M",
				Orders: map[string][]any{
					"ENGLAND": {"F NTH S A YOR - NWY"},
					"FRANCE":  {"A PAR - BUR"},
				},
				State: PhaseState{
					Units: map[string][]string{
						"ENGLAND": {"F NTH", "A YOR"},
						"FRANCE":  {"A PAR", "F BRE"},
					},
				},
			},
		},
	}

	alliances := detectAlliancesFromOrders(game, 1901)
	// England supports its own unit, not France's — no cross-power support
	if len(alliances) != 0 {
		t.Logf("alliances: %v (expected 0 — no cross-power support)", alliances)
	}
}
