package triggers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	defaultCronTimeZone = "UTC"
	cronKind            = "Cron"
)

var cronNonAlphaNum = regexp.MustCompile(`[^a-z0-9-]`)

type CronReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	StateStore store.StateStore
	Now        func() time.Time
}

// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=crons,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=crons/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *CronReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	cr := &triggersv1alpha1.Cron{}
	if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if cr.Spec.Suspend {
		if err := retryCronStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(cr), func(fresh *triggersv1alpha1.Cron) {
			fresh.Status.NextScheduleTime = nil
			fresh.Status.LastError = ""
			setCronCondition(fresh, metav1.ConditionFalse, "Suspended", "Cron trigger is suspended")
		}); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("updating Cron status: %w", err)
		}
		return ctrl.Result{}, nil
	}

	schedule, observedTimeZone, err := parseCronSchedule(cr.Spec.Schedule, cr.Spec.TimeZone)
	observedSchedule := strings.TrimSpace(cr.Spec.Schedule)
	if err != nil {
		if statusErr := retryCronStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(cr), func(fresh *triggersv1alpha1.Cron) {
			fresh.Status.LastError = err.Error()
			setCronCondition(fresh, metav1.ConditionFalse, "InvalidSchedule", err.Error())
		}); statusErr != nil && !apierrors.IsNotFound(statusErr) {
			return ctrl.Result{}, fmt.Errorf("updating Cron status: %w", statusErr)
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	now := r.now()
	scheduledTime := nextCronScheduleTime(cr, schedule, observedSchedule, observedTimeZone, now)
	if scheduledTime.IsZero() {
		err := fmt.Errorf("failed to compute next schedule time")
		if statusErr := retryCronStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(cr), func(fresh *triggersv1alpha1.Cron) {
			fresh.Status.LastError = err.Error()
			setCronCondition(fresh, metav1.ConditionFalse, "ScheduleError", err.Error())
		}); statusErr != nil && !apierrors.IsNotFound(statusErr) {
			return ctrl.Result{}, fmt.Errorf("updating Cron status: %w", statusErr)
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	if scheduledTime.After(now) {
		next := metav1.NewTime(scheduledTime)
		if err := retryCronStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(cr), func(fresh *triggersv1alpha1.Cron) {
			fresh.Status.NextScheduleTime = &next
			fresh.Status.ObservedSchedule = observedSchedule
			fresh.Status.ObservedTimeZone = observedTimeZone
			fresh.Status.LastError = ""
			setCronCondition(fresh, metav1.ConditionTrue, "Scheduled", "Cron schedule is valid")
		}); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("updating Cron status: %w", err)
		}
		return ctrl.Result{RequeueAfter: requeueAfter(scheduledTime.Sub(now))}, nil
	}

	if cr.Spec.ConcurrencyPolicy == "" || cr.Spec.ConcurrencyPolicy == triggersv1alpha1.CronConcurrencyForbid {
		activeRun, err := r.activeCronRun(ctx, cr, scheduledTime)
		if err != nil {
			return ctrl.Result{}, err
		}
		if activeRun != nil {
			nextScheduledTime := schedule.Next(now)
			last := metav1.NewTime(scheduledTime)
			next := metav1.NewTime(nextScheduledTime)
			msg := fmt.Sprintf("skipped tick %s: previous run %s still active", scheduledTime.UTC().Format(time.RFC3339), activeRun.Name)
			log.Info("skipping scheduled AgentRun because previous cron run is still active", "scheduledTime", scheduledTime, "activeRun", activeRun.Name)
			if err := retryCronStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(cr), func(fresh *triggersv1alpha1.Cron) {
				fresh.Status.LastScheduleTime = &last
				fresh.Status.NextScheduleTime = &next
				fresh.Status.ObservedSchedule = observedSchedule
				fresh.Status.ObservedTimeZone = observedTimeZone
				fresh.Status.LastError = msg
				setCronCondition(fresh, metav1.ConditionFalse, "ConcurrencyBlocked", msg)
			}); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("updating Cron status: %w", err)
			}
			return ctrl.Result{RequeueAfter: requeueAfter(nextScheduledTime.Sub(now))}, nil
		}
	}

	created, runName, err := r.createAgentRun(ctx, cr, scheduledTime)
	if err != nil {
		log.Error(err, "failed to create scheduled AgentRun", "scheduledTime", scheduledTime)
		if statusErr := retryCronStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(cr), func(fresh *triggersv1alpha1.Cron) {
			fresh.Status.LastError = err.Error()
			setCronCondition(fresh, metav1.ConditionFalse, "CreateRunFailed", err.Error())
		}); statusErr != nil && !apierrors.IsNotFound(statusErr) {
			return ctrl.Result{}, fmt.Errorf("updating Cron status: %w", statusErr)
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	nextScheduledTime := schedule.Next(now)
	last := metav1.NewTime(scheduledTime)
	next := metav1.NewTime(nextScheduledTime)
	if err := retryCronStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(cr), func(fresh *triggersv1alpha1.Cron) {
		fresh.Status.LastScheduleTime = &last
		fresh.Status.NextScheduleTime = &next
		fresh.Status.LastRunName = runName
		fresh.Status.ObservedSchedule = observedSchedule
		fresh.Status.ObservedTimeZone = observedTimeZone
		fresh.Status.LastError = ""
		if created {
			fresh.Status.RunsCreated++
		}
		setCronCondition(fresh, metav1.ConditionTrue, "Scheduled", "Cron schedule is valid")
	}); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("updating Cron status: %w", err)
	}

	return ctrl.Result{RequeueAfter: requeueAfter(nextScheduledTime.Sub(now))}, nil
}

func (r *CronReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func parseCronSchedule(expr, timeZone string) (cron.Schedule, string, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return nil, "", fmt.Errorf("spec.schedule is required")
	}
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "TZ=") || strings.HasPrefix(upper, "CRON_TZ=") {
		return nil, "", fmt.Errorf("inline cron time zones are not supported; use spec.timeZone")
	}

	zone := strings.TrimSpace(timeZone)
	if zone == "" {
		zone = defaultCronTimeZone
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return nil, "", fmt.Errorf("invalid spec.timeZone %q: %w", zone, err)
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	schedule, err := parser.Parse("CRON_TZ=" + zone + " " + trimmed)
	if err != nil {
		return nil, "", fmt.Errorf("invalid spec.schedule %q: %w", trimmed, err)
	}
	return schedule, zone, nil
}

// nextCronScheduleTime returns the schedule instant to process next. If a due
// instant is later skipped, such as by a Forbid concurrency policy, the
// reconciler records it as processed and advances to a future schedule; skipped
// ticks are not backfilled.
func nextCronScheduleTime(cr *triggersv1alpha1.Cron, schedule cron.Schedule, observedSchedule, observedTimeZone string, now time.Time) time.Time {
	if cr.Status.ObservedSchedule != observedSchedule || cr.Status.ObservedTimeZone != observedTimeZone {
		return schedule.Next(now)
	}
	if cr.Status.NextScheduleTime != nil && !cr.Status.NextScheduleTime.IsZero() {
		return cr.Status.NextScheduleTime.Time
	}
	if cr.Status.LastScheduleTime != nil && !cr.Status.LastScheduleTime.IsZero() {
		return schedule.Next(cr.Status.LastScheduleTime.Time)
	}
	return schedule.Next(now)
}

func requeueAfter(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Second
	}
	return d
}

func (r *CronReconciler) activeCronRun(ctx context.Context, cr *triggersv1alpha1.Cron, scheduledTime time.Time) (*platformv1alpha1.AgentRun, error) {
	runs := &platformv1alpha1.AgentRunList{}
	if err := r.List(ctx, runs, client.InNamespace(cr.Namespace)); err != nil {
		return nil, fmt.Errorf("listing AgentRuns: %w", err)
	}
	scheduledID := scheduledTime.UTC().Format(time.RFC3339)
	for i := range runs.Items {
		run := &runs.Items[i]
		if !TriggerRunMatches(run, cronKind, cr.Name) {
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

func isCronRunTerminal(phase platformv1alpha1.AgentRunPhase) bool {
	switch phase {
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhaseCancelled:
		return true
	default:
		return false
	}
}

func (r *CronReconciler) createAgentRun(ctx context.Context, cr *triggersv1alpha1.Cron, scheduledTime time.Time) (bool, string, error) {
	runName := cronRunName(cr.Name, scheduledTime)
	d := cr.Spec.Defaults
	provider := triggersv1alpha1.NormalizeProvider(d.Provider)
	if err := validateTriggerRunDefaults(TriggerRunSpec{
		Namespace:   cr.Namespace,
		TriggerKind: cronKind,
		TriggerName: cr.Name,
		Defaults:    d,
	}); err != nil {
		return false, runName, err
	}

	scheduledID := scheduledTime.UTC().Format(time.RFC3339)
	annotations := map[string]string{
		runModeAnnotation: "auto",
		"platform.gratefulagents.dev/scheduled-time": scheduledID,
	}
	if strings.TrimSpace(d.CustomInstructions) != "" {
		instructionsName := runName + "-instructions"
		instructions := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: instructionsName, Namespace: cr.Namespace}, Data: map[string]string{"instructions.md": d.CustomInstructions}}
		if err := ctrl.SetControllerReference(cr, instructions, r.Scheme); err != nil {
			return false, runName, fmt.Errorf("setting owner reference on instructions ConfigMap: %w", err)
		}
		if err := r.Create(ctx, instructions); err != nil && !apierrors.IsAlreadyExists(err) {
			return false, runName, fmt.Errorf("creating instructions ConfigMap: %w", err)
		}
		annotations["platform.gratefulagents.dev/instructions-configmap-ref"] = instructionsName
	}
	if triggersv1alpha1.IsOpenAICompatibleProvider(provider) {
		annotations["platform.gratefulagents.dev/openai-api-mode"] = triggersv1alpha1.NormalizeOpenAIAPIForProvider(provider, d.OpenAIAPI)
	}

	runContext := &platformv1alpha1.AgentRunContext{
		ProjectRef: &platformv1alpha1.ProjectRef{Kind: cronKind, Name: cr.Name},
	}
	created, _, err := CreateTriggerRun(ctx, r.Client, r.StateStore, TriggerRunSpec{
		RunName:            runName,
		Namespace:          cr.Namespace,
		TriggerKind:        cronKind,
		TriggerName:        cr.Name,
		ExternalID:         scheduledID,
		ExternalIdentifier: scheduledTime.Format(time.RFC3339),
		SeedMessage:        cr.Spec.Prompt,
		Defaults:           d,
		OwnerRef:           cr,
		Scheme:             r.Scheme,
		Annotations:        annotations,
		Context:            runContext,
		SeedLogPrefix:      "cron",
	})
	if err != nil {
		return false, runName, err
	}
	return created, runName, nil
}

func cronRunName(sourceName string, scheduledTime time.Time) string {
	suffix := scheduledTime.UTC().Format("20060102150405")
	base := cronNonAlphaNum.ReplaceAllString(strings.ToLower(sourceName), "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "run"
	}
	name := "cron-" + base + "-" + suffix
	if len(name) <= 63 {
		return name
	}
	hashBytes := sha1.Sum([]byte(name))
	hash := hex.EncodeToString(hashBytes[:])[:8]
	maxBase := 63 - len("cron-") - len("-") - len(suffix) - len("-") - len(hash)
	if maxBase < 1 {
		maxBase = 1
	}
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	return "cron-" + base + "-" + suffix + "-" + hash
}

func setCronCondition(cr *triggersv1alpha1.Cron, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type:               triggersv1alpha1.ConditionCronReady,
		Status:             status,
		ObservedGeneration: cr.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func (r *CronReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&triggersv1alpha1.Cron{}).
		Owns(&platformv1alpha1.AgentRun{}).
		Named("cron").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
