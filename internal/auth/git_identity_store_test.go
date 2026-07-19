package auth_test

import (
	"context"
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/auth"
)

// setupGitIdentityTestStore reuses the soul-store harness (Postgres-backed,
// skips without TEST_DATABASE_URL) and additionally cleans the git identity
// table.
func setupGitIdentityTestStore(t *testing.T) *auth.PGStore {
	t.Helper()
	store, pool := setupSoulTestStore(t)
	if _, err := pool.Exec(context.Background(), "DELETE FROM auth_user_git_identities"); err != nil {
		t.Fatalf("cleaning table auth_user_git_identities: %v", err)
	}
	return store
}

func TestUserGitIdentityUpsertAndGet(t *testing.T) {
	store := setupGitIdentityTestStore(t)
	ctx := context.Background()

	user, err := store.UpsertUser(ctx, &auth.User{Username: "alice", Name: "Alice", Role: "member"})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	// No identity yet.
	got, err := store.GetUserGitIdentity(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserGitIdentity (empty): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil identity before save, got %+v", got)
	}

	// Insert.
	saved, err := store.UpsertUserGitIdentity(ctx, &auth.UserGitIdentity{
		UserID: user.ID, Name: "Alice Doe", Email: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("UpsertUserGitIdentity (insert): %v", err)
	}
	if saved.Name != "Alice Doe" || saved.Email != "alice@example.com" || saved.UpdatedAt.IsZero() {
		t.Fatalf("unexpected saved identity: %+v", saved)
	}

	// Update.
	updated, err := store.UpsertUserGitIdentity(ctx, &auth.UserGitIdentity{
		UserID: user.ID, Name: "Alice D.", Email: "alice@users.noreply.github.com",
	})
	if err != nil {
		t.Fatalf("UpsertUserGitIdentity (update): %v", err)
	}
	if updated.Name != "Alice D." || updated.Email != "alice@users.noreply.github.com" {
		t.Fatalf("update identity = %+v", updated)
	}

	got, err = store.GetUserGitIdentity(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserGitIdentity: %v", err)
	}
	if got == nil || got.Name != "Alice D." || got.Email != "alice@users.noreply.github.com" {
		t.Fatalf("round-trip identity = %+v", got)
	}
}
