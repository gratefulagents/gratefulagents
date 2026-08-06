package postgres

import (
	"context"
	"encoding/json"
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

// assertSecuritySuppressionRevoked verifies the vendored finding's
// suppression was cleared with a preserved audit trail: the original
// "suppressed" event stays, exactly one "suppression_revoked" event records
// the previous rule/owner/reason, and the finding is back in default
// listings and failOnSeverity gate counts.
func assertSecuritySuppressionRevoked(t *testing.T, fixture securitySuppressionFixture) {
	t.Helper()
	s, ctx, vendored := fixture.s, fixture.ctx, fixture.vendored
	restored, err := s.GetSecurityFinding(ctx, "default", vendored.ID)
	if err != nil || restored == nil {
		t.Fatalf("GetSecurityFinding(after revocation) = %v, %v, want the row preserved", restored, err)
	}
	if restored.SuppressedBy != "" || restored.SuppressedReason != "" || restored.SuppressedOwner != "" ||
		restored.SuppressionExpiresAt != nil || restored.SuppressedAt != nil {
		t.Fatalf("revoked finding still suppressed: %+v", restored)
	}
	if restored.Title != "vendored xss" || restored.Status != "open" {
		t.Fatalf("revocation must not erase finding content: %+v", restored)
	}
	events, err := s.ListSecurityFindingEvents(ctx, "default", vendored.ID, 0)
	if err != nil {
		t.Fatalf("ListSecurityFindingEvents(after revocation): %v", err)
	}
	revokedEvents := 0
	var detail map[string]string
	for _, ev := range events {
		if ev.EventType == "suppression_revoked" {
			revokedEvents++
			if err := json.Unmarshal(ev.Detail, &detail); err != nil {
				t.Fatalf("suppression_revoked detail = %s: %v", ev.Detail, err)
			}
		}
	}
	if revokedEvents != 1 {
		t.Fatalf("suppression_revoked events = %d, want exactly 1", revokedEvents)
	}
	if detail["rule"] != fixture.rule.ID || detail["owner"] != "appsec" || detail["reason"] != "vendored code" {
		t.Fatalf("suppression_revoked detail = %v, want the previous rule/owner/reason", detail)
	}
	if securitySuppressedEventCount(events) != 1 {
		t.Fatalf("suppressed events = %v, want the original suppression history preserved", events)
	}

	// Back in default lists and, critically, back in the open_<severity>
	// gate counts failOnSeverity evaluates.
	listed, err := s.ListSecurityFindings(ctx, store.SecurityFindingFilter{Namespace: "default", ScanName: "nightly"})
	if err != nil || len(listed) != 2 {
		t.Fatalf("default list after revocation = %d findings, %v, want 2", len(listed), err)
	}
	summary, err := s.SummarizeSecurityFindings(ctx, "default", "nightly", "", false)
	if err != nil {
		t.Fatalf("SummarizeSecurityFindings(after revocation): %v", err)
	}
	if summary["total"] != 2 || summary["open_high"] != 2 || summary["suppressed"] != 0 {
		t.Fatalf("summary after revocation = %v, want the finding gated again", summary)
	}
}

func TestSecuritySuppressionRevocationRuleDeleted(t *testing.T) {
	fixture := newSecuritySuppressionFixture(t)
	s, ctx := fixture.s, fixture.ctx

	// The pack still has rules, just not the one that granted the
	// suppression (the same shape as swapping the pack for another one).
	remaining := store.SecuritySuppressionRule{
		ID: "org-policy/unrelated", Reason: "unrelated", Owner: "appsec",
		Matcher: store.SecuritySuppressionMatcher{Fingerprint: "fp-none"},
	}
	n, err := s.RevokeSecuritySuppressions(ctx, "default", "nightly", []store.SecuritySuppressionRule{remaining})
	if err != nil || n != 1 {
		t.Fatalf("RevokeSecuritySuppressions = %d, %v, want 1, nil", n, err)
	}
	assertSecuritySuppressionRevoked(t, fixture)

	// Idempotent: a second sweep revokes nothing and adds no events.
	if again, err := s.RevokeSecuritySuppressions(ctx, "default", "nightly", []store.SecuritySuppressionRule{remaining}); err != nil || again != 0 {
		t.Fatalf("RevokeSecuritySuppressions(again) = %d, %v, want 0, nil", again, err)
	}
	assertSecuritySuppressionRevoked(t, fixture)
}

func TestSecuritySuppressionRevocationMatcherNarrowed(t *testing.T) {
	fixture := newSecuritySuppressionFixture(t)
	s, ctx := fixture.s, fixture.ctx

	// The rule id survives, so a still-matching finding stays suppressed.
	n, err := s.RevokeSecuritySuppressions(ctx, "default", "nightly", []store.SecuritySuppressionRule{fixture.rule})
	if err != nil || n != 0 {
		t.Fatalf("RevokeSecuritySuppressions(unchanged matcher) = %d, %v, want 0, nil", n, err)
	}
	still, err := s.GetSecurityFinding(ctx, "default", fixture.vendored.ID)
	if err != nil || still == nil || still.SuppressedBy != fixture.rule.ID {
		t.Fatalf("finding = %+v, %v, want it still suppressed while the matcher matches", still, err)
	}

	// Narrowing the matcher so the finding no longer matches revokes it.
	narrowed := fixture.rule
	narrowed.Matcher = store.SecuritySuppressionMatcher{PathGlob: "vendor/elsewhere/*"}
	n, err = s.RevokeSecuritySuppressions(ctx, "default", "nightly", []store.SecuritySuppressionRule{narrowed})
	if err != nil || n != 1 {
		t.Fatalf("RevokeSecuritySuppressions(narrowed matcher) = %d, %v, want 1, nil", n, err)
	}
	assertSecuritySuppressionRevoked(t, fixture)
	if again, err := s.RevokeSecuritySuppressions(ctx, "default", "nightly", []store.SecuritySuppressionRule{narrowed}); err != nil || again != 0 {
		t.Fatalf("RevokeSecuritySuppressions(again) = %d, %v, want 0, nil", again, err)
	}
}

func TestSecuritySuppressionRevocationNoRules(t *testing.T) {
	fixture := newSecuritySuppressionFixture(t)
	s, ctx := fixture.s, fixture.ctx

	// A second scan's suppression must survive this scan's sweep.
	otherScan, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace: "default", ScanName: "weekly", RunName: "weekly-1", Repository: "org/repo",
	})
	if err != nil {
		t.Fatalf("UpsertSecurityScan(weekly): %v", err)
	}
	otherFinding, _, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: otherScan.ID, Namespace: "default", ScanName: "weekly", RunName: "weekly-1",
		Fingerprint: "fp-weekly", Title: "weekly vendored xss", Category: "xss",
		Severity: "high", Repository: "org/repo", FilePath: "vendor/lib/y.js",
	})
	if err != nil {
		t.Fatalf("UpsertSecurityFinding(weekly): %v", err)
	}
	if n, err := s.ApplySecuritySuppressions(ctx, "default", "weekly", []store.SecuritySuppressionRule{fixture.rule}); err != nil || n != 1 {
		t.Fatalf("ApplySecuritySuppressions(weekly) = %d, %v, want 1, nil", n, err)
	}

	// No active rules (pack deleted or policyPackRef removed): every
	// suppression on THIS scan is revoked.
	n, err := s.RevokeSecuritySuppressions(ctx, "default", "nightly", nil)
	if err != nil || n != 1 {
		t.Fatalf("RevokeSecuritySuppressions(no rules) = %d, %v, want 1, nil", n, err)
	}
	assertSecuritySuppressionRevoked(t, fixture)
	if again, err := s.RevokeSecuritySuppressions(ctx, "default", "nightly", nil); err != nil || again != 0 {
		t.Fatalf("RevokeSecuritySuppressions(again) = %d, %v, want 0, nil", again, err)
	}
	other, err := s.GetSecurityFinding(ctx, "default", otherFinding.ID)
	if err != nil || other == nil || other.SuppressedBy != fixture.rule.ID {
		t.Fatalf("other scan's finding = %+v, %v, want its suppression untouched", other, err)
	}
}
