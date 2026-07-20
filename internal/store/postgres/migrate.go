package postgres

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/001_initial_schema.up.sql
var migration001Up string

//go:embed migrations/005_pending_actions.up.sql
var migration005Up string

//go:embed migrations/006_add_memory.up.sql
var migration006Up string

//go:embed migrations/007_resource_ownership.up.sql
var migration007Up string

//go:embed migrations/008_resource_shares.up.sql
var migration008Up string

//go:embed migrations/009_notifications.up.sql
var migration009Up string

//go:embed migrations/010_pending_input_type.up.sql
var migration010Up string

//go:embed migrations/011_auth_integration.up.sql
var migration011Up string

//go:embed migrations/012_remove_stage.up.sql
var migration012Up string

//go:embed migrations/013_user_chat_settings.up.sql
var migration013Up string

//go:embed migrations/014_user_chat_auth_mode.up.sql
var migration014Up string

//go:embed migrations/015_project_state.up.sql
var migration015Up string

//go:embed migrations/016_user_souls.up.sql
var migration016Up string

//go:embed migrations/017_drop_user_chat_settings.up.sql
var migration017Up string

//go:embed migrations/018_user_namespaces.up.sql
var migration018Up string

//go:embed migrations/019_slack.up.sql
var migration019Up string

//go:embed migrations/020_slack_drafts_namespace.up.sql
var migration020Up string

//go:embed migrations/021_artifact_slack_reply_kind.up.sql
var migration021Up string

//go:embed migrations/022_slack_drafts_edit.up.sql
var migration022Up string

//go:embed migrations/023_slack_draft_channel_reply.up.sql
var migration023Up string

//go:embed migrations/024_drop_session_mode.up.sql
var migration024Up string

//go:embed migrations/025_session_transcripts.up.sql
var migration025Up string

//go:embed migrations/026_user_git_identity.up.sql
var migration026Up string

//go:embed migrations/027_observability_indexes.up.sql
var migration027Up string

//go:embed migrations/028_activity_events_session_id_index.up.sql
var migration028Up string

//go:embed migrations/029_unique_message_event_key.up.sql
var migration029Up string

//go:embed migrations/030_pending_request_id.up.sql
var migration030Up string

//go:embed migrations/031_user_role_models.up.sql
var migration031Up string

//go:embed migrations/032_user_last_login.up.sql
var migration032Up string

//go:embed migrations/034_activity_error_events_index.up.sql
var migration034Up string

//go:embed migrations/035_user_git_coauthor_setting.up.sql
var migration035Up string

//go:embed migrations/036_message_lifecycle.up.sql
var migration036Up string

//go:embed migrations/037_enforce_user_git_coauthor_setting.up.sql
var migration037Up string

//go:embed migrations/038_project_content.up.sql
var migration038Up string

//go:embed migrations/039_project_content_s3.up.sql
var migration039Up string

//go:embed migrations/040_observability_metric_events_index.up.sql
var migration040Up string

// noTxMigrations run statement-by-statement outside a transaction so they can
// use commands PostgreSQL forbids in transaction blocks, such as
// CREATE INDEX CONCURRENTLY (which avoids blocking writers during the build).
// If a statement fails, the version is not recorded and the migration is
// retried on the next startup — such migrations must be written idempotently
// (IF EXISTS / IF NOT EXISTS, plus cleanup of invalid leftover indexes).
var noTxMigrations = map[int]bool{40: true}

// applyNoTxMigration executes each semicolon-terminated statement of a
// migration directly on the connection (no surrounding transaction), then
// records the version. Line comments are stripped before splitting so a
// semicolon inside a comment cannot corrupt statement boundaries.
func applyNoTxMigration(ctx context.Context, conn *pgxpool.Conn, version int, sql string) error {
	for _, stmt := range strings.Split(stripSQLLineComments(sql), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("applying migration %d: %w", version, err)
		}
	}
	if _, err := conn.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
		return fmt.Errorf("recording migration %d: %w", version, err)
	}
	return nil
}

// stripSQLLineComments removes "--" line comments so blank statement chunks
// (for example a trailing comment block) are not sent to the server.
func stripSQLLineComments(sql string) string {
	lines := strings.Split(sql, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// Migrate applies all pending migrations.
// Uses a simple version table to track applied migrations.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring migration connection: %w", err)
	}
	defer conn.Release()

	const lockID int64 = 4143192210
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		return fmt.Errorf("acquiring migration advisory lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockID); err != nil && err != pgx.ErrNoRows {
			log.Printf("WARN: releasing migration advisory lock: %v", err)
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	migrations := []struct {
		version  int
		sql      string
		optional bool // optional migrations log a warning on failure instead of aborting
	}{
		{1, migration001Up, false},
		{5, migration005Up, false},
		{6, migration006Up, true}, // pgvector may not be installed; memory is gated by ENABLE_MEMORY
		{7, migration007Up, false},
		{8, migration008Up, false},
		{9, migration009Up, false},
		{10, migration010Up, false},
		{11, migration011Up, false},
		{12, migration012Up, false},
		{13, migration013Up, false},
		{14, migration014Up, false},
		{15, migration015Up, true}, // pgvector may not be installed; project state is gated by ENABLE_MEMORY
		{16, migration016Up, false},
		{17, migration017Up, false},
		{18, migration018Up, false},
		{19, migration019Up, false},
		{20, migration020Up, false},
		{21, migration021Up, false},
		{22, migration022Up, false},
		{23, migration023Up, false},
		{24, migration024Up, false},
		{25, migration025Up, false},
		{26, migration026Up, false},
		{27, migration027Up, false},
		{28, migration028Up, false},
		{29, migration029Up, false},
		{30, migration030Up, false},
		{31, migration031Up, false},
		{32, migration032Up, false},
		{34, migration034Up, false},
		{35, migration035Up, false},
		{36, migration036Up, false},
		{37, migration037Up, false},
		{38, migration038Up, false},
		{39, migration039Up, false},
		{40, migration040Up, false},
	}

	for _, m := range migrations {
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)", m.version).Scan(&exists); err != nil {
			return fmt.Errorf("checking migration %d: %w", m.version, err)
		}
		if exists {
			continue
		}

		if noTxMigrations[m.version] {
			if err := applyNoTxMigration(ctx, conn, m.version, m.sql); err != nil {
				if m.optional {
					log.Printf("WARN: skipping optional migration %d: %v", m.version, err)
					continue
				}
				return err
			}
			continue
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("beginning transaction for migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			if m.optional {
				log.Printf("WARN: skipping optional migration %d: %v", m.version, err)
				continue
			}
			return fmt.Errorf("applying migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("recording migration %d: %w", m.version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("committing migration %d: %w", m.version, err)
		}
	}

	return nil
}
