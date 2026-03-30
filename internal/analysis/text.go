package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
	"github.com/justinstimatze/gemot/internal/sanitize"
)

// ClaimCache provides optional caching for claim extraction LLM calls.
type ClaimCache interface {
	Get(key string) string       // returns "" if not found/expired
	Put(key, value, model string)
}

// TextAnalyzer performs LLM-based deliberation analysis.
type TextAnalyzer struct {
	structuredOutput llm.StructuredOutputFunc
	cache            ClaimCache
}

func NewTextAnalyzer(client *llm.Client) *TextAnalyzer {
	return &TextAnalyzer{structuredOutput: client.StructuredOutput}
}

// NewTextAnalyzerWithFunc creates a TextAnalyzer with a custom structured output function (for testing).
func NewTextAnalyzerWithFunc(fn llm.StructuredOutputFunc) *TextAnalyzer {
	return &TextAnalyzer{structuredOutput: fn}
}

// SetCache enables LLM response caching for claim extraction.
func (a *TextAnalyzer) SetCache(c ClaimCache) {
	a.cache = c
}

// GenerateCompromise produces a compromise statement based on analysis results.
func (a *TextAnalyzer) GenerateCompromise(ctx context.Context, topic string, result *deliberation.AnalysisResult) (string, error) {
	// Format cruxes
	var cruxesText string
	for i, c := range result.Cruxes {
		cruxesText += fmt.Sprintf("%d. %s (controversy: %.0f%%)\n   Agree: %v\n   Disagree: %v\n\n",
			i+1, c.Claim, c.ControversyScore*100, c.AgreeAgents, c.DisagreeAgents)
	}
	if cruxesText == "" {
		cruxesText = "No cruxes detected."
	}

	// Format bridging statements
	var bridgingText string
	for i, b := range result.BridgingStatements {
		bridgingText += fmt.Sprintf("%d. [bridging score: %.0f%%] %s (by %s)\n",
			i+1, b.BridgingScore*100, b.Content, b.AgentID)
	}
	if bridgingText == "" {
		bridgingText = "No bridging statements detected."
	}

	// Format clusters
	var clusterText string
	for _, c := range result.Clusters {
		clusterText += fmt.Sprintf("Cluster %d (%d agents: %v)\n", c.ID, c.Size, c.AgentIDs)
		for _, rep := range c.RepresentativePositions {
			clusterText += fmt.Sprintf("  Representative: %s\n", rep)
		}
		clusterText += "\n"
	}
	if clusterText == "" {
		clusterText = "No clusters detected."
	}

	prompt := fmt.Sprintf(compromisePrompt, topic, cruxesText, bridgingText, clusterText)

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"compromise_statement": map[string]any{"type": "string"},
			"rationale":            map[string]any{"type": "string"},
			"cruxes_addressed":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"compromise_statement", "rationale", "cruxes_addressed"},
	}

	var output struct {
		Statement       string   `json:"compromise_statement"`
		Rationale       string   `json:"rationale"`
		CruxesAddressed []string `json:"cruxes_addressed"`
	}
	if err := a.structuredOutput(ctx, systemPrompt, prompt, schema, &output); err != nil {
		return "", err
	}

	return output.Statement, nil
}

// Reframe restates a position emphasizing common ground with other agents.
func (a *TextAnalyzer) Reframe(ctx context.Context, position string, otherPositions string, cruxes string) (string, error) {
	prompt := fmt.Sprintf(reframePrompt, position, otherPositions, cruxes)
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reframed_position": map[string]any{"type": "string"},
		},
		"required": []string{"reframed_position"},
	}
	var output struct {
		Reframed string `json:"reframed_position"`
	}
	if err := a.structuredOutput(ctx, systemPrompt, prompt, schema, &output); err != nil {
		return "", err
	}
	return output.Reframed, nil
}

// --- Structured output types ---

type taxonomyResult struct {
	Topics []topicResult `json:"topics"`
}

type topicResult struct {
	TopicName        string           `json:"topic_name"`
	TopicDescription string           `json:"topic_description"`
	Subtopics        []subtopicResult `json:"subtopics"`
}

type subtopicResult struct {
	SubtopicName        string `json:"subtopic_name"`
	SubtopicDescription string `json:"subtopic_description"`
}

type claimExtractionResult struct {
	Claims []extractedClaim `json:"claims"`
}

type extractedClaim struct {
	Claim        string `json:"claim"`
	Quote        string `json:"quote"`
	TopicName    string `json:"topic_name"`
	SubtopicName string `json:"subtopic_name"`
}

type claimDeduplicationResult struct {
	Groups []claimGroup `json:"groups"`
}

type claimGroup struct {
	ClaimText        string `json:"claim_text"`
	OriginalClaimIDs []int  `json:"original_claim_ids"`
}

type cruxResult struct {
	CruxClaim       string   `json:"crux_claim"`
	Agree           []string `json:"agree"`
	Disagree        []string `json:"disagree"`
	NoClearPosition []string `json:"no_clear_position"`
	Explanation     string   `json:"explanation"`
}

type summaryResult struct {
	Summary string `json:"summary"`
}

// claimSource tracks a quote from a specific position that supports a claim.
type claimSource struct {
	PositionID string
	AgentID    string
	Quote      string
	ClaimText  string // the original extracted claim text
}

// claim tracks an extracted claim with its source agent and position.
type claim struct {
	AgentID      string
	AgentNum     string
	PositionID   string
	Claim        string
	Quote        string
	TopicName    string
	SubtopicName string
	Sources      []claimSource // all source quotes (populated during dedup)
}

// reportProgress sends a sub-status update if a progress callback is in the context.
func reportProgress(ctx context.Context, subStatus string) {
	if fn, ok := ctx.Value(deliberation.ContextKeyProgressFunc{}).(deliberation.ProgressFunc); ok {
		fn(subStatus)
	}
}

func (a *TextAnalyzer) Analyze(ctx context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	startTime := time.Now()

	if len(positions) == 0 {
		return &deliberation.AnalysisResult{
			Clusters:            []deliberation.OpinionCluster{},
			Cruxes:              []deliberation.Crux{},
			ConsensusStatements: []deliberation.ConsensusStatement{},
			TopicSummaries:      []deliberation.TopicSummary{},
			AgentCount:          len(agents),
			PositionCount:       0,
			VoteCount:           len(votes),
		}, nil
	}

	// Map agent IDs to numeric IDs (T3C pattern: anonymize before LLM)
	agentToNum := map[string]string{}
	numToAgent := map[string]string{}
	for i, agent := range agents {
		num := fmt.Sprintf("%d", i)
		agentToNum[agent] = num
		numToAgent[num] = agent
	}

	deliberationTopic := positions[0].DeliberationID
	positionText := formatPositions(positions, agentToNum)

	// Collect prior-round context for LLM prompts
	var priorContext string
	if norms, ok := ctx.Value(deliberation.ContextKeyPriorNorms{}).([]string); ok && len(norms) > 0 {
		priorContext += "\nPRIOR ROUND NORMS (established behavioral patterns):\n"
		for _, n := range norms {
			priorContext += "- " + n + "\n"
		}
	}
	if rules, ok := ctx.Value(deliberation.ContextKeyConstitutionalRules{}).([]string); ok && len(rules) > 0 {
		priorContext += "\nCONSTITUTIONAL RULES (high-consensus principles from prior rounds):\n"
		for _, r := range rules {
			priorContext += "- " + r + "\n"
		}
	}
	if tmplName, ok := ctx.Value(deliberation.ContextKeyTemplate{}).(string); ok {
		if tmpl, found := deliberation.GetTemplate(tmplName); found {
			priorContext += "\nGOVERNANCE TEMPLATE: " + tmplName + "\n" + tmpl.AnalysisHint + "\n"
		}
	}

	// Append prior context to position text so LLM sees it during taxonomy/claim extraction
	enrichedPositionText := positionText
	if priorContext != "" {
		enrichedPositionText += "\n\n" + priorContext
	}

	// Step 1: Taxonomy extraction
	reportProgress(ctx, "taxonomy")
	taxonomy, err := a.getTaxonomy(ctx, deliberationTopic, enrichedPositionText)
	if err != nil {
		return nil, fmt.Errorf("taxonomy extraction: %w", err)
	}

	taxonomyText := formatTaxonomy(taxonomy)

	var warnings []string
	var audit []deliberation.AuditEntry

	audit = append(audit, deliberation.AuditEntry{
		Stage:  "taxonomy",
		Detail: fmt.Sprintf("extracted %d topics", len(taxonomy.Topics)),
		Count:  len(taxonomy.Topics),
	})
	for _, topic := range taxonomy.Topics {
		audit = append(audit, deliberation.AuditEntry{
			Stage:  "taxonomy",
			Detail: fmt.Sprintf("topic %q: %d subtopics", topic.TopicName, len(topic.Subtopics)),
			Count:  len(topic.Subtopics),
		})
	}

	// Build constrained enum schema for claim extraction (T3C pattern: prevent topic/subtopic mismatches)
	claimSchema := buildConstrainedClaimSchema(taxonomy)

	// Step 2: Claim extraction (per position) with sanitization
	// Parallelized with bounded concurrency (T3C pattern: semaphore + goroutines)
	reportProgress(ctx, "extracting")
	var smallNPreamble string
	if len(positions) <= 10 {
		smallNPreamble = "IMPORTANT: With only " + strconv.Itoa(len(positions)) + " participants, each position is valuable. Extract at least one claim per position when the position contains any debatable stance. Do not apply the zero-extraction threshold as aggressively as you would with hundreds of comments.\n\n"
	}

	const maxConcurrentExtractions = 6
	type extractionResult struct {
		claims   []claim
		warnings []string
	}
	results := make([]extractionResult, len(positions))
	sem := make(chan struct{}, maxConcurrentExtractions)
	var wg sync.WaitGroup

	for i, p := range positions {
		wg.Add(1)
		go func(idx int, pos deliberation.Position) {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			num := agentToNum[pos.AgentID]
			sanitized := sanitize.Position(pos.Content)

			extracted, err := a.extractClaimsConstrained(ctx, num, deliberationTopic, sanitized.Text, taxonomyText, claimSchema, smallNPreamble)
			if err != nil {
				results[idx] = extractionResult{warnings: sanitized.Warnings}
				return
			}
			var claims []claim
			for _, ec := range extracted {
				claims = append(claims, claim{
					AgentID:      pos.AgentID,
					AgentNum:     num,
					PositionID:   pos.ID,
					Claim:        ec.Claim,
					Quote:        ec.Quote,
					TopicName:    strings.TrimSpace(ec.TopicName),
					SubtopicName: strings.TrimSpace(ec.SubtopicName),
				})
			}
			results[idx] = extractionResult{claims: claims, warnings: sanitized.Warnings}
		}(i, p)
	}
	wg.Wait()

	// Collect results in deterministic order
	var allClaims []claim
	for _, r := range results {
		allClaims = append(allClaims, r.claims...)
		warnings = append(warnings, r.warnings...)
	}

	// Audit: per-position claim counts
	agentClaimCounts := map[string]int{}
	for _, c := range allClaims {
		agentClaimCounts[c.AgentID]++
	}
	for agent, count := range agentClaimCounts {
		audit = append(audit, deliberation.AuditEntry{
			Stage:  "extraction",
			Detail: fmt.Sprintf("agent %q: %d claims extracted", agent, count),
			Count:  count,
		})
	}
	audit = append(audit, deliberation.AuditEntry{
		Stage:  "extraction",
		Detail: fmt.Sprintf("total: %d claims from %d positions", len(allClaims), len(positions)),
		Count:  len(allClaims),
	})

	// Validate topic-subtopic topology: fix claims with mismatched topic/subtopic pairs
	// Trim whitespace from taxonomy names (LLM may return padded strings)
	for i := range taxonomy.Topics {
		taxonomy.Topics[i].TopicName = strings.TrimSpace(taxonomy.Topics[i].TopicName)
		for j := range taxonomy.Topics[i].Subtopics {
			taxonomy.Topics[i].Subtopics[j].SubtopicName = strings.TrimSpace(taxonomy.Topics[i].Subtopics[j].SubtopicName)
		}
	}

	subtopicToTopic := map[string]string{}
	for _, topic := range taxonomy.Topics {
		for _, st := range topic.Subtopics {
			subtopicToTopic[strings.ToLower(st.SubtopicName)] = topic.TopicName
		}
	}
	topologyCorrected := 0
	for i := range allClaims {
		expectedTopic, ok := subtopicToTopic[strings.ToLower(allClaims[i].SubtopicName)]
		if ok && !strings.EqualFold(allClaims[i].TopicName, expectedTopic) {
			allClaims[i].TopicName = expectedTopic
			topologyCorrected++
		}
	}
	if topologyCorrected > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"TOPOLOGY: corrected %d claim(s) with mismatched topic-subtopic assignments",
			topologyCorrected,
		))
	}

	// Integrity: coverage validation — flag agents whose positions yielded no claims
	warnings = append(warnings, validateCoverage(positions, allClaims)...)

	// Debug: log claim distribution to stderr for production diagnostics
	{
		topicCounts := map[string]int{}
		subtopicCounts := map[string]int{}
		subtopicSpeakers := map[string]map[string]bool{}
		for _, c := range allClaims {
			topicCounts[c.TopicName]++
			key := c.TopicName + " > " + c.SubtopicName
			subtopicCounts[key]++
			if subtopicSpeakers[key] == nil {
				subtopicSpeakers[key] = map[string]bool{}
			}
			subtopicSpeakers[key][c.AgentNum] = true
		}
		log.Printf("[gemot] claim extraction: %d total claims from %d positions", len(allClaims), len(positions))
		for t, cnt := range topicCounts {
			log.Printf("[gemot]   topic %q: %d claims", t, cnt)
		}
		qualifiedSubtopics := 0
		for st, cnt := range subtopicCounts {
			speakers := len(subtopicSpeakers[st])
			log.Printf("[gemot]   subtopic %q: %d claims, %d speakers", st, cnt, speakers)
			if speakers >= 2 {
				qualifiedSubtopics++
			}
		}
		log.Printf("[gemot]   subtopics with 2+ speakers: %d", qualifiedSubtopics)
	}

	// Fail loudly if claim extraction produced nothing — caller should know analysis failed
	if len(allClaims) == 0 {
		return nil, fmt.Errorf("claim extraction produced 0 claims from %d positions; analysis cannot proceed", len(positions))
	}

	// Step 3: Group claims by subtopic, deduplicate, then detect cruxes
	reportProgress(ctx, "crux_detection")
	var cruxes []deliberation.Crux
	var summaries []deliberation.TopicSummary

	for _, topic := range taxonomy.Topics {
		// Summary for this topic (soft-fail: analysis continues without summaries)
		topicPositions := formatPositions(positions, agentToNum)
		summary, err := a.getSummary(ctx, deliberationTopic, topic.TopicName, topicPositions)
		if err != nil {
			log.Printf("[gemot] summary generation failed for topic %q (continuing): %v", topic.TopicName, err)
			warnings = append(warnings, fmt.Sprintf("SOFT_FAIL: summary generation failed for topic %q", topic.TopicName))
		} else {
			summaries = append(summaries, deliberation.TopicSummary{
				Topic:   topic.TopicName,
				Summary: summary,
			})
		}

		foundSubtopicCrux := false
		for _, subtopic := range topic.Subtopics {
			// Filter claims for this subtopic
			subtopicClaims := filterClaimsBySubtopic(allClaims, topic.TopicName, subtopic.SubtopicName)
			if len(subtopicClaims) < 2 || len(uniqueSpeakers(subtopicClaims)) < 2 {
				continue
			}

			// Deduplicate claims within this subtopic (soft-fail: use raw claims if dedup fails)
			deduped, err := a.deduplicateClaims(ctx, deliberationTopic, subtopic.SubtopicName, subtopicClaims)
			if err != nil {
				log.Printf("[gemot] dedup failed for subtopic %q (using raw claims): %v", subtopic.SubtopicName, err)
				deduped = subtopicClaims
			}

			if len(uniqueSpeakers(deduped)) < 2 {
				continue
			}

			claimsText := formatClaimsForCrux(deduped)
			crux, err := a.getCrux(ctx, deliberationTopic, topic.TopicName, subtopic.SubtopicName, subtopic.SubtopicDescription, claimsText, numToAgent)
			if err != nil {
				continue
			}
			crux.Topic = topic.TopicName
			crux.Subtopic = subtopic.SubtopicName
			crux.SourcePositionIDs = collectPositionIDs(subtopicClaims)
			crux.SourceQuotes = collectSourceQuotes(subtopicClaims)
			cruxes = append(cruxes, *crux)
			foundSubtopicCrux = true
		}

		// Fallback: run topic-level crux detection if orphaned claims exist.
		// This catches claims from single-speaker subtopics that were skipped above.
		// Previously only ran if NO subtopic crux was found; now runs whenever there
		// are topic-level claims from agents not already covered by subtopic cruxes.
		topicClaims := filterClaimsByTopic(allClaims, topic.TopicName)
		if len(topicClaims) >= 2 && len(uniqueSpeakers(topicClaims)) >= 2 {
			if !foundSubtopicCrux {
				// No subtopic cruxes at all — use full topic claims
				claimsText := formatClaimsForCrux(topicClaims)
				crux, err := a.getCrux(ctx, deliberationTopic, topic.TopicName, topic.TopicName, topic.TopicDescription, claimsText, numToAgent)
				if err == nil {
					crux.Topic = topic.TopicName
					crux.Subtopic = "(topic-level)"
					crux.SourcePositionIDs = collectPositionIDs(topicClaims)
					crux.SourceQuotes = collectSourceQuotes(topicClaims)
					cruxes = append(cruxes, *crux)
				}
			} else {
				// Some subtopic cruxes exist, but single-speaker subtopics were skipped.
				// Collect orphaned claims (from subtopics that didn't qualify) and run
				// a topic-level pass on them to avoid losing 22% of analysis.
				var orphanedClaims []claim
				for _, subtopic := range topic.Subtopics {
					sc := filterClaimsBySubtopic(allClaims, topic.TopicName, subtopic.SubtopicName)
					if len(uniqueSpeakers(sc)) < 2 && len(sc) > 0 {
						orphanedClaims = append(orphanedClaims, sc...)
					}
				}
				if len(orphanedClaims) > 0 && len(uniqueSpeakers(orphanedClaims)) >= 2 {
					claimsText := formatClaimsForCrux(orphanedClaims)
					crux, err := a.getCrux(ctx, deliberationTopic, topic.TopicName, "(orphaned subtopics)", topic.TopicDescription, claimsText, numToAgent)
					if err == nil {
						crux.Topic = topic.TopicName
						crux.Subtopic = "(cross-subtopic)"
						crux.SourcePositionIDs = collectPositionIDs(orphanedClaims)
						crux.SourceQuotes = collectSourceQuotes(orphanedClaims)
						cruxes = append(cruxes, *crux)
					}
				}
			}
		}
	}

	// Integrity: validate crux agents — remove hallucinated agent IDs, discard degenerate cruxes
	validAgentSet := map[string]bool{}
	for _, a := range agents {
		validAgentSet[a] = true
	}
	cruxes, cruxWarnings := validateCruxAgents(cruxes, validAgentSet)
	warnings = append(warnings, cruxWarnings...)

	// Integrity: check for Sybil-like voting patterns
	warnings = append(warnings, validateVoteSimilarity(votes, agents)...)

	// Integrity: check model diversity
	warnings = append(warnings, validateModelDiversity(positions)...)

	// Process integrity pre-check (must run before refusal gate)
	if len(agents) < 3 {
		warnings = append(warnings, "INSUFFICIENT_AGENTS: fewer than 3 agents — analysis may be unreliable")
	}
	votesByAgent := map[string]int{}
	for _, v := range votes {
		votesByAgent[v.AgentID]++
	}
	if len(votes) > 0 {
		for ag, count := range votesByAgent {
			if float64(count)/float64(len(votes)) > 0.6 {
				warnings = append(warnings, fmt.Sprintf("VOTE_DOMINATION: agent %q cast %.0f%% of all votes", ag, float64(count)/float64(len(votes))*100))
			}
		}
	}

	// Integrity gate: refuse to produce consensus/bridging if process is too compromised
	criticalCount := 0
	hasSybil := false
	for _, w := range warnings {
		if strings.Contains(w, "SYBIL_SIGNAL") {
			hasSybil = true
			criticalCount++
		}
		if strings.Contains(w, "DEGENERATE") || strings.Contains(w, "VOTE_DOMINATION") {
			criticalCount++
		}
	}
	refused := hasSybil || criticalCount >= 3

	// Classify cruxes as factual/value/mixed (Bench-Capon value-based argumentation)
	if len(cruxes) > 0 {
		a.classifyCruxes(ctx, deliberationTopic, cruxes)
	}

	// Step 4: Build clusters from crux alignment
	reportProgress(ctx, "clustering")
	clusters := buildClusters(cruxes, agents)

	// Step 5: Compute effective weights, then find consensus and bridging
	// Trust weights from integrity signals
	// Determine current round from positions for trust decay
	currentRound := 1
	for _, p := range positions {
		if p.Round > currentRound {
			currentRound = p.Round
		}
	}
	trustWeightsEarly := TrustWeights(agents, positions, votes, warnings, currentRound)
	// Correlation discounting (Plurality: degressive proportionality)
	correlationWeightsEarly := CorrelationDiscountedWeights(votes, agents)
	// Effective weights: trust × correlation × sqrt(conviction × time_weight)
	// Conviction voting: agents who sustain positions across rounds gain weight.
	// time_weight = 1 + 0.2*(roundsActive-1), so round 1 = 1.0, round 2 = 1.2, round 3 = 1.4
	effectiveWeights := map[string]float64{}
	convictionByAgent := map[string]float64{}
	// Track distinct rounds per agent for conviction time-weight
	type agentRound struct{ agent string; round int }
	seenRounds := map[agentRound]bool{}
	roundCount := map[string]int{}
	for _, p := range positions {
		if p.Conviction > convictionByAgent[p.AgentID] {
			convictionByAgent[p.AgentID] = p.Conviction
		}
		ar := agentRound{p.AgentID, p.Round}
		if !seenRounds[ar] {
			seenRounds[ar] = true
			roundCount[p.AgentID]++
		}
	}
	for _, a := range agents {
		conv := convictionByAgent[a]
		if conv <= 0 {
			conv = 0.5
		}
		timeWeight := 1.0 + 0.2*float64(roundCount[a]-1)
		if timeWeight < 1.0 {
			timeWeight = 1.0
		}
		effectiveWeights[a] = trustWeightsEarly[a] * correlationWeightsEarly[a] * math.Sqrt(conv*timeWeight)
	}

	var consensus []deliberation.ConsensusStatement
	var bridging []deliberation.BridgingStatement
	if refused {
		warnings = append(warnings, "ANALYSIS_REFUSED: integrity too compromised to produce reliable consensus/bridging. Cruxes and warnings are still available. Fix the underlying issues and re-analyze.")
	} else {
		consensus = findConsensus(ctx, positions, votes, clusters, effectiveWeights)
		bridging = findBridging(positions, votes, clusters, effectiveWeights)
	}

	// Per-criterion analysis (multi-criteria voting)
	criteriaResults := map[string]any{}
	criterionVotes := map[string][]deliberation.Vote{}
	for _, v := range votes {
		if v.CriterionID != "" {
			criterionVotes[v.CriterionID] = append(criterionVotes[v.CriterionID], v)
		}
	}
	for cID, cVotes := range criterionVotes {
		cConsensus := findConsensus(ctx, positions, cVotes, clusters, effectiveWeights)
		criteriaResults[cID] = map[string]any{
			"consensus_count": len(cConsensus),
			"consensus":       cConsensus,
		}
	}

	// ZOPA: Zone of Possible Agreement
	zopa := ComputeZOPA(positions, consensus, bridging)

	// Pareto analysis: identify Pareto-efficient proposals when criteria are defined
	var paretoEfficient, dominatedProposals []string
	if len(criteriaResults) > 0 && !refused && (len(consensus) > 0 || len(bridging) > 0) {
		paretoEfficient, dominatedProposals = a.analyzeParetoSurface(ctx, deliberationTopic, consensus, bridging, criteriaResults)
	}

	// Determine confidence level based on agent count and vote data
	confidence := "low" // 3-4 agents
	if refused {
		confidence = "refused"
	} else if len(agents) >= 10 && len(votes) > 0 {
		confidence = "high"
	} else if len(agents) >= 5 {
		confidence = "medium"
	}

	// Detect coalitions from crux alignment
	coalitions := detectCoalitions(cruxes, agents)

	// Extract emergent norms from deliberation patterns (CRSEC, IJCAI 2024)
	var emergentNorms []string
	if len(cruxes) > 0 && len(consensus) > 0 {
		emergentNorms = append(emergentNorms, "Positions that address identified cruxes directly receive more engagement")
	}
	if len(bridging) > 0 {
		emergentNorms = append(emergentNorms, "Cross-cluster bridging positions are more effective than single-cluster positions")
	}
	agentPositionCount := map[string]int{}
	for _, p := range positions {
		agentPositionCount[p.AgentID]++
	}
	multiPosition := 0
	for _, c := range agentPositionCount {
		if c > 1 {
			multiPosition++
		}
	}
	if multiPosition > len(agents)/2 {
		emergentNorms = append(emergentNorms, "Multi-round position refinement is the norm — agents refine rather than repeat")
	}

	// Extract constitutional rules from consensus + bridging
	var constitutionalRules []string
	for _, cs := range consensus {
		if cs.OverallAgreeRatio >= 0.8 {
			constitutionalRules = append(constitutionalRules, cs.Content)
		}
	}
	for _, bs := range bridging {
		if bs.BridgingScore >= 0.7 {
			constitutionalRules = append(constitutionalRules, bs.Content)
		}
	}

	// Check positions against prior constitutional rules
	var ruleViolations []string
	if rules, ok := ctx.Value(deliberation.ContextKeyConstitutionalRules{}).([]string); ok {
		for _, p := range positions {
			for _, rule := range rules {
				if positionContradictsRule(p.Content, rule) {
					violation := fmt.Sprintf("Agent %s's position may contradict constitutional rule: %s",
						p.AgentID, truncateStr(rule, 80))
					ruleViolations = append(ruleViolations, violation)
					warnings = append(warnings, "RULE_VIOLATION: "+violation)
				}
			}
		}
	}

	// Reuse weights computed earlier (before consensus/bridging)
	trustWeights := trustWeightsEarly
	correlationWeights := correlationWeightsEarly

	// BATNA: estimate failure scenarios from cruxes and reservations
	var failureScenarios []string
	for _, crux := range cruxes {
		if crux.ControversyScore >= 0.7 {
			failureScenarios = append(failureScenarios,
				fmt.Sprintf("If no resolution on %q: agents %v and %v remain deadlocked",
					truncateStr(crux.Claim, 80), crux.AgreeAgents, crux.DisagreeAgents))
		}
	}
	for _, p := range positions {
		if p.Reservation != "" {
			failureScenarios = append(failureScenarios,
				fmt.Sprintf("Agent %s cannot accept: %s", p.AgentID, truncateStr(p.Reservation, 100)))
		}
	}

	// Audit: final counts
	audit = append(audit, deliberation.AuditEntry{
		Stage: "crux_detection", Detail: fmt.Sprintf("%d cruxes detected", len(cruxes)), Count: len(cruxes),
	})
	audit = append(audit, deliberation.AuditEntry{
		Stage: "clustering", Detail: fmt.Sprintf("%d clusters, %d consensus, %d bridging",
			len(clusters), len(consensus), len(bridging)),
	})
	audit = append(audit, deliberation.AuditEntry{
		Stage: "summaries", Detail: fmt.Sprintf("%d topic summaries", len(summaries)), Count: len(summaries),
	})

	result := &deliberation.AnalysisResult{
		Clusters:            clusters,
		Coalitions:          coalitions,
		ConstitutionalRules: constitutionalRules,
		FailureScenarios:    failureScenarios,
		ZOPA:                zopa,
		CriteriaResults:     criteriaResults,
		EmergentNorms:       emergentNorms,
		RuleViolations:      ruleViolations,
		Cruxes:              cruxes,
		ConsensusStatements: consensus,
		BridgingStatements:  bridging,
		TopicSummaries:      summaries,
		AgentCount:          len(agents),
		PositionCount:       len(positions),
		VoteCount:           len(votes),
		Confidence:          confidence,
		TrustWeights:        trustWeights,
		CorrelationWeights:  correlationWeights,
		EffectiveWeights:    effectiveWeights,
		IntegrityWarnings:    warnings,
		AuditLog:             audit,
		ParticipationRate:    participationRate(len(agents), len(positions), len(votes)),
		PerspectiveDiversity: perspectiveDiversity(len(clusters), len(agents)),
		ParetoEfficient:     paretoEfficient,
		DominatedProposals:  dominatedProposals,
	}

	log.Printf("[gemot] analysis complete: deliberation=%s round=%d agents=%d positions=%d cruxes=%d clusters=%d duration=%s",
		result.DeliberationID, result.Round, result.AgentCount, result.PositionCount, len(result.Cruxes), len(result.Clusters), time.Since(startTime))

	return result, nil
}

// --- LLM call methods ---

func (a *TextAnalyzer) getTaxonomy(ctx context.Context, deliberationTopic, positionText string) (*taxonomyResult, error) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"topics": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"topic_name":        map[string]any{"type": "string"},
						"topic_description": map[string]any{"type": "string"},
						"subtopics": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"subtopic_name":        map[string]any{"type": "string"},
									"subtopic_description": map[string]any{"type": "string"},
								},
								"required": []string{"subtopic_name", "subtopic_description"},
							},
						},
					},
					"required": []string{"topic_name", "topic_description", "subtopics"},
				},
			},
		},
		"required": []string{"topics"},
	}

	prompt := fmt.Sprintf(taxonomyPrompt, deliberationTopic, positionText)
	var result taxonomyResult
	if err := a.structuredOutput(ctx, systemPrompt, prompt, schema, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// buildConstrainedClaimSchema creates a JSON schema for claim extraction where
// topic_name and subtopic_name are constrained to enum values from the taxonomy.
// This prevents the LLM from generating claims mapped to nonexistent subtopics.
// Adapted from T3C's structured_schemas.py.
func buildConstrainedClaimSchema(taxonomy *taxonomyResult) map[string]any {
	// Collect all valid topic and subtopic names
	var topicNames []any
	var subtopicNames []any
	for _, topic := range taxonomy.Topics {
		topicNames = append(topicNames, topic.TopicName)
		for _, st := range topic.Subtopics {
			subtopicNames = append(subtopicNames, st.SubtopicName)
		}
	}

	// Build schema with enum constraints
	topicField := map[string]any{"type": "string"}
	subtopicField := map[string]any{"type": "string"}
	if len(topicNames) > 0 {
		topicField["enum"] = topicNames
	}
	if len(subtopicNames) > 0 {
		subtopicField["enum"] = subtopicNames
	}

	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"claims": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"claim":         map[string]any{"type": "string"},
						"quote":         map[string]any{"type": "string"},
						"topic_name":    topicField,
						"subtopic_name": subtopicField,
					},
					"required": []string{"claim", "quote", "topic_name", "subtopic_name"},
				},
			},
		},
		"required": []string{"claims"},
	}
}

func (a *TextAnalyzer) extractClaimsConstrained(ctx context.Context, agentNum, deliberationTopic, content, taxonomyText string, schema map[string]any, preamble string) ([]extractedClaim, error) {
	prompt := preamble + fmt.Sprintf(claimExtractionPrompt, agentNum, deliberationTopic, taxonomyText, content)

	// Check cache (T3C pattern: cache per-comment LLM responses)
	if a.cache != nil {
		cacheKey := claimCacheKey(content, taxonomyText, ctx)
		if cached := a.cache.Get(cacheKey); cached != "" {
			var result claimExtractionResult
			if err := json.Unmarshal([]byte(cached), &result); err == nil {
				return result.Claims, nil
			}
		}
	}

	var result claimExtractionResult
	if err := a.structuredOutput(ctx, systemPrompt, prompt, schema, &result); err != nil {
		return nil, err
	}

	// Store in cache
	if a.cache != nil {
		cacheKey := claimCacheKey(content, taxonomyText, ctx)
		if b, err := json.Marshal(result); err == nil {
			model := ""
			if m, ok := ctx.Value(llm.ContextKeyModel{}).(string); ok {
				model = m
			}
			a.cache.Put(cacheKey, string(b), model)
		}
	}

	return result.Claims, nil
}

func (a *TextAnalyzer) deduplicateClaims(ctx context.Context, deliberationTopic, subtopicName string, claims []claim) ([]claim, error) {
	if len(claims) <= 1 {
		return claims, nil
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"groups": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"claim_text":         map[string]any{"type": "string"},
						"original_claim_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
					},
					"required": []string{"claim_text", "original_claim_ids"},
				},
			},
		},
		"required": []string{"groups"},
	}

	// Format claims with IDs
	var sb strings.Builder
	for i, c := range claims {
		fmt.Fprintf(&sb, "<claim id=\"%d\" participant=\"%s\">%s</claim>\n", i, c.AgentNum, c.Claim)
	}

	prompt := fmt.Sprintf(claimDeduplicationPrompt, deliberationTopic, subtopicName, sb.String())
	var result claimDeduplicationResult
	if err := a.structuredOutput(ctx, systemPrompt, prompt, schema, &result); err != nil {
		return nil, err
	}

	// Build deduplicated claims: each group becomes one claim, preserving all source agents
	// and collecting source quotes from all original claims in the group.
	var deduped []claim
	for _, g := range result.Groups {
		// Collect all agents and source quotes from the original claims in this group
		agentSet := map[string]bool{}
		var sources []claimSource
		var firstClaim claim
		for _, id := range g.OriginalClaimIDs {
			if id >= 0 && id < len(claims) {
				c := claims[id]
				agentSet[c.AgentID] = true
				if firstClaim.AgentID == "" {
					firstClaim = c
				}
				if c.Quote != "" {
					sources = append(sources, claimSource{
						PositionID: c.PositionID,
						AgentID:    c.AgentID,
						Quote:      c.Quote,
						ClaimText:  c.Claim,
					})
				}
			}
		}
		// Use the group's higher-level claim text, but keep the first agent's identity
		// for the crux detection stage (which needs agent attribution).
		// Attach all source quotes so they can be traced through to crux generation.
		for agentID := range agentSet {
			num := ""
			posID := ""
			for _, c := range claims {
				if c.AgentID == agentID {
					num = c.AgentNum
					if posID == "" {
						posID = c.PositionID
					}
					break
				}
			}
			deduped = append(deduped, claim{
				AgentID:      agentID,
				AgentNum:     num,
				PositionID:   posID,
				Claim:        g.ClaimText,
				TopicName:    firstClaim.TopicName,
				SubtopicName: firstClaim.SubtopicName,
				Sources:      sources,
			})
		}
	}

	if len(deduped) == 0 {
		return claims, nil // fall back if dedup produced nothing
	}
	return deduped, nil
}

func (a *TextAnalyzer) getCrux(ctx context.Context, deliberationTopic, topicName, subtopicName, subtopicDesc, claimsText string, numToAgent map[string]string) (*deliberation.Crux, error) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"crux_claim":        map[string]any{"type": "string"},
			"agree":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"disagree":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"no_clear_position": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"explanation":       map[string]any{"type": "string"},
		},
		"required": []string{"crux_claim", "agree", "disagree", "no_clear_position", "explanation"},
	}

	// Multi-candidate crux generation: generate 3 candidates, pick the most balanced
	type candidate struct {
		crux    *deliberation.Crux
		balance float64
	}
	var candidates []candidate

	for attempt := 1; attempt <= 3; attempt++ {
		differentiation := ""
		if attempt > 1 {
			differentiation = fmt.Sprintf("\nCandidate %d of 3: try to find a DIFFERENT crux than previous attempts. Look for an alternative dividing line among these participants.\n\n", attempt)
		}

		prompt := differentiation + fmt.Sprintf(cruxPrompt, deliberationTopic, topicName, subtopicName, subtopicDesc, claimsText)
		var result cruxResult
		if err := a.structuredOutput(ctx, systemPrompt, prompt, schema, &result); err != nil {
			continue
		}

		crux := &deliberation.Crux{
			Claim:           result.CruxClaim,
			AgreeAgents:     deAnonymize(result.Agree, numToAgent),
			DisagreeAgents:  deAnonymize(result.Disagree, numToAgent),
			NoClearPosition: deAnonymize(result.NoClearPosition, numToAgent),
			Explanation:     result.Explanation,
		}

		// Balance score: min(agree, disagree) * 2 / total (0=unanimous, 1.0=50/50)
		total := float64(len(crux.AgreeAgents) + len(crux.DisagreeAgents) + len(crux.NoClearPosition))
		if total > 0 {
			agreeRatio := float64(len(crux.AgreeAgents)) / total
			disagreeRatio := float64(len(crux.DisagreeAgents)) / total
			crux.ControversyScore = math.Min(agreeRatio, disagreeRatio) * 2
		}

		balance := 0.0
		totalSides := len(crux.AgreeAgents) + len(crux.DisagreeAgents)
		if totalSides > 0 {
			minSide := len(crux.AgreeAgents)
			if len(crux.DisagreeAgents) < minSide {
				minSide = len(crux.DisagreeAgents)
			}
			balance = float64(minSide*2) / float64(totalSides)
		}

		candidates = append(candidates, candidate{crux: crux, balance: balance})
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("all 3 crux candidates failed")
	}

	// Return the candidate with the highest balance score
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.balance > best.balance {
			best = c
		}
	}

	return best.crux, nil
}

func (a *TextAnalyzer) getSummary(ctx context.Context, deliberationTopic, topicName, positions string) (string, error) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
		},
		"required": []string{"summary"},
	}

	prompt := fmt.Sprintf(summaryPrompt, deliberationTopic, topicName, positions)
	var result summaryResult
	if err := a.structuredOutput(ctx, systemPrompt, prompt, schema, &result); err != nil {
		return "", err
	}
	return result.Summary, nil
}

// --- Helpers ---

func formatPositions(positions []deliberation.Position, agentToNum map[string]string) string {
	var sb strings.Builder
	for _, p := range positions {
		num := agentToNum[p.AgentID]
		content := p.Content
		// Inject declared interests so LLM sees what the agent optimizes for
		if p.Interests != "" {
			content = "[Declared interests: " + p.Interests + "]\n" + content
		}
		fmt.Fprintf(&sb, "<position participant=\"%s\">%s</position>\n\n", num, content)
	}
	return sb.String()
}

func formatTaxonomy(t *taxonomyResult) string {
	var sb strings.Builder
	for _, topic := range t.Topics {
		fmt.Fprintf(&sb, "Topic: %s — %s\n", topic.TopicName, topic.TopicDescription)
		for _, st := range topic.Subtopics {
			fmt.Fprintf(&sb, "  Subtopic: %s — %s\n", st.SubtopicName, st.SubtopicDescription)
		}
	}
	return sb.String()
}

func filterClaimsByTopic(claims []claim, topicName string) []claim {
	topicName = strings.TrimSpace(topicName)
	var filtered []claim
	for _, c := range claims {
		if strings.EqualFold(strings.TrimSpace(c.TopicName), topicName) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

func filterClaimsBySubtopic(claims []claim, topicName, subtopicName string) []claim {
	topicName = strings.TrimSpace(topicName)
	subtopicName = strings.TrimSpace(subtopicName)
	var filtered []claim
	for _, c := range claims {
		if strings.EqualFold(strings.TrimSpace(c.TopicName), topicName) && strings.EqualFold(strings.TrimSpace(c.SubtopicName), subtopicName) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// claimCacheKey generates a SHA256 cache key for claim extraction.
// Key components: position content + normalized taxonomy + model (T3C pattern).
func claimCacheKey(content, taxonomyText string, ctx context.Context) string {
	model := "default"
	if m, ok := ctx.Value(llm.ContextKeyModel{}).(string); ok {
		model = m
	}
	h := sha256.New()
	h.Write([]byte(content))
	h.Write([]byte("|"))
	h.Write([]byte(taxonomyText))
	h.Write([]byte("|"))
	h.Write([]byte(model))
	return "claims:" + hex.EncodeToString(h.Sum(nil))
}

// detectCoalitions identifies subsets of agents that consistently agree on cruxes.
func detectCoalitions(cruxes []deliberation.Crux, agents []string) []deliberation.Coalition {
	if len(cruxes) == 0 || len(agents) < 3 {
		return nil
	}

	// Build agreement matrix: for each pair of agents, count cruxes where they're on the same side
	type pair struct{ a, b string }
	agreement := map[pair]int{}
	totalCruxes := len(cruxes)

	for _, crux := range cruxes {
		agreeSet := map[string]bool{}
		disagreeSet := map[string]bool{}
		for _, a := range crux.AgreeAgents {
			agreeSet[a] = true
		}
		for _, a := range crux.DisagreeAgents {
			disagreeSet[a] = true
		}
		// Agents on the same side agree
		for i, a := range agents {
			for j := i + 1; j < len(agents); j++ {
				b := agents[j]
				if (agreeSet[a] && agreeSet[b]) || (disagreeSet[a] && disagreeSet[b]) {
					agreement[pair{a, b}]++
				}
			}
		}
	}

	// Find coalitions: groups of agents that agree on >50% of cruxes
	// Simple greedy: start with highest-agreement pairs, grow coalitions
	var coalitions []deliberation.Coalition
	used := map[string]bool{}

	for i, a := range agents {
		if used[a] {
			continue
		}
		coalition := []string{a}
		for j := i + 1; j < len(agents); j++ {
			b := agents[j]
			if used[b] {
				continue
			}
			// Check if b agrees with all current coalition members on >50% of cruxes
			allAgree := true
			minAgreement := totalCruxes
			for _, member := range coalition {
				p := pair{member, b}
				if member > b {
					p = pair{b, member}
				}
				ag := agreement[p]
				if ag < minAgreement {
					minAgreement = ag
				}
				if ag <= totalCruxes/2 {
					allAgree = false
					break
				}
			}
			if allAgree {
				coalition = append(coalition, b)
			}
		}
		if len(coalition) >= 2 {
			// Compute stability: average agreement ratio across all pairs
			totalPairs := 0
			totalAg := 0
			for ci, ca := range coalition {
				for cj := ci + 1; cj < len(coalition); cj++ {
					cb := coalition[cj]
					p := pair{ca, cb}
					if ca > cb {
						p = pair{cb, ca}
					}
					totalPairs++
					totalAg += agreement[p]
				}
			}
			stability := 0.0
			if totalPairs > 0 && totalCruxes > 0 {
				stability = float64(totalAg) / float64(totalPairs*totalCruxes)
			}

			coalitions = append(coalitions, deliberation.Coalition{
				AgentIDs:       coalition,
				SharedCruxes:   totalAg / max(totalPairs, 1),
				StabilityScore: math.Round(stability*100) / 100,
			})
			for _, m := range coalition {
				used[m] = true
			}
		}
	}

	return coalitions
}

// positionContradictsRule checks if a position text appears to contradict a constitutional rule.
// Uses negation keyword heuristic: if the position says "should not/never/oppose X" and
// the rule says "X", there may be a contradiction.
func positionContradictsRule(position, rule string) bool {
	posLower := strings.ToLower(position)
	ruleLower := strings.ToLower(rule)

	// Extract key content words from the rule (>4 chars)
	ruleWords := strings.Fields(ruleLower)
	var keyWords []string
	stopWords := map[string]bool{"should": true, "would": true, "could": true, "about": true, "their": true, "there": true, "these": true, "those": true, "which": true, "where": true}
	for _, w := range ruleWords {
		w = strings.Trim(w, ".,;:!?\"'")
		if len(w) > 4 && !stopWords[w] {
			keyWords = append(keyWords, w)
		}
	}
	if len(keyWords) == 0 {
		return false
	}

	// Check if position negates the rule's key concepts
	negMarkers := []string{"should not", "must not", "oppose", "reject", "against", "never", "eliminate", "remove", "abolish"}
	for _, marker := range negMarkers {
		if strings.Contains(posLower, marker) {
			// Count how many rule keywords appear near the negation
			idx := strings.Index(posLower, marker)
			vicinity := posLower[max(0, idx-20):min(len(posLower), idx+len(marker)+60)]
			matchCount := 0
			for _, kw := range keyWords {
				if strings.Contains(vicinity, kw) {
					matchCount++
				}
			}
			if matchCount >= 2 {
				return true
			}
		}
	}
	return false
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func collectPositionIDs(claims []claim) []string {
	seen := map[string]bool{}
	var ids []string
	for _, c := range claims {
		if c.PositionID != "" && !seen[c.PositionID] {
			seen[c.PositionID] = true
			ids = append(ids, c.PositionID)
		}
		// Also collect IDs from dedup sources
		for _, s := range c.Sources {
			if s.PositionID != "" && !seen[s.PositionID] {
				seen[s.PositionID] = true
				ids = append(ids, s.PositionID)
			}
		}
	}
	return ids
}

// collectSourceQuotes gathers all unique source quotes from claims.
func collectSourceQuotes(claims []claim) []deliberation.SourceQuote {
	type quoteKey struct{ posID, quote string }
	seen := map[quoteKey]bool{}
	var quotes []deliberation.SourceQuote

	for _, c := range claims {
		// Direct quote on the claim itself
		if c.Quote != "" && c.PositionID != "" {
			k := quoteKey{c.PositionID, c.Quote}
			if !seen[k] {
				seen[k] = true
				quotes = append(quotes, deliberation.SourceQuote{
					PositionID: c.PositionID,
					AgentID:    c.AgentID,
					Quote:      c.Quote,
					ClaimText:  c.Claim,
				})
			}
		}
		// Quotes from dedup sources
		for _, s := range c.Sources {
			k := quoteKey{s.PositionID, s.Quote}
			if !seen[k] {
				seen[k] = true
				quotes = append(quotes, deliberation.SourceQuote{
					PositionID: s.PositionID,
					AgentID:    s.AgentID,
					Quote:      s.Quote,
					ClaimText:  s.ClaimText,
				})
			}
		}
	}
	return quotes
}

func uniqueSpeakers(claims []claim) map[string]bool {
	speakers := map[string]bool{}
	for _, c := range claims {
		speakers[c.AgentNum] = true
	}
	return speakers
}

func formatClaimsForCrux(claims []claim) string {
	var sb strings.Builder
	for _, c := range claims {
		// Include quote when available so the LLM can ground cruxes in evidence
		if c.Quote != "" {
			fmt.Fprintf(&sb, "<claim participant=\"%s\" quote=\"%s\">%s</claim>\n", c.AgentNum, c.Quote, c.Claim)
		} else if len(c.Sources) > 0 {
			// Use first source quote from dedup
			fmt.Fprintf(&sb, "<claim participant=\"%s\" quote=\"%s\">%s</claim>\n", c.AgentNum, c.Sources[0].Quote, c.Claim)
		} else {
			fmt.Fprintf(&sb, "<claim participant=\"%s\">%s</claim>\n", c.AgentNum, c.Claim)
		}
	}
	return sb.String()
}

func deAnonymize(nums []string, numToAgent map[string]string) []string {
	result := make([]string, 0, len(nums))
	for _, n := range nums {
		if agent, ok := numToAgent[n]; ok {
			result = append(result, agent)
		}
	}
	return result
}

// classifyCruxes uses LLM to classify each crux as factual/value/mixed.
// Modifies cruxes in place. Failures are silently ignored (classification is optional enrichment).
func (a *TextAnalyzer) classifyCruxes(ctx context.Context, topic string, cruxes []deliberation.Crux) {
	claims := make([]map[string]string, len(cruxes))
	for i, c := range cruxes {
		claims[i] = map[string]string{"claim": c.Claim}
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return
	}
	prompt := fmt.Sprintf(cruxClassificationPrompt, topic, string(claimsJSON))
	schema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"claim":         map[string]any{"type": "string"},
				"type":          map[string]any{"type": "string", "enum": []string{"factual", "value", "mixed"}},
				"resolvability": map[string]any{"type": "number"},
			},
			"required": []string{"claim", "type", "resolvability"},
		},
	}
	var results []struct {
		Claim         string  `json:"claim"`
		Type          string  `json:"type"`
		Resolvability float64 `json:"resolvability"`
	}
	if err := a.structuredOutput(ctx, systemPrompt, prompt, schema, &results); err != nil {
		return
	}
	// Match results back to cruxes by claim text
	for _, r := range results {
		for i := range cruxes {
			if cruxes[i].Claim == r.Claim {
				cruxes[i].CruxType = r.Type
				cruxes[i].Resolvability = r.Resolvability
				break
			}
		}
	}
}

// buildClusters creates opinion clusters from crux alignment patterns.
func buildClusters(cruxes []deliberation.Crux, agents []string) []deliberation.OpinionCluster {
	if len(cruxes) == 0 || len(agents) < 2 {
		return []deliberation.OpinionCluster{{
			ID:       0,
			AgentIDs: agents,
			Size:     len(agents),
		}}
	}

	// Group agents by their agree/disagree pattern across cruxes
	agentPatterns := map[string][]bool{}
	for _, agent := range agents {
		p := make([]bool, len(cruxes))
		for i, crux := range cruxes {
			for _, a := range crux.AgreeAgents {
				if a == agent {
					p[i] = true
					break
				}
			}
		}
		agentPatterns[agent] = p
	}

	type group struct {
		pattern []bool
		agents  []string
	}
	var groups []group
	for agent, p := range agentPatterns {
		found := false
		for i, g := range groups {
			if patternsEqual(g.pattern, p) {
				groups[i].agents = append(groups[i].agents, agent)
				found = true
				break
			}
		}
		if !found {
			groups = append(groups, group{pattern: p, agents: []string{agent}})
		}
	}

	clusters := make([]deliberation.OpinionCluster, len(groups))
	for i, g := range groups {
		clusters[i] = deliberation.OpinionCluster{
			ID:       i,
			AgentIDs: g.agents,
			Size:     len(g.agents),
		}
	}
	return clusters
}

func patternsEqual(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// findConsensus identifies positions with broad agreement across clusters.
// consensusThreshold returns the supermajority threshold based on deliberation type.
// Reasoning tasks use a higher threshold (voting is more reliable).
// Knowledge/policy tasks use the standard 67%.
func consensusThreshold(ctx context.Context) float64 {
	// Template threshold takes precedence if set
	if tmplName, ok := ctx.Value(deliberation.ContextKeyTemplate{}).(string); ok {
		if tmpl, found := deliberation.GetTemplate(tmplName); found && tmpl.SuggestedThreshold > 0 {
			return tmpl.SuggestedThreshold
		}
	}
	// Fall back to type-based threshold
	if dt, ok := ctx.Value(deliberation.ContextKeyDeliberationType{}).(string); ok {
		switch dt {
		case "reasoning":
			return 0.75 // higher bar for reasoning — voting is more reliable here (ACL 2025)
		case "negotiation":
			return 0.60 // lower bar for negotiation — any cross-party agreement is valuable
		}
	}
	return 0.67 // default supermajority
}

func findConsensus(ctx context.Context, positions []deliberation.Position, votes []deliberation.Vote, clusters []deliberation.OpinionCluster, weights map[string]float64) []deliberation.ConsensusStatement {
	if len(votes) == 0 {
		return []deliberation.ConsensusStatement{}
	}

	threshold := consensusThreshold(ctx)

	// Adaptive quorum: for non-templated deliberations, scale threshold with group size
	// 4 agents → 50%, 9 agents → 67%, 25 agents → 80%
	// Only applies when no template set (templates have explicit thresholds)
	if _, hasTemplate := ctx.Value(deliberation.ContextKeyTemplate{}).(string); !hasTemplate {
		uniqueVoters := map[string]bool{}
		for _, v := range votes {
			uniqueVoters[v.AgentID] = true
		}
		if n := len(uniqueVoters); n > 3 {
			adaptive := 1.0 - 1.0/math.Sqrt(float64(n))
			if adaptive > threshold {
				threshold = adaptive
			}
		}
	}

	// Helper: get effective weight for an agent (default 1.0)
	w := func(agentID string) float64 {
		if weights != nil {
			if wt, ok := weights[agentID]; ok {
				return wt
			}
		}
		return 1.0
	}

	voteMap := map[string]map[string]int{}
	for _, v := range votes {
		if _, ok := voteMap[v.PositionID]; !ok {
			voteMap[v.PositionID] = map[string]int{}
		}
		voteMap[v.PositionID][v.AgentID] = v.Value
	}

	agentCluster := map[string]int{}
	for _, c := range clusters {
		for _, a := range c.AgentIDs {
			agentCluster[a] = c.ID
		}
	}

	var consensus []deliberation.ConsensusStatement
	for _, p := range positions {
		agentVotes, ok := voteMap[p.ID]
		if !ok || len(agentVotes) == 0 {
			continue
		}

		// Weighted agree/total computation
		weightedAgrees := 0.0
		weightedTotal := 0.0
		for agent, v := range agentVotes {
			aw := w(agent)
			weightedTotal += aw
			if v == 1 {
				weightedAgrees += aw
			}
		}
		overallRatio := 0.0
		if weightedTotal > 0 {
			overallRatio = weightedAgrees / weightedTotal
		}

		// Weighted per-cluster ratios
		clusterAgrees := map[int]float64{}
		clusterTotal := map[int]float64{}
		for agent, v := range agentVotes {
			cid := agentCluster[agent]
			aw := w(agent)
			clusterTotal[cid] += aw
			if v == 1 {
				clusterAgrees[cid] += aw
			}
		}

		minClusterRatio := 1.0
		for cid, total := range clusterTotal {
			ratio := 0.0
			if total > 0 {
				ratio = clusterAgrees[cid] / total
			}
			if ratio < minClusterRatio {
				minClusterRatio = ratio
			}
		}

		// Adaptive consensus threshold based on deliberation type
		if overallRatio >= threshold && minClusterRatio >= threshold {
			consensus = append(consensus, deliberation.ConsensusStatement{
				PositionID:           p.ID,
				Content:              p.Content,
				OverallAgreeRatio:    overallRatio,
				MinClusterAgreeRatio: minClusterRatio,
			})
		}
	}
	return consensus
}

// findBridging identifies positions that get agreement across opposing clusters.
// A bridging statement is one where every cluster has at least 40% agreement and
// the overall agree rate is at least 50%. Sorted by bridging score (min cluster agree ratio).
func findBridging(positions []deliberation.Position, votes []deliberation.Vote, clusters []deliberation.OpinionCluster, weights map[string]float64) []deliberation.BridgingStatement {
	if len(votes) == 0 || len(clusters) < 2 {
		return nil
	}

	w := func(agentID string) float64 {
		if weights != nil {
			if wt, ok := weights[agentID]; ok {
				return wt
			}
		}
		return 1.0
	}

	voteMap := map[string]map[string]int{}
	for _, v := range votes {
		if _, ok := voteMap[v.PositionID]; !ok {
			voteMap[v.PositionID] = map[string]int{}
		}
		voteMap[v.PositionID][v.AgentID] = v.Value
	}

	agentCluster := map[string]int{}
	for _, c := range clusters {
		for _, a := range c.AgentIDs {
			agentCluster[a] = c.ID
		}
	}

	var bridging []deliberation.BridgingStatement
	for _, p := range positions {
		agentVotes, ok := voteMap[p.ID]
		if !ok || len(agentVotes) == 0 {
			continue
		}

		weightedAgrees := 0.0
		weightedTotal := 0.0
		for agent, v := range agentVotes {
			aw := w(agent)
			weightedTotal += aw
			if v == 1 {
				weightedAgrees += aw
			}
		}
		overallRatio := 0.0
		if weightedTotal > 0 {
			overallRatio = weightedAgrees / weightedTotal
		}

		clusterAgrees := map[int]float64{}
		clusterTotal := map[int]float64{}
		for agent, v := range agentVotes {
			cid := agentCluster[agent]
			aw := w(agent)
			clusterTotal[cid] += aw
			if v == 1 {
				clusterAgrees[cid] += aw
			}
		}

		if len(clusterTotal) < 2 {
			continue
		}

		minRatio := 1.0
		clusterRates := map[string]float64{}
		for cid, total := range clusterTotal {
			ratio := 0.0
			if total > 0 {
				ratio = clusterAgrees[cid] / total
			}
			clusterRates[fmt.Sprintf("cluster_%d", cid)] = math.Round(ratio*100) / 100
			if ratio < minRatio {
				minRatio = ratio
			}
		}

		// Bridging: every cluster at least 40% agree, overall at least 50%
		if minRatio >= 0.4 && overallRatio >= 0.5 {
			bridging = append(bridging, deliberation.BridgingStatement{
				PositionID:       p.ID,
				AgentID:          p.AgentID,
				Content:          p.Content,
				BridgingScore:    math.Round(minRatio*100) / 100,
				OverallAgreeRate: math.Round(overallRatio*100) / 100,
				ClusterAgreeRate: clusterRates,
			})
		}
	}

	// Sort by bridging score descending
	sort.Slice(bridging, func(i, j int) bool {
		return bridging[i].BridgingScore > bridging[j].BridgingScore
	})

	// Return top 5
	if len(bridging) > 5 {
		bridging = bridging[:5]
	}
	return bridging
}

// participationRate = votes / (agents × positions). 1.0 = every agent voted on every position.
func participationRate(agents, positions, votes int) float64 {
	if agents == 0 || positions == 0 {
		return 0
	}
	maxVotes := agents * positions
	rate := float64(votes) / float64(maxVotes)
	if rate > 1 {
		rate = 1
	}
	return math.Round(rate*100) / 100
}

// perspectiveDiversity = clusters / agents. Higher = more diverse opinion landscape.
func perspectiveDiversity(clusters, agents int) float64 {
	if agents == 0 {
		return 0
	}
	diversity := float64(clusters) / float64(agents)
	return math.Round(diversity*100) / 100
}

// analyzeParetoSurface identifies Pareto-efficient proposals via LLM analysis.
// Only called when multi-criteria voting is used.
func (a *TextAnalyzer) analyzeParetoSurface(ctx context.Context, topic string, consensus []deliberation.ConsensusStatement, bridging []deliberation.BridgingStatement, criteria map[string]any) (paretoEfficient, dominated []string) {
	// Build proposals text
	var proposals []string
	for _, c := range consensus {
		proposals = append(proposals, c.PositionID+": "+c.Content)
	}
	for _, b := range bridging {
		proposals = append(proposals, b.Content)
	}
	if len(proposals) < 2 {
		return nil, nil // need 2+ proposals to compare
	}

	// Build criteria text
	criteriaText := ""
	for k := range criteria {
		criteriaText += "- " + k + "\n"
	}

	proposalsText := strings.Join(proposals, "\n\n")
	prompt := fmt.Sprintf(paretoPrompt, topic, proposalsText, criteriaText)

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pareto_efficient": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"dominated":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"pareto_efficient", "dominated"},
	}

	var result struct {
		ParetoEfficient []string `json:"pareto_efficient"`
		Dominated       []string `json:"dominated"`
	}
	if err := a.structuredOutput(ctx, systemPrompt, prompt, schema, &result); err != nil {
		return nil, nil // soft fail
	}
	return result.ParetoEfficient, result.Dominated
}
