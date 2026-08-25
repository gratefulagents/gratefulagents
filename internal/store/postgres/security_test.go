package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
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
			wantWhere: "WHERE suppressed_by IS NULL AND duplicate_of IS NULL",
			wantArgs:  nil,
		},
		{
			name:      "include duplicates with no other filters",
			filter:    store.SecurityFindingFilter{IncludeDuplicates: true},
			wantWhere: "WHERE suppressed_by IS NULL",
			wantArgs:  nil,
		},
		{
			name:      "namespace only",
			filter:    store.SecurityFindingFilter{Namespace: "default", IncludeDuplicates: true},
			wantWhere: "WHERE namespace = $1 AND suppressed_by IS NULL",
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
				" AND score >= $9 AND suppressed_by IS NULL AND duplicate_of IS NULL",
			wantArgs: []any{"default", "nightly", "nightly-1", "org/repo", "injection", "high", "open", "%sql%", 42.5},
		},
		{
			name:      "persisted scan id scopes sibling task runs",
			filter:    store.SecurityFindingFilter{Namespace: "ns", ScanID: uuid.MustParse("11111111-1111-1111-1111-111111111111")},
			wantWhere: "WHERE namespace = $1 AND scan_id = $2 AND suppressed_by IS NULL AND duplicate_of IS NULL",
			wantArgs:  []any{"ns", uuid.MustParse("11111111-1111-1111-1111-111111111111")},
		},
		{
			name:      "actionable status groups remediation states",
			filter:    store.SecurityFindingFilter{Namespace: "ns", Status: store.SecurityFindingStatusActionable, IncludeDuplicates: true},
			wantWhere: "WHERE namespace = $1 AND status = ANY($2) AND suppressed_by IS NULL",
			wantArgs: []any{"ns", []string{
				store.SecurityFindingStatusOpen,
				store.SecurityFindingStatusTriaged,
				store.SecurityFindingStatusConfirmed,
			}},
		},
		{
			name:      "search binds one wildcard-wrapped arg",
			filter:    store.SecurityFindingFilter{Search: "token", IncludeDuplicates: true},
			wantWhere: "WHERE (title ILIKE $1 OR description ILIKE $1 OR file_path ILIKE $1) AND suppressed_by IS NULL",
			wantArgs:  []any{"%token%"},
		},
		{
			name:      "search escapes ILIKE metacharacters",
			filter:    store.SecurityFindingFilter{Search: `50%_off\now`, IncludeDuplicates: true},
			wantWhere: "WHERE (title ILIKE $1 OR description ILIKE $1 OR file_path ILIKE $1) AND suppressed_by IS NULL",
			wantArgs:  []any{`%50\%\_off\\now%`},
		},
		{
			name:      "suppressed include drops the suppression condition",
			filter:    store.SecurityFindingFilter{Namespace: "ns", Suppressed: store.SecuritySuppressedInclude, IncludeDuplicates: true},
			wantWhere: "WHERE namespace = $1",
			wantArgs:  []any{"ns"},
		},
		{
			name:      "suppressed only selects only suppressed findings",
			filter:    store.SecurityFindingFilter{Namespace: "ns", Suppressed: store.SecuritySuppressedOnly, IncludeDuplicates: true},
			wantWhere: "WHERE namespace = $1 AND suppressed_by IS NOT NULL",
			wantArgs:  []any{"ns"},
		},
		{
			name:      "suppressed exclude matches the default",
			filter:    store.SecurityFindingFilter{Namespace: "ns", Suppressed: store.SecuritySuppressedExclude, IncludeDuplicates: true},
			wantWhere: "WHERE namespace = $1 AND suppressed_by IS NULL",
			wantArgs:  []any{"ns"},
		},
		{
			name:      "zero min score is not filtered",
			filter:    store.SecurityFindingFilter{Namespace: "ns", MinScore: 0, IncludeDuplicates: true},
			wantWhere: "WHERE namespace = $1 AND suppressed_by IS NULL",
			wantArgs:  []any{"ns"},
		},
		{
			name:      "excluded scan names bind one array arg",
			filter:    store.SecurityFindingFilter{Namespace: "ns", ExcludedScanNames: []string{"a", "b"}, IncludeDuplicates: true},
			wantWhere: "WHERE namespace = $1 AND NOT (scan_name = ANY($2)) AND suppressed_by IS NULL",
			wantArgs:  []any{"ns", []string{"a", "b"}},
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
				if !reflect.DeepEqual(args[i], tt.wantArgs[i]) {
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
	if err := s.SetSecurityFindingStatus(ctx, "", id, store.SecurityFindingStatusOpen, "a", "", nil); err == nil {
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
		if err := s.SetSecurityFindingStatus(context.Background(), "default", uuid.New(), status, "a", "", nil); err == nil {
			t.Errorf("SetSecurityFindingStatus(%q) = nil error, want validation error", status)
		}
	}
}

func TestSecurityFindingSummaryKeys(t *testing.T) {
	summary := newSecurityFindingSummary()
	for _, key := range []string{"total", "open", "actionable", "suppressed", "open_critical", "open_high", "open_medium", "open_low", "open_info", "actionable_critical", "actionable_high", "actionable_medium", "actionable_low", "actionable_info", "source_agent", "source_scanner", "correlated"} {
		if _, ok := summary[key]; !ok {
			t.Errorf("newSecurityFindingSummary missing key %q", key)
		}
	}
	addSecurityFindingSummaryCount(summary, "high", store.SecurityFindingStatusOpen, store.SecurityFindingBaselineNew, "agent", false, false, false, 2)
	addSecurityFindingSummaryCount(summary, "high", store.SecurityFindingStatusConfirmed, store.SecurityFindingBaselineRecurring, "scanner", true, false, false, 1)
	addSecurityFindingSummaryCount(summary, "low", store.SecurityFindingStatusOpen, "", "", true, false, false, 1)
	// Suppressed groups only bump "suppressed" when not included.
	addSecurityFindingSummaryCount(summary, "critical", store.SecurityFindingStatusOpen, store.SecurityFindingBaselineNew, "scanner", false, true, false, 3)
	want := map[string]int32{"total": 4, "open": 3, "high": 3, "low": 1, "open_high": 2, "open_low": 1, "open_critical": 0, "open_medium": 0, "open_info": 0, "baseline_new": 2, "baseline_recurring": 1, "baseline_tracked": 3, "suppressed": 3, "critical": 0, "source_agent": 3, "source_scanner": 1, "correlated": 2}
	for key, val := range want {
		if summary[key] != val {
			t.Errorf("summary[%q] = %d, want %d", key, summary[key], val)
		}
	}
	if summary["actionable"] != 4 || summary["actionable_high"] != 3 || summary["actionable_low"] != 1 {
		t.Errorf("actionable summary = %+v, want open, triaged, and confirmed findings grouped by severity", summary)
	}
	addSecurityFindingSummaryCount(summary, "critical", store.SecurityFindingStatusFalsePositive, "", "agent", false, false, false, 1)
	if summary["actionable"] != 4 || summary["actionable_critical"] != 0 {
		t.Errorf("false positive changed actionable summary: %+v", summary)
	}
}

func TestActionableFindingSummaryStatuses(t *testing.T) {
	summary := newSecurityFindingSummary()
	for _, status := range []string{
		store.SecurityFindingStatusOpen,
		store.SecurityFindingStatusTriaged,
		store.SecurityFindingStatusConfirmed,
		store.SecurityFindingStatusFalsePositive,
		store.SecurityFindingStatusFixed,
		store.SecurityFindingStatusAcceptedRisk,
	} {
		addSecurityFindingSummaryCount(summary, "high", status, "", "agent", false, false, false, 1)
	}
	if got := summary["actionable"]; got != 3 {
		t.Errorf("actionable = %d, want 3", got)
	}
	if got := summary["actionable_high"]; got != 3 {
		t.Errorf("actionable_high = %d, want 3", got)
	}
	if got := summary["total"]; got != 6 {
		t.Errorf("total = %d, want 6", got)
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
	for _, table := range []string{"security_finding_events", "security_finding_observations", "security_notification_markers", "security_findings", "security_saved_filters", "security_scans"} {
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

func TestSecurityFindingEmptyConfidenceDefaultsWithoutMutatingInput(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()
	scan, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace: "default", ScanName: "nightly", RunName: "nightly-1", Repository: "org/repo",
	})
	if err != nil {
		t.Fatalf("UpsertSecurityScan: %v", err)
	}

	insert := &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
		Fingerprint: "empty-confidence", Title: "Finding", Category: "other",
		Severity: "medium", Repository: "org/repo",
	}
	createdFinding, created, err := s.UpsertSecurityFinding(ctx, insert)
	if err != nil || !created {
		t.Fatalf("insert: created=%v err=%v", created, err)
	}
	if createdFinding.Confidence != "tentative" {
		t.Errorf("inserted confidence = %q, want tentative", createdFinding.Confidence)
	}
	if insert.Confidence != "" {
		t.Errorf("insert input confidence = %q, want unchanged empty value", insert.Confidence)
	}

	reobservation := &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: "default", ScanName: "nightly", RunName: "nightly-2",
		Fingerprint: "empty-confidence", Title: "Finding again", Category: "other",
		Severity: "medium", Repository: "org/repo",
	}
	merged, created, err := s.UpsertSecurityFinding(ctx, reobservation)
	if err != nil || created {
		t.Fatalf("reobservation: created=%v err=%v", created, err)
	}
	if merged.Confidence != "tentative" {
		t.Errorf("reobserved confidence = %q, want tentative", merged.Confidence)
	}
	if reobservation.Confidence != "" {
		t.Errorf("reobservation input confidence = %q, want unchanged empty value", reobservation.Confidence)
	}
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

	policy, err := s.RecordSecurityFindingPolicyDisposition(ctx, "default", finding.ID, "policy-run", "exec-1", "known_issue", "matches audit A-1")
	if err != nil {
		t.Fatalf("RecordSecurityFindingPolicyDisposition: %v", err)
	}
	if policy.EventType != "policy_disposition" || policy.Actor != "policy-run" || policy.Note != "matches audit A-1" {
		t.Errorf("policy event = %+v", policy)
	}
	if got := store.SecurityFindingBlockingPolicyDisposition([]store.SecurityFindingEvent{*policy}, "exec-1"); got != "known_issue" {
		t.Errorf("blocking disposition = %q, want known_issue", got)
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

	scans, err := s.ListSecurityScans(ctx, "default", "nightly", 0, nil)
	if err != nil || len(scans) != 1 {
		t.Fatalf("ListSecurityScans = %d scans, %v, want 1, nil", len(scans), err)
	}
	if excluded, err := s.ListSecurityScans(ctx, "default", "", 0, []string{"nightly"}); err != nil || len(excluded) != 0 {
		t.Fatalf("ListSecurityScans(excluded) = %d scans, %v, want 0, nil", len(excluded), err)
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

	if err := s.SetSecurityFindingStatus(ctx, "default", finding.ID, "bogus", "alice", "", nil); err == nil {
		t.Error("SetSecurityFindingStatus(bogus) = nil error, want validation error")
	}
	if err := s.SetSecurityFindingStatus(ctx, "default", uuid.New(), store.SecurityFindingStatusTriaged, "alice", "", nil); !errors.Is(err, store.ErrSecurityFindingNotFound) {
		t.Errorf("SetSecurityFindingStatus(missing) = %v, want ErrSecurityFindingNotFound", err)
	}
	if err := s.SetSecurityFindingStatus(ctx, "other-ns", finding.ID, store.SecurityFindingStatusTriaged, "alice", "", nil); !errors.Is(err, store.ErrSecurityFindingNotFound) {
		t.Errorf("SetSecurityFindingStatus(wrong namespace) = %v, want ErrSecurityFindingNotFound", err)
	}
	if err := s.SetSecurityFindingStatus(ctx, "default", finding.ID, store.SecurityFindingStatusConfirmed, "alice", "verified", nil); err != nil {
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

	summary, err := s.SummarizeSecurityFindings(ctx, "default", "nightly", "", false)
	if err != nil {
		t.Fatalf("SummarizeSecurityFindings: %v", err)
	}
	if summary["high"] != 1 || summary["total"] != 1 || summary["open"] != 0 || summary["open_high"] != 0 {
		t.Errorf("summary = %v, want high=1 total=1 open=0 open_high=0", summary)
	}
	summary, err = s.SummarizeSecurityFindings(ctx, "default", "weekly", "", false)
	if err != nil {
		t.Fatalf("SummarizeSecurityFindings(weekly): %v", err)
	}
	if summary["medium"] != 1 || summary["open"] != 1 || summary["open_medium"] != 1 {
		t.Errorf("summary(weekly) = %v, want medium=1 open=1 open_medium=1", summary)
	}

	excluded, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", ExcludedScanNames: []string{"nightly"}})
	if err != nil || len(excluded) != 1 || excluded[0].ScanName != "weekly" {
		t.Fatalf("ListSecurityFindings(excluding nightly) = %d findings, %v, want the weekly finding only", len(excluded), err)
	}
	summary, err = s.SummarizeSecurityFindingsScoped(ctx, store.SecurityFindingSummaryScope{Namespace: "default", ExcludedScanNames: []string{"nightly"}})
	if err != nil {
		t.Fatalf("SummarizeSecurityFindingsScoped(excluding nightly): %v", err)
	}
	if summary["total"] != 1 || summary["medium"] != 1 || summary["high"] != 0 {
		t.Errorf("summary(excluding nightly) = %v, want the weekly finding only", summary)
	}
	if _, err := s.GetSecurityFindingTrends(ctx, "default", "", []string{"nightly"}); err != nil {
		t.Fatalf("GetSecurityFindingTrends(excluding nightly): %v", err)
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

func TestSecurityNotificationMarkers(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	if _, err := s.ClaimSecurityNotifications(ctx, "", "scan", "rule/slack", []string{"fp"}); err == nil {
		t.Error("ClaimSecurityNotifications(empty namespace) = nil error, want error")
	}

	claimed, err := s.ClaimSecurityNotifications(ctx, "default", "scan", "rule/slack", []string{"fp-1", "fp-2"})
	if err != nil {
		t.Fatalf("ClaimSecurityNotifications: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed = %v, want both fingerprints", claimed)
	}

	again, err := s.ClaimSecurityNotifications(ctx, "default", "scan", "rule/slack", []string{"fp-1", "fp-2", "fp-3"})
	if err != nil {
		t.Fatalf("ClaimSecurityNotifications(repeat): %v", err)
	}
	if len(again) != 1 || again[0] != "fp-3" {
		t.Fatalf("repeat claimed = %v, want only fp-3", again)
	}

	// A different rule key claims independently.
	other, err := s.ClaimSecurityNotifications(ctx, "default", "scan", "rule/github", []string{"fp-1"})
	if err != nil || len(other) != 1 {
		t.Fatalf("other-rule claim = %v, %v; want fp-1 claimed", other, err)
	}

	if err := s.ReleaseSecurityNotifications(ctx, "default", "scan", "rule/slack", []string{"fp-1"}); err != nil {
		t.Fatalf("ReleaseSecurityNotifications: %v", err)
	}
	reclaimed, err := s.ClaimSecurityNotifications(ctx, "default", "scan", "rule/slack", []string{"fp-1"})
	if err != nil || len(reclaimed) != 1 {
		t.Fatalf("reclaim after release = %v, %v; want fp-1 claimable again", reclaimed, err)
	}

	if err := s.DeleteSecurityScanData(ctx, "default", "scan"); err != nil {
		t.Fatalf("DeleteSecurityScanData: %v", err)
	}
	afterDelete, err := s.ClaimSecurityNotifications(ctx, "default", "scan", "rule/slack", []string{"fp-2"})
	if err != nil || len(afterDelete) != 1 {
		t.Fatalf("claim after scan delete = %v, %v; want markers purged", afterDelete, err)
	}
}

func assertSecurityFindingCorrelation(t *testing.T, s *Store, ctx context.Context, agent, scanner *store.SecurityFindingRecord) {
	// Correlate both ways; neither row is deleted or rewritten.
	changed, err := s.CorrelateSecurityFindings(ctx, "default", "nightly", "org/repo", "agent-fp", "scanner-fp", "same location and shared CWE", "nightly-1")
	if err != nil || !changed {
		t.Fatalf("CorrelateSecurityFindings: changed=%v err=%v", changed, err)
	}
	agentRow, err := s.GetSecurityFinding(ctx, "default", agent.ID)
	if err != nil {
		t.Fatalf("GetSecurityFinding(agent): %v", err)
	}
	scannerRow, err := s.GetSecurityFinding(ctx, "default", scanner.ID)
	if err != nil {
		t.Fatalf("GetSecurityFinding(scanner): %v", err)
	}
	if len(agentRow.CorrelatedFingerprints) != 1 || agentRow.CorrelatedFingerprints[0] != "scanner-fp" {
		t.Errorf("agent correlations = %v", agentRow.CorrelatedFingerprints)
	}
	if len(scannerRow.CorrelatedFingerprints) != 1 || scannerRow.CorrelatedFingerprints[0] != "agent-fp" {
		t.Errorf("scanner correlations = %v", scannerRow.CorrelatedFingerprints)
	}
	if agentRow.SourceKind != "agent" || scannerRow.Tool != "gosec" {
		t.Error("correlation must not rewrite provenance")
	}
	for _, side := range []struct {
		name string
		id   uuid.UUID
	}{{"agent", agent.ID}, {"scanner", scanner.ID}} {
		events, err := s.ListSecurityFindingEvents(ctx, "default", side.id, 0)
		if err != nil {
			t.Fatalf("ListSecurityFindingEvents(%s): %v", side.name, err)
		}
		found := false
		for _, ev := range events {
			if ev.EventType == "correlated" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s finding missing correlated audit event", side.name)
		}
	}

}

func TestSecurityScannerProvenanceAndCorrelation(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	scan, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
		Repository: "org/repo",
	})
	if err != nil {
		t.Fatalf("UpsertSecurityScan: %v", err)
	}

	agent, _, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
		Fingerprint: "agent-fp", Title: "Weak crypto", Category: "crypto",
		Severity: "high", Repository: "org/repo", FilePath: "crypto/hash.go",
	})
	if err != nil {
		t.Fatalf("agent upsert: %v", err)
	}
	if agent.SourceKind != "agent" {
		t.Errorf("empty SourceKind must default to agent, got %q", agent.SourceKind)
	}

	scanner, created, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
		Fingerprint: "scanner-fp", Title: "Weak crypto (G401)", Category: "crypto",
		Severity: "high", Confidence: "firm", Repository: "org/repo", FilePath: "crypto/hash.go",
		SourceKind: "scanner", Tool: "gosec", ToolVersion: "2.18.2", RuleID: "G401",
	})
	if err != nil || !created {
		t.Fatalf("scanner upsert: created=%v err=%v", created, err)
	}
	if scanner.SourceKind != "scanner" || scanner.Tool != "gosec" || scanner.ToolVersion != "2.18.2" || scanner.RuleID != "G401" {
		t.Errorf("scanner provenance = %q/%q/%q/%q", scanner.SourceKind, scanner.Tool, scanner.ToolVersion, scanner.RuleID)
	}

	assertSecurityFindingCorrelation(t, s, ctx, agent, scanner)
	// Idempotent: re-correlating changes nothing and appends no event.
	changed, err := s.CorrelateSecurityFindings(ctx, "default", "nightly", "org/repo", "scanner-fp", "agent-fp", "again", "nightly-1")
	if err != nil || changed {
		t.Errorf("re-correlate: changed=%v err=%v, want false/nil", changed, err)
	}
	agentRow, _ := s.GetSecurityFinding(ctx, "default", agent.ID)
	if len(agentRow.CorrelatedFingerprints) != 1 {
		t.Errorf("re-correlate duplicated fingerprints: %v", agentRow.CorrelatedFingerprints)
	}

	// Reobservation of the scanner finding keeps provenance AND the
	// recorded correlation.
	reobserved, created, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: "default", ScanName: "nightly", RunName: "nightly-2",
		Fingerprint: "scanner-fp", Title: "Weak crypto (G401)", Category: "crypto",
		Severity: "high", Confidence: "firm", Repository: "org/repo", FilePath: "crypto/hash.go",
		SourceKind: "scanner", Tool: "gosec", ToolVersion: "2.19.0", RuleID: "G401",
	})
	if err != nil || created {
		t.Fatalf("scanner reobservation: created=%v err=%v", created, err)
	}
	if reobserved.ToolVersion != "2.19.0" || reobserved.Tool != "gosec" || reobserved.SourceKind != "scanner" {
		t.Errorf("reobservation provenance = %q/%q/%q", reobserved.SourceKind, reobserved.Tool, reobserved.ToolVersion)
	}
	if len(reobserved.CorrelatedFingerprints) != 1 || reobserved.CorrelatedFingerprints[0] != "agent-fp" {
		t.Errorf("reobservation dropped correlation: %v", reobserved.CorrelatedFingerprints)
	}

	// Unknown fingerprints and empty namespaces fail closed.
	if _, err := s.CorrelateSecurityFindings(ctx, "default", "nightly", "org/repo", "agent-fp", "missing-fp", "r", "a"); !errors.Is(err, store.ErrSecurityFindingNotFound) {
		t.Errorf("missing fingerprint: err = %v, want ErrSecurityFindingNotFound", err)
	}
	if _, err := s.CorrelateSecurityFindings(ctx, "", "nightly", "org/repo", "agent-fp", "scanner-fp", "r", "a"); err == nil {
		t.Error("empty namespace must be rejected")
	}

	summary, err := s.SummarizeSecurityFindings(ctx, "default", "nightly", "", false)
	if err != nil {
		t.Fatalf("SummarizeSecurityFindings: %v", err)
	}
	if summary["source_agent"] != 1 || summary["source_scanner"] != 1 || summary["correlated"] != 2 {
		t.Errorf("summary = %v, want source_agent 1, source_scanner 1, correlated 2", summary)
	}
}

// upsertExecutionFinding writes one finding of an execution, creating the
// reporting run scan row on demand so fan-out siblings can report under
// different run names.
func upsertExecutionFinding(ctx context.Context, t *testing.T, s *Store, runName, executionID, taskName, fingerprint string) (*store.SecurityFindingRecord, bool, error) {
	t.Helper()
	scan, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace: "default", ScanName: "nightly", RunName: runName, Repository: "org/repo",
	})
	if err != nil {
		t.Fatalf("UpsertSecurityScan(%s): %v", runName, err)
	}
	return s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: "default", ScanName: "nightly", RunName: runName,
		ExecutionID: executionID, TaskName: taskName, Fingerprint: fingerprint,
		Title: fingerprint, Category: "injection", Severity: "high",
		Repository: "org/repo", FilePath: "db/" + fingerprint + ".go",
	})
}

func TestSecurityFindingExecutionScope(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	for _, f := range []struct{ run, execution, task, fingerprint string }{
		{"run-a", "exec-1", "task-a", "fp-a"},
		{"run-b", "exec-1", "task-b", "fp-b"},
		{"run-c", "exec-2", "task-a", "fp-c"},
	} {
		rec, created, err := upsertExecutionFinding(ctx, t, s, f.run, f.execution, f.task, f.fingerprint)
		if err != nil || !created {
			t.Fatalf("upsert %s: created=%v err=%v", f.fingerprint, created, err)
		}
		if rec.ExecutionID != f.execution || rec.TaskName != f.task {
			t.Errorf("%s stored execution/task = %q/%q, want %q/%q", f.fingerprint, rec.ExecutionID, rec.TaskName, f.execution, f.task)
		}
	}

	found, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", ExecutionID: "exec-1"})
	if err != nil || len(found) != 2 {
		t.Fatalf("list by execution = %d findings, %v, want 2", len(found), err)
	}
	fingerprints := map[string]bool{}
	for _, f := range found {
		fingerprints[f.Fingerprint] = true
	}
	if !fingerprints["fp-a"] || !fingerprints["fp-b"] {
		t.Errorf("execution filter returned %v, want fp-a and fp-b across both runs", fingerprints)
	}

	scoped, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", ExecutionID: "exec-1", TaskName: "task-b"})
	if err != nil || len(scoped) != 1 || scoped[0].Fingerprint != "fp-b" {
		t.Fatalf("list by execution+task = %v, %v, want only fp-b", scoped, err)
	}

	summary, err := s.SummarizeSecurityFindingsScoped(ctx, store.SecurityFindingSummaryScope{
		Namespace: "default", ScanName: "nightly", ExecutionID: "exec-1",
	})
	if err != nil {
		t.Fatalf("SummarizeSecurityFindingsScoped(exec-1): %v", err)
	}
	if summary["total"] != 2 || summary["high"] != 2 {
		t.Errorf("exec-1 summary = %v, want total 2 / high 2", summary)
	}

	other, err := s.SummarizeSecurityFindingsScoped(ctx, store.SecurityFindingSummaryScope{
		Namespace: "default", ScanName: "nightly", ExecutionID: "exec-2",
	})
	if err != nil {
		t.Fatalf("SummarizeSecurityFindingsScoped(exec-2): %v", err)
	}
	if other["total"] != 1 {
		t.Errorf("exec-2 summary = %v, want total 1", other)
	}
}

func TestSecurityFindingOrdinaryUpsertIsUnlimited(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	for n := range 20 {
		fp := fmt.Sprintf("fp-%d", n)
		if _, created, err := upsertExecutionFinding(ctx, t, s, "run-1", "exec-1", "task-a", fp); err != nil || !created {
			t.Fatalf("upsert %s: created=%v err=%v", fp, created, err)
		}
	}
	all, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", ExecutionID: "exec-1"})
	if err != nil || len(all) != 20 {
		t.Fatalf("findings = %d, %v, want 20", len(all), err)
	}

	merged, created, err := upsertExecutionFinding(ctx, t, s, "run-2", "exec-1", "task-b", "fp-0")
	if err != nil || created {
		t.Fatalf("duplicate upsert: created=%v err=%v, want a merge", created, err)
	}
	if merged.Occurrences != 2 || merged.RunName != "run-2" {
		t.Errorf("merged = occ %d run %q, want occ 2 run run-2", merged.Occurrences, merged.RunName)
	}
}

func TestSecurityFindingConcurrentDuplicateUpsert(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()
	scan, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace: "default", ScanName: "nightly", RunName: "run-1", Repository: "org/repo",
	})
	if err != nil {
		t.Fatalf("UpsertSecurityScan: %v", err)
	}

	const workers = 8
	results := make(chan error, workers)
	created := make(chan bool, workers)
	var wg sync.WaitGroup
	for n := range workers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, isNew, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
				ScanID: scan.ID, Namespace: "default", ScanName: "nightly", RunName: fmt.Sprintf("run-%d", n),
				ExecutionID: "exec-1", TaskName: "task-a", Fingerprint: "shared-fingerprint",
				Title: "shared", Category: "injection", Severity: "high", Repository: "org/repo",
			})
			results <- err
			created <- isNew
		}(n)
	}
	wg.Wait()
	close(results)
	close(created)

	for err := range results {
		if err != nil {
			t.Errorf("concurrent upsert: %v", err)
		}
	}
	createdCount := 0
	for isNew := range created {
		if isNew {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count = %d, want 1", createdCount)
	}
	findings, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default"})
	if err != nil || len(findings) != 1 {
		t.Fatalf("findings = %d, %v, want one merged row", len(findings), err)
	}
	if findings[0].Occurrences != workers {
		t.Errorf("occurrences = %d, want %d", findings[0].Occurrences, workers)
	}
}

func TestSecurityFindingMergeKeepsFirstAttribution(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	if _, created, err := upsertExecutionFinding(ctx, t, s, "run-1", "exec-1", "task-a", "fp-a1"); err != nil || !created {
		t.Fatalf("first task-a finding: created=%v err=%v", created, err)
	}

	merged, created, err := upsertExecutionFinding(ctx, t, s, "run-2", "exec-1", "task-b", "fp-a1")
	if err != nil || created {
		t.Fatalf("re-report from task-b: created=%v err=%v, want a merge", created, err)
	}
	if merged.ExecutionID != "exec-1" || merged.TaskName != "task-a" {
		t.Errorf("merged attribution = %q/%q, want exec-1/task-a", merged.ExecutionID, merged.TaskName)
	}
	if merged.RunName != "run-2" || merged.Occurrences != 2 {
		t.Errorf("merged = run %q occ %d, want run-2 / 2", merged.RunName, merged.Occurrences)
	}
}

// A finding recurring in a later execution belongs to THAT execution: the
// merge re-stamps execution_id (and, since the execution changed, task_name)
// so the finding shows up in the new execution's listing and report instead
// of staying invisible under the execution that first reported it.
func TestSecurityFindingMergeMovesToLaterExecution(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()
	if _, created, err := upsertExecutionFinding(ctx, t, s, "run-1", "exec-1", "task-a", "fp-a1"); err != nil || !created {
		t.Fatalf("first exec-1 finding: created=%v err=%v", created, err)
	}

	merged, created, err := upsertExecutionFinding(ctx, t, s, "run-2", "exec-2", "task-b", "fp-a1")
	if err != nil || created {
		t.Fatalf("re-report in exec-2: created=%v err=%v, want a merge", created, err)
	}
	if merged.ExecutionID != "exec-2" || merged.TaskName != "task-b" {
		t.Errorf("merged attribution = %q/%q, want exec-2/task-b", merged.ExecutionID, merged.TaskName)
	}
	if merged.Occurrences != 2 {
		t.Errorf("merged occurrences = %d, want 2", merged.Occurrences)
	}

	later, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", ExecutionID: "exec-2"})
	if err != nil || len(later) != 1 || later[0].Fingerprint != "fp-a1" {
		t.Fatalf("list by exec-2 = %v, %v, want fp-a1", later, err)
	}

	earlier, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", ExecutionID: "exec-1"})
	if err != nil || len(earlier) != 0 {
		t.Fatalf("list by exec-1 = %d findings, %v, want 0", len(earlier), err)
	}

	summary, err := s.SummarizeSecurityFindingsScoped(ctx, store.SecurityFindingSummaryScope{
		Namespace: "default", ScanName: "nightly", ExecutionID: "exec-2",
	})
	if err != nil {
		t.Fatalf("SummarizeSecurityFindingsScoped(exec-2): %v", err)
	}
	if summary["total"] != 1 {
		t.Errorf("exec-2 summary = %v, want total 1", summary)
	}
}
