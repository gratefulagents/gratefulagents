package dashboard

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
	"unsafe"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type recordingStreamingHandlerConn struct {
	mu   sync.Mutex
	sent []*platform.AgentRun
	ch   chan *platform.AgentRun
}

func (c *recordingStreamingHandlerConn) Spec() connect.Spec           { return connect.Spec{} }
func (c *recordingStreamingHandlerConn) Peer() connect.Peer           { return connect.Peer{} }
func (c *recordingStreamingHandlerConn) Receive(any) error            { return errors.New("not implemented") }
func (c *recordingStreamingHandlerConn) RequestHeader() http.Header   { return http.Header{} }
func (c *recordingStreamingHandlerConn) ResponseHeader() http.Header  { return http.Header{} }
func (c *recordingStreamingHandlerConn) ResponseTrailer() http.Header { return http.Header{} }
func (c *recordingStreamingHandlerConn) Send(msg any) error {
	pb, ok := msg.(*platform.AgentRun)
	if !ok {
		return errors.New("unexpected message type")
	}
	clone := proto.Clone(pb).(*platform.AgentRun)
	c.mu.Lock()
	c.sent = append(c.sent, clone)
	c.mu.Unlock()
	if c.ch != nil {
		c.ch <- clone
	}
	return nil
}

func newAgentRunServerStream(conn connect.StreamingHandlerConn) *connect.ServerStream[platform.AgentRun] {
	stream := &connect.ServerStream[platform.AgentRun]{}
	streamPtr := (*struct{ Conn connect.StreamingHandlerConn })(unsafe.Pointer(stream))
	streamPtr.Conn = conn
	return stream
}

func TestWatchAgentRunSendsUpdateWhenConversationCountChangesWithoutResourceVersionChange(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-watch", Namespace: "default", ResourceVersion: "7"},
		Spec:       platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseRunning,
			CurrentStep: "implement",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-watch", "default", "running", "implement")
	ms.getMessagesBySession = map[uuid.UUID][]store.Message{
		sess.ID: {
			{ID: 1, SessionID: sess.ID, Role: "user", Content: "first prompt", CreatedAt: time.Unix(10, 0)},
			{ID: 2, SessionID: sess.ID, Role: "assistant", Content: "ack", CreatedAt: time.Unix(11, 0)},
		},
	}

	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}
	conn := &recordingStreamingHandlerConn{ch: make(chan *platform.AgentRun, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.WatchAgentRun(ctx, &platform.WatchAgentRunRequest{Namespace: "default", Name: "run-watch"}, newAgentRunServerStream(conn))
	}()

	first := <-conn.ch
	if got := len(first.Conversation); got != 2 {
		t.Fatalf("first conversation len = %d, want 2", got)
	}
	if got := first.Conversation[0].Content; got != "first prompt" {
		t.Fatalf("first conversation message = %q, want first prompt", got)
	}

	ms.mu.Lock()
	ms.getMessagesBySession[sess.ID] = append(ms.getMessagesBySession[sess.ID], store.Message{ID: 3, SessionID: sess.ID, Role: "user", Content: "follow up", CreatedAt: time.Unix(12, 0)})
	ms.mu.Unlock()

	second := <-conn.ch
	if got := len(second.Conversation); got != 3 {
		t.Fatalf("second conversation len = %d, want 3", got)
	}
	if got := second.Conversation[2].Content; got != "follow up" {
		t.Fatalf("new conversation message = %q, want follow up", got)
	}

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) && err != nil {
		t.Fatalf("WatchAgentRun() error = %v, want nil or context.Canceled", err)
	}
}

func TestGetActivityLogPodFallbackStaysIncompleteForTerminalRunWithoutFinalArtifact(t *testing.T) {
	scheme := newDashboardTestScheme(t)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-stream-log", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository:   platformv1alpha1.RepositoryContext{BaseBranch: "main"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:   platformv1alpha1.AgentRunPhaseFailed,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-log"}},
		},
	}

	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, podName, namespace string, _ []string) (string, error) {
		if podName != "sandbox-log" || namespace != "default" {
			t.Fatalf("execInPodFunc pod/namespace = %s/%s", namespace, podName)
		}
		return `{"message":"pod fallback"}` + "\n", nil
	}
	defer func() { execInPodFunc = origExec }()

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{k8sClient: c, scheme: scheme, clientset: &kubernetes.Clientset{}, restConfig: &rest.Config{}}

	resp, err := srv.GetActivityLog(context.Background(), &platform.GetActivityLogRequest{Namespace: "default", Name: "run-stream-log"})
	if err != nil {
		t.Fatalf("GetActivityLog() error = %v", err)
	}
	if resp.IsComplete {
		t.Fatal("IsComplete = true, want false when terminal run has only pod fallback")
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Message != "pod fallback" {
		t.Fatalf("Entries = %#v", resp.Entries)
	}
}
