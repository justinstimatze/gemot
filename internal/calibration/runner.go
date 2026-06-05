package calibration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
)

// Runner drives the calibration corpus through both the gemot fleet
// mechanism (deliberation + analysis + forced-choice compromise) and a
// single-agent solo baseline, scoring every question against the corpus
// ground truth. The same default model is used for fleet participants
// and the solo baseline so the comparison isolates the contribution of
// the aggregation mechanism rather than the model family.
type Runner struct {
	Svc          *deliberation.Service
	Client       *llm.Client
	GemotVersion string
	ModelVersion string
	Seed         int64
	NumAgents    int // default 5
	Concurrency  int // default 2
}

// agentChoice is the structured-output shape for one fleet agent's
// independent first-pass judgment on a corpus question.
type agentChoice struct {
	ChosenOption string `json:"chosen_option"`
	Rationale    string `json:"rationale"`
}

const agentSystemPrompt = "You are a careful judgment-making assistant participating in a multi-agent deliberation. You will be shown a question and a fixed set of options, and you must pick one option as your initial position. Other agents will see your reasoning and may agree or disagree."

const agentPromptTemplate = `Question:
%s

Options:
%s

Pick the option you believe is most correct. Give your reasoning in the rationale field; it will be visible to the other agents and may shape the collective deliberation.`

// agentChoose calls the LLM once to get one agent's independent choice.
func (r *Runner) agentChoose(ctx context.Context, agentID string, q Question) (*agentChoice, error) {
	var optionsText string
	for _, o := range q.Options {
		optionsText += "  - " + o + "\n"
	}
	prompt := fmt.Sprintf(agentPromptTemplate, q.Text, optionsText)

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"chosen_option": map[string]any{"type": "string", "enum": q.Options},
			"rationale":     map[string]any{"type": "string"},
		},
		"required": []string{"chosen_option", "rationale"},
	}
	var out agentChoice
	if err := r.Client.StructuredOutput(ctx, agentSystemPrompt, prompt, schema, &out); err != nil {
		return nil, fmt.Errorf("agent %s choose: %w", agentID, err)
	}
	return &out, nil
}

// formatPositionContent assembles the position body each agent submits.
// The [CHOICE:X] prefix is parsed back out when the runner computes the
// vote-only answer. The body otherwise contains the agent's free-form
// rationale so the analysis pipeline can extract claims / detect cruxes
// from it the same way it would for any free-text position.
func formatPositionContent(chosenOption, rationale string) string {
	return "[CHOICE:" + chosenOption + "]\n" + rationale
}

// parseChoice extracts the [CHOICE:X] prefix from a position body.
// Returns empty string if no prefix found.
func parseChoice(content string) string {
	if !strings.HasPrefix(content, "[CHOICE:") {
		return ""
	}
	end := strings.Index(content, "]")
	if end < 0 {
		return ""
	}
	return content[len("[CHOICE:"):end]
}

// runQuestion executes one corpus question end-to-end and returns its
// Result. Errors during fleet stages are converted to empty-string answers
// (which count as incorrect when scored), so a single bad question does
// not abort the whole run — the runner reports them in the Notes field
// for operator inspection.
func (r *Runner) runQuestion(ctx context.Context, q Question) Result {
	res := Result{QuestionID: q.ID}

	// 1) Solo baseline (single LLM call) — runs in parallel with the
	// fleet path to halve wall time per question.
	type soloResult struct {
		answer string
		err    error
	}
	soloCh := make(chan soloResult, 1)
	go func() {
		// Tag solo cost under a calibration namespace so the tracker can
		// separate it from the fleet's per-deliberation accounting.
		soloCtx := context.WithValue(ctx, deliberation.ContextKeyDeliberationID{}, "_calibration_solo:"+q.ID)
		answer, err := Solo(soloCtx, r.Client, q.Text, q.Options)
		soloCh <- soloResult{answer, err}
	}()

	// 2) Fleet path: create deliberation, agents pick + submit + vote,
	// analyze, propose compromise with forced choice.
	numAgents := r.NumAgents
	if numAgents <= 0 {
		numAgents = 5
	}
	d, err := r.Svc.CreateDeliberation(ctx, q.Text, "calibration corpus question "+q.ID,
		deliberation.WithType(q.DeliberationType),
	)
	if err != nil {
		res.Notes = "create deliberation: " + err.Error()
		<-soloCh
		return res
	}
	res.DeliberationID = d.ID

	// 3) Each agent independently picks an option + writes rationale.
	// Run concurrently for wall-time but the LLM client's semaphore
	// caps total in-flight calls cluster-wide.
	choices := make([]*agentChoice, numAgents)
	agentIDs := make([]string, numAgents)
	for i := range agentIDs {
		agentIDs[i] = fmt.Sprintf("calib_agent_%d", i+1)
	}
	{
		var wg sync.WaitGroup
		errs := make([]error, numAgents)
		for i := 0; i < numAgents; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ag, e := r.agentChoose(ctx, agentIDs[i], q)
				choices[i] = ag
				errs[i] = e
			}(i)
		}
		wg.Wait()
		for i, e := range errs {
			if e != nil {
				res.Notes = "agent " + agentIDs[i] + " choose: " + e.Error()
				<-soloCh
				return res
			}
		}
	}

	// 4) Submit each agent's position.
	positions := make([]*deliberation.Position, numAgents)
	for i, ag := range choices {
		content := formatPositionContent(ag.ChosenOption, ag.Rationale)
		p, err := r.Svc.SubmitPosition(ctx, d.ID, agentIDs[i], content)
		if err != nil {
			res.Notes = "submit position: " + err.Error()
			<-soloCh
			return res
		}
		positions[i] = p
	}

	// 5) Cross-voting: each agent votes on every position. +2 if the
	// position's tagged option matches the agent's own choice, -1 if
	// different (mild penalty rather than hard rejection — agents may
	// see merit in other rationales). Self-vote is +2 by definition.
	for i, voter := range agentIDs {
		voterChoice := choices[i].ChosenOption
		for j, p := range positions {
			value := -1
			if i == j {
				value = 2
			} else if parseChoice(p.Content) == voterChoice {
				value = 2
			}
			if err := r.Svc.Vote(ctx, d.ID, voter, p.ID, value, "", ""); err != nil {
				res.Notes = "vote: " + err.Error()
				<-soloCh
				return res
			}
		}
	}

	// 6) Run analysis with shortened taxonomy to keep per-question
	// time bounded. quick-panel uses 3 topics / 2 subtopics — same
	// values used by the expert_panel quick mode (internal/mcp/core.go).
	analyzeCtx := context.WithValue(ctx, analysis.ContextKeyMaxTopics{}, 3)
	analyzeCtx = context.WithValue(analyzeCtx, analysis.ContextKeyMaxSubtopics{}, 2)
	analyzeCtx = context.WithValue(analyzeCtx, deliberation.ContextKeyDeliberationID{}, d.ID)
	if _, err := r.Svc.Analyze(analyzeCtx, d.ID); err != nil {
		res.Notes = "analyze: " + err.Error()
		<-soloCh
		return res
	}

	// 7) Vote-only answer: position with the highest sum of incoming
	// votes (computed from each agent's vote tally above). Ties broken
	// by the order positions were submitted, which is in turn determined
	// by agent index — i.e., reproducible across runs.
	voteTotals := make(map[string]int)
	for _, p := range positions {
		choice := parseChoice(p.Content)
		for i := range agentIDs {
			voterChoice := choices[i].ChosenOption
			value := -1
			if voterChoice == choice {
				value = 2
			}
			voteTotals[choice] += value
		}
	}
	var voteOnlyAnswer string
	bestVote := math.MinInt
	for _, opt := range q.Options {
		if v := voteTotals[opt]; v > bestVote {
			bestVote = v
			voteOnlyAnswer = opt
		}
	}
	res.VoteOnlyAnswer = voteOnlyAnswer

	// 8) Fleet answer via forced-choice compromise.
	_, fleetAnswer, err := r.Svc.ProposeCompromiseWithChoice(analyzeCtx, d.ID, q.Options)
	if err != nil {
		res.Notes = "propose_compromise_with_choice: " + err.Error()
	} else {
		res.FleetAnswer = fleetAnswer
	}

	// 9) Solo baseline join.
	solo := <-soloCh
	if solo.err != nil {
		res.Notes = strings.TrimSpace(res.Notes + " solo: " + solo.err.Error())
	} else {
		res.SoloAnswer = solo.answer
	}

	// 10) Score. Ground truth is one of the option strings (A/B/C/D
	// keys are converted to the option text by the runner before
	// calling — see Run). Comparison is exact-match on option text.
	res.FleetCorrect = res.FleetAnswer == q.GroundTruth
	res.VoteOnlyCorrect = res.VoteOnlyAnswer == q.GroundTruth
	res.SoloCorrect = res.SoloAnswer == q.GroundTruth
	return res
}

// Run drives the full corpus, returning the completed Run with every
// per-question Result. The runner does not persist anything itself — the
// caller (CLI or release-CI job) decides whether to push to Postgres
// (via internal/store) or only embed/latest.json (via report.go).
func (r *Runner) Run(ctx context.Context, corpus *Corpus) (*Run, error) {
	if r.Svc == nil || r.Client == nil {
		return nil, fmt.Errorf("runner: Svc and Client are required")
	}
	concurrency := r.Concurrency
	if concurrency <= 0 {
		concurrency = 2
	}

	run := &Run{
		ID:            "calib_" + uuid.NewString(),
		CorpusVersion: corpus.Version,
		GemotVersion:  r.GemotVersion,
		ModelVersion:  r.ModelVersion,
		Seed:          r.Seed,
		StartedAt:     time.Now(),
		Results:       make([]Result, len(corpus.Questions)),
	}

	// Convert the corpus ground-truth (A/B/C/D keys) to the option text
	// so the runQuestion comparison can be exact-match on option text
	// without per-question conversion logic. Mutate a local copy of the
	// corpus rather than the embedded global to keep the embed pure.
	questions := make([]Question, len(corpus.Questions))
	copy(questions, corpus.Questions)
	for i := range questions {
		if gt := questions[i].GroundTruth; len(gt) == 1 && gt >= "A" && gt <= "D" {
			idx := int(gt[0] - 'A')
			if idx < len(questions[i].Options) {
				questions[i].GroundTruth = questions[i].Options[idx]
			}
		}
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, q := range questions {
		wg.Add(1)
		go func(i int, q Question) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			run.Results[i] = r.runQuestion(ctx, q)
		}(i, q)
	}
	wg.Wait()
	run.FinishedAt = time.Now()
	return run, nil
}

// MarshalRun is a convenience for tests/CLI that want the run as JSON.
func MarshalRun(r *Run) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
