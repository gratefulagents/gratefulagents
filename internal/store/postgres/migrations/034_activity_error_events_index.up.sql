-- Keep the dashboard's error-only polling bounded by matching the durable
-- ContentEvent error predicates. Ordinary trace/activity rows never enter this
-- index.
CREATE INDEX IF NOT EXISTS idx_activity_events_session_errors
    ON activity_events (session_id, created_at DESC, id DESC)
    WHERE detail @> '{"is_error": true}'::jsonb
       OR COALESCE(detail->>'failure_kind', '') <> ''
       OR LOWER(COALESCE(detail->>'attempt_status', detail->>'status', '')) IN ('error', 'failed', 'failure', 'fatal')
       OR LOWER(event_type) IN ('error', 'failed', 'failure', 'fatal', 'runtime_error');
