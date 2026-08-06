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

-- commitment_access is the externally-visible signal the payment-mesh
-- disclosure-window and stakes-gate both key to. Each row records that an
-- agent OTHER than the committer touched a commitment's artifact. gemot
-- stamps created_at and takes accessor_id from the authenticated caller, so
-- neither party can backdate or suppress it: it is the third-party-readable
-- clock (first/last downstream access) AND the party-independent stakes
-- marker (distinct accessors, dependent commitments) in one ledger.
CREATE TABLE IF NOT EXISTS commitment_access (
    id TEXT PRIMARY KEY,
    commitment_id TEXT NOT NULL REFERENCES commitments(id),
    accessor_id TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'read',
    note TEXT DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commitment_access_commitment ON commitment_access(commitment_id);

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

-- Schema v3: reputation ingestion tracking. NULL means the dispute has
-- not yet contributed a negative edge to the trust graph. Set once by
-- the reputation layer at round close, so re-running UpdateFromRound
-- on the same deliberation does not double-count disputes.
ALTER TABLE disputes ADD COLUMN IF NOT EXISTS rep_processed_at TIMESTAMPTZ;

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
-- Delegation attestation backing a position's on_behalf_of claim, plus the
-- server's record that it verified at submit time. The credential is stored so
-- the claim can be re-verified offline; the boolean is kept separately because
-- key rotation and revocation can make a credential that was genuinely valid at
-- submit time fail to re-verify later.
ALTER TABLE positions ADD COLUMN IF NOT EXISTS principal_credential BYTEA;
ALTER TABLE positions ADD COLUMN IF NOT EXISTS principal_verified BOOLEAN DEFAULT FALSE;

-- Per-deliberation principal policy: 'none' (on_behalf_of is an unverified
-- free-text claim), 'advisory' (unbacked claims audit-logged), 'required'
-- (on_behalf_of must carry a verified credential). Same CHECK-constraint
-- rationale as signature_policy: typos must not fall through to fail-open.
ALTER TABLE deliberations ADD COLUMN IF NOT EXISTS principal_policy TEXT DEFAULT 'none';
DO $$ BEGIN
    ALTER TABLE deliberations ADD CONSTRAINT deliberations_principal_policy_check
        CHECK (principal_policy IN ('none', 'advisory', 'required'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

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

-- EigenTrust reputation + cold-start cap (Track 1 Sybil resistance).
-- `agent_reputation` is the per-agent persistent state: `score` is the
-- most recent EigenTrust eigenvector component; `survived_count` is the
-- count of deliberation rounds where a position authored by this agent
-- survived to the final crux set. The cold-start cap clamps effective
-- weight until `survived_count >= GEMOT_EIGENTRUST_COLD_THRESHOLD` —
-- this is the primary Sybil defense because canonical EigenTrust
-- without OOB pre-trust does not defeat closed trust cycles.
--
-- `agent_trust_edges` is the sparse directed trust graph aggregated
-- across deliberations. `weight` is cumulative: each "A voted +1+ on a
-- surviving position by B" observation increments weight(A → B) by 1.
-- Power iteration is recomputed over this graph at round-close.
CREATE TABLE IF NOT EXISTS agent_reputation (
    agent_id TEXT PRIMARY KEY,
    score DOUBLE PRECISION DEFAULT 0,
    survived_count INTEGER DEFAULT 0,
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Schema v5: per-deliberation trust-edge partitioning. Rows with
-- deliberation_id = '' are GLOBAL edges emitted by open/link delibs
-- and feed the globally-visible EigenTrust eigenvector. Rows with
-- deliberation_id = <uuid> are PRIVATE edges scoped to that one
-- deliberation, fed into a per-delib EigenTrust computed on-the-fly
-- at WeightsFor time. Private-scoped edges do NOT leak into the
-- global recompute — LoadTrustEdges(ctx, "") filters to '' only.
--
-- Private deliberations' WeightsFor call loads WHERE delib_id IN
-- ('', <uuid>): the global graph is visible to private EigenTrust
-- (a seasoned agent carries their reputation in), but the private
-- graph stays isolated from the global one and from other private
-- delibs.
--
-- The PK is (from, to, delib_id) so the same (from, to) pair can
-- have distinct rows in global and one or more private delib scopes.
-- Rolling deploys are NOT safe across this PK change: a machine
-- still running v4 code against v5 schema will ERROR on ON CONFLICT
-- (from_agent, to_agent). Accept a brief (~30s) maintenance window
-- for the deploy.
CREATE TABLE IF NOT EXISTS agent_trust_edges (
    from_agent TEXT NOT NULL,
    to_agent TEXT NOT NULL,
    weight DOUBLE PRECISION DEFAULT 0,
    deliberation_id TEXT NOT NULL DEFAULT '',
    last_updated TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (from_agent, to_agent, deliberation_id)
);
CREATE INDEX IF NOT EXISTS idx_agent_trust_edges_from ON agent_trust_edges(from_agent);

-- Migrate pre-v5 databases: existing rows have the v4 (from, to) PK
-- and no deliberation_id column. ADD COLUMN is a no-op on fresh v5
-- creates (the column is already present); the PK swap is gated on
-- the old constraint's actual existence via a pg_constraint lookup
-- so it doesn't trip on fresh databases. Existing rows adopt
-- deliberation_id='' from the DEFAULT — preserves the global graph
-- across the migration. The ADD COLUMN must run BEFORE any index on
-- deliberation_id or the CREATE INDEX errors on pre-v5 DBs (the
-- column doesn't exist yet).
ALTER TABLE agent_trust_edges
    ADD COLUMN IF NOT EXISTS deliberation_id TEXT NOT NULL DEFAULT '';
DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'agent_trust_edges_pkey'
          AND conrelid = 'agent_trust_edges'::regclass
          AND array_length(conkey, 1) = 2
    ) THEN
        ALTER TABLE agent_trust_edges DROP CONSTRAINT agent_trust_edges_pkey;
        ALTER TABLE agent_trust_edges
            ADD PRIMARY KEY (from_agent, to_agent, deliberation_id);
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_agent_trust_edges_delib ON agent_trust_edges(deliberation_id);

-- Schema v4: pubkey-bound reputation identity.
-- `agent_reputation.agent_id` and `agent_trust_edges.from_agent` /
-- `to_agent` now carry a namespaced vertex string:
--   "key:<agent_keys.id>" when the agent had an active registered key at
--                         edge-emission / score-persistence time
--   "id:<agent_id>"       otherwise (unsigned deployments, or agents
--                         with no registered key)
-- Binding the vertex to the key-row id (not the symbolic agent_id) at
-- write time defeats the rename attack: re-registering "alice" under a
-- different pubkey yields a new `agent_keys.id`, and the new vertex is
-- distinct from the prior owner's accumulated reputation.
--
-- Legit key rotation is also identity-reset: the rotated key gets a fresh
-- vertex with no accumulated score. This is the intended defense against
-- a compromised K1 transferring rep to its replacement K2.
--
-- The prefixes "key:" and "id:" are reserved; bare agent_ids never go into
-- these columns — they're always wrapped by the reputation layer.
--
-- One-time migration: existing pre-v4 rows use the old bare-agent_id
-- format which would collide with the new prefix convention. Rather than
-- attempt an in-place rename (which has rolling-deploy PK-collision
-- hazards), the v4 migration resets accumulated reputation state. Fresh
-- rounds after v4 populate in the new format. Document in CHANGELOG.
DO $$ BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_version) < 4 THEN
        DELETE FROM agent_reputation;
        DELETE FROM agent_trust_edges;
    END IF;
EXCEPTION WHEN undefined_table THEN NULL;
END $$;

-- HotStuff BFT commit log (session 4). Each row is one committed
-- block plus the QC that formed on it, serialized as JSON. Height is
-- the primary key so the log is append-ordered and height-unique by
-- construction — a second (different) block at a height already
-- present is caught via post-insert hash compare in
-- PostgresLogStore.Append.
--
-- The log is currently unused by service.go (session 4 is
-- persistence-layer only; service wiring lands in session 5 alongside
-- multi-node deploy). An empty bft_log has no runtime cost.
--
-- block_bytes and qc_bytes are BYTEA-encoded JSON — chosen for MVP
-- debuggability. Session 5 may migrate to a deterministic binary
-- format if log size becomes an operational concern.
CREATE TABLE IF NOT EXISTS bft_log (
    height BIGINT PRIMARY KEY,
    block_hash BYTEA NOT NULL,
    block_bytes BYTEA NOT NULL,
    qc_bytes BYTEA NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_bft_log_hash ON bft_log (block_hash);

-- HotStuff BFT anti-equivocation vote history (session 5a). One row
-- per replica records the highest view in which that replica has
-- emitted a vote and the highest view in which it has emitted a
-- proposal. These counters are persisted BEFORE the vote or proposal
-- is broadcast, so a crash-and-restart cannot resurrect a voting
-- right the replica already used — closing the safety gap left by
-- session 4 where these counters were memory-only.
--
-- Writes are monotonic at the SQL level (GREATEST-based UPSERT in
-- PostgresVoteHistoryStore). Fresh replicas read (0, 0).
CREATE TABLE IF NOT EXISTS bft_vote_history (
    replica_id TEXT PRIMARY KEY,
    last_voted_view BIGINT NOT NULL DEFAULT 0,
    last_proposed_view BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- HotStuff BFT replica keypair persistence (session 5c). One row per
-- replica. Without this, fresh BLS keypairs are generated on every
-- boot and QCs signed under a prior boot's key cannot be verified
-- under the new roster — cross-boot chain extension was blocked in
-- session 5b. private_key is the 32-byte canonical fr.Element scalar;
-- public_key is the 96-byte compressed G2 point. Treat this table
-- like any other secret store — access to private_key is equivalent
-- to controlling the replica's signing authority.
CREATE TABLE IF NOT EXISTS bft_replica_keys (
    replica_id TEXT PRIMARY KEY,
    private_key BYTEA NOT NULL,
    public_key BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Judgment-aggregation calibration (session 9). The corpus, runs, and
-- per-question results that back the `calibration` field on
-- analyze action:get_result. The runtime field is populated from an
-- embedded JSON snapshot of the latest CI run (internal/calibration/
-- embed/latest.json) — these tables let a self-hoster running
-- `gemot calibration run` keep a queryable history beyond the embedded
-- snapshot. Demo mode (memory store) skips these tables entirely.
--
-- corpus_version is frozen per-row: a v2 corpus authoring does NOT
-- mutate v1 rows, so historical runs remain comparable.
CREATE TABLE IF NOT EXISTS calibration_questions (
    id TEXT PRIMARY KEY,
    corpus_version TEXT NOT NULL,
    question_text TEXT NOT NULL,
    options_json TEXT NOT NULL,
    ground_truth TEXT NOT NULL,
    source TEXT NOT NULL,
    source_ref TEXT DEFAULT '',
    deliberation_type TEXT NOT NULL,
    held_out INTEGER DEFAULT 0,
    tags TEXT DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_calibration_questions_corpus ON calibration_questions(corpus_version, deliberation_type);

CREATE TABLE IF NOT EXISTS calibration_runs (
    id TEXT PRIMARY KEY,
    corpus_version TEXT NOT NULL,
    gemot_version TEXT NOT NULL,
    model_version TEXT NOT NULL,
    seed BIGINT NOT NULL,
    started_at TIMESTAMPTZ DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    fleet_rate DOUBLE PRECISION,
    vote_only_rate DOUBLE PRECISION,
    solo_rate DOUBLE PRECISION,
    n INTEGER
);
CREATE INDEX IF NOT EXISTS idx_calibration_runs_started ON calibration_runs(started_at DESC);

CREATE TABLE IF NOT EXISTS calibration_results (
    run_id TEXT NOT NULL REFERENCES calibration_runs(id),
    question_id TEXT NOT NULL,
    fleet_answer TEXT,
    fleet_correct INTEGER,
    vote_only_answer TEXT,
    vote_only_correct INTEGER,
    solo_answer TEXT,
    solo_correct INTEGER,
    deliberation_id TEXT DEFAULT '',
    notes TEXT DEFAULT '',
    PRIMARY KEY (run_id, question_id)
);

-- Schema versioning
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at TIMESTAMPTZ DEFAULT NOW()
);
INSERT INTO schema_version (version) VALUES (9) ON CONFLICT DO NOTHING;
