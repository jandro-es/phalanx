-- =============================================================================
-- Phalanx — Migration 001: Initial Schema
-- =============================================================================
-- Run with: psql $DATABASE_URL -f 001_initial.sql
-- =============================================================================

BEGIN;

-- Extensions
CREATE EXTENSION IF NOT EXISTS "pgcrypto";  -- gen_random_uuid()

-- =============================================================================
-- LLM Providers
-- =============================================================================

CREATE TABLE llm_providers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL UNIQUE,
    base_url        TEXT NOT NULL,
    auth_method     TEXT NOT NULL CHECK (auth_method IN ('bearer', 'api-key-header', 'none')),
    api_key_ref     TEXT,                        -- vault reference, never plaintext
    default_model   TEXT NOT NULL,
    models          TEXT[] DEFAULT '{}',
    config          JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_providers_name ON llm_providers(name);

-- =============================================================================
-- Skills
-- =============================================================================

CREATE TABLE skills (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug            TEXT NOT NULL,
    name            TEXT NOT NULL,
    version         INT NOT NULL DEFAULT 1,
    system_prompt   TEXT NOT NULL,
    checklist_template TEXT NOT NULL,
    output_schema   JSONB,
    is_builtin      BOOLEAN NOT NULL DEFAULT false,
    tags            TEXT[] DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(slug, version)
);

CREATE INDEX idx_skills_slug ON skills(slug);
CREATE INDEX idx_skills_tags ON skills USING GIN(tags);

-- =============================================================================
-- Context Documents
-- =============================================================================

CREATE TABLE context_documents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    content         TEXT NOT NULL,
    doc_type        TEXT NOT NULL CHECK (doc_type IN ('guideline', 'non-negotiable', 'reference', 'example')),
    tags            TEXT[] DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_context_doc_type ON context_documents(doc_type);

-- =============================================================================
-- Agents
-- =============================================================================

CREATE TABLE agents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    skill_id        UUID NOT NULL REFERENCES skills(id) ON DELETE RESTRICT,
    provider_id     UUID NOT NULL REFERENCES llm_providers(id) ON DELETE RESTRICT,
    model_override  TEXT,
    temperature     REAL NOT NULL DEFAULT 0.0,
    max_tokens      INT NOT NULL DEFAULT 4096,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    priority        INT NOT NULL DEFAULT 100,
    config          JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_agents_enabled ON agents(enabled) WHERE enabled = true;
CREATE INDEX idx_agents_skill ON agents(skill_id);
CREATE INDEX idx_agents_provider ON agents(provider_id);

-- Agent ↔ Context many-to-many
CREATE TABLE agent_context (
    agent_id        UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    context_id      UUID NOT NULL REFERENCES context_documents(id) ON DELETE CASCADE,
    PRIMARY KEY (agent_id, context_id)
);

-- =============================================================================
-- Review Sessions
-- =============================================================================

CREATE TABLE review_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    external_pr_id  TEXT NOT NULL,                -- "github:owner/repo#123"
    platform        TEXT NOT NULL CHECK (platform IN ('github', 'gitlab', 'bitbucket')),
    repository_full_name TEXT NOT NULL,
    pr_number       INT NOT NULL,
    pr_title        TEXT,
    pr_author       TEXT,
    pr_url          TEXT,
    head_sha        TEXT NOT NULL,
    base_sha        TEXT NOT NULL,
    base_branch     TEXT,
    head_branch     TEXT,
    diff_snapshot   TEXT,                         -- full diff for reproducibility
    file_tree       JSONB,                        -- array of FileEntry
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'queued', 'running', 'completed', 'failed', 'cancelled')),
    composite_report TEXT,
    overall_verdict TEXT CHECK (overall_verdict IN ('pass', 'warn', 'fail', 'error', 'not_applicable')),
    trigger_source  TEXT NOT NULL DEFAULT 'webhook'
                    CHECK (trigger_source IN ('webhook', 'ci-action', 'cli', 'api', 'dashboard')),
    metadata        JSONB DEFAULT '{}',
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_sessions_external ON review_sessions(external_pr_id);
CREATE INDEX idx_sessions_repo ON review_sessions(repository_full_name);
CREATE INDEX idx_sessions_status ON review_sessions(status);
CREATE INDEX idx_sessions_started ON review_sessions(started_at DESC);

-- =============================================================================
-- Agent Reports
-- =============================================================================

CREATE TABLE agent_reports (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES review_sessions(id) ON DELETE CASCADE,
    agent_id        UUID NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    skill_slug      TEXT NOT NULL,
    skill_version   INT NOT NULL DEFAULT 1,
    model_used      TEXT NOT NULL,
    provider_name   TEXT NOT NULL,
    prompt_hash     TEXT NOT NULL,                -- SHA-256 of full prompt
    input_tokens    INT,
    output_tokens   INT,
    latency_ms      INT,
    cost_estimate_usd NUMERIC(10, 6),
    raw_response    TEXT NOT NULL,
    report_md       TEXT NOT NULL,
    checklist_json  JSONB DEFAULT '[]',
    findings        JSONB DEFAULT '[]',
    verdict         TEXT NOT NULL CHECK (verdict IN ('pass', 'warn', 'fail', 'error', 'not_applicable')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_reports_session ON agent_reports(session_id);
CREATE INDEX idx_reports_verdict ON agent_reports(verdict);
CREATE INDEX idx_reports_skill ON agent_reports(skill_slug);

-- =============================================================================
-- Approval Decisions
-- =============================================================================

CREATE TABLE approval_decisions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES review_sessions(id) ON DELETE CASCADE,
    decision        TEXT NOT NULL CHECK (decision IN ('approve', 'request_changes', 'defer')),
    engineer_id     TEXT NOT NULL,                -- SSO / Git platform user ID
    engineer_name   TEXT NOT NULL,
    engineer_email  TEXT,
    engineer_avatar_url TEXT,
    justification   TEXT,
    overridden_verdicts JSONB DEFAULT '[]',
    decided_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_decisions_session ON approval_decisions(session_id);
CREATE INDEX idx_decisions_engineer ON approval_decisions(engineer_id);
CREATE INDEX idx_decisions_decided ON approval_decisions(decided_at DESC);

-- =============================================================================
-- Audit Log (APPEND-ONLY)
-- =============================================================================

CREATE TABLE audit_log (
    id              BIGSERIAL PRIMARY KEY,
    event_type      TEXT NOT NULL,
    session_id      UUID,
    agent_id        UUID,
    actor           TEXT NOT NULL,
    payload         JSONB NOT NULL,
    payload_hash    TEXT,                         -- optional hash-chain
    prev_hash       TEXT,                         -- hash of previous record
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_session ON audit_log(session_id);
CREATE INDEX idx_audit_event ON audit_log(event_type);
CREATE INDEX idx_audit_actor ON audit_log(actor);
CREATE INDEX idx_audit_created ON audit_log(created_at DESC);

-- Composite index for common dashboard queries
CREATE INDEX idx_audit_session_event ON audit_log(session_id, event_type);

-- =============================================================================
-- Audit Log Security: Restrict to INSERT + SELECT only
-- =============================================================================

-- Create a restricted role for the application
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'phalanx_app') THEN
    CREATE ROLE phalanx_app WITH LOGIN;
  END IF;
END $$;

-- Grant full CRUD on all tables except audit_log
GRANT SELECT, INSERT, UPDATE, DELETE ON llm_providers TO phalanx_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON skills TO phalanx_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON context_documents TO phalanx_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON agents TO phalanx_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON agent_context TO phalanx_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON review_sessions TO phalanx_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON agent_reports TO phalanx_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON approval_decisions TO phalanx_app;

-- Audit log: INSERT + SELECT ONLY — no UPDATE, no DELETE
GRANT SELECT, INSERT ON audit_log TO phalanx_app;
GRANT USAGE, SELECT ON SEQUENCE audit_log_id_seq TO phalanx_app;

-- =============================================================================
-- Helper function: Updated timestamp trigger
-- =============================================================================

CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER tr_providers_updated BEFORE UPDATE ON llm_providers
  FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER tr_skills_updated BEFORE UPDATE ON skills
  FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER tr_agents_updated BEFORE UPDATE ON agents
  FOR EACH ROW EXECUTE FUNCTION update_updated_at();

COMMIT;
