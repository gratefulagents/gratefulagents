-- Bound historical observability range scans and namespace session lookups.
CREATE INDEX IF NOT EXISTS idx_activity_events_observability_range
    ON activity_events (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_agent_sessions_namespace_created
    ON agent_sessions (agentrun_ns, created_at DESC);
