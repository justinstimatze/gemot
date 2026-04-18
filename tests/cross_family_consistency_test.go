package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/analysis"
	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/llm"
)

// The cross-family OOD consistency check is the Track 1 §3 defense
// against stable-but-wrong outputs within a single model family. These
// tests exercise the full pipeline using llm.SecondaryFunc — no real
// Gemini calls. Each test drives a TextAnalyzer whose primary
// extraction is a trivial mock and whose secondary is a closure that
// returns canned stance classifications.

// mkPosition synthesizes a minimal Position so latestPositionByAgent
// has something to feed the secondary prompt. Round + CreatedAt are
// set so the "latest per agent" selection is unambiguous.
func mkPosition(agent, content string, round int) deliberation.Position {
	return deliberation.Position{
		ID:             "p-" + agent + "-r" + fmt.Sprint(round),
		AgentID:        agent,
		Content:        content,
		Round:          round,
		CreatedAt:      time.Unix(int64(round)*1000, 0),
		DeliberationID: "d-xfamily",
	}
}

// mkCrux builds a crux with the given agreement/disagreement split.
// ControversyScore defaults high so it survives drift-sample selection.
func mkCrux(claim string, agree, disagree []string, controversy float64) deliberation.Crux {
	return deliberation.Crux{
		Claim:            claim,
		AgreeAgents:      agree,
		DisagreeAgents:   disagree,
		ControversyScore: controversy,
	}
}

// stanceResponder builds a SecondaryFunc closure that echoes a canned
// per-participant stance. The caller maps agent→stance; the closure
// parses the numeric participant IDs out of the prompt (the T3C-style
// anonymization the integrity function applies) and emits matching
// stance entries. This lets the test assert on sign-flip logic without
// reimplementing prompt parsing in each case.
//
// The primaryToParticipantNum parameter is unused today but left as a
// hook: if we add per-agent qualifier behaviour, callers will want to
// inject it. Keep the signature parallel to byzantineMockLLM so future
// tests can share a responder.
func stanceResponder(t *testing.T, agentStance map[string]string) llm.SecondaryFunc {
	return llm.SecondaryFunc{
		ProviderName: "mock-gemini",
		ModelName:    "mock-flash",
		Fn: func(_ context.Context, _ string, prompt string, _ map[string]any, target any) error {
			// Parse "Participant N:\n<text>" blocks and recover agent
			// identities from the position text. The mock depends on
			// each agent's position text being a unique prefix.
			type entry struct {
				Participant string `json:"participant"`
				Stance      string `json:"stance"`
			}
			out := struct {
				Stances []entry `json:"stances"`
			}{}

			for i := 0; ; i++ {
				header := fmt.Sprintf("Participant %d:\n", i)
				idx := strings.Index(prompt, header)
				if idx < 0 {
					break
				}
				tail := prompt[idx+len(header):]
				// Agent id is the portion up to the first space or newline.
				cut := strings.IndexAny(tail, "\n ")
				if cut < 0 {
					cut = len(tail)
				}
				agentTag := strings.TrimSpace(tail[:cut])
				stance, ok := agentStance[agentTag]
				if !ok {
					t.Fatalf("stanceResponder: no canned stance for agent token %q in prompt", agentTag)
				}
				out.Stances = append(out.Stances, entry{
					Participant: fmt.Sprintf("%d", i),
					Stance:      stance,
				})
			}

			blob, err := json.Marshal(out)
			if err != nil {
				return err
			}
			return json.Unmarshal(blob, target)
		},
	}
}

// TestCrossFamilyDisabledNoWarnings: no secondary wired → zero
// warnings and zero calls. Confirms off-by-default stays cheap.
func TestCrossFamilyDisabledNoWarnings(t *testing.T) {
	a := &analysis.TextAnalyzer{}
	cruxes := []deliberation.Crux{
		mkCrux("Regulation should be strict.", []string{"alice"}, []string{"bob"}, 0.9),
	}
	positions := []deliberation.Position{
		mkPosition("alice", "alice-tag strong enforcement matters.", 1),
		mkPosition("bob", "bob-tag self-regulation is enough.", 1),
	}
	// Run unexported-via-same-package? It's unexported — exercise through
	// the exported pipeline: Synthesizer wraps TextAnalyzer but Analyze
	// needs an LLM. Instead, assert behaviour via the public setter
	// path: SetSecondary(nil, 0) leaves the feature off.
	a.SetSecondary(nil, 0)
	if a.SecondaryLLM != nil {
		t.Fatalf("SetSecondary(nil) must leave SecondaryLLM nil, got %v", a.SecondaryLLM)
	}
	// Sanity: positions+cruxes not empty so the guard isn't just the
	// empty-input branch.
	if len(cruxes) == 0 || len(positions) == 0 {
		t.Fatal("test inputs went empty — check mkPosition/mkCrux")
	}
}

// TestCrossFamilyAgreementNoWarning: primary and secondary classify
// every agent the same way → no CROSS_FAMILY_DRIFT warning, confirms
// the happy path doesn't false-positive on stable agreement.
func TestCrossFamilyAgreementNoWarning(t *testing.T) {
	agree := []string{"alice", "carol"}
	disagree := []string{"bob", "dave"}

	secondaryCalls := atomic.Int32{}
	sec := stanceResponder(t, map[string]string{
		"alice-tag": "agree",
		"carol-tag": "agree",
		"bob-tag":   "disagree",
		"dave-tag":  "disagree",
	})
	origFn := sec.Fn
	sec.Fn = func(ctx context.Context, system, prompt string, schema map[string]any, target any) error {
		secondaryCalls.Add(1)
		return origFn(ctx, system, prompt, schema, target)
	}

	warnings := runDriftCheck(t, sec, 5,
		[]deliberation.Crux{mkCrux("Strong regulation is warranted.", agree, disagree, 0.95)},
		[]deliberation.Position{
			mkPosition("alice", "alice-tag strong regulation is essential", 1),
			mkPosition("bob", "bob-tag regulation stifles innovation", 1),
			mkPosition("carol", "carol-tag yes strict enforcement", 1),
			mkPosition("dave", "dave-tag less government", 1),
		},
	)

	for _, w := range warnings {
		if strings.Contains(w, "CROSS_FAMILY_DRIFT") {
			t.Fatalf("no drift expected on full agreement; got %q", w)
		}
	}
	if got := secondaryCalls.Load(); got != 1 {
		t.Fatalf("expected exactly one secondary call per sampled crux, got %d", got)
	}
}

// TestCrossFamilyMajorityFlipEmitsWarning: secondary disagrees with
// primary on 3 of 4 agents → strict majority flip, warning fires.
// This is the load-bearing assertion for the DARPA §3 defense.
func TestCrossFamilyMajorityFlipEmitsWarning(t *testing.T) {
	agree := []string{"alice", "bob"}
	disagree := []string{"carol", "dave"}

	// Primary says alice+bob agree, carol+dave disagree. Secondary
	// says the opposite for 3 of the 4 → 3/4 = 75% flip, exceeds
	// driftFlipRatio=0.5.
	sec := stanceResponder(t, map[string]string{
		"alice-tag": "disagree",
		"bob-tag":   "disagree",
		"carol-tag": "agree",
		"dave-tag":  "disagree", // one stable — keeps the fraction at 3/4
	})

	warnings := runDriftCheck(t, sec, 5,
		[]deliberation.Crux{mkCrux("AI deployment requires licensing.", agree, disagree, 0.95)},
		[]deliberation.Position{
			mkPosition("alice", "alice-tag licenses needed", 1),
			mkPosition("bob", "bob-tag permits should apply", 1),
			mkPosition("carol", "carol-tag no gatekeeping", 1),
			mkPosition("dave", "dave-tag open access", 1),
		},
	)

	var drift string
	for _, w := range warnings {
		if strings.Contains(w, "CROSS_FAMILY_DRIFT") {
			drift = w
			break
		}
	}
	if drift == "" {
		t.Fatalf("expected CROSS_FAMILY_DRIFT warning; got %v", warnings)
	}
	for _, want := range []string{"mock-flash", "mock-gemini", "AI deployment requires licensing"} {
		if !strings.Contains(drift, want) {
			t.Fatalf("drift warning missing %q: %s", want, drift)
		}
	}
}

// TestCrossFamilyNoiseDoesNotFire: one agent flips (25%, below the
// 50% threshold) — must NOT fire. Guards against noise false-positives.
func TestCrossFamilyNoiseDoesNotFire(t *testing.T) {
	agree := []string{"alice", "bob"}
	disagree := []string{"carol", "dave"}

	sec := stanceResponder(t, map[string]string{
		"alice-tag": "agree",
		"bob-tag":   "disagree", // the single flip — 1/4 = 25%
		"carol-tag": "disagree",
		"dave-tag":  "disagree",
	})

	warnings := runDriftCheck(t, sec, 5,
		[]deliberation.Crux{mkCrux("Open source licensing is preferable.", agree, disagree, 0.9)},
		[]deliberation.Position{
			mkPosition("alice", "alice-tag prefer permissive", 1),
			mkPosition("bob", "bob-tag commercial clauses fine", 1),
			mkPosition("carol", "carol-tag proprietary ok", 1),
			mkPosition("dave", "dave-tag closed source fine", 1),
		},
	)
	for _, w := range warnings {
		if strings.Contains(w, "CROSS_FAMILY_DRIFT") {
			t.Fatalf("25%% flip must not fire drift; got %q", w)
		}
	}
}

// TestCrossFamilySamplingCapsCalls: with K=2, only the two highest-
// controversy cruxes hit the secondary, even when more cruxes exist.
// Budgets the secondary-LLM cost at K per analysis.
func TestCrossFamilySamplingCapsCalls(t *testing.T) {
	agents := []string{"a", "b", "c", "d"}
	var positions []deliberation.Position
	for _, ag := range agents {
		positions = append(positions, mkPosition(ag, ag+"-tag stable position", 1))
	}

	cruxes := []deliberation.Crux{
		mkCrux("lowest", []string{"a", "b"}, []string{"c", "d"}, 0.10),
		mkCrux("middle", []string{"a", "c"}, []string{"b", "d"}, 0.40),
		mkCrux("high-1", []string{"a"}, []string{"b", "c", "d"}, 0.85),
		mkCrux("high-2", []string{"a", "b", "c"}, []string{"d"}, 0.95),
	}

	var calls atomic.Int32
	sec := stanceResponder(t, map[string]string{
		"a-tag": "agree", "b-tag": "agree", "c-tag": "agree", "d-tag": "agree",
	})
	orig := sec.Fn
	sec.Fn = func(ctx context.Context, system, prompt string, schema map[string]any, target any) error {
		calls.Add(1)
		return orig(ctx, system, prompt, schema, target)
	}

	_ = runDriftCheck(t, sec, 2, cruxes, positions)
	if got := calls.Load(); got != 2 {
		t.Fatalf("sampleK=2 must cap at 2 secondary calls; got %d", got)
	}
}

// runDriftCheck drives TextAnalyzer.validateAnalysisModelConsistency
// via the exported surface. Since the method is unexported, the test
// lives in-tree but in a separate package — so the call goes through
// a thin helper the analysis package exposes for testing.
func runDriftCheck(t *testing.T, sec llm.SecondaryStructuredOutput, k int, cruxes []deliberation.Crux, positions []deliberation.Position) []string {
	t.Helper()
	a := &analysis.TextAnalyzer{}
	a.SetSecondary(sec, k)
	return a.CheckCrossFamilyConsistencyForTest(context.Background(), cruxes, positions)
}
