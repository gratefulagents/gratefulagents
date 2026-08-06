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
// last_seen_at by ageDays.
func retentionSeedFinding(ctx context.Context, t *testing.T, s *Store, pool *pgxpool.Pool, namespace, scanName, runName, fingerprint string, ageDays int) uuid.UUID {
	t.Helper()
	rec, _, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
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
	retentionSeedScan(ctx, t, s, pool, "team-b", "nightly", "b-1", 100)
	for _, fp := range []string{"fp-1", "fp-2", "fp-3"} {
		retentionSeedFinding(ctx, t, s, pool, "team-a", "nightly", "a-1", fp, 100)
	}
	otherID := retentionSeedFinding(ctx, t, s, pool, "team-b", "nightly", "b-1", "fp-other", 100)

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
	if counts.FindingsDeleted != 1 {
		t.Fatalf("resumed batch = %+v, want the final deletion", counts)
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
	var sessionID uuid.UUID
	if err := pool.QueryRow(ctx,
		"INSERT INTO agent_sessions (agentrun_name, agentrun_ns) VALUES ('nightly-1', 'default') RETURNING id").Scan(&sessionID); err != nil {
		t.Fatalf("inserting session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM agent_sessions WHERE id = $1", sessionID)
	})
	if _, err := pool.Exec(ctx, "UPDATE security_scans SET session_id = $2 WHERE id = $1", scanID, sessionID); err != nil {
		t.Fatalf("linking session: %v", err)
	}
	for _, kind := range []string{"security_report", "security_sarif"} {
		if _, err := pool.Exec(ctx,
			"INSERT INTO agent_artifacts (session_id, kind, content) VALUES ($1, $2, 'report body')", sessionID, kind); err != nil {
			t.Fatalf("inserting %s artifact: %v", kind, err)
		}
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
}
