package auth_test

import (
	"context"
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/auth"
)

func TestUserProfileUpsertPreservesExplicitRole(t *testing.T) {
	store, _ := setupSoulTestStore(t)
	ctx := context.Background()

	user, err := store.UpsertUser(ctx, &auth.User{Username: "promoted@example.com", Name: "Before", Role: auth.RoleMember})
	if err != nil {
		t.Fatalf("initial UpsertUser() error = %v", err)
	}
	if err := store.SetUserRole(ctx, user.ID, auth.RoleAdmin); err != nil {
		t.Fatalf("SetUserRole() error = %v", err)
	}

	updated, err := store.UpsertUser(ctx, &auth.User{Username: user.Username, Name: "After", Role: auth.RoleMember})
	if err != nil {
		t.Fatalf("profile UpsertUser() error = %v", err)
	}
	if updated.Name != "After" {
		t.Fatalf("name = %q, want refreshed profile", updated.Name)
	}
	if updated.Role != auth.RoleAdmin {
		t.Fatalf("role = %q, want explicit admin role preserved", updated.Role)
	}
}
