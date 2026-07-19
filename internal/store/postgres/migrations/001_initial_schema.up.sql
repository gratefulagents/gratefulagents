-- 001_initial_schema.up.sql
-- State backend for engg-operator agent sessions.
-- Replaces etcd-stored conversation history, ConfigMap plans, and ephemeral activity logs.

CREATE TABLE IF NOT EXISTS agent_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agentrun_name   TEXT NOT NULL,
    agentrun_ns     TEXT NOT NULL,
    phase           TEXT NOT NULL DEFAULT 'pending',
    current_step    TEXT NOT NULL DEFAULT '',
    session_mode    TEXT NOT NULL DEFAULT 'plan',
    pending_question TEXT NOT NULL DEFAULT '',
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (agentrun_name, agentrun_ns)
);

CREATE TABLE IF NOT EXISTS conversation_messages (
    id          BIGSERIAL PRIMARY KEY,
    session_id  UUID NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    role        TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system')),
    content     TEXT NOT NULL DEFAULT '',
    metadata    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_conversation_messages_session_id ON conversation_messages(session_id, id);

CREATE TABLE IF NOT EXISTS activity_events (
    id          BIGSERIAL PRIMARY KEY,
    session_id  UUID NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    event_type  TEXT NOT NULL,
    summary     TEXT NOT NULL DEFAULT '',
    detail      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_activity_events_session_id ON activity_events(session_id, created_at);

CREATE TABLE IF NOT EXISTS agent_artifacts (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id   UUID NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN ('plan', 'diff', 'activity_log', 'review', 'feasibility')),
    content      TEXT NOT NULL DEFAULT '',
    s3_url       TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL DEFAULT '',
    metadata     JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_agent_artifacts_session_kind ON agent_artifacts(session_id, kind);

-- Trigger to auto-update updated_at on agent_sessions.
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ language 'plpgsql';

CREATE TRIGGER update_agent_sessions_updated_at
    BEFORE UPDATE ON agent_sessions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_agent_artifacts_updated_at
    BEFORE UPDATE ON agent_artifacts
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
