package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

func (s *DB) CreateDeliberation(d *deliberation.Deliberation) error {
	d.ID = uuid.New().String()
	d.CreatedAt = time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO deliberations (id, topic, description, round_number, status, type, visibility, creator_key, max_participants, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Topic, d.Description, d.Round, d.Status, d.Type, d.Visibility, d.CreatorKey, d.MaxParticipants, d.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *DB) GetDeliberation(id string) (*deliberation.Deliberation, error) {
	d := &deliberation.Deliberation{}
	var createdAt string
	err := s.db.QueryRow(
		`SELECT id, topic, description, round_number, status, COALESCE(sub_status, ''), COALESCE(type, ''), COALESCE(visibility, 'open'), COALESCE(creator_key, ''), COALESCE(max_participants, 0), created_at FROM deliberations WHERE id = ?`, id,
	).Scan(&d.ID, &d.Topic, &d.Description, &d.Round, &d.Status, &d.SubStatus, &d.Type, &d.Visibility, &d.CreatorKey, &d.MaxParticipants, &createdAt)
	if err != nil {
		return nil, err
	}
	d.CreatedAt = parseTime(createdAt)
	return d, nil
}

func (s *DB) ListDeliberations() ([]deliberation.Deliberation, error) {
	rows, err := s.db.Query(`SELECT id, topic, description, round_number, status, COALESCE(sub_status, ''), COALESCE(type, ''), COALESCE(visibility, 'open'), COALESCE(creator_key, ''), COALESCE(max_participants, 0), created_at FROM deliberations ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var result []deliberation.Deliberation
	for rows.Next() {
		var d deliberation.Deliberation
		var createdAt string
		if err := rows.Scan(&d.ID, &d.Topic, &d.Description, &d.Round, &d.Status, &d.SubStatus, &d.Type, &d.Visibility, &d.CreatorKey, &d.MaxParticipants, &createdAt); err != nil {
			return nil, err
		}
		d.CreatedAt = parseTime(createdAt)
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *DB) UpdateDeliberationStatus(id, status string) error {
	res, err := s.db.Exec(`UPDATE deliberations SET status = ?, sub_status = '', status_changed_at = datetime('now') WHERE id = ?`, status, id)
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
	_, err := s.db.Exec(`UPDATE deliberations SET sub_status = ? WHERE id = ?`, subStatus, id)
	return err
}

// TrySetAnalyzing atomically transitions status from "open" to "analyzing".
// Returns false if the deliberation is not in "open" status (prevents race conditions).
func (s *DB) TrySetAnalyzing(id string) (bool, error) {
	res, err := s.db.Exec(`UPDATE deliberations SET status = 'analyzing', status_changed_at = datetime('now') WHERE id = ? AND status = 'open'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *DB) AdvanceRound(id string) error {
	res, err := s.db.Exec(`UPDATE deliberations SET round_number = round_number + 1 WHERE id = ?`, id)
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
	err := s.db.QueryRow(`SELECT COUNT(*) FROM positions WHERE deliberation_id = ?`, deliberationID).Scan(&count)
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
		`INSERT INTO positions (id, deliberation_id, agent_id, content, model_family, group_name, conviction, reservation, on_behalf_of, draft, round_number, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.DeliberationID, p.AgentID, p.Content, p.ModelFamily, p.Group, p.Conviction, p.Reservation, p.OnBehalfOf, draft, p.Round, p.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *DB) GetPositions(deliberationID string, round *int) ([]deliberation.Position, error) {
	var rows *rowsWrapper
	if round != nil {
		r, err := s.db.Query(
			`SELECT id, deliberation_id, agent_id, content, COALESCE(model_family, ''), COALESCE(group_name, ''), COALESCE(conviction, 0.5), COALESCE(reservation, ''), COALESCE(on_behalf_of, ''), COALESCE(draft, 0), round_number, created_at FROM positions WHERE deliberation_id = ? AND round_number = ? AND COALESCE(draft, 0) = 0 ORDER BY created_at`,
			deliberationID, *round,
		)
		if err != nil {
			return nil, err
		}
		rows = &rowsWrapper{r}
	} else {
		r, err := s.db.Query(
			`SELECT id, deliberation_id, agent_id, content, COALESCE(model_family, ''), COALESCE(group_name, ''), COALESCE(conviction, 0.5), COALESCE(reservation, ''), COALESCE(on_behalf_of, ''), COALESCE(draft, 0), round_number, created_at FROM positions WHERE deliberation_id = ? AND COALESCE(draft, 0) = 0 ORDER BY created_at`,
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
		var createdAt string
		var draftInt int
		if err := rows.Scan(&p.ID, &p.DeliberationID, &p.AgentID, &p.Content, &p.ModelFamily, &p.Group, &p.Conviction, &p.Reservation, &p.OnBehalfOf, &draftInt, &p.Round, &createdAt); err != nil {
			return nil, err
		}
		p.Draft = draftInt == 1
		p.CreatedAt = parseTime(createdAt)
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *DB) GetPositionByID(id string) (*deliberation.Position, error) {
	p := &deliberation.Position{}
	var createdAt string
	var draftInt int
	err := s.db.QueryRow(
		`SELECT id, deliberation_id, agent_id, content, COALESCE(model_family, ''), COALESCE(group_name, ''), COALESCE(conviction, 0.5), COALESCE(reservation, ''), COALESCE(on_behalf_of, ''), COALESCE(draft, 0), round_number, created_at FROM positions WHERE id = ?`, id,
	).Scan(&p.ID, &p.DeliberationID, &p.AgentID, &p.Content, &p.ModelFamily, &p.Group, &p.Conviction, &p.Reservation, &p.OnBehalfOf, &draftInt, &p.Round, &createdAt)
	p.Draft = draftInt == 1
	if err != nil {
		return nil, err
	}
	p.CreatedAt = parseTime(createdAt)
	return p, nil
}

func (s *DB) CreateVote(v *deliberation.Vote) error {
	v.ID = uuid.New().String()
	v.CreatedAt = time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO votes (id, deliberation_id, agent_id, position_id, value, criterion_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.DeliberationID, v.AgentID, v.PositionID, v.Value, v.CriterionID, v.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *DB) GetVotes(deliberationID string) ([]deliberation.Vote, error) {
	rows, err := s.db.Query(
		`SELECT id, deliberation_id, agent_id, position_id, value, COALESCE(criterion_id, ''), created_at FROM votes WHERE deliberation_id = ? ORDER BY created_at`,
		deliberationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var result []deliberation.Vote
	for rows.Next() {
		var v deliberation.Vote
		var createdAt string
		if err := rows.Scan(&v.ID, &v.DeliberationID, &v.AgentID, &v.PositionID, &v.Value, &v.CriterionID, &createdAt); err != nil {
			return nil, err
		}
		v.CreatedAt = parseTime(createdAt)
		result = append(result, v)
	}
	return result, rows.Err()
}

// GetVotesByRound returns votes on positions from a specific round.
func (s *DB) GetVotesByRound(deliberationID string, round int) ([]deliberation.Vote, error) {
	rows, err := s.db.Query(
		`SELECT v.id, v.deliberation_id, v.agent_id, v.position_id, v.value, COALESCE(v.criterion_id, ''), v.created_at
		 FROM votes v JOIN positions p ON v.position_id = p.id
		 WHERE v.deliberation_id = ? AND p.round_number = ?
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
		var createdAt string
		if err := rows.Scan(&v.ID, &v.DeliberationID, &v.AgentID, &v.PositionID, &v.Value, &v.CriterionID, &createdAt); err != nil {
			return nil, err
		}
		v.CreatedAt = parseTime(createdAt)
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
		`INSERT OR REPLACE INTO analysis_results (id, deliberation_id, round_number, result_json, analyzed_at) VALUES (?, ?, ?, ?, ?)`,
		id, deliberationID, round, string(b), time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (s *DB) GetAnalysisResult(deliberationID string, round int) (*deliberation.AnalysisResult, error) {
	var resultJSON string
	err := s.db.QueryRow(
		`SELECT result_json FROM analysis_results WHERE deliberation_id = ? AND round_number = ?`,
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
		`SELECT result_json FROM analysis_results WHERE deliberation_id = ? ORDER BY round_number DESC LIMIT 1`,
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

// RecoverStuckAnalyzing resets deliberations stuck in "analyzing" status
// back to "open" if their created_at is older than maxAge.
// Returns the count of recovered deliberations.
func (s *DB) RecoverStuckAnalyzing(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).Format(time.RFC3339)
	res, err := s.db.Exec(
		`UPDATE deliberations SET status = 'open', sub_status = '', status_changed_at = datetime('now')
		 WHERE status = 'analyzing' AND COALESCE(status_changed_at, created_at) < ?`,
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
	var createdAt string
	err := s.db.QueryRow(`SELECT response_json, created_at FROM llm_cache WHERE cache_key = ?`, key).Scan(&response, &createdAt)
	if err != nil {
		return ""
	}
	if maxAge > 0 {
		t := parseTime(createdAt)
		if time.Since(t) > maxAge {
			return ""
		}
	}
	return response
}

// CachePut stores an LLM response by key.
func (s *DB) CachePut(key, response, model string) {
	s.db.Exec( //nolint:errcheck
		`INSERT OR REPLACE INTO llm_cache (cache_key, response_json, model, created_at) VALUES (?, ?, ?, datetime('now'))`,
		key, response, model,
	)
}

func (s *DB) CreateDelegation(d *deliberation.Delegation) error {
	d.ID = uuid.New().String()
	d.CreatedAt = time.Now().UTC()
	d.Active = true
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO delegations (id, deliberation_id, from_agent, to_agent, scope, active, created_at) VALUES (?, ?, ?, ?, ?, 1, ?)`,
		d.ID, d.DeliberationID, d.FromAgent, d.ToAgent, d.Scope, d.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *DB) RevokeDelegation(deliberationID, fromAgent string) error {
	_, err := s.db.Exec(
		`UPDATE delegations SET active = 0 WHERE deliberation_id = ? AND from_agent = ?`,
		deliberationID, fromAgent,
	)
	return err
}

func (s *DB) GetDelegations(deliberationID string) ([]deliberation.Delegation, error) {
	rows, err := s.db.Query(
		`SELECT id, deliberation_id, from_agent, to_agent, COALESCE(scope, ''), active, created_at FROM delegations WHERE deliberation_id = ? AND active = 1 ORDER BY created_at`,
		deliberationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var result []deliberation.Delegation
	for rows.Next() {
		var d deliberation.Delegation
		var createdAt string
		var active int
		if err := rows.Scan(&d.ID, &d.DeliberationID, &d.FromAgent, &d.ToAgent, &d.Scope, &active, &createdAt); err != nil {
			return nil, err
		}
		d.Active = active == 1
		d.CreatedAt = parseTime(createdAt)
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *DB) PublishPosition(id string) error {
	_, err := s.db.Exec(`UPDATE positions SET draft = 0 WHERE id = ?`, id)
	return err
}

func (s *DB) CreateCommitment(c *deliberation.Commitment) error {
	c.ID = uuid.New().String()
	c.CreatedAt = time.Now().UTC()
	c.Status = "pending"
	_, err := s.db.Exec(
		`INSERT INTO commitments (id, deliberation_id, agent_id, analysis_round, statement, conditional, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.DeliberationID, c.AgentID, c.AnalysisRound, c.Statement, c.Conditional, c.Status, c.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *DB) GetCommitments(deliberationID string) ([]deliberation.Commitment, error) {
	rows, err := s.db.Query(
		`SELECT id, deliberation_id, agent_id, analysis_round, statement, COALESCE(conditional, ''), status, created_at FROM commitments WHERE deliberation_id = ? ORDER BY created_at`,
		deliberationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var result []deliberation.Commitment
	for rows.Next() {
		var c deliberation.Commitment
		var createdAt string
		if err := rows.Scan(&c.ID, &c.DeliberationID, &c.AgentID, &c.AnalysisRound, &c.Statement, &c.Conditional, &c.Status, &createdAt); err != nil {
			return nil, err
		}
		c.CreatedAt = parseTime(createdAt)
		result = append(result, c)
	}
	return result, rows.Err()
}

func (s *DB) UpdateCommitmentStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE commitments SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *DB) CreateJoinCode(jc *deliberation.JoinCode) error {
	_, err := s.db.Exec(
		`INSERT INTO join_codes (code, deliberation_id, role, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		jc.Code, jc.DeliberationID, jc.Role, jc.ExpiresAt.Format(time.RFC3339), jc.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *DB) ClaimJoinCode(code, agentID string) (*deliberation.JoinCode, error) {
	jc := &deliberation.JoinCode{}
	var expiresAt, createdAt string
	var used int
	err := s.db.QueryRow(
		`SELECT code, deliberation_id, role, expires_at, used, used_by, created_at FROM join_codes WHERE code = ?`, code,
	).Scan(&jc.Code, &jc.DeliberationID, &jc.Role, &expiresAt, &used, &jc.UsedBy, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("invalid join code")
	}
	jc.ExpiresAt = parseTime(expiresAt)
	jc.CreatedAt = parseTime(createdAt)
	jc.Used = used == 1

	if jc.Used {
		return nil, fmt.Errorf("join code already used")
	}
	if time.Now().After(jc.ExpiresAt) {
		return nil, fmt.Errorf("join code expired")
	}

	_, err = s.db.Exec(
		`UPDATE join_codes SET used = 1, used_by = ? WHERE code = ? AND used = 0`,
		agentID, code,
	)
	if err != nil {
		return nil, err
	}
	jc.Used = true
	jc.UsedBy = agentID
	return jc, nil
}

func (s *DB) LookupJoinCode(code string) (*deliberation.JoinCode, error) {
	jc := &deliberation.JoinCode{}
	var expiresAt, createdAt string
	var used int
	err := s.db.QueryRow(
		`SELECT code, deliberation_id, role, expires_at, used, COALESCE(used_by, ''), created_at FROM join_codes WHERE code = ?`, code,
	).Scan(&jc.Code, &jc.DeliberationID, &jc.Role, &expiresAt, &used, &jc.UsedBy, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("join code not found")
	}
	jc.ExpiresAt = parseTime(expiresAt)
	jc.CreatedAt = parseTime(createdAt)
	jc.Used = used == 1
	return jc, nil
}

func (s *DB) AddToACL(deliberationID, keyID string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO deliberation_acl (deliberation_id, key_id) VALUES (?, ?)`,
		deliberationID, keyID,
	)
	return err
}

func (s *DB) CheckACL(deliberationID, keyID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM deliberation_acl WHERE deliberation_id = ? AND key_id = ?`,
		deliberationID, keyID,
	).Scan(&count)
	return count > 0, err
}

func (s *DB) CreateInvitation(inv *deliberation.Invitation) error {
	inv.ID = uuid.New().String()
	inv.CreatedAt = time.Now().UTC()
	inv.Status = "pending"
	_, err := s.db.Exec(
		`INSERT INTO invitations (id, deliberation_id, invited_by, invited_agent, role, reason, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		inv.ID, inv.DeliberationID, inv.InvitedBy, inv.InvitedAgent, inv.Role, inv.Reason, inv.Status, inv.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *DB) GetInvitations(deliberationID string) ([]deliberation.Invitation, error) {
	rows, err := s.db.Query(
		`SELECT id, deliberation_id, invited_by, invited_agent, COALESCE(role, ''), reason, status, created_at FROM invitations WHERE deliberation_id = ? ORDER BY created_at`,
		deliberationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var result []deliberation.Invitation
	for rows.Next() {
		var inv deliberation.Invitation
		var createdAt string
		if err := rows.Scan(&inv.ID, &inv.DeliberationID, &inv.InvitedBy, &inv.InvitedAgent, &inv.Role, &inv.Reason, &inv.Status, &createdAt); err != nil {
			return nil, err
		}
		inv.CreatedAt = parseTime(createdAt)
		result = append(result, inv)
	}
	return result, rows.Err()
}

func (s *DB) UpdateInvitationStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE invitations SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *DB) CreateDispute(d *deliberation.Dispute) error {
	d.ID = uuid.New().String()
	d.CreatedAt = time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO disputes (id, deliberation_id, agent_id, crux_claim, correction, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		d.ID, d.DeliberationID, d.AgentID, d.CruxClaim, d.Correction, d.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *DB) GetDisputes(deliberationID string) ([]deliberation.Dispute, error) {
	rows, err := s.db.Query(
		`SELECT id, deliberation_id, agent_id, crux_claim, correction, created_at FROM disputes WHERE deliberation_id = ? ORDER BY created_at`,
		deliberationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var result []deliberation.Dispute
	for rows.Next() {
		var d deliberation.Dispute
		var createdAt string
		if err := rows.Scan(&d.ID, &d.DeliberationID, &d.AgentID, &d.CruxClaim, &d.Correction, &createdAt); err != nil {
			return nil, err
		}
		d.CreatedAt = parseTime(createdAt)
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
		`INSERT INTO jobs (id, deliberation_id, status, model, api_key, credit_cost) VALUES (?, ?, 'pending', ?, ?, ?)`,
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
		`UPDATE jobs SET status = ?, error = ?, completed_at = datetime('now') WHERE id = ?`,
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
	cutoff := time.Now().Add(-maxAge).Format(time.RFC3339)
	res, err := s.db.Exec(
		`UPDATE jobs SET status = 'pending' WHERE status = 'running' AND created_at < ?`,
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

// parseTime tries RFC3339 first, then falls back to SQLite's datetime('now') format.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, _ = time.Parse("2006-01-02 15:04:05", s)
	}
	return t
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
