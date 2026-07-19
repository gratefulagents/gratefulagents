package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"unsafe"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func newArtifactsTestServer(t *testing.T, run *platformv1alpha1.AgentRun) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	return &Server{
		k8sClient:  c,
		scheme:     scheme,
		clientset:  &kubernetes.Clientset{},
		restConfig: &rest.Config{},
	}
}

func runningRunWithSandbox(name, sandbox string) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{BaseBranch: "main"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:   platformv1alpha1.AgentRunPhaseRunning,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{SandboxRef: &platformv1alpha1.NamedRef{Name: sandbox}},
		},
	}
}

// newTypedServerStream builds a ServerStream around a StreamingHandlerConn,
// mirroring newAgentRunServerStream for other response types.
func newTypedServerStream[T any](conn connect.StreamingHandlerConn) *connect.ServerStream[T] {
	stream := &connect.ServerStream[T]{}
	streamPtr := (*struct{ Conn connect.StreamingHandlerConn })(unsafe.Pointer(stream))
	streamPtr.Conn = conn
	return stream
}

type refusingStreamingConn struct{}

func (refusingStreamingConn) Spec() connect.Spec           { return connect.Spec{} }
func (refusingStreamingConn) Peer() connect.Peer           { return connect.Peer{} }
func (refusingStreamingConn) Receive(any) error            { return errors.New("not implemented") }
func (refusingStreamingConn) RequestHeader() http.Header   { return http.Header{} }
func (refusingStreamingConn) ResponseHeader() http.Header  { return http.Header{} }
func (refusingStreamingConn) ResponseTrailer() http.Header { return http.Header{} }
func (refusingStreamingConn) Send(any) error               { return errors.New("must not send") }

func TestGetDiffCoalescesPodExecsAcrossCallers(t *testing.T) {
	srv := newArtifactsTestServer(t, runningRunWithSandbox("run-diff", "sandbox-diff"))

	var execs atomic.Int32
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, podName, _ string, _ []string) (string, error) {
		if podName != "sandbox-diff" {
			t.Errorf("exec pod = %q, want sandbox-diff", podName)
		}
		execs.Add(1)
		return "diff --git a/x b/x", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	req := &platform.GetDiffRequest{Namespace: "default", Name: "run-diff"}
	first, err := srv.GetDiff(context.Background(), req)
	if err != nil {
		t.Fatalf("GetDiff #1: %v", err)
	}
	second, err := srv.GetDiff(context.Background(), req)
	if err != nil {
		t.Fatalf("GetDiff #2: %v", err)
	}
	if first.Diff != "diff --git a/x b/x" || first.Source != "pod" {
		t.Errorf("first = (%q, %q), want pod diff", first.Diff, first.Source)
	}
	if second.Diff != first.Diff {
		t.Errorf("second diff = %q, want cached %q", second.Diff, first.Diff)
	}
	if got := execs.Load(); got != 2 {
		t.Errorf("pod execs = %d, want 2 (one diff and one path-only new-file listing, coalesced within probeDiffTTL)", got)
	}
}

func TestGetDiffCacheIsPerRepoPath(t *testing.T) {
	srv := newArtifactsTestServer(t, runningRunWithSandbox("run-diff", "sandbox-diff"))

	var execs atomic.Int32
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _, _ string, command []string) (string, error) {
		execs.Add(1)
		return fmt.Sprintf("exec-%d %v", execs.Load(), command[len(command)-1]), nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	if _, err := srv.GetDiff(context.Background(), &platform.GetDiffRequest{Namespace: "default", Name: "run-diff"}); err != nil {
		t.Fatalf("GetDiff primary: %v", err)
	}
	if _, err := srv.GetDiff(context.Background(), &platform.GetDiffRequest{Namespace: "default", Name: "run-diff", RepoPath: "/workspace/repo/repos/sdk"}); err != nil {
		t.Fatalf("GetDiff extra repo: %v", err)
	}
	if got := execs.Load(); got != 4 {
		t.Errorf("pod execs = %d, want 4 (diff and new-file listing for each distinct repo path)", got)
	}
}

func TestWatchDiffDeniesStrangerBeforeAnyBuild(t *testing.T) {
	srv, ms := newAuthzTestServer(t, ownedRun("run-owned"))
	srv.clientset = &kubernetes.Clientset{}
	srv.restConfig = &rest.Config{}
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _, _ string, _ []string) (string, error) {
		t.Error("stranger's WatchDiff must not exec into the pod")
		return "", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	err := srv.WatchDiff(actorContext("mallory", "member", "", ""),
		&platform.GetDiffRequest{Namespace: "default", Name: "run-owned"},
		newTypedServerStream[platform.GetDiffResponse](refusingStreamingConn{}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("WatchDiff by stranger: want PermissionDenied, got %v", err)
	}
}

func TestGetAgentTraceCoalescesJaegerFetches(t *testing.T) {
	var hits atomic.Int32
	jaegerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[]}`)
	}))
	t.Cleanup(jaegerSrv.Close)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-trace", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhaseRunning,
			Artifacts: &platformv1alpha1.AgentRunArtifacts{TraceID: "abc123"},
		},
	}
	srv := newArtifactsTestServer(t, run)
	srv.jaeger = &jaegerClient{baseURL: jaegerSrv.URL, httpClient: jaegerSrv.Client()}

	req := &platform.GetAgentTraceRequest{Namespace: "default", Name: "run-trace"}
	first, err := srv.GetAgentTrace(context.Background(), req)
	if err != nil {
		t.Fatalf("GetAgentTrace #1: %v", err)
	}
	if _, err := srv.GetAgentTrace(context.Background(), req); err != nil {
		t.Fatalf("GetAgentTrace #2: %v", err)
	}
	if first.TraceId != "abc123" {
		t.Errorf("TraceId = %q, want abc123", first.TraceId)
	}
	if first.IsComplete {
		t.Error("IsComplete = true for running run, want false")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("jaeger fetches = %d, want 1 (coalesced within probeTraceTTL)", got)
	}
}

func TestWatchAgentTraceDeniesStrangerBeforeAnyFetch(t *testing.T) {
	var hits atomic.Int32
	jaegerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"data":[]}`)
	}))
	t.Cleanup(jaegerSrv.Close)

	srv, ms := newAuthzTestServer(t, ownedRun("run-owned"))
	srv.jaeger = &jaegerClient{baseURL: jaegerSrv.URL, httpClient: jaegerSrv.Client()}
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	err := srv.WatchAgentTrace(actorContext("mallory", "member", "", ""),
		&platform.GetAgentTraceRequest{Namespace: "default", Name: "run-owned"},
		newTypedServerStream[platform.GetAgentTraceResponse](refusingStreamingConn{}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("WatchAgentTrace by stranger: want PermissionDenied, got %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("jaeger fetches = %d, want 0", hits.Load())
	}
}
