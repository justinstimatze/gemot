package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/notnil/chess"
)

// maxLoss caps the centipawn loss charged for any single move. Without a cap a
// missed forced mate scores tens of thousands of centipawns and drowns out
// every other decision in the average — this is the same convention chess
// analysis tools use for average centipawn loss.
const maxLoss = 1000

// Run is one complete game plus everything measured about it.
type Run struct {
	ID          string        `json:"run_id"`
	GroupID     string        `json:"group_id"`
	WhiteArm    Arm           `json:"white_arm"`
	BlackArm    Arm           `json:"black_arm"`
	Personas    []Personality `json:"personalities"`
	BaseDepth   int           `json:"base_depth"`
	RefDepth    int           `json:"reference_depth"`
	Persuasion  float64       `json:"persuasion"`
	LLMMode     string        `json:"llm_mode"`
	Asymmetric  bool          `json:"asymmetric_depth"`
	Template    string        `json:"template"`
	AlwaysDelib bool          `json:"always_deliberate"`

	Mode          string       `json:"mode"`           // "suite" or "game"
	Information   string       `json:"information"`    // "hidden" or "full"
	ReviewPenalty int          `json:"review_penalty"` // plies lost judging a move outside the information set
	Hidden        HiddenConfig `json:"hidden_config"`
	SuitePath     string       `json:"suite_path,omitempty"`
	Suite         *Suite       `json:"-"` // referenced by path; not duplicated into run.json
	StartedAt     time.Time    `json:"started_at"`
	FinishedAt    time.Time    `json:"finished_at"`
	Plies         []*PlyRecord `json:"plies"`
	Outcome       string       `json:"outcome"`
	Method        string       `json:"method"`
	PGN           string       `json:"pgn"`
	FinalFEN      string       `json:"final_fen"`
	GemotCalls    int          `json:"gemot_calls"`
	LLMCalls      int          `json:"llm_calls"`
	Summary       Summary      `json:"summary"`

	reference *Engine
	gemot     *Gemot
	llm       *LLM
}

// Quality is the decision quality of one decision maker over one side's moves.
type Quality struct {
	Name         string  `json:"name"`
	Moves        int     `json:"moves"`
	ACPL         float64 `json:"acpl"`
	Blunders     int     `json:"blunders"`       // >= 100cp
	Mistakes     int     `json:"mistakes"`       // 50-99cp
	Inaccuracies int     `json:"inaccuracies"`   // 20-49cp
	BestMoveRate float64 `json:"best_move_rate"` // fraction played with 0cp loss
	WorstLoss    int     `json:"worst_loss"`
}

// SideSummary compares, over the identical stream of positions one side faced,
// what the side actually played against what each agent would have played alone
// and what a no-discussion plurality vote would have produced.
type SideSummary struct {
	Side            string             `json:"side"`
	Arm             Arm                `json:"arm"`
	Decisions       int                `json:"decisions"`
	Deliberations   int                `json:"deliberations"`
	UnanimousPlies  int                `json:"unanimous_plies"`
	DisagreementPct float64            `json:"disagreement_pct"`
	SwitchesByAgent map[string]int     `json:"switches_by_agent"`
	Quality         map[string]Quality `json:"quality"` // "played", "plurality", "solo:<id>"
	AvgAnalysisSecs float64            `json:"avg_analysis_seconds"`
}

type Summary struct {
	White SideSummary `json:"white"`
	Black SideSummary `json:"black"`
	Suite SideSummary `json:"suite"`
	Gem   GemSummary  `json:"gem"`
}

// GemSummary is the hidden-profile scoreboard. Under a hidden profile the best
// move sits with exactly one agent, so these three numbers say precisely where
// a group loses information: never surfaced, or surfaced and then talked down.
type GemSummary struct {
	Positions int `json:"positions"`
	// Proposed: the holder actually put the gem on the table. When this is low
	// the group never even had the chance — the holder's own taste buried it.
	Proposed int `json:"proposed"`
	// Adopted: the group played it.
	Adopted int `json:"adopted"`
	// AdoptedGivenProposed is the persuasion rate — of the times the gem was
	// tabled, how often did the other agents come across. This is the number
	// the experiment exists to move.
	AdoptedGivenProposed float64 `json:"adopted_given_proposed"`
	// PluralityAdopted is the control. Under a clean deal it should sit near
	// zero: two of three agents cannot see the gem, so aggregation alone
	// cannot find it.
	PluralityAdopted int            `json:"plurality_adopted"`
	DictatorAdopted  int            `json:"random_dictator_adopted"`
	HolderCounts     map[string]int `json:"gem_holder_counts"`
	ProposedByHolder map[string]int `json:"proposed_by_holder"`
}

// score evaluates every move that was, or would have been, played at this ply
// against a reference search of the same position. Because all candidates are
// scored from the same node at the same depth, the losses are directly
// comparable — that is what makes the counterfactuals meaningful.
func (r *Run) score(pos *chess.Position, rec *PlyRecord) error {
	fen := pos.String()
	best, err := r.reference.Analyze(fen, SearchOpts{Depth: r.RefDepth, MultiPV: 1})
	if err != nil {
		return err
	}
	rec.RefEval = best[0].Eval
	bestMove, err := decodeMove(pos, best[0].UCI)
	if err == nil {
		rec.RefBest = chess.AlgebraicNotation{}.Encode(pos, bestMove)
	}
	baseline := best[0].Eval.Centipawns()

	candidates := map[string]string{"played": rec.Chosen}
	for name, uciMove := range rec.Counterfactual {
		candidates[name] = uciMove
	}

	// One reference search per distinct move, reused across the names that
	// picked it — three agents agreeing costs one search, not three.
	lossByMove := map[string]int{}
	for _, uciMove := range candidates {
		if uciMove == "" {
			continue
		}
		if _, done := lossByMove[uciMove]; done {
			continue
		}
		if uciMove == best[0].UCI {
			lossByMove[uciMove] = 0
			continue
		}
		lines, err := r.reference.Analyze(fen, SearchOpts{Depth: r.RefDepth, MultiPV: 1, SearchMoves: []string{uciMove}})
		if err != nil {
			return err
		}
		loss := baseline - lines[0].Eval.Centipawns()
		if loss < 0 {
			loss = 0
		}
		if loss > maxLoss {
			loss = maxLoss
		}
		lossByMove[uciMove] = loss
	}
	for name, uciMove := range candidates {
		if uciMove == "" {
			continue
		}
		rec.Loss[name] = lossByMove[uciMove]
	}

	// The oracle picks the best move anyone actually proposed. It is the
	// ceiling for any procedure that only chooses among the proposals, so the
	// gap between it and what was played is exactly the aggregation loss —
	// separate from whatever the agents failed to propose in the first place.
	oracleUCI, oracleLoss := "", maxLoss+1
	for _, p := range rec.Proposals {
		if l, ok := lossByMove[p.Move.UCI]; ok && l < oracleLoss {
			oracleUCI, oracleLoss = p.Move.UCI, l
		}
	}
	if oracleUCI != "" {
		rec.Counterfactual["oracle:best-proposal"] = oracleUCI
		rec.Loss["oracle:best-proposal"] = oracleLoss
	}
	return nil
}

// summarizeSuite aggregates a suite run into a single bucket plus the
// hidden-profile scoreboard.
func (r *Run) summarizeSuite() {
	r.Summary.Suite = r.summarizeSide("suite", r.WhiteArm)
	r.Summary.Gem = r.summarizeGem()
}

func (r *Run) summarizeGem() GemSummary {
	g := GemSummary{HolderCounts: map[string]int{}, ProposedByHolder: map[string]int{}}
	for _, rec := range r.Plies {
		if rec.InfoSets == nil {
			continue
		}
		g.Positions++
		g.HolderCounts[rec.InfoSets.GemHolder]++
		if rec.GemProposed {
			g.Proposed++
			g.ProposedByHolder[rec.InfoSets.GemHolder]++
		}
		if rec.GemAdopted {
			g.Adopted++
		}
		if rec.Counterfactual["plurality"] == rec.InfoSets.GemUCI {
			g.PluralityAdopted++
		}
		if rec.Counterfactual["random-dictator"] == rec.InfoSets.GemUCI {
			g.DictatorAdopted++
		}
	}
	if g.Proposed > 0 {
		g.AdoptedGivenProposed = float64(g.Adopted) / float64(g.Proposed)
	}
	return g
}

func (r *Run) summarize() {
	r.Summary.White = r.summarizeSide(sideName(chess.White), r.WhiteArm)
	r.Summary.Black = r.summarizeSide(sideName(chess.Black), r.BlackArm)
}

func (r *Run) summarizeSide(side string, arm Arm) SideSummary {
	s := SideSummary{
		Side:            side,
		Arm:             arm,
		SwitchesByAgent: map[string]int{},
		Quality:         map[string]Quality{},
	}
	type acc struct {
		n, total, blunders, mistakes, inaccuracies, best, worst int
	}
	accs := map[string]*acc{}
	var analysisSecs float64
	var analysed int

	for _, rec := range r.Plies {
		if rec.Side != side {
			continue
		}
		s.Decisions++
		if rec.Unanimous {
			s.UnanimousPlies++
		}
		if rec.DeliberationID != "" {
			s.Deliberations++
		}
		if rec.Analysis != nil {
			analysisSecs += rec.Analysis.Elapsed
			analysed++
		}
		for _, agentID := range rec.Switched {
			s.SwitchesByAgent[agentID]++
		}
		for name, loss := range rec.Loss {
			a, ok := accs[name]
			if !ok {
				a = &acc{}
				accs[name] = a
			}
			a.n++
			a.total += loss
			switch {
			case loss >= 100:
				a.blunders++
			case loss >= 50:
				a.mistakes++
			case loss >= 20:
				a.inaccuracies++
			}
			if loss == 0 {
				a.best++
			}
			if loss > a.worst {
				a.worst = loss
			}
		}
	}
	if s.Decisions > 0 {
		s.DisagreementPct = 100 * float64(s.Decisions-s.UnanimousPlies) / float64(s.Decisions)
	}
	if analysed > 0 {
		s.AvgAnalysisSecs = analysisSecs / float64(analysed)
	}
	for name, a := range accs {
		if a.n == 0 {
			continue
		}
		s.Quality[name] = Quality{
			Name:         name,
			Moves:        a.n,
			ACPL:         float64(a.total) / float64(a.n),
			Blunders:     a.blunders,
			Mistakes:     a.mistakes,
			Inaccuracies: a.inaccuracies,
			BestMoveRate: float64(a.best) / float64(a.n),
			WorstLoss:    a.worst,
		}
	}
	return s
}

// write saves the full record, the PGN, and the human-readable report.
func (r *Run) write(dir string) error {
	if err := writeJSON(filepath.Join(dir, "run.json"), r); err != nil {
		return err
	}
	if r.PGN != "" {
		if err := writeFile(filepath.Join(dir, "game.pgn"), r.PGN+"\n"); err != nil {
			return err
		}
	}
	return writeFile(filepath.Join(dir, "REPORT.md"), r.report())
}

func (r *Run) report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# chess-consensus %s\n\n", r.ID)
	if r.Mode == "suite" {
		fmt.Fprintf(&b, "%d positions | rule: **%s** | information: **%s**\n\n", len(r.Plies), r.WhiteArm, r.Information)
	} else {
		fmt.Fprintf(&b, "White: **%s** | Black: **%s** | result: **%s** (%s)\n\n", r.WhiteArm, r.BlackArm, r.Outcome, r.Method)
	}
	fmt.Fprintf(&b, "Agents: %s. Search depth %d, reference depth %d. LLM mode `%s`.",
		personaIDs(r.Personas), r.BaseDepth, r.RefDepth, r.LLMMode)
	if r.Information == "hidden" {
		fmt.Fprintf(&b, " Shared pool starts at rank %d (%d moves); review penalty %d plies.",
			r.Hidden.SharedStart, r.Hidden.SharedCount, r.ReviewPenalty)
	}
	if r.Asymmetric {
		b.WriteString(" Asymmetric search budgets.")
	}
	fmt.Fprintf(&b, " %d decisions in %s.\n", len(r.Plies), r.FinishedAt.Sub(r.StartedAt).Round(time.Second))
	if r.GroupID != "" && r.GemotCalls > 0 {
		fmt.Fprintf(&b, "\ngemot group `%s` — %d tool calls.", r.GroupID, r.GemotCalls)
	}
	if r.LLMCalls > 0 {
		fmt.Fprintf(&b, " %d LLM calls.", r.LLMCalls)
	}
	b.WriteString("\n")

	if g := r.Summary.Gem; g.Positions > 0 {
		b.WriteString(gemReport(g))
	}

	for _, s := range []SideSummary{r.Summary.Suite, r.Summary.White, r.Summary.Black} {
		if s.Decisions == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s (%s)\n\n", s.Side, s.Arm)
		fmt.Fprintf(&b, "%d decisions, %d with genuine disagreement (%.0f%%), %d deliberations run",
			s.Decisions, s.Decisions-s.UnanimousPlies, s.DisagreementPct, s.Deliberations)
		if s.AvgAnalysisSecs > 0 {
			fmt.Fprintf(&b, ", %.1fs average analysis", s.AvgAnalysisSecs)
		}
		b.WriteString(".\n\n")

		b.WriteString("| decision maker | ACPL | best-move rate | inacc. | mistakes | blunders | worst |\n")
		b.WriteString("|----------------|------|----------------|--------|----------|----------|-------|\n")
		for _, q := range rankQuality(s.Quality) {
			label := q.Name
			if label == "played" {
				label = fmt.Sprintf("**%s (played)**", s.Arm)
			}
			fmt.Fprintf(&b, "| %s | %.1f | %.0f%% | %d | %d | %d | %d |\n",
				label, q.ACPL, q.BestMoveRate*100, q.Inaccuracies, q.Mistakes, q.Blunders, q.WorstLoss)
		}

		if len(s.SwitchesByAgent) > 0 {
			b.WriteString("\nAgents persuaded off their own proposal: ")
			var parts []string
			for _, agentID := range sortedKeys(s.SwitchesByAgent) {
				parts = append(parts, fmt.Sprintf("%s %d×", agentID, s.SwitchesByAgent[agentID]))
			}
			b.WriteString(strings.Join(parts, ", ") + ".\n")
		} else if s.Decisions > 0 {
			b.WriteString("\nNo agent was ever persuaded off its own proposal.\n")
		}

		if v := verdict(s); v != "" {
			fmt.Fprintf(&b, "\n%s\n", v)
		}
	}

	if r.PGN != "" {
		fmt.Fprintf(&b, "\n## Game\n\n```\n%s\n```\n", r.PGN)
	}
	return b.String()
}

// gemReport renders the hidden-profile scoreboard: whether the one agent who
// could see the best move said so, and whether anyone listened.
func gemReport(g GemSummary) string {
	var b strings.Builder
	b.WriteString("\n## Hidden profile\n\n")
	b.WriteString("The best move was dealt to exactly one agent. Two of three could not see it at all.\n\n")
	b.WriteString("| stage | count | rate |\n|-------|-------|------|\n")
	pct := func(n int) string { return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(g.Positions)) }
	fmt.Fprintf(&b, "| positions dealt | %d | — |\n", g.Positions)
	fmt.Fprintf(&b, "| gem surfaced by its holder | %d | %s |\n", g.Proposed, pct(g.Proposed))
	fmt.Fprintf(&b, "| **gem adopted by the group** | **%d** | **%s** |\n", g.Adopted, pct(g.Adopted))
	fmt.Fprintf(&b, "| plurality found it (control) | %d | %s |\n", g.PluralityAdopted, pct(g.PluralityAdopted))
	fmt.Fprintf(&b, "| random dictator found it (control) | %d | %s |\n", g.DictatorAdopted, pct(g.DictatorAdopted))

	fmt.Fprintf(&b, "\n**Persuasion rate: %.0f%%** — of the %d positions where the gem was tabled, the group adopted it %d times.\n",
		g.AdoptedGivenProposed*100, g.Proposed, g.Adopted)

	if g.Proposed < g.Positions {
		fmt.Fprintf(&b, "\nOn %d positions the holder never proposed the gem — its own taste buried the best move before discussion began. No decision rule can recover those.\n",
			g.Positions-g.Proposed)
	}
	if len(g.ProposedByHolder) > 0 {
		b.WriteString("\nSurfacing rate by holder: ")
		var parts []string
		for _, id := range sortedKeys(g.HolderCounts) {
			parts = append(parts, fmt.Sprintf("%s %d/%d", id, g.ProposedByHolder[id], g.HolderCounts[id]))
		}
		b.WriteString(strings.Join(parts, ", ") + ".\n")
	}
	return b.String()
}

// verdict states plainly whether the side's decision rule beat the
// alternatives, since that is the entire question the harness exists to answer.
func verdict(s SideSummary) string {
	played, ok := s.Quality["played"]
	if !ok {
		return ""
	}
	rule := "The " + string(s.Arm) + " rule"
	var better, worse []string
	for name, q := range s.Quality {
		if name == "played" {
			continue
		}
		if q.ACPL < played.ACPL {
			better = append(better, fmt.Sprintf("%s (%.1f)", name, q.ACPL))
		} else if q.ACPL > played.ACPL {
			worse = append(worse, fmt.Sprintf("%s (%.1f)", name, q.ACPL))
		}
	}
	sort.Strings(better)
	sort.Strings(worse)
	switch {
	case len(better) == 0 && len(worse) == 0:
		return fmt.Sprintf("%s scored %.1f ACPL — identical to every alternative, so the decision procedure changed nothing here.", rule, played.ACPL)
	case len(better) == 0:
		return fmt.Sprintf("%s (%.1f ACPL) beat every alternative: %s.", rule, played.ACPL, strings.Join(worse, ", "))
	default:
		return fmt.Sprintf("%s (%.1f ACPL) was beaten by %s and beat %s.",
			rule, played.ACPL, strings.Join(better, ", "), joinOrNone(worse))
	}
}

func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "nothing"
	}
	return strings.Join(items, ", ")
}

func rankQuality(q map[string]Quality) []Quality {
	out := make([]Quality, 0, len(q))
	for _, v := range q {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ACPL != out[j].ACPL {
			return out[i].ACPL < out[j].ACPL
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
