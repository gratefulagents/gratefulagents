package triggers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	monitorTestOpen     = "open"
	monitorTestClosed   = "closed"
	monitorTestAlice    = "alice"
	monitorTestToken    = "gh-token"
	monitorTestOldSHA   = "old"
	monitorTestNewSHA   = "new"
	monitorTestTokenKey = "token"
	monitorTestAgentBot = "agent-bot"
	monitorTestBaseRef  = "main"
)

func TestArtifactPullRequestsCanonicalizesAndDeduplicatesURLs(t *testing.T) {
	run := monitorProvenanceRun(t)
	run.Status.Artifacts.PullRequestURL = " HTTPS://GitHub.com/Acme/Widgets/pull/42/ "
	run.Status.Artifacts.PullRequestURLs = []string{
		"https://github.com/acme/widgets/pull/42",
		"https://github.com/ACME/WIDGETS/PULL/42?ignored=no",
		"https://gitlab.com/acme/widgets/pull/42",
		"not a URL",
	}

	got := artifactPullRequests(run)
	if len(got) != 1 {
		t.Fatalf("artifactPullRequests() = %#v, want one canonical PR", got)
	}
	if want := "https://github.com/acme/widgets/pull/42"; got[0].URL != want {
		t.Fatalf("canonical URL = %q, want %q", got[0].URL, want)
	}
	if got[0].Repository != "acme/widgets" || got[0].Number != 42 {
		t.Fatalf("canonical identity = %#v, want acme/widgets#42", got[0])
	}
}

func TestPullRequestMonitorNameIncludesAgentRunUID(t *testing.T) {
	const pullURL = "https://github.com/acme/widgets/pull/42"
	first := pullRequestMonitorName(types.UID("run-uid-one"), pullURL)
	second := pullRequestMonitorName(types.UID("run-uid-two"), pullURL)
	if first == second {
		t.Fatalf("names for different AgentRun UIDs are equal: %q", first)
	}
	if !strings.HasPrefix(first, "pr-monitor-") || len(first) > 63 {
		t.Fatalf("monitor name %q is not a valid bounded generated name", first)
	}
}

func TestPullRequestArtifactReconcileIdempotentlyCreatesMultipleOwnedMonitors(t *testing.T) {
	run := monitorProvenanceRun(t)
	run.Status.Artifacts.PullRequestURL = "https://github.com/acme/widgets/pull/42"
	run.Status.Artifacts.PullRequestURLs = []string{
		"https://github.com/acme/widgets/pull/42",
		"https://github.com/acme/gadgets/pull/7",
	}
	run.Spec.Repository.AdditionalRepos = []string{"https://github.com/acme/gadgets"}
	scheme := prLoopTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	reconciler := &PullRequestArtifactReconciler{Client: c, Scheme: scheme}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}

	for i := 0; i < 2; i++ {
		if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("Reconcile() pass %d error = %v", i+1, err)
		}
	}

	var monitors triggersv1alpha1.PullRequestMonitorList
	if err := c.List(context.Background(), &monitors, client.InNamespace(run.Namespace)); err != nil {
		t.Fatalf("list PullRequestMonitors: %v", err)
	}
	if got, want := len(monitors.Items), 2; got != want {
		t.Fatalf("monitor count after two reconciles = %d, want %d", got, want)
	}
	urls := map[string]bool{}
	for i := range monitors.Items {
		monitor := &monitors.Items[i]
		owner := metav1.GetControllerOf(monitor)
		if owner == nil || owner.UID != run.UID {
			t.Errorf("monitor %q controller owner = %#v, want AgentRun UID %q", monitor.Name, owner, run.UID)
		}
		if monitor.Spec.ImplementerRef.Name != run.Name {
			t.Errorf("monitor %q implementer = %q, want %q", monitor.Name, monitor.Spec.ImplementerRef.Name, run.Name)
		}
		urls[monitor.Spec.URL] = true
	}
	for _, want := range []string{"https://github.com/acme/widgets/pull/42", "https://github.com/acme/gadgets/pull/7"} {
		if !urls[want] {
			t.Errorf("missing monitor for %q; got URLs %v", want, urls)
		}
	}
}

func TestPullRequestArtifactCreatesMonitorWithoutReviewLoop(t *testing.T) {
	run := monitorProvenanceRun(t)
	run.Status.Artifacts.PullRequestURL = "https://github.com/acme/widgets/pull/42"
	delete(run.Annotations, PRLoopOptAnnotation)
	scheme := prLoopTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	reconciler := &PullRequestArtifactReconciler{Client: c, Scheme: scheme}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var monitors triggersv1alpha1.PullRequestMonitorList
	if err := c.List(context.Background(), &monitors, client.InNamespace(run.Namespace)); err != nil {
		t.Fatalf("list PullRequestMonitors: %v", err)
	}
	if len(monitors.Items) != 1 {
		t.Fatalf("monitor count = %d, want 1 without a review loop", len(monitors.Items))
	}
}

func TestPullRequestArtifactCreatesMonitorWhenReviewLoopDisabled(t *testing.T) {
	run := monitorProvenanceRun(t)
	run.Status.Artifacts.PullRequestURL = "https://github.com/acme/widgets/pull/42"
	if run.Annotations == nil {
		run.Annotations = map[string]string{}
	}
	run.Annotations[PRLoopOptAnnotation] = PRLoopOptDisabled
	scheme := prLoopTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	reconciler := &PullRequestArtifactReconciler{Client: c, Scheme: scheme}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var monitors triggersv1alpha1.PullRequestMonitorList
	if err := c.List(context.Background(), &monitors, client.InNamespace(run.Namespace)); err != nil {
		t.Fatalf("list PullRequestMonitors: %v", err)
	}
	if len(monitors.Items) != 1 {
		t.Fatalf("monitor count = %d, want 1 when review loop is disabled", len(monitors.Items))
	}
}

func TestAfterCursorUsesTimestampThenID(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cursor := &triggersv1alpha1.GitHubObjectCursor{Timestamp: metav1.NewTime(at), ID: 20}
	cases := []struct {
		name string
		at   time.Time
		id   int64
		want bool
	}{
		{name: "older timestamp", at: at.Add(-time.Nanosecond), id: 100, want: false},
		{name: "same timestamp lower ID", at: at, id: 19, want: false},
		{name: "exact cursor", at: at, id: 20, want: false},
		{name: "same timestamp higher ID", at: at, id: 21, want: true},
		{name: "newer timestamp", at: at.Add(time.Nanosecond), id: 1, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := afterCursor(tc.at, tc.id, cursor); got != tc.want {
				t.Fatalf("afterCursor() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCombinedFeedbackSortsByTimestampThenID(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	monitor := &triggersv1alpha1.PullRequestMonitor{Spec: triggersv1alpha1.PullRequestMonitorSpec{Repository: "acme/widgets", Number: 42, URL: "https://github.com/acme/widgets/pull/42"}}
	cursor := &triggersv1alpha1.GitHubObjectCursor{Timestamp: metav1.NewTime(at.Add(-time.Second))}

	feedback := combinedFeedback(monitor, nil,
		[]polledPullRequestReview{{ID: 30, SubmittedAt: at.Add(time.Second)}, {ID: 20, SubmittedAt: at}},
		[]polledIssueComment{{ID: 10, CreatedAt: at}, {ID: 40, CreatedAt: at.Add(2 * time.Second)}},
		cursor, cursor,
	)
	got := make([]int64, len(feedback))
	for i := range feedback {
		got[i] = feedback[i].id
	}
	want := []int64{10, 20, 30, 40}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted feedback IDs = %v, want %v", got, want)
		}
	}
}

func TestPullRequestFactsTrackLifecycleMergeabilityAndReviews(t *testing.T) {
	mergedAt := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	cases := []struct {
		name         string
		pull         polledPullRequest
		lifecycle    triggersv1alpha1.PullRequestLifecycle
		mergeability triggersv1alpha1.PullRequestMergeability
	}{
		{name: "draft unknown mergeability", pull: polledPullRequest{State: monitorTestOpen, Draft: true}, lifecycle: triggersv1alpha1.PullRequestLifecycleDraft, mergeability: triggersv1alpha1.PullRequestMergeabilityUnknown},
		{name: "open mergeable", pull: polledPullRequest{State: monitorTestOpen, MergeableKnown: true, Mergeable: true}, lifecycle: triggersv1alpha1.PullRequestLifecycleOpen, mergeability: triggersv1alpha1.PullRequestMergeabilityMergeable},
		{name: "closed conflicting", pull: polledPullRequest{State: monitorTestClosed, MergeableKnown: true}, lifecycle: triggersv1alpha1.PullRequestLifecycleClosed, mergeability: triggersv1alpha1.PullRequestMergeabilityConflicting},
		{name: "merged", pull: polledPullRequest{State: monitorTestClosed, Merged: true, MergedAt: mergedAt}, lifecycle: triggersv1alpha1.PullRequestLifecycleMerged, mergeability: triggersv1alpha1.PullRequestMergeabilityUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pullRequestLifecycle(&tc.pull); got != tc.lifecycle {
				t.Fatalf("lifecycle = %q, want %q", got, tc.lifecycle)
			}
			if got := pullRequestMergeability(&tc.pull); got != tc.mergeability {
				t.Fatalf("mergeability = %q, want %q", got, tc.mergeability)
			}
		})
	}

	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	reviews := []polledPullRequestReview{
		{ID: 1, AuthorLogin: monitorTestAlice, State: string(triggersv1alpha1.PullRequestReviewDecisionApproved), SubmittedAt: now.Add(-time.Minute)},
		{ID: 2, AuthorLogin: monitorTestAlice, State: string(triggersv1alpha1.PullRequestReviewDecisionChangesRequested), SubmittedAt: now},
		{ID: 3, AuthorLogin: "bob", State: string(triggersv1alpha1.PullRequestReviewDecisionApproved), SubmittedAt: now},
	}
	if got, want := aggregateReviewDecision(reviews), triggersv1alpha1.PullRequestReviewDecisionChangesRequested; got != want {
		t.Fatalf("review decision = %q, want %q", got, want)
	}
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	if got, want := backoff(3), 2*time.Minute; got != want {
		t.Fatalf("backoff(3) = %s, want %s", got, want)
	}
	if got, want := backoff(100), monitorMaxBackoff; got != want {
		t.Fatalf("backoff(100) = %s, want cap %s", got, want)
	}
}

func TestMonitorStateKeepsLoopOutcomeSeparateFromLifecycle(t *testing.T) {
	run := monitorProvenanceRun(t)
	setLoopLabel(run, prLoopKey("acme/widgets", 42), PRLoopStateLabel, PRLoopStateApproved)
	monitor := ownedMonitor(run)

	if got, want := monitorStateForRun(run, monitor), triggersv1alpha1.PullRequestMonitorStateApproved; got != want {
		t.Fatalf("monitorStateForRun() = %q, want %q", got, want)
	}
	if got, want := intervalForLifecycle(triggersv1alpha1.PullRequestLifecycleClosed, monitor.Status.State), monitorClosedInterval; got != want {
		t.Fatalf("closed lifecycle interval = %s, want %s", got, want)
	}
}

func TestClosedMonitorRequeuesForReopening(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	run := monitorProvenanceRun(t)
	run.Annotations[PRLoopOptAnnotation] = PRLoopOptDisabled
	run.Status.Artifacts.PullRequestURL = "https://github.com/acme/widgets/pull/42"
	monitor := ownedMonitor(run)
	gh := prLoopTestRepo()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: monitorTestToken, Namespace: run.Namespace}, Data: map[string][]byte{monitorTestTokenKey: []byte("test-token")}}
	scheme := prLoopTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.PullRequestMonitor{}).WithObjects(run, monitor, gh, secret).Build()
	poller := &monitorFakePoller{pull: &polledPullRequest{Number: 42, URL: monitor.Spec.URL, State: monitorTestClosed, HeadSHA: "closed-head"}}
	reconciler := &PullRequestMonitorReconciler{Client: c, Scheme: scheme, Poller: poller, Now: func() time.Time { return now }}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(monitor)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter < monitorClosedInterval*9/10 || result.RequeueAfter > monitorClosedInterval*11/10 {
		t.Fatalf("closed requeue = %s, want jittered %s", result.RequeueAfter, monitorClosedInterval)
	}
	updated := &triggersv1alpha1.PullRequestMonitor{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(monitor), updated); err != nil {
		t.Fatalf("Get(monitor): %v", err)
	}
	if updated.Status.Lifecycle != triggersv1alpha1.PullRequestLifecycleClosed {
		t.Fatalf("lifecycle = %q, want closed", updated.Status.Lifecycle)
	}
}

func TestHeadChangeInvalidatesPreviousCIRollups(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	run := monitorProvenanceRun(t)
	run.Annotations[PRLoopOptAnnotation] = PRLoopOptDisabled
	run.Status.Artifacts.PullRequestURL = "https://github.com/acme/widgets/pull/42"
	monitor := ownedMonitor(run)
	monitor.Status.HeadSHA = monitorTestOldSHA
	monitor.Status.Checks = triggersv1alpha1.PullRequestMonitorHeadRollup{HeadSHA: monitorTestOldSHA, State: gitHubRollupSuccess, ObservedAt: metav1.NewTime(now.Add(-time.Minute))}
	monitor.Status.Statuses = triggersv1alpha1.PullRequestMonitorHeadRollup{HeadSHA: monitorTestOldSHA, State: gitHubRollupSuccess, ObservedAt: metav1.NewTime(now.Add(-time.Minute))}
	gh := prLoopTestRepo()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: monitorTestToken, Namespace: run.Namespace}, Data: map[string][]byte{monitorTestTokenKey: []byte("test-token")}}
	scheme := prLoopTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.PullRequestMonitor{}).WithObjects(run, monitor, gh, secret).Build()
	poller := &monitorFakePoller{
		pull:      &polledPullRequest{Number: 42, URL: monitor.Spec.URL, State: monitorTestOpen, HeadSHA: monitorTestNewSHA, MergeableKnown: true, Mergeable: true},
		checksErr: errors.New("checks unavailable"),
	}
	reconciler := &PullRequestMonitorReconciler{Client: c, Scheme: scheme, Poller: poller, Now: func() time.Time { return now }}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(monitor)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	updated := &triggersv1alpha1.PullRequestMonitor{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(monitor), updated); err != nil {
		t.Fatalf("Get(monitor): %v", err)
	}
	if updated.Status.Lifecycle != triggersv1alpha1.PullRequestLifecycleOpen || updated.Status.HeadSHA != monitorTestNewSHA {
		t.Fatalf("lifecycle facts = %#v", updated.Status)
	}
	if updated.Status.Checks.HeadSHA != monitorTestNewSHA || updated.Status.Checks.State != "" || updated.Status.Checks.Error == "" {
		t.Fatalf("checks after head change = %#v, want new-head error without old rollup", updated.Status.Checks)
	}
	if updated.Status.Statuses.HeadSHA != "" || updated.Status.Statuses.State != "" {
		t.Fatalf("statuses after head change = %#v, want invalidated old rollup", updated.Status.Statuses)
	}
}

func TestPullRequestMonitorReconcileStartsLoopAndPersistsFeedbackCursors(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	run := prLoopImplementerRun("")
	run.UID = types.UID("implementer-run-uid")
	run.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{PullRequestURLs: []string{"https://github.com/acme/widgets/pull/42"}}
	monitor := ownedMonitor(run)
	gh := prLoopTestRepo()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: monitorTestToken, Namespace: run.Namespace}, Data: map[string][]byte{monitorTestTokenKey: []byte("test-token")}}
	engine, c, stateStore := newPRLoopEngine(t, gh, run, monitor, secret)
	if _, err := stateStore.CreateSession(context.Background(), run.Name, run.Namespace, "succeeded", "done"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	markSucceeded(t, c, run)
	poller := &monitorFakePoller{
		pull:         &polledPullRequest{Number: 42, Title: "PR", URL: monitor.Spec.URL, State: monitorTestOpen, HeadRef: run.Name, HeadSHA: "abc123", BaseRef: monitorTestBaseRef, AuthorLogin: monitorTestAgentBot, CreatedAt: now.Add(-time.Hour)},
		pullResponse: gitHubPollResponse{StatusCode: 200, ETag: `"pull-v1"`},
		reviews:      []polledPullRequestReview{{ID: 11, State: "commented", Body: "fix the edge case", AuthorLogin: "reviewer", AuthorAssociation: "MEMBER", SubmittedAt: now.Add(-time.Minute)}},
		comments:     []polledIssueComment{{ID: 12, Body: "@agent add a regression test", AuthorLogin: "maintainer", AuthorAssociation: "MEMBER", CreatedAt: now}},
	}
	reconciler := &PullRequestMonitorReconciler{Client: c, Scheme: engine.Scheme, Engine: engine, Poller: poller, Now: func() time.Time { return now }}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(monitor)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	updated := &triggersv1alpha1.PullRequestMonitor{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(monitor), updated); err != nil {
		t.Fatalf("Get(monitor): %v", err)
	}
	if !updated.Status.OpenedDispatched || updated.Status.LastReviewCursor == nil || updated.Status.LastReviewCursor.ID != 11 || updated.Status.LastIssueCommentCursor == nil || updated.Status.LastIssueCommentCursor.ID != 12 {
		t.Fatalf("monitor status = %#v", updated.Status)
	}
	if got := len(stateStore.messagesFor(run.Name, run.Namespace)); got != 2 {
		t.Fatalf("feedback messages = %d, want 2", got)
	}
	runs := &platformv1alpha1.AgentRunList{}
	if err := c.List(context.Background(), runs, client.InNamespace(run.Namespace)); err != nil {
		t.Fatalf("List(AgentRuns): %v", err)
	}
	if len(runs.Items) != 2 {
		t.Fatalf("AgentRuns = %d, want implementer plus reviewer", len(runs.Items))
	}
	poller.reviews = nil
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(monitor)}); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if len(poller.reviewSince) != 2 || !poller.reviewSince[1].Equal(now.Add(-time.Minute)) {
		t.Fatalf("review cursors = %#v", poller.reviewSince)
	}
}

func TestExternalPREventDuplicateUsesExactTypeAndSourceID(t *testing.T) {
	run := monitorProvenanceRun(t)
	const loopKey = "acme/widgets#42"
	event := PullRequestEvent{Type: PREventReviewSubmitted, SourceID: 99}
	markExternalPREvent(run, loopKey, event)
	markExternalPREvent(run, loopKey, event)

	if got := len(externalPREventIDs(run, loopKey, event.Type)); got != 1 {
		t.Fatalf("stored event IDs after exact duplicate = %d, want 1", got)
	}
	if !externalPREventHandled(run, loopKey, event) {
		t.Fatal("exact SourceID duplicate is not marked handled")
	}
	if externalPREventHandled(run, loopKey, PullRequestEvent{Type: PREventComment, SourceID: event.SourceID}) {
		t.Fatal("same SourceID from a different collection was incorrectly deduplicated")
	}
	if externalPREventHandled(run, loopKey, PullRequestEvent{Type: PREventReviewSubmitted, SourceID: 98}) {
		t.Fatal("older out-of-order review ID was incorrectly treated as handled")
	}
}

type monitorFakePoller struct {
	calls            int
	pull             *polledPullRequest
	pullResponse     gitHubPollResponse
	pullErr          error
	reviews          []polledPullRequestReview
	reviewSince      []time.Time
	reviewResponse   gitHubPollResponse
	reviewErr        error
	comments         []polledIssueComment
	commentResponse  gitHubPollResponse
	commentErr       error
	checks           polledHeadRollup
	checksResponse   gitHubPollResponse
	checksErr        error
	statuses         polledHeadRollup
	statusesResponse gitHubPollResponse
	statusesErr      error
}

func (p *monitorFakePoller) GetPullRequest(context.Context, string, string, int, string) (*polledPullRequest, gitHubPollResponse, error) {
	p.calls++
	return p.pull, p.pullResponse, p.pullErr
}

func (p *monitorFakePoller) ListReviews(_ context.Context, _ string, _ string, _ int, since time.Time) ([]polledPullRequestReview, gitHubPollResponse, error) {
	p.calls++
	p.reviewSince = append(p.reviewSince, since)
	return p.reviews, p.reviewResponse, p.reviewErr
}

func (p *monitorFakePoller) GetReviewDecision(context.Context, string, string, int) (triggersv1alpha1.PullRequestReviewDecision, gitHubPollResponse, error) {
	p.calls++
	if p.reviewErr != nil {
		return triggersv1alpha1.PullRequestReviewDecisionUnknown, p.reviewResponse, p.reviewErr
	}
	return aggregateReviewDecision(p.reviews), p.reviewResponse, nil
}

func (p *monitorFakePoller) ListIssueComments(context.Context, string, string, int, time.Time) ([]polledIssueComment, gitHubPollResponse, error) {
	p.calls++
	return p.comments, p.commentResponse, p.commentErr
}

func (p *monitorFakePoller) ListCheckRuns(context.Context, string, string, string) (polledHeadRollup, gitHubPollResponse, error) {
	p.calls++
	if p.checks.HeadSHA == "" && p.pull != nil {
		p.checks = polledHeadRollup{HeadSHA: p.pull.HeadSHA, State: gitHubRollupSuccess, Count: 1}
	}
	return p.checks, p.checksResponse, p.checksErr
}

func (p *monitorFakePoller) GetCommitStatus(context.Context, string, string, string) (polledHeadRollup, gitHubPollResponse, error) {
	p.calls++
	if p.statuses.HeadSHA == "" && p.pull != nil {
		p.statuses = polledHeadRollup{HeadSHA: p.pull.HeadSHA, State: gitHubRollupNone}
	}
	return p.statuses, p.statusesResponse, p.statusesErr
}

func ownedMonitor(run *platformv1alpha1.AgentRun) *triggersv1alpha1.PullRequestMonitor {
	controller := true
	return &triggersv1alpha1.PullRequestMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-monitor-test",
			Namespace: run.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: platformv1alpha1.GroupVersion.String(),
				Kind:       "AgentRun",
				Name:       run.Name,
				UID:        run.UID,
				Controller: &controller,
			}},
		},
		Spec: triggersv1alpha1.PullRequestMonitorSpec{
			ImplementerRef: corev1.LocalObjectReference{Name: run.Name},
			Repository:     "acme/widgets",
			Number:         42,
			URL:            "https://github.com/acme/widgets/pull/42",
			DiscoveredAt:   metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		},
	}
}

func monitorProvenanceRun(t *testing.T) *platformv1alpha1.AgentRun {
	t.Helper()
	run := prLoopImplementerRun("in_review")
	run.Annotations[PRLoopOptAnnotation] = PRLoopOptEnabled
	run.UID = types.UID("implementer-run-uid")
	if run.UID == "" {
		t.Fatal("monitor fixture must set AgentRun UID for controller-owner provenance")
	}
	if run.Status.Artifacts == nil {
		run.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{}
	}
	return run
}
