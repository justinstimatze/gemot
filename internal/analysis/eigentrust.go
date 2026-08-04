package analysis

import "context"

// ReputationWeigher returns per-agent reputation multipliers in [0, 1]
// for a given cohort. The caller multiplies these into the effective-
// weight chain alongside TrustWeights and CorrelationDiscountedWeights.
//
// Agents omitted from the returned map are treated as unit-weight
// (reputation neutral). Implementations apply both the EigenTrust score
// and the cold-start cap internally — text.go just multiplies the
// result in.
//
// Concrete implementation lives in internal/deliberation because it
// composes the store (for persisted reputation + trust edges) and the
// config (for the cold-start threshold and cap). Defining the interface
// here avoids a store → analysis dependency.
type ReputationWeigher interface {
	// WeightsFor returns the per-agent weight map. A non-nil error
	// signals the implementation is in fail-closed mode and the
	// backing store was unreachable — callers must abort the analysis
	// rather than proceed with neutralized reputation.
	//
	// principalOf maps an agent_id to the verified delegation principal
	// whose reputation it should be weighted by this round (Move 5).
	// Reputation is looked up by that principal, and a principal's total
	// weight is conserved across the agents it fields in the cohort
	// (equal split), so fielding N agents does not multiply its influence.
	// A nil/empty map weights every agent by its own standing.
	WeightsFor(ctx context.Context, agents []string, principalOf map[string]string) (map[string]float64, error)
}

// EigenTrust implements the global trust-eigenvector computation of
// Kamvar, Schlosser, Garcia-Molina (SIGIR 2003) over a sparse directed
// trust graph. Vertices are agent IDs; an edge (a -> b, w) means "a's
// local trust in b is w". The stationary distribution of the
// row-stochastic transition matrix is the EigenTrust score vector.
//
// A pre-trusted-agent teleport vector (the "p" term in the paper) is
// used both as the initial distribution and as the uniform restart
// component during power iteration. Restart mass alpha defaults to
// 0.15 following the paper's recommendation; pre-trust defaults to
// uniform over all agents when no seeds are provided, which is the
// correct fallback for deployments without an out-of-band seed set.
//
// This file has no persistence or network concerns. Edge aggregation,
// Postgres reads/writes, and cold-start cap enforcement happen at
// higher layers (see internal/store/reputation.go,
// internal/reputation/reputation.go, and internal/analysis/text.go).

// Edge is a single directed trust edge with non-negative weight.
type Edge struct {
	From   string
	To     string
	Weight float64
}

// EigenTrustConfig controls power iteration. Zero values are replaced
// with the paper-recommended defaults.
type EigenTrustConfig struct {
	// Iterations caps the power-iteration loop. 50 is plenty for the
	// graph sizes we care about; convergence is typically < 20.
	Iterations int
	// Epsilon is the L1 convergence threshold. When ||x_{k+1} - x_k||_1
	// falls below Epsilon, iteration stops early.
	Epsilon float64
	// Alpha is the restart mass (probability of teleporting to the
	// pre-trust vector each step). Paper suggests 0.15.
	Alpha float64
	// PreTrust is the teleport distribution. If nil or empty, a uniform
	// distribution over all vertices is used.
	PreTrust map[string]float64
}

func (c *EigenTrustConfig) ensureDefaults() {
	if c.Iterations <= 0 {
		c.Iterations = 50
	}
	if c.Epsilon <= 0 {
		c.Epsilon = 1e-6
	}
	if c.Alpha <= 0 || c.Alpha >= 1 {
		c.Alpha = 0.15
	}
}

// EigenTrust computes trust scores over the given edges. The returned
// map contains a score in [0, 1] for every vertex appearing in edges
// or in the optional agents set. Scores sum to 1.
//
// Vertices with no outgoing edges (sinks) are treated per the paper:
// their mass is redistributed to the pre-trust vector. This prevents
// the "leaked mass" pathology where power iteration converges to the
// zero vector when sinks absorb all trust.
//
// The agents slice is used to ensure isolated vertices still appear
// in the output with their pre-trust share. Pass nil if you only care
// about vertices that participate in at least one edge.
func EigenTrust(edges []Edge, agents []string, cfg EigenTrustConfig) map[string]float64 {
	cfg.ensureDefaults()

	// Collect vertex set: union of edges' endpoints and the agents slice.
	vertices := map[string]struct{}{}
	for _, e := range edges {
		vertices[e.From] = struct{}{}
		vertices[e.To] = struct{}{}
	}
	for _, a := range agents {
		vertices[a] = struct{}{}
	}
	if len(vertices) == 0 {
		return map[string]float64{}
	}

	// Pre-trust vector: uniform fallback when none supplied or when
	// supplied weights sum to zero.
	preTrust := make(map[string]float64, len(vertices))
	preTrustSum := 0.0
	for v := range vertices {
		w := cfg.PreTrust[v]
		if w < 0 {
			w = 0
		}
		preTrust[v] = w
		preTrustSum += w
	}
	if preTrustSum <= 0 {
		uniform := 1.0 / float64(len(vertices))
		for v := range vertices {
			preTrust[v] = uniform
		}
	} else {
		for v := range vertices {
			preTrust[v] /= preTrustSum
		}
	}

	// Row-stochastic adjacency in sparse form, keyed by source.
	// For each source, we normalize its outgoing weights so they sum
	// to 1. Sources with no outgoing edges (or all-zero weights) are
	// recorded in sinks for teleport redistribution.
	type outEdge struct {
		to string
		w  float64
	}
	out := map[string][]outEdge{}
	rowSum := map[string]float64{}
	for _, e := range edges {
		if e.Weight <= 0 {
			continue
		}
		out[e.From] = append(out[e.From], outEdge{e.To, e.Weight})
		rowSum[e.From] += e.Weight
	}
	for src, edges := range out {
		s := rowSum[src]
		if s <= 0 {
			continue
		}
		for i := range edges {
			edges[i].w /= s
		}
		out[src] = edges
	}

	// Power iteration. Start at the pre-trust vector.
	x := make(map[string]float64, len(vertices))
	for v, w := range preTrust {
		x[v] = w
	}

	next := make(map[string]float64, len(vertices))
	for iter := 0; iter < cfg.Iterations; iter++ {
		for v := range vertices {
			next[v] = 0
		}
		// Distribute sink mass to the pre-trust vector. This is the
		// "leaked rank" correction — without it, sinks swallow trust
		// and the total probability mass shrinks each iteration.
		leaked := 0.0
		for v := range vertices {
			if len(out[v]) == 0 {
				leaked += x[v]
			}
		}
		// Transition step: C^T * x, then blend with teleport.
		// C^T means: for each source u, push x[u] * C[u][v] to v.
		for u := range vertices {
			flow := x[u]
			if flow <= 0 {
				continue
			}
			for _, e := range out[u] {
				next[e.to] += flow * e.w
			}
		}
		// Teleport blend: (1 - alpha) * next + alpha * preTrust,
		// plus leaked mass redistributed through pre-trust.
		oneMinusA := 1.0 - cfg.Alpha
		diff := 0.0
		for v := range vertices {
			nv := oneMinusA*next[v] + cfg.Alpha*preTrust[v] + oneMinusA*leaked*preTrust[v]
			d := nv - x[v]
			if d < 0 {
				d = -d
			}
			diff += d
			x[v] = nv
		}
		if diff < cfg.Epsilon {
			break
		}
	}

	return x
}
