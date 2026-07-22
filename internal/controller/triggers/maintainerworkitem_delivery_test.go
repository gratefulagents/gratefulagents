package triggers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	maintainerDeliveryTestHeadSHA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	maintainerDeliveryTestImplementer = "implementer"
	maintainerDeliveryTestItem        = "item"
	maintainerDeliveryTestRepo        = "widgets"
	maintainerDeliveryTestRunUID      = "run-uid"
)

type fakeMaintainerDeliveryClient struct {
	pulls        []*polledPullRequest
	review       triggersv1alpha1.PullRequestReviewDecision
	checks       polledHeadRollup
	statuses     polledHeadRollup
	mergeCalls   int
	mergeErr     error
	unsafePolicy bool
}

func (f *fakeMaintainerDeliveryClient) GetPullRequest(context.Context, string, string, int, string) (*polledPullRequest, gitHubPollResponse, error) {
	if len(f.pulls) == 0 {
		return nil, gitHubPollResponse{}, errors.New("no pull response")
	}
	pull := f.pulls[0]
	if pull.BaseRef == "" {
		pull.BaseRef = monitorTestBaseRef
	}
	if len(f.pulls) > 1 {
		f.pulls = f.pulls[1:]
	}
	return pull, gitHubPollResponse{}, nil
}
func (f *fakeMaintainerDeliveryClient) GetReviewDecision(context.Context, string, string, int) (triggersv1alpha1.PullRequestReviewDecision, gitHubPollResponse, error) {
	return f.review, gitHubPollResponse{}, nil
}
func (f *fakeMaintainerDeliveryClient) ListCheckRuns(context.Context, string, string, string) (polledHeadRollup, gitHubPollResponse, error) {
	return f.checks, gitHubPollResponse{}, nil
}
func (f *fakeMaintainerDeliveryClient) GetCommitStatus(context.Context, string, string, string) (polledHeadRollup, gitHubPollResponse, error) {
	return f.statuses, gitHubPollResponse{}, nil
}
func (f *fakeMaintainerDeliveryClient) GetMergePolicy(context.Context, string, string, string) (maintainerMergePolicy, error) {
	return maintainerMergePolicy{RequiredReviews: true, RequiredChecks: true, CanMerge: true, ActorCanBypass: f.unsafePolicy}, nil
}
func (f *fakeMaintainerDeliveryClient) MergePullRequest(context.Context, string, string, int, string, string) (*github.PullRequestMergeResult, error) {
	f.mergeCalls++
	return &github.PullRequestMergeResult{Merged: new(f.mergeErr == nil)}, f.mergeErr
}

func newMaintainerMergeFixture(t *testing.T) (*GitHubRepositoryReconciler, *triggersv1alpha1.GitHubRepository, *triggersv1alpha1.MaintainerWorkItem, *triggersv1alpha1.MaintainerWorkItemCommand) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	mergeable := true
	repository := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "repository", Namespace: maintainerWorkItemTestNamespace}, Spec: triggersv1alpha1.GitHubRepositorySpec{Owner: "octo", Repo: maintainerDeliveryTestRepo, Maintainer: &triggersv1alpha1.MaintainerSpec{AllowPullRequestMerge: true}}}
	item := &triggersv1alpha1.MaintainerWorkItem{ObjectMeta: metav1.ObjectMeta{Name: maintainerDeliveryTestItem, Namespace: maintainerWorkItemTestNamespace, UID: "item-uid"}, Spec: triggersv1alpha1.MaintainerWorkItemSpec{RepositoryRef: localRef(repository.Name), IssueNumber: 7}, Status: triggersv1alpha1.MaintainerWorkItemStatus{ProjectionSequence: 3, Readiness: &triggersv1alpha1.MaintainerWorkItemReadiness{ReadyToMerge: true}, PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{IntentName: "monitor", Repository: projectionTestRepository, Number: 11, HeadSHA: maintainerDeliveryTestHeadSHA, State: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, Mergeable: &mergeable, ReviewDecision: string(triggersv1alpha1.PullRequestReviewDecisionApproved), CheckState: triggersv1alpha1.MaintainerWorkItemCheckStatePassing, Fresh: true, HeadObservedAt: &now, ReviewObservedAt: &now, ChecksObservedAt: &now, StatusesObservedAt: &now}}}}
	command := &triggersv1alpha1.MaintainerWorkItemCommand{ObjectMeta: metav1.ObjectMeta{Name: "merge-command", Namespace: maintainerWorkItemTestNamespace}, Spec: triggersv1alpha1.MaintainerWorkItemCommandSpec{Preconditions: triggersv1alpha1.MaintainerWorkItemCommandPreconditions{WorkItemName: item.Name, WorkItemUID: item.UID}, Type: triggersv1alpha1.MaintainerWorkItemCommandTypeRequestMerge, RequestMerge: &triggersv1alpha1.MaintainerRequestMergeCommand{IssueNumber: 7, Repository: projectionTestRepository, PullRequestNumber: 11, ExpectedHeadSHA: maintainerDeliveryTestHeadSHA, MergeMethod: triggersv1alpha1.MaintainerWorkItemMergeMethodSquash}}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(item, command).WithObjects(repository, item, command).Build()
	return &GitHubRepositoryReconciler{Client: k8sClient, Scheme: scheme}, repository, item, command
}

func localRef(name string) corev1.LocalObjectReference {
	return corev1.LocalObjectReference{Name: name}
}

func commandPhase(t *testing.T, reconciler *GitHubRepositoryReconciler, command *triggersv1alpha1.MaintainerWorkItemCommand) triggersv1alpha1.MaintainerWorkItemCommandPhase {
	t.Helper()
	fresh := &triggersv1alpha1.MaintainerWorkItemCommand{}
	if err := reconciler.Get(context.Background(), client.ObjectKeyFromObject(command), fresh); err != nil {
		t.Fatal(err)
	}
	return fresh.Status.Phase
}

func TestRequestMergeFailsClosedOnZeroReportedChecks(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	githubClient := &fakeMaintainerDeliveryClient{pulls: []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}}, review: triggersv1alpha1.PullRequestReviewDecisionApproved, checks: polledHeadRollup{HeadSHA: head, State: gitHubRollupNone}, statuses: polledHeadRollup{HeadSHA: head, State: gitHubRollupNone}}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected || githubClient.mergeCalls != 0 {
		t.Fatalf("phase=%s mergeCalls=%d", phase, githubClient.mergeCalls)
	}
}

func TestRequestMergeRejectsHeadChangedAtPreflight(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	githubClient := &fakeMaintainerDeliveryClient{pulls: []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}, review: triggersv1alpha1.PullRequestReviewDecisionApproved, checks: polledHeadRollup{HeadSHA: head, State: gitHubRollupSuccess, Count: 1}, statuses: polledHeadRollup{HeadSHA: head, State: gitHubRollupNone}}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected || githubClient.mergeCalls != 0 {
		t.Fatalf("phase=%s mergeCalls=%d", phase, githubClient.mergeCalls)
	}
}

func TestRequestMergeRejectsBlankReviewDecision(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	githubClient := &fakeMaintainerDeliveryClient{pulls: []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}}, checks: polledHeadRollup{HeadSHA: head, State: gitHubRollupSuccess, Count: 1}}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected || githubClient.mergeCalls != 0 {
		t.Fatalf("phase=%s mergeCalls=%d", phase, githubClient.mergeCalls)
	}
}

func TestRequestMergeRejectsClosedUnmergedPullRequest(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	githubClient := &fakeMaintainerDeliveryClient{pulls: []*polledPullRequest{{State: monitorTestClosed, HeadSHA: command.Spec.RequestMerge.ExpectedHeadSHA}}}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected || githubClient.mergeCalls != 0 {
		t.Fatalf("phase=%s mergeCalls=%d", phase, githubClient.mergeCalls)
	}
}

func TestRequestMergeRejectsBypassCapableActor(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	githubClient := &fakeMaintainerDeliveryClient{pulls: []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}}, unsafePolicy: true}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected || githubClient.mergeCalls != 0 {
		t.Fatalf("phase=%s mergeCalls=%d", phase, githubClient.mergeCalls)
	}
}

func TestRequestMergeRecordsOnlyConfirmedExpectedHead(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	mergedAt := time.Now().UTC()
	githubClient := &fakeMaintainerDeliveryClient{pulls: []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}, {State: monitorTestClosed, Merged: true, MergedAt: mergedAt, HeadSHA: head}}, review: triggersv1alpha1.PullRequestReviewDecisionApproved, checks: polledHeadRollup{HeadSHA: head, State: gitHubRollupSuccess, Count: 1}, statuses: polledHeadRollup{HeadSHA: head, State: gitHubRollupNone}}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded || githubClient.mergeCalls != 1 {
		t.Fatalf("phase=%s mergeCalls=%d", phase, githubClient.mergeCalls)
	}
	fresh := &triggersv1alpha1.MaintainerWorkItem{}
	if err := reconciler.Get(context.Background(), client.ObjectKeyFromObject(item), fresh); err != nil {
		t.Fatal(err)
	}
	if len(fresh.Status.VerifiedMerges) != 1 || fresh.Status.VerifiedMerges[0].HeadSHA != head || fresh.Status.VerifiedMerges[0].MergedAt.IsZero() {
		t.Fatalf("verified merges = %#v", fresh.Status.VerifiedMerges)
	}
}

func TestRequestMergeDoesNotResubmitQueuedMerge(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	open := &polledPullRequest{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}
	githubClient := &fakeMaintainerDeliveryClient{pulls: []*polledPullRequest{open, open, open}, review: triggersv1alpha1.PullRequestReviewDecisionApproved, checks: polledHeadRollup{HeadSHA: head, State: gitHubRollupSuccess, Count: 1}, statuses: polledHeadRollup{HeadSHA: head, State: gitHubRollupNone}}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseFailed || githubClient.mergeCalls != 1 {
		t.Fatalf("queued phase=%s mergeCalls=%d", phase, githubClient.mergeCalls)
	}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, false); err != nil {
		t.Fatal(err)
	}
	if githubClient.mergeCalls != 1 {
		t.Fatalf("queued merge was resubmitted: calls=%d", githubClient.mergeCalls)
	}
}

type flakyFinalizeGitHub struct {
	*fakeMaintainerGitHub
	failClose int
}

func (f *flakyFinalizeGitHub) EditIssue(ctx context.Context, owner, repo string, number int, request *github.IssueRequest) (*github.Issue, *github.Response, error) {
	if f.failClose > 0 {
		f.failClose--
		return nil, nil, errors.New("temporary close outage")
	}
	return f.fakeMaintainerGitHub.EditIssue(ctx, owner, repo, number, request)
}

func TestFinalizeWorkItemRetriesPartialCloseWithoutLosingAttestation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	repository := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "repository", Namespace: maintainerWorkItemTestNamespace}, Spec: triggersv1alpha1.GitHubRepositorySpec{Owner: "octo", Repo: maintainerDeliveryTestRepo, Maintainer: &triggersv1alpha1.MaintainerSpec{}}}
	scope := &triggersv1alpha1.MaintainerAcceptedScope{Statement: "deliver guarded finalization", AcceptanceCriteria: []string{"verified"}}
	item := &triggersv1alpha1.MaintainerWorkItem{ObjectMeta: metav1.ObjectMeta{Name: maintainerDeliveryTestItem, Namespace: maintainerWorkItemTestNamespace, UID: "item-uid"}, Spec: triggersv1alpha1.MaintainerWorkItemSpec{RepositoryRef: localRef(repository.Name), IssueNumber: 7, AcceptedScope: scope}, Status: triggersv1alpha1.MaintainerWorkItemStatus{ProjectionSequence: 5, PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{IntentName: "monitor", Repository: projectionTestRepository, Number: 11, HeadSHA: maintainerDeliveryTestHeadSHA, State: triggersv1alpha1.MaintainerWorkItemPullRequestStateMerged, MergedAt: &now}}, AgentRuns: []triggersv1alpha1.MaintainerWorkItemAgentRunProjection{{Name: maintainerDeliveryTestImplementer, UID: maintainerDeliveryTestRunUID, Role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer, Phase: string(platformv1alpha1.AgentRunPhasePaused)}}}}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: maintainerDeliveryTestImplementer, Namespace: maintainerWorkItemTestNamespace, UID: maintainerDeliveryTestRunUID, Labels: map[string]string{triggersv1alpha1.MaintainerWorkItemNameLabelKey: item.Name, triggersv1alpha1.MaintainerWorkItemUIDLabelKey: string(item.UID)}}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhasePaused}}
	command := &triggersv1alpha1.MaintainerWorkItemCommand{ObjectMeta: metav1.ObjectMeta{Name: "finalize-command", Namespace: maintainerWorkItemTestNamespace}, Spec: triggersv1alpha1.MaintainerWorkItemCommandSpec{Preconditions: triggersv1alpha1.MaintainerWorkItemCommandPreconditions{WorkItemName: item.Name, WorkItemUID: item.UID}, Type: triggersv1alpha1.MaintainerWorkItemCommandTypeFinalizeWorkItem, Finalize: &triggersv1alpha1.MaintainerFinalizeWorkItemCommand{IssueNumber: 7, AcceptedScopeHash: maintainerAcceptedScopeHash(scope), DeliverySummary: "all accepted scope delivered", DeliveryEvidence: "PR octo/widgets#11", ImplementerRunNames: []string{maintainerDeliveryTestImplementer}}}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(item, command).WithObjects(repository, item, run, command).Build()
	reconciler := &GitHubRepositoryReconciler{Client: k8sClient, Scheme: scheme}
	delivery := &fakeMaintainerDeliveryClient{pulls: []*polledPullRequest{{Merged: true, MergedAt: now.Time, HeadSHA: item.Status.PullRequests[0].HeadSHA}}}
	issues := &flakyFinalizeGitHub{fakeMaintainerGitHub: &fakeMaintainerGitHub{issue: &github.Issue{State: new(monitorTestOpen)}}, failClose: 1}
	if err := reconciler.processMaintainerFinalizeWorkItem(context.Background(), repository, command, item, issues, delivery, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseFailed {
		t.Fatalf("first phase = %s", phase)
	}
	partial := &triggersv1alpha1.MaintainerWorkItem{}
	if err := reconciler.Get(context.Background(), client.ObjectKeyFromObject(item), partial); err != nil {
		t.Fatal(err)
	}
	if partial.Status.DeliveryAttestation == nil || partial.Status.DeliveryAttestation.RunSuccessRequestedAt == nil || partial.Status.DeliveryAttestation.CompletedAt != nil {
		t.Fatalf("partial attestation = %#v", partial.Status.DeliveryAttestation)
	}
	if err := reconciler.processMaintainerFinalizeWorkItem(context.Background(), repository, command, item, issues, delivery, false); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded {
		t.Fatalf("retry phase = %s", phase)
	}
	completed := &triggersv1alpha1.MaintainerWorkItem{}
	if err := reconciler.Get(context.Background(), client.ObjectKeyFromObject(item), completed); err != nil {
		t.Fatal(err)
	}
	if completed.Status.DeliveryAttestation.CompletedAt == nil || completed.Status.Phase != triggersv1alpha1.MaintainerWorkItemPhaseDelivered {
		t.Fatalf("completed item = %#v", completed.Status)
	}
}
