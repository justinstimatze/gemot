package reputation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/store"
	"github.com/justinstimatze/gemot/types"
)

// Store is the narrow slice of *store.DB that the reputation layer
// needs. Broken out as an interface so the Weigher can be tested
// without a live DB.
type Store interface {
	LoadReputation(ctx context.Context, agents []string) (map[string]store.Reputation, error)
	ResolveVertices(ctx context.Context, agents []string) (map[string]string, error)
	LoadTrustEdges(ctx context.Context) ([]analysis.Edge, error)
	AccumulateTrustEdges(ctx context.Context, edges []analysis.Edge) error
	ApplyDisputeEdges(ctx context.Context, edges []analysis.Edge) error
	DecayTrustEdges(ctx context.Context, halfLife time.Duration) error
	IncrementSurvivedCounts(ctx context.Context, agents []string) error
	PersistEigenTrustScores(ctx context.Context, scores map[string]float64) error
}

// Config captures the knobs read from env at startup.
type Config struct {
	Enabled       bool
	ColdCap       float64
	ColdThreshold int
	Iterations    int
	// DBFail is "open" (default, preserves legacy behaviour) or
	// "closed". Under "closed", a LoadReputation failure propagates as
	// an error from WeightsFor, aborting the round. Under "open", the
	// weigher falls back to unit weights with a slog.Warn — which in a
	// Byzantine context silently neutralizes the cold-start defense if
	// an attacker can DoS Postgres. Hosted deployments should set
	// GEMOT_EIGENTRUST_DB_FAIL=closed.
	DBFail string
	// DecayHalfLifeDays controls exponential decay of stored trust-edge
	// weights. 0 (the default) disables decay — today's cumulative-
	// forever semantics. Positive values apply weight *= 0.5^(age/half)
	// to edges older than ~1h at the start of each recompute, damping
	// the whitewashing attack where a graduated Sybil ring retains
	// pumped-up edge mass indefinitely. Recommended value: 14 (two-
	// week half-life) for a deployment that expects weekly usage.
	DecayHalfLifeDays int
	// DisputeWeight is the per-dispute negative edge weight added when
	// an agent files a Dispute against a crux. A dispute from X against
	// a crux authored by Y subtracts this much weight from edge X→Y.
	// Default 0.5 — half the weight of a single agreement. Stored
	// weight is allowed to go negative; EigenTrust ignores non-positive
	// edges at input, so a sufficiently-disputed agent has that inbound
	// trust effectively clamped to zero.
	DisputeWeight float64
}

// DBFailClosed is the sentinel that activates fail-closed semantics.
const DBFailClosed = "closed"

// Weigher composes the persisted reputation state with the cold-start
// cap to produce per-agent multipliers for the effective-weight chain
// in text.go. Implements analysis.ReputationWeigher.
type Weigher struct {
	store Store
	cfg   Config
}

// NewWeigher returns nil when the feature is disabled, so callers can
// unconditionally pass the result to setters that accept nil as a
// disable signal.
func NewWeigher(s Store, cfg Config) *Weigher {
	if !cfg.Enabled {
		return nil
	}
	return &Weigher{store: s, cfg: cfg}
}

// WeightsFor implements analysis.ReputationWeigher. For each agent:
//
//   - If the agent's survived_count is below the cold-start threshold,
//     the returned weight is clamped to ColdCap regardless of score.
//     This is the primary Sybil defense during cold-start — fresh
//     identities cannot carry more than a fraction of a seasoned
//     agent's weight until they have accumulated validated history.
//     Post-graduation, the cap no longer applies and the raw score
//     governs, which is why larger Sybil rings can still pool weight
//     via mutual endorsement (see THREAT_MODEL graduation-cliff note).
//   - Otherwise, the weight is the normalized EigenTrust score scaled
//     into [ColdCap, 1.0]. The scaling anchors the minimum at the cold
//     cap so graduation is non-decreasing within a single cohort call.
//     Across calls with different cohort composition the normalization
//     denominator shifts, so graduation is not globally monotone over
//     time.
//
// Reads from the DB on every call; one call per analysis is the
// current rate. Batching scales as a tracked item in THREAT_MODEL.
//
// Returns an error only under Config.DBFail="closed" when the store
// read fails. Under the default fail-open mode, a store error is
// logged and the result is unit weights — preserves availability at
// the cost of stripping the cold-start cap during DB outages.
func (r *Weigher) WeightsFor(ctx context.Context, agents []string) (map[string]float64, error) {
	out := make(map[string]float64, len(agents))
	if len(agents) == 0 {
		return out, nil
	}
	reps, err := r.store.LoadReputation(ctx, agents)
	if err != nil {
		if r.cfg.DBFail == DBFailClosed {
			// Fail closed: abort the round rather than silently strip
			// the cold-start defense. Caller propagates via text.go →
			// service.Analyze, so the deliberation surfaces an error.
			return nil, fmt.Errorf("reputation load failed (fail-closed): %w", err)
		}
		// Fail open: unit weights keep the deliberation running. This
		// is the wrong default for a Byzantine-context attacker who can
		// DoS the DB to strip cold-start — hence the DBFail="closed"
		// toggle above.
		slog.Warn("reputation load failed — falling back to unit weights", "err", err)
		for _, a := range agents {
			out[a] = 1.0
		}
		return out, nil
	}

	var maxScore float64
	for _, a := range agents {
		if reps[a].Score > maxScore {
			maxScore = reps[a].Score
		}
	}

	for _, a := range agents {
		rep := reps[a]
		if rep.SurvivedCount < r.cfg.ColdThreshold {
			out[a] = r.cfg.ColdCap
			continue
		}
		if maxScore <= 0 {
			out[a] = 1.0
			continue
		}
		normalized := rep.Score / maxScore
		out[a] = r.cfg.ColdCap + normalized*(1.0-r.cfg.ColdCap)
	}
	return out, nil
}

// minDistinctAgreers is the number of distinct non-self agents whose
// agreement must point at an author's surviving positions before the
// author's survived_count increments for the round. Two blocks a
// 2-Sybil pair from graduating each other (the simplest ring attack);
// N ≥ 3 rings can still graduate via mutual endorsement — see
// THREAT_MODEL for the remaining graduation-cliff disclosure.
const minDistinctAgreers = 2

// UpdateFromRound ingests the qualified signals from a just-completed
// round and persists the reputation-layer state. Called by the service
// layer after analysis succeeds.
//
// Signals consumed per round:
//   - Trust edges: for each final crux, each distinct agent in
//     AgreeAgents contributes a unit edge toward each author of a
//     source-position on that crux (self-edges dropped). Duplicate
//     agreer entries on a single crux are deduped so repeated
//     listings cannot inflate edge weight.
//   - survived_count: increments for an author only when they have
//     received agreement from ≥ minDistinctAgreers distinct non-self
//     agents across their surviving cruxes this round.
//
// After edge accumulation, the global EigenTrust eigenvector is
// recomputed and persisted. Synchronous today; batching / debounced
// recompute is tracked in THREAT_MODEL.
func (r *Weigher) UpdateFromRound(
	ctx context.Context,
	cruxes []types.Crux,
	positionAuthors map[string]string,
	disputes []types.Dispute,
) error {
	// Collect every symbolic agent_id that appears in this round so we
	// can pin each one to its current vertex in a single batched lookup.
	// Emitted edges + survived_count increments + dispute edges all use
	// the vertex form so that a later key rotation doesn't retroactively
	// reassign attribution of edges written under the pre-rotation key.
	allAgents := map[string]struct{}{}
	for _, c := range cruxes {
		for _, pid := range c.SourcePositionIDs {
			if author, ok := positionAuthors[pid]; ok && author != "" {
				allAgents[author] = struct{}{}
			}
		}
		for _, agreer := range c.AgreeAgents {
			if agreer != "" {
				allAgents[agreer] = struct{}{}
			}
		}
	}
	for _, d := range disputes {
		if d.AgentID != "" {
			allAgents[d.AgentID] = struct{}{}
		}
	}
	names := make([]string, 0, len(allAgents))
	for a := range allAgents {
		names = append(names, a)
	}
	vertices, err := r.store.ResolveVertices(ctx, names)
	if err != nil {
		return fmt.Errorf("resolve vertices: %w", err)
	}
	vertex := func(id string) string {
		if v, ok := vertices[id]; ok {
			return v
		}
		// Defensive fallback: if ResolveVertices returned nothing for an
		// agent (shouldn't happen because every symbolic id went into the
		// input), treat as unsigned. Prefix with store.VertexIDPrefix to
		// stay consistent with the storage format.
		return store.VertexIDPrefix + id
	}

	agreersByAuthor := map[string]map[string]struct{}{}
	var edges []analysis.Edge
	// Build a claim→authors index for dispute mapping. We key by the
	// surviving crux's claim text because Dispute.CruxClaim holds the
	// claim string (the Dispute model predates persistent crux IDs).
	// Authors in this map are already vertex-form so disputes can emit
	// edges without re-resolving.
	cruxAuthorsByClaim := map[string]map[string]struct{}{}
	for _, c := range cruxes {
		authors := map[string]struct{}{}
		for _, pid := range c.SourcePositionIDs {
			if author, ok := positionAuthors[pid]; ok && author != "" {
				authors[vertex(author)] = struct{}{}
			}
		}
		if c.Claim != "" {
			cruxAuthorsByClaim[c.Claim] = authors
		}
		seenAgreers := map[string]struct{}{}
		for _, agreer := range c.AgreeAgents {
			agreerV := vertex(agreer)
			if _, dup := seenAgreers[agreerV]; dup {
				continue
			}
			seenAgreers[agreerV] = struct{}{}
			for authorV := range authors {
				if agreerV == authorV {
					continue
				}
				edges = append(edges, analysis.Edge{From: agreerV, To: authorV, Weight: 1})
				if agreersByAuthor[authorV] == nil {
					agreersByAuthor[authorV] = map[string]struct{}{}
				}
				agreersByAuthor[authorV][agreerV] = struct{}{}
			}
		}
	}

	var disputeEdges []analysis.Edge
	disputeWeight := r.cfg.DisputeWeight
	if disputeWeight <= 0 {
		disputeWeight = 0.5
	}
	for _, d := range disputes {
		authors, ok := cruxAuthorsByClaim[d.CruxClaim]
		if !ok {
			continue
		}
		disputerV := vertex(d.AgentID)
		for authorV := range authors {
			if authorV == disputerV {
				continue
			}
			disputeEdges = append(disputeEdges, analysis.Edge{
				From: disputerV, To: authorV, Weight: disputeWeight,
			})
		}
	}

	var survivedAuthors []string
	for authorV, agreers := range agreersByAuthor {
		if len(agreers) >= minDistinctAgreers {
			survivedAuthors = append(survivedAuthors, authorV)
		}
	}

	if err := r.store.IncrementSurvivedCounts(ctx, survivedAuthors); err != nil {
		return fmt.Errorf("increment survived: %w", err)
	}
	if err := r.store.AccumulateTrustEdges(ctx, edges); err != nil {
		return fmt.Errorf("accumulate edges: %w", err)
	}
	if err := r.store.ApplyDisputeEdges(ctx, disputeEdges); err != nil {
		return fmt.Errorf("apply dispute edges: %w", err)
	}
	return r.recomputeGlobalScores(ctx)
}

// recomputeGlobalScores runs power iteration over the full trust graph
// and persists the result. Called at round close after edge
// accumulation and survived-count increments.
func (r *Weigher) recomputeGlobalScores(ctx context.Context) error {
	if r.cfg.DecayHalfLifeDays > 0 {
		halfLife := time.Duration(r.cfg.DecayHalfLifeDays) * 24 * time.Hour
		if err := r.store.DecayTrustEdges(ctx, halfLife); err != nil {
			// Non-fatal: decay is a damping signal, and proceeding with
			// stale weights is better than aborting the reputation
			// recompute entirely. Log and continue.
			slog.Warn("decay trust edges failed — continuing with undecayed weights", "err", err)
		}
	}
	edges, err := r.store.LoadTrustEdges(ctx)
	if err != nil {
		return fmt.Errorf("load edges: %w", err)
	}
	// EigenTrust ignores non-positive edges at input (see eigentrust.go
	// line ~142), so negative dispute-edge weights naturally clamp to
	// zero in the transition matrix — an inbound dispute cancels an
	// endorsement of equal magnitude rather than punching below zero.
	cfg := analysis.EigenTrustConfig{Iterations: r.cfg.Iterations}
	scores := analysis.EigenTrust(edges, nil, cfg)
	if err := r.store.PersistEigenTrustScores(ctx, scores); err != nil {
		return fmt.Errorf("persist scores: %w", err)
	}
	return nil
}
