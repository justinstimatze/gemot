package tests

import (
	"context"
	"math"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/config"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
	"github.com/justinstimatze/gemot/internal/store"
)

// TestSyntheticAgentDeliberation simulates 5 agents deliberating on AI governance,
// runs the full analysis pipeline (real LLM calls), and validates the output.
//
// This is gemot's target use case: a small number of agents submitting substantive
// multi-sentence positions, voting on each other's positions, then getting analysis.
//
// Skip with: go test -short ./tests/
func TestSyntheticAgentDeliberation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping synthetic benchmark (makes real API calls)")
	}

	cfg := config.Load()
	if cfg.AnthropicKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	// Set up service with real LLM analyzer
	db := tempDB(t)
	client := llm.NewClient(cfg.AnthropicKey, cfg.Model)
	analyzer := analysis.NewSynthesizer(client)
	svc := deliberation.NewService(db, analyzer)

	// Create deliberation
	delib, err := svc.CreateDeliberation(context.Background(), 
		"AI Development Governance",
		"How should frontier AI development be governed? Consider international coordination, industry self-regulation, compute governance, open-source policy, and safety requirements.",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Created deliberation: %s", delib.ID)

	// 5 agents with distinct, substantive positions that create real disagreements
	agents := map[string]string{
		"safety-hawk": `Frontier AI development must be paused immediately above a compute threshold of 10^26 FLOP until we have reliable alignment techniques. The potential for existential risk from misaligned superintelligence dwarfs any economic benefit from racing ahead. We need an international treaty modeled on the Nuclear Non-Proliferation Treaty, with inspection regimes for large training runs. Voluntary commitments from labs are meaningless — we've seen them broken repeatedly. The precautionary principle must govern: if we can't prove safety, we don't deploy.`,

		"industry-pragmatist": `Government regulators lack the technical sophistication to write effective AI rules. The field moves too fast for legislation. Industry self-regulation through voluntary commitments, third-party audits, and transparency reports is the right approach. Companies that ship unsafe products face lawsuits, reputational damage, and customer loss — market incentives work. A heavy regulatory regime would entrench incumbents, kill open-source innovation, and push development to less responsible jurisdictions. The EU AI Act is already causing brain drain to the US and Singapore.`,

		"open-source-advocate": `Open-source AI is the most important safety mechanism we have. Closed development concentrates power in a few corporations with no accountability. Open weights enable independent safety research, red-teaming at scale, and democratic oversight. Restricting model weights would be like banning the printing press to prevent misinformation. Compute governance through export controls on chips is a far better lever than restricting model distribution. The real risk is not open models — it's concentrated, unaccountable power in closed labs.`,

		"governance-institutionalist": `We need new international institutions purpose-built for AI governance, not retreads of nuclear or trade frameworks. An International AI Agency should coordinate safety standards, conduct evaluations, and maintain a registry of frontier systems. National regulation alone creates a race to the bottom. But we also need adaptive governance — hard rules will be obsolete before the ink dries. Regulatory sandboxes, iterative standard-setting, and mandatory incident reporting are better tools than bans or moratoria. The goal is to govern the technology, not stop it.`,

		"global-south-voice": `The AI governance conversation is dominated by wealthy nations and their corporations. Most proposals — compute thresholds, chip export controls, licensing regimes — would lock developing nations out of AI entirely. We need technology transfer mechanisms, capacity building, and governance structures that include the Global South as equal partners, not supplicants. Open-source AI is essential for our sovereignty. The biggest risk isn't superintelligence — it's a permanent two-tier world where a few nations control the most powerful technology in history.`,
	}

	// Submit positions
	positionIDs := map[string]string{} // agent -> position ID
	for agentID, content := range agents {
		p, err := svc.SubmitPosition(context.Background(), delib.ID, agentID, content)
		if err != nil {
			t.Fatalf("submitting position for %s: %v", agentID, err)
		}
		positionIDs[agentID] = p.ID
		t.Logf("  %s submitted position %s", agentID, p.ID)
	}

	// Voting matrix — designed to create realistic alliances and divisions
	// safety-hawk and governance-institutionalist partially align (both want regulation)
	// industry-pragmatist and open-source-advocate partially align (both resist government control)
	// global-south-voice aligns with open-source but opposes compute governance
	type voteEntry struct {
		voter, target string
		value         int
	}
	voteMatrix := []voteEntry{
		// safety-hawk votes
		{"safety-hawk", "industry-pragmatist", -1},        // disagrees with self-regulation
		{"safety-hawk", "open-source-advocate", -1},       // disagrees with open weights
		{"safety-hawk", "governance-institutionalist", 1}, // agrees on institutions
		{"safety-hawk", "global-south-voice", 0},          // pass — sees the point

		// industry-pragmatist votes
		{"industry-pragmatist", "safety-hawk", -1},                 // disagrees with pause
		{"industry-pragmatist", "open-source-advocate", 1},         // agrees on open innovation
		{"industry-pragmatist", "governance-institutionalist", -1}, // disagrees with new agencies
		{"industry-pragmatist", "global-south-voice", 0},           // pass

		// open-source-advocate votes
		{"open-source-advocate", "safety-hawk", -1},                // disagrees with pause
		{"open-source-advocate", "industry-pragmatist", 0},         // mixed feelings
		{"open-source-advocate", "governance-institutionalist", 0}, // partial agreement
		{"open-source-advocate", "global-south-voice", 1},          // agrees on sovereignty

		// governance-institutionalist votes
		{"governance-institutionalist", "safety-hawk", 0},          // partial agreement
		{"governance-institutionalist", "industry-pragmatist", -1}, // disagrees with self-reg
		{"governance-institutionalist", "open-source-advocate", 0}, // mixed
		{"governance-institutionalist", "global-south-voice", 1},   // agrees on inclusion

		// global-south-voice votes
		{"global-south-voice", "safety-hawk", -1},                // disagrees with compute thresholds
		{"global-south-voice", "industry-pragmatist", 0},         // mixed
		{"global-south-voice", "open-source-advocate", 1},        // agrees on open-source
		{"global-south-voice", "governance-institutionalist", 0}, // partial agreement
	}

	for _, v := range voteMatrix {
		if err := svc.Vote(context.Background(), delib.ID, v.voter, positionIDs[v.target], v.value, "", ""); err != nil {
			t.Fatalf("vote %s->%s: %v", v.voter, v.target, err)
		}
	}
	t.Logf("Recorded %d votes", len(voteMatrix))

	// Run analysis
	t.Log("Running full analysis pipeline (real LLM calls)...")
	result, err := svc.Analyze(context.Background(), delib.ID)
	if err != nil {
		t.Fatalf("analysis failed: %v", err)
	}

	// === Validate results ===

	t.Logf("\n=== ANALYSIS RESULTS ===")
	t.Logf("Agents: %d, Positions: %d, Votes: %d", result.AgentCount, result.PositionCount, result.VoteCount)

	// 1. Topic summaries should exist and be substantive
	t.Logf("\nTopics (%d):", len(result.TopicSummaries))
	for _, ts := range result.TopicSummaries {
		t.Logf("  %s: %s", ts.Topic, truncate(ts.Summary, 150))
	}
	if len(result.TopicSummaries) == 0 {
		t.Error("expected at least one topic summary")
	}

	// 2. Cruxes should exist and capture real disagreements
	t.Logf("\nCruxes (%d):", len(result.Cruxes))
	for _, crux := range result.Cruxes {
		t.Logf("  [%.2f] %s > %s", crux.ControversyScore, crux.Topic, crux.Subtopic)
		t.Logf("    Claim: %s", truncate(crux.Claim, 120))
		t.Logf("    Agree: %v", crux.AgreeAgents)
		t.Logf("    Disagree: %v", crux.DisagreeAgents)
		t.Logf("    Explanation: %s", truncate(crux.Explanation, 150))
	}
	if len(result.Cruxes) == 0 {
		t.Error("expected at least one crux")
	}

	// 3. Cruxes should actually divide agents (not put everyone on one side)
	for _, crux := range result.Cruxes {
		if len(crux.AgreeAgents) == 0 || len(crux.DisagreeAgents) == 0 {
			t.Errorf("crux %q has empty agree or disagree side", crux.Claim)
		}
		// Controversy score should be meaningful
		if crux.ControversyScore < 0.1 {
			t.Errorf("crux %q has very low controversy score %.2f", truncate(crux.Claim, 50), crux.ControversyScore)
		}
	}

	// 4. Clusters should separate agents with opposing views
	t.Logf("\nClusters (%d):", len(result.Clusters))
	for _, c := range result.Clusters {
		t.Logf("  Cluster %d: %v (size=%d)", c.ID, c.AgentIDs, c.Size)
		if len(c.RepresentativePositions) > 0 {
			t.Logf("    Rep positions: %s", truncate(c.RepresentativePositions[0], 100))
		}
	}

	// 5. Agent context should be meaningful
	for _, agentID := range []string{"safety-hawk", "open-source-advocate"} {
		actx, err := svc.GetContext(context.Background(), delib.ID, agentID)
		if err != nil {
			t.Errorf("get context for %s: %v", agentID, err)
			continue
		}
		t.Logf("\nContext for %s:", agentID)
		t.Logf("  Cluster: %v", actx.ClusterID)
		t.Logf("  Allies: %v", actx.NearestAllies)
		t.Logf("  Disagreements: %v", actx.BiggestDisagreements)
		t.Logf("  Relevant cruxes: %d", len(actx.RelevantCruxes))
	}

	// 6. Safety-hawk and open-source-advocate should NOT be allies
	safetyCtx, _ := svc.GetContext(context.Background(), delib.ID, "safety-hawk")
	if safetyCtx != nil {
		for _, ally := range safetyCtx.NearestAllies {
			if ally == "open-source-advocate" {
				t.Log("WARNING: safety-hawk and open-source-advocate are allies — expected them in different clusters")
			}
		}
		// safety-hawk should have disagreements
		if len(safetyCtx.BiggestDisagreements) == 0 && len(result.Cruxes) > 0 {
			t.Log("WARNING: safety-hawk has no disagreements despite cruxes existing")
		}
	}

	// 7. Verify round advanced
	d2, _ := svc.GetDeliberation(context.Background(), delib.ID)
	if d2.Round != 2 {
		t.Errorf("expected round 2 after analysis, got %d", d2.Round)
	}

	// Summary metrics
	t.Logf("\n=== BENCHMARK SUMMARY ===")
	t.Logf("Topics extracted: %d", len(result.TopicSummaries))
	t.Logf("Cruxes found: %d", len(result.Cruxes))
	t.Logf("Clusters formed: %d", len(result.Clusters))
	if len(result.Cruxes) > 0 {
		avgControversy := 0.0
		for _, c := range result.Cruxes {
			avgControversy += c.ControversyScore
		}
		avgControversy /= float64(len(result.Cruxes))
		t.Logf("Avg controversy score: %.2f", avgControversy)
	}
	maxSideDiff := 0
	for _, c := range result.Cruxes {
		diff := int(math.Abs(float64(len(c.AgreeAgents) - len(c.DisagreeAgents))))
		if diff > maxSideDiff {
			maxSideDiff = diff
		}
	}
	t.Logf("Max agree/disagree imbalance: %d", maxSideDiff)

	// Quality evaluation (T3C-inspired automated scoring)
	evalScore := analysis.EvaluateResult(result)
	t.Logf("\n=== QUALITY EVALUATION ===")
	t.Logf("Crux structure: %.2f", evalScore.CruxStructureScore)
	t.Logf("Explanation quality: %.2f", evalScore.ExplanationScore)
	t.Logf("Agent coverage: %.2f", evalScore.CoverageScore)
	t.Logf("Side balance: %.2f", evalScore.BalanceScore)
	t.Logf("Overall: %.2f", evalScore.OverallScore)
	for _, issue := range evalScore.Issues {
		t.Logf("  Issue: %s", issue)
	}
	if evalScore.OverallScore < 0.3 {
		t.Errorf("overall quality score %.2f is below minimum threshold 0.3", evalScore.OverallScore)
	}
}

// Ensure store satisfies the interface (compile-time check).
var _ deliberation.Store = (*store.DB)(nil)
