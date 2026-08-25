package triggers

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Deterministic execution: when spec.execution.mode is "deterministic" the
// controller compiles the workflow DAG into per-task AgentRuns instead of
// seeding one coordinating run. All execution state lives in
// status.lastExecution plus the task AgentRuns themselves; every reconcile
// re-derives its decisions from cluster state, so the engine is idempotent
// under repeated reconciles and controller restarts.
const (
	// securityScanTaskModeTemplate is the ModeTemplate applied to
	// deterministic task runs when spec.defaults.modeRef is not set.
	securityScanTaskModeTemplate = "security-scan-task"
	// webSecurityScanTaskModeTemplate is the unclamped task mode used only for
	// URL/domain targets.
	webSecurityScanTaskModeTemplate = "web-security-scan-task"

	// securityScanTaskLabel marks a task AgentRun with its workflow task
	// name; securityScanTaskInstanceLabel carries the 0-based instance.
	securityScanTaskLabel         = "security.gratefulagents.dev/scan-task"
	securityScanTaskInstanceLabel = "security.gratefulagents.dev/scan-task-instance"

	// securityScanTaskOutputSchemaAnnotation carries the task's outputSchema
	// JSON on its AgentRun; the worker gates submit_task_output registration
	// and validation on it.
	securityScanTaskOutputSchemaAnnotation = "security.gratefulagents.dev/task-output-schema"

	// securityScanTaskRetryHistoryLimit caps the per-task attempt history;
	// the oldest entries are dropped first.
	securityScanTaskRetryHistoryLimit = 10

	// securityScanTaskRetryBackoffCap bounds the exponential retry backoff.
	securityScanTaskRetryBackoffCap = 15 * time.Minute

	// securityScanExecutionPollInterval is the steady-state requeue while an
	// execution has active task runs (run phase changes also trigger
	// reconciles through the AgentRun watch).
	securityScanExecutionPollInterval = 30 * time.Second

	// securityScanExecutionMaxTaskEntries caps status.lastExecution.tasks so
	// the SecurityScan object stays well below the etcd object-size limit.
	// Validation enforces the same planned-instance budget up front
	// (MaxSecurityWorkflowPlannedInstances); the engine re-enforces it at
	// expansion time. Legacy expansions truncate; targetRuns fails closed.
	securityScanExecutionMaxTaskEntries = triggersv1alpha1.MaxSecurityWorkflowPlannedInstances

	// securityScanMaxRenderedObjectiveBytes caps a fully rendered task
	// objective: a fan-in objective can interpolate many 64KiB upstream
	// outputs, so the assembled prompt must be bounded explicitly.
	securityScanMaxRenderedObjectiveBytes = 256 * 1024

	// securityScanMaxStructuredOutputBytes matches the AgentRun structured
	// output contract and prevents controller-native reducers from bloating the CR.
	securityScanMaxStructuredOutputBytes = 64 * 1024

	// securityScanReasonOutputContractUnmet marks a task run that succeeded
	// without publishing the structured output its schema requires.
	securityScanReasonOutputContractUnmet = "OutputContractUnmet"
)

// securityScanExecutionTerminal reports whether an execution phase is final.
// A cancelled execution is terminal like a failed one: the engine must stop
// advancing it, and the terminal side effects must run exactly once.
func securityScanExecutionTerminal(phase string) bool {
	return phase == triggersv1alpha1.SecurityScanExecutionPhaseSucceeded ||
		phase == triggersv1alpha1.SecurityScanExecutionPhaseFailed ||
		phase == triggersv1alpha1.SecurityScanExecutionPhaseCancelled
}

// securityScanExecutionActive reports whether exec is a live deterministic
// execution this controller must keep advancing.
func securityScanExecutionActive(exec *triggersv1alpha1.SecurityScanExecutionStatus) bool {
	return exec != nil &&
		exec.Mode == triggersv1alpha1.SecurityScanExecutionModeDeterministic &&
		!securityScanExecutionTerminal(exec.Phase)
}

// securityScanTaskTerminal reports whether a task-instance state is final.
func securityScanTaskTerminal(state string) bool {
	switch state {
	case triggersv1alpha1.SecurityScanTaskStateSucceeded,
		triggersv1alpha1.SecurityScanTaskStateFailed,
		triggersv1alpha1.SecurityScanTaskStateSkipped:
		return true
	}
	return false
}

// reconcileDeterministic is the deterministic-mode counterpart of the
// coordinator dispatch: it resumes failed executions on request, advances the
// live execution, publishes terminal side effects exactly once, and starts
// new executions from the manual, event, one-shot, and scheduled paths.
func (r *SecurityScanReconciler) reconcileDeterministic(ctx context.Context, scan *triggersv1alpha1.SecurityScan) (ctrl.Result, error) {
	if token := pendingSecurityScanResumeToken(scan); token != "" {
		return r.reconcileExecutionResume(ctx, scan, token)
	}

	if securityScanExecutionActive(scan.Status.LastExecution) {
		return r.advanceDeterministicExecution(ctx, scan)
	}

	retrySideEffects := false
	if exec := scan.Status.LastExecution; exec != nil &&
		exec.Mode == triggersv1alpha1.SecurityScanExecutionModeDeterministic &&
		securityScanExecutionTerminal(exec.Phase) {
		retrySideEffects = r.finishTerminalExecution(ctx, scan, exec)
	}

	res, err := r.dispatchDeterministic(ctx, scan)
	if err != nil {
		return res, err
	}
	if retrySideEffects && (res.RequeueAfter == 0 || res.RequeueAfter > time.Minute) {
		res.RequeueAfter = time.Minute
	}
	return res, nil
}

// pendingSecurityScanResumeToken returns the resume-scan annotation token
// when it has not been consumed yet. Consumed-token semantics mirror
// LastManualRunToken: the token is recorded in
// status.lastExecution.lastResumeToken once processed, so the request is
// idempotent and durable across controller restarts.
func pendingSecurityScanResumeToken(scan *triggersv1alpha1.SecurityScan) string {
	exec := scan.Status.LastExecution
	if exec == nil || exec.Mode != triggersv1alpha1.SecurityScanExecutionModeDeterministic {
		return ""
	}
	token := strings.TrimSpace(scan.Annotations[triggersv1alpha1.SecurityScanResumeAnnotation])
	if token == "" || token == exec.LastResumeToken {
		return ""
	}
	return token
}

// reconcileExecutionResume consumes one resume token. Only a Failed
// execution is actually resumed: every Failed and Skipped task instance is
// reset to Pending with a refreshed retry budget. The attempts counter stays
// cumulative — budgets.maxModelJobs accounting must never forget prior runs
// — so the refreshed allowance is granted by recording the pre-resume count
// in resumeBaselineAttempts; the full failure history stays in retries and
// run names of the resumed cycle carry a resume-token discriminator so they
// never collide with earlier attempts. A token arriving in any other phase
// is recorded without action so it cannot fire later by surprise — notably a
// Cancelled execution, which is never resurrected: a stopped campaign is
// restarted with a fresh run, not resumed mid-flight.
func (r *SecurityScanReconciler) reconcileExecutionResume(ctx context.Context, scan *triggersv1alpha1.SecurityScan, token string) (ctrl.Result, error) {
	execID := scan.Status.LastExecution.ID
	resumed := scan.Status.LastExecution.Phase == triggersv1alpha1.SecurityScanExecutionPhaseFailed
	if err := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
		exec := fresh.Status.LastExecution
		if exec == nil || exec.ID != execID {
			return
		}
		exec.LastResumeToken = token
		if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed {
			return
		}
		exec.Phase = triggersv1alpha1.SecurityScanExecutionPhaseRunning
		exec.CompletedAt = nil
		for i := range exec.Tasks {
			task := &exec.Tasks[i]
			switch task.State {
			case triggersv1alpha1.SecurityScanTaskStateFailed, triggersv1alpha1.SecurityScanTaskStateSkipped:
				task.State = triggersv1alpha1.SecurityScanTaskStatePending
				task.ResumeBaselineAttempts = task.Attempts
				task.NextRetryTime = nil
				task.LastError = ""
				task.FinishedAt = nil
			}
		}
		// Failed post-script jobs are reset with their tasks: a resume is a
		// request to complete the campaign, and a report gated on jobs that
		// stay Failed forever would keep disclosing the same coverage gap no
		// retry can ever close. Only the gaps THOSE jobs recorded are
		// retracted (matched by their source prefix): a wholesale clear would
		// also drop gaps nothing re-derives — expandFanOuts skips
		// already-expanded tasks, so a fan-out truncation is never re-stated,
		// and materializePostScripts runs once, so a truncated matrix is
		// never re-stated either — and the sink prompt would then present
		// partial coverage as authoritative. Attempts stay cumulative
		// (budgets.maxModelJobs must never forget prior runs), so a resume
		// grants each failed job exactly one further attempt before its
		// exhausted retry budget marks it Failed again.
		var reset []triggersv1alpha1.SecurityScanPostScriptJobStatus
		for i := range exec.PostScriptJobs {
			job := &exec.PostScriptJobs[i]
			if job.State == triggersv1alpha1.SecurityScanPostScriptStateFailed {
				job.State = triggersv1alpha1.SecurityScanPostScriptStatePending
				job.LastError = ""
				job.Result = ""
				job.FinishedAt = nil
				reset = append(reset, *job)
			}
		}
		securityScanRetractPostScriptJobGaps(exec, reset)
		fresh.Status.Phase = "Running"
		fresh.Status.LastError = ""
		setSecurityScanCondition(fresh, metav1.ConditionTrue, "ExecutionResumed", "Failed execution tasks reset for a fresh attempt")
	}); err != nil {
		return ctrl.Result{}, err
	}
	if resumed {
		// The terminal finalizer marked the execution's scans-list row
		// failed; the resumed campaign is running again, so reopen the row —
		// otherwise the list keeps showing "failed" while tasks run and the
		// settled-row guards would block the eventual outcome from landing.
		r.reopenExecutionScanRecord(ctx, scan, execID)
		r.recordScanEvent(scan, corev1.EventTypeNormal, "ExecutionResumed", fmt.Sprintf("execution %s resumed by token", execID))
	}
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

// reopenExecutionScanRecord flips a resumed execution's persisted scan
// record back to "running" and clears its completion time, preserving any
// summary/counts a previous cycle recorded. Best-effort: on error the row
// stays terminal until the resumed execution finishes and the terminal
// finalizer cannot touch it, matching the pre-resume state.
func (r *SecurityScanReconciler) reopenExecutionScanRecord(ctx context.Context, scan *triggersv1alpha1.SecurityScan, execID string) {
	if r.Findings == nil {
		return
	}
	log := logf.FromContext(ctx)
	recordName := securityScanExecutionRecordName(scan.Name, execID)
	rec, err := r.Findings.GetSecurityScan(ctx, scan.Namespace, recordName)
	if err != nil {
		log.Error(err, "failed to load scan record for resume", "record", recordName)
		return
	}
	if rec == nil || (rec.Status == "running" && rec.CompletedAt == nil) {
		return
	}
	rec.Status = "running"
	rec.CompletedAt = nil
	if _, err := r.Findings.UpsertSecurityScan(ctx, rec); err != nil {
		log.Error(err, "failed to reopen scan record for resumed execution", "record", recordName)
	}
}

// dispatchDeterministic mirrors the coordinator dispatch order for
// deterministic mode. Executions always serialize: status.lastExecution
// holds exactly one execution, so a new dispatch replaces it only when the
// previous one is terminal (a live one is handled by the advance path before
// this is reached).
func (r *SecurityScanReconciler) dispatchDeterministic(ctx context.Context, scan *triggersv1alpha1.SecurityScan) (ctrl.Result, error) {
	if token := pendingManualRunToken(scan); token != "" {
		return r.deterministicRunNow(ctx, scan, token)
	}
	if scan.Spec.ManualOnly {
		return r.reconcileManualOnly(ctx, scan)
	}
	if ev := pendingTriggerEvent(scan); ev != nil {
		return r.deterministicTriggerEvent(ctx, scan, ev)
	}
	if strings.TrimSpace(scan.Spec.Schedule) == "" {
		return r.deterministicOneShot(ctx, scan)
	}
	return r.deterministicScheduled(ctx, scan)
}

func (r *SecurityScanReconciler) deterministicRunNow(ctx context.Context, scan *triggersv1alpha1.SecurityScan, token string) (ctrl.Result, error) {
	externalID := "manual-" + securityScanManualRunSuffix(token)
	if blocked, res, err := r.deterministicConcurrencyBlocked(ctx, scan, externalID, func(fresh *triggersv1alpha1.SecurityScan, msg string) {
		fresh.Status.LastManualRunToken = token
		fresh.Status.LastError = msg
	}); blocked {
		return res, err
	}
	oneShot := strings.TrimSpace(scan.Spec.Schedule) == ""
	generation := scan.Generation
	return r.startDeterministicExecution(ctx, scan, externalID, func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.LastManualRunToken = token
		fresh.Status.ManualRunsCreated++
		if oneShot {
			fresh.Status.ObservedGeneration = generation
		}
		setSecurityScanCondition(fresh, metav1.ConditionTrue, "ManualRunStarted", "Manual deterministic execution started")
	})
}

func (r *SecurityScanReconciler) deterministicTriggerEvent(ctx context.Context, scan *triggersv1alpha1.SecurityScan, ev *SecurityScanTriggerEvent) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	// Fork gate: identical rules to the coordinator event path. An untrusted
	// fork contribution is skipped unless explicitly allowed, and allowed
	// fork executions never receive repository credentials (enforced in
	// buildScanRunBase for every task run).
	allowForks := scan.Spec.Triggers != nil && scan.Spec.Triggers.AllowForks
	if ev.Fork && !allowForks {
		msg := fmt.Sprintf("skipped %s event for fork head repository %s (revision %s): set spec.triggers.allowForks to scan untrusted fork contributions without repository credentials",
			ev.Source, ev.HeadRepo, ev.Revision)
		log.Info("skipping fork-originated security scan event", "headRepo", ev.HeadRepo, "revision", ev.Revision)
		r.recordScanEvent(scan, corev1.EventTypeWarning, "ForkPullRequestSkipped", msg)
		if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastEventToken = ev.Token
			fresh.Status.LastEventRevision = ev.Revision
			fresh.Status.LastError = msg
			setSecurityScanCondition(fresh, metav1.ConditionFalse, "ForkPullRequestSkipped", msg)
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	externalID := "event-" + securityScanManualRunSuffix(ev.Token)
	if blocked, res, err := r.deterministicConcurrencyBlocked(ctx, scan, externalID, func(fresh *triggersv1alpha1.SecurityScan, msg string) {
		fresh.Status.LastEventToken = ev.Token
		fresh.Status.LastEventRevision = ev.Revision
		fresh.Status.LastError = msg
	}); blocked {
		return res, err
	}

	runCtx := &securityScanRunContext{Event: ev}
	if scan.Spec.Triggers != nil && scan.Spec.Triggers.DiffScope {
		runCtx.ChangedFiles, runCtx.DiffFallback = r.eventChangedFiles(ctx, scan, ev)
	}
	oneShot := strings.TrimSpace(scan.Spec.Schedule) == ""
	generation := scan.Generation
	msg := fmt.Sprintf("Deterministic execution started for %s event at revision %s", ev.Source, ev.Revision)
	if runCtx.DiffFallback != "" {
		msg += "; diff scope fell back to a full-repository scan: " + runCtx.DiffFallback
	}
	res, err := r.startDeterministicExecution(ctx, scan, externalID, func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.LastEventToken = ev.Token
		fresh.Status.LastEventRevision = ev.Revision
		fresh.Status.EventRunsCreated++
		if oneShot {
			fresh.Status.ObservedGeneration = generation
		}
		setSecurityScanCondition(fresh, metav1.ConditionTrue, "EventRunStarted", msg)
	})
	if err == nil {
		r.recordScanEvent(scan, corev1.EventTypeNormal, "EventRunStarted", msg)
	}
	return res, err
}

func (r *SecurityScanReconciler) deterministicOneShot(ctx context.Context, scan *triggersv1alpha1.SecurityScan) (ctrl.Result, error) {
	if scan.Status.ObservedGeneration == scan.Generation && scan.Status.LastExecution != nil {
		// Already dispatched for this generation; the execution is terminal
		// here (a live one never reaches dispatch).
		if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.Phase = "Completed"
			fresh.Status.LastError = ""
			setSecurityScanCondition(fresh, metav1.ConditionTrue, "ScanUpToDate", "Scan already ran for the current spec generation")
			applySecurityScanExecutionOutcomeCondition(fresh)
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	generation := scan.Generation
	return r.startDeterministicExecution(ctx, scan, fmt.Sprintf("generation-%d", generation), func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.ObservedGeneration = generation
		setSecurityScanCondition(fresh, metav1.ConditionTrue, "ScanStarted", "Deterministic execution started")
	})
}

func (r *SecurityScanReconciler) deterministicScheduled(ctx context.Context, scan *triggersv1alpha1.SecurityScan) (ctrl.Result, error) {
	schedule, observedTimeZone, err := parseCronSchedule(scan.Spec.Schedule, scan.Spec.TimeZone)
	observedSchedule := strings.TrimSpace(scan.Spec.Schedule)
	if err != nil {
		if statusErr := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastError = err.Error()
			setSecurityScanCondition(fresh, metav1.ConditionFalse, "InvalidSchedule", err.Error())
		}); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	now := r.now()
	scheduledTime := nextSecurityScanScheduleTime(scan, schedule, observedSchedule, observedTimeZone, now)
	if scheduledTime.IsZero() {
		err := fmt.Errorf("failed to compute next schedule time")
		if statusErr := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastError = err.Error()
			setSecurityScanCondition(fresh, metav1.ConditionFalse, "ScheduleError", err.Error())
		}); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	if scheduledTime.After(now) {
		next := metav1.NewTime(scheduledTime)
		if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
			if fresh.Status.Phase == "Running" && !securityScanExecutionActive(fresh.Status.LastExecution) {
				fresh.Status.Phase = "Completed"
			}
			if fresh.Status.Phase == "" || fresh.Status.Phase == "Suspended" {
				fresh.Status.Phase = "Scheduled"
			}
			fresh.Status.NextScheduleTime = &next
			fresh.Status.ObservedSchedule = observedSchedule
			fresh.Status.ObservedTimeZone = observedTimeZone
			fresh.Status.ObservedGeneration = scan.Generation
			fresh.Status.LastError = ""
			setSecurityScanCondition(fresh, metav1.ConditionTrue, "Scheduled", "SecurityScan schedule is valid")
			applySecurityScanExecutionOutcomeCondition(fresh)
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: requeueAfter(scheduledTime.Sub(now))}, nil
	}

	scheduledID := scheduledTime.UTC().Format(time.RFC3339)
	if blocked, res, err := r.deterministicConcurrencyBlocked(ctx, scan, scheduledID, func(fresh *triggersv1alpha1.SecurityScan, msg string) {
		nextScheduledTime := schedule.Next(now)
		next := metav1.NewTime(nextScheduledTime)
		fresh.Status.NextScheduleTime = &next
		fresh.Status.ObservedSchedule = observedSchedule
		fresh.Status.ObservedTimeZone = observedTimeZone
		fresh.Status.ObservedGeneration = scan.Generation
		fresh.Status.LastError = msg
	}); blocked {
		return res, err
	}

	nextScheduledTime := schedule.Next(now)
	next := metav1.NewTime(nextScheduledTime)
	generation := scan.Generation
	res, err := r.startDeterministicExecution(ctx, scan, scheduledID, func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.NextScheduleTime = &next
		fresh.Status.ObservedSchedule = observedSchedule
		fresh.Status.ObservedTimeZone = observedTimeZone
		fresh.Status.ObservedGeneration = generation
		setSecurityScanCondition(fresh, metav1.ConditionTrue, "Scheduled", "SecurityScan schedule is valid")
	})
	if err != nil {
		return res, err
	}
	if res.RequeueAfter == 0 || res.RequeueAfter > nextScheduledTime.Sub(now) {
		res.RequeueAfter = requeueAfter(nextScheduledTime.Sub(now))
	}
	return res, nil
}

// deterministicConcurrencyBlocked applies Forbid semantics before starting a
// new execution: any non-terminal scan AgentRun (for example a coordinator
// run from a previous mode, or task runs of an older execution still
// draining) blocks the dispatch and consumes the request via record.
func (r *SecurityScanReconciler) deterministicConcurrencyBlocked(ctx context.Context, scan *triggersv1alpha1.SecurityScan, externalID string, record func(fresh *triggersv1alpha1.SecurityScan, msg string)) (bool, ctrl.Result, error) {
	if scan.Spec.ConcurrencyPolicy != "" && scan.Spec.ConcurrencyPolicy != triggersv1alpha1.SecurityScanConcurrencyForbid {
		// concurrencyPolicy Allow: a lingering run does not block, but a
		// live execution still serializes (handled by the advance path).
		return false, ctrl.Result{}, nil
	}
	activeRun, err := r.activeScanRun(ctx, scan, externalID)
	if err != nil {
		return true, ctrl.Result{}, err
	}
	if activeRun == nil {
		return false, ctrl.Result{}, nil
	}
	msg := fmt.Sprintf("execution %s skipped: previous run %s still active", externalID, activeRun.Name)
	logf.FromContext(ctx).Info("skipping deterministic execution because a previous run is still active", "activeRun", activeRun.Name)
	if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
		record(fresh, msg)
		setSecurityScanCondition(fresh, metav1.ConditionFalse, "ConcurrencyBlocked", msg)
	}); err != nil {
		return true, ctrl.Result{}, err
	}
	return true, ctrl.Result{RequeueAfter: time.Minute}, nil
}

// startDeterministicExecution validates the workflow and parameters, plans
// the task instances, records status.lastExecution, and immediately advances
// once so the first task runs are created in the same reconcile.
// status.lastRunName intentionally stays empty for deterministic executions:
// the per-task runs are tracked in lastExecution, and the run-centric
// coordinator plumbing (lastRunTerminal, publishRunCheck, notifyRunFindings)
// must not observe a single task run as "the" scan run.
func (r *SecurityScanReconciler) startDeterministicExecution(ctx context.Context, scan *triggersv1alpha1.SecurityScan, externalID string, mutate func(fresh *triggersv1alpha1.SecurityScan)) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	recordFailure := func(err error) (ctrl.Result, error) {
		log.Error(err, "failed to start deterministic execution", "execution", externalID)
		reason := securityScanRunFailureReason(err)
		if statusErr := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastError = err.Error()
			setSecurityScanCondition(fresh, metav1.ConditionFalse, reason, err.Error())
		}); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	resolved, err := resolveSecurityScanRefs(ctx, r.Client, scan)
	if err != nil {
		return recordFailure(err)
	}
	workflow := resolved.spec.Workflow
	if errs := triggersv1alpha1.ValidateSecurityWorkflowTasks(workflow); len(errs) != 0 {
		return recordFailure(&securityScanRefError{reason: securityScanReasonInvalidSpec, message: "workflow is invalid: " + errs[0].Error()})
	}
	if err := validateSecurityScanTaskSkillRefs(ctx, r.Client, scan.Namespace, scan.Spec.Defaults.SkillRefs, workflow); err != nil {
		return recordFailure(err)
	}
	// Required or referenced parameters without a value fail the dispatch
	// non-retryably before any task run exists.
	if _, err := resolveSecurityScanParameters(resolved); err != nil {
		return recordFailure(err)
	}

	now := metav1.NewTime(r.now())
	exec := planSecurityScanExecution(workflow, externalID, resolved.spec.EffectiveParallelism(), now)
	if resolved.program != nil {
		programSnapshot := *resolved.program
		exec.SecurityProgramSnapshot = &programSnapshot
		for i := range resolved.refs {
			if resolved.refs[i].Kind == "SecurityProgram" {
				programRef := resolved.refs[i]
				exec.SecurityProgramResolvedRef = &programRef
				break
			}
		}
	}
	if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.Phase = "Running"
		fresh.Status.LastRunName = ""
		fresh.Status.LastScanTime = &now
		fresh.Status.LastError = ""
		fresh.Status.LastExecution = exec
		fresh.Status.LastResolvedRefs = append([]triggersv1alpha1.SecurityScanResolvedRef(nil), resolved.refs...)
		setSecurityScanCondition(fresh, metav1.ConditionTrue, "ExecutionRunning", fmt.Sprintf("Deterministic execution %s is running", exec.ID))
		setSecurityScanCoverageCondition(fresh, metav1.ConditionUnknown, "ExecutionRunning", "coverage completeness is unknown while the execution is running")
		mutate(fresh)
	}); err != nil {
		return ctrl.Result{}, err
	}

	fresh := &triggersv1alpha1.SecurityScan{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(scan), fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if fresh.Status.LastExecution == nil || fresh.Status.LastExecution.ID != exec.ID ||
		!securityScanExecutionActive(fresh.Status.LastExecution) {
		// The cached read has not observed the status write above yet.
		// Advance off the execution value just written instead of
		// dereferencing the stale (possibly nil) cached one; the exec value
		// is authoritative because the write succeeded.
		fresh.Status.LastExecution = exec.DeepCopy()
	}
	// A cached read can also carry the prior execution's conditions. Reapply
	// the running state to the exact object advanceDeterministicExecution will
	// persist so stale CoverageComplete=True/False cannot leak into this run.
	setSecurityScanCondition(fresh, metav1.ConditionTrue, "ExecutionRunning", fmt.Sprintf("Deterministic execution %s is running", exec.ID))
	setSecurityScanCoverageCondition(fresh, metav1.ConditionUnknown, "ExecutionRunning", "coverage completeness is unknown while the execution is running")
	return r.advanceDeterministicExecution(ctx, fresh)
}

// planSecurityScanExecution expands the workflow into the initial
// task-instance list: ensemble repeats expand immediately; forEach tasks get
// a single Pending placeholder instance that is expanded only after their
// source task succeeds. All instances start Pending — the scheduler flips
// entries whose dependencies are not yet satisfied to Blocked on the first
// advance.
func planSecurityScanExecution(workflow []triggersv1alpha1.SecurityScanTask, externalID string, parallelism int32, now metav1.Time) *triggersv1alpha1.SecurityScanExecutionStatus {
	var entries []triggersv1alpha1.SecurityScanTaskExecutionStatus
	// The plan records the workflow snapshot's graph shape (names, edges,
	// fan-out sources) so DAG consumers stay truthful after the source
	// workflow — referenced, inline, or the built-in default — changes.
	plan := make([]triggersv1alpha1.SecurityScanExecutionPlanNode, 0, len(workflow))
	for _, task := range workflow {
		plan = append(plan, triggersv1alpha1.SecurityScanExecutionPlanNode{
			Name:       task.Name,
			DependsOn:  append([]string(nil), task.DependsOn...),
			ForEach:    task.ForEach,
			TargetRuns: task.TargetRuns,
			Reduce:     task.Reduce,
			When:       task.When.DeepCopy(),
		})
		instances := int32(1)
		if task.ForEach == "" {
			instances = task.EffectiveRepeats()
		}
		for i := int32(0); i < instances; i++ {
			entries = append(entries, triggersv1alpha1.SecurityScanTaskExecutionStatus{
				Name:     task.Name,
				Instance: i,
				State:    triggersv1alpha1.SecurityScanTaskStatePending,
			})
		}
	}
	return &triggersv1alpha1.SecurityScanExecutionStatus{
		ID:                   externalID,
		Mode:                 triggersv1alpha1.SecurityScanExecutionModeDeterministic,
		Phase:                triggersv1alpha1.SecurityScanExecutionPhaseRunning,
		EffectiveParallelism: parallelism,
		Tasks:                entries,
		Plan:                 plan,
		StartedAt:            &now,
	}
}

// securityScanExecutionEvent decodes the durable scan-event annotation when
// it belongs to this execution: the annotation stays on the scan after its
// token is consumed, and the execution id binds the execution to exactly one
// event.
func securityScanExecutionEvent(scan *triggersv1alpha1.SecurityScan, exec *triggersv1alpha1.SecurityScanExecutionStatus) *SecurityScanTriggerEvent {
	raw := strings.TrimSpace(scan.Annotations[triggersv1alpha1.SecurityScanEventAnnotation])
	if raw == "" {
		return nil
	}
	ev := &SecurityScanTriggerEvent{}
	if err := json.Unmarshal([]byte(raw), ev); err != nil || ev.Token == "" {
		return nil
	}
	if exec.ID != "event-"+securityScanManualRunSuffix(ev.Token) {
		return nil
	}
	return ev
}

// executionTriggerEvent reconstructs the trigger-event run context for a
// live execution, including the diff-scope file list when configured.
func (r *SecurityScanReconciler) executionTriggerEvent(ctx context.Context, scan *triggersv1alpha1.SecurityScan, exec *triggersv1alpha1.SecurityScanExecutionStatus) *securityScanRunContext {
	ev := securityScanExecutionEvent(scan, exec)
	if ev == nil {
		return nil
	}
	runCtx := &securityScanRunContext{Event: ev}
	if scan.Spec.Triggers != nil && scan.Spec.Triggers.DiffScope {
		runCtx.ChangedFiles, runCtx.DiffFallback = r.eventChangedFiles(ctx, scan, ev)
	}
	return runCtx
}

// advanceDeterministicExecution runs one scheduler pass over the live
// execution: observe terminal task runs, expand fan-outs, propagate skips,
// enforce budgets, launch ready tasks up to the parallelism bound, and fold
// the resulting execution state back into status. Everything is derived from
// cluster state plus status, so the pass is idempotent.
func (r *SecurityScanReconciler) advanceDeterministicExecution(ctx context.Context, scan *triggersv1alpha1.SecurityScan) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	exec := scan.Status.LastExecution.DeepCopy()
	now := metav1.NewTime(r.now())

	// New dispatch requests arriving while an execution is in flight are
	// consumed as ConcurrencyBlocked: executions always serialize.
	var consumeRequest func(fresh *triggersv1alpha1.SecurityScan)
	if token := pendingManualRunToken(scan); token != "" {
		msg := fmt.Sprintf("manual run skipped: execution %s still active", exec.ID)
		consumeRequest = func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastManualRunToken = token
			fresh.Status.LastError = msg
			setSecurityScanCondition(fresh, metav1.ConditionFalse, "ConcurrencyBlocked", msg)
		}
	} else if ev := pendingTriggerEvent(scan); ev != nil {
		msg := fmt.Sprintf("%s event for revision %s skipped: execution %s still active", ev.Source, ev.Revision, exec.ID)
		consumeRequest = func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastEventToken = ev.Token
			fresh.Status.LastEventRevision = ev.Revision
			fresh.Status.LastError = msg
			setSecurityScanCondition(fresh, metav1.ConditionFalse, "ConcurrencyBlocked", msg)
		}
	}
	scheduleMutate := r.skipDueScheduleTick(ctx, scan)

	var requeue time.Duration
	// Program presence as well as program content is part of the execution
	// snapshot. Always discard the live reference while an execution is active;
	// otherwise adding a program to a scan that started without one would alter
	// later task and post-script prompts in that already-running execution.
	scanForResolution := scan.DeepCopy()
	scanForResolution.Spec.SecurityProgramRef = nil
	resolved, err := resolveSecurityScanRefs(ctx, r.Client, scanForResolution)
	if err == nil && exec.SecurityProgramSnapshot != nil {
		programSnapshot := *exec.SecurityProgramSnapshot
		resolved.program = &programSnapshot
		if exec.SecurityProgramResolvedRef != nil {
			programRef := *exec.SecurityProgramResolvedRef
			resolved.refs = append([]triggersv1alpha1.SecurityScanResolvedRef{programRef}, resolved.refs...)
		}
	}
	if err != nil {
		// Only a deterministic spec/reference failure (missing referenced
		// resource, invalid spec, policy violation) fails the execution; a
		// transient API error must requeue and retry instead of permanently
		// failing a healthy execution.
		var refErr *securityScanRefError
		if !errors.As(err, &refErr) {
			return ctrl.Result{}, err
		}
		failSecurityScanExecution(exec, now, "resolving references during execution: "+err.Error())
	} else {
		engine := &securityScanExecutionEngine{
			r:        r,
			scan:     scan,
			resolved: resolved,
			exec:     exec,
			now:      now,
			runs:     map[string]*platformv1alpha1.AgentRun{},
		}
		requeue = engine.advance(ctx)
	}

	execID := exec.ID
	if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
		if scheduleMutate != nil {
			scheduleMutate(fresh)
		}
		if consumeRequest != nil {
			consumeRequest(fresh)
		}
		if fresh.Status.LastExecution == nil || fresh.Status.LastExecution.ID != execID ||
			fresh.Status.LastExecution.Mode != triggersv1alpha1.SecurityScanExecutionModeDeterministic {
			return
		}
		fresh.Status.LastExecution = exec
		if securityScanExecutionTerminal(exec.Phase) {
			fresh.Status.Phase = "Completed"
			applySecurityScanExecutionOutcomeCondition(fresh)
			return
		}
		fresh.Status.Phase = "Running"
		if consumeRequest == nil {
			fresh.Status.LastError = ""
			setSecurityScanCondition(fresh, metav1.ConditionTrue, "ExecutionRunning", fmt.Sprintf("Deterministic execution %s is running", exec.ID))
		}
	}); err != nil {
		return ctrl.Result{}, err
	}

	if securityScanExecutionTerminal(exec.Phase) {
		log.Info("deterministic execution reached a terminal phase", "execution", exec.ID, "phase", exec.Phase)
		// Terminal side effects — including scan-record finalization — run
		// on the next reconcile through finishTerminalExecution, the durable
		// terminal path, off the freshly written status.
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if requeue <= 0 {
		requeue = securityScanExecutionPollInterval
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// applySecurityScanExecutionOutcomeCondition reports terminal readiness and
// distinguishes a complete success from a success with explicit coverage gaps.
// The execution phase remains Succeeded for compatibility; CoverageComplete is
// the additive machine-readable warning signal.
func applySecurityScanExecutionOutcomeCondition(fresh *triggersv1alpha1.SecurityScan) {
	exec := fresh.Status.LastExecution
	if exec == nil || exec.Mode != triggersv1alpha1.SecurityScanExecutionModeDeterministic {
		return
	}
	if exec.Phase == triggersv1alpha1.SecurityScanExecutionPhaseCancelled {
		// No LastError: a user-requested stop is not a scan error.
		setSecurityScanCondition(fresh, metav1.ConditionFalse, securityScanReasonCancelled, securityScanCancelMessage)
		setSecurityScanCoverageCondition(fresh, metav1.ConditionUnknown, securityScanReasonCancelled, "coverage completeness is unknown because the execution was cancelled")
		return
	}
	if exec.Phase == triggersv1alpha1.SecurityScanExecutionPhaseSucceeded {
		if len(exec.CoverageGaps) == 0 {
			setSecurityScanCondition(fresh, metav1.ConditionTrue, "ExecutionSucceeded", fmt.Sprintf("Deterministic execution %s succeeded", exec.ID))
			setSecurityScanCoverageCondition(fresh, metav1.ConditionTrue, "Complete", "Execution completed without coverage gaps")
			return
		}
		message := truncateSecurityScanError(fmt.Sprintf("Execution completed with %d coverage gap(s); first: %s", len(exec.CoverageGaps), exec.CoverageGaps[0]))
		setSecurityScanCondition(fresh, metav1.ConditionTrue, "ExecutionDegraded", message)
		setSecurityScanCoverageCondition(fresh, metav1.ConditionFalse, "CoverageGaps", message)
		return
	}
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		return
	}
	msg := fmt.Sprintf("deterministic execution %s failed", exec.ID)
	if reason := failedSecurityScanExecutionDetail(exec); reason != "" {
		msg += ": " + reason
	}
	fresh.Status.LastError = msg
	setSecurityScanCondition(fresh, metav1.ConditionFalse, "ExecutionFailed", msg)
	setSecurityScanCoverageCondition(fresh, metav1.ConditionUnknown, "ExecutionFailed", "coverage completeness is unknown because the execution failed")
}

func setSecurityScanCoverageCondition(scan *triggersv1alpha1.SecurityScan, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&scan.Status.Conditions, metav1.Condition{
		Type:               triggersv1alpha1.ConditionSecurityScanCoverageComplete,
		Status:             status,
		ObservedGeneration: scan.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// failedSecurityScanExecutionDetail summarizes the first failed task of a
// terminal execution, falling back to the first skipped task carrying an
// error so execution-level failures (drift, budget) that never fail an
// individual task still surface their reason.
func failedSecurityScanExecutionDetail(exec *triggersv1alpha1.SecurityScanExecutionStatus) string {
	for _, task := range exec.Tasks {
		if task.State == triggersv1alpha1.SecurityScanTaskStateFailed && task.LastError != "" {
			return fmt.Sprintf("task %q: %s", task.Name, task.LastError)
		}
	}
	for _, task := range exec.Tasks {
		if task.State == triggersv1alpha1.SecurityScanTaskStateSkipped && task.LastError != "" {
			return fmt.Sprintf("task %q: %s", task.Name, task.LastError)
		}
	}
	return ""
}

// skipDueScheduleTick returns a status mutation that consumes a due schedule
// tick while an execution is in flight (Forbid semantics: skipped instants
// are recorded as processed and never backfilled), or nil when no tick is
// due.
func (r *SecurityScanReconciler) skipDueScheduleTick(ctx context.Context, scan *triggersv1alpha1.SecurityScan) func(fresh *triggersv1alpha1.SecurityScan) {
	if strings.TrimSpace(scan.Spec.Schedule) == "" {
		return nil
	}
	schedule, observedTimeZone, err := parseCronSchedule(scan.Spec.Schedule, scan.Spec.TimeZone)
	if err != nil {
		return nil
	}
	observedSchedule := strings.TrimSpace(scan.Spec.Schedule)
	now := r.now()
	scheduledTime := nextSecurityScanScheduleTime(scan, schedule, observedSchedule, observedTimeZone, now)
	if scheduledTime.IsZero() || scheduledTime.After(now) {
		return nil
	}
	next := metav1.NewTime(schedule.Next(now))
	msg := fmt.Sprintf("skipped tick %s: execution still active", scheduledTime.UTC().Format(time.RFC3339))
	logf.FromContext(ctx).Info("skipping scheduled tick because an execution is still active", "scheduledTime", scheduledTime)
	return func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.NextScheduleTime = &next
		fresh.Status.ObservedSchedule = observedSchedule
		fresh.Status.ObservedTimeZone = observedTimeZone
		fresh.Status.LastError = msg
	}
}

// failSecurityScanExecution marks the whole execution Failed: running tasks
// are left to their runs (which keep running to completion but are no longer
// observed), everything not started is Skipped.
func failSecurityScanExecution(exec *triggersv1alpha1.SecurityScanExecutionStatus, now metav1.Time, reason string) {
	for i := range exec.Tasks {
		task := &exec.Tasks[i]
		switch task.State {
		case triggersv1alpha1.SecurityScanTaskStatePending, triggersv1alpha1.SecurityScanTaskStateBlocked:
			task.State = triggersv1alpha1.SecurityScanTaskStateSkipped
			task.LastError = reason
			task.FinishedAt = &now
		}
	}
	exec.Phase = triggersv1alpha1.SecurityScanExecutionPhaseFailed
	exec.CompletedAt = &now
}

// cancelSecurityScanExecution marks the whole execution Cancelled once its
// live runs have been asked to stop. Running work is recorded as Failed
// rather than through a state of its own: the task states are a published
// contract every consumer (reports, checks, dashboard) switches on, and the
// stop reason on LastError already tells a stopped task apart from a failed
// one. Nothing that never started is charged with a failure, so Pending and
// Blocked work becomes Skipped, and the post-script jobs follow the same
// rule so no job entry survives that the engine would keep polling.
func cancelSecurityScanExecution(exec *triggersv1alpha1.SecurityScanExecutionStatus, now metav1.Time, reason string) {
	reason = truncateSecurityScanError(reason)
	for i := range exec.Tasks {
		task := &exec.Tasks[i]
		switch task.State {
		case triggersv1alpha1.SecurityScanTaskStateRunning:
			task.State = triggersv1alpha1.SecurityScanTaskStateFailed
			task.LastError = reason
			task.FinishedAt = &now
		case triggersv1alpha1.SecurityScanTaskStatePending, triggersv1alpha1.SecurityScanTaskStateBlocked:
			task.State = triggersv1alpha1.SecurityScanTaskStateSkipped
			task.LastError = reason
			task.FinishedAt = &now
		}
	}
	for i := range exec.PostScriptJobs {
		job := &exec.PostScriptJobs[i]
		switch job.State {
		case triggersv1alpha1.SecurityScanPostScriptStateRunning:
			job.State = triggersv1alpha1.SecurityScanPostScriptStateFailed
			job.LastError = reason
			job.FinishedAt = &now
		case triggersv1alpha1.SecurityScanPostScriptStatePending:
			job.State = triggersv1alpha1.SecurityScanPostScriptStateSkipped
			job.Result = reason
			job.FinishedAt = &now
		}
	}
	exec.Phase = triggersv1alpha1.SecurityScanExecutionPhaseCancelled
	exec.CompletedAt = &now
}

// cancelDeterministicExecution stops a live deterministic execution on user
// request: every running task run and post-script job run is cancelled the
// same way the budget enforcement cancels its runs, then the execution is
// marked Cancelled — terminal and, unlike Failed, never resumable — and the
// token is consumed in the same conflict-safe status write. A run that
// already finished or vanished needs no cancellation, but a transient API
// error is returned instead of logged: terminalizing the execution consumes
// the token, so nothing would ever retry the cancellation and the run the
// user asked to stop would keep working. Returning leaves the token pending
// and the next reconcile stops the run.
func (r *SecurityScanReconciler) cancelDeterministicExecution(ctx context.Context, scan *triggersv1alpha1.SecurityScan, active *triggersv1alpha1.SecurityScanExecutionStatus, token string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	exec := active.DeepCopy()
	now := metav1.NewTime(r.now())

	cancelRun := func(runName string) error {
		if runName == "" {
			return nil
		}
		run := &platformv1alpha1.AgentRun{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: scan.Namespace, Name: runName}, run); err != nil {
			if apierrors.IsNotFound(err) {
				// A run that no longer exists cannot keep working.
				return nil
			}
			return fmt.Errorf("getting scan run %s for cancellation: %w", runName, err)
		}
		if isCronRunTerminal(run.Status.Phase) {
			return nil
		}
		if _, err := r.cancelScanRun(ctx, run); err != nil {
			return fmt.Errorf("cancelling scan run %s: %w", runName, err)
		}
		return nil
	}
	for _, entry := range exec.Tasks {
		if entry.State == triggersv1alpha1.SecurityScanTaskStateRunning {
			if err := cancelRun(entry.RunName); err != nil {
				return ctrl.Result{}, err
			}
		}
	}
	for _, job := range exec.PostScriptJobs {
		if job.State == triggersv1alpha1.SecurityScanPostScriptStateRunning {
			if err := cancelRun(job.RunName); err != nil {
				return ctrl.Result{}, err
			}
		}
	}
	cancelSecurityScanExecution(exec, now, securityScanCancelMessage)

	execID := exec.ID
	// A run-now token queued before the stop must not survive it: the very
	// next reconcile would dispatch a replacement for the run the user just
	// stopped, so the stop supersedes the queued request by consuming it.
	supersededManualToken := pendingManualRunToken(scan)
	if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.LastCancelToken = token
		if supersededManualToken != "" {
			fresh.Status.LastManualRunToken = supersededManualToken
		}
		if securityScanExecutionActive(fresh.Status.LastExecution) && fresh.Status.LastExecution.ID == execID {
			fresh.Status.LastExecution = exec
			fresh.Status.Phase = "Completed"
		}
		fresh.Status.LastError = ""
		setSecurityScanCondition(fresh, metav1.ConditionFalse, securityScanReasonCancelled, securityScanCancelMessage)
	}); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("cancelled deterministic execution on user request", "execution", execID)
	r.recordScanEvent(scan, corev1.EventTypeWarning, "ScanCancelled",
		fmt.Sprintf("%s: execution %s stopped (recorded findings are preserved)", securityScanCancelMessage, execID))
	// The terminal side effects run HERE, not on the next reconcile: a stop
	// is honoured above the suspend gate, so a scan stopped while suspended
	// never reaches the dispatch paths that would otherwise settle the scan
	// record and publish the aggregate check. finishTerminalExecution is
	// idempotent and self-gated, so the dispatch paths repeating it later
	// are no-ops.
	requeue := time.Second
	if r.finishTerminalExecution(ctx, scan, exec) {
		// Failed deliveries retry on whichever reconcile comes first.
		requeue = min(requeue, time.Minute)
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// securityScanExecutionEngine is one scheduler pass over a deterministic
// execution. It mutates exec in memory; the caller persists the result.
type securityScanExecutionEngine struct {
	r        *SecurityScanReconciler
	scan     *triggersv1alpha1.SecurityScan
	resolved *resolvedSecurityScanSpec
	exec     *triggersv1alpha1.SecurityScanExecutionStatus
	now      metav1.Time

	tasks   map[string]triggersv1alpha1.SecurityScanTask
	order   []triggersv1alpha1.SecurityScanTask
	params  map[string]string
	runs    map[string]*platformv1alpha1.AgentRun
	runCtx  *securityScanRunContext
	budgets *triggersv1alpha1.SecurityScanBudgets

	// postScriptRequeue is the earliest post-script retry delay this pass
	// computed; post-script jobs carry no retry timestamp in status, so the
	// delay only survives within the pass that derived it.
	postScriptRequeue time.Duration
}

// advance executes one pass and returns the desired requeue delay (0 = use
// the default poll interval).
func (e *securityScanExecutionEngine) advance(ctx context.Context) time.Duration {
	exec := e.exec
	e.order = e.resolved.spec.Workflow
	e.tasks = make(map[string]triggersv1alpha1.SecurityScanTask, len(e.order))
	for _, task := range e.order {
		e.tasks[task.Name] = task
	}
	if drift := e.workflowDrift(); drift != "" {
		failSecurityScanExecution(exec, e.now, drift)
		return 0
	}
	params, err := resolveSecurityScanParameters(e.resolved)
	if err != nil {
		failSecurityScanExecution(exec, e.now, err.Error())
		return 0
	}
	e.params = params
	e.runCtx = e.r.executionTriggerEvent(ctx, e.scan, exec)

	e.observe(ctx)
	e.observePostScripts(ctx)
	materializedFanOut := e.expandFanOuts(ctx)
	e.propagateSkips()
	if materializedFanOut {
		// Chunk entries and their source binding must be durable before any
		// corresponding AgentRun can exist. The next reconcile dispatches the
		// frozen plan. Do not finalize here: a vacuous sink fan-out can make all
		// task entries successful, but post-script pipelines have not yet been
		// materialized and must still get their own persistence barrier.
		return e.nextRequeue()
	}

	e.budgets = effectiveSecurityScanBudgets(e.scan, e.r.scanPolicyPack(ctx, e.scan))
	if budgetErr := e.enforceBudgets(ctx); budgetErr != "" {
		e.failForBudget(ctx, budgetErr)
		return 0
	}

	if !e.anyFailed() {
		// Post-scripts run between the research phase and the report: the
		// per-finding pipelines are materialized once research is terminal,
		// and schedule() holds the sink until every pipeline settles.
		if e.materializePostScripts(ctx) {
			// Eligibility is derived from mutable finding state. Persist the
			// frozen pipeline snapshot before a pipeline can mutate a finding;
			// dispatch begins on the next reconciliation.
			e.finalizePhase(false)
			return e.nextRequeue()
		}
		e.dispatchPostScripts(ctx)
		e.schedule(ctx)
	}
	if securityScanExecutionTerminal(exec.Phase) {
		return 0 // schedule stopped the execution (budget)
	}

	e.finalizePhase(e.anyFailed())
	return e.nextRequeue()
}

// workflowDrift detects mid-execution edits to the effective workflow that
// the execution state cannot follow, in both directions: an execution entry
// whose task no longer exists, a workflow task that has no execution entries
// (added after planning, so a dependsOn edge pointing at it would block
// forever), and a dependsOn edge referencing a task outside the effective
// workflow. Any drift fails the execution non-retryably instead of letting
// Blocked entries poll forever.
func (e *securityScanExecutionEngine) workflowDrift() string {
	const remedy = "workflow changed while the execution was in progress; re-run the scan"
	for i := range e.exec.Tasks {
		if _, ok := e.tasks[e.exec.Tasks[i].Name]; !ok {
			return fmt.Sprintf("%s (task %q no longer exists)", remedy, e.exec.Tasks[i].Name)
		}
	}
	planned := make(map[string]bool, len(e.exec.Tasks))
	for i := range e.exec.Tasks {
		planned[e.exec.Tasks[i].Name] = true
	}
	planByName := make(map[string]triggersv1alpha1.SecurityScanExecutionPlanNode, len(e.exec.Plan))
	for _, node := range e.exec.Plan {
		planByName[node.Name] = node
	}
	for _, task := range e.order {
		if !planned[task.Name] {
			return fmt.Sprintf("%s (task %q was added after the execution was planned)", remedy, task.Name)
		}
		if node, ok := planByName[task.Name]; ok {
			if !slices.Equal(node.DependsOn, task.DependsOn) {
				return fmt.Sprintf("%s (task %q changed dependsOn from %q to %q)", remedy, task.Name, node.DependsOn, task.DependsOn)
			}
			if node.ForEach != task.ForEach {
				return fmt.Sprintf("%s (task %q changed forEach from %q to %q)", remedy, task.Name, node.ForEach, task.ForEach)
			}
			if node.Reduce != task.Reduce {
				return fmt.Sprintf("%s (task %q changed reduce from %q to %q)", remedy, task.Name, node.Reduce, task.Reduce)
			}
		}
		for _, dep := range task.DependsOn {
			if !planned[dep] {
				return fmt.Sprintf("%s (task %q depends on %q, which has no planned instances)", remedy, task.Name, dep)
			}
		}
	}
	return ""
}

// observe folds terminal task-run phases into the task entries.
func (e *securityScanExecutionEngine) observe(ctx context.Context) {
	for i := range e.exec.Tasks {
		entry := &e.exec.Tasks[i]
		if entry.State != triggersv1alpha1.SecurityScanTaskStateRunning {
			continue
		}
		run, err := e.getRun(ctx, entry.RunName)
		if err != nil {
			continue // transient read error: retry next reconcile
		}
		if run == nil {
			e.recordAttemptFailure(entry, nil, "task run disappeared before completing", triggersv1alpha1.SecurityScanTaskFailureRetryable)
			continue
		}
		// Paused is published before the AgentRun controller drains the old
		// sandbox. Do not launch a retry while that worker can still exit and
		// publish results from the previous attempt.
		if run.Status.Phase == platformv1alpha1.AgentRunPhasePaused && run.Status.Sandbox != nil {
			continue
		}
		switch run.Status.Phase {
		case platformv1alpha1.AgentRunPhaseSucceeded:
			task := e.tasks[entry.Name]
			plan := e.fanOutStatus(entry.Name)
			if plan != nil && plan.Strategy == "chunk-v1" {
				if _, outputErr := validateSecurityScanChunkOutput(run.Status.StructuredOutput, entry.RecordStart, entry.RecordEnd); outputErr != nil {
					e.recordAttemptFailure(entry, run,
						securityScanReasonOutputContractUnmet+": "+outputErr.Error(),
						triggersv1alpha1.SecurityScanTaskFailureNonRetryable)
					continue
				}
			} else if strings.TrimSpace(task.OutputSchema) != "" {
				out := strings.TrimSpace(run.Status.StructuredOutput)
				if out == "" || !json.Valid([]byte(out)) {
					e.recordAttemptFailure(entry, run,
						securityScanReasonOutputContractUnmet+": the run succeeded without submitting structured output conforming to the task's outputSchema",
						triggersv1alpha1.SecurityScanTaskFailureNonRetryable)
					continue
				}
			}
			entry.State = triggersv1alpha1.SecurityScanTaskStateSucceeded
			entry.LastError = ""
			entry.NextRetryTime = nil
			entry.FinishedAt = &e.now
		case platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhaseCancelled,
			platformv1alpha1.AgentRunPhasePaused:
			reason := securityScanAgentRunFailureReason(run, "task")
			e.recordAttemptFailure(entry, run, reason, classifySecurityScanTaskFailure(reason))
		}
	}
}

func validateSecurityScanChunkOutput(output string, start, end int32) ([]json.RawMessage, error) {
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &entries); err != nil || entries == nil {
		return nil, fmt.Errorf("chunk output is not a JSON array")
	}
	want := int(end - start)
	if len(entries) != want {
		return nil, fmt.Errorf("chunk output has %d records, want exactly %d", len(entries), want)
	}
	results := make([]json.RawMessage, want)
	seen := make([]bool, want)
	for _, entry := range entries {
		if len(entry) != 2 {
			return nil, fmt.Errorf("each chunk output entry must contain exactly recordIndex and result")
		}
		rawIndex, hasIndex := entry["recordIndex"]
		result, hasResult := entry["result"]
		if !hasIndex || !hasResult {
			return nil, fmt.Errorf("each chunk output entry must contain exactly recordIndex and result")
		}
		var index int64
		if err := json.Unmarshal(rawIndex, &index); err != nil {
			return nil, fmt.Errorf("chunk output recordIndex must be an integer")
		}
		if index < int64(start) || index >= int64(end) {
			return nil, fmt.Errorf("chunk output contains foreign recordIndex %d outside [%d,%d)", index, start, end)
		}
		offset := int(index - int64(start))
		if seen[offset] {
			return nil, fmt.Errorf("chunk output contains duplicate recordIndex %d", index)
		}
		seen[offset] = true
		results[offset] = result
	}
	for i, present := range seen {
		if !present {
			return nil, fmt.Errorf("chunk output is missing recordIndex %d", int(start)+i)
		}
	}
	return results, nil
}

// securityScanAgentRunFailureReason preserves the actionable queue reason used by
// Paused AgentRuns. Runtime limits pause (rather than fail) a run and publish
// the timeout only in status.queue.blockedReason; ignoring that phase leaves a
// deterministic scan counting the run as live forever after its worker has
// already been drained.
func securityScanAgentRunFailureReason(run *platformv1alpha1.AgentRun, kind string) string {
	if run == nil {
		return kind + " run failed"
	}
	if run.Status.Phase == platformv1alpha1.AgentRunPhasePaused && run.Status.Queue != nil {
		if reason := strings.TrimSpace(run.Status.Queue.BlockedReason); reason != "" {
			return reason
		}
	}
	if reason := strings.TrimSpace(run.Status.LastError); reason != "" {
		return reason
	}
	if run.Status.Queue != nil {
		if reason := strings.TrimSpace(run.Status.Queue.BlockedReason); reason != "" {
			return reason
		}
	}
	return kind + " run " + strings.ToLower(string(run.Status.Phase))
}

// recordAttemptFailure appends the failed attempt to the retry history
// (capped, oldest dropped) and either schedules a retry with exponential
// backoff or marks the task Failed.
func (e *securityScanExecutionEngine) recordAttemptFailure(entry *triggersv1alpha1.SecurityScanTaskExecutionStatus, run *platformv1alpha1.AgentRun, reason, class string) {
	reason = truncateSecurityScanError(reason)
	attempt := triggersv1alpha1.SecurityScanTaskAttempt{
		RunName:    entry.RunName,
		FinishedAt: &e.now,
		Reason:     reason,
		Class:      class,
	}
	if run != nil {
		created := run.CreationTimestamp
		attempt.StartedAt = &created
		if run.Status.CompletedAt != nil {
			attempt.FinishedAt = run.Status.CompletedAt
		}
	}
	entry.Retries = append(entry.Retries, attempt)
	if len(entry.Retries) > securityScanTaskRetryHistoryLimit {
		entry.Retries = entry.Retries[len(entry.Retries)-securityScanTaskRetryHistoryLimit:]
	}
	entry.LastError = reason

	maxRetries := e.resolved.spec.EffectiveTaskMaxRetries(e.tasks[entry.Name])
	// The retry budget is per resume cycle: attempts stay cumulative for the
	// durable budgets.maxModelJobs accounting, so the cycle's attempt count
	// is measured against the baseline recorded at resume.
	cycleAttempts := entry.Attempts - entry.ResumeBaselineAttempts
	if class == triggersv1alpha1.SecurityScanTaskFailureRetryable && cycleAttempts < 1+maxRetries {
		backoff := securityScanRetryBackoff(e.resolved.spec.EffectiveRetryBackoff(), cycleAttempts)
		next := metav1.NewTime(e.now.Add(backoff))
		entry.NextRetryTime = &next
		entry.State = triggersv1alpha1.SecurityScanTaskStatePending
		return
	}
	entry.State = triggersv1alpha1.SecurityScanTaskStateFailed
	entry.FinishedAt = &e.now
}

// securityScanRetryBackoff computes base * 2^(attempt-1), capped.
func securityScanRetryBackoff(base time.Duration, attempt int32) time.Duration {
	backoff := base
	for i := int32(1); i < attempt; i++ {
		backoff *= 2
		if backoff >= securityScanTaskRetryBackoffCap {
			return securityScanTaskRetryBackoffCap
		}
	}
	if backoff > securityScanTaskRetryBackoffCap {
		return securityScanTaskRetryBackoffCap
	}
	return backoff
}

// expandFanOuts replaces the un-started placeholder instance of each forEach
// task whose source task has fully succeeded. Legacy maxInstances tasks keep
// their one-instance-per-record behavior. targetRuns tasks instead persist a
// source-bound, count-balanced chunk plan and return true so dispatch is
// deferred until the plan has survived a status write.
func (e *securityScanExecutionEngine) expandFanOuts(ctx context.Context) bool {
	materialized := false
	for _, task := range e.order {
		sourceName := e.plannedForEach(task.Name)
		if sourceName == "" {
			continue
		}
		if targetRuns := e.plannedTargetRuns(task.Name); targetRuns > 0 {
			if e.expandTargetRunsFanOut(ctx, task, sourceName, targetRuns) {
				materialized = true
			}
			continue
		}
		entries := e.taskEntries(task.Name)
		if len(entries) != 1 {
			continue // already expanded
		}
		placeholder := entries[0]
		if placeholder.Attempts > 0 || securityScanTaskTerminal(placeholder.State) {
			continue
		}
		if !e.taskComplete(sourceName) {
			continue
		}
		records, err := e.fanOutRecords(ctx, sourceName)
		if err != nil {
			placeholder.State = triggersv1alpha1.SecurityScanTaskStateFailed
			placeholder.LastError = truncateSecurityScanError(fmt.Sprintf("forEach source %q: %s", sourceName, err.Error()))
			placeholder.FinishedAt = &e.now
			continue
		}
		limit := int(task.EffectiveMaxInstances())
		if len(records) > limit {
			msg := fmt.Sprintf("task %q fan-out truncated from %d to %d instances by maxInstances", task.Name, len(records), limit)
			logf.FromContext(ctx).Info(msg, "execution", e.exec.ID)
			e.r.recordScanEvent(e.scan, corev1.EventTypeWarning, "FanOutTruncated", msg)
			appendSecurityScanCoverageGap(e.exec, msg)
			records = records[:limit]
		}
		// Never let the execution outgrow the entry ceiling: replacing the
		// placeholder adds len(records)-1 entries, so the expansion may only
		// use whatever budget the other entries leave.
		if budget := securityScanExecutionMaxTaskEntries - (len(e.exec.Tasks) - 1); len(records) > budget {
			if budget < 0 {
				budget = 0
			}
			msg := fmt.Sprintf("task %q fan-out truncated from %d to %d instances: the execution is capped at %d total task instances", task.Name, len(records), budget, securityScanExecutionMaxTaskEntries)
			logf.FromContext(ctx).Info(msg, "execution", e.exec.ID)
			e.r.recordScanEvent(e.scan, corev1.EventTypeWarning, "FanOutTruncated", msg)
			appendSecurityScanCoverageGap(e.exec, msg)
			records = records[:budget]
		}
		if len(records) == 0 {
			placeholder.State = triggersv1alpha1.SecurityScanTaskStateSucceeded
			placeholder.FinishedAt = &e.now
			continue
		}
		expanded := make([]triggersv1alpha1.SecurityScanTaskExecutionStatus, len(records))
		for i := range records {
			expanded[i] = triggersv1alpha1.SecurityScanTaskExecutionStatus{
				Name:     task.Name,
				Instance: int32(i), //nolint:gosec // bounded by maxInstances <= 50
				State:    triggersv1alpha1.SecurityScanTaskStatePending,
			}
		}
		e.replaceTaskEntries(task.Name, expanded)
	}
	return materialized
}

func (e *securityScanExecutionEngine) plannedWhen(name string) *triggersv1alpha1.SecurityScanTaskCondition {
	for _, node := range e.exec.Plan {
		if node.Name == name {
			return node.When.DeepCopy()
		}
	}
	return nil
}

func (e *securityScanExecutionEngine) plannedReduce(name string) string {
	for _, node := range e.exec.Plan {
		if node.Name == name {
			return node.Reduce
		}
	}
	if len(e.exec.Plan) == 0 {
		return e.tasks[name].Reduce
	}
	return ""
}

func (e *securityScanExecutionEngine) plannedForEach(name string) string {
	for _, node := range e.exec.Plan {
		if node.Name == name {
			return node.ForEach
		}
	}
	// Executions created before plan snapshots existed retain their legacy
	// behavior. A non-empty plan is authoritative and must not be filled from
	// a subsequently edited workflow.
	if len(e.exec.Plan) == 0 {
		return e.tasks[name].ForEach
	}
	return ""
}

func (e *securityScanExecutionEngine) plannedTargetRuns(name string) int32 {
	for _, node := range e.exec.Plan {
		if node.Name == name {
			return node.TargetRuns
		}
	}
	// Executions created before targetRuns was snapshotted remain legacy.
	return 0
}

func (e *securityScanExecutionEngine) expandTargetRunsFanOut(ctx context.Context, task triggersv1alpha1.SecurityScanTask, sourceName string, targetRuns int32) bool {
	if e.fanOutStatus(task.Name) != nil {
		return false
	}
	entries := e.taskEntries(task.Name)
	if len(entries) != 1 {
		return false
	}
	placeholder := entries[0]
	if placeholder.Attempts > 0 || securityScanTaskTerminal(placeholder.State) || !e.taskComplete(sourceName) {
		return false
	}

	sourceRunName, sourceOutput, records, err := e.targetRunsSource(ctx, sourceName)
	if err != nil {
		placeholder.State = triggersv1alpha1.SecurityScanTaskStateFailed
		placeholder.LastError = truncateSecurityScanError(fmt.Sprintf("forEach source %q: %s", sourceName, err.Error()))
		placeholder.FinishedAt = &e.now
		return false
	}

	chunkCount := len(records)
	if target := int(targetRuns); chunkCount > target {
		chunkCount = target
	}
	sourceHash := securityScanSHA256(sourceOutput)
	e.exec.FanOuts = append(e.exec.FanOuts, triggersv1alpha1.SecurityScanFanOutExecutionStatus{
		Name:               task.Name,
		SourceTask:         sourceName,
		SourceRunName:      sourceRunName,
		Strategy:           "chunk-v1",
		SourceOutputSHA256: sourceHash,
		RecordCount:        int32(len(records)), //nolint:gosec // structured outputs are bounded well below int32
		ChunkCount:         int32(chunkCount),   //nolint:gosec // targetRuns is API-bounded
	})

	if chunkCount == 0 {
		placeholder.State = triggersv1alpha1.SecurityScanTaskStateSucceeded
		placeholder.FinishedAt = &e.now
		return true
	}
	if len(e.exec.Tasks)-1+chunkCount > securityScanExecutionMaxTaskEntries {
		placeholder.State = triggersv1alpha1.SecurityScanTaskStateFailed
		placeholder.LastError = truncateSecurityScanError(fmt.Sprintf("task %q targetRuns fan-out requires %d chunks, exceeding the execution cap of %d total task instances", task.Name, chunkCount, securityScanExecutionMaxTaskEntries))
		placeholder.FinishedAt = &e.now
		return true
	}

	expanded := make([]triggersv1alpha1.SecurityScanTaskExecutionStatus, 0, chunkCount)
	start := 0
	base, extra := len(records)/chunkCount, len(records)%chunkCount
	for i := 0; i < chunkCount; i++ {
		size := base
		if i < extra {
			size++
		}
		end := start + size
		items := securityScanIndexedItems(records, start, end)
		expanded = append(expanded, triggersv1alpha1.SecurityScanTaskExecutionStatus{
			Name:        task.Name,
			Instance:    int32(i), //nolint:gosec // targetRuns is API-bounded
			State:       triggersv1alpha1.SecurityScanTaskStatePending,
			RecordStart: int32(start), //nolint:gosec // structured outputs are bounded well below int32
			RecordEnd:   int32(end),   //nolint:gosec // structured outputs are bounded well below int32
			InputSHA256: securityScanSHA256(items),
		})
		start = end
	}
	e.replaceTaskEntries(task.Name, expanded)
	return true
}

func (e *securityScanExecutionEngine) fanOutStatus(name string) *triggersv1alpha1.SecurityScanFanOutExecutionStatus {
	for i := range e.exec.FanOuts {
		if e.exec.FanOuts[i].Name == name {
			return &e.exec.FanOuts[i]
		}
	}
	return nil
}

func (e *securityScanExecutionEngine) targetRunsSource(ctx context.Context, sourceName string) (string, string, []json.RawMessage, error) {
	entries := e.taskEntries(sourceName)
	if len(entries) != 1 || entries[0].RunName == "" {
		return "", "", nil, fmt.Errorf("targetRuns requires a single source task run")
	}
	run, err := e.getRun(ctx, entries[0].RunName)
	if err != nil {
		return "", "", nil, err
	}
	if run == nil {
		return "", "", nil, fmt.Errorf("task %q run %q no longer exists; its output is unavailable", sourceName, entries[0].RunName)
	}
	output := run.Status.StructuredOutput
	var records []json.RawMessage
	if err := json.Unmarshal([]byte(output), &records); err != nil || records == nil {
		return "", "", nil, fmt.Errorf("structured output is not a JSON array")
	}
	return run.Name, output, records, nil
}

func securityScanIndexedItems(records []json.RawMessage, start, end int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := start; i < end; i++ {
		if i > start {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"recordIndex":%d,"item":%s}`, i, records[i])
	}
	b.WriteByte(']')
	return b.String()
}

func securityScanSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// fanOutRecords parses the forEach source task's output into records: a
// single-instance source must publish a JSON array; a multi-instance source
// contributes one record per instance output.
func (e *securityScanExecutionEngine) fanOutRecords(ctx context.Context, sourceName string) ([]json.RawMessage, error) {
	if plan := e.fanOutStatus(sourceName); plan != nil && plan.Strategy == "chunk-v1" {
		output, err := e.taskOutput(ctx, sourceName)
		if err != nil {
			return nil, err
		}
		var records []json.RawMessage
		if err := json.Unmarshal([]byte(output), &records); err != nil {
			return nil, fmt.Errorf("structured output is not a JSON array")
		}
		return records, nil
	}
	outputs, vacuous, err := e.taskInstanceOutputs(ctx, sourceName)
	if err != nil {
		return nil, err
	}
	if vacuous {
		return nil, nil
	}
	if len(outputs) > 1 {
		records := make([]json.RawMessage, len(outputs))
		for i, out := range outputs {
			records[i] = json.RawMessage(out)
		}
		return records, nil
	}
	var records []json.RawMessage
	if err := json.Unmarshal([]byte(outputs[0]), &records); err != nil {
		return nil, fmt.Errorf("structured output is not a JSON array")
	}
	return records, nil
}

// propagateSkips marks every not-yet-started transitive dependent of a
// failed task Skipped.
func (e *securityScanExecutionEngine) propagateSkips() {
	failed := map[string]bool{}
	for _, entry := range e.exec.Tasks {
		if entry.State == triggersv1alpha1.SecurityScanTaskStateFailed {
			failed[entry.Name] = true
		}
	}
	if len(failed) == 0 {
		return
	}
	skip := map[string]string{}
	for changed := true; changed; {
		changed = false
		for _, task := range e.order {
			if _, done := skip[task.Name]; done || failed[task.Name] {
				continue
			}
			for _, dep := range task.DependsOn {
				if _, depSkipped := skip[dep]; failed[dep] || depSkipped {
					skip[task.Name] = dep
					changed = true
					break
				}
			}
		}
	}
	for i := range e.exec.Tasks {
		entry := &e.exec.Tasks[i]
		dep, skipped := skip[entry.Name]
		if !skipped {
			continue
		}
		switch entry.State {
		case triggersv1alpha1.SecurityScanTaskStatePending, triggersv1alpha1.SecurityScanTaskStateBlocked:
			entry.State = triggersv1alpha1.SecurityScanTaskStateSkipped
			entry.LastError = fmt.Sprintf("skipped: dependency task %q failed", dep)
			entry.FinishedAt = &e.now
		}
	}
}

// enforceBudgets returns a non-empty reason when a scan budget forbids any
// further scheduling. Every task run counts toward maxModelJobs; cost and
// token budgets are summed over the execution's still-existing task runs;
// the platform-computed status.budget breach (e.g. persisted findings) also
// stops the execution.
func (e *securityScanExecutionEngine) enforceBudgets(ctx context.Context) string {
	if b := e.scan.Status.Budget; b != nil && b.Exceeded {
		return b.Message
	}
	budgets := e.budgets
	if budgets == nil || budgets.IsZero() {
		return ""
	}
	if budgets.MaxModelJobs > 0 {
		if attempts := e.totalAttempts(); attempts > budgets.MaxModelJobs || (attempts == budgets.MaxModelJobs && e.hasSchedulableWork()) {
			return fmt.Sprintf("execution used %d task runs, reaching budgets.maxModelJobs %d", attempts, budgets.MaxModelJobs)
		}
	}
	costLimit := securityBudgetCostUSD(budgets.MaxCostUSD)
	if costLimit >= 0 || budgets.MaxTokens > 0 {
		var cost float64
		var tokens int64
		for _, name := range e.executionRunNames() {
			run, err := e.getRun(ctx, name)
			if err != nil || run == nil || run.Status.Metrics == nil {
				continue
			}
			if c := securityBudgetCostUSD(run.Status.Metrics.CostUsd); c > 0 {
				cost += c
			}
			tokens += run.Status.Metrics.InputTokens + run.Status.Metrics.OutputTokens
		}
		if costLimit >= 0 && cost > costLimit {
			return fmt.Sprintf("execution cost $%.2f exceeds budgets.maxCostUSD %s", cost, budgets.MaxCostUSD)
		}
		if budgets.MaxTokens > 0 && tokens > budgets.MaxTokens {
			return fmt.Sprintf("execution tokens %d exceed budgets.maxTokens %d", tokens, budgets.MaxTokens)
		}
	}
	return ""
}

// hasSchedulableWork reports whether any task instance still needs a run.
func (e *securityScanExecutionEngine) hasSchedulableWork() bool {
	for _, entry := range e.exec.Tasks {
		switch entry.State {
		case triggersv1alpha1.SecurityScanTaskStatePending, triggersv1alpha1.SecurityScanTaskStateBlocked:
			return true
		}
	}
	return false
}

// totalAttempts counts every model run the execution has started so far:
// task attempts plus post-script job attempts, since both consume the
// budgets.maxModelJobs allowance.
func (e *securityScanExecutionEngine) totalAttempts() int32 {
	var attempts int32
	for _, entry := range e.exec.Tasks {
		attempts += entry.Attempts
	}
	for _, job := range e.exec.PostScriptJobs {
		attempts += job.Attempts
	}
	return attempts
}

// executionRunNames collects every run name recorded for the execution:
// current attempts plus retry history, and every attempt of the durable
// post-script jobs. Post-script jobs are model executions of this campaign
// like any task run, so cost and token budgets must see them; leaving them
// out would let a large finding x script matrix spend past
// budgets.maxCostUSD unobserved.
func (e *securityScanExecutionEngine) executionRunNames() []string {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, entry := range e.exec.Tasks {
		add(entry.RunName)
		for _, attempt := range entry.Retries {
			add(attempt.RunName)
		}
	}
	for _, job := range e.exec.PostScriptJobs {
		// job.RunName only holds the LATEST attempt (launchPostScript
		// overwrites it on every retry), so the earlier attempts are
		// re-derived from their deterministic names instead; the current
		// name is still added because a resume changes the token the derived
		// names embed.
		for _, name := range securityScanPostScriptAttemptRunNames(e.scan.Name, e.exec.ID, job, e.exec.LastResumeToken) {
			add(name)
		}
		add(job.RunName)
	}
	return names
}

// failForBudget stops the execution with reason BudgetExceeded: running task
// runs are cancelled the same way the coordinator budget enforcement cancels
// its run, running entries are marked Failed non-retryably, and everything
// not started is Skipped.
func (e *securityScanExecutionEngine) failForBudget(ctx context.Context, reason string) {
	log := logf.FromContext(ctx)
	msg := securityScanReasonBudgetExceeded + ": " + reason
	for i := range e.exec.Tasks {
		entry := &e.exec.Tasks[i]
		if entry.State != triggersv1alpha1.SecurityScanTaskStateRunning {
			continue
		}
		if run, err := e.getRun(ctx, entry.RunName); err == nil && run != nil {
			if _, cancelErr := e.r.cancelScanRun(ctx, run); cancelErr != nil {
				log.Error(cancelErr, "failed to cancel task run over budget", "run", run.Name)
			}
		}
		entry.State = triggersv1alpha1.SecurityScanTaskStateFailed
		entry.LastError = truncateSecurityScanError(msg)
		entry.FinishedAt = &e.now
	}
	failSecurityScanExecution(e.exec, e.now, truncateSecurityScanError(msg))
	e.r.recordScanEvent(e.scan, corev1.EventTypeWarning, securityScanReasonBudgetExceeded,
		fmt.Sprintf("execution %s stopped: %s (completed work is preserved)", e.exec.ID, reason))
}

// anyFailed reports whether any task instance failed terminally. Once true,
// no new task runs are launched: running tasks finish, then the execution is
// finalized as Failed.
func (e *securityScanExecutionEngine) anyFailed() bool {
	for _, entry := range e.exec.Tasks {
		if entry.State == triggersv1alpha1.SecurityScanTaskStateFailed {
			return true
		}
	}
	return false
}

// schedule launches ready task instances while the running count stays
// below the execution's parallelism bound. Readiness: every instance of
// every dependsOn task succeeded and any retry backoff has elapsed. A
// template-rendering failure is non-retryable (the input contract is broken)
// and fails the instance without launching a run.
func (e *securityScanExecutionEngine) schedule(ctx context.Context) {
	running := e.runningPostScriptJobs()
	for _, entry := range e.exec.Tasks {
		if entry.State == triggersv1alpha1.SecurityScanTaskStateRunning {
			running++
		}
	}
	parallelism := e.exec.EffectiveParallelism
	if parallelism <= 0 {
		parallelism = e.resolved.spec.EffectiveParallelism()
	}
	sinks := securityScanSinkTasks(e.order)
	postScriptsGate := e.postScriptsGateSink()

	for i := range e.exec.Tasks {
		entry := &e.exec.Tasks[i]
		switch entry.State {
		case triggersv1alpha1.SecurityScanTaskStatePending, triggersv1alpha1.SecurityScanTaskStateBlocked:
		default:
			continue
		}
		task := e.tasks[entry.Name]
		task.When = e.plannedWhen(task.Name)
		task.Reduce = e.plannedReduce(task.Name)
		if !e.depsSatisfied(task) {
			entry.State = triggersv1alpha1.SecurityScanTaskStateBlocked
			continue
		}
		if task.When != nil {
			matched, err := e.taskConditionMatches(ctx, task)
			if err != nil {
				entry.State = triggersv1alpha1.SecurityScanTaskStateFailed
				entry.LastError = truncateSecurityScanError("task condition: " + err.Error())
				entry.FinishedAt = &e.now
				continue
			}
			if !matched {
				output := strings.TrimSpace(task.When.OtherwiseOutput)
				if schema := strings.TrimSpace(task.OutputSchema); schema != "" {
					if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(schema, output); err != nil {
						entry.State = triggersv1alpha1.SecurityScanTaskStateFailed
						entry.LastError = truncateSecurityScanError("task condition otherwiseOutput: " + err.Error())
						entry.FinishedAt = &e.now
						continue
					}
				}
				entry.State = triggersv1alpha1.SecurityScanTaskStateSucceeded
				entry.StructuredOutput = output
				entry.FinishedAt = &e.now
				continue
			}
		}
		if task.Reduce != "" {
			output, err := e.reduceTaskOutput(ctx, task)
			if err != nil {
				entry.State = triggersv1alpha1.SecurityScanTaskStateFailed
				entry.LastError = truncateSecurityScanError("task reduction: " + err.Error())
				entry.FinishedAt = &e.now
				continue
			}
			entry.State = triggersv1alpha1.SecurityScanTaskStateSucceeded
			entry.StructuredOutput = output
			entry.FinishedAt = &e.now
			continue
		}
		// A forEach task whose entries still form the un-started placeholder
		// is expanded (not launched) once its source completes; expansion
		// runs before scheduling, so reaching here with a completed source
		// means the expansion produced exactly this instance.
		entry.State = triggersv1alpha1.SecurityScanTaskStatePending
		if postScriptsGate && sinks[entry.Name] {
			// The sink submits the scan-wide report; launching it while
			// post-script jobs are unmaterialized or unfinished would report
			// verdicts that do not exist yet.
			continue
		}
		if entry.NextRetryTime != nil && e.now.Time.Before(entry.NextRetryTime.Time) {
			continue
		}
		if running >= parallelism {
			continue
		}
		if e.budgets != nil && e.budgets.MaxModelJobs > 0 && e.totalAttempts() >= e.budgets.MaxModelJobs {
			// The next launch would exceed the job budget: stop and fail.
			e.failForBudget(ctx, fmt.Sprintf("execution used %d task runs, reaching budgets.maxModelJobs %d", e.totalAttempts(), e.budgets.MaxModelJobs))
			return
		}
		if e.launch(ctx, entry, task) {
			running++
		}
	}
}

// reduceTaskOutput deterministically combines dependency outputs without an
// AgentRun. concat flattens dependency arrays one level and appends every other
// JSON value as one element, preserving dependsOn and instance order.
func (e *securityScanExecutionEngine) reduceTaskOutput(ctx context.Context, task triggersv1alpha1.SecurityScanTask) (string, error) {
	if task.Reduce != "concat" {
		return "", fmt.Errorf("unsupported reduction %q", task.Reduce)
	}
	items := make([]json.RawMessage, 0)
	for _, dependency := range task.DependsOn {
		raw, err := e.taskOutput(ctx, dependency)
		if err != nil {
			return "", fmt.Errorf("read %q output: %w", dependency, err)
		}
		value := json.RawMessage(strings.TrimSpace(raw))
		if !json.Valid(value) {
			return "", fmt.Errorf("task %q output is invalid JSON", dependency)
		}
		if len(value) > 0 && value[0] == '[' {
			var array []json.RawMessage
			if err := json.Unmarshal(value, &array); err != nil {
				return "", fmt.Errorf("decode %q output: %w", dependency, err)
			}
			items = append(items, array...)
			continue
		}
		items = append(items, value)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("encode reduction: %w", err)
	}
	output := string(encoded)
	if len(output) > securityScanMaxStructuredOutputBytes {
		return "", fmt.Errorf("output is too large: %d bytes, limit is %d", len(output), securityScanMaxStructuredOutputBytes)
	}
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(task.OutputSchema, output); err != nil {
		return "", fmt.Errorf("validate output: %w", err)
	}
	return output, nil
}

// taskConditionMatches evaluates a task's controller-side launch condition.
// It deliberately supports object traversal only: selectors over arrays are
// ambiguous under schema evolution and belong in an explicit producer field.
func (e *securityScanExecutionEngine) taskConditionMatches(ctx context.Context, task triggersv1alpha1.SecurityScanTask) (bool, error) {
	condition := task.When
	if condition == nil {
		return true, nil
	}
	raw, err := e.taskOutput(ctx, condition.Task)
	if err != nil {
		return false, fmt.Errorf("read %q output: %w", condition.Task, err)
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return false, fmt.Errorf("decode %q output: %w", condition.Task, err)
	}
	for segment := range strings.SplitSeq(condition.Path, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return false, fmt.Errorf("path %q traverses a non-object at %q", condition.Path, segment)
		}
		value, ok = object[segment]
		if !ok {
			return false, fmt.Errorf("path %q is missing segment %q", condition.Path, segment)
		}
	}
	actual, err := json.Marshal(value)
	if err != nil {
		return false, fmt.Errorf("encode value at %q: %w", condition.Path, err)
	}
	var expected any
	if err := json.Unmarshal([]byte(condition.Equals), &expected); err != nil {
		return false, fmt.Errorf("decode expected value: %w", err)
	}
	canonicalExpected, err := json.Marshal(expected)
	if err != nil {
		return false, fmt.Errorf("encode expected value: %w", err)
	}
	return string(actual) == string(canonicalExpected), nil
}

// executionStarted reports whether the execution has ever dispatched a task
// run (including runs superseded by retries). Its negation marks the FIRST
// dispatch, which anchors the execution's one eager scan record.
func (e *securityScanExecutionEngine) executionStarted() bool {
	for _, entry := range e.exec.Tasks {
		if entry.Attempts > 0 || entry.RunName != "" || len(entry.Retries) > 0 {
			return true
		}
	}
	return false
}

// depsSatisfied reports whether every instance of every dependency task
// succeeded.
func (e *securityScanExecutionEngine) depsSatisfied(task triggersv1alpha1.SecurityScanTask) bool {
	for _, dep := range task.DependsOn {
		if !e.taskComplete(dep) {
			return false
		}
	}
	return true
}

// taskComplete reports whether every instance of the named task succeeded.
func (e *securityScanExecutionEngine) taskComplete(name string) bool {
	found := false
	for _, entry := range e.exec.Tasks {
		if entry.Name != name {
			continue
		}
		found = true
		if entry.State != triggersv1alpha1.SecurityScanTaskStateSucceeded {
			return false
		}
	}
	return found
}

// launch renders the instance's objective and creates its AgentRun. It
// returns true when the instance transitioned to Running.
func (e *securityScanExecutionEngine) launch(ctx context.Context, entry *triggersv1alpha1.SecurityScanTaskExecutionStatus, task triggersv1alpha1.SecurityScanTask) bool {
	log := logf.FromContext(ctx)
	inst, err := e.taskInstanceContext(ctx, entry, task)
	if err != nil {
		entry.State = triggersv1alpha1.SecurityScanTaskStateFailed
		entry.LastError = truncateSecurityScanError(err.Error())
		entry.FinishedAt = &e.now
		return false
	}

	attempt := entry.Attempts + 1
	runName := securityScanTaskRunName(e.scan.Name, e.exec.ID, task.Name, attempt, entry.Instance, e.exec.LastResumeToken)
	created, err := e.r.createScanTaskRun(ctx, e.scan, e.resolved, e.runCtx, e.exec, task, runName, inst, !e.executionStarted())
	if err != nil {
		log.Error(err, "failed to create task AgentRun", "task", task.Name, "run", runName)
		entry.LastError = truncateSecurityScanError(err.Error())
		if classifySecurityScanTaskFailure(err.Error()) == triggersv1alpha1.SecurityScanTaskFailureNonRetryable {
			// A deterministically rejected dispatch (missing role, invalid
			// defaults) cannot heal by re-dispatching; leaving it pending
			// would keep the execution Running forever with no run to await.
			entry.State = triggersv1alpha1.SecurityScanTaskStateFailed
			entry.FinishedAt = &e.now
			return false
		}
		return false // retried on the next reconcile without consuming an attempt
	}
	_ = created
	entry.Attempts = attempt
	entry.RunName = runName
	entry.State = triggersv1alpha1.SecurityScanTaskStateRunning
	entry.NextRetryTime = nil
	if entry.StartedAt == nil {
		entry.StartedAt = &e.now
	}
	return true
}

// taskInstanceContext renders the instance's objective and assembles the
// prompt instance context.
func (e *securityScanExecutionEngine) taskInstanceContext(ctx context.Context, entry *triggersv1alpha1.SecurityScanTaskExecutionStatus, task triggersv1alpha1.SecurityScanTask) (SecurityScanTaskInstance, error) {
	var item json.RawMessage
	var items json.RawMessage
	total := int32(len(e.taskEntries(task.Name)))
	plan := e.fanOutStatus(task.Name)
	chunked := plan != nil && plan.Strategy == "chunk-v1"
	sourceName := e.plannedForEach(task.Name)
	if sourceName != "" {
		if chunked {
			chunkItems, err := e.targetRunsChunkItems(ctx, task, entry)
			if err != nil {
				return SecurityScanTaskInstance{}, err
			}
			items = json.RawMessage(chunkItems)
		} else {
			records, err := e.fanOutRecords(ctx, sourceName)
			if err != nil {
				return SecurityScanTaskInstance{}, fmt.Errorf("forEach source %q: %w", sourceName, err)
			}
			if int(entry.Instance) >= len(records) {
				return SecurityScanTaskInstance{}, fmt.Errorf("forEach source %q no longer yields record %d", sourceName, entry.Instance)
			}
			item = records[entry.Instance]
		}
	}
	objective, err := renderSecurityScanTaskObjective(task.Objective, &securityScanTaskTemplateContext{
		params:      e.params,
		item:        item,
		items:       items,
		recordStart: entry.RecordStart,
		recordEnd:   entry.RecordEnd,
		output:      func(name string) (string, error) { return e.taskOutput(ctx, name) },
	})
	if err != nil {
		return SecurityScanTaskInstance{}, err
	}
	itemJSON := ""
	if item != nil {
		itemJSON = string(item)
	}
	itemsJSON := ""
	if items != nil {
		itemsJSON = string(items)
	}
	// Only the sink states coverage gaps: it writes the report the gaps
	// qualify, and a research task cannot act on them.
	sink := securityScanSinkTasks(e.order)[task.Name]
	var coverageGaps []string
	if sink {
		coverageGaps = e.exec.CoverageGaps
	}
	return SecurityScanTaskInstance{
		Objective:   objective,
		Instance:    entry.Instance,
		Total:       total,
		ItemJSON:    itemJSON,
		ItemsJSON:   itemsJSON,
		Chunked:     chunked,
		RecordStart: entry.RecordStart,
		RecordEnd:   entry.RecordEnd,
		Sink:        sink,
		// A workflow with no research phase never got a platform-executed
		// post-script matrix (materializePostScripts declares it vacuous), so
		// the sink is asked to run the scripts itself instead of being told
		// they already ran.
		PostScriptsInline: !e.workflowHasResearchPhase(),
		CoverageGaps:      coverageGaps,
	}, nil
}

func (e *securityScanExecutionEngine) targetRunsChunkItems(ctx context.Context, task triggersv1alpha1.SecurityScanTask, entry *triggersv1alpha1.SecurityScanTaskExecutionStatus) (string, error) {
	plan := e.fanOutStatus(task.Name)
	if plan == nil || plan.Strategy != "chunk-v1" {
		return "", fmt.Errorf("task %q targetRuns chunk plan is unavailable", task.Name)
	}
	sourceRunName, sourceOutput, records, err := e.targetRunsSource(ctx, plan.SourceTask)
	if err != nil {
		return "", fmt.Errorf("forEach source %q drifted after chunk planning: %w", plan.SourceTask, err)
	}
	if sourceRunName != plan.SourceRunName || securityScanSHA256(sourceOutput) != plan.SourceOutputSHA256 || int32(len(records)) != plan.RecordCount {
		return "", fmt.Errorf("forEach source %q output drifted after chunk planning; re-run the scan", plan.SourceTask)
	}
	start, end := int(entry.RecordStart), int(entry.RecordEnd)
	if start < 0 || end <= start || end > len(records) {
		return "", fmt.Errorf("task %q has invalid persisted record range [%d,%d)", task.Name, start, end)
	}
	items := securityScanIndexedItems(records, start, end)
	if securityScanSHA256(items) != entry.InputSHA256 {
		return "", fmt.Errorf("task %q chunk %d input drifted after chunk planning; re-run the scan", task.Name, entry.Instance)
	}
	return items, nil
}

// finalizePhase computes the execution phase from the task states.
func (e *securityScanExecutionEngine) finalizePhase(failed bool) {
	allSucceeded := true
	anyRunning := false
	for _, entry := range e.exec.Tasks {
		if entry.State != triggersv1alpha1.SecurityScanTaskStateSucceeded {
			allSucceeded = false
		}
		if entry.State == triggersv1alpha1.SecurityScanTaskStateRunning {
			anyRunning = true
		}
	}
	if allSucceeded {
		if e.postScriptJobsInFlight() {
			return // jobs still owe verdicts: the execution is not complete
		}
		e.exec.Phase = triggersv1alpha1.SecurityScanExecutionPhaseSucceeded
		e.exec.CompletedAt = &e.now
		return
	}
	if failed && !anyRunning {
		// Running tasks have drained; nothing new launches after a terminal
		// failure, so whatever is still Pending or Blocked never runs.
		failSecurityScanExecution(e.exec, e.now, "skipped: the execution failed before this task started")
	}
}

// nextRequeue returns the earliest pending retry delay, or 0 for the default
// poll interval.
func (e *securityScanExecutionEngine) nextRequeue() time.Duration {
	var earliest time.Duration
	for _, entry := range e.exec.Tasks {
		if entry.State != triggersv1alpha1.SecurityScanTaskStatePending || entry.NextRetryTime == nil {
			continue
		}
		delay := entry.NextRetryTime.Sub(e.now.Time)
		if delay <= 0 {
			delay = time.Second
		}
		if earliest == 0 || delay < earliest {
			earliest = delay
		}
	}
	if delay := e.postScriptRequeueDelay(); delay > 0 && (earliest == 0 || delay < earliest) {
		earliest = delay
	}
	if earliest > 0 && earliest < securityScanExecutionPollInterval {
		return earliest
	}
	return 0
}

// taskEntries returns pointers to the execution entries of one task, in
// instance order.
func (e *securityScanExecutionEngine) taskEntries(name string) []*triggersv1alpha1.SecurityScanTaskExecutionStatus {
	var entries []*triggersv1alpha1.SecurityScanTaskExecutionStatus
	for i := range e.exec.Tasks {
		if e.exec.Tasks[i].Name == name {
			entries = append(entries, &e.exec.Tasks[i])
		}
	}
	return entries
}

// replaceTaskEntries swaps one task's entries in place, preserving the
// workflow ordering of the surrounding tasks.
func (e *securityScanExecutionEngine) replaceTaskEntries(name string, replacement []triggersv1alpha1.SecurityScanTaskExecutionStatus) {
	var rebuilt []triggersv1alpha1.SecurityScanTaskExecutionStatus
	inserted := false
	for _, entry := range e.exec.Tasks {
		if entry.Name != name {
			rebuilt = append(rebuilt, entry)
			continue
		}
		if !inserted {
			rebuilt = append(rebuilt, replacement...)
			inserted = true
		}
	}
	e.exec.Tasks = rebuilt
}

// getRun fetches a task AgentRun by name with a per-pass cache. A nil run
// with nil error means the run does not exist.
func (e *securityScanExecutionEngine) getRun(ctx context.Context, name string) (*platformv1alpha1.AgentRun, error) {
	if name == "" {
		return nil, nil
	}
	if run, ok := e.runs[name]; ok {
		return run, nil
	}
	run := &platformv1alpha1.AgentRun{}
	err := e.r.Get(ctx, client.ObjectKey{Namespace: e.scan.Namespace, Name: name}, run)
	if apierrors.IsNotFound(err) {
		e.runs[name] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.runs[name] = run
	return run, nil
}

// taskInstanceOutputs returns the structured outputs of one succeeded task's
// instances in instance order. vacuous reports a zero-instance fan-out task
// that completed without running.
func (e *securityScanExecutionEngine) taskInstanceOutputs(ctx context.Context, name string) (outputs []string, vacuous bool, err error) {
	entries := e.taskEntries(name)
	for _, entry := range entries {
		if entry.State != triggersv1alpha1.SecurityScanTaskStateSucceeded {
			return nil, false, fmt.Errorf("task %q has not succeeded", name)
		}
		if out := strings.TrimSpace(entry.StructuredOutput); out != "" {
			if !json.Valid([]byte(out)) {
				return nil, false, fmt.Errorf("task %q controller output is invalid JSON", name)
			}
			outputs = append(outputs, out)
			continue
		}
		if entry.RunName == "" {
			continue // vacuously succeeded zero-instance fan-out marker
		}
		run, getErr := e.getRun(ctx, entry.RunName)
		if getErr != nil {
			return nil, false, getErr
		}
		if run == nil {
			return nil, false, fmt.Errorf("task %q run %q no longer exists; its output is unavailable", name, entry.RunName)
		}
		out := strings.TrimSpace(run.Status.StructuredOutput)
		if out == "" || !json.Valid([]byte(out)) {
			return nil, false, fmt.Errorf("task %q published no structured output", name)
		}
		outputs = append(outputs, out)
	}
	if len(outputs) == 0 {
		return nil, true, nil
	}
	return outputs, false, nil
}

// taskOutput renders one dependency task's output for {{tasks.NAME.output}}:
// a single instance yields its raw structured output; multiple instances (or
// a vacuous fan-out) yield a JSON array of instance outputs in instance
// order.
func (e *securityScanExecutionEngine) taskOutput(ctx context.Context, name string) (string, error) {
	if _, ok := e.tasks[name]; !ok {
		return "", fmt.Errorf("unknown task %q", name)
	}
	outputs, vacuous, err := e.taskInstanceOutputs(ctx, name)
	if err != nil {
		return "", err
	}
	if vacuous {
		return "[]", nil
	}
	if plan := e.fanOutStatus(name); plan != nil && plan.Strategy == "chunk-v1" {
		entries := e.taskEntries(name)
		results := make([]string, 0)
		for i, output := range outputs {
			chunk, chunkErr := validateSecurityScanChunkOutput(output, entries[i].RecordStart, entries[i].RecordEnd)
			if chunkErr != nil {
				return "", fmt.Errorf("task %q: %w", name, chunkErr)
			}
			for _, result := range chunk {
				results = append(results, string(result))
			}
		}
		return "[" + strings.Join(results, ",") + "]", nil
	}
	if len(outputs) == 1 {
		return outputs[0], nil
	}
	return "[" + strings.Join(outputs, ",") + "]", nil
}

// securityScanSinkTasks returns the tasks no other task depends on. Only
// sink tasks receive submit_security_scan_report instructions: they are the
// DAG's terminal aggregation points.
func securityScanSinkTasks(workflow []triggersv1alpha1.SecurityScanTask) map[string]bool {
	sinks := make(map[string]bool, len(workflow))
	for _, task := range workflow {
		sinks[task.Name] = true
	}
	for _, task := range workflow {
		for _, dep := range task.DependsOn {
			delete(sinks, dep)
		}
	}
	return sinks
}

// createScanTaskRun creates one deterministic task AgentRun. It shares the
// coordinator's run construction (labels, owner ref, defaults, event
// context, policy annotations, resolved-refs snapshot) and layers the
// per-task overrides on top: task mode template, per-task model, folded
// timeout, turn/cost limits, tool policy, and the output-schema annotation
// the worker's submit_task_output tool consumes.
func (r *SecurityScanReconciler) createScanTaskRun(ctx context.Context, scan *triggersv1alpha1.SecurityScan, resolved *resolvedSecurityScanSpec, runCtx *securityScanRunContext, exec *triggersv1alpha1.SecurityScanExecutionStatus, task triggersv1alpha1.SecurityScanTask, runName string, inst SecurityScanTaskInstance, firstRun bool) (bool, error) {
	if err := validateSecurityScanTaskSkillRefs(ctx, r.Client, scan.Namespace, scan.Spec.Defaults.SkillRefs, []triggersv1alpha1.SecurityScanTask{task}); err != nil {
		return false, err
	}
	role, err := r.resolveSecurityScanTaskRole(ctx, task)
	if err != nil {
		return false, err
	}
	base, err := r.buildScanRunBase(ctx, scan, resolved, runName, runCtx, role.instructionsSection())
	if err != nil {
		return false, err
	}
	d := base.defaults
	d.SkillRefs = mergeSecurityScanTaskSkillRefs(d.SkillRefs, task.SkillRefs)
	if model := strings.TrimSpace(task.Model); model != "" {
		d.Model = model
	}
	if level := role.spec.ReasoningLevel; level != "" && d.ReasoningLevel == "" {
		// The scan pins reasoning explicitly or not at all; only the latter
		// lets the role's default through, so a scan-level decision always wins.
		d.ReasoningLevel = level
	}
	// Per-task timeout folds with the scan/budget runtime: smaller wins,
	// like budgets.maxRuntime in buildScanRunBase.
	if task.Timeout.Duration > 0 && (d.Timeout.Duration == 0 || task.Timeout.Duration < d.Timeout.Duration) {
		d.Timeout = task.Timeout
	}
	if err := validateTriggerRunDefaults(TriggerRunSpec{
		Namespace:   scan.Namespace,
		TriggerKind: securityScanKind,
		TriggerName: scan.Name,
		Defaults:    d,
	}); err != nil {
		return false, err
	}

	limits := base.limits.DeepCopy()
	if task.MaxTurns > 0 || strings.TrimSpace(task.MaxCostUSD) != "" {
		if limits == nil {
			limits = &platformv1alpha1.AgentRunLimits{}
		}
		if task.MaxTurns > 0 {
			limits.MaxTurns = task.MaxTurns
		}
		// The per-task cost cap folds with the scan-wide budgets.maxCostUSD
		// already in the base limits: smaller wins, so a task can narrow
		// but never loosen the scan budget.
		if cost := strings.TrimSpace(task.MaxCostUSD); cost != "" {
			if scanCost := securityBudgetCostUSD(limits.MaxCostUsd); scanCost < 0 || securityBudgetCostUSD(cost) < scanCost {
				limits.MaxCostUsd = cost
			}
		}
	}
	var toolPolicy *platformv1alpha1.AgentRunToolPolicy
	if task.Tools != nil {
		toolPolicy = securityScanTaskToolPolicy(task, inst)
	}
	// URL-driven web research intentionally retains the complete tool surface.
	// Source-review roles normally suppress repository mutation tools, but that
	// role-level narrowing must not cripple browser/CLI web tasks that have been
	// explicitly configured for a live target. The task's own tools block can
	// still express an operator-requested narrowing when one is present.
	if strings.TrimSpace(scan.Spec.TargetURL) == "" {
		toolPolicy = securityScanApplyRoleToolAccess(toolPolicy, role)
	}

	annotations := base.annotations
	annotations[securityScanTaskLabel] = task.Name
	// The execution id is the aggregation key agent-side finding tools use:
	// every run of one execution reports into the SAME campaign, so the scan's
	// budget and dedupe span the whole DAG instead of a single run. A RESUMED
	// execution deliberately keeps its id — its new task runs continue the
	// same campaign and their findings aggregate with what earlier cycles
	// already reported — while a NEW execution gets a new id and can never mix
	// with a previous one.
	annotations[triggersv1alpha1.SecurityScanExecutionIDAnnotation] = exec.ID
	// One scans-list row per EXECUTION: every task run reports into the
	// same persisted scan record, keyed by the execution record name, so
	// the dashboard shows the execution as a single top-level scan instead
	// of one row per reporting task run.
	annotations[triggersv1alpha1.SecurityScanRecordNameAnnotation] = securityScanExecutionRecordName(scan.Name, exec.ID)
	annotations[triggersv1alpha1.SecurityScanTaskNameAnnotation] = task.Name
	annotations[triggersv1alpha1.SecurityScanTaskRoleAnnotation] = role.name
	if schema := securityScanTaskOutputSchema(task, inst); schema != "" {
		annotations[securityScanTaskOutputSchemaAnnotation] = schema
	}

	modeRef := base.modeRef
	if scan.Spec.Defaults.ModeRef == nil {
		modeRef = &platformv1alpha1.ModeRef{Name: securityScanDefaultModeTemplate(scan.Spec, true)}
	}

	created, _, err := CreateTriggerRun(ctx, r.Client, r.StateStore, TriggerRunSpec{
		RunName:            runName,
		Namespace:          scan.Namespace,
		TriggerKind:        securityScanKind,
		TriggerName:        scan.Name,
		ExternalID:         exec.ID,
		ExternalIdentifier: fmt.Sprintf("%s/%s[%d]", exec.ID, task.Name, inst.Instance),
		SeedMessage:        BuildSecurityScanTaskPromptWithProgram(resolved.spec, securityScanPromptEvent(runCtx), task, inst, role.promptRole(), resolved.program),
		Revision:           base.revision,
		Defaults:           d,
		OwnerRef:           scan,
		Scheme:             r.Scheme,
		Labels: map[string]string{
			securityScanLabel:             securityScanLabelValue(scan.Name),
			securityScanTaskLabel:         task.Name,
			securityScanTaskInstanceLabel: strconv.Itoa(int(inst.Instance)),
		},
		Annotations: annotations,
		Context: &platformv1alpha1.AgentRunContext{
			ProjectRef: &platformv1alpha1.ProjectRef{Kind: securityScanKind, Name: scan.Name},
		},
		ModeRef:       modeRef,
		Limits:        limits,
		ToolPolicy:    toolPolicy,
		SeedLogPrefix: "securityscan",
	})
	if err == nil && firstRun {
		// The execution's FIRST dispatch eagerly creates the execution's one
		// scan record so the scans list shows the whole execution the
		// moment it starts. Later dispatches and every task run's finding
		// tools reuse this row (same record-name key), so exactly one row
		// represents the execution; finalizeExecutionScanRecords settles it
		// when the execution terminates. created=false (the run already
		// existed from a reconcile that crashed before persisting task
		// status) must still ensure the record — the ensure is idempotent,
		// and skipping it here would lose the eager row for good.
		r.ensureSecurityScanRecord(ctx, scan, securityScanExecutionRecordName(scan.Name, exec.ID), annotations)
	}
	return created, err
}

func mergeSecurityScanTaskSkillRefs(defaults, task []platformv1alpha1.NamedRef) []platformv1alpha1.NamedRef {
	refs := make([]platformv1alpha1.NamedRef, 0, len(defaults)+len(task))
	seen := make(map[string]struct{}, cap(refs))
	for _, candidates := range [][]platformv1alpha1.NamedRef{defaults, task} {
		for _, ref := range candidates {
			name := strings.TrimSpace(ref.Name)
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			refs = append(refs, platformv1alpha1.NamedRef{Name: name})
		}
	}
	return refs
}

// securityScanTaskToolPolicy maps a task's tools narrowing to the run's tool
// policy. An allow-list is exclusive, so it would silently strip the
// platform contract tools the engine depends on: findings persistence, the
// typed output the schema contract requires, and the sink task's report
// submission. Auto-append them so a user-provided allow-list can narrow
// workspace tools without breaking the execution contract.
func securityScanTaskToolPolicy(task triggersv1alpha1.SecurityScanTask, inst SecurityScanTaskInstance) *platformv1alpha1.AgentRunToolPolicy {
	allowed := append([]string(nil), task.Tools.Allowed...)
	if len(allowed) > 0 {
		contract := []string{"report_security_finding", "update_security_finding"}
		if strings.TrimSpace(task.OutputSchema) != "" || inst.Chunked {
			contract = append(contract, "submit_task_output")
		}
		if inst.Sink {
			contract = append(contract, "submit_security_scan_report")
		}
		present := make(map[string]bool, len(allowed))
		for _, tool := range allowed {
			present[tool] = true
		}
		for _, tool := range contract {
			if !present[tool] {
				allowed = append(allowed, tool)
			}
		}
	}
	return &platformv1alpha1.AgentRunToolPolicy{
		AllowedTools: allowed,
		DeniedTools:  append([]string(nil), task.Tools.Denied...),
	}
}

func securityScanTaskOutputSchema(task triggersv1alpha1.SecurityScanTask, inst SecurityScanTaskInstance) string {
	if !inst.Chunked {
		return strings.TrimSpace(task.OutputSchema)
	}
	count := inst.RecordEnd - inst.RecordStart
	schema := map[string]any{}
	if declared := strings.TrimSpace(task.OutputSchema); declared != "" {
		// Workflow validation guarantees an object-form JSON Schema. Merge the
		// chunk constraints into it because the worker's intentionally minimal
		// validator does not implement allOf.
		_ = json.Unmarshal([]byte(declared), &schema)
	}
	schema["type"] = "array"
	schema["minItems"] = count
	schema["maxItems"] = count
	items, _ := schema["items"].(map[string]any)
	if items == nil {
		items = map[string]any{}
	}
	items["type"] = "object"
	items["additionalProperties"] = false
	items["required"] = []any{"recordIndex", "result"}
	properties, _ := items["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	properties["recordIndex"] = map[string]any{
		"type": "integer", "minimum": inst.RecordStart, "maximum": inst.RecordEnd - 1,
	}
	if _, ok := properties["result"]; !ok {
		properties["result"] = map[string]any{}
	}
	items["properties"] = properties
	schema["items"] = items
	encoded, _ := json.Marshal(schema)
	return string(encoded)
}

// securityScanExecutionRecordName is the run-name key of the ONE persisted
// scan record a deterministic execution reports into. It reuses the
// coordinator run naming (scan name + execution id suffix) so the record
// reads like the execution's top-level run in the scans list; execution ids
// are unique per scan generation/trigger, so it never collides with a
// coordinator run of the same scan.
func securityScanExecutionRecordName(scanName, execID string) string {
	return securityScanRunName(scanName, execID)
}

// ensureSecurityScanRecord eagerly creates the persisted scan record for a
// newly created scan run so the dashboard scans list shows the scan as soon
// as it is dispatched instead of only after its first persisted finding or
// report. It is called for COORDINATOR runs and for the FIRST dispatched
// task run of a deterministic execution — never for every task run, which
// would flood the scans list with one row per task. The record is keyed by
// (namespace, run name) — the same key the run's finding tools use — so the
// tools' lazy creation path reuses this row and never writes a duplicate
// one. Coordinator rows are settled by finalizeScanRecord when the run
// terminates; deterministic rows by finalizeExecutionScanRecords when the
// execution does. Best-effort: without a findings store, or on error, the
// lazy path still creates the row later.
func (r *SecurityScanReconciler) ensureSecurityScanRecord(ctx context.Context, scan *triggersv1alpha1.SecurityScan, runName string, annotations map[string]string) {
	if r.Findings == nil || runName == "" {
		return
	}
	log := logf.FromContext(ctx)
	existing, err := r.Findings.GetSecurityScan(ctx, scan.Namespace, runName)
	if err != nil {
		log.Error(err, "failed to check for an existing security scan record", "run", runName)
		return
	}
	if existing != nil {
		// Never clobber a record the run (or a previous reconcile) already
		// wrote: the upsert would reset its status and completion fields.
		return
	}
	started := r.now().UTC()
	if _, err := r.Findings.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace:  scan.Namespace,
		ScanName:   scan.Name,
		RunName:    runName,
		Repository: annotations[triggersv1alpha1.SecurityScanRepositoryAnnotation],
		Revision:   annotations[triggersv1alpha1.SecurityScanRevisionAnnotation],
		Status:     "running",
		StartedAt:  &started,
	}); err != nil {
		log.Error(err, "failed to create eager security scan record", "run", runName)
	}
}

// finalizeExecutionScanRecords settles every scan record anchored by a
// terminal deterministic execution: the execution's own record (the one
// scans-list row every task run reports into) plus any legacy per-run rows
// created before the shared record key existed. A row still "running" with
// no completion time inherits the execution's outcome (Succeeded ->
// completed, Cancelled -> cancelled, Failed -> failed) and its completion
// time; a row the sink's submit_security_scan_report already finalized is
// never touched. Idempotent and best-effort: a failed lookup or upsert only
// leaves that row for the lazy paths or a later reconcile.
func (r *SecurityScanReconciler) finalizeExecutionScanRecords(ctx context.Context, scan *triggersv1alpha1.SecurityScan, exec *triggersv1alpha1.SecurityScanExecutionStatus) {
	if r.Findings == nil || exec == nil {
		return
	}
	namespace := scan.Namespace
	status := "failed"
	switch exec.Phase {
	case triggersv1alpha1.SecurityScanExecutionPhaseSucceeded:
		status = "completed"
	case triggersv1alpha1.SecurityScanExecutionPhaseCancelled:
		// A stopped campaign is not a failed one: the scans list already
		// carries "cancelled" rows from the coordinator path, so the
		// deterministic one reports the stop the same way.
		status = "cancelled"
	}
	completed := r.now().UTC()
	if exec.CompletedAt != nil {
		completed = exec.CompletedAt.UTC()
	}
	log := logf.FromContext(ctx)
	seen := map[string]bool{}
	finalize := func(runName string) {
		if runName == "" || seen[runName] {
			return
		}
		seen[runName] = true
		rec, err := r.Findings.GetSecurityScan(ctx, namespace, runName)
		if err != nil {
			log.Error(err, "failed to load scan record for execution finalization", "run", runName)
			return
		}
		if rec == nil || rec.CompletedAt != nil || rec.Status != "running" {
			return
		}
		rec.Status = status
		done := completed
		rec.CompletedAt = &done
		if _, err := r.Findings.UpsertSecurityScan(ctx, rec); err != nil {
			log.Error(err, "failed to finalize scan record for terminal execution", "run", runName)
		}
	}
	for i := range exec.Tasks {
		finalize(exec.Tasks[i].RunName)
		for _, retry := range exec.Tasks[i].Retries {
			finalize(retry.RunName)
		}
	}
	finalize(securityScanExecutionRecordName(scan.Name, exec.ID))
}

// securityScanTaskRole is the effective role contract one deterministic task
// run is dispatched with: the cluster-scoped RoleInstruction (CR name == role
// name) plus its resolved name. It is re-resolved on every dispatch — the
// engine keeps no scheduler state — and snapshotted into the run it creates,
// so editing the CR mid-execution changes the NEXT dispatch and never a run
// that already exists.
type securityScanTaskRole struct {
	name string
	spec platformv1alpha1.RoleInstructionSpec
}

// resolveSecurityScanTaskRole loads the RoleInstruction a task runs as.
// Dispatching without it would run a generic agent under a role's name while
// the prompt claims otherwise, so an unresolvable role fails the task instead.
// The wording carries the "not found"/"invalid" markers
// classifySecurityScanTaskFailure buckets as non-retryable (retrying cannot
// conjure a missing CR); a transient API error keeps its own transient marker
// and therefore stays retryable.
func (r *SecurityScanReconciler) resolveSecurityScanTaskRole(ctx context.Context, task triggersv1alpha1.SecurityScanTask) (securityScanTaskRole, error) {
	name := strings.TrimSpace(task.EffectiveRole())
	role := &platformv1alpha1.RoleInstruction{}
	if err := r.Get(ctx, client.ObjectKey{Name: name}, role); err != nil {
		if apierrors.IsNotFound(err) {
			return securityScanTaskRole{}, fmt.Errorf("task %q: effective role %q not found: no RoleInstruction of that name exists", task.Name, name)
		}
		return securityScanTaskRole{}, fmt.Errorf("task %q: reading RoleInstruction %q for effective role: %w", task.Name, name, err)
	}
	if strings.TrimSpace(role.Spec.Instructions) == "" {
		return securityScanTaskRole{}, fmt.Errorf("task %q: effective role %q is invalid: RoleInstruction has no spec.instructions", task.Name, name)
	}
	return securityScanTaskRole{name: name, spec: role.Spec}, nil
}

// instructionsSection renders the role prompt appended to the run's custom
// instructions. The heading delimits it from scan-level instructions, which
// are preserved: the run must show both contracts, not one replacing the other.
func (role securityScanTaskRole) instructionsSection() string {
	if role.name == "" {
		return ""
	}
	return "## Role: " + role.name + "\n\n" + strings.TrimSpace(role.spec.Instructions)
}

// promptRole projects the resolved contract into the prompt so the seeded
// message states the same role, and the same tool-access constraint, the run
// is actually configured with.
func (role securityScanTaskRole) promptRole() *SecurityScanTaskRole {
	if role.name == "" {
		return nil
	}
	return &SecurityScanTaskRole{
		Name:        role.name,
		Description: strings.TrimSpace(role.spec.Description),
		ToolAccess:  strings.ToLower(strings.TrimSpace(role.spec.ToolAccess)),
		ReadOnly:    role.readOnly(),
	}
}

// readOnly mirrors the runtime's tool-access normalization: read-only and
// analysis are the same restriction, an empty value inherits (no narrowing),
// and an unrecognized value is treated as read-only rather than silently
// granting writes.
func (role securityScanTaskRole) readOnly() bool {
	switch strings.ToLower(strings.TrimSpace(role.spec.ToolAccess)) {
	case "", "full", "execution":
		return false
	default:
		return true
	}
}

// securityScanRoleWriteTools are the repository- and forge-mutating tools a
// read-only role must never register. A security scan reads code and reports
// findings; nothing in it legitimately writes files, commits, or posts to
// GitHub. Bash is deliberately absent: name-based denial is all-or-nothing and
// every analysis role depends on shell inspection, so its writes stay governed
// by the pod's permission mode and command sandbox instead.
var securityScanRoleWriteTools = []string{
	"Write", "Edit", "ApplyPatch", "Move", "Delete",
	"git_commit", "git_push", "git_pull", "git_merge", "git_merge_abort",
	"create_pull_request", "update_pull_request", "submit_pull_request_review",
	"reply_to_review_thread", "resolve_review_thread", "request_re_review",
	"create_github_issue", "update_github_issue", "update_github_issue_labels",
	"add_github_issue_comment", "close_github_issue",
}

// securityScanApplyRoleToolAccess narrows the run's tool policy to the role's
// tool-access contract. spec.toolPolicy is the only run-level lever available:
// permission mode is resolved by the AgentRun controller from the RuntimeProfile
// and ModeTemplate, so a trigger cannot set it per run. Denials win over allows
// in the worker registry, and the task's allow-list is scrubbed too so the
// persisted policy states the truth rather than advertising tools the role can
// never register.
func securityScanApplyRoleToolAccess(policy *platformv1alpha1.AgentRunToolPolicy, role securityScanTaskRole) *platformv1alpha1.AgentRunToolPolicy {
	if !role.readOnly() {
		return policy
	}
	if policy == nil {
		policy = &platformv1alpha1.AgentRunToolPolicy{}
	}
	write := make(map[string]bool, len(securityScanRoleWriteTools))
	denied := make(map[string]bool, len(policy.DeniedTools)+len(securityScanRoleWriteTools))
	for _, tool := range policy.DeniedTools {
		denied[tool] = true
	}
	for _, tool := range securityScanRoleWriteTools {
		write[tool] = true
		if !denied[tool] {
			policy.DeniedTools = append(policy.DeniedTools, tool)
			denied[tool] = true
		}
	}
	if len(policy.AllowedTools) > 0 {
		allowed := make([]string, 0, len(policy.AllowedTools))
		for _, tool := range policy.AllowedTools {
			if !write[tool] {
				allowed = append(allowed, tool)
			}
		}
		policy.AllowedTools = allowed
	}
	return policy
}

// securityScanTaskRunName derives the deterministic task-run name
// secscan-<scan>-<execution>-<task>[-r<attempt>][-i<instance>][-z<resume>],
// truncated with a stable hash suffix past the 63-character object-name
// limit (mirroring securityScanRunName's convention). Attempt 1 of the
// initial cycle carries no -r suffix; resumed cycles always carry the
// resume-token discriminator so their names never collide with earlier
// cycles (the cumulative attempts counter already makes the -r suffix
// differ as well).
func securityScanTaskRunName(scanName, executionID, taskName string, attempt, instance int32, resumeToken string) string {
	sanitize := func(s string) string {
		s = cronNonAlphaNum.ReplaceAllString(strings.ToLower(s), "-")
		return strings.Trim(s, "-")
	}
	name := "secscan-" + sanitize(scanName) + "-" + sanitize(executionID) + "-" + sanitize(taskName)
	if attempt > 1 {
		name += fmt.Sprintf("-r%d", attempt)
	}
	if instance > 0 {
		name += fmt.Sprintf("-i%d", instance)
	}
	if resumeToken != "" {
		hashBytes := sha1.Sum([]byte(resumeToken))
		name += "-z" + hex.EncodeToString(hashBytes[:])[:5]
	}
	if len(name) <= 63 {
		return name
	}
	hashBytes := sha1.Sum([]byte(name))
	hash := hex.EncodeToString(hashBytes[:])[:8]
	truncated := strings.TrimRight(name[:63-len(hash)-1], "-.")
	return truncated + "-" + hash
}

// classifySecurityScanTaskFailure buckets a task-run failure reason.
// Explicit transient signals win, deterministic rejections are
// non-retryable, and anything unrecognized defaults to retryable so flaky
// infrastructure never permanently fails a task.
func classifySecurityScanTaskFailure(reason string) string {
	lower := strings.ToLower(reason)
	for _, marker := range []string{"rate limit", "rate-limit", "ratelimit", "429", "overloaded", "timeout", "timed out", "transient", "connection", "unavailable", "temporar"} {
		if strings.Contains(lower, marker) {
			return triggersv1alpha1.SecurityScanTaskFailureRetryable
		}
	}
	for _, marker := range []string{"unauthorized", "forbidden", "invalid", "not found", "budget", "cost cap", "cost limit", "cost ceiling"} {
		if strings.Contains(lower, marker) {
			return triggersv1alpha1.SecurityScanTaskFailureNonRetryable
		}
	}
	return triggersv1alpha1.SecurityScanTaskFailureRetryable
}

// truncateSecurityScanError bounds error strings persisted into status. The
// 160-character limit applies to both task lastError and retry-attempt
// reasons: attempt history is the dominant per-entry cost in
// status.lastExecution, so one shared tight bound keeps the object small
// even at the execution-entry ceiling.
func truncateSecurityScanError(s string) string {
	const limit = 160
	if len(s) <= limit {
		return s
	}
	return s[:limit-3] + "..."
}

// securityScanTemplateRefPattern matches one whitespace-tolerant {{ ... }}
// template reference.
var securityScanTemplateRefPattern = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

// resolveSecurityScanParameters computes the effective {{params.*}} values:
// spec.parameterValues layered over the referenced workflow's declared
// defaults. It fails when a declared required parameter has no value or when
// any task objective references a parameter that ends up without a value
// (the free-form inline-workflow case included), so an unresolved
// placeholder can never reach a prompt.
func resolveSecurityScanParameters(resolved *resolvedSecurityScanSpec) (map[string]string, error) {
	values := map[string]string{}
	for _, param := range resolved.workflowParams {
		if param.Default != "" {
			values[param.Name] = param.Default
		}
	}
	maps.Copy(values, resolved.spec.ParameterValues)
	hasValue := func(name string) bool {
		value, ok := values[name]
		return ok && strings.TrimSpace(value) != ""
	}
	var missing []string
	seen := map[string]bool{}
	for _, param := range resolved.workflowParams {
		if param.Required && !hasValue(param.Name) {
			missing = append(missing, param.Name)
			seen[param.Name] = true
		}
	}
	for _, task := range resolved.spec.Workflow {
		for _, match := range securityScanTemplateRefPattern.FindAllStringSubmatch(task.Objective, -1) {
			name, ok := strings.CutPrefix(match[1], "params.")
			if !ok {
				continue
			}
			name = strings.TrimSpace(name)
			if !hasValue(name) && !seen[name] {
				missing = append(missing, name)
				seen[name] = true
			}
		}
	}
	if len(missing) > 0 {
		return nil, &securityScanRefError{
			reason:  securityScanReasonInvalidSpec,
			message: fmt.Sprintf("missing required workflow parameter values: %s (set spec.parameterValues)", strings.Join(missing, ", ")),
		}
	}
	return values, nil
}

// renderSecurityScanParams substitutes {{params.NAME}} references, leaving
// every other template construct (tasks/item references consumed by the
// deterministic engine) untouched. Used for coordinator prompts so
// parameters work in both execution modes.
func renderSecurityScanParams(text string, params map[string]string) string {
	return securityScanTemplateRefPattern.ReplaceAllStringFunc(text, func(m string) string {
		ref := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(m, "{{"), "}}"))
		if name, ok := strings.CutPrefix(ref, "params."); ok {
			if value, exists := params[strings.TrimSpace(name)]; exists {
				return value
			}
		}
		return m
	})
}

// securityScanTaskTemplateContext feeds renderSecurityScanTaskObjective.
type securityScanTaskTemplateContext struct {
	params map[string]string
	// output returns the aggregated structured output of a dependency task.
	output func(name string) (string, error)
	// item is this instance's fan-out record; nil outside forEach tasks.
	item json.RawMessage
	// items is a targetRuns chunk's indexed input array.
	items       json.RawMessage
	recordStart int32
	recordEnd   int32
}

// renderSecurityScanTaskObjective substitutes every supported template
// reference in a task objective: {{params.NAME}}, {{tasks.NAME.output}},
// {{tasks.NAME.output.FIELD}}, {{item}}, and {{item.FIELD}}, all
// whitespace-tolerant inside the braces. Unsupported constructs are left
// verbatim; a resolvable-but-broken reference (missing parameter, non-object
// field access, unavailable upstream output) returns an error, which the
// engine treats as a non-retryable task failure. The rendered result is
// capped at securityScanMaxRenderedObjectiveBytes: upstream outputs are
// bounded individually (64KiB each) but an objective can interpolate several
// of them, and an unbounded prompt would be rejected downstream anyway.
func renderSecurityScanTaskObjective(text string, tctx *securityScanTaskTemplateContext) (string, error) {
	var firstErr error
	rendered := securityScanTemplateRefPattern.ReplaceAllStringFunc(text, func(m string) string {
		ref := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(m, "{{"), "}}"))
		value, ok, err := resolveSecurityScanTemplateRef(ref, tctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return m
		}
		if !ok {
			return m
		}
		return value
	})
	if firstErr != nil {
		return "", firstErr
	}
	if len(rendered) > securityScanMaxRenderedObjectiveBytes {
		return "", fmt.Errorf("rendered objective exceeds 256KiB (%d bytes); reduce the interpolated upstream outputs", len(rendered))
	}
	return rendered, nil
}

// resolveSecurityScanTemplateRef resolves one reference. ok=false means the
// construct is not one this renderer owns and stays verbatim.
func resolveSecurityScanTemplateRef(ref string, tctx *securityScanTaskTemplateContext) (string, bool, error) {
	if name, ok := strings.CutPrefix(ref, "params."); ok {
		name = strings.TrimSpace(name)
		value, exists := tctx.params[name]
		if !exists {
			return "", true, fmt.Errorf("parameter %q has no value", name)
		}
		return value, true, nil
	}
	if ref == "item" {
		if tctx.item == nil {
			return "", true, fmt.Errorf("{{item}} is only available in forEach task instances")
		}
		return string(tctx.item), true, nil
	}
	if field, ok := strings.CutPrefix(ref, "item."); ok {
		if tctx.item == nil {
			return "", true, fmt.Errorf("{{item.%s}} is only available in forEach task instances", field)
		}
		value, err := securityScanJSONField(tctx.item, strings.TrimSpace(field), "item")
		return value, true, err
	}
	if ref == "items" {
		if tctx.items == nil {
			return "", true, fmt.Errorf("{{items}} is only available in targetRuns task instances")
		}
		return string(tctx.items), true, nil
	}
	if ref == "range.start" || ref == "range.end" {
		if tctx.items == nil {
			return "", true, fmt.Errorf("{{%s}} is only available in targetRuns task instances", ref)
		}
		if ref == "range.start" {
			return strconv.Itoa(int(tctx.recordStart)), true, nil
		}
		return strconv.Itoa(int(tctx.recordEnd)), true, nil
	}
	if rest, ok := strings.CutPrefix(ref, "tasks."); ok {
		name, accessor, found := strings.Cut(rest, ".")
		if !found {
			return "", false, nil
		}
		name = strings.TrimSpace(name)
		if accessor == "output" {
			out, err := tctx.output(name)
			return out, true, err
		}
		if field, isField := strings.CutPrefix(accessor, "output."); isField {
			out, err := tctx.output(name)
			if err != nil {
				return "", true, err
			}
			value, err := securityScanJSONField(json.RawMessage(out), strings.TrimSpace(field), fmt.Sprintf("task %q output", name))
			return value, true, err
		}
		return "", false, nil
	}
	return "", false, nil
}

// securityScanJSONField extracts one field of a JSON object. Strings render
// unquoted; other values render as compact JSON. A non-object value or an
// unknown field is an error (non-retryable at the engine level).
func securityScanJSONField(raw json.RawMessage, field, what string) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return "", fmt.Errorf("%s is not a JSON object; field %q is unavailable", what, field)
	}
	value, ok := object[field]
	if !ok {
		return "", fmt.Errorf("%s has no field %q", what, field)
	}
	var s string
	if err := json.Unmarshal(value, &s); err == nil {
		return s, nil
	}
	return string(value), nil
}

// finishTerminalExecution runs the once-per-execution side effects of a
// terminal deterministic execution: the aggregate GitHub check, the finding
// notifications, and — after success — the accepted-risk and suppression
// sweeps. Each part is idempotent and self-gated (state hash / execution
// key), so per-task runs never publish individually and repeated reconciles
// never publish twice. The scan-to-scan baseline finalization is
// intentionally NOT run for deterministic executions: findings are spread
// over many task runs and no single run defines the observation baseline, so
// finalizing against one task run would wrongly resolve every finding the
// other tasks reported. The returned flag asks the caller to requeue so
// failed deliveries retry.
func (r *SecurityScanReconciler) finishTerminalExecution(ctx context.Context, scan *triggersv1alpha1.SecurityScan, exec *triggersv1alpha1.SecurityScanExecutionStatus) bool {
	// Settle the execution's scan records first: this runs on EVERY
	// reconcile of a terminal execution (idempotent — settled rows are
	// skipped), so a controller restart or a transient store error after
	// the terminal status was persisted still converges instead of leaving
	// the scans-list row "running" forever. Executions failed outside the
	// engine (invalid spec, reference drift) converge through here too.
	r.finalizeExecutionScanRecords(ctx, scan, exec)
	if exec.Phase == triggersv1alpha1.SecurityScanExecutionPhaseSucceeded && r.Findings != nil {
		log := logf.FromContext(ctx)
		r.sweepSecuritySuppressions(ctx, scan)
		if _, err := r.Findings.ExpireAcceptedRisks(ctx, scan.Namespace); err != nil {
			log.Error(err, "failed to expire accepted-risk findings", "scan", scan.Name)
		}
	}
	retryCheck := r.publishExecutionCheck(ctx, scan, exec)
	retryNotify := r.notifyExecutionFindings(ctx, scan, exec)
	return retryCheck || retryNotify
}

// securityScanExecutionRunNames lists every run name recorded on a terminal
// execution (attempts plus retry history), deduplicated in task order.
func securityScanExecutionRunNames(exec *triggersv1alpha1.SecurityScanExecutionStatus) []string {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, entry := range exec.Tasks {
		add(entry.RunName)
		for _, attempt := range entry.Retries {
			add(attempt.RunName)
		}
	}
	return names
}

// securityScanExecutionReportRuns lists the runs whose stored reports
// represent the execution in the published check: every succeeded task run
// in task order (each sink task stores its own report/SARIF artifact),
// falling back to the last recorded run when none succeeded. The last name
// is the primary run the check summary and details link point at.
func securityScanExecutionReportRuns(exec *triggersv1alpha1.SecurityScanExecutionStatus) []string {
	seen := map[string]bool{}
	var succeeded []string
	lastAny := ""
	for _, entry := range exec.Tasks {
		if entry.RunName == "" || seen[entry.RunName] {
			continue
		}
		seen[entry.RunName] = true
		lastAny = entry.RunName
		if entry.State == triggersv1alpha1.SecurityScanTaskStateSucceeded {
			succeeded = append(succeeded, entry.RunName)
		}
	}
	if len(succeeded) > 0 {
		return succeeded
	}
	if lastAny == "" {
		return nil
	}
	return []string{lastAny}
}
