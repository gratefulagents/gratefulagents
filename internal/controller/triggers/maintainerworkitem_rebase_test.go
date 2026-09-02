package triggers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	rebaseTestMergedHead  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	rebaseTestSiblingHead = "cccccccccccccccccccccccccccccccccccccccc"
)

type rebaseSibling struct {
	name     string
	prNumber int32
	prState  triggersv1alpha1.MaintainerWorkItemPullRequestState
	mergedAt *metav1.Time
	runName  string
	runPhase platformv1alpha1.AgentRunPhase
	role     triggersv1alpha1.MaintainerWorkItemAgentRunRole
	// repositoryRef overrides the bound GitHubRepository name when set.
	repositoryRef string
}

func newRebaseSiblingObjects(repositoryName string, s rebaseSibling) (*triggersv1alpha1.MaintainerWorkItem, *platformv1alpha1.AgentRun) {
	if s.repositoryRef != "" {
		repositoryName = s.repositoryRef
	}
	item := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{Name: s.name, Namespace: maintainerWorkItemTestNamespace},
		Spec:       triggersv1alpha1.MaintainerWorkItemSpec{RepositoryRef: localRef(repositoryName), IssueNumber: 100 + s.prNumber},
		Status: triggersv1alpha1.MaintainerWorkItemStatus{
			PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{IntentName: "monitor", Repository: projectionTestRepository, Number: s.prNumber, HeadSHA: rebaseTestSiblingHead, State: s.prState, MergedAt: s.mergedAt}},
			AgentRuns:    []triggersv1alpha1.MaintainerWorkItemAgentRunProjection{{Name: s.runName, Role: s.role, Phase: string(s.runPhase)}},
		},
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: s.runName, Namespace: maintainerWorkItemTestNamespace},
		Status:     platformv1alpha1.AgentRunStatus{Phase: s.runPhase},
	}
	return item, run
}

func newRebaseFixture(t *testing.T, siblings ...rebaseSibling) (*GitHubRepositoryReconciler, *triggersv1alpha1.GitHubRepository, *triggersv1alpha1.MaintainerWorkItem, *prLoopStateStore) {
	t.Helper()
	scheme := prLoopTestScheme(t)
	repository := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "repository", Namespace: maintainerWorkItemTestNamespace}, Spec: triggersv1alpha1.GitHubRepositorySpec{Owner: "octo", Repo: maintainerDeliveryTestRepo, Maintainer: &triggersv1alpha1.MaintainerSpec{AllowPullRequestMerge: true}}}
	now := metav1.Now()
	mergeable := true
	merged := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{Name: maintainerDeliveryTestItem, Namespace: maintainerWorkItemTestNamespace, UID: "item-uid"},
		Spec:       triggersv1alpha1.MaintainerWorkItemSpec{RepositoryRef: localRef(repository.Name), IssueNumber: 7},
		Status: triggersv1alpha1.MaintainerWorkItemStatus{
			ProjectionSequence: 3,
			Readiness:          &triggersv1alpha1.MaintainerWorkItemReadiness{ReadyToMerge: true},
			PullRequests:       []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{IntentName: "monitor", Repository: projectionTestRepository, Number: 11, HeadSHA: rebaseTestMergedHead, State: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, Mergeable: &mergeable, ReviewDecision: string(triggersv1alpha1.PullRequestReviewDecisionApproved), CheckState: triggersv1alpha1.MaintainerWorkItemCheckStatePassing, Fresh: true, HeadObservedAt: &now, ReviewObservedAt: &now, ChecksObservedAt: &now, StatusesObservedAt: &now}},
			AgentRuns:          []triggersv1alpha1.MaintainerWorkItemAgentRunProjection{{Name: "merged-implementer", Role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer, Phase: string(platformv1alpha1.AgentRunPhaseRunning)}},
		},
	}
	mergedRun := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "merged-implementer", Namespace: maintainerWorkItemTestNamespace}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning}}
	objects := make([]client.Object, 0, 3+2*len(siblings))
	objects = append(objects, repository, merged, mergedRun)
	stateStore := newPRLoopStateStore()
	if _, err := stateStore.CreateSession(context.Background(), mergedRun.Name, mergedRun.Namespace, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, s := range siblings {
		item, run := newRebaseSiblingObjects(repository.Name, s)
		objects = append(objects, item, run)
		if _, err := stateStore.CreateSession(context.Background(), run.Name, run.Namespace, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}, &triggersv1alpha1.MaintainerWorkItem{}, &triggersv1alpha1.MaintainerWorkItemCommand{}).WithObjects(objects...).Build()
	return &GitHubRepositoryReconciler{Client: k8sClient, Scheme: scheme, StateStore: stateStore}, repository, merged, stateStore
}

func rebaseMessagesFor(t *testing.T, stateStore *prLoopStateStore, runName string) []string {
	t.Helper()
	session, err := stateStore.GetSessionByRun(context.Background(), runName, maintainerWorkItemTestNamespace)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := stateStore.GetMessages(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Content)
	}
	return contents
}

func rebaseDeliveryID(t *testing.T, stateStore *prLoopStateStore, runName string) string {
	t.Helper()
	session, err := stateStore.GetSessionByRun(context.Background(), runName, maintainerWorkItemTestNamespace)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := stateStore.GetMessages(context.Background(), session.ID)
	if err != nil || len(messages) == 0 {
		t.Fatalf("expected at least one message: %v", err)
	}
	var metadata struct {
		DeliveryID string `json:"delivery_id"`
	}
	if err := json.Unmarshal(messages[0].Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	return metadata.DeliveryID
}

func TestNotifyOpenFleetPullRequestsAfterMergeDeliversOnceToActiveImplementer(t *testing.T) {
	sibling := rebaseSibling{name: "sibling", prNumber: 83, prState: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, runName: "sibling-implementer", runPhase: platformv1alpha1.AgentRunPhaseRunning, role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer}
	reconciler, repository, merged, stateStore := newRebaseFixture(t, sibling)
	ctx := context.Background()

	reconciler.notifyOpenFleetPullRequestsAfterMerge(ctx, repository, merged, projectionTestRepository, 11, rebaseTestMergedHead, "main")
	reconciler.notifyOpenFleetPullRequestsAfterMerge(ctx, repository, merged, projectionTestRepository, 11, rebaseTestMergedHead, "main")

	messages := rebaseMessagesFor(t, stateStore, sibling.runName)
	if len(messages) != 1 {
		t.Fatalf("expected exactly one rebase message, got %d: %v", len(messages), messages)
	}
	for _, want := range []string{"[maintainer]", "PR #11 merged at bbbbbbbbbbbb into main", "PR #83 (head cccccccccccc)", "git_merge", "origin/main", "git_push", "Do not open a new PR"} {
		if !strings.Contains(messages[0], want) {
			t.Fatalf("message missing %q: %s", want, messages[0])
		}
	}
	if got, want := rebaseDeliveryID(t, stateStore, sibling.runName), "maintainer-rebase-bbbbbbbbbbbb-83"; got != want {
		t.Fatalf("delivery_id = %q, want %q", got, want)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := reconciler.Get(ctx, client.ObjectKey{Namespace: maintainerWorkItemTestNamespace, Name: sibling.runName}, run); err != nil {
		t.Fatal(err)
	}
	if run.Annotations[orchestration.LastWakeDeliveryAnnotation] != "maintainer-rebase-bbbbbbbbbbbb-83" {
		t.Fatalf("expected wake delivery annotation, got %v", run.Annotations)
	}
	if run.Spec.WakeRequests != 0 {
		t.Fatalf("Running implementer must only receive the enqueued message, got wakeRequests=%d", run.Spec.WakeRequests)
	}
	if merged := rebaseMessagesFor(t, stateStore, "merged-implementer"); len(merged) != 0 {
		t.Fatalf("merged item's implementer must not be notified, got %v", merged)
	}
}

func TestNotifyOpenFleetPullRequestsAfterMergeWakesPausedImplementer(t *testing.T) {
	sibling := rebaseSibling{name: "sibling", prNumber: 84, prState: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, runName: "paused-implementer", runPhase: platformv1alpha1.AgentRunPhasePaused, role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer}
	reconciler, repository, merged, stateStore := newRebaseFixture(t, sibling)
	ctx := context.Background()

	reconciler.notifyOpenFleetPullRequestsAfterMerge(ctx, repository, merged, projectionTestRepository, 11, rebaseTestMergedHead, "")

	messages := rebaseMessagesFor(t, stateStore, sibling.runName)
	if len(messages) != 1 {
		t.Fatalf("expected exactly one rebase message, got %d", len(messages))
	}
	if !strings.Contains(messages[0], "origin/main") {
		t.Fatalf("expected default base branch fallback in message: %s", messages[0])
	}
	run := &platformv1alpha1.AgentRun{}
	if err := reconciler.Get(ctx, client.ObjectKey{Namespace: maintainerWorkItemTestNamespace, Name: sibling.runName}, run); err != nil {
		t.Fatal(err)
	}
	if run.Spec.WakeRequests != 1 {
		t.Fatalf("Paused implementer must be woken, got wakeRequests=%d", run.Spec.WakeRequests)
	}
}

func TestNotifyOpenFleetPullRequestsAfterMergeSkipsIneligibleSiblings(t *testing.T) {
	now := metav1.Now()
	siblings := []rebaseSibling{
		{name: "merged-pr", prNumber: 21, prState: triggersv1alpha1.MaintainerWorkItemPullRequestStateMerged, mergedAt: &now, runName: "merged-pr-implementer", runPhase: platformv1alpha1.AgentRunPhaseRunning, role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer},
		{name: "closed-pr", prNumber: 22, prState: triggersv1alpha1.MaintainerWorkItemPullRequestStateClosed, runName: "closed-pr-implementer", runPhase: platformv1alpha1.AgentRunPhaseRunning, role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer},
		{name: "succeeded-run", prNumber: 23, prState: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, runName: "succeeded-implementer", runPhase: platformv1alpha1.AgentRunPhaseSucceeded, role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer},
		{name: "failed-run", prNumber: 24, prState: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, runName: "failed-implementer", runPhase: platformv1alpha1.AgentRunPhaseFailed, role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer},
		{name: "reviewer-only", prNumber: 25, prState: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, runName: "reviewer", runPhase: platformv1alpha1.AgentRunPhaseRunning, role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleReviewer},
	}
	reconciler, repository, merged, stateStore := newRebaseFixture(t, siblings...)

	reconciler.notifyOpenFleetPullRequestsAfterMerge(context.Background(), repository, merged, projectionTestRepository, 11, rebaseTestMergedHead, "main")

	for _, s := range siblings {
		if messages := rebaseMessagesFor(t, stateStore, s.runName); len(messages) != 0 {
			t.Fatalf("sibling %s must not be notified, got %v", s.name, messages)
		}
	}
}

func TestNotifyOpenFleetPullRequestsAfterMergeSkipsOtherRepositories(t *testing.T) {
	sibling := rebaseSibling{name: "sibling", prNumber: 83, prState: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, runName: "sibling-implementer", runPhase: platformv1alpha1.AgentRunPhaseRunning, role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer, repositoryRef: "other-repository"}
	reconciler, repository, merged, stateStore := newRebaseFixture(t, sibling)

	reconciler.notifyOpenFleetPullRequestsAfterMerge(context.Background(), repository, merged, projectionTestRepository, 11, rebaseTestMergedHead, "main")

	if messages := rebaseMessagesFor(t, stateStore, sibling.runName); len(messages) != 0 {
		t.Fatalf("work item bound to another repository must not be notified, got %v", messages)
	}
}

func TestNotifyOpenFleetPullRequestsAfterMergeSkipsWithoutStateStore(t *testing.T) {
	sibling := rebaseSibling{name: "sibling", prNumber: 83, prState: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, runName: "sibling-implementer", runPhase: platformv1alpha1.AgentRunPhaseRunning, role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer}
	reconciler, repository, merged, stateStore := newRebaseFixture(t, sibling)
	reconciler.StateStore = nil

	reconciler.notifyOpenFleetPullRequestsAfterMerge(context.Background(), repository, merged, projectionTestRepository, 11, rebaseTestMergedHead, "main")

	if messages := rebaseMessagesFor(t, stateStore, sibling.runName); len(messages) != 0 {
		t.Fatalf("nil state store must skip notification, got %v", messages)
	}
}
