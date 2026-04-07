package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

type replicationResult struct {
	NumRuns   int               `json:"num_runs"`
	DelibIDs  []string          `json:"delib_ids,omitempty"`
	Runs      []pipelineMetrics `json:"runs,omitempty"`
	Stability stabilityReport   `json:"stability"`
}

type stabilityReport struct {
	Tier        int     `json:"tier"`
	CruxCV      float64 `json:"crux_cv"`
	ControvCV   float64 `json:"controv_cv"`
	ConsensusCV float64 `json:"consensus_cv"`
	AllStable   bool    `json:"all_stable"`
}

func computeStability(runs []pipelineMetrics) stabilityReport {
	n := len(runs)
	if n < 2 {
		return stabilityReport{Tier: 0}
	}

	cruxCounts := make([]float64, n)
	controvs := make([]float64, n)
	consensusCounts := make([]float64, n)

	for i, r := range runs {
		cruxCounts[i] = float64(r.CruxCount)
		controvs[i] = r.AvgControversy
		consensusCounts[i] = float64(r.ConsensusCount)
	}

	cruxCV := coefficientOfVariation(cruxCounts)
	controvCV := coefficientOfVariation(controvs)
	consensusCV := coefficientOfVariation(consensusCounts)

	allStable := cruxCV < 0.2 && controvCV < 0.2 && consensusCV < 0.2

	tier := 1
	if n >= 5 && allStable {
		tier = 2
	}

	return stabilityReport{
		Tier:        tier,
		CruxCV:      cruxCV,
		ControvCV:   controvCV,
		ConsensusCV: consensusCV,
		AllStable:   allStable,
	}
}

func coefficientOfVariation(vals []float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	mean := 0.0
	for _, v := range vals {
		mean += v
	}
	mean /= float64(len(vals))
	if mean == 0 {
		return 0
	}
	variance := 0.0
	for _, v := range vals {
		d := v - mean
		variance += d * d
	}
	variance /= float64(len(vals) - 1)
	return math.Sqrt(variance) / mean
}

// runReplication runs the R1 pipeline multiple times on the same data to test stability.
func runReplication(data *ReportData, mcpURL, secret, tmpl, groupID string, threshold float64, numRuns int) *replicationResult {
	fmt.Fprintf(os.Stderr, "\n=== Replication (%d runs) ===\n", numRuns)

	setup := buildR1Setup(data, threshold, "t3c-")
	if len(setup.agents) == 0 {
		fmt.Fprintf(os.Stderr, "  replication: no agents\n")
		return nil
	}

	repTmpl := tmpl
	if len(setup.agents) > 10 && (repTmpl == "negotiation" || repTmpl == "review") {
		repTmpl = "assembly"
	}

	result := &replicationResult{NumRuns: numRuns}

	for i := range numRuns {
		fmt.Fprintf(os.Stderr, "\n  --- Run %d/%d ---\n", i+1, numRuns)

		session, err := connect(mcpURL, secret)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  run %d: connect failed: %v\n", i+1, err)
			continue
		}

		topic := fmt.Sprintf("[T3C replicate %d/%d] %s", i+1, numRuns, data.Title)
		if len(topic) > 300 {
			topic = topic[:300]
		}
		createJSON := call(session, "deliberation", map[string]any{
			"action": "create", "topic": topic, "template": repTmpl,
			"type": "reasoning", "group_id": fmt.Sprintf("%s-rep-%d", groupID, i+1),
		})
		var created struct {
			DeliberationID string `json:"deliberation_id"`
		}
		json.Unmarshal([]byte(createJSON), &created)
		if created.DeliberationID == "" {
			fmt.Fprintf(os.Stderr, "  run %d: failed to create deliberation\n", i+1)
			session.Close()
			continue
		}
		delibID := created.DeliberationID

		// Submit same agents each run
		for _, a := range setup.agents {
			call(session, "participate", map[string]any{
				"action": "submit_position", "deliberation_id": delibID,
				"agent_id": a.ID, "content": a.Position,
			})
		}

		voteCount := seedClaimVotes(session, data, setup.agents, delibID)
		fmt.Fprintf(os.Stderr, "  %d agents, %d votes → %s\n", len(setup.agents), voteCount, delibID)

		fmt.Fprintf(os.Stderr, "  analyzing...\n")
		call(session, "analyze", map[string]any{"action": "run", "deliberation_id": delibID})

		runResult := pollAndGetResult(session, mcpURL, secret, delibID, 1)
		session.Close()

		if runResult == "" {
			fmt.Fprintf(os.Stderr, "  run %d: analysis did not complete\n", i+1)
			continue
		}

		metrics := extractMetrics(runResult, len(setup.clusters))
		result.Runs = append(result.Runs, metrics)
		result.DelibIDs = append(result.DelibIDs, delibID)

		fmt.Fprintf(os.Stderr, "  run %d: %d cruxes, %.2f avg controversy, %s confidence\n",
			i+1, metrics.CruxCount, metrics.AvgControversy, metrics.Confidence)
	}

	if len(result.Runs) < 2 {
		fmt.Fprintf(os.Stderr, "  replication: insufficient successful runs (%d)\n", len(result.Runs))
		return result
	}

	result.Stability = computeStability(result.Runs)

	fmt.Fprintf(os.Stderr, "\n  Stability: Tier %d\n", result.Stability.Tier)
	fmt.Fprintf(os.Stderr, "    Crux count CV: %.2f\n", result.Stability.CruxCV)
	fmt.Fprintf(os.Stderr, "    Controversy CV: %.2f\n", result.Stability.ControvCV)
	fmt.Fprintf(os.Stderr, "    Consensus CV: %.2f\n", result.Stability.ConsensusCV)
	if result.Stability.AllStable {
		fmt.Fprintf(os.Stderr, "    All metrics stable (CV < 0.2)\n")
	} else {
		fmt.Fprintf(os.Stderr, "    Some metrics unstable (CV >= 0.2)\n")
	}

	return result
}
