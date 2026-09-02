-- SendAgentRunMessage idempotency: a client-chosen key stored in the user
-- message metadata (client_message_id). The partial unique index turns a
-- concurrent duplicate into a unique violation, which the dashboard maps to
-- the stored message instead of inserting a second pending turn. The lookup
-- predicate in internal/dashboard/message_dedupe.go must keep the textual
-- `metadata ? 'client_message_id'` test so the planner can use this index.
--
-- Runs outside a transaction (noTxMigrations) so the build is CONCURRENTLY
-- and never blocks agent claims or dashboard sends on conversation_messages.
-- The drop clears an invalid leftover from an interrupted build so the retry
-- can succeed.
-- NOTE: statements are split on semicolons after comment stripping, so keep
-- semicolons out of comment text.
DROP INDEX CONCURRENTLY IF EXISTS idx_conversation_messages_client_message_id;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_conversation_messages_client_message_id
    ON conversation_messages (session_id, (metadata->>'client_message_id'))
    WHERE role = 'user' AND metadata ? 'client_message_id';
