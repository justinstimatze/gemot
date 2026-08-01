package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/notnil/chess"
)

// Candidate is one move as a single agent sees it: the engine's verdict, the
// agent's taste, and the sum of the two.
type Candidate struct {
	UCI      string             `json:"uci"`
	SAN      string             `json:"san"`
	Eval     Eval               `json:"eval"`    // engine score at this agent's depth
	Bias     int                `json:"bias"`    // centipawn adjustment from personality
	Utility  int                `json:"utility"` // Eval.Centipawns() + Bias — what the agent maximises
	Depth    int                `json:"depth"`
	PV       []string           `json:"pv"` // principal variation, SAN
	Features map[string]float64 `json:"features"`
	Why      []string           `json:"why"` // bias breakdown, largest first
}

// Agent is one deliberating chess player: a personality plus its own engine view.
type Agent struct {
	Personality Personality
	Side        chess.Color

	engine    *Engine
	baseDepth int
	llm       *LLM // nil when --llm=off
}

// Survey returns the agent's ranked candidate moves for a position. The engine
// supplies the evaluations; the personality reorders them.
func (a *Agent) Survey(pos *chess.Position) ([]Candidate, error) {
	lines, err := a.engine.Analyze(pos.String(), SearchOpts{
		Depth:   a.depth(),
		MultiPV: a.Personality.MultiPV,
	})
	if err != nil {
		return nil, err
	}
	return a.rank(pos, lines), nil
}

// Assess evaluates one specific move, even if it never appeared in the agent's
// own shortlist. This is how an agent forms an opinion about a peer's proposal
// instead of dismissing anything it did not think of.
func (a *Agent) Assess(pos *chess.Position, uciMove string) (Candidate, error) {
	lines, err := a.engine.Analyze(pos.String(), SearchOpts{
		Depth:       a.depth(),
		MultiPV:     1,
		SearchMoves: []string{uciMove},
	})
	if err != nil {
		return Candidate{}, err
	}
	ranked := a.rank(pos, lines)
	if len(ranked) == 0 {
		return Candidate{}, fmt.Errorf("no evaluation for %s", uciMove)
	}
	return ranked[0], nil
}

func (a *Agent) depth() int {
	d := a.baseDepth + a.Personality.DepthDelta
	if d < 4 {
		d = 4
	}
	return d
}

// rank converts engine lines into candidates scored by this agent's taste.
func (a *Agent) rank(pos *chess.Position, lines []Line) []Candidate {
	var out []Candidate
	for _, l := range lines {
		move, err := decodeMove(pos, l.UCI)
		if err != nil {
			continue
		}
		f := features(pos, move, l.PV)
		bias := a.Personality.bias(f)
		out = append(out, Candidate{
			UCI:      l.UCI,
			SAN:      chess.AlgebraicNotation{}.Encode(pos, move),
			Eval:     l.Eval,
			Bias:     bias,
			Utility:  l.Eval.Centipawns() + bias,
			Depth:    l.Depth,
			PV:       pvToSAN(pos, l.PV, 6),
			Features: f,
			Why:      a.Personality.biasBreakdown(f),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Utility > out[j].Utility })
	return out
}

// Proposal is what an agent brings to the deliberation.
type Proposal struct {
	AgentID    string    `json:"agent_id"`
	Move       Candidate `json:"move"`
	Argument   string    `json:"argument"`
	Conviction float64   `json:"conviction"` // 0-1: how far ahead its pick is of its own runner-up
	PositionID string    `json:"position_id,omitempty"`
}

// Propose picks the agent's preferred move and writes its case for it.
func (a *Agent) Propose(pos *chess.Position, shortlist []Candidate, moveLabel string) (Proposal, error) {
	if len(shortlist) == 0 {
		return Proposal{}, fmt.Errorf("%s has no candidates", a.Personality.ID)
	}
	best := shortlist[0]
	conviction := 0.5
	if len(shortlist) > 1 {
		margin := best.Utility - shortlist[1].Utility
		conviction = clamp01(float64(margin) / 150.0)
	}

	argument := a.heuristicArgument(best, shortlist, moveLabel)
	if a.llm != nil {
		if text, err := a.llm.Argue(a.Personality, moveLabel, pos.String(), shortlist); err == nil && text != "" {
			argument = text
		} else if err != nil {
			fmt.Printf("  [llm] %s argue failed, using heuristic: %v\n", a.Personality.ID, err)
		}
	}
	return Proposal{
		AgentID:    a.Personality.ID,
		Move:       best,
		Argument:   argument,
		Conviction: conviction,
	}, nil
}

// heuristicArgument states the agent's case from engine data alone. It is what
// runs with --llm=off, and it keeps the offline mode fully grounded: every
// number in the text came from a real search.
func (a *Agent) heuristicArgument(best Candidate, shortlist []Candidate, moveLabel string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "I propose %s at %s.\n\n", best.SAN, moveLabel)
	fmt.Fprintf(&b, "Engine evaluation after %s: %s at depth %d.\n", best.SAN, best.Eval, best.Depth)
	if len(best.PV) > 0 {
		fmt.Fprintf(&b, "Principal variation: %s\n", strings.Join(best.PV, " "))
	}
	if len(best.Why) > 0 {
		fmt.Fprintf(&b, "\nWhat I weight beyond the raw evaluation: %s (net %+dcp by my judgement).\n",
			strings.Join(best.Why, ", "), best.Bias)
	} else {
		fmt.Fprintf(&b, "\nNothing in this move triggers my usual preferences — I back it on the evaluation alone.\n")
	}
	if len(shortlist) > 1 {
		alt := shortlist[1]
		fmt.Fprintf(&b, "\nMy runner-up is %s (%s, %+dcp of my preference). I rate %s ahead of it by %dcp overall",
			alt.SAN, alt.Eval, alt.Bias, best.SAN, best.Utility-alt.Utility)
		if diff := best.Eval.Centipawns() - alt.Eval.Centipawns(); diff < 0 {
			fmt.Fprintf(&b, " — note that the engine actually prefers %s by %dcp, and I am overriding that on %s grounds",
				alt.SAN, -diff, a.Personality.ID)
		}
		b.WriteString(".\n")
	}
	fmt.Fprintf(&b, "\nMy standing constraint: %s", a.Personality.Reservation)
	return b.String()
}

// voteOn returns a gemot vote value (-2..2) for a peer's move, derived from how
// much utility this agent gives up by playing it instead of its own choice.
func (a *Agent) voteOn(own, peer Candidate) (int, string) {
	delta := peer.Utility - own.Utility
	var value int
	var qualifier string
	switch {
	case delta >= -20:
		value, qualifier = 2, "as good as my own choice"
	case delta >= -60:
		value, qualifier = 1, "slightly worse than my choice but acceptable"
	case delta >= -120:
		value, qualifier = 0, "a real concession, though not unsound"
	case delta >= -250:
		value, qualifier = -1, "gives up meaningful ground"
	default:
		value, qualifier = -2, "unacceptable by my standards"
	}
	// A hard constraint overrides preference: an agent never endorses a move
	// that violates its own stated reservation.
	if a.violatesReservation(peer) && value > -1 {
		value, qualifier = -1, "violates my stated reservation"
	}
	caveat := fmt.Sprintf("%s: engine %s, my adjustment %+dcp, %dcp against my own pick (%s)",
		peer.SAN, peer.Eval, peer.Bias, delta, qualifier)
	return value, caveat
}

// violatesReservation encodes each personality's hard constraint as a check on
// the feature vector, so a reservation is enforced rather than merely declared.
func (a *Agent) violatesReservation(c Candidate) bool {
	switch a.Personality.ID {
	case "defender":
		return c.Features[featEnPrise] > 0 || c.Features[featMaterialLoss] >= 3
	case "aggressor":
		return c.Eval.Mate < 0 || c.Features[featMaterialLoss] >= 5
	case "tactician":
		return c.Eval.Mate < 0
	default:
		return c.Eval.Mate < 0
	}
}

// endorsementValue is what a unanimous peer endorsement is worth to an agent,
// in the same centipawn currency as its own preferences. Setting it at 150cp
// means a fully-backed move can overcome about a pawn and a half of personal
// taste — enough to move an agent off a preference, not enough to override a
// concrete refutation.
const endorsementValue = 150.0

// Reconsider is the offline persuasion step: an agent re-ranks the moves on the
// table with peer support priced in. A move nobody backs is judged on the
// agent's own terms; a move the group stands behind gains up to
// strength × endorsementValue centipawns of pull.
//
// strength 0 means nobody is ever persuaded. With --llm=full this is replaced
// by a real re-vote from the model.
func (a *Agent) Reconsider(own Candidate, options map[string]Candidate, backing map[string]float64, strength float64) (string, int) {
	bestMove, bestScore := "", 0.0
	for uciMove, c := range options {
		score := float64(c.Utility) + strength*endorsementValue*clamp01(backing[uciMove])
		// Ties resolve to the agent's own move, then lexicographically, so the
		// step is deterministic and never churns for churn's sake.
		better := bestMove == "" || score > bestScore
		if !better && score == bestScore {
			better = uciMove == own.UCI || (bestMove != own.UCI && uciMove < bestMove)
		}
		if better {
			bestMove, bestScore = uciMove, score
		}
	}
	return bestMove, int(bestScore)
}

// decodeMove turns a UCI move from the engine into a fully tagged chess.Move.
//
// chess.UCINotation.Decode sets castle, capture, and en-passant tags but never
// Check — only move generation does. Since the aggressor and tactician weight
// checks heavily, decoding through the legal move list is not a nicety: without
// it every check silently scores as a quiet move.
func decodeMove(pos *chess.Position, uciMove string) (*chess.Move, error) {
	parsed, err := chess.UCINotation{}.Decode(pos, uciMove)
	if err != nil {
		return nil, err
	}
	for _, legal := range pos.ValidMoves() {
		if legal.S1() == parsed.S1() && legal.S2() == parsed.S2() && legal.Promo() == parsed.Promo() {
			return legal, nil
		}
	}
	return nil, fmt.Errorf("%s is not legal in %s", uciMove, pos.String())
}

func pvToSAN(pos *chess.Position, pv []string, limit int) []string {
	var out []string
	cur := pos
	for i, uciMove := range pv {
		if i >= limit {
			break
		}
		m, err := decodeMove(cur, uciMove)
		if err != nil {
			break
		}
		out = append(out, chess.AlgebraicNotation{}.Encode(cur, m))
		cur = cur.Update(m)
	}
	return out
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
