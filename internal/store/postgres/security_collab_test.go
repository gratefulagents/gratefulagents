package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

func TestClassifySecurityFindingReobservation(t *testing.T) {
	existing := func(status, baseline, severity string, resolved bool) *store.SecurityFindingRecord {
		rec := &store.SecurityFindingRecord{Status: status, BaselineState: baseline, Severity: severity}
		if resolved {
			now := time.Now()
			rec.ResolvedAt = &now
		}
		return rec
	}
	tests := []struct {
		name         string
		existing     *store.SecurityFindingRecord
		incoming     string
		wantBaseline string
		wantStatus   string
		wantEvent    string
		wantSeverity string
		wantClearRes bool
		wantClearExp bool
	}{
		{
			name:         "open finding recurs",
			existing:     existing("open", "new", "high", false),
			incoming:     "high",
			wantBaseline: "recurring", wantStatus: "open", wantEvent: "reobserved", wantSeverity: "high",
		},
		{
			name:         "triaged finding stays triaged",
			existing:     existing("triaged", "recurring", "medium", false),
			incoming:     "medium",
			wantBaseline: "recurring", wantStatus: "triaged", wantEvent: "reobserved", wantSeverity: "medium",
		},
		{
			name:         "fixed finding regresses even when unchanged",
			existing:     existing("fixed", "recurring", "high", false),
			incoming:     "high",
			wantBaseline: "regressed", wantStatus: "open", wantEvent: "regressed", wantSeverity: "high",
		},
		{
			name:         "false positive is sticky when evidence unchanged",
			existing:     existing("false_positive", "recurring", "high", false),
			incoming:     "high",
			wantBaseline: "recurring", wantStatus: "false_positive", wantEvent: "reobserved", wantSeverity: "high",
		},
		{
			name:         "false positive is sticky when severity decreased",
			existing:     existing("false_positive", "recurring", "high", false),
			incoming:     "low",
			wantBaseline: "recurring", wantStatus: "false_positive", wantEvent: "reobserved", wantSeverity: "high",
		},
		{
			name:         "false positive regresses when severity increased",
			existing:     existing("false_positive", "recurring", "medium", false),
			incoming:     "critical",
			wantBaseline: "regressed", wantStatus: "open", wantEvent: "regressed", wantSeverity: "critical",
		},
		{
			name:         "accepted risk sticky when unchanged",
			existing:     existing("accepted_risk", "recurring", "medium", false),
			incoming:     "medium",
			wantBaseline: "recurring", wantStatus: "accepted_risk", wantEvent: "reobserved", wantSeverity: "medium",
		},
		{
			name:         "accepted risk regresses on severity increase and clears expiry",
			existing:     existing("accepted_risk", "recurring", "medium", false),
			incoming:     "high",
			wantBaseline: "regressed", wantStatus: "open", wantEvent: "regressed", wantSeverity: "high", wantClearExp: true,
		},
		{
			name:         "resolved finding reopens",
			existing:     existing("open", "resolved", "high", true),
			incoming:     "high",
			wantBaseline: "reopened", wantStatus: "open", wantEvent: "reopened", wantSeverity: "high", wantClearRes: true,
		},
		{
			name:         "resolved fixed finding reopens to open",
			existing:     existing("fixed", "resolved", "high", true),
			incoming:     "high",
			wantBaseline: "reopened", wantStatus: "open", wantEvent: "reopened", wantSeverity: "high", wantClearRes: true,
		},
		{
			name:         "resolved false positive stays suppressed when unchanged",
			existing:     existing("false_positive", "resolved", "high", true),
			incoming:     "high",
			wantBaseline: "reopened", wantStatus: "false_positive", wantEvent: "reopened", wantSeverity: "high", wantClearRes: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := classifySecurityFindingReobservation(tt.existing, tt.incoming)
			if m.baseline != tt.wantBaseline || m.status != tt.wantStatus || m.eventType != tt.wantEvent ||
				m.severity != tt.wantSeverity || m.clearResolved != tt.wantClearRes || m.clearExpiry != tt.wantClearExp {
				t.Errorf("classify = %+v, want baseline=%s status=%s event=%s severity=%s clearResolved=%v clearExpiry=%v",
					m, tt.wantBaseline, tt.wantStatus, tt.wantEvent, tt.wantSeverity, tt.wantClearRes, tt.wantClearExp)
			}
		})
	}
}

func TestSecurityFindingMergeEventDetail(t *testing.T) {
	m := securityFindingMerge{
		severity: "critical", fromSeverity: "medium",
		status: "open", fromStatus: "false_positive",
		baseline: "regressed", eventType: "regressed",
	}
	detail := m.eventDetail("nightly", "nightly-2")
	if detail["from_status"] != "false_positive" || detail["to_status"] != "open" ||
		detail["severity_from"] != "medium" || detail["severity_to"] != "critical" ||
		detail["reason"] != "severity_increased" {
		t.Errorf("regressed detail = %v", detail)
	}
	plain := securityFindingMerge{eventType: "reobserved"}.eventDetail("nightly", "nightly-2")
	if _, ok := plain["from_status"]; ok {
		t.Errorf("reobserved detail should not carry status transition: %v", plain)
	}
}

func TestValidateSecurityTicketURL(t *testing.T) {
	for _, ok := range []string{"https://github.com/org/repo/issues/1", "http://tracker.local/x"} {
		if err := store.ValidateSecurityTicketURL(ok); err != nil {
			t.Errorf("ValidateSecurityTicketURL(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"javascript:alert(1)", "ftp://host/x", "/relative", "github.com/org/repo", ""} {
		if err := store.ValidateSecurityTicketURL(bad); err == nil {
			t.Errorf("ValidateSecurityTicketURL(%q) = nil, want error", bad)
		}
	}
}

func TestSecurityFindingFilterSQLCollabFields(t *testing.T) {
	where, args := securityFindingFilterSQL(store.SecurityFindingFilter{
		Namespace: "default", BaselineState: "regressed", Assignee: "alice",
	})
	want := "WHERE namespace = $1 AND baseline_state = $2 AND assignee = $3 AND suppressed_by IS NULL AND duplicate_of IS NULL"
	if where != want {
		t.Errorf("where = %q, want %q", where, want)
	}
	if len(args) != 3 || args[1] != "regressed" || args[2] != "alice" {
		t.Errorf("args = %v", args)
	}
}

// --- live-DB integration tests (skip without TEST_DATABASE_URL) ---

// collabTestScan records a completed scan run so it can define a baseline.
func collabTestScan(ctx context.Context, t *testing.T, s *Store, scanName, runName string, completed bool) *store.SecurityScanRecord {
	t.Helper()
	rec := &store.SecurityScanRecord{
		Namespace: "default", ScanName: scanName, RunName: runName,
		Repository: "org/repo", Status: "running",
	}
	if completed {
		now := time.Now().UTC()
		rec.Status = "completed"
		rec.CompletedAt = &now
	}
	scan, err := s.UpsertSecurityScan(ctx, rec)
	if err != nil {
		t.Fatalf("UpsertSecurityScan(%s): %v", runName, err)
	}
	return scan
}

func collabTestFinding(scan *store.SecurityScanRecord, fingerprint, severity string) *store.SecurityFindingRecord {
	return &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: "default", ScanName: scan.ScanName, RunName: scan.RunName,
		Fingerprint: fingerprint, Title: "finding " + fingerprint, Category: "injection",
		Severity: severity, Confidence: "high", Repository: "org/repo", FilePath: "main.go",
	}
}

func findingEvents(ctx context.Context, t *testing.T, s *Store, id uuid.UUID) []store.SecurityFindingEvent {
	t.Helper()
	events, err := s.ListSecurityFindingEvents(ctx, "default", id, 0)
	if err != nil {
		t.Fatalf("ListSecurityFindingEvents: %v", err)
	}
	return events
}

func TestSecurityBaselineLifecycle(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	a1, b1, dup := testSecurityBaselineFirstTwoRuns(t, s, ctx)
	bAfter, err := s.GetSecurityFinding(ctx, "default", b1.ID)
	if err != nil || bAfter == nil {
		t.Fatalf("get B: %v", err)
	}
	if bAfter.BaselineState != store.SecurityFindingBaselineResolved || bAfter.ResolvedAt == nil {
		t.Errorf("B = baseline %q resolvedAt %v, want resolved with timestamp", bAfter.BaselineState, bAfter.ResolvedAt)
	}
	if events := findingEvents(ctx, t, s, b1.ID); events[0].EventType != "resolved" {
		t.Errorf("B newest event = %q, want resolved", events[0].EventType)
	}
	dupAfter, err := s.GetSecurityFinding(ctx, "default", dup.ID)
	if err != nil || dupAfter == nil {
		t.Fatalf("get dup: %v", err)
	}
	if dupAfter.BaselineState == store.SecurityFindingBaselineResolved {
		t.Error("duplicate child was baseline-resolved; duplicates must be ignored")
	}

	summary, err := s.SummarizeSecurityFindings(ctx, "default", "nightly", "", false)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary["baseline_recurring"] != 1 || summary["baseline_resolved"] != 1 || summary["baseline_tracked"] != 2 {
		t.Errorf("summary baselines = recurring %d resolved %d tracked %d, want 1/1/2 (duplicates excluded)",
			summary["baseline_recurring"], summary["baseline_resolved"], summary["baseline_tracked"])
	}

	// Run 3 sees B again: reopened, resolved_at cleared.
	scan3 := collabTestScan(ctx, t, s, "nightly", "nightly-3", true)
	b3, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan3, "fp-b", "medium"))
	if err != nil {
		t.Fatalf("reobserve B: %v", err)
	}
	if b3.BaselineState != store.SecurityFindingBaselineReopened || b3.ResolvedAt != nil {
		t.Errorf("B after run3 = baseline %q resolvedAt %v, want reopened/nil", b3.BaselineState, b3.ResolvedAt)
	}
	if events := findingEvents(ctx, t, s, b1.ID); events[0].EventType != "reopened" {
		t.Errorf("B newest event = %q, want reopened", events[0].EventType)
	}

	// Filtering by baseline state and assignee.
	if err := s.SetSecurityFindingAssignee(ctx, "default", a1.ID, "alice", "bob"); err != nil {
		t.Fatalf("assign A: %v", err)
	}
	got, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", BaselineState: "reopened"})
	if err != nil || len(got) != 1 || got[0].ID != b1.ID {
		t.Errorf("list baseline=reopened = %d rows, err %v", len(got), err)
	}
	got, err = s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", Assignee: "alice"})
	if err != nil || len(got) != 1 || got[0].ID != a1.ID {
		t.Errorf("list assignee=alice = %d rows, err %v", len(got), err)
	}
}

func testSecurityBaselineFirstTwoRuns(t *testing.T, s *Store, ctx context.Context) (*store.SecurityFindingRecord, *store.SecurityFindingRecord, *store.SecurityFindingRecord) {
	scan1 := collabTestScan(ctx, t, s, "nightly", "nightly-1", true)
	a1, createdA, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan1, "fp-a", "high"))
	if err != nil || !createdA {
		t.Fatalf("upsert A: created=%v err=%v", createdA, err)
	}
	if a1.BaselineState != store.SecurityFindingBaselineNew {
		t.Errorf("A baseline = %q, want new", a1.BaselineState)
	}
	b1, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan1, "fp-b", "medium"))
	if err != nil {
		t.Fatalf("upsert B: %v", err)
	}
	// A duplicate child of A: must never distort baseline counts or get
	// resolved by finalization.
	dupRec := collabTestFinding(scan1, "fp-a-dup", "high")
	dupRec.DuplicateOf = &a1.ID
	dup, _, err := s.UpsertSecurityFinding(ctx, dupRec)
	if err != nil {
		t.Fatalf("upsert dup: %v", err)
	}

	if _, err := s.FinalizeSecurityScanBaseline(ctx, "default", "nightly-1"); err != nil {
		t.Fatalf("finalize run1: %v", err)
	}

	// Run 2 reobserves A (recurring) but not B.
	scan2 := collabTestScan(ctx, t, s, "nightly", "nightly-2", true)
	a2, created, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan2, "fp-a", "high"))
	if err != nil || created {
		t.Fatalf("reobserve A: created=%v err=%v", created, err)
	}
	if a2.BaselineState != store.SecurityFindingBaselineRecurring || a2.Occurrences != 2 {
		t.Errorf("A after run2 = baseline %q occurrences %d, want recurring/2", a2.BaselineState, a2.Occurrences)
	}
	resolved, err := s.FinalizeSecurityScanBaseline(ctx, "default", "nightly-2")
	if err != nil {
		t.Fatalf("finalize run2: %v", err)
	}
	if resolved != 1 {
		t.Errorf("finalize run2 resolved %d findings, want 1 (only B)", resolved)
	}
	// Finalization is idempotent.
	if again, err := s.FinalizeSecurityScanBaseline(ctx, "default", "nightly-2"); err != nil || again != 0 {
		t.Errorf("finalize run2 again = %d, %v, want 0, nil", again, err)
	}

	return a1, b1, dup
}

func TestSecurityBaselineRegressionFlows(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	scan1 := collabTestScan(ctx, t, s, "nightly", "nightly-1", true)
	fixed, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan1, "fp-fixed", "high"))
	if err != nil {
		t.Fatalf("upsert fixed: %v", err)
	}
	fp, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan1, "fp-fp", "medium"))
	if err != nil {
		t.Fatalf("upsert fp: %v", err)
	}
	if err := s.SetSecurityFindingStatus(ctx, "default", fixed.ID, store.SecurityFindingStatusFixed, "alice", "", nil); err != nil {
		t.Fatalf("mark fixed: %v", err)
	}
	if err := s.SetSecurityFindingStatus(ctx, "default", fp.ID, store.SecurityFindingStatusFalsePositive, "alice", "", nil); err != nil {
		t.Fatalf("mark fp: %v", err)
	}

	scan2 := collabTestScan(ctx, t, s, "nightly", "nightly-2", true)
	// fixed reappears unchanged -> regressed + open.
	fixed2, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan2, "fp-fixed", "high"))
	if err != nil {
		t.Fatalf("reobserve fixed: %v", err)
	}
	if fixed2.BaselineState != store.SecurityFindingBaselineRegressed || fixed2.Status != store.SecurityFindingStatusOpen {
		t.Errorf("fixed reobserved = baseline %q status %q, want regressed/open", fixed2.BaselineState, fixed2.Status)
	}
	events := findingEvents(ctx, t, s, fixed.ID)
	if events[0].EventType != "regressed" {
		t.Fatalf("fixed newest event = %q, want regressed", events[0].EventType)
	}
	var detail map[string]string
	if err := json.Unmarshal(events[0].Detail, &detail); err != nil || detail["from_status"] != "fixed" || detail["to_status"] != "open" {
		t.Errorf("regressed detail = %s (err %v), want from fixed to open preserved in history", events[0].Detail, err)
	}

	// false positive reappears unchanged -> sticky suppression.
	fp2, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan2, "fp-fp", "medium"))
	if err != nil {
		t.Fatalf("reobserve fp: %v", err)
	}
	if fp2.Status != store.SecurityFindingStatusFalsePositive || fp2.BaselineState != store.SecurityFindingBaselineRecurring {
		t.Errorf("fp reobserved unchanged = status %q baseline %q, want false_positive/recurring", fp2.Status, fp2.BaselineState)
	}

	// false positive reappears with HIGHER severity -> regressed + open.
	scan3 := collabTestScan(ctx, t, s, "nightly", "nightly-3", true)
	esc := collabTestFinding(scan3, "fp-fp", "critical")
	fp3, _, err := s.UpsertSecurityFinding(ctx, esc)
	if err != nil {
		t.Fatalf("reobserve fp escalated: %v", err)
	}
	if fp3.Status != store.SecurityFindingStatusOpen || fp3.BaselineState != store.SecurityFindingBaselineRegressed || fp3.Severity != "critical" {
		t.Errorf("fp escalated = status %q baseline %q severity %q, want open/regressed/critical", fp3.Status, fp3.BaselineState, fp3.Severity)
	}
}

func TestFinalizeSecurityScanBaselineGuards(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	// Incomplete (failed / running) run never resolves anything.
	scan1 := collabTestScan(ctx, t, s, "nightly", "nightly-1", true)
	if _, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan1, "fp-a", "high")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	collabTestScan(ctx, t, s, "nightly", "nightly-2", false)
	if n, err := s.FinalizeSecurityScanBaseline(ctx, "default", "nightly-2"); err != nil || n != 0 {
		t.Errorf("finalize(incomplete run) = %d, %v, want 0, nil", n, err)
	}
	// Missing scan run is a no-op, not an error.
	if n, err := s.FinalizeSecurityScanBaseline(ctx, "default", "no-such-run"); err != nil || n != 0 {
		t.Errorf("finalize(missing run) = %d, %v, want 0, nil", n, err)
	}

	// A stale (older) run must not resolve findings observed only by newer runs.
	scan3 := collabTestScan(ctx, t, s, "nightly", "nightly-3", true)
	if _, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan3, "fp-new", "low")); err != nil {
		t.Fatalf("upsert new: %v", err)
	}
	if n, err := s.FinalizeSecurityScanBaseline(ctx, "default", "nightly-1"); err != nil || n != 0 {
		t.Errorf("finalize(stale run) = %d, %v, want 0, nil (newer observations exist)", n, err)
	}
}

func TestExpireAcceptedRisks(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	scan := collabTestScan(ctx, t, s, "nightly", "nightly-1", true)
	expired, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan, "fp-expired", "high"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	future, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan, "fp-future", "high"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	ahead := time.Now().Add(24 * time.Hour)
	if err := s.SetSecurityFindingStatus(ctx, "default", expired.ID, store.SecurityFindingStatusAcceptedRisk, "alice", "", &past); err != nil {
		t.Fatalf("accept expired: %v", err)
	}
	if err := s.SetSecurityFindingStatus(ctx, "default", future.ID, store.SecurityFindingStatusAcceptedRisk, "alice", "", &ahead); err != nil {
		t.Fatalf("accept future: %v", err)
	}
	// Expiry with a non-accepted_risk status is rejected.
	if err := s.SetSecurityFindingStatus(ctx, "default", future.ID, store.SecurityFindingStatusTriaged, "alice", "", &ahead); err == nil {
		t.Error("SetSecurityFindingStatus(triaged with expiry) = nil, want error")
	}

	n, err := s.ExpireAcceptedRisks(ctx, "default")
	if err != nil || n != 1 {
		t.Fatalf("ExpireAcceptedRisks = %d, %v, want 1", n, err)
	}
	got, err := s.GetSecurityFinding(ctx, "default", expired.ID)
	if err != nil || got == nil {
		t.Fatalf("get expired: %v", err)
	}
	if got.Status != store.SecurityFindingStatusOpen || got.AcceptedRiskExpiresAt != nil {
		t.Errorf("expired finding = status %q expiry %v, want open/nil", got.Status, got.AcceptedRiskExpiresAt)
	}
	if events := findingEvents(ctx, t, s, expired.ID); events[0].EventType != "accepted_risk_expired" {
		t.Errorf("newest event = %q, want accepted_risk_expired", events[0].EventType)
	}
	still, err := s.GetSecurityFinding(ctx, "default", future.ID)
	if err != nil || still == nil || still.Status != store.SecurityFindingStatusAcceptedRisk {
		t.Errorf("future finding = %+v, %v, want accepted_risk kept", still, err)
	}
	// Idempotent.
	if n, err := s.ExpireAcceptedRisks(ctx, "default"); err != nil || n != 0 {
		t.Errorf("second sweep = %d, %v, want 0, nil", n, err)
	}
	// triaged_at was stamped by the first status transition.
	if got.TriagedAt == nil {
		t.Error("expired finding lost triaged_at, want first-triage timestamp preserved")
	}
}

func TestBulkUpdateSecurityFindings(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	scan := collabTestScan(ctx, t, s, "nightly", "nightly-1", true)
	f1, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan, "fp-1", "high"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	f2, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan, "fp-2", "low"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	status := store.SecurityFindingStatusTriaged
	assignee := "alice"
	err = s.BulkUpdateSecurityFindings(ctx, "default", "nightly", []uuid.UUID{f1.ID, f2.ID},
		store.SecurityFindingBulkUpdate{Status: &status, Assignee: &assignee, Note: "sweep", Actor: "bob"})
	if err != nil {
		t.Fatalf("bulk update: %v", err)
	}
	for _, id := range []uuid.UUID{f1.ID, f2.ID} {
		got, err := s.GetSecurityFinding(ctx, "default", id)
		if err != nil || got == nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.Status != status || got.Assignee != "alice" || got.TriagedAt == nil {
			t.Errorf("finding %s = status %q assignee %q triagedAt %v", id, got.Status, got.Assignee, got.TriagedAt)
		}
		events := findingEvents(ctx, t, s, id)
		var types []string
		for _, ev := range events {
			types = append(types, ev.EventType)
		}
		joined := strings.Join(types, ",")
		if !strings.Contains(joined, "status_changed") || !strings.Contains(joined, "assignee_changed") {
			t.Errorf("finding %s events = %v, want status_changed and assignee_changed", id, types)
		}
	}

	// Atomicity: a missing id aborts the whole batch.
	open := store.SecurityFindingStatusOpen
	missing := uuid.New()
	err = s.BulkUpdateSecurityFindings(ctx, "default", "nightly", []uuid.UUID{f1.ID, missing},
		store.SecurityFindingBulkUpdate{Status: &open, Actor: "bob"})
	var bulkErr *store.BulkSecurityFindingError
	if !errors.As(err, &bulkErr) || bulkErr.FindingID != missing {
		t.Fatalf("bulk update with missing id = %v, want BulkSecurityFindingError{%s}", err, missing)
	}
	got, err := s.GetSecurityFinding(ctx, "default", f1.ID)
	if err != nil || got == nil || got.Status != status {
		t.Errorf("f1 after aborted batch = %+v, want status still %q (rolled back)", got, status)
	}

	// Scan scoping: an id from a different scan aborts.
	otherScan := collabTestScan(ctx, t, s, "weekly", "weekly-1", true)
	f3, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(otherScan, "fp-3", "low"))
	if err != nil {
		t.Fatalf("upsert other scan: %v", err)
	}
	err = s.BulkUpdateSecurityFindings(ctx, "default", "nightly", []uuid.UUID{f3.ID},
		store.SecurityFindingBulkUpdate{Status: &open, Actor: "bob"})
	if !errors.As(err, &bulkErr) {
		t.Errorf("bulk update across scans = %v, want BulkSecurityFindingError", err)
	}
}

func TestSecuritySavedFilterCRUD(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	saved, err := s.SaveSecuritySavedFilter(ctx, &store.SecuritySavedFilter{
		Namespace: "default", Owner: "alice", Name: "high open",
		Query: json.RawMessage(`{"severity":"high","status":"open"}`),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ID == uuid.Nil {
		t.Error("saved filter has no id")
	}
	// Upsert by (namespace, owner, name).
	updated, err := s.SaveSecuritySavedFilter(ctx, &store.SecuritySavedFilter{
		Namespace: "default", Owner: "alice", Name: "high open",
		Query: json.RawMessage(`{"severity":"critical"}`),
	})
	if err != nil || updated.ID != saved.ID {
		t.Fatalf("re-save = %+v, %v, want same id", updated, err)
	}

	// Owner isolation: bob sees nothing.
	if list, err := s.ListSecuritySavedFilters(ctx, "default", "bob"); err != nil || len(list) != 0 {
		t.Errorf("bob's filters = %d, %v, want 0", len(list), err)
	}
	list, err := s.ListSecuritySavedFilters(ctx, "default", "alice")
	if err != nil || len(list) != 1 || string(list[0].Query) != `{"severity": "critical"}` && string(list[0].Query) != `{"severity":"critical"}` {
		t.Errorf("alice's filters = %+v, %v", list, err)
	}

	// Bob deleting alice's filter is a silent no-op that removes nothing.
	if err := s.DeleteSecuritySavedFilter(ctx, "default", "bob", "high open"); err != nil {
		t.Fatalf("delete as bob: %v", err)
	}
	if list, _ := s.ListSecuritySavedFilters(ctx, "default", "alice"); len(list) != 1 {
		t.Error("bob's delete removed alice's filter")
	}
	if err := s.DeleteSecuritySavedFilter(ctx, "default", "alice", "high open"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := s.ListSecuritySavedFilters(ctx, "default", "alice"); len(list) != 0 {
		t.Error("filter not deleted")
	}
}

func TestSecurityFindingTrendsAndExport(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()

	scan := collabTestScan(ctx, t, s, "nightly", "nightly-1", true)
	f1, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan, "fp-1", "high"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, _, err := s.UpsertSecurityFinding(ctx, collabTestFinding(scan, "fp-2", "low")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetSecurityFindingStatus(ctx, "default", f1.ID, store.SecurityFindingStatusConfirmed, "alice", "", nil); err != nil {
		t.Fatalf("triage: %v", err)
	}

	trends, err := s.GetSecurityFindingTrends(ctx, "default", "nightly")
	if err != nil {
		t.Fatalf("trends: %v", err)
	}
	if trends.TriagedCount != 1 || trends.ResolvedCount != 0 {
		t.Errorf("trends = %+v, want triaged 1 resolved 0", trends)
	}
	if trends.AvgTimeToTriageSeconds < 0 || trends.MedianTimeToTriageSeconds < 0 {
		t.Errorf("negative triage durations: %+v", trends)
	}

	records, err := s.ExportSecurityFindingEvents(ctx, "default", "nightly", 0)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("export = %d events, want 1 (the status change)", len(records))
	}
	if records[0].Event.EventType != "status_changed" || records[0].FindingID != f1.ID ||
		records[0].Fingerprint != "fp-1" || records[0].Title == "" {
		t.Errorf("export record = %+v", records[0])
	}
	if _, err := s.ExportSecurityFindingEvents(ctx, "default", "", 0); err == nil {
		t.Error("export without scan name should error")
	}
}
