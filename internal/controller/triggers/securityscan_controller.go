package triggers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
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
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	securityScanKind = "SecurityScan"

	// securityScanModeTemplate is the ModeTemplate applied to scan runs when
	// spec.defaults.modeRef is not set.
	securityScanModeTemplate = "security-scan"

	// Agent-side security tools read these annotations to bind reported
	// findings to the scan.
	securityScanNameAnnotation       = "security.gratefulagents.dev/scan-name"
	securityScanRepositoryAnnotation = "security.gratefulagents.dev/repository"
	securityScanRevisionAnnotation   = "security.gratefulagents.dev/revision"

	// securityScanLabel marks AgentRuns created by a SecurityScan for listing.
	securityScanLabel = "security.gratefulagents.dev/scan"
)

type SecurityScanReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	StateStore store.StateStore
	// Findings, when non-nil, refreshes status.findings from persisted
	// findings after runs are created or observed.
	Findings store.SecurityFindingStore
	Now      func() time.Time
}

// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=securityscans,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=securityscans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *SecurityScanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	scan := &triggersv1alpha1.SecurityScan{}
	if err := r.Get(ctx, req.NamespacedName, scan); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
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

	if strings.TrimSpace(scan.Spec.Schedule) == "" {
		return r.reconcileOneShot(ctx, scan)
	}
	return r.reconcileScheduled(ctx, scan)
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
		if terminal {
			phase = "Completed"
		}
		if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan, scan.Status.LastRunName), func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.Phase = phase
			fresh.Status.LastError = ""
			setSecurityScanCondition(fresh, metav1.ConditionTrue, "ScanUpToDate", "Scan already ran for the current spec generation")
		}); err != nil {
			return ctrl.Result{}, err
		}
		if terminal {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	runName := securityScanRunName(scan.Name, fmt.Sprintf("g%d", scan.Generation))
	externalID := fmt.Sprintf("generation-%d", scan.Generation)
	created, err := r.createScanRun(ctx, scan, runName, externalID, externalID)
	if err != nil {
		log.Error(err, "failed to create scan AgentRun", "run", runName)
		if statusErr := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastError = err.Error()
			setSecurityScanCondition(fresh, metav1.ConditionFalse, "CreateRunFailed", err.Error())
		}); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	now := metav1.NewTime(r.now())
	generation := scan.Generation
	if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan, runName), func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.Phase = "Running"
		fresh.Status.ObservedGeneration = generation
		fresh.Status.LastRunName = runName
		fresh.Status.LastScanTime = &now
		fresh.Status.LastError = ""
		if created {
			fresh.Status.RunsCreated++
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

	if scheduledTime.After(now) {
		next := metav1.NewTime(scheduledTime)
		if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan, scan.Status.LastRunName), func(fresh *triggersv1alpha1.SecurityScan) {
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
		return ctrl.Result{RequeueAfter: requeueAfter(scheduledTime.Sub(now))}, nil
	}

	if scan.Spec.ConcurrencyPolicy == "" || scan.Spec.ConcurrencyPolicy == triggersv1alpha1.SecurityScanConcurrencyForbid {
		activeRun, err := r.activeScanRun(ctx, scan, scheduledTime)
		if err != nil {
			return ctrl.Result{}, err
		}
		if activeRun != nil {
			nextScheduledTime := schedule.Next(now)
			next := metav1.NewTime(nextScheduledTime)
			msg := fmt.Sprintf("skipped tick %s: previous run %s still active", scheduledTime.UTC().Format(time.RFC3339), activeRun.Name)
			log.Info("skipping scheduled scan AgentRun because previous run is still active", "scheduledTime", scheduledTime, "activeRun", activeRun.Name)
			if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan, scan.Status.LastRunName), func(fresh *triggersv1alpha1.SecurityScan) {
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
	created, err := r.createScanRun(ctx, scan, runName, scheduledID, scheduledTime.Format(time.RFC3339))
	if err != nil {
		log.Error(err, "failed to create scheduled scan AgentRun", "scheduledTime", scheduledTime)
		if statusErr := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastError = err.Error()
			setSecurityScanCondition(fresh, metav1.ConditionFalse, "CreateRunFailed", err.Error())
		}); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	nextScheduledTime := schedule.Next(now)
	last := metav1.NewTime(scheduledTime)
	next := metav1.NewTime(nextScheduledTime)
	if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan, runName), func(fresh *triggersv1alpha1.SecurityScan) {
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
		}
		setSecurityScanCondition(fresh, metav1.ConditionTrue, "Scheduled", "SecurityScan schedule is valid")
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter(nextScheduledTime.Sub(now))}, nil
}

func (r *SecurityScanReconciler) createScanRun(ctx context.Context, scan *triggersv1alpha1.SecurityScan, runName, externalID, externalIdentifier string) (bool, error) {
	d := scan.Spec.Defaults
	d.RepoURL = scan.Spec.RepoURL
	d.BaseBranch = scan.Spec.EffectiveBaseBranch()
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

	provider := triggersv1alpha1.NormalizeProvider(d.Provider)
	annotations := map[string]string{
		runModeAnnotation:                "auto",
		securityScanNameAnnotation:       scan.Name,
		securityScanRepositoryAnnotation: scan.Spec.RepoURL,
	}
	if rev := strings.TrimSpace(scan.Spec.Revision); rev != "" {
		annotations[securityScanRevisionAnnotation] = rev
	}
	if strings.TrimSpace(d.CustomInstructions) != "" {
		instructionsName := runName + "-instructions"
		instructions := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: instructionsName, Namespace: scan.Namespace}, Data: map[string]string{"instructions.md": d.CustomInstructions}}
		if err := ctrl.SetControllerReference(scan, instructions, r.Scheme); err != nil {
			return false, fmt.Errorf("setting owner reference on instructions ConfigMap: %w", err)
		}
		if err := r.Create(ctx, instructions); err != nil && !apierrors.IsAlreadyExists(err) {
			return false, fmt.Errorf("creating instructions ConfigMap: %w", err)
		}
		annotations["platform.gratefulagents.dev/instructions-configmap-ref"] = instructionsName
	}
	if triggersv1alpha1.IsOpenAICompatibleProvider(provider) {
		annotations["platform.gratefulagents.dev/openai-api-mode"] = triggersv1alpha1.NormalizeOpenAIAPIForProvider(provider, d.OpenAIAPI)
	}

	created, _, err := CreateTriggerRun(ctx, r.Client, r.StateStore, TriggerRunSpec{
		RunName:            runName,
		Namespace:          scan.Namespace,
		TriggerKind:        securityScanKind,
		TriggerName:        scan.Name,
		ExternalID:         externalID,
		ExternalIdentifier: externalIdentifier,
		SeedMessage:        BuildSecurityScanPrompt(scan.Spec),
		Defaults:           d,
		OwnerRef:           scan,
		Scheme:             r.Scheme,
		Labels:             map[string]string{securityScanLabel: scan.Name},
		Annotations:        annotations,
		Context: &platformv1alpha1.AgentRunContext{
			ProjectRef: &platformv1alpha1.ProjectRef{Kind: securityScanKind, Name: scan.Name},
		},
		ModeRef:       modeRef,
		SeedLogPrefix: "securityscan",
	})
	return created, err
}

func (r *SecurityScanReconciler) activeScanRun(ctx context.Context, scan *triggersv1alpha1.SecurityScan, scheduledTime time.Time) (*platformv1alpha1.AgentRun, error) {
	runs := &platformv1alpha1.AgentRunList{}
	if err := r.List(ctx, runs, client.InNamespace(scan.Namespace), client.MatchingLabels{securityScanLabel: scan.Name}); err != nil {
		return nil, fmt.Errorf("listing AgentRuns: %w", err)
	}
	scheduledID := scheduledTime.UTC().Format(time.RFC3339)
	for i := range runs.Items {
		run := &runs.Items[i]
		if !TriggerRunMatches(run, securityScanKind, scan.Name) {
			continue
		}
		if run.Spec.Trigger.ExternalRef != nil && strings.TrimSpace(run.Spec.Trigger.ExternalRef.ID) == scheduledID {
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

// summarizeFindings refreshes finding counts from the finding store. It is
// best-effort: a nil store or a summarize error leaves status.findings as is.
func (r *SecurityScanReconciler) summarizeFindings(ctx context.Context, scan *triggersv1alpha1.SecurityScan, runName string) *triggersv1alpha1.SecurityScanFindingCounts {
	if r.Findings == nil || runName == "" {
		return nil
	}
	counts, err := r.Findings.SummarizeSecurityFindings(ctx, scan.Namespace, scan.Name, runName)
	if err != nil {
		logf.FromContext(ctx).Error(err, "failed to summarize security findings", "run", runName)
		return nil
	}
	return &triggersv1alpha1.SecurityScanFindingCounts{
		Total:    counts["total"],
		Open:     counts["open"],
		Critical: counts["critical"],
		High:     counts["high"],
		Medium:   counts["medium"],
		Low:      counts["low"],
		Info:     counts["info"],
	}
}

// updateStatus applies mutate to a fresh copy of the scan, then folds in the
// finding counts and the failOnSeverity threshold, which overrides the Ready
// condition set by mutate.
func (r *SecurityScanReconciler) updateStatus(ctx context.Context, scan *triggersv1alpha1.SecurityScan, findings *triggersv1alpha1.SecurityScanFindingCounts, mutate func(*triggersv1alpha1.SecurityScan)) error {
	failOn := scan.Spec.FailOnSeverity
	err := retrySecurityScanStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(scan), func(fresh *triggersv1alpha1.SecurityScan) {
		mutate(fresh)
		if findings != nil {
			fresh.Status.Findings = findings
		}
		if failOn == "" || fresh.Status.Findings == nil {
			return
		}
		if n := findingsAtOrAbove(fresh.Status.Findings, failOn); n > 0 {
			msg := fmt.Sprintf("%d findings at or above severity %q", n, failOn)
			setSecurityScanCondition(fresh, metav1.ConditionFalse, "FindingsExceedThreshold", msg)
		}
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("updating SecurityScan status: %w", err)
	}
	return nil
}

// findingsAtOrAbove counts findings with severity at or above min.
func findingsAtOrAbove(counts *triggersv1alpha1.SecurityScanFindingCounts, min string) int32 {
	bySeverity := []struct {
		severity string
		count    int32
	}{
		{"critical", counts.Critical},
		{"high", counts.High},
		{"medium", counts.Medium},
		{"low", counts.Low},
		{"info", counts.Info},
	}
	var total int32
	for _, entry := range bySeverity {
		total += entry.count
		if entry.severity == min {
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
	maxBase := 63 - len("secscan-") - len("-") - len(suffix) - len("-") - len(hash)
	if maxBase < 1 {
		maxBase = 1
	}
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
