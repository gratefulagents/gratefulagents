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
	"k8s.io/client-go/tools/record"
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
	Recorder record.EventRecorder
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
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=securityworkflows;securityrankers;securitypostscripts,verbs=get;list;watch
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
	created, resolvedRefs, err := r.createScanRun(ctx, scan, runName, externalID, externalID, nil)
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
	created, resolvedRefs, err := r.createScanRun(ctx, scan, runName, externalID, externalID, nil)
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
	created, resolvedRefs, err := r.createScanRun(ctx, scan, runName, scheduledID, scheduledTime.Format(time.RFC3339), nil)
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

func (r *SecurityScanReconciler) createScanRun(ctx context.Context, scan *triggersv1alpha1.SecurityScan, runName, externalID, externalIdentifier string, runCtx *securityScanRunContext) (bool, []triggersv1alpha1.SecurityScanResolvedRef, error) {
	// Resolve library references at run-creation time and build the prompt
	// from the resolved snapshot: the seed message is persisted when the run
	// is created, so later edits to the referenced resources never change
	// this run.
	resolved, err := resolveSecurityScanRefs(ctx, r.Client, scan)
	if err != nil {
		return false, nil, err
	}

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
		return false, nil, err
	}

	provider := d.EffectiveProvider()
	dedupePermille := int32(0)
	if scan.Spec.DedupeEnabled() {
		dedupePermille = scan.Spec.DedupeSimilarityThresholdPermille()
	}
	annotations := map[string]string{
		runModeAnnotation: "auto",
		triggersv1alpha1.SecurityScanNameAnnotation:           scan.Name,
		triggersv1alpha1.SecurityScanRepositoryAnnotation:     scan.Spec.RepoURL,
		triggersv1alpha1.SecurityScanMinSeverityAnnotation:    scan.Spec.EffectiveMinSeverity(),
		triggersv1alpha1.SecurityScanDedupePermilleAnnotation: strconv.Itoa(int(dedupePermille)),
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
			return false, nil, fmt.Errorf("setting owner reference on instructions ConfigMap: %w", err)
		}
		if err := r.Create(ctx, instructions); err != nil && !apierrors.IsAlreadyExists(err) {
			return false, nil, fmt.Errorf("creating instructions ConfigMap: %w", err)
		}
		annotations["platform.gratefulagents.dev/instructions-configmap-ref"] = instructionsName
	}
	if triggersv1alpha1.IsOpenAICompatibleProvider(provider) {
		annotations["platform.gratefulagents.dev/openai-api-mode"] = triggersv1alpha1.NormalizeOpenAIAPIForProvider(provider, d.OpenAIAPI)
	}
	if refsJSON := securityScanResolvedRefsJSON(resolved.refs); refsJSON != "" {
		annotations[triggersv1alpha1.SecurityScanResolvedRefsAnnotation] = refsJSON
	}

	created, _, err := CreateTriggerRun(ctx, r.Client, r.StateStore, TriggerRunSpec{
		RunName:            runName,
		Namespace:          scan.Namespace,
		TriggerKind:        securityScanKind,
		TriggerName:        scan.Name,
		ExternalID:         externalID,
		ExternalIdentifier: externalIdentifier,
		SeedMessage:        BuildSecurityScanPromptWithEvent(resolved.spec, securityScanPromptEvent(runCtx)),
		Revision:           revision,
		Defaults:           d,
		OwnerRef:           scan,
		Scheme:             r.Scheme,
		Labels:             map[string]string{securityScanLabel: securityScanLabelValue(scan.Name)},
		Annotations:        annotations,
		Context: &platformv1alpha1.AgentRunContext{
			ProjectRef: &platformv1alpha1.ProjectRef{Kind: securityScanKind, Name: scan.Name},
		},
		ModeRef:       modeRef,
		SeedLogPrefix: "securityscan",
	})
	return created, resolved.refs, err
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
	counts, err := r.Findings.SummarizeSecurityFindings(ctx, scan.Namespace, scan.Name, "")
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
	failOn := scan.Spec.FailOnSeverity
	err := retrySecurityScanStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(scan), func(fresh *triggersv1alpha1.SecurityScan) {
		mutate(fresh)
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
