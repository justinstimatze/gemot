package tests

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

const polisDataDir = "/home/justin/Documents/agent-coordination-research/polis/delphi/real_data/r4tykwac8thvzv35jrn53-biodiversity/"

// goldenCluster is the ground truth from Polis's golden_snapshot.json.
type goldenCluster struct {
	ID      int       `json:"id"`
	Center  []float64 `json:"center"`
	Members []int     `json:"members"`
}

type goldenSnapshot struct {
	Stages map[string]struct {
		GroupClusters []goldenCluster `json:"group_clusters"`
		PCA           json.RawMessage `json:"pca"`
	} `json:"stages"`
}

func loadPolisData(t *testing.T) ([]deliberation.Position, []deliberation.Vote, []string) {
	t.Helper()

	// Load comments
	commentsFile := polisDataDir + "2025-11-11-1704-r4tykwac8thvzv35jrn53-comments.csv"
	cf, err := os.Open(commentsFile)
	if err != nil {
		t.Skipf("Polis data not available: %v", err)
	}
	defer cf.Close()

	cr := csv.NewReader(cf)
	cr.LazyQuotes = true
	records, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("parsing comments CSV: %v", err)
	}

	// Build positions from moderated (approved) comments
	// Schema: timestamp,datetime,comment-id,author-id,agrees,disagrees,moderated,comment-body
	positionMap := map[string]deliberation.Position{}
	for _, row := range records[1:] { // skip header
		if len(row) < 8 {
			continue
		}
		moderated, _ := strconv.Atoi(row[6])
		if moderated != 1 { // only approved comments
			continue
		}
		commentID := row[2]
		authorID := row[3]
		positionMap[commentID] = deliberation.Position{
			ID:             commentID,
			DeliberationID: "polis-biodiversity",
			AgentID:        "voter-" + authorID,
			Content:        row[7],
			Round:          1,
		}
	}

	positions := make([]deliberation.Position, 0, len(positionMap))
	for _, p := range positionMap {
		positions = append(positions, p)
	}

	// Load votes
	votesFile := polisDataDir + "2025-11-11-1704-r4tykwac8thvzv35jrn53-votes.csv"
	vf, err := os.Open(votesFile)
	if err != nil {
		t.Fatalf("opening votes CSV: %v", err)
	}
	defer vf.Close()

	vr := csv.NewReader(vf)
	vRecords, err := vr.ReadAll()
	if err != nil {
		t.Fatalf("parsing votes CSV: %v", err)
	}

	// Schema: timestamp,datetime,comment-id,voter-id,vote
	agentSet := map[string]bool{}
	var votes []deliberation.Vote
	for i, row := range vRecords[1:] {
		if len(row) < 5 {
			continue
		}
		commentID := row[2]
		// Only include votes for moderated comments
		if _, ok := positionMap[commentID]; !ok {
			continue
		}
		voterID := "voter-" + row[3]
		voteVal, _ := strconv.Atoi(row[4])
		agentSet[voterID] = true
		votes = append(votes, deliberation.Vote{
			ID:             fmt.Sprintf("v-%d", i),
			DeliberationID: "polis-biodiversity",
			AgentID:        voterID,
			PositionID:     commentID,
			Value:          voteVal,
		})
	}

	agents := make([]string, 0, len(agentSet))
	for a := range agentSet {
		agents = append(agents, a)
	}

	return positions, votes, agents
}

func loadGoldenClusters(t *testing.T) []goldenCluster {
	t.Helper()
	f, err := os.Open(polisDataDir + "golden_snapshot.json")
	if err != nil {
		t.Skipf("Golden snapshot not available: %v", err)
	}
	defer f.Close()

	var snapshot goldenSnapshot
	if err := json.NewDecoder(f).Decode(&snapshot); err != nil {
		t.Fatalf("parsing golden snapshot: %v", err)
	}

	stage, ok := snapshot.Stages["after_full_recompute"]
	if !ok {
		t.Fatal("missing after_full_recompute stage")
	}
	return stage.GroupClusters
}

func TestPolisVoteMatrixBenchmark(t *testing.T) {
	positions, votes, agents := loadPolisData(t)
	golden := loadGoldenClusters(t)

	t.Logf("Loaded: %d positions, %d votes, %d agents", len(positions), len(votes), len(agents))
	t.Logf("Golden: %d clusters", len(golden))
	for _, gc := range golden {
		t.Logf("  Cluster %d: %d members", gc.ID, len(gc.Members))
	}

	// Run our vote matrix analysis
	va := analysis.NewVoteAnalyzer()
	va.MinAgents = 5
	va.MinPositions = 3
	va.MinCoverage = 0.01 // Polis data is sparse
	result := va.Analyze(context.Background(), positions, votes, agents)
	if result == nil {
		t.Fatal("vote analyzer returned nil — insufficient data")
	}

	t.Logf("Our result: %d clusters", len(result.Clusters))
	for _, c := range result.Clusters {
		t.Logf("  Cluster %d: %d members", c.ID, len(c.AgentIDs))
	}

	// Metric 1: Did we find a similar number of clusters?
	if len(result.Clusters) < 2 {
		t.Errorf("expected at least 2 clusters (Polis found %d), got %d", len(golden), len(result.Clusters))
	}

	// Metric 2: Cluster size distribution — is the largest cluster roughly proportional?
	// Polis: 456/536 = 85% in cluster 0, 80/536 = 15% in cluster 1
	maxClusterSize := 0
	for _, c := range result.Clusters {
		if len(c.AgentIDs) > maxClusterSize {
			maxClusterSize = len(c.AgentIDs)
		}
	}
	largestRatio := float64(maxClusterSize) / float64(len(agents))
	polisLargestRatio := float64(len(golden[0].Members)) / float64(len(golden[0].Members)+len(golden[1].Members))
	t.Logf("Largest cluster ratio: ours=%.2f, polis=%.2f", largestRatio, polisLargestRatio)

	// Metric 3: Build voter-ID to Polis-cluster mapping for overlap analysis
	polisAssignment := map[string]int{} // voter-ID -> polis cluster
	for _, gc := range golden {
		for _, memberIdx := range gc.Members {
			polisAssignment[fmt.Sprintf("voter-%d", memberIdx)] = gc.ID
		}
	}

	// For each of our clusters, compute overlap with each Polis cluster
	t.Log("Cluster overlap analysis:")
	for _, c := range result.Clusters {
		polisOverlap := map[int]int{} // polis cluster -> count
		unmatched := 0
		for _, agentID := range c.AgentIDs {
			if polisCluster, ok := polisAssignment[agentID]; ok {
				polisOverlap[polisCluster]++
			} else {
				unmatched++
			}
		}
		bestPolisCluster := -1
		bestOverlap := 0
		for pid, count := range polisOverlap {
			if count > bestOverlap {
				bestOverlap = count
				bestPolisCluster = pid
			}
		}
		purity := 0.0
		if len(c.AgentIDs)-unmatched > 0 {
			purity = float64(bestOverlap) / float64(len(c.AgentIDs)-unmatched)
		}
		t.Logf("  Our cluster %d (%d agents): best match = Polis cluster %d (overlap=%d, purity=%.2f)",
			c.ID, len(c.AgentIDs), bestPolisCluster, bestOverlap, purity)
	}

	// Metric 4: Consensus positions
	t.Logf("Consensus positions found: %d", len(result.Consensus))
	for _, cs := range result.Consensus {
		t.Logf("  Position %s: overall=%.2f, min_cluster=%.2f — %s",
			cs.PositionID, cs.OverallAgreeRatio, cs.MinClusterAgreeRatio,
			truncate(cs.Content, 80))
	}

	// Metric 5: PCA coordinates sanity — are they spread out?
	if len(result.PCACoords) == 0 {
		t.Error("no PCA coordinates generated")
	} else {
		minX, maxX := 1e9, -1e9
		minY, maxY := 1e9, -1e9
		for _, coord := range result.PCACoords {
			if coord[0] < minX {
				minX = coord[0]
			}
			if coord[0] > maxX {
				maxX = coord[0]
			}
			if coord[1] < minY {
				minY = coord[1]
			}
			if coord[1] > maxY {
				maxY = coord[1]
			}
		}
		t.Logf("PCA spread: X=[%.2f, %.2f], Y=[%.2f, %.2f]", minX, maxX, minY, maxY)
		if maxX-minX < 0.001 {
			t.Error("PCA X dimension has no spread — likely degenerate")
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
