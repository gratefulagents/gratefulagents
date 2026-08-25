package platform

import (
	"context"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	agentsandboxextensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestClassifyPodFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pod  *corev1.Pod
		want []string
	}{
		{
			name: "oom killed container",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "agent",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 137, Reason: "OOMKilled",
					}},
				}},
			}},
			want: []string{`container "agent" exited with code 137`, "OOMKilled"},
		},
		{
			name: "image pull backoff",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "agent",
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
						Reason: "ImagePullBackOff", Message: "Back-off pulling image \"ghcr.io/x/missing:latest\"",
					}},
				}},
			}},
			want: []string{`container "agent" waiting (ImagePullBackOff)`, "missing:latest"},
		},
		{
			name: "unschedulable",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{{
					Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
					Reason: "Unschedulable", Message: "0/3 nodes are available",
				}},
			}},
			want: []string{"unschedulable", "0/3 nodes are available"},
		},
		{
			name: "init container failure",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
				InitContainerStatuses: []corev1.ContainerStatus{{
					Name: "workspace-init",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 1, Reason: "Error",
					}},
				}},
			}},
			want: []string{`init container "workspace-init" exited with code 1`},
		},
		{
			name: "no details",
			pod:  &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed}},
			want: []string{"no diagnostic details"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyPodFailure(tc.pod)
			for _, fragment := range tc.want {
				if !strings.Contains(got, fragment) {
					t.Fatalf("classifyPodFailure() = %q, want it to contain %q", got, fragment)
				}
			}
		})
	}
	if got := classifyPodFailure(nil); got == "" {
		t.Fatal("classifyPodFailure(nil) returned empty string")
	}
}

func TestFatalPodStartupReason(t *testing.T) {
	t.Parallel()
	fatal := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name:  "agent",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError", Message: "secret \"missing\" not found"}},
	}}}}
	if reason, ok := fatalPodStartupReason(fatal); !ok || !strings.Contains(reason, "CreateContainerConfigError") {
		t.Fatalf("fatalPodStartupReason() = (%q, %v), want fatal CreateContainerConfigError", reason, ok)
	}
	transient := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name:  "agent",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
	}}}}
	if reason, ok := fatalPodStartupReason(transient); ok {
		t.Fatalf("fatalPodStartupReason() = (%q, %v), want ImagePullBackOff treated as transient", reason, ok)
	}
}

func TestDrainEscalationWindows(t *testing.T) {
	now := time.Now()
	fresh := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{Time: now.Add(-10 * time.Second)}}}
	if podDrainEscalationDue(fresh, now) {
		t.Fatal("pod terminating for 10s should not escalate yet")
	}
	stuck := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{Time: now.Add(-5 * time.Minute)}}}
	if !podDrainEscalationDue(stuck, now) {
		t.Fatal("pod terminating for 5m should escalate to force delete")
	}
	if podDrainEscalationDue(&corev1.Pod{}, now) {
		t.Fatal("pod without deletionTimestamp must not escalate")
	}

	recent := now.Add(-time.Minute)
	if claimDrainAbandoned(&recent, now) {
		t.Fatal("claim terminating for 1m should still block the drain")
	}
	old := now.Add(-time.Hour)
	if !claimDrainAbandoned(&old, now) {
		t.Fatal("claim terminating for 1h should be abandoned")
	}
	if claimDrainAbandoned(nil, now) {
		t.Fatal("claim without deletionTimestamp must not be abandoned")
	}
}

func TestDurationFromSecondsEnvOverride(t *testing.T) {
	t.Setenv(podStartupDeadlineEnv, "60")
	if got := podStartupDeadline(); got != time.Minute {
		t.Fatalf("podStartupDeadline() = %v, want 1m", got)
	}
	t.Setenv(podStartupDeadlineEnv, "garbage")
	if got := podStartupDeadline(); got != defaultPodStartupDeadline {
		t.Fatalf("podStartupDeadline() with invalid env = %v, want default %v", got, defaultPodStartupDeadline)
	}
}

func terminationTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := agentsandboxextensionsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func runWithPodSandbox(name string, phase platformv1alpha1.AgentRunPhase, podName string) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default", UID: types.UID("uid-" + name),
			Finalizers: []string{agentRunCleanupFinalizer},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: phase,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: podName},
			},
		},
	}
}

func TestMonitorPodFailedRecordsClassifiedDiagnosis(t *testing.T) {
	t.Parallel()
	scheme := terminationTestScheme(t)
	run := runWithPodSandbox("run-oom", platformv1alpha1.AgentRunPhaseRunning, "run-oom-pod")
	now := metav1.Now()
	run.Status.StartedAt = &now
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "run-oom-pod", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodFailed,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "agent",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}},
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, pod).Build()
	r := &AgentRunReconciler{Client: c}
	if _, err := r.monitorPodName(context.Background(), run, "run-oom-pod", time.Second); err != nil {
		t.Fatalf("monitorPodName() error = %v", err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("phase = %q, want Failed", updated.Status.Phase)
	}
	if !strings.Contains(updated.Status.LastError, "OOMKilled") || !strings.Contains(updated.Status.LastError, "137") {
		t.Fatalf("LastError = %q, want OOMKilled exit-code diagnosis", updated.Status.LastError)
	}
}

func TestMonitorPodPendingFailsFastOnFatalConfigError(t *testing.T) {
	t.Parallel()
	scheme := terminationTestScheme(t)
	run := runWithPodSandbox("run-cfg", platformv1alpha1.AgentRunPhaseProvisioning, "run-cfg-pod")
	now := metav1.Now()
	run.Status.StartedAt = &now
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "run-cfg-pod", Namespace: "default",
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-5 * time.Minute)},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "agent",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError", Message: "secret not found"}},
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, pod).Build()
	r := &AgentRunReconciler{Client: c}
	if _, err := r.monitorPodName(context.Background(), run, "run-cfg-pod", time.Second); err != nil {
		t.Fatalf("monitorPodName() error = %v", err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("phase = %q, want Failed for fatal config error", updated.Status.Phase)
	}
	if !strings.Contains(updated.Status.LastError, "CreateContainerConfigError") {
		t.Fatalf("LastError = %q, want CreateContainerConfigError diagnosis", updated.Status.LastError)
	}
}

func TestMonitorPodPendingFailsAtStartupDeadline(t *testing.T) {
	t.Setenv(podStartupDeadlineEnv, "60")
	scheme := terminationTestScheme(t)
	run := runWithPodSandbox("run-stuck", platformv1alpha1.AgentRunPhaseProvisioning, "run-stuck-pod")
	now := metav1.Now()
	run.Status.StartedAt = &now
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "run-stuck-pod", Namespace: "default",
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-2 * time.Minute)},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
				Reason: "Unschedulable", Message: "0/3 nodes are available",
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, pod).Build()
	r := &AgentRunReconciler{Client: c}
	if _, err := r.monitorPodName(context.Background(), run, "run-stuck-pod", time.Second); err != nil {
		t.Fatalf("monitorPodName() error = %v", err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("phase = %q, want Failed after startup deadline", updated.Status.Phase)
	}
	if !strings.Contains(updated.Status.LastError, "unschedulable") {
		t.Fatalf("LastError = %q, want unschedulable diagnosis", updated.Status.LastError)
	}
}

func TestMonitorPodPendingWithinDeadlineKeepsWaiting(t *testing.T) {
	t.Parallel()
	scheme := terminationTestScheme(t)
	run := runWithPodSandbox("run-fresh", platformv1alpha1.AgentRunPhaseProvisioning, "run-fresh-pod")
	now := metav1.Now()
	run.Status.StartedAt = &now
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "run-fresh-pod", Namespace: "default",
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-30 * time.Second)},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, pod).Build()
	r := &AgentRunReconciler{Client: c}
	result, err := r.monitorPodName(context.Background(), run, "run-fresh-pod", time.Second)
	if err != nil {
		t.Fatalf("monitorPodName() error = %v", err)
	}
	if result.RequeueAfter != time.Second {
		t.Fatalf("result = %#v, want requeue while pending within deadline", result)
	}
}

func TestReconcileTerminalRunDrainsSandboxAfterTTL(t *testing.T) {
	t.Setenv(terminalSandboxTTLEnv, "60")
	scheme := terminationTestScheme(t)
	run := runWithPodSandbox("run-done", platformv1alpha1.AgentRunPhaseSucceeded, "run-done-pod")
	completed := metav1.Time{Time: time.Now().Add(-5 * time.Minute)}
	run.Status.CompletedAt = &completed
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "run-done-pod", Namespace: "default",
		Labels: map[string]string{"platform.gratefulagents.dev/owner-run-uid": string(run.UID)},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, pod).Build()
	r := &AgentRunReconciler{Client: c}

	// First pass: the TTL has elapsed, so the drain deletes the pod.
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("result = %#v, want requeue while draining", result)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-done-pod"}, &corev1.Pod{}); err == nil {
		t.Fatal("zombie pod still exists after TTL drain pass")
	}

	// Second pass: fully drained, sandbox status cleared, no more requeue.
	result, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
	if err != nil {
		t.Fatalf("Reconcile() second pass error = %v", err)
	}
	if result.RequeueAfter != 0 || result.Requeue {
		t.Fatalf("result = %#v, want steady state after drain", result)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Sandbox != nil {
		t.Fatal("status.Sandbox not cleared after terminal drain")
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("phase = %q, want terminal phase preserved", updated.Status.Phase)
	}
}

func TestReconcileTerminalRunKeepsSandboxWithinTTL(t *testing.T) {
	t.Parallel()
	scheme := terminationTestScheme(t)
	run := runWithPodSandbox("run-fresh-done", platformv1alpha1.AgentRunPhaseSucceeded, "run-fresh-done-pod")
	completed := metav1.Now()
	run.Status.CompletedAt = &completed
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "run-fresh-done-pod", Namespace: "default",
		Labels: map[string]string{"platform.gratefulagents.dev/owner-run-uid": string(run.UID)},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, pod).Build()
	r := &AgentRunReconciler{Client: c}
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("result = %#v, want requeue at TTL expiry", result)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-fresh-done-pod"}, &corev1.Pod{}); err != nil {
		t.Fatalf("pod should be retained within TTL: %v", err)
	}
}

func TestReleaseRunSandboxForceDeletesStuckPod(t *testing.T) {
	t.Parallel()
	scheme := terminationTestScheme(t)
	run := runWithPodSandbox("run-wedged", platformv1alpha1.AgentRunPhaseCancelled, "run-wedged-pod")
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "run-wedged-pod", Namespace: "default",
		Labels:            map[string]string{"platform.gratefulagents.dev/owner-run-uid": string(run.UID)},
		Finalizers:        []string{"example.com/stuck"},
		DeletionTimestamp: &metav1.Time{Time: time.Now().Add(-10 * time.Minute)},
	}}
	var forceDeletes int
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, pod).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				options := &client.DeleteOptions{}
				for _, opt := range opts {
					opt.ApplyToDelete(options)
				}
				if options.GracePeriodSeconds != nil && *options.GracePeriodSeconds == 0 {
					forceDeletes++
				}
				return cl.Delete(ctx, obj, opts...)
			},
		}).Build()
	r := &AgentRunReconciler{Client: c}
	drained, err := r.releaseRunSandbox(context.Background(), run)
	if err != nil {
		t.Fatalf("releaseRunSandbox() error = %v", err)
	}
	if drained {
		t.Fatal("drain reported complete while the stuck pod object persists")
	}
	if forceDeletes == 0 {
		t.Fatal("expected a force delete (GracePeriodSeconds=0) for the pod stuck terminating past the escalation window")
	}
}

func TestReleaseRunSandboxAbandonsClaimStuckTerminating(t *testing.T) {
	t.Parallel()
	scheme := terminationTestScheme(t)
	run := runWithPodSandbox("run-claim", platformv1alpha1.AgentRunPhaseCancelled, "")
	run.Status.Sandbox = nil
	controllerTrue := true
	claim := &agentsandboxextensionsv1alpha1.SandboxClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "run-claim-sbx", Namespace: "default",
		Finalizers:        []string{"example.com/stuck"},
		DeletionTimestamp: &metav1.Time{Time: time.Now().Add(-time.Hour)},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun",
			Name: run.Name, UID: run.UID, Controller: &controllerTrue,
		}},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, claim).Build()
	r := &AgentRunReconciler{Client: c}
	drained, err := r.releaseRunSandbox(context.Background(), run)
	if err != nil {
		t.Fatalf("releaseRunSandbox() error = %v", err)
	}
	if !drained {
		t.Fatal("drain must proceed past a claim stuck terminating beyond the give-up window")
	}
}
