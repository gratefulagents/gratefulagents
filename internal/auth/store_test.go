package auth_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/auth"
	pgstore "github.com/gratefulagents/gratefulagents/internal/store/postgres"
)

// setupAuthTestStore returns a Postgres-backed auth store and pool.
// It skips when TEST_DATABASE_URL is unset, matching the project's integration
// test convention.
func setupAuthTestStore(t *testing.T) (*auth.PGStore, *pgxpool.Pool) {
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
	if _, err := pool.Exec(ctx, "DELETE FROM auth_users"); err != nil {
		t.Fatalf("cleaning table auth_users: %v", err)
	}
	return auth.NewPGStore(pool), pool
}
