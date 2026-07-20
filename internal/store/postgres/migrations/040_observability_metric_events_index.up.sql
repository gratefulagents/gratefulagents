-- Bound the historical observability event scan to metric-relevant rows.
-- The overview query filters to these event types before applying its LIMIT;
-- without a matching partial index PostgreSQL walks every activity row in the
-- window (chatter included) evaluating the JSON expression. The predicate
-- must stay textually equivalent to the WHERE clause in
-- internal/store/postgres/observability.go for the planner to use it.
CREATE INDEX IF NOT EXISTS idx_activity_events_observability_metrics
    ON activity_events (created_at DESC, id DESC)
    WHERE event_type IN ('tool_end', 'subagent_status', 'llm_attempt', 'compact_boundary')
       OR detail->>'type' IN ('tool_end', 'subagent_status', 'llm_attempt', 'compact_boundary');
