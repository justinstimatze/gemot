package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// synthesizeBriefing merges intelligence from all scopes into one briefing per power.
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

// --- Commitments Integration (2d) ---

type commitment struct {
	AgentID     string `json:"agent_id"`
	Statement   string `json:"statement"`
	Conditional string `json:"conditional"`
	Status      string `json:"status"`
}
