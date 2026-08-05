package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

func TestSecuritySeverityRankSQL(t *testing.T) {
	got := securitySeverityRankSQL("severity")
	want := "CASE severity WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'medium' THEN 2 WHEN 'low' THEN 1 WHEN 'info' THEN 0 ELSE -1 END"
	if got != want {
		t.Errorf("securitySeverityRankSQL(severity) =\n%q\nwant\n%q", got, want)
	}
	if excl := securitySeverityRankSQL("EXCLUDED.severity"); !strings.HasPrefix(excl, "CASE EXCLUDED.severity ") {
		t.Errorf("securitySeverityRankSQL(EXCLUDED.severity) = %q, want CASE on that expression", excl)
	}
}

func TestSecurityFindingFilterSQL(t *testing.T) {
	tests := []struct {
		name      string
		filter    store.SecurityFindingFilter
		wantWhere string
		wantArgs  []any
	}{
		{
			name:      "empty filter still excludes duplicates",
			filter:    store.SecurityFindingFilter{},
			wantWhere: "WHERE duplicate_of IS NULL",
			wantArgs:  nil,
		},
		{
			name:      "include duplicates with no other filters",
			filter:    store.SecurityFindingFilter{IncludeDuplicates: true},
			wantWhere: "",
			wantArgs:  nil,
		},
		{
			name:      "namespace only",
			filter:    store.SecurityFindingFilter{Namespace: "default", IncludeDuplicates: true},
			wantWhere: "WHERE namespace = $1",
			wantArgs:  []any{"default"},
		},
		{
			name: "all filters number placeholders sequentially",
			filter: store.SecurityFindingFilter{
				Namespace:  "default",
				ScanName:   "nightly",
				RunName:    "nightly-1",
				Repository: "org/repo",
				Category:   "injection",
				Severity:   "high",
				Status:     "open",
				Search:     "sql",
				MinScore:   42.5,
			},
			wantWhere: "WHERE namespace = $1 AND scan_name = $2 AND run_name = $3 AND repository = $4" +
				" AND category = $5 AND severity = $6 AND status = $7" +
				" AND (title ILIKE $8 OR description ILIKE $8 OR file_path ILIKE $8)" +
				" AND score >= $9 AND duplicate_of IS NULL",
			wantArgs: []any{"default", "nightly", "nightly-1", "org/repo", "injection", "high", "open", "%sql%", 42.5},
		},
		{
			name:      "search binds one wildcard-wrapped arg",
			filter:    store.SecurityFindingFilter{Search: "token", IncludeDuplicates: true},
			wantWhere: "WHERE (title ILIKE $1 OR description ILIKE $1 OR file_path ILIKE $1)",
			wantArgs:  []any{"%token%"},
		},
		{
			name:      "search escapes ILIKE metacharacters",
			filter:    store.SecurityFindingFilter{Search: `50%_off\now`, IncludeDuplicates: true},
			wantWhere: "WHERE (title ILIKE $1 OR description ILIKE $1 OR file_path ILIKE $1)",
			wantArgs:  []any{`%50\%\_off\\now%`},
		},
		{
			name:      "zero min score is not filtered",
			filter:    store.SecurityFindingFilter{Namespace: "ns", MinScore: 0, IncludeDuplicates: true},
			wantWhere: "WHERE namespace = $1",
			wantArgs:  []any{"ns"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, args := securityFindingFilterSQL(tt.filter)
			if where != tt.wantWhere {
				t.Errorf("where =\n%q\nwant\n%q", where, tt.wantWhere)
			}
			if len(args) != len(tt.wantArgs) {
				t.Fatalf("args = %v, want %v", args, tt.wantArgs)
			}
			for i := range args {
				if args[i] != tt.wantArgs[i] {
					t.Errorf("args[%d] = %v, want %v", i, args[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestSecurityLimit(t *testing.T) {
	tests := []struct {
		limit, def, max, want int32
	}{
		{0, 200, 1000, 200},
		{-5, 200, 1000, 200},
		{50, 200, 1000, 50},
		{1000, 200, 1000, 1000},
		{5000, 200, 1000, 1000},
	}
	for _, tt := range tests {
		if got := securityLimit(tt.limit, tt.def, tt.max); got != tt.want {
			t.Errorf("securityLimit(%d, %d, %d) = %d, want %d", tt.limit, tt.def, tt.max, got, tt.want)
		}
	}
}

func TestValidSecurityFindingStatus(t *testing.T) {
	for _, s := range []string{"open", "triaged", "confirmed", "false_positive", "fixed", "accepted_risk"} {
		if !store.ValidSecurityFindingStatus(s) {
			t.Errorf("ValidSecurityFindingStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "OPEN", "closed", "wontfix"} {
		if store.ValidSecurityFindingStatus(s) {
			t.Errorf("ValidSecurityFindingStatus(%q) = true, want false", s)
		}
	}
}

func TestEscapeSecurityLike(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"50%", `50\%`},
		{"a_b", `a\_b`},
		{`back\slash`, `back\\slash`},
		{`%_\`, `\%\_\\`},
	}
	for _, tt := range tests {
		if got := escapeSecurityLike(tt.in); got != tt.want {
			t.Errorf("escapeSecurityLike(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSecurityStatementsScopeByNamespace(t *testing.T) {
	stmts := map[string]string{
		"getSecurityFindingSQL":           getSecurityFindingSQL,
		"setSecurityFindingStatusSQL":     setSecurityFindingStatusSQL,
		"listSecurityFindingEventsSQL":    listSecurityFindingEventsSQL,
		"addSecurityFindingCommentSQL":    addSecurityFindingCommentSQL,
		"deleteSecurityFindingsByScanSQL": deleteSecurityFindingsByScanSQL,
		"deleteSecurityScansByScanSQL":    deleteSecurityScansByScanSQL,
	}
	for name, sql := range stmts {
		if !strings.Contains(sql, "namespace = $1") {
			t.Errorf("%s does not bind a namespace predicate:\n%s", name, sql)
		}
	}
}

func TestSecurityEmptyNamespaceRejected(t *testing.T) {
	s := &Store{}
	ctx := context.Background()
	id := uuid.New()
	if _, err := s.GetSecurityFinding(ctx, "", id); err == nil {
		t.Error("GetSecurityFinding(empty namespace) = nil error, want error")
	}
	if err := s.SetSecurityFindingStatus(ctx, "", id, store.SecurityFindingStatusOpen, "a", ""); err == nil {
		t.Error("SetSecurityFindingStatus(empty namespace) = nil error, want error")
	}
	if _, err := s.ListSecurityFindingEvents(ctx, "", id, 10); err == nil {
		t.Error("ListSecurityFindingEvents(empty namespace) = nil error, want error")
	}
	if _, err := s.AddSecurityFindingComment(ctx, "", id, "a", "body"); err == nil {
		t.Error("AddSecurityFindingComment(empty namespace) = nil error, want error")
	}
	if err := s.DeleteSecurityScanData(ctx, "", "scan"); err == nil {
		t.Error("DeleteSecurityScanData(empty namespace) = nil error, want error")
	}
}

func TestSetSecurityFindingStatusRejectsInvalidStatus(t *testing.T) {
	s := &Store{}
	for _, status := range []string{"", "bogus", "OPEN"} {
		if err := s.SetSecurityFindingStatus(context.Background(), "default", uuid.New(), status, "a", ""); err == nil {
			t.Errorf("SetSecurityFindingStatus(%q) = nil error, want validation error", status)
		}
	}
}

func TestSecurityFindingSummaryKeys(t *testing.T) {
	summary := newSecurityFindingSummary()
	for _, key := range []string{"total", "open", "open_critical", "open_high", "open_medium", "open_low", "open_info"} {
		if _, ok := summary[key]; !ok {
			t.Errorf("newSecurityFindingSummary missing key %q", key)
		}
	}
	addSecurityFindingSummaryCount(summary, "high", store.SecurityFindingStatusOpen, 2)
	addSecurityFindingSummaryCount(summary, "high", store.SecurityFindingStatusConfirmed, 1)
	addSecurityFindingSummaryCount(summary, "low", store.SecurityFindingStatusOpen, 1)
	want := map[string]int32{"total": 4, "open": 3, "high": 3, "low": 1, "open_high": 2, "open_low": 1, "open_critical": 0, "open_medium": 0, "open_info": 0}
	for key, val := range want {
		if summary[key] != val {
			t.Errorf("summary[%q] = %d, want %d", key, summary[key], val)
		}
	}
}

func setupSecurityTestStore(t *testing.T) *Store {
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
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("running migrations: %v", err)
	}
	for _, table := range []string{"security_finding_events", "security_findings", "security_scans"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("cleaning table %s: %v", table, err)
		}
	}
	return NewFromPool(pool)
}

func TestSecurityFindingStoreLifecycle(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	scan := lifecycleTestScanUpserts(ctx, t, s)
	finding, other := lifecycleTestFindingUpserts(ctx, t, s, scan)
	lifecycleTestStatusTransitions(ctx, t, s, scan, finding)
	lifecycleTestQueriesAndSummaries(ctx, t, s, finding)
	lifecycleTestComments(ctx, t, s, finding)
	lifecycleTestScanDeletion(ctx, t, s, finding, other)
}

func lifecycleTestComments(ctx context.Context, t *testing.T, s *Store, finding *store.SecurityFindingRecord) {
	t.Helper()

	if _, err := s.AddSecurityFindingComment(ctx, "default", uuid.New(), "alice", "hi"); !errors.Is(err, store.ErrSecurityFindingNotFound) {
		t.Errorf("AddSecurityFindingComment(missing) = %v, want ErrSecurityFindingNotFound", err)
	}
	if _, err := s.AddSecurityFindingComment(ctx, "other-ns", finding.ID, "alice", "hi"); !errors.Is(err, store.ErrSecurityFindingNotFound) {
		t.Errorf("AddSecurityFindingComment(wrong namespace) = %v, want ErrSecurityFindingNotFound", err)
	}

	event, err := s.AddSecurityFindingComment(ctx, "default", finding.ID, "alice", "needs an exploit review")
	if err != nil {
		t.Fatalf("AddSecurityFindingComment: %v", err)
	}
	if event.EventType != "comment" || event.Actor != "alice" || event.Note != "needs an exploit review" ||
		event.FindingID != finding.ID || event.ID == 0 || event.CreatedAt.IsZero() {
		t.Errorf("comment event = %+v", event)
	}

	events, err := s.ListSecurityFindingEvents(ctx, "default", finding.ID, 0)
	if err != nil || len(events) != 4 {
		t.Fatalf("events after comment = %d, %v, want 4", len(events), err)
	}
	if events[0].EventType != "comment" || events[0].Note != "needs an exploit review" {
		t.Errorf("newest event = %+v, want the comment", events[0])
	}
}

func lifecycleTestScanUpserts(ctx context.Context, t *testing.T, s *Store) *store.SecurityScanRecord {
	t.Helper()

	missing, err := s.GetSecurityScan(ctx, "default", "no-such-run")
	if err != nil || missing != nil {
		t.Fatalf("GetSecurityScan(missing) = %v, %v, want nil, nil", missing, err)
	}

	started := time.Now().UTC().Truncate(time.Second)
	scan, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
		Repository: "org/repo", Revision: "abc123", StartedAt: &started,
	})
	if err != nil {
		t.Fatalf("UpsertSecurityScan: %v", err)
	}
	if scan.Status != "running" {
		t.Errorf("scan.Status = %q, want running", scan.Status)
	}

	scan2, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
		Repository: "org/repo", Revision: "abc123", Status: "completed",
		Counts: map[string]int32{"high": 1, "total": 1},
	})
	if err != nil {
		t.Fatalf("UpsertSecurityScan(update): %v", err)
	}
	if scan2.ID != scan.ID {
		t.Errorf("upsert created a new scan row: %s != %s", scan2.ID, scan.ID)
	}
	if scan2.Status != "completed" || scan2.Counts["high"] != 1 {
		t.Errorf("scan2 = status %q counts %v, want completed / high=1", scan2.Status, scan2.Counts)
	}

	scans, err := s.ListSecurityScans(ctx, "default", "nightly", 0)
	if err != nil || len(scans) != 1 {
		t.Fatalf("ListSecurityScans = %d scans, %v, want 1, nil", len(scans), err)
	}
	return scan
}

func lifecycleTestFindingUpserts(ctx context.Context, t *testing.T, s *Store, scan *store.SecurityScanRecord) (finding, other *store.SecurityFindingRecord) {
	t.Helper()

	finding, created, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
		Fingerprint: "fp-1", Title: "SQL injection", Category: "injection",
		Severity: "medium", Repository: "org/repo", FilePath: "db/query.go",
		StartLine: 10, EndLine: 12, CWE: []string{"CWE-89"}, Score: 40,
		Raw: json.RawMessage(`{"k":"v"}`),
	})
	if err != nil {
		t.Fatalf("UpsertSecurityFinding: %v", err)
	}
	if !created {
		t.Error("first upsert: created = false, want true")
	}
	if finding.Status != "open" || finding.Occurrences != 1 || finding.Confidence != "tentative" {
		t.Errorf("finding defaults = status %q occ %d conf %q", finding.Status, finding.Occurrences, finding.Confidence)
	}

	merged, created, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: "default", ScanName: "nightly", RunName: "nightly-2",
		Fingerprint: "fp-1", Title: "SQL injection (updated)", Category: "injection",
		Severity: "high", Confidence: "firm", Repository: "org/repo", FilePath: "db/query.go",
		Score: 30, SourceAgent: "scanner",
	})
	if err != nil {
		t.Fatalf("UpsertSecurityFinding(merge): %v", err)
	}
	if created {
		t.Error("merge upsert: created = true, want false")
	}
	if merged.ID != finding.ID || merged.Occurrences != 2 {
		t.Errorf("merged = id %s occ %d, want id %s occ 2", merged.ID, merged.Occurrences, finding.ID)
	}
	if merged.Severity != "high" || merged.Score != 40 {
		t.Errorf("merged kept severity %q score %v, want highest high / 40", merged.Severity, merged.Score)
	}
	if merged.RunName != "nightly-2" || merged.Title != "SQL injection (updated)" {
		t.Errorf("merge did not refresh mutable fields: run %q title %q", merged.RunName, merged.Title)
	}

	events, err := s.ListSecurityFindingEvents(ctx, "default", finding.ID, 0)
	if err != nil || len(events) != 1 || events[0].EventType != "reobserved" {
		t.Fatalf("events after merge = %v, %v, want one reobserved event", events, err)
	}

	weeklyScan, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace: "default", ScanName: "weekly", RunName: "weekly-1", Repository: "org/repo",
	})
	if err != nil {
		t.Fatalf("UpsertSecurityScan(weekly): %v", err)
	}
	other, created, err = s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: weeklyScan.ID, Namespace: "default", ScanName: "weekly", RunName: "weekly-1",
		Fingerprint: "fp-1", Title: "SQL injection", Category: "injection",
		Severity: "medium", Repository: "org/repo", Score: 10,
	})
	if err != nil {
		t.Fatalf("UpsertSecurityFinding(other scan): %v", err)
	}
	if !created || other.ID == finding.ID {
		t.Errorf("same fingerprint under a different scan_name = created %v id %s, want new row", created, other.ID)
	}
	return finding, other
}

func lifecycleTestStatusTransitions(ctx context.Context, t *testing.T, s *Store, scan *store.SecurityScanRecord, finding *store.SecurityFindingRecord) {
	t.Helper()

	if err := s.SetSecurityFindingStatus(ctx, "default", finding.ID, "bogus", "alice", ""); err == nil {
		t.Error("SetSecurityFindingStatus(bogus) = nil error, want validation error")
	}
	if err := s.SetSecurityFindingStatus(ctx, "default", uuid.New(), store.SecurityFindingStatusTriaged, "alice", ""); !errors.Is(err, store.ErrSecurityFindingNotFound) {
		t.Errorf("SetSecurityFindingStatus(missing) = %v, want ErrSecurityFindingNotFound", err)
	}
	if err := s.SetSecurityFindingStatus(ctx, "other-ns", finding.ID, store.SecurityFindingStatusTriaged, "alice", ""); !errors.Is(err, store.ErrSecurityFindingNotFound) {
		t.Errorf("SetSecurityFindingStatus(wrong namespace) = %v, want ErrSecurityFindingNotFound", err)
	}
	if err := s.SetSecurityFindingStatus(ctx, "default", finding.ID, store.SecurityFindingStatusConfirmed, "alice", "verified"); err != nil {
		t.Fatalf("SetSecurityFindingStatus: %v", err)
	}

	// Triage status must survive re-observation of the same finding.
	reobserved, created, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: "default", ScanName: "nightly", RunName: "nightly-3",
		Fingerprint: "fp-1", Title: "SQL injection", Category: "injection",
		Severity: "high", Repository: "org/repo", Score: 30,
	})
	if err != nil || created {
		t.Fatalf("UpsertSecurityFinding(reobserve) = created %v, %v, want merge", created, err)
	}
	if reobserved.Status != "confirmed" {
		t.Errorf("reobserved.Status = %q, want confirmed to survive re-observation", reobserved.Status)
	}
}

func lifecycleTestQueriesAndSummaries(ctx context.Context, t *testing.T, s *Store, finding *store.SecurityFindingRecord) {
	t.Helper()

	got, err := s.GetSecurityFinding(ctx, "default", finding.ID)
	if err != nil || got == nil || got.Status != "confirmed" {
		t.Fatalf("GetSecurityFinding after status = %+v, %v, want status confirmed", got, err)
	}
	if crossNS, err := s.GetSecurityFinding(ctx, "other-ns", finding.ID); err != nil || crossNS != nil {
		t.Fatalf("GetSecurityFinding(wrong namespace) = %v, %v, want nil, nil", crossNS, err)
	}
	if crossEvents, err := s.ListSecurityFindingEvents(ctx, "other-ns", finding.ID, 0); err != nil || len(crossEvents) != 0 {
		t.Fatalf("ListSecurityFindingEvents(wrong namespace) = %v, %v, want none", crossEvents, err)
	}
	events, err := s.ListSecurityFindingEvents(ctx, "default", finding.ID, 0)
	if err != nil || len(events) != 3 || events[1].EventType != "status_changed" || events[1].Actor != "alice" {
		t.Fatalf("events after status = %v, %v, want status_changed second-newest", events, err)
	}

	listed, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", ScanName: "nightly", Severity: "high", Search: "injection"})
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListSecurityFindings = %d findings, %v, want 1, nil", len(listed), err)
	}
	listed, err = s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", MinScore: 90})
	if err != nil || len(listed) != 0 {
		t.Fatalf("ListSecurityFindings(min score 90) = %d findings, %v, want 0, nil", len(listed), err)
	}

	summary, err := s.SummarizeSecurityFindings(ctx, "default", "nightly", "")
	if err != nil {
		t.Fatalf("SummarizeSecurityFindings: %v", err)
	}
	if summary["high"] != 1 || summary["total"] != 1 || summary["open"] != 0 || summary["open_high"] != 0 {
		t.Errorf("summary = %v, want high=1 total=1 open=0 open_high=0", summary)
	}
	summary, err = s.SummarizeSecurityFindings(ctx, "default", "weekly", "")
	if err != nil {
		t.Fatalf("SummarizeSecurityFindings(weekly): %v", err)
	}
	if summary["medium"] != 1 || summary["open"] != 1 || summary["open_medium"] != 1 {
		t.Errorf("summary(weekly) = %v, want medium=1 open=1 open_medium=1", summary)
	}
}

func lifecycleTestScanDeletion(ctx context.Context, t *testing.T, s *Store, finding, other *store.SecurityFindingRecord) {
	t.Helper()

	if err := s.DeleteSecurityScanData(ctx, "default", "nightly"); err != nil {
		t.Fatalf("DeleteSecurityScanData: %v", err)
	}
	if err := s.DeleteSecurityScanData(ctx, "default", "nightly"); err != nil {
		t.Fatalf("DeleteSecurityScanData(repeat) not idempotent: %v", err)
	}
	got, err := s.GetSecurityFinding(ctx, "default", finding.ID)
	if err != nil || got != nil {
		t.Fatalf("GetSecurityFinding after delete = %v, %v, want nil, nil", got, err)
	}
	gone, err := s.GetSecurityScan(ctx, "default", "nightly-1")
	if err != nil || gone != nil {
		t.Fatalf("GetSecurityScan after delete = %v, %v, want nil, nil", gone, err)
	}
	if kept, err := s.GetSecurityFinding(ctx, "default", other.ID); err != nil || kept == nil {
		t.Fatalf("GetSecurityFinding(other scan) after delete = %v, %v, want kept", kept, err)
	}

	if _, err := s.GetSecurityFinding(ctx, "default", uuid.New()); err != nil {
		t.Fatalf("GetSecurityFinding(random) = %v, want nil error", err)
	}
}
