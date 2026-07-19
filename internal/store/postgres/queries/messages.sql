-- name: AppendMessage :one
INSERT INTO conversation_messages (session_id, role, content, metadata)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetMessages :many
SELECT * FROM conversation_messages
WHERE session_id = $1
  AND NOT (COALESCE(metadata, '{}'::jsonb) ? 'overseer_held')
  AND NOT (role = 'user' AND COALESCE(metadata, '{}'::jsonb) ? 'cancelled_at_unix')
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
  AND NOT (role = 'user' AND COALESCE(metadata, '{}'::jsonb) ? 'cancelled_at_unix')
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
SELECT candidate.* FROM conversation_messages AS candidate
WHERE candidate.session_id = $1 AND candidate.role = 'user'
  AND candidate.delivery_state = 'pending'
  AND NOT (COALESCE(candidate.metadata, '{}'::jsonb) ? 'cancelled_at_unix')
  -- Pending state is authoritative. Exact stopped prompts are completed when
  -- interrupted; a scalar cursor must never hide a different pending hole.
  AND sqlc.arg(after_id) >= 0
  AND NOT (COALESCE(candidate.metadata, '{}'::jsonb) ? 'overseer_held')
  AND NOT EXISTS (
      SELECT 1 FROM conversation_messages AS held
      WHERE held.session_id = candidate.session_id
        AND held.id <= candidate.id
        AND COALESCE(held.metadata, '{}'::jsonb) ? 'overseer_held'
  )
ORDER BY candidate.id ASC;
