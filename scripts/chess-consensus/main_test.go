package main

import (
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/notnil/chess"
)

func mustPosition(t *testing.T, fen string) *chess.Position {
	t.Helper()
	pos := &chess.Position{}
	if err := pos.UnmarshalText([]byte(fen)); err != nil {
		t.Fatalf("bad FEN %q: %v", fen, err)
	}
	return pos
}

func mustMove(t *testing.T, pos *chess.Position, uciMove string) *chess.Move {
	t.Helper()
	m, err := decodeMove(pos, uciMove)
	if err != nil {
		t.Fatalf("decoding %q: %v", uciMove, err)
	}
	return m
}

// scholarsMate is the position before Qxf7#. The f7 pawn is defended by the
// bishop on c4, which makes it a useful negative case for en prise.
const scholarsMate = "r1bqkbnr/pppp1ppp/2n5/4p2Q/2B1P3/8/PPPP1PPP/RNB1K1NR w KQkq - 4 4"

// hangingQueen: 1.Qxc5 wins a pawn but drops the queen to Bxc5.
const hangingQueen = "4k3/8/1b6/2p5/3Q4/8/8/4K3 w - - 0 1"

func TestParseInfo(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Line
		ok   bool
	}{
		{
			name: "multipv line",
			line: "info depth 20 seldepth 28 multipv 2 score cp -35 nodes 500 nps 1000 time 500 pv e7e5 g1f3",
			want: Line{Rank: 2, UCI: "e7e5", Eval: Eval{CP: -35}, Depth: 20, PV: []string{"e7e5", "g1f3"}},
			ok:   true,
		},
		{
			name: "mate score",
			line: "info depth 12 multipv 1 score mate 3 nodes 100 pv d1h5 g7g6",
			want: Line{Rank: 1, UCI: "d1h5", Eval: Eval{Mate: 3}, Depth: 12, PV: []string{"d1h5", "g7g6"}},
			ok:   true,
		},
		{
			name: "single pv defaults to rank 1",
			line: "info depth 8 score cp 29 pv e2e4",
			want: Line{Rank: 1, UCI: "e2e4", Eval: Eval{CP: 29}, Depth: 8, PV: []string{"e2e4"}},
			ok:   true,
		},
		{
			name: "progress line without pv is skipped",
			line: "info depth 3 currmove e2e4 currmovenumber 1",
			ok:   false,
		},
		{
			name: "bound-only score is skipped",
			line: "info depth 14 multipv 1 score cp 40 lowerbound pv e2e4",
			ok:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseInfo(tc.line)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if got.Rank != tc.want.Rank || got.UCI != tc.want.UCI || got.Depth != tc.want.Depth {
				t.Errorf("rank/uci/depth = %d/%s/%d, want %d/%s/%d",
					got.Rank, got.UCI, got.Depth, tc.want.Rank, tc.want.UCI, tc.want.Depth)
			}
			if got.Eval != tc.want.Eval {
				t.Errorf("eval = %+v, want %+v", got.Eval, tc.want.Eval)
			}
			if len(got.PV) != len(tc.want.PV) {
				t.Fatalf("pv = %v, want %v", got.PV, tc.want.PV)
			}
			for i := range got.PV {
				if got.PV[i] != tc.want.PV[i] {
					t.Errorf("pv[%d] = %s, want %s", i, got.PV[i], tc.want.PV[i])
				}
			}
		})
	}
}

func TestEvalCentipawns(t *testing.T) {
	tests := []struct {
		eval Eval
		want int
	}{
		{Eval{CP: 45}, 45},
		{Eval{CP: -120}, -120},
		{Eval{Mate: 1}, mateScore - 100},
		{Eval{Mate: 5}, mateScore - 500},
		{Eval{Mate: -2}, -mateScore + 200},
	}
	for _, tc := range tests {
		if got := tc.eval.Centipawns(); got != tc.want {
			t.Errorf("%+v.Centipawns() = %d, want %d", tc.eval, got, tc.want)
		}
	}
	// A mate for us must always outrank any material advantage, and a mate
	// against us must always rank below any material deficit.
	if (Eval{Mate: 10}).Centipawns() <= (Eval{CP: 2000}).Centipawns() {
		t.Error("mate in 10 should outrank +20 pawns")
	}
	if (Eval{Mate: -10}).Centipawns() >= (Eval{CP: -2000}).Centipawns() {
		t.Error("mated in 10 should rank below -20 pawns")
	}
}

func TestFeaturesCheckAndCapture(t *testing.T) {
	pos := mustPosition(t, scholarsMate)
	f := features(pos, mustMove(t, pos, "h5f7"), nil)
	if f[featCheck] != 1 {
		t.Error("Qxf7+ should be flagged as a check")
	}
	if f[featCapture] != 1 {
		t.Errorf("Qxf7 captures a pawn, got capture value %v", f[featCapture])
	}
	// The queen lands next to the black king.
	if f[featKingProximity] < 6 {
		t.Errorf("f7 is adjacent to e8, expected high king proximity, got %v", f[featKingProximity])
	}
}

func TestFeaturesEnPrise(t *testing.T) {
	// Qxc5 grabs a pawn and hangs the queen to Bxc5.
	pos := mustPosition(t, hangingQueen)
	f := features(pos, mustMove(t, pos, "d4c5"), nil)
	if f[featEnPrise] != 1 {
		t.Error("the queen on c5 is capturable by a bishop and should be flagged en prise")
	}
	if f[featMaterialLoss] != 8 {
		t.Errorf("material_loss = %v, want 8 (a 9-point queen for a 1-point pawn)", f[featMaterialLoss])
	}

	// A piece defended by a cheaper attacker's target is not en prise: on
	// scholar's mate the f7 queen is covered by the bishop on c4, so the only
	// legal recapture would be by the king — which is illegal, hence no capture
	// at all.
	defended := mustPosition(t, scholarsMate)
	df := features(defended, mustMove(t, defended, "h5f7"), nil)
	if df[featEnPrise] != 0 {
		t.Error("Qxf7 is defended by Bc4 and must not be flagged en prise")
	}

	// A developing knight move to a safe square is not en prise.
	start := mustPosition(t, chess.StartingPosition().String())
	safe := features(start, mustMove(t, start, "g1f3"), nil)
	if safe[featEnPrise] != 0 {
		t.Error("Nf3 from the starting position is not en prise")
	}
	if safe[featDevelopment] != 1 {
		t.Error("Nf3 should count as development")
	}
}

func TestFeaturesEnPriseCatchesKingRecapture(t *testing.T) {
	// A piece that only the enemy king can take is free by definition — the
	// capture would be illegal if the square were defended. Scoring the king at
	// its nominal 100 points would hide exactly this blunder.
	pos := mustPosition(t, "4k3/8/8/8/3Q4/8/8/4K3 w - - 0 1")
	f := features(pos, mustMove(t, pos, "d4d7"), nil)
	if f[featEnPrise] != 1 {
		t.Error("Qd7 can be taken by the king for free and should be flagged en prise")
	}
}

func TestFeaturesCastleAndShield(t *testing.T) {
	pos := mustPosition(t, "rnbqk2r/pppp1ppp/5n2/2b1p3/2B1P3/5N2/PPPP1PPP/RNBQK2R w KQkq - 4 4")
	f := features(pos, mustMove(t, pos, "e1g1"), nil)
	if f[featCastle] != 1 {
		t.Error("O-O should be flagged as a castle")
	}
	if f[featKingShield] != 3 {
		t.Errorf("castled king on g1 has an intact f2/g2/h2 shield, got %v", f[featKingShield])
	}
}

func TestFeaturesForcingPV(t *testing.T) {
	pos := mustPosition(t, hangingQueen)
	// Qxc5 Bxc5 — two forcing plies, both captures.
	f := features(pos, mustMove(t, pos, "d4c5"), []string{"d4c5", "b6c5"})
	if f[featForcingPV] != 2 {
		t.Errorf("forcing_pv = %v, want 2", f[featForcingPV])
	}

	// A quiet line scores zero, and an illegal continuation stops the count
	// rather than corrupting it.
	quiet := features(pos, mustMove(t, pos, "d4d5"), []string{"d4d5", "e8d8"})
	if quiet[featForcingPV] != 0 {
		t.Errorf("quiet forcing_pv = %v, want 0", quiet[featForcingPV])
	}
}

func TestPersonalityBias(t *testing.T) {
	p := Personality{Weights: map[string]float64{featCheck: 45, featForcingPV: 25, featEnPrise: -75}}
	got := p.bias(map[string]float64{featCheck: 1, featForcingPV: 2, featEnPrise: 1})
	want := 45 + 50 - 75
	if got != want {
		t.Errorf("bias = %d, want %d", got, want)
	}

	// The breakdown must be ordered by magnitude so the agent cites its
	// dominant reason first.
	parts := p.biasBreakdown(map[string]float64{featCheck: 1, featForcingPV: 2, featEnPrise: 1})
	if len(parts) != 3 {
		t.Fatalf("breakdown has %d entries, want 3", len(parts))
	}
	if parts[0] != "en prise -75cp" {
		t.Errorf("largest contribution = %q, want the en-prise penalty", parts[0])
	}
}

func TestDefaultPersonalitiesDisagree(t *testing.T) {
	// The whole design depends on the three temperaments ranking the same move
	// differently. A sacrificial check should split them.
	f := map[string]float64{featCheck: 1, featForcingPV: 3, featEnPrise: 1, featMaterialLoss: 3, featKingProximity: 5}
	biases := map[string]int{}
	for _, p := range defaultPersonalities() {
		biases[p.ID] = p.bias(f)
	}
	if biases["aggressor"] <= biases["defender"] {
		t.Errorf("aggressor (%d) should like a sacrificial check more than defender (%d)",
			biases["aggressor"], biases["defender"])
	}
	if biases["defender"] >= 0 {
		t.Errorf("defender bias on a piece-hanging sacrifice should be negative, got %d", biases["defender"])
	}
}

func TestLoadPersonalitiesAsymmetric(t *testing.T) {
	ps, err := loadPersonalities("", true)
	if err != nil {
		t.Fatalf("loadPersonalities: %v", err)
	}
	byID := map[string]Personality{}
	for _, p := range ps {
		byID[p.ID] = p
	}
	if byID["tactician"].DepthDelta <= byID["aggressor"].DepthDelta {
		t.Error("asymmetric mode should give the tactician the deeper search")
	}

	symmetric, err := loadPersonalities("", false)
	if err != nil {
		t.Fatalf("loadPersonalities: %v", err)
	}
	for _, p := range symmetric {
		if p.DepthDelta != 0 {
			t.Errorf("%s has depth delta %d in symmetric mode, want 0", p.ID, p.DepthDelta)
		}
	}
}

func TestVoteOn(t *testing.T) {
	a := &Agent{Personality: Personality{ID: "tactician"}}
	own := Candidate{SAN: "Nf3", Utility: 100}

	tests := []struct {
		name string
		peer Candidate
		want int
	}{
		{"equivalent move", Candidate{SAN: "d4", Utility: 90}, 2},
		{"minor concession", Candidate{SAN: "d4", Utility: 55}, 1},
		{"real concession", Candidate{SAN: "d4", Utility: 0}, 0},
		{"meaningful loss", Candidate{SAN: "d4", Utility: -100}, -1},
		{"unacceptable", Candidate{SAN: "d4", Utility: -400}, -2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, caveat := a.voteOn(own, tc.peer)
			if got != tc.want {
				t.Errorf("vote = %d, want %d", got, tc.want)
			}
			if caveat == "" {
				t.Error("vote should always carry a caveat explaining itself")
			}
		})
	}
}

func TestVoteReservationOverridesPreference(t *testing.T) {
	defender := &Agent{Personality: Personality{ID: "defender"}}
	own := Candidate{SAN: "Be2", Utility: 100}
	// Utility-wise this is an easy +2, but it hangs a piece, which the
	// defender's stated reservation forbids it from endorsing.
	hanging := Candidate{SAN: "Bxf7", Utility: 95, Features: map[string]float64{featEnPrise: 1}}
	got, caveat := defender.voteOn(own, hanging)
	if got != -1 {
		t.Errorf("vote = %d, want -1 (reservation violated)", got)
	}
	if caveat == "" {
		t.Error("expected a caveat naming the reservation")
	}

	// A tactician has no such reservation and votes on the merits.
	tactician := &Agent{Personality: Personality{ID: "tactician"}}
	if got, _ := tactician.voteOn(own, hanging); got != 2 {
		t.Errorf("tactician vote = %d, want 2", got)
	}
}

func TestReconsiderRespectsBacking(t *testing.T) {
	a := &Agent{Personality: Personality{ID: "aggressor"}}
	// The agent's own move is worse by the engine but its bias carries it.
	own := Candidate{UCI: "d1h5", Eval: Eval{CP: -20}, Bias: 60, Utility: 40}
	rival := Candidate{UCI: "g1f3", Eval: Eval{CP: 20}, Bias: 0, Utility: 20}
	options := map[string]Candidate{own.UCI: own, rival.UCI: rival}

	// With no peer backing the agent keeps its own move.
	if pick, _ := a.Reconsider(own, options, map[string]float64{}, 0.6); pick != own.UCI {
		t.Errorf("unbacked rival: picked %s, want %s", pick, own.UCI)
	}

	// Full backing discounts the agent's own bias and the engine's view wins.
	backing := map[string]float64{rival.UCI: 1, own.UCI: 0}
	if pick, _ := a.Reconsider(own, options, backing, 1.0); pick != rival.UCI {
		t.Errorf("fully backed rival: picked %s, want %s", pick, rival.UCI)
	}

	// Persuasion strength 0 means nothing ever moves the agent.
	if pick, _ := a.Reconsider(own, options, backing, 0); pick != own.UCI {
		t.Errorf("strength 0: picked %s, want %s", pick, own.UCI)
	}
}

func TestBackingFromVotes(t *testing.T) {
	options := map[string]Candidate{
		"e2e4": {UCI: "e2e4", SAN: "e4"},
		"d2d4": {UCI: "d2d4", SAN: "d4"},
	}
	votes := []Vote{
		{AgentID: "defender", MoveUCI: "e2e4", Value: 2},
		{AgentID: "tactician", MoveUCI: "e2e4", Value: 2},
		{AgentID: "aggressor", MoveUCI: "d2d4", Value: -2},
		{AgentID: "tactician", MoveUCI: "d2d4", Value: -1},
	}
	backing := backingFrom(votes, 3, options, nil)
	if backing["e2e4"] != 1 {
		t.Errorf("e4 drew both peers' full support, backing = %v, want 1", backing["e2e4"])
	}
	if backing["d2d4"] != 0 {
		t.Errorf("negative support should floor at 0, got %v", backing["d2d4"])
	}
}

func TestBackingFromAnalysis(t *testing.T) {
	options := map[string]Candidate{
		"e2e4": {UCI: "e2e4", SAN: "e4"},
		"g1f3": {UCI: "g1f3", SAN: "Nf3"},
		"f1c4": {UCI: "f1c4", SAN: "Bxc4"},
	}
	analysis := &Analysis{
		Consensus:  []string{"All three agents accept that Nf3 keeps the position flexible."},
		Compromise: "Play Bxc4 and reassess.",
	}
	backing := backingFrom(nil, 3, options, analysis)
	if backing["g1f3"] < 0.5 {
		t.Errorf("Nf3 is named in the consensus, backing = %v, want >= 0.5", backing["g1f3"])
	}
	if backing["f1c4"] < 0.5 {
		t.Errorf("Bxc4 is named in the compromise, backing = %v, want >= 0.5", backing["f1c4"])
	}
	if backing["e2e4"] != 0 {
		t.Errorf("e4 is unmentioned, backing = %v, want 0", backing["e2e4"])
	}
}

func TestBackingSANMatchIsWordBounded(t *testing.T) {
	// "e4" must not match inside "Nxe4", or every analysis would appear to
	// endorse every pawn move.
	options := map[string]Candidate{"e2e4": {UCI: "e2e4", SAN: "e4"}}
	analysis := &Analysis{Consensus: []string{"The knight recapture Nxe4 is forced."}}
	if backing := backingFrom(nil, 3, options, analysis); backing["e2e4"] != 0 {
		t.Errorf("substring match leaked: backing = %v, want 0", backing["e2e4"])
	}
}

func TestTallyApproval(t *testing.T) {
	options := map[string]Candidate{
		"e2e4": {UCI: "e2e4"},
		"d2d4": {UCI: "d2d4"},
	}
	views := map[string]map[string]Candidate{
		"a": {"e2e4": {UCI: "e2e4", Eval: Eval{CP: 30}}, "d2d4": {UCI: "d2d4", Eval: Eval{CP: 20}}},
		"b": {"e2e4": {UCI: "e2e4", Eval: Eval{CP: 30}}, "d2d4": {UCI: "d2d4", Eval: Eval{CP: 20}}},
	}
	ballots := []Vote{
		{AgentID: "a", MoveUCI: "e2e4", Value: 2},
		{AgentID: "a", MoveUCI: "d2d4", Value: -1},
		{AgentID: "b", MoveUCI: "e2e4", Value: 1},
		{AgentID: "b", MoveUCI: "d2d4", Value: 2},
	}
	finalPick := map[string]string{"a": "e2e4", "b": "d2d4"}
	if got := tally(ballots, finalPick, views, options, "test"); got != "e2e4" {
		t.Errorf("tally = %s, want e2e4 (approval 3 vs 1)", got)
	}
}

func TestTallyTieBreaksOnEngineEval(t *testing.T) {
	options := map[string]Candidate{"e2e4": {UCI: "e2e4"}, "d2d4": {UCI: "d2d4"}}
	views := map[string]map[string]Candidate{
		"a": {"e2e4": {UCI: "e2e4", Eval: Eval{CP: 50}}, "d2d4": {UCI: "d2d4", Eval: Eval{CP: 10}}},
	}
	// Equal approval and equal first-choice counts: the unbiased engine
	// evaluation decides, not the agents' tastes.
	ballots := []Vote{
		{AgentID: "a", MoveUCI: "e2e4", Value: 1},
		{AgentID: "a", MoveUCI: "d2d4", Value: 1},
	}
	if got := tally(ballots, map[string]string{}, views, options, "test"); got != "e2e4" {
		t.Errorf("tally = %s, want e2e4 on the evaluation tie-break", got)
	}
}

func TestPlurality(t *testing.T) {
	options := map[string]Candidate{"e2e4": {UCI: "e2e4"}, "d2d4": {UCI: "d2d4"}}
	proposals := []Proposal{
		{AgentID: "a", Move: Candidate{UCI: "e2e4", Eval: Eval{CP: 10}}},
		{AgentID: "b", Move: Candidate{UCI: "d2d4", Eval: Eval{CP: 40}}},
		{AgentID: "c", Move: Candidate{UCI: "e2e4", Eval: Eval{CP: 10}}},
	}
	if got := plurality(proposals, options, "test"); got != "e2e4" {
		t.Errorf("plurality = %s, want e2e4 (2 proposers vs 1)", got)
	}

	// An even split falls to the coin, never to the evaluation — see
	// TestPluralityCannotLeakTheGem for why that matters. All the coin owes us
	// is a legal, reproducible answer.
	split := []Proposal{
		{AgentID: "a", Move: Candidate{UCI: "e2e4", Eval: Eval{CP: 10}}},
		{AgentID: "b", Move: Candidate{UCI: "d2d4", Eval: Eval{CP: 40}}},
	}
	got := plurality(split, options, "test")
	if _, ok := options[got]; !ok {
		t.Errorf("split plurality = %q, which is not on the table", got)
	}
	if again := plurality(split, options, "test"); again != got {
		t.Errorf("split plurality is not reproducible: %s then %s", got, again)
	}
}

// candidateLines builds a fake reference ranking, best first.
func candidateLines(evals ...int) []Line {
	lines := make([]Line, len(evals))
	for i, cp := range evals {
		lines[i] = Line{Rank: i + 1, UCI: fmt.Sprintf("m%02d", i+1), Eval: Eval{CP: cp}}
	}
	return lines
}

func TestPartitionDealsTheGemToExactlyOneAgent(t *testing.T) {
	lines := candidateLines(120, 90, 80, 40, 35, 30, 25, 20, 15, 10)
	agents := []string{"aggressor", "defender", "tactician"}
	sets, err := Partition("fen-1", lines, map[string]string{}, agents, DefaultHiddenConfig())
	if err != nil {
		t.Fatalf("Partition: %v", err)
	}

	if sets.GemUCI != "m01" {
		t.Errorf("gem = %s, want the top-ranked move m01", sets.GemUCI)
	}
	holders := 0
	for _, a := range agents {
		if sets.Sees(a, sets.GemUCI) {
			holders++
			if a != sets.GemHolder {
				t.Errorf("%s can see the gem but %s is the recorded holder", a, sets.GemHolder)
			}
		}
	}
	if holders != 1 {
		t.Fatalf("%d agents can see the gem, want exactly 1", holders)
	}

	// Every agent sees the whole shared pool.
	for _, a := range agents {
		for _, shared := range sets.Shared {
			if !sets.Sees(a, shared) {
				t.Errorf("%s cannot see shared move %s", a, shared)
			}
		}
	}

	// Ranks between the gem and the shared pool reach nobody, which is what
	// guarantees the shared pool is genuinely worse than the gem.
	if len(sets.Discarded) != 2 {
		t.Errorf("discarded %v, want ranks 2 and 3", sets.Discarded)
	}
	for _, a := range agents {
		for _, d := range sets.Discarded {
			if sets.Sees(a, d) {
				t.Errorf("%s can see discarded move %s", a, d)
			}
		}
	}
}

func TestPartitionIsStablePerPosition(t *testing.T) {
	// The same position must deal identically every time, or arms would not be
	// comparable — one arm could get an easier deal than another.
	lines := candidateLines(120, 90, 80, 40, 35, 30, 25, 20, 15, 10)
	agents := []string{"aggressor", "defender", "tactician"}
	first, err := Partition("fen-A", lines, map[string]string{}, agents, DefaultHiddenConfig())
	if err != nil {
		t.Fatalf("Partition: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := Partition("fen-A", lines, map[string]string{}, agents, DefaultHiddenConfig())
		if err != nil {
			t.Fatalf("Partition: %v", err)
		}
		if again.GemHolder != first.GemHolder {
			t.Fatalf("holder changed between deals: %s then %s", first.GemHolder, again.GemHolder)
		}
	}

	// Different positions must not all land on the same holder.
	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		d, err := Partition(fmt.Sprintf("fen-%d", i), lines, map[string]string{}, agents, DefaultHiddenConfig())
		if err != nil {
			t.Fatalf("Partition: %v", err)
		}
		seen[d.GemHolder] = true
	}
	if len(seen) != len(agents) {
		t.Errorf("gem holder only ever %v across 40 positions, want all of %v", sortedKeys(seen), agents)
	}
}

func TestPartitionRejectsThinCandidateLists(t *testing.T) {
	if _, err := Partition("f", candidateLines(50, 40, 30), map[string]string{}, []string{"a", "b"}, DefaultHiddenConfig()); err == nil {
		t.Error("expected an error when there are too few candidates to deal")
	}
	if _, err := Partition("f", candidateLines(50, 40, 30, 20, 10, 5, 1), map[string]string{}, []string{"a"}, DefaultHiddenConfig()); err == nil {
		t.Error("expected an error with a single agent — a hidden profile needs someone to hide from")
	}
}

// TestPluralityCannotLeakTheGem is a regression test for the subtlest bug in
// this design. Breaking plurality ties by engine evaluation handed the control
// the answer: under a hidden profile every agent proposes a different move, so
// every move has one proposer, and the gem holder's evaluation is always the
// highest. The control would have "found" information it never received.
func TestPluralityCannotLeakTheGem(t *testing.T) {
	gem := Candidate{UCI: "gem", Eval: Eval{CP: 200}}
	shared1 := Candidate{UCI: "sh1", Eval: Eval{CP: 40}}
	shared2 := Candidate{UCI: "sh2", Eval: Eval{CP: 35}}
	options := map[string]Candidate{"gem": gem, "sh1": shared1, "sh2": shared2}
	proposals := []Proposal{
		{AgentID: "holder", Move: gem},
		{AgentID: "b", Move: shared1},
		{AgentID: "c", Move: shared2},
	}

	// Across many distinct positions with the same three-way split, plurality
	// must land on the gem no more often than chance (1 in 3).
	gemWins := 0
	const trials = 300
	for i := 0; i < trials; i++ {
		if plurality(proposals, options, fmt.Sprintf("position-%d", i)) == "gem" {
			gemWins++
		}
	}
	rate := float64(gemWins) / trials
	if rate > 0.45 {
		t.Errorf("plurality picked the gem %.0f%% of the time — the tie-break is leaking move quality", rate*100)
	}
	if rate < 0.20 {
		t.Errorf("plurality picked the gem only %.0f%% of the time — the tie-break is not uniform", rate*100)
	}
}

func TestPluralityStillRespectsCounts(t *testing.T) {
	// A move two agents proposed must win outright, however the coin falls.
	options := map[string]Candidate{"gem": {UCI: "gem"}, "sh1": {UCI: "sh1"}}
	proposals := []Proposal{
		{AgentID: "holder", Move: Candidate{UCI: "gem", Eval: Eval{CP: 200}}},
		{AgentID: "b", Move: Candidate{UCI: "sh1"}},
		{AgentID: "c", Move: Candidate{UCI: "sh1"}},
	}
	for i := 0; i < 50; i++ {
		if got := plurality(proposals, options, fmt.Sprintf("p%d", i)); got != "sh1" {
			t.Fatalf("plurality = %s, want sh1 (2 proposers vs 1)", got)
		}
	}
}

func TestRandomDictatorIsUniformAndStable(t *testing.T) {
	proposals := []Proposal{
		{AgentID: "a", Move: Candidate{UCI: "m1"}},
		{AgentID: "b", Move: Candidate{UCI: "m2"}},
		{AgentID: "c", Move: Candidate{UCI: "m3"}},
	}
	counts := map[string]int{}
	for i := 0; i < 300; i++ {
		counts[randomDictator(proposals, fmt.Sprintf("p%d", i))]++
	}
	if len(counts) != 3 {
		t.Errorf("dictator only ever chose %v, want all three proposals", sortedKeys(counts))
	}
	for uciMove, n := range counts {
		if n < 60 || n > 160 {
			t.Errorf("%s chosen %d/300 times — not uniform", uciMove, n)
		}
	}
	// Stable for a given position.
	first := randomDictator(proposals, "fixed")
	for i := 0; i < 10; i++ {
		if randomDictator(proposals, "fixed") != first {
			t.Fatal("random dictator is not reproducible for a fixed position")
		}
	}
}

func TestReviewDepthPenalty(t *testing.T) {
	a := &Agent{Personality: Personality{ID: "x"}, baseDepth: 12, reviewPenalty: 4}
	if a.depth() != 12 {
		t.Errorf("depth = %d, want 12", a.depth())
	}
	if a.reviewDepth() != 8 {
		t.Errorf("reviewDepth = %d, want 8", a.reviewDepth())
	}
	// The floor holds even with an absurd penalty, so an agent always forms
	// some opinion rather than none.
	deep := &Agent{Personality: Personality{ID: "x"}, baseDepth: 6, reviewPenalty: 99}
	if deep.reviewDepth() != 4 {
		t.Errorf("reviewDepth = %d, want the floor of 4", deep.reviewDepth())
	}
	// Zero penalty means information transfers for free — the null condition.
	free := &Agent{Personality: Personality{ID: "x"}, baseDepth: 12, reviewPenalty: 0}
	if free.reviewDepth() != free.depth() {
		t.Errorf("with no penalty reviewDepth (%d) should equal depth (%d)", free.reviewDepth(), free.depth())
	}
}

func TestSummarizeGem(t *testing.T) {
	sets := func(holder, gem string) *InfoSets {
		return &InfoSets{GemUCI: gem, GemHolder: holder}
	}
	r := &Run{Plies: []*PlyRecord{
		// Surfaced and adopted.
		{Side: "suite", InfoSets: sets("aggressor", "gem"), GemProposed: true, GemAdopted: true,
			Counterfactual: map[string]string{"plurality": "sh1", "random-dictator": "gem"}},
		// Surfaced but talked down.
		{Side: "suite", InfoSets: sets("defender", "gem"), GemProposed: true, GemAdopted: false,
			Counterfactual: map[string]string{"plurality": "sh1", "random-dictator": "sh2"}},
		// Never surfaced — the holder's own taste buried it.
		{Side: "suite", InfoSets: sets("defender", "gem"), GemProposed: false, GemAdopted: false,
			Counterfactual: map[string]string{"plurality": "sh1", "random-dictator": "sh1"}},
	}}
	g := r.summarizeGem()

	if g.Positions != 3 || g.Proposed != 2 || g.Adopted != 1 {
		t.Fatalf("positions/proposed/adopted = %d/%d/%d, want 3/2/1", g.Positions, g.Proposed, g.Adopted)
	}
	if math.Abs(g.AdoptedGivenProposed-0.5) > 0.001 {
		t.Errorf("persuasion rate = %.2f, want 0.50 (adopted 1 of the 2 surfaced)", g.AdoptedGivenProposed)
	}
	if g.PluralityAdopted != 0 {
		t.Errorf("plurality found the gem %d times, want 0", g.PluralityAdopted)
	}
	if g.DictatorAdopted != 1 {
		t.Errorf("dictator found the gem %d times, want 1", g.DictatorAdopted)
	}
	if g.HolderCounts["defender"] != 2 || g.ProposedByHolder["defender"] != 1 {
		t.Errorf("defender held %d and surfaced %d, want 2 and 1",
			g.HolderCounts["defender"], g.ProposedByHolder["defender"])
	}
}

func TestQualifyFiltersPositions(t *testing.T) {
	pos := mustPosition(t, chess.StartingPosition().String())
	opts := SuiteOpts{MinEdge: 15, MinGap: 40, SharedStart: 4, MinCandidates: 6}

	if _, ok := qualify(pos, candidateLines(120, 90, 80, 40, 35, 30), opts); !ok {
		t.Error("a position with a 30cp edge and an 80cp gap should qualify")
	}
	if _, ok := qualify(pos, candidateLines(120, 118, 80, 40, 35, 30), opts); ok {
		t.Error("a 2cp edge means the gem is not uniquely best — should be rejected")
	}
	if _, ok := qualify(pos, candidateLines(120, 90, 88, 100, 85, 80), opts); ok {
		t.Error("a 20cp gap to the shared pool is too small — should be rejected")
	}
	if _, ok := qualify(pos, candidateLines(120, 90, 80), opts); ok {
		t.Error("too few candidates — should be rejected")
	}

	// Forced mates are excluded: they are not on the centipawn scale, and a
	// suite dominated by them would measure tactics rather than pooling.
	mated := candidateLines(120, 90, 80, 40, 35, 30)
	mated[0].Eval = Eval{Mate: 3}
	if _, ok := qualify(pos, mated, opts); ok {
		t.Error("positions containing a forced mate should be rejected")
	}
}

func TestSummarizeSideCountsQuality(t *testing.T) {
	run := &Run{Plies: []*PlyRecord{
		{Side: "White", Unanimous: true, Loss: map[string]int{"played": 0, "solo:aggressor": 120}},
		{Side: "White", Switched: []string{"aggressor"}, Loss: map[string]int{"played": 60, "solo:aggressor": 30}},
		{Side: "Black", Loss: map[string]int{"played": 25}},
	}}
	s := run.summarizeSide("White", ArmGemot)

	if s.Decisions != 2 {
		t.Fatalf("decisions = %d, want 2", s.Decisions)
	}
	if s.UnanimousPlies != 1 {
		t.Errorf("unanimous = %d, want 1", s.UnanimousPlies)
	}
	if math.Abs(s.DisagreementPct-50) > 0.01 {
		t.Errorf("disagreement = %.1f%%, want 50%%", s.DisagreementPct)
	}
	if s.SwitchesByAgent["aggressor"] != 1 {
		t.Errorf("aggressor switches = %d, want 1", s.SwitchesByAgent["aggressor"])
	}

	played := s.Quality["played"]
	if played.ACPL != 30 {
		t.Errorf("played ACPL = %.1f, want 30", played.ACPL)
	}
	if played.Mistakes != 1 || played.Blunders != 0 {
		t.Errorf("played mistakes/blunders = %d/%d, want 1/0", played.Mistakes, played.Blunders)
	}
	if played.BestMoveRate != 0.5 {
		t.Errorf("played best-move rate = %.2f, want 0.50", played.BestMoveRate)
	}

	solo := s.Quality["solo:aggressor"]
	if solo.Blunders != 1 {
		t.Errorf("aggressor blunders = %d, want 1", solo.Blunders)
	}
}

func TestVerdictNamesTheWinner(t *testing.T) {
	s := SideSummary{Arm: ArmGemot, Quality: map[string]Quality{
		"played":         {Name: "played", ACPL: 10},
		"plurality":      {Name: "plurality", ACPL: 20},
		"solo:tactician": {Name: "solo:tactician", ACPL: 5},
	}}
	got := verdict(s)
	if got == "" {
		t.Fatal("expected a verdict")
	}
	if !contains(got, "solo:tactician") || !contains(got, "beaten by") {
		t.Errorf("verdict should report the loss to the tactician, got: %s", got)
	}
}

func TestClaimableDrawIgnoresDrawOffer(t *testing.T) {
	// EligibleDraws always includes DrawOffer; the game must not end on it.
	if got := claimableDraw(chess.NewGame()); got != chess.NoMethod {
		t.Errorf("claimableDraw at the start = %v, want NoMethod", got)
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct{ in, want string }{
		{`{"value": 2}`, `{"value": 2}`},
		{"```json\n{\"value\": 2}\n```", `{"value": 2}`},
		{"Here is my vote:\n{\"value\": -1}\nHope that helps.", `{"value": -1}`},
	}
	for _, tc := range tests {
		if got := extractJSON(tc.in); got != tc.want {
			t.Errorf("extractJSON(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveEngineRejectsMissing(t *testing.T) {
	if _, err := ResolveEngine("definitely-not-a-chess-engine"); err == nil {
		t.Error("expected an error for a nonexistent engine")
	}
	// An explicit path is taken as given, even if it does not exist yet.
	if got, err := ResolveEngine("/opt/custom/sf"); err != nil || got != "/opt/custom/sf" {
		t.Errorf("explicit path = %q, %v; want it passed through unchanged", got, err)
	}
}

// TestEngineIntegration exercises the real UCI client. It is skipped when no
// engine is installed so the suite stays green on machines without one.
func TestEngineIntegration(t *testing.T) {
	binary, err := ResolveEngine("stockfish")
	if err != nil {
		t.Skipf("no engine available: %v", err)
	}
	eng, err := NewEngine(binary, map[string]string{"Threads": "1", "Hash": "16"})
	if err != nil {
		t.Fatalf("starting engine: %v", err)
	}
	defer eng.Close() //nolint:errcheck

	start := chess.StartingPosition().String()
	lines, err := eng.Analyze(start, SearchOpts{Depth: 8, MultiPV: 4})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(lines) != 4 {
		t.Fatalf("got %d variations, want 4 — MultiPV parsing is broken", len(lines))
	}
	for i, l := range lines {
		if l.Rank != i+1 {
			t.Errorf("line %d has rank %d", i, l.Rank)
		}
		if l.UCI == "" || len(l.PV) == 0 {
			t.Errorf("line %d has no move", i)
		}
		if l.Depth < 8 {
			t.Errorf("line %d reported depth %d, want at least 8", i, l.Depth)
		}
	}
	if lines[0].Eval.Centipawns() < lines[3].Eval.Centipawns() {
		t.Error("variations should be ordered best-first")
	}

	// searchmoves must restrict the search to the named move, which is how
	// agents evaluate a peer's proposal they did not shortlist.
	restricted, err := eng.Analyze(start, SearchOpts{Depth: 8, MultiPV: 1, SearchMoves: []string{"a2a3"}})
	if err != nil {
		t.Fatalf("restricted Analyze: %v", err)
	}
	if restricted[0].UCI != "a2a3" {
		t.Errorf("searchmoves returned %s, want a2a3", restricted[0].UCI)
	}

	// A mate-in-one must be reported as a mate, not a centipawn score.
	mate := "6k1/5ppp/8/8/8/8/8/R5K1 w - - 0 1"
	mateLines, err := eng.Analyze(mate, SearchOpts{Depth: 10, MultiPV: 1})
	if err != nil {
		t.Fatalf("mate Analyze: %v", err)
	}
	if mateLines[0].Eval.Mate <= 0 {
		t.Errorf("expected a forced mate for White, got %+v", mateLines[0].Eval)
	}
}

// TestAgentSurveyIntegration checks that personalities genuinely diverge on a
// real position — the premise the whole experiment rests on.
func TestAgentSurveyIntegration(t *testing.T) {
	binary, err := ResolveEngine("stockfish")
	if err != nil {
		t.Skipf("no engine available: %v", err)
	}
	eng, err := NewEngine(binary, map[string]string{"Threads": "1", "Hash": "16"})
	if err != nil {
		t.Fatalf("starting engine: %v", err)
	}
	defer eng.Close() //nolint:errcheck

	// An open Italian-style position with tactical and quiet options available.
	pos := mustPosition(t, "r1bqk1nr/pppp1ppp/2n5/2b1p3/2B1P3/5N2/PPPP1PPP/RNBQK2R w KQkq - 4 4")
	picks := map[string]string{}
	for _, p := range defaultPersonalities() {
		a := &Agent{Personality: p, Side: chess.White, engine: eng, baseDepth: 8, reviewPenalty: 4}
		list, err := a.Survey(pos, nil)
		if err != nil {
			t.Fatalf("%s survey: %v", p.ID, err)
		}
		if len(list) == 0 {
			t.Fatalf("%s produced no candidates", p.ID)
		}
		for _, c := range list {
			if c.SAN == "" {
				t.Errorf("%s: candidate %s has no SAN", p.ID, c.UCI)
			}
			if c.Utility != c.Eval.Centipawns()+c.Bias {
				t.Errorf("%s: utility %d != eval %d + bias %d", p.ID, c.Utility, c.Eval.Centipawns(), c.Bias)
			}
		}
		// The list must be ordered by the agent's own utility.
		for i := 1; i < len(list); i++ {
			if list[i-1].Utility < list[i].Utility {
				t.Errorf("%s: candidates out of order at %d", p.ID, i)
			}
		}
		picks[p.ID] = list[0].SAN
	}
	t.Logf("picks: %v", picks)

	distinct := map[string]bool{}
	for _, san := range picks {
		distinct[san] = true
	}
	if len(distinct) < 2 {
		t.Errorf("all three personalities chose %v — biases are not separating them", picks)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
