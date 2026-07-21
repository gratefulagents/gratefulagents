package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type noWatchClient struct{ client.Client }

type gapSafeWatchClient struct {
	client.WithWatch
	resourceVersion string
	replay          runtime.Object
}

func (c *gapSafeWatchClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := c.WithWatch.List(ctx, list, opts...); err != nil {
		return err
	}
	list.SetResourceVersion(c.resourceVersion)
	return nil
}

func (c *gapSafeWatchClient) Watch(_ context.Context, _ client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
	options := &client.ListOptions{}
	for _, option := range opts {
		option.ApplyToList(options)
	}
	if options.Raw == nil || options.Raw.ResourceVersion != c.resourceVersion {
		return nil, errors.New("watch did not start at snapshot resourceVersion")
	}
	watcher := watch.NewRaceFreeFake()
	watcher.Add(c.replay)
	return watcher, nil
}

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
		BacklogFingerprint:    baseline.backlogFingerprint,
		IssueSignatures:       baseline.issueSignatures,
		FleetSignatures:       map[string]string{"run": "signature"},
		PullRequestSignatures: map[string]string{},
		WorkItemSignatures:    map[string]string{},
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
	cursor := repoEventsCursorForEmitted(maintainerRepoEventsSnapshot{}, current, emitted, nil, nil, nil)
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
	fullCursor := repoEventsCursorForEmitted(previous, current, remaining, nil, nil, nil)
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

const maintainerTestPullRequestURL = "https://github.com/octo/widgets/pull/7"

func maintainerPullRequestRunnerOutputs(checkStatus, conclusion, reviewDecision string) map[string]string {
	return map[string]string{
		"api repos/octo/widgets/pulls?state=open&per_page=100 --paginate":                                                 `[{"number":7}]`,
		"api repos/octo/widgets/pulls/7":                                                                                  `{"head":{"sha":"abc123"},"state":"OPEN","draft":false,"mergeable":true,"mergeable_state":"clean"}`,
		"api graphql -f query=" + maintainerPullRequestReviewDecisionQuery + " -f owner=octo -f repo=widgets -F number=7": `{"data":{"repository":{"pullRequest":{"reviewDecision":"` + reviewDecision + `"}}}}`,
		"api repos/octo/widgets/commits/abc123/check-runs --paginate":                                                     `{"check_runs":[{"name":"build","status":"` + checkStatus + `","conclusion":"` + conclusion + `"}]}`,
		"api repos/octo/widgets/commits/abc123/status":                                                                    `{"statuses":[]}`,
	}
}

func TestRepoEventsDetectsPullRequestCIPendingToPassed(t *testing.T) {
	runner := &maintainerFakeRunner{out: maintainerPullRequestRunnerOutputs("in_progress", "", "REVIEW_REQUIRED")}
	tool := &waitForRepoEventsTool{runner: runner}
	fleet := map[string]maintainerRepoFleetEvent{"implementer": {PullRequestURLs: []string{maintainerTestPullRequestURL}}}

	pending, err := tool.pullRequestEventsSnapshot(context.Background(), maintainerTestGitRepoDir(t), fleet)
	if err != nil {
		t.Fatal(err)
	}
	runner.out["api repos/octo/widgets/commits/abc123/check-runs --paginate"] = `{"check_runs":[{"name":"build","status":"completed","conclusion":"success"}]}`
	passed, err := tool.pullRequestEventsSnapshot(context.Background(), maintainerTestGitRepoDir(t), fleet)
	if err != nil {
		t.Fatal(err)
	}
	if !repoEventsChanged(pending, passed) {
		t.Fatal("CI pending-to-passed change did not wake the waiter")
	}
	changes := changedRepoPullRequestEvents(pending, passed)
	if len(changes) != 1 || changes[0].URL != maintainerTestPullRequestURL || changes[0].Checks.Pending != 0 || changes[0].Checks.Passed != 1 {
		t.Fatalf("pull request changes = %#v", changes)
	}
	runner.out["api graphql -f query="+maintainerPullRequestReviewDecisionQuery+" -f owner=octo -f repo=widgets -F number=7"] = `{"data":{"repository":{"pullRequest":{"reviewDecision":"APPROVED"}}}}`
	reviewed, err := tool.pullRequestEventsSnapshot(context.Background(), maintainerTestGitRepoDir(t), fleet)
	if err != nil {
		t.Fatal(err)
	}
	if !repoEventsChanged(passed, reviewed) || changedRepoPullRequestEvents(passed, reviewed)[0].ReviewDecision != "APPROVED" {
		t.Fatalf("review decision change = %#v", changedRepoPullRequestEvents(passed, reviewed))
	}
}

func TestRepoEventsIsolatesBadPullRequestAndContinuesMonitoringOthers(t *testing.T) {
	runner := &maintainerFakeRunner{out: maintainerPullRequestRunnerOutputs("completed", "success", "APPROVED")}
	tool := &waitForRepoEventsTool{runner: runner}
	fleet := map[string]maintainerRepoFleetEvent{
		"good": {PullRequestURLs: []string{maintainerTestPullRequestURL}},
		"bad":  {PullRequestURLs: []string{"https://example.test/not-a-pr"}},
	}

	snapshot, err := tool.pullRequestEventsSnapshot(context.Background(), maintainerTestGitRepoDir(t), fleet)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.pullRequests[maintainerTestPullRequestURL]; got.ReviewDecision != "APPROVED" || got.Checks.Passed != 1 {
		t.Fatalf("good pull request was not monitored: %#v", got)
	}
	bad := snapshot.pullRequests["https://example.test/not-a-pr"]
	if bad.Error == "" || snapshot.pullRequestError == "" || snapshot.pullRequestSignatures[bad.URL] == "" {
		t.Fatalf("bad pull request event/error = %#v / %q", bad, snapshot.pullRequestError)
	}
	previous := snapshot
	delete(fleet, "bad")
	recovered, err := tool.pullRequestEventsSnapshot(context.Background(), maintainerTestGitRepoDir(t), fleet)
	if err != nil {
		t.Fatal(err)
	}
	if !repoEventsChanged(previous, recovered) {
		t.Fatal("removing a failed pull request must wake the waiter")
	}
}

func TestRepoEventsDoesNotPollChecksForClosedHistoricalPullRequests(t *testing.T) {
	runner := &maintainerFakeRunner{out: map[string]string{
		"api repos/octo/widgets/pulls?state=open&per_page=100 --paginate": `[]`,
	}}
	tool := &waitForRepoEventsTool{runner: runner}
	fleet := map[string]maintainerRepoFleetEvent{"historical": {PullRequestURLs: []string{maintainerTestPullRequestURL}}}

	snapshot, err := tool.pullRequestEventsSnapshot(context.Background(), maintainerTestGitRepoDir(t), fleet)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.pullRequests[maintainerTestPullRequestURL]; got.State != "closed" || got.Error != "" {
		t.Fatalf("closed pull request = %#v", got)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("closed historical pull request made per-PR/check calls: %v", runner.calls)
	}
}

func TestParseMaintainerPullRequestURLMatchesArtifactNormalization(t *testing.T) {
	owner, repository, number, err := parseMaintainerPullRequestURL("HTTPS://www.github.com/Octo/Widgets/PULL/7/files")
	if err != nil || owner != "octo" || repository != "widgets" || number != 7 {
		t.Fatalf("parse = %q/%q#%d, %v", owner, repository, number, err)
	}
}

func TestFleetEventDetectsPRLoopStateAndRound(t *testing.T) {
	maintainer := maintainerRun()
	run := fleetRun("reviewer", platformv1alpha1.AgentRunPhaseRunning)
	run.Labels = map[string]string{maintainerPRLoopStateLabel: "reviewing"}
	run.Annotations = map[string]string{maintainerPRLoopRoundAnnotation: "1"}
	base, k8sClient, _ := newMaintainerToolBase(t, maintainer, run)
	tool := &waitForRepoEventsTool{maintainerToolBase: base}

	previous, err := tool.fleetEventsSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: run.Name, Namespace: run.Namespace}, updated); err != nil {
		t.Fatal(err)
	}
	updated.Labels[maintainerPRLoopStateLabel] = "approved"
	updated.Annotations[maintainerPRLoopRoundAnnotation] = "2"
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	current, err := tool.fleetEventsSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !repoEventsChanged(previous, current) {
		t.Fatal("PR-loop state change did not wake the waiter")
	}
	change := changedRepoFleetEvents(previous, current)
	if len(change) != 1 || change[0].PRLoopState != "approved" || change[0].PRLoopRound != "2" {
		t.Fatalf("fleet changes = %#v", change)
	}
}

func TestMaintainerRepoEventsCursorDecodesOlderSnapshots(t *testing.T) {
	legacy, err := json.Marshal(map[string]any{
		"backlog_fingerprint": "backlog",
		"issue_signatures":    map[string]string{"1": "issue"},
		"fleet_signatures":    map[string]string{"run": "fleet"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := decodeMaintainerRepoEventsCursor(base64.RawStdEncoding.EncodeToString(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if cursor.PullRequestSignatures == nil || len(cursor.PullRequestSignatures) != 0 {
		t.Fatalf("pull request signatures = %#v", cursor.PullRequestSignatures)
	}
	if cursor.WorkItemSignatures == nil || len(cursor.WorkItemSignatures) != 0 {
		t.Fatalf("work item signatures = %#v", cursor.WorkItemSignatures)
	}
}

func TestWorkItemWaitRejectsClientWithoutWatch(t *testing.T) {
	t.Parallel()

	base, k8sClient, _ := newMaintainerToolBase(t, maintainerRun())
	base.k8sClient = &noWatchClient{Client: k8sClient}
	_, watcher, err := (&waitForRepoEventsTool{maintainerToolBase: base}).workItemSnapshotAndWatch(context.Background())
	if err == nil || watcher != nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("watcher=%#v err=%v", watcher, err)
	}
}

func TestWorkItemCurrentSnapshotPlusWatchCannotLoseCreate(t *testing.T) {
	maintainer := maintainerRun()
	base, k8sClient, _ := newMaintainerToolBase(t, maintainer)
	watchClient, ok := k8sClient.(client.WithWatch)
	if !ok {
		t.Fatal("fake Kubernetes client does not support watch")
	}
	gapClient := &gapSafeWatchClient{WithWatch: watchClient, resourceVersion: "snapshot-rv"}
	base.k8sClient = gapClient
	tool := &waitForRepoEventsTool{maintainerToolBase: base}

	baseline, err := tool.workItemEventsSnapshot(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := encodeMaintainerRepoEventsCursor(maintainerRepoEventsCursor{
		IssueSignatures: map[string]string{}, FleetSignatures: map[string]string{}, WorkItemSignatures: baseline.workItemSignatures,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeMaintainerRepoEventsCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	previous := snapshotFromMaintainerRepoEventsCursor(decoded)
	item := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{
			Name: "issue-7", Namespace: "default",
			Labels: map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: "repo"},
		},
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{
			RepositoryRef: corev1.LocalObjectReference{Name: "repo"}, IssueNumber: 7,
			Disposition: triggersv1alpha1.MaintainerWorkItemDispositionBounded,
		},
		Status: triggersv1alpha1.MaintainerWorkItemStatus{
			Phase: triggersv1alpha1.MaintainerWorkItemPhaseTriaged, ProjectionSequence: 3,
			Conditions: []metav1.Condition{{Type: triggersv1alpha1.ConditionMaintainerWorkItemObservationFresh, Status: metav1.ConditionTrue}},
		},
	}
	gapClient.replay = item
	var createErr error
	tool.beforeWorkItemWatch = func() {
		// This mutation lands after the current snapshot List but before Watch.
		// Starting the watch from that List's resourceVersion must replay it.
		createErr = k8sClient.Create(context.Background(), item)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	currentBeforeCreate, watcher, err := tool.workItemSnapshotAndWatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if createErr != nil {
		t.Fatal(createErr)
	}
	if watcher == nil {
		t.Fatal("fake Kubernetes client does not support watch")
	}
	defer watcher.Stop()
	if repoEventsChanged(previous, currentBeforeCreate) {
		t.Fatal("snapshot unexpectedly included the between-list-and-watch create")
	}
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Added {
			t.Fatalf("watch event = %s, want Added", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("create between List and Watch was lost")
	}

	current, err := tool.workItemEventsSnapshot(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !repoEventsChanged(previous, current) {
		t.Fatal("watched work-item snapshot did not detect creation")
	}
	changes := changedRepoWorkItemEvents(previous, current)
	if len(changes) != 1 || changes[0].Name != "issue-7" || changes[0].IssueNumber != 7 || !changes[0].ObservationFresh || changes[0].ProjectionSequence != 3 {
		t.Fatalf("work item changes = %#v", changes)
	}
	initial, err := tool.repoEventsResult(maintainerRepoEventsSnapshot{}, current, true, false, time.Time{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var initialOutput waitForRepoEventsOutput
	if err := json.Unmarshal([]byte(initial.Content), &initialOutput); err != nil {
		t.Fatal(err)
	}
	if len(initialOutput.WorkItemChanges) != 1 || initialOutput.WorkItemChanges[0].Name != "issue-7" {
		t.Fatalf("initial work item output = %#v", initialOutput.WorkItemChanges)
	}
	result, err := tool.repoEventsResult(previous, current, true, false, time.Time{}, true)
	if err != nil {
		t.Fatal(err)
	}
	var output waitForRepoEventsOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.WorkItemChanges) != 1 || output.WorkItemChanges[0].Name != "issue-7" {
		t.Fatalf("work item output = %#v", output.WorkItemChanges)
	}
	acknowledged, err := decodeMaintainerRepoEventsCursor(output.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if repoEventsChanged(snapshotFromMaintainerRepoEventsCursor(acknowledged), current) {
		t.Fatal("emitted work item must be acknowledged by the cursor")
	}
}

func TestWaitForRepoEventsFirstSnapshotIncludesPullRequestChanges(t *testing.T) {
	maintainer := maintainerRun()
	run := fleetRun("implementer", platformv1alpha1.AgentRunPhaseRunning)
	run.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{PullRequestURLs: []string{maintainerTestPullRequestURL}}
	base, _, _ := newMaintainerToolBase(t, maintainer, run)
	runner := &maintainerFakeRunner{out: maintainerPullRequestRunnerOutputs("completed", "success", "APPROVED")}
	runner.out["issue list --state open --json number,title,labels,updatedAt,url --limit 200"] = "[]"
	tool := &waitForRepoEventsTool{maintainerToolBase: base, runner: runner}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"timeout_seconds":30}`), maintainerTestGitRepoDir(t))
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
	if len(output.PullRequestChanges) != 1 || output.PullRequestChanges[0].URL != maintainerTestPullRequestURL || output.PullRequestChanges[0].ReviewDecision != "APPROVED" {
		t.Fatalf("pull request changes = %#v", output.PullRequestChanges)
	}
	cursor, err := decodeMaintainerRepoEventsCursor(output.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.PullRequestSignatures[maintainerTestPullRequestURL] == "" {
		t.Fatalf("pull request signatures = %#v", cursor.PullRequestSignatures)
	}
}

func TestWaitForRepoEventsDegradesOnFleetSnapshotError(t *testing.T) {
	maintainer := maintainerRun()
	run := fleetRun("implementer", platformv1alpha1.AgentRunPhaseRunning)
	base, _, stateStore := newMaintainerToolBase(t, maintainer, run)
	stateStore.sessionErr = errors.New("state store unavailable")
	runner := &maintainerFakeRunner{out: map[string]string{
		"issue list --state open --json number,title,labels,updatedAt,url --limit 200": `[{"number":9,"title":"new work","labels":[],"updatedAt":"2026-01-09T00:00:00Z","url":"https://example.test/issues/9"}]`,
	}}
	tool := &waitForRepoEventsTool{
		maintainerToolBase: base, runner: runner,
		backlogPollInterval: 5 * time.Millisecond, fleetPollInterval: time.Hour, pullRequestPollInterval: time.Hour,
	}
	cursor, err := encodeMaintainerRepoEventsCursor(maintainerRepoEventsCursor{
		BacklogFingerprint: maintainerBacklogSnapshot(nil).backlogFingerprint,
		IssueSignatures:    map[string]string{},
		FleetSignatures:    map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"timeout_seconds":30,"cursor":"`+cursor+`"}`), maintainerTestGitRepoDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("fleet snapshot failure aborted the wait: %s", result.Content)
	}
	var output waitForRepoEventsOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatal(err)
	}
	if !output.Changed || len(output.ChangedIssues) != 1 || output.ChangedIssues[0].Number != 9 {
		t.Fatalf("output = %#v, want the backlog change despite the fleet error", output)
	}
	if !strings.Contains(output.FleetError, "state store unavailable") {
		t.Fatalf("fleet_error = %q, want the degraded fleet failure surfaced", output.FleetError)
	}
}

func TestChangedRepoEventIssuesEmitsRemovals(t *testing.T) {
	previous := maintainerBacklogSnapshot([]maintainerBacklogIssue{
		{Number: 3, Title: "keep", UpdatedAt: "2026-01-01T00:00:00Z", URL: "https://example.test/issues/3"},
		{Number: 7, Title: "closed", UpdatedAt: "2026-01-01T00:00:00Z", URL: "https://example.test/issues/7"},
	})
	current := maintainerBacklogSnapshot([]maintainerBacklogIssue{
		{Number: 3, Title: "keep", UpdatedAt: "2026-01-01T00:00:00Z", URL: "https://example.test/issues/3"},
	})
	if !repoEventsChanged(previous, current) {
		t.Fatal("issue removal did not register as a change")
	}
	changes := changedRepoEventIssues(previous, current)
	if len(changes) != 1 || changes[0].Number != 7 || !changes[0].Removed {
		t.Fatalf("changed issues = %#v, want issue 7 removed", changes)
	}

	// An emitted removal is acknowledged; the cursor converges with live state.
	cursor := repoEventsCursorForEmitted(previous, current, changes, nil, nil, nil)
	if cursor.BacklogFingerprint != current.backlogFingerprint {
		t.Fatalf("cursor fingerprint = %q, want live %q", cursor.BacklogFingerprint, current.backlogFingerprint)
	}
	if repoEventsChanged(snapshotFromMaintainerRepoEventsCursor(cursor), current) {
		t.Fatal("acknowledged removal must not re-fire")
	}

	// A trimmed (not emitted) removal stays pending instead of being silently
	// acknowledged.
	trimmed := repoEventsCursorForEmitted(previous, current, nil, nil, nil, nil)
	if !repoEventsChanged(snapshotFromMaintainerRepoEventsCursor(trimmed), current) {
		t.Fatal("suppressed removal was silently acknowledged")
	}
}

func TestPullRequestEventKeepsUnknownMergeabilityDistinct(t *testing.T) {
	outputs := maintainerPullRequestRunnerOutputs("completed", "success", "APPROVED")
	outputs["api repos/octo/widgets/pulls/7"] = `{"head":{"sha":"abc123"},"state":"OPEN","draft":false,"mergeable":null,"mergeable_state":"unknown"}`
	runner := &maintainerFakeRunner{out: outputs}
	tool := &waitForRepoEventsTool{runner: runner}
	event, err := tool.pullRequestEvent(context.Background(), maintainerTestGitRepoDir(t), maintainerTestPullRequestURL)
	if err != nil {
		t.Fatal(err)
	}
	if event.Mergeable != nil || event.MergeState != "unknown" {
		t.Fatalf("event = %#v, want nil mergeable while GitHub recomputes", event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"mergeable":null`) {
		t.Fatalf("encoded event = %s, want explicit null mergeable", encoded)
	}

	runner.out["api repos/octo/widgets/pulls/7"] = `{"head":{"sha":"abc123"},"state":"OPEN","draft":false,"mergeable":true,"mergeable_state":"clean"}`
	computed, err := tool.pullRequestEvent(context.Background(), maintainerTestGitRepoDir(t), maintainerTestPullRequestURL)
	if err != nil {
		t.Fatal(err)
	}
	if computed.Mergeable == nil || !*computed.Mergeable {
		t.Fatalf("event = %#v, want computed mergeable true", computed)
	}
}

func TestWaitForRepoEventsSkipsPullRequestPollingWhileFleetDegraded(t *testing.T) {
	maintainer := maintainerRun()
	run := fleetRun("implementer", platformv1alpha1.AgentRunPhaseRunning)
	base, _, stateStore := newMaintainerToolBase(t, maintainer, run)
	stateStore.sessionErr = errors.New("state store unavailable")
	runner := &maintainerFakeRunner{out: map[string]string{
		"issue list --state open --json number,title,labels,updatedAt,url --limit 200": `[{"number":9,"title":"new work","labels":[],"updatedAt":"2026-01-09T00:00:00Z","url":"https://example.test/issues/9"}]`,
	}}
	tool := &waitForRepoEventsTool{
		maintainerToolBase: base, runner: runner,
		backlogPollInterval: 40 * time.Millisecond, fleetPollInterval: time.Hour, pullRequestPollInterval: time.Millisecond,
	}
	cursor, err := encodeMaintainerRepoEventsCursor(maintainerRepoEventsCursor{
		BacklogFingerprint:    maintainerBacklogSnapshot(nil).backlogFingerprint,
		IssueSignatures:       map[string]string{},
		FleetSignatures:       map[string]string{},
		PullRequestSignatures: map[string]string{maintainerTestPullRequestURL: "tracked"},
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
	// Many pull-request ticks fire before the backlog change returns; none of
	// them may poll with the degraded (empty) fleet map and misreport the
	// tracked pull request as removed.
	if len(output.PullRequestChanges) != 0 {
		t.Fatalf("pull request changes = %#v, want none while fleet state is degraded", output.PullRequestChanges)
	}
	if !output.Changed || len(output.ChangedIssues) != 1 || output.ChangedIssues[0].Number != 9 {
		t.Fatalf("output = %#v, want only the backlog change", output)
	}
	decoded, err := decodeMaintainerRepoEventsCursor(output.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.PullRequestSignatures[maintainerTestPullRequestURL] != "tracked" {
		t.Fatalf("cursor pull request signatures = %#v, want the tracked PR preserved", decoded.PullRequestSignatures)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "pulls") {
			t.Fatalf("pull request polling ran while fleet state was degraded: %v", runner.calls)
		}
	}
}
