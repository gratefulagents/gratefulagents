-- Per-session id-cursor access for activity_events. Version probes
-- (MAX(id)), delta tails (id > $n), and latest-event lookups descend this
-- index in O(log n) instead of scanning every event row of a session; the
-- existing (session_id, created_at) index keeps serving created_at-ordered
-- reads.
CREATE INDEX IF NOT EXISTS idx_activity_events_session_id_id
    ON activity_events (session_id, id);
