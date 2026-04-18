package store

import (
	"context"
	"fmt"

	"github.com/justinstimatze/gemot/internal/analysis"
)

// Reputation is the persisted per-agent reputation state.
type Reputation struct {
	AgentID       string
	Score         float64
	SurvivedCount int
}

// LoadReputation fetches the reputation rows for the given agents.
// Agents with no row yet are omitted from the result map (caller treats
// missing entries as zero score / zero survived_count — the cold-start
// state).
func (s *DB) LoadReputation(ctx context.Context, agents []string) (map[string]Reputation, error) {
	out := make(map[string]Reputation, len(agents))
	if len(agents) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id, score, survived_count FROM agent_reputation WHERE agent_id = ANY($1)`,
		agents)
	if err != nil {
		return nil, fmt.Errorf("load reputation: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var r Reputation
		if err := rows.Scan(&r.AgentID, &r.Score, &r.SurvivedCount); err != nil {
			return nil, fmt.Errorf("scan reputation: %w", err)
		}
		out[r.AgentID] = r
	}
	return out, rows.Err()
}

// IncrementSurvivedCounts increments survived_count by 1 for each
// agent in one batched round-trip via `unnest`. Replaces the old
// per-row transaction loop that was a lock-contention amplifier on
// large cohorts.
func (s *DB) IncrementSurvivedCounts(ctx context.Context, agents []string) error {
	if len(agents) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_reputation (agent_id, survived_count, updated_at)
		 SELECT a, 1, NOW() FROM unnest($1::text[]) AS t(a)
		 ON CONFLICT (agent_id) DO UPDATE
		 SET survived_count = agent_reputation.survived_count + 1,
		     updated_at = NOW()`,
		agents)
	if err != nil {
		return fmt.Errorf("increment survived: %w", err)
	}
	return nil
}

// AccumulateTrustEdges adds the given weights to existing edge rows
// in one batched round-trip via `unnest`. Edges are aggregated
// cumulatively across deliberations — the weight column is the total
// number of observed "A endorsed B" events, not an average.
func (s *DB) AccumulateTrustEdges(ctx context.Context, edges []analysis.Edge) error {
	if len(edges) == 0 {
		return nil
	}
	froms := make([]string, 0, len(edges))
	tos := make([]string, 0, len(edges))
	weights := make([]float64, 0, len(edges))
	for _, e := range edges {
		if e.Weight <= 0 {
			continue
		}
		froms = append(froms, e.From)
		tos = append(tos, e.To)
		weights = append(weights, e.Weight)
	}
	if len(froms) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_trust_edges (from_agent, to_agent, weight, last_updated)
		 SELECT f, t, w, NOW()
		 FROM unnest($1::text[], $2::text[], $3::float8[]) AS u(f, t, w)
		 ON CONFLICT (from_agent, to_agent) DO UPDATE
		 SET weight = agent_trust_edges.weight + EXCLUDED.weight,
		     last_updated = NOW()`,
		froms, tos, weights)
	if err != nil {
		return fmt.Errorf("accumulate trust edges: %w", err)
	}
	return nil
}

// LoadTrustEdges returns the full trust graph. For deployments with
// many agents this is not scalable long-term — a future version should
// load only the subgraph reachable from the current cohort. For now
// power iteration over the full graph is cheap enough (O(V + E)) that
// a full load is fine at the scales this reaches.
func (s *DB) LoadTrustEdges(ctx context.Context) ([]analysis.Edge, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT from_agent, to_agent, weight FROM agent_trust_edges`)
	if err != nil {
		return nil, fmt.Errorf("load trust edges: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var edges []analysis.Edge
	for rows.Next() {
		var e analysis.Edge
		if err := rows.Scan(&e.From, &e.To, &e.Weight); err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// PersistEigenTrustScores upserts scores for all agents in one batched
// round-trip via `unnest`. Called once per round close after power
// iteration.
func (s *DB) PersistEigenTrustScores(ctx context.Context, scores map[string]float64) error {
	if len(scores) == 0 {
		return nil
	}
	agents := make([]string, 0, len(scores))
	values := make([]float64, 0, len(scores))
	for a, v := range scores {
		agents = append(agents, a)
		values = append(values, v)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_reputation (agent_id, score, updated_at)
		 SELECT a, s, NOW() FROM unnest($1::text[], $2::float8[]) AS t(a, s)
		 ON CONFLICT (agent_id) DO UPDATE
		 SET score = EXCLUDED.score, updated_at = NOW()`,
		agents, values)
	if err != nil {
		return fmt.Errorf("persist scores: %w", err)
	}
	return nil
}
