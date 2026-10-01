-- Idempotency index for activity_events.client_event_id (see migration 064).
-- Writers insert with ON CONFLICT (session_id, client_event_id) WHERE
-- client_event_id IS NOT NULL DO NOTHING, so a retried batch skips the rows
-- an earlier ambiguous attempt already committed.
--
-- Runs outside a transaction (noTxMigrations) so the build is CONCURRENTLY
-- and never blocks event writers on activity_events. The drop clears an
-- invalid leftover from an interrupted build so the retry can succeed.
-- NOTE: statements are split on semicolons after comment stripping, so keep
-- semicolons out of comment text.
DROP INDEX CONCURRENTLY IF EXISTS idx_activity_events_client_event_id;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_activity_events_client_event_id
    ON activity_events (session_id, client_event_id)
    WHERE client_event_id IS NOT NULL;
