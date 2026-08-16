package triggers

import (
	"context"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// recordedScanEvent is one event emitted through the reconciler's recorder.
type recordedScanEvent struct {
	eventType string
	reason    string
	note      string
}

type recordingScanEventRecorder struct {
	events []recordedScanEvent
}

func (r *recordingScanEventRecorder) Eventf(_ runtime.Object, _ runtime.Object, eventType, reason, _ string, note string, _ ...interface{}) {
	r.events = append(r.events, recordedScanEvent{eventType: eventType, reason: reason, note: note})
}

func (r *recordingScanEventRecorder) has(eventType, reason string) bool {
	for _, event := range r.events {
		if event.eventType == eventType && event.reason == reason {
			return true
		}
	}
	return false
}

func annotateSecurityScanCancel(t *testing.T, k8sClient client.Client, scan *triggersv1alpha1.SecurityScan, token string) {
	t.Helper()
	annotateSecurityScan(t, k8sClient, scan, triggersv1alpha1.SecurityScanCancelAnnotation, token)
}

func annotateSecurityScan(t *testing.T, k8sClient client.Client, scan *triggersv1alpha1.SecurityScan, key, value string) {
	t.Helper()
	fresh := getSecurityScan(t, k8sClient, scan)
	if fresh.Annotations == nil {
		fresh.Annotations = map[string]string{}
	}
	fresh.Annotations[key] = value
	if err := k8sClient.Update(context.Background(), fresh); err != nil {
		t.Fatalf("Update(SecurityScan %s annotation): %v", key, err)
	}
}

func getAgentRun(t *testing.T, k8sClient client.Client, namespace, name string) *platformv1alpha1.AgentRun {
	t.Helper()
	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, run); err != nil {
		t.Fatalf("Get(AgentRun %s): %v", name, err)
	}
	return run
}

func TestSecurityScanCancelStopsDeterministicExecution(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "a", Objective: "inspect a"},
		{Name: "b", Objective: "inspect b"},
		{Name: "c", Objective: "join", DependsOn: []string{"a", "b"}},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	recorder := &recordingScanEventRecorder{}
	reconciler.Recorder = recorder

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	running := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "a")

	annotateSecurityScanCancel(t, k8sClient, scan, "stop-1")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	if got := getAgentRun(t, k8sClient, scan.Namespace, running.Name).Annotations[cancelRequestedAnnotation]; got == "" {
		t.Fatalf("running task run %s has no %s annotation", running.Name, cancelRequestedAnnotation)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	exec := updated.Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseCancelled || exec.CompletedAt == nil {
		t.Fatalf("execution = %#v, want Cancelled with a completion time", exec)
	}
	assertExecutionTaskState(t, exec, "a", 0, triggersv1alpha1.SecurityScanTaskStateFailed)
	assertExecutionTaskState(t, exec, "b", 0, triggersv1alpha1.SecurityScanTaskStateSkipped)
	assertExecutionTaskState(t, exec, "c", 0, triggersv1alpha1.SecurityScanTaskStateSkipped)
	if got := executionTask(t, exec, "a", 0).LastError; got != securityScanCancelMessage {
		t.Fatalf("cancelled task a LastError = %q, want %q", got, securityScanCancelMessage)
	}
	if updated.Status.LastCancelToken != "stop-1" || updated.Status.Phase != "Completed" || updated.Status.LastError != "" {
		t.Fatalf("status after cancel = %+v, want consumed token, phase Completed, no error", updated.Status)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, securityScanReasonCancelled)
	if !recorder.has(corev1.EventTypeWarning, "ScanCancelled") {
		t.Fatalf("events = %#v, want a warning ScanCancelled event", recorder.events)
	}

	// The cancelled execution is terminal, so the next pass publishes the
	// terminal side effects and must not schedule the skipped tasks.
	before := len(securityScanRuns(t, k8sClient, scan.Namespace))
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if after := len(securityScanRuns(t, k8sClient, scan.Namespace)); after != before {
		t.Fatalf("AgentRuns after cancel = %d, want %d (no new task runs)", after, before)
	}
	if phase := getSecurityScan(t, k8sClient, scan).Status.LastExecution.Phase; phase != triggersv1alpha1.SecurityScanExecutionPhaseCancelled {
		t.Fatalf("execution phase = %q, want it to stay Cancelled", phase)
	}
}

func TestSecurityScanCancelTokenIsConsumedExactlyOnce(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "a", Objective: "inspect a"}}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	running := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "a")

	annotateSecurityScanCancel(t, k8sClient, scan, "stop-1")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	// Clear the run's cancel request and revive the execution: only a token
	// that was never consumed may stop the scan a second time.
	run := getAgentRun(t, k8sClient, scan.Namespace, running.Name)
	delete(run.Annotations, cancelRequestedAnnotation)
	if err := k8sClient.Update(context.Background(), run); err != nil {
		t.Fatalf("Update(AgentRun) error = %v", err)
	}
	revived := getSecurityScan(t, k8sClient, scan)
	revived.Status.LastExecution.Phase = triggersv1alpha1.SecurityScanExecutionPhaseRunning
	revived.Status.LastExecution.CompletedAt = nil
	revived.Status.LastExecution.Tasks[0].State = triggersv1alpha1.SecurityScanTaskStateRunning
	if err := k8sClient.Status().Update(context.Background(), revived); err != nil {
		t.Fatalf("Status().Update(SecurityScan) error = %v", err)
	}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if got := getAgentRun(t, k8sClient, scan.Namespace, running.Name).Annotations[cancelRequestedAnnotation]; got != "" {
		t.Fatalf("run re-cancelled by the already consumed token (annotation %q)", got)
	}
	if phase := getSecurityScan(t, k8sClient, scan).Status.LastExecution.Phase; phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning {
		t.Fatalf("execution phase = %q, want Running: the consumed token must not cancel again", phase)
	}
}

func TestSecurityScanCancelStopsCoordinatorRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Generation = 1
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
	recorder := &recordingScanEventRecorder{}
	reconciler.Recorder = recorder

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	dispatched := getSecurityScan(t, k8sClient, scan)
	if dispatched.Status.LastRunName == "" {
		t.Fatalf("status = %+v, want a dispatched coordinator run", dispatched.Status)
	}

	annotateSecurityScanCancel(t, k8sClient, scan, "stop-1")
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	run := getAgentRun(t, k8sClient, scan.Namespace, dispatched.Status.LastRunName)
	if run.Annotations[cancelRequestedAnnotation] == "" {
		t.Fatalf("coordinator run %s has no %s annotation", run.Name, cancelRequestedAnnotation)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastCancelToken != "stop-1" || updated.Status.Phase != "Completed" || updated.Status.LastError != "" {
		t.Fatalf("status after cancel = %+v, want consumed token, phase Completed, no error", updated.Status)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, securityScanReasonCancelled)
	if !recorder.has(corev1.EventTypeWarning, "ScanCancelled") {
		t.Fatalf("events = %#v, want a warning ScanCancelled event", recorder.events)
	}

	// The one-shot dispatch must not replace the run the user just stopped.
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1 (no replacement run after a cancel)", len(runs))
	}
}

func TestSecurityScanCancelledExecutionIsNotResumable(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "a", Objective: "inspect a"},
		{Name: "b", Objective: "join", DependsOn: []string{"a"}},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	annotateSecurityScanCancel(t, k8sClient, scan, "stop-1")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	before := len(securityScanRuns(t, k8sClient, scan.Namespace))

	annotateSecurityScan(t, k8sClient, scan, triggersv1alpha1.SecurityScanResumeAnnotation, "resume-1")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	updated := getSecurityScan(t, k8sClient, scan)
	exec := updated.Status.LastExecution
	if exec.LastResumeToken != "resume-1" {
		t.Fatalf("LastResumeToken = %q, want the resume token recorded", exec.LastResumeToken)
	}
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseCancelled {
		t.Fatalf("execution phase = %q, want it to stay Cancelled after a resume request", exec.Phase)
	}
	assertExecutionTaskState(t, exec, "a", 0, triggersv1alpha1.SecurityScanTaskStateFailed)
	assertExecutionTaskState(t, exec, "b", 0, triggersv1alpha1.SecurityScanTaskStateSkipped)
	if after := len(securityScanRuns(t, k8sClient, scan.Namespace)); after != before {
		t.Fatalf("AgentRuns after resume request = %d, want %d (a stopped campaign is never resumed)", after, before)
	}
}

// Suspending a scan does not stop the work already in flight, so a stop must
// still be honoured on a suspended scan — and must never be left pending to
// hit a later run once the scan is resumed.
func TestSecurityScanCancelIsHonouredWhileSuspended(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "a", Objective: "inspect a"},
		{Name: "b", Objective: "join", DependsOn: []string{"a"}},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	records := map[string]*store.SecurityScanRecord{}
	reconciler.Findings = securityScanFindingStore{securityScanRecordStubStore: securityScanRecordStubStore{scanRecords: records}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	running := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "a")

	suspended := getSecurityScan(t, k8sClient, scan)
	suspended.Spec.Suspend = true
	if err := k8sClient.Update(context.Background(), suspended); err != nil {
		t.Fatalf("Update(SecurityScan suspend) error = %v", err)
	}
	annotateSecurityScanCancel(t, k8sClient, scan, "stop-1")
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if got := getAgentRun(t, k8sClient, scan.Namespace, running.Name).Annotations[cancelRequestedAnnotation]; got == "" {
		t.Fatalf("running task run %s was not cancelled while the scan was suspended", running.Name)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastCancelToken != "stop-1" {
		t.Fatalf("LastCancelToken = %q, want the token consumed while suspended", updated.Status.LastCancelToken)
	}
	if phase := updated.Status.LastExecution.Phase; phase != triggersv1alpha1.SecurityScanExecutionPhaseCancelled {
		t.Fatalf("execution phase = %q, want Cancelled", phase)
	}
	// The stop settles the scans-list row in the same reconcile: the paths
	// that finalize a terminal execution all sit below the suspend gate, so
	// deferring it would leave the record "running" until the scan is resumed
	// — forever, if it never is.
	key := scan.Namespace + "/" + securityScanExecutionRecordName(scan.Name, updated.Status.LastExecution.ID)
	rec := records[key]
	if rec == nil || rec.Status != "cancelled" || rec.CompletedAt == nil {
		t.Fatalf("scan record %q = %+v, want status=cancelled with completedAt; records = %v", key, rec, records)
	}
}

// A stop must also settle a running post-script pipeline: its live run is
// cancelled and no job entry survives that the engine would keep polling.
func TestSecurityScanCancelStopsRunningPostScriptJob(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", Prompt: "Build a proof of concept."},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconciler.Findings = &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical"),
		postScriptTestFinding("00000000-0000-0000-0000-0000000000b1", "fp-beta", "high"),
	}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan) // materialization barrier
	reconcileDeterministicSecurityScan(t, reconciler, scan) // dispatch under parallelism 1

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if postScriptJob(t, exec, "fp-alpha").State != triggersv1alpha1.SecurityScanPostScriptStateRunning ||
		postScriptJob(t, exec, "fp-beta").State != triggersv1alpha1.SecurityScanPostScriptStatePending {
		t.Fatalf("post-script jobs = %#v, want one running and one pending pipeline", exec.PostScriptJobs)
	}
	jobRun := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate", "fp-alpha")

	annotateSecurityScanCancel(t, k8sClient, scan, "stop-1")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	if got := getAgentRun(t, k8sClient, scan.Namespace, jobRun.Name).Annotations[cancelRequestedAnnotation]; got == "" {
		t.Fatalf("running post-script run %s has no %s annotation", jobRun.Name, cancelRequestedAnnotation)
	}
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseCancelled {
		t.Fatalf("execution phase = %q, want Cancelled", exec.Phase)
	}
	alpha := postScriptJob(t, exec, "fp-alpha")
	if alpha.State != triggersv1alpha1.SecurityScanPostScriptStateFailed || alpha.FinishedAt == nil || alpha.LastError != securityScanCancelMessage {
		t.Fatalf("stopped post-script job = %#v, want Failed with the stop reason and a finish time", alpha)
	}
	beta := postScriptJob(t, exec, "fp-beta")
	if beta.State != triggersv1alpha1.SecurityScanPostScriptStateSkipped || beta.FinishedAt == nil || beta.Result != securityScanCancelMessage {
		t.Fatalf("pending post-script job = %#v, want Skipped with the stop reason and a finish time", beta)
	}
}

func TestFinalizeExecutionScanRecordsSettlesCancelledExecution(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "a", Objective: "inspect a"}}, 1)
	reconciler, _, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	records := map[string]*store.SecurityScanRecord{}
	reconciler.Findings = securityScanFindingStore{securityScanRecordStubStore: securityScanRecordStubStore{scanRecords: records}}

	completed := now.Add(time.Minute)
	exec := &triggersv1alpha1.SecurityScanExecutionStatus{
		ID:          "20260101-abc",
		Mode:        triggersv1alpha1.SecurityScanExecutionModeDeterministic,
		Phase:       triggersv1alpha1.SecurityScanExecutionPhaseCancelled,
		CompletedAt: &metav1.Time{Time: completed},
		Tasks: []triggersv1alpha1.SecurityScanTaskExecutionStatus{
			{Name: "a", RunName: "secscan-a-run"},
		},
	}
	started := now
	for _, runName := range []string{securityScanExecutionRecordName(scan.Name, exec.ID), "secscan-a-run"} {
		records[scan.Namespace+"/"+runName] = &store.SecurityScanRecord{
			Namespace: scan.Namespace, ScanName: scan.Name, RunName: runName,
			Status: "running", StartedAt: &started,
		}
	}

	reconciler.finalizeExecutionScanRecords(context.Background(), scan, exec)

	// A stopped campaign is not a failed one: the scans list must report it
	// as cancelled, exactly like the coordinator path does.
	for key, rec := range records {
		if rec.Status != "cancelled" || rec.CompletedAt == nil || !rec.CompletedAt.Equal(completed.UTC()) {
			t.Fatalf("record %q = %+v, want status=cancelled completed at %s", key, rec, completed.UTC())
		}
	}
}

// A run-now request queued before the stop must not start a replacement for
// the execution the user just stopped.
func TestSecurityScanCancelSupersedesPendingRunNowToken(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "a", Objective: "inspect a"}}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	execID := getSecurityScan(t, k8sClient, scan).Status.LastExecution.ID
	before := len(securityScanRuns(t, k8sClient, scan.Namespace))

	annotateSecurityScan(t, k8sClient, scan, triggersv1alpha1.SecurityScanRunNowAnnotation, "run-2")
	annotateSecurityScanCancel(t, k8sClient, scan, "stop-1")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastManualRunToken != "run-2" {
		t.Fatalf("LastManualRunToken = %q, want the queued run-now token superseded by the stop", updated.Status.LastManualRunToken)
	}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	after := getSecurityScan(t, k8sClient, scan)
	if after.Status.LastExecution.ID != execID || after.Status.LastExecution.Phase != triggersv1alpha1.SecurityScanExecutionPhaseCancelled {
		t.Fatalf("execution = %#v, want the stopped execution %q kept Cancelled", after.Status.LastExecution, execID)
	}
	if got := len(securityScanRuns(t, k8sClient, scan.Namespace)); got != before {
		t.Fatalf("AgentRuns = %d, want %d (the queued run-now token must not start a replacement run)", got, before)
	}
}

func TestSecurityScanCancelSupersedesPendingRunNowTokenForCoordinatorRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Generation = 1
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	annotateSecurityScan(t, k8sClient, scan, triggersv1alpha1.SecurityScanRunNowAnnotation, "run-2")
	annotateSecurityScanCancel(t, k8sClient, scan, "stop-1")
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastManualRunToken != "run-2" {
		t.Fatalf("LastManualRunToken = %q, want the queued run-now token superseded by the stop", updated.Status.LastManualRunToken)
	}
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1 (the queued run-now token must not start a replacement run)", len(runs))
	}
}

// A coordinator run the user stopped keeps its stop reported: the
// once-per-generation path must not re-mark the scan Ready afterwards.
func TestSecurityScanStoppedCoordinatorRunStaysCancelled(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Generation = 1
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runName := getSecurityScan(t, k8sClient, scan).Status.LastRunName
	annotateSecurityScanCancel(t, k8sClient, scan, "stop-1")
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// The platform settles the cancelled run, and the scan reconciles again.
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, runName, platformv1alpha1.AgentRunPhaseCancelled, "", "")
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	assertSecurityScanCondition(t, getSecurityScan(t, k8sClient, scan), metav1.ConditionFalse, securityScanReasonCancelled)
}
