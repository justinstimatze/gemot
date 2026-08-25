// In-memory implementation of the storage layer used by gemot in demo mode.
//
// When DATABASE_URL is empty (or set to "memory:"), main.go wires this
// implementation in place of the Postgres-backed *DB. Everything works
// (deliberations, positions, votes, analysis, commitments, audit log) but
// state lives only for the lifetime of the process. Restart loses
// everything — exactly what you want for a `docker run gemot/gemot`
// kick-the-tires session and exactly what you do not want for a real
// deployment.
//
// Concurrency: a single sync.RWMutex guards the whole store. The
// granularity is coarse on purpose — demo workloads don't fan out across
// goroutines hard enough for finer locks to matter, and a single mutex
// makes the "did I forget to lock?" review trivial.
//
// Defensive copying: top-level structs are returned by value, so a caller
// that mutates the returned struct's value-typed fields (string, int,
// bool, time.Time) doesn't affect the stored copy. Map, slice, and
// pointer fields (Position.Metadata, Position.Signature, Vote.Signature,
// Deliberation.Rules, Deliberation.Criteria, Deliberation.Resolution,
// Commitment.FulfilledAt) DO share underlying storage with the stored
// version. This matches the contract callers should already follow with
// the Postgres adapter (each fresh QueryRow scan is independent, but
// nothing in the codebase mutates returned structs in place — and a
// future reviewer should not start). If we ever need true deep-copy
// semantics, switch the read paths to JSON-roundtrip the struct.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

// Backend is the union of every persistence interface gemot consumes.
// Both *DB (Postgres) and *MemoryStore (in-process demo mode) satisfy it,
// letting main.go and the MCP/HTTP handlers stay backend-agnostic.
//
// Smaller interfaces (deliberation.Store, CacheBackend, reputation.Store)
// remain useful for tests and for components that genuinely need a
// narrower contract — Backend is just the umbrella for "the thing main.go
// passes around."
type Backend interface {
	deliberation.Store

	// Audit log
	LogAuditEvent(keyID, ip, method, deliberationID, agentID string)
	GetAuditLog(deliberationID string, limit int) ([]map[string]string, error)

	// Async-analysis job tracking
	CreateJob(j *Job) error
	CompleteJob(jobID, status, errMsg string) error
	RecoverStuckJobs(maxAge time.Duration) (int, error)

	// Long-running maintenance
	DeleteExpiredSandboxDeliberations(maxAge time.Duration) (int, error)
	PurgeSoftDeleted(maxAge time.Duration) (int, error)

	// LLM cache (delegated by store.LLMCache)
	CacheGet(key string, maxAge time.Duration) string
	CachePut(key, response, model string)

	// Reputation surface — both backends provide a no-op-friendly
	// implementation so the reputation package can wire in without
	// branching on backend.
	ResolveVertices(ctx context.Context, agents []string) (map[string]string, error)
}

// Compile-time assertion: both backends satisfy the Backend interface.
// If a method drifts on either side, the build breaks here instead of
// at the call site.
var (
	_ Backend = (*DB)(nil)
	_ Backend = (*MemoryStore)(nil)
)

// MemoryStore is an in-memory backing for every persistence interface gemot
// uses. It satisfies deliberation.Store plus the gemot-specific surface
// (audit log, jobs, llm cache, agent keys, sandbox cleanup) consumed by
// main.go and the MCP/HTTP handlers.
type MemoryStore struct {
	mu sync.RWMutex

	// Core deliberation data
	deliberations    map[string]*deliberation.Deliberation           // id -> deliberation
	statusChanged    map[string]time.Time                            // id -> when status last changed
	positions        map[string]*deliberation.Position               // id -> position
	votes            map[string]*deliberation.Vote                   // id -> vote
	delegations      map[string]*deliberation.Delegation             // id -> delegation
	commitments      map[string]*deliberation.Commitment             // id -> commitment
	commitmentAccess map[string][]*deliberation.CommitmentAccess     // commitment_id -> downstream accesses
	analyses         map[string]map[int]*deliberation.AnalysisResult // delibID -> round -> result

	// Access control
	joinCodes   map[string]*deliberation.JoinCode // code -> jc
	acl         map[string]map[string]bool        // delibID -> keyID -> true
	invitations map[string]*deliberation.Invitation
	shareTokens map[string]shareToken // token -> {groupID, expiresAt}

	// Moderation
	disputes         map[string]*deliberation.Dispute
	disputeProcessed map[string]time.Time // disputeID -> processedAt
	abuseReports     []abuseReport
	contextAccess    map[string]bool // "delibID|agentID|round" -> true

	// Agent keys (envelope signing)
	agentKeys map[string]agentKey // agentID -> {publicKey, algo}

	// Audit log
	auditLog []auditEntry

	// Job tracking (analysis async runs)
	jobs map[string]*jobRecord

	// LLM cache
	llmCache map[string]llmCacheEntry

	// Reputation (no-op friendly: returns empty / unit weights)
	reputation map[string]reputationRecord
}

type shareToken struct {
	GroupID   string
	ExpiresAt time.Time
}

type abuseReport struct {
	DeliberationID string
	ReporterKey    string
	Reason         string
	CreatedAt      time.Time
}

type agentKey struct {
	PublicKey []byte
	Algo      string
	Active    bool
}

type auditEntry struct {
	KeyID          string
	IP             string
	Method         string
	DeliberationID string
	AgentID        string
	CreatedAt      time.Time
}

type jobRecord struct {
	ID             string
	DeliberationID string
	Status         string
	Error          string
	CreatedAt      time.Time
	StartedAt      time.Time
	CompletedAt    time.Time
}

type llmCacheEntry struct {
	Response  string
	Model     string
	CreatedAt time.Time
}

type reputationRecord struct {
	AgentID       string
	Score         float64
	SurvivedCount int
	UpdatedAt     time.Time
}

// NewMemoryStore returns an empty in-memory store ready for use.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		deliberations:    map[string]*deliberation.Deliberation{},
		statusChanged:    map[string]time.Time{},
		positions:        map[string]*deliberation.Position{},
		votes:            map[string]*deliberation.Vote{},
		delegations:      map[string]*deliberation.Delegation{},
		commitments:      map[string]*deliberation.Commitment{},
		commitmentAccess: map[string][]*deliberation.CommitmentAccess{},
		analyses:         map[string]map[int]*deliberation.AnalysisResult{},
		joinCodes:        map[string]*deliberation.JoinCode{},
		acl:              map[string]map[string]bool{},
		invitations:      map[string]*deliberation.Invitation{},
		shareTokens:      map[string]shareToken{},
		disputes:         map[string]*deliberation.Dispute{},
		disputeProcessed: map[string]time.Time{},
		contextAccess:    map[string]bool{},
		agentKeys:        map[string]agentKey{},
		jobs:             map[string]*jobRecord{},
		llmCache:         map[string]llmCacheEntry{},
		reputation:       map[string]reputationRecord{},
	}
}

// ============================================================================
// DeliberationStore
// ============================================================================

func (m *MemoryStore) CreateDeliberation(_ context.Context, d *deliberation.Deliberation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d.ID == "" {
		d.ID = newUUID()
	}
	if _, ok := m.deliberations[d.ID]; ok {
		return fmt.Errorf("deliberation %s already exists", d.ID)
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	clone := *d
	m.deliberations[d.ID] = &clone
	m.statusChanged[d.ID] = clone.CreatedAt
	return nil
}

func (m *MemoryStore) GetDeliberation(_ context.Context, id string) (*deliberation.Deliberation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.deliberations[id]
	if !ok {
		return nil, fmt.Errorf("deliberation not found: %s", id)
	}
	clone := *d
	return &clone, nil
}

// listSorted returns deliberations matching pred, sorted by CreatedAt desc
// then sliced by [offset, offset+limit). limit<=0 → no cap.
func (m *MemoryStore) listSorted(pred func(*deliberation.Deliberation) bool, limit, offset int) []deliberation.Deliberation {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []deliberation.Deliberation
	for _, d := range m.deliberations {
		if pred(d) {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if offset >= len(out) {
		return nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (m *MemoryStore) ListDeliberations(_ context.Context, limit, offset int, keyID string) ([]deliberation.Deliberation, error) {
	pred := func(d *deliberation.Deliberation) bool {
		// Mirror the Postgres adapter's visibility model: open delibs are
		// listable; private/link delibs are listable only by their creator
		// or someone in the ACL.
		if d.Visibility == "open" || d.Visibility == "" {
			return true
		}
		if keyID == "" {
			return false
		}
		if d.CreatorKey == keyID {
			return true
		}
		if acl, ok := m.acl[d.ID]; ok && acl[keyID] {
			return true
		}
		return false
	}
	return m.listSorted(pred, limit, offset), nil
}

func (m *MemoryStore) ListByGroup(_ context.Context, groupID string, limit, offset int, keyID string) ([]deliberation.Deliberation, error) {
	pred := func(d *deliberation.Deliberation) bool {
		if d.GroupID != groupID {
			return false
		}
		if d.Visibility == "open" || d.Visibility == "" {
			return true
		}
		if keyID != "" && (d.CreatorKey == keyID || (m.acl[d.ID] != nil && m.acl[d.ID][keyID])) {
			return true
		}
		return false
	}
	return m.listSorted(pred, limit, offset), nil
}

func (m *MemoryStore) ListByAgent(_ context.Context, agentID string, limit, offset int, keyID string) ([]deliberation.Deliberation, error) {
	// "Deliberations involving this agent" = ones where the agent has
	// submitted at least one position. Private delibs still gated by keyID.
	m.mu.RLock()
	involves := map[string]bool{}
	for _, p := range m.positions {
		if p.AgentID == agentID {
			involves[p.DeliberationID] = true
		}
	}
	m.mu.RUnlock()
	pred := func(d *deliberation.Deliberation) bool {
		if !involves[d.ID] {
			return false
		}
		if d.Visibility == "open" || d.Visibility == "" {
			return true
		}
		if keyID != "" && (d.CreatorKey == keyID || (m.acl[d.ID] != nil && m.acl[d.ID][keyID])) {
			return true
		}
		return false
	}
	return m.listSorted(pred, limit, offset), nil
}

func (m *MemoryStore) SetGroupID(_ context.Context, deliberationID, groupID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deliberations[deliberationID]
	if !ok {
		return fmt.Errorf("deliberation not found: %s", deliberationID)
	}
	d.GroupID = groupID
	return nil
}

func (m *MemoryStore) UpdateDeliberationStatus(_ context.Context, id, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deliberations[id]
	if !ok {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	if d.Status != status {
		m.statusChanged[id] = time.Now().UTC()
	}
	d.Status = status
	return nil
}

func (m *MemoryStore) SaveResolution(_ context.Context, id string, resolution *deliberation.Resolution) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deliberations[id]
	if !ok {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	if resolution == nil {
		d.Resolution = nil
	} else {
		clone := *resolution
		d.Resolution = &clone
	}
	return nil
}

func (m *MemoryStore) UpdateDeliberationTemplate(_ context.Context, id, template string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deliberations[id]
	if !ok {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	d.Template = template
	return nil
}

func (m *MemoryStore) UpdateDeliberationRules(_ context.Context, id string, rules map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deliberations[id]
	if !ok {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	if rules == nil {
		d.Rules = nil
	} else {
		copyMap := make(map[string]any, len(rules))
		for k, v := range rules {
			copyMap[k] = v
		}
		d.Rules = copyMap
	}
	return nil
}

func (m *MemoryStore) DeleteDeliberation(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deliberations[id]
	if !ok {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	// Soft-delete: mirror Postgres adapter, mark status=deleted instead of
	// removing the row, so audit history and resolution data survive.
	d.Status = "deleted"
	m.statusChanged[id] = time.Now().UTC()
	return nil
}

func (m *MemoryStore) GetStatusChangedAt(_ context.Context, deliberationID string) (time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.statusChanged[deliberationID]
	if !ok {
		return time.Time{}, fmt.Errorf("deliberation not found: %s", deliberationID)
	}
	return t, nil
}

func (m *MemoryStore) UpdateSubStatus(_ context.Context, id, subStatus string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deliberations[id]
	if !ok {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	d.SubStatus = subStatus
	return nil
}

// TrySetAnalyzing is the in-memory analog of the SQL-level CAS: only
// transitions open→analyzing if the deliberation is currently open.
// Returns (true, nil) on the winning transition; (false, nil) otherwise.
func (m *MemoryStore) TrySetAnalyzing(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deliberations[id]
	if !ok {
		return false, fmt.Errorf("deliberation not found: %s", id)
	}
	if d.Status != "open" {
		return false, nil
	}
	d.Status = "analyzing"
	m.statusChanged[id] = time.Now().UTC()
	return true, nil
}

func (m *MemoryStore) AdvanceRound(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deliberations[id]
	if !ok {
		return fmt.Errorf("deliberation not found: %s", id)
	}
	d.Round++
	d.Status = "open"
	d.SubStatus = ""
	m.statusChanged[id] = time.Now().UTC()
	return nil
}

// ============================================================================
// PositionStore
// ============================================================================

func (m *MemoryStore) CreatePosition(_ context.Context, p *deliberation.Position) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.ID == "" {
		p.ID = newUUID()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	clone := *p
	m.positions[p.ID] = &clone
	return nil
}

func (m *MemoryStore) GetPositions(_ context.Context, deliberationID string, round *int) ([]deliberation.Position, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []deliberation.Position
	for _, p := range m.positions {
		if p.DeliberationID != deliberationID {
			continue
		}
		if round != nil && p.Round != *round {
			continue
		}
		if p.Draft {
			continue // mirror Postgres GetPositions: published only
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) GetPositionByID(_ context.Context, id string) (*deliberation.Position, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.positions[id]
	if !ok {
		return nil, fmt.Errorf("position not found: %s", id)
	}
	clone := *p
	return &clone, nil
}

func (m *MemoryStore) CountPositions(_ context.Context, deliberationID string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, p := range m.positions {
		if p.DeliberationID == deliberationID && !p.Draft {
			n++
		}
	}
	return n, nil
}

func (m *MemoryStore) CheckParticipantCap(_ context.Context, deliberationID, agentID string, maxParticipants int) (bool, bool, error) {
	if maxParticipants <= 0 {
		return false, false, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	uniqueAgents := map[string]bool{}
	alreadyIn := false
	for _, p := range m.positions {
		if p.DeliberationID != deliberationID {
			continue
		}
		uniqueAgents[p.AgentID] = true
		if p.AgentID == agentID {
			alreadyIn = true
		}
	}
	capped := len(uniqueAgents) >= maxParticipants && !alreadyIn
	return capped, alreadyIn, nil
}

func (m *MemoryStore) PublishPosition(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.positions[id]
	if !ok {
		return fmt.Errorf("position not found: %s", id)
	}
	p.Draft = false
	return nil
}

func (m *MemoryStore) WithdrawPositions(_ context.Context, deliberationID, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, p := range m.positions {
		if p.DeliberationID == deliberationID && p.AgentID == agentID {
			delete(m.positions, id)
		}
	}
	return nil
}

// ============================================================================
// VoteStore
// ============================================================================

func (m *MemoryStore) CreateVote(_ context.Context, v *deliberation.Vote) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v.ID == "" {
		v.ID = newUUID()
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	// Last write wins per (delib, agent, position, criterion) — same as
	// Postgres's ON CONFLICT DO UPDATE pattern.
	for id, existing := range m.votes {
		if existing.DeliberationID == v.DeliberationID &&
			existing.AgentID == v.AgentID &&
			existing.PositionID == v.PositionID &&
			existing.CriterionID == v.CriterionID {
			delete(m.votes, id)
		}
	}
	clone := *v
	m.votes[v.ID] = &clone
	return nil
}

func (m *MemoryStore) GetVotes(_ context.Context, deliberationID string) ([]deliberation.Vote, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []deliberation.Vote
	for _, v := range m.votes {
		if v.DeliberationID == deliberationID {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) GetVotesByRound(ctx context.Context, deliberationID string, round int) ([]deliberation.Vote, error) {
	// Match votes to positions cast in the given round, since votes
	// themselves don't carry a round field.
	positions, err := m.GetPositions(ctx, deliberationID, &round)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, p := range positions {
		allowed[p.ID] = true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []deliberation.Vote
	for _, v := range m.votes {
		if v.DeliberationID == deliberationID && allowed[v.PositionID] {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) DeleteVotesByAgent(_ context.Context, deliberationID, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, v := range m.votes {
		if v.DeliberationID == deliberationID && v.AgentID == agentID {
			delete(m.votes, id)
		}
	}
	return nil
}

func (m *MemoryStore) CreateDelegation(_ context.Context, d *deliberation.Delegation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d.ID == "" {
		d.ID = newUUID()
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	d.Active = true
	clone := *d
	m.delegations[d.ID] = &clone
	return nil
}

func (m *MemoryStore) RevokeDelegation(_ context.Context, deliberationID, fromAgent string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.delegations {
		if d.DeliberationID == deliberationID && d.FromAgent == fromAgent {
			d.Active = false
		}
	}
	return nil
}

func (m *MemoryStore) GetDelegations(_ context.Context, deliberationID string) ([]deliberation.Delegation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []deliberation.Delegation
	for _, d := range m.delegations {
		if d.DeliberationID == deliberationID && d.Active {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) DeleteDelegationsByAgent(_ context.Context, deliberationID, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, d := range m.delegations {
		if d.DeliberationID == deliberationID && d.FromAgent == agentID {
			delete(m.delegations, id)
		}
	}
	return nil
}

// ============================================================================
// CommitmentStore
// ============================================================================

func (m *MemoryStore) CreateCommitment(_ context.Context, c *deliberation.Commitment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ID == "" {
		c.ID = newUUID()
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if c.Status == "" {
		c.Status = "pending"
	}
	clone := *c
	m.commitments[c.ID] = &clone
	return nil
}

// sortByCreatedAt is the default ordering used across the Postgres
// adapter (every list-style query has `ORDER BY created_at`). Mirrored
// here so callers can rely on stable, ascending-by-creation order
// regardless of which backend is wired in.
func sortByCommitmentCreatedAt(out []deliberation.Commitment) {
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
}

func (m *MemoryStore) GetCommitmentByID(_ context.Context, id string) (*deliberation.Commitment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.commitments[id]
	if !ok {
		return nil, fmt.Errorf("commitment not found: %s", id)
	}
	clone := *c
	return &clone, nil
}

func (m *MemoryStore) GetCommitments(_ context.Context, deliberationID string) ([]deliberation.Commitment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []deliberation.Commitment
	for _, c := range m.commitments {
		if c.DeliberationID == deliberationID {
			out = append(out, *c)
		}
	}
	sortByCommitmentCreatedAt(out)
	return out, nil
}

func (m *MemoryStore) GetCommitmentsByAgent(_ context.Context, agentID string) ([]deliberation.Commitment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []deliberation.Commitment
	for _, c := range m.commitments {
		if c.AgentID == agentID {
			out = append(out, *c)
		}
	}
	sortByCommitmentCreatedAt(out)
	return out, nil
}

func (m *MemoryStore) GetCommitmentsByGroup(_ context.Context, groupID string) ([]deliberation.Commitment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	delibsInGroup := map[string]bool{}
	for _, d := range m.deliberations {
		if d.GroupID == groupID {
			delibsInGroup[d.ID] = true
		}
	}
	var out []deliberation.Commitment
	for _, c := range m.commitments {
		if delibsInGroup[c.DeliberationID] {
			out = append(out, *c)
		}
	}
	sortByCommitmentCreatedAt(out)
	return out, nil
}

func (m *MemoryStore) UpdateCommitmentStatus(_ context.Context, id, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.commitments[id]
	if !ok {
		return fmt.Errorf("commitment not found: %s", id)
	}
	c.Status = status
	return nil
}

func (m *MemoryStore) FulfillCommitment(_ context.Context, id, verifiedBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.commitments[id]
	if !ok {
		return fmt.Errorf("commitment not found: %s", id)
	}
	c.Status = "fulfilled"
	now := time.Now().UTC()
	c.FulfilledAt = &now
	c.VerifiedBy = verifiedBy
	return nil
}

func (m *MemoryStore) BreakCommitment(_ context.Context, id, reason, verifiedBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.commitments[id]
	if !ok {
		return fmt.Errorf("commitment not found: %s", id)
	}
	c.Status = "broken"
	now := time.Now().UTC()
	c.BrokenAt = &now
	c.BrokenReason = reason
	c.VerifiedBy = verifiedBy
	return nil
}

// ============================================================================
// AnalysisStore
// ============================================================================

func (m *MemoryStore) SaveAnalysisResult(_ context.Context, deliberationID string, round int, result *deliberation.AnalysisResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.analyses[deliberationID] == nil {
		m.analyses[deliberationID] = map[int]*deliberation.AnalysisResult{}
	}
	clone := *result
	m.analyses[deliberationID][round] = &clone
	return nil
}

func (m *MemoryStore) GetAnalysisResult(_ context.Context, deliberationID string, round int) (*deliberation.AnalysisResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rounds, ok := m.analyses[deliberationID]
	if !ok {
		return nil, nil // mirror Postgres adapter: nil, nil for missing
	}
	r, ok := rounds[round]
	if !ok {
		return nil, nil
	}
	clone := *r
	return &clone, nil
}

func (m *MemoryStore) GetLatestAnalysisResult(_ context.Context, deliberationID string) (*deliberation.AnalysisResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rounds, ok := m.analyses[deliberationID]
	if !ok || len(rounds) == 0 {
		return nil, nil
	}
	maxRound := -1
	for r := range rounds {
		if r > maxRound {
			maxRound = r
		}
	}
	clone := *rounds[maxRound]
	return &clone, nil
}

func (m *MemoryStore) GetAllAnalysisResults(_ context.Context, deliberationID string) ([]deliberation.AnalysisResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rounds, ok := m.analyses[deliberationID]
	if !ok {
		return nil, nil
	}
	var keys []int
	for r := range rounds {
		keys = append(keys, r)
	}
	sort.Ints(keys)
	out := make([]deliberation.AnalysisResult, 0, len(keys))
	for _, k := range keys {
		out = append(out, *rounds[k])
	}
	return out, nil
}

func (m *MemoryStore) GetStuckAnalyzing(_ context.Context, maxAge time.Duration) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	cutoff := time.Now().UTC().Add(-maxAge)
	for id, d := range m.deliberations {
		if d.Status == "analyzing" && m.statusChanged[id].Before(cutoff) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (m *MemoryStore) RecoverStuckAnalyzing(_ context.Context, maxAge time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	cutoff := time.Now().UTC().Add(-maxAge)
	for id, d := range m.deliberations {
		if d.Status == "analyzing" && m.statusChanged[id].Before(cutoff) {
			d.Status = "open"
			d.SubStatus = ""
			m.statusChanged[id] = time.Now().UTC()
			n++
		}
	}
	return n, nil
}

// ============================================================================
// AccessStore
// ============================================================================

func (m *MemoryStore) CreateJoinCode(_ context.Context, jc *deliberation.JoinCode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if jc.CreatedAt.IsZero() {
		jc.CreatedAt = time.Now().UTC()
	}
	clone := *jc
	m.joinCodes[jc.Code] = &clone
	return nil
}

func (m *MemoryStore) ClaimJoinCode(_ context.Context, code, agentID string) (*deliberation.JoinCode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	jc, ok := m.joinCodes[code]
	if !ok {
		return nil, fmt.Errorf("join code not found")
	}
	if time.Now().UTC().After(jc.ExpiresAt) {
		return nil, fmt.Errorf("join code expired")
	}
	if jc.MaxUses > 0 && jc.UseCount >= jc.MaxUses {
		return nil, fmt.Errorf("join code exhausted")
	}
	jc.UseCount++
	jc.Used = true
	jc.UsedBy = agentID
	clone := *jc
	return &clone, nil
}

func (m *MemoryStore) LookupJoinCode(_ context.Context, code string) (*deliberation.JoinCode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	jc, ok := m.joinCodes[code]
	if !ok {
		return nil, fmt.Errorf("join code not found")
	}
	clone := *jc
	return &clone, nil
}

func (m *MemoryStore) AddToACL(_ context.Context, deliberationID, keyID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.acl[deliberationID] == nil {
		m.acl[deliberationID] = map[string]bool{}
	}
	m.acl[deliberationID][keyID] = true
	return nil
}

func (m *MemoryStore) CheckACL(_ context.Context, deliberationID, keyID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.deliberations[deliberationID]
	if !ok {
		return false, fmt.Errorf("deliberation not found: %s", deliberationID)
	}
	if d.Visibility == "open" || d.Visibility == "" {
		return true, nil
	}
	if d.CreatorKey == keyID {
		return true, nil
	}
	if acl, ok := m.acl[deliberationID]; ok && acl[keyID] {
		return true, nil
	}
	return false, nil
}

func (m *MemoryStore) CreateInvitation(_ context.Context, inv *deliberation.Invitation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inv.ID == "" {
		inv.ID = newUUID()
	}
	if inv.CreatedAt.IsZero() {
		inv.CreatedAt = time.Now().UTC()
	}
	if inv.Status == "" {
		inv.Status = "pending"
	}
	clone := *inv
	m.invitations[inv.ID] = &clone
	return nil
}

func (m *MemoryStore) GetInvitations(_ context.Context, deliberationID string) ([]deliberation.Invitation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []deliberation.Invitation
	for _, i := range m.invitations {
		if i.DeliberationID == deliberationID {
			out = append(out, *i)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) UpdateInvitationStatus(_ context.Context, id, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.invitations[id]
	if !ok {
		return fmt.Errorf("invitation not found: %s", id)
	}
	i.Status = status
	return nil
}

func (m *MemoryStore) CreateShareToken(_ context.Context, token, groupID string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shareTokens[token] = shareToken{GroupID: groupID, ExpiresAt: expiresAt}
	return nil
}

func (m *MemoryStore) LookupShareToken(_ context.Context, token string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, ok := m.shareTokens[token]
	if !ok {
		return "", fmt.Errorf("share token not found")
	}
	if time.Now().UTC().After(st.ExpiresAt) {
		return "", fmt.Errorf("share token expired")
	}
	return st.GroupID, nil
}

// ============================================================================
// ModerationStore
// ============================================================================

func (m *MemoryStore) CreateDispute(_ context.Context, d *deliberation.Dispute) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d.ID == "" {
		d.ID = newUUID()
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	clone := *d
	m.disputes[d.ID] = &clone
	return nil
}

func (m *MemoryStore) GetDisputes(_ context.Context, deliberationID string) ([]deliberation.Dispute, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []deliberation.Dispute
	for _, d := range m.disputes {
		if d.DeliberationID == deliberationID {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) GetUnprocessedDisputes(_ context.Context, deliberationID string) ([]deliberation.Dispute, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []deliberation.Dispute
	for _, d := range m.disputes {
		if d.DeliberationID == deliberationID {
			if _, processed := m.disputeProcessed[d.ID]; !processed {
				out = append(out, *d)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) MarkDisputesProcessed(_ context.Context, disputeIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	for _, id := range disputeIDs {
		m.disputeProcessed[id] = now
	}
	return nil
}

func (m *MemoryStore) CreateAbuseReport(_ context.Context, deliberationID, reporterKey, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.abuseReports = append(m.abuseReports, abuseReport{
		DeliberationID: deliberationID,
		ReporterKey:    reporterKey,
		Reason:         reason,
		CreatedAt:      time.Now().UTC(),
	})
	return nil
}

func (m *MemoryStore) RecordContextAccess(_ context.Context, deliberationID, agentID string, round int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.contextAccess[fmt.Sprintf("%s|%s|%d", deliberationID, agentID, round)] = true
	return nil
}

func (m *MemoryStore) HasContextAccess(_ context.Context, deliberationID, agentID string, round int) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.contextAccess[fmt.Sprintf("%s|%s|%d", deliberationID, agentID, round)], nil
}

// ============================================================================
// AgentKeyStore
// ============================================================================

func (m *MemoryStore) RegisterAgentKey(_ context.Context, agentID string, publicKey []byte, algo string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	pkCopy := make([]byte, len(publicKey))
	copy(pkCopy, publicKey)
	m.agentKeys[agentID] = agentKey{PublicKey: pkCopy, Algo: algo, Active: true}
	return nil
}

func (m *MemoryStore) GetActiveAgentKey(_ context.Context, agentID string) ([]byte, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	k, ok := m.agentKeys[agentID]
	if !ok || !k.Active {
		return nil, "", deliberation.ErrAgentKeyNotFound
	}
	pkCopy := make([]byte, len(k.PublicKey))
	copy(pkCopy, k.PublicKey)
	return pkCopy, k.Algo, nil
}

func (m *MemoryStore) RevokeAgentKey(_ context.Context, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.agentKeys[agentID]
	if !ok {
		return fmt.Errorf("agent key not found: %s", agentID)
	}
	k.Active = false
	m.agentKeys[agentID] = k
	return nil
}

// ============================================================================
// gemot DB surface (audit log, jobs, llm cache, sandbox cleanup, misc)
// These methods mirror *DB's public API so consumers (main.go, http.go,
// server.go) can hold a single concrete type that does both the
// deliberation.Store and the gemot-specific work.
// ============================================================================

// LogAuditEvent appends an entry to the in-memory audit log. Returns nil
// always; any DB-shaped error in the Postgres adapter would be a panic
// in the in-memory case. Mirrors *DB.LogAuditEvent.
func (m *MemoryStore) LogAuditEvent(keyID, ip, method, deliberationID, agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auditLog = append(m.auditLog, auditEntry{
		KeyID:          keyID,
		IP:             ip,
		Method:         method,
		DeliberationID: deliberationID,
		AgentID:        agentID,
		CreatedAt:      time.Now().UTC(),
	})
}

// GetAuditLog returns the most recent entries for a deliberation, newest
// first, capped at limit. Returns []map[string]string to match
// *DB.GetAuditLog's shape (lets MCP admin tool serialize uniformly).
func (m *MemoryStore) GetAuditLog(deliberationID string, limit int) ([]map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []map[string]string
	for i := len(m.auditLog) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		e := m.auditLog[i]
		if e.DeliberationID == deliberationID {
			out = append(out, map[string]string{
				"key_id":          e.KeyID,
				"ip":              e.IP,
				"method":          e.Method,
				"deliberation_id": e.DeliberationID,
				"agent_id":        e.AgentID,
				"created_at":      e.CreatedAt.Format(time.RFC3339),
			})
		}
	}
	return out, nil
}

// ClaimJob registers an analysis job in 'running' state. Returns the new
// jobID, or empty string + nil if another job is already running for the
// same deliberation (caller should treat this as "skip — already in flight").
func (m *MemoryStore) ClaimJob(deliberationID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.DeliberationID == deliberationID && j.Status == "running" {
			return "", nil
		}
	}
	id := newID("job")
	m.jobs[id] = &jobRecord{
		ID:             id,
		DeliberationID: deliberationID,
		Status:         "running",
		StartedAt:      time.Now().UTC(),
		CreatedAt:      time.Now().UTC(),
	}
	return id, nil
}

// CreateJob mirrors *DB.CreateJob: assigns Job.ID, persists in pending
// state. Used by RunAnalysisAsync to track async analyses for recovery.
func (m *MemoryStore) CreateJob(j *Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j.ID = newID("job")
	m.jobs[j.ID] = &jobRecord{
		ID:             j.ID,
		DeliberationID: j.DeliberationID,
		Status:         "pending",
		CreatedAt:      time.Now().UTC(),
		StartedAt:      time.Now().UTC(),
	}
	return nil
}

func (m *MemoryStore) CompleteJob(jobID, status, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok {
		return fmt.Errorf("job not found: %s", jobID)
	}
	j.Status = status
	j.Error = errMsg
	j.CompletedAt = time.Now().UTC()
	return nil
}

func (m *MemoryStore) GetPendingJobs() ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for _, j := range m.jobs {
		if j.Status == "running" {
			out = append(out, j.DeliberationID)
		}
	}
	return out, nil
}

func (m *MemoryStore) RecoverStuckJobs(maxAge time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().UTC().Add(-maxAge)
	n := 0
	for _, j := range m.jobs {
		if j.Status == "running" && j.StartedAt.Before(cutoff) {
			j.Status = "failed"
			j.Error = "stuck — recovered"
			j.CompletedAt = time.Now().UTC()
			n++
		}
	}
	return n, nil
}

// CacheGet returns a cached LLM response if present and within maxAge.
func (m *MemoryStore) CacheGet(key string, maxAge time.Duration) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.llmCache[key]
	if !ok {
		return ""
	}
	if maxAge > 0 && time.Since(e.CreatedAt) > maxAge {
		return ""
	}
	return e.Response
}

func (m *MemoryStore) CachePut(key, response, model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.llmCache[key] = llmCacheEntry{Response: response, Model: model, CreatedAt: time.Now().UTC()}
}

// DeleteExpiredSandboxDeliberations removes sandbox deliberations older
// than the cutoff. Returns the count of removed entries.
func (m *MemoryStore) DeleteExpiredSandboxDeliberations(maxAge time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().UTC().Add(-maxAge)
	n := 0
	for id, d := range m.deliberations {
		// Sandbox delibs have CreatorKey starting with "sbx:" by convention
		// (set by the sandbox-mode middleware in payments/mpp.go).
		if strings.HasPrefix(d.CreatorKey, "sbx:") && d.CreatedAt.Before(cutoff) {
			delete(m.deliberations, id)
			delete(m.statusChanged, id)
			n++
		}
	}
	return n, nil
}

// PurgeSoftDeleted permanently removes deliberations marked deleted.
// In-memory backend uses this to bound memory growth in long-running demos.
func (m *MemoryStore) PurgeSoftDeleted(maxAge time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().UTC().Add(-maxAge)
	n := 0
	for id, d := range m.deliberations {
		if d.Status == "deleted" && m.statusChanged[id].Before(cutoff) {
			delete(m.deliberations, id)
			delete(m.statusChanged, id)
			delete(m.acl, id)
			n++
		}
	}
	return n, nil
}

// Reputation surface — no-op friendly. Demo mode runs without persistent
// reputation; everything returns empty / unit weights, which the analysis
// pipeline already handles gracefully.

// ResolveVertices echoes the input agentIDs back as id:<agent> form, the
// fallback used when no envelope-key binding exists. Demo mode never
// upgrades to key-bound vertices.
func (m *MemoryStore) ResolveVertices(_ context.Context, agents []string) (map[string]string, error) {
	out := make(map[string]string, len(agents))
	for _, a := range agents {
		out[a] = "id:" + a
	}
	return out, nil
}

// LoadReputation returns nil — the analyzer treats nil as "no reputation
// data, use unit weights". Identical to the production path on a fresh
// deployment with no prior rounds.
func (m *MemoryStore) LoadReputation(_ context.Context, _ []string) (map[string]float64, map[string]int, error) {
	return nil, nil, nil
}

// LoadTrustEdges returns the in-memory edge set. Empty in fresh demos.
func (m *MemoryStore) LoadTrustEdges(_ context.Context, _ string) ([]TrustEdgeRow, error) {
	return nil, nil
}

// LoadTrustEdgesForCohort returns the cohort-scoped edge subgraph. Same
// as LoadTrustEdges in the in-memory path — there's no global vs. private
// distinction worth modeling for ephemeral state.
func (m *MemoryStore) LoadTrustEdgesForCohort(_ context.Context, _ string, _ []string) ([]TrustEdgeRow, error) {
	return nil, nil
}

// TrustEdgeRow mirrors the column shape used by the reputation package.
type TrustEdgeRow struct {
	From           string
	To             string
	Weight         float64
	DeliberationID string
}

// ============================================================================
// Helpers
// ============================================================================

// newUUID returns a UUID-v4-shaped string, mirroring uuid.New().String()
// from the Postgres adapter. We hand-roll instead of importing google/uuid
// to keep the in-memory backend dependency-free and small.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Same deal as newID — should never happen on a sane platform.
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	// Set version (4) and variant (RFC 4122) bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// newID generates a short opaque identifier for in-memory rows. Not
// crypto-secure (uses crypto/rand for the entropy but no domain-binding);
// good enough for demo mode where collisions are vanishingly rare.
func newID(prefix string) string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Should never happen on a reasonable platform; if it does the
		// demo store is the least of our problems.
		return prefix + "-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return prefix + "-" + hex.EncodeToString(buf[:])
}

// errInDemoMode is returned by the few paths that genuinely don't make
// sense without persistent state (currently unused; kept for future
// methods that need to refuse rather than no-op).
//
//lint:ignore U1000 kept for future methods that need to refuse rather than no-op
var errInDemoMode = errors.New("operation requires a persistent database (set DATABASE_URL)") //nolint:unused //nolint:unused

// RecordCommitmentAccess appends a server-stamped downstream-access record.
// Mirrors the Postgres adapter: id and created_at are filled here so the
// timestamp is the store's, never the caller's.
func (m *MemoryStore) RecordCommitmentAccess(_ context.Context, a *deliberation.CommitmentAccess) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.commitments[a.CommitmentID]; !ok {
		return fmt.Errorf("commitment not found: %s", a.CommitmentID)
	}
	if a.ID == "" {
		a.ID = newUUID()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.Kind == "" {
		a.Kind = "read"
	}
	clone := *a
	m.commitmentAccess[a.CommitmentID] = append(m.commitmentAccess[a.CommitmentID], &clone)
	return nil
}

// GetCommitmentAccesses returns a commitment's downstream-access ledger in
// ascending created_at order, matching the Postgres adapter.
func (m *MemoryStore) GetCommitmentAccesses(_ context.Context, commitmentID string) ([]deliberation.CommitmentAccess, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []deliberation.CommitmentAccess
	for _, a := range m.commitmentAccess[commitmentID] {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
