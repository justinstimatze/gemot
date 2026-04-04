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

// CoreGetAnalysisResult returns the latest analysis result for a deliberation.
func CoreGetAnalysisResult(svc *deliberation.Service, deliberationID, keyID string) (*deliberation.AnalysisResult, error) {
	if deliberationID == "" {
		return nil, fmt.Errorf("deliberation_id is required")
	}
	if err := svc.CheckAccess(deliberationID, keyID); err != nil {
		return nil, err
	}
	return svc.GetLatestAnalysisResult(deliberationID)
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

// CoreListByGroup lists deliberations in a group with visibility filtering.
func CoreListByGroup(svc *deliberation.Service, groupID, keyID string, isAdmin bool, limit, offset int) ([]deliberation.Deliberation, error) {
	if groupID == "" {
		return nil, fmt.Errorf("group_id is required")
	}
	all, err := svc.ListByGroup(groupID, limit, offset)
	if err != nil {
		return nil, err
	}
	return filterVisible(all, keyID, isAdmin), nil
}

// CoreListByAgent lists deliberations an agent has participated in.
func CoreListByAgent(svc *deliberation.Service, agentID, keyID string, isAdmin bool, limit, offset int) ([]deliberation.Deliberation, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	all, err := svc.ListByAgent(agentID, limit, offset)
	if err != nil {
		return nil, err
	}
	return filterVisible(all, keyID, isAdmin), nil
}

// filterVisible removes private deliberations not owned by the caller.
func filterVisible(all []deliberation.Deliberation, keyID string, isAdmin bool) []deliberation.Deliberation {
	result := make([]deliberation.Deliberation, 0, len(all))
	for _, d := range all {
		if d.Visibility == "private" && d.CreatorKey != keyID && !isAdmin {
			continue
		}
		result = append(result, d)
	}
	return result
}
