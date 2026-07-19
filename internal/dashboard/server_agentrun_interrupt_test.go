package dashboard

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func TestPromoteAgentRunAllowsOwnerToRequestPromotion(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-promote", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-promote", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.PromoteAgentRun(actorContext("user-1", "", "", ""), &platform.PromoteAgentRunRequest{
		Namespace: "default",
		Name:      "run-promote",
	})
	if err != nil {
		t.Fatalf("PromoteAgentRun() error = %v", err)
	}
	if resp.Name != "run-promote" {
		t.Fatalf("response name = %q, want run-promote", resp.Name)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-promote"}, updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	ts := updated.Annotations[promoteSucceededAnnotation]
	if ts == "" {
		t.Fatal("promote annotation is empty")
	}
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Fatalf("promote annotation = %q, want RFC3339 timestamp: %v", ts, err)
	}
}

func TestPromoteAgentRunRejectsNonOwner(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-promote-viewer", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-promote-viewer", "default", "owner-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.PromoteAgentRun(actorContext("viewer-1", "", "", ""), &platform.PromoteAgentRunRequest{
		Namespace: "default",
		Name:      "run-promote-viewer",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("connect.CodeOf(err) = %v, want PermissionDenied (err=%v)", connect.CodeOf(err), err)
	}
}

func TestPromoteAgentRunRejectsTerminalRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-promote-done", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseCancelled,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-promote-done", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.PromoteAgentRun(actorContext("user-1", "", "", ""), &platform.PromoteAgentRunRequest{
		Namespace: "default",
		Name:      "run-promote-done",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("connect.CodeOf(err) = %v, want FailedPrecondition (err=%v)", connect.CodeOf(err), err)
	}
}

func TestInterruptAgentRunWritesInterruptRequest(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-interrupt", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-interrupt", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	sess, err := ms.CreateSession(context.Background(), "run-interrupt", "default", "Running", "")
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.InterruptAgentRun(actorContext("user-1", "", "", ""), &platform.InterruptAgentRunRequest{
		Namespace: "default",
		Name:      "run-interrupt",
	}); err != nil {
		t.Fatalf("InterruptAgentRun() error = %v", err)
	}

	merged := ms.mergedMetadata[sess.ID]["interrupt"]
	if len(merged) == 0 {
		t.Fatal("interrupt metadata not merged onto session")
	}
	var req struct {
		RequestedAt time.Time `json:"requested_at"`
		RequestedBy string    `json:"requested_by"`
	}
	if err := json.Unmarshal(merged, &req); err != nil {
		t.Fatalf("unmarshal interrupt request: %v", err)
	}
	if req.RequestedBy != "user-1" {
		t.Fatalf("RequestedBy = %q, want user-1", req.RequestedBy)
	}
	if req.RequestedAt.IsZero() {
		t.Fatal("RequestedAt is zero, want set")
	}
}

func TestInterruptAgentRunRejectsRunWithoutSession(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-interrupt-nosession", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-interrupt-nosession", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.InterruptAgentRun(actorContext("user-1", "", "", ""), &platform.InterruptAgentRunRequest{
		Namespace: "default",
		Name:      "run-interrupt-nosession",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("connect.CodeOf(err) = %v, want FailedPrecondition (err=%v)", connect.CodeOf(err), err)
	}
}

func TestInterruptAgentRunRejectsTerminalRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-interrupt-done", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseSucceeded,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-interrupt-done", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.InterruptAgentRun(actorContext("user-1", "", "", ""), &platform.InterruptAgentRunRequest{
		Namespace: "default",
		Name:      "run-interrupt-done",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("connect.CodeOf(err) = %v, want FailedPrecondition (err=%v)", connect.CodeOf(err), err)
	}
}
