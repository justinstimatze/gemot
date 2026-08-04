package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/notnil/chess"
)

// Personality is an agent's chess temperament. It never changes the engine's
// evaluation — it changes what the agent *wants* on top of it. Every weight is
// denominated in centipawns, so a Check weight of 45 literally means "I will
// advocate a move up to 0.45 pawns worse if it gives check".
//
// This is the experimental lever: each agent carries a known, quantified bias,
// and the question is whether three-way deliberation cancels those biases out
// or compounds them.
type Personality struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Style       string             `json:"style"`       // prose given to the LLM when --llm=on
	Interests   string             `json:"interests"`   // gemot `interests` field
	Reservation string             `json:"reservation"` // gemot `reservation` field — the hard constraint
	DepthDelta  int                `json:"depth_delta"` // added to the base search depth
	MultiPV     int                `json:"multipv"`     // candidate moves this agent considers
	Weights     map[string]float64 `json:"weights"`     // feature name -> centipawns per unit
}

// Feature names. Each is computed from the move and the position it produces —
// no engine involvement, so an agent's bias is fully independent of its search.
const (
	featCheck         = "check"          // move gives check (0/1)
	featCapture       = "capture"        // value of the captured piece in pawns (0-9)
	featPromotion     = "promotion"      // move promotes (0/1)
	featMaterialLoss  = "material_loss"  // pawns given up immediately, before recapture (>=0)
	featKingProximity = "king_proximity" // 7 minus Chebyshev distance from destination to enemy king (0-7)
	featKingShield    = "king_shield"    // own pawns shielding own king after the move (0-3)
	featForcingPV     = "forcing_pv"     // checks + captures in the first 4 plies of the PV (0-4)
	featEnPrise       = "en_prise"       // moved piece can be taken by a cheaper piece (0/1)
	featDevelopment   = "development"    // minor piece leaves its home square (0/1)
	featCastle        = "castle"         // move is a castle (0/1)
)

// defaultPersonalities are the three temperaments described in the task: an
// attacker, a defender, and a calculator. They search to the same depth by
// default (DepthDelta 0), so any disagreement comes purely from taste — which
// keeps the "does deliberation cancel bias" question clean. Pass --asymmetric
// to give each a different search budget instead.
func defaultPersonalities() []Personality {
	return []Personality{
		{
			ID:          "aggressor",
			Name:        "Aggressor",
			Style:       "You play for the initiative. You believe attacking chances and king pressure are worth more than the engine's static evaluation admits, and that a defended position is a lost tempo. You are willing to give up material for an attack.",
			Interests:   "initiative, attacking chances, pressure on the enemy king, tempo",
			Reservation: "I will not support a move that lets the opponent force mate or wins a full piece for nothing.",
			DepthDelta:  0,
			MultiPV:     6,
			Weights: map[string]float64{
				featCheck:         45,
				featCapture:       12,
				featForcingPV:     28,
				featKingProximity: 11,
				featMaterialLoss:  25, // positive: rewards sacrifices
				featEnPrise:       -15,
				featKingShield:    -4,
			},
		},
		{
			ID:          "defender",
			Name:        "Defender",
			Style:       "You play for structural soundness. You believe most games are lost rather than won, and that unforced complications are how strong positions evaporate. You want your king safe, your pieces defended, and your pawn structure intact.",
			Interests:   "king safety, pawn structure, material security, avoiding unforced complications",
			Reservation: "I will not support a move that leaves a piece hanging to a cheaper attacker or strips my own king's pawn cover.",
			DepthDelta:  0,
			MultiPV:     6,
			Weights: map[string]float64{
				featKingShield:   28,
				featEnPrise:      -75,
				featMaterialLoss: -60,
				featCastle:       35,
				featForcingPV:    -14,
				featCapture:      6,
				featDevelopment:  10,
			},
		},
		{
			ID:          "tactician",
			Name:        "Tactician",
			Style:       "You play concrete variations. You trust calculation over principle: if the line works it works, and if it doesn't, no amount of positional justification saves it. You prefer forcing moves because they are the ones you can verify to the end.",
			Interests:   "concrete calculation, forcing sequences, verified variations, tactical accuracy",
			Reservation: "I will not support a move whose refutation I can see in the principal variation.",
			DepthDelta:  0,
			MultiPV:     6,
			Weights: map[string]float64{
				featForcingPV: 16,
				featCheck:     10,
				featEnPrise:   -30,
				featPromotion: 20,
			},
		},
	}
}

// asymmetricDepths gives each personality a different search budget, modelling
// agents of genuinely unequal strength rather than merely unequal taste.
var asymmetricDepths = map[string]int{
	"aggressor": -4,
	"defender":  -1,
	"tactician": +3,
}

func loadPersonalities(path string, asymmetric bool) ([]Personality, error) {
	ps := defaultPersonalities()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading personalities: %w", err)
		}
		ps = nil
		if err := json.Unmarshal(data, &ps); err != nil {
			return nil, fmt.Errorf("parsing personalities: %w", err)
		}
		if len(ps) < 2 {
			return nil, fmt.Errorf("need at least 2 personalities, got %d", len(ps))
		}
	}
	for i := range ps {
		if ps[i].MultiPV < 2 {
			ps[i].MultiPV = 6
		}
		if asymmetric {
			if d, ok := asymmetricDepths[ps[i].ID]; ok {
				ps[i].DepthDelta = d
			}
		}
	}
	return ps, nil
}

// features scores one candidate move in the position it is played from.
// pv is the engine's principal variation for the move, in UCI notation.
func features(pos *chess.Position, move *chess.Move, pv []string) map[string]float64 {
	f := map[string]float64{}
	board := pos.Board()
	mover := pos.Turn()
	after := pos.Update(move)

	if move.HasTag(chess.Check) {
		f[featCheck] = 1
	}
	if move.HasTag(chess.KingSideCastle) || move.HasTag(chess.QueenSideCastle) {
		f[featCastle] = 1
	}
	if move.Promo() != chess.NoPieceType {
		f[featPromotion] = 1
	}

	captured := 0.0
	if move.HasTag(chess.EnPassant) {
		captured = 1
	} else if move.HasTag(chess.Capture) {
		captured = pieceValue(board.Piece(move.S2()).Type())
	}
	f[featCapture] = captured

	movedValue := pieceValue(board.Piece(move.S1()).Type())

	// en prise: after the move, can the opponent capture the piece we just moved
	// with something cheaper? This is a one-ply proxy for a static exchange
	// evaluation — it catches outright hangs, not deep tactics.
	cheapestAttacker := -1.0
	for _, reply := range after.ValidMoves() {
		if reply.S2() != move.S2() || !reply.HasTag(chess.Capture) {
			continue
		}
		v := pieceValue(after.Board().Piece(reply.S1()).Type())
		// A king capture is only legal when the square is undefended, so the
		// piece is free — scoring the king at its nominal value would hide
		// exactly the blunders this feature exists to catch.
		if v == kingValue {
			v = 0
		}
		if cheapestAttacker < 0 || v < cheapestAttacker {
			cheapestAttacker = v
		}
	}
	if cheapestAttacker >= 0 && cheapestAttacker < movedValue {
		f[featEnPrise] = 1
		// Material we stand to lose beyond what we just won.
		if loss := movedValue - captured; loss > 0 {
			f[featMaterialLoss] = loss
		}
	}

	// Proximity to the enemy king, inverted so that closer scores higher.
	if enemyKing, ok := kingSquare(after.Board(), mover.Other()); ok {
		f[featKingProximity] = float64(7 - chebyshev(move.S2(), enemyKing))
	}

	// Pawn cover in front of our own king, on its file and both neighbours.
	if ownKing, ok := kingSquare(after.Board(), mover); ok {
		f[featKingShield] = float64(pawnShield(after.Board(), ownKing, mover))
	}

	// Forcing content of the engine's principal variation.
	forcing := 0.0
	line := pos
	for i, uciMove := range pv {
		if i >= 4 {
			break
		}
		m, err := decodeMove(line, uciMove)
		if err != nil {
			break
		}
		if m.HasTag(chess.Check) || m.HasTag(chess.Capture) {
			forcing++
		}
		line = line.Update(m)
	}
	f[featForcingPV] = forcing

	// Development: a minor piece leaving its starting rank in the opening.
	movedType := board.Piece(move.S1()).Type()
	if movedType == chess.Knight || movedType == chess.Bishop {
		homeRank := chess.Rank1
		if mover == chess.Black {
			homeRank = chess.Rank8
		}
		if move.S1().Rank() == homeRank {
			f[featDevelopment] = 1
		}
	}
	return f
}

// bias applies the personality's weights to a feature vector, returning the
// centipawn adjustment this agent makes to the engine's evaluation.
func (p Personality) bias(f map[string]float64) int {
	total := 0.0
	for name, weight := range p.Weights {
		total += weight * f[name]
	}
	return int(total)
}

// biasBreakdown explains a bias score, largest contribution first. It is what
// the agent cites in its argument, so its reasoning is auditable.
func (p Personality) biasBreakdown(f map[string]float64) []string {
	type item struct {
		name string
		cp   float64
	}
	var items []item
	for name, weight := range p.Weights {
		if cp := weight * f[name]; cp != 0 {
			items = append(items, item{name, cp})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return abs(items[i].cp) > abs(items[j].cp)
	})
	var out []string
	for _, it := range items {
		out = append(out, fmt.Sprintf("%s %+.0fcp", strings.ReplaceAll(it.name, "_", " "), it.cp))
	}
	return out
}

// kingValue is a sentinel rather than a real price — the king is never traded.
const kingValue = 100.0

func pieceValue(t chess.PieceType) float64 {
	switch t {
	case chess.Pawn:
		return 1
	case chess.Knight, chess.Bishop:
		return 3
	case chess.Rook:
		return 5
	case chess.Queen:
		return 9
	case chess.King:
		return kingValue
	default:
		return 0
	}
}

func kingSquare(b *chess.Board, c chess.Color) (chess.Square, bool) {
	king := chess.WhiteKing
	if c == chess.Black {
		king = chess.BlackKing
	}
	for sq := chess.A1; sq <= chess.H8; sq++ {
		if b.Piece(sq) == king {
			return sq, true
		}
	}
	return chess.NoSquare, false
}

func chebyshev(a, b chess.Square) int {
	df := abs(float64(int(a.File()) - int(b.File())))
	dr := abs(float64(int(a.Rank()) - int(b.Rank())))
	if df > dr {
		return int(df)
	}
	return int(dr)
}

// pawnShield counts friendly pawns on the king's file and its neighbours, on
// the two ranks in front of the king.
func pawnShield(b *chess.Board, king chess.Square, c chess.Color) int {
	pawn := chess.WhitePawn
	dir := 1
	if c == chess.Black {
		pawn = chess.BlackPawn
		dir = -1
	}
	kingFile := int(king.File())
	kingRank := int(king.Rank())
	count := 0
	for df := -1; df <= 1; df++ {
		for dr := 1; dr <= 2; dr++ {
			file := kingFile + df
			rank := kingRank + dir*dr
			if file < 0 || file > 7 || rank < 0 || rank > 7 {
				continue
			}
			if b.Piece(chess.NewSquare(chess.File(file), chess.Rank(rank))) == pawn {
				count++
			}
		}
	}
	return count
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
