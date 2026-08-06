package triggers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	securityScanKind = "SecurityScan"

	// securityScanModeTemplate is the ModeTemplate applied to scan runs when
	// spec.defaults.modeRef is not set.
	securityScanModeTemplate = "security-scan"

	// securityScanLabel marks AgentRuns created by a SecurityScan for listing.
	securityScanLabel = "security.gratefulagents.dev/scan"

	// securityScanCleanupFinalizer keeps SecurityScan resources around long
	// enough for the controller to purge persisted findings from the store.
	securityScanCleanupFinalizer = "triggers.gratefulagents.dev/cleanup"

	// securityScanCleanupDeadline bounds how long a failing findings store can
	// delay SecurityScan deletion. Past this deadline the finalizer is removed
	// even when the purge keeps failing, so a permanently broken store cannot
	// wedge deletion; the orphaned rows are keyed by (namespace, scan name)
	// and DeleteSecurityScanData is idempotent, so a later cleanup can still
	// remove them.
	securityScanCleanupDeadline = 15 * time.Minute
)

type SecurityScanReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	StateStore store.StateStore
	// Findings, when non-nil, refreshes status.findings from persisted
	// findings after runs are created or observed.
	Findings store.SecurityFindingStore
	Now      func() time.Time
	// Recorder, when non-nil, emits Kubernetes events for skipped fork
	// contributions, check publish failures, and notification failures.
	Recorder events.EventRecorder
	// DiffLister computes diff-scope changed files; nil uses the GitHub
	// compare API with the trigger repository's read-only credential.
	DiffLister SecurityScanDiffLister
	// CheckPublisher publishes GitHub checks for terminal scan runs; nil
	// uses the trigger repository's credentials against the GitHub API.
	CheckPublisher SecurityCheckPublisher
	// Notifier delivers finding notifications; nil uses the built-in Slack
	// webhook / GitHub issue / Linear issue senders.
	Notifier SecurityScanNotifier
	// DashboardBaseURL, when set, prefixes dashboard links embedded in
	// published checks and notifications (e.g. "https://agents.example.com").
	DashboardBaseURL string
}

// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=securityscans,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=securityscans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=securityscans/finalizers,verbs=update
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=securityworkflows;securityrankers;securitypostscripts;securitypolicypacks,verbs=get;list;watch
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *SecurityScanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	scan := &triggersv1alpha1.SecurityScan{}
	if err := r.Get(ctx, req.NamespacedName, scan); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !scan.DeletionTimestamp.IsZero() {
		return r.reconcileDeletion(ctx, scan)
	}

	if !controllerutil.ContainsFinalizer(scan, securityScanCleanupFinalizer) {
		if err := retrySecurityScanPatch(ctx, r.Client, client.ObjectKeyFromObject(scan), func(fresh *triggersv1alpha1.SecurityScan) {
			controllerutil.AddFinalizer(fresh, securityScanCleanupFinalizer)
		}); err != nil {
			return ctrl.Result{}, err
		}
	}

	if scan.Spec.Suspend {
		if err := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.Phase = "Suspended"
			fresh.Status.NextScheduleTime = nil
			fresh.Status.LastError = ""
			setSecurityScanCondition(fresh, metav1.ConditionFalse, "Suspended", "SecurityScan trigger is suspended")
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if msg := insecureScanDefaults(scan.Spec.Defaults); msg != "" {
		if err := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastError = msg
			setSecurityScanCondition(fresh, metav1.ConditionFalse, "InsecureDefaults", msg)
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if msg := securityScanInvalidSpecMessage(scan.Spec); msg != "" {
		if err := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastError = msg
			setSecurityScanCondition(fresh, metav1.ConditionFalse, securityScanReasonInvalidSpec, msg)
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Retention sweeps run only here — never in the deletion/finalizer path
	// above — so a slow or failing purge can never delay scan deletion.
	retentionRequeue := r.sweepSecurityRetention(ctx, scan)

	res, err := r.reconcileActive(ctx, scan)
	if err != nil {
		return res, err
	}
	// Budget enforcement runs after the dispatch so its BudgetExceeded
	// condition is not overwritten by the branch's own status refresh.
	r.enforceSecurityBudgets(ctx, scan)
	if retentionRequeue > 0 && (res.RequeueAfter == 0 || retentionRequeue < res.RequeueAfter) {
		res.RequeueAfter = retentionRequeue
	}
	return res, nil
}

// reconcileActive dispatches a live (non-deleted, non-suspended, valid) scan
// to the manual, event, one-shot, or scheduled path.
func (r *SecurityScanReconciler) reconcileActive(ctx context.Context, scan *triggersv1alpha1.SecurityScan) (ctrl.Result, error) {
	if scan.Spec.EffectiveExecutionMode() == triggersv1alpha1.SecurityScanExecutionModeDeterministic {
		return r.reconcileDeterministic(ctx, scan)
	}

	if token := pendingManualRunToken(scan); token != "" {
		return r.reconcileRunNow(ctx, scan, token)
	}

	if ev := pendingTriggerEvent(scan); ev != nil {
		return r.reconcileTriggerEvent(ctx, scan, ev)
	}

	if strings.TrimSpace(scan.Spec.Schedule) == "" {
		return r.reconcileOneShot(ctx, scan)
	}
	return r.reconcileScheduled(ctx, scan)
}

// pendingManualRunToken returns the run-now annotation token when it has not
// been consumed yet. Suspended scans never reach this point: the suspend
// branch returns earlier, so a pending token is processed on resume.
func pendingManualRunToken(scan *triggersv1alpha1.SecurityScan) string {
	token := strings.TrimSpace(scan.Annotations[triggersv1alpha1.SecurityScanRunNowAnnotation])
	if token == "" || token == scan.Status.LastManualRunToken {
		return ""
	}
	return token
}

// reconcileRunNow processes a manual run-now request. The request is
// idempotent and durable across controller restarts: the run name is derived
// deterministically from the token, so a crash between run creation and the
// status update re-enters here and CreateTriggerRun observes AlreadyExists
// instead of creating a second run; the consumed token lives in status, never
// in memory. Under concurrencyPolicy Forbid (or empty) an active run consumes
// the token without creating a run and reports ConcurrencyBlocked.
func (r *SecurityScanReconciler) reconcileRunNow(ctx context.Context, scan *triggersv1alpha1.SecurityScan, token string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	externalID := "manual-" + securityScanManualRunSuffix(token)

	if scan.Spec.ConcurrencyPolicy == "" || scan.Spec.ConcurrencyPolicy == triggersv1alpha1.SecurityScanConcurrencyForbid {
		activeRun, err := r.activeScanRun(ctx, scan, externalID)
		if err != nil {
			return ctrl.Result{}, err
		}
		if activeRun != nil {
			msg := fmt.Sprintf("manual run skipped: previous run %s still active", activeRun.Name)
			log.Info("skipping manual scan AgentRun because previous run is still active", "activeRun", activeRun.Name)
			if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
				fresh.Status.LastManualRunToken = token
				fresh.Status.LastError = msg
				setSecurityScanCondition(fresh, metav1.ConditionFalse, "ConcurrencyBlocked", msg)
			}); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
	}

	runName := securityScanRunName(scan.Name, externalID)
	created, resolvedRefs, execStatus, err := r.createScanRun(ctx, scan, runName, externalID, externalID, nil)
	if err != nil {
		log.Error(err, "failed to create manual scan AgentRun", "run", runName)
		reason := securityScanRunFailureReason(err)
		if statusErr := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastError = err.Error()
			setSecurityScanCondition(fresh, metav1.ConditionFalse, reason, err.Error())
		}); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	now := metav1.NewTime(r.now())
	generation := scan.Generation
	oneShot := strings.TrimSpace(scan.Spec.Schedule) == ""
	if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.Phase = "Running"
		fresh.Status.LastRunName = runName
		fresh.Status.LastScanTime = &now
		fresh.Status.LastManualRunToken = token
		fresh.Status.LastError = ""
		if oneShot {
			// The manual run satisfies the current generation, so the
			// run-once-per-generation path does not immediately start a
			// second, overlapping run.
			fresh.Status.ObservedGeneration = generation
		}
		if created {
			fresh.Status.RunsCreated++
			fresh.Status.ManualRunsCreated++
			fresh.Status.LastResolvedRefs = resolvedRefs
		}
		applyCoordinatorExecutionStatus(fresh, execStatus, created)
		setSecurityScanCondition(fresh, metav1.ConditionTrue, "ManualRunStarted", "Manual scan AgentRun created")
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// securityScanManualRunSuffix derives a short deterministic run-name suffix
// from a run-now token, which is opaque and not DNS-safe.
func securityScanManualRunSuffix(token string) string {
	hashBytes := sha1.Sum([]byte(token))
	return hex.EncodeToString(hashBytes[:])[:10]
}

// reconcileDeletion purges persisted findings before releasing the cleanup
// finalizer. When Findings is nil there is no persisted data to purge and the
// finalizer is released immediately. Transient store errors are returned so
// controller-runtime requeues with exponential backoff, but once the deletion
// has been pending longer than securityScanCleanupDeadline the finalizer is
// removed anyway: a permanently failing store orphans the persisted rows
// instead of wedging deletion forever.
func (r *SecurityScanReconciler) reconcileDeletion(ctx context.Context, scan *triggersv1alpha1.SecurityScan) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(scan, securityScanCleanupFinalizer) {
		return ctrl.Result{}, nil
	}
	if r.Findings != nil {
		if err := r.Findings.DeleteSecurityScanData(ctx, scan.Namespace, scan.Name); err != nil {
			if r.now().Sub(scan.DeletionTimestamp.Time) < securityScanCleanupDeadline {
				return ctrl.Result{}, fmt.Errorf("purging security scan data for %s/%s: %w", scan.Namespace, scan.Name, err)
			}
			logf.FromContext(ctx).Error(err, "removing SecurityScan cleanup finalizer despite purge failure: cleanup deadline exceeded", "scan", scan.Name)
		}
	}
	if err := retrySecurityScanPatch(ctx, r.Client, client.ObjectKeyFromObject(scan), func(fresh *triggersv1alpha1.SecurityScan) {
		controllerutil.RemoveFinalizer(fresh, securityScanCleanupFinalizer)
	}); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

// insecureScanDefaults rejects defaults that would run untrusted third-party
// code without the command sandbox or with cluster-admin privileges.
func insecureScanDefaults(d triggersv1alpha1.AgentRunDefaults) string {
	switch {
	case d.DisableCommandSandbox:
		return "spec.defaults.disableCommandSandbox is not allowed for SecurityScans: scans execute untrusted third-party code"
	case d.KubernetesAdmin:
		return "spec.defaults.kubernetesAdmin is not allowed for SecurityScans: scans execute untrusted third-party code"
	}
	return ""
}

// reconcileOneShot runs the scan exactly once per spec generation: the run is
// created when status.observedGeneration does not match metadata.generation,
// and never again until the spec changes.
func (r *SecurityScanReconciler) reconcileOneShot(ctx context.Context, scan *triggersv1alpha1.SecurityScan) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if scan.Status.ObservedGeneration == scan.Generation && scan.Status.LastRunName != "" {
		terminal, err := r.lastRunTerminal(ctx, scan)
		if err != nil {
			return ctrl.Result{}, err
		}
		phase := "Running"
		retryPostRun := false
		if terminal {
			phase = "Completed"
			r.finalizeCompletedRun(ctx, scan)
			retryPostRun = r.finishTerminalRun(ctx, scan)
		}
		if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.Phase = phase
			fresh.Status.LastError = ""
			setSecurityScanCondition(fresh, metav1.ConditionTrue, "ScanUpToDate", "Scan already ran for the current spec generation")
		}); err != nil {
			return ctrl.Result{}, err
		}
		if terminal {
			if retryPostRun {
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	runName := securityScanRunName(scan.Name, fmt.Sprintf("g%d", scan.Generation))
	externalID := fmt.Sprintf("generation-%d", scan.Generation)
	created, resolvedRefs, execStatus, err := r.createScanRun(ctx, scan, runName, externalID, externalID, nil)
	if err != nil {
		log.Error(err, "failed to create scan AgentRun", "run", runName)
		reason := securityScanRunFailureReason(err)
		if statusErr := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastError = err.Error()
			setSecurityScanCondition(fresh, metav1.ConditionFalse, reason, err.Error())
		}); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	now := metav1.NewTime(r.now())
	generation := scan.Generation
	if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.Phase = "Running"
		fresh.Status.ObservedGeneration = generation
		fresh.Status.LastRunName = runName
		fresh.Status.LastScanTime = &now
		fresh.Status.LastError = ""
		if created {
			fresh.Status.RunsCreated++
			fresh.Status.LastResolvedRefs = resolvedRefs
		}
		applyCoordinatorExecutionStatus(fresh, execStatus, created)
		setSecurityScanCondition(fresh, metav1.ConditionTrue, "ScanStarted", "Scan AgentRun created")
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

func (r *SecurityScanReconciler) reconcileScheduled(ctx context.Context, scan *triggersv1alpha1.SecurityScan) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

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
		lastRunDone := false
		if scan.Status.LastRunName != "" {
			terminal, err := r.lastRunTerminal(ctx, scan)
			if err != nil {
				return ctrl.Result{}, err
			}
			lastRunDone = terminal
		}
		retryPostRun := false
		if lastRunDone {
			r.finalizeCompletedRun(ctx, scan)
			retryPostRun = r.finishTerminalRun(ctx, scan)
		}
		next := metav1.NewTime(scheduledTime)
		if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
			if lastRunDone && fresh.Status.Phase == "Running" {
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
		}); err != nil {
			return ctrl.Result{}, err
		}
		delay := requeueAfter(scheduledTime.Sub(now))
		if retryPostRun && delay > time.Minute {
			delay = time.Minute
		}
		return ctrl.Result{RequeueAfter: delay}, nil
	}

	if scan.Spec.ConcurrencyPolicy == "" || scan.Spec.ConcurrencyPolicy == triggersv1alpha1.SecurityScanConcurrencyForbid {
		activeRun, err := r.activeScanRun(ctx, scan, scheduledTime.UTC().Format(time.RFC3339))
		if err != nil {
			return ctrl.Result{}, err
		}
		if activeRun != nil {
			nextScheduledTime := schedule.Next(now)
			next := metav1.NewTime(nextScheduledTime)
			msg := fmt.Sprintf("skipped tick %s: previous run %s still active", scheduledTime.UTC().Format(time.RFC3339), activeRun.Name)
			log.Info("skipping scheduled scan AgentRun because previous run is still active", "scheduledTime", scheduledTime, "activeRun", activeRun.Name)
			if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
				fresh.Status.NextScheduleTime = &next
				fresh.Status.ObservedSchedule = observedSchedule
				fresh.Status.ObservedTimeZone = observedTimeZone
				fresh.Status.ObservedGeneration = scan.Generation
				fresh.Status.LastError = msg
				setSecurityScanCondition(fresh, metav1.ConditionFalse, "ConcurrencyBlocked", msg)
			}); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: requeueAfter(nextScheduledTime.Sub(now))}, nil
		}
	}

	runName := securityScanRunName(scan.Name, scheduledTime.UTC().Format("20060102150405"))
	scheduledID := scheduledTime.UTC().Format(time.RFC3339)
	created, resolvedRefs, execStatus, err := r.createScanRun(ctx, scan, runName, scheduledID, scheduledTime.Format(time.RFC3339), nil)
	if err != nil {
		log.Error(err, "failed to create scheduled scan AgentRun", "scheduledTime", scheduledTime)
		reason := securityScanRunFailureReason(err)
		if statusErr := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastError = err.Error()
			setSecurityScanCondition(fresh, metav1.ConditionFalse, reason, err.Error())
		}); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	nextScheduledTime := schedule.Next(now)
	last := metav1.NewTime(scheduledTime)
	next := metav1.NewTime(nextScheduledTime)
	if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.Phase = "Running"
		fresh.Status.LastScanTime = &last
		fresh.Status.NextScheduleTime = &next
		fresh.Status.LastRunName = runName
		fresh.Status.ObservedSchedule = observedSchedule
		fresh.Status.ObservedTimeZone = observedTimeZone
		fresh.Status.ObservedGeneration = scan.Generation
		fresh.Status.LastError = ""
		if created {
			fresh.Status.RunsCreated++
			fresh.Status.LastResolvedRefs = resolvedRefs
		}
		applyCoordinatorExecutionStatus(fresh, execStatus, created)
		setSecurityScanCondition(fresh, metav1.ConditionTrue, "Scheduled", "SecurityScan schedule is valid")
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter(nextScheduledTime.Sub(now))}, nil
}

// securityScanRunContext carries repository-event context into run creation.
// A nil context means a normal (manual, one-shot, or scheduled) run.
type securityScanRunContext struct {
	Event *SecurityScanTriggerEvent
	// ChangedFiles scopes a diff-scope run; empty with a non-empty
	// DiffFallback means diff scope was requested but unavailable.
	ChangedFiles []string
	DiffFallback string
}

func (r *SecurityScanReconciler) createScanRun(ctx context.Context, scan *triggersv1alpha1.SecurityScan, runName, externalID, externalIdentifier string, runCtx *securityScanRunContext) (bool, []triggersv1alpha1.SecurityScanResolvedRef, *triggersv1alpha1.SecurityScanExecutionStatus, error) {
	// Resolve library references at run-creation time and build the prompt
	// from the resolved snapshot: the seed message is persisted when the run
	// is created, so later edits to the referenced resources never change
	// this run.
	resolved, err := resolveSecurityScanRefs(ctx, r.Client, scan)
	if err != nil {
		return false, nil, nil, err
	}
	// Parameters are substituted into the task objectives BEFORE prompt
	// construction so {{params.*}} references work identically in
	// coordinator and deterministic mode; a missing required parameter
	// rejects the scan instead of leaking an unresolved placeholder.
	params, err := resolveSecurityScanParameters(resolved)
	if err != nil {
		return false, nil, nil, err
	}
	for i := range resolved.spec.Workflow {
		resolved.spec.Workflow[i].Objective = renderSecurityScanParams(resolved.spec.Workflow[i].Objective, params)
	}

	base, err := r.buildScanRunBase(ctx, scan, resolved, runName, runCtx)
	if err != nil {
		return false, nil, nil, err
	}
	bound, boundNote := r.coordinatorParallelismBound(ctx, scan, resolved.spec)

	created, _, err := CreateTriggerRun(ctx, r.Client, r.StateStore, TriggerRunSpec{
		RunName:            runName,
		Namespace:          scan.Namespace,
		TriggerKind:        securityScanKind,
		TriggerName:        scan.Name,
		ExternalID:         externalID,
		ExternalIdentifier: externalIdentifier,
		SeedMessage:        BuildSecurityScanPromptWithEvent(resolved.spec, securityScanPromptEvent(runCtx), bound),
		Revision:           base.revision,
		Defaults:           base.defaults,
		OwnerRef:           scan,
		Scheme:             r.Scheme,
		Labels:             map[string]string{securityScanLabel: securityScanLabelValue(scan.Name)},
		Annotations:        base.annotations,
		Context: &platformv1alpha1.AgentRunContext{
			ProjectRef: &platformv1alpha1.ProjectRef{Kind: securityScanKind, Name: scan.Name},
		},
		ModeRef:       base.modeRef,
		Limits:        base.limits,
		SeedLogPrefix: "securityscan",
	})
	if err != nil {
		return created, resolved.refs, nil, err
	}
	// The coordinator execution record is informational: it publishes the
	// concurrency bound actually stated in the prompt. Its phase stays
	// Running; the authoritative run outcome remains the AgentRun phase
	// tracked through status.lastRunName.
	startedAt := metav1.NewTime(r.now())
	execStatus := &triggersv1alpha1.SecurityScanExecutionStatus{
		ID:                       externalID,
		Mode:                     triggersv1alpha1.SecurityScanExecutionModeCoordinator,
		Phase:                    triggersv1alpha1.SecurityScanExecutionPhaseRunning,
		EffectiveParallelism:     bound,
		EffectiveParallelismNote: boundNote,
		StartedAt:                &startedAt,
	}
	return created, resolved.refs, execStatus, nil
}

// coordinatorParallelismBound computes the concurrency bound a coordinator
// run can actually honor: spec parallelism clamped to the mode template's
// in-process sub-agent ceiling. ModeTemplates are cluster-scoped; a missing
// or unreadable template falls back to the spec value with an explanatory
// note.
func (r *SecurityScanReconciler) coordinatorParallelismBound(ctx context.Context, scan *triggersv1alpha1.SecurityScan, spec triggersv1alpha1.SecurityScanSpec) (int32, string) {
	bound := spec.EffectiveParallelism()
	modeName := securityScanModeTemplate
	if ref := scan.Spec.Defaults.ModeRef; ref != nil && strings.TrimSpace(ref.Name) != "" {
		modeName = strings.TrimSpace(ref.Name)
	}
	mode := &platformv1alpha1.ModeTemplate{}
	if err := r.Get(ctx, client.ObjectKey{Name: modeName}, mode); err != nil {
		return bound, fmt.Sprintf("mode template %q could not be read (%s); using spec parallelism %d", modeName, apierrors.ReasonForError(err), bound)
	}
	if c := mode.Spec.Constraints; c != nil && c.MaxConcurrentSubAgents > 0 && c.MaxConcurrentSubAgents < bound {
		return c.MaxConcurrentSubAgents, fmt.Sprintf("parallelism %d clamped to %d by mode template %q sub-agent ceiling", bound, c.MaxConcurrentSubAgents, modeName)
	}
	return bound, ""
}

// applyCoordinatorExecutionStatus records the coordinator execution snapshot
// without clobbering an existing record for the same dispatch (crash
// recovery re-enters run creation with created=false).
func applyCoordinatorExecutionStatus(fresh *triggersv1alpha1.SecurityScan, execStatus *triggersv1alpha1.SecurityScanExecutionStatus, created bool) {
	if execStatus == nil {
		return
	}
	if !created && fresh.Status.LastExecution != nil && fresh.Status.LastExecution.ID == execStatus.ID {
		return
	}
	fresh.Status.LastExecution = execStatus
}

// securityScanRunBase carries the assembled, validated run inputs shared by
// the coordinator scan run and every deterministic task run: merged
// defaults, policy/reporting annotations, the pinned revision, budget-derived
// limits, and the mode reference.
type securityScanRunBase struct {
	defaults    triggersv1alpha1.AgentRunDefaults
	annotations map[string]string
	revision    string
	limits      *platformv1alpha1.AgentRunLimits
	modeRef     *platformv1alpha1.ModeRef
}

// buildScanRunBase assembles the shared AgentRun inputs for one scan run.
// Enforcement (policy floors, budgets) has already been folded into resolved
// by resolveSecurityScanRefs, so every limit derives from CRD spec before
// prompt construction.
func (r *SecurityScanReconciler) buildScanRunBase(ctx context.Context, scan *triggersv1alpha1.SecurityScan, resolved *resolvedSecurityScanSpec, runName string, runCtx *securityScanRunContext) (*securityScanRunBase, error) {
	d := scan.Spec.Defaults
	d.RepoURL = scan.Spec.RepoURL
	d.BaseBranch = scan.Spec.EffectiveBaseBranch()
	if runCtx != nil && runCtx.Event != nil && runCtx.Event.Fork {
		// Untrusted fork contribution: never hand the run a GitHub
		// credential, even when the scan's defaults configure one.
		d.Secrets.GithubToken = ""
	}
	if len(scan.Spec.AdditionalRepos) > 0 {
		d.AdditionalRepos = append([]string(nil), scan.Spec.AdditionalRepos...)
	}
	if scan.Spec.MaxRuntime.Duration > 0 {
		d.Timeout = scan.Spec.MaxRuntime
	}
	// Budgets come from the RESOLVED spec (scan merged with the policy pack,
	// enforced floors already checked), so every limit derives from CRD spec
	// before prompt construction and model output can never relax it. What
	// the run supports natively becomes an AgentRun limit; the rest is
	// monitored controller-side each reconcile.
	var runLimits *platformv1alpha1.AgentRunLimits
	if budgets := resolved.spec.Budgets; budgets != nil {
		if budgets.MaxRuntime.Duration > 0 && (d.Timeout.Duration == 0 || budgets.MaxRuntime.Duration < d.Timeout.Duration) {
			d.Timeout = budgets.MaxRuntime
		}
		if strings.TrimSpace(budgets.MaxCostUSD) != "" {
			runLimits = &platformv1alpha1.AgentRunLimits{MaxCostUsd: strings.TrimSpace(budgets.MaxCostUSD)}
		}
	}
	var modeRef *platformv1alpha1.ModeRef
	if d.ModeRef == nil {
		modeRef = &platformv1alpha1.ModeRef{Name: securityScanModeTemplate}
	}

	if err := validateTriggerRunDefaults(TriggerRunSpec{
		Namespace:   scan.Namespace,
		TriggerKind: securityScanKind,
		TriggerName: scan.Name,
		Defaults:    d,
	}); err != nil {
		return nil, err
	}

	provider := d.EffectiveProvider()
	// Reporting-policy annotations come from the RESOLVED spec so policy
	// pack defaults and enforced floors apply to what agents may report.
	dedupePermille := int32(0)
	if resolved.spec.DedupeEnabled() {
		dedupePermille = resolved.spec.DedupeSimilarityThresholdPermille()
	}
	annotations := map[string]string{
		runModeAnnotation: "auto",
		triggersv1alpha1.SecurityScanNameAnnotation:           scan.Name,
		triggersv1alpha1.SecurityScanRepositoryAnnotation:     scan.Spec.RepoURL,
		triggersv1alpha1.SecurityScanMinSeverityAnnotation:    resolved.spec.EffectiveMinSeverity(),
		triggersv1alpha1.SecurityScanDedupePermilleAnnotation: strconv.Itoa(int(dedupePermille)),
	}
	if budgets := resolved.spec.Budgets; budgets != nil && budgets.MaxFindings > 0 {
		annotations[triggersv1alpha1.SecurityScanMaxFindingsAnnotation] = strconv.Itoa(int(budgets.MaxFindings))
	}
	if rev := strings.TrimSpace(scan.Spec.Revision); rev != "" {
		annotations[triggersv1alpha1.SecurityScanRevisionAnnotation] = rev
	}
	revision := strings.TrimSpace(scan.Spec.Revision)
	if runCtx != nil && runCtx.Event != nil {
		// Platform-stamped from the verified webhook payload; overrides any
		// pinned spec revision so the run and any published check agree on
		// the commit.
		revision = strings.TrimSpace(runCtx.Event.Revision)
		annotations[triggersv1alpha1.SecurityScanRevisionAnnotation] = revision
		if payload, err := json.Marshal(runCtx.Event); err == nil {
			annotations[triggersv1alpha1.SecurityScanEventAnnotation] = string(payload)
		}
	}
	if strings.TrimSpace(d.CustomInstructions) != "" {
		instructionsName := runName + "-instructions"
		instructions := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: instructionsName, Namespace: scan.Namespace}, Data: map[string]string{"instructions.md": d.CustomInstructions}}
		if err := ctrl.SetControllerReference(scan, instructions, r.Scheme); err != nil {
			return nil, fmt.Errorf("setting owner reference on instructions ConfigMap: %w", err)
		}
		if err := r.Create(ctx, instructions); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating instructions ConfigMap: %w", err)
		}
		annotations["platform.gratefulagents.dev/instructions-configmap-ref"] = instructionsName
	}
	if triggersv1alpha1.IsOpenAICompatibleProvider(provider) {
		annotations["platform.gratefulagents.dev/openai-api-mode"] = triggersv1alpha1.NormalizeOpenAIAPIForProvider(provider, d.OpenAIAPI)
	}
	if refsJSON := securityScanResolvedRefsJSON(resolved.refs); refsJSON != "" {
		annotations[triggersv1alpha1.SecurityScanResolvedRefsAnnotation] = refsJSON
	}

	return &securityScanRunBase{
		defaults:    d,
		annotations: annotations,
		revision:    revision,
		limits:      runLimits,
		modeRef:     modeRef,
	}, nil
}

// activeScanRun returns a non-terminal AgentRun owned by this scan, ignoring
// the run identified by excludeExternalID (the run the caller is about to
// create or may have already created for the current tick or manual request).
func (r *SecurityScanReconciler) activeScanRun(ctx context.Context, scan *triggersv1alpha1.SecurityScan, excludeExternalID string) (*platformv1alpha1.AgentRun, error) {
	runs := &platformv1alpha1.AgentRunList{}
	if err := r.List(ctx, runs, client.InNamespace(scan.Namespace), client.MatchingLabels{securityScanLabel: securityScanLabelValue(scan.Name)}); err != nil {
		return nil, fmt.Errorf("listing AgentRuns: %w", err)
	}
	for i := range runs.Items {
		run := &runs.Items[i]
		if !TriggerRunMatches(run, securityScanKind, scan.Name) {
			continue
		}
		if run.Spec.Trigger.ExternalRef != nil && strings.TrimSpace(run.Spec.Trigger.ExternalRef.ID) == excludeExternalID {
			continue
		}
		if !isCronRunTerminal(run.Status.Phase) {
			return run, nil
		}
	}
	return nil, nil
}

func (r *SecurityScanReconciler) lastRunTerminal(ctx context.Context, scan *triggersv1alpha1.SecurityScan) (bool, error) {
	run := &platformv1alpha1.AgentRun{}
	err := r.Get(ctx, client.ObjectKey{Namespace: scan.Namespace, Name: scan.Status.LastRunName}, run)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("getting scan AgentRun: %w", err)
	}
	return isCronRunTerminal(run.Status.Phase), nil
}

// finalizeCompletedRun sweeps expired accepted-risk findings and finalizes
// the scan-to-scan baseline (marking findings absent from the just-completed
// run resolved) once the scan's last run has terminated SUCCESSFULLY. A
// failed or cancelled run never defines a baseline: findings it did not
// re-observe must not be marked resolved. Both store calls are idempotent,
// so re-running on every status refresh is safe; errors are best-effort
// (logged, never failing the reconcile).
func (r *SecurityScanReconciler) finalizeCompletedRun(ctx context.Context, scan *triggersv1alpha1.SecurityScan) {
	if r.Findings == nil || scan.Status.LastRunName == "" {
		return
	}
	log := logf.FromContext(ctx)
	r.sweepSecuritySuppressions(ctx, scan)
	run := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: scan.Namespace, Name: scan.Status.LastRunName}, run); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "failed to get scan AgentRun for baseline finalization", "run", scan.Status.LastRunName)
		}
		return
	}
	if run.Status.Phase != platformv1alpha1.AgentRunPhaseSucceeded {
		return
	}
	if _, err := r.Findings.ExpireAcceptedRisks(ctx, scan.Namespace); err != nil {
		log.Error(err, "failed to expire accepted-risk findings", "scan", scan.Name)
	}
	if _, err := r.Findings.FinalizeSecurityScanBaseline(ctx, scan.Namespace, scan.Status.LastRunName); err != nil {
		log.Error(err, "failed to finalize scan baseline", "scan", scan.Name, "run", scan.Status.LastRunName)
	}
}

// finishTerminalRun runs the post-terminal side effects that talk to
// external systems: GitHub check publishing and finding notifications. Both
// are best-effort, idempotent, and record their own status; the returned
// flag asks the caller to requeue soon so failures retry.
func (r *SecurityScanReconciler) finishTerminalRun(ctx context.Context, scan *triggersv1alpha1.SecurityScan) bool {
	retryCheck := r.publishRunCheck(ctx, scan)
	retryNotify := r.notifyRunFindings(ctx, scan)
	return retryCheck || retryNotify
}

// securityScanSeverities orders severities from most to least severe.
var securityScanSeverities = []string{"critical", "high", "medium", "low", "info"}

// securityScanFindingSummary carries scan-wide finding counts plus open
// counts by severity used to evaluate spec.failOnSeverity.
type securityScanFindingSummary struct {
	counts         *triggersv1alpha1.SecurityScanFindingCounts
	openBySeverity map[string]int32
}

// summarizeFindings refreshes finding counts from the finding store,
// aggregated across every run of the scan (runName is intentionally empty so
// findings re-attributed across runs stay counted). It is best-effort: a nil
// store or a summarize error leaves status.findings as is.
func (r *SecurityScanReconciler) summarizeFindings(ctx context.Context, scan *triggersv1alpha1.SecurityScan) *securityScanFindingSummary {
	if r.Findings == nil {
		return nil
	}
	counts, err := r.Findings.SummarizeSecurityFindings(ctx, scan.Namespace, scan.Name, "", false)
	if err != nil {
		logf.FromContext(ctx).Error(err, "failed to summarize security findings", "scan", scan.Name)
		return nil
	}
	openBySeverity := make(map[string]int32, len(securityScanSeverities))
	for _, severity := range securityScanSeverities {
		if open, ok := counts["open_"+severity]; ok {
			openBySeverity[severity] = open
			continue
		}
		openBySeverity[severity] = counts[severity]
	}
	return &securityScanFindingSummary{
		counts: &triggersv1alpha1.SecurityScanFindingCounts{
			Total:    counts["total"],
			Open:     counts["open"],
			Critical: counts["critical"],
			High:     counts["high"],
			Medium:   counts["medium"],
			Low:      counts["low"],
			Info:     counts["info"],
		},
		openBySeverity: openBySeverity,
	}
}

// updateStatus applies mutate to a fresh copy of the scan, then folds in the
// finding counts and the failOnSeverity threshold, which overrides the Ready
// condition set by mutate. The threshold only counts open findings, so
// findings triaged away no longer trip it.
func (r *SecurityScanReconciler) updateStatus(ctx context.Context, scan *triggersv1alpha1.SecurityScan, findings *securityScanFindingSummary, mutate func(*triggersv1alpha1.SecurityScan)) error {
	failOn := r.effectiveFailOnSeverity(ctx, scan)
	err := retrySecurityScanStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(scan), func(fresh *triggersv1alpha1.SecurityScan) {
		mutate(fresh)
		// A recorded budget breach stays visible until the budget evaluation
		// clears it, like the failOnSeverity threshold below.
		if fresh.Status.Budget != nil && fresh.Status.Budget.Exceeded {
			setSecurityScanCondition(fresh, metav1.ConditionFalse, securityScanReasonBudgetExceeded, fresh.Status.Budget.Message)
		}
		if findings == nil {
			return
		}
		fresh.Status.Findings = findings.counts
		if failOn == "" {
			return
		}
		if n := openFindingsAtOrAbove(findings.openBySeverity, failOn); n > 0 {
			msg := fmt.Sprintf("%d open findings at or above severity %q", n, failOn)
			setSecurityScanCondition(fresh, metav1.ConditionFalse, "FindingsExceedThreshold", msg)
		}
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("updating SecurityScan status: %w", err)
	}
	return nil
}

// openFindingsAtOrAbove counts open findings with severity at or above min.
func openFindingsAtOrAbove(openBySeverity map[string]int32, min string) int32 {
	var total int32
	for _, severity := range securityScanSeverities {
		total += openBySeverity[severity]
		if severity == min {
			return total
		}
	}
	return 0
}

// nextSecurityScanScheduleTime mirrors nextCronScheduleTime for SecurityScan:
// skipped due instants are recorded as processed and never backfilled.
func nextSecurityScanScheduleTime(scan *triggersv1alpha1.SecurityScan, schedule interface{ Next(time.Time) time.Time }, observedSchedule, observedTimeZone string, now time.Time) time.Time {
	if scan.Status.ObservedSchedule != observedSchedule || scan.Status.ObservedTimeZone != observedTimeZone {
		return schedule.Next(now)
	}
	if scan.Status.NextScheduleTime != nil && !scan.Status.NextScheduleTime.IsZero() {
		return scan.Status.NextScheduleTime.Time
	}
	if scan.Status.LastScanTime != nil && !scan.Status.LastScanTime.IsZero() {
		return schedule.Next(scan.Status.LastScanTime.Time)
	}
	return schedule.Next(now)
}

// securityScanLabelValue converts a scan name into a valid label value: names
// longer than the 63-character label limit are truncated and suffixed with a
// short hash. TriggerRunMatches on the untruncated trigger name remains the
// authoritative run-ownership check.
func securityScanLabelValue(scanName string) string {
	if len(scanName) <= 63 {
		return scanName
	}
	hashBytes := sha1.Sum([]byte(scanName))
	hash := hex.EncodeToString(hashBytes[:])[:8]
	truncated := strings.TrimRight(scanName[:63-len(hash)-1], "-.")
	return truncated + "-" + hash
}

func securityScanRunName(sourceName, suffix string) string {
	base := cronNonAlphaNum.ReplaceAllString(strings.ToLower(sourceName), "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "run"
	}
	name := "secscan-" + base + "-" + suffix
	if len(name) <= 63 {
		return name
	}
	hashBytes := sha1.Sum([]byte(name))
	hash := hex.EncodeToString(hashBytes[:])[:8]
	maxBase := max(63-len("secscan-")-len("-")-len(suffix)-len("-")-len(hash), 1)
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	return "secscan-" + base + "-" + suffix + "-" + hash
}

func setSecurityScanCondition(scan *triggersv1alpha1.SecurityScan, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&scan.Status.Conditions, metav1.Condition{
		Type:               triggersv1alpha1.ConditionSecurityScanReady,
		Status:             status,
		ObservedGeneration: scan.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func retrySecurityScanPatch(ctx context.Context, c client.Client, key client.ObjectKey, mutate func(*triggersv1alpha1.SecurityScan)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.SecurityScan{}
		if err := c.Get(ctx, key, fresh); err != nil {
			return err
		}
		patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
		mutate(fresh)
		return c.Patch(ctx, fresh, patch)
	})
}

func retrySecurityScanStatusUpdate(ctx context.Context, c client.Client, key client.ObjectKey, mutate func(*triggersv1alpha1.SecurityScan)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.SecurityScan{}
		if err := c.Get(ctx, key, fresh); err != nil {
			return err
		}
		mutate(fresh)
		return c.Status().Update(ctx, fresh)
	})
}

func (r *SecurityScanReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *SecurityScanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&triggersv1alpha1.SecurityScan{}).
		Owns(&platformv1alpha1.AgentRun{}).
		Named("securityscan").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
