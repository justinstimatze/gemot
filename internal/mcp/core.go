package mcp

// core.go contains shared business logic called by both MCP and A2A handlers.
// This prevents bugs from diverging code paths — the transport layers are thin
// wrappers that extract params and format responses.

import (
	"context"
	"fmt"
	"strings"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
	"github.com/justinstimatze/gemot/internal/payments"
)

// CoreGetCommitments returns all commitments for a deliberation after access check.
func CoreGetCommitments(svc *deliberation.Service, deliberationID, keyID string) ([]deliberation.Commitment, error) {
	if deliberationID == "" {
		return nil, fmt.Errorf("deliberation_id is required")
	}
	if err := svc.CheckAccess(deliberationID, keyID); err != nil {
		return nil, err
	}
	return svc.GetCommitments(deliberationID)
}

// CorePublishPosition publishes a draft position after verifying ownership.
func CorePublishPosition(svc *deliberation.Service, positionID, keyID string) error {
	if positionID == "" {
		return fmt.Errorf("position_id is required")
	}
	if keyID != "" {
		pos, err := svc.GetPositionByID(positionID)
		if err != nil {
			return fmt.Errorf("position not found")
		}
		if !strings.HasPrefix(pos.AgentID, keyID+":") {
			return fmt.Errorf("access denied: you can only publish your own positions")
		}
	}
	return svc.PublishPosition(positionID)
}

// CoreChallengeAnalysis files a full analysis challenge as a dispute.
func CoreChallengeAnalysis(svc *deliberation.Service, deliberationID, agentID, reason, keyID string) (map[string]string, error) {
	if deliberationID == "" || agentID == "" || reason == "" {
		return nil, fmt.Errorf("deliberation_id, agent_id, and reason are required")
	}
	if err := svc.CheckAccess(deliberationID, keyID); err != nil {
		return nil, err
	}
	if _, err := svc.DisputeCrux(deliberationID, agentID, "[FULL ANALYSIS CHALLENGE]", reason); err != nil {
		return nil, err
	}
	return map[string]string{
		"status": "analysis challenged by " + agentID,
		"detail": "Challenge recorded as integrity warning. Call analyze to trigger re-analysis.",
	}, nil
}

// CoreReframe reframes a position with credit handling.
func CoreReframe(svc *deliberation.Service, credits *payments.CreditStore, deliberationID, positionID, model, keyID string, isAdmin bool, apiKey string) (map[string]string, error) {
	if deliberationID == "" || positionID == "" {
		return nil, fmt.Errorf("deliberation_id and position_id are required")
	}
	if err := svc.CheckAccess(deliberationID, keyID); err != nil {
		return nil, err
	}
	if model != "" && !llm.AllowedModels[model] {
		return nil, fmt.Errorf("unsupported model %q", model)
	}

	// Deduct credits
	var creditCost int
	if !isAdmin && credits != nil && apiKey != "" && strings.HasPrefix(apiKey, "gmt_") {
		creditCost = payments.CreditCost(model)
		if _, err := credits.Deduct(apiKey, creditCost); err != nil {
			balance, _ := credits.GetBalance(apiKey)
			return nil, fmt.Errorf("insufficient credits: have %d, need %d", balance, creditCost)
		}
	}

	ctx := context.Background()
	if model != "" {
		ctx = context.WithValue(ctx, llm.ContextKeyModel{}, model)
	}
	reframed, err := svc.ReframePosition(ctx, deliberationID, positionID)
	if err != nil {
		if creditCost > 0 && credits != nil && apiKey != "" {
			credits.AddCredits(apiKey, creditCost) //nolint:errcheck
		}
		return nil, err
	}
	return map[string]string{
		"original_position_id": positionID,
		"reframed":             reframed,
	}, nil
}

// CoreGetAnalysisResult returns an analysis result for a deliberation.
// If round is non-nil, returns that specific round; otherwise returns the latest.
func CoreGetAnalysisResult(svc *deliberation.Service, deliberationID, keyID string, round *int) (*deliberation.AnalysisResult, error) {
	if deliberationID == "" {
		return nil, fmt.Errorf("deliberation_id is required")
	}
	if err := svc.CheckAccess(deliberationID, keyID); err != nil {
		return nil, err
	}
	if round != nil {
		return svc.GetAnalysisResult(deliberationID, *round)
	}
	return svc.GetLatestAnalysisResult(deliberationID)
}

// CoreExportDeliberation returns the complete multi-round history of a deliberation.
func CoreExportDeliberation(svc *deliberation.Service, deliberationID, keyID string) (map[string]any, error) {
	if deliberationID == "" {
		return nil, fmt.Errorf("deliberation_id is required")
	}
	if err := svc.CheckAccess(deliberationID, keyID); err != nil {
		return nil, err
	}

	d, err := svc.GetDeliberation(deliberationID)
	if err != nil {
		return nil, err
	}

	// Get all positions (no round filter)
	positions, err := svc.GetPositions(deliberationID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("getting positions: %w", err)
	}

	// Group positions by round
	positionsByRound := make(map[int][]deliberation.Position)
	for _, p := range positions {
		positionsByRound[p.Round] = append(positionsByRound[p.Round], p)
	}

	// Build rounds array
	rounds := make([]map[string]any, 0, d.Round)
	for r := 1; r <= d.Round; r++ {
		roundData := map[string]any{
			"round":     r,
			"positions": positionsByRound[r],
		}
		// Get analysis for this round (may not exist)
		analysis, err := svc.GetAnalysisResult(deliberationID, r)
		if err == nil && analysis != nil {
			roundData["analysis"] = analysis
		} else {
			roundData["analysis"] = nil
		}
		rounds = append(rounds, roundData)
	}

	// Votes are not per-round — attach to first round for backwards compat
	votes, err := svc.GetVotes(deliberationID)
	if err != nil {
		return nil, fmt.Errorf("getting votes: %w", err)
	}
	if len(rounds) > 0 {
		rounds[0]["votes"] = votes
	}

	// Commitments
	commitments, err := svc.GetCommitments(deliberationID)
	if err != nil {
		return nil, fmt.Errorf("getting commitments: %w", err)
	}

	export := map[string]any{
		"deliberation": d,
		"rounds":       rounds,
		"commitments":  commitments,
		"resolution":   d.Resolution,
	}
	return export, nil
}

// CoreGetVotes returns all votes for a deliberation.
func CoreGetVotes(svc *deliberation.Service, deliberationID, keyID string) ([]deliberation.Vote, error) {
	if deliberationID == "" {
		return nil, fmt.Errorf("deliberation_id is required")
	}
	if err := svc.CheckAccess(deliberationID, keyID); err != nil {
		return nil, err
	}
	return svc.GetVotes(deliberationID)
}

// CoreListByGroup lists deliberations in a group.
func CoreListByGroup(svc *deliberation.Service, groupID, keyID string, isAdmin bool, limit, offset int) ([]deliberation.Deliberation, error) {
	if groupID == "" {
		return nil, fmt.Errorf("group_id is required")
	}
	effectiveKeyID := keyID
	if isAdmin {
		effectiveKeyID = "" // admins see all, empty keyID matches the OR condition
	}
	return svc.ListByGroup(groupID, limit, offset, effectiveKeyID)
}

// CoreListByAgent lists deliberations an agent has participated in.
func CoreListByAgent(svc *deliberation.Service, agentID, keyID string, isAdmin bool, limit, offset int) ([]deliberation.Deliberation, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	effectiveKeyID := keyID
	if isAdmin {
		effectiveKeyID = ""
	}
	return svc.ListByAgent(agentID, limit, offset, effectiveKeyID)
}

// CoreFulfillCommitment marks a commitment as fulfilled.
func CoreFulfillCommitment(svc *deliberation.Service, commitmentID, verifiedBy string) error {
	if commitmentID == "" {
		return fmt.Errorf("commitment_id is required")
	}
	return svc.FulfillCommitment(commitmentID, verifiedBy)
}

// CoreBreakCommitment marks a commitment as broken with a reason.
func CoreBreakCommitment(svc *deliberation.Service, commitmentID, reason, verifiedBy string) error {
	if commitmentID == "" || reason == "" {
		return fmt.Errorf("commitment_id and reason are required")
	}
	return svc.BreakCommitment(commitmentID, reason, verifiedBy)
}

// CoreAgentReputation returns an agent's commitment track record.
func CoreAgentReputation(svc *deliberation.Service, agentID, groupID string) (deliberation.ReputationSummary, error) {
	if agentID == "" {
		return deliberation.ReputationSummary{}, fmt.Errorf("agent_id is required")
	}
	return svc.AgentReputation(agentID, groupID)
}

// CoreCancelAnalysis cancels an in-progress analysis after access check.
func CoreCancelAnalysis(svc *deliberation.Service, deliberationID, keyID string) error {
	if deliberationID == "" {
		return fmt.Errorf("deliberation_id is required")
	}
	if err := svc.CheckAccess(deliberationID, keyID); err != nil {
		return err
	}
	return svc.CancelAnalysis(deliberationID)
}

// CoreWithdraw removes an agent from a deliberation after access check and agent scoping.
func CoreWithdraw(svc *deliberation.Service, deliberationID, agentID, keyID string) error {
	if deliberationID == "" || agentID == "" {
		return fmt.Errorf("deliberation_id and agent_id are required")
	}
	if err := svc.CheckAccess(deliberationID, keyID); err != nil {
		return err
	}
	return svc.WithdrawAgent(deliberationID, agentID)
}
