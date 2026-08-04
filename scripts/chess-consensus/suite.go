package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"

	"github.com/notnil/chess"
)

// A Suite is a fixed set of positions every arm decides on. Running a game
// instead makes the position stream endogenous — a group that plays differently
// faces different positions — so nothing compares across runs. A suite holds
// the positions constant, lets n be whatever the budget allows, and spends no
// compute on positions where every candidate move is equally fine.
type Suite struct {
	Seed        uint64          `json:"seed"`
	RefDepth    int             `json:"reference_depth"`
	MultiPV     int             `json:"multipv"`
	MinEdge     int             `json:"min_edge"`     // rank 1 must beat rank 2 by this much
	MinGap      int             `json:"min_gap"`      // rank 1 must beat the shared pool by this much
	MaxGap      int             `json:"max_gap"`      // ...but not by more than this
	SharedStart int             `json:"shared_start"` // 1-indexed rank where the shared pool begins
	Positions   []SuitePosition `json:"positions"`
}

// SuitePosition is one decision point with its reference ranking already
// computed, so every arm scores against identical ground truth.
type SuitePosition struct {
	ID         string `json:"id"`
	FEN        string `json:"fen"`
	Side       string `json:"side"`
	Candidates []Line `json:"candidates"` // reference MultiPV ranking, best first
	Edge       int    `json:"edge"`       // rank 1 minus rank 2, centipawns
	Gap        int    `json:"gap"`        // rank 1 minus the best shared move — the hidden-profile deficit
}

// SuiteOpts configures suite generation.
type SuiteOpts struct {
	Count         int
	Seed          uint64
	RefDepth      int
	MultiPV       int
	MinEdge       int
	MinGap        int
	MaxGap        int
	SharedStart   int
	MinCandidates int
	OpeningPlies  int // plies to skip at the start of each sampling game
	MaxPlies      int // plies to play before starting a fresh sampling game
}

// GenerateSuite walks engine-played games and keeps the positions where the
// hidden-profile setup is actually meaningful: one uniquely best move, and a
// real quality gap down to the moves the group will collectively share.
//
// Positions containing a forced mate are skipped. Mate scores are not on the
// same scale as centipawns, and a suite whose difficulty is dominated by "did
// you see the mate" measures tactics rather than information pooling.
func GenerateSuite(eng *Engine, opts SuiteOpts) (*Suite, error) {
	if opts.MinCandidates < opts.SharedStart+2 {
		opts.MinCandidates = opts.SharedStart + 2
	}
	suite := &Suite{
		Seed:        opts.Seed,
		RefDepth:    opts.RefDepth,
		MultiPV:     opts.MultiPV,
		MinEdge:     opts.MinEdge,
		MinGap:      opts.MinGap,
		MaxGap:      opts.MaxGap,
		SharedStart: opts.SharedStart,
	}
	rng := rand.New(rand.NewPCG(opts.Seed, 0x5eed))

	for game := 0; len(suite.Positions) < opts.Count; game++ {
		if game > opts.Count*4+50 {
			return nil, fmt.Errorf("gave up after %d sampling games with %d/%d positions — filters may be too strict",
				game, len(suite.Positions), opts.Count)
		}
		if err := eng.NewGame(); err != nil {
			return nil, err
		}
		g := chess.NewGame()
		for ply := 0; ply < opts.MaxPlies && len(suite.Positions) < opts.Count; ply++ {
			if g.Outcome() != chess.NoOutcome {
				break
			}
			pos := g.Position()
			lines, err := eng.Analyze(pos.String(), SearchOpts{Depth: opts.RefDepth, MultiPV: opts.MultiPV})
			if err != nil {
				return nil, err
			}
			if ply >= opts.OpeningPlies {
				if sp, ok := qualify(pos, lines, opts); ok {
					sp.ID = fmt.Sprintf("g%02d-p%02d", game, ply)
					suite.Positions = append(suite.Positions, sp)
				}
			}
			// Wander: play a random move from the top three so successive
			// sampling games explore different structures.
			pick := lines[0]
			if len(lines) > 1 {
				pick = lines[min(rng.IntN(3), len(lines)-1)]
			}
			move, err := decodeMove(pos, pick.UCI)
			if err != nil {
				break
			}
			if err := g.Move(move); err != nil {
				break
			}
		}
	}
	return suite, nil
}

// qualify decides whether a position makes a usable hidden-profile item.
func qualify(pos *chess.Position, lines []Line, opts SuiteOpts) (SuitePosition, bool) {
	if len(lines) < opts.MinCandidates {
		return SuitePosition{}, false
	}
	for _, l := range lines {
		if l.Eval.Mate != 0 {
			return SuitePosition{}, false
		}
	}
	edge := lines[0].Eval.CP - lines[1].Eval.CP
	if edge < opts.MinEdge {
		return SuitePosition{}, false
	}
	sharedIdx := opts.SharedStart - 1
	if sharedIdx >= len(lines) {
		return SuitePosition{}, false
	}
	// The gap is the hidden-profile deficit: what the group forfeits by failing
	// to pool information. Bounding it above matters as much as below — a
	// handful of positions where the shared pool is catastrophic would dominate
	// every average and turn the experiment into a report on those few items.
	gap := lines[0].Eval.CP - lines[sharedIdx].Eval.CP
	if gap < opts.MinGap {
		return SuitePosition{}, false
	}
	if opts.MaxGap > 0 && gap > opts.MaxGap {
		return SuitePosition{}, false
	}
	return SuitePosition{
		FEN:        pos.String(),
		Side:       sideName(pos.Turn()),
		Candidates: lines,
		Edge:       edge,
		Gap:        gap,
	}, true
}

func LoadSuite(path string) (*Suite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading suite: %w", err)
	}
	var s Suite
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing suite: %w", err)
	}
	if len(s.Positions) == 0 {
		return nil, fmt.Errorf("suite %s contains no positions", path)
	}
	return &s, nil
}

func (s *Suite) Save(path string) error {
	return writeJSON(path, s)
}
