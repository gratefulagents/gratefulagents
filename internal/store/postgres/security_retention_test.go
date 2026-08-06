package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

func setupSecurityRetentionTestStore(t *testing.T) (*Store, *pgxpool.Pool) {
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
	if _, err := pool.Exec(ctx, "DELETE FROM agent_artifacts WHERE kind IN ('security_report', 'security_sarif')"); err != nil {
		t.Fatalf("cleaning security artifacts: %v", err)
	}
	return NewFromPool(pool), pool
}

// retentionSeedFinding upserts one finding for scan run and backdates its
// last_seen_at by ageDays. The scan row for (namespace, runName) must exist:
// findings carry a non-null scan_id foreign key.
func retentionSeedFinding(ctx context.Context, t *testing.T, s *Store, pool *pgxpool.Pool, namespace, scanName, runName, fingerprint string, ageDays int) uuid.UUID {
	t.Helper()
	scan, err := s.GetSecurityScan(ctx, namespace, runName)
	if err != nil || scan == nil {
		t.Fatalf("GetSecurityScan(%s/%s) = %v, %v: seed the scan before its findings", namespace, runName, scan, err)
	}
	rec, _, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID:    scan.ID,
		Namespace: namespace, ScanName: scanName, RunName: runName,
		Fingerprint: fingerprint, Title: "t " + fingerprint, Category: "injection", Severity: "high",
		Repository:   "org/repo",
		AttackVector: "send crafted payload to /login",
		Raw:          json.RawMessage(`{"evidence":[{"file_path":"a.go","snippet":"secret"}],"attack_vector":"send crafted payload to /login","tags":["x"]}`),
	})
	if err != nil {
		t.Fatalf("UpsertSecurityFinding(%s): %v", fingerprint, err)
	}
	old := time.Now().UTC().Add(-time.Duration(ageDays) * 24 * time.Hour)
	if _, err := pool.Exec(ctx, "UPDATE security_findings SET first_seen_at = $2, last_seen_at = $2 WHERE id = $1", rec.ID, old); err != nil {
		t.Fatalf("backdating finding: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE security_finding_events SET created_at = $2 WHERE finding_id = $1", rec.ID, old); err != nil {
		t.Fatalf("backdating finding events: %v", err)
	}
	return rec.ID
}

// retentionSeedScan upserts one completed scan run backdated by ageDays.
func retentionSeedScan(ctx context.Context, t *testing.T, s *Store, pool *pgxpool.Pool, namespace, scanName, runName string, ageDays int) uuid.UUID {
	t.Helper()
	completed := time.Now().UTC().Add(-time.Duration(ageDays) * 24 * time.Hour)
	rec, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace: namespace, ScanName: scanName, RunName: runName,
		Repository: "org/repo", Status: "completed", CompletedAt: &completed,
	})
	if err != nil {
		t.Fatalf("UpsertSecurityScan(%s): %v", runName, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE security_scans SET created_at = $2 WHERE id = $1", rec.ID, completed); err != nil {
		t.Fatalf("backdating scan: %v", err)
	}
	return rec.ID
}

// retentionSeedReportSession inserts an agent session in the namespace with
// one security_report and one security_sarif artifact and, when scanID is
// non-nil, links the scan row to it. It returns the session id.
func retentionSeedReportSession(ctx context.Context, t *testing.T, pool *pgxpool.Pool, namespace, runName string, scanID *uuid.UUID) uuid.UUID {
	t.Helper()
	var sessionID uuid.UUID
	if err := pool.QueryRow(ctx,
		"INSERT INTO agent_sessions (agentrun_name, agentrun_ns) VALUES ($1, $2) RETURNING id", runName, namespace).Scan(&sessionID); err != nil {
		t.Fatalf("inserting session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM agent_sessions WHERE id = $1", sessionID)
	})
	if scanID != nil {
		if _, err := pool.Exec(ctx, "UPDATE security_scans SET session_id = $2 WHERE id = $1", *scanID, sessionID); err != nil {
			t.Fatalf("linking session: %v", err)
		}
	}
	for _, kind := range []string{"security_report", "security_sarif"} {
		if _, err := pool.Exec(ctx,
			"INSERT INTO agent_artifacts (session_id, kind, content) VALUES ($1, $2, 'report body')", sessionID, kind); err != nil {
			t.Fatalf("inserting %s artifact: %v", kind, err)
		}
	}
	return sessionID
}

// countSessionReportArtifacts counts the session's remaining security report
// artifacts.
func countSessionReportArtifacts(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM agent_artifacts WHERE session_id = $1 AND kind IN ('security_report', 'security_sarif')", sessionID).Scan(&n); err != nil {
		t.Fatalf("counting artifacts: %v", err)
	}
	return n
}

func TestPurgeExpiredSecurityDataEvidenceAndPoCRedactionKeepsRowAndAudit(t *testing.T) {
	s, pool := setupSecurityRetentionTestStore(t)
	ctx := context.Background()

	retentionSeedScan(ctx, t, s, pool, "default", "nightly", "nightly-1", 100)
	findingID := retentionSeedFinding(ctx, t, s, pool, "default", "nightly", "nightly-1", "fp-old", 100)
	freshID := retentionSeedFinding(ctx, t, s, pool, "default", "nightly", "nightly-1", "fp-fresh", 1)

	policy := store.SecurityRetentionPolicy{EvidenceDays: 30, PoCDays: 30}
	counts, moreWork, err := s.PurgeExpiredSecurityData(ctx, "default", policy, 50)
	if err != nil {
		t.Fatalf("PurgeExpiredSecurityData: %v", err)
	}
	if counts.EvidenceRedacted != 1 || counts.PoCsRedacted != 1 || moreWork {
		t.Fatalf("counts = %+v moreWork = %v, want one evidence and one PoC redaction", counts, moreWork)
	}

	rec, err := s.GetSecurityFinding(ctx, "default", findingID)
	if err != nil || rec == nil {
		t.Fatalf("GetSecurityFinding after redaction = %v, %v: the finding row must survive", rec, err)
	}
	if rec.AttackVector != "" {
		t.Errorf("AttackVector = %q, want redacted", rec.AttackVector)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Raw, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if _, ok := raw["evidence"]; ok {
		t.Errorf("raw still contains evidence after redaction: %s", rec.Raw)
	}
	if _, ok := raw["attack_vector"]; ok {
		t.Errorf("raw still contains attack_vector after redaction: %s", rec.Raw)
	}
	if _, ok := raw["tags"]; !ok {
		t.Errorf("raw lost unrelated keys during redaction: %s", rec.Raw)
	}

	events, err := s.ListSecurityFindingEvents(ctx, "default", findingID, 0)
	if err != nil {
		t.Fatalf("ListSecurityFindingEvents: %v", err)
	}
	kinds := map[string]bool{}
	for _, ev := range events {
		kinds[ev.EventType] = true
	}
	if !kinds["evidence_purged"] || !kinds["poc_purged"] {
		t.Errorf("audit events = %v, want evidence_purged and poc_purged recorded", kinds)
	}

	fresh, err := s.GetSecurityFinding(ctx, "default", freshID)
	if err != nil || fresh == nil || fresh.AttackVector == "" {
		t.Fatalf("fresh finding = %+v, %v: findings inside the window must stay intact", fresh, err)
	}

	// Idempotent: a second sweep finds nothing left to redact.
	counts, moreWork, err = s.PurgeExpiredSecurityData(ctx, "default", policy, 50)
	if err != nil || moreWork || !counts.IsZero() {
		t.Fatalf("second purge = %+v, %v, %v; want a clean no-op", counts, moreWork, err)
	}
}

func TestPurgeExpiredSecurityDataIsNamespaceScopedAndBatched(t *testing.T) {
	s, pool := setupSecurityRetentionTestStore(t)
	ctx := context.Background()

	retentionSeedScan(ctx, t, s, pool, "team-a", "nightly", "a-1", 100)
	retentionSeedScan(ctx, t, s, pool, "team-b", "weekly", "b-1", 90)
	for _, fp := range []string{"fp-1", "fp-2", "fp-3"} {
		retentionSeedFinding(ctx, t, s, pool, "team-a", "nightly", "a-1", fp, 100)
	}
	otherID := retentionSeedFinding(ctx, t, s, pool, "team-b", "weekly", "b-1", "fp-other", 100)

	policy := store.SecurityRetentionPolicy{FindingDays: 30}
	counts, moreWork, err := s.PurgeExpiredSecurityData(ctx, "team-a", policy, 2)
	if err != nil {
		t.Fatalf("PurgeExpiredSecurityData: %v", err)
	}
	if counts.FindingsDeleted != 2 || !moreWork {
		t.Fatalf("first batch = %+v moreWork = %v, want 2 deletions and more work", counts, moreWork)
	}
	counts, moreWork, err = s.PurgeExpiredSecurityData(ctx, "team-a", policy, 2)
	if err != nil {
		t.Fatalf("PurgeExpiredSecurityData(resume): %v", err)
	}
	if counts.FindingsDeleted != 1 || moreWork {
		t.Fatalf("resumed batch = %+v moreWork = %v, want final deletion and no more work", counts, moreWork)
	}

	if got, err := s.GetSecurityFinding(ctx, "team-b", otherID); err != nil || got == nil {
		t.Fatalf("team-b finding = %v, %v: other namespaces must be untouched", got, err)
	}

	// team-a scan rows are only deletable once nothing references them.
	counts, _, err = s.PurgeExpiredSecurityData(ctx, "team-a", store.SecurityRetentionPolicy{ScanDays: 30}, 50)
	if err != nil {
		t.Fatalf("PurgeExpiredSecurityData(scans): %v", err)
	}
	if counts.ScansDeleted == 0 {
		t.Fatalf("counts = %+v, want the now-unreferenced team-a scan row purged", counts)
	}
	if scan, err := s.GetSecurityScan(ctx, "team-b", "b-1"); err != nil || scan == nil {
		t.Fatalf("team-b scan = %v, %v: other namespaces must be untouched", scan, err)
	}
}

func TestPurgeExpiredSecurityDataScanRowsPinnedByFindings(t *testing.T) {
	s, pool := setupSecurityRetentionTestStore(t)
	ctx := context.Background()

	retentionSeedScan(ctx, t, s, pool, "default", "nightly", "nightly-1", 100)
	retentionSeedFinding(ctx, t, s, pool, "default", "nightly", "nightly-1", "fp-1", 1)

	counts, _, err := s.PurgeExpiredSecurityData(ctx, "default", store.SecurityRetentionPolicy{ScanDays: 30}, 50)
	if err != nil {
		t.Fatalf("PurgeExpiredSecurityData: %v", err)
	}
	if scan, err := s.GetSecurityScan(ctx, "default", "nightly-1"); err != nil || scan == nil {
		t.Fatalf("scan = %v, %v (counts=%+v): a scan row referenced by a live finding must never be purged", scan, err, counts)
	}
}

func TestPurgeExpiredSecurityDataAuditEventsAndReports(t *testing.T) {
	s, pool := setupSecurityRetentionTestStore(t)
	ctx := context.Background()

	scanID := retentionSeedScan(ctx, t, s, pool, "default", "nightly", "nightly-1", 100)
	findingID := retentionSeedFinding(ctx, t, s, pool, "default", "nightly", "nightly-1", "fp-1", 100)

	// Attach a session with report artifacts to the old scan run.
	sessionID := retentionSeedReportSession(ctx, t, pool, "default", "nightly-1", &scanID)

	// Give the finding a backdated audit event for the audit purge to sweep.
	if err := s.SetSecurityFindingStatus(ctx, "default", findingID, store.SecurityFindingStatusTriaged, "tester", "seed audit event", nil); err != nil {
		t.Fatalf("SetSecurityFindingStatus: %v", err)
	}
	old := time.Now().UTC().Add(-100 * 24 * time.Hour)
	if _, err := pool.Exec(ctx, "UPDATE security_finding_events SET created_at = $2 WHERE finding_id = $1", findingID, old); err != nil {
		t.Fatalf("backdating finding events: %v", err)
	}

	counts, _, err := s.PurgeExpiredSecurityData(ctx, "default",
		store.SecurityRetentionPolicy{ReportDays: 30, AuditEventDays: 30}, 50)
	if err != nil {
		t.Fatalf("PurgeExpiredSecurityData: %v", err)
	}
	if counts.ReportsDeleted != 2 {
		t.Errorf("ReportsDeleted = %d, want both report artifacts purged", counts.ReportsDeleted)
	}
	if counts.AuditEventsDeleted == 0 {
		t.Errorf("AuditEventsDeleted = %d, want the backdated events purged", counts.AuditEventsDeleted)
	}
	if rec, err := s.GetSecurityFinding(ctx, "default", findingID); err != nil || rec == nil {
		t.Fatalf("finding = %v, %v: audit purge must not delete the finding row", rec, err)
	}
	if n := countSessionReportArtifacts(ctx, t, pool, sessionID); n != 0 {
		t.Errorf("%d report artifact(s) remain, want none", n)
	}
}

func TestPurgeExpiredSecurityDataReportsPurgeableWhenScanExpiresFirst(t *testing.T) {
	s, pool := setupSecurityRetentionTestStore(t)
	ctx := context.Background()

	// scanDays (30) is shorter than reportDays (200): the 100-day-old scan
	// row is expired while its report artifacts must be retained.
	scanID := retentionSeedScan(ctx, t, s, pool, "default", "nightly", "nightly-1", 100)
	sessionID := retentionSeedReportSession(ctx, t, pool, "default", "nightly-1", &scanID)

	policy := store.SecurityRetentionPolicy{ScanDays: 30, ReportDays: 200}
	counts, _, err := s.PurgeExpiredSecurityData(ctx, "default", policy, 50)
	if err != nil {
		t.Fatalf("PurgeExpiredSecurityData: %v", err)
	}
	if counts.ReportsDeleted != 0 {
		t.Errorf("ReportsDeleted = %d, want reports inside their retention window kept", counts.ReportsDeleted)
	}
	if counts.ScansDeleted != 0 {
		t.Errorf("ScansDeleted = %d, want the scan row pinned while its reports are pending purge", counts.ScansDeleted)
	}
	if scan, err := s.GetSecurityScan(ctx, "default", "nightly-1"); err != nil || scan == nil {
		t.Fatalf("scan = %v, %v: a scan row must not be deleted while its reports are still pending purge", scan, err)
	}

	// Once the reports expire too, one sweep purges the reports first and
	// then the no-longer-pinned scan row: nothing is stranded.
	policy = store.SecurityRetentionPolicy{ScanDays: 30, ReportDays: 30}
	counts, _, err = s.PurgeExpiredSecurityData(ctx, "default", policy, 50)
	if err != nil {
		t.Fatalf("PurgeExpiredSecurityData(reports expired): %v", err)
	}
	if counts.ReportsDeleted != 2 {
		t.Errorf("ReportsDeleted = %d, want both expired report artifacts purged", counts.ReportsDeleted)
	}
	if counts.ScansDeleted != 1 {
		t.Errorf("ScansDeleted = %d, want the scan row purged once its reports are gone", counts.ScansDeleted)
	}
	if n := countSessionReportArtifacts(ctx, t, pool, sessionID); n != 0 {
		t.Errorf("%d report artifact(s) remain, want none", n)
	}
	if scan, err := s.GetSecurityScan(ctx, "default", "nightly-1"); err != nil || scan != nil {
		t.Fatalf("scan = %v, %v: the expired scan row must be gone", scan, err)
	}
}

func TestPurgeExpiredSecurityDataScanAndReportsExpireInOneSweep(t *testing.T) {
	s, pool := setupSecurityRetentionTestStore(t)
	ctx := context.Background()

	scanID := retentionSeedScan(ctx, t, s, pool, "default", "nightly", "nightly-1", 100)
	sessionID := retentionSeedReportSession(ctx, t, pool, "default", "nightly-1", &scanID)

	policy := store.SecurityRetentionPolicy{ScanDays: 30, ReportDays: 30}
	counts, _, err := s.PurgeExpiredSecurityData(ctx, "default", policy, 50)
	if err != nil {
		t.Fatalf("PurgeExpiredSecurityData: %v", err)
	}
	if counts.ReportsDeleted != 2 {
		t.Errorf("ReportsDeleted = %d, want both report artifacts purged in the same sweep", counts.ReportsDeleted)
	}
	if counts.ScansDeleted != 1 {
		t.Errorf("ScansDeleted = %d, want the scan row purged in the same sweep", counts.ScansDeleted)
	}
	if n := countSessionReportArtifacts(ctx, t, pool, sessionID); n != 0 {
		t.Errorf("%d orphaned report artifact(s) remain, want none", n)
	}

	counts, moreWork, err := s.PurgeExpiredSecurityData(ctx, "default", policy, 50)
	if err != nil || moreWork || !counts.IsZero() {
		t.Fatalf("second purge = %+v, %v, %v; want a clean no-op", counts, moreWork, err)
	}
}

func TestPurgeExpiredSecurityDataBatchLimitedSweepDoesNotStrandReports(t *testing.T) {
	s, pool := setupSecurityRetentionTestStore(t)
	ctx := context.Background()

	scanID := retentionSeedScan(ctx, t, s, pool, "default", "nightly", "nightly-1", 100)
	sessionID := retentionSeedReportSession(ctx, t, pool, "default", "nightly-1", &scanID)

	// batchLimit 1: the first sweep can only purge one of the two report
	// artifacts, so the scan row must survive the sweep even though it is
	// itself expired.
	policy := store.SecurityRetentionPolicy{ScanDays: 30, ReportDays: 30}
	counts, moreWork, err := s.PurgeExpiredSecurityData(ctx, "default", policy, 1)
	if err != nil {
		t.Fatalf("PurgeExpiredSecurityData: %v", err)
	}
	if counts.ReportsDeleted != 1 || !moreWork {
		t.Fatalf("first batch = %+v moreWork = %v, want one report purged and more work", counts, moreWork)
	}
	if counts.ScansDeleted != 0 {
		t.Errorf("ScansDeleted = %d, want the scan row kept while a report artifact is still pending purge", counts.ScansDeleted)
	}
	if scan, err := s.GetSecurityScan(ctx, "default", "nightly-1"); err != nil || scan == nil {
		t.Fatalf("scan = %v, %v: the batch-limited sweep must not delete the scan before its reports", scan, err)
	}

	// The resumed sweep purges the remaining artifact and then the scan row.
	counts, _, err = s.PurgeExpiredSecurityData(ctx, "default", policy, 1)
	if err != nil {
		t.Fatalf("PurgeExpiredSecurityData(resume): %v", err)
	}
	if counts.ReportsDeleted != 1 || counts.ScansDeleted != 1 {
		t.Fatalf("resumed batch = %+v, want the last report and the scan row purged", counts)
	}
	if n := countSessionReportArtifacts(ctx, t, pool, sessionID); n != 0 {
		t.Errorf("%d report artifact(s) remain, want none", n)
	}
}

func TestPurgeExpiredSecurityDataPurgesOrphanedReportArtifacts(t *testing.T) {
	s, pool := setupSecurityRetentionTestStore(t)
	ctx := context.Background()

	// Report artifacts whose security_scans row is already gone (the state
	// pre-existing sweeps could leave behind): purgeable via the session's
	// namespace and the artifact's own age.
	orphanID := retentionSeedReportSession(ctx, t, pool, "default", "orphan-run", nil)
	otherID := retentionSeedReportSession(ctx, t, pool, "team-b", "other-run", nil)
	old := time.Now().UTC().Add(-100 * 24 * time.Hour)
	for _, sessionID := range []uuid.UUID{orphanID, otherID} {
		if _, err := pool.Exec(ctx, "UPDATE agent_artifacts SET created_at = $2 WHERE session_id = $1", sessionID, old); err != nil {
			t.Fatalf("backdating artifacts: %v", err)
		}
	}

	counts, _, err := s.PurgeExpiredSecurityData(ctx, "default", store.SecurityRetentionPolicy{ReportDays: 30}, 50)
	if err != nil {
		t.Fatalf("PurgeExpiredSecurityData: %v", err)
	}
	if counts.ReportsDeleted != 2 {
		t.Errorf("ReportsDeleted = %d, want both orphaned report artifacts purged", counts.ReportsDeleted)
	}
	if n := countSessionReportArtifacts(ctx, t, pool, orphanID); n != 0 {
		t.Errorf("%d orphaned report artifact(s) remain, want none", n)
	}
	if n := countSessionReportArtifacts(ctx, t, pool, otherID); n != 2 {
		t.Errorf("other namespace has %d artifact(s), want 2: the purge must stay namespace-scoped", n)
	}
}
