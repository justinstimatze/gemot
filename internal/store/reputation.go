package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/justinstimatze/gemot/internal/analysis"
)

// VertexKey prefix constants. Reputation vertices are namespaced to
// distinguish key-bound identities ("key:<agent_keys.id>") from legacy
// symbolic identities ("id:<agent_id>"). The prefix is reserved — bare
// agent_ids are always wrapped by the reputation layer before hitting
// persistence, so these prefixes can never collide with a user-supplied
// agent_id. See schema.sql v4 migration for the full rationale.
const (
	VertexKeyPrefix = "key:"
	VertexIDPrefix  = "id:"
)

// Reputation is the persisted per-agent reputation state.
type Reputation struct {
	AgentID       string
	Score         float64
	SurvivedCount int
}

// ResolveVertices maps symbolic agent_ids to their canonical reputation
// vertex strings. An agent with an active (non-revoked) key gets the
// "key:<agent_keys.id>" form; agents without active keys fall back to
// "id:<agent_id>". One batched query via `unnest` + LEFT JOIN so the
// call cost is O(1) DB round-trips regardless of cohort size. Callers
// that emit edges or persist scores must resolve before writing so the
// vertex pinned on-row matches the key active at emission time.
func (s *DB) ResolveVertices(ctx context.Context, agents []string) (map[string]string, error) {
	out := make(map[string]string, len(agents))
	if len(agents) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.raw, k.id
		 FROM unnest($1::text[]) AS a(raw)
		 LEFT JOIN agent_keys k
		     ON k.agent_id = a.raw AND k.revoked_at IS NULL`,
		agents)
	if err != nil {
		return nil, fmt.Errorf("resolve vertices: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var agent string
		var keyID sql.NullString
		if err := rows.Scan(&agent, &keyID); err != nil {
			return nil, fmt.Errorf("scan vertex: %w", err)
		}
		if keyID.Valid && keyID.String != "" {
			out[agent] = VertexKeyPrefix + keyID.String
		} else {
			out[agent] = VertexIDPrefix + agent
		}
	}
	return out, rows.Err()
}

// LoadReputation fetches the reputation rows for the given symbolic
// agents. Internally resolves each agent to its current canonical vertex
// via agent_keys lookup so callers do not need to know about the
// key-binding storage format. Agents with no row yet are omitted from
// the result map (caller treats missing entries as zero score / zero
// survived_count — the cold-start state). The returned map is keyed by
// the originally-requested symbolic agent_id, not by the vertex string.
func (s *DB) LoadReputation(ctx context.Context, agents []string) (map[string]Reputation, error) {
	out := make(map[string]Reputation, len(agents))
	if len(agents) == 0 {
		return out, nil
	}
	vertices, err := s.ResolveVertices(ctx, agents)
	if err != nil {
		return nil, err
	}
	reverseMap := make(map[string]string, len(vertices))
	vertexList := make([]string, 0, len(vertices))
	for agent, vertex := range vertices {
		reverseMap[vertex] = agent
		vertexList = append(vertexList, vertex)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id, score, survived_count FROM agent_reputation WHERE agent_id = ANY($1)`,
		vertexList)
	if err != nil {
		return nil, fmt.Errorf("load reputation: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var vertex string
		var r Reputation
		if err := rows.Scan(&vertex, &r.Score, &r.SurvivedCount); err != nil {
			return nil, fmt.Errorf("scan reputation: %w", err)
		}
		if sym, ok := reverseMap[vertex]; ok {
			r.AgentID = sym
			out[sym] = r
		}
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

// ApplyDisputeEdges subtracts dispute-edge weights from the trust graph
// in one batched round-trip. Unlike AccumulateTrustEdges, this intentionally
// does NOT touch last_updated — decay tracks "when was this endorsement
// last reinforced" and a dispute is not a reinforcement. Weights are
// allowed to go negative; EigenTrust clamps non-positive edges to zero
// at input, so negative storage means "inbound trust mass is cancelled
// out until disputes are balanced by fresh endorsements."
func (s *DB) ApplyDisputeEdges(ctx context.Context, edges []analysis.Edge) error {
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
	// Insert with negated weight (-w). On conflict, add EXCLUDED.weight
	// (which equals -w) to the existing row, yielding weight - w.
	// last_updated is NOT bumped: a dispute is not an endorsement
	// refresh, so decay should continue to see the true age of the
	// inbound-trust signal (the previous endorsement).
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_trust_edges (from_agent, to_agent, weight, last_updated)
		 SELECT f, t, -w, NOW()
		 FROM unnest($1::text[], $2::text[], $3::float8[]) AS u(f, t, w)
		 ON CONFLICT (from_agent, to_agent) DO UPDATE
		 SET weight = agent_trust_edges.weight + EXCLUDED.weight`,
		froms, tos, weights)
	if err != nil {
		return fmt.Errorf("apply dispute edges: %w", err)
	}
	return nil
}

// DecayTrustEdges applies exponential weight decay to edges older than
// one hour: weight *= 0.5^(age / halfLife). The one-hour skip prevents
// double-decay when UpdateFromRound runs back-to-back in quick
// succession. Uses Postgres' POWER + EXTRACT(EPOCH FROM ...) so the
// calculation happens server-side in a single UPDATE.
func (s *DB) DecayTrustEdges(ctx context.Context, halfLife time.Duration) error {
	if halfLife <= 0 {
		return nil
	}
	halfLifeSeconds := halfLife.Seconds()
	_, err := s.db.ExecContext(ctx,
		`UPDATE agent_trust_edges
		 SET weight = weight * POWER(0.5, EXTRACT(EPOCH FROM (NOW() - last_updated)) / $1),
		     last_updated = NOW()
		 WHERE last_updated < NOW() - INTERVAL '1 hour'`,
		halfLifeSeconds)
	if err != nil {
		return fmt.Errorf("decay trust edges: %w", err)
	}
	return nil
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
