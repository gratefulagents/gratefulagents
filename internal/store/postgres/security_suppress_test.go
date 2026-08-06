package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

func TestSecurityGlobToLike(t *testing.T) {
	tests := []struct{ glob, want string }{
		{"vendor/*", `vendor/%`},
		{"*.min.js", `%.min.js`},
		{"a?c", `a_c`},
		{`50%_off\now`, `50\%\_off\\now`},
		{"plain/path.go", "plain/path.go"},
	}
	for _, tt := range tests {
		if got := securityGlobToLike(tt.glob); got != tt.want {
			t.Errorf("securityGlobToLike(%q) = %q, want %q", tt.glob, got, tt.want)
		}
	}
}

func TestSecuritySuppressionMatchSQL(t *testing.T) {
	args := []any{"ns", "scan", "rule", "reason", "owner", nil}
	match := securitySuppressionMatchSQL(store.SecuritySuppressionMatcher{
		Category:    "injection",
		CWE:         "CWE-89",
		PathGlob:    "vendor/*",
		Fingerprint: "fp-1",
	}, &args)
	want := "category = $7 AND $8 = ANY(cwe) AND file_path LIKE $9 AND fingerprint = $10"
	if match != want {
		t.Errorf("match =\n%q\nwant\n%q", match, want)
	}
	if len(args) != 10 || args[6] != "injection" || args[7] != "CWE-89" || args[8] != "vendor/%" || args[9] != "fp-1" {
		t.Errorf("args = %v", args)
	}
}

// TestSecuritySuppressionLifecycle exercises apply/expire semantics against a
// live database: suppression marks (never deletes) findings, appends audit
// events, excludes them from default lists/summaries while keeping them
// retrievable via the explicit filter, and expiry restores them with a
// preserved audit trail.
type securitySuppressionFixture struct {
	s          *Store
	ctx        context.Context
	vendored   *store.SecurityFindingRecord
	firstParty *store.SecurityFindingRecord
	rule       store.SecuritySuppressionRule
}

func newSecuritySuppressionFixture(t *testing.T) securitySuppressionFixture {
	s := setupSecurityTestStore(t)
	ctx := context.Background()
	scan, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace: "default", ScanName: "nightly", RunName: "nightly-1", Repository: "org/repo",
	})
	if err != nil {
		t.Fatalf("UpsertSecurityScan: %v", err)
	}
	vendored, _, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
		Fingerprint: "fp-vendor", Title: "vendored xss", Category: "xss",
		Severity: "high", Repository: "org/repo", FilePath: "vendor/lib/x.js", CWE: []string{"CWE-79"},
	})
	if err != nil {
		t.Fatalf("UpsertSecurityFinding: %v", err)
	}
	firstParty, _, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
		Fingerprint: "fp-app", Title: "app sqli", Category: "injection",
		Severity: "high", Repository: "org/repo", FilePath: "app/db.go", CWE: []string{"CWE-89"},
	})
	if err != nil {
		t.Fatalf("UpsertSecurityFinding: %v", err)
	}

	expires := time.Now().UTC().Add(time.Hour)
	rule := store.SecuritySuppressionRule{
		ID: "org-policy/noisy-vendor", Reason: "vendored code", Owner: "appsec",
		Matcher:   store.SecuritySuppressionMatcher{PathGlob: "vendor/*"},
		ExpiresAt: &expires,
	}
	n, err := s.ApplySecuritySuppressions(ctx, "default", "nightly", []store.SecuritySuppressionRule{rule})
	if err != nil || n != 1 {
		t.Fatalf("ApplySecuritySuppressions = %d, %v, want 1, nil", n, err)
	}
	// Idempotent: re-applying suppresses nothing new and adds no events.
	n, err = s.ApplySecuritySuppressions(ctx, "default", "nightly", []store.SecuritySuppressionRule{rule})
	if err != nil || n != 0 {
		t.Fatalf("ApplySecuritySuppressions(again) = %d, %v, want 0, nil", n, err)
	}

	return securitySuppressionFixture{s: s, ctx: ctx, vendored: vendored, firstParty: firstParty, rule: rule}
}

func securitySuppressedEventCount(events []store.SecurityFindingEvent) int {
	count := 0
	for _, event := range events {
		if event.EventType == "suppressed" {
			count++
		}
	}
	return count
}

func assertSecuritySuppressionInitialState(t *testing.T, fixture securitySuppressionFixture) {
	s, ctx := fixture.s, fixture.ctx
	vendored, firstParty, rule := fixture.vendored, fixture.firstParty, fixture.rule
	// The finding row is marked, never deleted or erased.
	got, err := s.GetSecurityFinding(ctx, "default", vendored.ID)
	if err != nil || got == nil {
		t.Fatalf("GetSecurityFinding(suppressed) = %v, %v, want the row preserved", got, err)
	}
	if got.SuppressedBy != rule.ID || got.SuppressedReason != "vendored code" || got.SuppressedOwner != "appsec" ||
		got.SuppressionExpiresAt == nil || got.SuppressedAt == nil {
		t.Fatalf("suppressed finding = %+v, want suppression fields recorded", got)
	}
	if got.Title != "vendored xss" || got.Status != "open" {
		t.Fatalf("suppression must not erase finding content: %+v", got)
	}
	events, err := s.ListSecurityFindingEvents(ctx, "default", vendored.ID, 0)
	if err != nil {
		t.Fatalf("ListSecurityFindingEvents: %v", err)
	}
	suppressedEvents := securitySuppressedEventCount(events)
	if suppressedEvents != 1 {
		t.Fatalf("suppressed events = %d, want exactly 1 (idempotent)", suppressedEvents)
	}

	// Default list excludes suppressed; the explicit filter retrieves them.
	listed, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", ScanName: "nightly"})
	if err != nil || len(listed) != 1 || listed[0].ID != firstParty.ID {
		t.Fatalf("default list = %d findings, %v, want only the unsuppressed one", len(listed), err)
	}
	only, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{
		Namespace: "default", ScanName: "nightly", Suppressed: store.SecuritySuppressedOnly,
	})
	if err != nil || len(only) != 1 || only[0].ID != vendored.ID {
		t.Fatalf("suppressed-only list = %d findings, %v, want the suppressed one", len(only), err)
	}
	both, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{
		Namespace: "default", ScanName: "nightly", Suppressed: store.SecuritySuppressedInclude,
	})
	if err != nil || len(both) != 2 {
		t.Fatalf("include list = %d findings, %v, want 2", len(both), err)
	}

	// Default summary excludes suppressed from every count (so
	// failOnSeverity gating on open_<severity> ignores them) but reports
	// them under "suppressed"; includeSuppressed folds them back in.
	summary, err := s.SummarizeSecurityFindings(ctx, "default", "nightly", "", false)
	if err != nil {
		t.Fatalf("SummarizeSecurityFindings: %v", err)
	}
	if summary["total"] != 1 || summary["open"] != 1 || summary["open_high"] != 1 || summary["suppressed"] != 1 {
		t.Fatalf("default summary = %v, want the suppressed finding out of every gate count", summary)
	}
	included, err := s.SummarizeSecurityFindings(ctx, "default", "nightly", "", true)
	if err != nil {
		t.Fatalf("SummarizeSecurityFindings(include): %v", err)
	}
	if included["total"] != 2 || included["open_high"] != 2 || included["suppressed"] != 1 {
		t.Fatalf("included summary = %v, want suppressed folded into counts", included)
	}

}

func TestSecuritySuppressionLifecycle(t *testing.T) {
	fixture := newSecuritySuppressionFixture(t)
	assertSecuritySuppressionInitialState(t, fixture)
	s, ctx, vendored, rule := fixture.s, fixture.ctx, fixture.vendored, fixture.rule
	// A rule edit refreshes metadata on the already-suppressed finding
	// without a second event.
	newExpires := time.Now().UTC().Add(30 * time.Minute)
	edited := rule
	edited.Reason = "vendored code, upstream fix pending"
	edited.ExpiresAt = &newExpires
	if n, err := s.ApplySecuritySuppressions(ctx, "default", "nightly", []store.SecuritySuppressionRule{edited}); err != nil || n != 0 {
		t.Fatalf("ApplySecuritySuppressions(edited) = %d, %v, want 0, nil", n, err)
	}
	refreshed, err := s.GetSecurityFinding(ctx, "default", vendored.ID)
	if err != nil || refreshed == nil || refreshed.SuppressedReason != "vendored code, upstream fix pending" {
		t.Fatalf("refreshed suppression = %+v, %v", refreshed, err)
	}

	// Expiry: force the deadline into the past, sweep, and verify the
	// finding is restored with its audit history intact.
	past := time.Now().UTC().Add(-time.Minute)
	expiredRule := rule
	expiredRule.ExpiresAt = &past
	if n, err := s.ApplySecuritySuppressions(ctx, "default", "nightly", []store.SecuritySuppressionRule{expiredRule}); err != nil || n != 0 {
		t.Fatalf("ApplySecuritySuppressions(past expiry refresh) = %d, %v", n, err)
	}
	expired, err := s.ExpireSecuritySuppressions(ctx, "default")
	if err != nil || expired != 1 {
		t.Fatalf("ExpireSecuritySuppressions = %d, %v, want 1, nil", expired, err)
	}
	if again, err := s.ExpireSecuritySuppressions(ctx, "default"); err != nil || again != 0 {
		t.Fatalf("ExpireSecuritySuppressions(again) = %d, %v, want 0, nil (idempotent)", again, err)
	}
	restored, err := s.GetSecurityFinding(ctx, "default", vendored.ID)
	if err != nil || restored == nil {
		t.Fatalf("GetSecurityFinding(after expiry) = %v, %v", restored, err)
	}
	if restored.SuppressedBy != "" || restored.SuppressionExpiresAt != nil || restored.SuppressedAt != nil {
		t.Fatalf("expired finding still suppressed: %+v", restored)
	}
	events, err := s.ListSecurityFindingEvents(ctx, "default", vendored.ID, 0)
	if err != nil {
		t.Fatalf("ListSecurityFindingEvents(after expiry): %v", err)
	}
	var sawSuppressed, sawExpired bool
	for _, ev := range events {
		switch ev.EventType {
		case "suppressed":
			sawSuppressed = true
		case "suppression_expired":
			sawExpired = true
		}
	}
	if !sawSuppressed || !sawExpired {
		t.Fatalf("audit trail = %+v, want suppressed AND suppression_expired preserved", events)
	}
	listed, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", ScanName: "nightly"})
	if err != nil || len(listed) != 2 {
		t.Fatalf("default list after expiry = %d findings, %v, want 2", len(listed), err)
	}
}
