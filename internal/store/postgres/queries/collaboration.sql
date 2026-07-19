-- name: SetResourceOwner :exec
INSERT INTO resource_ownership (resource_type, resource_id, resource_namespace, owner_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (resource_type, resource_id, resource_namespace) DO UPDATE SET
    owner_id = EXCLUDED.owner_id;

-- name: GetResourceOwner :one
SELECT * FROM resource_ownership
WHERE resource_type = $1 AND resource_id = $2 AND resource_namespace = $3;

-- name: ListOwnedResources :many
SELECT * FROM resource_ownership
WHERE owner_id = $1 AND resource_type = $2
ORDER BY created_at DESC;

-- name: ShareResource :one
INSERT INTO resource_shares (resource_type, resource_id, resource_namespace, shared_with_user_id, shared_by_user_id, permission)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (resource_type, resource_id, resource_namespace, shared_with_user_id) DO UPDATE SET
    permission = EXCLUDED.permission,
    updated_at = now()
RETURNING *;

-- name: RevokeShare :exec
DELETE FROM resource_shares WHERE id = $1;

-- name: UpdateSharePermission :exec
UPDATE resource_shares SET permission = $2, updated_at = now() WHERE id = $1;

-- name: ListSharesForResource :many
SELECT * FROM resource_shares
WHERE resource_type = $1 AND resource_id = $2 AND resource_namespace = $3
ORDER BY created_at DESC;

-- name: ListSharedWithMe :many
SELECT * FROM resource_shares
WHERE shared_with_user_id = $1 AND ($2::text = '' OR resource_type = $2)
ORDER BY created_at DESC;

-- name: GetSharePermission :one
SELECT * FROM resource_shares
WHERE resource_type = $1 AND resource_id = $2 AND resource_namespace = $3 AND shared_with_user_id = $4;

-- name: CreateNotification :exec
INSERT INTO notifications (user_id, type, title, body, resource_type, resource_id, resource_namespace, actor_id, actor_name)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ListNotifications :many
SELECT * FROM notifications
WHERE user_id = $1 AND ($2::boolean = false OR read = false)
ORDER BY created_at DESC
LIMIT $3;

-- name: MarkNotificationRead :exec
UPDATE notifications SET read = true WHERE id = $1;

-- name: MarkAllNotificationsRead :exec
UPDATE notifications SET read = true WHERE user_id = $1 AND read = false;

-- name: GetUnreadNotificationCount :one
SELECT count(*)::int FROM notifications WHERE user_id = $1 AND read = false;
