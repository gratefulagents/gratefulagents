package main

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
)

// The progress loop must not rewrite unchanged metrics every tick: each
// write is a session UPDATE + NOTIFY that wakes this pod's own pollers. Only
// changed payloads, failed-write retries, and the final write go through.
func TestProgressMetricsGateSkipsUnchangedWrites(t *testing.T) {
	var gate progressMetricsGate
	first := sessionclient.SessionMetrics{CostUSD: 0.5, InputTokens: 100, OutputTokens: 20, ToolCallCount: 3}

	if !gate.shouldWrite(first, false) {
		t.Fatal("first write must go through")
	}
	gate.recordWritten(first)
	if gate.shouldWrite(first, false) {
		t.Fatal("identical metrics must be skipped")
	}
	if !gate.shouldWrite(first, true) {
		t.Fatal("final write must go through even when unchanged")
	}

	changed := first
	changed.ContextTokens = 4096
	if !gate.shouldWrite(changed, false) {
		t.Fatal("changed context usage must be written")
	}
	// A failed write is not recorded, so the same payload is retried.
	if !gate.shouldWrite(changed, false) {
		t.Fatal("unrecorded (failed) write must be retried")
	}
	gate.recordWritten(changed)
	if gate.shouldWrite(changed, false) {
		t.Fatal("recorded metrics must be skipped")
	}
}

// TestWriteResultToStatusKeepsPublishedEventsLog: a pod that did not upload
// an events log must not erase the URL an earlier pod of the run published.
func TestWriteResultToStatusKeepsPublishedEventsLog(t *testing.T) {
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns"}}
	run.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{EventsLogURL: "s3://bucket/events.jsonl"}
	c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).WithObjects(run).WithStatusSubresource(run).Build()
	if err := writeResultToStatus(context.Background(), c, "run", "ns", runResult{}, ""); err != nil {
		t.Fatal(err)
	}
	got := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run", Namespace: "ns"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Artifacts == nil || got.Status.Artifacts.EventsLogURL != "s3://bucket/events.jsonl" {
		t.Fatalf("events log URL = %+v, want preserved", got.Status.Artifacts)
	}
}
