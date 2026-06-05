package analysis

import (
	"context"
	"math"
	"sort"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
)

// Synthesizer cross-references text analysis (LLM-based taxonomy, claims, cruxes)
// with vote matrix analysis (PCA clusters, repness, consensus).
type Synthesizer struct {
	text  *TextAnalyzer
	votes *VoteAnalyzer
}

func NewSynthesizer(client *llm.Client) *Synthesizer {
	return &Synthesizer{
		text:  NewTextAnalyzer(client),
		votes: NewVoteAnalyzer(),
	}
}

// SetCache enables LLM response caching for claim extraction.
func (s *Synthesizer) SetCache(c ClaimCache) {
	s.text.SetCache(c)
}

// SetStabilityCheckSamples enables opt-in crux-stability re-sampling. 0 or 1
// disables the check; 2+ triggers N extra LLM calls per generated crux plus a
// semantic-judge call per sample. See TextAnalyzer.StabilityCheckSamples for
// the cost and semantics.
func (s *Synthesizer) SetStabilityCheckSamples(n int) {
	s.text.StabilityCheckSamples = n
}

// SetReputation wires the persistent EigenTrust + cold-start reputation
// layer into the effective-weight chain. Pass nil (or leave unset) to
// keep the feature disabled — this is the default.
func (s *Synthesizer) SetReputation(r ReputationWeigher) {
	s.text.Reputation = r
}

// SetSecondary wires the cross-family OOD consistency check. Pass nil
// to disable. sampleK <= 0 falls back to the default (5).
func (s *Synthesizer) SetSecondary(sec llm.SecondaryStructuredOutput, sampleK int) {
	s.text.SetSecondary(sec, sampleK)
}

// GenerateCompromise produces a compromise statement from analysis results.
func (s *Synthesizer) GenerateCompromise(ctx context.Context, topic string, result *deliberation.AnalysisResult) (string, error) {
	return s.text.GenerateCompromise(ctx, topic, result)
}

// GenerateCompromiseWithChoice is the forced-choice variant used by the
// calibration runner. See TextAnalyzer.GenerateCompromiseWithChoice.
func (s *Synthesizer) GenerateCompromiseWithChoice(ctx context.Context, topic string, result *deliberation.AnalysisResult, options []string, optionVotes map[string]int) (string, string, error) {
	return s.text.GenerateCompromiseWithChoice(ctx, topic, result, options, optionVotes)
}

// Reframe restates a position emphasizing common ground.
func (s *Synthesizer) Reframe(ctx context.Context, position, otherPositions, cruxes string) (string, error) {
	return s.text.Reframe(ctx, position, otherPositions, cruxes)
}

func (s *Synthesizer) Analyze(ctx context.Context, positions []deliberation.Position, votes []deliberation.Vote, agents []string) (*deliberation.AnalysisResult, error) {
	// Always run text analysis (the primary engine)
	textResult, err := s.text.Analyze(ctx, positions, votes, agents)
	if err != nil {
		return nil, err
	}

	// Attempt vote matrix analysis (activates only with sufficient data)
	voteResult := s.votes.Analyze(ctx, positions, votes, agents)
	if voteResult == nil {
		return textResult, nil
	}

	// Cross-reference: merge the two analysis results
	return s.merge(textResult, voteResult, positions), nil
}

func (s *Synthesizer) merge(text *deliberation.AnalysisResult, vote *VoteResult, positions []deliberation.Position) *deliberation.AnalysisResult {
	result := *text // copy text result as base

	// Use vote-based clusters (statistically grounded) over text-based (heuristic)
	result.Clusters = vote.Clusters

	// Enrich clusters with representative positions from repness analysis
	for i, cluster := range result.Clusters {
		if reps, ok := vote.Repness[cluster.ID]; ok {
			repContents := make([]string, 0, len(reps))
			for _, r := range reps {
				if r.Score > 0 { // only positive repness (cluster agrees more than average)
					repContents = append(repContents, r.Content)
				}
			}
			result.Clusters[i].RepresentativePositions = repContents
		}
	}

	// Merge consensus: union of text and vote consensus, prefer vote scores
	result.ConsensusStatements = mergeConsensus(text.ConsensusStatements, vote.Consensus)

	// Enrich cruxes with vote-derived controversy data
	result.Cruxes = enrichCruxes(result.Cruxes, vote, positions)

	return &result
}

// mergeConsensus combines consensus from both engines, deduplicating by position ID.
func mergeConsensus(textConsensus, voteConsensus []deliberation.ConsensusStatement) []deliberation.ConsensusStatement {
	seen := map[string]deliberation.ConsensusStatement{}

	// Vote consensus takes precedence (more statistically rigorous)
	for _, c := range voteConsensus {
		seen[c.PositionID] = c
	}
	// Add text consensus only for positions not already covered
	for _, c := range textConsensus {
		if _, ok := seen[c.PositionID]; !ok {
			seen[c.PositionID] = c
		}
	}

	result := make([]deliberation.ConsensusStatement, 0, len(seen))
	for _, c := range seen {
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].OverallAgreeRatio > result[j].OverallAgreeRatio
	})
	return result
}

// enrichCruxes uses PCA coordinates to add distance-based controversy scoring.
func enrichCruxes(cruxes []deliberation.Crux, vote *VoteResult, positions []deliberation.Position) []deliberation.Crux {
	if len(vote.PCACoords) == 0 {
		return cruxes
	}

	for i, crux := range cruxes {
		// Compute mean PCA distance between agree and disagree groups
		agreeCoords := collectCoords(crux.AgreeAgents, vote.PCACoords)
		disagreeCoords := collectCoords(crux.DisagreeAgents, vote.PCACoords)

		if len(agreeCoords) > 0 && len(disagreeCoords) > 0 {
			dist := meanGroupDistance(agreeCoords, disagreeCoords)
			// Blend: 70% LLM-based controversy, 30% vote-distance-based
			// Normalize distance to [0,1] using sigmoid
			voteFactor := 2.0/(1.0+math.Exp(-dist)) - 1.0 // maps [0,inf) -> [0,1)
			cruxes[i].ControversyScore = 0.7*crux.ControversyScore + 0.3*voteFactor
		}
	}
	return cruxes
}

func collectCoords(agents []string, coords map[string][2]float64) [][2]float64 {
	var result [][2]float64
	for _, a := range agents {
		if c, ok := coords[a]; ok {
			result = append(result, c)
		}
	}
	return result
}

func meanGroupDistance(a, b [][2]float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// Mean of all pairwise distances between group centroids
	ax, ay := centroid(a)
	bx, by := centroid(b)
	dx := ax - bx
	dy := ay - by
	return math.Sqrt(dx*dx + dy*dy)
}

func centroid(pts [][2]float64) (float64, float64) {
	x, y := 0.0, 0.0
	for _, p := range pts {
		x += p[0]
		y += p[1]
	}
	n := float64(len(pts))
	return x / n, y / n
}
