package triggers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestSecurityScanReconcileSuspendedScanCreatesNoRuns(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.Suspend = true
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	result, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("RequeueAfter = %v, want 0", result.RequeueAfter)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 0 {
		t.Fatalf("AgentRuns = %d, want 0", len(got))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.Phase != "Suspended" {
		t.Fatalf("Phase = %q, want Suspended", updated.Status.Phase)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "Suspended")
}

func TestSecurityScanReconcileOneShotCreatesExactlyOneRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	reconciler, k8sClient, stateStore := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}

	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want exactly one", len(runs))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastRunName != runs[0].Name {
		t.Fatalf("LastRunName = %q, want %q", updated.Status.LastRunName, runs[0].Name)
	}
	if updated.Status.RunsCreated != 1 {
		t.Fatalf("RunsCreated = %d, want 1", updated.Status.RunsCreated)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionTrue, "ScanUpToDate")

	sessionID := stateStore.sessions[scan.Namespace+"/"+runs[0].Name]
	messages := stateStore.messages[sessionID]
	if len(messages) != 1 || messages[0].Content != BuildSecurityScanPrompt(scan.Spec) {
		t.Fatalf("seed messages = %#v, want one security scan prompt", messages)
	}
}

func TestSecurityScanReconcileScheduledScanCreatesDueRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 1, 30, 0, time.UTC)
	due := metav1.NewTime(time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC))
	scan := securityScanTestScan()
	scan.Spec.Schedule = "* * * * *"
	scan.Status.ObservedSchedule = scan.Spec.Schedule
	scan.Status.ObservedTimeZone = "UTC"
	scan.Status.NextScheduleTime = &due
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	result, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("RequeueAfter = %v, want 30s", result.RequeueAfter)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(got))
	}
}

func TestSecurityScanReconcileScheduledScanRequeuesBeforeTick(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 1, 30, 0, time.UTC)
	next := metav1.NewTime(time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC))
	scan := securityScanTestScan()
	scan.Spec.Schedule = "* * * * *"
	scan.Status.ObservedSchedule = scan.Spec.Schedule
	scan.Status.ObservedTimeZone = "UTC"
	scan.Status.NextScheduleTime = &next
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	result, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("RequeueAfter = %v, want 30s", result.RequeueAfter)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 0 {
		t.Fatalf("AgentRuns = %d, want 0", len(got))
	}
}

func TestSecurityScanReconcileForbidSkipsDueTickWhilePriorRunIsActive(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 1, 30, 0, time.UTC)
	due := metav1.NewTime(time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC))
	scan := securityScanTestScan()
	scan.Spec.Schedule = "* * * * *"
	scan.Status.ObservedSchedule = scan.Spec.Schedule
	scan.Status.ObservedTimeZone = "UTC"
	scan.Status.NextScheduleTime = &due
	activeRun := securityScanPriorRun(scan, platformv1alpha1.AgentRunPhaseRunning)
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, activeRun)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 1 {
		t.Fatalf("AgentRuns = %d, want only active prior run", len(got))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastError == "" {
		t.Fatal("LastError = empty, want concurrency block message")
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "ConcurrencyBlocked")
}

func TestSecurityScanReconcileAllowCreatesDueRunWhilePriorRunIsActive(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 1, 30, 0, time.UTC)
	due := metav1.NewTime(time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC))
	scan := securityScanTestScan()
	scan.Spec.Schedule = "* * * * *"
	scan.Spec.ConcurrencyPolicy = triggersv1alpha1.SecurityScanConcurrencyAllow
	scan.Status.ObservedSchedule = scan.Spec.Schedule
	scan.Status.ObservedTimeZone = "UTC"
	scan.Status.NextScheduleTime = &due
	activeRun := securityScanPriorRun(scan, platformv1alpha1.AgentRunPhaseRunning)
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, activeRun)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 2 {
		t.Fatalf("AgentRuns = %d, want active prior run plus due run", len(got))
	}
}

func TestSecurityScanReconcileCreatedRunCarriesScanMetadataOwnerAndDefaultMode(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.Revision = "abc123"
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	run := runs[0]
	if run.Annotations[triggersv1alpha1.SecurityScanNameAnnotation] != scan.Name {
		t.Fatalf("scan-name annotation = %q, want %q", run.Annotations[triggersv1alpha1.SecurityScanNameAnnotation], scan.Name)
	}
	if run.Annotations[triggersv1alpha1.SecurityScanRepositoryAnnotation] != scan.Spec.RepoURL {
		t.Fatalf("repository annotation = %q, want %q", run.Annotations[triggersv1alpha1.SecurityScanRepositoryAnnotation], scan.Spec.RepoURL)
	}
	if run.Annotations[triggersv1alpha1.SecurityScanRevisionAnnotation] != scan.Spec.Revision {
		t.Fatalf("revision annotation = %q, want %q", run.Annotations[triggersv1alpha1.SecurityScanRevisionAnnotation], scan.Spec.Revision)
	}
	if run.Spec.Repository.Revision != scan.Spec.Revision {
		t.Fatalf("Spec.Repository.Revision = %q, want pinned revision %q", run.Spec.Repository.Revision, scan.Spec.Revision)
	}
	if len(run.OwnerReferences) != 1 || run.OwnerReferences[0].Kind != securityScanKind || run.OwnerReferences[0].Name != scan.Name {
		t.Fatalf("OwnerReferences = %#v, want SecurityScan/%s", run.OwnerReferences, scan.Name)
	}
	if run.Spec.ModeRef == nil || run.Spec.ModeRef.Name != securityScanModeTemplate {
		t.Fatalf("ModeRef = %#v, want default %q", run.Spec.ModeRef, securityScanModeTemplate)
	}
}

func TestSecurityScanReconcileInvalidScheduleRecordsFailureCondition(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.Schedule = "not a cron expression"
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	result, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != time.Minute {
		t.Fatalf("RequeueAfter = %v, want 1m", result.RequeueAfter)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastError == "" {
		t.Fatal("LastError = empty, want invalid schedule error")
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "InvalidSchedule")
}

func TestSecurityScanReconcileFailsReadyConditionWhenFindingsMeetThreshold(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.FailOnSeverity = "high"
	findings := securityScanFindingStore{counts: map[string]int32{"total": 2, "open": 2, "critical": 1, "high": 1}}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
	reconciler.Findings = findings

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.Findings == nil || updated.Status.Findings.Critical != 1 || updated.Status.Findings.High != 1 {
		t.Fatalf("Findings = %#v, want critical and high counts", updated.Status.Findings)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "FindingsExceedThreshold")
}

// securityScanRecordStubStore gives finding-store fakes working
// GetSecurityScan/UpsertSecurityScan implementations so the eager scan
// record created on run dispatch never dereferences the fakes' nil embedded
// interface. Upserts are recorded only when a test initializes scanRecords.
type securityScanRecordStubStore struct {
	store.SecurityFindingStore
	scanRecords map[string]*store.SecurityScanRecord
}

func (s securityScanRecordStubStore) GetSecurityScan(_ context.Context, namespace, runName string) (*store.SecurityScanRecord, error) {
	if rec := s.scanRecords[namespace+"/"+runName]; rec != nil {
		cp := *rec
		return &cp, nil
	}
	return nil, nil
}

func (s securityScanRecordStubStore) UpsertSecurityScan(_ context.Context, rec *store.SecurityScanRecord) (*store.SecurityScanRecord, error) {
	if s.scanRecords != nil {
		cp := *rec
		s.scanRecords[rec.Namespace+"/"+rec.RunName] = &cp
	}
	return rec, nil
}

type securityScanFindingStore struct {
	securityScanRecordStubStore
	counts map[string]int32
}

func (s securityScanFindingStore) SummarizeSecurityFindings(context.Context, string, string, string, bool) (map[string]int32, error) {
	return s.counts, nil
}

type recordingSecurityScanFindingStore struct {
	securityScanRecordStubStore
	counts   map[string]int32
	scanName string
	runName  string
}

func (s *recordingSecurityScanFindingStore) SummarizeSecurityFindings(_ context.Context, _, scanName, runName string, _ bool) (map[string]int32, error) {
	s.scanName = scanName
	s.runName = runName
	return s.counts, nil
}

type deletingSecurityScanFindingStore struct {
	securityScanRecordStubStore
	err       error
	calls     int
	namespace string
	scanName  string
}

func (s *deletingSecurityScanFindingStore) DeleteSecurityScanData(_ context.Context, namespace, scanName string) error {
	s.calls++
	s.namespace = namespace
	s.scanName = scanName
	return s.err
}

func (s *deletingSecurityScanFindingStore) SummarizeSecurityFindings(context.Context, string, string, string, bool) (map[string]int32, error) {
	return nil, nil
}

func TestSecurityScanReconcileAddsCleanupFinalizer(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if !controllerutil.ContainsFinalizer(updated, securityScanCleanupFinalizer) {
		t.Fatalf("Finalizers = %v, want %q", updated.Finalizers, securityScanCleanupFinalizer)
	}
}

func TestSecurityScanReconcileDeletionPurgesFindingsAndRemovesFinalizer(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Finalizers = []string{securityScanCleanupFinalizer}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
	findings := &deletingSecurityScanFindingStore{}
	reconciler.Findings = findings

	if err := k8sClient.Delete(context.Background(), scan); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if findings.calls != 1 || findings.namespace != scan.Namespace || findings.scanName != scan.Name {
		t.Fatalf("DeleteSecurityScanData calls = %d with (%q, %q), want 1 with (%q, %q)",
			findings.calls, findings.namespace, findings.scanName, scan.Namespace, scan.Name)
	}
	assertSecurityScanGone(t, k8sClient, scan)
}

func TestSecurityScanReconcileDeletionWithNilStoreRemovesFinalizer(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Finalizers = []string{securityScanCleanupFinalizer}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if err := k8sClient.Delete(context.Background(), scan); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	assertSecurityScanGone(t, k8sClient, scan)
}

func TestSecurityScanReconcileDeletionStoreErrorRetriesThenReleasesAfterDeadline(t *testing.T) {
	scan := securityScanTestScan()
	scan.Finalizers = []string{securityScanCleanupFinalizer}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, time.Now(), scan)
	findings := &deletingSecurityScanFindingStore{err: errors.New("store down")}
	reconciler.Findings = findings

	if err := k8sClient.Delete(context.Background(), scan); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Within the cleanup deadline a store error requeues with backoff and
	// keeps the finalizer so the purge is retried.
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err == nil {
		t.Fatal("Reconcile() error = nil, want store error for backoff requeue")
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if !controllerutil.ContainsFinalizer(updated, securityScanCleanupFinalizer) {
		t.Fatalf("Finalizers = %v, want finalizer kept while within cleanup deadline", updated.Finalizers)
	}

	// Past the deadline the finalizer is released despite the failing store
	// so deletion can never be wedged permanently.
	reconciler.Now = func() time.Time { return time.Now().Add(securityScanCleanupDeadline + time.Minute) }
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() after deadline error = %v", err)
	}
	if findings.calls != 2 {
		t.Fatalf("DeleteSecurityScanData calls = %d, want 2", findings.calls)
	}
	assertSecurityScanGone(t, k8sClient, scan)
}

func assertSecurityScanGone(t *testing.T, k8sClient client.Client, scan *triggersv1alpha1.SecurityScan) {
	t.Helper()
	err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(scan), &triggersv1alpha1.SecurityScan{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("Get(SecurityScan) error = %v, want NotFound after finalizer removal", err)
	}
}

func TestSecurityScanReconcileUnsatisfiableScheduleSetsScheduleError(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.Schedule = "0 0 30 2 *"
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	result, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != time.Minute {
		t.Fatalf("RequeueAfter = %v, want 1m", result.RequeueAfter)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 0 {
		t.Fatalf("AgentRuns = %d, want 0", len(got))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastError == "" {
		t.Fatal("LastError = empty, want schedule error")
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "ScheduleError")
}

func TestSecurityScanReconcileSpecGenerationBumpCreatesSecondOneShotRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Generation = 1
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 1 {
		t.Fatalf("AgentRuns after first generation = %d, want 1", len(got))
	}

	updated := getSecurityScan(t, k8sClient, scan)
	updated.Spec.Revision = "def456"
	updated.Generation = 2
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(SecurityScan) error = %v", err)
	}
	bumped := getSecurityScan(t, k8sClient, scan)
	if bumped.Generation == bumped.Status.ObservedGeneration {
		t.Fatalf("Generation = %d did not advance past observedGeneration %d", bumped.Generation, bumped.Status.ObservedGeneration)
	}

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 2 {
		t.Fatalf("AgentRuns after generation bump = %d, want 2", len(got))
	}
	final := getSecurityScan(t, k8sClient, scan)
	if final.Status.RunsCreated != 2 {
		t.Fatalf("RunsCreated = %d, want 2", final.Status.RunsCreated)
	}
}

func TestSecurityScanReconcileSuspendThenUnsuspend(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 1, 30, 0, time.UTC)
	next := metav1.NewTime(time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC))
	scan := securityScanTestScan()
	scan.Spec.Schedule = "* * * * *"
	scan.Status.ObservedSchedule = scan.Spec.Schedule
	scan.Status.ObservedTimeZone = "UTC"
	scan.Status.NextScheduleTime = &next
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("initial Reconcile() error = %v", err)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.Phase != "Scheduled" {
		t.Fatalf("Phase = %q, want Scheduled", updated.Status.Phase)
	}

	updated.Spec.Suspend = true
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(suspend) error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("suspended Reconcile() error = %v", err)
	}
	updated = getSecurityScan(t, k8sClient, scan)
	if updated.Status.Phase != "Suspended" {
		t.Fatalf("Phase = %q, want Suspended", updated.Status.Phase)
	}
	if updated.Status.NextScheduleTime != nil {
		t.Fatalf("NextScheduleTime = %v, want nil while suspended", updated.Status.NextScheduleTime)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "Suspended")

	updated.Spec.Suspend = false
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(unsuspend) error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("unsuspended Reconcile() error = %v", err)
	}
	updated = getSecurityScan(t, k8sClient, scan)
	if updated.Status.Phase != "Scheduled" {
		t.Fatalf("Phase = %q, want Scheduled after unsuspend", updated.Status.Phase)
	}
	if updated.Status.NextScheduleTime == nil {
		t.Fatal("NextScheduleTime = nil, want restored after unsuspend")
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionTrue, "Scheduled")
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 0 {
		t.Fatalf("AgentRuns = %d, want 0 before the next tick", len(got))
	}
}

func TestSecurityScanReconcileScheduledScanReachesCompletedPhase(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 1, 30, 0, time.UTC)
	due := metav1.NewTime(time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC))
	scan := securityScanTestScan()
	scan.Spec.Schedule = "* * * * *"
	scan.Status.ObservedSchedule = scan.Spec.Schedule
	scan.Status.ObservedTimeZone = "UTC"
	scan.Status.NextScheduleTime = &due
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("tick Reconcile() error = %v", err)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.Phase != "Running" {
		t.Fatalf("Phase = %q, want Running after tick", updated.Status.Phase)
	}

	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	run := runs[0].DeepCopy()
	run.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	if err := k8sClient.Status().Update(context.Background(), run); err != nil {
		t.Fatalf("Status().Update(AgentRun) error = %v", err)
	}

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("post-run Reconcile() error = %v", err)
	}
	updated = getSecurityScan(t, k8sClient, scan)
	if updated.Status.Phase != "Completed" {
		t.Fatalf("Phase = %q, want Completed after run finished", updated.Status.Phase)
	}
}

func TestSecurityScanReconcileLongScanNameProducesValidLabel(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Name = strings.Repeat("very-long-scan-name-", 5) + "tail"
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	value := runs[0].Labels[securityScanLabel]
	if errs := validation.IsValidLabelValue(value); len(errs) != 0 {
		t.Fatalf("label value %q invalid: %v", value, errs)
	}
	if value != securityScanLabelValue(scan.Name) {
		t.Fatalf("label value = %q, want %q so activeScanRun listing matches", value, securityScanLabelValue(scan.Name))
	}
}

func TestSecurityScanLabelValueShortNameUnchanged(t *testing.T) {
	if got := securityScanLabelValue("nightly-security"); got != "nightly-security" {
		t.Fatalf("securityScanLabelValue = %q, want unchanged name", got)
	}
}

func TestSecurityScanReconcileInsecureDefaultsRejected(t *testing.T) {
	for name, mutate := range map[string]func(*triggersv1alpha1.SecurityScan){
		"disableCommandSandbox": func(scan *triggersv1alpha1.SecurityScan) { scan.Spec.Defaults.DisableCommandSandbox = true },
		"kubernetesAdmin":       func(scan *triggersv1alpha1.SecurityScan) { scan.Spec.Defaults.KubernetesAdmin = true },
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			scan := securityScanTestScan()
			mutate(scan)
			reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

			if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 0 {
				t.Fatalf("AgentRuns = %d, want 0", len(got))
			}
			updated := getSecurityScan(t, k8sClient, scan)
			if updated.Status.LastError == "" {
				t.Fatal("LastError = empty, want insecure defaults error")
			}
			assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "InsecureDefaults")
		})
	}
}

func TestSecurityScanReconcileInvalidDefaultsDoNotOrphanInstructionsConfigMap(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.Defaults.Model = "small"
	scan.Spec.Defaults.CustomInstructions = "focus on the auth package"
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 0 {
		t.Fatalf("AgentRuns = %d, want 0", len(got))
	}
	configMaps := &corev1.ConfigMapList{}
	if err := k8sClient.List(context.Background(), configMaps, client.InNamespace(scan.Namespace)); err != nil {
		t.Fatalf("List(ConfigMap) error = %v", err)
	}
	if len(configMaps.Items) != 0 {
		t.Fatalf("ConfigMaps = %d, want 0 (no orphaned instructions)", len(configMaps.Items))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "CreateRunFailed")
}

func TestSecurityScanReconcileFailOnSeverityIgnoresTriagedFindings(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.FailOnSeverity = "high"
	findings := &recordingSecurityScanFindingStore{counts: map[string]int32{
		"total": 3, "open": 0,
		"critical": 2, "high": 1,
		"open_critical": 0, "open_high": 0,
	}}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
	reconciler.Findings = findings

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.Findings == nil || updated.Status.Findings.Critical != 2 {
		t.Fatalf("Findings = %#v, want severity counts recorded", updated.Status.Findings)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionTrue, "ScanStarted")
	if findings.scanName != scan.Name || findings.runName != "" {
		t.Fatalf("SummarizeSecurityFindings(scan=%q, run=%q), want scan-level summary with empty run name", findings.scanName, findings.runName)
	}
}

func TestSecurityScanReconcileCreatedRunCarriesPolicyAnnotations(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.MinSeverity = "medium"
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	run := runs[0]
	if got := run.Annotations[triggersv1alpha1.SecurityScanMinSeverityAnnotation]; got != "medium" {
		t.Fatalf("min-severity annotation = %q, want medium", got)
	}
	if got := run.Annotations[triggersv1alpha1.SecurityScanDedupePermilleAnnotation]; got != "820" {
		t.Fatalf("dedupe-permille annotation = %q, want default 820", got)
	}
}

func TestSecurityScanReconcileDedupeDisabledAnnotatesZeroPermille(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	disabled := false
	scan := securityScanTestScan()
	scan.Spec.Dedupe = &triggersv1alpha1.SecurityScanDedupe{Enabled: &disabled}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	if got := runs[0].Annotations[triggersv1alpha1.SecurityScanDedupePermilleAnnotation]; got != "0" {
		t.Fatalf("dedupe-permille annotation = %q, want 0 when dedupe is disabled", got)
	}
}

func newSecurityScanReconciler(t *testing.T, now time.Time, objects ...client.Object) (*SecurityScanReconciler, client.Client, *seedTestStore) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.SecurityScan{}, &platformv1alpha1.AgentRun{}).
		WithObjects(objects...).
		Build()
	stateStore := newSeedTestStore()
	return &SecurityScanReconciler{
		Client:     k8sClient,
		Scheme:     scheme,
		StateStore: stateStore,
		Now:        func() time.Time { return now },
	}, k8sClient, stateStore
}

func securityScanTestScan() *triggersv1alpha1.SecurityScan {
	return &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nightly-security",
			Namespace: "default",
			UID:       types.UID("security-scan-uid"),
		},
		Spec: triggersv1alpha1.SecurityScanSpec{
			RepoURL:    "https://github.com/acme/widget.git",
			BaseBranch: "main",
			Execution: &triggersv1alpha1.SecurityScanExecution{
				Mode: triggersv1alpha1.SecurityScanExecutionModeCoordinator,
			},
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Model:    "gpt-5.4",
				Provider: triggersv1alpha1.ProviderOpenAI,
				Secrets: triggersv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{{
					Provider:   triggersv1alpha1.ProviderOpenAI,
					SecretName: "openai-key",
				}}},
			},
		},
	}
}

func securityScanPriorRun(scan *triggersv1alpha1.SecurityScan, phase platformv1alpha1.AgentRunPhase) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "secscan-prior-run",
			Namespace: scan.Namespace,
			Labels:    map[string]string{securityScanLabel: scan.Name},
		},
		Spec: platformv1alpha1.AgentRunSpec{Trigger: platformv1alpha1.TriggerRef{
			Kind: securityScanKind,
			Name: scan.Name,
			ExternalRef: &platformv1alpha1.ExternalRef{
				ID: "2026-01-01T00:00:00Z",
			},
		}},
		Status: platformv1alpha1.AgentRunStatus{Phase: phase},
	}
}

func securityScanRequest(scan *triggersv1alpha1.SecurityScan) ctrl.Request {
	return ctrl.Request{NamespacedName: client.ObjectKeyFromObject(scan)}
}

func securityScanRuns(t *testing.T, k8sClient client.Client, namespace string) []platformv1alpha1.AgentRun {
	t.Helper()
	runs := &platformv1alpha1.AgentRunList{}
	if err := k8sClient.List(context.Background(), runs, client.InNamespace(namespace)); err != nil {
		t.Fatalf("List(AgentRun) error = %v", err)
	}
	return runs.Items
}

func getSecurityScan(t *testing.T, k8sClient client.Client, scan *triggersv1alpha1.SecurityScan) *triggersv1alpha1.SecurityScan {
	t.Helper()
	updated := &triggersv1alpha1.SecurityScan{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(scan), updated); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	return updated
}

func assertSecurityScanCondition(t *testing.T, scan *triggersv1alpha1.SecurityScan, wantStatus metav1.ConditionStatus, wantReason string) {
	t.Helper()
	for _, condition := range scan.Status.Conditions {
		if condition.Type == triggersv1alpha1.ConditionSecurityScanReady {
			if condition.Status != wantStatus || condition.Reason != wantReason {
				t.Fatalf("Ready condition = %#v, want status %q and reason %q", condition, wantStatus, wantReason)
			}
			return
		}
	}
	t.Fatalf("Ready condition missing from %#v", scan.Status.Conditions)
}

// securityScanWithRunNowToken returns a scheduled scan whose next tick is far
// in the future (so only the run-now path can create runs) carrying the given
// run-now annotation token.
func securityScanWithRunNowToken(now time.Time, token string) *triggersv1alpha1.SecurityScan {
	next := metav1.NewTime(now.Add(time.Hour))
	scan := securityScanTestScan()
	scan.Spec.Schedule = "0 3 * * *"
	scan.Status.ObservedSchedule = scan.Spec.Schedule
	scan.Status.ObservedTimeZone = "UTC"
	scan.Status.NextScheduleTime = &next
	scan.Annotations = map[string]string{triggersv1alpha1.SecurityScanRunNowAnnotation: token}
	return scan
}

func annotateSecurityScanRunNow(t *testing.T, k8sClient client.Client, scan *triggersv1alpha1.SecurityScan, token string) {
	t.Helper()
	fresh := getSecurityScan(t, k8sClient, scan)
	if fresh.Annotations == nil {
		fresh.Annotations = map[string]string{}
	}
	fresh.Annotations[triggersv1alpha1.SecurityScanRunNowAnnotation] = token
	if err := k8sClient.Update(context.Background(), fresh); err != nil {
		t.Fatalf("Update(SecurityScan) error = %v", err)
	}
}

func TestSecurityScanRunNowCreatesRunOncePerToken(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanWithRunNowToken(now, "tok-1")
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastManualRunToken != "tok-1" {
		t.Fatalf("LastManualRunToken = %q, want tok-1", updated.Status.LastManualRunToken)
	}
	if updated.Status.ManualRunsCreated != 1 || updated.Status.RunsCreated != 1 {
		t.Fatalf("ManualRunsCreated = %d, RunsCreated = %d, want 1 and 1",
			updated.Status.ManualRunsCreated, updated.Status.RunsCreated)
	}
	if updated.Status.Phase != "Running" || updated.Status.LastRunName != runs[0].Name {
		t.Fatalf("Phase = %q, LastRunName = %q, want Running and %q",
			updated.Status.Phase, updated.Status.LastRunName, runs[0].Name)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionTrue, "ManualRunStarted")

	// A second reconcile with the consumed token creates nothing.
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 1 {
		t.Fatalf("AgentRuns after second reconcile = %d, want 1", len(got))
	}
}

func TestSecurityScanRunNowTokenSurvivesControllerRestart(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanWithRunNowToken(now, "tok-restart")
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// A brand-new reconciler (controller restart) sees the consumed token in
	// status, not in memory, and never creates a second run.
	restarted := &SecurityScanReconciler{
		Client:     k8sClient,
		Scheme:     reconciler.Scheme,
		StateStore: newSeedTestStore(),
		Now:        func() time.Time { return now },
	}
	if _, err := restarted.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("restarted Reconcile() error = %v", err)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(got))
	}
	if updated := getSecurityScan(t, k8sClient, scan); updated.Status.ManualRunsCreated != 1 {
		t.Fatalf("ManualRunsCreated = %d, want 1", updated.Status.ManualRunsCreated)
	}
}

func TestSecurityScanRunNowIdempotentAfterCrashBeforeStatusWrite(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanWithRunNowToken(now, "tok-crash")
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Simulate a crash after the run was created but before the token was
	// recorded: clear the consumed token and reconcile again. The run name is
	// derived from the token, so creation dedupes on AlreadyExists.
	if err := retrySecurityScanStatusUpdate(context.Background(), k8sClient, client.ObjectKeyFromObject(scan), func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.LastManualRunToken = ""
	}); err != nil {
		t.Fatalf("clearing token: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() after crash error = %v", err)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(got))
	}
	if updated := getSecurityScan(t, k8sClient, scan); updated.Status.LastManualRunToken != "tok-crash" {
		t.Fatalf("LastManualRunToken = %q, want tok-crash", updated.Status.LastManualRunToken)
	}
}

func TestSecurityScanRunNowForbidConsumesTokenWithoutRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanWithRunNowToken(now, "tok-1")
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	// First token creates a run that stays active (empty phase is
	// non-terminal).
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	annotateSecurityScanRunNow(t, k8sClient, scan, "tok-2")
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("blocked Reconcile() error = %v", err)
	}

	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 1 {
		t.Fatalf("AgentRuns = %d, want 1 (Forbid must not double-run)", len(got))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastManualRunToken != "tok-2" {
		t.Fatalf("LastManualRunToken = %q, want tok-2 (token consumed)", updated.Status.LastManualRunToken)
	}
	if updated.Status.ManualRunsCreated != 1 {
		t.Fatalf("ManualRunsCreated = %d, want 1", updated.Status.ManualRunsCreated)
	}
	if !strings.Contains(updated.Status.LastError, "still active") {
		t.Fatalf("LastError = %q, want mention of the active run", updated.Status.LastError)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "ConcurrencyBlocked")

	// A consumed blocked token never fires later, even after the active run
	// finishes.
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	run := runs[0]
	run.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	if err := k8sClient.Status().Update(context.Background(), &run); err != nil {
		t.Fatalf("marking run terminal: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() after completion error = %v", err)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(got))
	}
}

func TestSecurityScanRunNowAllowRunsDespiteActiveRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanWithRunNowToken(now, "tok-1")
	scan.Spec.ConcurrencyPolicy = triggersv1alpha1.SecurityScanConcurrencyAllow
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	annotateSecurityScanRunNow(t, k8sClient, scan, "tok-2")
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 2 {
		t.Fatalf("AgentRuns = %d, want 2 (Allow permits overlap)", len(got))
	}
	if updated := getSecurityScan(t, k8sClient, scan); updated.Status.ManualRunsCreated != 2 {
		t.Fatalf("ManualRunsCreated = %d, want 2", updated.Status.ManualRunsCreated)
	}
}

func TestSecurityScanRunNowIgnoredWhileSuspended(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanWithRunNowToken(now, "tok-1")
	scan.Spec.Suspend = true
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 0 {
		t.Fatalf("AgentRuns = %d, want 0 while suspended", len(got))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.Phase != "Suspended" || updated.Status.LastManualRunToken != "" {
		t.Fatalf("Phase = %q, LastManualRunToken = %q, want Suspended and unconsumed token",
			updated.Status.Phase, updated.Status.LastManualRunToken)
	}
}

func TestSecurityScanRunNowOneShotSatisfiesGeneration(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Generation = 3
	scan.Annotations = map[string]string{triggersv1alpha1.SecurityScanRunNowAnnotation: "tok-1"}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if got := securityScanRuns(t, k8sClient, scan.Namespace); len(got) != 1 {
		t.Fatalf("AgentRuns = %d, want 1 (manual run satisfies the generation)", len(got))
	}
	if updated := getSecurityScan(t, k8sClient, scan); updated.Status.ObservedGeneration != 3 {
		t.Fatalf("ObservedGeneration = %d, want 3", updated.Status.ObservedGeneration)
	}
}

func (s securityScanFindingStore) ApplySecuritySuppressions(context.Context, string, string, []store.SecuritySuppressionRule) (int32, error) {
	return 0, nil
}

func (s securityScanFindingStore) ExpireSecuritySuppressions(context.Context, string) (int32, error) {
	return 0, nil
}

func (s *recordingSecurityScanFindingStore) ApplySecuritySuppressions(context.Context, string, string, []store.SecuritySuppressionRule) (int32, error) {
	return 0, nil
}

func (s *recordingSecurityScanFindingStore) ExpireSecuritySuppressions(context.Context, string) (int32, error) {
	return 0, nil
}

func (s *deletingSecurityScanFindingStore) ApplySecuritySuppressions(context.Context, string, string, []store.SecuritySuppressionRule) (int32, error) {
	return 0, nil
}

func (s *deletingSecurityScanFindingStore) ExpireSecuritySuppressions(context.Context, string) (int32, error) {
	return 0, nil
}
