package auth_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/auth"
	pgstore "github.com/gratefulagents/gratefulagents/internal/store/postgres"
)

// setupSoulTestStore returns a Postgres-backed auth store and a seeded user id.
// It skips when TEST_DATABASE_URL is unset, matching the project's integration
// test convention.
func setupSoulTestStore(t *testing.T) (*auth.PGStore, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to test db: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("running migrations: %v", err)
	}
	for _, table := range []string{"auth_user_souls", "auth_users"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("cleaning table %s: %v", table, err)
		}
	}
	return auth.NewPGStore(pool), pool
}

func TestUserSoulUpsertAndGet(t *testing.T) {
	store, _ := setupSoulTestStore(t)
	ctx := context.Background()

	user, err := store.UpsertUser(ctx, &auth.User{Username: "alice", Name: "Alice", Role: "member"})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	// No soul yet.
	got, err := store.GetUserSoul(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserSoul (empty): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil soul before save, got %+v", got)
	}

	// Insert.
	saved, err := store.UpsertUserSoul(ctx, &auth.UserSoul{UserID: user.ID, Content: "# Alice persona"})
	if err != nil {
		t.Fatalf("UpsertUserSoul (insert): %v", err)
	}
	if saved.Content != "# Alice persona" || saved.UpdatedAt.IsZero() {
		t.Fatalf("unexpected saved soul: %+v", saved)
	}

	// Update.
	updated, err := store.UpsertUserSoul(ctx, &auth.UserSoul{UserID: user.ID, Content: "# Alice persona v2"})
	if err != nil {
		t.Fatalf("UpsertUserSoul (update): %v", err)
	}
	if updated.Content != "# Alice persona v2" {
		t.Fatalf("update content = %q", updated.Content)
	}

	got, err = store.GetUserSoul(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserSoul: %v", err)
	}
	if got == nil || got.Content != "# Alice persona v2" {
		t.Fatalf("round-trip soul = %+v", got)
	}
}
