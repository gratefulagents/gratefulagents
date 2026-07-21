package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func TestTrimAgentRunLogTail(t *testing.T) {
	got, truncated := trimAgentRunLogTail("one\ntwo\nthree\n", 2)
	if got != "two\nthree\n" {
		t.Fatalf("trimAgentRunLogTail() content = %q", got)
	}
	if !truncated {
		t.Fatal("trimAgentRunLogTail() truncated = false, want true")
	}

	got, truncated = trimAgentRunLogTail("one\ntwo", 2)
	if got != "one\ntwo" || truncated {
		t.Fatalf("trimAgentRunLogTail() = %q, %v; want unchanged", got, truncated)
	}
}

func TestReadBoundedLogSuffixKeepsNewestCompleteLines(t *testing.T) {
	got, truncated, err := readBoundedLogSuffix(strings.NewReader("first\nsecond\nthird\n"), 12)
	if err != nil {
		t.Fatalf("readBoundedLogSuffix() error = %v", err)
	}
	if got != "third\n" || !truncated {
		t.Fatalf("readBoundedLogSuffix() = %q, %v; want newest complete line", got, truncated)
	}
}

func TestIsWorkerContainerWaiting(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name:  "worker",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
	}}}}
	if !isWorkerContainerWaiting(pod) {
		t.Fatal("isWorkerContainerWaiting() = false, want true")
	}
}

func TestGetAgentRunLogsReturnsBoundedOwnedWorkerOutput(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "active", Namespace: "default", UID: "run-uid"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "active-worker"},
			},
		},
	}
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "active-worker", Namespace: "default",
			Labels: map[string]string{
				"platform.gratefulagents.dev/owner-run":     run.Name,
				"platform.gratefulagents.dev/owner-run-uid": string(run.UID),
			},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker"}}},
	}

	api := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/log") {
			if request.URL.Query().Get("container") != "worker" || request.URL.Query().Get("tailLines") != "3" {
				t.Errorf("unexpected log query: %s", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte("first\nsecond\nthird\n"))
			return
		}
		response.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(response).Encode(pod); err != nil {
			t.Errorf("encoding pod: %v", err)
		}
	}))
	defer api.Close()
	clientset, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatalf("NewForConfig() error = %v", err)
	}
	srv := &Server{
		k8sClient: fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build(),
		clientset: clientset,
	}

	response, err := srv.GetAgentRunLogs(context.Background(), &platform.GetAgentRunLogsRequest{
		Namespace: run.Namespace,
		Name:      run.Name,
		TailLines: 2,
	})
	if err != nil {
		t.Fatalf("GetAgentRunLogs() error = %v", err)
	}
	if !response.Available || response.PodName != pod.Name {
		t.Fatalf("GetAgentRunLogs() = %#v, want available worker logs", response)
	}
	if response.Content != "second\nthird\n" || !response.Truncated {
		t.Fatalf("GetAgentRunLogs() content = %q, truncated = %v", response.Content, response.Truncated)
	}
}

func TestGetAgentRunLogsReportsUnavailableWithoutWorkerPod(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "completed", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseSucceeded,
		},
	}
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()}

	response, err := srv.GetAgentRunLogs(context.Background(), &platform.GetAgentRunLogsRequest{
		Namespace: run.Namespace,
		Name:      run.Name,
	})
	if err != nil {
		t.Fatalf("GetAgentRunLogs() error = %v", err)
	}
	if response.Available || response.Content != "" {
		t.Fatalf("GetAgentRunLogs() = %#v, want unavailable empty response", response)
	}
	if !response.IsComplete {
		t.Fatal("GetAgentRunLogs() IsComplete = false, want true")
	}
}
