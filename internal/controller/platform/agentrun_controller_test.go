package platform

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/projectstate"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	agentsandboxextensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	standingOverseerOwnerKind = "AgentRun"
	explicitSkillName         = "explicit-skill"
)

func TestReconcileHoldsPendingAuthorizationRunBeforeInitialization(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name: "held", Namespace: "default", Finalizers: []string{agentRunCleanupFinalizer},
		Annotations: map[string]string{platformv1alpha1.AuthorizationPendingAnnotation: "true"},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	result, err := (&AgentRunReconciler{Client: c}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
	if err != nil || result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("Reconcile() = (%#v, %v)", result, err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != "" {
		t.Fatalf("held run phase = %q, want uninitialized", updated.Status.Phase)
	}
}

func TestProjectStateIDForRunUsesSharedHashedIdentity(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "Team-A"},
		Spec: platformv1alpha1.AgentRunSpec{Repository: platformv1alpha1.RepositoryContext{
			URL: "git@github.com:Acme/Widgets.git",
		}},
	}
	want := projectstate.ProjectID(run.Namespace, run.Spec.Repository.URL)
	if got := projectStateIDForRun(run); got != want {
		t.Fatalf("projectStateIDForRun() = %q, want %q", got, want)
	}
	if got := projectStateIDForRun(nil); got != "" {
		t.Fatalf("projectStateIDForRun(nil) = %q, want empty", got)
	}
}

func addAgentSandboxSchemes(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()
	if err := agentsandboxv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add agent-sandbox scheme: %v", err)
	}
	if err := agentsandboxextensionsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add agent-sandbox extensions scheme: %v", err)
	}
	// Drains release the run's ClusterRoleBindings.
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbac scheme: %v", err)
	}
}

func hasFinalizer(obj client.Object, finalizer string) bool {
	for _, existing := range obj.GetFinalizers() {
		if existing == finalizer {
			return true
		}
	}
	return false
}

type agentRunDataCleanupCall struct {
	name      string
	namespace string
	projectID string
}

type recordingAgentRunDataStore struct {
	store.StateStore
	calls []agentRunDataCleanupCall
	err   error
}

func (s *recordingAgentRunDataStore) DeleteAgentRunData(_ context.Context, name, namespace, projectID string) error {
	s.calls = append(s.calls, agentRunDataCleanupCall{name: name, namespace: namespace, projectID: projectID})
	return s.err
}

type conflictOnceStatusClient struct {
	client.Client
	statusConflictCount int
}

func (c *conflictOnceStatusClient) Status() client.SubResourceWriter {
	return &conflictOnceStatusWriter{
		SubResourceWriter: c.Client.Status(),
		parent:            c,
	}
}

type conflictOnceStatusWriter struct {
	client.SubResourceWriter
	parent *conflictOnceStatusClient
}

func (w *conflictOnceStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if w.parent.statusConflictCount == 0 {
		w.parent.statusConflictCount++
		return apierrors.NewConflict(
			schema.GroupResource{Group: platformv1alpha1.GroupVersion.Group, Resource: "agentruns"},
			obj.GetName(),
			context.DeadlineExceeded,
		)
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

type concurrentGitHubStatusClient struct {
	client.Client
	concurrentIssueID string
	updateCalls       int
}

func (c *concurrentGitHubStatusClient) Status() client.SubResourceWriter {
	return &concurrentGitHubStatusWriter{
		SubResourceWriter: c.Client.Status(),
		parent:            c,
	}
}

type concurrentGitHubStatusWriter struct {
	client.SubResourceWriter
	parent *concurrentGitHubStatusClient
}

func (w *concurrentGitHubStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	gh, ok := obj.(*triggersv1alpha1.GitHubRepository)
	if !ok {
		return w.SubResourceWriter.Update(ctx, obj, opts...)
	}
	w.parent.updateCalls++
	if w.parent.updateCalls == 1 {
		concurrent := &triggersv1alpha1.GitHubRepository{}
		if err := w.parent.Client.Get(ctx, client.ObjectKeyFromObject(gh), concurrent); err != nil {
			return err
		}
		concurrent.Status.ProcessedIssueIDs = append(concurrent.Status.ProcessedIssueIDs, w.parent.concurrentIssueID)
		if err := w.parent.Client.Status().Update(ctx, concurrent); err != nil {
			return err
		}
	}
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

func TestIsRunAwaitingPod(t *testing.T) {
	now := metav1.Now()
	for _, run := range []*platformv1alpha1.AgentRun{
		{Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhasePending, StartedAt: &now}},
		{Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseAdmitted, StartedAt: &now}},
		{Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning, StartedAt: &now, Queue: &platformv1alpha1.AgentRunQueueStatus{State: "Queued"}}},
	} {
		if !isRunAwaitingPod(run) {
			t.Fatalf("isRunAwaitingPod(%#v) = false, want true", run.Status)
		}
	}
	if isRunAwaitingPod(&platformv1alpha1.AgentRun{Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning}}) {
		t.Fatal("running run without a startup queue state should treat a missing pod as a failure")
	}
	stale := metav1.NewTime(time.Now().Add(-podVisibilityGrace - time.Second))
	if isRunAwaitingPod(&platformv1alpha1.AgentRun{Status: platformv1alpha1.AgentRunStatus{
		Phase: platformv1alpha1.AgentRunPhaseAdmitted, StartedAt: &stale,
	}}) {
		t.Fatal("startup grace must be bounded")
	}
}

func TestMonitorPodRunningPreservesInteractivePhases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		initialPhase      platformv1alpha1.AgentRunPhase
		initialQueueState string
		initialBlocked    string
		wantPhase         platformv1alpha1.AgentRunPhase
		wantQueueState    string
		wantBlocked       string
	}{
		{
			name:              "blocked waiting for user stays blocked",
			initialPhase:      platformv1alpha1.AgentRunPhaseBlocked,
			initialQueueState: "Blocked",
			initialBlocked:    "waiting-for-user",
			wantPhase:         platformv1alpha1.AgentRunPhaseBlocked,
			wantQueueState:    "Blocked",
			wantBlocked:       "waiting-for-user",
		},
		{
			name:              "waiting approval stays waiting approval",
			initialPhase:      platformv1alpha1.AgentRunPhaseWaitingApproval,
			initialQueueState: "Blocked",
			initialBlocked:    "waiting-for-approval",
			wantPhase:         platformv1alpha1.AgentRunPhaseWaitingApproval,
			wantQueueState:    "Blocked",
			wantBlocked:       "waiting-for-approval",
		},
		{
			name:              "admitted transitions to running",
			initialPhase:      platformv1alpha1.AgentRunPhaseAdmitted,
			initialQueueState: "Queued",
			initialBlocked:    "",
			wantPhase:         platformv1alpha1.AgentRunPhaseRunning,
			wantQueueState:    "Running",
			wantBlocked:       "",
		},
		{
			name:              "running worker repairs stale startup queue",
			initialPhase:      platformv1alpha1.AgentRunPhaseRunning,
			initialQueueState: "Queued",
			wantPhase:         platformv1alpha1.AgentRunPhaseRunning,
			wantQueueState:    "Running",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatalf("add corev1 scheme: %v", err)
			}
			if err := platformv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatalf("add platform scheme: %v", err)
			}
			addAgentSandboxSchemes(t, scheme)

			runName := "run-" + sanitizeName(tc.name)
			podName := "pod-" + sanitizeName(tc.name)
			run := &platformv1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      runName,
					Namespace: "default",
				},
				Spec: platformv1alpha1.AgentRunSpec{
					WorkflowMode: platformv1alpha1.WorkflowModeChat,
				},
				Status: platformv1alpha1.AgentRunStatus{
					Phase: tc.initialPhase,
					Queue: &platformv1alpha1.AgentRunQueueStatus{
						State:         tc.initialQueueState,
						BlockedReason: tc.initialBlocked,
					},
					Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
						SandboxRef: &platformv1alpha1.NamedRef{Name: podName},
					},
				},
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: "default",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			}

			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&platformv1alpha1.AgentRun{}).
				WithObjects(run, pod).
				Build()

			reconciler := &AgentRunReconciler{Client: k8sClient}
			requeue := 7 * time.Second
			result, err := reconciler.monitorPodName(context.Background(), run, podName, requeue)
			if err != nil {
				t.Fatalf("monitorPodName() error = %v", err)
			}
			if result.RequeueAfter != requeue {
				t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, requeue)
			}

			updated := &platformv1alpha1.AgentRun{}
			if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: runName, Namespace: "default"}, updated); err != nil {
				t.Fatalf("get updated run: %v", err)
			}

			if updated.Status.Phase != tc.wantPhase {
				t.Fatalf("Phase = %q, want %q", updated.Status.Phase, tc.wantPhase)
			}
			if updated.Status.Queue == nil {
				t.Fatalf("Queue = nil")
			}
			if updated.Status.Queue.State != tc.wantQueueState {
				t.Fatalf("Queue.State = %q, want %q", updated.Status.Queue.State, tc.wantQueueState)
			}
			if updated.Status.Queue.BlockedReason != tc.wantBlocked {
				t.Fatalf("Queue.BlockedReason = %q, want %q", updated.Status.Queue.BlockedReason, tc.wantBlocked)
			}
		})
	}
}

func TestMonitorPodRunningRetriesStatusConflict(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-retry",
			Namespace: "default",
		},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseAdmitted,
			Queue: &platformv1alpha1.AgentRunQueueStatus{State: "Queued"},
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "pod-retry"},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-retry",
			Namespace: "default",
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, pod).
		Build()

	reconciler := &AgentRunReconciler{
		Client: &conflictOnceStatusClient{Client: baseClient},
	}

	result, err := reconciler.monitorPodName(context.Background(), run, pod.Name, 5*time.Second)
	if err != nil {
		t.Fatalf("monitorPodName() error = %v", err)
	}
	if result.RequeueAfter != 5*time.Second {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, 5*time.Second)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := baseClient.Get(context.Background(), client.ObjectKey{Name: "run-retry", Namespace: "default"}, updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseRunning {
		t.Fatalf("Phase = %q, want Running", updated.Status.Phase)
	}
	if updated.Status.Queue == nil || updated.Status.Queue.State != "Running" {
		t.Fatalf("Queue = %#v, want running queue state", updated.Status.Queue)
	}
}

func TestMonitorPodTimeoutPausesAndReconcileReleasesSandbox(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	started := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-timeout-pauses",
			Namespace: "default",
			UID:       types.UID("run-timeout-pauses-uid"),
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Limits: &platformv1alpha1.AgentRunLimits{
				MaxRuntime: metav1.Duration{Duration: time.Hour},
			},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhaseRunning,
			Queue:     &platformv1alpha1.AgentRunQueueStatus{State: "Running"},
			StartedAt: &started,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "pod-timeout-pauses"},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-timeout-pauses", Namespace: "default", Labels: map[string]string{
			"platform.gratefulagents.dev/owner-run-uid": string(run.UID),
		}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, pod).
		Build()

	reconciler := &AgentRunReconciler{Client: k8sClient}
	if _, err := reconciler.monitorAgentSandbox(context.Background(), run, 5*time.Second); err != nil {
		t.Fatalf("monitorAgentSandbox() error = %v", err)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhasePaused {
		t.Fatalf("Phase = %q, want Paused", updated.Status.Phase)
	}
	if updated.Status.LastError != "" {
		t.Fatalf("LastError = %q, want empty", updated.Status.LastError)
	}

	// The first paused reconcile only requests runner pod deletion; dependent
	// sandbox cleanup waits until a later reconcile observes the pod absent.
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile paused run error = %v", err)
	}
	if result.RequeueAfter != drainRequeueAfter {
		t.Fatalf("RequeueAfter = %s, want fixed drain interval while runner pod drains", result.RequeueAfter)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get draining run: %v", err)
	}
	if updated.Status.Sandbox == nil {
		t.Fatal("Sandbox = nil before runner pod drain completes")
	}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}}); err != nil {
		t.Fatalf("Reconcile drained paused run error = %v", err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pod get err = %v, want NotFound", err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhasePaused {
		t.Fatalf("Phase after release = %q, want Paused", updated.Status.Phase)
	}
	if updated.Status.Sandbox != nil {
		t.Fatalf("Sandbox = %#v, want nil after release", updated.Status.Sandbox)
	}
}

func TestRunPastTimeoutRestartsWindowOnWake(t *testing.T) {
	t.Parallel()

	limits := &platformv1alpha1.AgentRunLimits{MaxRuntime: metav1.Duration{Duration: time.Hour}}
	staleStart := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	recentWake := metav1.NewTime(time.Now().Add(-time.Minute))
	staleWake := metav1.NewTime(time.Now().Add(-90 * time.Minute))

	cases := []struct {
		name string
		run  *platformv1alpha1.AgentRun
		want bool
	}{
		{"nil run", nil, false},
		{"no startedAt", &platformv1alpha1.AgentRun{Spec: platformv1alpha1.AgentRunSpec{Limits: limits}}, false},
		{"stale start without wake", &platformv1alpha1.AgentRun{
			Spec:   platformv1alpha1.AgentRunSpec{Limits: limits},
			Status: platformv1alpha1.AgentRunStatus{StartedAt: &staleStart},
		}, true},
		{"recent wake restarts window", &platformv1alpha1.AgentRun{
			Spec:   platformv1alpha1.AgentRunSpec{Limits: limits},
			Status: platformv1alpha1.AgentRunStatus{StartedAt: &staleStart, LastWakeTime: &recentWake},
		}, false},
		{"stale wake still times out", &platformv1alpha1.AgentRun{
			Spec:   platformv1alpha1.AgentRunSpec{Limits: limits},
			Status: platformv1alpha1.AgentRunStatus{StartedAt: &staleStart, LastWakeTime: &staleWake},
		}, true},
		{"standing maintainer never times out", &platformv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleMaintainer}},
			Spec:       platformv1alpha1.AgentRunSpec{Limits: limits},
			Status:     platformv1alpha1.AgentRunStatus{StartedAt: &staleStart, LastWakeTime: &staleWake},
		}, false},
		{"standing overseer never times out", &platformv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleOverseer}},
			Spec:       platformv1alpha1.AgentRunSpec{Limits: limits},
			Status:     platformv1alpha1.AgentRunStatus{StartedAt: &staleStart},
		}, false},
	}
	for _, tc := range cases {
		if got := runPastTimeout(tc.run); got != tc.want {
			t.Errorf("%s: runPastTimeout() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEffectiveSkillRefsUsesOnlySelectionsAndModeDefaults(t *testing.T) {
	cases := []struct {
		name string
		run  []platformv1alpha1.NamedRef
		mode []platformv1alpha1.NamedRef
		want []platformv1alpha1.NamedRef
	}{
		{name: "no selection enables no skills"},
		{name: "project selection", run: []platformv1alpha1.NamedRef{{Name: "frontend-design"}}, want: []platformv1alpha1.NamedRef{{Name: "frontend-design"}}},
		{name: "mode defaults", mode: []platformv1alpha1.NamedRef{{Name: "frontend-design"}}, want: []platformv1alpha1.NamedRef{{Name: "frontend-design"}}},
		{name: "trim and deduplicate", run: []platformv1alpha1.NamedRef{{Name: " frontend-design "}, {Name: ""}}, mode: []platformv1alpha1.NamedRef{{Name: "frontend-design"}, {Name: "astro"}}, want: []platformv1alpha1.NamedRef{{Name: "frontend-design"}, {Name: "astro"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := &platformv1alpha1.AgentRun{Spec: platformv1alpha1.AgentRunSpec{SkillRefs: tc.run}}
			mode := &platformv1alpha1.ModeTemplateSpec{DefaultSkillRefs: tc.mode}
			applySpecDefaults(run, mode, nil)
			if !namedRefsEqual(run.Spec.SkillRefs, tc.want) {
				t.Fatalf("skills = %v, want %v", run.Spec.SkillRefs, tc.want)
			}
			if needsSpecDefaults(run, mode, nil) {
				t.Fatal("skill defaults should be idempotent")
			}
		})
	}
}

func TestEffectiveSkillRefsExcludesStandingOverseer(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Labels: map[string]string{
				orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleOverseer,
				orchestration.SupervisedRunLabel:   "primary-run",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: platformv1alpha1.GroupVersion.String(),
				Kind:       standingOverseerOwnerKind,
				Name:       "primary-run",
			}},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			SkillRefs: []platformv1alpha1.NamedRef{{Name: explicitSkillName}},
		},
	}
	snapshot := &platformv1alpha1.ModeTemplateSpec{
		DefaultSkillRefs: []platformv1alpha1.NamedRef{{Name: "mode-skill"}},
	}

	if got := effectiveSkillRefs(run, snapshot); len(got) != 0 {
		t.Fatalf("effectiveSkillRefs() = %v, want no skills for standing overseer", got)
	}
}

func TestSecurityScanRunDoesNotInheritNamespaceSkillCatalog(t *testing.T) {
	run := &platformv1alpha1.AgentRun{Spec: platformv1alpha1.AgentRunSpec{
		Trigger:   platformv1alpha1.TriggerRef{Kind: " securityscan ", Name: "nightly"},
		SkillRefs: []platformv1alpha1.NamedRef{{Name: "task-skill"}},
	}}
	snapshot := &platformv1alpha1.ModeTemplateSpec{
		DefaultSkillRefs: []platformv1alpha1.NamedRef{{Name: "security-scan"}},
	}

	got := effectiveSkillRefs(run, snapshot)
	gotNames := make([]string, 0, len(got))
	for _, ref := range got {
		gotNames = append(gotNames, ref.Name)
	}
	if !slices.Equal(gotNames, []string{"task-skill", "security-scan"}) {
		t.Fatalf("effectiveSkillRefs() = %v, want only task and mode skills", gotNames)
	}
}

func TestEnsureInitializedAppliesRuntimeAndMCPDefaults(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-init-defaults",
			Namespace: "default",
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:           platformv1alpha1.TriggerRef{Kind: "LinearProject", Name: "payments"},
			Repository:        platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			WorkflowMode:      platformv1alpha1.WorkflowModeChat,
			RuntimeProfileRef: &platformv1alpha1.NamedRef{Name: "interactive-readonly"},
			MCPPolicyRef:      &platformv1alpha1.NamedRef{Name: "safe-mcp"},
			SkillRefs:         []platformv1alpha1.NamedRef{{Name: explicitSkillName}},
		},
	}
	runtimeProfile := &platformv1alpha1.RuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "interactive-readonly", Namespace: "default"},
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Security: &platformv1alpha1.RuntimeProfileSecurity{
				PermissionMode:  platformv1alpha1.PermissionMode("read-only"),
				GitRemoteWrites: platformv1alpha1.GitRemoteWritesDisabled,
				DefaultTimeout:  metav1.Duration{Duration: 45 * time.Minute},
			},
		},
	}
	mcpPolicy := &platformv1alpha1.MCPPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "safe-mcp", Namespace: "default"},
		Spec: platformv1alpha1.MCPPolicySpec{
			AllowedServers: []platformv1alpha1.MCPAllowedServer{
				{Name: "github"},
				{Name: "filesystem"},
			},
		},
	}
	alphaSkill := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-skill", Namespace: "default"},
		Spec: platformv1alpha1.SkillSpec{
			Source: platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: "alpha"}},
		},
	}
	zetaSkill := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "zeta-skill", Namespace: "default"},
		Spec: platformv1alpha1.SkillSpec{
			Source: platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: "zeta"}},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, runtimeProfile, mcpPolicy, zetaSkill, alphaSkill).
		Build()

	reconciler := &AgentRunReconciler{Client: k8sClient}
	changed, err := reconciler.ensureInitialized(context.Background(), run)
	if err != nil {
		t.Fatalf("ensureInitialized() error = %v", err)
	}
	if !changed {
		t.Fatal("ensureInitialized() changed = false, want true")
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}

	if updated.Spec.Limits == nil {
		t.Fatal("Spec.Limits = nil, want populated defaults")
	}
	if updated.Spec.Limits.MaxRuntime.Duration != 45*time.Minute {
		t.Fatalf("Spec.Limits.MaxRuntime = %s, want 45m", updated.Spec.Limits.MaxRuntime.Duration)
	}
	gotSkillRefs := make([]string, 0, len(updated.Spec.SkillRefs))
	for _, ref := range updated.Spec.SkillRefs {
		gotSkillRefs = append(gotSkillRefs, ref.Name)
	}
	if !slices.Equal(gotSkillRefs, []string{explicitSkillName}) {
		t.Fatalf("Spec.SkillRefs = %v, want only explicitly selected skills", gotSkillRefs)
	}
	if updated.Status.Policy == nil {
		t.Fatal("Status.Policy = nil, want resolved defaults")
	}
	if updated.Status.Policy.ResolvedPermissionMode != "read-only" {
		t.Fatalf("ResolvedPermissionMode = %q, want read-only", updated.Status.Policy.ResolvedPermissionMode)
	}
	if updated.Status.Policy.ResolvedGitRemoteWrites != string(platformv1alpha1.GitRemoteWritesDisabled) {
		t.Fatalf("ResolvedGitRemoteWrites = %q, want disabled", updated.Status.Policy.ResolvedGitRemoteWrites)
	}
	if len(updated.Status.Policy.ResolvedMCPServers) != 2 {
		t.Fatalf("ResolvedMCPServers len = %d, want 2", len(updated.Status.Policy.ResolvedMCPServers))
	}
	if updated.Status.Policy.ResolvedMCPServers[0] != "github" || updated.Status.Policy.ResolvedMCPServers[1] != "filesystem" {
		t.Fatalf("ResolvedMCPServers = %#v, want [github filesystem]", updated.Status.Policy.ResolvedMCPServers)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhasePending {
		t.Fatalf("Phase = %q, want %q", updated.Status.Phase, platformv1alpha1.AgentRunPhasePending)
	}
}

func TestResolvedGitRemoteWritesUsesRestrictiveFallback(t *testing.T) {
	profile := &platformv1alpha1.RuntimeProfile{Spec: platformv1alpha1.RuntimeProfileSpec{
		Security: &platformv1alpha1.RuntimeProfileSecurity{GitRemoteWrites: platformv1alpha1.GitRemoteWritesEnabled},
	}}
	if got := resolvedGitRemoteWrites(profile); got != string(platformv1alpha1.GitRemoteWritesDisabled) {
		t.Fatalf("resolvedGitRemoteWrites() = %q, want disabled for missing permissionMode", got)
	}
	profile.Spec.Security.PermissionMode = platformv1alpha1.PermissionModeWorkspaceWrite
	if got := resolvedGitRemoteWrites(profile); got != string(platformv1alpha1.GitRemoteWritesEnabled) {
		t.Fatalf("resolvedGitRemoteWrites() = %q, want enabled", got)
	}
}

func TestReconcileRunCreatesSandboxClaimWithoutLifecycleExpiry(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	started := metav1.Now()
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "claim-without-expiry",
			Namespace: "default",
			UID:       types.UID("claim-without-expiry-uid"),
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main"},
			Model:      "gpt-5.4",
			Image:      "ghcr.io/example/worker:latest",
			Limits: &platformv1alpha1.AgentRunLimits{
				MaxRuntime: metav1.Duration{Duration: 30 * time.Minute},
			},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhasePending,
			Queue:     &platformv1alpha1.AgentRunQueueStatus{State: "Queued"},
			StartedAt: &started,
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()

	reconciler := &AgentRunReconciler{Client: k8sClient}
	if _, err := reconciler.reconcileRun(context.Background(), run); err != nil {
		t.Fatalf("reconcileRun() error = %v", err)
	}

	claim := &agentsandboxextensionsv1alpha1.SandboxClaim{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: sandboxClaimName(run), Namespace: run.Namespace}, claim); err != nil {
		t.Fatalf("get sandbox claim: %v", err)
	}
	if claim.Spec.Lifecycle != nil {
		t.Fatalf("SandboxClaim lifecycle = %#v, want nil so AgentRun timeouts do not delete sandbox resources", claim.Spec.Lifecycle)
	}
}

func TestClaimReadyFailureIgnoresTransientPodUpdateConflict(t *testing.T) {
	t.Parallel()

	claim := &agentsandboxextensionsv1alpha1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "run-slack-conflict", Namespace: "default"},
		Status: agentsandboxextensionsv1alpha1.SandboxClaimStatus{
			Conditions: []metav1.Condition{{
				Type:   string(agentsandboxv1alpha1.SandboxConditionReady),
				Status: metav1.ConditionFalse,
				Reason: "ReconcilerError",
				Message: "Error seen: failed to update pod: Operation cannot be fulfilled on pods \"run-slack-conflict\": " +
					"the object has been modified; please apply your changes to the latest version and try again",
			}},
		},
	}

	if err := claimReadyFailure(claim); err != nil {
		t.Fatalf("claimReadyFailure() error = %v, want transient conflict ignored", err)
	}
}

func TestClaimReadyFailureKeepsPermanentReconcilerError(t *testing.T) {
	t.Parallel()

	claim := &agentsandboxextensionsv1alpha1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "run-owned-elsewhere", Namespace: "default"},
		Status: agentsandboxextensionsv1alpha1.SandboxClaimStatus{
			Conditions: []metav1.Condition{{
				Type:    string(agentsandboxv1alpha1.SandboxConditionReady),
				Status:  metav1.ConditionFalse,
				Reason:  "ReconcilerError",
				Message: "Error seen: sandbox is controlled by another claim",
			}},
		},
	}

	err := claimReadyFailure(claim)
	if err == nil || !strings.Contains(err.Error(), "controlled by another claim") {
		t.Fatalf("claimReadyFailure() error = %v, want permanent reconciler error", err)
	}
}

func TestClaimTemplateNotFoundIsTransient(t *testing.T) {
	t.Parallel()

	newClaim := func(age time.Duration) *agentsandboxextensionsv1alpha1.SandboxClaim {
		return &agentsandboxextensionsv1alpha1.SandboxClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "run-claim",
				Namespace:         "default",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
			},
			Spec: agentsandboxextensionsv1alpha1.SandboxClaimSpec{
				TemplateRef: agentsandboxextensionsv1alpha1.SandboxTemplateRef{Name: "run-template"},
			},
			Status: agentsandboxextensionsv1alpha1.SandboxClaimStatus{
				Conditions: []metav1.Condition{{
					Type:    string(agentsandboxv1alpha1.SandboxConditionReady),
					Status:  metav1.ConditionFalse,
					Reason:  "TemplateNotFound",
					Message: "SandboxTemplate \"run-template\" not found",
				}},
			},
		}
	}
	template := &agentsandboxextensionsv1alpha1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "run-template", Namespace: "default"},
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "default"}}

	cases := []struct {
		name     string
		claim    *agentsandboxextensionsv1alpha1.SandboxClaim
		template *agentsandboxextensionsv1alpha1.SandboxTemplate
		want     bool
	}{
		{name: "template exists so the claim controller cache is stale", claim: newClaim(10 * time.Minute), template: template, want: true},
		{name: "template invisible but claim within the creation grace window", claim: newClaim(time.Second), want: true},
		{name: "template truly missing past the grace window", claim: newClaim(10 * time.Minute), want: false},
		{
			name: "other ready failure reasons stay terminal",
			claim: func() *agentsandboxextensionsv1alpha1.SandboxClaim {
				claim := newClaim(time.Second)
				claim.Status.Conditions[0].Reason = "InvalidMetadata"
				return claim
			}(),
			template: template,
			want:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := platformv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			addAgentSandboxSchemes(t, scheme)
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.claim)
			if tc.template != nil {
				builder = builder.WithObjects(tc.template)
			}
			r := &AgentRunReconciler{Client: builder.Build()}
			got, err := r.claimTemplateNotFoundIsTransient(context.Background(), run, tc.claim)
			if err != nil {
				t.Fatalf("claimTemplateNotFoundIsTransient() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("claimTemplateNotFoundIsTransient() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMonitorAgentSandboxClearsLegacyClaimLifecycle(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	future := metav1.NewTime(time.Now().Add(time.Hour))
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-lifecycle", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				Provider: agentSandboxProvider,
				ClaimRef: &platformv1alpha1.NamedRef{Name: "run-legacy-lifecycle"},
			},
		},
	}
	claim := &agentsandboxextensionsv1alpha1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "run-legacy-lifecycle", Namespace: "default"},
		Spec: agentsandboxextensionsv1alpha1.SandboxClaimSpec{
			Lifecycle: &agentsandboxextensionsv1alpha1.Lifecycle{
				ShutdownTime:   &future,
				ShutdownPolicy: agentsandboxextensionsv1alpha1.ShutdownPolicyRetain,
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, claim).
		Build()

	reconciler := &AgentRunReconciler{Client: k8sClient}
	result, err := reconciler.monitorAgentSandbox(context.Background(), run, 5*time.Second)
	if err != nil {
		t.Fatalf("monitorAgentSandbox() error = %v", err)
	}
	if result.RequeueAfter != 5*time.Second {
		t.Fatalf("RequeueAfter = %s, want 5s", result.RequeueAfter)
	}

	updatedClaim := &agentsandboxextensionsv1alpha1.SandboxClaim{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(claim), updatedClaim); err != nil {
		t.Fatalf("get updated claim: %v", err)
	}
	if updatedClaim.Spec.Lifecycle != nil {
		t.Fatalf("Lifecycle = %#v, want nil", updatedClaim.Spec.Lifecycle)
	}
}

func TestMonitorAgentSandboxExpiredClaimPausesTimedOutRun(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	started := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "expired-claim-pauses", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Limits: &platformv1alpha1.AgentRunLimits{
				MaxRuntime: metav1.Duration{Duration: time.Hour},
			},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhaseRunning,
			Queue:     &platformv1alpha1.AgentRunQueueStatus{State: "Running"},
			StartedAt: &started,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				Provider: agentSandboxProvider,
				ClaimRef: &platformv1alpha1.NamedRef{Name: "run-expired-claim-pauses"},
			},
		},
	}
	claim := expiredSandboxClaim("run-expired-claim-pauses", "default")

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, claim).
		Build()

	reconciler := &AgentRunReconciler{Client: k8sClient}
	if _, err := reconciler.monitorAgentSandbox(context.Background(), run, 5*time.Second); err != nil {
		t.Fatalf("monitorAgentSandbox() error = %v", err)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhasePaused {
		t.Fatalf("Phase = %q, want Paused", updated.Status.Phase)
	}
	if updated.Status.LastError != "" {
		t.Fatalf("LastError = %q, want empty", updated.Status.LastError)
	}
	if updated.Status.Queue == nil || updated.Status.Queue.State != "Paused" ||
		!strings.Contains(updated.Status.Queue.BlockedReason, "extend maxRuntime to resume") {
		t.Fatalf("Queue = %#v, want paused resume message", updated.Status.Queue)
	}
}

func TestMonitorAgentSandboxExpiredClaimWithinTimeoutReplacesForResume(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	started := metav1.Now()
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "expired-claim-resume", Namespace: "default", UID: types.UID("expired-claim-resume-uid")},
		Spec: platformv1alpha1.AgentRunSpec{
			Limits: &platformv1alpha1.AgentRunLimits{
				MaxRuntime: metav1.Duration{Duration: time.Hour},
			},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhaseProvisioning,
			Queue:     &platformv1alpha1.AgentRunQueueStatus{State: "Resuming"},
			StartedAt: &started,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				Provider: agentSandboxProvider,
				ClaimRef: &platformv1alpha1.NamedRef{Name: "run-expired-claim-resume"},
			},
		},
	}
	claim := expiredSandboxClaim("run-expired-claim-resume", "default")
	claim.OwnerReferences = []metav1.OwnerReference{runOwnerRef(run)}
	claim.Labels = sandboxClaimLabels(run)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, claim).
		Build()

	reconciler := &AgentRunReconciler{Client: k8sClient}
	result, err := reconciler.monitorAgentSandbox(context.Background(), run, 5*time.Second)
	if err != nil {
		t.Fatalf("monitorAgentSandbox() error = %v", err)
	}
	if result.RequeueAfter != 2*time.Second {
		t.Fatalf("RequeueAfter = %s, want 2s", result.RequeueAfter)
	}

	claimCheck := &agentsandboxextensionsv1alpha1.SandboxClaim{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(claim), claimCheck); !apierrors.IsNotFound(err) {
		t.Fatalf("expired claim should be deleted before reprovision, got err=%v claim=%#v", err, claimCheck)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseProvisioning {
		t.Fatalf("Phase = %q, want Provisioning", updated.Status.Phase)
	}
	if updated.Status.Sandbox != nil {
		t.Fatalf("Sandbox = %#v, want cleared so the next reconcile creates a fresh claim", updated.Status.Sandbox)
	}
	if updated.Status.LastError != "" {
		t.Fatalf("LastError = %q, want empty", updated.Status.LastError)
	}
}

func TestReconcileChatRunStartsPlanPodWithoutExecuteMaterialization(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat-run",
			Namespace: "default",
			UID:       types.UID("chat-run-uid"),
			Annotations: map[string]string{
				runModeAnnotation: "chat",
			},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			Image:        "ghcr.io/example/worker:latest",
			Model:        "gpt-5.4",
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseBlocked,
			CurrentStep: awaitingUserStep,
			Queue:       &platformv1alpha1.AgentRunQueueStatus{State: "Blocked", BlockedReason: "waiting-for-user"},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()

	reconciler := &AgentRunReconciler{Client: k8sClient}
	result, err := reconciler.reconcileRun(context.Background(), run)
	if err != nil {
		t.Fatalf("reconcileRun() error = %v", err)
	}
	if result.RequeueAfter != 2*time.Second {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, 2*time.Second)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "chat-run", Namespace: "default"}, updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseAdmitted {
		t.Fatalf("Phase = %q, want Admitted", updated.Status.Phase)
	}
	if updated.Status.CurrentStep != awaitingUserStep {
		t.Fatalf("CurrentStep = %q, want %q", updated.Status.CurrentStep, awaitingUserStep)
	}
	if updated.Status.Sandbox == nil || updated.Status.Sandbox.Provider != agentSandboxProvider || updated.Status.Sandbox.ClaimRef == nil {
		t.Fatalf("Sandbox status = %#v, want agent-sandbox claim ref", updated.Status.Sandbox)
	}

	claims := &agentsandboxextensionsv1alpha1.SandboxClaimList{}
	if err := k8sClient.List(context.Background(), claims, client.InNamespace("default")); err != nil {
		t.Fatalf("list sandbox claims: %v", err)
	}
	if len(claims.Items) != 1 {
		t.Fatalf("len(SandboxClaims) = %d, want 1", len(claims.Items))
	}
	templates := &agentsandboxextensionsv1alpha1.SandboxTemplateList{}
	if err := k8sClient.List(context.Background(), templates, client.InNamespace("default")); err != nil {
		t.Fatalf("list sandbox templates: %v", err)
	}
	if len(templates.Items) != 1 {
		t.Fatalf("len(SandboxTemplates) = %d, want 1", len(templates.Items))
	}
	template := templates.Items[0]
	if claims.Items[0].Spec.TemplateRef.Name != template.Name {
		t.Fatalf("claim template ref = %q, want %q", claims.Items[0].Spec.TemplateRef.Name, template.Name)
	}
	if len(template.Spec.PodTemplate.Spec.Containers) == 0 || len(template.Spec.PodTemplate.Spec.Containers[0].Command) < 2 || template.Spec.PodTemplate.Spec.Containers[0].Command[1] != "run" {
		t.Fatalf("template command = %#v, want agent run runner", template.Spec.PodTemplate.Spec.Containers[0].Command)
	}
	foundPLANTASK := false
	for _, env := range template.Spec.PodTemplate.Spec.Containers[0].Env {
		if env.Name == "PLANTASK_NAME" {
			foundPLANTASK = true
			break
		}
	}
	if !foundPLANTASK {
		t.Fatalf("PLANTASK_NAME env was not set: %#v", template.Spec.PodTemplate.Spec.Containers[0].Env)
	}
}

func TestReconcileRestartRunningRunBouncesCompute(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	started := metav1.Now()
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restart-run",
			Namespace: "default",
			UID:       types.UID("restart-run-uid"),
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository:      platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main"},
			WorkflowMode:    platformv1alpha1.WorkflowModeAuto,
			Image:           "ghcr.io/example/worker:latest",
			Model:           "gpt-5.4",
			RestartRequests: 1,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhaseRunning,
			StartedAt: &started,
			Queue:     &platformv1alpha1.AgentRunQueueStatus{State: "Admitted"},
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				Provider:   agentSandboxProvider,
				ClaimRef:   &platformv1alpha1.NamedRef{Name: "run-restart-run"},
				SandboxRef: &platformv1alpha1.NamedRef{Name: "old-restart-pod"},
			},
		},
	}
	oldClaim := &agentsandboxextensionsv1alpha1.SandboxClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "run-restart-run", Namespace: "default", Labels: sandboxClaimLabels(run), OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
	}}
	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "old-restart-pod", Namespace: "default", Labels: map[string]string{
			"platform.gratefulagents.dev/owner-run-uid": string(run.UID),
		}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, oldClaim, oldPod).
		Build()

	reconciler := &AgentRunReconciler{Client: k8sClient}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile restart drain error = %v", err)
	}
	if result.RequeueAfter != drainRequeueAfter {
		t.Fatalf("RequeueAfter = %s, want fixed drain interval while runner pod drains", result.RequeueAfter)
	}
	draining := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), draining); err != nil {
		t.Fatalf("get draining run: %v", err)
	}
	if draining.Status.Sandbox == nil || draining.Status.RestartRequestsHandled != 0 {
		t.Fatalf("draining status = %#v, want sandbox retained and restart unhandled", draining.Status)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "run-restart-run", Namespace: "default"}, &agentsandboxextensionsv1alpha1.SandboxClaim{}); err != nil {
		t.Fatalf("claim get err = %v, want retained while pod drains", err)
	}

	result, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile restarted run error = %v", err)
	}
	if !result.Requeue {
		t.Fatalf("Requeue = false, want true after restart transition")
	}

	restarted := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), restarted); err != nil {
		t.Fatalf("get restarted run: %v", err)
	}
	if restarted.Status.Phase != platformv1alpha1.AgentRunPhasePending {
		t.Fatalf("Phase = %q, want Pending", restarted.Status.Phase)
	}
	if restarted.Status.RestartRequestsHandled != 1 {
		t.Fatalf("RestartRequestsHandled = %d, want 1", restarted.Status.RestartRequestsHandled)
	}
	if restarted.Status.Sandbox != nil {
		t.Fatalf("Sandbox = %#v, want nil before reprovision", restarted.Status.Sandbox)
	}
	if restarted.Status.Queue == nil || restarted.Status.Queue.State != "Restarting" {
		t.Fatalf("Queue = %#v, want Restarting", restarted.Status.Queue)
	}
	if restarted.Status.LastWakeTime == nil || restarted.Status.LastWakeReason != "restart-request" {
		t.Fatalf("restart observability = (%v, %q), want time and restart-request", restarted.Status.LastWakeTime, restarted.Status.LastWakeReason)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "old-restart-pod", Namespace: "default"}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old pod get err = %v, want NotFound", err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "run-restart-run", Namespace: "default"}, &agentsandboxextensionsv1alpha1.SandboxClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old claim get err = %v, want NotFound", err)
	}
}

func TestRestartRequestOnTerminalRunConsumesCounterWithoutAction(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}

	completed := metav1.Now()
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "restart-terminal", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Model:           "gpt-5.4",
			RestartRequests: 1,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseSucceeded,
			CompletedAt: &completed,
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()

	reconciler := &AgentRunReconciler{Client: k8sClient}
	handled, err := reconciler.handleRestartRequest(context.Background(), run)
	if err != nil {
		t.Fatalf("handleRestartRequest error = %v", err)
	}
	if !handled {
		t.Fatalf("handled = false, want true (counter consumed)")
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("Phase = %q, want Succeeded (terminal runs are not restarted)", updated.Status.Phase)
	}
	if updated.Status.RestartRequestsHandled != 1 {
		t.Fatalf("RestartRequestsHandled = %d, want 1", updated.Status.RestartRequestsHandled)
	}
}

func TestReconcileWakeSucceededRunRequeuesFreshSandbox(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	completed := metav1.Now()
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wake-run",
			Namespace: "default",
			UID:       types.UID("wake-run-uid"),
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main"},
			WorkflowMode: platformv1alpha1.WorkflowModeAuto,
			Image:        "ghcr.io/example/worker:latest",
			Model:        "gpt-5.4",
			WakeRequests: 1,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:               platformv1alpha1.AgentRunPhaseSucceeded,
			CompletedAt:         &completed,
			CompletionRequested: true,
			WakeRequestsHandled: 0,
			Queue:               &platformv1alpha1.AgentRunQueueStatus{State: "Succeeded"},
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				Provider:   agentSandboxProvider,
				ClaimRef:   &platformv1alpha1.NamedRef{Name: "run-wake-run"},
				SandboxRef: &platformv1alpha1.NamedRef{Name: "old-wake-pod"},
			},
		},
	}
	oldClaim := &agentsandboxextensionsv1alpha1.SandboxClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "run-wake-run", Namespace: "default", Labels: sandboxClaimLabels(run), OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
	}}
	oldTemplate := &agentsandboxextensionsv1alpha1.SandboxTemplate{ObjectMeta: metav1.ObjectMeta{
		Name: managedSandboxTemplateName(run), Namespace: "default", OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
	}}
	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "old-wake-pod", Namespace: "default", Labels: map[string]string{
			"platform.gratefulagents.dev/owner-run-uid": string(run.UID),
		}},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, oldClaim, oldTemplate, oldPod).
		Build()

	reconciler := &AgentRunReconciler{Client: k8sClient}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile wake drain error = %v", err)
	}
	if result.RequeueAfter != drainRequeueAfter {
		t.Fatalf("RequeueAfter = %s, want fixed drain interval while runner pod drains", result.RequeueAfter)
	}
	draining := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), draining); err != nil {
		t.Fatalf("get draining run: %v", err)
	}
	if draining.Status.Sandbox == nil || draining.Status.WakeRequestsHandled != 0 {
		t.Fatalf("draining status = %#v, want sandbox retained and wake unhandled", draining.Status)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "run-wake-run", Namespace: "default"}, &agentsandboxextensionsv1alpha1.SandboxClaim{}); err != nil {
		t.Fatalf("claim get err = %v, want retained while pod drains", err)
	}

	result, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile woken run error = %v", err)
	}
	if !result.Requeue {
		t.Fatalf("Requeue = false, want true after wake transition")
	}

	woken := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), woken); err != nil {
		t.Fatalf("get woken run: %v", err)
	}
	if woken.Status.Phase != platformv1alpha1.AgentRunPhasePending {
		t.Fatalf("Phase = %q, want Pending", woken.Status.Phase)
	}
	if woken.Status.WakeRequestsHandled != 1 {
		t.Fatalf("WakeRequestsHandled = %d, want 1", woken.Status.WakeRequestsHandled)
	}
	if woken.Status.CompletedAt != nil || woken.Status.CompletionRequested {
		t.Fatalf("completion fields not cleared: completedAt=%v completionRequested=%v", woken.Status.CompletedAt, woken.Status.CompletionRequested)
	}
	if woken.Status.Sandbox != nil {
		t.Fatalf("Sandbox = %#v, want nil before reprovision", woken.Status.Sandbox)
	}
	if woken.Status.LastWakeTime == nil || woken.Status.LastWakeReason != "wake-request" {
		t.Fatalf("wake observability = (%v, %q), want time and wake-request", woken.Status.LastWakeTime, woken.Status.LastWakeReason)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "old-wake-pod", Namespace: "default"}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old pod get err = %v, want NotFound", err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "run-wake-run", Namespace: "default"}, &agentsandboxextensionsv1alpha1.SandboxClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old claim get err = %v, want NotFound", err)
	}

	result, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile reprovision error = %v", err)
	}
	if result.RequeueAfter != 2*time.Second {
		t.Fatalf("RequeueAfter = %s, want 2s", result.RequeueAfter)
	}
	reprovisioned := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), reprovisioned); err != nil {
		t.Fatalf("get reprovisioned run: %v", err)
	}
	if reprovisioned.Status.Phase != platformv1alpha1.AgentRunPhaseAdmitted {
		t.Fatalf("Phase after reprovision = %q, want Admitted", reprovisioned.Status.Phase)
	}
	if reprovisioned.Status.Sandbox == nil || reprovisioned.Status.Sandbox.ClaimRef == nil || reprovisioned.Status.Sandbox.ClaimRef.Name != "run-wake-run" {
		t.Fatalf("Sandbox after reprovision = %#v, want fresh run-wake-run claim", reprovisioned.Status.Sandbox)
	}
}

func TestReconcileCancelRunningRunTearsDownComputeAndMarksCancelled(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "cancel-run",
			Namespace:   "default",
			UID:         types.UID("cancel-run-uid"),
			Annotations: map[string]string{cancelRequestedAnnotation: time.Now().Format(time.RFC3339)},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			WakeRequests: 3,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:               platformv1alpha1.AgentRunPhaseRunning,
			WakeRequestsHandled: 1,
			LastError:           "previous error",
			Queue:               &platformv1alpha1.AgentRunQueueStatus{State: "Running"},
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				Provider:   agentSandboxProvider,
				ClaimRef:   &platformv1alpha1.NamedRef{Name: "run-cancel-run"},
				SandboxRef: &platformv1alpha1.NamedRef{Name: "cancel-pod"},
			},
		},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "cancel-pod", Namespace: "default", Labels: map[string]string{
		"platform.gratefulagents.dev/owner-run-uid": string(run.UID),
	}}}
	claim := &agentsandboxextensionsv1alpha1.SandboxClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "run-cancel-run", Namespace: "default", Labels: sandboxClaimLabels(run), OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
	}}
	template := &agentsandboxextensionsv1alpha1.SandboxTemplate{ObjectMeta: metav1.ObjectMeta{
		Name: managedSandboxTemplateName(run), Namespace: "default", OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
	}}
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cancel-run-binding",
			Labels: map[string]string{
				"platform.gratefulagents.dev/owner-run": run.Name,
				"platform.gratefulagents.dev/namespace": run.Namespace,
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, pod, claim, template, crb).
		Build()
	reconciler := &AgentRunReconciler{Client: k8sClient}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile cancel drain error = %v", err)
	}
	if result.RequeueAfter != drainRequeueAfter {
		t.Fatalf("result = %#v, want requeue while runner pod drains", result)
	}
	draining := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), draining); err != nil {
		t.Fatalf("get draining run: %v", err)
	}
	if draining.Status.Phase != platformv1alpha1.AgentRunPhaseRunning || draining.Status.Sandbox == nil {
		t.Fatalf("draining status = %#v, want running phase with sandbox retained", draining.Status)
	}
	if got := draining.Annotations[cancelRequestedAnnotation]; got == "" {
		t.Fatal("cancel annotation cleared before runner pod drain completed")
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "run-cancel-run", Namespace: "default"}, &agentsandboxextensionsv1alpha1.SandboxClaim{}); err != nil {
		t.Fatalf("claim get err = %v, want retained while pod drains", err)
	}

	result, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile cancelled run error = %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("result = %#v, want no requeue", result)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseCancelled {
		t.Fatalf("Phase = %q, want Cancelled", updated.Status.Phase)
	}
	if updated.Status.CompletedAt == nil {
		t.Fatal("CompletedAt = nil, want set")
	}
	if updated.Status.LastError != "" {
		t.Fatalf("LastError = %q, want empty", updated.Status.LastError)
	}
	if updated.Status.Queue == nil || updated.Status.Queue.State != "Cancelled" || updated.Status.Queue.BlockedReason != "cancelled by user" {
		t.Fatalf("Queue = %#v, want Cancelled/cancelled by user", updated.Status.Queue)
	}
	if updated.Status.Sandbox != nil {
		t.Fatalf("Sandbox = %#v, want nil", updated.Status.Sandbox)
	}
	if updated.Status.WakeRequestsHandled != updated.Spec.WakeRequests {
		t.Fatalf("WakeRequestsHandled = %d, want current wake count %d", updated.Status.WakeRequestsHandled, updated.Spec.WakeRequests)
	}
	if got := updated.Annotations[cancelRequestedAnnotation]; got != "" {
		t.Fatalf("cancel annotation = %q, want cleared", got)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "cancel-pod", Namespace: "default"}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pod get err = %v, want NotFound", err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "run-cancel-run", Namespace: "default"}, &agentsandboxextensionsv1alpha1.SandboxClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("claim get err = %v, want NotFound", err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: managedSandboxTemplateName(run), Namespace: "default"}, &agentsandboxextensionsv1alpha1.SandboxTemplate{}); !apierrors.IsNotFound(err) {
		t.Fatalf("template get err = %v, want NotFound", err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: crb.Name}, &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatalf("clusterrolebinding get err = %v, want NotFound", err)
	}
}

func TestReconcileAddsCleanupFinalizerToNonTerminalRun(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}

	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "finalizer-run", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhasePaused,
			Queue: &platformv1alpha1.AgentRunQueueStatus{State: "Paused"},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	reconciler := &AgentRunReconciler{Client: k8sClient}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile finalizer add error = %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("result = %#v, want no requeue", result)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if !hasFinalizer(updated, agentRunCleanupFinalizer) {
		t.Fatalf("finalizers = %v, want %q", updated.Finalizers, agentRunCleanupFinalizer)
	}
}

func TestReconcileDeletedRunCleansClusterRoleBindingsAndRemovesFinalizer(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}

	addAgentSandboxSchemes(t, scheme)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "delete-run",
			Namespace:  "default",
			Finalizers: []string{agentRunCleanupFinalizer},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "delete-run-binding",
			Labels: map[string]string{
				"platform.gratefulagents.dev/owner-run": run.Name,
				"platform.gratefulagents.dev/namespace": run.Namespace,
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, crb).
		Build()
	if err := k8sClient.Delete(context.Background(), run); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	reconciler := &AgentRunReconciler{Client: k8sClient}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile deleted run error = %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("result = %#v, want no requeue", result)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: crb.Name}, &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatalf("clusterrolebinding get err = %v, want NotFound", err)
	}

	updated := &platformv1alpha1.AgentRun{}
	err = k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated)
	if err == nil && hasFinalizer(updated, agentRunCleanupFinalizer) {
		t.Fatalf("finalizers = %v, want cleanup finalizer removed", updated.Finalizers)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get deleted run after cleanup: %v", err)
	}
}

func TestReconcileDeletedRunWithoutClusterRoleBindingsRemovesFinalizer(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}

	addAgentSandboxSchemes(t, scheme)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "delete-run-no-crb",
			Namespace:  "default",
			Finalizers: []string{agentRunCleanupFinalizer},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/acme/widgets.git"},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	if err := k8sClient.Delete(context.Background(), run); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	stateStore := &recordingAgentRunDataStore{}
	reconciler := &AgentRunReconciler{Client: k8sClient, StateStore: stateStore}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile deleted run without CRBs error = %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("result = %#v, want no requeue", result)
	}
	if len(stateStore.calls) != 1 {
		t.Fatalf("DeleteAgentRunData calls = %#v, want one call", stateStore.calls)
	}
	wantProjectID := projectstate.ProjectID(run.Namespace, run.Spec.Repository.URL)
	if got := stateStore.calls[0]; got.name != run.Name || got.namespace != run.Namespace || got.projectID != wantProjectID {
		t.Fatalf("DeleteAgentRunData call = %#v, want projectID %q", got, wantProjectID)
	}

	updated := &platformv1alpha1.AgentRun{}
	err = k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated)
	if err == nil && hasFinalizer(updated, agentRunCleanupFinalizer) {
		t.Fatalf("finalizers = %v, want cleanup finalizer removed", updated.Finalizers)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get deleted run after cleanup: %v", err)
	}
}

func TestReconcileDeletedRunDrainsPodBeforeDeletingSandboxOrData(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "delete-drain-run",
			Namespace:  "default",
			UID:        types.UID("delete-drain-run-uid"),
			Finalizers: []string{agentRunCleanupFinalizer},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				ClaimRef:   &platformv1alpha1.NamedRef{Name: "delete-drain-claim"},
				SandboxRef: &platformv1alpha1.NamedRef{Name: "delete-drain-pod"},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "delete-drain-pod",
			Namespace:  "default",
			Finalizers: []string{"test.example/drain"},
			Labels: map[string]string{
				"platform.gratefulagents.dev/owner-run-uid": string(run.UID),
			},
		},
	}
	claim := &agentsandboxextensionsv1alpha1.SandboxClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "delete-drain-claim", Namespace: "default", Labels: sandboxClaimLabels(run), OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
	}}
	template := &agentsandboxextensionsv1alpha1.SandboxTemplate{ObjectMeta: metav1.ObjectMeta{
		Name: managedSandboxTemplateName(run), Namespace: "default", OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
	}}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, pod, claim, template).
		Build()
	if err := k8sClient.Delete(context.Background(), run); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	stateStore := &recordingAgentRunDataStore{}
	reconciler := &AgentRunReconciler{Client: k8sClient, StateStore: stateStore}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
	if err != nil {
		t.Fatalf("Reconcile deleting run drain error = %v", err)
	}
	if result.RequeueAfter != drainRequeueAfter {
		t.Fatalf("result = %#v, want requeue while runner pod terminates", result)
	}
	if len(stateStore.calls) != 0 {
		t.Fatalf("DeleteAgentRunData calls = %#v, want none before pod drain", stateStore.calls)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(claim), &agentsandboxextensionsv1alpha1.SandboxClaim{}); err != nil {
		t.Fatalf("claim get err = %v, want retained while pod terminates", err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(template), &agentsandboxextensionsv1alpha1.SandboxTemplate{}); err != nil {
		t.Fatalf("template get err = %v, want retained while pod terminates", err)
	}
	deletingRun := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), deletingRun); err != nil {
		t.Fatalf("get deleting run: %v", err)
	}
	if !hasFinalizer(deletingRun, agentRunCleanupFinalizer) {
		t.Fatal("cleanup finalizer removed before runner pod drain completed")
	}

	deletingPod := &corev1.Pod{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(pod), deletingPod); err != nil {
		t.Fatalf("get deleting pod: %v", err)
	}
	deletingPod.Finalizers = nil
	if err := k8sClient.Update(context.Background(), deletingPod); err != nil {
		t.Fatalf("finish pod termination: %v", err)
	}

	result, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
	if err != nil {
		t.Fatalf("Reconcile drained deleting run error = %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("result = %#v, want cleanup completion", result)
	}
	if len(stateStore.calls) != 1 {
		t.Fatalf("DeleteAgentRunData calls = %#v, want one call after pod drain", stateStore.calls)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(claim), &agentsandboxextensionsv1alpha1.SandboxClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("claim get err = %v, want NotFound after pod drain", err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(template), &agentsandboxextensionsv1alpha1.SandboxTemplate{}); !apierrors.IsNotFound(err) {
		t.Fatalf("template get err = %v, want NotFound after pod drain", err)
	}
}

func TestReconcileDeletedGitHubRunReleasesProcessedIssue(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add triggers scheme: %v", err)
	}

	addAgentSandboxSchemes(t, scheme)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "delete-github-run",
			Namespace:  "default",
			Finalizers: []string{agentRunCleanupFinalizer},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{
				Kind: "GitHubRepository",
				Name: "widgets",
				ExternalRef: &platformv1alpha1.ExternalRef{
					ID: "42",
				},
			},
		},
	}
	gh := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets", Namespace: "default"},
		Status: triggersv1alpha1.GitHubRepositoryStatus{
			IssuesProcessed:   3,
			ProcessedIssueIDs: []string{"41", " 42 ", "43", "42"},
		},
	}
	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}, &triggersv1alpha1.GitHubRepository{}).
		WithObjects(run, gh).
		Build()
	if err := baseClient.Delete(context.Background(), run); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	k8sClient := &concurrentGitHubStatusClient{Client: baseClient, concurrentIssueID: "44"}

	reconciler := &AgentRunReconciler{Client: k8sClient}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
		t.Fatalf("Reconcile deleted GitHub run error = %v", err)
	}
	if got := k8sClient.updateCalls; got != 2 {
		t.Fatalf("GitHubRepository status update calls = %d, want conflict then retry", got)
	}

	updatedGH := &triggersv1alpha1.GitHubRepository{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(gh), updatedGH); err != nil {
		t.Fatalf("get GitHubRepository: %v", err)
	}
	if got, want := updatedGH.Status.ProcessedIssueIDs, []string{"41", "43", "44"}; !slices.Equal(got, want) {
		t.Fatalf("ProcessedIssueIDs = %#v, want %#v", got, want)
	}
	if got := updatedGH.Status.IssuesProcessed; got != 3 {
		t.Fatalf("IssuesProcessed = %d, want cumulative value 3", got)
	}

	updatedRun := &platformv1alpha1.AgentRun{}
	err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updatedRun)
	if err == nil && hasFinalizer(updatedRun, agentRunCleanupFinalizer) {
		t.Fatalf("finalizers = %v, want cleanup finalizer removed", updatedRun.Finalizers)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get deleted run after cleanup: %v", err)
	}
}

func TestReconcileDeletedGitHubRunIgnoresMissingRepository(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add triggers scheme: %v", err)
	}

	addAgentSandboxSchemes(t, scheme)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "delete-orphaned-github-run",
			Namespace:  "default",
			Finalizers: []string{agentRunCleanupFinalizer},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{
				Kind:        "GitHubRepository",
				Name:        "missing",
				ExternalRef: &platformv1alpha1.ExternalRef{ID: "42"},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}, &triggersv1alpha1.GitHubRepository{}).
		WithObjects(run).
		Build()
	if err := k8sClient.Delete(context.Background(), run); err != nil {
		t.Fatalf("delete run: %v", err)
	}

	reconciler := &AgentRunReconciler{Client: k8sClient}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
		t.Fatalf("Reconcile deleted GitHub run with missing repository error = %v", err)
	}

	updatedRun := &platformv1alpha1.AgentRun{}
	err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updatedRun)
	if err == nil && hasFinalizer(updatedRun, agentRunCleanupFinalizer) {
		t.Fatalf("finalizers = %v, want cleanup finalizer removed", updatedRun.Finalizers)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get deleted run after cleanup: %v", err)
	}
}

func TestReconcileDeletedRunKeepsFinalizerWhenDatabaseCleanupFails(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}

	addAgentSandboxSchemes(t, scheme)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "delete-run-db-error",
			Namespace:  "default",
			Finalizers: []string{agentRunCleanupFinalizer},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	if err := k8sClient.Delete(context.Background(), run); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	stateStore := &recordingAgentRunDataStore{err: errors.New("db unavailable")}
	reconciler := &AgentRunReconciler{Client: k8sClient, StateStore: stateStore}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err == nil || !strings.Contains(err.Error(), "deleting AgentRun database state") {
		t.Fatalf("Reconcile deleted run error = %v, want database cleanup error", err)
	}
	if len(stateStore.calls) != 1 {
		t.Fatalf("DeleteAgentRunData calls = %#v, want one call", stateStore.calls)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get deleting run: %v", err)
	}
	if !hasFinalizer(updated, agentRunCleanupFinalizer) {
		t.Fatalf("finalizers = %v, want cleanup finalizer retained", updated.Finalizers)
	}
}

func TestReconcileCancelTerminalRunStripsAnnotationOnly(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}

	completed := metav1.Now()
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "cancel-done",
			Namespace:   "default",
			Annotations: map[string]string{cancelRequestedAnnotation: time.Now().Format(time.RFC3339)},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseSucceeded,
			CompletedAt: &completed,
			Queue:       &platformv1alpha1.AgentRunQueueStatus{State: "Succeeded"},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	reconciler := &AgentRunReconciler{Client: k8sClient}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile terminal cancel error = %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("result = %#v, want no requeue", result)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("Phase = %q, want Succeeded", updated.Status.Phase)
	}
	if updated.Status.CompletedAt == nil {
		t.Fatal("CompletedAt = nil, want preserved")
	}
	if got := updated.Annotations[cancelRequestedAnnotation]; got != "" {
		t.Fatalf("cancel annotation = %q, want cleared", got)
	}
}

func TestPausedRunResumeRespectsCostCap(t *testing.T) {
	t.Parallel()

	buildRun := func(capUSD, spentUSD string) *platformv1alpha1.AgentRun {
		started := metav1.NewTime(time.Now().Add(-time.Minute))
		return &platformv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Name: "cost-paused", Namespace: "default", UID: types.UID("cost-paused-uid")},
			Spec: platformv1alpha1.AgentRunSpec{
				Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
				Limits: &platformv1alpha1.AgentRunLimits{
					MaxCostUsd: capUSD,
					MaxRuntime: metav1.Duration{Duration: time.Hour},
				},
			},
			Status: platformv1alpha1.AgentRunStatus{
				Phase:     platformv1alpha1.AgentRunPhasePaused,
				StartedAt: &started,
				Queue:     &platformv1alpha1.AgentRunQueueStatus{State: "Paused", BlockedReason: "cost cap reached"},
				Metrics:   &platformv1alpha1.AgentRunMetrics{CostUsd: spentUSD},
			},
		}
	}

	cases := []struct {
		name       string
		capUSD     string
		spentUSD   string
		wantPhase  platformv1alpha1.AgentRunPhase
		wantResume bool
	}{
		{"cap still exceeded stays paused", "2", "5.0000", platformv1alpha1.AgentRunPhasePaused, false},
		{"cap raised resumes", "10", "5.0000", platformv1alpha1.AgentRunPhaseProvisioning, true},
		{"no cap resumes", "", "5.0000", platformv1alpha1.AgentRunPhaseProvisioning, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatalf("add corev1 scheme: %v", err)
			}
			if err := platformv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatalf("add platform scheme: %v", err)
			}
			addAgentSandboxSchemes(t, scheme)
			run := buildRun(tc.capUSD, tc.spentUSD)
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&platformv1alpha1.AgentRun{}).
				WithObjects(run).
				Build()

			reconciler := &AgentRunReconciler{Client: k8sClient}
			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
			if err != nil {
				t.Fatalf("Reconcile error = %v", err)
			}
			if result.Requeue != tc.wantResume {
				t.Fatalf("Requeue = %v, want %v", result.Requeue, tc.wantResume)
			}
			updated := &platformv1alpha1.AgentRun{}
			if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
				t.Fatalf("get run: %v", err)
			}
			if updated.Status.Phase != tc.wantPhase {
				t.Fatalf("Phase = %q, want %q", updated.Status.Phase, tc.wantPhase)
			}
		})
	}
}

func TestPausedRunClearsDrainedSandboxAndResumesAtomically(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)
	started := metav1.NewTime(time.Now().Add(-time.Minute))
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "paused-drained", Namespace: "default", UID: types.UID("paused-drained-uid")},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			Limits:     &platformv1alpha1.AgentRunLimits{MaxRuntime: metav1.Duration{Duration: time.Hour}},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhasePaused,
			StartedAt: &started,
			Queue:     &platformv1alpha1.AgentRunQueueStatus{State: "Paused", BlockedReason: "old limit reached"},
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				Provider: agentSandboxProvider,
				ClaimRef: &platformv1alpha1.NamedRef{Name: "missing-drained-claim"},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	reconciler := &AgentRunReconciler{Client: k8sClient}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
	if err != nil {
		t.Fatalf("Reconcile error = %v", err)
	}
	if !result.Requeue {
		t.Fatalf("result = %#v, want immediate requeue", result)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status.Sandbox != nil || updated.Status.Phase != platformv1alpha1.AgentRunPhaseProvisioning {
		t.Fatalf("status = %#v, want sandbox nil and Provisioning without an intermediate Paused/nil state", updated.Status)
	}
	if updated.Status.Queue == nil || updated.Status.Queue.State != "Resuming" {
		t.Fatalf("queue = %#v, want Resuming", updated.Status.Queue)
	}
}

func TestHandleWakeRequestPinsCurrentPhaseGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		phase          platformv1alpha1.AgentRunPhase
		retryCount     int32
		wantHandled    bool
		wantPhase      platformv1alpha1.AgentRunPhase
		wantSandbox    bool
		wantRetryCount int32
	}{
		{"succeeded run wakes", platformv1alpha1.AgentRunPhaseSucceeded, 0, true, platformv1alpha1.AgentRunPhasePending, false, 0},
		{"failed run wakes and consumes retry", platformv1alpha1.AgentRunPhaseFailed, 1, true, platformv1alpha1.AgentRunPhasePending, false, 2},
		{"paused run wakes", platformv1alpha1.AgentRunPhasePaused, 0, true, platformv1alpha1.AgentRunPhasePending, false, 0},
		{"cancelled run resumes without consuming retry", platformv1alpha1.AgentRunPhaseCancelled, 0, true, platformv1alpha1.AgentRunPhasePending, false, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatalf("add corev1 scheme: %v", err)
			}
			if err := platformv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatalf("add platform scheme: %v", err)
			}
			addAgentSandboxSchemes(t, scheme)

			run := &platformv1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{Name: "wake-gate-" + sanitizeName(tc.name), Namespace: "default"},
				Spec: platformv1alpha1.AgentRunSpec{
					WorkflowMode: platformv1alpha1.WorkflowModeChat,
					WakeRequests: 2,
					Limits:       &platformv1alpha1.AgentRunLimits{MaxRetries: 3},
				},
				Status: platformv1alpha1.AgentRunStatus{
					Phase:               tc.phase,
					RetryCount:          tc.retryCount,
					WakeRequestsHandled: 1,
					Sandbox:             &platformv1alpha1.AgentRunSandboxStatus{Provider: agentSandboxProvider},
					CompletedAt:         &metav1.Time{Time: time.Now()},
					CompletionRequested: true,
				},
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&platformv1alpha1.AgentRun{}).
				WithObjects(run).
				Build()
			reconciler := &AgentRunReconciler{Client: k8sClient}

			handled, err := reconciler.handleWakeRequest(context.Background(), run)
			if err != nil {
				t.Fatalf("handleWakeRequest() error = %v", err)
			}
			if handled != tc.wantHandled {
				t.Fatalf("handled = %v, want %v", handled, tc.wantHandled)
			}

			updated := &platformv1alpha1.AgentRun{}
			if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
				t.Fatalf("get updated run: %v", err)
			}
			if updated.Status.Phase != tc.wantPhase {
				t.Fatalf("Phase = %q, want %q", updated.Status.Phase, tc.wantPhase)
			}
			if tc.wantHandled {
				if updated.Status.WakeRequestsHandled != 2 {
					t.Fatalf("WakeRequestsHandled = %d, want 2", updated.Status.WakeRequestsHandled)
				}
				if updated.Status.LastWakeReason != "wake-request" {
					t.Fatalf("LastWakeReason = %q, want wake-request", updated.Status.LastWakeReason)
				}
				if updated.Status.RetryCount != tc.wantRetryCount {
					t.Fatalf("RetryCount = %d, want %d", updated.Status.RetryCount, tc.wantRetryCount)
				}
				if updated.Status.CompletedAt != nil || updated.Status.CompletionRequested {
					t.Fatalf("completion fields not cleared: completedAt=%v completionRequested=%v", updated.Status.CompletedAt, updated.Status.CompletionRequested)
				}
			} else {
				if updated.Status.WakeRequestsHandled != 1 {
					t.Fatalf("WakeRequestsHandled = %d, want unchanged 1", updated.Status.WakeRequestsHandled)
				}
				if updated.Status.LastWakeReason != "" {
					t.Fatalf("LastWakeReason = %q, want unchanged empty", updated.Status.LastWakeReason)
				}
			}

			if gotSandbox := updated.Status.Sandbox != nil; gotSandbox != tc.wantSandbox {
				t.Fatalf("Sandbox present = %v, want %v", gotSandbox, tc.wantSandbox)
			}
		})
	}
}

func TestHandleWakeRequestRefusesFailedWakeWhenMaxRetriesExhausted(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "wake-max-retries-exhausted", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			WakeRequests: 2,
			Limits:       &platformv1alpha1.AgentRunLimits{MaxRetries: 2},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:               platformv1alpha1.AgentRunPhaseFailed,
			RetryCount:          2,
			WakeRequestsHandled: 1,
			Sandbox:             &platformv1alpha1.AgentRunSandboxStatus{Provider: agentSandboxProvider},
			CompletedAt:         &metav1.Time{Time: time.Now()},
			CompletionRequested: true,
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	reconciler := &AgentRunReconciler{Client: k8sClient}

	handled, err := reconciler.handleWakeRequest(context.Background(), run)
	if err != nil {
		t.Fatalf("handleWakeRequest() error = %v", err)
	}
	if !handled {
		t.Fatalf("handled = false, want true for consumed refused wake")
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("Phase = %q, want Failed", updated.Status.Phase)
	}
	if updated.Status.WakeRequestsHandled != 2 {
		t.Fatalf("WakeRequestsHandled = %d, want 2", updated.Status.WakeRequestsHandled)
	}
	if updated.Status.RetryCount != 2 {
		t.Fatalf("RetryCount = %d, want unchanged 2", updated.Status.RetryCount)
	}
	if updated.Status.LastError != "wake refused: maxRetries (2) exhausted" {
		t.Fatalf("LastError = %q, want maxRetries refusal", updated.Status.LastError)
	}
	if updated.Status.Sandbox == nil {
		t.Fatalf("Sandbox = nil, want unchanged")
	}
	if updated.Status.LastWakeReason != "" {
		t.Fatalf("LastWakeReason = %q, want unchanged empty", updated.Status.LastWakeReason)
	}
}

func TestReconcileChatRunRunningWithoutSandboxStartsPod(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat-run-running",
			Namespace: "default",
			UID:       types.UID("chat-run-running-uid"),
			Annotations: map[string]string{
				runModeAnnotation: "chat",
			},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			Image:        "ghcr.io/example/worker:latest",
			Model:        "gpt-5.4",
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
			Queue: &platformv1alpha1.AgentRunQueueStatus{State: "Running"},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()

	reconciler := &AgentRunReconciler{Client: k8sClient}
	result, err := reconciler.reconcileRun(context.Background(), run)
	if err != nil {
		t.Fatalf("reconcileRun() error = %v", err)
	}
	if result.RequeueAfter != 2*time.Second {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, 2*time.Second)
	}

	claims := &agentsandboxextensionsv1alpha1.SandboxClaimList{}
	if err := k8sClient.List(context.Background(), claims, client.InNamespace("default")); err != nil {
		t.Fatalf("list sandbox claims: %v", err)
	}
	if len(claims.Items) != 1 {
		t.Fatalf("len(SandboxClaims) = %d, want 1", len(claims.Items))
	}
}

func TestIsDelegatedChildRunDetectsLabelOrOwner(t *testing.T) {
	t.Parallel()

	withLabel := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-with-label",
			Namespace: "default",
			Labels: map[string]string{
				teamParentLabelName: "parent-run",
			},
		},
	}
	if !isDelegatedChildRun(withLabel) {
		t.Fatal("isDelegatedChildRun(withLabel) = false, want true")
	}

	withOwner := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-with-owner",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: platformv1alpha1.GroupVersion.String(),
				Kind:       "AgentRun",
				Name:       "parent-run",
				UID:        types.UID("parent-uid"),
			}},
		},
	}
	if !isDelegatedChildRun(withOwner) {
		t.Fatal("isDelegatedChildRun(withOwner) = false, want true")
	}

	normalRun := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "normal", Namespace: "default"},
	}
	if isDelegatedChildRun(normalRun) {
		t.Fatal("isDelegatedChildRun(normalRun) = true, want false")
	}
}

func TestReconcileChildRunUsesWorkflowModeForInitialStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		autonomous      bool
		wantInitialStep string
	}{
		{
			name:            "interactive child",
			autonomous:      false,
			wantInitialStep: awaitingUserStep,
		},
		{
			name:            "autonomous child",
			autonomous:      true,
			wantInitialStep: "auto",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatalf("add corev1 scheme: %v", err)
			}
			if err := rbacv1.AddToScheme(scheme); err != nil {
				t.Fatalf("add rbacv1 scheme: %v", err)
			}
			if err := platformv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatalf("add platform scheme: %v", err)
			}
			addAgentSandboxSchemes(t, scheme)

			childAnnotations := map[string]string{}
			if tt.autonomous {
				childAnnotations[childAutonomousAnnotation] = "true"
			}
			child := &platformv1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "child-" + sanitizeName(tt.name),
					Namespace:   "default",
					UID:         types.UID("child-" + sanitizeName(tt.name) + "-uid"),
					Annotations: childAnnotations,
					Labels: map[string]string{
						teamParentLabelName: "parent-run",
					},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: platformv1alpha1.GroupVersion.String(),
						Kind:       "AgentRun",
						Name:       "parent-run",
						UID:        types.UID("parent-uid"),
					}},
				},
				Spec: platformv1alpha1.AgentRunSpec{
					Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main"},
					WorkflowMode: platformv1alpha1.WorkflowModeChat,
					Image:        "ghcr.io/example/worker:latest",
					Model:        "gpt-5.4",
				},
				Status: platformv1alpha1.AgentRunStatus{
					Phase: platformv1alpha1.AgentRunPhasePending,
					Queue: &platformv1alpha1.AgentRunQueueStatus{State: "Queued"},
				},
			}

			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&platformv1alpha1.AgentRun{}).
				WithObjects(child).
				Build()

			reconciler := &AgentRunReconciler{Client: k8sClient}
			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(child)})
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if result.RequeueAfter != 2*time.Second {
				t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, 2*time.Second)
			}

			updated := &platformv1alpha1.AgentRun{}
			if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(child), updated); err != nil {
				t.Fatalf("Get(updated child) error = %v", err)
			}
			if updated.Status.Phase != platformv1alpha1.AgentRunPhaseAdmitted {
				t.Fatalf("Phase = %q, want Admitted", updated.Status.Phase)
			}
			if updated.Status.CurrentStep != tt.wantInitialStep {
				t.Fatalf("CurrentStep = %q, want %q", updated.Status.CurrentStep, tt.wantInitialStep)
			}

			claims := &agentsandboxextensionsv1alpha1.SandboxClaimList{}
			if err := k8sClient.List(context.Background(), claims, client.InNamespace("default")); err != nil {
				t.Fatalf("List(sandbox claims) error = %v", err)
			}
			if len(claims.Items) != 1 {
				t.Fatalf("len(SandboxClaims) = %d, want 1", len(claims.Items))
			}
			templates := &agentsandboxextensionsv1alpha1.SandboxTemplateList{}
			if err := k8sClient.List(context.Background(), templates, client.InNamespace("default")); err != nil {
				t.Fatalf("List(sandbox templates) error = %v", err)
			}
			if len(templates.Items) != 1 {
				t.Fatalf("len(SandboxTemplates) = %d, want 1", len(templates.Items))
			}
			template := templates.Items[0]
			if len(template.Spec.PodTemplate.Spec.Containers) == 0 || len(template.Spec.PodTemplate.Spec.Containers[0].Command) < 2 || template.Spec.PodTemplate.Spec.Containers[0].Command[1] != "run" {
				t.Fatalf("template command = %#v, want agent run runner", template.Spec.PodTemplate.Spec.Containers[0].Command)
			}
			// Env vars CHAT_MODE and AUTONOMOUS_CHILD_RUN should NOT be present —
			// mode is resolved from the CRD by the agent pod at startup.
			for _, env := range template.Spec.PodTemplate.Spec.Containers[0].Env {
				if env.Name == "CHAT_MODE" || env.Name == "AUTONOMOUS_CHILD_RUN" {
					t.Fatalf("unexpected env %s=%s — mode should be CRD-driven, not env-var-driven", env.Name, env.Value)
				}
			}
		})
	}
}

func TestSyncTeamParentStatusAggregatesChildren(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	parent := newTeamParentRun()
	parent.Status.CurrentStep = "implement"

	runningChild := newOwnedTeamChild(parent, "child-b", "implement", "coder")
	runningChild.Status.Phase = platformv1alpha1.AgentRunPhaseRunning

	blockedChild := newOwnedTeamChild(parent, "child-a", "implement", "reviewer")
	blockedChild.Status.Phase = platformv1alpha1.AgentRunPhaseBlocked
	blockedChild.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Blocked", BlockedReason: "merge conflict"}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(parent, runningChild, blockedChild).
		WithObjects(parent, runningChild, blockedChild).
		Build()

	reconciler := &AgentRunReconciler{Client: client}
	changed, err := reconciler.syncTeamParentStatus(context.Background(), parent)
	if err != nil {
		t.Fatalf("syncTeamParentStatus() error = %v", err)
	}
	if !changed {
		t.Fatal("syncTeamParentStatus() changed = false, want true")
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, updated); err != nil {
		t.Fatalf("Get(parent) error = %v", err)
	}
	if updated.Status.TeamSummary == nil {
		t.Fatal("TeamSummary = nil, want populated summary")
	}
	if updated.Status.TeamSummary.CurrentStep != "implement" {
		t.Fatalf("CurrentStep = %q, want implement", updated.Status.TeamSummary.CurrentStep)
	}
	if updated.Status.TeamSummary.CurrentStepIndex != 0 {
		t.Fatalf("CurrentStepIndex = %d, want 0", updated.Status.TeamSummary.CurrentStepIndex)
	}
	if updated.Status.TeamSummary.TotalChildren != 2 {
		t.Fatalf("TotalChildren = %d, want 2", updated.Status.TeamSummary.TotalChildren)
	}
	if updated.Status.TeamSummary.RunningChildren != 2 {
		t.Fatalf("RunningChildren = %d, want 2", updated.Status.TeamSummary.RunningChildren)
	}
	if updated.Status.TeamSummary.BlockedReason != "merge conflict" {
		t.Fatalf("BlockedReason = %q, want merge conflict", updated.Status.TeamSummary.BlockedReason)
	}
	if updated.Status.TeamSummary.ApprovalState != "pending" {
		t.Fatalf("ApprovalState = %q, want pending", updated.Status.TeamSummary.ApprovalState)
	}
	if len(updated.Status.Children) != 2 {
		t.Fatalf("len(Children) = %d, want 2", len(updated.Status.Children))
	}
	if updated.Status.Children[0].Name != "child-a" || updated.Status.Children[1].Name != "child-b" {
		t.Fatalf("Children order = %#v, want child-a then child-b", updated.Status.Children)
	}
}

func TestSyncTeamParentStatusRetriesConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	parent := newTeamParentRun()
	child := newOwnedTeamChild(parent, "child-a", "implement", "coder")
	child.Status.Phase = platformv1alpha1.AgentRunPhaseRunning

	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(parent, child).
		WithObjects(parent, child).
		Build()
	conflictClient := &conflictOnceStatusClient{Client: baseClient}

	reconciler := &AgentRunReconciler{Client: conflictClient}
	changed, err := reconciler.syncTeamParentStatus(context.Background(), parent)
	if err != nil {
		t.Fatalf("syncTeamParentStatus() error = %v", err)
	}
	if !changed {
		t.Fatal("syncTeamParentStatus() changed = false, want true")
	}
	if conflictClient.statusConflictCount != 1 {
		t.Fatalf("statusConflictCount = %d, want 1", conflictClient.statusConflictCount)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := baseClient.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, updated); err != nil {
		t.Fatalf("Get(parent) error = %v", err)
	}
	if updated.Status.TeamSummary == nil || updated.Status.TeamSummary.TotalChildren != 1 {
		t.Fatalf("TeamSummary = %#v, want one child", updated.Status.TeamSummary)
	}
}

func TestSyncTeamStatusFromChildRefreshesParent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	parent := newTeamParentRun()
	child := newOwnedTeamChild(parent, "child-a", "implement", "coder")
	child.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(parent, child).
		WithObjects(parent, child).
		Build()

	reconciler := &AgentRunReconciler{Client: client}
	changed, err := reconciler.syncTeamStatus(context.Background(), child)
	if err != nil {
		t.Fatalf("syncTeamStatus(child) error = %v", err)
	}
	if !changed {
		t.Fatal("syncTeamStatus(child) changed = false, want true")
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, updated); err != nil {
		t.Fatalf("Get(parent) error = %v", err)
	}
	if updated.Status.TeamSummary == nil || updated.Status.TeamSummary.SucceededChildren != 1 {
		t.Fatalf("SucceededChildren = %#v, want 1", updated.Status.TeamSummary)
	}
	if len(updated.Status.Children) != 1 || updated.Status.Children[0].Name != "child-a" {
		t.Fatalf("Children = %#v, want single child-a entry", updated.Status.Children)
	}
}

func newTeamParentRun() *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-run",
			Namespace: "default",
			UID:       types.UID("parent-uid"),
		},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeTeam,
			Team: &platformv1alpha1.AgentRunTeamSpec{
				Steps: []platformv1alpha1.AgentRunTeamStep{{Name: "implement"}},
				CompletionPolicy: &platformv1alpha1.AgentRunCompletionPolicy{
					RequireApproval: true,
				},
			},
		},
	}
}

func newOwnedTeamChild(parent *platformv1alpha1.AgentRun, name, step, role string) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: parent.Namespace,
			Labels: map[string]string{
				teamParentLabel: parent.Name,
				teamStepLabel:   step,
				teamRoleLabel:   role,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: platformv1alpha1.GroupVersion.String(),
				Kind:       "AgentRun",
				Name:       parent.Name,
				UID:        parent.UID,
			}},
		},
	}
}

func expiredSandboxClaim(name, namespace string) *agentsandboxextensionsv1alpha1.SandboxClaim {
	return &agentsandboxextensionsv1alpha1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status: agentsandboxextensionsv1alpha1.SandboxClaimStatus{
			Conditions: []metav1.Condition{{
				Type:    string(agentsandboxv1alpha1.SandboxConditionReady),
				Status:  metav1.ConditionFalse,
				Reason:  agentsandboxextensionsv1alpha1.ClaimExpiredReason,
				Message: "Claim expired. Sandbox resources deleted.",
			}},
		},
	}
}

func sanitizeName(input string) string {
	out := make([]rune, 0, len(input))
	for _, r := range input {
		switch {
		case r >= 'a' && r <= 'z':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+'a'-'A')
		case r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

func newReconcilerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	addAgentSandboxSchemes(t, scheme)
	return scheme
}

func getRun(t *testing.T, c client.Client, run *platformv1alpha1.AgentRun) *platformv1alpha1.AgentRun {
	t.Helper()
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	return updated
}

func provisioningRun(name string, started time.Time) *platformv1alpha1.AgentRun {
	startedAt := metav1.NewTime(started)
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-uid")},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main"},
			Model:      "gpt-5.4",
			Image:      "ghcr.io/example/worker:latest",
			Limits:     &platformv1alpha1.AgentRunLimits{MaxRuntime: metav1.Duration{Duration: 6 * time.Hour}},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhasePending,
			Queue:     &platformv1alpha1.AgentRunQueueStatus{State: "Queued"},
			StartedAt: &startedAt,
		},
	}
}

func TestReconcileRunRetriesTransientProvisioningErrors(t *testing.T) {
	t.Parallel()

	transient := []error{
		apierrors.NewServerTimeout(schema.GroupResource{Resource: "serviceaccounts"}, "create", 1),
		apierrors.NewConflict(schema.GroupResource{Resource: "serviceaccounts"}, "run", errors.New("modified")),
		apierrors.NewTooManyRequests("slow down", 1),
		apierrors.NewInternalError(errors.New("webhook unavailable")),
	}
	for _, injected := range transient {
		// A long-running run re-provisioning its sandbox: the deadline is
		// measured from this attempt, not from the original start.
		run := provisioningRun("transient-provision", time.Now().Add(-5*time.Hour))
		admittedLongAgo := metav1.NewTime(time.Now().Add(-5 * time.Hour))
		run.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
		run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Running", AdmittedAt: &admittedLongAgo}
		c := fake.NewClientBuilder().
			WithScheme(newReconcilerTestScheme(t)).
			WithStatusSubresource(&platformv1alpha1.AgentRun{}).
			WithObjects(run).
			WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*corev1.ServiceAccount); ok {
					return injected
				}
				return c.Create(ctx, obj, opts...)
			}}).
			Build()
		r := &AgentRunReconciler{Client: c}
		if _, err := r.reconcileRun(context.Background(), run); err == nil {
			t.Fatalf("%v: reconcileRun() error = nil, want error returned for backoff", injected)
		}
		updated := getRun(t, c, run)
		if updated.Status.Phase != platformv1alpha1.AgentRunPhaseRunning {
			t.Fatalf("%v: phase = %q, want Running (not failed)", injected, updated.Status.Phase)
		}
		started := provisioningAttemptStart(updated)
		if started == nil || time.Since(started.Time) > time.Minute {
			t.Fatalf("%v: queue = %#v, want a fresh provisioning attempt marker", injected, updated.Status.Queue)
		}
	}
}

func TestReconcileRunFailsTransientProvisioningErrorPastDeadline(t *testing.T) {
	t.Parallel()

	run := provisioningRun("deadline-provision", time.Now().Add(-time.Hour))
	attemptStarted := metav1.NewTime(time.Now().Add(-time.Hour))
	run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: provisioningQueueState, AdmittedAt: &attemptStarted}
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*corev1.ServiceAccount); ok {
				return apierrors.NewServerTimeout(schema.GroupResource{Resource: "serviceaccounts"}, "create", 1)
			}
			return c.Create(ctx, obj, opts...)
		}}).
		Build()
	r := &AgentRunReconciler{Client: c}
	if _, err := r.reconcileRun(context.Background(), run); err != nil {
		t.Fatalf("reconcileRun() error = %v", err)
	}
	updated := getRun(t, c, run)
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseFailed || !strings.Contains(updated.Status.LastError, "not provisioned within") {
		t.Fatalf("status = %q / %q, want Failed after provisioning deadline", updated.Status.Phase, updated.Status.LastError)
	}
}

func TestReconcileRunFailsPermanentOwnershipConflict(t *testing.T) {
	t.Parallel()

	run := provisioningRun("ownership-conflict", time.Now().Add(-time.Minute))
	controller := true
	foreignTemplate := &agentsandboxextensionsv1alpha1.SandboxTemplate{ObjectMeta: metav1.ObjectMeta{
		Name: managedSandboxTemplateName(run), Namespace: run.Namespace,
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: "someone-else", UID: types.UID("other"), Controller: &controller,
		}},
	}}
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, foreignTemplate).
		Build()
	r := &AgentRunReconciler{Client: c}
	if _, err := r.reconcileRun(context.Background(), run); err != nil {
		t.Fatalf("reconcileRun() error = %v", err)
	}
	if got := getRun(t, c, run).Status.Phase; got != platformv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("phase = %q, want Failed for ownership conflict", got)
	}
}

func TestReconcileRunWaitsForStaleServiceAccountFromPreviousIncarnation(t *testing.T) {
	t.Parallel()

	run := provisioningRun("recreated-run", time.Now().Add(-time.Minute))
	controller := true
	staleSA := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: sandboxRunResourceName("run", run), Namespace: run.Namespace,
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: run.Name, UID: types.UID("previous-uid"), Controller: &controller,
		}},
	}}
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, staleSA).
		Build()
	r := &AgentRunReconciler{Client: c}
	result, err := r.reconcileRun(context.Background(), run)
	if err != nil {
		t.Fatalf("reconcileRun() error = %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("result = %#v, want requeue while the stale ServiceAccount is collected", result)
	}
	updated := getRun(t, c, run)
	if updated.Status.Phase != platformv1alpha1.AgentRunPhasePending || updated.Status.Sandbox != nil {
		t.Fatalf("status = %#v, want still Pending without a sandbox", updated.Status)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: sandboxClaimName(run), Namespace: run.Namespace}, &agentsandboxextensionsv1alpha1.SandboxClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("claim get err = %v, want no claim while the stale ServiceAccount exists", err)
	}
}

func claimMonitoringRun(name string, admittedAgo time.Duration) *platformv1alpha1.AgentRun {
	run := provisioningRun(name, time.Now().Add(-admittedAgo))
	admitted := metav1.NewTime(time.Now().Add(-admittedAgo))
	run.Status.Phase = platformv1alpha1.AgentRunPhaseAdmitted
	run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Queued", AdmittedAt: &admitted}
	run.Status.Sandbox = &platformv1alpha1.AgentRunSandboxStatus{
		Provider: agentSandboxProvider,
		ClaimRef: &platformv1alpha1.NamedRef{Name: sandboxClaimName(run)},
	}
	return run
}

func TestMonitorAgentSandboxToleratesUncachedNewClaim(t *testing.T) {
	t.Parallel()

	run := claimMonitoringRun("fresh-claim", 5*time.Second)
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	r := &AgentRunReconciler{Client: c}
	result, err := r.monitorAgentSandbox(context.Background(), run, time.Second)
	if err != nil || result.RequeueAfter != time.Second {
		t.Fatalf("monitorAgentSandbox() = %#v, %v; want requeue within the visibility grace", result, err)
	}
	if got := getRun(t, c, run).Status.Phase; got != platformv1alpha1.AgentRunPhaseAdmitted {
		t.Fatalf("phase = %q, want Admitted", got)
	}
}

func TestMonitorAgentSandboxConfirmsMissingClaimWithAPIReader(t *testing.T) {
	t.Parallel()

	run := claimMonitoringRun("lagging-cache", 10*time.Minute)
	scheme := newReconcilerTestScheme(t)
	cached := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	claim := &agentsandboxextensionsv1alpha1.SandboxClaim{ObjectMeta: metav1.ObjectMeta{
		Name: sandboxClaimName(run), Namespace: run.Namespace, Labels: sandboxClaimLabels(run),
		OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
	}}
	live := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claim).Build()

	r := &AgentRunReconciler{Client: cached, APIReader: live}
	if _, err := r.monitorAgentSandbox(context.Background(), run, time.Second); err != nil {
		t.Fatalf("monitorAgentSandbox() error = %v", err)
	}
	if got := getRun(t, cached, run).Status.Phase; got == platformv1alpha1.AgentRunPhaseFailed {
		t.Fatal("run failed although the uncached read found the claim")
	}

	r = &AgentRunReconciler{Client: cached, APIReader: fake.NewClientBuilder().WithScheme(scheme).Build()}
	if _, err := r.monitorAgentSandbox(context.Background(), run, time.Second); err != nil {
		t.Fatalf("monitorAgentSandbox() error = %v", err)
	}
	if got := getRun(t, cached, run).Status.Phase; got != platformv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("phase = %q, want Failed once the claim is confirmed missing", got)
	}
}

func TestMonitorAgentSandboxFailsUnboundClaimAfterStartupDeadline(t *testing.T) {
	t.Parallel()

	run := claimMonitoringRun("unbound-claim", time.Hour)
	claim := &agentsandboxextensionsv1alpha1.SandboxClaim{ObjectMeta: metav1.ObjectMeta{
		Name: sandboxClaimName(run), Namespace: run.Namespace, Labels: sandboxClaimLabels(run),
		OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
	}}
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, claim).
		Build()
	r := &AgentRunReconciler{Client: c}
	if _, err := r.monitorAgentSandbox(context.Background(), run, time.Second); err != nil {
		t.Fatalf("monitorAgentSandbox() error = %v", err)
	}
	updated := getRun(t, c, run)
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseFailed || !strings.Contains(updated.Status.LastError, "was not bound") {
		t.Fatalf("status = %q / %q, want Failed for an unbound claim past the startup deadline", updated.Status.Phase, updated.Status.LastError)
	}
}

func TestMonitorAgentSandboxPausesTimedOutRunBeforeClaimLookup(t *testing.T) {
	t.Parallel()

	run := claimMonitoringRun("timed-out-unbound", 7*time.Hour)
	run.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	r := &AgentRunReconciler{Client: c}
	if _, err := r.monitorAgentSandbox(context.Background(), run, time.Second); err != nil {
		t.Fatalf("monitorAgentSandbox() error = %v", err)
	}
	if got := getRun(t, c, run).Status.Phase; got != platformv1alpha1.AgentRunPhasePaused {
		t.Fatalf("phase = %q, want Paused", got)
	}
}

func TestStatusWritesDoNotOverwriteFresherPhase(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		freshPhase platformv1alpha1.AgentRunPhase
		write      func(*AgentRunReconciler, *platformv1alpha1.AgentRun) error
	}{
		{"succeeded keeps worker pause", platformv1alpha1.AgentRunPhasePaused, func(r *AgentRunReconciler, run *platformv1alpha1.AgentRun) error {
			return r.patchSucceeded(context.Background(), run)
		}},
		{"running keeps worker question", platformv1alpha1.AgentRunPhaseQuestion, func(r *AgentRunReconciler, run *platformv1alpha1.AgentRun) error {
			return r.patchRunning(context.Background(), run)
		}},
		{"failed keeps terminal", platformv1alpha1.AgentRunPhaseSucceeded, func(r *AgentRunReconciler, run *platformv1alpha1.AgentRun) error {
			return r.markRunFailed(context.Background(), run, errors.New("pod failed"))
		}},
		{"paused keeps terminal", platformv1alpha1.AgentRunPhaseFailed, func(r *AgentRunReconciler, run *platformv1alpha1.AgentRun) error {
			return r.markRunPaused(context.Background(), run)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stored := provisioningRun("stale-write", time.Now().Add(-time.Minute))
			stored.Status.Phase = tc.freshPhase
			stored.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: string(tc.freshPhase), BlockedReason: "worker reason"}
			c := fake.NewClientBuilder().
				WithScheme(newReconcilerTestScheme(t)).
				WithStatusSubresource(&platformv1alpha1.AgentRun{}).
				WithObjects(stored).
				Build()
			// The caller decided from a stale cached copy.
			cached := stored.DeepCopy()
			cached.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
			if err := tc.write(&AgentRunReconciler{Client: c}, cached); err != nil {
				t.Fatalf("write error = %v", err)
			}
			updated := getRun(t, c, stored)
			if updated.Status.Phase != tc.freshPhase || updated.Status.Queue.BlockedReason != "worker reason" {
				t.Fatalf("status = %q / %#v, want fresh %q preserved", updated.Status.Phase, updated.Status.Queue, tc.freshPhase)
			}
		})
	}
}

func TestPatchSandboxQueuedRecordsSandboxWithoutReactivatingTerminalRun(t *testing.T) {
	t.Parallel()

	stored := provisioningRun("cancelled-while-provisioning", time.Now().Add(-time.Minute))
	stored.Status.Phase = platformv1alpha1.AgentRunPhaseCancelled
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(stored).
		Build()
	sandbox := &platformv1alpha1.AgentRunSandboxStatus{Provider: agentSandboxProvider, ClaimRef: &platformv1alpha1.NamedRef{Name: "claim"}}
	if _, err := (&AgentRunReconciler{Client: c}).patchSandboxQueued(context.Background(), stored, sandbox); err != nil {
		t.Fatalf("patchSandboxQueued() error = %v", err)
	}
	updated := getRun(t, c, stored)
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseCancelled || updated.Status.Sandbox == nil {
		t.Fatalf("status = %q / sandbox %#v, want Cancelled with the sandbox recorded for draining", updated.Status.Phase, updated.Status.Sandbox)
	}
}

func pausedRun(name, reason, capUSD string, started time.Duration) *platformv1alpha1.AgentRun {
	run := provisioningRun(name, time.Now().Add(-started))
	run.Spec.Limits.MaxCostUsd = capUSD
	run.Status.Phase = platformv1alpha1.AgentRunPhasePaused
	run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Paused", BlockedReason: reason}
	run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{CostUsd: "5.0000"}
	return run
}

func TestPausedRunCostCapResumeIgnoresPausedTime(t *testing.T) {
	t.Parallel()

	// Paused at the cost cap at 5h of a 6h window; the cap is raised after
	// another 2h. The paused time must not block the resume.
	run := pausedRun("cost-cap-lifted", "Cost cap reached: $5.0000 spent of the $5.00 limit — increase spec.limits.maxCostUsd to resume.", "10", 7*time.Hour)
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	if _, err := (&AgentRunReconciler{Client: c}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	updated := getRun(t, c, run)
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseProvisioning {
		t.Fatalf("phase = %q, want Provisioning", updated.Status.Phase)
	}
	if runPastTimeout(updated) {
		t.Fatal("resumed run is already past its runtime window")
	}
}

func TestPausedRunUpdatesBlockedReasonWhenBlockerChanges(t *testing.T) {
	t.Parallel()

	// Paused for the 6h timeout; maxRuntime extended to 12h but the cost cap
	// is now exceeded: the reason must point at the cost cap.
	run := pausedRun("blocker-changes", timeoutPauseReason(6*time.Hour), "2", 7*time.Hour)
	run.Spec.Limits.MaxRuntime = metav1.Duration{Duration: 12 * time.Hour}
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	r := &AgentRunReconciler{Client: c}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	updated := getRun(t, c, run)
	if updated.Status.Phase != platformv1alpha1.AgentRunPhasePaused || !strings.Contains(updated.Status.Queue.BlockedReason, "maxCostUsd") {
		t.Fatalf("status = %q / %q, want Paused with cost-cap reason", updated.Status.Phase, updated.Status.Queue.BlockedReason)
	}
	// Steady state: no further write once the reason is current.
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
	if err != nil || result.RequeueAfter != 0 {
		t.Fatalf("second Reconcile() = %#v, %v; want idle", result, err)
	}
}

func TestPausedRunTimeoutStillBlocksUntilExtended(t *testing.T) {
	t.Parallel()

	run := pausedRun("timeout-paused", timeoutPauseReason(6*time.Hour), "", 7*time.Hour)
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	if _, err := (&AgentRunReconciler{Client: c}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := getRun(t, c, run).Status.Phase; got != platformv1alpha1.AgentRunPhasePaused {
		t.Fatalf("phase = %q, want Paused until maxRuntime is extended", got)
	}
}

func TestCostCapBlockerTreatsInvalidCapAsBlocked(t *testing.T) {
	t.Parallel()

	run := pausedRun("invalid-cap", "", "0", time.Minute)
	if blocker := costCapBlocker(run); !strings.Contains(blocker, "invalid") {
		t.Fatalf("costCapBlocker() = %q, want invalid-cap blocker", blocker)
	}
	run.Spec.Limits.MaxCostUsd = ""
	if blocker := costCapBlocker(run); blocker != "" {
		t.Fatalf("costCapBlocker() = %q, want none without a cap", blocker)
	}
}

func runClusterRoleBinding(run *platformv1alpha1.AgentRun) *rbacv1.ClusterRoleBinding {
	return clusterRoleBindingForRun(clusterRoleBindingName(run, "run-"+run.Name, "admin-binding"), run, "run-"+run.Name, clusterAdminRoleName)
}

func TestReconcileTerminalRunReleasesClusterRoleBindingsAfterDrain(t *testing.T) {
	t.Parallel()

	run := provisioningRun("worker-finished", time.Now().Add(-time.Hour))
	completed := metav1.NewTime(time.Now().Add(-time.Hour))
	run.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	run.Status.CompletedAt = &completed
	run.Status.Sandbox = &platformv1alpha1.AgentRunSandboxStatus{Provider: agentSandboxProvider, ClaimRef: &platformv1alpha1.NamedRef{Name: "gone"}}
	crb := runClusterRoleBinding(run)
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, crb).
		Build()
	if _, err := (&AgentRunReconciler{Client: c}).reconcileTerminalRun(context.Background(), run); err != nil {
		t.Fatalf("reconcileTerminalRun() error = %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(crb), &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatalf("ClusterRoleBinding get err = %v, want NotFound after terminal drain", err)
	}
}

func TestPausedRunReleasesClusterRoleBindingsAfterDrain(t *testing.T) {
	t.Parallel()

	run := pausedRun("paused-admin", timeoutPauseReason(6*time.Hour), "", 7*time.Hour)
	run.Status.Sandbox = &platformv1alpha1.AgentRunSandboxStatus{Provider: agentSandboxProvider, ClaimRef: &platformv1alpha1.NamedRef{Name: "gone"}}
	crb := runClusterRoleBinding(run)
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, crb).
		Build()
	if _, err := (&AgentRunReconciler{Client: c}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(crb), &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatalf("ClusterRoleBinding get err = %v, want NotFound after paused drain", err)
	}
	if updated := getRun(t, c, run); updated.Status.Sandbox != nil {
		t.Fatalf("sandbox = %#v, want cleared", updated.Status.Sandbox)
	}
}

func TestRequestsForRunPodMapsLabeledPods(t *testing.T) {
	t.Parallel()

	run := provisioningRun("pod-owner", time.Now())
	longRun := provisioningRun(strings.Repeat("l", 70), time.Now())
	c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithObjects(run, longRun).Build()
	r := &AgentRunReconciler{Client: c}
	pod := func(owner *platformv1alpha1.AgentRun) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default", Labels: map[string]string{
			ownerRunLabel: runNameLabelValue(owner.Name), ownerRunUIDLabel: string(owner.UID),
		}}}
	}
	for _, owner := range []*platformv1alpha1.AgentRun{run, longRun} {
		got := r.requestsForRunPod(context.Background(), pod(owner))
		if len(got) != 1 || got[0].Name != owner.Name {
			t.Fatalf("requestsForRunPod(%s) = %#v, want the owning run", owner.Name, got)
		}
	}
	if got := r.requestsForRunPod(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "default"}}); len(got) != 0 {
		t.Fatalf("unlabeled pod mapped to %#v", got)
	}
}

func TestRequestsForRuntimeProfileSkipsTerminalAndOtherProfiles(t *testing.T) {
	t.Parallel()

	active := provisioningRun("profile-active", time.Now())
	active.Spec.RuntimeProfileRef = &platformv1alpha1.NamedRef{Name: "profile"}
	finished := provisioningRun("profile-finished", time.Now())
	finished.Spec.RuntimeProfileRef = &platformv1alpha1.NamedRef{Name: "profile"}
	finished.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	other := provisioningRun("profile-other", time.Now())
	other.Spec.RuntimeProfileRef = &platformv1alpha1.NamedRef{Name: "other"}
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithIndex(&platformv1alpha1.AgentRun{}, runtimeProfileRefIndex, agentRunRuntimeProfileRef).
		WithObjects(active, finished, other).
		Build()
	profile := &platformv1alpha1.RuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "profile", Namespace: "default"}}
	got := (&AgentRunReconciler{Client: c}).requestsForRuntimeProfile(context.Background(), profile)
	if len(got) != 1 || got[0].Name != active.Name {
		t.Fatalf("requestsForRuntimeProfile() = %#v, want only the active run", got)
	}
}

func TestReconcilePreservesTerminalPolicy(t *testing.T) {
	t.Parallel()

	run := provisioningRun("finished-policy", time.Now())
	run.Spec.RuntimeProfileRef = &platformv1alpha1.NamedRef{Name: "profile"}
	run.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	run.Status.Policy = &platformv1alpha1.AgentRunResolvedPolicy{ResolvedGitRemoteWrites: "ran-under-this"}
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	_, err := (&AgentRunReconciler{Client: c}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
	if err != nil {
		t.Fatal(err)
	}
	if got := getRun(t, c, run).Status.Policy.ResolvedGitRemoteWrites; got != "ran-under-this" {
		t.Fatalf("resolved policy = %q, want preserved", got)
	}
}

func TestReconcileTerminalRunReleasesClusterRoleBindingsWithinTTL(t *testing.T) {
	t.Parallel()

	run := provisioningRun("worker-just-finished", time.Now().Add(-time.Hour))
	completed := metav1.Now()
	run.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	run.Status.CompletedAt = &completed
	run.Status.Sandbox = &platformv1alpha1.AgentRunSandboxStatus{
		Provider: agentSandboxProvider, ClaimRef: &platformv1alpha1.NamedRef{Name: "kept"},
	}
	crb := runClusterRoleBinding(run)
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, crb).
		Build()
	r := &AgentRunReconciler{Client: c}
	for range 2 {
		result, err := r.reconcileTerminalRun(context.Background(), run)
		if err != nil {
			t.Fatalf("reconcileTerminalRun() error = %v", err)
		}
		if result.RequeueAfter <= 0 {
			t.Fatalf("result = %#v, want requeue at TTL expiry", result)
		}
	}
	err := c.Get(context.Background(), client.ObjectKeyFromObject(crb), &rbacv1.ClusterRoleBinding{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("ClusterRoleBinding get err = %v, want NotFound before the sandbox TTL elapses", err)
	}
	if updated := getRun(t, c, run); updated.Status.Sandbox == nil {
		t.Fatal("sandbox status cleared within TTL, want it retained for log retention")
	}
}

func TestRestartRequestOnPausedRunConsumesCounterAndStaysPaused(t *testing.T) {
	t.Parallel()

	run := pausedRun("restart-paused", timeoutPauseReason(6*time.Hour), "", 7*time.Hour)
	run.Spec.RestartRequests = 1
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	r := &AgentRunReconciler{Client: c}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
	for range 2 {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
	}
	updated := getRun(t, c, run)
	if updated.Status.Phase != platformv1alpha1.AgentRunPhasePaused {
		t.Fatalf("phase = %q, want Paused (restart must not lift an unraised limit)", updated.Status.Phase)
	}
	if updated.Status.RestartRequestsHandled != 1 {
		t.Fatalf("RestartRequestsHandled = %d, want 1", updated.Status.RestartRequestsHandled)
	}
	if updated.Status.StartedAt == nil || updated.Status.StartedAt.Unix() != run.Status.StartedAt.Unix() {
		t.Fatalf("StartedAt = %v, want unchanged runtime window", updated.Status.StartedAt)
	}
}

func TestCancelRequestKeepsTerminalPhaseWrittenDuringDrain(t *testing.T) {
	t.Parallel()

	stored := provisioningRun("cancel-raced", time.Now().Add(-time.Hour))
	stored.Annotations = map[string]string{cancelRequestedAnnotation: time.Now().Format(time.RFC3339)}
	completed := metav1.Now()
	stored.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	stored.Status.CompletedAt = &completed
	stored.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Succeeded"}
	stored.Status.Sandbox = &platformv1alpha1.AgentRunSandboxStatus{
		Provider: agentSandboxProvider, ClaimRef: &platformv1alpha1.NamedRef{Name: "gone"},
	}
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerTestScheme(t)).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(stored).
		Build()

	// The reconciler observed the run while it was still Running; the worker
	// wrote Succeeded while compute drained.
	observed := stored.DeepCopy()
	observed.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
	observed.Status.CompletedAt = nil
	handled, err := (&AgentRunReconciler{Client: c}).handleCancelRequest(context.Background(), observed)
	if err != nil || !handled {
		t.Fatalf("handleCancelRequest() = %v, %v; want handled", handled, err)
	}
	updated := getRun(t, c, stored)
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseSucceeded || updated.Status.Queue.State != "Succeeded" {
		t.Fatalf("status = %#v, want worker-written Succeeded preserved", updated.Status)
	}
	if updated.Status.Sandbox != nil {
		t.Fatalf("sandbox = %#v, want cleared after drain", updated.Status.Sandbox)
	}
	if _, ok := updated.Annotations[cancelRequestedAnnotation]; ok {
		t.Fatal("cancel annotation not cleared")
	}
}
