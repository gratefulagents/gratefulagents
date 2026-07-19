-- name: WriteActivityEvent :one
INSERT INTO activity_events (session_id, event_type, summary, detail)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetActivityEvents :many
SELECT * FROM activity_events
WHERE session_id = $1
ORDER BY created_at ASC, id ASC;

-- name: GetRecentActivityEvents :many
SELECT * FROM activity_events
WHERE session_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2;
