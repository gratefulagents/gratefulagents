-- name: UpsertSlackThread :exec
INSERT INTO slack_threads (slack_agent, channel_id, thread_ts, run_namespace, run_name, kind)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (slack_agent, channel_id, thread_ts) DO UPDATE SET
    run_namespace = EXCLUDED.run_namespace,
    run_name = EXCLUDED.run_name,
    kind = EXCLUDED.kind,
    updated_at = now();

-- name: GetSlackThread :one
SELECT * FROM slack_threads
WHERE slack_agent = $1 AND channel_id = $2 AND thread_ts = $3;

-- name: GetSlackThreadByRun :one
SELECT * FROM slack_threads
WHERE run_namespace = $1 AND run_name = $2
LIMIT 1;

-- name: CreateSlackDraft :one
INSERT INTO slack_drafts (slack_agent, namespace, owner_subject, channel_id, thread_ts, target_user, incoming_text, draft_text, status, notify_msg_ts, kind, origin_msg_ts, run_name)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetSlackDraft :one
SELECT * FROM slack_drafts WHERE id = $1;

-- name: ClaimSlackDraft :one
UPDATE slack_drafts
SET status = sqlc.arg(next_status)
WHERE id = sqlc.arg(id)
  AND namespace = sqlc.arg(namespace)
  AND slack_agent = sqlc.arg(slack_agent)
  AND owner_subject = sqlc.arg(owner_subject)
  AND kind = sqlc.arg(kind)
  AND status = 'pending'
RETURNING *;

-- name: ListPendingSlackDrafts :many
SELECT * FROM slack_drafts
WHERE slack_agent = $1 AND status = 'pending'
ORDER BY created_at DESC
LIMIT $2;

-- name: ListSlackDraftsForOwner :many
SELECT * FROM slack_drafts
WHERE owner_subject = $1 AND ($2::text = '' OR status = $2)
ORDER BY created_at DESC
LIMIT $3;

-- name: ListSlackDraftsByAgent :many
SELECT * FROM slack_drafts
WHERE namespace = sqlc.arg(namespace)
  AND slack_agent = sqlc.arg(slack_agent)
  AND (sqlc.arg(status)::text = '' OR status = sqlc.arg(status))
ORDER BY created_at DESC
LIMIT sqlc.arg(max_rows);

-- name: CountSlackDraftsByStatus :many
SELECT status, COUNT(*) AS count FROM slack_drafts
WHERE slack_agent = $1
GROUP BY status;

-- name: ResolveSlackDraft :exec
UPDATE slack_drafts
SET status = sqlc.arg(status), draft_text = sqlc.arg(draft_text), decided_at = now()
WHERE id = sqlc.arg(id) AND status = sqlc.arg(expected_status);

-- name: MarkSlackEventSeen :one
INSERT INTO slack_events (slack_agent, envelope_id)
VALUES ($1, $2)
ON CONFLICT (slack_agent, envelope_id) DO NOTHING
RETURNING envelope_id;

-- name: PruneSlackEvents :exec
DELETE FROM slack_events WHERE seen_at < $1;

-- name: SetSlackDraftNotifyTS :exec
UPDATE slack_drafts SET notify_msg_ts = $2 WHERE id = $1;

-- name: ResolveSlackDraftEdited :exec
UPDATE slack_drafts
SET status = sqlc.arg(status), edited_text = sqlc.arg(edited_text), decided_at = now()
WHERE id = sqlc.arg(id) AND status = sqlc.arg(expected_status);

-- name: UpdateSlackDraftText :exec
UPDATE slack_drafts
SET draft_text = sqlc.arg(draft_text), status = sqlc.arg(next_status), decided_at = NULL
WHERE id = sqlc.arg(id) AND status = sqlc.arg(expected_status);
