package analysis

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"gonum.org/v1/gonum/mat"
	"gonum.org/v1/gonum/stat"
)

// VoteAnalyzer performs Polis-inspired vote matrix analysis:
// PCA dimensionality reduction, K-means clustering, repness, consensus.
type VoteAnalyzer struct {
	MinAgents    int     // minimum agents for activation (default 10)
	MinPositions int     // minimum positions (default 3)
	MinCoverage  float64 // minimum vote coverage ratio (default 0.5)
}

func NewVoteAnalyzer() *VoteAnalyzer {
	return &VoteAnalyzer{
		MinAgents:    10,
		MinPositions: 3,
		MinCoverage:  0.5,
	}
}

// VoteResult contains the output of vote matrix analysis.
type VoteResult struct {
	Clusters  []deliberation.OpinionCluster
	Consensus []deliberation.ConsensusStatement
	Repness   map[int][]RepPosition // cluster ID -> representative positions
	PCACoords map[string][2]float64 // agent ID -> (x, y) in PCA space
}

// RepPosition is a position that is representative of a cluster.
type RepPosition struct {
	PositionID string
	Content    string
	Score      float64 // how representative (higher = more representative)
}

// Analyze runs the full vote matrix pipeline. Returns nil if insufficient data.
// ctx is accepted for interface consistency but not used (no I/O).
func (v *VoteAnalyzer) Analyze(_ context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) *VoteResult {
	if len(agents) < v.MinAgents || len(positions) < v.MinPositions {
		return nil
	}

	// Build vote matrix: rows=agents, cols=positions, values in {-1, 0, 1}, NaN=missing
	agentIdx := map[string]int{}
	for i, a := range agents {
		agentIdx[a] = i
	}
	posIdx := map[string]int{}
	posIDs := make([]string, len(positions))
	for i, p := range positions {
		posIdx[p.ID] = i
		posIDs[i] = p.ID
	}

	nAgents := len(agents)
	nPositions := len(positions)

	// Initialize with NaN (missing votes)
	raw := make([]float64, nAgents*nPositions)
	for i := range raw {
		raw[i] = math.NaN()
	}

	for _, vote := range votes {
		ai, aOK := agentIdx[vote.AgentID]
		pi, pOK := posIdx[vote.PositionID]
		if aOK && pOK {
			raw[ai*nPositions+pi] = float64(vote.Value)
		}
	}

	// Check coverage
	filled := 0
	for _, v := range raw {
		if !math.IsNaN(v) {
			filled++
		}
	}
	coverage := float64(filled) / float64(nAgents*nPositions)
	if coverage < v.MinCoverage {
		return nil
	}

	// Impute missing votes with 0 (pass) for matrix operations
	imputed := make([]float64, len(raw))
	copy(imputed, raw)
	for i, v := range imputed {
		if math.IsNaN(v) {
			imputed[i] = 0
		}
	}

	matrix := mat.NewDense(nAgents, nPositions, imputed)

	// PCA: reduce to 2D for clustering and visualization
	pcaCoords := runPCA(matrix, nAgents, nPositions)

	// K-means clustering on PCA coordinates (silhouette-optimal k)
	k := choosek(pcaCoords, nAgents)
	labels := kmeans(pcaCoords, k, nAgents)

	// Build clusters
	clusterAgents := map[int][]string{}
	for i, label := range labels {
		clusterAgents[label] = append(clusterAgents[label], agents[i])
	}

	var clusters []deliberation.OpinionCluster
	for id, agentList := range clusterAgents {
		clusters = append(clusters, deliberation.OpinionCluster{
			ID:       id,
			AgentIDs: agentList,
			Size:     len(agentList),
		})
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].ID < clusters[j].ID })

	// Repness: find representative positions for each cluster
	repness := computeRepness(matrix, labels, agents, positions, nAgents, nPositions)

	// Consensus: positions with high agreement across all clusters
	consensus := computeConsensus(matrix, labels, positions, nAgents, nPositions)

	// Build PCA coordinate map
	coordMap := map[string][2]float64{}
	for i, agent := range agents {
		coordMap[agent] = [2]float64{pcaCoords[i*2], pcaCoords[i*2+1]}
	}

	return &VoteResult{
		Clusters:  clusters,
		Consensus: consensus,
		Repness:   repness,
		PCACoords: coordMap,
	}
}

// runPCA projects the vote matrix to 2D using SVD-based PCA.
func runPCA(matrix *mat.Dense, nAgents, nPositions int) []float64 {
	// Center columns (subtract column means)
	centered := mat.NewDense(nAgents, nPositions, nil)
	centered.Copy(matrix)

	for j := 0; j < nPositions; j++ {
		col := mat.Col(nil, j, centered)
		mean := stat.Mean(col, nil)
		for i := 0; i < nAgents; i++ {
			centered.Set(i, j, centered.At(i, j)-mean)
		}
	}

	// SVD
	var svd mat.SVD
	if !svd.Factorize(centered, mat.SVDThin) {
		// Fallback: return zeros
		return make([]float64, nAgents*2)
	}

	var u mat.Dense
	svd.UTo(&u)

	// Take first 2 columns of U * S for 2D projection
	vals := svd.Values(nil)
	coords := make([]float64, nAgents*2)
	for i := 0; i < nAgents; i++ {
		if len(vals) > 0 {
			coords[i*2] = u.At(i, 0) * vals[0]
		}
		if len(vals) > 1 {
			coords[i*2+1] = u.At(i, 1) * vals[1]
		}
	}
	return coords
}

// choosek picks the optimal number of clusters using silhouette score.
// Tries k=2..maxK, runs K-means for each, picks k with highest mean silhouette.
func choosek(coords []float64, n int) int {
	maxK := int(math.Round(math.Sqrt(float64(n) / 2)))
	if maxK < 2 {
		maxK = 2
	}
	if maxK > 8 {
		maxK = 8
	}

	bestK := 2
	bestScore := -1.0

	for k := 2; k <= maxK; k++ {
		labels := kmeans(coords, k, n)
		score := silhouetteScore(coords, labels, n)
		if score > bestScore {
			bestScore = score
			bestK = k
		}
	}
	return bestK
}

// silhouetteScore computes the mean silhouette coefficient for a clustering.
// For each point: s(i) = (b(i) - a(i)) / max(a(i), b(i))
// a(i) = mean distance to points in same cluster
// b(i) = min over other clusters of mean distance to that cluster's points
// Returns mean s(i) in [-1, 1]. Higher is better.
func silhouetteScore(coords []float64, labels []int, n int) float64 {
	if n <= 2 {
		return 0
	}

	// Collect unique cluster IDs
	clusterIDs := uniqueInts(labels)
	if len(clusterIDs) <= 1 {
		return 0
	}

	totalScore := 0.0
	counted := 0

	for i := 0; i < n; i++ {
		myCluster := labels[i]

		// a(i): mean distance to same-cluster points
		aSum, aCount := 0.0, 0
		for j := 0; j < n; j++ {
			if j != i && labels[j] == myCluster {
				aSum += dist2D(coords, i, j)
				aCount++
			}
		}
		if aCount == 0 {
			continue // singleton cluster
		}
		ai := aSum / float64(aCount)

		// b(i): min mean distance to other clusters
		bi := math.Inf(1)
		for _, cid := range clusterIDs {
			if cid == myCluster {
				continue
			}
			bSum, bCount := 0.0, 0
			for j := 0; j < n; j++ {
				if labels[j] == cid {
					bSum += dist2D(coords, i, j)
					bCount++
				}
			}
			if bCount > 0 {
				meanDist := bSum / float64(bCount)
				if meanDist < bi {
					bi = meanDist
				}
			}
		}

		denom := math.Max(ai, bi)
		if denom > 0 {
			totalScore += (bi - ai) / denom
			counted++
		}
	}

	if counted == 0 {
		return 0
	}
	return totalScore / float64(counted)
}

func dist2D(coords []float64, i, j int) float64 {
	dx := coords[i*2] - coords[j*2]
	dy := coords[i*2+1] - coords[j*2+1]
	return math.Sqrt(dx*dx + dy*dy)
}

// kmeans runs K-means clustering on 2D PCA coordinates using K-means++ initialization.
func kmeans(coords []float64, k, n int) []int {
	if n <= k {
		labels := make([]int, n)
		for i := range labels {
			labels[i] = i
		}
		return labels
	}

	// K-means++ initialization: pick first centroid randomly, then each subsequent
	// centroid with probability proportional to squared distance from nearest existing centroid.
	centroids := make([]float64, k*2)
	// First centroid: pick the point with the largest norm (deterministic, avoids rand dependency)
	bestIdx := 0
	bestNorm := 0.0
	for i := 0; i < n; i++ {
		norm := coords[i*2]*coords[i*2] + coords[i*2+1]*coords[i*2+1]
		if norm > bestNorm {
			bestNorm = norm
			bestIdx = i
		}
	}
	centroids[0] = coords[bestIdx*2]
	centroids[1] = coords[bestIdx*2+1]

	// Subsequent centroids: pick the point with max min-distance to existing centroids
	// (deterministic K-means++ variant: greedy farthest-first traversal)
	for c := 1; c < k; c++ {
		bestDist := -1.0
		bestPt := 0
		for i := 0; i < n; i++ {
			minDist := math.Inf(1)
			for j := 0; j < c; j++ {
				dx := coords[i*2] - centroids[j*2]
				dy := coords[i*2+1] - centroids[j*2+1]
				d := dx*dx + dy*dy
				if d < minDist {
					minDist = d
				}
			}
			if minDist > bestDist {
				bestDist = minDist
				bestPt = i
			}
		}
		centroids[c*2] = coords[bestPt*2]
		centroids[c*2+1] = coords[bestPt*2+1]
	}

	labels := make([]int, n)
	for iter := 0; iter < 100; iter++ {
		// Assign each point to nearest centroid
		changed := false
		for i := 0; i < n; i++ {
			x, y := coords[i*2], coords[i*2+1]
			bestDist := math.Inf(1)
			bestK := 0
			for j := 0; j < k; j++ {
				dx := x - centroids[j*2]
				dy := y - centroids[j*2+1]
				d := dx*dx + dy*dy
				if d < bestDist {
					bestDist = d
					bestK = j
				}
			}
			if labels[i] != bestK {
				labels[i] = bestK
				changed = true
			}
		}
		if !changed {
			break
		}

		// Recompute centroids
		for j := 0; j < k; j++ {
			sumX, sumY := 0.0, 0.0
			count := 0
			for i := 0; i < n; i++ {
				if labels[i] == j {
					sumX += coords[i*2]
					sumY += coords[i*2+1]
					count++
				}
			}
			if count > 0 {
				centroids[j*2] = sumX / float64(count)
				centroids[j*2+1] = sumY / float64(count)
			}
		}
	}

	// Compact: remove empty clusters
	return compactLabels(labels)
}

// compactLabels renumbers labels to remove gaps (e.g. {0, 2, 2} -> {0, 1, 1}).
func compactLabels(labels []int) []int {
	seen := map[int]int{}
	next := 0
	result := make([]int, len(labels))
	for i, l := range labels {
		if _, ok := seen[l]; !ok {
			seen[l] = next
			next++
		}
		result[i] = seen[l]
	}
	return result
}

// computeRepness finds representative positions for each cluster.
// Repness = how much more a cluster agrees with a position vs other clusters.
func computeRepness(matrix *mat.Dense, labels []int, agents []string, positions []deliberation.Position, nAgents, nPositions int) map[int][]RepPosition {
	// Compute per-cluster mean vote for each position
	clusterIDs := uniqueInts(labels)
	clusterMeans := map[int][]float64{} // cluster -> position means
	for _, cid := range clusterIDs {
		means := make([]float64, nPositions)
		count := 0
		for i := 0; i < nAgents; i++ {
			if labels[i] == cid {
				for j := 0; j < nPositions; j++ {
					means[j] += matrix.At(i, j)
				}
				count++
			}
		}
		if count > 0 {
			for j := range means {
				means[j] /= float64(count)
			}
		}
		clusterMeans[cid] = means
	}

	// Global mean per position
	globalMeans := make([]float64, nPositions)
	for j := 0; j < nPositions; j++ {
		for i := 0; i < nAgents; i++ {
			globalMeans[j] += matrix.At(i, j)
		}
		globalMeans[j] /= float64(nAgents)
	}

	// Repness score = cluster_mean - global_mean (positive = cluster agrees more than average)
	result := map[int][]RepPosition{}
	for _, cid := range clusterIDs {
		var reps []RepPosition
		for j := 0; j < nPositions; j++ {
			score := clusterMeans[cid][j] - globalMeans[j]
			if math.Abs(score) > 0.1 { // only include meaningfully different positions
				reps = append(reps, RepPosition{
					PositionID: positions[j].ID,
					Content:    positions[j].Content,
					Score:      score,
				})
			}
		}
		// Sort by absolute score descending
		sort.Slice(reps, func(i, j int) bool {
			return math.Abs(reps[i].Score) > math.Abs(reps[j].Score)
		})
		// Keep top 5
		if len(reps) > 5 {
			reps = reps[:5]
		}
		result[cid] = reps
	}
	return result
}

// computeConsensus finds positions with high agreement across all clusters.
// Polis consensus: overall agree > 50%, agree in every cluster > 50%.
func computeConsensus(matrix *mat.Dense, labels []int, positions []deliberation.Position, nAgents, nPositions int) []deliberation.ConsensusStatement {
	clusterIDs := uniqueInts(labels)

	var consensus []deliberation.ConsensusStatement
	for j := 0; j < nPositions; j++ {
		// Overall agree ratio (vote == 1)
		agrees := 0
		voters := 0
		for i := 0; i < nAgents; i++ {
			v := matrix.At(i, j)
			if v != 0 { // count non-pass votes
				voters++
				if v == 1 {
					agrees++
				}
			}
		}
		if voters == 0 {
			continue
		}
		overallRatio := float64(agrees) / float64(voters)

		// Per-cluster agree ratio
		minClusterRatio := 1.0
		allClustersHaveVoters := true
		for _, cid := range clusterIDs {
			cAgrees, cVoters := 0, 0
			for i := 0; i < nAgents; i++ {
				if labels[i] == cid {
					v := matrix.At(i, j)
					if v != 0 {
						cVoters++
						if v == 1 {
							cAgrees++
						}
					}
				}
			}
			if cVoters == 0 {
				allClustersHaveVoters = false
				break
			}
			ratio := float64(cAgrees) / float64(cVoters)
			if ratio < minClusterRatio {
				minClusterRatio = ratio
			}
		}

		if !allClustersHaveVoters {
			continue
		}

		// Supermajority consensus: 67% overall and 67% in every cluster
		if overallRatio >= 0.67 && minClusterRatio >= 0.67 {
			consensus = append(consensus, deliberation.ConsensusStatement{
				PositionID:           positions[j].ID,
				Content:              positions[j].Content,
				OverallAgreeRatio:    overallRatio,
				MinClusterAgreeRatio: minClusterRatio,
			})
		}
	}

	// Sort by overall agree ratio descending
	sort.Slice(consensus, func(i, j int) bool {
		return consensus[i].OverallAgreeRatio > consensus[j].OverallAgreeRatio
	})
	return filterPlatitudes(consensus)
}

// filterPlatitudes removes consensus statements that are too vague to be informative.
func filterPlatitudes(statements []deliberation.ConsensusStatement) []deliberation.ConsensusStatement {
	var filtered []deliberation.ConsensusStatement
	for _, s := range statements {
		if !isPlatitude(s.Content) {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// isPlatitude detects vague, non-specific statements that trivially achieve consensus.
func isPlatitude(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"new frameworks are needed",
		"more research is needed",
		"a balanced approach",
		"raises important implications",
		"it is important to",
		"there should be a focus on",
		"stakeholders should be consulted",
		"further investigation is warranted",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	// Very short statements with only vague terms are likely platitudes.
	// Don't filter short statements that contain specific terms (proper nouns, numbers, etc.)
	words := strings.Fields(text)
	if len(words) >= 5 && len(words) < 10 {
		hasSpecific := false
		for _, w := range words {
			// Check for capitalized words (proper nouns) beyond sentence start
			if len(w) > 3 && w[0] >= 'A' && w[0] <= 'Z' {
				hasSpecific = true
				break
			}
		}
		if !hasSpecific {
			return true
		}
	}
	return false
}

func uniqueInts(s []int) []int {
	seen := map[int]bool{}
	var result []int
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	sort.Ints(result)
	return result
}
