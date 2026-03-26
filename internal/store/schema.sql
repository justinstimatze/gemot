CREATE TABLE IF NOT EXISTS deliberations (
    id TEXT PRIMARY KEY,
    topic TEXT NOT NULL,
    description TEXT DEFAULT '',
    round_number INTEGER DEFAULT 1,
    status TEXT DEFAULT 'open',
    sub_status TEXT DEFAULT '',
    created_at TEXT DEFAULT (datetime('now')),
    status_changed_at TEXT DEFAULT (datetime('now')),
    type TEXT DEFAULT '',
    visibility TEXT DEFAULT 'open',
    creator_key TEXT DEFAULT '',
    max_participants INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS positions (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    agent_id TEXT NOT NULL,
    content TEXT NOT NULL,
    model_family TEXT DEFAULT '',
    group_name TEXT DEFAULT '',
    conviction REAL DEFAULT 0.5,
    reservation TEXT DEFAULT '',
    on_behalf_of TEXT DEFAULT '',
    draft INTEGER DEFAULT 0,
    round_number INTEGER DEFAULT 1,
    created_at TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS votes (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    agent_id TEXT NOT NULL,
    position_id TEXT NOT NULL REFERENCES positions(id),
    value INTEGER NOT NULL CHECK (value IN (-1, 0, 1)),
    criterion_id TEXT DEFAULT '',
    created_at TEXT DEFAULT (datetime('now')),
    UNIQUE(deliberation_id, agent_id, position_id, criterion_id)
);

CREATE TABLE IF NOT EXISTS analysis_results (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    round_number INTEGER NOT NULL,
    result_json TEXT NOT NULL,
    analyzed_at TEXT DEFAULT (datetime('now')),
    UNIQUE(deliberation_id, round_number)
);

-- Migration: add sub_status column if missing (safe to run multiple times)
-- SQLite doesn't support IF NOT EXISTS for ALTER TABLE, so we use a pragma check
-- This is handled in Go code instead.

CREATE TABLE IF NOT EXISTS delegations (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    from_agent TEXT NOT NULL,
    to_agent TEXT NOT NULL,
    scope TEXT DEFAULT '',
    active INTEGER DEFAULT 1,
    created_at TEXT DEFAULT (datetime('now')),
    UNIQUE(deliberation_id, from_agent)
);

CREATE INDEX IF NOT EXISTS idx_delegations_delib ON delegations(deliberation_id);

CREATE TABLE IF NOT EXISTS commitments (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    agent_id TEXT NOT NULL,
    analysis_round INTEGER NOT NULL,
    statement TEXT NOT NULL,
    conditional TEXT DEFAULT '',
    status TEXT DEFAULT 'pending',
    created_at TEXT DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_commitments_delib ON commitments(deliberation_id);

CREATE TABLE IF NOT EXISTS join_codes (
    code TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    role TEXT DEFAULT '',
    expires_at TEXT NOT NULL,
    used INTEGER DEFAULT 0,
    used_by TEXT DEFAULT '',
    created_at TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS deliberation_acl (
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    key_id TEXT NOT NULL,
    added_at TEXT DEFAULT (datetime('now')),
    PRIMARY KEY (deliberation_id, key_id)
);

CREATE TABLE IF NOT EXISTS invitations (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    invited_by TEXT NOT NULL,
    invited_agent TEXT NOT NULL,
    role TEXT DEFAULT '',
    reason TEXT NOT NULL,
    status TEXT DEFAULT 'pending',
    created_at TEXT DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_invitations_delib ON invitations(deliberation_id);

CREATE TABLE IF NOT EXISTS disputes (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    agent_id TEXT NOT NULL,
    crux_claim TEXT NOT NULL,
    correction TEXT NOT NULL,
    created_at TEXT DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_disputes_delib ON disputes(deliberation_id);

CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    status TEXT DEFAULT 'pending',
    model TEXT DEFAULT '',
    api_key TEXT DEFAULT '',
    credit_cost INTEGER DEFAULT 0,
    error TEXT DEFAULT '',
    created_at TEXT DEFAULT (datetime('now')),
    completed_at TEXT DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);

CREATE TABLE IF NOT EXISTS llm_cache (
    cache_key TEXT PRIMARY KEY,
    response_json TEXT NOT NULL,
    model TEXT DEFAULT '',
    created_at TEXT DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_positions_delib ON positions(deliberation_id, round_number);
CREATE INDEX IF NOT EXISTS idx_votes_delib ON votes(deliberation_id);
CREATE INDEX IF NOT EXISTS idx_votes_position ON votes(position_id);
