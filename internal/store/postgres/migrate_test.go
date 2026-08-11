package postgres

import (
	"strings"
	"testing"
)

// splitNoTxStatements mirrors applyNoTxMigration's statement splitting:
// comments are stripped first so semicolons inside comment text cannot
// corrupt statement boundaries.
func splitNoTxStatements(sql string) []string {
	var statements []string
	for _, stmt := range strings.Split(stripSQLLineComments(sql), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		statements = append(statements, stmt)
	}
	return statements
}

// A semicolon inside a comment must not produce a bogus executable chunk —
// that would make Migrate fail on startup for every deployment missing the
// migration.
func TestNoTxStatementSplittingIgnoresCommentSemicolons(t *testing.T) {
	got := splitNoTxStatements("-- comment with a semicolon; still comment\nSELECT 1;\n-- trailing note\n")
	if len(got) != 1 || got[0] != "SELECT 1" {
		t.Fatalf("splitNoTxStatements() = %q, want [\"SELECT 1\"]", got)
	}
}

// Migration 040 must stay compatible with the no-transaction runner: each
// statement runs on its own, and both the cleanup and the build must be
// CONCURRENTLY so neither blocks worker event writes.
func TestMigration040SplitsIntoConcurrentSafeStatements(t *testing.T) {
	if !noTxMigrations[40] {
		t.Fatal("migration 040 uses CREATE INDEX CONCURRENTLY and must be registered in noTxMigrations")
	}
	statements := splitNoTxStatements(migration040Up)
	if len(statements) != 2 {
		t.Fatalf("expected 2 statements (cleanup drop + concurrent create), got %d: %q", len(statements), statements)
	}
	if !strings.HasPrefix(statements[0], "DROP INDEX CONCURRENTLY IF EXISTS") {
		t.Fatalf("cleanup must drop concurrently to avoid blocking writers, got %q", statements[0])
	}
	if !strings.HasPrefix(statements[1], "CREATE INDEX CONCURRENTLY IF NOT EXISTS") {
		t.Fatalf("build must create the index concurrently, got %q", statements[1])
	}
	if !strings.Contains(statements[1], "(session_id, created_at DESC, id DESC)") {
		t.Fatalf("index must lead on session_id so selections do not scan other runs' events, got %q", statements[1])
	}
}

// Migration 049 adds the execution scope columns the security finding store
// selects, so it must be registered in the migration list — an unregistered
// migration never runs and every finding query fails on missing columns. It
// builds its index CONCURRENTLY, so it also belongs in noTxMigrations and
// each statement must be retry-safe.
func TestMigration049RegisteredAndConcurrentSafe(t *testing.T) {
	var found *schemaMigration
	for i, m := range orderedMigrations() {
		if m.version == 49 {
			found = &orderedMigrations()[i]
		}
	}
	if found == nil {
		t.Fatal("migration 049 is not registered in orderedMigrations and would never be applied")
	}
	if found.sql != migration049Up || found.sql == "" {
		t.Fatal("migration 049 must carry the embedded 049 SQL")
	}
	if found.optional {
		t.Fatal("migration 049 adds columns the finding store selects and must not be optional")
	}
	if !noTxMigrations[49] {
		t.Fatal("migration 049 uses CREATE INDEX CONCURRENTLY and must be registered in noTxMigrations")
	}
	statements := splitNoTxStatements(migration049Up)
	if len(statements) != 3 {
		t.Fatalf("expected 3 statements (add columns + cleanup drop + concurrent create), got %d: %q", len(statements), statements)
	}
	if !strings.HasPrefix(statements[0], "ALTER TABLE security_findings") ||
		strings.Count(statements[0], "ADD COLUMN IF NOT EXISTS") != 2 {
		t.Fatalf("column additions must be idempotent, got %q", statements[0])
	}
	if !strings.HasPrefix(statements[1], "DROP INDEX CONCURRENTLY IF EXISTS") {
		t.Fatalf("cleanup must drop concurrently so a retry after an invalid build succeeds, got %q", statements[1])
	}
	if !strings.HasPrefix(statements[2], "CREATE INDEX CONCURRENTLY IF NOT EXISTS") {
		t.Fatalf("build must create the index concurrently so it does not block finding writes, got %q", statements[2])
	}
	if !strings.Contains(statements[2], "(namespace, scan_name, execution_id)") {
		t.Fatalf("index must cover the execution scope lookup, got %q", statements[2])
	}
}

func TestMigration050Registered(t *testing.T) {
	for _, migration := range orderedMigrations() {
		if migration.version != 50 {
			continue
		}
		if migration.sql == "" || migration.sql != migration050Up {
			t.Fatal("migration 050 must carry the embedded security finding artifacts SQL")
		}
		if migration.optional {
			t.Fatal("migration 050 creates a table required by bounty artifact tools and must not be optional")
		}
		return
	}
	t.Fatal("migration 050 is not registered in orderedMigrations and would never be applied")
}
