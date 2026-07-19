package platform

import (
	"context"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	agentsandboxextensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReconcilePromoteRunningRunTearsDownComputeAndMarksSucceeded(t *testing.T) {
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
			Name:        "promote-run",
			Namespace:   "default",
			UID:         types.UID("promote-run-uid"),
			Annotations: map[string]string{promoteSucceededAnnotation: time.Now().Format(time.RFC3339)},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhaseRunning,
			LastError: "previous error",
			Queue:     &platformv1alpha1.AgentRunQueueStatus{State: "Running"},
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				Provider:   agentSandboxProvider,
				ClaimRef:   &platformv1alpha1.NamedRef{Name: "run-promote-run"},
				SandboxRef: &platformv1alpha1.NamedRef{Name: "promote-pod"},
			},
		},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "promote-pod", Namespace: "default"}}
	claim := &agentsandboxextensionsv1alpha1.SandboxClaim{ObjectMeta: metav1.ObjectMeta{Name: "run-promote-run", Namespace: "default"}}
	template := &agentsandboxextensionsv1alpha1.SandboxTemplate{ObjectMeta: metav1.ObjectMeta{Name: managedSandboxTemplateName(run), Namespace: "default"}}
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "promote-run-binding",
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
		t.Fatalf("Reconcile promote drain error = %v", err)
	}
	if !result.Requeue {
		t.Fatalf("result = %#v, want requeue while runner drains", result)
	}
	result, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile promote error = %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("result = %#v, want no requeue after drain", result)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("Phase = %q, want Succeeded", updated.Status.Phase)
	}
	if updated.Status.CompletedAt == nil {
		t.Fatal("CompletedAt = nil, want set")
	}
	if updated.Status.LastError != "" {
		t.Fatalf("LastError = %q, want empty", updated.Status.LastError)
	}
	if updated.Status.Queue == nil || updated.Status.Queue.State != "Succeeded" {
		t.Fatalf("Queue = %#v, want Succeeded", updated.Status.Queue)
	}
	if updated.Status.Sandbox != nil {
		t.Fatalf("Sandbox = %#v, want nil", updated.Status.Sandbox)
	}
	if got := updated.Annotations[promoteSucceededAnnotation]; got != "" {
		t.Fatalf("promote annotation = %q, want cleared", got)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "promote-pod", Namespace: "default"}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pod get err = %v, want NotFound", err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "run-promote-run", Namespace: "default"}, &agentsandboxextensionsv1alpha1.SandboxClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("claim get err = %v, want NotFound", err)
	}
}

func TestReconcilePromoteTerminalRunStripsAnnotationOnly(t *testing.T) {
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

	completed := metav1.NewTime(time.Now().Add(-time.Hour))
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "promote-done-run",
			Namespace:   "default",
			UID:         types.UID("promote-done-run-uid"),
			Annotations: map[string]string{promoteSucceededAnnotation: time.Now().Format(time.RFC3339)},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseCancelled,
			CompletedAt: &completed,
			Queue:       &platformv1alpha1.AgentRunQueueStatus{State: "Cancelled"},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	reconciler := &AgentRunReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}}); err != nil {
		t.Fatalf("Reconcile promote error = %v", err)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseCancelled {
		t.Fatalf("Phase = %q, want Cancelled (terminal phase must not change)", updated.Status.Phase)
	}
	if got := updated.Annotations[promoteSucceededAnnotation]; got != "" {
		t.Fatalf("promote annotation = %q, want cleared", got)
	}
}
