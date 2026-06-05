package calibration

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

// TestWilsonInterval checks the 95% Wilson score interval against
// hand-computed values. The Wilson interval is preferred over the
// normal approximation precisely because it stays well-defined at the
// boundary (p=0 or p=1), so we explicitly test those.
func TestWilsonInterval(t *testing.T) {
	cases := []struct {
		name      string
		successes int
		n         int
		wantLow   float64
		wantHigh  float64
	}{
		{"n=0 returns full range", 0, 0, 0, 1},
		{"5/10 centered", 5, 10, 0.237, 0.763},
		{"perfect score at small n stays wide", 10, 10, 0.722, 1.000},
		{"zero score at small n stays wide", 0, 10, 0.000, 0.278},
		{"large n tightens interval", 50, 100, 0.402, 0.598},
		{"discriminating finding at n=25", 17, 25, 0.485, 0.832},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := WilsonInterval(c.successes, c.n)
			if math.Abs(got[0]-c.wantLow) > 0.01 {
				t.Errorf("low: got %.3f, want %.3f", got[0], c.wantLow)
			}
			if math.Abs(got[1]-c.wantHigh) > 0.01 {
				t.Errorf("high: got %.3f, want %.3f", got[1], c.wantHigh)
			}
		})
	}
}

// TestParseChoiceRoundtrip ensures the [CHOICE:X] prefix the runner adds
// to position bodies can be deterministically extracted back out. The
// vote-only-rate computation depends on this — if extraction silently
// returns empty, every vote-only-rate would be 0.
func TestParseChoiceRoundtrip(t *testing.T) {
	cases := []struct {
		choice    string
		rationale string
		want      string
	}{
		{"A", "because of foo", "A"},
		{"Option A: the first option", "longer reasoning here", "Option A: the first option"},
		{"", "no choice", ""}, // empty choice is a valid edge — runner should error before here, but parse must not panic
	}
	for _, c := range cases {
		body := formatPositionContent(c.choice, c.rationale)
		got := parseChoice(body)
		if got != c.want {
			t.Errorf("formatPositionContent(%q,...)/parseChoice mismatch: got %q want %q", c.choice, got, c.want)
		}
	}

	// Defensive: a position body without the prefix returns empty, not
	// a panic — protects against legacy positions being mixed in.
	if got := parseChoice("no prefix at all\nrationale only"); got != "" {
		t.Errorf("expected empty for no-prefix body, got %q", got)
	}
	if got := parseChoice("[CHOICE: incomplete"); got != "" {
		t.Errorf("expected empty for incomplete prefix, got %q", got)
	}
}

// TestAggregate verifies bucket counting + Wilson CI threading through
// the full Aggregate → MakeReferenceClass path. Held-out questions must
// land in the held-out bucket only, not the public per-type buckets.
func TestAggregate(t *testing.T) {
	corpus := &Corpus{
		Version: "test",
		Questions: []Question{
			{ID: "q1", DeliberationType: "reasoning", Options: []string{"A", "B", "C", "D"}, GroundTruth: "A"},
			{ID: "q2", DeliberationType: "reasoning", Options: []string{"A", "B", "C", "D"}, GroundTruth: "A"},
			{ID: "q3", DeliberationType: "knowledge", Options: []string{"A", "B", "C", "D"}, GroundTruth: "A"},
			{ID: "q4", DeliberationType: "reasoning", Options: []string{"A", "B", "C", "D"}, GroundTruth: "A", HeldOut: true},
		},
	}
	run := &Run{
		Results: []Result{
			{QuestionID: "q1", FleetCorrect: true, VoteOnlyCorrect: false, SoloCorrect: false},
			{QuestionID: "q2", FleetCorrect: false, VoteOnlyCorrect: true, SoloCorrect: false},
			{QuestionID: "q3", FleetCorrect: true, VoteOnlyCorrect: true, SoloCorrect: true},
			{QuestionID: "q4", FleetCorrect: true, VoteOnlyCorrect: true, SoloCorrect: false},
		},
	}
	public, heldOut := Aggregate(run, corpus)

	if got := public["reasoning"].N; got != 2 {
		t.Errorf("reasoning N: got %d, want 2 (held-out must be excluded)", got)
	}
	if got := public["reasoning"].Rate; math.Abs(got-0.5) > 0.001 {
		t.Errorf("reasoning rate: got %.3f, want 0.5", got)
	}
	if got := public["knowledge"].N; got != 1 {
		t.Errorf("knowledge N: got %d, want 1", got)
	}
	if got := public["_all"].N; got != 3 {
		t.Errorf("_all N: got %d, want 3 (held-out must be excluded)", got)
	}
	if got := heldOut.N; got != 1 {
		t.Errorf("heldOut N: got %d, want 1", got)
	}
	if got := heldOut.Rate; math.Abs(got-1.0) > 0.001 {
		t.Errorf("heldOut rate: got %.3f, want 1.0", got)
	}
}

// TestBuildEmbeddedRunRoundtrip walks Run → EmbeddedRun → JSON → parse,
// confirming the embedded format the lookup reads at runtime matches
// what the runner writes. Held-out must not appear.
func TestBuildEmbeddedRunRoundtrip(t *testing.T) {
	corpus := &Corpus{
		Version: "v1",
		Questions: []Question{
			{ID: "q1", DeliberationType: "reasoning", Options: []string{"A", "B", "C", "D"}, GroundTruth: "A"},
			{ID: "q2", DeliberationType: "reasoning", Options: []string{"A", "B", "C", "D"}, GroundTruth: "A", HeldOut: true},
		},
	}
	run := &Run{
		ID:            "r1",
		CorpusVersion: "v1",
		GemotVersion:  "test",
		ModelVersion:  "claude-sonnet-4-6",
		StartedAt:     time.Now(),
		FinishedAt:    time.Now(),
		Results: []Result{
			{QuestionID: "q1", FleetCorrect: true},
			{QuestionID: "q2", FleetCorrect: true},
		},
	}
	er := BuildEmbeddedRun(run, corpus)
	if _, hasHeld := er.ReferenceClasses["_held_out"]; hasHeld {
		t.Error("EmbeddedRun must not surface _held_out — it's operator-internal only")
	}
	if got := er.ReferenceClasses["reasoning"].N; got != 1 {
		t.Errorf("public reasoning N: got %d, want 1 (held-out excluded)", got)
	}

	var buf bytes.Buffer
	if err := WriteEmbeddedRun(&buf, er); err != nil {
		t.Fatalf("WriteEmbeddedRun: %v", err)
	}
	var back EmbeddedRun
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.CorpusVersion != "v1" {
		t.Errorf("CorpusVersion roundtrip: got %q want %q", back.CorpusVersion, "v1")
	}
}

// TestFormatReportNonEmpty is a smoke test for the human-readable
// table — guards against silent regressions where the table header
// row is correct but the data rows are empty.
func TestFormatReportNonEmpty(t *testing.T) {
	corpus := &Corpus{
		Version: "v1",
		Questions: []Question{
			{ID: "q1", DeliberationType: "reasoning", Options: []string{"A", "B", "C", "D"}, GroundTruth: "A"},
		},
	}
	run := &Run{
		ID:            "r1",
		CorpusVersion: "v1",
		StartedAt:     time.Now(),
		FinishedAt:    time.Now(),
		Results: []Result{
			{QuestionID: "q1", FleetCorrect: true},
		},
	}
	out := FormatReport(run, corpus)
	if !strings.Contains(out, "reasoning") || !strings.Contains(out, "100.0%") {
		t.Errorf("expected reasoning row with 100.0%% rate; got:\n%s", out)
	}
}
