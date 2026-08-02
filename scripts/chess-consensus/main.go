// chess-consensus tests whether a three-agent gemot can pool information that
// no single agent holds.
//
// The default setup is a hidden profile: at each position the best move is
// dealt to exactly one agent, while the other two choose from a shared pool of
// worse options. No individual can reach the right answer alone, and a group
// that merely aggregates first preferences fails systematically. Only a group
// that actually surfaces and acts on private information wins.
//
// A reference engine scores every decision, so each position yields a paired
// comparison between the rule that ran, a no-discussion plurality vote, a
// random dictator, each agent alone, and the best move anyone proposed.
//
// Usage:
//
//	go run ./scripts/chess-consensus --generate-suite 40 --suite-out suite.json
//	go run ./scripts/chess-consensus --mode suite --suite suite.json --gemot=false
//	go run ./scripts/chess-consensus --mode suite --suite suite.json --llm full
//	go run ./scripts/chess-consensus --mode game --information full   # self-play demo
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/notnil/chess"
)

// Arm is how one side decides its moves.
type Arm string

const (
	// ArmGemot is the treatment: propose, argue, vote, analyse, reconsider, tally.
	ArmGemot Arm = "gemot"
	// ArmPlurality is the control: the same three agents propose, but never
	// discuss. Most-proposed move wins. This isolates deliberation from the
	// mere fact of having three opinions.
	ArmPlurality Arm = "plurality"
	// ArmEngine is a plain reference engine, for use as a sparring partner.
	ArmEngine Arm = "engine"
)

func main() {
	// Load .env so ANTHROPIC_API_KEY, GEMOT_API_SECRET, GEMOT_LIVE_URL etc. are
	// available without a shell wrapper. Values already in the environment win.
	loadDotEnv()

	var (
		enginePath  = flag.String("engine", "stockfish", "path to a UCI engine binary")
		baseDepth   = flag.Int("depth", 12, "search depth for the agents")
		refDepth    = flag.Int("ref-depth", 18, "search depth for the reference engine that scores every decision")
		maxPlies    = flag.Int("max-plies", 60, "stop after this many plies")
		startFEN    = flag.String("start-fen", "", "start from this FEN instead of the initial position")
		whiteArm    = flag.String("white", "gemot", "how White decides: gemot, plurality, or engine")
		blackArm    = flag.String("black", "gemot", "how Black decides: gemot, plurality, or engine")
		personas    = flag.String("personalities", "", "path to a personalities JSON file (default: aggressor, defender, tactician)")
		asymmetric  = flag.Bool("asymmetric", false, "give each personality a different search depth as well as a different bias")
		persuasion  = flag.Float64("persuasion", 0.6, "how far an agent discounts its own bias for a well-supported rival move, 0-1 (offline mode only)")
		llmMode     = flag.String("llm", "off", "off | args (LLM writes arguments) | full (LLM also votes and reconsiders)")
		llmModel    = flag.String("llm-model", "claude-haiku-4-5", "model used when --llm is not off")
		useGemot    = flag.Bool("gemot", true, "run the gemot deliberation; --gemot=false degrades the gemot arm to vote aggregation only")
		gemotURL    = flag.String("url", "", "gemot MCP URL (default: GEMOT_LIVE_URL, else https://gemot.dev/mcp)")
		groupID     = flag.String("group", "", "gemot group_id for this run (default: chess-consensus-<timestamp>)")
		template    = flag.String("template", "review", "gemot governance template")
		alwaysDelib = flag.Bool("always-deliberate", false, "deliberate even when all three agents propose the same move")
		outDir      = flag.String("out", "", "directory for results (default: ./chess-consensus-<timestamp>)")
		runID       = flag.String("run-id", "", "identifier for this run (default: derived from the clock)")

		mode          = flag.String("mode", "suite", "suite (fixed position set, the experiment) or game (self-play, the demo)")
		information   = flag.String("information", "hidden", "hidden (each agent sees a different slice of the candidates) or full")
		reviewPenalty = flag.Int("review-penalty", 4, "plies subtracted when an agent judges a move outside its information set — the cost of receiving information secondhand")
		suitePath     = flag.String("suite", "", "path to a position suite JSON file")
		genSuite      = flag.Int("generate-suite", 0, "generate a suite of this many positions, save it, and exit")
		suiteOut      = flag.String("suite-out", "suite.json", "where to write a generated suite")
		seed          = flag.Uint64("seed", 2026, "seed for suite generation")
		sharedStart   = flag.Int("shared-start", 4, "reference rank at which the shared pool begins; ranks between it and the gem are dealt to nobody")
		sharedCount   = flag.Int("shared-count", 4, "how many candidate moves every agent can see")
		minGap        = flag.Int("min-gap", 40, "suite filter: centipawns by which the gem must beat the shared pool")
		minEdge       = flag.Int("min-edge", 15, "suite filter: centipawns by which the gem must be uniquely best")
		maxGap        = flag.Int("max-gap", 300, "suite filter: upper bound on the gem's advantage, so a few extreme positions cannot dominate the averages")
	)
	flag.Parse()

	stamp := time.Now().UTC().Format("20060102-150405")
	if *runID == "" {
		*runID = stamp
	}
	if *groupID == "" {
		*groupID = "chess-consensus-" + *runID
	}
	if *outDir == "" {
		*outDir = "chess-consensus-" + *runID
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatal("creating output directory: %v", err)
	}

	ps, err := loadPersonalities(*personas, *asymmetric)
	if err != nil {
		fatal("%v", err)
	}

	var llm *LLM
	if *llmMode != "off" {
		llm, err = NewLLM(*llmModel)
		if err != nil {
			fatal("%v", err)
		}
	}

	var gemot *Gemot
	if *useGemot && *genSuite == 0 {
		url := *gemotURL
		if url == "" {
			url = os.Getenv("GEMOT_LIVE_URL")
		}
		if url == "" {
			url = "https://gemot.dev/mcp"
		}
		secret := gemotSecret()
		if secret == "" {
			fmt.Fprintln(os.Stderr, "note: no GEMOT_API_SECRET found; continuing unauthenticated")
		}
		gemot = NewGemot(url, secret)
		defer gemot.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	binary, err := ResolveEngine(*enginePath)
	if err != nil {
		fatal("%v", err)
	}

	// One engine process per agent keeps their hash tables — and therefore
	// their evaluations — independent, plus one for scoring.
	engineOpts := map[string]string{"Threads": "1", "Hash": "64"}
	reference, err := NewEngine(binary, engineOpts)
	if err != nil {
		fatal("starting reference engine: %v", err)
	}
	defer reference.Close() //nolint:errcheck

	run := &Run{
		ID:          *runID,
		GroupID:     *groupID,
		Personas:    ps,
		BaseDepth:   *baseDepth,
		RefDepth:    *refDepth,
		Persuasion:  *persuasion,
		LLMMode:     *llmMode,
		Asymmetric:  *asymmetric,
		Template:    *template,
		AlwaysDelib: *alwaysDelib,
		StartedAt:   time.Now().UTC(),
		reference:   reference,
		gemot:       gemot,
		llm:         llm,
	}

	// --generate-suite builds the position set and exits. Suites are reusable
	// across arms and seeds, which is the point: every condition decides on
	// exactly the same positions.
	if *genSuite > 0 {
		fmt.Printf("generating a %d-position suite (seed %d, reference depth %d)...\n", *genSuite, *seed, *refDepth)
		suite, err := GenerateSuite(reference, SuiteOpts{
			Count: *genSuite, Seed: *seed, RefDepth: *refDepth,
			MultiPV: *sharedStart + *sharedCount + 4,
			MinEdge: *minEdge, MinGap: *minGap, MaxGap: *maxGap, SharedStart: *sharedStart,
			OpeningPlies: 6, MaxPlies: 60,
		})
		if err != nil {
			fatal("%v", err)
		}
		if err := suite.Save(*suiteOut); err != nil {
			fatal("writing suite: %v", err)
		}
		fmt.Printf("wrote %d positions to %s\n", len(suite.Positions), *suiteOut)
		return
	}

	run.Mode = *mode
	run.Information = *information
	run.ReviewPenalty = *reviewPenalty
	run.Hidden = HiddenConfig{SharedStart: *sharedStart, SharedCount: *sharedCount}

	fmt.Printf("chess-consensus %s (%s mode, %s information)\n", *runID, *mode, *information)
	fmt.Printf("  agents: %s\n", personaIDs(ps))
	fmt.Printf("  depth %d, reference depth %d, review penalty %d, llm=%s, gemot=%v\n\n",
		*baseDepth, *refDepth, *reviewPenalty, *llmMode, *useGemot)

	switch *mode {
	case "suite":
		if *suitePath == "" {
			fatal("--mode suite needs --suite <file>; build one with --generate-suite N")
		}
		suite, err := LoadSuite(*suitePath)
		if err != nil {
			fatal("%v", err)
		}
		run.SuitePath = *suitePath
		council, err := newCouncil(chess.White, Arm(*whiteArm), ps, binary, engineOpts, *baseDepth, *reviewPenalty, llm, *llmMode)
		if err != nil {
			fatal("building council: %v", err)
		}
		defer council.Close()
		run.WhiteArm, run.BlackArm = Arm(*whiteArm), Arm(*whiteArm)
		run.runSuite(ctx, council, suite)

	case "game":
		councils := map[chess.Color]*Council{}
		for color, armName := range map[chess.Color]string{chess.White: *whiteArm, chess.Black: *blackArm} {
			council, err := newCouncil(color, Arm(armName), ps, binary, engineOpts, *baseDepth, *reviewPenalty, llm, *llmMode)
			if err != nil {
				fatal("building %s council: %v", color, err)
			}
			defer council.Close()
			councils[color] = council
		}
		run.WhiteArm, run.BlackArm = Arm(*whiteArm), Arm(*blackArm)
		run.runGame(ctx, councils, *startFEN, *maxPlies)

	default:
		fatal("unknown --mode %q (want suite or game)", *mode)
	}

	run.FinishedAt = time.Now().UTC()
	if gemot != nil {
		run.GemotCalls = gemot.Calls
	}
	if llm != nil {
		run.LLMCalls = llm.Calls
	}
	run.summarize()

	if err := run.write(*outDir); err != nil {
		fatal("writing results: %v", err)
	}
	fmt.Print("\n" + run.report())
	fmt.Printf("\nResults written to %s/\n", *outDir)
}

// runSuite decides every position in the suite once. Because the counterfactual
// columns are computed for every decision, a single pass yields every arm —
// gemot, plurality, random dictator, each agent alone, and the oracle ceiling —
// on identical positions. No separate control runs, and no confound from arms
// facing different boards.
func (r *Run) runSuite(ctx context.Context, council *Council, suite *Suite) {
	r.Suite = suite
	for i, sp := range suite.Positions {
		if ctx.Err() != nil {
			fmt.Println("\ninterrupted — writing partial results")
			break
		}
		pos, err := positionFromFEN(sp.FEN)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", sp.ID, err)
			continue
		}

		var sets *InfoSets
		if r.Information == "hidden" {
			sans := map[string]string{}
			for _, c := range sp.Candidates {
				if m, err := decodeMove(pos, c.UCI); err == nil {
					sans[c.UCI] = chess.AlgebraicNotation{}.Encode(pos, m)
				}
			}
			dealt, err := Partition(sp.FEN, sp.Candidates, sans, council.agentIDs(), r.Hidden)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", sp.ID, err)
				continue
			}
			sets = &dealt
		}

		// Every position starts from a clean engine state so runs are paired.
		council.Reset()
		r.reference.NewGame() //nolint:errcheck

		label := fmt.Sprintf("%s %s", sp.ID, sideName(pos.Turn()))
		rec, err := council.Decide(ctx, r, pos, label, sets)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", label, err)
			continue
		}
		rec.Ply = i + 1
		rec.Side = "suite"
		if err := r.score(pos, rec); err != nil {
			fmt.Fprintf(os.Stderr, "scoring %s: %v\n", label, err)
		}
		r.Plies = append(r.Plies, rec)
		printPly(rec)
	}
	r.summarizeSuite()
}

// runGame is the original self-play demo: two councils alternating moves.
func (r *Run) runGame(ctx context.Context, councils map[chess.Color]*Council, startFEN string, maxPlies int) {
	game := chess.NewGame(chess.UseNotation(chess.AlgebraicNotation{}))
	if startFEN != "" {
		fen, err := chess.FEN(startFEN)
		if err != nil {
			fatal("bad --start-fen: %v", err)
		}
		game = chess.NewGame(fen, chess.UseNotation(chess.AlgebraicNotation{}))
	}

	for ply := 0; ply < maxPlies; ply++ {
		if game.Outcome() != chess.NoOutcome {
			break
		}
		if ctx.Err() != nil {
			fmt.Println("\ninterrupted — writing partial results")
			break
		}
		// EligibleDraws always offers DrawOffer; only the automatic claims end
		// the game here.
		if claim := claimableDraw(game); claim != chess.NoMethod {
			_ = game.Draw(claim) //nolint:errcheck
			break
		}

		pos := game.Position()
		council := councils[pos.Turn()]
		label := moveLabel(pos)

		// Hidden profiles need a reference ranking to deal from, which game
		// mode does not precompute — so game mode is always full information.
		rec, err := council.Decide(ctx, r, pos, label, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", label, err)
			break
		}
		rec.Ply = ply + 1
		if err := r.score(pos, rec); err != nil {
			fmt.Fprintf(os.Stderr, "scoring %s: %v\n", label, err)
		}
		r.Plies = append(r.Plies, rec)
		printPly(rec)

		move, err := decodeMove(pos, rec.Chosen)
		if err != nil {
			fatal("%s: decoding chosen move %q: %v", label, rec.Chosen, err)
		}
		if err := game.Move(move); err != nil {
			fatal("%s: illegal move %s: %v", label, rec.ChosenSAN, err)
		}
	}

	r.Outcome = game.Outcome().String()
	r.Method = game.Method().String()
	r.PGN = strings.TrimSpace(game.String())
	r.FinalFEN = game.FEN()
	r.summarize()
}

func positionFromFEN(fen string) (*chess.Position, error) {
	pos := &chess.Position{}
	if err := pos.UnmarshalText([]byte(fen)); err != nil {
		return nil, fmt.Errorf("bad FEN %q: %w", fen, err)
	}
	return pos, nil
}

// Council is one side: its agents, its decision rule, and its engines.
type Council struct {
	Side    chess.Color
	Arm     Arm
	Agents  []*Agent
	byID    map[string]*Agent
	engines []*Engine
	llmMode string
}

func newCouncil(side chess.Color, arm Arm, ps []Personality, enginePath string, opts map[string]string, baseDepth, reviewPenalty int, llm *LLM, llmMode string) (*Council, error) {
	c := &Council{Side: side, Arm: arm, byID: map[string]*Agent{}, llmMode: llmMode}
	if arm == ArmEngine {
		e, err := NewEngine(enginePath, opts)
		if err != nil {
			return nil, err
		}
		c.engines = append(c.engines, e)
		c.Agents = []*Agent{{Personality: Personality{ID: "engine", Name: "Engine"}, Side: side, engine: e, baseDepth: baseDepth}}
		c.byID["engine"] = c.Agents[0]
		return c, nil
	}
	for _, p := range ps {
		e, err := NewEngine(enginePath, opts)
		if err != nil {
			return nil, err
		}
		c.engines = append(c.engines, e)
		agent := &Agent{Personality: p, Side: side, engine: e, baseDepth: baseDepth, reviewPenalty: reviewPenalty}
		if llmMode != "off" {
			agent.llm = llm
		}
		c.Agents = append(c.Agents, agent)
		c.byID[p.ID] = agent
	}
	return c, nil
}

// Reset clears every agent's transposition table. Without this the engines
// carry hash state from one position into the next, and because different arms
// issue different sequences of searches, the same agent would return slightly
// different evaluations for the same position depending on what ran before it.
// That would silently unpair the comparison the whole design rests on.
func (c *Council) Reset() {
	for _, e := range c.engines {
		if err := e.NewGame(); err != nil {
			fmt.Fprintf(os.Stderr, "  [soft] engine reset failed: %v\n", err)
		}
	}
}

// agentIDs returns the council's agent identifiers.
func (c *Council) agentIDs() []string {
	ids := make([]string, 0, len(c.Agents))
	for _, a := range c.Agents {
		ids = append(ids, a.Personality.ID)
	}
	return ids
}

func (c *Council) Close() {
	for _, e := range c.engines {
		e.Close() //nolint:errcheck
	}
}

// PlyRecord is everything that happened on one side's turn.
type PlyRecord struct {
	Ply            int                  `json:"ply"`
	Label          string               `json:"label"` // e.g. "12. (White)"
	Side           string               `json:"side"`
	Arm            Arm                  `json:"arm"`
	FEN            string               `json:"fen"`
	Proposals      []Proposal           `json:"proposals"`
	Votes          []Vote               `json:"votes,omitempty"`
	FinalVotes     []Vote               `json:"final_votes,omitempty"`
	Analysis       *Analysis            `json:"analysis,omitempty"`
	DeliberationID string               `json:"deliberation_id,omitempty"`
	Chosen         string               `json:"chosen_uci"`
	ChosenSAN      string               `json:"chosen_san"`
	ChosenBy       string               `json:"chosen_by"` // "unanimous", "approval", "plurality", "engine"
	Unanimous      bool                 `json:"unanimous"`
	Switched       []string             `json:"switched,omitempty"` // agents whose final pick differed from their proposal
	Counterfactual map[string]string    `json:"counterfactual"`     // decision maker -> UCI it would have played
	Loss           map[string]int       `json:"loss"`               // decision maker -> centipawns lost vs reference best
	RefBest        string               `json:"reference_best_san"`
	RefEval        Eval                 `json:"reference_eval"`
	Seconds        float64              `json:"seconds"`
	InfoSets       *InfoSets            `json:"info_sets,omitempty"`
	GemProposed    bool                 `json:"gem_proposed,omitempty"` // its holder put the hidden gem on the table
	GemAdopted     bool                 `json:"gem_adopted,omitempty"`  // the group actually played it
	views          map[string]Candidate // uci -> reference-independent view, used only during scoring
}

// decisionID is a stable key for this decision, used for every pseudo-random
// choice so that the same position deals and tie-breaks identically no matter
// which arm is running or in what order.
func (rec *PlyRecord) decisionID() string {
	return rec.FEN
}

// Decide runs one side's decision procedure for one position. infoSets is nil
// under full information; under a hidden profile it restricts what each agent
// may search.
func (c *Council) Decide(ctx context.Context, run *Run, pos *chess.Position, label string, infoSets *InfoSets) (*PlyRecord, error) {
	start := time.Now()
	rec := &PlyRecord{
		Label:          label,
		Side:           sideName(pos.Turn()),
		Arm:            c.Arm,
		FEN:            pos.String(),
		Counterfactual: map[string]string{},
		Loss:           map[string]int{},
		InfoSets:       infoSets,
	}

	if c.Arm == ArmEngine {
		lines, err := c.Agents[0].engine.Analyze(pos.String(), SearchOpts{Depth: c.Agents[0].depth(), MultiPV: 1})
		if err != nil {
			return nil, err
		}
		move, err := decodeMove(pos, lines[0].UCI)
		if err != nil {
			return nil, err
		}
		rec.Chosen, rec.ChosenSAN, rec.ChosenBy = lines[0].UCI, chess.AlgebraicNotation{}.Encode(pos, move), "engine"
		rec.Seconds = time.Since(start).Seconds()
		return rec, nil
	}

	// 1. Every agent surveys the position — through its own engine, its own
	//    taste, and under a hidden profile only the moves it can see.
	shortlists := map[string][]Candidate{}
	for _, a := range c.Agents {
		var set []string
		if infoSets != nil {
			set = infoSets.Sets[a.Personality.ID]
		}
		list, err := a.Survey(pos, set)
		if err != nil {
			return nil, fmt.Errorf("%s survey: %w", a.Personality.ID, err)
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("%s found no legal moves", a.Personality.ID)
		}
		shortlists[a.Personality.ID] = list
	}

	// 2. Each states its case.
	for _, a := range c.Agents {
		p, err := a.Propose(pos, shortlists[a.Personality.ID], label)
		if err != nil {
			return nil, err
		}
		rec.Proposals = append(rec.Proposals, p)
		rec.Counterfactual["solo:"+a.Personality.ID] = p.Move.UCI
	}

	// The moves actually on the table.
	options := map[string]Candidate{}
	for _, p := range rec.Proposals {
		options[p.Move.UCI] = p.Move
	}
	rec.Unanimous = len(options) == 1
	rec.Counterfactual["plurality"] = plurality(rec.Proposals, options, rec.decisionID())
	rec.Counterfactual["random-dictator"] = randomDictator(rec.Proposals, rec.decisionID())
	if infoSets != nil {
		_, rec.GemProposed = options[infoSets.GemUCI]
	}

	// 3. Each agent forms a view of every move on the table, including the ones
	//    it did not think of. Without this an agent can only ever vote against
	//    a proposal it has not evaluated.
	//
	//    A move already in the agent's own shortlist costs nothing to revisit.
	//    One a peer has just introduced gets a fresh search at review depth —
	//    under a hidden profile that is the cost of receiving information
	//    secondhand.
	views := map[string]map[string]Candidate{} // agentID -> uci -> its view
	for _, a := range c.Agents {
		views[a.Personality.ID] = map[string]Candidate{}
		for uciMove := range options {
			if own := findCandidate(shortlists[a.Personality.ID], uciMove); own != nil {
				firsthand := *own
				firsthand.Firsthand = true
				views[a.Personality.ID][uciMove] = firsthand
				continue
			}
			assessed, err := a.Assess(pos, uciMove, false)
			if err != nil {
				return nil, fmt.Errorf("%s assessing %s: %w", a.Personality.ID, uciMove, err)
			}
			views[a.Personality.ID][uciMove] = assessed
		}
	}

	if c.Arm == ArmPlurality {
		return rec.finish(rec.Counterfactual["plurality"], "plurality", options, start), nil
	}

	// A unanimous table has nothing to deliberate about. Skipping it is the
	// difference between a runnable experiment and an unaffordable one.
	if rec.Unanimous && !run.AlwaysDelib {
		return rec.finish(rec.Counterfactual["plurality"], "unanimous", options, start), nil
	}

	// 4. Everyone votes on everyone else's proposal.
	rec.Votes = c.collectVotes(rec.Proposals, views, label)

	// 5. gemot analyses the disagreement.
	if run.gemot != nil {
		analysis, delibID, err := run.deliberate(ctx, c, pos, label, rec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [soft] %s deliberation failed, falling back to vote aggregation: %v\n", label, err)
		} else {
			rec.Analysis, rec.DeliberationID = analysis, delibID
		}
	}

	// 6. Each agent reconsiders in light of the analysis.
	backing := backingFrom(rec.Votes, len(c.Agents), options, rec.Analysis)
	finalPick := map[string]string{}
	for _, a := range c.Agents {
		own := views[a.Personality.ID][ownMove(rec.Proposals, a.Personality.ID)]
		pick := own.UCI
		if c.llmMode == "full" && rec.Analysis != nil && a.llm != nil {
			agentCtx := ""
			if run.gemot != nil && rec.DeliberationID != "" {
				agentCtx = truncate(run.gemot.Context(ctx, rec.DeliberationID, a.Personality.ID), 1500)
			}
			if chosen, _, err := a.llm.Reconsider(a.Personality, label, own, views[a.Personality.ID], rec.Analysis, agentCtx); err == nil {
				pick = chosen
			} else {
				fmt.Fprintf(os.Stderr, "  [soft] %s reconsider failed: %v\n", a.Personality.ID, err)
				pick, _ = a.Reconsider(own, views[a.Personality.ID], backing, run.Persuasion)
			}
		} else {
			pick, _ = a.Reconsider(own, views[a.Personality.ID], backing, run.Persuasion)
		}
		finalPick[a.Personality.ID] = pick
		if pick != own.UCI {
			rec.Switched = append(rec.Switched, a.Personality.ID)
		}
	}

	// 7. Approval tally over the final positions. Each agent scores every move
	//    on the table relative to its own final pick; the highest total wins.
	rec.FinalVotes = c.finalBallots(finalPick, views, options)
	rec.finish(tally(rec.FinalVotes, finalPick, views, options, rec.decisionID()), "approval", options, start)

	if run.gemot != nil && rec.DeliberationID != "" {
		run.gemot.Commit(ctx, rec.DeliberationID, c.Agents[0].Personality.ID,
			fmt.Sprintf("%s plays %s at %s", sideName(pos.Turn()), rec.ChosenSAN, label))
	}
	return rec, nil
}

// collectVotes has each agent vote on the others' proposals.
func (c *Council) collectVotes(proposals []Proposal, views map[string]map[string]Candidate, label string) []Vote {
	var votes []Vote
	for _, a := range c.Agents {
		ownUCI := ownMove(proposals, a.Personality.ID)
		own := views[a.Personality.ID][ownUCI]
		for _, p := range proposals {
			if p.AgentID == a.Personality.ID {
				continue
			}
			peerView := views[a.Personality.ID][p.Move.UCI]
			value, caveat := a.voteOn(own, peerView)
			if c.llmMode == "full" && a.llm != nil {
				if v, ca, err := a.llm.Vote(a.Personality, label, own, p, peerView); err == nil {
					value, caveat = v, ca
				} else {
					fmt.Fprintf(os.Stderr, "  [soft] %s vote failed: %v\n", a.Personality.ID, err)
				}
			}
			votes = append(votes, Vote{
				AgentID:  a.Personality.ID,
				TargetID: p.AgentID,
				MoveUCI:  p.Move.UCI,
				Value:    value,
				Caveat:   caveat,
			})
		}
	}
	return votes
}

// finalBallots scores every move on the table from every agent's post-analysis
// standpoint, including a +2 for its own final pick.
func (c *Council) finalBallots(finalPick map[string]string, views map[string]map[string]Candidate, options map[string]Candidate) []Vote {
	var ballots []Vote
	for _, a := range c.Agents {
		own := views[a.Personality.ID][finalPick[a.Personality.ID]]
		for uciMove := range options {
			if uciMove == own.UCI {
				ballots = append(ballots, Vote{AgentID: a.Personality.ID, MoveUCI: uciMove, Value: 2, Caveat: "my final choice"})
				continue
			}
			value, caveat := a.voteOn(own, views[a.Personality.ID][uciMove])
			ballots = append(ballots, Vote{AgentID: a.Personality.ID, MoveUCI: uciMove, Value: value, Caveat: caveat})
		}
	}
	return ballots
}

// finish records the decision and the shared bookkeeping every exit path needs.
func (rec *PlyRecord) finish(chosen, by string, options map[string]Candidate, start time.Time) *PlyRecord {
	rec.Chosen, rec.ChosenBy = chosen, by
	rec.ChosenSAN = options[chosen].SAN
	rec.views = flatten(options)
	rec.Seconds = time.Since(start).Seconds()
	if rec.InfoSets != nil {
		rec.GemAdopted = chosen == rec.InfoSets.GemUCI
	}
	return rec
}

// tally picks the winning move: highest approval total, then most first-choice
// ballots, then the cross-agent mean engine evaluation, then a deterministic
// coin.
//
// The evaluation tiebreak is legitimate here and only here: by this point every
// agent has assessed every move on the table, so the comparison uses genuinely
// pooled information. The plurality control cannot use it — see plurality.
func tally(ballots []Vote, finalPick map[string]string, views map[string]map[string]Candidate, options map[string]Candidate, decisionID string) string {
	approval := map[string]int{}
	firsts := map[string]int{}
	engineSum := map[string]int{}
	for _, b := range ballots {
		approval[b.MoveUCI] += b.Value
	}
	for _, pick := range finalPick {
		firsts[pick]++
	}
	for _, byMove := range views {
		for uciMove, c := range byMove {
			engineSum[uciMove] += c.Eval.Centipawns()
		}
	}
	return pickBest(options, decisionID+"|tally", func(a, b string) int {
		if approval[a] != approval[b] {
			return approval[a] - approval[b]
		}
		if firsts[a] != firsts[b] {
			return firsts[a] - firsts[b]
		}
		return engineSum[a] - engineSum[b]
	})
}

// backingFrom converts peer votes — and, when gemot ran, the analysis itself —
// into a 0-1 measure of how strongly the group stands behind each move. This is
// the only channel through which deliberation influences the offline agents, so
// it is deliberately narrow and inspectable.
func backingFrom(votes []Vote, agents int, options map[string]Candidate, analysis *Analysis) map[string]float64 {
	backing := map[string]float64{}
	if agents < 2 {
		return backing
	}
	maxSupport := float64(2 * (agents - 1)) // each peer can give at most +2
	sums := map[string]int{}
	for _, v := range votes {
		sums[v.MoveUCI] += v.Value
	}
	for uciMove := range options {
		backing[uciMove] = clamp01(float64(sums[uciMove]) / maxSupport)
	}

	// A move that gemot's analysis names in its consensus, bridging statements,
	// or compromise carries group weight beyond the raw vote count.
	if analysis == nil {
		return backing
	}
	named := strings.Join(append(append([]string{analysis.Compromise}, analysis.Consensus...), analysis.Bridging...), " ")
	if named == "" {
		return backing
	}
	tokens := map[string]bool{}
	for _, t := range strings.FieldsFunc(named, func(r rune) bool {
		return !strings.ContainsRune("abcdefghABCDEFGHKQRBNOPx12345678+#=-", r)
	}) {
		tokens[t] = true
	}
	for uciMove, c := range options {
		if tokens[c.SAN] {
			backing[uciMove] = maxFloat(backing[uciMove], 0.5)
		}
	}
	return backing
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// plurality is the no-discussion control: the most-proposed move, ties broken
// by a deterministic coin.
//
// The coin is not laziness. An earlier version broke ties by summed engine
// evaluation, which quietly handed the control the answer: under a hidden
// profile each agent proposes a different move, every move has exactly one
// proposer, and the gem holder's own evaluation is the highest — so a
// three-way split would resolve to the gem every time and "plurality" would
// look like it had pooled information it never received. Without discussion
// there is no shared basis for ranking singleton proposals, so there must not
// be one here either.
func plurality(proposals []Proposal, options map[string]Candidate, decisionID string) string {
	counts := map[string]int{}
	for _, p := range proposals {
		counts[p.Move.UCI]++
	}
	return pickBest(options, decisionID+"|plurality", func(a, b string) int {
		return counts[a] - counts[b]
	})
}

// randomDictator picks one agent's proposal at random — the classic
// strategyproof baseline, and the floor any aggregation rule should clear.
func randomDictator(proposals []Proposal, decisionID string) string {
	if len(proposals) == 0 {
		return ""
	}
	picks := make([]string, 0, len(proposals))
	for _, p := range proposals {
		picks = append(picks, p.Move.UCI)
	}
	sort.Strings(picks)
	return picks[hashIndex(decisionID+"|dictator", len(picks))]
}

// pickBest returns the highest-ranked move under cmp, breaking ties with a
// hash of the decision ID so the choice is arbitrary but reproducible — and
// carries no information about move quality.
func pickBest(options map[string]Candidate, salt string, cmp func(a, b string) int) string {
	moves := make([]string, 0, len(options))
	for uciMove := range options {
		moves = append(moves, uciMove)
	}
	sort.Strings(moves)
	if len(moves) == 0 {
		return ""
	}
	// Rotate by a hash so the tiebreak is not systematically lexicographic,
	// which would correlate with board geometry.
	offset := hashIndex(salt, len(moves))
	best := moves[offset]
	for i := 1; i < len(moves); i++ {
		candidate := moves[(offset+i)%len(moves)]
		if cmp(candidate, best) > 0 {
			best = candidate
		}
	}
	return best
}

func ownMove(proposals []Proposal, agentID string) string {
	for _, p := range proposals {
		if p.AgentID == agentID {
			return p.Move.UCI
		}
	}
	return ""
}

func findCandidate(list []Candidate, uciMove string) *Candidate {
	for i := range list {
		if list[i].UCI == uciMove {
			return &list[i]
		}
	}
	return nil
}

func flatten(options map[string]Candidate) map[string]Candidate {
	out := make(map[string]Candidate, len(options))
	for k, v := range options {
		out[k] = v
	}
	return out
}

// claimableDraw returns the draw a player could claim without the opponent's
// agreement, or NoMethod when there is none.
func claimableDraw(game *chess.Game) chess.Method {
	for _, m := range game.EligibleDraws() {
		if m == chess.ThreefoldRepetition || m == chess.FiftyMoveRule {
			return m
		}
	}
	return chess.NoMethod
}

func moveLabel(pos *chess.Position) string {
	// FEN's last field is the full move number.
	fields := strings.Fields(pos.String())
	number := "?"
	if len(fields) == 6 {
		number = fields[5]
	}
	return fmt.Sprintf("move %s (%s)", number, sideName(pos.Turn()))
}

// shortID abbreviates an identifier for console output without the ellipsis
// that truncate adds.
func shortID(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func sideName(c chess.Color) string {
	if c == chess.Black {
		return "Black"
	}
	return "White"
}

func personaIDs(ps []Personality) string {
	ids := make([]string, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	return strings.Join(ids, ", ")
}

func printPly(rec *PlyRecord) {
	var picks []string
	for _, p := range rec.Proposals {
		marker := ""
		if p.Move.UCI == rec.Chosen {
			marker = "*"
		}
		picks = append(picks, fmt.Sprintf("%s=%s%s", shortID(p.AgentID, 3), p.Move.SAN, marker))
	}
	loss := rec.Loss["played"]
	extra := ""
	if len(rec.Switched) > 0 {
		extra = fmt.Sprintf(" switched:%s", strings.Join(rec.Switched, ","))
	}
	if rec.DeliberationID != "" {
		extra += " delib:" + shortID(rec.DeliberationID, 8)
	}
	fmt.Printf("%-18s %-6s via %-9s loss %3dcp  [%s]%s\n",
		rec.Label, rec.ChosenSAN, rec.ChosenBy, loss, strings.Join(picks, " "), extra)
}

// loadDotEnv reads .env from the working directory and exports any key not
// already set in the environment (existing values win). Values may be quoted.
// The repo does not use godotenv; this matches the hand-rolled loader in the
// other scripts/ commands.
func loadDotEnv() {
	data, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
}

func gemotSecret() string {
	// .env is loaded into the environment by loadDotEnv at startup.
	return os.Getenv("GEMOT_API_SECRET")
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "chess-consensus: "+format+"\n", args...)
	os.Exit(1)
}

// deliberate drives one gemot deliberation for one ply.
func (r *Run) deliberate(ctx context.Context, c *Council, pos *chess.Position, label string, rec *PlyRecord) (*Analysis, string, error) {
	topic := fmt.Sprintf("%s to play at %s — which move?", sideName(pos.Turn()), label)
	description := fmt.Sprintf(
		"Three agents with different chess temperaments must agree on one move.\n\nPosition (FEN): %s\n\nEach agent has its own engine analysis and its own standing preferences. Moves on the table: %s.",
		pos.String(), strings.Join(sortedSANs(rec.Proposals), ", "))

	delibID, err := r.gemot.CreateDeliberation(ctx, topic, description, r.Template, r.GroupID)
	if err != nil {
		return nil, "", err
	}
	if err := r.gemot.SubmitProposals(ctx, delibID, c.byID, rec.Proposals); err != nil {
		return nil, delibID, err
	}
	if err := r.gemot.SubmitVotes(ctx, delibID, rec.Proposals, rec.Votes); err != nil {
		return nil, delibID, err
	}
	analysis, err := r.gemot.Analyze(ctx, delibID, 5*time.Minute)
	if err != nil {
		return nil, delibID, err
	}
	return analysis, delibID, nil
}

func sortedSANs(proposals []Proposal) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range proposals {
		if !seen[p.Move.SAN] {
			seen[p.Move.SAN] = true
			out = append(out, p.Move.SAN)
		}
	}
	sort.Strings(out)
	return out
}

// writeJSON is a small helper shared by the result writers.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
