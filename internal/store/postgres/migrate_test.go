package postgres

import (
	"strings"
	"testing"
)

// Migration 040 must stay compatible with the no-transaction runner: each
// semicolon-separated chunk is executed as its own statement, and CREATE
// INDEX CONCURRENTLY must not share a statement with anything else.
func TestMigration040SplitsIntoConcurrentSafeStatements(t *testing.T) {
	if !noTxMigrations[40] {
		t.Fatal("migration 040 uses CREATE INDEX CONCURRENTLY and must be registered in noTxMigrations")
	}
	var statements []string
	for _, stmt := range strings.Split(migration040Up, ";") {
		if strings.TrimLeft(stripSQLLineComments(stmt), " \t\r\n") == "" {
			continue
		}
		statements = append(statements, stmt)
	}
	if len(statements) != 2 {
		t.Fatalf("expected 2 statements (cleanup drop + concurrent create), got %d: %q", len(statements), statements)
	}
	if !strings.Contains(statements[0], "DROP INDEX IF EXISTS") {
		t.Fatalf("first statement must clean up an invalid leftover index, got %q", statements[0])
	}
	if !strings.Contains(statements[1], "CREATE INDEX CONCURRENTLY IF NOT EXISTS") {
		t.Fatalf("second statement must build the index concurrently, got %q", statements[1])
	}
	if !strings.Contains(statements[1], "(session_id, created_at DESC, id DESC)") {
		t.Fatalf("index must lead on session_id so selections do not scan other runs' events, got %q", statements[1])
	}
}
