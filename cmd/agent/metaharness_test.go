package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

// Path contract: the store root is <workspace>/metaharness and the store
// itself nests traces/<run-id>. A duplicated .../traces/traces/... layout is
// a regression.
func TestNewMetaHarnessWriterPathContract(t *testing.T) {
	workspace := t.TempDir()
	cfg := runConfig{
		WorkspaceDir: workspace,
		TaskName:     "run-abc",
		Model:        "test-model",
		RepoDir:      filepath.Join(workspace, "repo"),
	}

	writer, traceDir := newMetaHarnessWriter(cfg, "auto")
	if writer == nil {
		t.Fatal("expected writer, got nil")
	}
	wantDir := filepath.Join(workspace, "metaharness", "traces", "run-abc")
	if resolved, err := filepath.EvalSymlinks(wantDir); err == nil {
		wantDir = resolved
	}
	gotDir := traceDir
	if resolved, err := filepath.EvalSymlinks(traceDir); err == nil {
		gotDir = resolved
	}
	if gotDir != wantDir {
		t.Fatalf("trace dir = %q, want %q", gotDir, wantDir)
	}
	if _, err := os.Stat(filepath.Join(traceDir, "metadata.json")); err != nil {
		t.Fatalf("metadata.json not found in trace dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "metaharness", "traces", "traces")); !os.IsNotExist(err) {
		t.Fatalf("duplicated traces/traces layout exists (err=%v)", err)
	}
}

// Structural spans recorded through the progress tracker must reach the
// Meta-Harness writer when the composed processor is installed.
func TestTrackerStructuralSpansReachMetaHarness(t *testing.T) {
	workspace := t.TempDir()
	cfg := runConfig{WorkspaceDir: workspace, TaskName: "run-spans"}
	writer, traceDir := newMetaHarnessWriter(cfg, "")
	if writer == nil {
		t.Fatal("expected writer, got nil")
	}

	tracker := agent.NewRunProgress()
	var tp agent.TracingProcessor = agent.NoOpTracingProcessor{}
	tp = &agent.MultiTracingProcessor{Processors: []agent.TracingProcessor{tp, writer}}
	tracker.SetTracingProcessor(tp)

	tracker.RecordAPIRetry("overloaded", 1000, 1, 5)
	tracker.RecordCompactBoundary(200000, 50000, "")

	data, err := os.ReadFile(filepath.Join(traceDir, "spans.jsonl"))
	if err != nil {
		t.Fatalf("reading spans.jsonl: %v", err)
	}
	spans := string(data)
	for _, want := range []string{"api.retry", "compaction"} {
		if !strings.Contains(spans, want) {
			t.Errorf("spans.jsonl missing %q span:\n%s", want, spans)
		}
	}
}

func TestMetaHarnessTraceObjectKey(t *testing.T) {
	now := time.Unix(0, 1234567890)
	key := metaHarnessTraceObjectKey("ns-a", "uid-b", now)
	want := "metaharness-traces/v1/ns-a/uid-b/1234567890.tar.gz.enc"
	if key != want {
		t.Fatalf("object key = %q, want %q", key, want)
	}
	if strings.HasPrefix(key, workspaceCheckpointStorePrefix) {
		t.Fatal("trace objects must not live under the workspace checkpoint prefix (checkpoint cleanup would delete them)")
	}
}

func TestUploadMetaHarnessTraceRoundTrip(t *testing.T) {
	traceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(traceDir, "metadata.json"), []byte(`{"run_id":"r"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(traceDir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(traceDir, "nested", "tool_calls.jsonl"), []byte("{\"tool\":\"x\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := newMemoryWorkspaceObjectStore()
	key, err := uploadMetaHarnessTrace(context.Background(), store, testWorkspaceSnapshotKey, traceDir, "test-ns", "test-uid")
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	if !strings.HasPrefix(key, "metaharness-traces/v1/test-ns/test-uid/") {
		t.Fatalf("object key %q is not tenant-scoped", key)
	}

	envelope, ok, err := store.Get(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("stored object missing: ok=%v err=%v", ok, err)
	}
	plaintext, err := decryptWorkspaceArchive(testWorkspaceSnapshotKey, envelope)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if bytes.Equal(plaintext, envelope) {
		t.Fatal("stored object was not encrypted")
	}

	zr, err := gzip.NewReader(bytes.NewReader(plaintext))
	if err != nil {
		t.Fatalf("gzip open: %v", err)
	}
	tr := tar.NewReader(zr)
	files := map[string]string{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar entry read: %v", err)
		}
		files[header.Name] = string(content)
	}
	if files["metadata.json"] != `{"run_id":"r"}` {
		t.Errorf("metadata.json content = %q", files["metadata.json"])
	}
	if files["nested/tool_calls.jsonl"] != "{\"tool\":\"x\"}\n" {
		t.Errorf("nested/tool_calls.jsonl content = %q", files["nested/tool_calls.jsonl"])
	}
}

func TestMetaHarnessEnabled(t *testing.T) {
	t.Setenv("ENABLE_METAHARNESS", "")
	if metaHarnessEnabled() {
		t.Fatal("must be disabled by default")
	}
	t.Setenv("ENABLE_METAHARNESS", "true")
	if !metaHarnessEnabled() {
		t.Fatal("expected enabled with ENABLE_METAHARNESS=true")
	}
	t.Setenv("ENABLE_METAHARNESS", "false")
	if metaHarnessEnabled() {
		t.Fatal("expected disabled with ENABLE_METAHARNESS=false")
	}
}
