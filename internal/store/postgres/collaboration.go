package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

// --- Resource Ownership ---

func (s *Store) SetResourceOwner(ctx context.Context, resourceType, resourceID, resourceNS, ownerID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO resource_ownership (resource_type, resource_id, resource_namespace, owner_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (resource_type, resource_id, resource_namespace) DO UPDATE SET
			owner_id = EXCLUDED.owner_id`,
		resourceType, resourceID, resourceNS, ownerID)
	if err != nil {
		return fmt.Errorf("setting resource owner: %w", err)
	}
	return nil
}

func (s *Store) GetResourceOwner(ctx context.Context, resourceType, resourceID, resourceNS string) (*store.ResourceOwnership, error) {
	var o store.ResourceOwnership
	err := s.pool.QueryRow(ctx, `
		SELECT id, resource_type, resource_id, resource_namespace, owner_id, created_at
		FROM resource_ownership
		WHERE resource_type = $1 AND resource_id = $2 AND resource_namespace = $3`,
		resourceType, resourceID, resourceNS).
		Scan(&o.ID, &o.ResourceType, &o.ResourceID, &o.ResourceNamespace, &o.OwnerID, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// No ownership recorded — distinct from a lookup failure so callers
		// can treat the resource as system/trigger-owned.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting resource owner: %w", err)
	}
	return &o, nil
}

// ListResourceOwnersByType returns all ownership records for a resource type,
// used to bulk-resolve visibility when filtering lists.
func (s *Store) ListResourceOwnersByType(ctx context.Context, resourceType string) ([]store.ResourceOwnership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, resource_type, resource_id, resource_namespace, owner_id, created_at
		FROM resource_ownership
		WHERE resource_type = $1`,
		resourceType)
	if err != nil {
		return nil, fmt.Errorf("listing resource owners: %w", err)
	}
	defer rows.Close()
	var out []store.ResourceOwnership
	for rows.Next() {
		var o store.ResourceOwnership
		if err := rows.Scan(&o.ID, &o.ResourceType, &o.ResourceID, &o.ResourceNamespace, &o.OwnerID, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning resource ownership: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) ListOwnedResources(ctx context.Context, ownerID, resourceType string) ([]store.ResourceOwnership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, resource_type, resource_id, resource_namespace, owner_id, created_at
		FROM resource_ownership
		WHERE owner_id = $1 AND resource_type = $2
		ORDER BY created_at DESC`,
		ownerID, resourceType)
	if err != nil {
		return nil, fmt.Errorf("listing owned resources: %w", err)
	}
	defer rows.Close()
	var out []store.ResourceOwnership
	for rows.Next() {
		var o store.ResourceOwnership
		if err := rows.Scan(&o.ID, &o.ResourceType, &o.ResourceID, &o.ResourceNamespace, &o.OwnerID, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning resource ownership: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// --- Resource Sharing ---

func (s *Store) ShareResource(ctx context.Context, share *store.ResourceShare) (*store.ResourceShare, error) {
	var rs store.ResourceShare
	err := s.pool.QueryRow(ctx, `
		INSERT INTO resource_shares (resource_type, resource_id, resource_namespace, shared_with_user_id, shared_by_user_id, permission)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (resource_type, resource_id, resource_namespace, shared_with_user_id) DO UPDATE SET
			permission = EXCLUDED.permission,
			updated_at = now()
		RETURNING id, resource_type, resource_id, resource_namespace, shared_with_user_id, shared_by_user_id, permission, created_at, updated_at`,
		share.ResourceType, share.ResourceID, share.ResourceNamespace,
		share.SharedWithUserID, share.SharedByUserID, share.Permission).
		Scan(&rs.ID, &rs.ResourceType, &rs.ResourceID, &rs.ResourceNamespace,
			&rs.SharedWithUserID, &rs.SharedByUserID, &rs.Permission,
			&rs.CreatedAt, &rs.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("sharing resource: %w", err)
	}
	return &rs, nil
}

func (s *Store) RevokeShare(ctx context.Context, shareID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM resource_shares WHERE id = $1`, shareID)
	if err != nil {
		return fmt.Errorf("revoking share: %w", err)
	}
	return nil
}

// GetShareByID returns a single share by its ID, or an error when missing.
func (s *Store) GetShareByID(ctx context.Context, shareID string) (*store.ResourceShare, error) {
	var rs store.ResourceShare
	err := s.pool.QueryRow(ctx, `
		SELECT id, resource_type, resource_id, resource_namespace, shared_with_user_id, shared_by_user_id, permission, created_at, updated_at
		FROM resource_shares
		WHERE id = $1`, shareID).
		Scan(&rs.ID, &rs.ResourceType, &rs.ResourceID, &rs.ResourceNamespace,
			&rs.SharedWithUserID, &rs.SharedByUserID, &rs.Permission,
			&rs.CreatedAt, &rs.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting share: %w", err)
	}
	return &rs, nil
}

func (s *Store) UpdateSharePermission(ctx context.Context, shareID, permission string) error {
	_, err := s.pool.Exec(ctx, `UPDATE resource_shares SET permission = $2, updated_at = now() WHERE id = $1`, shareID, permission)
	if err != nil {
		return fmt.Errorf("updating share permission: %w", err)
	}
	return nil
}

func (s *Store) ListSharesForResource(ctx context.Context, resourceType, resourceID, resourceNS string) ([]store.ResourceShare, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, resource_type, resource_id, resource_namespace, shared_with_user_id, shared_by_user_id, permission, created_at, updated_at
		FROM resource_shares
		WHERE resource_type = $1 AND resource_id = $2 AND resource_namespace = $3
		ORDER BY created_at DESC`,
		resourceType, resourceID, resourceNS)
	if err != nil {
		return nil, fmt.Errorf("listing shares for resource: %w", err)
	}
	defer rows.Close()
	return collectShares(rows)
}

func (s *Store) ListSharedWithMe(ctx context.Context, userID, resourceType string) ([]store.ResourceShare, error) {
	query := `
		SELECT id, resource_type, resource_id, resource_namespace, shared_with_user_id, shared_by_user_id, permission, created_at, updated_at
		FROM resource_shares
		WHERE shared_with_user_id = $1`
	var args []interface{}
	args = append(args, userID)
	if resourceType != "" {
		query += ` AND resource_type = $2`
		args = append(args, resourceType)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing shared with me: %w", err)
	}
	defer rows.Close()
	var out []store.ResourceShare
	for rows.Next() {
		var rs store.ResourceShare
		if err := rows.Scan(&rs.ID, &rs.ResourceType, &rs.ResourceID, &rs.ResourceNamespace,
			&rs.SharedWithUserID, &rs.SharedByUserID, &rs.Permission,
			&rs.CreatedAt, &rs.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning resource share: %w", err)
		}
		out = append(out, rs)
	}
	return out, rows.Err()
}

func (s *Store) GetSharePermission(ctx context.Context, resourceType, resourceID, resourceNS, userID string) (*store.ResourceShare, error) {
	var rs store.ResourceShare
	err := s.pool.QueryRow(ctx, `
		SELECT id, resource_type, resource_id, resource_namespace, shared_with_user_id, shared_by_user_id, permission, created_at, updated_at
		FROM resource_shares
		WHERE resource_type = $1 AND resource_id = $2 AND resource_namespace = $3 AND shared_with_user_id = $4`,
		resourceType, resourceID, resourceNS, userID).
		Scan(&rs.ID, &rs.ResourceType, &rs.ResourceID, &rs.ResourceNamespace,
			&rs.SharedWithUserID, &rs.SharedByUserID, &rs.Permission,
			&rs.CreatedAt, &rs.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting share permission: %w", err)
	}
	return &rs, nil
}

// --- Notifications ---

func (s *Store) CreateNotification(ctx context.Context, n *store.Notification) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO notifications (user_id, type, title, body, resource_type, resource_id, resource_namespace, actor_id, actor_name)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		n.UserID, n.Type, n.Title, n.Body, n.ResourceType, n.ResourceID, n.ResourceNamespace, n.ActorID, n.ActorName)
	if err != nil {
		return fmt.Errorf("creating notification: %w", err)
	}
	return nil
}

func (s *Store) HasUnreadNotification(ctx context.Context, userID, notifType, resourceID, resourceNamespace string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM notifications
			WHERE user_id = $1 AND type = $2 AND resource_id = $3 AND resource_namespace = $4 AND read = false
		)`, userID, notifType, resourceID, resourceNamespace).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking unread notification: %w", err)
	}
	return exists, nil
}

func (s *Store) ListNotifications(ctx context.Context, userID string, unreadOnly bool, limit int32) ([]store.Notification, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, type, title, body, resource_type, resource_id, resource_namespace, actor_id, actor_name, read, created_at
		FROM notifications
		WHERE user_id = $1 AND ($2::boolean = false OR read = false)
		ORDER BY created_at DESC
		LIMIT $3`,
		userID, unreadOnly, limit)
	if err != nil {
		return nil, fmt.Errorf("listing notifications: %w", err)
	}
	defer rows.Close()
	var out []store.Notification
	for rows.Next() {
		var n store.Notification
		if err := rows.Scan(&n.ID, &n.UserID, &n.Type, &n.Title, &n.Body, &n.ResourceType, &n.ResourceID, &n.ResourceNamespace, &n.ActorID, &n.ActorName, &n.Read, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning notification: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) MarkNotificationRead(ctx context.Context, notificationID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE notifications SET read = true WHERE id = $1`, notificationID)
	if err != nil {
		return fmt.Errorf("marking notification read: %w", err)
	}
	return nil
}

// MarkNotificationReadForUser marks a notification read only when it belongs
// to the given user, preventing cross-user mutation by guessed IDs.
func (s *Store) MarkNotificationReadForUser(ctx context.Context, notificationID, userID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE notifications SET read = true WHERE id = $1 AND user_id = $2`, notificationID, userID)
	if err != nil {
		return fmt.Errorf("marking notification read: %w", err)
	}
	return nil
}

func (s *Store) MarkAllNotificationsRead(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE notifications SET read = true WHERE user_id = $1 AND read = false`, userID)
	if err != nil {
		return fmt.Errorf("marking all notifications read: %w", err)
	}
	return nil
}

func (s *Store) GetUnreadNotificationCount(ctx context.Context, userID string) (int32, error) {
	var count int32
	err := s.pool.QueryRow(ctx, `SELECT count(*)::int FROM notifications WHERE user_id = $1 AND read = false`, userID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("getting unread notification count: %w", err)
	}
	return count, nil
}

// --- helpers ---

func collectShares(rows pgx.Rows) ([]store.ResourceShare, error) {
	var out []store.ResourceShare
	for rows.Next() {
		var rs store.ResourceShare
		if err := rows.Scan(&rs.ID, &rs.ResourceType, &rs.ResourceID, &rs.ResourceNamespace,
			&rs.SharedWithUserID, &rs.SharedByUserID, &rs.Permission,
			&rs.CreatedAt, &rs.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning resource share: %w", err)
		}
		out = append(out, rs)
	}
	return out, rows.Err()
}
