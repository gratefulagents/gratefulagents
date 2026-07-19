package triggers

import (
	"context"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCronReconcileSchedulesFirstRunWithoutCreatingImmediately(t *testing.T) {
	scheme := cronTestScheme(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cr := cronTestTrigger()

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.Cron{}).
		WithObjects(cr).
		Build()

	reconciler := &CronReconciler{
		Client: k8sClient,
		Scheme: scheme,
		Now:    func() time.Time { return now },
	}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != time.Minute {
		t.Fatalf("RequeueAfter = %v, want 1m", result.RequeueAfter)
	}

	updated := &triggersv1alpha1.Cron{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cr), updated); err != nil {
		t.Fatalf("Get(Cron) error = %v", err)
	}
	wantNext := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	if updated.Status.NextScheduleTime == nil || !updated.Status.NextScheduleTime.Time.Equal(wantNext) {
		t.Fatalf("NextScheduleTime = %v, want %v", updated.Status.NextScheduleTime, wantNext)
	}
	if updated.Status.LastScheduleTime != nil {
		t.Fatalf("LastScheduleTime = %v, want nil", updated.Status.LastScheduleTime)
	}

	runs := &platformv1alpha1.AgentRunList{}
	if err := k8sClient.List(context.Background(), runs, client.InNamespace("default")); err != nil {
		t.Fatalf("List(AgentRun) error = %v", err)
	}
	if len(runs.Items) != 0 {
		t.Fatalf("AgentRuns len = %d, want 0", len(runs.Items))
	}
}

func TestCronReconcileCreatesDueRun(t *testing.T) {
	scheme := cronTestScheme(t)
	now := time.Date(2026, 1, 1, 0, 1, 30, 0, time.UTC)
	next := metav1.NewTime(time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC))
	cr := cronTestTrigger()
	cr.Status.ObservedSchedule = "* * * * *"
	cr.Status.ObservedTimeZone = "UTC"
	cr.Status.NextScheduleTime = &next

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.Cron{}).
		WithObjects(cr).
		Build()

	reconciler := &CronReconciler{
		Client: k8sClient,
		Scheme: scheme,
		Now:    func() time.Time { return now },
	}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("RequeueAfter = %v, want 30s", result.RequeueAfter)
	}

	updated := &triggersv1alpha1.Cron{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cr), updated); err != nil {
		t.Fatalf("Get(Cron) error = %v", err)
	}
	if updated.Status.RunsCreated != 1 {
		t.Fatalf("RunsCreated = %d, want 1", updated.Status.RunsCreated)
	}
	if updated.Status.LastScheduleTime == nil || !updated.Status.LastScheduleTime.Time.Equal(next.Time) {
		t.Fatalf("LastScheduleTime = %v, want %v", updated.Status.LastScheduleTime, next.Time)
	}
	wantFuture := time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC)
	if updated.Status.NextScheduleTime == nil || !updated.Status.NextScheduleTime.Time.Equal(wantFuture) {
		t.Fatalf("NextScheduleTime = %v, want %v", updated.Status.NextScheduleTime, wantFuture)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: cronRunName(cr.Name, next.Time)}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if run.Spec.Trigger.Kind != "Cron" || run.Spec.Trigger.Name != cr.Name {
		t.Fatalf("Trigger = %#v, want Cron/%s", run.Spec.Trigger, cr.Name)
	}
	if run.Spec.Trigger.ExternalRef == nil || run.Spec.Trigger.ExternalRef.ID != "2026-01-01T00:01:00Z" {
		t.Fatalf("ExternalRef = %#v, want scheduled timestamp", run.Spec.Trigger.ExternalRef)
	}
	if run.Spec.Context == nil || run.Spec.Context.ProjectRef == nil || run.Spec.Context.ProjectRef.Kind != "Cron" {
		t.Fatalf("Context = %#v, want Cron project ref", run.Spec.Context)
	}
	if run.Spec.WorkflowMode != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("WorkflowMode = %q, want auto (autonomous default)", run.Spec.WorkflowMode)
	}
}

func TestCronReconcileDefaultForbidSkipsDueRunWhenPreviousRunActive(t *testing.T) {
	scheme := cronTestScheme(t)
	now := time.Date(2026, 1, 1, 0, 1, 30, 0, time.UTC)
	next := metav1.NewTime(time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC))
	cr := cronTestTrigger()
	cr.Status.ObservedSchedule = "* * * * *"
	cr.Status.ObservedTimeZone = "UTC"
	cr.Status.NextScheduleTime = &next
	activeRun := cronPriorRun(cr, platformv1alpha1.AgentRunPhaseRunning)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.Cron{}).
		WithObjects(cr, activeRun).
		Build()

	reconciler := &CronReconciler{
		Client: k8sClient,
		Scheme: scheme,
		Now:    func() time.Time { return now },
	}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("RequeueAfter = %v, want 30s", result.RequeueAfter)
	}

	runs := &platformv1alpha1.AgentRunList{}
	if err := k8sClient.List(context.Background(), runs, client.InNamespace("default")); err != nil {
		t.Fatalf("List(AgentRun) error = %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("AgentRuns len = %d, want only prior run", len(runs.Items))
	}

	updated := &triggersv1alpha1.Cron{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cr), updated); err != nil {
		t.Fatalf("Get(Cron) error = %v", err)
	}
	if updated.Status.LastError != "skipped tick 2026-01-01T00:01:00Z: previous run cron-nightly-maintenance-20260101000000 still active" {
		t.Fatalf("LastError = %q, want skipped tick message", updated.Status.LastError)
	}
	if updated.Status.RunsCreated != 0 {
		t.Fatalf("RunsCreated = %d, want 0", updated.Status.RunsCreated)
	}
}

func TestCronReconcileForbidAllowsDueRunWhenPreviousRunSucceeded(t *testing.T) {
	scheme := cronTestScheme(t)
	now := time.Date(2026, 1, 1, 0, 1, 30, 0, time.UTC)
	next := metav1.NewTime(time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC))
	cr := cronTestTrigger()
	cr.Spec.ConcurrencyPolicy = triggersv1alpha1.CronConcurrencyForbid
	cr.Status.ObservedSchedule = "* * * * *"
	cr.Status.ObservedTimeZone = "UTC"
	cr.Status.NextScheduleTime = &next
	succeededRun := cronPriorRun(cr, platformv1alpha1.AgentRunPhaseSucceeded)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.Cron{}).
		WithObjects(cr, succeededRun).
		Build()

	reconciler := &CronReconciler{
		Client: k8sClient,
		Scheme: scheme,
		Now:    func() time.Time { return now },
	}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	runs := &platformv1alpha1.AgentRunList{}
	if err := k8sClient.List(context.Background(), runs, client.InNamespace("default")); err != nil {
		t.Fatalf("List(AgentRun) error = %v", err)
	}
	if len(runs.Items) != 2 {
		t.Fatalf("AgentRuns len = %d, want prior plus new run", len(runs.Items))
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: cronRunName(cr.Name, next.Time)}, &platformv1alpha1.AgentRun{}); err != nil {
		t.Fatalf("Get(new AgentRun) error = %v", err)
	}
}

func TestCronReconcileAllowCreatesDueRunWhenPreviousRunActive(t *testing.T) {
	scheme := cronTestScheme(t)
	now := time.Date(2026, 1, 1, 0, 1, 30, 0, time.UTC)
	next := metav1.NewTime(time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC))
	cr := cronTestTrigger()
	cr.Spec.ConcurrencyPolicy = triggersv1alpha1.CronConcurrencyAllow
	cr.Status.ObservedSchedule = "* * * * *"
	cr.Status.ObservedTimeZone = "UTC"
	cr.Status.NextScheduleTime = &next
	activeRun := cronPriorRun(cr, platformv1alpha1.AgentRunPhaseRunning)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.Cron{}).
		WithObjects(cr, activeRun).
		Build()

	reconciler := &CronReconciler{
		Client: k8sClient,
		Scheme: scheme,
		Now:    func() time.Time { return now },
	}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	runs := &platformv1alpha1.AgentRunList{}
	if err := k8sClient.List(context.Background(), runs, client.InNamespace("default")); err != nil {
		t.Fatalf("List(AgentRun) error = %v", err)
	}
	if len(runs.Items) != 2 {
		t.Fatalf("AgentRuns len = %d, want prior plus new run", len(runs.Items))
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: cronRunName(cr.Name, next.Time)}, &platformv1alpha1.AgentRun{}); err != nil {
		t.Fatalf("Get(new AgentRun) error = %v", err)
	}
}

func TestParseCronScheduleRejectsInlineTimeZone(t *testing.T) {
	if _, _, err := parseCronSchedule("CRON_TZ=UTC * * * * *", "UTC"); err == nil {
		t.Fatal("parseCronSchedule() error = nil, want inline timezone error")
	}
}

func TestParseCronScheduleSupportsDescriptors(t *testing.T) {
	schedule, _, err := parseCronSchedule("@hourly", "UTC")
	if err != nil {
		t.Fatalf("parseCronSchedule() error = %v", err)
	}
	now := time.Date(2026, 1, 1, 0, 15, 0, 0, time.UTC)
	want := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	if got := schedule.Next(now); !got.Equal(want) {
		t.Fatalf("Next() = %v, want %v", got, want)
	}
}

func cronTestScheme(t *testing.T) *runtime.Scheme {
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
	return scheme
}

func cronPriorRun(cr *triggersv1alpha1.Cron, phase platformv1alpha1.AgentRunPhase) *platformv1alpha1.AgentRun {
	scheduled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: cronRunName(cr.Name, scheduled), Namespace: cr.Namespace},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{
				Kind: cronKind,
				Name: cr.Name,
				ExternalRef: &platformv1alpha1.ExternalRef{
					ID: scheduled.Format(time.RFC3339),
				},
			},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: phase},
	}
}

func cronTestTrigger() *triggersv1alpha1.Cron {
	return &triggersv1alpha1.Cron{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-maintenance", Namespace: "default"},
		Spec: triggersv1alpha1.CronSpec{
			Schedule: "* * * * *",
			TimeZone: "UTC",
			Prompt:   "Run the scheduled maintenance task.",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:    "https://github.com/example/repo.git",
				BaseBranch: "main",
				Model:      "gpt-5.4",
				Provider:   "openai",
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "openai-key",
					GithubToken:  "github-token",
				},
			},
		},
	}
}
