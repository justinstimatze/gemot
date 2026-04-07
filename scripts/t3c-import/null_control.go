package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"time"
)

// r1Setup holds the derived agents and supporting data for Round 1.
type r1Setup struct {
	agents              []agentPlan
	clusters            []cluster
	controversialCruxes []SubtopicCrux
	consensusCruxes     []SubtopicCrux
	topicCruxes         map[string][]SubtopicCrux
}

// buildR1Setup derives clusters, classifies cruxes, and constructs R1 agents.
// prefix controls agent ID prefix (e.g., "t3c-" for real, "t3c-null-" for null control).
func buildR1Setup(data *ReportData, threshold float64, prefix string) r1Setup {
	var s r1Setup

	if data.AddOns.SpeakerCruxMatrix != nil {
		s.clusters = deriveClusters(data.AddOns.SpeakerCruxMatrix)
	}

	for _, c := range data.AddOns.SubtopicCruxes {
		if c.ControversyScore >= threshold {
			s.controversialCruxes = append(s.controversialCruxes, c)
		} else if c.ControversyScore == 0 && len(c.Agree) >= 3 {
			s.consensusCruxes = append(s.consensusCruxes, c)
		}
	}

	s.topicCruxes = map[string][]SubtopicCrux{}
	for _, c := range s.controversialCruxes {
		s.topicCruxes[c.Topic] = append(s.topicCruxes[c.Topic], c)
	}

	// Cluster steelmen (2+ members) and speaker agents (singletons)
	for i := range s.clusters {
		cl := &s.clusters[i]
		if len(cl.Members) == 1 {
			name := parseSpeakerID(cl.Members[0])
			displayName := parseSpeakerName(cl.Members[0])
			claims := distinctiveClaims(data, []string{name}, s.clusters, 7)

			var pos strings.Builder
			fmt.Fprintf(&pos, "SPEAKER: %s\n\n", displayName)
			for _, claim := range claims {
				fmt.Fprintf(&pos, "- %s\n", claim)
			}
			if data.AddOns.SpeakerCruxMatrix != nil && len(cl.Pattern) > 0 {
				pos.WriteString("\nStances:\n")
				for j, stance := range cl.Pattern {
					if stance == "no_position" {
						continue
					}
					if j < len(data.AddOns.SpeakerCruxMatrix.CruxLabels) {
						label := data.AddOns.SpeakerCruxMatrix.CruxLabels[j]
						for _, c := range data.AddOns.SubtopicCruxes {
							if cruxLabel(c.Topic, c.Subtopic) == normLabel(label) {
								fmt.Fprintf(&pos, "- %s: %s\n", strings.ToUpper(stance), c.CruxClaim[:min(80, len(c.CruxClaim))])
								break
							}
						}
					}
				}
			}

			s.agents = append(s.agents, agentPlan{
				ID: fmt.Sprintf("%sspeaker-%s", prefix, slugify(name)), Role: fmt.Sprintf("Speaker: %s", displayName),
				Position: pos.String(), Kind: "speaker", Round: 1, Cluster: cl,
			})
		} else {
			names := make([]string, len(cl.Members))
			speakerNames := make([]string, len(cl.Members))
			for j, m := range cl.Members {
				names[j] = parseSpeakerName(m)
				speakerNames[j] = parseSpeakerID(m)
			}
			claims := distinctiveClaims(data, speakerNames, s.clusters, 7)

			var pos strings.Builder
			fmt.Fprintf(&pos, "STEELMAN for %s\n\n", strings.Join(names, ", "))
			for _, claim := range claims {
				fmt.Fprintf(&pos, "- %s\n", claim)
			}
			if data.AddOns.SpeakerCruxMatrix != nil && len(cl.Pattern) > 0 {
				pos.WriteString("\nStances:\n")
				for j, stance := range cl.Pattern {
					if stance == "no_position" {
						continue
					}
					if j < len(data.AddOns.SpeakerCruxMatrix.CruxLabels) {
						label := data.AddOns.SpeakerCruxMatrix.CruxLabels[j]
						for _, c := range data.AddOns.SubtopicCruxes {
							if cruxLabel(c.Topic, c.Subtopic) == normLabel(label) {
								fmt.Fprintf(&pos, "- %s: %s\n", strings.ToUpper(stance), c.CruxClaim[:min(80, len(c.CruxClaim))])
								break
							}
						}
					}
				}
			}
			pos.WriteString("\nPresent the strongest, most defensible version of this group's collective position.")

			s.agents = append(s.agents, agentPlan{
				ID: fmt.Sprintf("%ssteelman-%s", prefix, slugify(names[0])), Role: fmt.Sprintf("Steelman: %s", strings.Join(names, ", ")),
				Position: pos.String(), Kind: "steelman", Round: 1, Cluster: cl,
			})
		}
	}

	// Topic adversary agents (one per T3C topic, not one per crux)
	for topicName, cruxes := range s.topicCruxes {
		var pos strings.Builder
		fmt.Fprintf(&pos, "PROBE for topic: %s\n\n", topicName)
		fmt.Fprintf(&pos, "Cruxes in this topic (%d):\n", len(cruxes))
		for _, c := range cruxes {
			agreeNames := make([]string, len(c.Agree))
			for j, a := range c.Agree {
				agreeNames[j] = parseSpeakerName(a)
			}
			disagreeNames := make([]string, len(c.Disagree))
			for j, d := range c.Disagree {
				disagreeNames[j] = parseSpeakerName(d)
			}
			fmt.Fprintf(&pos, "\n[%.0f%%] %s\n", c.ControversyScore*100, c.CruxClaim)
			fmt.Fprintf(&pos, "  Agree: %s | Disagree: %s\n", strings.Join(agreeNames, ", "), strings.Join(disagreeNames, ", "))
		}
		pos.WriteString("\nWhat's the underlying disagreement beneath all of these?")

		s.agents = append(s.agents, agentPlan{
			ID: fmt.Sprintf("%sprobe-%s", prefix, slugify(topicName)), Role: fmt.Sprintf("Probe: %s", topicName),
			Position: pos.String(), Kind: "probe", Round: 1, Topic: topicName,
		})
	}

	return s
}

// shuffleReport creates a copy of the report data with randomized speaker-crux assignments.
// Preserves the same claims and marginal distributions but destroys real agreement patterns.
func shuffleReport(data *ReportData) *ReportData {
	if data.AddOns == nil || data.AddOns.SpeakerCruxMatrix == nil {
		return data
	}

	matrix := data.AddOns.SpeakerCruxMatrix
	n := len(matrix.Speakers)
	nCruxes := len(matrix.CruxLabels)
	if n == 0 || nCruxes == 0 {
		return data
	}

	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0))

	// Deep copy the matrix, then shuffle each crux column independently.
	// This preserves per-crux marginals (same number of agree/disagree)
	// while destroying speaker-crux correlations.
	newMatrix := make([][]string, n)
	for i := range n {
		newMatrix[i] = make([]string, nCruxes)
		if i < len(matrix.Matrix) {
			copy(newMatrix[i], matrix.Matrix[i])
		} else {
			for j := range nCruxes {
				newMatrix[i][j] = "no_position"
			}
		}
	}
	for col := range nCruxes {
		vals := make([]string, n)
		for row := range n {
			vals[row] = newMatrix[row][col]
		}
		rng.Shuffle(len(vals), func(i, j int) {
			vals[i], vals[j] = vals[j], vals[i]
		})
		for row := range n {
			newMatrix[row][col] = vals[row]
		}
	}

	// Rebuild SubtopicCruxes agree/disagree lists from shuffled matrix
	newCruxes := make([]SubtopicCrux, len(data.AddOns.SubtopicCruxes))
	for i, crux := range data.AddOns.SubtopicCruxes {
		newCruxes[i] = crux
		label := cruxLabel(crux.Topic, crux.Subtopic)
		cruxIdx := -1
		for j, l := range matrix.CruxLabels {
			if normLabel(l) == label {
				cruxIdx = j
				break
			}
		}
		if cruxIdx < 0 {
			continue
		}
		var agree, disagree, noPos []string
		for row, speaker := range matrix.Speakers {
			if row < n && cruxIdx < len(newMatrix[row]) {
				switch newMatrix[row][cruxIdx] {
				case "agree":
					agree = append(agree, speaker)
				case "disagree":
					disagree = append(disagree, speaker)
				default:
					noPos = append(noPos, speaker)
				}
			}
		}
		newCruxes[i].Agree = agree
		newCruxes[i].Disagree = disagree
		newCruxes[i].NoPosition = noPos
	}

	shuffled := *data
	newAddOns := *data.AddOns
	newAddOns.SubtopicCruxes = newCruxes
	newAddOns.SpeakerCruxMatrix = &SpeakerCruxMatrix{
		Speakers:   matrix.Speakers,
		CruxLabels: matrix.CruxLabels,
		Matrix:     newMatrix,
	}
	shuffled.AddOns = &newAddOns
	return &shuffled
}

// nullControlResult holds comparison metrics between real and null control runs.
type nullControlResult struct {
	NullDelibID   string          `json:"null_delib_id"`
	RealMetrics   pipelineMetrics `json:"real_metrics"`
	NullMetrics   pipelineMetrics `json:"null_metrics"`
	FailedMetrics []string        `json:"failed_metrics,omitempty"`
	Pass          bool            `json:"pass"`
}

type pipelineMetrics struct {
	CruxCount      int     `json:"crux_count"`
	AvgControversy float64 `json:"avg_controversy"`
	ConsensusCount int     `json:"consensus_count"`
	BridgingCount  int     `json:"bridging_count"`
	Confidence     string  `json:"confidence"`
	ClusterCount   int     `json:"cluster_count"`
}

func extractMetrics(resultJSON string, clusterCount int) pipelineMetrics {
	var ar analysisResult
	json.Unmarshal([]byte(resultJSON), &ar)

	m := pipelineMetrics{
		CruxCount:      len(ar.Cruxes),
		ConsensusCount: len(ar.ConsensusStatements),
		BridgingCount:  len(ar.BridgingStatements),
		Confidence:     ar.Confidence,
		ClusterCount:   clusterCount,
	}
	if len(ar.Cruxes) > 0 {
		total := 0.0
		for _, c := range ar.Cruxes {
			total += c.Score
		}
		m.AvgControversy = total / float64(len(ar.Cruxes))
	}
	return m
}

func compareMetrics(real, null pipelineMetrics) (bool, []string) {
	const threshold = 0.15
	var failed []string

	if real.CruxCount > 0 {
		delta := absFloat(float64(real.CruxCount-null.CruxCount)) / float64(real.CruxCount)
		if delta <= threshold {
			failed = append(failed, fmt.Sprintf("crux count (real: %d, null: %d, delta: %.0f%%)", real.CruxCount, null.CruxCount, delta*100))
		}
	}
	if real.AvgControversy > 0 {
		delta := absFloat(real.AvgControversy-null.AvgControversy) / real.AvgControversy
		if delta <= threshold {
			failed = append(failed, fmt.Sprintf("avg controversy (real: %.2f, null: %.2f, delta: %.0f%%)", real.AvgControversy, null.AvgControversy, delta*100))
		}
	}
	if real.ConsensusCount > 0 {
		delta := absFloat(float64(real.ConsensusCount-null.ConsensusCount)) / float64(real.ConsensusCount)
		if delta <= threshold {
			failed = append(failed, fmt.Sprintf("consensus count (real: %d, null: %d, delta: %.0f%%)", real.ConsensusCount, null.ConsensusCount, delta*100))
		}
	}
	if real.BridgingCount > 0 {
		delta := absFloat(float64(real.BridgingCount-null.BridgingCount)) / float64(real.BridgingCount)
		if delta <= threshold {
			failed = append(failed, fmt.Sprintf("bridging count (real: %d, null: %d, delta: %.0f%%)", real.BridgingCount, null.BridgingCount, delta*100))
		}
	}

	// Pass if fewer than half of tested metrics are indistinguishable
	pass := len(failed) < 2
	return pass, failed
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// runNullControl runs a simplified R1-only pipeline on shuffled data and compares with real results.
func runNullControl(data *ReportData, realR1JSON string, realClusterCount int, mcpURL, secret, tmpl, groupID string, threshold float64) *nullControlResult {
	fmt.Fprintf(os.Stderr, "\n=== Null Control ===\n")
	fmt.Fprintf(os.Stderr, "  shuffling speaker-crux assignments...\n")

	shuffled := shuffleReport(data)
	setup := buildR1Setup(shuffled, threshold, "t3c-null-")

	if len(setup.agents) == 0 {
		fmt.Fprintf(os.Stderr, "  null control: no agents from shuffled data\n")
		return nil
	}

	nullTmpl := tmpl
	if len(setup.agents) > 10 && (nullTmpl == "negotiation" || nullTmpl == "review") {
		nullTmpl = "assembly"
	}

	fmt.Fprintf(os.Stderr, "  %d agents, %d clusters (shuffled)\n", len(setup.agents), len(setup.clusters))

	session, err := connect(mcpURL, secret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  null control: connect failed: %v\n", err)
		return nil
	}
	defer session.Close()

	topic := fmt.Sprintf("[T3C null control · shuffled] %s", data.Title)
	if len(topic) > 300 {
		topic = topic[:300]
	}
	createJSON := call(session, "deliberation", map[string]any{
		"action": "create", "topic": topic, "template": nullTmpl,
		"type": "reasoning", "group_id": groupID + "-null",
	})
	var created struct {
		DeliberationID string `json:"deliberation_id"`
	}
	json.Unmarshal([]byte(createJSON), &created)
	if created.DeliberationID == "" {
		fmt.Fprintf(os.Stderr, "  null control: failed to create deliberation\n")
		return nil
	}
	delibID := created.DeliberationID
	fmt.Fprintf(os.Stderr, "  deliberation: %s\n", delibID)

	for _, a := range setup.agents {
		call(session, "participate", map[string]any{
			"action": "submit_position", "deliberation_id": delibID,
			"agent_id": a.ID, "content": a.Position,
		})
	}

	voteCount := seedStructuralVotes(session, shuffled, setup.agents, setup.clusters, setup.topicCruxes, delibID)
	fmt.Fprintf(os.Stderr, "  %d votes seeded\n", voteCount)

	fmt.Fprintf(os.Stderr, "  analyzing...\n")
	call(session, "analyze", map[string]any{"action": "run", "deliberation_id": delibID})

	nullResult := pollAndGetResult(session, mcpURL, secret, delibID, 1)
	if nullResult == "" {
		fmt.Fprintf(os.Stderr, "  null control: analysis did not complete\n")
		return nil
	}
	fmt.Fprintf(os.Stderr, "  null control complete\n")

	realMetrics := extractMetrics(realR1JSON, realClusterCount)
	nullMetrics := extractMetrics(nullResult, len(setup.clusters))
	pass, failed := compareMetrics(realMetrics, nullMetrics)

	result := &nullControlResult{
		NullDelibID:   delibID,
		RealMetrics:   realMetrics,
		NullMetrics:   nullMetrics,
		FailedMetrics: failed,
		Pass:          pass,
	}

	if pass {
		fmt.Fprintf(os.Stderr, "  PASS: real run distinguishable from null control\n")
	} else {
		fmt.Fprintf(os.Stderr, "  WARN: %d metrics indistinguishable from null control\n", len(failed))
		for _, f := range failed {
			fmt.Fprintf(os.Stderr, "    - %s\n", f)
		}
	}

	return result
}

// --- Multi-threshold cluster analysis ---

type thresholdInfo struct {
	Threshold   float64
	NumClusters int
	NumMulti    int // clusters with 2+ members
}

// deriveClustersAt runs cluster derivation at a specific similarity threshold.
func deriveClustersAt(matrix *SpeakerCruxMatrix, threshold float64) []cluster {
	n := len(matrix.Speakers)
	if n == 0 {
		return nil
	}

	patterns := make([][]string, n)
	for i := range n {
		if i < len(matrix.Matrix) {
			patterns[i] = matrix.Matrix[i]
		}
	}

	assigned := make([]int, n)
	for i := range assigned {
		assigned[i] = -1
	}

	clusterID := 0
	for i := range n {
		if assigned[i] >= 0 {
			continue
		}
		assigned[i] = clusterID
		for j := i + 1; j < n; j++ {
			if assigned[j] >= 0 {
				continue
			}
			if patternSimilarity(patterns[i], patterns[j]) >= threshold {
				assigned[j] = clusterID
			}
		}
		clusterID++
	}

	clusterMap := map[int]*cluster{}
	for i, cid := range assigned {
		if _, ok := clusterMap[cid]; !ok {
			clusterMap[cid] = &cluster{ID: cid}
		}
		clusterMap[cid].Members = append(clusterMap[cid].Members, matrix.Speakers[i])
	}

	var clusters []cluster
	for _, c := range clusterMap {
		clusters = append(clusters, *c)
	}
	sort.Slice(clusters, func(i, j int) bool {
		return len(clusters[i].Members) > len(clusters[j].Members)
	})
	return clusters
}

// multiThresholdClusters returns cluster counts at 70%, 80%, 90% thresholds.
func multiThresholdClusters(matrix *SpeakerCruxMatrix) []thresholdInfo {
	if matrix == nil {
		return nil
	}
	var info []thresholdInfo
	for _, t := range []float64{0.7, 0.8, 0.9} {
		clusters := deriveClustersAt(matrix, t)
		multi := 0
		for _, c := range clusters {
			if len(c.Members) > 1 {
				multi++
			}
		}
		info = append(info, thresholdInfo{Threshold: t, NumClusters: len(clusters), NumMulti: multi})
	}
	return info
}
