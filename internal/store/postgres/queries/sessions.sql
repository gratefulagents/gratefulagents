-- name: CreateSession :one
INSERT INTO agent_sessions (agentrun_name, agentrun_ns, phase, current_step)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetSession :one
SELECT * FROM agent_sessions WHERE id = $1;

-- name: GetSessionByRun :one
SELECT * FROM agent_sessions WHERE agentrun_name = $1 AND agentrun_ns = $2;

-- name: UpdateSessionPhase :exec
UPDATE agent_sessions
SET phase = $2, current_step = $3
WHERE id = $1;

-- name: UpdateSessionPendingQuestion :exec
UPDATE agent_sessions
SET phase = $2, pending_question = $3, pending_input_type = $4, pending_request_id = $5
WHERE id = $1;

-- name: ClearPendingQuestion :exec
UPDATE agent_sessions
SET pending_question = '', pending_actions = '[]', pending_input_type = '', pending_request_id = '', phase = $2
WHERE id = $1;

-- name: UpdateSessionMetadata :exec
UPDATE agent_sessions SET metadata = $2 WHERE id = $1;

-- name: UpdateSessionPendingAction :exec
UPDATE agent_sessions
SET phase = $2, pending_question = $3, pending_actions = $4, pending_input_type = $5, pending_request_id = $6
WHERE id = $1;

-- name: ClearPendingAction :exec
UPDATE agent_sessions
SET pending_question = '', pending_actions = '[]', pending_input_type = '', pending_request_id = '', phase = $2
WHERE id = $1;

-- name: ListAllSessionMetrics :many
SELECT agentrun_name, agentrun_ns, metadata
FROM agent_sessions
WHERE metadata IS NOT NULL AND metadata != '{}';
