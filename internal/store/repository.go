package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

// deliberationColumns is the canonical SELECT column list for deliberation queries.
const deliberationColumns = `id, topic, description, round_number, status, COALESCE(sub_status, ''), COALESCE(type, ''), COALESCE(visibility, 'open'), COALESCE(creator_key, ''), COALESCE(max_participants, 0), COALESCE(template, ''), COALESCE(rules, '{}'), COALESCE(group_id, ''), created_at`

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanDeliberation scans a row into a Deliberation using the deliberationColumns layout.
func scanDeliberation(s scanner) (deliberation.Deliberation, error) {
	var d deliberation.Deliberation
	var createdAt time.Time
	var rulesJSON string
	if err := s.Scan(&d.ID, &d.Topic, &d.Description, &d.Round, &d.Status, &d.SubStatus, &d.Type, &d.Visibility, &d.CreatorKey, &d.MaxParticipants, &d.Template, &rulesJSON, &d.GroupID, &createdAt); err != nil {
		return d, err
	}
	d.CreatedAt = createdAt
	d.Rules = unmarshalRules(rulesJSON)
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

func (s *DB) CreateDeliberation(d *deliberation.Deliberation) error {
	d.ID = uuid.New().String()
	d.CreatedAt = time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO deliberations (id, topic, description, round_number, status, type, visibility, creator_key, max_participants, template, rules, group_id, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		d.ID, d.Topic, d.Description, d.Round, d.Status, d.Type, d.Visibility, d.CreatorKey, d.MaxParticipants, d.Template, marshalRules(d.Rules), d.GroupID, d.CreatedAt,
	)
	return err
}

func (s *DB) GetDeliberation(id string) (*deliberation.Deliberation, error) {
	row := s.db.QueryRow(`SELECT `+deliberationColumns+` FROM deliberations WHERE id = $1`, id)
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

func (s *DB) ListDeliberations(limit, offset int) ([]deliberation.Deliberation, error) {
	limit, offset = normalizePagination(limit, offset)
	rows, err := s.db.Query(`SELECT `+deliberationColumns+` FROM deliberations WHERE status != 'deleted' ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	return scanDeliberationRows(rows)
}

// ListByGroup returns deliberations in a group, ordered by creation time.
func (s *DB) ListByGroup(groupID string, limit, offset int) ([]deliberation.Deliberation, error) {
	limit, offset = normalizePagination(limit, offset)
	rows, err := s.db.Query(`SELECT `+deliberationColumns+` FROM deliberations WHERE group_id = $1 AND status != 'deleted' ORDER BY created_at LIMIT $2 OFFSET $3`, groupID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeliberationRows(rows)
}

// ListByAgent returns deliberations where an agent has submitted positions.
func (s *DB) ListByAgent(agentID string, limit, offset int) ([]deliberation.Deliberation, error) {
	limit, offset = normalizePagination(limit, offset)
	// deliberationColumns uses unqualified names; prefix with "d." for the JOIN query.
	const cols = `d.id, d.topic, d.description, d.round_number, d.status, COALESCE(d.sub_status, ''), COALESCE(d.type, ''), COALESCE(d.visibility, 'open'), COALESCE(d.creator_key, ''), COALESCE(d.max_participants, 0), COALESCE(d.template, ''), COALESCE(d.rules, '{}'), COALESCE(d.group_id, ''), d.created_at`
	rows, err := s.db.Query(`SELECT DISTINCT `+cols+` FROM deliberations d JOIN positions p ON d.id = p.deliberation_id WHERE p.agent_id = $1 AND d.status != 'deleted' ORDER BY d.created_at DESC LIMIT $2 OFFSET $3`, agentID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeliberationRows(rows)
}

func (s *DB) SetGroupID(deliberationID, groupID string) error {
	_, err := s.db.Exec(`UPDATE deliberations SET group_id = $1 WHERE id = $2`, groupID, deliberationID)
	return err
}

func (s *DB) UpdateDeliberationTemplate(id, template string) error {
	res, err := s.db.Exec(`UPDATE deliberations SET template = $1 WHERE id = $2`, template, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	return nil
}

func (s *DB) UpdateDeliberationRules(id string, rules map[string]any) error {
	res, err := s.db.Exec(`UPDATE deliberations SET rules = $1 WHERE id = $2`, marshalRules(rules), id)
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
func (s *DB) DeleteDeliberation(id string) error {
	res, err := s.db.Exec(
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

func (s *DB) UpdateDeliberationStatus(id, status string) error {
	res, err := s.db.Exec(`UPDATE deliberations SET status = $1, sub_status = '', status_changed_at = NOW() WHERE id = $2`, status, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	return nil
}

func (s *DB) UpdateSubStatus(id, subStatus string) error {
	_, err := s.db.Exec(`UPDATE deliberations SET sub_status = $1 WHERE id = $2`, subStatus, id)
	return err
}

// TrySetAnalyzing atomically transitions status from "open" to "analyzing".
// Returns false if the deliberation is not in "open" status (prevents race conditions).
func (s *DB) TrySetAnalyzing(id string) (bool, error) {
	res, err := s.db.Exec(`UPDATE deliberations SET status = 'analyzing', status_changed_at = NOW() WHERE id = $1 AND status = 'open'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *DB) AdvanceRound(id string) error {
	res, err := s.db.Exec(`UPDATE deliberations SET round_number = round_number + 1 WHERE id = $1`, id)
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
func (s *DB) CountPositions(deliberationID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM positions WHERE deliberation_id = $1`, deliberationID).Scan(&count)
	return count, err
}

func (s *DB) CreatePosition(p *deliberation.Position) error {
	p.ID = uuid.New().String()
	p.CreatedAt = time.Now().UTC()
	draft := 0
	if p.Draft {
		draft = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO positions (id, deliberation_id, agent_id, content, model_family, group_name, conviction, reservation, on_behalf_of, interests, draft, round_number, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		p.ID, p.DeliberationID, p.AgentID, p.Content, p.ModelFamily, p.Group, p.Conviction, p.Reservation, p.OnBehalfOf, p.Interests, draft, p.Round, p.CreatedAt,
	)
	return err
}

func (s *DB) GetPositions(deliberationID string, round *int) ([]deliberation.Position, error) {
	var rows *rowsWrapper
	if round != nil {
		r, err := s.db.Query(
			`SELECT id, deliberation_id, agent_id, content, COALESCE(model_family, ''), COALESCE(group_name, ''), COALESCE(conviction, 0.5), COALESCE(reservation, ''), COALESCE(on_behalf_of, ''), COALESCE(interests, ''), COALESCE(draft, 0), round_number, created_at FROM positions WHERE deliberation_id = $1 AND round_number = $2 AND COALESCE(draft, 0) = 0 ORDER BY created_at`,
			deliberationID, *round,
		)
		if err != nil {
			return nil, err
		}
		rows = &rowsWrapper{r}
	} else {
		r, err := s.db.Query(
			`SELECT id, deliberation_id, agent_id, content, COALESCE(model_family, ''), COALESCE(group_name, ''), COALESCE(conviction, 0.5), COALESCE(reservation, ''), COALESCE(on_behalf_of, ''), COALESCE(interests, ''), COALESCE(draft, 0), round_number, created_at FROM positions WHERE deliberation_id = $1 AND COALESCE(draft, 0) = 0 ORDER BY created_at`,
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
		if err := rows.Scan(&p.ID, &p.DeliberationID, &p.AgentID, &p.Content, &p.ModelFamily, &p.Group, &p.Conviction, &p.Reservation, &p.OnBehalfOf, &p.Interests, &draftInt, &p.Round, &createdAt); err != nil {
			return nil, err
		}
		p.Draft = draftInt == 1
		p.CreatedAt = createdAt
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *DB) GetPositionByID(id string) (*deliberation.Position, error) {
	p := &deliberation.Position{}
	var createdAt time.Time
	var draftInt int
	err := s.db.QueryRow(
		`SELECT id, deliberation_id, agent_id, content, COALESCE(model_family, ''), COALESCE(group_name, ''), COALESCE(conviction, 0.5), COALESCE(reservation, ''), COALESCE(on_behalf_of, ''), COALESCE(interests, ''), COALESCE(draft, 0), round_number, created_at FROM positions WHERE id = $1`, id,
	).Scan(&p.ID, &p.DeliberationID, &p.AgentID, &p.Content, &p.ModelFamily, &p.Group, &p.Conviction, &p.Reservation, &p.OnBehalfOf, &p.Interests, &draftInt, &p.Round, &createdAt)
	p.Draft = draftInt == 1
	if err != nil {
		return nil, err
	}
	p.CreatedAt = createdAt
	return p, nil
}

func (s *DB) CreateVote(v *deliberation.Vote) error {
	v.ID = uuid.New().String()
	v.CreatedAt = time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO votes (id, deliberation_id, agent_id, position_id, value, criterion_id, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (deliberation_id, agent_id, position_id, criterion_id) DO UPDATE SET id = $1, value = $5, created_at = $7`,
		v.ID, v.DeliberationID, v.AgentID, v.PositionID, v.Value, v.CriterionID, v.CreatedAt,
	)
	return err
}

func (s *DB) GetVotes(deliberationID string) ([]deliberation.Vote, error) {
	rows, err := s.db.Query(
		`SELECT id, deliberation_id, agent_id, position_id, value, COALESCE(criterion_id, ''), created_at FROM votes WHERE deliberation_id = $1 ORDER BY created_at`,
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
		if err := rows.Scan(&v.ID, &v.DeliberationID, &v.AgentID, &v.PositionID, &v.Value, &v.CriterionID, &createdAt); err != nil {
			return nil, err
		}
		v.CreatedAt = createdAt
		result = append(result, v)
	}
	return result, rows.Err()
}

// GetVotesByRound returns votes on positions from a specific round.
func (s *DB) GetVotesByRound(deliberationID string, round int) ([]deliberation.Vote, error) {
	rows, err := s.db.Query(
		`SELECT v.id, v.deliberation_id, v.agent_id, v.position_id, v.value, COALESCE(v.criterion_id, ''), v.created_at
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
		if err := rows.Scan(&v.ID, &v.DeliberationID, &v.AgentID, &v.PositionID, &v.Value, &v.CriterionID, &createdAt); err != nil {
			return nil, err
		}
		v.CreatedAt = createdAt
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *DB) SaveAnalysisResult(deliberationID string, round int, result *deliberation.AnalysisResult) error {
	b, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshaling analysis result: %w", err)
	}
	id := uuid.New().String()
	_, err = s.db.Exec(
		`INSERT INTO analysis_results (id, deliberation_id, round_number, result_json, analyzed_at) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (deliberation_id, round_number) DO UPDATE SET id = $1, result_json = $4, analyzed_at = $5`,
		id, deliberationID, round, string(b), time.Now().UTC(),
	)
	return err
}

func (s *DB) GetAnalysisResult(deliberationID string, round int) (*deliberation.AnalysisResult, error) {
	var resultJSON string
	err := s.db.QueryRow(
		`SELECT result_json FROM analysis_results WHERE deliberation_id = $1 AND round_number = $2`,
		deliberationID, round,
	).Scan(&resultJSON)
	if err != nil {
		return nil, err
	}
	var result deliberation.AnalysisResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *DB) GetLatestAnalysisResult(deliberationID string) (*deliberation.AnalysisResult, error) {
	var resultJSON string
	err := s.db.QueryRow(
		`SELECT result_json FROM analysis_results WHERE deliberation_id = $1 ORDER BY round_number DESC LIMIT 1`,
		deliberationID,
	).Scan(&resultJSON)
	if err != nil {
		return nil, err
	}
	var result deliberation.AnalysisResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetStuckAnalyzing returns deliberation IDs stuck in "analyzing" for longer than maxAge.
func (s *DB) GetStuckAnalyzing(maxAge time.Duration) ([]string, error) {
	cutoff := time.Now().UTC().Add(-maxAge)
	rows, err := s.db.Query(
		`SELECT id FROM deliberations WHERE status = 'analyzing' AND COALESCE(status_changed_at, created_at) < $1`,
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

func (s *DB) RecoverStuckAnalyzing(maxAge time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-maxAge)
	res, err := s.db.Exec(
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

func (s *DB) CreateDelegation(d *deliberation.Delegation) error {
	d.ID = uuid.New().String()
	d.CreatedAt = time.Now().UTC()
	d.Active = true
	_, err := s.db.Exec(
		`INSERT INTO delegations (id, deliberation_id, from_agent, to_agent, scope, active, created_at) VALUES ($1, $2, $3, $4, $5, 1, $6)
		 ON CONFLICT (deliberation_id, from_agent) DO UPDATE SET id = $1, to_agent = $4, scope = $5, active = 1, created_at = $6`,
		d.ID, d.DeliberationID, d.FromAgent, d.ToAgent, d.Scope, d.CreatedAt,
	)
	return err
}

func (s *DB) RevokeDelegation(deliberationID, fromAgent string) error {
	_, err := s.db.Exec(
		`UPDATE delegations SET active = 0 WHERE deliberation_id = $1 AND from_agent = $2`,
		deliberationID, fromAgent,
	)
	return err
}

func (s *DB) GetDelegations(deliberationID string) ([]deliberation.Delegation, error) {
	rows, err := s.db.Query(
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

func (s *DB) PublishPosition(id string) error {
	_, err := s.db.Exec(`UPDATE positions SET draft = 0 WHERE id = $1`, id)
	return err
}

func (s *DB) CreateCommitment(c *deliberation.Commitment) error {
	c.ID = uuid.New().String()
	c.CreatedAt = time.Now().UTC()
	c.Status = "pending"
	_, err := s.db.Exec(
		`INSERT INTO commitments (id, deliberation_id, agent_id, analysis_round, statement, conditional, status, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		c.ID, c.DeliberationID, c.AgentID, c.AnalysisRound, c.Statement, c.Conditional, c.Status, c.CreatedAt,
	)
	return err
}

func (s *DB) GetCommitments(deliberationID string) ([]deliberation.Commitment, error) {
	return s.scanCommitments(
		`SELECT id, deliberation_id, agent_id, analysis_round, statement, COALESCE(conditional, ''), status, created_at, fulfilled_at, broken_at, COALESCE(broken_reason, ''), COALESCE(verified_by, '') FROM commitments WHERE deliberation_id = $1 ORDER BY created_at`,
		deliberationID,
	)
}

func (s *DB) GetCommitmentsByAgent(agentID string) ([]deliberation.Commitment, error) {
	return s.scanCommitments(
		`SELECT id, deliberation_id, agent_id, analysis_round, statement, COALESCE(conditional, ''), status, created_at, fulfilled_at, broken_at, COALESCE(broken_reason, ''), COALESCE(verified_by, '') FROM commitments WHERE agent_id = $1 ORDER BY created_at`,
		agentID,
	)
}

func (s *DB) GetCommitmentsByGroup(groupID string) ([]deliberation.Commitment, error) {
	return s.scanCommitments(
		`SELECT c.id, c.deliberation_id, c.agent_id, c.analysis_round, c.statement, COALESCE(c.conditional, ''), c.status, c.created_at, c.fulfilled_at, c.broken_at, COALESCE(c.broken_reason, ''), COALESCE(c.verified_by, '') FROM commitments c JOIN deliberations d ON c.deliberation_id = d.id WHERE d.group_id = $1 ORDER BY c.created_at`,
		groupID,
	)
}

func (s *DB) scanCommitments(query string, args ...any) ([]deliberation.Commitment, error) {
	rows, err := s.db.Query(query, args...)
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

func (s *DB) UpdateCommitmentStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE commitments SET status = $1 WHERE id = $2`, status, id)
	return err
}

func (s *DB) FulfillCommitment(id, verifiedBy string) error {
	_, err := s.db.Exec(
		`UPDATE commitments SET status = 'fulfilled', fulfilled_at = NOW(), verified_by = $1 WHERE id = $2`,
		verifiedBy, id,
	)
	return err
}

func (s *DB) BreakCommitment(id, reason, verifiedBy string) error {
	_, err := s.db.Exec(
		`UPDATE commitments SET status = 'broken', broken_at = NOW(), broken_reason = $1, verified_by = $2 WHERE id = $3`,
		reason, verifiedBy, id,
	)
	return err
}

func (s *DB) CreateJoinCode(jc *deliberation.JoinCode) error {
	_, err := s.db.Exec(
		`INSERT INTO join_codes (code, deliberation_id, role, expires_at, created_at, max_uses, use_count) VALUES ($1, $2, $3, $4, $5, $6, 0)`,
		jc.Code, jc.DeliberationID, jc.Role, jc.ExpiresAt, jc.CreatedAt, jc.MaxUses,
	)
	return err
}

func (s *DB) ClaimJoinCode(code, agentID string) (*deliberation.JoinCode, error) {
	jc := &deliberation.JoinCode{}
	var expiresAt, createdAt time.Time
	err := s.db.QueryRow(
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

	res, err := s.db.Exec(
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

func (s *DB) LookupJoinCode(code string) (*deliberation.JoinCode, error) {
	jc := &deliberation.JoinCode{}
	var expiresAt, createdAt time.Time
	err := s.db.QueryRow(
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

func (s *DB) AddToACL(deliberationID, keyID string) error {
	_, err := s.db.Exec(
		`INSERT INTO deliberation_acl (deliberation_id, key_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		deliberationID, keyID,
	)
	return err
}

func (s *DB) CheckACL(deliberationID, keyID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM deliberation_acl WHERE deliberation_id = $1 AND key_id = $2`,
		deliberationID, keyID,
	).Scan(&count)
	return count > 0, err
}

func (s *DB) CreateInvitation(inv *deliberation.Invitation) error {
	inv.ID = uuid.New().String()
	inv.CreatedAt = time.Now().UTC()
	inv.Status = "pending"
	_, err := s.db.Exec(
		`INSERT INTO invitations (id, deliberation_id, invited_by, invited_agent, role, reason, status, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		inv.ID, inv.DeliberationID, inv.InvitedBy, inv.InvitedAgent, inv.Role, inv.Reason, inv.Status, inv.CreatedAt,
	)
	return err
}

func (s *DB) GetInvitations(deliberationID string) ([]deliberation.Invitation, error) {
	rows, err := s.db.Query(
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

func (s *DB) UpdateInvitationStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE invitations SET status = $1 WHERE id = $2`, status, id)
	return err
}

func (s *DB) CreateDispute(d *deliberation.Dispute) error {
	d.ID = uuid.New().String()
	d.CreatedAt = time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO disputes (id, deliberation_id, agent_id, crux_claim, correction, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		d.ID, d.DeliberationID, d.AgentID, d.CruxClaim, d.Correction, d.CreatedAt,
	)
	return err
}

func (s *DB) GetDisputes(deliberationID string) ([]deliberation.Dispute, error) {
	rows, err := s.db.Query(
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
func (s *DB) GetStatusChangedAt(deliberationID string) (time.Time, error) {
	var ts sql.NullTime
	err := s.db.QueryRow(
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
func (s *DB) RecordContextAccess(deliberationID, agentID string, round int) error {
	_, err := s.db.Exec(
		`INSERT INTO context_access (deliberation_id, agent_id, round) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		deliberationID, agentID, round,
	)
	return err
}

// HasContextAccess checks if an agent has called get_context for a given round.
func (s *DB) HasContextAccess(deliberationID, agentID string, round int) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM context_access WHERE deliberation_id = $1 AND agent_id = $2 AND round = $3`,
		deliberationID, agentID, round,
	).Scan(&count)
	return count > 0, err
}

// GetAuditLog returns audit entries for a deliberation.
// Agents can query this to verify their operations were recorded (mutual verification).
func (s *DB) GetAuditLog(deliberationID string, limit int) ([]map[string]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, COALESCE(timestamp, NOW()), COALESCE(key_id,''), method, COALESCE(agent_id,'') FROM audit_log WHERE deliberation_id = $1 ORDER BY id ASC LIMIT $2`,
		deliberationID, limit,
	)
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
func (s *DB) CreateAbuseReport(deliberationID, reporterKey, reason string) error {
	_, err := s.db.Exec(
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

// DeleteExpiredSandboxDeliberations hard-deletes old sandbox deliberations.
// Sandbox data has no compliance value — safe to purge.
func (s *DB) DeleteExpiredSandboxDeliberations(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).UTC()
	rows, err := s.db.Query(
		`SELECT id FROM deliberations WHERE visibility = 'link' AND template != '' AND created_at < $1`, cutoff,
	)
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

// PurgeSoftDeleted hard-deletes deliberations that were soft-deleted more than maxAge ago.
func (s *DB) PurgeSoftDeleted(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).UTC()
	rows, err := s.db.Query(
		`SELECT id FROM deliberations WHERE status = 'deleted' AND status_changed_at < $1`, cutoff,
	)
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

// CreateShareToken stores a share token for a group.
func (s *DB) CreateShareToken(token, groupID string, expiresAt time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO share_tokens (token, group_id, expires_at) VALUES ($1, $2, $3)`,
		token, groupID, expiresAt,
	)
	return err
}

// LookupShareToken returns the group ID for a valid (non-expired) share token.
func (s *DB) LookupShareToken(token string) (string, error) {
	var groupID string
	var expiresAt sql.NullTime
	err := s.db.QueryRow(
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
