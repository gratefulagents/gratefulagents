package postgres

import (
	"context"
	"fmt"

	"github.com/gratefulagents/gratefulagents/internal/auth"
)

// ListUserRoleModelPreferences lets trigger controllers snapshot the owner's
// personal routing without giving workers database access.
func (s *Store) ListUserRoleModelPreferences(ctx context.Context, userID string) ([]*auth.UserRoleModelPreference, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, role_name, provider, model, updated_at
		FROM auth_user_role_models
		WHERE user_id = $1
		ORDER BY role_name, provider`, userID)
	if err != nil {
		return nil, fmt.Errorf("listing user role models: %w", err)
	}
	defer rows.Close()
	var out []*auth.UserRoleModelPreference
	for rows.Next() {
		p := &auth.UserRoleModelPreference{}
		if err := rows.Scan(&p.UserID, &p.RoleName, &p.Provider, &p.Model, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning user role model: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
