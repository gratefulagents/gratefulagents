package triggers

import (
	"context"
	"errors"
	"testing"
	"time"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// purgeCall records one PurgeExpiredSecurityData invocation.
type purgeCall struct {
	namespace  string
	policy     store.SecurityRetentionPolicy
	batchLimit int
}

// retentionTestFindingStore scripts PurgeExpiredSecurityData batches and
// records every call plus every DeleteSecurityScanData (finalizer) call.
type retentionTestFindingStore struct {
	securityScanRecordStubStore
	calls   []purgeCall
	results []struct {
		counts   store.SecurityRetentionCounts
		moreWork bool
		err      error
	}
	deletes []string
}

func (s *retentionTestFindingStore) push(counts store.SecurityRetentionCounts, moreWork bool, err error) {
	s.results = append(s.results, struct {
		counts   store.SecurityRetentionCounts
		moreWork bool
		err      error
	}{counts, moreWork, err})
}

func (s *retentionTestFindingStore) PurgeExpiredSecurityData(_ context.Context, namespace string, policy store.SecurityRetentionPolicy, batchLimit int) (store.SecurityRetentionCounts, bool, error) {
	s.calls = append(s.calls, purgeCall{namespace: namespace, policy: policy, batchLimit: batchLimit})
	if len(s.results) == 0 {
		return store.SecurityRetentionCounts{}, false, nil
	}
	next := s.results[0]
	s.results = s.results[1:]
	return next.counts, next.moreWork, next.err
}

func (s *retentionTestFindingStore) DeleteSecurityScanData(_ context.Context, namespace, scanName string) error {
	s.deletes = append(s.deletes, namespace+"/"+scanName)
	return nil
}

func (s *retentionTestFindingStore) SummarizeSecurityFindings(context.Context, string, string, string, bool) (map[string]int32, error) {
	return map[string]int32{}, nil
}

func (s *retentionTestFindingStore) ExpireSecuritySuppressions(context.Context, string) (int32, error) {
	return 0, nil
}

func retentionTestScanAndPack() (*triggersv1alpha1.SecurityScan, *triggersv1alpha1.SecurityPolicyPack) {
	scan := securityScanTestScan()
	scan.Spec.PolicyPackRef = &triggersv1alpha1.SecurityResourceRef{Name: "org-policy"}
	pack := securityTestPolicyPack(scan.Namespace)
	pack.Spec.Retention = &triggersv1alpha1.SecurityPolicyPackRetention{
		ScanDays:       30,
		FindingDays:    90,
		EvidenceDays:   14,
		PoCDays:        7,
		AuditEventDays: 365,
	}
	return scan, pack
}

// TestSecurityScanRetentionSweepRunsOneBoundedBatchAndResumes pins the
// batching model: one bounded, namespace-scoped purge batch per reconcile,
// prompt requeue while the store reports more work, cumulative counters in
// status.retention.
func TestSecurityScanRetentionSweepRunsOneBoundedBatchAndResumes(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan, pack := retentionTestScanAndPack()
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, pack)
	findings := &retentionTestFindingStore{}
	findings.push(store.SecurityRetentionCounts{FindingsDeleted: 200, EvidenceRedacted: 5}, true, nil)
	findings.push(store.SecurityRetentionCounts{FindingsDeleted: 40}, false, nil)
	reconciler.Findings = findings

	result, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan))
	if err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if len(findings.calls) != 1 {
		t.Fatalf("purge calls = %d, want exactly one bounded batch per reconcile", len(findings.calls))
	}
	call := findings.calls[0]
	if call.namespace != scan.Namespace {
		t.Fatalf("purge namespace = %q, want the scan's namespace %q", call.namespace, scan.Namespace)
	}
	if call.batchLimit != securityRetentionBatchLimit {
		t.Fatalf("batchLimit = %d, want %d", call.batchLimit, securityRetentionBatchLimit)
	}
	if call.policy.FindingDays != 90 || call.policy.EvidenceDays != 14 || call.policy.PoCDays != 7 {
		t.Fatalf("policy = %+v, want the pack's retention day counts", call.policy)
	}
	if result.RequeueAfter != securityRetentionResumeDelay {
		t.Fatalf("RequeueAfter = %v, want prompt resume %v while moreWork", result.RequeueAfter, securityRetentionResumeDelay)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	st := updated.Status.Retention
	if st == nil || !st.MoreWork || st.FindingsPurged != 200 || st.EvidenceRedacted != 5 || st.LastSweepTime == nil {
		t.Fatalf("status.retention = %+v, want observable batch outcome with moreWork", st)
	}

	// moreWork resumes immediately even though the sweep interval has not
	// elapsed, and the counters accumulate.
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if len(findings.calls) != 2 {
		t.Fatalf("purge calls after resume = %d, want 2", len(findings.calls))
	}
	updated = getSecurityScan(t, k8sClient, scan)
	st = updated.Status.Retention
	if st == nil || st.MoreWork || st.FindingsPurged != 240 {
		t.Fatalf("status.retention = %+v, want cumulative 240 findings purged and moreWork cleared", st)
	}

	// A clean sweep is rate-limited: the next reconcile inside the interval
	// runs no purge.
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("third Reconcile() error = %v", err)
	}
	if len(findings.calls) != 2 {
		t.Fatalf("purge calls inside the sweep interval = %d, want still 2", len(findings.calls))
	}
}

// TestSecurityScanRetentionSweepSkipsWithoutRetention pins that scans whose
// pack configures no retention never purge.
func TestSecurityScanRetentionSweepSkipsWithoutRetention(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.PolicyPackRef = &triggersv1alpha1.SecurityResourceRef{Name: "org-policy"}
	pack := securityTestPolicyPack(scan.Namespace)
	reconciler, _, _ := newSecurityScanReconciler(t, now, scan, pack)
	findings := &retentionTestFindingStore{}
	reconciler.Findings = findings

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(findings.calls) != 0 {
		t.Fatalf("purge calls = %d, want none without a retention policy", len(findings.calls))
	}
}

// TestSecurityScanRetentionSweepErrorIsRecordedAndRetried pins that a store
// failure surfaces in status.retention.lastError and requeues without
// failing the reconcile.
func TestSecurityScanRetentionSweepErrorIsRecordedAndRetried(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan, pack := retentionTestScanAndPack()
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, pack)
	findings := &retentionTestFindingStore{}
	findings.push(store.SecurityRetentionCounts{}, true, errors.New("db down"))
	reconciler.Findings = findings

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v, want retention failures to stay best-effort", err)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	st := updated.Status.Retention
	if st == nil || st.LastError != "db down" {
		t.Fatalf("status.retention = %+v, want lastError recorded", st)
	}
}

// TestSecurityScanDeletionNeverRunsRetention pins that the deletion
// finalizer path never runs retention work: deletion stays bounded by the
// cleanup purge alone.
func TestSecurityScanDeletionNeverRunsRetention(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan, pack := retentionTestScanAndPack()
	deleted := metav1.NewTime(now.Add(-time.Minute))
	scan.DeletionTimestamp = &deleted
	scan.Finalizers = []string{securityScanCleanupFinalizer}
	reconciler, _, _ := newSecurityScanReconciler(t, now, scan, pack)
	findings := &retentionTestFindingStore{}
	reconciler.Findings = findings

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(findings.calls) != 0 {
		t.Fatalf("purge calls during deletion = %d, want none: retention must never delay the finalizer", len(findings.calls))
	}
	if len(findings.deletes) != 1 {
		t.Fatalf("cleanup deletes = %v, want exactly the finalizer purge", findings.deletes)
	}
}
