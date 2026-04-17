CREATE TABLE IF NOT EXISTS deliberations (
    id TEXT PRIMARY KEY,
    topic TEXT NOT NULL,
    description TEXT DEFAULT '',
    round_number INTEGER DEFAULT 1,
    status TEXT DEFAULT 'open',
    sub_status TEXT DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    status_changed_at TIMESTAMPTZ DEFAULT NOW(),
    type TEXT DEFAULT '',
    visibility TEXT DEFAULT 'open',
    creator_key TEXT DEFAULT '',
    max_participants INTEGER DEFAULT 0,
    template TEXT DEFAULT '',
    rules TEXT DEFAULT '{}',
    group_id TEXT DEFAULT ''
);

CREATE TABLE IF NOT EXISTS positions (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    agent_id TEXT NOT NULL,
    content TEXT NOT NULL,
    model_family TEXT DEFAULT '',
    group_name TEXT DEFAULT '',
    conviction DOUBLE PRECISION DEFAULT 0.5,
    reservation TEXT DEFAULT '',
    on_behalf_of TEXT DEFAULT '',
    interests TEXT DEFAULT '',
    draft INTEGER DEFAULT 0,
    round_number INTEGER DEFAULT 1,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS votes (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    agent_id TEXT NOT NULL,
    position_id TEXT NOT NULL REFERENCES positions(id),
    value INTEGER NOT NULL CHECK (value BETWEEN -2 AND 2),
    criterion_id TEXT DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(deliberation_id, agent_id, position_id, criterion_id)
);

CREATE TABLE IF NOT EXISTS analysis_results (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    round_number INTEGER NOT NULL,
    result_json TEXT NOT NULL,
    analyzed_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(deliberation_id, round_number)
);

CREATE TABLE IF NOT EXISTS delegations (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    from_agent TEXT NOT NULL,
    to_agent TEXT NOT NULL,
    scope TEXT DEFAULT '',
    active INTEGER DEFAULT 1,
    created_at TIMESTAMPTZ DEFAULT NOW(),
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
    created_at TIMESTAMPTZ DEFAULT NOW(),
    fulfilled_at TIMESTAMPTZ,
    broken_at TIMESTAMPTZ,
    broken_reason TEXT DEFAULT '',
    verified_by TEXT DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_commitments_delib ON commitments(deliberation_id);
CREATE INDEX IF NOT EXISTS idx_commitments_agent ON commitments(agent_id);

CREATE TABLE IF NOT EXISTS join_codes (
    code TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    role TEXT DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    used INTEGER DEFAULT 0,
    used_by TEXT DEFAULT '',
    max_uses INTEGER DEFAULT 1,
    use_count INTEGER DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS deliberation_acl (
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    key_id TEXT NOT NULL,
    added_at TIMESTAMPTZ DEFAULT NOW(),
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
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_invitations_delib ON invitations(deliberation_id);

CREATE TABLE IF NOT EXISTS disputes (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL REFERENCES deliberations(id),
    agent_id TEXT NOT NULL,
    crux_claim TEXT NOT NULL,
    correction TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
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
    created_at TIMESTAMPTZ DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);

CREATE TABLE IF NOT EXISTS llm_cache (
    cache_key TEXT PRIMARY KEY,
    response_json TEXT NOT NULL,
    model TEXT DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS abuse_reports (
    id TEXT PRIMARY KEY,
    deliberation_id TEXT NOT NULL,
    reporter_key TEXT DEFAULT '',
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS audit_log (
    id SERIAL PRIMARY KEY,
    timestamp TIMESTAMPTZ DEFAULT NOW(),
    key_id TEXT DEFAULT '',
    ip TEXT DEFAULT '',
    method TEXT NOT NULL,
    deliberation_id TEXT DEFAULT '',
    agent_id TEXT DEFAULT ''
);

CREATE TABLE IF NOT EXISTS context_access (
    deliberation_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    round INTEGER NOT NULL,
    accessed_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (deliberation_id, agent_id, round)
);

CREATE TABLE IF NOT EXISTS share_tokens (
    token TEXT PRIMARY KEY,
    group_id TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS api_keys (
    key TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    credits_remaining INTEGER DEFAULT 0,
    stripe_customer_id TEXT DEFAULT '',
    stripe_session_id TEXT DEFAULT '',
    suspended INTEGER DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    last_used_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_api_keys_email ON api_keys(email);
CREATE INDEX IF NOT EXISTS idx_api_keys_stripe ON api_keys(stripe_customer_id);
-- Prevent double-crediting from concurrent webhook delivery
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_session_unique ON api_keys(stripe_session_id) WHERE stripe_session_id != '';

CREATE INDEX IF NOT EXISTS idx_positions_delib ON positions(deliberation_id, round_number);
CREATE INDEX IF NOT EXISTS idx_votes_delib ON votes(deliberation_id);
CREATE INDEX IF NOT EXISTS idx_votes_position ON votes(position_id);

-- Migration: resolution field for vote-based decisions
ALTER TABLE deliberations ADD COLUMN IF NOT EXISTS resolution_json TEXT DEFAULT '';

-- Migration: commitment accountability audit trail
ALTER TABLE commitments ADD COLUMN IF NOT EXISTS fulfilled_at TIMESTAMPTZ;
ALTER TABLE commitments ADD COLUMN IF NOT EXISTS broken_at TIMESTAMPTZ;
ALTER TABLE commitments ADD COLUMN IF NOT EXISTS broken_reason TEXT DEFAULT '';
ALTER TABLE commitments ADD COLUMN IF NOT EXISTS verified_by TEXT DEFAULT '';

-- Migration: deliberation deadline
ALTER TABLE deliberations ADD COLUMN IF NOT EXISTS deadline_at TIMESTAMPTZ;

-- Migration: parent_position_id for Roberts Rules amendments
ALTER TABLE positions ADD COLUMN IF NOT EXISTS parent_position_id TEXT DEFAULT '';

-- Migration: position metadata (JSON map for agent coordinates, labels, etc.)
ALTER TABLE positions ADD COLUMN IF NOT EXISTS metadata TEXT DEFAULT '{}';

-- Qualified votes: expand value range from {-1,0,1} to [-2,+2] and add reasoning fields
DO $$ BEGIN
    ALTER TABLE votes DROP CONSTRAINT IF EXISTS votes_value_check;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;
ALTER TABLE votes DROP CONSTRAINT IF EXISTS votes_check;
ALTER TABLE votes ADD COLUMN IF NOT EXISTS qualifier TEXT DEFAULT '';
ALTER TABLE votes ADD COLUMN IF NOT EXISTS caveat TEXT DEFAULT '';

-- Per-agent public keys for signed positions and votes.
-- One agent may have multiple registered keys over time; the active key is the
-- most-recently-registered non-revoked row. Rotation = register new + revoke old.
-- `id` is a UUID so concurrent registrations in the same microsecond do not
-- collide on the primary key.
CREATE TABLE IF NOT EXISTS agent_keys (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    public_key BYTEA NOT NULL,
    algo TEXT NOT NULL DEFAULT 'ed25519',
    registered_at TIMESTAMPTZ DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);
-- UNIQUE partial index: one active key per agent at a time. Doubles as the
-- active-key lookup index AND prevents a race where two concurrent
-- RegisterAgentKey transactions both UPDATE-to-revoke zero rows and then both
-- INSERT, leaving two active keys. The unique constraint forces one of the
-- INSERTs to fail with a uniqueness violation.
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_keys_active ON agent_keys(agent_id) WHERE revoked_at IS NULL;

-- Per-action signatures. Nullable because signing is opt-in per signature_policy.
ALTER TABLE positions ADD COLUMN IF NOT EXISTS signature BYTEA;
ALTER TABLE votes ADD COLUMN IF NOT EXISTS signature BYTEA;

-- Per-deliberation signature policy: 'none' (legacy, no signing),
-- 'advisory' (warn on unsigned when key registered), 'required' (reject unsigned).
-- CHECK constraint prevents typos from silently falling through to fail-open
-- behavior in the verification switch.
ALTER TABLE deliberations ADD COLUMN IF NOT EXISTS signature_policy TEXT DEFAULT 'none';
DO $$ BEGIN
    ALTER TABLE deliberations ADD CONSTRAINT deliberations_sig_policy_check
        CHECK (signature_policy IN ('none', 'advisory', 'required'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Envelope nonce cache (durable, multi-instance safe).
-- Replaces the per-process MemoryNonceCache when GEMOT_NONCE_STORE=postgres.
-- `nonce` is the 32-byte base64url from the envelope header; `expires_at`
-- is set to now + 2*ReplayWindow at insert time. A background janitor
-- periodically deletes expired rows so the table stays bounded.
CREATE TABLE IF NOT EXISTS envelope_nonces (
    nonce TEXT PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_envelope_nonces_expires ON envelope_nonces(expires_at);

-- Schema versioning
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at TIMESTAMPTZ DEFAULT NOW()
);
INSERT INTO schema_version (version) VALUES (1) ON CONFLICT DO NOTHING;
