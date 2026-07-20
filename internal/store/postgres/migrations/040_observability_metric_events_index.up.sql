-- Bound the historical observability event scan to metric-relevant rows for
-- the selected sessions. The overview query filters to these event types and
-- joins the selected runs' sessions before applying its LIMIT, and without a
-- matching partial index PostgreSQL walks every activity row in the window
-- (chatter and other tenants' runs included) evaluating the JSON expression.
-- Leading on session_id keeps multi-tenant ranges cheap: only the selected
-- sessions' metric rows are visited, already ordered by recency per session.
-- The WHERE predicate must stay textually equivalent to the query in
-- internal/store/postgres/observability.go for the planner to use it.
--
-- This migration runs outside a transaction (noTxMigrations) so both the
-- cleanup and the build can run CONCURRENTLY without blocking worker event
-- writes on large installations. The drop clears any invalid leftover from a
-- previously interrupted concurrent build so the retry can succeed.
-- NOTE: statements are split on semicolons after comment stripping, so keep
-- semicolons out of comment text.
DROP INDEX CONCURRENTLY IF EXISTS idx_activity_events_observability_metrics;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_activity_events_observability_metrics
    ON activity_events (session_id, created_at DESC, id DESC)
    WHERE event_type IN ('tool_end', 'subagent_status', 'llm_attempt', 'compact_boundary')
       OR detail->>'type' IN ('tool_end', 'subagent_status', 'llm_attempt', 'compact_boundary');
