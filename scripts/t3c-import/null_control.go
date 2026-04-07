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

// buildR1Setup constructs R1 agents from claims and quotes (not from speakerCruxMatrix).
// Clustering is by subtopic overlap (Jaccard >= 0.5), not by matrix similarity.
// prefix controls agent ID prefix (e.g., "t3c-" for real, "t3c-null-" for null control).
func buildR1Setup(data *ReportData, threshold float64, prefix string) r1Setup {
	var s r1Setup

	// Classify cruxes (for probe agents — crux claims are well-grounded even if stances aren't)
	if data.AddOns != nil {
		for _, c := range data.AddOns.SubtopicCruxes {
			if c.ControversyScore >= threshold {
				s.controversialCruxes = append(s.controversialCruxes, c)
			} else if c.ControversyScore == 0 && len(c.Agree) >= 3 {
				s.consensusCruxes = append(s.consensusCruxes, c)
			}
		}
	}
	s.topicCruxes = map[string][]SubtopicCrux{}
	for _, c := range s.controversialCruxes {
		s.topicCruxes[c.Topic] = append(s.topicCruxes[c.Topic], c)
	}

	// Collect unique speakers from sources
	speakerNames := []string{}
	seen := map[string]bool{}
	for _, src := range data.Sources {
		name := parseSpeakerID(src.Interview)
		if !seen[name] {
			seen[name] = true
			speakerNames = append(speakerNames, name)
		}
	}

	// Compute subtopic sets for each speaker
	subtopicSets := map[string]map[string]bool{}
	for _, name := range speakerNames {
		subtopicSets[name] = speakerSubtopicSet(data, name)
	}

	// Cluster by subtopic overlap (Jaccard >= 0.5)
	assigned := make([]int, len(speakerNames))
	for i := range assigned {
		assigned[i] = -1
	}
	clusterID := 0
	for i := range speakerNames {
		if assigned[i] >= 0 {
			continue
		}
		assigned[i] = clusterID
		for j := i + 1; j < len(speakerNames); j++ {
			if assigned[j] >= 0 {
				continue
			}
			if jaccardSubtopics(subtopicSets[speakerNames[i]], subtopicSets[speakerNames[j]]) >= 0.5 {
				assigned[j] = clusterID
			}
		}
		clusterID++
	}

	// Build clusters
	clusterMap := map[int]*cluster{}
	for i, cid := range assigned {
		if _, ok := clusterMap[cid]; !ok {
			clusterMap[cid] = &cluster{ID: cid}
		}
		clusterMap[cid].Members = append(clusterMap[cid].Members, speakerNames[i])
	}
	for _, c := range clusterMap {
		s.clusters = append(s.clusters, *c)
	}
	sort.Slice(s.clusters, func(i, j int) bool {
		return len(s.clusters[i].Members) > len(s.clusters[j].Members)
	})

	// Build speaker/steelman agents from claims + quotes
	for i := range s.clusters {
		cl := &s.clusters[i]
		if len(cl.Members) == 1 {
			name := cl.Members[0]
			displayName := parseSpeakerName(name)
			s.agents = append(s.agents, agentPlan{
				ID: fmt.Sprintf("%sspeaker-%s", prefix, slugify(name)), Role: fmt.Sprintf("Speaker: %s", displayName),
				Position: buildClaimsPosition(data, []string{name}, displayName, false),
				Kind: "speaker", Round: 1, Cluster: cl,
			})
		} else {
			displayNames := make([]string, len(cl.Members))
			for j, m := range cl.Members {
				displayNames[j] = parseSpeakerName(m)
			}
			label := strings.Join(displayNames, ", ")
			s.agents = append(s.agents, agentPlan{
				ID: fmt.Sprintf("%ssteelman-%s", prefix, slugify(cl.Members[0])), Role: fmt.Sprintf("Steelman: %s", label),
				Position: buildClaimsPosition(data, cl.Members, label, true),
				Kind: "steelman", Round: 1, Cluster: cl,
			})
		}
	}

	// Probe agents (unchanged — built from T3C crux structure, not from matrix)
	for topicName, cruxes := range s.topicCruxes {
		var pos strings.Builder
		fmt.Fprintf(&pos, "PROBE for topic: %s\n\n", topicName)
		fmt.Fprintf(&pos, "Cruxes in this topic (%d):\n", len(cruxes))
		for _, c := range cruxes {
			fmt.Fprintf(&pos, "\n[%.0f%%] %s\n", c.ControversyScore*100, c.CruxClaim)
		}
		pos.WriteString("\nWhat's the underlying disagreement beneath all of these?")

		s.agents = append(s.agents, agentPlan{
			ID: fmt.Sprintf("%sprobe-%s", prefix, slugify(topicName)), Role: fmt.Sprintf("Probe: %s", topicName),
			Position: pos.String(), Kind: "probe", Round: 1, Topic: topicName,
		})
	}

	return s
}

// buildClaimsPosition constructs a position from a speaker's claims and quotes.
// No matrix stances — just what the person actually said.
func buildClaimsPosition(data *ReportData, speakers []string, label string, isSteelman bool) string {
	var pos strings.Builder
	if isSteelman {
		fmt.Fprintf(&pos, "STEELMAN for %s\n\n", label)
	} else {
		fmt.Fprintf(&pos, "SPEAKER: %s\n\n", label)
	}

	// Collect claims (use distinctiveClaims for steelmen, all for singletons)
	var claims []string
	if isSteelman {
		// For steelmen, prefer distinctive claims (not shared with all clusters)
		claims = distinctiveClaims(data, speakers, nil, 10)
	} else {
		claims = findAllClaimsForSpeaker(data, speakers[0])
		if len(claims) > 10 {
			claims = claims[:10]
		}
	}

	if len(claims) > 0 {
		pos.WriteString("Key claims:\n")
		for _, claim := range claims {
			fmt.Fprintf(&pos, "- %s\n", claim)
		}
	}

	// Add source quotes for grounding
	var allQuotes []string
	for _, name := range speakers {
		allQuotes = append(allQuotes, findAllQuotesForSpeaker(data, name)...)
	}
	if len(allQuotes) > 8 {
		allQuotes = allQuotes[:8]
	}
	if len(allQuotes) > 0 {
		pos.WriteString("\nSource quotes:\n")
		for _, q := range allQuotes {
			if len(q) > 150 {
				q = q[:147] + "..."
			}
			fmt.Fprintf(&pos, "- \"%s\"\n", q)
		}
	}

	if isSteelman {
		pos.WriteString("\nPresent the strongest, most defensible version of this group's collective position.")
	}

	return pos.String()
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

	voteCount := seedClaimVotes(session, shuffled, setup.agents, delibID)
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

// multiThresholdClusters returns cluster counts at different Jaccard thresholds.
func multiThresholdClusters(data *ReportData) []thresholdInfo {
	// Collect speakers
	var speakers []string
	seen := map[string]bool{}
	for _, src := range data.Sources {
		name := parseSpeakerID(src.Interview)
		if !seen[name] {
			seen[name] = true
			speakers = append(speakers, name)
		}
	}
	if len(speakers) == 0 {
		return nil
	}

	// Compute subtopic sets once
	sets := map[string]map[string]bool{}
	for _, name := range speakers {
		sets[name] = speakerSubtopicSet(data, name)
	}

	var info []thresholdInfo
	for _, t := range []float64{0.3, 0.5, 0.7} {
		assigned := make([]int, len(speakers))
		for i := range assigned {
			assigned[i] = -1
		}
		cid := 0
		for i := range speakers {
			if assigned[i] >= 0 {
				continue
			}
			assigned[i] = cid
			for j := i + 1; j < len(speakers); j++ {
				if assigned[j] >= 0 {
					continue
				}
				if jaccardSubtopics(sets[speakers[i]], sets[speakers[j]]) >= t {
					assigned[j] = cid
				}
			}
			cid++
		}
		multi := 0
		clusterSizes := map[int]int{}
		for _, c := range assigned {
			clusterSizes[c]++
		}
		for _, size := range clusterSizes {
			if size > 1 {
				multi++
			}
		}
		info = append(info, thresholdInfo{Threshold: t, NumClusters: len(clusterSizes), NumMulti: multi})
	}
	return info
}
