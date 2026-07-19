package dashboard

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"google.golang.org/protobuf/encoding/protojson"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// exportJaegerTrace is a minimal Jaeger API payload with a single span.
const exportJaegerTrace = `{
  "data": [
    {
      "traceID": "abc123",
      "spans": [
        {
          "traceID": "abc123",
          "spanID": "s1",
          "operationName": "agent",
          "references": [],
          "startTime": 1000000,
          "duration": 500000,
          "tags": [],
          "processID": "p1"
        }
      ],
      "processes": {"p1": {"serviceName": "gratefulagents-agent"}}
    }
  ]
}`

func readZipArchive(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	files := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s in zip: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s in zip: %v", f.Name, err)
		}
		files[f.Name] = data
	}
	return files
}

func TestExportAgentRunArchiveLiveRun(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-live", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhaseRunning,
			Artifacts: &platformv1alpha1.AgentRunArtifacts{TraceID: "abc123"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()

	// Live activity comes from the Postgres tier.
	ms := newMockStateStore()
	sess, err := ms.CreateSession(context.Background(), "run-live", "default", "Running", "implement")
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	ms.allActivityBySession = map[uuid.UUID][]store.ActivityEvent{
		sess.ID: {
			{ID: 1, SessionID: sess.ID, EventType: "tool_start", Detail: json.RawMessage(`{"ts":"2026-07-05T10:00:00Z","type":"tool_start","session":1,"tool":"Bash","tool_use_id":"t1","input_raw":"{\"command\":\"ls\"}"}`)},
			{ID: 2, SessionID: sess.ID, EventType: "tool_end", Detail: json.RawMessage(`{"ts":"2026-07-05T10:00:01Z","type":"tool_end","session":1,"tool":"Bash","tool_use_id":"t1","output":"ok","tool_duration_ms":1000}`)},
		},
	}

	jaegerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(exportJaegerTrace))
	}))
	defer jaegerSrv.Close()

	srv := &Server{
		k8sClient:  c,
		scheme:     scheme,
		stateStore: ms,
		jaeger:     &jaegerClient{baseURL: jaegerSrv.URL, httpClient: jaegerSrv.Client()},
	}

	resp, err := srv.ExportAgentRunArchive(context.Background(), &platform.ExportAgentRunArchiveRequest{Namespace: "default", Name: "run-live"})
	if err != nil {
		t.Fatalf("ExportAgentRunArchive() error = %v", err)
	}
	if !strings.HasPrefix(resp.Filename, "run-live-export-") || !strings.HasSuffix(resp.Filename, ".zip") {
		t.Fatalf("Filename = %q, want run-live-export-*.zip", resp.Filename)
	}

	files := readZipArchive(t, resp.Archive)
	for _, name := range []string{exportReadmeName, exportRunName, exportActivityName, exportTraceName} {
		if _, ok := files[name]; !ok {
			t.Fatalf("archive missing %s (has %v)", name, keysOf(files))
		}
	}

	// run.json must round-trip into the AgentRun proto.
	var runPB platform.AgentRun
	if err := protojson.Unmarshal(files[exportRunName], &runPB); err != nil {
		t.Fatalf("unmarshal run.json: %v", err)
	}
	if runPB.Name != "run-live" || runPB.Namespace != "default" {
		t.Fatalf("run.json = %s/%s, want default/run-live", runPB.Namespace, runPB.Name)
	}

	// activity.jsonl: one protojson ActivityEntry per line.
	lines := splitNonEmptyLines(files[exportActivityName])
	if len(lines) != 2 {
		t.Fatalf("activity.jsonl has %d lines, want 2", len(lines))
	}
	var first platform.ActivityEntry
	if err := protojson.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("unmarshal activity line: %v", err)
	}
	if first.Tool != "Bash" || first.Type != "tool_use" {
		t.Fatalf("first entry = type %q tool %q, want tool_use/Bash", first.Type, first.Tool)
	}

	// trace.json: spans from Jaeger, incomplete because the run is live.
	var trace platform.GetAgentTraceResponse
	if err := protojson.Unmarshal(files[exportTraceName], &trace); err != nil {
		t.Fatalf("unmarshal trace.json: %v", err)
	}
	if len(trace.Spans) != 1 || trace.IsComplete {
		t.Fatalf("trace = %d spans, isComplete=%t; want 1 span, incomplete", len(trace.Spans), trace.IsComplete)
	}

	readme := string(files[exportReadmeName])
	if !strings.Contains(readme, "default/run-live") || !strings.Contains(readme, "still in progress") {
		t.Fatalf("README missing run id or in-progress note:\n%s", readme)
	}
}

func TestExportAgentRunArchiveTerminalRunWithoutTrace(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-done", Namespace: "default"},
		Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.ExportAgentRunArchive(context.Background(), &platform.ExportAgentRunArchiveRequest{Namespace: "default", Name: "run-done"})
	if err != nil {
		t.Fatalf("ExportAgentRunArchive() error = %v", err)
	}

	files := readZipArchive(t, resp.Archive)
	if _, ok := files[exportTraceName]; ok {
		t.Fatal("archive should not contain trace.json when Jaeger is unconfigured")
	}
	for _, name := range []string{exportReadmeName, exportRunName, exportActivityName} {
		if _, ok := files[name]; !ok {
			t.Fatalf("archive missing %s (has %v)", name, keysOf(files))
		}
	}

	readme := string(files[exportReadmeName])
	if !strings.Contains(readme, "Jaeger is not configured") {
		t.Fatalf("README missing Jaeger note:\n%s", readme)
	}
	if !strings.Contains(readme, "Complete:  true") {
		t.Fatalf("README should mark terminal run complete:\n%s", readme)
	}
}

func TestExportAgentRunArchiveNotFound(t *testing.T) {
	scheme := newDashboardTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.ExportAgentRunArchive(context.Background(), &platform.ExportAgentRunArchiveRequest{Namespace: "default", Name: "missing"})
	if err == nil {
		t.Fatal("expected error for missing AgentRun")
	}
}

func keysOf(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func splitNonEmptyLines(data []byte) []string {
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
