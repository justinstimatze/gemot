package calibration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/config"
	"github.com/justinstimatze/gemot/internal/cost"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
	"github.com/justinstimatze/gemot/internal/store"
)

// CLI is the entry point for `gemot calibration <subcommand>`. The dispatch
// is intentionally simple — calibration has three operator-facing actions
// and no plans to grow more in v1.
func CLI(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:])
	case "report":
		return cmdReport(args[1:])
	case "questions":
		return cmdQuestions(args[1:])
	case "validate-solo":
		return cmdValidateSolo(args[1:])
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: gemot calibration <run|report|questions> [flags]")
	fmt.Fprintln(os.Stderr, "  run                  Run the embedded corpus through fleet + solo baseline; write embed/latest.json")
	fmt.Fprintln(os.Stderr, "  report               Print a human-readable summary of the latest run")
	fmt.Fprintln(os.Stderr, "  questions            List corpus questions (id, type, source)")
	fmt.Fprintln(os.Stderr, "  validate-solo        Re-run bare Solo + chain-of-thought Solo on public corpus; check shipped baseline")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Environment:")
	fmt.Fprintln(os.Stderr, "  GEMOT_CALIBRATION_CORPUS  override path to a corpus JSON (development)")
	fmt.Fprintln(os.Stderr, "  GEMOT_CALIBRATION_OUT     override path to write the embedded-run JSON (default: ./calibration_latest.json)")
}

func cmdRun(_ []string) int {
	cfg := config.Load()
	if cfg.AnthropicKey == "" {
		fmt.Fprintln(os.Stderr, "calibration: ANTHROPIC_API_KEY is required")
		return 1
	}

	// Backend — Postgres if DATABASE_URL set, memory otherwise. The
	// runner does not require Postgres, but a self-hoster running a
	// benchmark probably wants the run history persisted.
	demoMode := os.Getenv("DATABASE_URL") == ""
	var backend store.Backend
	if demoMode {
		backend = store.NewMemoryStore()
		fmt.Fprintln(os.Stderr, "calibration: running against in-memory store (DATABASE_URL unset)")
	} else {
		pg, err := store.Open(cfg.DatabaseURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "calibration: opening database: %v\n", err)
			return 1
		}
		defer pg.Close() //nolint:errcheck
		backend = pg
	}

	tracker := cost.NewTracker()
	var totalInput, totalOutput int64
	var costMu sync.Mutex
	client := llm.NewClient(cfg.AnthropicKey, cfg.Model)
	client.OnUsage = func(ctx context.Context, input, output int) {
		delibID := "_calibration"
		if id, ok := ctx.Value(deliberation.ContextKeyDeliberationID{}).(string); ok {
			delibID = id
		}
		tracker.Record(delibID, cfg.Model, input, output)
		costMu.Lock()
		totalInput += int64(input)
		totalOutput += int64(output)
		costMu.Unlock()
	}

	synth := analysis.NewSynthesizer(client)
	synth.SetCache(store.NewLLMCache(backend, 24*time.Hour))
	svc := deliberation.NewService(backend, synth)
	svc.SetCompromiseGenerator(synth)
	svc.SetReframer(synth)

	corpus, err := LoadCorpus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "calibration: loading corpus: %v\n", err)
		return 1
	}
	if len(corpus.Questions) == 0 {
		fmt.Fprintln(os.Stderr, "calibration: corpus is empty — author internal/calibration/corpus/v1.json before running")
		return 1
	}

	runner := &Runner{
		Svc:          svc,
		Client:       client,
		GemotVersion: gemotVersion(),
		ModelVersion: cfg.Model,
		Seed:         1, // deterministic across reruns; the LLM itself is not seedable
	}

	ctx := context.Background()
	fmt.Fprintf(os.Stderr, "calibration: running %d questions (concurrency %d)\n", len(corpus.Questions), runner.Concurrency)
	run, err := runner.Run(ctx, corpus)
	if err != nil {
		fmt.Fprintf(os.Stderr, "calibration: run failed: %v\n", err)
		return 1
	}

	// Persist to Postgres if we have it.
	if pg, ok := backend.(*store.DB); ok {
		_ = persistRun(ctx, pg, run, corpus)
	}

	// Build the embedded JSON and write it.
	er := BuildEmbeddedRun(run, corpus)
	outPath := os.Getenv("GEMOT_CALIBRATION_OUT")
	if outPath == "" {
		outPath = "calibration_latest.json"
	}
	f, err := os.Create(outPath) //nolint:gosec // operator-provided path
	if err != nil {
		fmt.Fprintf(os.Stderr, "calibration: creating output: %v\n", err)
		return 1
	}
	defer f.Close() //nolint:errcheck
	if err := WriteEmbeddedRun(f, er); err != nil {
		fmt.Fprintf(os.Stderr, "calibration: writing output: %v\n", err)
		return 1
	}

	// Discrimination check — Step 12 of the plan: fail loudly if no
	// question separates fleet from solo. The point of calibration is
	// to measure mechanism lift; a corpus where fleet=solo on every
	// question has no signal, regardless of how high the rates are.
	if discriminating := countDiscriminating(run); discriminating == 0 {
		fmt.Fprintln(os.Stderr, "calibration: WARNING: no question separated fleet from solo — corpus is non-discriminating and needs revision before publishing the rate")
	}

	fmt.Println(FormatReport(run, corpus))
	fmt.Fprintf(os.Stderr, "calibration: wrote %s\n", outPath)
	costMu.Lock()
	fmt.Fprintf(os.Stderr, "calibration: token totals — %d input + %d output across all LLM calls\n", totalInput, totalOutput)
	costMu.Unlock()
	return 0
}

func cmdReport(_ []string) int {
	cfg := config.Load()
	if os.Getenv("DATABASE_URL") == "" {
		// In demo mode there's no persistent run history — show the
		// embedded snapshot instead.
		er, err := LoadEmbeddedRun()
		if err != nil {
			fmt.Fprintf(os.Stderr, "calibration: loading embedded run: %v\n", err)
			return 1
		}
		printEmbeddedRun(er)
		return 0
	}
	pg, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "calibration: opening database: %v\n", err)
		return 1
	}
	defer pg.Close() //nolint:errcheck

	ctx := context.Background()
	row, err := pg.LoadLatestCalibrationRun(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "calibration: no finished runs in the database — try `gemot calibration run` first")
		return 1
	}
	results, err := pg.LoadCalibrationResults(ctx, row.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "calibration: loading results: %v\n", err)
		return 1
	}
	corpus, err := LoadCorpus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "calibration: loading corpus: %v\n", err)
		return 1
	}
	run := &Run{
		ID:            row.ID,
		CorpusVersion: row.CorpusVersion,
		GemotVersion:  row.GemotVersion,
		ModelVersion:  row.ModelVersion,
		Seed:          row.Seed,
		StartedAt:     row.StartedAt,
	}
	if row.FinishedAt != nil {
		run.FinishedAt = *row.FinishedAt
	}
	for _, r := range results {
		run.Results = append(run.Results, Result{
			QuestionID:      r.QuestionID,
			FleetAnswer:     r.FleetAnswer,
			FleetCorrect:    r.FleetCorrect,
			VoteOnlyAnswer:  r.VoteOnlyAnswer,
			VoteOnlyCorrect: r.VoteOnlyCorrect,
			SoloAnswer:      r.SoloAnswer,
			SoloCorrect:     r.SoloCorrect,
			DeliberationID:  r.DeliberationID,
			Notes:           r.Notes,
		})
	}
	fmt.Println(FormatReport(run, corpus))
	return 0
}

// cmdValidateSolo is the TIER 1 follow-up to the shipped v2 baseline.
// Literature reports Sonnet 4.6 ~70% on GPQA Diamond with chain-of-thought;
// the shipped Solo() got 32% with bare prompting. This subcommand runs
// both bare Solo and SoloCoT head-to-head on the public corpus subset in
// one session, so any baseline shift attributable to prompting is visible
// independent of session/timing noise. Read-only: writes nothing to disk
// or Postgres, only prints a summary.
func cmdValidateSolo(_ []string) int {
	cfg := config.Load()
	if cfg.AnthropicKey == "" {
		fmt.Fprintln(os.Stderr, "calibration: ANTHROPIC_API_KEY is required")
		return 1
	}
	client := llm.NewClient(cfg.AnthropicKey, cfg.Model)

	corpus, err := LoadCorpus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "calibration: loading corpus: %v\n", err)
		return 1
	}

	// Public subset only, same as the shipped reference class.
	public := make([]Question, 0, len(corpus.Questions))
	for _, q := range corpus.Questions {
		if !q.HeldOut {
			public = append(public, q)
		}
	}
	// Convert ground-truth letters → option text, matching runner.Run.
	for i := range public {
		if gt := public[i].GroundTruth; len(gt) == 1 && gt >= "A" && gt <= "D" {
			idx := int(gt[0] - 'A')
			if idx < len(public[i].Options) {
				public[i].GroundTruth = public[i].Options[idx]
			}
		}
	}

	type pairResult struct {
		idx         int
		bareAnswer  string
		cotAnswer   string
		bareCorrect bool
		cotCorrect  bool
		bareErr     error
		cotErr      error
	}

	fmt.Fprintf(os.Stderr, "calibration: validate-solo on %d public questions (corpus %s, model %s)\n",
		len(public), corpus.Version, cfg.Model)

	ctx := context.Background()
	sem := make(chan struct{}, 4)
	results := make([]pairResult, len(public))
	var wg sync.WaitGroup
	for i, q := range public {
		wg.Add(1)
		go func(i int, q Question) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			pr := pairResult{idx: i}
			var inner sync.WaitGroup
			inner.Add(2)
			go func() {
				defer inner.Done()
				a, err := Solo(ctx, client, q.Text, q.Options)
				pr.bareAnswer, pr.bareErr = a, err
			}()
			go func() {
				defer inner.Done()
				a, _, err := SoloCoT(ctx, client, q.Text, q.Options)
				pr.cotAnswer, pr.cotErr = a, err
			}()
			inner.Wait()
			pr.bareCorrect = pr.bareErr == nil && pr.bareAnswer == q.GroundTruth
			pr.cotCorrect = pr.cotErr == nil && pr.cotAnswer == q.GroundTruth
			results[i] = pr
			status := "."
			if pr.bareErr != nil || pr.cotErr != nil {
				status = "!"
			}
			fmt.Fprintf(os.Stderr, "  %s [%2d/%2d] %s bare=%v cot=%v\n",
				status, i+1, len(public), q.ID, pr.bareCorrect, pr.cotCorrect)
		}(i, q)
	}
	wg.Wait()

	bareOK, cotOK, n := 0, 0, len(public)
	var bareErrs, cotErrs int
	for _, r := range results {
		if r.bareCorrect {
			bareOK++
		}
		if r.cotCorrect {
			cotOK++
		}
		if r.bareErr != nil {
			bareErrs++
		}
		if r.cotErr != nil {
			cotErrs++
		}
	}

	bareCI := WilsonInterval(bareOK, n)
	cotCI := WilsonInterval(cotOK, n)

	fmt.Printf("\nValidate-solo head-to-head (corpus %s, n=%d)\n", corpus.Version, n)
	fmt.Println(strings.Repeat("-", 72))
	fmt.Printf("%-24s %5s %10s %14s\n", "prompt", "ok", "rate", "ci_95")
	fmt.Println(strings.Repeat("-", 72))
	fmt.Printf("%-24s %5d %9.1f%% [%.2f, %.2f]\n", "bare (shipped Solo)", bareOK, float64(bareOK)/float64(n)*100, bareCI[0], bareCI[1])
	fmt.Printf("%-24s %5d %9.1f%% [%.2f, %.2f]\n", "chain-of-thought", cotOK, float64(cotOK)/float64(n)*100, cotCI[0], cotCI[1])
	fmt.Println(strings.Repeat("-", 72))

	// Compare to shipped baseline from embedded run.
	if er, err := LoadEmbeddedRun(); err == nil && er != nil {
		if rc, ok := er.ReferenceClasses["reasoning"]; ok && rc.N > 0 {
			fmt.Printf("\nShipped embed/latest.json reasoning bucket: solo=%.1f%% fleet=%.1f%% n=%d\n",
				rc.SoloBaselineRate*100, rc.Rate*100, rc.N)
			shippedLift := (rc.Rate - rc.SoloBaselineRate) * 100
			cotRate := float64(cotOK) / float64(n)
			impliedLift := (rc.Rate - cotRate) * 100
			fmt.Printf("Shipped fleet-vs-solo lift: %+.1fpp\n", shippedLift)
			fmt.Printf("Implied fleet-vs-CoT-solo lift (using CoT as baseline): %+.1fpp\n", impliedLift)
			if impliedLift < 0 && shippedLift > 0 {
				fmt.Println("\nVERDICT: shipped lift INVERTS under CoT baseline. The published fleet-vs-solo rate is misleading and should be revised.")
			} else if impliedLift < shippedLift-5 {
				fmt.Printf("\nVERDICT: shipped lift shrinks by %.1fpp under CoT baseline; finding is materially weakened.\n", shippedLift-impliedLift)
			} else {
				fmt.Println("\nVERDICT: shipped lift survives the CoT baseline check.")
			}
		}
	}

	if bareErrs > 0 || cotErrs > 0 {
		fmt.Fprintf(os.Stderr, "calibration: warning — bare errors=%d cot errors=%d (counted as incorrect)\n", bareErrs, cotErrs)
	}
	return 0
}

func cmdQuestions(_ []string) int {
	corpus, err := LoadCorpus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "calibration: loading corpus: %v\n", err)
		return 1
	}
	qs := make([]Question, len(corpus.Questions))
	copy(qs, corpus.Questions)
	sort.Slice(qs, func(i, j int) bool { return qs[i].ID < qs[j].ID })
	fmt.Printf("Corpus version %s — %d questions (%d held-out)\n\n", corpus.Version, len(qs), countHeldOut(qs))
	fmt.Printf("%-12s %-12s %-12s %-6s %s\n", "id", "type", "source", "held?", "question")
	fmt.Println(strings.Repeat("-", 100))
	for _, q := range qs {
		held := "no"
		if q.HeldOut {
			held = "yes"
		}
		text := q.Text
		if len(text) > 50 {
			text = text[:50] + "..."
		}
		fmt.Printf("%-12s %-12s %-12s %-6s %s\n", q.ID, q.DeliberationType, q.Source, held, text)
	}
	return 0
}

func countHeldOut(qs []Question) int {
	n := 0
	for _, q := range qs {
		if q.HeldOut {
			n++
		}
	}
	return n
}

func countDiscriminating(run *Run) int {
	n := 0
	for _, r := range run.Results {
		if r.FleetAnswer != "" && r.SoloAnswer != "" && r.FleetAnswer != r.SoloAnswer {
			n++
		}
	}
	return n
}

func printEmbeddedRun(er *EmbeddedRun) {
	fmt.Printf("Embedded calibration snapshot — corpus %s, gemot %s, model %s, measured %s\n",
		er.CorpusVersion, er.GemotVersion, er.ModelVersion, er.MeasuredAt.Format(time.RFC3339))
	if len(er.ReferenceClasses) == 0 {
		fmt.Println("(no reference classes — embed/latest.json is the placeholder; run `gemot calibration run` to populate)")
		return
	}
	types := make([]string, 0, len(er.ReferenceClasses))
	for k := range er.ReferenceClasses {
		types = append(types, k)
	}
	sort.Strings(types)
	for _, t := range types {
		rc := er.ReferenceClasses[t]
		fmt.Printf("  %-12s n=%-4d fleet=%.1f%% vote_only=%.1f%% solo=%.1f%% ci_95=[%.2f, %.2f]\n",
			t, rc.N, rc.Rate*100, rc.VoteOnlyRate*100, rc.SoloBaselineRate*100, rc.CI95[0], rc.CI95[1],
		)
	}
}

// persistRun writes the run + results to Postgres, mirroring the corpus
// questions for searchability. Failure is non-fatal — the embed/JSON
// path is the authoritative output of `gemot calibration run`.
func persistRun(ctx context.Context, pg *store.DB, run *Run, corpus *Corpus) error {
	runRow := store.CalibrationRunRow{
		ID:            run.ID,
		CorpusVersion: run.CorpusVersion,
		GemotVersion:  run.GemotVersion,
		ModelVersion:  run.ModelVersion,
		Seed:          run.Seed,
		StartedAt:     run.StartedAt,
	}
	if err := pg.InsertCalibrationRun(ctx, runRow); err != nil {
		return err
	}
	for _, q := range corpus.Questions {
		optsJSON, _ := store.MarshalOptions(q.Options)
		tagsJSON, _ := store.MarshalTags(q.Tags)
		_ = pg.UpsertCalibrationQuestion(ctx, store.CalibrationQuestionRow{
			ID:               q.ID,
			CorpusVersion:    q.CorpusVersion,
			QuestionText:     q.Text,
			OptionsJSON:      optsJSON,
			GroundTruth:      q.GroundTruth,
			Source:           q.Source,
			SourceRef:        q.SourceRef,
			DeliberationType: q.DeliberationType,
			HeldOut:          q.HeldOut,
			TagsJSON:         tagsJSON,
		})
	}
	totalFleet, totalVote, totalSolo, totalN := 0, 0, 0, 0
	for _, r := range run.Results {
		row := store.CalibrationResultRow{
			RunID:           run.ID,
			QuestionID:      r.QuestionID,
			FleetAnswer:     r.FleetAnswer,
			FleetCorrect:    r.FleetCorrect,
			VoteOnlyAnswer:  r.VoteOnlyAnswer,
			VoteOnlyCorrect: r.VoteOnlyCorrect,
			SoloAnswer:      r.SoloAnswer,
			SoloCorrect:     r.SoloCorrect,
			DeliberationID:  r.DeliberationID,
			Notes:           r.Notes,
		}
		_ = pg.InsertCalibrationResult(ctx, row)
		totalN++
		if r.FleetCorrect {
			totalFleet++
		}
		if r.VoteOnlyCorrect {
			totalVote++
		}
		if r.SoloCorrect {
			totalSolo++
		}
	}
	if totalN > 0 {
		_ = pg.FinishCalibrationRun(ctx, run.ID,
			float64(totalFleet)/float64(totalN),
			float64(totalVote)/float64(totalN),
			float64(totalSolo)/float64(totalN),
			totalN, time.Now())
	}
	return nil
}

// gemotVersion reads the gemot version from a build-time env var, or
// returns "dev" if unset. CI sets this to the release tag.
func gemotVersion() string {
	if v := os.Getenv("GEMOT_VERSION"); v != "" {
		return v
	}
	return "dev"
}

// MarshalRunIndent is a JSON-encoding convenience used by tests.
func MarshalRunIndent(r *Run) (string, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
