-- PR feedback can arrive concurrently through webhook acceleration and polling.
-- Remove any race-created duplicates before enforcing one durable message per
-- GitHub event key within a session.
WITH duplicates AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY session_id, metadata ->> 'github_event_key'
               ORDER BY id
           ) AS duplicate_number
    FROM conversation_messages
    WHERE metadata ? 'github_event_key'
)
DELETE FROM conversation_messages AS messages
USING duplicates
WHERE messages.id = duplicates.id
  AND duplicates.duplicate_number > 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_conversation_messages_github_event_key_unique
    ON conversation_messages (session_id, (metadata ->> 'github_event_key'))
    WHERE metadata ? 'github_event_key';
