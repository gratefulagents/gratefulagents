-- Agent-filed platform bug reports, complaints, and feature requests. Agents
-- use the report_bug tool to record platform/tooling failures (for example a
-- tool that keeps misbehaving) so humans can triage them across runs. Reports
-- deduplicate per (namespace, fingerprint): a reoccurrence bumps
-- occurrences/last_seen_at instead of inserting a new row.
CREATE TABLE IF NOT EXISTS agent_bug_reports (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    namespace     TEXT NOT NULL,
    run_name      TEXT NOT NULL,
    session_id    UUID REFERENCES agent_sessions(id) ON DELETE SET NULL,
    category      TEXT NOT NULL DEFAULT 'bug',
    tool_name     TEXT NOT NULL DEFAULT '',
    title         TEXT NOT NULL,
    body          TEXT NOT NULL,
    fingerprint   TEXT NOT NULL,
    occurrences   INT NOT NULL DEFAULT 1,
    status        TEXT NOT NULL DEFAULT 'open',
    status_note   TEXT NOT NULL DEFAULT '',
    status_actor  TEXT NOT NULL DEFAULT '',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (namespace, fingerprint)
);

CREATE INDEX IF NOT EXISTS idx_agent_bug_reports_namespace_status
    ON agent_bug_reports(namespace, status, last_seen_at DESC);
