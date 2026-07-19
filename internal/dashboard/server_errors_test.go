package dashboard

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPodErrorEntriesKeepsOnlyErrorLines(t *testing.T) {
	logs := `2026-04-06T10:00:00.000000000Z Cloning https://github.com/acme/repo...
2026-04-06T10:00:01.000000000Z trace export completed
2026-04-06T10:00:02.000000000Z ERROR: setup failed: cloning repo: authentication required
2026-04-06T10:00:03.000000000Z {"level":"error","message":"provider retrying"}
2026-04-06T10:00:04.000000000Z request finished with 0 errors`

	entries := podErrorEntries(logs)
	if len(entries) != 2 {
		t.Fatalf("podErrorEntries() returned %d entries, want 2: %#v", len(entries), entries)
	}
	if entries[0].TimestampUnix == 0 || entries[0].Source != "pod" {
		t.Fatalf("first error = %#v", entries[0])
	}
	if got := entries[0].Message; got != "ERROR: setup failed: cloning repo: authentication required" {
		t.Fatalf("first message = %q", got)
	}
}

func TestGetAgentRunErrorsContinuesWhenDurableSessionIsMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-failed", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhaseFailed,
			LastError: "Postgres session client required: connection refused",
		},
	}
	state := newMockStateStore()
	state.getSessionByRunErr = fmt.Errorf("getting session by run: %w", pgx.ErrNoRows)
	srv := &Server{
		k8sClient:  fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build(),
		stateStore: state,
	}

	response, err := srv.GetAgentRunErrors(context.Background(), &platform.GetAgentRunErrorsRequest{
		Namespace: run.Namespace,
		Name:      run.Name,
	})
	if err != nil {
		t.Fatalf("GetAgentRunErrors() error = %v", err)
	}
	if len(response.Errors) != 1 || response.Errors[0].Source != "status" || response.Errors[0].Message != run.Status.LastError {
		t.Fatalf("Errors = %#v, want status fallback", response.Errors)
	}
}

func TestIsPodOwnedByAgentRunRejectsRepointedStatus(t *testing.T) {
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run-a", Namespace: "default", UID: "uid-a"}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other-pod", Namespace: "default",
			Labels: map[string]string{
				"platform.gratefulagents.dev/owner-run":     "run-b",
				"platform.gratefulagents.dev/owner-run-uid": "uid-b",
			},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker"}}},
	}
	if isPodOwnedByAgentRun(pod, run) {
		t.Fatal("pod owned by another run was accepted")
	}
	pod.Labels["platform.gratefulagents.dev/owner-run"] = run.Name
	if isPodOwnedByAgentRun(pod, run) {
		t.Fatal("same-name pod from an older run UID was accepted")
	}
	pod.Labels["platform.gratefulagents.dev/owner-run-uid"] = string(run.UID)
	if !isPodOwnedByAgentRun(pod, run) {
		t.Fatal("controller-labelled worker pod was rejected")
	}
}

func TestBoundedErrorMessagePreservesUTF8(t *testing.T) {
	message := strings.Repeat("a", maxAgentRunErrorBytes-2) + "🙂tail"
	got := boundedErrorMessage(message)
	if !utf8.ValidString(got) {
		t.Fatalf("boundedErrorMessage() returned invalid UTF-8")
	}
	if len(got) > maxAgentRunErrorBytes {
		t.Fatalf("boundedErrorMessage() length = %d, want <= %d", len(got), maxAgentRunErrorBytes)
	}
}

func TestIsErrorContentEventIncludesRecoveredFailures(t *testing.T) {
	tests := []struct {
		name  string
		event agent.ContentEvent
		want  bool
	}{
		{name: "tool error", event: agent.ContentEvent{Type: "tool_result", IsError: true}, want: true},
		{name: "failed attempt that will retry", event: agent.ContentEvent{Type: "llm_attempt", AttemptStatus: "failed", FailureKind: "rate_limit"}, want: true},
		{name: "successful trace", event: agent.ContentEvent{Type: "llm_attempt", AttemptStatus: "success"}, want: false},
		{name: "ordinary output", event: agent.ContentEvent{Type: "tool_result", Output: "done"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isErrorContentEvent(&tt.event, tt.event.Type); got != tt.want {
				t.Fatalf("isErrorContentEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}
