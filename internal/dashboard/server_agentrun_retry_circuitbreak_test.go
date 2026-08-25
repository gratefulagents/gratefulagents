package dashboard

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// TestRetryAgentRunResumesCircuitBreakBlockedRun verifies that a run parked
// in Blocked by a circuit breaker (e.g. provider outage) is retryable: the
// retry is delivered as a session message to the live pod instead of failing
// with FailedPrecondition or bouncing compute through the wake machinery.
func TestRetryAgentRunResumesCircuitBreakBlockedRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-cb", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseBlocked,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, err := ms.CreateSession(context.Background(), "run-cb", "default", "Blocked", "auto")
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := ms.SetPendingQuestion(context.Background(), sess.ID, "Blocked", "provider outage", string(platformv1alpha1.UserInputCircuitBreak)); err != nil {
		t.Fatalf("SetPendingQuestion() error = %v", err)
	}
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-cb", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.RetryAgentRun(actorContext("user-1", "", "", ""), &platform.RetryAgentRunRequest{
		Namespace: "default",
		Name:      "run-cb",
	})
	if err != nil {
		t.Fatalf("RetryAgentRun() error = %v", err)
	}
	if resp.Name != "run-cb" {
		t.Fatalf("response name = %q, want run-cb", resp.Name)
	}

	// The retry must not bounce the healthy pod through wake machinery.
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-cb"}, updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Spec.WakeRequests != 0 {
		t.Fatalf("wakeRequests = %d, want 0 (message-post path)", updated.Spec.WakeRequests)
	}

	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 {
		t.Fatalf("messages appended = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "user" || !strings.Contains(msgs[0].Content, "Retry requested") {
		t.Fatalf("message = %q role %q, want default retry user message", msgs[0].Content, msgs[0].Role)
	}

	refreshed, err := ms.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if refreshed.PendingInputType != "" {
		t.Fatalf("PendingInputType = %q, want cleared", refreshed.PendingInputType)
	}
}

// TestRetryAgentRunStillRejectsNonCircuitBreakBlockedRun verifies that a run
// blocked on ordinary user input (a question) keeps the FailedPrecondition
// contract — retry is only widened for circuit-break parking.
func TestRetryAgentRunStillRejectsNonCircuitBreakBlockedRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-blocked", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseBlocked,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, err := ms.CreateSession(context.Background(), "run-blocked", "default", "Blocked", "auto")
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := ms.SetPendingQuestion(context.Background(), sess.ID, "Blocked", "which option?", string(platformv1alpha1.UserInputQuestion)); err != nil {
		t.Fatalf("SetPendingQuestion() error = %v", err)
	}
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-blocked", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err = srv.RetryAgentRun(actorContext("user-1", "", "", ""), &platform.RetryAgentRunRequest{
		Namespace: "default",
		Name:      "run-blocked",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("connect.CodeOf(err) = %v, want FailedPrecondition (err=%v)", connect.CodeOf(err), err)
	}
	if msgs := ms.messagesFor(sess.ID); len(msgs) != 0 {
		t.Fatalf("messages appended = %d, want 0", len(msgs))
	}
}
