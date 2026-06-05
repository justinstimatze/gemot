package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// CalibrationRunRow is the persistence shape for one row of calibration_runs.
// The fields mirror internal/calibration.Run but are duplicated here so
// internal/store doesn't import internal/calibration (which would create a
// cycle: store ← calibration ← deliberation ← store).
type CalibrationRunRow struct {
	ID            string
	CorpusVersion string
	GemotVersion  string
	ModelVersion  string
	Seed          int64
	StartedAt     time.Time
	FinishedAt    *time.Time
	FleetRate     *float64
	VoteOnlyRate  *float64
	SoloRate      *float64
	N             *int
}

// CalibrationResultRow is the persistence shape for one row of
// calibration_results.
type CalibrationResultRow struct {
	RunID           string
	QuestionID      string
	FleetAnswer     string
	FleetCorrect    bool
	VoteOnlyAnswer  string
	VoteOnlyCorrect bool
	SoloAnswer      string
	SoloCorrect     bool
	DeliberationID  string
	Notes           string
}

// CalibrationQuestionRow is the persistence shape for one row of
// calibration_questions. Stored only when a self-hoster wants a queryable
// corpus history; the embedded JSON at internal/calibration/corpus/v1.json
// is the authoritative source for runtime use.
type CalibrationQuestionRow struct {
	ID               string
	CorpusVersion    string
	QuestionText     string
	OptionsJSON      string
	GroundTruth      string
	Source           string
	SourceRef        string
	DeliberationType string
	HeldOut          bool
	TagsJSON         string
}

// InsertCalibrationRun starts a row in calibration_runs. Aggregate fields
// (FleetRate, VoteOnlyRate, SoloRate, N) are NULL at start and are set via
// FinishCalibrationRun once the runner finishes scoring.
func (s *DB) InsertCalibrationRun(ctx context.Context, r CalibrationRunRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO calibration_runs (id, corpus_version, gemot_version, model_version, seed, started_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, r.ID, r.CorpusVersion, r.GemotVersion, r.ModelVersion, r.Seed, r.StartedAt)
	if err != nil {
		return fmt.Errorf("insert calibration_run: %w", err)
	}
	return nil
}

// FinishCalibrationRun updates the aggregate fields once the runner has
// scored every question. fleetRate/voteOnlyRate/soloRate are computed over
// the non-held-out subset.
func (s *DB) FinishCalibrationRun(ctx context.Context, runID string, fleetRate, voteOnlyRate, soloRate float64, n int, finishedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE calibration_runs
		   SET finished_at = $2, fleet_rate = $3, vote_only_rate = $4, solo_rate = $5, n = $6
		 WHERE id = $1
	`, runID, finishedAt, fleetRate, voteOnlyRate, soloRate, n)
	if err != nil {
		return fmt.Errorf("finish calibration_run: %w", err)
	}
	return nil
}

// InsertCalibrationResult persists one (run_id, question_id) row. Idempotent
// via PK conflict — re-running the same run+question overwrites.
func (s *DB) InsertCalibrationResult(ctx context.Context, r CalibrationResultRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO calibration_results
			(run_id, question_id, fleet_answer, fleet_correct, vote_only_answer, vote_only_correct, solo_answer, solo_correct, deliberation_id, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (run_id, question_id) DO UPDATE
			SET fleet_answer = EXCLUDED.fleet_answer,
			    fleet_correct = EXCLUDED.fleet_correct,
			    vote_only_answer = EXCLUDED.vote_only_answer,
			    vote_only_correct = EXCLUDED.vote_only_correct,
			    solo_answer = EXCLUDED.solo_answer,
			    solo_correct = EXCLUDED.solo_correct,
			    deliberation_id = EXCLUDED.deliberation_id,
			    notes = EXCLUDED.notes
	`,
		r.RunID, r.QuestionID, r.FleetAnswer, boolToInt(r.FleetCorrect),
		r.VoteOnlyAnswer, boolToInt(r.VoteOnlyCorrect),
		r.SoloAnswer, boolToInt(r.SoloCorrect),
		r.DeliberationID, r.Notes,
	)
	if err != nil {
		return fmt.Errorf("insert calibration_result: %w", err)
	}
	return nil
}

// LoadLatestCalibrationRun returns the most recent finished run, or
// sql.ErrNoRows if no run has finished yet. The CLI `gemot calibration
// report` reads this; production runtime uses the embedded JSON instead.
func (s *DB) LoadLatestCalibrationRun(ctx context.Context) (*CalibrationRunRow, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, corpus_version, gemot_version, model_version, seed, started_at, finished_at, fleet_rate, vote_only_rate, solo_rate, n
		  FROM calibration_runs
		 WHERE finished_at IS NOT NULL
		 ORDER BY finished_at DESC
		 LIMIT 1
	`)
	var r CalibrationRunRow
	var finishedAt sql.NullTime
	var fleetRate, voteOnlyRate, soloRate sql.NullFloat64
	var n sql.NullInt64
	if err := row.Scan(&r.ID, &r.CorpusVersion, &r.GemotVersion, &r.ModelVersion, &r.Seed, &r.StartedAt, &finishedAt, &fleetRate, &voteOnlyRate, &soloRate, &n); err != nil {
		return nil, err
	}
	if finishedAt.Valid {
		t := finishedAt.Time
		r.FinishedAt = &t
	}
	if fleetRate.Valid {
		f := fleetRate.Float64
		r.FleetRate = &f
	}
	if voteOnlyRate.Valid {
		f := voteOnlyRate.Float64
		r.VoteOnlyRate = &f
	}
	if soloRate.Valid {
		f := soloRate.Float64
		r.SoloRate = &f
	}
	if n.Valid {
		i := int(n.Int64)
		r.N = &i
	}
	return &r, nil
}

// LoadCalibrationResults returns every result row for one run, in insertion
// order (by question_id). Used by `gemot calibration report` to print the
// full breakdown.
func (s *DB) LoadCalibrationResults(ctx context.Context, runID string) ([]CalibrationResultRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, question_id, fleet_answer, fleet_correct, vote_only_answer, vote_only_correct, solo_answer, solo_correct, deliberation_id, notes
		  FROM calibration_results
		 WHERE run_id = $1
		 ORDER BY question_id
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("load calibration_results: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []CalibrationResultRow
	for rows.Next() {
		var r CalibrationResultRow
		var fleetCorrect, voteOnlyCorrect, soloCorrect int
		if err := rows.Scan(&r.RunID, &r.QuestionID, &r.FleetAnswer, &fleetCorrect, &r.VoteOnlyAnswer, &voteOnlyCorrect, &r.SoloAnswer, &soloCorrect, &r.DeliberationID, &r.Notes); err != nil {
			return nil, fmt.Errorf("scan calibration_result: %w", err)
		}
		r.FleetCorrect = fleetCorrect != 0
		r.VoteOnlyCorrect = voteOnlyCorrect != 0
		r.SoloCorrect = soloCorrect != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertCalibrationQuestion mirrors the embedded corpus into Postgres for a
// self-hoster who wants a queryable corpus history. Idempotent on id.
func (s *DB) UpsertCalibrationQuestion(ctx context.Context, q CalibrationQuestionRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO calibration_questions
			(id, corpus_version, question_text, options_json, ground_truth, source, source_ref, deliberation_type, held_out, tags)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO UPDATE
			SET corpus_version = EXCLUDED.corpus_version,
			    question_text = EXCLUDED.question_text,
			    options_json = EXCLUDED.options_json,
			    ground_truth = EXCLUDED.ground_truth,
			    source = EXCLUDED.source,
			    source_ref = EXCLUDED.source_ref,
			    deliberation_type = EXCLUDED.deliberation_type,
			    held_out = EXCLUDED.held_out,
			    tags = EXCLUDED.tags
	`,
		q.ID, q.CorpusVersion, q.QuestionText, q.OptionsJSON, q.GroundTruth,
		q.Source, q.SourceRef, q.DeliberationType, boolToInt(q.HeldOut), q.TagsJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert calibration_question: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// MarshalOptions / MarshalTags are tiny helpers callers use to flatten
// in-memory representations before passing to UpsertCalibrationQuestion.
// They live in store/ rather than calibration/ so the calibration package
// stays free of database concerns.
func MarshalOptions(options []string) (string, error) {
	b, err := json.Marshal(options)
	if err != nil {
		return "", fmt.Errorf("marshal options: %w", err)
	}
	return string(b), nil
}

func MarshalTags(tags []string) (string, error) {
	if tags == nil {
		return "[]", nil
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "", fmt.Errorf("marshal tags: %w", err)
	}
	return string(b), nil
}
