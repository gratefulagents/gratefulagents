package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func TestDeriveJaegerQueryURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"jaeger.jaeger.svc:4317", "http://jaeger.jaeger.svc:16686"},
		{"http://jaeger.jaeger.svc:4317", "http://jaeger.jaeger.svc:16686"},
		{"https://jaeger.jaeger.svc:4317/", "http://jaeger.jaeger.svc:16686"},
		{"jaeger.jaeger.svc", "http://jaeger.jaeger.svc:16686"},
		{"", ""},
		{"  ", ""},
		{"http://host:4317/v1/traces", "http://host:16686"},
	}
	for _, tt := range tests {
		if got := deriveJaegerQueryURL(tt.in); got != tt.want {
			t.Errorf("deriveJaegerQueryURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

const cannedJaegerTrace = `{
  "data": [
    {
      "traceID": "abc123",
      "spans": [
        {
          "traceID": "abc123",
          "spanID": "s2",
          "operationName": "tool_call",
          "references": [{"refType": "CHILD_OF", "traceID": "abc123", "spanID": "s1"}],
          "startTime": 1100000,
          "duration": 100000,
          "tags": [{"key": "error", "type": "bool", "value": true}],
          "processID": "p1"
        },
        {
          "traceID": "abc123",
          "spanID": "s1",
          "operationName": "agent_run",
          "references": [],
          "startTime": 1000000,
          "duration": 500000,
          "tags": [{"key": "otel.status_code", "type": "string", "value": "ERROR"}],
          "processID": "p2"
        },
        {
          "traceID": "abc123",
          "spanID": "s3",
          "operationName": "generation",
          "references": [{"refType": "CHILD_OF", "traceID": "abc123", "spanID": "s1"}],
          "startTime": 1200000,
          "duration": 200000,
          "tags": [{"key": "gen.success", "type": "bool", "value": false}],
          "processID": "p1"
        },
        {
          "traceID": "abc123",
          "spanID": "s4",
          "operationName": "generation",
          "references": [{"refType": "CHILD_OF", "traceID": "abc123", "spanID": "s1"}],
          "startTime": 1300000,
          "duration": 100000,
          "tags": [{"key": "gen.success", "type": "bool", "value": true}],
          "processID": "p1"
        }
      ],
      "processes": {
        "p1": {"serviceName": "other-svc"},
        "p2": {"serviceName": "gratefulagents-agent"}
      }
    }
  ]
}`

func TestFetchTraceConvertsJaegerResponse(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(cannedJaegerTrace)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	jc := &jaegerClient{baseURL: srv.URL, httpClient: srv.Client()}
	resp, err := jc.FetchTrace("abc123")
	if err != nil {
		t.Fatalf("FetchTrace() error = %v", err)
	}
	if gotPath != "/api/traces/abc123" {
		t.Errorf("request path = %q, want /api/traces/abc123", gotPath)
	}
	if resp.TraceId != "abc123" {
		t.Errorf("TraceId = %q, want abc123", resp.TraceId)
	}
	if resp.ServiceName != "gratefulagents-agent" {
		t.Errorf("ServiceName = %q, want gratefulagents-agent (root span process)", resp.ServiceName)
	}
	if len(resp.Spans) != 4 {
		t.Fatalf("len(Spans) = %d, want 4", len(resp.Spans))
	}

	byID := map[string]*platform.TraceSpan{}
	for _, s := range resp.Spans {
		byID[s.SpanId] = s
	}
	root := byID["s1"]
	if root.ParentSpanId != "" {
		t.Errorf("root ParentSpanId = %q, want empty", root.ParentSpanId)
	}
	if root.StartTimeUnixUs != 1000000 || root.DurationUs != 500000 {
		t.Errorf("root start/duration = %d/%d, want 1000000/500000 (microseconds)", root.StartTimeUnixUs, root.DurationUs)
	}
	if byID["s2"].ParentSpanId != "s1" {
		t.Errorf("s2 ParentSpanId = %q, want s1", byID["s2"].ParentSpanId)
	}
	if root.ChildCount != 3 {
		t.Errorf("root ChildCount = %d, want 3", root.ChildCount)
	}

	// All three error conditions map to IsError; a successful span does not.
	if !byID["s2"].IsError {
		t.Error("s2 (error=true tag) IsError = false, want true")
	}
	if !byID["s1"].IsError {
		t.Error("s1 (otel.status_code=ERROR) IsError = false, want true")
	}
	if !byID["s3"].IsError {
		t.Error("s3 (gen.success=false) IsError = false, want true")
	}
	if byID["s4"].IsError {
		t.Error("s4 (gen.success=true) IsError = true, want false")
	}
}

func TestFetchTraceNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	jc := &jaegerClient{baseURL: srv.URL, httpClient: srv.Client()}
	resp, err := jc.FetchTrace("deadbeef")
	if err != nil {
		t.Fatalf("FetchTrace() error = %v", err)
	}
	if resp.TraceId != "deadbeef" {
		t.Errorf("TraceId = %q, want deadbeef", resp.TraceId)
	}
	if len(resp.Spans) != 0 {
		t.Errorf("len(Spans) = %d, want 0", len(resp.Spans))
	}
}

func TestFetchTraceRejectsInvalidTraceID(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer srv.Close()

	jc := &jaegerClient{baseURL: srv.URL, httpClient: srv.Client()}
	for _, id := range []string{"../x", strings.Repeat("a", 40), "", "abc123!"} {
		if _, err := jc.FetchTrace(id); err == nil {
			t.Errorf("FetchTrace(%q) error = nil, want error", id)
		}
	}
	if hit {
		t.Error("HTTP server was hit for invalid trace ID")
	}
}

func TestGetAgentTraceEmptyTraceIDTerminalRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-trace", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseSucceeded,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-trace", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{
		k8sClient:  c,
		scheme:     scheme,
		stateStore: ms,
		jaeger:     &jaegerClient{baseURL: "http://unused:16686", httpClient: http.DefaultClient},
	}

	resp, err := srv.GetAgentTrace(actorContext("user-1", "", "", ""), &platform.GetAgentTraceRequest{
		Namespace: "default",
		Name:      "run-trace",
	})
	if err != nil {
		t.Fatalf("GetAgentTrace() error = %v", err)
	}
	if !resp.IsComplete {
		t.Error("IsComplete = false for terminal run without trace ID, want true")
	}
}
