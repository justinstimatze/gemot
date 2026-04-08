package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// controversyLabel returns an ordinal label factoring in effective sample size.
func controversyLabel(score float64, nAgree, nDisagree int) string {
	total := nAgree + nDisagree
	if total <= 2 {
		return "Divided (small N)"
	}
	switch {
	case score >= 0.9:
		return "Sharp division"
	case score >= 0.7:
		return "Strong disagreement"
	case score >= 0.55:
		return "Moderate disagreement"
	case score >= 0.4:
		return "Contested"
	default:
		return "Mild disagreement"
	}
}

func stanceLabel(value int) string {
	switch value {
	case 2:
		return "+2"
	case 1:
		return "+1"
	case 0:
		return " 0"
	case -1:
		return "-1"
	case -2:
		return "-2"
	default:
		return fmt.Sprintf("%+d", value)
	}
}

func isStructuralAgent(id string) bool {
	return strings.Contains(id, "probe") ||
		strings.Contains(id, "adversary") ||
		strings.Contains(id, "bridge") ||
		strings.Contains(id, "dissent") ||
		strings.Contains(id, "empty-chair") ||
		strings.Contains(id, "resolution")
}

// hasRealSpeakerCruxes checks if any crux has a non-structural agent in agree, disagree, or stances.
func hasRealSpeakerCruxes(a *analysisResult) bool {
	for _, c := range a.Cruxes {
		for _, agent := range c.Agree {
			if !isStructuralAgent(agent) {
				return true
			}
		}
		for _, agent := range c.Disagree {
			if !isStructuralAgent(agent) {
				return true
			}
		}
		for _, st := range c.Stances {
			if !isStructuralAgent(st.AgentID) {
				return true
			}
		}
	}
	return false
}

func generateReport(ri *reportInput) string {
	data := ri.Data
	agents := ri.R1Agents
	r2Agents := ri.R2Agents
	r3Agents := ri.R3Agents
	r2JSON := ri.R2JSON
	r3JSON := ri.R3JSON
	r1Compromise := ri.R1Compromise
	r3Compromise := ri.R3Compromise
	tmpl := ri.Template
	delibID := ri.DelibID
	joinCode := ri.JoinCode
	ncResult := ri.NullControl
	scResult := ri.SpotCheck
	cruxSCResult := ri.CruxSpotCheck
	repResult := ri.Replication
	covResult := ri.Coverage

	var r1 analysisResult
	json.Unmarshal([]byte(ri.R1JSON), &r1)
	var r2 analysisResult
	if r2JSON != "" {
		json.Unmarshal([]byte(r2JSON), &r2)
	}
	var r3 analysisResult
	if r3JSON != "" {
		json.Unmarshal([]byte(r3JSON), &r3)
	}

	// Classify agents
	var steelmen, speakers, probes []agentPlan
	for _, a := range agents {
		switch a.Kind {
		case "steelman":
			steelmen = append(steelmen, a)
		case "speaker":
			speakers = append(speakers, a)
		case "probe":
			probes = append(probes, a)
		}
	}
	nSpeakerDerived := len(steelmen) + len(speakers)
	nStructural := len(probes) + len(r2Agents)
	nRevised := len(r3Agents)
	totalAgents := len(agents) + len(r2Agents) + len(r3Agents)

	// Pick the final round's analysis for the main body
	finalAnalysis := &r1
	finalCompromise := r1Compromise
	if r3JSON != "" {
		finalAnalysis = &r3
		finalCompromise = r3Compromise
	} else if r2JSON != "" {
		finalAnalysis = &r2
		finalCompromise = ri.R2Compromise
	}

	// For Key Disagreements, prefer the round with the most real-speaker cruxes.
	// If R3/R2 cruxes only have structural agents, fall back to R1.
	cruxAnalysis := finalAnalysis
	if !hasRealSpeakerCruxes(cruxAnalysis) {
		if r2JSON != "" && hasRealSpeakerCruxes(&r2) {
			cruxAnalysis = &r2
		} else {
			cruxAnalysis = &r1
		}
	}

	// Collect all discarded cruxes and integrity warnings
	allDiscarded := r1.DiscardedCruxes
	var allWarnings []string
	for _, w := range r1.IntegrityWarnings {
		if !strings.HasPrefix(w, "DEGENERATE:") {
			allWarnings = append(allWarnings, w)
		}
	}
	if r2JSON != "" {
		allDiscarded = append(allDiscarded, r2.DiscardedCruxes...)
		for _, w := range r2.IntegrityWarnings {
			if !strings.HasPrefix(w, "DEGENERATE:") {
				allWarnings = append(allWarnings, w)
			}
		}
	}
	if r3JSON != "" {
		allDiscarded = append(allDiscarded, r3.DiscardedCruxes...)
		for _, w := range r3.IntegrityWarnings {
			if !strings.HasPrefix(w, "DEGENERATE:") {
				allWarnings = append(allWarnings, w)
			}
		}
	}

	var b strings.Builder

	// Count total cruxes across all rounds for context
	totalCruxAllRounds := len(r1.Cruxes) + len(r1.DiscardedCruxes)
	if r2JSON != "" {
		totalCruxAllRounds += len(r2.Cruxes) + len(r2.DiscardedCruxes)
	}
	if r3JSON != "" {
		totalCruxAllRounds += len(r3.Cruxes) + len(r3.DiscardedCruxes)
	}

	// Extract resolutions early (needed for TL;DR and Proposed Actions)
	var resolutions []agentPlan
	for _, a := range r3Agents {
		if a.Kind == "resolution" {
			resolutions = append(resolutions, a)
		}
	}

	nRounds := 1
	if r2JSON != "" {
		nRounds = 2
	}
	if r3JSON != "" {
		nRounds = 3
	}

	// ═══════════════════════════════════════════════════
	// 1. HEADER + TL;DR
	// ═══════════════════════════════════════════════════
	fmt.Fprintf(&b, "# %s: Deliberation Report\n\n", data.Title)
	b.WriteString("> AI-synthesized agents from [Talk to the City](https://talktothe.city) claims. Not human expert consensus -- verify against primary sources.\n\n")

	// TL;DR paragraph
	fmt.Fprintf(&b, "%d speakers were synthesized into %d deliberation agents across %d rounds. ", len(data.Sources), totalAgents, nRounds)
	if len(finalAnalysis.Cruxes) > 0 {
		topCrux := finalAnalysis.Cruxes[0]
		speakerAgree, _ := splitAgentTypes(topCrux.Agree)
		speakerDisagree, _ := splitAgentTypes(topCrux.Disagree)
		nFor := len(speakerAgree)
		nAgainst := len(speakerDisagree)
		if nFor > 0 && nAgainst > 0 {
			claim := strings.ToLower(strings.TrimRight(topCrux.Claim, "."))
			fmt.Fprintf(&b, "Strongest division: %d speakers for vs. %d against on whether %s. ",
				nFor, nAgainst, claim)
		}
	}
	if len(resolutions) > 0 {
		fmt.Fprintf(&b, "%d resolution proposals generated.", len(resolutions))
	}
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "*Deliberation `%s` -- %s template*\n\n", delibID, tmpl)

	// ═══════════════════════════════════════════════════
	// 2. PROPOSED ACTIONS — lead with outcomes
	// ═══════════════════════════════════════════════════
	if len(resolutions) > 0 {
		b.WriteString("## Proposed Actions\n\n")
		for i, res := range resolutions {
			title := strings.TrimPrefix(res.Role, "Resolution: ")
			fmt.Fprintf(&b, "### %d. %s\n\n", i+1, title)
			// Extract proposal body (skip RESOLUTION: header and REQUIRES: line)
			lines := strings.Split(res.Position, "\n")
			var proposal, requires []string
			inRequires := false
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "RESOLUTION:") || trimmed == "" {
					continue
				}
				if strings.HasPrefix(trimmed, "REQUIRES:") {
					inRequires = true
					requires = append(requires, strings.TrimPrefix(trimmed, "REQUIRES: "))
					continue
				}
				if inRequires {
					requires = append(requires, trimmed)
				} else {
					proposal = append(proposal, trimmed)
				}
			}
			if len(proposal) > 0 {
				for _, p := range proposal {
					fmt.Fprintf(&b, "%s\n", p)
				}
				b.WriteString("\n")
			}
			if len(requires) > 0 {
				fmt.Fprintf(&b, "*Requires*: %s\n\n", strings.Join(requires, " "))
			}
			// Find R3 cruxes where this resolution agent appears
			support, opposition := resolutionStances(res.ID, finalAnalysis.Cruxes)
			if len(support) > 0 {
				fmt.Fprintf(&b, "**Support**: %s\n", prettyAgentList(support))
			}
			if len(opposition) > 0 {
				fmt.Fprintf(&b, "**Opposition**: %s\n", prettyAgentList(opposition))
			}
			if len(support) > 0 || len(opposition) > 0 {
				b.WriteString("\n")
			}
		}
	} else if finalCompromise != "" {
		b.WriteString("## Proposed Compromise\n\n")
		fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(finalCompromise, "\n", "\n> "))
	}

	// ═══════════════════════════════════════════════════
	// 3. KEY DISAGREEMENTS — final round cruxes, speakers only
	// ═══════════════════════════════════════════════════
	// Count visible cruxes (those with at least one real speaker)
	visibleCruxes := 0
	for _, c := range cruxAnalysis.Cruxes {
		sa, _ := splitAgentTypes(c.Agree)
		sd, _ := splitAgentTypes(c.Disagree)
		hasReal := len(sa) > 0 || len(sd) > 0
		if !hasReal {
			for _, st := range c.Stances {
				if !isStructuralAgent(st.AgentID) {
					hasReal = true
					break
				}
			}
		}
		if hasReal {
			visibleCruxes++
		}
	}
	if visibleCruxes > 0 {
		fmt.Fprintf(&b, "## Key Disagreements\n\n")
		if cruxAnalysis == finalAnalysis {
			fmt.Fprintf(&b, "*%d cruxes from the final round (%d generated across all %d rounds).*\n\n", visibleCruxes, totalCruxAllRounds, nRounds)
		} else {
			fmt.Fprintf(&b, "*%d cruxes from an earlier round (final round lacked real-speaker data; %d generated across all %d rounds).*\n\n", visibleCruxes, totalCruxAllRounds, nRounds)
		}
		cruxNum := 0
		for _, c := range cruxAnalysis.Cruxes {
			speakerAgree, _ := splitAgentTypes(c.Agree)
			speakerDisagree, _ := splitAgentTypes(c.Disagree)
			nFor := len(speakerAgree)
			nAgainst := len(speakerDisagree)
			// Skip cruxes with no real speakers
			hasRealStance := nFor > 0 || nAgainst > 0
			if !hasRealStance {
				for _, st := range c.Stances {
					if !isStructuralAgent(st.AgentID) {
						hasRealStance = true
						break
					}
				}
			}
			if !hasRealStance {
				continue
			}
			cruxNum++
			fmt.Fprintf(&b, "**%d. (%d vs %d)** %s\n", cruxNum, nFor, nAgainst, c.Claim)
			if len(c.Stances) > 0 {
				// Show qualified stances
				for _, st := range c.Stances {
					if isStructuralAgent(st.AgentID) {
						continue
					}
					label := stanceLabel(st.Value)
					name := prettyAgentList([]string{st.AgentID})
					if st.Qualifier != "" {
						fmt.Fprintf(&b, "%s %s (%s)\n", label, name, st.Qualifier)
					} else {
						fmt.Fprintf(&b, "%s %s\n", label, name)
					}
				}
			} else if nFor > 0 || nAgainst > 0 {
				// Backwards compat: flat agree/disagree
				parts := []string{}
				if nFor > 0 {
					parts = append(parts, fmt.Sprintf("Agree: %s", prettyAgentList(speakerAgree)))
				}
				if nAgainst > 0 {
					parts = append(parts, fmt.Sprintf("Disagree: %s", prettyAgentList(speakerDisagree)))
				}
				b.WriteString(strings.Join(parts, " | "))
				b.WriteString("\n")
			}
			if c.Explanation != "" {
				fmt.Fprintf(&b, "> %s\n", c.Explanation)
			}
			b.WriteString("\n")
		}
	}

	// ═══════════════════════════════════════════════════
	// 4. COMMON GROUND — final round consensus
	// ═══════════════════════════════════════════════════
	cleaned := cleanConsensus(finalAnalysis.ConsensusStatements)
	b.WriteString("## Common Ground\n\n")
	if len(cleaned) > 0 {
		if scResult != nil && scResult.PassRate() < 0.5 {
			fmt.Fprintf(&b, "*Low input quality (%.0f%% spot-check) -- these consensus items may reflect vote-seeding patterns rather than genuine agreement across all participants.*\n\n", scResult.PassRate()*100)
		}
		for _, cs := range cleaned {
			fmt.Fprintf(&b, "- %s\n", cs)
		}
	} else {
		b.WriteString("*No consensus statements survived quality filtering. Positions remained divergent across all clusters — deliberation did not produce artificial convergence.*\n")
	}
	b.WriteString("\n")

	// ═══════════════════════════════════════════════════
	// 5. HOW POSITIONS EVOLVED — R1→R3 table + compromises
	// ═══════════════════════════════════════════════════
	if r3JSON != "" && len(r3Agents) > 0 {
		b.WriteString("## How Positions Evolved\n\n")
		for _, r3a := range r3Agents {
			if r3a.Kind == "resolution" {
				continue
			}
			originalID := strings.TrimSuffix(r3a.ID, "-r3")
			for _, r1a := range agents {
				if r1a.ID == originalID {
					name := strings.TrimPrefix(r1a.Role, "Steelman: ")
					name = strings.TrimPrefix(name, "Speaker: ")
					fmt.Fprintf(&b, "**%s**\n", name)
					fmt.Fprintf(&b, "- *Started*: %s\n", firstLine(r1a.Position))
					// Strip REVISED POSITION / name headers from R3 text
					r3Text := r3a.Position
					for range 5 { // strip up to 5 header lines
						idx := strings.Index(r3Text, "\n")
						if idx < 0 {
							break
						}
						first := strings.TrimSpace(r3Text[:idx])
						upper := strings.ToUpper(first)
						isHeader := first == "" ||
							strings.HasPrefix(upper, "REVISED POSITION") ||
							strings.HasPrefix(first, "**REVISED") ||
							strings.HasPrefix(first, "# ") ||
							strings.HasPrefix(first, "## ")
						if !isHeader {
							break
						}
						r3Text = strings.TrimSpace(r3Text[idx+1:])
					}
					fmt.Fprintf(&b, "- *Revised*: %s\n\n", firstLine(r3Text))
					break
				}
			}
		}

		// Show final compromise
		if r3Compromise != "" {
			b.WriteString("### Synthesis\n\n")
			b.WriteString("*LLM-generated synthesis from the final round -- treat as a starting point.*\n\n")
			fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(r3Compromise, "\n", "\n> "))
		}
	}

	// ═══════════════════════════════════════════════════
	// 6. PARTICIPANTS — compact
	// ═══════════════════════════════════════════════════
	b.WriteString("## Participants\n\n")
	if len(steelmen) > 0 {
		b.WriteString("**Clusters**: ")
		var clusterNames []string
		for _, a := range steelmen {
			clusterNames = append(clusterNames, strings.TrimPrefix(a.Role, "Steelman: "))
		}
		b.WriteString(strings.Join(clusterNames, " | "))
		b.WriteString("\n\n")
	}
	if len(speakers) > 0 {
		b.WriteString("**Individual**: ")
		var speakerNames []string
		for _, a := range speakers {
			speakerNames = append(speakerNames, strings.TrimPrefix(a.Role, "Speaker: "))
		}
		b.WriteString(strings.Join(speakerNames, ", "))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "*%d total agents across %d rounds (%d speaker-derived, %d structural, %d revised/resolution)*\n\n", totalAgents, nRounds, nSpeakerDerived, nStructural, nRevised)

	// ═══════════════════════════════════════════════════
	// 7. CONFIDENCE & CAVEATS
	// ═══════════════════════════════════════════════════
	b.WriteString("## Confidence & Caveats\n\n")

	// Reliability table
	degenerateRate := 0
	if totalCruxAllRounds > 0 {
		degenerateRate = (len(allDiscarded) * 100) / totalCruxAllRounds
	}

	b.WriteString("| Check | Status | Detail |\n")
	b.WriteString("|-------|--------|--------|\n")
	coherenceLabel := "pass"
	if degenerateRate > 40 {
		coherenceLabel = "fail"
	} else if degenerateRate > 20 {
		coherenceLabel = "partial"
	}
	fmt.Fprintf(&b, "| Crux coherence | %s | %d/%d survived (%d%% discard rate) |\n",
		coherenceLabel, totalCruxAllRounds-len(allDiscarded), totalCruxAllRounds, degenerateRate)

	hallucinationCount := countWarnings(allWarnings, "HALLUCINATION:")
	if hallucinationCount > 0 {
		fmt.Fprintf(&b, "| Agent hallucinations | minor | %d phantom agents removed |\n", hallucinationCount)
	} else {
		b.WriteString("| Agent hallucinations | none | -- |\n")
	}
	if ncResult != nil {
		if ncResult.Pass {
			b.WriteString("| Null control | pass | Distinguishable from noise |\n")
		} else {
			fmt.Fprintf(&b, "| Null control | fail | %d metrics indistinguishable |\n", len(ncResult.FailedMetrics))
		}
	} else {
		b.WriteString("| Null control | untested | -- |\n")
	}
	if repResult != nil && len(repResult.Runs) >= 2 {
		if repResult.Stability.AllStable {
			fmt.Fprintf(&b, "| Replication | pass | %d runs, all CV < 0.2 |\n", len(repResult.Runs))
		} else {
			fmt.Fprintf(&b, "| Replication | partial | %d runs, some metrics unstable |\n", len(repResult.Runs))
		}
	} else {
		b.WriteString("| Replication | untested | -- |\n")
	}
	if scResult != nil {
		scLabel := "pass"
		passRate := scResult.PassRate() * 100
		if passRate < 70 {
			scLabel = "fail"
		} else if passRate < 85 {
			scLabel = "partial"
		}
		fmt.Fprintf(&b, "| T3C input quality | %s | %d/%d spot-checks passed (%.0f%%) |\n", scLabel, scResult.Passed, scResult.Sampled, passRate)
	} else {
		b.WriteString("| T3C input quality | unchecked | -- |\n")
	}
	if cruxSCResult != nil {
		cscLabel := "pass"
		cscPassRate := cruxSCResult.PassRate() * 100
		if cscPassRate < 70 {
			cscLabel = "fail"
		} else if cscPassRate < 85 {
			cscLabel = "partial"
		}
		fmt.Fprintf(&b, "| Crux assignments | %s | %d/%d spot-checks passed (%.0f%%) |\n", cscLabel, cruxSCResult.Passed, cruxSCResult.Sampled, cscPassRate)
	} else {
		b.WriteString("| Crux assignments | unchecked | -- |\n")
	}
	b.WriteString("\n")

	if len(allWarnings) > 0 {
		for _, w := range allWarnings {
			if strings.HasPrefix(w, "SYBIL") || strings.HasPrefix(w, "ANALYSIS_REFUSED") {
				fmt.Fprintf(&b, "**Warning**: %s\n\n", w)
			}
		}
	}

	b.WriteString("**Key caveat**: AI-synthesized agents deliberating is inherently circular. This maps discourse structure -- it does not produce independent evidence. Verify conclusions against primary sources.\n\n")
	fmt.Fprintf(&b, "**Methodology**: Agents built from T3C claims+quotes. Clustered by Jaccard subtopic overlap (>=50%%) + shared claims (>=2). %d-round phased protocol", nRounds)
	if r3JSON != "" {
		b.WriteString(" with position revision and resolution proposals")
	}
	b.WriteString(". LLM outputs are stochastic -- replicate to confirm stability.\n\n")

	// ═══════════════════════════════════════════════════
	// 8. APPENDIX — diagnostic detail in collapsible sections
	// ═══════════════════════════════════════════════════
	b.WriteString("---\n\n")
	b.WriteString("## Appendix\n\n")

	// Per-round analysis
	b.WriteString("### Round 1: Initial Analysis\n\n")
	writeAnalysis(&b, &r1, r1Compromise, nSpeakerDerived, nStructural)

	if r2JSON != "" {
		b.WriteString("### Round 2: Emergent Findings\n\n")
		writeAnalysis(&b, &r2, ri.R2Compromise, nSpeakerDerived, nStructural+len(r2Agents))
	}

	if r3JSON != "" {
		b.WriteString("### Round 3: Revised Positions\n\n")
		writeAnalysis(&b, &r3, r3Compromise, nSpeakerDerived, nStructural+nRevised)
	}

	// Discarded cruxes
	if len(allDiscarded) > 0 {
		b.WriteString("### Discarded Cruxes\n\n")
		for _, c := range allDiscarded {
			emptySide := "agree"
			if len(c.Agree) > 0 {
				emptySide = "disagree"
			}
			failureMode := "agent pool gap"
			claim := strings.ToLower(c.Claim)
			if strings.Contains(claim, "inevitably") || strings.Contains(claim, "impossible") ||
				strings.Contains(claim, "will always") || strings.Contains(claim, "can never") {
				failureMode = "crux over-specified"
			}
			fmt.Fprintf(&b, "- **%s** (empty: %s side - likely: %s)\n", c.Claim, emptySide, failureMode)
		}
		b.WriteString("\n")
	}

	// Spot-check
	if scResult != nil && len(scResult.Failed) > 0 {
		b.WriteString("### Spot-Check: T3C Input Quality\n\n")
		b.WriteString("*Checks T3C's matrix stance assignments against source quotes. Agents are built from claims+quotes directly, not from matrix stances.*\n\n")
		for _, f := range scResult.Failed {
			fmt.Fprintf(&b, "- **%s** classified as *%s*: %s\n", f.Speaker, f.Stance, f.Crux)
			if f.Verdict != "" {
				verdict := f.Verdict
				if len(verdict) > 200 {
					verdict = verdict[:197] + "..."
				}
				fmt.Fprintf(&b, "  - Reason: %s\n", verdict)
			}
		}
		b.WriteString("\n")
	}

	// Cluster stability
	{
		mtc := multiThresholdClusters(data)
		if len(mtc) == 3 {
			b.WriteString("### Cluster Stability\n\n")
			b.WriteString("| Threshold | Total Clusters | Multi-member |\n")
			b.WriteString("|---|---|---|\n")
			for _, t := range mtc {
				fmt.Fprintf(&b, "| %.0f%% | %d | %d |\n", t.Threshold*100, t.NumClusters, t.NumMulti)
			}
			if mtc[0].NumMulti > 0 && mtc[0].NumMulti > mtc[1].NumMulti {
				dissolved := mtc[0].NumMulti - mtc[1].NumMulti
				fmt.Fprintf(&b, "\n*%d cluster(s) dissolve between 70%% and 80%%.*\n", dissolved)
			}
			b.WriteString("\n")
		}
	}

	// Null control
	if ncResult != nil {
		b.WriteString("### Null Control\n\n")
		b.WriteString("| Metric | Real Run | Null Control | Delta |\n")
		b.WriteString("|---|---|---|---|\n")
		realM := ncResult.RealMetrics
		nullM := ncResult.NullMetrics
		fmt.Fprintf(&b, "| Cruxes | %d | %d | %s |\n", realM.CruxCount, nullM.CruxCount, metricDelta(realM.CruxCount, nullM.CruxCount))
		fmt.Fprintf(&b, "| Avg controversy | %.2f | %.2f | %s |\n", realM.AvgControversy, nullM.AvgControversy, metricDeltaFloat(realM.AvgControversy, nullM.AvgControversy))
		fmt.Fprintf(&b, "| Consensus | %d | %d | %s |\n", realM.ConsensusCount, nullM.ConsensusCount, metricDelta(realM.ConsensusCount, nullM.ConsensusCount))
		fmt.Fprintf(&b, "| Bridging | %d | %d | %s |\n", realM.BridgingCount, nullM.BridgingCount, metricDelta(realM.BridgingCount, nullM.BridgingCount))
		fmt.Fprintf(&b, "| Clusters | %d | %d | %s |\n", realM.ClusterCount, nullM.ClusterCount, metricDelta(realM.ClusterCount, nullM.ClusterCount))
		if ncResult.Pass {
			b.WriteString("\n**Pass**: Real run distinguishable from noise.\n\n")
		} else {
			fmt.Fprintf(&b, "\n**Fail**: %d metrics indistinguishable from noise.\n\n", len(ncResult.FailedMetrics))
		}
	}

	// Replication
	if repResult != nil && len(repResult.Runs) >= 2 {
		b.WriteString("### Replication\n\n")
		b.WriteString("| Run | Cruxes | Avg Controversy | Consensus | Bridging | Confidence |\n")
		b.WriteString("|---|---|---|---|---|---|\n")
		for i, r := range repResult.Runs {
			fmt.Fprintf(&b, "| %d | %d | %.2f | %d | %d | %s |\n",
				i+1, r.CruxCount, r.AvgControversy, r.ConsensusCount, r.BridgingCount, r.Confidence)
		}
		s := repResult.Stability
		fmt.Fprintf(&b, "\n**Stability (CV):** crux %.2f, controversy %.2f, consensus %.2f\n\n", s.CruxCV, s.ControvCV, s.ConsensusCV)
	}

	// Coverage gaps
	if covResult != nil && len(covResult.Gaps) > 0 {
		b.WriteString("### Missing Perspectives\n\n")
		for _, gap := range covResult.Gaps {
			fmt.Fprintf(&b, "- **%s**\n", gap.Position)
			if gap.MissingPerspective != "" {
				fmt.Fprintf(&b, "  - Missing: %s\n", gap.MissingPerspective)
			}
			if gap.SuggestedSource != "" {
				fmt.Fprintf(&b, "  - Would contest: %s\n", gap.SuggestedSource)
			}
		}
		b.WriteString("\n")
	}

	// Next steps
	if joinCode != "" || delibID != "" {
		b.WriteString("---\n\n")
		fmt.Fprintf(&b, "*Continue: submit positions to extend beyond Round %d. ", nRounds)
		fmt.Fprintf(&b, "Replicate: run again to test stability. ")
		fmt.Fprintf(&b, "Deliberation: `%s`*\n", delibID)
	}

	report := insertTOC(b.String())
	if !ri.Named {
		report = anonymizeSpeakers(report, agents)
	}
	return report
}

// insertTOC scans the report for ## headings and inserts a table of contents
// after the metadata line (*Deliberation `...`*).
func insertTOC(report string) string {
	lines := strings.Split(report, "\n")

	// Collect ## headings (skip # title and ### sub-headings)
	type tocEntry struct {
		title string
		slug  string
	}
	var entries []tocEntry
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") && !strings.HasPrefix(line, "### ") {
			title := strings.TrimPrefix(line, "## ")
			// GitHub slug: lowercase, spaces→hyphens, strip non-alphanumeric except hyphens
			slug := strings.ToLower(title)
			slug = strings.ReplaceAll(slug, " ", "-")
			// GitHub slug rules: strip everything except alphanumeric, hyphens, spaces
			// then spaces→hyphens
			var slugBuf strings.Builder
			for _, r := range slug {
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == ' ' {
					slugBuf.WriteRune(r)
				}
			}
			slug = strings.ReplaceAll(slugBuf.String(), " ", "-")
			entries = append(entries, tocEntry{title: title, slug: slug})
		}
	}

	if len(entries) < 3 {
		return report // too few sections, skip TOC
	}

	// Build TOC
	var toc strings.Builder
	toc.WriteString("**Contents**: ")
	for i, e := range entries {
		if i > 0 {
			toc.WriteString(" | ")
		}
		fmt.Fprintf(&toc, "[%s](#%s)", e.title, e.slug)
	}
	toc.WriteString("\n\n")

	// Insert after the metadata line (*Deliberation `...`*)
	insertAfter := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "*Deliberation ") {
			insertAfter = i
			break
		}
	}
	if insertAfter < 0 {
		return toc.String() + report // fallback: prepend
	}

	var out strings.Builder
	for i, line := range lines {
		out.WriteString(line)
		out.WriteString("\n")
		if i == insertAfter {
			out.WriteString("\n")
			out.WriteString(toc.String())
		}
	}
	return out.String()
}

// anonymizeSpeakers replaces real speaker names with pseudonyms throughout the report.
// This is the default behavior — real names require explicit --named flag.
//
// Legal rationale: Angwin v. Superhuman Platform (S.D.N.Y. 2026) established that
// attributing AI-generated output to named real people without consent violates
// right-of-publicity statutes (CA Civil Code §3344, NY Civil Rights Law §§50-51).
// Even with disclaimers, publishing "+2 [Real Name]: [AI-generated stance]" creates
// perceived endorsement risk. Anonymization by default eliminates this exposure.
func anonymizeSpeakers(report string, agents []agentPlan) string {
	// Build name → pseudonym mapping from agent roles
	type nameEntry struct {
		full  string // "Speaker E"
		parts []string // ["Sam", "Speaker E"] — for last-name-only references
		label string // "Speaker C"
	}

	var entries []nameEntry
	seen := map[string]bool{}
	letterIdx := 0

	for _, a := range agents {
		if a.Kind != "speaker" && a.Kind != "steelman" {
			continue
		}
		// Extract display names from role
		role := strings.TrimPrefix(a.Role, "Steelman: ")
		role = strings.TrimPrefix(role, "Speaker: ")
		// Handle compound names like "Speaker J, Speaker I & Speaker K"
		// Split on both ", " and " & "
		role = strings.ReplaceAll(role, " & ", ", ")
		for _, name := range strings.Split(role, ", ") {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			label := fmt.Sprintf("Speaker %c", 'A'+rune(letterIdx))
			letterIdx++
			parts := strings.Fields(name)
			entries = append(entries, nameEntry{full: name, parts: parts, label: label})
		}
	}

	if len(entries) == 0 {
		return report
	}

	// Sort by name length descending to avoid partial replacements
	// ("Speaker A" before "Marc")
	sort.Slice(entries, func(i, j int) bool {
		return len(entries[i].full) > len(entries[j].full)
	})

	// Case-insensitive replacement helper
	replaceCI := func(s, old, replacement string) string {
		lower := strings.ToLower(s)
		oldLower := strings.ToLower(old)
		var result strings.Builder
		i := 0
		for {
			idx := strings.Index(lower[i:], oldLower)
			if idx < 0 {
				result.WriteString(s[i:])
				break
			}
			result.WriteString(s[i : i+idx])
			result.WriteString(replacement)
			i += idx + len(old)
		}
		return result.String()
	}

	// Replace full names first, then individual name parts
	for _, e := range entries {
		report = replaceCI(report, e.full, e.label)
	}
	// Replace individual name parts (for prose references like "Speaker F argues...")
	for _, e := range entries {
		for _, part := range e.parts {
			if len(part) >= 3 { // catch short names like "Speaker H"
				report = replaceCI(report, part, e.label)
			}
		}
	}

	// Add anonymization notice
	notice := "\n> **Speaker identities anonymized.** This report attributes AI-synthesized stances to %d pseudonymous speakers to prevent false attribution to real individuals. Use `--named` to generate a version with real names (requires accepting liability for potential misattribution). See *Angwin v. Superhuman Platform* (S.D.N.Y. 2026).\n\n"
	keyLines := fmt.Sprintf(notice, len(entries))

	// Insert after the provenance blockquote
	if idx := strings.Index(report, "\n\n*Deliberation "); idx > 0 {
		report = report[:idx] + "\n" + keyLines + report[idx:]
	}

	return report
}

// resolutionStances finds speaker agents that appear on the same or opposite side
// of cruxes as a resolution agent, indicating support or opposition.
func resolutionStances(resolutionID string, cruxes []struct {
	Claim       string   `json:"crux_claim"`
	Agree       []string `json:"agree_agents"`
	Disagree    []string `json:"disagree_agents"`
	Score       float64  `json:"controversy_score"`
	Explanation string   `json:"explanation"`
	Stances     []struct {
		AgentID   string `json:"agent_id"`
		Value     int    `json:"value"`
		Qualifier string `json:"qualifier"`
	} `json:"stances"`
}) (support, opposition []string) {
	supportSet := map[string]bool{}
	oppositionSet := map[string]bool{}
	for _, c := range cruxes {
		resOnAgree := false
		resOnDisagree := false
		for _, a := range c.Agree {
			if a == resolutionID {
				resOnAgree = true
			}
		}
		for _, a := range c.Disagree {
			if a == resolutionID {
				resOnDisagree = true
			}
		}
		if !resOnAgree && !resOnDisagree {
			continue
		}
		for _, a := range c.Agree {
			if a == resolutionID || isStructuralAgent(a) {
				continue
			}
			if resOnAgree {
				supportSet[a] = true
			} else {
				oppositionSet[a] = true
			}
		}
		for _, a := range c.Disagree {
			if a == resolutionID || isStructuralAgent(a) {
				continue
			}
			if resOnDisagree {
				supportSet[a] = true
			} else {
				oppositionSet[a] = true
			}
		}
	}
	// Resolve conflicts: agents appearing on both sides are ambiguous — remove them
	for a := range supportSet {
		if oppositionSet[a] {
			delete(supportSet, a)
			delete(oppositionSet, a)
		}
	}
	for a := range supportSet {
		support = append(support, a)
	}
	for a := range oppositionSet {
		opposition = append(opposition, a)
	}
	return
}

func countWarnings(warnings []string, prefix string) int {
	n := 0
	for _, w := range warnings {
		if strings.HasPrefix(w, prefix) {
			n++
		}
	}
	return n
}

func writeAnalysis(b *strings.Builder, r *analysisResult, compromise string, nSpeakers, nStructural int) {
	if len(r.Cruxes) > 0 {
		b.WriteString("### Cruxes\n\n")
		for _, c := range r.Cruxes {
			label := controversyLabel(c.Score, len(c.Agree), len(c.Disagree))
			fmt.Fprintf(b, "**[%s]** %s\n", label, c.Claim)

			if len(c.Stances) > 0 {
				// Show qualified stances, speakers first, then structural
				for _, st := range c.Stances {
					if isStructuralAgent(st.AgentID) {
						continue
					}
					lbl := stanceLabel(st.Value)
					name := prettyAgentList([]string{st.AgentID})
					if st.Qualifier != "" {
						fmt.Fprintf(b, "- %s %s (%s)\n", lbl, name, st.Qualifier)
					} else {
						fmt.Fprintf(b, "- %s %s\n", lbl, name)
					}
				}
				for _, st := range c.Stances {
					if !isStructuralAgent(st.AgentID) {
						continue
					}
					lbl := stanceLabel(st.Value)
					name := prettyAgentList([]string{st.AgentID})
					if st.Qualifier != "" {
						fmt.Fprintf(b, "- %s %s (%s)\n", lbl, name, st.Qualifier)
					} else {
						fmt.Fprintf(b, "- %s %s\n", lbl, name)
					}
				}
			} else {
				speakerAgree, structAgree := splitAgentTypes(c.Agree)
				speakerDisagree, structDisagree := splitAgentTypes(c.Disagree)

				// Speaker-only line
				if len(speakerAgree) > 0 || len(speakerDisagree) > 0 {
					b.WriteString("- *Speakers*: ")
					parts := []string{}
					if len(speakerAgree) > 0 {
						parts = append(parts, fmt.Sprintf("Agree: %s", prettyAgentList(speakerAgree)))
					}
					if len(speakerDisagree) > 0 {
						parts = append(parts, fmt.Sprintf("Disagree: %s", prettyAgentList(speakerDisagree)))
					}
					b.WriteString(strings.Join(parts, " | "))
					b.WriteString("\n")
				}
				// Structural line
				if len(structAgree) > 0 || len(structDisagree) > 0 {
					b.WriteString("- *Structural*: ")
					parts := []string{}
					if len(structAgree) > 0 {
						parts = append(parts, fmt.Sprintf("Agree: %s", prettyAgentList(structAgree)))
					}
					if len(structDisagree) > 0 {
						parts = append(parts, fmt.Sprintf("Disagree: %s", prettyAgentList(structDisagree)))
					}
					b.WriteString(strings.Join(parts, " | "))
					b.WriteString("\n")
				}
			}
			if c.Explanation != "" {
				fmt.Fprintf(b, "> %s\n", c.Explanation)
			}
			b.WriteString("\n")
		}
	}

	// Convergence points
	cleaned := cleanConsensus(r.ConsensusStatements)
	if len(cleaned) > 0 {
		b.WriteString("### Unchallenged Within This Agent Pool\n\n")
		b.WriteString("*Positions on which no synthetic agent registered disagreement. These reflect the topology of this specific agent pool -- not established truths or real-world expert consensus.*\n\n")
		for _, cs := range cleaned {
			fmt.Fprintf(b, "- %s\n", cs)
		}
		b.WriteString("\n")
	}

	// Compromise proposal
	if compromise != "" {
		b.WriteString("### Compromise Proposal\n\n")
		b.WriteString("*LLM-generated synthesis -- not grounded in specific agent positions. Treat as a starting point, not a conclusion.*\n\n")
		fmt.Fprintf(b, "> %s\n\n", strings.ReplaceAll(compromise, "\n", "\n> "))
	}

	// Topics
	if len(r.TopicSummaries) > 0 {
		b.WriteString("### Topics\n\n")
		for _, ts := range r.TopicSummaries {
			summary := ts.Summary
			if len(summary) > 500 {
				summary = summary[:497] + "..."
			}
			if ts.TopicID != "" {
				fmt.Fprintf(b, "**%s: %s**: %s\n\n", ts.TopicID, ts.Topic, summary)
			} else {
				fmt.Fprintf(b, "**%s**: %s\n\n", ts.Topic, summary)
			}
		}
	}

	// Reliability per-round
	fmt.Fprintf(b, "*Internal coherence: %s (self-assessed, not externally validated)*\n\n", r.Confidence)
}

func splitAgentTypes(agents []string) (speakers, structural []string) {
	for _, a := range agents {
		if isStructuralAgent(a) {
			structural = append(structural, a)
		} else {
			speakers = append(speakers, a)
		}
	}
	return
}

func cleanConsensus(statements []struct{ Content string }) []string {
	var cleaned []string
	for _, cs := range statements {
		lines := strings.Split(cs.Content, "\n")
		var substantive []string
		pastHeader := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if !pastHeader {
				if trimmed == "" {
					continue
				}
				upper := strings.ToUpper(trimmed)
				if strings.HasPrefix(upper, "SPEAKER:") ||
					strings.HasPrefix(upper, "STEELMAN") ||
					strings.HasPrefix(upper, "ADVERSARY") ||
					strings.HasPrefix(upper, "PROBE") ||
					strings.HasPrefix(upper, "BRIDGE") ||
					strings.HasPrefix(upper, "DISSENT") ||
					strings.HasPrefix(upper, "EMPTY CHAIR") ||
					strings.HasPrefix(upper, "REVISED POSITION") ||
					strings.HasPrefix(upper, "ROUND ") ||
					strings.HasPrefix(upper, "CRUX") ||
					strings.HasPrefix(upper, "KEY CLAIMS:") ||
					strings.HasPrefix(upper, "SOURCE QUOTES:") ||
					strings.HasPrefix(upper, "WHAT'S THE ") {
					continue
				}
				pastHeader = true
			}
			if trimmed == "" && len(substantive) == 0 {
				continue
			}
			if strings.HasPrefix(trimmed, "- ") {
				bullet := strings.TrimPrefix(trimmed, "- ")
				bulletUpper := strings.ToUpper(bullet)
				if strings.HasPrefix(bulletUpper, "AGREE:") ||
					strings.HasPrefix(bulletUpper, "DISAGREE:") ||
					strings.HasPrefix(bulletUpper, "SPEAKER:") ||
					strings.HasPrefix(bulletUpper, "STEELMAN") ||
					strings.HasPrefix(bulletUpper, "ROUND ") ||
					strings.HasPrefix(bullet, "What's the ") ||
					strings.HasPrefix(bullet, "Round 1 found") {
					continue
				}
				substantive = append(substantive, bullet)
			} else if trimmed != "" && !strings.HasPrefix(trimmed, "Stances:") &&
				!strings.HasPrefix(trimmed, "Task:") &&
				!strings.HasPrefix(trimmed, "Present the") &&
				!strings.HasPrefix(trimmed, "Probe deeper") &&
				!strings.HasPrefix(trimmed, "Play devil") &&
				!strings.HasPrefix(trimmed, "Build outward") &&
				!strings.HasPrefix(trimmed, "AGREE:") &&
				!strings.HasPrefix(trimmed, "DISAGREE:") &&
				!strings.HasPrefix(trimmed, "Round 1 found") &&
				!strings.HasPrefix(trimmed, "What's the strongest") {
				substantive = append(substantive, trimmed)
			}
		}
		for _, s := range substantive {
			if len(s) <= 10 {
				continue
			}
			// Skip questions (meta-commentary, not consensus)
			if s[len(s)-1] == '?' {
				continue
			}
			// Skip leaked markdown headers in bullet form
			if strings.HasPrefix(s, "## ") || strings.HasPrefix(s, "**") && strings.HasSuffix(s, ":**") {
				continue
			}
			// Skip leaked source quotes (start with " or contain "...)
			if strings.HasPrefix(s, "\"") || strings.HasPrefix(s, "'") {
				continue
			}
			// Skip lines with percentage brackets like "[80%]" (leaked crux scores)
			if len(s) > 3 && s[0] == '[' && strings.Contains(s[:min(6, len(s))], "%]") {
				continue
			}
			// Must end with sentence punctuation
			last := s[len(s)-1]
			if last != '.' && last != '!' && last != ')' && last != '"' {
				continue
			}
			cleaned = append(cleaned, s)
		}
	}

	// Deduplicate identical and near-identical items
	seen := map[string]bool{}
	var deduped []string
	for _, s := range cleaned {
		norm := strings.ToLower(strings.TrimRight(s, ".!?)\""))
		norm = strings.TrimSpace(norm)
		if seen[norm] {
			continue
		}
		seen[norm] = true
		deduped = append(deduped, s)
	}
	cleaned = deduped

	// Drop items that contradict each other (share topic words + opposing signals)
	opposites := [][2]string{
		{"misjudged", "futile"},
		{"inevitable", "preventable"},
		{"necessary", "unnecessary"},
		{"accelerate", "slow"},
		{"beneficial", "harmful"},
		{"overstated", "understated"},
		{"essential", "futile"},
		{"insufficient", "sufficient"},
	}
	drop := map[int]bool{}
	for i := 0; i < len(cleaned); i++ {
		for j := i + 1; j < len(cleaned); j++ {
			// Check shared content words (4+ chars)
			wordsI := strings.Fields(strings.ToLower(cleaned[i]))
			wordsJ := strings.Fields(strings.ToLower(cleaned[j]))
			common := 0
			for _, w := range wordsI {
				if len(w) < 4 {
					continue
				}
				for _, u := range wordsJ {
					if w == u {
						common++
						break
					}
				}
			}
			if common < 2 {
				continue
			}
			// Check opposing signals
			lowerI := strings.ToLower(cleaned[i])
			lowerJ := strings.ToLower(cleaned[j])
			for _, pair := range opposites {
				if (strings.Contains(lowerI, pair[0]) && strings.Contains(lowerJ, pair[1])) ||
					(strings.Contains(lowerI, pair[1]) && strings.Contains(lowerJ, pair[0])) {
					if len(cleaned[i]) < len(cleaned[j]) {
						drop[i] = true
					} else {
						drop[j] = true
					}
				}
			}
		}
	}
	if len(drop) > 0 {
		var filtered []string
		for i, s := range cleaned {
			if !drop[i] {
				filtered = append(filtered, s)
			}
		}
		cleaned = filtered
	}

	return cleaned
}

var acronyms = map[string]string{
	"ai": "AI", "agi": "AGI", "llm": "LLM", "api": "API",
}

func prettyAgentList(agents []string) string {
	if len(agents) == 0 {
		return "(none)"
	}
	names := make([]string, len(agents))
	for i, a := range agents {
		a = strings.TrimPrefix(a, "t3c-")
		if strings.HasPrefix(a, "empty-chair-") {
			num := strings.TrimPrefix(a, "empty-chair-")
			n := 0
			fmt.Sscanf(num, "%d", &n)
			names[i] = fmt.Sprintf("Empty Chair %d", n+1)
			continue
		}
		if strings.HasPrefix(a, "resolution-") {
			num := strings.TrimPrefix(a, "resolution-")
			names[i] = fmt.Sprintf("Resolution %s", num)
			continue
		}
		a = strings.TrimPrefix(a, "speaker-")
		a = strings.TrimPrefix(a, "steelman-")
		a = strings.TrimPrefix(a, "probe-")
		a = strings.TrimPrefix(a, "adversary-")
		a = strings.TrimPrefix(a, "resolution-")
		a = strings.ReplaceAll(a, "-", " ")
		words := strings.Fields(a)
		for j, w := range words {
			if upper, ok := acronyms[strings.ToLower(w)]; ok {
				words[j] = upper
			} else if len(w) > 0 {
				words[j] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
		names[i] = strings.Join(words, " ")
	}
	return strings.Join(names, ", ")
}

func firstLine(s string) string {
	// Skip header lines, markdown headers, and structural prefixes
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip markdown headers and bold-only headers like "**What held firm:**"
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "**") && strings.HasSuffix(trimmed, ":**") {
			continue
		}
		// Skip structural prefixes
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "SPEAKER:") || strings.HasPrefix(upper, "STEELMAN") ||
			strings.HasPrefix(upper, "REVISED POSITION") || strings.HasPrefix(upper, "PROBE") ||
			strings.HasPrefix(upper, "RESOLUTION:") {
			continue
		}
		// Skip "Stances:" and similar labels
		if strings.HasPrefix(trimmed, "Stances:") || strings.HasPrefix(trimmed, "REQUIRES:") {
			continue
		}
		if strings.HasPrefix(trimmed, "Key claims:") || strings.HasPrefix(trimmed, "Source quotes:") {
			continue
		}
		if len(trimmed) > 120 {
			return trimmed[:117] + "..."
		}
		return trimmed
	}
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}

func metricDelta(real, null int) string {
	if real == 0 {
		if null == 0 {
			return "--"
		}
		return fmt.Sprintf("%+d", null-real)
	}
	pct := float64(real-null) / float64(real) * 100
	return fmt.Sprintf("%+.0f%%", pct)
}

func metricDeltaFloat(real, null float64) string {
	if real == 0 {
		if null == 0 {
			return "--"
		}
		return fmt.Sprintf("%+.2f", real-null)
	}
	pct := (real - null) / real * 100
	return fmt.Sprintf("%+.0f%%", pct)
}

type cruxInfo struct {
	Claim     string
	Score     float64
	NAgree    int
	NDisagree int
}

func findNewCruxes(r1, r2 []struct {
	Claim       string   `json:"crux_claim"`
	Agree       []string `json:"agree_agents"`
	Disagree    []string `json:"disagree_agents"`
	Score       float64  `json:"controversy_score"`
	Explanation string   `json:"explanation"`
	Stances     []struct {
		AgentID   string `json:"agent_id"`
		Value     int    `json:"value"`
		Qualifier string `json:"qualifier"`
	} `json:"stances"`
}) []cruxInfo {
	r1Claims := map[string]bool{}
	for _, c := range r1 {
		r1Claims[c.Claim] = true
	}
	var newOnes []cruxInfo
	for _, c := range r2 {
		if !r1Claims[c.Claim] {
			newOnes = append(newOnes, cruxInfo{Claim: c.Claim, Score: c.Score, NAgree: len(c.Agree), NDisagree: len(c.Disagree)})
		}
	}
	return newOnes
}
