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

func TestMigration055Registered(t *testing.T) {
	for _, migration := range orderedMigrations() {
		if migration.version != 55 {
			continue
		}
		if migration.sql == "" || migration.sql != migration055Up {
			t.Fatal("migration 055 must carry the embedded security research SQL")
		}
		if migration.optional || noTxMigrations[55] {
			t.Fatal("migration 055 is required and must run transactionally")
		}
		return
	}
	t.Fatal("migration 055 is not registered in orderedMigrations and would never be applied")
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

func TestMigration056Registered(t *testing.T) {
	for _, migration := range orderedMigrations() {
		if migration.version != 56 {
			continue
		}
		if migration.sql == "" || migration.sql != migration056Up {
			t.Fatal("migration 056 must carry the embedded session change_seq SQL")
		}
		if migration.optional {
			t.Fatal("migration 056 adds the change_seq column the fingerprint reads and must not be optional")
		}
		if noTxMigrations[56] {
			t.Fatal("migration 056 creates plpgsql functions and must run transactionally")
		}
		return
	}
	t.Fatal("migration 056 is not registered in orderedMigrations and would never be applied")
}

func TestMigration058Registered(t *testing.T) {
	for _, migration := range orderedMigrations() {
		if migration.version != 58 {
			continue
		}
		if migration.sql == "" || migration.sql != migration058Up {
			t.Fatal("migration 058 must carry the embedded security research artifact SQL")
		}
		if migration.optional || noTxMigrations[58] {
			t.Fatal("migration 058 is required and must run transactionally")
		}
		return
	}
	t.Fatal("migration 058 is not registered in orderedMigrations and would never be applied")
}

// Every registered migration version must be unique and strictly increasing:
// a duplicate registration would re-apply DDL on startup, and an out-of-order
// entry silently changes the effective schema on fresh installs.
func TestOrderedMigrationsUniqueAndSorted(t *testing.T) {
	migrations := orderedMigrations()
	seen := make(map[int]bool, len(migrations))
	previous := 0
	for _, migration := range migrations {
		if seen[migration.version] {
			t.Fatalf("migration %d is registered more than once", migration.version)
		}
		seen[migration.version] = true
		if migration.version <= previous {
			t.Fatalf("migration %d is registered out of order (after %d)", migration.version, previous)
		}
		previous = migration.version
		if migration.sql == "" {
			t.Fatalf("migration %d carries empty SQL", migration.version)
		}
	}
}

func TestMigration063Registered(t *testing.T) {
	for _, migration := range orderedMigrations() {
		if migration.version != 63 {
			continue
		}
		if migration.sql == "" || migration.sql != migration063Up {
			t.Fatal("migration 063 must carry the embedded machine accepted_risk reset SQL")
		}
		if migration.optional || noTxMigrations[63] {
			t.Fatal("migration 063 resets findings and audits each reset; it must run transactionally and is required")
		}
		for _, marker := range []string{
			"e.detail->>'to' = 'accepted_risk'",
			"latest.actor LIKE 'secscan-%'",
			"SET status = 'triaged'",
			"accepted_risk_expires_at = NULL",
			"'migration-063'",
			"reset: machine-set accepted_risk is not a risk acceptance; see policy_disposition events",
		} {
			if !strings.Contains(migration.sql, marker) {
				t.Errorf("migration 063 is missing %q", marker)
			}
		}
		return
	}
	t.Fatal("migration 063 is not registered in orderedMigrations and would never be applied")
}

func TestMigration062Registered(t *testing.T) {
	for _, migration := range orderedMigrations() {
		if migration.version != 62 {
			continue
		}
		if migration.sql == "" || migration.sql != migration062Up {
			t.Fatal("migration 062 must carry the embedded submission lifecycle SQL")
		}
		if migration.optional || noTxMigrations[62] {
			t.Fatal("migration 062 is required and must run transactionally")
		}
		return
	}
	t.Fatal("migration 062 is not registered in orderedMigrations and would never be applied")
}
