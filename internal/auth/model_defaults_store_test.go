package auth_test

import (
	"context"
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/auth"
)

// setupModelDefaultsTestStore reuses the soul-store harness (Postgres-backed,
// skips without TEST_DATABASE_URL) and additionally cleans the model defaults
// table.
func setupModelDefaultsTestStore(t *testing.T) *auth.PGStore {
	t.Helper()
	store, pool := setupSoulTestStore(t)
	if _, err := pool.Exec(context.Background(), "DELETE FROM auth_user_model_defaults"); err != nil {
		t.Fatalf("cleaning table auth_user_model_defaults: %v", err)
	}
	return store
}

func TestUserModelDefaultsUpsertGetDelete(t *testing.T) {
	store := setupModelDefaultsTestStore(t)
	ctx := context.Background()

	user, err := store.UpsertUser(ctx, &auth.User{Username: "alice", Name: "Alice", Role: "member"})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	// No defaults yet.
	got, err := store.GetUserModelDefaults(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserModelDefaults (empty): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil defaults before save, got %+v", got)
	}

	// Insert.
	saved, err := store.UpsertUserModelDefaults(ctx, &auth.UserModelDefaults{
		UserID: user.ID, Provider: "openai", Model: "gpt-5.2", ReasoningLevel: "high",
	})
	if err != nil {
		t.Fatalf("UpsertUserModelDefaults (insert): %v", err)
	}
	if saved.Provider != "openai" || saved.Model != "gpt-5.2" || saved.ReasoningLevel != "high" || saved.Disabled || saved.UpdatedAt.IsZero() {
		t.Fatalf("unexpected saved defaults: %+v", saved)
	}

	// Update, flipping disabled while keeping values.
	updated, err := store.UpsertUserModelDefaults(ctx, &auth.UserModelDefaults{
		UserID: user.ID, Provider: "anthropic", Model: "claude-sonnet-4-6", ReasoningLevel: "medium", Disabled: true,
	})
	if err != nil {
		t.Fatalf("UpsertUserModelDefaults (update): %v", err)
	}
	if updated.Provider != "anthropic" || updated.Model != "claude-sonnet-4-6" || updated.ReasoningLevel != "medium" || !updated.Disabled {
		t.Fatalf("unexpected updated defaults: %+v", updated)
	}

	got, err = store.GetUserModelDefaults(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserModelDefaults (after update): %v", err)
	}
	if got == nil || got.Provider != "anthropic" || !got.Disabled {
		t.Fatalf("round-trip defaults = %+v", got)
	}

	// Delete.
	if err := store.DeleteUserModelDefaults(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUserModelDefaults: %v", err)
	}
	got, err = store.GetUserModelDefaults(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserModelDefaults (after delete): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil defaults after delete, got %+v", got)
	}

	// Deleting again is a no-op.
	if err := store.DeleteUserModelDefaults(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUserModelDefaults (idempotent): %v", err)
	}
}
