package tests

// Byzantine adversarial test suite — DARPA-PS-26-09 Track 1 Task 5.
//
// Each scenario wires the real analysis pipeline (TextAnalyzer +
// reputation.Weigher) against a real Postgres schema via tempDB, with
// a deterministic Byzantine-flavoured mockLLM. We measure concrete
// metrics (honest/Sybil effective-weight ratio, consensus inclusion,
// crux inclusion) and log them via t.Log for citation in the DARPA
// bid. Failure modes at f >= n/3 are documented rather than papered
// over.
//
// Harness contract: byzantineHarness exposes a single-shot Analyze
// helper that runs claim extraction + crux + consensus + reputation
// update, returning the AnalysisResult and the effective-weight map.
// Tests then assert metrics on that map.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
	"github.com/justinstimatze/gemot/internal/reputation"
	"github.com/justinstimatze/gemot/internal/store"
)

// byzantineHarness composes the real analysis stack against a live
// Postgres schema with reputation enabled. Use for any test that needs
// to observe effective weights or reputation state under attack.
type byzantineHarness struct {
	t        *testing.T
	db       *store.DB
	weigher  *reputation.Weigher
	analyzer *analysis.TextAnalyzer
}

// newByzantineHarness spins up a fresh Postgres schema + wired analyzer
// with reputation on. ColdThreshold is configurable per scenario: tests
// covering cold-start flooding want threshold=5 (default), tests
// covering graduation cliff want threshold low enough to exercise
// graduation within the test run.
func newByzantineHarness(t *testing.T, coldThreshold int, mock llm.StructuredOutputFunc) *byzantineHarness {
	t.Helper()
	db := tempDB(t)
	cfg := reputation.Config{
		Enabled:       true,
		ColdCap:       0.1,
		ColdThreshold: coldThreshold,
		Iterations:    50,
	}
	w := reputation.NewWeigher(db, cfg)
	if w == nil {
		t.Fatal("weigher must be non-nil when enabled")
	}
	a := analysis.NewTextAnalyzerWithFunc(mock)
	a.Reputation = w
	return &byzantineHarness{t: t, db: db, weigher: w, analyzer: a}
}

// runRound performs a single analysis pass and then feeds the resulting
// cruxes back into the reputation weigher, simulating what the real
// service layer does after SaveAnalysisResult.
func (h *byzantineHarness) runRound(
	ctx context.Context,
	positions []deliberation.Position,
	votes []deliberation.Vote,
	agents []string,
) *deliberation.AnalysisResult {
	h.t.Helper()
	result, err := h.analyzer.Analyze(ctx, positions, votes, agents)
	if err != nil {
		h.t.Fatalf("analyze: %v", err)
	}
	authors := map[string]string{}
	for _, p := range positions {
		authors[p.ID] = p.AgentID
	}
	if err := h.weigher.UpdateFromRound(ctx, "", false, result.Cruxes, authors, nil); err != nil {
		h.t.Fatalf("UpdateFromRound: %v", err)
	}
	return result
}

// weightRatio returns sum(weights[honest]) / sum(weights[sybil]).
// Returns +Inf when Sybil weight is zero. The cold-start guarantee is
// that this ratio is bounded below by a constant times the cohort
// composition — a metric the DARPA bid cites empirically.
func weightRatio(effective map[string]float64, honest, sybil []string) float64 {
	var h, s float64
	for _, a := range honest {
		h += effective[a]
	}
	for _, a := range sybil {
		s += effective[a]
	}
	if s == 0 {
		return math.Inf(1)
	}
	return h / s
}

// inclusionRate returns the fraction of the target agents that appear
// in at least one crux's AgreeAgents ∪ DisagreeAgents list.
func inclusionRate(cruxes []deliberation.Crux, targets []string) float64 {
	if len(targets) == 0 {
		return 0
	}
	seen := map[string]bool{}
	for _, c := range cruxes {
		for _, a := range c.AgreeAgents {
			seen[a] = true
		}
		for _, a := range c.DisagreeAgents {
			seen[a] = true
		}
	}
	hit := 0
	for _, a := range targets {
		if seen[a] {
			hit++
		}
	}
	return float64(hit) / float64(len(targets))
}

// consensusHonestShare returns the fraction of ConsensusStatements
// authored by honest (non-Sybil) agents. A low share at high f
// documents the silencing effect.
func consensusHonestShare(result *deliberation.AnalysisResult, honestSet map[string]bool) float64 {
	if len(result.ConsensusStatements) == 0 {
		return 0
	}
	honest := 0
	for _, s := range result.ConsensusStatements {
		// ConsensusStatement has no explicit agent field — map back via
		// positions. The test builds a positionAuthor map outside and
		// asserts post-hoc; here we approximate via content prefix which
		// the mock sets up to include the agent name.
		for agent := range honestSet {
			if strings.Contains(s.Content, "["+agent+"]") {
				honest++
				break
			}
		}
	}
	return float64(honest) / float64(len(result.ConsensusStatements))
}

// byzantineMockLLM returns a structured-output func that emits
// deterministic analysis for the Byzantine scenarios. Each participant
// gets one claim whose content reflects positionType(i):
//
//	"honest"     → tight-governance claim
//	"sybil"      → loose-governance claim
//	"moderate"   → balanced/framing claim
//	"low_effort" → zero claims (triggers COVERAGE/LOW_EFFORT_ABS)
//
// Dedup is pass-through (one group per input claim), so speaker
// identity is preserved into the crux stage. The crux step parses
// participant numbers out of the prompt and partitions them by their
// type: honest/moderate on the agree side, sybil on disagree.
//
// Content-agnostic agreement fallback ("no votes" path) is also
// handled so scenarios that don't supply votes still complete without
// a noisy WARN log.
func byzantineMockLLM(positionType func(i int) string) llm.StructuredOutputFunc {
	// formatClaimsForCrux and the dedup stage both emit
	// participant="N" XML attributes. speaker= is only used in a
	// handful of logging paths and doesn't appear in the prompts this
	// mock sees, so we match only participant=.
	participantRE := regexp.MustCompile(`participant="(\d+)"`)

	return func(_ context.Context, system, prompt string, schema map[string]any, target any) error {
		switch {
		case strings.Contains(prompt, "break down the information"):
			return json.Unmarshal([]byte(`{
				"topics": [{
					"topic_name": "Governance",
					"topic_description": "How to govern the system.",
					"subtopics": [{
						"subtopic_name": "Approach",
						"subtopic_description": "Tight vs loose governance."
					}]
				}]
			}`), target)

		case strings.Contains(prompt, "extract the most important concise claims"):
			idx := -1
			if i := strings.Index(prompt, "Participant "); i >= 0 {
				fmt.Sscanf(prompt[i:], "Participant %d", &idx)
			}
			if idx < 0 {
				return json.Unmarshal([]byte(`{"claims": []}`), target)
			}
			switch positionType(idx) {
			case "low_effort":
				return json.Unmarshal([]byte(`{"claims": []}`), target)
			case "sybil":
				return json.Unmarshal([]byte(`{"claims": [
					{"claim": "Governance should be loose and minimal",
					 "quote": "loose governance is best",
					 "topic_name": "Governance", "subtopic_name": "Approach"}
				]}`), target)
			case "moderate":
				return json.Unmarshal([]byte(`{"claims": [
					{"claim": "A balanced pragmatic approach is best",
					 "quote": "balanced pragmatic approach",
					 "topic_name": "Governance", "subtopic_name": "Approach"}
				]}`), target)
			default:
				return json.Unmarshal([]byte(`{"claims": [
					{"claim": "Governance should be tight with real enforcement",
					 "quote": "tight enforcement is required",
					 "topic_name": "Governance", "subtopic_name": "Approach"}
				]}`), target)
			}

		case strings.Contains(prompt, "grouping claims"):
			// Pass-through dedup: one group per input claim, preserving
			// participant identity for the crux stage.
			var groups []map[string]any
			matches := participantRE.FindAllStringSubmatch(prompt, -1)
			claimIDRE := regexp.MustCompile(`<claim id="(\d+)"`)
			idMatches := claimIDRE.FindAllStringSubmatch(prompt, -1)
			for i, m := range matches {
				id := i
				if i < len(idMatches) {
					fmt.Sscanf(idMatches[i][1], "%d", &id)
				}
				groups = append(groups, map[string]any{
					"claim_text":         fmt.Sprintf("Claim from P%s", m[1]),
					"original_claim_ids": []int{id},
				})
			}
			out, _ := json.Marshal(map[string]any{"groups": groups})
			return json.Unmarshal(out, target)

		case strings.Contains(prompt, "maximally controversial statement"):
			// Partition participants present in the prompt by positionType.
			var agree, disagree []string
			seen := map[string]bool{}
			for _, m := range participantRE.FindAllStringSubmatch(prompt, -1) {
				if seen[m[1]] {
					continue
				}
				seen[m[1]] = true
				var idx int
				fmt.Sscanf(m[1], "%d", &idx)
				switch positionType(idx) {
				case "sybil":
					disagree = append(disagree, m[1])
				default:
					agree = append(agree, m[1])
				}
			}
			out := fmt.Sprintf(`{
				"crux_claim": "Governance should be tight with real enforcement",
				"agree": %s,
				"disagree": %s,
				"no_clear_position": [],
				"explanation": "Tight-governance honest participants vs loose-governance Sybils."
			}`, jsonStrs(agree), jsonStrs(disagree))
			return json.Unmarshal([]byte(out), target)

		case strings.Contains(prompt, "Generate a detailed summary"):
			return json.Unmarshal([]byte(`{"summary": "Participants split on governance tightness."}`), target)

		case strings.Contains(prompt, "no votes"), strings.Contains(prompt, "identify agreement and potential compromises"):
			// No-votes agreement-detection fallback (agreementPrompt).
			// Return an empty consensus set — enough to keep the pipeline
			// quiet without forcing a particular outcome.
			return json.Unmarshal([]byte(`{"consensus": [], "bridging": []}`), target)

		default:
			return fmt.Errorf("unexpected prompt (first 80 chars): %s",
				prompt[:min(80, len(prompt))])
		}
	}
}

// jsonStrs renders a slice of strings as a JSON array literal.
func jsonStrs(s []string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// positionSet builds n positions alternating honest/Sybil by test
// intent. pickType(i) decides each agent's role.
func positionSet(
	prefix string,
	round int,
	agents []string,
	contentFor func(i int, agent string) string,
) []deliberation.Position {
	out := make([]deliberation.Position, len(agents))
	for i, a := range agents {
		out[i] = deliberation.Position{
			ID:             fmt.Sprintf("%s-p%d-r%d", prefix, i, round),
			DeliberationID: "byzantine-test",
			AgentID:        a,
			Content:        contentFor(i, a),
			Round:          round,
		}
	}
	return out
}

// TestByzantineSybilRing_f_lt_quarter: 4 agents, 1 Sybil. Protocol is
// well within the f < n/3 tolerance and must produce correct consensus.
func TestByzantineSybilRing_f_lt_quarter(t *testing.T) {
	agents := []string{"honest-0", "honest-1", "honest-2", "sybil-3"}
	honest := agents[:3]
	sybil := agents[3:]
	types := func(i int) string {
		if i == 3 {
			return "sybil"
		}
		return "honest"
	}
	h := newByzantineHarness(t, 5, byzantineMockLLM(types))
	positions := positionSet("r1", 1, agents, func(i int, a string) string {
		if types(i) == "sybil" {
			return "Loose governance, minimal enforcement."
		}
		return "Tight governance with real enforcement and verifiable rules."
	})
	result := h.runRound(context.Background(), positions, nil, agents)

	ratio := weightRatio(result.EffectiveWeights, honest, sybil)
	t.Logf("[f<1/4] honest/sybil effective-weight ratio = %.3f (honest=%d, sybil=%d)",
		ratio, len(honest), len(sybil))
	if ratio < 1.0 {
		t.Fatalf("honest weight must dominate at f<1/4, got ratio=%.3f", ratio)
	}

	cruxInc := inclusionRate(result.Cruxes, honest)
	t.Logf("[f<1/4] honest crux inclusion rate = %.2f", cruxInc)
	if cruxInc < 1.0 {
		t.Fatalf("every honest agent must appear in some crux at f<1/4, got %.2f", cruxInc)
	}
}

// TestByzantineSybilRing_f_eq_third: 6 agents, 2 Sybils. This is the
// theoretical limit for f < n/3. Expect the protocol to degrade but
// remain usable. We check that honest weight still exceeds Sybil weight
// under cold-start cap, and we record the exact ratio for METRICS.md.
func TestByzantineSybilRing_f_eq_third(t *testing.T) {
	agents := []string{"honest-0", "honest-1", "honest-2", "honest-3", "sybil-4", "sybil-5"}
	honest := agents[:4]
	sybil := agents[4:]
	types := func(i int) string {
		if i >= 4 {
			return "sybil"
		}
		return "honest"
	}
	h := newByzantineHarness(t, 5, byzantineMockLLM(types))
	positions := positionSet("r1", 1, agents, func(i int, a string) string {
		if types(i) == "sybil" {
			return "Loose governance, minimal enforcement."
		}
		return "Tight governance with real enforcement and verifiable rules."
	})
	result := h.runRound(context.Background(), positions, nil, agents)

	ratio := weightRatio(result.EffectiveWeights, honest, sybil)
	t.Logf("[f=1/3] honest/sybil effective-weight ratio = %.3f (honest=%d, sybil=%d)",
		ratio, len(honest), len(sybil))
	// At the theoretical limit we still expect majority > minority under
	// cold-start cap. Both sides are cold, so the ratio tracks cohort
	// sizes: 4/2 = 2.0 in the cold-capped regime.
	if ratio < 1.5 {
		t.Fatalf("honest weight should still dominate at f=1/3, got ratio=%.3f", ratio)
	}
}

// TestByzantineSybilRing_f_over_half: 4 agents, 3 Sybils. Documented
// failure mode — when f > 1/2 the attacker numerically dominates. The
// cold-start cap still bounds their total weight proportional to cohort
// size, which is the most we can promise without additional defenses.
// We log, we do not fail: the point is to baseline the failure
// empirically.
func TestByzantineSybilRing_f_over_half(t *testing.T) {
	agents := []string{"honest-0", "sybil-1", "sybil-2", "sybil-3"}
	honest := agents[:1]
	sybil := agents[1:]
	types := func(i int) string {
		if i == 0 {
			return "honest"
		}
		return "sybil"
	}
	h := newByzantineHarness(t, 5, byzantineMockLLM(types))
	positions := positionSet("r1", 1, agents, func(i int, a string) string {
		if types(i) == "sybil" {
			return "Loose governance, minimal enforcement."
		}
		return "Tight governance with real enforcement and verifiable rules."
	})
	result := h.runRound(context.Background(), positions, nil, agents)

	ratio := weightRatio(result.EffectiveWeights, honest, sybil)
	t.Logf("[f>1/2] honest/sybil effective-weight ratio = %.3f (honest=%d, sybil=%d)",
		ratio, len(honest), len(sybil))
	t.Logf("[f>1/2] DOCUMENTED FAILURE: attacker majority dominates without verified identity")

	// Sanity: cold-cap still bounds Sybil aggregate to at most 3 * 0.1 = 0.3
	// within the pre-trust-weight multiplication. Record the raw sum for
	// METRICS.md.
	var sybilSum float64
	for _, a := range sybil {
		sybilSum += result.EffectiveWeights[a]
	}
	t.Logf("[f>1/2] sybil aggregate effective weight = %.3f (all cold-capped)", sybilSum)
}

// TestByzantineColdStartFlooding: many fresh Sybils vs a few seasoned
// honest agents. The cold-start cap must bound total Sybil weight. We
// directly persist a seasoned reputation for the legit agents and a
// zero-survived-count state for the Sybils so the scenario doesn't
// depend on N rounds of setup.
func TestByzantineColdStartFlooding(t *testing.T) {
	const nSybil = 10
	legit := []string{"legit-0", "legit-1"}
	sybils := make([]string, nSybil)
	for i := range sybils {
		sybils[i] = fmt.Sprintf("sybil-flood-%d", i)
	}
	agents := append(append([]string{}, legit...), sybils...)

	types := func(i int) string {
		if i < len(legit) {
			return "honest"
		}
		return "sybil"
	}
	h := newByzantineHarness(t, 5, byzantineMockLLM(types))

	// Seed the legit agents as graduated with moderate scores.
	// Write-side reputation APIs take vertex-form strings (schema v4);
	// none of these agents have registered keys so they all resolve to
	// "id:<name>".
	ctx := context.Background()
	legitVertices := make([]string, len(legit))
	for i, a := range legit {
		legitVertices[i] = idV(a)
	}
	if err := h.db.IncrementSurvivedCounts(ctx, legitVertices); err != nil {
		t.Fatalf("seed legit: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := h.db.IncrementSurvivedCounts(ctx, legitVertices); err != nil {
			t.Fatalf("seed legit iter %d: %v", i, err)
		}
	}
	if err := h.db.PersistEigenTrustScores(ctx, map[string]float64{
		idV("legit-0"): 0.5, idV("legit-1"): 0.5,
	}); err != nil {
		t.Fatalf("persist seeds: %v", err)
	}

	positions := positionSet("r1", 1, agents, func(i int, a string) string {
		if types(i) == "sybil" {
			return "Loose governance, minimal enforcement — attacker flood."
		}
		return "Tight governance with real enforcement and verifiable rules."
	})
	result := h.runRound(ctx, positions, nil, agents)

	var legitSum, sybilSum float64
	for _, a := range legit {
		legitSum += result.EffectiveWeights[a]
	}
	for _, a := range sybils {
		sybilSum += result.EffectiveWeights[a]
	}

	// Each Sybil must be cold-capped (reputation weight = 0.1), so the
	// reputation-factor ceiling on Sybil contribution is nSybil * 0.1 =
	// 1.0. Other factors (trust, correlation, conviction, time) in the
	// effective-weight chain can scale this up or down, but the
	// reputation lever is the one we're measuring.
	reps, err := h.weigher.WeightsFor(ctx, agents)
	if err != nil {
		t.Fatalf("WeightsFor: %v", err)
	}
	var sybilRepSum float64
	for _, a := range sybils {
		sybilRepSum += reps[a]
	}
	expected := float64(nSybil) * 0.1
	t.Logf("[flooding] sybil reputation-weight sum = %.3f (expected %.3f, bound = nSybil * ColdCap)",
		sybilRepSum, expected)
	if math.Abs(sybilRepSum-expected) > 1e-6 {
		t.Fatalf("cold-start cap violated: sybil reputation sum=%.6f want %.6f",
			sybilRepSum, expected)
	}
	t.Logf("[flooding] legit effective sum = %.3f, sybil effective sum = %.3f",
		legitSum, sybilSum)
}

// TestByzantineReputationLaundering: an agent cycles good→bad behavior
// across rounds. We increment survived_count to simulate graduation,
// then run a round where their position is discarded (no agreers).
// EigenTrust dampens via the absence of new inbound edges, but there
// is no decay or negative signal — survived_count is sticky and the
// agent's cold-start protection is not revoked. The test surfaces the
// gap as a concrete delta for the decay/negative-signals roadmap item.
func TestByzantineReputationLaundering(t *testing.T) {
	agents := []string{"cycler", "honest-1", "honest-2", "honest-3"}
	types := func(i int) string {
		if i == 0 {
			return "honest" // mock role; laundering is in survived_count state
		}
		return "honest"
	}
	h := newByzantineHarness(t, 3, byzantineMockLLM(types))
	ctx := context.Background()

	// Pre-seed the cycler as already graduated. IncrementSurvivedCounts
	// takes vertex-form strings (schema v4); unsigned agents get "id:" prefix.
	for i := 0; i < 5; i++ {
		if err := h.db.IncrementSurvivedCounts(ctx, []string{idV("cycler")}); err != nil {
			t.Fatalf("seed cycler: %v", err)
		}
	}
	preReps, _ := h.db.LoadReputation(ctx, []string{"cycler"})
	t.Logf("[laundering] pre-round cycler survived_count=%d (graduated=%v)",
		preReps["cycler"].SurvivedCount,
		preReps["cycler"].SurvivedCount >= 3)

	// Now run a round where cycler is effectively silent (ignored in
	// agreement graph). We directly avoid giving them inbound edges.
	positions := positionSet("r1", 1, agents, func(i int, a string) string {
		return "Tight governance with real enforcement and verifiable rules."
	})
	_ = h.runRound(ctx, positions, nil, agents)

	postReps, _ := h.db.LoadReputation(ctx, []string{"cycler"})
	t.Logf("[laundering] post-round cycler survived_count=%d (still graduated=%v)",
		postReps["cycler"].SurvivedCount,
		postReps["cycler"].SurvivedCount >= 3)

	// The test documents the gap: survived_count does not decrease.
	if postReps["cycler"].SurvivedCount < preReps["cycler"].SurvivedCount {
		t.Fatalf("survived_count must not decrease without explicit decay; pre=%d post=%d",
			preReps["cycler"].SurvivedCount, postReps["cycler"].SurvivedCount)
	}
	t.Logf("[laundering] DOCUMENTED GAP: graduated agents retain cold-cap exemption without decay. " +
		"Motivates agent_trust_edges.weight half-life + negative-signal ingestion (THREAT_MODEL Planned row).")
}

// TestByzantineCruxPoisoning: crafted low-effort positions from a
// subset of agents. Verifies LOW_EFFORT warnings fire AND documents
// that the cold-start cap does NOT defend the claim-extraction stage —
// an important finding because it isolates which attack surface
// reputation actually protects.
func TestByzantineCruxPoisoning(t *testing.T) {
	agents := []string{"honest-0", "honest-1", "honest-2", "poisoner-3"}
	types := func(i int) string {
		if i == 3 {
			return "low_effort"
		}
		return "honest"
	}
	h := newByzantineHarness(t, 5, byzantineMockLLM(types))
	positions := positionSet("r1", 1, agents, func(i int, a string) string {
		if types(i) == "low_effort" {
			return "."
		}
		return "Tight governance with real enforcement and verifiable rules for this round."
	})
	result := h.runRound(context.Background(), positions, nil, agents)

	// Find COVERAGE or LOW_EFFORT warning for the poisoner.
	var foundLowEffort, foundCoverage bool
	for _, w := range result.IntegrityWarnings {
		if strings.Contains(w, "LOW_EFFORT") && strings.Contains(w, "poisoner-3") {
			foundLowEffort = true
			t.Logf("[poisoning] LOW_EFFORT warning: %s", w)
		}
		if strings.Contains(w, "COVERAGE") && strings.Contains(w, "poisoner-3") {
			foundCoverage = true
			t.Logf("[poisoning] COVERAGE warning: %s", w)
		}
	}
	if !(foundLowEffort || foundCoverage) {
		t.Fatalf("expected LOW_EFFORT or COVERAGE warning for low-effort poisoner; got %v",
			result.IntegrityWarnings)
	}

	// The poisoner's reputation weight is cold-capped — confirm that
	// this does NOT retroactively sanitize the already-warned cruxes.
	// The cap only multiplies into the effective-weight chain at the
	// consensus/bridging stage.
	weights, err := h.weigher.WeightsFor(context.Background(), agents)
	if err != nil {
		t.Fatalf("WeightsFor: %v", err)
	}
	if weights["poisoner-3"] != 0.1 {
		t.Fatalf("poisoner should be cold-capped at 0.1, got %f", weights["poisoner-3"])
	}
	t.Logf("[poisoning] cold-cap multiplies at synthesis stage only; crux extraction " +
		"remains an independent attack surface (noted in THREAT_MODEL).")
}

// TestByzantineGraduationCliff: the 3-agent Sybil ring that is the
// documented failure case for minDistinctAgreers=2. Each Sybil submits
// a position, each agrees with the other two. Across enough rounds all
// three graduate, removing the cold-start cap and allowing them to
// pool arbitrary EigenTrust mass via mutual edges. Proves the cliff
// empirically — the DARPA bid cites this as the motivation for decay +
// negative signals.
func TestByzantineGraduationCliff(t *testing.T) {
	// ColdThreshold=2 so 2 rounds suffice for all three to graduate.
	sybils := []string{"S1", "S2", "S3"}
	types := func(i int) string { return "honest" } // unused; we drive reputation directly
	h := newByzantineHarness(t, 2, byzantineMockLLM(types))
	ctx := context.Background()

	// Synthesize the ring directly via UpdateFromRound so the scenario
	// is tight and independent of the LLM mock's crux emission.
	cruxesR1 := []deliberation.Crux{
		{SourcePositionIDs: []string{"p-S1-r1"}, AgreeAgents: []string{"S2", "S3"}},
		{SourcePositionIDs: []string{"p-S2-r1"}, AgreeAgents: []string{"S1", "S3"}},
		{SourcePositionIDs: []string{"p-S3-r1"}, AgreeAgents: []string{"S1", "S2"}},
	}
	authorsR1 := map[string]string{
		"p-S1-r1": "S1", "p-S2-r1": "S2", "p-S3-r1": "S3",
	}
	if err := h.weigher.UpdateFromRound(ctx, "", false, cruxesR1, authorsR1, nil); err != nil {
		t.Fatalf("ring round 1: %v", err)
	}
	cruxesR2 := []deliberation.Crux{
		{SourcePositionIDs: []string{"p-S1-r2"}, AgreeAgents: []string{"S2", "S3"}},
		{SourcePositionIDs: []string{"p-S2-r2"}, AgreeAgents: []string{"S1", "S3"}},
		{SourcePositionIDs: []string{"p-S3-r2"}, AgreeAgents: []string{"S1", "S2"}},
	}
	authorsR2 := map[string]string{
		"p-S1-r2": "S1", "p-S2-r2": "S2", "p-S3-r2": "S3",
	}
	if err := h.weigher.UpdateFromRound(ctx, "", false, cruxesR2, authorsR2, nil); err != nil {
		t.Fatalf("ring round 2: %v", err)
	}

	reps, err := h.db.LoadReputation(ctx, sybils)
	if err != nil {
		t.Fatalf("LoadReputation: %v", err)
	}
	for _, s := range sybils {
		if reps[s].SurvivedCount < 2 {
			t.Fatalf("ring failed to graduate %s (survived=%d, want >=2)",
				s, reps[s].SurvivedCount)
		}
	}

	weights, err := h.weigher.WeightsFor(ctx, sybils)
	if err != nil {
		t.Fatalf("WeightsFor: %v", err)
	}
	var ringSum float64
	for _, s := range sybils {
		ringSum += weights[s]
	}
	t.Logf("[cliff] 3-Sybil ring graduated in 2 rounds at K=2, ColdThreshold=2; "+
		"ring reputation weight sum = %.3f (each > ColdCap now)", ringSum)
	for _, s := range sybils {
		if weights[s] <= 0.1 {
			t.Fatalf("post-graduation Sybil weight must exceed ColdCap; %s = %f",
				s, weights[s])
		}
	}
	t.Logf("[cliff] DOCUMENTED LIMITATION: N>=3 mutual-endorsement ring defeats "+
		"minDistinctAgreers=%d. Motivates agent_trust_edges decay + dispute-signal ingestion.",
		2)
}

// TestByzantineReframingAttack: a Sybil frames its position as the
// moderate middle; an honest agent takes a strong position. With
// reputation enabled and both agents cold-capped equally, the moderate
// framing still gets pulled in if the LLM honors it. The test records
// whether the crux wording loads against the honest side and logs the
// reputation state — it does not require a specific outcome because
// the real defense here is cross-family consistency (Task 4), not
// reputation.
func TestByzantineReframingAttack(t *testing.T) {
	agents := []string{"honest-0", "framer-1"}
	types := func(i int) string {
		if i == 1 {
			return "moderate"
		}
		return "honest"
	}
	h := newByzantineHarness(t, 5, byzantineMockLLM(types))
	positions := positionSet("r1", 1, agents, func(i int, a string) string {
		if types(i) == "moderate" {
			return "A balanced, pragmatic, reasonable approach to governance is best."
		}
		return "Tight governance with real enforcement and verifiable rules."
	})
	result := h.runRound(context.Background(), positions, nil, agents)

	t.Logf("[framing] integrity warnings: %d", len(result.IntegrityWarnings))
	for _, w := range result.IntegrityWarnings {
		t.Logf("  - %s", w)
	}
	for _, c := range result.Cruxes {
		t.Logf("[framing] crux claim: %q", c.Claim)
		lower := strings.ToLower(c.Claim)
		for _, loaded := range []string{"extreme", "radical", "unreasonable"} {
			if strings.Contains(lower, loaded) {
				t.Logf("[framing] WARNING: crux contains loaded term %q", loaded)
			}
		}
	}
	weights, err := h.weigher.WeightsFor(context.Background(), agents)
	if err != nil {
		t.Fatalf("WeightsFor: %v", err)
	}
	t.Logf("[framing] reputation weights: honest=%.3f framer=%.3f (both cold-capped)",
		weights["honest-0"], weights["framer-1"])
	t.Logf("[framing] reputation does not defend against framing; cross-family " +
		"consistency (Task 4) is the intended countermeasure.")
}

// consume references — prevent "declared but not used" for now-commented helpers.
var _ = consensusHonestShare
