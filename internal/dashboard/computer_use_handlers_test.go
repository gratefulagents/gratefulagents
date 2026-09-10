package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/computeruse"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestExchangeComputerUseAuthorizationAndRouting(t *testing.T) {
	for _, tc := range []struct {
		name, subject, role                          string
		recorded, shared, noStore, noOwner, wrongPod bool
		code                                         connect.Code
	}{
		{name: "internal", code: connect.CodeUnauthenticated},
		{name: "anonymous", recorded: true, code: connect.CodeUnauthenticated},
		{name: "viewer", recorded: true, subject: "alice", role: "viewer", code: connect.CodeNotFound},
		{name: "shared", recorded: true, subject: "bob", role: "member", shared: true, code: connect.CodeNotFound},
		{name: "nonowner", recorded: true, subject: "bob", role: "member", code: connect.CodeNotFound},
		{name: "owner", recorded: true, subject: "alice", role: "member"},
		{name: "admin", recorded: true, subject: "bob", role: "admin"},
		{name: "missing-store", recorded: true, subject: "bob", role: "admin", noStore: true, code: connect.CodeNotFound},
		{name: "missing-owner", recorded: true, subject: "bob", role: "admin", noOwner: true, code: connect.CodeNotFound},
		{name: "wrong-pod", recorded: true, subject: "alice", wrongPod: true, code: connect.CodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := platformv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns", UID: "run-uid"}, Status: platformv1alpha1.AgentRunStatus{Sandbox: &platformv1alpha1.AgentRunSandboxStatus{SandboxRef: &platformv1alpha1.NamedRef{Name: "actual-pod"}}}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "actual-pod", Namespace: "ns", Labels: map[string]string{"platform.gratefulagents.dev/owner-run": "run", "platform.gratefulagents.dev/owner-run-uid": "run-uid"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			if tc.wrongPod {
				pod.Labels["platform.gratefulagents.dev/owner-run-uid"] = "previous-run"
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/namespaces/ns/pods/actual-pod" {
					t.Error("wrong pod route")
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(pod)
			}))
			defer api.Close()
			config := &rest.Config{Host: api.URL}
			clientset, err := kubernetes.NewForConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			ms := newCollaborationStateStore()
			if !tc.noOwner {
				addCollaborationOwner(t, ms, "agent_run", "ns", "run", "alice")
			}
			if tc.shared {
				addCollaborationShare(ms, "share", "agent_run", "ns", "run", "bob", "alice", "collaborator")
			}
			srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build(), clientset: clientset, restConfig: config, stateStore: ms}
			if tc.noStore {
				srv.stateStore = nil
			}
			old := execComputerUse
			defer func() { execComputerUse = old }()
			calls := 0
			execComputerUse = func(ctx context.Context, c *kubernetes.Clientset, rc *rest.Config, pod, namespace string, input []byte) ([]byte, error) {
				calls++
				if pod != "actual-pod" || namespace != "ns" {
					t.Error("wrong exec target")
				}
				var e computeruse.Exchange
				if computeruse.Decode(bytes.NewReader(input), &e) != nil || e.Owner != tc.subject || e.Namespace != "ns" || e.Run != "run" {
					t.Error("identity not backend-bound")
				}
				if e.Operation == "claim" {
					return []byte(`{"active":false,"reason":"computer use request rejected","visionAvailable":true}`), nil
				}
				return []byte(`{"active":true,"visionAvailable":true}`), nil
			}
			ctx := context.Background()
			if tc.recorded {
				ctx = context.WithValue(ctx, requestActorContextKey{}, requestActor{Subject: tc.subject, Role: tc.role})
			}
			response, err := srv.ExchangeComputerUse(ctx, &platform.ExchangeComputerUseRequest{Namespace: "ns", Name: "run", SessionId: "session", Operation: "attach"})
			if tc.code != 0 {
				if connect.CodeOf(err) != tc.code || calls != 0 {
					t.Fatalf("code=%v calls=%d", connect.CodeOf(err), calls)
				}
			} else if err != nil || response.ResponseJson != `{"active":true,"visionAvailable":true}` || calls != 1 {
				t.Fatalf("response=%+v error=%v calls=%d", response, err, calls)
			}
			if tc.code == 0 {
				agentResponse, agentErr := srv.ExchangeComputerUse(ctx, &platform.ExchangeComputerUseRequest{Namespace: "ns", Name: "run", SessionId: "agent-session", Operation: "attach_agent"})
				if agentErr != nil || agentResponse == nil {
					t.Fatalf("agent attachment: %v", agentErr)
				}
				_, err := srv.ExchangeComputerUse(ctx, &platform.ExchangeComputerUseRequest{Namespace: "ns", Name: "run", SessionId: "session", Operation: "claim", RequestId: "expired"})
				if connect.CodeOf(err) != connect.CodeFailedPrecondition || calls != 3 {
					t.Fatalf("rejected claim: code=%v calls=%d", connect.CodeOf(err), calls)
				}
			}
		})
	}
}

func TestComputerUseExecFixedCommandAndBounds(t *testing.T) {
	called := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		q := r.URL.Query()
		command := q["command"]
		if len(command) != 2 || command[0] != "/opt/gratefulagents/bin/agent" || command[1] != "desktop-bridge" || q.Get("stdin") != "true" || q.Get("container") != "worker" || strings.Contains(r.URL.String(), "PRIVATE") {
			t.Error("unsafe exec request")
		}
		http.Error(w, "PRIVATE", http.StatusBadRequest)
	}))
	defer api.Close()
	config := &rest.Config{Host: api.URL}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = execComputerUseInPod(context.Background(), clientset, config, "pod", "ns", []byte("PRIVATE"))
	if !called || err == nil || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("transport leaked error or missed exec")
	}
	b := &computerUseBuffer{limit: 4}
	if _, err := b.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("PRIVATE")); err == nil || b.String() != "1234" {
		t.Fatal("unbounded output")
	}
}

func TestExchangeComputerUseLifecycleTransitions(t *testing.T) {
	for _, state := range []string{"succeeded", "failed", "cancelled", "deleting-run", "cancel-requested", "promote-succeeded", "terminating-pod", "pending-pod", "succeeded-pod", "failed-pod", "unknown-pod"} {
		t.Run(state, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := platformv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns", UID: "run-uid", Finalizers: []string{"test/finalizer"}}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning, Sandbox: &platformv1alpha1.AgentRunSandboxStatus{SandboxRef: &platformv1alpha1.NamedRef{Name: "pod"}}}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns", Labels: map[string]string{"platform.gratefulagents.dev/owner-run": "run", "platform.gratefulagents.dev/owner-run-uid": "run-uid"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			var currentPod atomic.Pointer[corev1.Pod]
			currentPod.Store(pod)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(currentPod.Load())
			}))
			defer api.Close()
			config := &rest.Config{Host: api.URL}
			clientset, err := kubernetes.NewForConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			ms := newCollaborationStateStore()
			addCollaborationOwner(t, ms, "agent_run", "ns", "run", "alice")
			k8s := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
			srv := &Server{k8sClient: k8s, clientset: clientset, restConfig: config, stateStore: ms}
			ctx := context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: "alice", Role: "member"})
			old := execComputerUse
			defer func() { execComputerUse = old }()
			calls := 0
			execComputerUse = func(context.Context, *kubernetes.Clientset, *rest.Config, string, string, []byte) ([]byte, error) {
				calls++
				return []byte(`{"active":true,"visionAvailable":true}`), nil
			}
			req := &platform.ExchangeComputerUseRequest{Namespace: "ns", Name: "run", SessionId: "session", Operation: "attach"}
			if _, err := srv.ExchangeComputerUse(ctx, req); err != nil || calls != 1 {
				t.Fatalf("live attach: %v calls=%d", err, calls)
			}
			if err := k8s.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
				t.Fatal(err)
			}
			nextPod := pod.DeepCopy()
			switch state {
			case "succeeded":
				run.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
			case "failed":
				run.Status.Phase = platformv1alpha1.AgentRunPhaseFailed
			case "cancelled":
				run.Status.Phase = platformv1alpha1.AgentRunPhaseCancelled
			case "deleting-run":
				if err := k8s.Delete(ctx, run); err != nil {
					t.Fatal(err)
				}
			case "cancel-requested", "promote-succeeded":
				key := cancelRequestedAnnotation
				if state == "promote-succeeded" {
					key = promoteSucceededAnnotation
				}
				run.Annotations = map[string]string{key: "requested"}
				if err := k8s.Update(ctx, run); err != nil {
					t.Fatal(err)
				}
			case "terminating-pod":
				now := metav1.Now()
				nextPod.DeletionTimestamp = &now
			case "pending-pod":
				nextPod.Status.Phase = corev1.PodPending
			case "succeeded-pod":
				nextPod.Status.Phase = corev1.PodSucceeded
			case "failed-pod":
				nextPod.Status.Phase = corev1.PodFailed
			case "unknown-pod":
				nextPod.Status.Phase = corev1.PodUnknown
			}
			if state == "succeeded" || state == "failed" || state == "cancelled" {
				if err := k8s.Status().Update(ctx, run); err != nil {
					t.Fatal(err)
				}
			}
			currentPod.Store(nextPod)
			for _, operation := range []string{"attach", "poll", "claim", "resolve"} {
				req.Operation, req.RequestId, req.OutcomeJson = operation, "", ""
				if operation == "claim" || operation == "resolve" {
					req.RequestId = "request"
				}
				if operation == "resolve" {
					req.OutcomeJson = `{"requestId":"request","status":"completed"}`
				}
				if _, err := srv.ExchangeComputerUse(ctx, req); connect.CodeOf(err) != connect.CodeFailedPrecondition || calls != 1 {
					t.Fatalf("%s: code=%v calls=%d", operation, connect.CodeOf(err), calls)
				}
			}
			req.Operation, req.RequestId, req.OutcomeJson = "stop", "", ""
			if _, err := srv.ExchangeComputerUse(ctx, req); err != nil || calls != 2 {
				t.Fatalf("best-effort stop: %v calls=%d", err, calls)
			}
		})
	}
}
