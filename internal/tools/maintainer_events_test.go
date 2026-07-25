package tools

import (
	"bytes"
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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var maintainerEventsTestWorkItemName = triggersv1alpha1.MaintainerWorkItemName(maintainerTestRepositoryName, 7)

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

const (
	maintainerTestMergedState = "merged"
	maintainerTestHeadSHA     = "abc123"
	maintainerTestPRURL       = "https://example.test/pr/1"
)

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
	run := fleetRun(maintainerTestFleetRunName, platformv1alpha1.AgentRunPhaseRunning)
	base, _, stateStore := newMaintainerToolBase(t, maintainer, run)
	stateStore.sessions["default/implementer"] = &store.Session{AgentRunName: maintainerTestFleetRunName, AgentRunNS: maintainerTestNamespace}
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
	run := fleetRun(maintainerTestFleetRunName, platformv1alpha1.AgentRunPhaseRunning)
	base, _, stateStore := newMaintainerToolBase(t, maintainer, run)
	stateStore.sessions["default/implementer"] = &store.Session{AgentRunName: maintainerTestFleetRunName, AgentRunNS: maintainerTestNamespace, PendingInputType: "question", PendingRequestID: "request"}
	tool := &waitForRepoEventsTool{maintainerToolBase: base}
	snapshot, err := tool.fleetEventsSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	event := snapshot.fleet[maintainerTestFleetRunName]
	if !event.PendingInput || snapshot.fleetSignatures[maintainerTestFleetRunName] == "" {
		t.Fatalf("fleet event = %#v, signatures = %#v", event, snapshot.fleetSignatures)
	}
}

func TestWaitForRepoEventsFirstCallReturnsCurrentStateImmediately(t *testing.T) {
	maintainer := maintainerRun()
	run := fleetRun(maintainerTestFleetRunName, platformv1alpha1.AgentRunPhaseSucceeded)
	base, _, stateStore := newMaintainerToolBase(t, maintainer, run)
	stateStore.sessions["default/implementer"] = &store.Session{AgentRunName: maintainerTestFleetRunName, AgentRunNS: maintainerTestNamespace}
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
	if len(output.FleetChanges) != 1 || output.FleetChanges[0].Name != maintainerTestFleetRunName {
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
		"api repos/octo/widgets/pulls/7":                                                                                  `{"head":{"sha":"` + maintainerTestHeadSHA + `"},"state":"OPEN","draft":false,"mergeable":true,"mergeable_state":"clean"}`,
		"api graphql -f query=" + maintainerPullRequestReviewDecisionQuery + " -f owner=octo -f repo=widgets -F number=7": `{"data":{"repository":{"pullRequest":{"reviewDecision":"` + reviewDecision + `"}}}}`,
		"api repos/octo/widgets/commits/abc123/check-runs --paginate":                                                     `{"check_runs":[{"name":"build","status":"` + checkStatus + `","conclusion":"` + conclusion + `"}]}`,
		"api repos/octo/widgets/commits/abc123/status":                                                                    `{"statuses":[]}`,
	}
}

func TestRepoEventsDetectsPullRequestCIPendingToPassed(t *testing.T) {
	runner := &maintainerFakeRunner{out: maintainerPullRequestRunnerOutputs("in_progress", "", "REVIEW_REQUIRED")}
	tool := &waitForRepoEventsTool{runner: runner}
	fleet := map[string]maintainerRepoFleetEvent{maintainerTestFleetRunName: {PullRequestURLs: []string{maintainerTestPullRequestURL}}}

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

func TestMergedMonitorRemainsAuthoritativeAfterPollingStops(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-24 * time.Hour))
	monitor := &triggersv1alpha1.PullRequestMonitor{Spec: triggersv1alpha1.PullRequestMonitorSpec{URL: maintainerTestPullRequestURL}, Status: triggersv1alpha1.PullRequestMonitorStatus{Lifecycle: triggersv1alpha1.PullRequestLifecycleMerged, PullObservedAt: old, LastError: "obsolete pre-merge error"}}
	if !maintainerMonitorEquivalentAndFresh(monitor, time.Now()) {
		t.Fatal("irreversible merged observation became stale")
	}
	if event := maintainerEventFromMonitor(monitor); event.State != maintainerTestMergedState {
		t.Fatalf("event = %#v", event)
	}
}

func TestRepoEventsUsesFreshMonitorWithoutDirectGitHubCalls(t *testing.T) {
	now := metav1.Now()
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	monitor := &triggersv1alpha1.PullRequestMonitor{
		ObjectMeta: metav1.ObjectMeta{Name: "monitor-7", Namespace: maintainerTestNamespace},
		Spec:       triggersv1alpha1.PullRequestMonitorSpec{Repository: "octo/widgets", Number: 7, URL: maintainerTestPullRequestURL, ImplementerRef: corev1.LocalObjectReference{Name: maintainerTestFleetRunName}},
		Status:     triggersv1alpha1.PullRequestMonitorStatus{Lifecycle: triggersv1alpha1.PullRequestLifecycleOpen, HeadSHA: maintainerTestHeadSHA, Mergeability: triggersv1alpha1.PullRequestMergeabilityMergeable, ReviewDecision: triggersv1alpha1.PullRequestReviewDecisionApproved, PullObservedAt: now, ReviewsObservedAt: now, Checks: triggersv1alpha1.PullRequestMonitorHeadRollup{HeadSHA: maintainerTestHeadSHA, State: maintainerRollupSuccess, Count: 1, ObservedAt: now}, Statuses: triggersv1alpha1.PullRequestMonitorHeadRollup{HeadSHA: maintainerTestHeadSHA, State: "none", ObservedAt: now}},
	}
	runner := &maintainerFakeRunner{out: map[string]string{}}
	tool := &waitForRepoEventsTool{maintainerToolBase: maintainerToolBase{k8sClient: fake.NewClientBuilder().WithScheme(scheme).WithObjects(monitor).Build(), repositoryNamespace: maintainerTestNamespace}, runner: runner}
	fleet := map[string]maintainerRepoFleetEvent{maintainerTestFleetRunName: {PullRequestURLs: []string{maintainerTestPullRequestURL}}}
	snapshot, err := tool.pullRequestEventsSnapshot(context.Background(), maintainerTestGitRepoDir(t), fleet)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("fresh monitor caused duplicate GitHub calls: %#v", runner.calls)
	}
	got := snapshot.pullRequests[maintainerTestPullRequestURL]
	if got.HeadSHA != maintainerTestHeadSHA || got.ReviewDecision != "APPROVED" || got.Checks.Passed != 1 {
		t.Fatalf("monitor projection = %#v", got)
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

func TestMaintainerDualReadParityComparesSemanticContent(t *testing.T) {
	snapshot := maintainerRepoEventsSnapshot{
		issues:       []maintainerRepoEventIssue{{Number: 7, Title: "issue", Labels: []string{"bug"}}},
		fleet:        map[string]maintainerRepoFleetEvent{"run": {Name: "run", Phase: platformv1alpha1.AgentRunPhasePaused}},
		pullRequests: map[string]maintainerRepoPullRequestEvent{maintainerTestPRURL: {URL: maintainerTestPRURL, HeadSHA: "abc", State: "open", ReviewDecision: "APPROVED"}},
		workItems:    map[string]maintainerRepoWorkItemEvent{"item": {ObservationFresh: true, IssueObservation: &triggersv1alpha1.MaintainerIssueObservation{Number: 7, Title: "issue", State: triggersv1alpha1.MaintainerIssueStateOpen, Labels: []string{"bug"}}, AgentRuns: []triggersv1alpha1.MaintainerWorkItemAgentRunProjection{{Name: "run", Phase: string(platformv1alpha1.AgentRunPhasePaused)}}, PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{URL: maintainerTestPRURL, HeadSHA: "abc", State: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, ReviewDecision: "APPROVED"}}}},
	}
	if mismatches := maintainerSemanticParityMismatches(snapshot); len(mismatches) != 0 {
		t.Fatalf("matching projections reported %v", mismatches)
	}
	snapshot.workItems["item"] = maintainerRepoWorkItemEvent{IssueObservation: &triggersv1alpha1.MaintainerIssueObservation{Number: 7, Title: "different", State: triggersv1alpha1.MaintainerIssueStateOpen}}
	if mismatches := maintainerSemanticParityMismatches(snapshot); len(mismatches) == 0 {
		t.Fatal("content mismatch was reported as parity")
	}
}

func TestSemanticWaiterUsesPersistedProjectionWithoutGitHubPolling(t *testing.T) {
	base, k8sClient, _ := newMaintainerToolBase(t, maintainerRun())
	repository := &triggersv1alpha1.GitHubRepository{}
	key := client.ObjectKey{Name: maintainerTestRepositoryName, Namespace: maintainerTestNamespace}
	if err := k8sClient.Get(context.Background(), key, repository); err != nil {
		t.Fatal(err)
	}
	repository.Spec.Maintainer.WorkItemCutover = triggersv1alpha1.MaintainerWorkItemCutoverController
	if err := k8sClient.Update(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	semanticName := triggersv1alpha1.MaintainerWorkItemName(maintainerTestRepositoryName, 9)
	item := &triggersv1alpha1.MaintainerWorkItem{ObjectMeta: metav1.ObjectMeta{Name: semanticName, Namespace: maintainerTestNamespace, Labels: map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: maintainerTestRepositoryName}}, Spec: triggersv1alpha1.MaintainerWorkItemSpec{RepositoryRef: corev1.LocalObjectReference{Name: maintainerTestRepositoryName}, IssueNumber: 9}, Status: triggersv1alpha1.MaintainerWorkItemStatus{Phase: triggersv1alpha1.MaintainerWorkItemPhasePendingTriage, ProjectionSequence: 4, IssueObservation: &triggersv1alpha1.MaintainerIssueObservation{Number: 9, Title: "durably observed"}}}
	if err := k8sClient.Create(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	runner := &maintainerFakeRunner{out: map[string]string{}, err: map[string]error{}}
	tool := &waitForRepoEventsTool{maintainerToolBase: base, runner: runner}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"timeout_seconds":30}`), "missing-workdir")
	if err != nil || result.IsError {
		t.Fatalf("semantic wait result=%#v err=%v", result, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("controller waiter made direct GitHub calls: %v", runner.calls)
	}
	var output maintainerSemanticWaitOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatal(err)
	}
	if !output.Changed || len(output.WorkItems) != 1 || output.WorkItems[0].ProjectionSequence != 4 || output.WorkItems[0].IssueObservation.Title != "durably observed" {
		t.Fatalf("semantic output = %#v", output)
	}
	cursor, err := decodeMaintainerSemanticCursor(output.Cursor)
	if err != nil || cursor.Sequences[semanticName] != 4 {
		t.Fatalf("cursor=%#v err=%v", cursor, err)
	}
	if output.CursorHandle != maintainerSemanticLatestHandle {
		t.Fatalf("cursor_handle = %q, want latest", output.CursorHandle)
	}

	// A reconstructed tool instance can resolve continuity from the AgentRun
	// rather than process-local memory.
	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: base.currentRunName, Namespace: base.currentRunNamespace}, run); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := tool.semanticCursorCheckpoint(context.Background(), run.UID, types.UID("repo-uid"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := resolveMaintainerSemanticCursorState(checkpoint, run, types.UID("repo-uid"), maintainerSemanticLatestHandle, time.Now())
	if err != nil || state.semanticCursor().Sequences[semanticName] != 4 {
		t.Fatalf("reconstructed latest state=%#v err=%v", state, err)
	}
}

func TestSemanticLatestHandleAdvancesAndDeletionConverges(t *testing.T) {
	base, k8sClient, _ := newMaintainerToolBase(t, maintainerRun())
	repository := &triggersv1alpha1.GitHubRepository{}
	repositoryKey := client.ObjectKey{Name: maintainerTestRepositoryName, Namespace: maintainerTestNamespace}
	if err := k8sClient.Get(t.Context(), repositoryKey, repository); err != nil {
		t.Fatal(err)
	}
	repository.Spec.Maintainer.WorkItemCutover = triggersv1alpha1.MaintainerWorkItemCutoverController
	if err := k8sClient.Update(t.Context(), repository); err != nil {
		t.Fatal(err)
	}
	name := triggersv1alpha1.MaintainerWorkItemName(maintainerTestRepositoryName, 12)
	item := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: maintainerTestNamespace, Labels: map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: maintainerTestRepositoryName}},
		Spec:       triggersv1alpha1.MaintainerWorkItemSpec{RepositoryRef: corev1.LocalObjectReference{Name: maintainerTestRepositoryName}, IssueNumber: 12},
		Status:     triggersv1alpha1.MaintainerWorkItemStatus{ProjectionSequence: 1},
	}
	if err := k8sClient.Create(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	tool := &waitForRepoEventsTool{maintainerToolBase: base}
	initialResult, err := tool.Execute(t.Context(), json.RawMessage(`{"timeout_seconds":30}`), "")
	if err != nil || initialResult.IsError {
		t.Fatalf("initial result=%#v err=%v", initialResult, err)
	}
	var initial maintainerSemanticWaitOutput
	if err := json.Unmarshal([]byte(initialResult.Content), &initial); err != nil {
		t.Fatal(err)
	}

	// Encoded v2 cursors remain accepted during the compatibility window and,
	// like latest, enter the watch when the snapshot is unchanged.
	compatCtx, cancelCompat := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelCompat()
	compatInput, err := json.Marshal(waitForRepoEventsInput{TimeoutSeconds: 30, Cursor: initial.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(compatCtx, compatInput, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unchanged encoded-v2 wait error = %v, want context deadline", err)
	}

	// With no semantic change, latest enters the watch instead of returning a
	// duplicate snapshot.
	blockedCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := tool.Execute(blockedCtx, json.RawMessage(`{"timeout_seconds":30,"cursor":"latest"}`), ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unchanged latest wait error = %v, want context deadline", err)
	}

	updateDone := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		fresh := &triggersv1alpha1.MaintainerWorkItem{}
		if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: name, Namespace: maintainerTestNamespace}, fresh); err != nil {
			updateDone <- err
			return
		}
		fresh.Status.ProjectionSequence = 2
		updateDone <- k8sClient.Update(context.Background(), fresh)
	}()
	result, err := tool.Execute(t.Context(), json.RawMessage(`{"timeout_seconds":30,"cursor":"latest"}`), "")
	if err != nil || result.IsError {
		t.Fatalf("updated result=%#v err=%v", result, err)
	}
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	var updated maintainerSemanticWaitOutput
	if err := json.Unmarshal([]byte(result.Content), &updated); err != nil {
		t.Fatal(err)
	}
	if len(updated.WorkItems) != 1 || updated.WorkItems[0].ProjectionSequence != 2 {
		t.Fatalf("projection update = %#v", updated.WorkItems)
	}

	deleteDone := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		fresh := &triggersv1alpha1.MaintainerWorkItem{}
		if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: name, Namespace: maintainerTestNamespace}, fresh); err != nil {
			deleteDone <- err
			return
		}
		deleteDone <- k8sClient.Delete(context.Background(), fresh)
	}()
	result, err = tool.Execute(t.Context(), json.RawMessage(`{"timeout_seconds":30,"cursor":"latest"}`), "")
	if err != nil || result.IsError {
		t.Fatalf("deletion result=%#v err=%v", result, err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatal(err)
	}
	var deleted maintainerSemanticWaitOutput
	if err := json.Unmarshal([]byte(result.Content), &deleted); err != nil {
		t.Fatal(err)
	}
	if len(deleted.WorkItems) != 1 || !deleted.WorkItems[0].Removed {
		t.Fatalf("deletion changes = %#v", deleted.WorkItems)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(t.Context(), client.ObjectKey{Name: base.currentRunName, Namespace: base.currentRunNamespace}, run); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := tool.semanticCursorCheckpoint(t.Context(), run.UID, repository.UID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := resolveMaintainerSemanticCursorState(checkpoint, run, repository.UID, maintainerSemanticLatestHandle, time.Now())
	if err != nil || len(state.Entries) != 0 {
		t.Fatalf("deletion was not acknowledged: state=%#v err=%v", state, err)
	}
}

func TestSemanticCursorHandleValidationAndIsolation(t *testing.T) {
	now := time.Now()
	run := maintainerRun()
	state := maintainerSemanticCursorState{
		Version: 1, RunUID: run.UID, RepositoryUID: types.UID("repo-uid"),
		Handle: maintainerSemanticOpaquePrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 16)), ExpiresAt: now.Add(time.Hour),
	}
	controller := true
	secretForState := func(candidate maintainerSemanticCursorState) *corev1.Secret {
		raw, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: run.Name, UID: run.UID, Controller: &controller}}},
			Data:       map[string][]byte{maintainerSemanticCursorDataKey: raw},
		}
	}
	if _, err := resolveMaintainerSemanticCursorState(secretForState(state), run, state.RepositoryUID, state.Handle, now); err != nil {
		t.Fatalf("valid handle: %v", err)
	}
	cases := []struct {
		name, handle string
		repository   types.UID
		mutate       func(*maintainerSemanticCursorState)
		want         string
	}{
		{name: "malformed", handle: maintainerSemanticOpaquePrefix + "bad", repository: state.RepositoryUID, want: "malformed"},
		{name: "stale", handle: maintainerSemanticOpaquePrefix + base64.RawURLEncoding.EncodeToString([]byte("1234567890123456")), repository: state.RepositoryUID, want: "stale"},
		{name: "repository isolation", handle: maintainerSemanticLatestHandle, repository: types.UID("other-repo"), want: "cross-boundary"},
		{name: "run isolation", handle: maintainerSemanticLatestHandle, repository: state.RepositoryUID, mutate: func(s *maintainerSemanticCursorState) { s.RunUID = types.UID("other-run") }, want: "cross-boundary"},
		{name: "expired", handle: maintainerSemanticLatestHandle, repository: state.RepositoryUID, mutate: func(s *maintainerSemanticCursorState) { s.ExpiresAt = now.Add(-time.Second) }, want: "expired"},
		{name: "malformed stored handle", handle: maintainerSemanticLatestHandle, repository: state.RepositoryUID, mutate: func(s *maintainerSemanticCursorState) { s.Handle = "bad" }, want: "malformed stored"},
		{name: "malformed stored sequence", handle: maintainerSemanticLatestHandle, repository: state.RepositoryUID, mutate: func(s *maintainerSemanticCursorState) {
			s.Entries = []maintainerSemanticCursorEntry{{Name: "item", Sequence: -1, IssueNumber: 1}}
		}, want: "malformed stored"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := state
			if tc.mutate != nil {
				tc.mutate(&candidate)
			}
			if _, resolveErr := resolveMaintainerSemanticCursorState(secretForState(candidate), run, tc.repository, tc.handle, now); resolveErr == nil || !strings.Contains(resolveErr.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", resolveErr, tc.want)
			}
		})
	}
}

func TestSemanticCursorRejectsNonCanonicalIdentity(t *testing.T) {
	malformedName := "mwi-widget-7-deadbeef00"
	cursor := maintainerSemanticCursor{Version: 2, Sequences: map[string]int64{malformedName: 1}, Identities: map[string]int32{malformedName: 7}}
	if err := validateSemanticCursorIdentities(maintainerTestRepositoryName, cursor, nil); err == nil || !strings.Contains(err.Error(), "non-canonical cursor identity") {
		t.Fatalf("error = %v", err)
	}
	legacyMalformed := maintainerSemanticCursor{Version: 2, Sequences: map[string]int64{malformedName: 1}}
	if err := validateSemanticCursorIdentities(maintainerTestRepositoryName, legacyMalformed, nil); err == nil || !strings.Contains(err.Error(), "non-canonical cursor identity") {
		t.Fatalf("legacy malformed error = %v", err)
	}
	longRepositoryName := strings.Repeat("long-repository-", 5)
	longName := triggersv1alpha1.MaintainerWorkItemName(longRepositoryName, 42)
	longCursor := maintainerSemanticCursor{Version: 2, Sequences: map[string]int64{longName: 3}, Identities: map[string]int32{longName: 42}}
	if err := validateSemanticCursorIdentities(longRepositoryName, longCursor, nil); err != nil {
		t.Fatalf("valid truncated deletion identity rejected: %v", err)
	}
	legacyLongCursor := maintainerSemanticCursor{Version: 2, Sequences: map[string]int64{longName: 3}}
	if err := validateSemanticCursorIdentities(longRepositoryName, legacyLongCursor, nil); err != nil {
		t.Fatalf("legacy v2 truncated deletion identity rejected: %v", err)
	}
	partialRepositoryName := strings.Repeat("a", 46)
	partialName := triggersv1alpha1.MaintainerWorkItemName(partialRepositoryName, 42)
	partialCursor := maintainerSemanticCursor{Version: 2, Sequences: map[string]int64{partialName: 3}}
	if err := validateSemanticCursorIdentities(partialRepositoryName, partialCursor, nil); err != nil {
		t.Fatalf("legacy v2 partially truncated deletion identity rejected: %v", err)
	}
	encodedLegacy, err := encodeMaintainerSemanticCursor(legacyLongCursor)
	if err != nil {
		t.Fatal(err)
	}
	decodedLegacy, err := decodeMaintainerSemanticCursor(encodedLegacy)
	if err != nil {
		t.Fatal(err)
	}
	changes := semanticSnapshotChanges(decodedLegacy.Sequences, map[string]maintainerRepoWorkItemEvent{}, false)
	if len(changes) != 1 || changes[0].Name != longName || !changes[0].Removed {
		t.Fatalf("legacy truncated deletion changes = %#v", changes)
	}

	base, k8sClient, _ := newMaintainerToolBase(t, maintainerRun())
	repository := &triggersv1alpha1.GitHubRepository{}
	if err := k8sClient.Get(t.Context(), client.ObjectKey{Name: maintainerTestRepositoryName, Namespace: maintainerTestNamespace}, repository); err != nil {
		t.Fatal(err)
	}
	repository.Spec.Maintainer.WorkItemCutover = triggersv1alpha1.MaintainerWorkItemCutoverController
	if err := k8sClient.Update(t.Context(), repository); err != nil {
		t.Fatal(err)
	}
	item := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{Name: "non-canonical", Namespace: maintainerTestNamespace, Labels: map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: maintainerTestRepositoryName}},
		Spec:       triggersv1alpha1.MaintainerWorkItemSpec{RepositoryRef: corev1.LocalObjectReference{Name: maintainerTestRepositoryName}, IssueNumber: 13},
	}
	if err := k8sClient.Create(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	tool := &waitForRepoEventsTool{maintainerToolBase: base}
	result, err := tool.Execute(t.Context(), json.RawMessage(`{"timeout_seconds":30}`), "")
	if err != nil || !result.IsError || !strings.Contains(result.Content, "non-canonical cursor identity") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	checkpoint, err := tool.semanticCursorCheckpoint(t.Context(), base.currentRunUID, repository.UID)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint == nil || len(checkpoint.Data[maintainerSemanticCursorDataKey]) != 0 {
		t.Fatal("rejected current identity advanced cursor state")
	}
}

func TestSemanticCursorCheckpointIsCompactForLargeRepositories(t *testing.T) {
	cursor := maintainerSemanticCursor{Version: 2, Sequences: make(map[string]int64, 2000), Identities: make(map[string]int32, 2000)}
	for issue := int32(1); issue <= 2000; issue++ {
		name := triggersv1alpha1.MaintainerWorkItemName(strings.Repeat("long-repository-name-", 4), issue)
		cursor.Sequences[name] = int64(issue)
		cursor.Identities[name] = issue
	}
	state := newMaintainerSemanticCursorState(types.UID("run-uid"), types.UID("repository-uid"), maintainerSemanticOpaquePrefix+base64.RawURLEncoding.EncodeToString(make([]byte, 16)), time.Now().Add(time.Hour), cursor)
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= 256*1024 {
		t.Fatalf("compact checkpoint size = %d bytes, want below 256 KiB", len(encoded))
	}
	for name := range cursor.Sequences {
		if bytes.Count(encoded, []byte(name)) != 1 {
			t.Fatalf("work-item name %q is duplicated in compact checkpoint", name)
		}
	}
}

func TestSemanticCursorAdvanceUsesCompareAndSwap(t *testing.T) {
	base, k8sClient, _ := newMaintainerToolBase(t, maintainerRun())
	tool := &waitForRepoEventsTool{maintainerToolBase: base}
	checkpoint, err := tool.semanticCursorCheckpoint(t.Context(), base.currentRunUID, types.UID("repo-uid"))
	if err != nil {
		t.Fatal(err)
	}
	if err := tool.persistMaintainerSemanticCursorState(t.Context(), base.currentRunUID, types.UID("repo-uid"), checkpoint.ResourceVersion, maintainerSemanticCursor{Version: 2, Sequences: map[string]int64{}}); err != nil {
		t.Fatal(err)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(t.Context(), client.ObjectKey{Name: base.currentRunName, Namespace: base.currentRunNamespace}, run); err != nil {
		t.Fatal(err)
	}
	checkpoint, err = tool.semanticCursorCheckpoint(t.Context(), run.UID, types.UID("repo-uid"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveMaintainerSemanticCursorState(checkpoint, run, types.UID("repo-uid"), maintainerSemanticLatestHandle, time.Now()); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for sequence := int64(1); sequence <= 2; sequence++ {
		go func() {
			<-start
			results <- tool.persistMaintainerSemanticCursorState(context.Background(), base.currentRunUID, types.UID("repo-uid"), checkpoint.ResourceVersion, maintainerSemanticCursor{Version: 2, Sequences: map[string]int64{"item": sequence}, Identities: map[string]int32{"item": 1}})
		}()
	}
	close(start)
	var succeeded, stale int
	for range 2 {
		if persistErr := <-results; persistErr == nil {
			succeeded++
		} else if strings.Contains(persistErr.Error(), "stale semantic cursor handle") {
			stale++
		} else {
			t.Fatalf("unexpected persistence error: %v", persistErr)
		}
	}
	if succeeded != 1 || stale != 1 {
		t.Fatalf("successes=%d stale=%d, want one of each", succeeded, stale)
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

	baseline, err := tool.workItemEventsSnapshot(context.Background())
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
			Name: maintainerEventsTestWorkItemName, Namespace: maintainerTestNamespace,
			Labels: map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: maintainerTestRepositoryName},
		},
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{
			RepositoryRef: corev1.LocalObjectReference{Name: maintainerTestRepositoryName}, IssueNumber: 7,
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
	currentBeforeCreate, watcher, err := tool.workItemSnapshotAndWatch(t.Context())
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

	current, err := tool.workItemEventsSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !repoEventsChanged(previous, current) {
		t.Fatal("watched work-item snapshot did not detect creation")
	}
	changes := changedRepoWorkItemEvents(previous, current)
	if len(changes) != 1 || changes[0].Name != maintainerEventsTestWorkItemName || changes[0].IssueNumber != 7 || !changes[0].ObservationFresh || changes[0].ProjectionSequence != 3 {
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
	if len(initialOutput.WorkItemChanges) != 1 || initialOutput.WorkItemChanges[0].Name != maintainerEventsTestWorkItemName {
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
	if len(output.WorkItemChanges) != 1 || output.WorkItemChanges[0].Name != maintainerEventsTestWorkItemName {
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
	run := fleetRun(maintainerTestFleetRunName, platformv1alpha1.AgentRunPhaseRunning)
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
	run := fleetRun(maintainerTestFleetRunName, platformv1alpha1.AgentRunPhaseRunning)
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
	outputs["api repos/octo/widgets/pulls/7"] = `{"head":{"sha":"` + maintainerTestHeadSHA + `"},"state":"OPEN","draft":false,"mergeable":null,"mergeable_state":"unknown"}`
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

	runner.out["api repos/octo/widgets/pulls/7"] = `{"head":{"sha":"` + maintainerTestHeadSHA + `"},"state":"OPEN","draft":false,"mergeable":true,"mergeable_state":"clean"}`
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
	run := fleetRun(maintainerTestFleetRunName, platformv1alpha1.AgentRunPhaseRunning)
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
