// Package calibration measures how well the gemot mechanism performs on
// direction-judgment questions vs a single-agent baseline. It owns:
//
//   - A curated corpus of multiple-choice direction-judgment questions
//     (internal/calibration/corpus/v1.json, embedded into the binary).
//   - A runner that drives each question through both a fleet deliberation
//     and a solo-agent baseline, scores both, and writes results to
//     internal/calibration/embed/latest.json.
//   - A lookup used by analyze action:get_result to populate the
//     types.CalibrationField on every response that matches a reference
//     class (deliberation type with sufficient n in the latest run).
//
// The mechanism never claims accuracy it can't back: when no matching
// reference class exists, the calibration field is absent (omitempty) from
// get_result output. See docs/calibration.md for methodology and the
// trap list.
package calibration

import "time"

// Question is one corpus entry — a multiple-choice direction-judgment
// problem with a single known-correct answer key.
type Question struct {
	ID               string   `json:"id"`
	CorpusVersion    string   `json:"corpus_version"`
	Text             string   `json:"text"`
	Options          []string `json:"options"`      // exactly 4, indexed A=0, B=1, C=2, D=3
	GroundTruth      string   `json:"ground_truth"` // one of "A", "B", "C", "D"
	Source           string   `json:"source"`       // "swebench" | "diplomacy" | "synthetic"
	SourceRef        string   `json:"source_ref,omitempty"`
	DeliberationType string   `json:"deliberation_type"`  // "reasoning" | "knowledge" | "negotiation" | "policy"
	HeldOut          bool     `json:"held_out,omitempty"` // excluded from the public-reported rate
	Tags             []string `json:"tags,omitempty"`
}

// Corpus is the on-disk representation of a frozen set of Questions.
type Corpus struct {
	Version   string     `json:"version"`
	Questions []Question `json:"questions"`
}

// Result is the outcome of one Question being scored against one Run.
//
// VoteOnly is the round-1 ensemble vote (agents pick independently before
// seeing each other). Revised is the round-2 vote after each agent reads
// the other positions + cruxes and re-picks. The delta tests whether the
// deliberation mechanism's claim — that agents update on each other's
// arguments — is mechanically real or just one-shot ensemble voting.
// ChangedCount is how many of the N agents changed their choice across
// the revision round.
type Result struct {
	QuestionID      string `json:"question_id"`
	FleetAnswer     string `json:"fleet_answer"` // A/B/C/D, empty if extraction failed
	FleetCorrect    bool   `json:"fleet_correct"`
	VoteOnlyAnswer  string `json:"vote_only_answer"` // round-1 plurality
	VoteOnlyCorrect bool   `json:"vote_only_correct"`
	RevisedAnswer   string `json:"revised_answer,omitempty"` // round-2 plurality (after revision)
	RevisedCorrect  bool   `json:"revised_correct,omitempty"`
	ChangedCount    int    `json:"changed_count,omitempty"`
	SoloAnswer      string `json:"solo_answer"`
	SoloCorrect     bool   `json:"solo_correct"`
	DeliberationID  string `json:"deliberation_id,omitempty"`
	Notes           string `json:"notes,omitempty"`
}

// Run is the metadata for one full benchmark execution.
type Run struct {
	ID            string    `json:"id"`
	CorpusVersion string    `json:"corpus_version"`
	GemotVersion  string    `json:"gemot_version"`
	ModelVersion  string    `json:"model_version"`
	Seed          int64     `json:"seed"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	Results       []Result  `json:"results"`
}

// ReferenceClass is the aggregated rate for one bucket (deliberation type)
// from a Run, used to populate types.CalibrationField on get_result.
type ReferenceClass struct {
	DeliberationType string     `json:"deliberation_type"`
	Rate             float64    `json:"rate"`
	VoteOnlyRate     float64    `json:"vote_only_rate"`
	SoloBaselineRate float64    `json:"solo_baseline_rate"`
	N                int        `json:"n"`
	CI95             [2]float64 `json:"ci_95"`
}

// EmbeddedRun is what gets serialized to internal/calibration/embed/latest.json
// for binary-time lookup. Held-out questions are excluded from the per-type
// aggregates here; the runner reports them separately to the operator.
type EmbeddedRun struct {
	CorpusVersion    string                    `json:"corpus_version"`
	GemotVersion     string                    `json:"gemot_version"`
	ModelVersion     string                    `json:"model_version"`
	MeasuredAt       time.Time                 `json:"measured_at"`
	ReferenceClasses map[string]ReferenceClass `json:"reference_classes"` // keyed by deliberation_type
}
