package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRetirementMigrationRegistered(t *testing.T) {
	for _, migration := range orderedMigrations() {
		if migration.version == 67 {
			if migration.sql != migration067Up || migration.optional || noTxMigrations[67] {
				t.Fatal("retirement migration must be required and transactional")
			}
			return
		}
	}
	t.Fatal("retirement migration is not registered")
}

func TestRetirementMigration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, upgrade := range []bool{false, true} {
		name := "fresh"
		if upgrade {
			name = "upgrade"
		}
		t.Run(name, func(t *testing.T) {
			database := "retirement_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			identifier := pgx.Identifier{database}.Sanitize()
			if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
					t.Error(err)
				}
			})
			config, err := pgxpool.ParseConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			config.ConnConfig.Database = database
			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if upgrade {
				// Mark only retirement applied to construct the previous release's schema.
				_, err = pool.Exec(ctx, `CREATE TABLE schema_migrations (version INT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()); INSERT INTO schema_migrations(version) VALUES (67)`)
				if err != nil {
					t.Fatal(err)
				}
				if err := Migrate(ctx, pool); err != nil {
					t.Fatal(err)
				}
				_, err = pool.Exec(ctx, `
INSERT INTO agent_sessions(agentrun_name, agentrun_ns) VALUES ('keep', 'test');
INSERT INTO agent_artifacts(session_id, kind, content) SELECT id, kind, 'keep or retire' FROM agent_sessions CROSS JOIN unnest(ARRAY['plan', 'security_report', 'security_sarif']) AS kind;
INSERT INTO security_scans(namespace, scan_name, run_name) VALUES ('test', 'retire', 'retire');
INSERT INTO agent_bug_reports(namespace, run_name, title, body, fingerprint) VALUES ('test', 'retire', 'retire', 'retire', 'retire');
INSERT INTO resource_ownership(resource_type, resource_id, resource_namespace, owner_id) VALUES ('securityscan', 'retire', 'test', 'user'), ('project', 'keep', 'test', 'user');
INSERT INTO resource_shares(resource_type, resource_id, resource_namespace, shared_with_user_id, shared_by_user_id) VALUES ('securityprogram', 'retire', 'test', 'user', 'owner'), ('project', 'keep', 'test', 'user', 'owner');
INSERT INTO notifications(user_id, type, title, resource_type) VALUES ('user', 'share', 'retire', 'securityscan'), ('user', 'share', 'keep', 'project');
DELETE FROM schema_migrations WHERE version = 67;
`)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND (tablename LIKE 'security_%' OR tablename = 'agent_bug_reports')`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("retired tables remaining = %d, error = %v", count, err)
			}
			if upgrade {
				for _, table := range []string{"agent_sessions", "agent_artifacts", "resource_ownership", "resource_shares", "notifications"} {
					if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 1 {
						t.Fatalf("%s: retained rows = %d, want 1; error = %v", table, count, err)
					}
				}
				_, err = pool.Exec(ctx, `INSERT INTO agent_artifacts(session_id, kind) SELECT id, 'security_report' FROM agent_sessions`)
				if err == nil {
					t.Fatal("retired artifact kind is still accepted")
				}
			}
			if err := Migrate(ctx, pool); err != nil {
				t.Fatalf("repeat migration: %v", err)
			}
		})
	}
}
