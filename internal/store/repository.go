package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

// deliberationColumns is the canonical SELECT column list for deliberation queries.
const deliberationColumns = `id, topic, description, round_number, status, COALESCE(sub_status, ''), COALESCE(type, ''), COALESCE(visibility, 'open'), COALESCE(creator_key, ''), COALESCE(max_participants, 0), COALESCE(template, ''), COALESCE(rules, '{}'), COALESCE(group_id, ''), COALESCE(resolution_json, ''), COALESCE(signature_policy, 'none'), deadline_at, created_at`

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanDeliberation scans a row into a Deliberation using the deliberationColumns layout.
func scanDeliberation(s scanner) (deliberation.Deliberation, error) {
	var d deliberation.Deliberation
	var createdAt time.Time
	var deadlineAt sql.NullTime
	var rulesJSON, resolutionJSON string
	if err := s.Scan(&d.ID, &d.Topic, &d.Description, &d.Round, &d.Status, &d.SubStatus, &d.Type, &d.Visibility, &d.CreatorKey, &d.MaxParticipants, &d.Template, &rulesJSON, &d.GroupID, &resolutionJSON, &d.SignaturePolicy, &deadlineAt, &createdAt); err != nil {
		return d, err
	}
	d.CreatedAt = createdAt
	if deadlineAt.Valid {
		d.DeadlineAt = &deadlineAt.Time
	}
	d.Rules = unmarshalRules(rulesJSON)
	if resolutionJSON != "" {
		var res deliberation.Resolution
		if err := json.Unmarshal([]byte(resolutionJSON), &res); err == nil {
			d.Resolution = &res
		}
	}
	return d, nil
}

// scanDeliberationRows scans all rows into a slice using scanDeliberation.
func scanDeliberationRows(rows *sql.Rows) ([]deliberation.Deliberation, error) {
	var result []deliberation.Deliberation
	for rows.Next() {
		d, err := scanDeliberation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *DB) CreateDeliberation(ctx context.Context, d *deliberation.Deliberation) error {
	d.ID = uuid.New().String()
	d.CreatedAt = time.Now().UTC()
	if d.SignaturePolicy == "" {
		d.SignaturePolicy = "none"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deliberations (id, topic, description, round_number, status, type, visibility, creator_key, max_participants, template, rules, group_id, signature_policy, deadline_at, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		d.ID, d.Topic, d.Description, d.Round, d.Status, d.Type, d.Visibility, d.CreatorKey, d.MaxParticipants, d.Template, marshalRules(d.Rules), d.GroupID, d.SignaturePolicy, d.DeadlineAt, d.CreatedAt,
	)
	return err
}

func (s *DB) GetDeliberation(ctx context.Context, id string) (*deliberation.Deliberation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deliberationColumns+` FROM deliberations WHERE id = $1`, id)
	d, err := scanDeliberation(row)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// normalizePagination clamps limit to [1, 500] (default 100) and ensures offset >= 0.
func normalizePagination(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func (s *DB) ListDeliberations(ctx context.Context, limit, offset int, keyID string) ([]deliberation.Deliberation, error) {
	limit, offset = normalizePagination(limit, offset)
	// keyID="" means admin — show everything. Otherwise filter out private deliberations not owned by caller.
	query := `SELECT ` + deliberationColumns + ` FROM deliberations WHERE status != 'deleted'`
	var args []any
	if keyID != "" {
		query += ` AND (visibility != 'private' OR creator_key = $3)`
		args = []any{limit, offset, keyID}
	} else {
		args = []any{limit, offset}
	}
	query += ` ORDER BY created_at DESC LIMIT $1 OFFSET $2`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	return scanDeliberationRows(rows)
}

// ListByGroup returns deliberations in a group, ordered by creation time.
func (s *DB) ListByGroup(ctx context.Context, groupID string, limit, offset int, keyID string) ([]deliberation.Deliberation, error) {
	limit, offset = normalizePagination(limit, offset)
	query := `SELECT ` + deliberationColumns + ` FROM deliberations WHERE group_id = $1 AND status != 'deleted'`
	var args []any
	if keyID != "" {
		query += ` AND (visibility != 'private' OR creator_key = $4)`
		args = []any{groupID, limit, offset, keyID}
	} else {
		args = []any{groupID, limit, offset}
	}
	query += ` ORDER BY created_at LIMIT $2 OFFSET $3`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	return scanDeliberationRows(rows)
}

// ListByAgent returns deliberations where an agent has submitted positions.
func (s *DB) ListByAgent(ctx context.Context, agentID string, limit, offset int, keyID string) ([]deliberation.Deliberation, error) {
	limit, offset = normalizePagination(limit, offset)
	const cols = `d.id, d.topic, d.description, d.round_number, d.status, COALESCE(d.sub_status, ''), COALESCE(d.type, ''), COALESCE(d.visibility, 'open'), COALESCE(d.creator_key, ''), COALESCE(d.max_participants, 0), COALESCE(d.template, ''), COALESCE(d.rules, '{}'), COALESCE(d.group_id, ''), COALESCE(d.resolution_json, ''), COALESCE(d.signature_policy, 'none'), d.deadline_at, d.created_at`
	query := `SELECT DISTINCT ` + cols + ` FROM deliberations d JOIN positions p ON d.id = p.deliberation_id WHERE p.agent_id = $1 AND d.status != 'deleted'`
	var args []any
	if keyID != "" {
		query += ` AND (d.visibility != 'private' OR d.creator_key = $4)`
		args = []any{agentID, limit, offset, keyID}
	} else {
		args = []any{agentID, limit, offset}
	}
	query += ` ORDER BY d.created_at DESC LIMIT $2 OFFSET $3`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	return scanDeliberationRows(rows)
}

func (s *DB) SetGroupID(ctx context.Context, deliberationID, groupID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE deliberations SET group_id = $1 WHERE id = $2`, groupID, deliberationID)
	return err
}

func (s *DB) UpdateDeliberationTemplate(ctx context.Context, id, template string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE deliberations SET template = $1 WHERE id = $2`, template, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	return nil
}

func (s *DB) UpdateDeliberationRules(ctx context.Context, id string, rules map[string]any) error {
	res, err := s.db.ExecContext(ctx, `UPDATE deliberations SET rules = $1 WHERE id = $2`, marshalRules(rules), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	return nil
}

// DeleteDeliberation soft-deletes a deliberation by setting status to "deleted".
// Data is preserved for compliance and abuse auditing.
func (s *DB) DeleteDeliberation(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE deliberations SET status = 'deleted', sub_status = '', status_changed_at = NOW() WHERE id = $1 AND status != 'deleted'`,
		id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("deliberation not found or already deleted: %s", id)
	}
	return nil
}

var validStatuses = map[string]bool{
	"open": true, "analyzing": true, "closed": true, "deleted": true,
}

func (s *DB) UpdateDeliberationStatus(ctx context.Context, id, status string) error {
	if !validStatuses[status] {
		return fmt.Errorf("invalid status %q", status)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE deliberations SET status = $1, sub_status = '', status_changed_at = NOW() WHERE id = $2`, status, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	return nil
}

// SaveResolution updates the resolution field without changing status.
// Pass nil to clear a stale resolution.
func (s *DB) SaveResolution(ctx context.Context, id string, resolution *deliberation.Resolution) error {
	if resolution == nil {
		_, err := s.db.ExecContext(ctx, `UPDATE deliberations SET resolution_json = '' WHERE id = $1`, id)
		return err
	}
	resJSON, err := json.Marshal(resolution)
	if err != nil {
		return fmt.Errorf("marshal resolution: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE deliberations SET resolution_json = $1 WHERE id = $2`,
		string(resJSON), id,
	)
	return err
}

func (s *DB) UpdateSubStatus(ctx context.Context, id, subStatus string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE deliberations SET sub_status = $1 WHERE id = $2`, subStatus, id)
	return err
}

// TrySetAnalyzing atomically transitions status from "open" to "analyzing".
// Returns false if the deliberation is not in "open" status (prevents race conditions).
func (s *DB) TrySetAnalyzing(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE deliberations SET status = 'analyzing', status_changed_at = NOW() WHERE id = $1 AND status = 'open'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *DB) AdvanceRound(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE deliberations SET round_number = round_number + 1 WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	return nil
}

// CountPositions returns the number of positions in a deliberation.
func (s *DB) CountPositions(ctx context.Context, deliberationID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM positions WHERE deliberation_id = $1`, deliberationID).Scan(&count)
	return count, err
}

// CheckParticipantCap checks if a deliberation has reached its max participants
// and whether the given agent is already participating. Single query instead of
// loading all positions into memory.
func (s *DB) CheckParticipantCap(ctx context.Context, deliberationID, agentID string, maxParticipants int) (capped bool, alreadyIn bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT agent_id) >= $2, BOOL_OR(agent_id = $3) FROM positions WHERE deliberation_id = $1`,
		deliberationID, maxParticipants, agentID,
	).Scan(&capped, &alreadyIn)
	return
}

func (s *DB) CreatePosition(ctx context.Context, p *deliberation.Position) error {
	p.ID = uuid.New().String()
	p.CreatedAt = time.Now().UTC()
	draft := 0
	if p.Draft {
		draft = 1
	}
	metadataJSON := "{}"
	if p.Metadata != nil {
		if b, err := json.Marshal(p.Metadata); err == nil {
			metadataJSON = string(b)
		}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO positions (id, deliberation_id, agent_id, content, model_family, group_name, conviction, reservation, on_behalf_of, interests, draft, round_number, created_at, parent_position_id, metadata, signature) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		p.ID, p.DeliberationID, p.AgentID, p.Content, p.ModelFamily, p.Group, p.Conviction, p.Reservation, p.OnBehalfOf, p.Interests, draft, p.Round, p.CreatedAt, p.ParentPositionID, metadataJSON, p.Signature,
	)
	return err
}

func (s *DB) GetPositions(ctx context.Context, deliberationID string, round *int) ([]deliberation.Position, error) {
	var rows *rowsWrapper
	if round != nil {
		r, err := s.db.QueryContext(ctx,
			`SELECT id, deliberation_id, agent_id, content, COALESCE(model_family, ''), COALESCE(group_name, ''), COALESCE(conviction, 0.5), COALESCE(reservation, ''), COALESCE(on_behalf_of, ''), COALESCE(interests, ''), COALESCE(draft, 0), round_number, created_at, COALESCE(parent_position_id, ''), COALESCE(metadata, '{}'), signature FROM positions WHERE deliberation_id = $1 AND round_number = $2 AND COALESCE(draft, 0) = 0 ORDER BY created_at`,
			deliberationID, *round,
		)
		if err != nil {
			return nil, err
		}
		rows = &rowsWrapper{r}
	} else {
		r, err := s.db.QueryContext(ctx,
			`SELECT id, deliberation_id, agent_id, content, COALESCE(model_family, ''), COALESCE(group_name, ''), COALESCE(conviction, 0.5), COALESCE(reservation, ''), COALESCE(on_behalf_of, ''), COALESCE(interests, ''), COALESCE(draft, 0), round_number, created_at, COALESCE(parent_position_id, ''), COALESCE(metadata, '{}'), signature FROM positions WHERE deliberation_id = $1 AND COALESCE(draft, 0) = 0 ORDER BY created_at`,
			deliberationID,
		)
		if err != nil {
			return nil, err
		}
		rows = &rowsWrapper{r}
	}
	defer rows.Close() //nolint:errcheck

	var result []deliberation.Position
	for rows.Next() {
		var p deliberation.Position
		var createdAt time.Time
		var draftInt int
		var metadataJSON string
		if err := rows.Scan(&p.ID, &p.DeliberationID, &p.AgentID, &p.Content, &p.ModelFamily, &p.Group, &p.Conviction, &p.Reservation, &p.OnBehalfOf, &p.Interests, &draftInt, &p.Round, &createdAt, &p.ParentPositionID, &metadataJSON, &p.Signature); err != nil {
			return nil, err
		}
		p.Draft = draftInt == 1
		p.CreatedAt = createdAt
		if metadataJSON != "" && metadataJSON != "{}" {
			_ = json.Unmarshal([]byte(metadataJSON), &p.Metadata)
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *DB) GetPositionByID(ctx context.Context, id string) (*deliberation.Position, error) {
	p := &deliberation.Position{}
	var createdAt time.Time
	var draftInt int
	var metadataJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, deliberation_id, agent_id, content, COALESCE(model_family, ''), COALESCE(group_name, ''), COALESCE(conviction, 0.5), COALESCE(reservation, ''), COALESCE(on_behalf_of, ''), COALESCE(interests, ''), COALESCE(draft, 0), round_number, created_at, COALESCE(parent_position_id, ''), COALESCE(metadata, '{}'), signature FROM positions WHERE id = $1`, id,
	).Scan(&p.ID, &p.DeliberationID, &p.AgentID, &p.Content, &p.ModelFamily, &p.Group, &p.Conviction, &p.Reservation, &p.OnBehalfOf, &p.Interests, &draftInt, &p.Round, &createdAt, &p.ParentPositionID, &metadataJSON, &p.Signature)
	p.Draft = draftInt == 1
	if err != nil {
		return nil, err
	}
	if metadataJSON != "" && metadataJSON != "{}" {
		_ = json.Unmarshal([]byte(metadataJSON), &p.Metadata)
	}
	p.CreatedAt = createdAt
	return p, nil
}

func (s *DB) CreateVote(ctx context.Context, v *deliberation.Vote) error {
	v.ID = uuid.New().String()
	v.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO votes (id, deliberation_id, agent_id, position_id, value, criterion_id, qualifier, caveat, created_at, signature) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (deliberation_id, agent_id, position_id, criterion_id) DO UPDATE SET id = $1, value = $5, qualifier = $7, caveat = $8, created_at = $9, signature = $10`,
		v.ID, v.DeliberationID, v.AgentID, v.PositionID, v.Value, v.CriterionID, v.Qualifier, v.Caveat, v.CreatedAt, v.Signature,
	)
	return err
}

func (s *DB) GetVotes(ctx context.Context, deliberationID string) ([]deliberation.Vote, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, deliberation_id, agent_id, position_id, value, COALESCE(criterion_id, ''), COALESCE(qualifier, ''), COALESCE(caveat, ''), created_at, signature FROM votes WHERE deliberation_id = $1 ORDER BY created_at`,
		deliberationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var result []deliberation.Vote
	for rows.Next() {
		var v deliberation.Vote
		var createdAt time.Time
		if err := rows.Scan(&v.ID, &v.DeliberationID, &v.AgentID, &v.PositionID, &v.Value, &v.CriterionID, &v.Qualifier, &v.Caveat, &createdAt, &v.Signature); err != nil {
			return nil, err
		}
		v.CreatedAt = createdAt
		result = append(result, v)
	}
	return result, rows.Err()
}

// GetVotesByRound returns votes on positions from a specific round.
func (s *DB) GetVotesByRound(ctx context.Context, deliberationID string, round int) ([]deliberation.Vote, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT v.id, v.deliberation_id, v.agent_id, v.position_id, v.value, COALESCE(v.criterion_id, ''), COALESCE(v.qualifier, ''), COALESCE(v.caveat, ''), v.created_at, v.signature
		 FROM votes v JOIN positions p ON v.position_id = p.id
		 WHERE v.deliberation_id = $1 AND p.round_number = $2
		 ORDER BY v.created_at`,
		deliberationID, round,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var result []deliberation.Vote
	for rows.Next() {
		var v deliberation.Vote
		var createdAt time.Time
		if err := rows.Scan(&v.ID, &v.DeliberationID, &v.AgentID, &v.PositionID, &v.Value, &v.CriterionID, &v.Qualifier, &v.Caveat, &createdAt, &v.Signature); err != nil {
			return nil, err
		}
		v.CreatedAt = createdAt
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *DB) SaveAnalysisResult(ctx context.Context, deliberationID string, round int, result *deliberation.AnalysisResult) error {
	b, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshaling analysis result: %w", err)
	}
	id := uuid.New().String()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO analysis_results (id, deliberation_id, round_number, result_json, analyzed_at) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (deliberation_id, round_number) DO UPDATE SET id = $1, result_json = $4, analyzed_at = $5`,
		id, deliberationID, round, string(b), time.Now().UTC(),
	)
	return err
}

func (s *DB) GetAnalysisResult(ctx context.Context, deliberationID string, round int) (*deliberation.AnalysisResult, error) {
	var resultJSON string
	var analyzedAt time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT result_json, analyzed_at FROM analysis_results WHERE deliberation_id = $1 AND round_number = $2`,
		deliberationID, round,
	).Scan(&resultJSON, &analyzedAt)
	if err != nil {
		return nil, err
	}
	var result deliberation.AnalysisResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, err
	}
	if result.AnalyzedAt.IsZero() {
		result.AnalyzedAt = analyzedAt
	}
	return &result, nil
}

func (s *DB) GetLatestAnalysisResult(ctx context.Context, deliberationID string) (*deliberation.AnalysisResult, error) {
	var resultJSON string
	var analyzedAt time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT result_json, analyzed_at FROM analysis_results WHERE deliberation_id = $1 ORDER BY round_number DESC LIMIT 1`,
		deliberationID,
	).Scan(&resultJSON, &analyzedAt)
	if err != nil {
		return nil, err
	}
	var result deliberation.AnalysisResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, err
	}
	if result.AnalyzedAt.IsZero() {
		result.AnalyzedAt = analyzedAt
	}
	return &result, nil
}

func (s *DB) GetAllAnalysisResults(ctx context.Context, deliberationID string) ([]deliberation.AnalysisResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT result_json, analyzed_at FROM analysis_results WHERE deliberation_id = $1 ORDER BY round_number ASC`,
		deliberationID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var results []deliberation.AnalysisResult
	for rows.Next() {
		var resultJSON string
		var analyzedAt time.Time
		if err := rows.Scan(&resultJSON, &analyzedAt); err != nil {
			return nil, err
		}
		var result deliberation.AnalysisResult
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return nil, err
		}
		if result.AnalyzedAt.IsZero() {
			result.AnalyzedAt = analyzedAt
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

// GetStuckAnalyzing returns deliberation IDs stuck in "analyzing" for longer than maxAge.
func (s *DB) GetStuckAnalyzing(ctx context.Context, maxAge time.Duration) ([]string, error) {
	cutoff := time.Now().UTC().Add(-maxAge)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM deliberations WHERE status = 'analyzing' AND COALESCE(status_changed_at, created_at) < $1`,
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *DB) RecoverStuckAnalyzing(ctx context.Context, maxAge time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-maxAge)
	res, err := s.db.ExecContext(ctx,
		`UPDATE deliberations SET status = 'open', sub_status = '', status_changed_at = NOW()
		 WHERE status = 'analyzing' AND COALESCE(status_changed_at, created_at) < $1`,
		cutoff,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CacheGet retrieves a cached LLM response by key. Returns "" if not found or expired.
func (s *DB) CacheGet(key string, maxAge time.Duration) string {
	var response string
	var createdAt time.Time
	err := s.db.QueryRow(`SELECT response_json, created_at FROM llm_cache WHERE cache_key = $1`, key).Scan(&response, &createdAt)
	if err != nil {
		return ""
	}
	if maxAge > 0 {
		if time.Since(createdAt) > maxAge {
			return ""
		}
	}
	return response
}

// CachePut stores an LLM response by key.
func (s *DB) CachePut(key, response, model string) {
	s.db.Exec( //nolint:errcheck
		`INSERT INTO llm_cache (cache_key, response_json, model, created_at) VALUES ($1, $2, $3, NOW())
		 ON CONFLICT (cache_key) DO UPDATE SET response_json = $2, model = $3, created_at = NOW()`,
		key, response, model,
	)
}

func (s *DB) CreateDelegation(ctx context.Context, d *deliberation.Delegation) error {
	d.ID = uuid.New().String()
	d.CreatedAt = time.Now().UTC()
	d.Active = true
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO delegations (id, deliberation_id, from_agent, to_agent, scope, active, created_at) VALUES ($1, $2, $3, $4, $5, 1, $6)
		 ON CONFLICT (deliberation_id, from_agent) DO UPDATE SET id = $1, to_agent = $4, scope = $5, active = 1, created_at = $6`,
		d.ID, d.DeliberationID, d.FromAgent, d.ToAgent, d.Scope, d.CreatedAt,
	)
	return err
}

func (s *DB) RevokeDelegation(ctx context.Context, deliberationID, fromAgent string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE delegations SET active = 0 WHERE deliberation_id = $1 AND from_agent = $2`,
		deliberationID, fromAgent,
	)
	return err
}

func (s *DB) GetDelegations(ctx context.Context, deliberationID string) ([]deliberation.Delegation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, deliberation_id, from_agent, to_agent, COALESCE(scope, ''), active, created_at FROM delegations WHERE deliberation_id = $1 AND active = 1 ORDER BY created_at`,
		deliberationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var result []deliberation.Delegation
	for rows.Next() {
		var d deliberation.Delegation
		var createdAt time.Time
		var active int
		if err := rows.Scan(&d.ID, &d.DeliberationID, &d.FromAgent, &d.ToAgent, &d.Scope, &active, &createdAt); err != nil {
			return nil, err
		}
		d.Active = active == 1
		d.CreatedAt = createdAt
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *DB) PublishPosition(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE positions SET draft = 0 WHERE id = $1`, id)
	return err
}

func (s *DB) CreateCommitment(ctx context.Context, c *deliberation.Commitment) error {
	c.ID = uuid.New().String()
	c.CreatedAt = time.Now().UTC()
	c.Status = "pending"
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO commitments (id, deliberation_id, agent_id, analysis_round, statement, conditional, status, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		c.ID, c.DeliberationID, c.AgentID, c.AnalysisRound, c.Statement, c.Conditional, c.Status, c.CreatedAt,
	)
	return err
}

func (s *DB) GetCommitments(ctx context.Context, deliberationID string) ([]deliberation.Commitment, error) {
	return s.scanCommitments(ctx,
		`SELECT id, deliberation_id, agent_id, analysis_round, statement, COALESCE(conditional, ''), status, created_at, fulfilled_at, broken_at, COALESCE(broken_reason, ''), COALESCE(verified_by, '') FROM commitments WHERE deliberation_id = $1 ORDER BY created_at`,
		deliberationID,
	)
}

func (s *DB) GetCommitmentByID(ctx context.Context, id string) (*deliberation.Commitment, error) {
	out, err := s.scanCommitments(ctx,
		`SELECT id, deliberation_id, agent_id, analysis_round, statement, COALESCE(conditional, ''), status, created_at, fulfilled_at, broken_at, COALESCE(broken_reason, ''), COALESCE(verified_by, '') FROM commitments WHERE id = $1`,
		id,
	)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("commitment not found: %s", id)
	}
	return &out[0], nil
}

func (s *DB) GetCommitmentsByAgent(ctx context.Context, agentID string) ([]deliberation.Commitment, error) {
	return s.scanCommitments(ctx,
		`SELECT id, deliberation_id, agent_id, analysis_round, statement, COALESCE(conditional, ''), status, created_at, fulfilled_at, broken_at, COALESCE(broken_reason, ''), COALESCE(verified_by, '') FROM commitments WHERE agent_id = $1 ORDER BY created_at`,
		agentID,
	)
}

func (s *DB) GetCommitmentsByGroup(ctx context.Context, groupID string) ([]deliberation.Commitment, error) {
	return s.scanCommitments(ctx,
		`SELECT c.id, c.deliberation_id, c.agent_id, c.analysis_round, c.statement, COALESCE(c.conditional, ''), c.status, c.created_at, c.fulfilled_at, c.broken_at, COALESCE(c.broken_reason, ''), COALESCE(c.verified_by, '') FROM commitments c JOIN deliberations d ON c.deliberation_id = d.id WHERE d.group_id = $1 ORDER BY c.created_at`,
		groupID,
	)
}

func (s *DB) scanCommitments(ctx context.Context, query string, args ...any) ([]deliberation.Commitment, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var result []deliberation.Commitment
	for rows.Next() {
		var c deliberation.Commitment
		if err := rows.Scan(&c.ID, &c.DeliberationID, &c.AgentID, &c.AnalysisRound, &c.Statement, &c.Conditional, &c.Status, &c.CreatedAt, &c.FulfilledAt, &c.BrokenAt, &c.BrokenReason, &c.VerifiedBy); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (s *DB) UpdateCommitmentStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE commitments SET status = $1 WHERE id = $2`, status, id)
	return err
}

func (s *DB) FulfillCommitment(ctx context.Context, id, verifiedBy string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE commitments SET status = 'fulfilled', fulfilled_at = NOW(), verified_by = $1 WHERE id = $2`,
		verifiedBy, id,
	)
	return err
}

func (s *DB) BreakCommitment(ctx context.Context, id, reason, verifiedBy string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE commitments SET status = 'broken', broken_at = NOW(), broken_reason = $1, verified_by = $2 WHERE id = $3`,
		reason, verifiedBy, id,
	)
	return err
}

func (s *DB) CreateJoinCode(ctx context.Context, jc *deliberation.JoinCode) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO join_codes (code, deliberation_id, role, expires_at, created_at, max_uses, use_count) VALUES ($1, $2, $3, $4, $5, $6, 0)`,
		jc.Code, jc.DeliberationID, jc.Role, jc.ExpiresAt, jc.CreatedAt, jc.MaxUses,
	)
	return err
}

func (s *DB) ClaimJoinCode(ctx context.Context, code, agentID string) (*deliberation.JoinCode, error) {
	jc := &deliberation.JoinCode{}
	var expiresAt, createdAt time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT code, deliberation_id, role, expires_at, COALESCE(used_by, ''), created_at, max_uses, use_count FROM join_codes WHERE code = $1`, code,
	).Scan(&jc.Code, &jc.DeliberationID, &jc.Role, &expiresAt, &jc.UsedBy, &createdAt, &jc.MaxUses, &jc.UseCount)
	if err != nil {
		return nil, fmt.Errorf("invalid join code")
	}
	jc.ExpiresAt = expiresAt
	jc.CreatedAt = createdAt

	if time.Now().After(jc.ExpiresAt) {
		return nil, fmt.Errorf("join code expired")
	}
	if jc.UseCount >= jc.MaxUses {
		return nil, fmt.Errorf("join code has reached maximum uses (%d)", jc.MaxUses)
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE join_codes SET use_count = use_count + 1, used_by = $1 WHERE code = $2 AND use_count < max_uses`,
		agentID, code,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("join code has reached maximum uses")
	}

	jc.UseCount++
	jc.Used = jc.UseCount >= jc.MaxUses
	jc.UsedBy = agentID
	return jc, nil
}

func (s *DB) LookupJoinCode(ctx context.Context, code string) (*deliberation.JoinCode, error) {
	jc := &deliberation.JoinCode{}
	var expiresAt, createdAt time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT code, deliberation_id, role, expires_at, COALESCE(used_by, ''), created_at, max_uses, use_count FROM join_codes WHERE code = $1`, code,
	).Scan(&jc.Code, &jc.DeliberationID, &jc.Role, &expiresAt, &jc.UsedBy, &createdAt, &jc.MaxUses, &jc.UseCount)
	if err != nil {
		return nil, fmt.Errorf("join code not found")
	}
	jc.ExpiresAt = expiresAt
	jc.CreatedAt = createdAt
	jc.Used = jc.UseCount >= jc.MaxUses
	return jc, nil
}

func (s *DB) AddToACL(ctx context.Context, deliberationID, keyID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deliberation_acl (deliberation_id, key_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		deliberationID, keyID,
	)
	return err
}

func (s *DB) CheckACL(ctx context.Context, deliberationID, keyID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM deliberation_acl WHERE deliberation_id = $1 AND key_id = $2`,
		deliberationID, keyID,
	).Scan(&count)
	return count > 0, err
}

func (s *DB) CreateInvitation(ctx context.Context, inv *deliberation.Invitation) error {
	inv.ID = uuid.New().String()
	inv.CreatedAt = time.Now().UTC()
	inv.Status = "pending"
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO invitations (id, deliberation_id, invited_by, invited_agent, role, reason, status, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		inv.ID, inv.DeliberationID, inv.InvitedBy, inv.InvitedAgent, inv.Role, inv.Reason, inv.Status, inv.CreatedAt,
	)
	return err
}

func (s *DB) GetInvitations(ctx context.Context, deliberationID string) ([]deliberation.Invitation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, deliberation_id, invited_by, invited_agent, COALESCE(role, ''), reason, status, created_at FROM invitations WHERE deliberation_id = $1 ORDER BY created_at`,
		deliberationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var result []deliberation.Invitation
	for rows.Next() {
		var inv deliberation.Invitation
		var createdAt time.Time
		if err := rows.Scan(&inv.ID, &inv.DeliberationID, &inv.InvitedBy, &inv.InvitedAgent, &inv.Role, &inv.Reason, &inv.Status, &createdAt); err != nil {
			return nil, err
		}
		inv.CreatedAt = createdAt
		result = append(result, inv)
	}
	return result, rows.Err()
}

func (s *DB) UpdateInvitationStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE invitations SET status = $1 WHERE id = $2`, status, id)
	return err
}

func (s *DB) CreateDispute(ctx context.Context, d *deliberation.Dispute) error {
	d.ID = uuid.New().String()
	d.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO disputes (id, deliberation_id, agent_id, crux_claim, correction, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		d.ID, d.DeliberationID, d.AgentID, d.CruxClaim, d.Correction, d.CreatedAt,
	)
	return err
}

func (s *DB) GetDisputes(ctx context.Context, deliberationID string) ([]deliberation.Dispute, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, deliberation_id, agent_id, crux_claim, correction, created_at FROM disputes WHERE deliberation_id = $1 ORDER BY created_at`,
		deliberationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var result []deliberation.Dispute
	for rows.Next() {
		var d deliberation.Dispute
		var createdAt time.Time
		if err := rows.Scan(&d.ID, &d.DeliberationID, &d.AgentID, &d.CruxClaim, &d.Correction, &createdAt); err != nil {
			return nil, err
		}
		d.CreatedAt = createdAt
		result = append(result, d)
	}
	return result, rows.Err()
}

// GetUnprocessedDisputes returns disputes that have not yet been
// ingested by the reputation layer (rep_processed_at IS NULL). The
// reputation updater uses this to avoid double-counting disputes across
// rounds — calling MarkDisputesProcessed after a successful update.
func (s *DB) GetUnprocessedDisputes(ctx context.Context, deliberationID string) ([]deliberation.Dispute, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, deliberation_id, agent_id, crux_claim, correction, created_at FROM disputes WHERE deliberation_id = $1 AND rep_processed_at IS NULL ORDER BY created_at`,
		deliberationID,
	)
	if err != nil {
		return nil, fmt.Errorf("get unprocessed disputes: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var result []deliberation.Dispute
	for rows.Next() {
		var d deliberation.Dispute
		var createdAt time.Time
		if err := rows.Scan(&d.ID, &d.DeliberationID, &d.AgentID, &d.CruxClaim, &d.Correction, &createdAt); err != nil {
			return nil, fmt.Errorf("scan dispute: %w", err)
		}
		d.CreatedAt = createdAt
		result = append(result, d)
	}
	return result, rows.Err()
}

// MarkDisputesProcessed stamps rep_processed_at on the given disputes
// so a subsequent UpdateFromRound skips them. Idempotent — once
// stamped, further calls are no-ops.
func (s *DB) MarkDisputesProcessed(ctx context.Context, disputeIDs []string) error {
	if len(disputeIDs) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE disputes SET rep_processed_at = NOW() WHERE id = ANY($1) AND rep_processed_at IS NULL`,
		disputeIDs,
	)
	if err != nil {
		return fmt.Errorf("mark disputes processed: %w", err)
	}
	return nil
}

// --- Job queue ---

type Job struct {
	ID             string
	DeliberationID string
	Status         string // pending | running | completed | failed
	Model          string
	APIKey         string
	CreditCost     int
	Error          string
}

func (s *DB) CreateJob(j *Job) error {
	j.ID = uuid.New().String()
	_, err := s.db.Exec(
		`INSERT INTO jobs (id, deliberation_id, status, model, api_key, credit_cost) VALUES ($1, $2, 'pending', $3, $4, $5)`,
		j.ID, j.DeliberationID, j.Model, j.APIKey, j.CreditCost,
	)
	return err
}

func (s *DB) ClaimJob() (*Job, error) {
	j := &Job{}
	err := s.db.QueryRow(
		`UPDATE jobs SET status = 'running' WHERE id = (SELECT id FROM jobs WHERE status = 'pending' ORDER BY created_at LIMIT 1) RETURNING id, deliberation_id, model, api_key, credit_cost`,
	).Scan(&j.ID, &j.DeliberationID, &j.Model, &j.APIKey, &j.CreditCost)
	if err != nil {
		return nil, err
	}
	j.Status = "running"
	return j, nil
}

func (s *DB) CompleteJob(id, status, errMsg string) error {
	_, err := s.db.Exec(
		`UPDATE jobs SET status = $1, error = $2, completed_at = NOW() WHERE id = $3`,
		status, errMsg, id,
	)
	return err
}

func (s *DB) GetPendingJobs() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status = 'pending'`).Scan(&count)
	return count, err
}

func (s *DB) RecoverStuckJobs(maxAge time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-maxAge)
	res, err := s.db.Exec(
		`UPDATE jobs SET status = 'pending' WHERE status = 'running' AND created_at < $1`,
		cutoff,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// TestExec exposes raw SQL execution for testing only (e.g., manipulating timestamps).
// Do not use in production code.
func (s *DB) TestExec(query string, args ...any) error {
	_, err := s.db.Exec(query, args...)
	return err
}

func marshalRules(rules map[string]any) string {
	if len(rules) == 0 {
		return "{}"
	}
	b, err := json.Marshal(rules)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func unmarshalRules(s string) map[string]any {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

// rowsWrapper avoids code duplication for conditional queries.
type rowsWrapper struct {
	rows interface {
		Close() error
		Next() bool
		Scan(dest ...any) error
		Err() error
	}
}

func (r *rowsWrapper) Close() error           { return r.rows.Close() }
func (r *rowsWrapper) Next() bool             { return r.rows.Next() }
func (r *rowsWrapper) Scan(dest ...any) error { return r.rows.Scan(dest...) }
func (r *rowsWrapper) Err() error             { return r.rows.Err() }

// GetStatusChangedAt returns the time of the last status change for a deliberation.
func (s *DB) GetStatusChangedAt(ctx context.Context, deliberationID string) (time.Time, error) {
	var ts sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT status_changed_at FROM deliberations WHERE id = $1`, deliberationID,
	).Scan(&ts)
	if err != nil {
		return time.Time{}, err
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return ts.Time, nil
}

// RecordContextAccess tracks that an agent called get_context for a given round.
func (s *DB) RecordContextAccess(ctx context.Context, deliberationID, agentID string, round int) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO context_access (deliberation_id, agent_id, round) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		deliberationID, agentID, round,
	)
	return err
}

// HasContextAccess checks if an agent has called get_context for a given round.
func (s *DB) HasContextAccess(ctx context.Context, deliberationID, agentID string, round int) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM context_access WHERE deliberation_id = $1 AND agent_id = $2 AND round = $3`,
		deliberationID, agentID, round,
	).Scan(&count)
	return count > 0, err
}

// GetAuditLog returns audit entries for a deliberation.
// Agents can query this to verify their operations were recorded (mutual verification).
func (s *DB) GetAuditLog(deliberationID string, limit int) ([]map[string]string, error) {
	var rows *sql.Rows
	var err error
	if limit < 0 {
		// No limit — return all entries (used by export)
		rows, err = s.db.Query(
			`SELECT id, COALESCE(timestamp, NOW()), COALESCE(key_id,''), method, COALESCE(agent_id,'') FROM audit_log WHERE deliberation_id = $1 ORDER BY id ASC`,
			deliberationID,
		)
	} else {
		if limit == 0 {
			limit = 50
		}
		rows, err = s.db.Query(
			`SELECT id, COALESCE(timestamp, NOW()), COALESCE(key_id,''), method, COALESCE(agent_id,'') FROM audit_log WHERE deliberation_id = $1 ORDER BY id ASC LIMIT $2`,
			deliberationID, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var result []map[string]string
	for rows.Next() {
		var seq int
		var ts time.Time
		var kid, method, aid string
		if err := rows.Scan(&seq, &ts, &kid, &method, &aid); err != nil {
			return nil, err
		}
		result = append(result, map[string]string{
			"sequence": fmt.Sprintf("%d", seq), "timestamp": ts.Format(time.RFC3339), "key_id": kid, "method": method, "agent_id": aid,
		})
	}
	return result, nil
}

// CreateAbuseReport stores an abuse report for manual review.
func (s *DB) CreateAbuseReport(ctx context.Context, deliberationID, reporterKey, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO abuse_reports (id, deliberation_id, reporter_key, reason) VALUES ($1, $2, $3, $4)`,
		uuid.New().String(), deliberationID, reporterKey, reason,
	)
	return err
}

// LogAuditEvent records a write operation for audit purposes.
func (s *DB) LogAuditEvent(keyID, ip, method, deliberationID, agentID string) {
	s.db.Exec( //nolint:errcheck
		`INSERT INTO audit_log (key_id, ip, method, deliberation_id, agent_id) VALUES ($1, $2, $3, $4, $5)`,
		keyID, ip, method, deliberationID, agentID,
	)
}

// purgeByQuery finds deliberation IDs matching the query and hard-deletes them.
func (s *DB) purgeByQuery(query string, args ...any) (int, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close() //nolint:errcheck
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		s.hardDeleteDeliberation(id) //nolint:errcheck
	}
	return len(ids), nil
}

// DeleteExpiredSandboxDeliberations hard-deletes old sandbox deliberations.
// Sandbox data has no compliance value — safe to purge.
func (s *DB) DeleteExpiredSandboxDeliberations(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).UTC()
	return s.purgeByQuery(
		`SELECT id FROM deliberations WHERE visibility = 'link' AND template != '' AND created_at < $1`, cutoff,
	)
}

// PurgeSoftDeleted hard-deletes deliberations that were soft-deleted more than maxAge ago.
func (s *DB) PurgeSoftDeleted(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).UTC()
	return s.purgeByQuery(
		`SELECT id FROM deliberations WHERE status = 'deleted' AND status_changed_at < $1`, cutoff,
	)
}

// CreateShareToken stores a share token for a group.
func (s *DB) CreateShareToken(ctx context.Context, token, groupID string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO share_tokens (token, group_id, expires_at) VALUES ($1, $2, $3)`,
		token, groupID, expiresAt,
	)
	return err
}

// LookupShareToken returns the group ID for a valid (non-expired) share token.
func (s *DB) LookupShareToken(ctx context.Context, token string) (string, error) {
	var groupID string
	var expiresAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT group_id, expires_at FROM share_tokens WHERE token = $1`, token,
	).Scan(&groupID, &expiresAt)
	if err != nil {
		return "", fmt.Errorf("share token not found")
	}
	if expiresAt.Valid && time.Now().After(expiresAt.Time) {
		return "", fmt.Errorf("share token expired")
	}
	return groupID, nil
}

// WithdrawPositions marks all positions by an agent in a deliberation as drafts (invisible).
func (s *DB) WithdrawPositions(ctx context.Context, deliberationID, agentID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE positions SET draft = 1 WHERE deliberation_id = $1 AND agent_id = $2`,
		deliberationID, agentID,
	)
	return err
}

// DeleteVotesByAgent removes all votes by an agent in a deliberation.
func (s *DB) DeleteVotesByAgent(ctx context.Context, deliberationID, agentID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM votes WHERE deliberation_id = $1 AND agent_id = $2`,
		deliberationID, agentID,
	)
	return err
}

// DeleteDelegationsByAgent deactivates all delegations from or to an agent in a deliberation.
func (s *DB) DeleteDelegationsByAgent(ctx context.Context, deliberationID, agentID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE delegations SET active = 0 WHERE deliberation_id = $1 AND (from_agent = $2 OR to_agent = $2)`,
		deliberationID, agentID,
	)
	return err
}

// hardDeleteDeliberation permanently removes a deliberation and all related data.
func (s *DB) hardDeleteDeliberation(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, table := range []string{
		"analysis_results", "votes", "positions", "commitments",
		"delegations", "disputes", "invitations", "join_codes", "deliberation_acl",
	} {
		tx.Exec("DELETE FROM "+table+" WHERE deliberation_id = $1", id) //nolint:errcheck
	}
	tx.Exec("DELETE FROM deliberations WHERE id = $1", id) //nolint:errcheck
	return tx.Commit()
}
