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
