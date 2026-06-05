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

// agentSystemPrompt elicits the same chain-of-thought reasoning the
// SoloCoT baseline uses, so the fleet-vs-solo comparison isolates the
// deliberation-and-compromise mechanism rather than measuring CoT vs
// bare prompting. The 2026-06-04 rollback documented why this matters:
// asymmetric prompting between fleet and solo can flip the sign of the
// measured lift. See docs/calibration.md.
const agentSystemPrompt = "You are an expert participating in a multi-agent deliberation on a graduate-level science question. Always reason step-by-step before committing to an answer; show your work in the rationale so the other agents can engage with it. Never guess."

const agentPromptTemplate = `You are answering a graduate-level science question as one of several agents who will independently take a position before deliberating.

Question:
%s

Options:
%s

Reason step-by-step:
1. Identify what the question is actually asking.
2. List the relevant principles, formulas, or facts.
3. Work through the analysis explicitly, including any numerical computation.
4. Evaluate each option against your analysis.
5. State your final answer.

Put the full step-by-step reasoning in the rationale field; it will be visible to the other agents and may shape the collective deliberation. Then set chosen_option to your final answer (verbatim, one of the listed options).`

// agentRevise is round 2. The agent sees the question + options as before,
// plus its own initial choice/rationale, the OTHER agents' choices and
// rationales (which it hasn't seen before), and the cruxes surfaced by
// analysis. It then re-picks via the same enum-constrained schema —
// either confirming or changing its choice.
//
// The prompt is intentionally neutral on stay-vs-change: the point is to
// measure whether agents *can* update on each other when given the
// opportunity, not to push them toward agreement. Sycophancy/cascade risk
// is real, so we frame revision as "did anyone make a stronger argument?"
// rather than "consider whether you were wrong" or "see what the majority
// thinks". The change-rate is itself a measurement — if zero agents ever
// change, the deliberation framing is mechanically empty on this corpus.
const reviseSystemPrompt = "You are an expert in a multi-agent deliberation, now in the revision round. You will see the other agents' initial rationales and the cruxes (disagreements) the analysis surfaced. You may keep your original answer or change it. Change only if another agent made a genuinely stronger argument or if a crux reveals a flaw in your reasoning. Independence is valued — do not change just because others disagreed."

const revisePromptTemplate = `You initially answered a graduate-level science question. Now you can revise your choice after seeing what the other agents argued.

Question:
%s

Options:
%s

YOUR INITIAL CHOICE: %s
YOUR INITIAL RATIONALE:
%s

OTHER AGENTS' POSITIONS:
%s

CRUXES (disagreements detected by analysis):
%s

Reason step-by-step:
1. Restate the core of your initial reasoning.
2. Identify the strongest opposing argument from another agent.
3. Identify any crux that reveals a real flaw in your reasoning (if any).
4. Decide: keep your initial choice, or change. Independence is fine — keep your answer if no one made a genuinely stronger case.
5. State your final answer.

Set chosen_option to your FINAL answer (verbatim, one of the listed options). Put your step-by-step reasoning in rationale.`

func (r *Runner) agentRevise(ctx context.Context, agentID string, q Question, ownChoice, ownRationale, othersText, cruxesText string) (*agentChoice, error) {
	var optionsText string
	for _, o := range q.Options {
		optionsText += "  - " + o + "\n"
	}
	prompt := fmt.Sprintf(revisePromptTemplate, q.Text, optionsText, ownChoice, ownRationale, othersText, cruxesText)
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"chosen_option": map[string]any{"type": "string", "enum": q.Options},
			"rationale":     map[string]any{"type": "string"},
		},
		"required": []string{"chosen_option", "rationale"},
	}
	var out agentChoice
	if err := r.Client.StructuredOutput(ctx, reviseSystemPrompt, prompt, schema, &out); err != nil {
		return nil, fmt.Errorf("agent %s revise: %w", agentID, err)
	}
	return &out, nil
}

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
	// fleet path to halve wall time per question. Uses SoloCoT (not bare
	// Solo) so the comparison is symmetric with the CoT prompting used by
	// agentChoose; the 2026-06-04 rollback showed bare-vs-CoT asymmetry
	// can flip the sign of the measured fleet-vs-solo lift. The bare Solo()
	// path remains exposed for `validate-solo` so future drift between
	// bare and CoT baselines stays measurable.
	type soloResult struct {
		answer string
		err    error
	}
	soloCh := make(chan soloResult, 1)
	go func() {
		soloCtx := context.WithValue(ctx, deliberation.ContextKeyDeliberationID{}, "_calibration_solo:"+q.ID)
		answer, _, err := SoloCoT(soloCtx, r.Client, q.Text, q.Options)
		soloCh <- soloResult{answer, err}
	}()

	// 2) Fleet path: create deliberation, agents pick + submit + vote,
	// analyze, propose compromise with forced choice. The topic is just
	// the question ID — keeping it short stays under the service's 500-char
	// topic cap (a UI ergonomic, not a system limit) for GPQA questions
	// that can run to 1500+ chars. The full question text is in the
	// description and is also re-stated in each agent's prompt.
	numAgents := r.NumAgents
	if numAgents <= 0 {
		numAgents = 5
	}
	d, err := r.Svc.CreateDeliberation(ctx, "calibration corpus question "+q.ID, q.Text,
		deliberation.WithType(q.DeliberationType),
	)
	if err != nil {
		res.Notes = "create deliberation: " + err.Error()
		solo := <-soloCh
		if solo.err == nil {
			res.SoloAnswer = solo.answer
			res.SoloCorrect = res.SoloAnswer == q.GroundTruth
		}
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
				solo := <-soloCh
				if solo.err == nil {
					res.SoloAnswer = solo.answer
					res.SoloCorrect = res.SoloAnswer == q.GroundTruth
				}
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
			solo := <-soloCh
			if solo.err == nil {
				res.SoloAnswer = solo.answer
				res.SoloCorrect = res.SoloAnswer == q.GroundTruth
			}
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
				solo := <-soloCh
				if solo.err == nil {
					res.SoloAnswer = solo.answer
					res.SoloCorrect = res.SoloAnswer == q.GroundTruth
				}
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
	analysisResult, err := r.Svc.Analyze(analyzeCtx, d.ID)
	if err != nil {
		res.Notes = "analyze: " + err.Error()
		solo := <-soloCh
		if solo.err == nil {
			res.SoloAnswer = solo.answer
			res.SoloCorrect = res.SoloAnswer == q.GroundTruth
		}
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

	// 7.5) Revision round (MVP test, 2026-06-05). Each agent re-picks
	// after seeing the other agents' rationales and the cruxes. The
	// fleet path below continues to use round-1 choices so the existing
	// fleet/vote_only measurements stay comparable; the new RevisedAnswer
	// captures the round-2 plurality. The mechanism's deliberation claim
	// is only mechanically meaningful if revision changes any answers —
	// if ChangedCount is 0 across the corpus, the framing is empty here.
	var cruxesText string
	for i, c := range analysisResult.Cruxes {
		cruxesText += fmt.Sprintf("%d. %s\n   Agree: %v\n   Disagree: %v\n\n",
			i+1, c.Claim, c.AgreeAgents, c.DisagreeAgents)
	}
	if cruxesText == "" {
		cruxesText = "(No cruxes detected — agents agreed at the claim level.)"
	}
	revisedChoices := make([]*agentChoice, numAgents)
	{
		var wg sync.WaitGroup
		revisedErrs := make([]error, numAgents)
		for i := 0; i < numAgents; i++ {
			i := i
			var othersText string
			for j := 0; j < numAgents; j++ {
				if j == i {
					continue
				}
				othersText += fmt.Sprintf("---\nAgent %s chose: %s\nRationale:\n%s\n",
					agentIDs[j], choices[j].ChosenOption, choices[j].Rationale)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				rev, e := r.agentRevise(ctx, agentIDs[i], q,
					choices[i].ChosenOption, choices[i].Rationale,
					othersText, cruxesText)
				revisedChoices[i] = rev
				revisedErrs[i] = e
			}()
		}
		wg.Wait()
		for i, e := range revisedErrs {
			if e != nil || revisedChoices[i] == nil {
				// On revision error, fall back to round-1 choice rather
				// than dropping the agent — keeps n=5 per question.
				revisedChoices[i] = choices[i]
			}
		}
	}
	revisedCounts := make(map[string]int)
	for _, rc := range revisedChoices {
		revisedCounts[rc.ChosenOption]++
	}
	for i := range revisedChoices {
		if revisedChoices[i].ChosenOption != choices[i].ChosenOption {
			res.ChangedCount++
		}
	}
	var revisedAnswer string
	bestCount := -1
	for _, opt := range q.Options {
		if c := revisedCounts[opt]; c > bestCount {
			bestCount = c
			revisedAnswer = opt
		}
	}
	res.RevisedAnswer = revisedAnswer
	res.RevisedCorrect = revisedAnswer == q.GroundTruth

	// 8) Fleet answer. Short-circuit when all agents picked the same
	// option: that's a 5/5 consensus and the LLM compromise step has no
	// claim-level disagreement to resolve. The 2026-06-05 Haiku run
	// surfaced 9 questions where vote-only was right and the compromise
	// step overrode the unanimous agent vote with a different option —
	// the compromise prompt never saw the agents' explicit choices, only
	// the cluster/crux analysis, which can diverge from option-level
	// agreement on the intermediate reasoning even when the final
	// answer is unanimous.
	choiceCounts := make(map[string]int)
	for _, ag := range choices {
		choiceCounts[ag.ChosenOption]++
	}
	if len(choiceCounts) == 1 {
		for opt := range choiceCounts {
			res.FleetAnswer = opt
		}
	} else {
		_, fleetAnswer, err := r.Svc.ProposeCompromiseWithChoiceAndVotes(analyzeCtx, d.ID, q.Options, choiceCounts)
		if err != nil {
			res.Notes = "propose_compromise_with_choice: " + err.Error()
		} else {
			res.FleetAnswer = fleetAnswer
		}
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
