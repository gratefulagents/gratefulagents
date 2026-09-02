package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
	pulls             []*polledPullRequest
	review            triggersv1alpha1.PullRequestReviewDecision
	individualReviews []polledPullRequestReview
	checks            polledHeadRollup
	statuses          polledHeadRollup
	mergeCalls        int
	mergeErr          error
	unsafePolicy      bool
	noRequiredReview  bool
	noRequiredChecks  bool
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
func (f *fakeMaintainerDeliveryClient) GetReviewDecision(_ context.Context, _, _ string, _ int, expectedHead string) (triggersv1alpha1.PullRequestReviewDecision, gitHubPollResponse, error) {
	if f.review != "" && f.review != triggersv1alpha1.PullRequestReviewDecisionUnknown {
		return f.review, gitHubPollResponse{}, nil
	}
	return individualReviewDecision(f.individualReviews, expectedHead), gitHubPollResponse{}, nil
}
func (f *fakeMaintainerDeliveryClient) ListCheckRuns(context.Context, string, string, string) (polledHeadRollup, gitHubPollResponse, error) {
	return f.checks, gitHubPollResponse{}, nil
}
func (f *fakeMaintainerDeliveryClient) GetCommitStatus(context.Context, string, string, string) (polledHeadRollup, gitHubPollResponse, error) {
	return f.statuses, gitHubPollResponse{}, nil
}
func (f *fakeMaintainerDeliveryClient) GetMergePolicy(context.Context, string, string, string) (maintainerMergePolicy, error) {
	return maintainerMergePolicy{RequiredReviews: !f.noRequiredReview, RequiredChecks: !f.noRequiredChecks, CanMerge: true, ActorCanBypass: f.unsafePolicy}, nil
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

func TestGetMergePolicyTreatsPlanLimitedBranchProtectionAsAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/octo/widgets":
			fmt.Fprint(w, `{"permissions":{"push":true}}`)
		case "/repos/octo/widgets/branches/main/protection":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"Upgrade to GitHub Pro or make this repository public to enable this feature."}`)
		default:
			t.Errorf("unexpected GitHub request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := github.NewClient(server.Client())
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	client.BaseURL = baseURL
	policy, err := newMaintainerGitHubDeliveryClient(client).GetMergePolicy(context.Background(), "octo", "widgets", "main")
	if err != nil {
		t.Fatalf("GetMergePolicy returned plan limitation: %v", err)
	}
	if !policy.CanMerge || policy.RequiredReviews || policy.RequiredChecks || policy.ActorCanBypass {
		t.Fatalf("unexpected policy: %#v", policy)
	}
}

func TestBranchProtectionAuthorizationFailureRemainsAnError(t *testing.T) {
	httpResponse := &http.Response{StatusCode: http.StatusForbidden}
	err := &github.ErrorResponse{Response: httpResponse, Message: "Resource not accessible by integration"}
	if isGitHubBranchProtectionUnavailable(err, &github.Response{Response: httpResponse}) {
		t.Fatal("ordinary authorization failure was mistaken for unavailable branch protection")
	}
}

func TestMaintainerRulesetMergeRequirements(t *testing.T) {
	pullRequestParameters := json.RawMessage(`{"required_approving_review_count":1}`)
	checkParameters := json.RawMessage(`{"required_status_checks":[{"context":"test"}],"strict_required_status_checks_policy":true}`)
	reviews, checks, err := maintainerRulesetMergeRequirements([]*github.RepositoryRule{
		{Type: "pull_request", Parameters: &pullRequestParameters},
		{Type: "required_status_checks", Parameters: &checkParameters},
	})
	if err != nil || !reviews || !checks {
		t.Fatalf("ruleset requirements: reviews=%v checks=%v err=%v", reviews, checks, err)
	}

	noReviewParameters := json.RawMessage(`{"required_approving_review_count":0}`)
	reviews, checks, err = maintainerRulesetMergeRequirements([]*github.RepositoryRule{{Type: "pull_request", Parameters: &noReviewParameters}})
	if err != nil || reviews || checks {
		t.Fatalf("zero-review ruleset requirements: reviews=%v checks=%v err=%v", reviews, checks, err)
	}
	if _, _, err := maintainerRulesetMergeRequirements([]*github.RepositoryRule{{Type: "required_status_checks"}}); err == nil {
		t.Fatal("missing ruleset parameters did not fail closed")
	}
}

func TestRequestMergeRejectsRequiredCheckThatHasNotAppeared(t *testing.T) {
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

func TestRequestMergeFullControlMergesWhenPolicyRequiresNoChecksOrReview(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	repository.Spec.Maintainer.FullControl = true
	if err := reconciler.Update(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	item.Status.Readiness.ReadyToMerge = false
	item.Status.PullRequests[0].CheckState = triggersv1alpha1.MaintainerWorkItemCheckStateNone
	item.Status.PullRequests[0].ReviewDecision = ""
	if err := reconciler.Status().Update(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	mergedAt := time.Now().UTC()
	githubClient := &fakeMaintainerDeliveryClient{
		pulls:            []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}, {State: monitorTestClosed, Merged: true, MergedAt: mergedAt, HeadSHA: head}},
		checks:           polledHeadRollup{HeadSHA: head, State: gitHubRollupNone},
		statuses:         polledHeadRollup{HeadSHA: head, State: gitHubRollupNone},
		noRequiredReview: true,
		noRequiredChecks: true,
	}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded || githubClient.mergeCalls != 1 {
		t.Fatalf("phase=%s mergeCalls=%d", phase, githubClient.mergeCalls)
	}
}

func TestRequestMergeMergesApprovedPullRequestWithoutBranchPolicy(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	mergedAt := time.Now().UTC()
	githubClient := &fakeMaintainerDeliveryClient{
		pulls:             []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}, {State: monitorTestClosed, Merged: true, MergedAt: mergedAt, HeadSHA: head}},
		individualReviews: []polledPullRequestReview{{CommitSHA: head, AuthorLogin: "reviewer", AuthorAssociation: "MEMBER", State: "APPROVED"}},
		checks:            polledHeadRollup{HeadSHA: head, State: gitHubRollupSuccess, Count: 1},
		statuses:          polledHeadRollup{HeadSHA: head, State: gitHubRollupNone},
		noRequiredReview:  true,
		noRequiredChecks:  true,
	}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded || githubClient.mergeCalls != 1 {
		t.Fatalf("phase=%s mergeCalls=%d", phase, githubClient.mergeCalls)
	}
}

// TestRequestMergeMatchesProjectionCaseInsensitively reproduces the stalled
// maintainer merge for repositories whose owner/repo contains uppercase
// characters: the pull-request monitor records a lowercased repository
// identity in the projection while the merge request carries the
// GitHubRepository spec casing. GitHub identities are case-insensitive, so the
// merge must still find the projection and succeed.
func TestRequestMergeMatchesProjectionCaseInsensitively(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	repository.Spec.Owner = "Octo"
	if err := reconciler.Update(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	// Projection keeps the monitor's lowercased identity ("octo/widgets");
	// the request carries the spec casing, as request_merge enforces.
	command.Spec.RequestMerge.Repository = "Octo/" + maintainerDeliveryTestRepo
	if err := reconciler.Update(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	mergedAt := time.Now().UTC()
	githubClient := &fakeMaintainerDeliveryClient{
		pulls:             []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}, {State: monitorTestClosed, Merged: true, MergedAt: mergedAt, HeadSHA: head}},
		individualReviews: []polledPullRequestReview{{CommitSHA: head, AuthorLogin: "reviewer", AuthorAssociation: "MEMBER", State: "APPROVED"}},
		checks:            polledHeadRollup{HeadSHA: head, State: gitHubRollupSuccess, Count: 1},
		statuses:          polledHeadRollup{HeadSHA: head, State: gitHubRollupNone},
		noRequiredReview:  true,
		noRequiredChecks:  true,
	}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded || githubClient.mergeCalls != 1 {
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

// TestRequestMergeAllowsBypassCapableActorWithoutConfiguredGates reproduces a
// standing maintainer running with the repository owner's admin token (always
// bypass-capable) against a repository with no branch protection or rulesets:
// with no server-side gates to bypass, the merge must proceed.
func TestRequestMergeAllowsBypassCapableActorWithoutConfiguredGates(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	repository.Spec.Maintainer.FullControl = true
	if err := reconciler.Update(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	mergedAt := time.Now().UTC()
	githubClient := &fakeMaintainerDeliveryClient{
		pulls:            []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}, {State: monitorTestClosed, Merged: true, MergedAt: mergedAt, HeadSHA: head}},
		checks:           polledHeadRollup{HeadSHA: head, State: gitHubRollupNone},
		statuses:         polledHeadRollup{HeadSHA: head, State: gitHubRollupNone},
		unsafePolicy:     true,
		noRequiredReview: true,
		noRequiredChecks: true,
	}
	item.Status.PullRequests[0].CheckState = triggersv1alpha1.MaintainerWorkItemCheckStateNone
	item.Status.PullRequests[0].ReviewDecision = ""
	if err := reconciler.Status().Update(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded || githubClient.mergeCalls != 1 {
		t.Fatalf("phase=%s mergeCalls=%d", phase, githubClient.mergeCalls)
	}
}

func TestRequestMergeFullControlDoesNotRequireHumanApproval(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	repository.Spec.Maintainer.AllowPullRequestMerge = false
	repository.Spec.Maintainer.FullControl = true
	item.Status.PullRequests[0].ReviewDecision = ""
	if err := reconciler.Update(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Status().Update(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	mergedAt := time.Now().UTC()
	githubClient := &fakeMaintainerDeliveryClient{pulls: []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}, {State: monitorTestClosed, Merged: true, MergedAt: mergedAt, HeadSHA: head}}, noRequiredReview: true, checks: polledHeadRollup{HeadSHA: head, State: gitHubRollupSuccess, Count: 1}, statuses: polledHeadRollup{HeadSHA: head, State: gitHubRollupNone}}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded || githubClient.mergeCalls != 1 {
		t.Fatalf("phase=%s mergeCalls=%d", phase, githubClient.mergeCalls)
	}
}

func TestRequestMergeFullControlRejectsChangesRequested(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	repository.Spec.Maintainer.FullControl = true
	item.Status.PullRequests[0].ReviewDecision = ""
	if err := reconciler.Update(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Status().Update(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	githubClient := &fakeMaintainerDeliveryClient{pulls: []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}}, review: triggersv1alpha1.PullRequestReviewDecisionChangesRequested, noRequiredReview: true}
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

// TestRequestMergeNotifiesOpenFleetPullRequestsOnce reproduces the fleet
// conflict churn: after a merge lands, sibling implementers with open PRs must
// be told by the controller to merge the base branch, exactly once, even when
// the merge command is replayed.
func TestRequestMergeNotifiesOpenFleetPullRequestsOnce(t *testing.T) {
	sibling := rebaseSibling{name: "sibling", prNumber: 83, prState: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, runName: "sibling-implementer", runPhase: platformv1alpha1.AgentRunPhaseRunning, role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer}
	reconciler, repository, item, stateStore := newRebaseFixture(t, sibling)
	ctx := context.Background()
	command := &triggersv1alpha1.MaintainerWorkItemCommand{ObjectMeta: metav1.ObjectMeta{Name: "merge-command", Namespace: maintainerWorkItemTestNamespace}, Spec: triggersv1alpha1.MaintainerWorkItemCommandSpec{Preconditions: triggersv1alpha1.MaintainerWorkItemCommandPreconditions{WorkItemName: item.Name, WorkItemUID: item.UID}, Type: triggersv1alpha1.MaintainerWorkItemCommandTypeRequestMerge, RequestMerge: &triggersv1alpha1.MaintainerRequestMergeCommand{IssueNumber: 7, Repository: projectionTestRepository, PullRequestNumber: 11, ExpectedHeadSHA: rebaseTestMergedHead, MergeMethod: triggersv1alpha1.MaintainerWorkItemMergeMethodSquash}}}
	if err := reconciler.Create(ctx, command); err != nil {
		t.Fatal(err)
	}
	head := rebaseTestMergedHead
	mergedAt := time.Now().UTC()
	githubClient := &fakeMaintainerDeliveryClient{
		pulls:             []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head, BaseRef: "develop"}, {State: monitorTestClosed, Merged: true, MergedAt: mergedAt, HeadSHA: head, BaseRef: "develop"}},
		individualReviews: []polledPullRequestReview{{CommitSHA: head, AuthorLogin: "reviewer", AuthorAssociation: "MEMBER", State: "APPROVED"}},
		checks:            polledHeadRollup{HeadSHA: head, State: gitHubRollupSuccess, Count: 1},
		statuses:          polledHeadRollup{HeadSHA: head, State: gitHubRollupNone},
		noRequiredReview:  true,
		noRequiredChecks:  true,
	}
	if err := reconciler.processMaintainerRequestMerge(ctx, repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded || githubClient.mergeCalls != 1 {
		t.Fatalf("phase=%s mergeCalls=%d", phase, githubClient.mergeCalls)
	}
	messages := rebaseMessagesFor(t, stateStore, sibling.runName)
	if len(messages) != 1 {
		t.Fatalf("expected one rebase notification after merge, got %d: %v", len(messages), messages)
	}
	if !strings.Contains(messages[0], "origin/develop") || !strings.Contains(messages[0], "PR #83") {
		t.Fatalf("notification must name the merged base branch and the sibling PR: %s", messages[0])
	}
	// Replaying the completed command takes the already-verified path and must
	// not notify again.
	if err := reconciler.processMaintainerRequestMerge(ctx, repository, command, item, githubClient, false); err != nil {
		t.Fatal(err)
	}
	if messages := rebaseMessagesFor(t, stateStore, sibling.runName); len(messages) != 1 {
		t.Fatalf("replay must not re-notify, got %d messages", len(messages))
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
	command := &triggersv1alpha1.MaintainerWorkItemCommand{ObjectMeta: metav1.ObjectMeta{Name: "finalize-command", Namespace: maintainerWorkItemTestNamespace}, Spec: triggersv1alpha1.MaintainerWorkItemCommandSpec{HumanIssuer: &triggersv1alpha1.MaintainerWorkItemCommandHumanIssuer{Subject: "user:dashboard-alice", Proof: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Preconditions: triggersv1alpha1.MaintainerWorkItemCommandPreconditions{WorkItemName: item.Name, WorkItemUID: item.UID}, Type: triggersv1alpha1.MaintainerWorkItemCommandTypeFinalizeWorkItem, Finalize: &triggersv1alpha1.MaintainerFinalizeWorkItemCommand{IssueNumber: 7, AcceptedScopeHash: triggersv1alpha1.MaintainerAcceptedScopeHash(scope), DeliverySummary: "all accepted scope delivered", DeliveryEvidence: "PR octo/widgets#11", ImplementerRunNames: []string{maintainerDeliveryTestImplementer}}}}
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
	if partial.Status.DeliveryAttestation.Issuer != nil || partial.Status.DeliveryAttestation.HumanIssuer == nil || partial.Status.DeliveryAttestation.HumanIssuer.Subject != "user:dashboard-alice" {
		t.Fatalf("finalization issuer = %#v", partial.Status.DeliveryAttestation)
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

func TestHumanRequestMergeRejectedWithoutMergePermission(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	repository.Spec.Maintainer.AllowPullRequestMerge = false
	if err := reconciler.Update(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	command.Spec.HumanIssuer = &triggersv1alpha1.MaintainerWorkItemCommandHumanIssuer{Subject: "user:dashboard-alice", Proof: ""}
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	mergedAt := time.Now().UTC()
	githubClient := &fakeMaintainerDeliveryClient{
		pulls:    []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}, {State: monitorTestClosed, Merged: true, MergedAt: mergedAt, HeadSHA: head}},
		review:   triggersv1alpha1.PullRequestReviewDecisionApproved,
		checks:   polledHeadRollup{HeadSHA: head, State: gitHubRollupSuccess, Count: 1},
		statuses: polledHeadRollup{HeadSHA: head, State: gitHubRollupNone},
	}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected {
		t.Fatalf("phase=%s; human merge without AllowPullRequestMerge should be rejected", phase)
	}
}

func TestAgentRunRequestMergeRejectedWithoutMergePermission(t *testing.T) {
	reconciler, repository, item, command := newMaintainerMergeFixture(t)
	repository.Spec.Maintainer.AllowPullRequestMerge = false
	if err := reconciler.Update(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	// command has no HumanIssuer, so the agent delegation check applies.
	head := command.Spec.RequestMerge.ExpectedHeadSHA
	githubClient := &fakeMaintainerDeliveryClient{
		pulls:    []*polledPullRequest{{State: monitorTestOpen, MergeableKnown: true, Mergeable: true, HeadSHA: head}},
		review:   triggersv1alpha1.PullRequestReviewDecisionApproved,
		checks:   polledHeadRollup{HeadSHA: head, State: gitHubRollupSuccess, Count: 1},
		statuses: polledHeadRollup{HeadSHA: head, State: gitHubRollupNone},
	}
	if err := reconciler.processMaintainerRequestMerge(context.Background(), repository, command, item, githubClient, true); err != nil {
		t.Fatal(err)
	}
	if phase := commandPhase(t, reconciler, command); phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected {
		t.Fatalf("phase=%s; agent merge without AllowPullRequestMerge should be rejected", phase)
	}
}
