-- name: AppendMessage :one
INSERT INTO conversation_messages (session_id, role, content, metadata)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetMessages :many
SELECT * FROM conversation_messages
WHERE session_id = $1
  AND NOT (COALESCE(metadata, '{}'::jsonb) ? 'overseer_held')
  AND NOT (role = 'user' AND delivery_state = 'cancelled')
ORDER BY id ASC;

-- name: GetMessagesIncludingCancelled :many
SELECT * FROM conversation_messages
WHERE session_id = $1
  AND NOT (COALESCE(metadata, '{}'::jsonb) ? 'overseer_held')
ORDER BY id ASC;

-- name: GetMessagesSince :many
SELECT * FROM conversation_messages
WHERE session_id = $1 AND id > $2
  AND NOT (COALESCE(metadata, '{}'::jsonb) ? 'overseer_held')
  AND NOT (role = 'user' AND delivery_state = 'cancelled')
ORDER BY id ASC;

-- name: GetMessageCount :one
SELECT count(*) FROM conversation_messages
WHERE session_id = $1
  AND NOT (COALESCE(metadata, '{}'::jsonb) ? 'overseer_held');

-- name: GetLatestUserMessage :one
SELECT * FROM conversation_messages
WHERE session_id = $1 AND role = 'user'
  AND NOT (COALESCE(metadata, '{}'::jsonb) ? 'overseer_held')
ORDER BY id DESC
LIMIT 1;

-- name: PollNewUserMessages :many
-- Pending delivery_state is authoritative and there is deliberately no scalar
-- cursor: exact stopped prompts are completed when interrupted, and a cursor
-- could hide a different pending hole inserted before a later assistant reply.
-- Candidates come off the (session_id, id) pending partial index; the held
-- gate is one probe of the overseer_held partial index — a message at or after
-- the earliest held row (including a held candidate itself) stays invisible
-- until the overseer releases it.
SELECT candidate.* FROM conversation_messages AS candidate
WHERE candidate.session_id = $1 AND candidate.role = 'user'
  AND candidate.delivery_state = 'pending'
  AND candidate.id < COALESCE((
      SELECT min(held.id) FROM conversation_messages AS held
      WHERE held.session_id = $1
        AND COALESCE(held.metadata, '{}'::jsonb) ? 'overseer_held'
  ), 9223372036854775807)
ORDER BY candidate.id ASC;
