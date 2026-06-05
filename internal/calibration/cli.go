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
