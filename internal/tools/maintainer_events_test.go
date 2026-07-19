package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

type maintainerFakeRunner struct {
	out   map[string]string
	err   map[string]error
	calls []string
}

func (r *maintainerFakeRunner) RunGH(_ context.Context, _ string, args ...string) (string, error) {
	key := strings.Join(args, " ")
	r.calls = append(r.calls, key)
	return r.out[key], r.err[key]
}

func (r *maintainerFakeRunner) RunGHWithInput(_ context.Context, _ string, _ string, args ...string) (string, error) {
	return r.RunGH(context.Background(), "", args...)
}

func maintainerTestGitRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMaintainerBacklogFingerprintAndCursorRoundTrip(t *testing.T) {
	issues := []maintainerBacklogIssue{
		{Number: 2, UpdatedAt: "2026-01-02T00:00:00Z", Labels: []maintainerBacklogLabel{{Name: "z"}, {Name: "a"}}},
		{Number: 1, UpdatedAt: "2026-01-01T00:00:00Z", Labels: []maintainerBacklogLabel{{Name: "bug"}}},
	}
	baseline := maintainerBacklogSnapshot(issues)
	reordered := maintainerBacklogSnapshot([]maintainerBacklogIssue{
		{Number: 1, UpdatedAt: "2026-01-01T00:00:00Z", Labels: []maintainerBacklogLabel{{Name: "bug"}}},
		{Number: 2, UpdatedAt: "2026-01-02T00:00:00Z", Labels: []maintainerBacklogLabel{{Name: "a"}, {Name: "z"}}},
	})
	if baseline.backlogFingerprint != reordered.backlogFingerprint {
		t.Fatalf("fingerprint changed after reordering: %q != %q", baseline.backlogFingerprint, reordered.backlogFingerprint)
	}
	updated := maintainerBacklogSnapshot([]maintainerBacklogIssue{{Number: 1, UpdatedAt: "2026-01-03T00:00:00Z"}})
	if baseline.backlogFingerprint == updated.backlogFingerprint {
		t.Fatal("fingerprint did not change for an updated issue")
	}
	want := maintainerRepoEventsCursor{
		BacklogFingerprint: baseline.backlogFingerprint,
		IssueSignatures:    baseline.issueSignatures,
		FleetSignatures:    map[string]string{"run": "signature"},
	}
	encoded, err := encodeMaintainerRepoEventsCursor(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeMaintainerRepoEventsCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cursor round trip = %#v, want %#v", got, want)
	}
}

func TestWaitForRepoEventsReturnsCursorDeltaWithoutSleeping(t *testing.T) {
	maintainer := maintainerRun()
	run := fleetRun("implementer", platformv1alpha1.AgentRunPhaseRunning)
	base, _, stateStore := newMaintainerToolBase(t, maintainer, run)
	stateStore.sessions["default/implementer"] = &store.Session{AgentRunName: "implementer", AgentRunNS: "default"}
	runner := &maintainerFakeRunner{out: map[string]string{
		"issue list --state open --json number,title,labels,updatedAt,url --limit 200": `[{"number":4,"title":"new work","labels":[{"name":"bug"}],"updatedAt":"2026-01-04T00:00:00Z","url":"https://example.test/issues/4"}]`,
	}}
	tool := &waitForRepoEventsTool{
		maintainerToolBase: base, runner: runner, backlogPollInterval: time.Hour, fleetPollInterval: time.Hour,
	}
	fleet, err := tool.fleetEventsSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := encodeMaintainerRepoEventsCursor(maintainerRepoEventsCursor{
		BacklogFingerprint: maintainerBacklogSnapshot(nil).backlogFingerprint,
		IssueSignatures:    map[string]string{},
		FleetSignatures:    fleet.fleetSignatures,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"timeout_seconds":30,"cursor":"`+cursor+`"}`), maintainerTestGitRepoDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("Execute() error result: %s", result.Content)
	}
	var output waitForRepoEventsOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatal(err)
	}
	if !output.Changed || !output.BacklogChanged || len(output.ChangedIssues) != 1 || output.ChangedIssues[0].Number != 4 {
		t.Fatalf("output = %#v", output)
	}
	if _, err := decodeMaintainerRepoEventsCursor(output.Cursor); err != nil {
		t.Fatalf("returned cursor: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("gh calls = %v", runner.calls)
	}
}

func TestFleetEventConditionIncludesPendingInput(t *testing.T) {
	maintainer := maintainerRun()
	run := fleetRun("implementer", platformv1alpha1.AgentRunPhaseRunning)
	base, _, stateStore := newMaintainerToolBase(t, maintainer, run)
	stateStore.sessions["default/implementer"] = &store.Session{AgentRunName: "implementer", AgentRunNS: "default", PendingInputType: "question", PendingRequestID: "request"}
	tool := &waitForRepoEventsTool{maintainerToolBase: base}
	snapshot, err := tool.fleetEventsSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	event := snapshot.fleet["implementer"]
	if !event.PendingInput || snapshot.fleetSignatures["implementer"] == "" {
		t.Fatalf("fleet event = %#v, signatures = %#v", event, snapshot.fleetSignatures)
	}
}

func TestWaitForRepoEventsFirstCallReturnsCurrentStateImmediately(t *testing.T) {
	maintainer := maintainerRun()
	run := fleetRun("implementer", platformv1alpha1.AgentRunPhaseSucceeded)
	base, _, stateStore := newMaintainerToolBase(t, maintainer, run)
	stateStore.sessions["default/implementer"] = &store.Session{AgentRunName: "implementer", AgentRunNS: "default"}
	runner := &maintainerFakeRunner{out: map[string]string{
		"issue list --state open --json number,title,labels,updatedAt,url --limit 200": `[{"number":9,"title":"open work","labels":[{"name":"autopilot"}],"updatedAt":"2026-01-09T00:00:00Z","url":"https://example.test/issues/9"}]`,
	}}
	tool := &waitForRepoEventsTool{
		maintainerToolBase: base, runner: runner, backlogPollInterval: time.Hour, fleetPollInterval: time.Hour,
	}
	started := time.Now()
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"timeout_seconds":21600}`), maintainerTestGitRepoDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("Execute() error result: %s", result.Content)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("first call blocked for %v; must return immediately", elapsed)
	}
	var output waitForRepoEventsOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatal(err)
	}
	if !output.Changed || output.TimedOut {
		t.Fatalf("output = %#v", output)
	}
	if len(output.ChangedIssues) != 1 || output.ChangedIssues[0].Number != 9 {
		t.Fatalf("changed issues = %#v", output.ChangedIssues)
	}
	if len(output.FleetChanges) != 1 || output.FleetChanges[0].Name != "implementer" {
		t.Fatalf("fleet changes = %#v", output.FleetChanges)
	}
	if _, err := decodeMaintainerRepoEventsCursor(output.Cursor); err != nil {
		t.Fatalf("returned cursor: %v", err)
	}
}

func TestRepoEventsCursorAcknowledgesOnlyEmittedIssues(t *testing.T) {
	issues := make([]maintainerBacklogIssue, 0, 40)
	for i := 1; i <= 40; i++ {
		issues = append(issues, maintainerBacklogIssue{
			Number: i, Title: "issue", UpdatedAt: "2026-01-01T00:00:00Z", URL: "https://example.test/issues/" + strconv.Itoa(i),
		})
	}
	current := maintainerBacklogSnapshot(issues)

	// Emit only the first 25 issues, as a cap or byte-budget trim would.
	emitted := append([]maintainerRepoEventIssue(nil), current.issues[:25]...)
	cursor := repoEventsCursorForEmitted(maintainerRepoEventsSnapshot{}, current, emitted, nil)
	if cursor.BacklogFingerprint == current.backlogFingerprint {
		t.Fatal("suppressed issues must keep the cursor distinct from the live snapshot")
	}
	if len(cursor.IssueSignatures) != 25 {
		t.Fatalf("acknowledged signatures = %d, want 25", len(cursor.IssueSignatures))
	}

	// A follow-up call with that cursor must surface the remaining issues.
	previous := snapshotFromMaintainerRepoEventsCursor(cursor)
	if !repoEventsChanged(previous, current) {
		t.Fatal("remaining backlog must register as a pending change")
	}
	remaining := changedRepoEventIssues(previous, current)
	if len(remaining) != 15 {
		t.Fatalf("remaining issues = %d, want 15", len(remaining))
	}
	for _, issue := range remaining {
		if issue.Number <= 25 {
			t.Fatalf("issue %d was already acknowledged", issue.Number)
		}
	}

	// Once everything is emitted the cursor converges with the live snapshot
	// and a further wait blocks instead of spinning.
	fullCursor := repoEventsCursorForEmitted(previous, current, remaining, nil)
	if fullCursor.BacklogFingerprint != current.backlogFingerprint {
		t.Fatal("fully acknowledged cursor must match the live fingerprint")
	}
	if repoEventsChanged(snapshotFromMaintainerRepoEventsCursor(fullCursor), current) {
		t.Fatal("fully acknowledged cursor must not report a change")
	}
}

func TestWaitForRepoEventsFirstCallReturnsBacklogsBeyondThirtyIssues(t *testing.T) {
	maintainer := maintainerRun()
	base, _, _ := newMaintainerToolBase(t, maintainer)
	entries := make([]string, 0, 35)
	for i := 1; i <= 35; i++ {
		entries = append(entries, `{"number":`+strconv.Itoa(i)+`,"title":"work","labels":[],"updatedAt":"2026-01-01T00:00:00Z","url":"https://example.test/issues/`+strconv.Itoa(i)+`"}`)
	}
	runner := &maintainerFakeRunner{out: map[string]string{
		"issue list --state open --json number,title,labels,updatedAt,url --limit 200": "[" + strings.Join(entries, ",") + "]",
	}}
	tool := &waitForRepoEventsTool{
		maintainerToolBase: base, runner: runner, backlogPollInterval: time.Hour, fleetPollInterval: time.Hour,
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"timeout_seconds":21600}`), maintainerTestGitRepoDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("Execute() error result: %s", result.Content)
	}
	var output waitForRepoEventsOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.ChangedIssues) != 35 {
		t.Fatalf("changed issues = %d, want all 35", len(output.ChangedIssues))
	}
	cursor, err := decodeMaintainerRepoEventsCursor(output.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(cursor.IssueSignatures) != 35 {
		t.Fatalf("cursor signatures = %d, want 35", len(cursor.IssueSignatures))
	}
}
