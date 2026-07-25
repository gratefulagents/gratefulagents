package triggers

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeGitHubIssueLister struct {
	pages []githubIssuePage
	seen  []int
}

type githubIssuePage struct {
	issues   []*github.Issue
	nextPage int
}

func (f *fakeGitHubIssueLister) ListByRepo(ctx context.Context, owner, repo string, opts *github.IssueListByRepoOptions) ([]*github.Issue, *github.Response, error) {
	f.seen = append(f.seen, opts.Page)
	idx := opts.Page - 1
	if idx < 0 || idx >= len(f.pages) {
		return nil, &github.Response{}, nil
	}
	return f.pages[idx].issues, &github.Response{NextPage: f.pages[idx].nextPage}, nil
}

func TestGitHubIssueUserRequestAutoCloseDirective(t *testing.T) {
	t.Parallel()

	const issue = "# GitHub Issue #42: Fix the widget\n\nThe widget is broken."
	tests := []struct {
		name             string
		includeAutoClose bool
		want             string
	}{
		{
			name:             "direct issue trigger",
			includeAutoClose: true,
			want:             issue + "\n\nWhen creating a pull request for this work, include `Closes #42` in the PR description so GitHub automatically closes the issue.",
		},
		{
			name:             "maintainer dispatch",
			includeAutoClose: false,
			want:             issue,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := githubIssueUserRequest(42, "Fix the widget", "The widget is broken.", tt.includeAutoClose); got != tt.want {
				t.Fatalf("githubIssueUserRequest() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGHIssueNameAddsHashWhenTruncated(t *testing.T) {
	t.Parallel()

	owner := "very-long-owner-name-that-forces-truncation"
	repo := "very-long-repository-name-that-also-forces-truncation"
	first := ghIssueName(owner, repo, "1234567890")
	second := ghIssueName(owner, repo, "1234567891")

	if len(first) > 63 {
		t.Fatalf("first name length = %d, want <= 63: %q", len(first), first)
	}
	if len(second) > 63 {
		t.Fatalf("second name length = %d, want <= 63: %q", len(second), second)
	}
	if first == second {
		t.Fatalf("ghIssueName collision: %q", first)
	}
}

func TestCreateAgentRunRejectsInstructionsConfigMapContentCollision(t *testing.T) {
	t.Parallel()
	scheme := maintainerWorkItemScheme(t)
	gh := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "repo", Namespace: "default", UID: "repo-uid"}, Spec: triggersv1alpha1.GitHubRepositorySpec{
		Owner: "owner", Repo: "repo",
		Defaults: triggersv1alpha1.AgentRunDefaults{Model: "claude-sonnet", RepoURL: "https://github.com/owner/repo.git", CustomInstructions: "trusted instructions"},
	}}
	runName := ghIssueName(gh.Spec.Owner, gh.Spec.Repo, "1")
	collision := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: runName + "-instructions", Namespace: gh.Namespace}, Data: map[string]string{"instructions.md": "injected instructions"}}
	c := buildTriggerFakeClient(fake.NewClientBuilder().WithScheme(scheme).WithObjects(collision))
	reconciler := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}

	if _, err := reconciler.createAgentRun(context.Background(), gh, "1", 1, "https://github.com/owner/repo/issues/1", "body", "author", nil); err == nil {
		t.Fatal("instructions ConfigMap content collision was accepted")
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: runName, Namespace: gh.Namespace}, &platformv1alpha1.AgentRun{}); err == nil {
		t.Fatal("AgentRun was created after instructions collision")
	}
}

func TestAgentRunAuthorizationIntentRecoversOnlyMatchingPendingRun(t *testing.T) {
	t.Parallel()
	controller := true
	repository := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "repo", Namespace: "default", UID: "repo-uid"}}
	item := &triggersv1alpha1.MaintainerWorkItem{ObjectMeta: metav1.ObjectMeta{
		Name: "repo-issue-1", Namespace: repository.Namespace, UID: "item-uid",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: "GitHubRepository", Name: repository.Name, UID: repository.UID, Controller: &controller}},
	}, Spec: triggersv1alpha1.MaintainerWorkItemSpec{RepositoryRef: corev1.LocalObjectReference{Name: repository.Name}, IssueNumber: 1}}
	scheme := maintainerWorkItemScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(repository, item).Build()
	reconciler := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}
	annotations, err := reconciler.createAgentRunAuthorizationIntent(context.Background(), repository, item, "run", "issue-event")
	if err != nil {
		t.Fatal(err)
	}
	annotations[platformv1alpha1.AuthorizationPendingAnnotation] = "true"
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: repository.Namespace, UID: "run-uid", Annotations: annotations}}
	if err := reconciler.claimAgentRunAuthorizationIntent(context.Background(), repository, item, run, "issue-event"); err != nil {
		t.Fatalf("valid recovery intent rejected: %v", err)
	}
	impostor := run.DeepCopy()
	impostor.UID = "replacement-run-uid"
	if err := reconciler.claimAgentRunAuthorizationIntent(context.Background(), repository, item, impostor, "issue-event"); err == nil {
		t.Fatal("consumed intent was replayed for a replacement AgentRun UID")
	}
	run.Annotations[agentRunAuthorizationProofAnnotation] = "forged"
	if err := reconciler.claimAgentRunAuthorizationIntent(context.Background(), repository, item, run, "issue-event"); err == nil {
		t.Fatal("forged recovery intent was accepted")
	}
}

func TestBindAuthorizedAgentRunRecordsControllerIssuedUIDBinding(t *testing.T) {
	t.Parallel()
	controller := true
	repository := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "repo", Namespace: "default", UID: types.UID("repo-uid")}}
	item := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{
			Name: "repo-issue-1", Namespace: repository.Namespace, UID: types.UID("item-uid"),
			OwnerReferences: []metav1.OwnerReference{{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: "GitHubRepository", Name: repository.Name, UID: repository.UID, Controller: &controller}},
		},
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{RepositoryRef: corev1.LocalObjectReference{Name: repository.Name}, IssueNumber: 1},
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "implementer", Namespace: repository.Namespace, UID: types.UID("run-uid")}}
	c := fake.NewClientBuilder().WithScheme(maintainerWorkItemScheme(t)).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}).WithObjects(repository, item, run).Build()
	reconciler := &GitHubRepositoryReconciler{Client: c}

	if err := reconciler.bindAuthorizedAgentRun(context.Background(), repository, item, run); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.bindAuthorizedAgentRun(context.Background(), repository, item, run); err != nil {
		t.Fatalf("idempotent bind failed: %v", err)
	}
	updated := &triggersv1alpha1.MaintainerWorkItem{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(item), updated); err != nil {
		t.Fatal(err)
	}
	if len(updated.Status.AuthorizedAgentRuns) != 1 || updated.Status.AuthorizedAgentRuns[0].Name != run.Name || updated.Status.AuthorizedAgentRuns[0].UID != run.UID {
		t.Fatalf("authorized bindings = %#v", updated.Status.AuthorizedAgentRuns)
	}
	if err := reconciler.requireAuthorizedAgentRun(context.Background(), repository, item, run); err != nil {
		t.Fatalf("exact existing binding rejected: %v", err)
	}
	impostor := run.DeepCopy()
	impostor.UID = "other-run-uid"
	if err := reconciler.requireAuthorizedAgentRun(context.Background(), repository, item, impostor); err == nil {
		t.Fatal("same-name run with an unbound UID was authorized")
	}
}

func TestBindAuthorizedAgentRunRejectsForeignWorkItemOwner(t *testing.T) {
	t.Parallel()
	controller := true
	repository := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "repo", Namespace: "default", UID: types.UID("repo-uid")}}
	item := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{
			Name: "repo-issue-1", Namespace: repository.Namespace, UID: types.UID("item-uid"),
			OwnerReferences: []metav1.OwnerReference{{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: "GitHubRepository", Name: repository.Name, UID: types.UID("foreign-uid"), Controller: &controller}},
		},
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{RepositoryRef: corev1.LocalObjectReference{Name: repository.Name}, IssueNumber: 1},
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "implementer", Namespace: repository.Namespace, UID: types.UID("run-uid")}}
	c := fake.NewClientBuilder().WithScheme(maintainerWorkItemScheme(t)).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}).WithObjects(repository, item, run).Build()

	err := (&GitHubRepositoryReconciler{Client: c}).bindAuthorizedAgentRun(context.Background(), repository, item, run)
	if err == nil {
		t.Fatal("foreign work-item owner was authorized")
	}
}

func TestListOpenGitHubIssuesConsumesMultiplePages(t *testing.T) {
	t.Parallel()

	firstNumber := 1
	secondNumber := 2
	lister := &fakeGitHubIssueLister{
		pages: []githubIssuePage{
			{issues: []*github.Issue{{Number: &firstNumber}}, nextPage: 2},
			{issues: []*github.Issue{{Number: &secondNumber}}},
		},
	}

	issues, complete, err := listOpenGitHubIssues(context.Background(), lister, "owner", "repo", logr.Discard())
	if err != nil {
		t.Fatalf("listOpenGitHubIssues() error = %v", err)
	}
	if !complete {
		t.Fatal("complete listing reported incomplete")
	}
	if len(issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2", len(issues))
	}
	if issues[0].GetNumber() != firstNumber || issues[1].GetNumber() != secondNumber {
		t.Fatalf("issues = %#v, want both pages in order", issues)
	}
	if len(lister.seen) != 2 || lister.seen[0] != 1 || lister.seen[1] != 2 {
		t.Fatalf("seen pages = %#v, want [1 2]", lister.seen)
	}
}

func TestListOpenGitHubIssuesReportsPaginationCapIncomplete(t *testing.T) {
	t.Parallel()

	issueNumber := 1
	batch := make([]*github.Issue, maxGitHubIssues)
	for i := range batch {
		batch[i] = &github.Issue{Number: &issueNumber}
	}
	lister := &fakeGitHubIssueLister{pages: []githubIssuePage{{issues: batch, nextPage: 2}}}

	issues, complete, err := listOpenGitHubIssues(context.Background(), lister, maintainerWorkItemTestOwner, maintainerWorkItemTestRepo, logr.Discard())
	if err != nil {
		t.Fatalf("listOpenGitHubIssues() error = %v", err)
	}
	if complete || len(issues) != maxGitHubIssues {
		t.Fatalf("complete=%t len(issues)=%d, want false and %d", complete, len(issues), maxGitHubIssues)
	}

	completeLister := &fakeGitHubIssueLister{pages: []githubIssuePage{{issues: batch}}}
	issues, complete, err = listOpenGitHubIssues(context.Background(), completeLister, maintainerWorkItemTestOwner, maintainerWorkItemTestRepo, logr.Discard())
	if err != nil {
		t.Fatalf("listOpenGitHubIssues() exact-cap error = %v", err)
	}
	if !complete || len(issues) != maxGitHubIssues {
		t.Fatalf("exact-cap complete=%t len(issues)=%d, want true and %d", complete, len(issues), maxGitHubIssues)
	}
}

func TestGitHubRepositoryPollSkipsProcessedIssueWithoutLiveRun(t *testing.T) {
	t.Parallel()
	scheme := prLoopTestScheme(t)
	gh := prLoopTestRepo()
	gh.Status.ProcessedIssueIDs = []string{"42"}
	mode := &platformv1alpha1.ModeTemplate{ObjectMeta: metav1.ObjectMeta{Name: "bug"}}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.GitHubRepository{}).
		WithObjects(gh, mode).
		Build()
	r := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}
	number := 42
	title := "Fix bug"
	body := "details"
	author := "human"
	assoc := "MEMBER"
	label := "bug"
	_, err := r.syncGitHubIssues(context.Background(), gh, []*github.Issue{{
		Number:            &number,
		Title:             &title,
		Body:              &body,
		User:              &github.User{Login: &author},
		AuthorAssociation: &assoc,
		Labels:            []*github.Label{{Name: &label}},
	}})
	if err != nil {
		t.Fatalf("syncGitHubIssues() error = %v", err)
	}
	runs := &platformv1alpha1.AgentRunList{}
	if err := c.List(context.Background(), runs, client.InNamespace("default")); err != nil {
		t.Fatalf("list AgentRuns: %v", err)
	}
	if len(runs.Items) != 0 {
		t.Fatalf("AgentRuns = %d, want 0", len(runs.Items))
	}
}

func TestPullRequestMonitorProjectionChangesEnqueueMaintainedRepository(t *testing.T) {
	monitor := &triggersv1alpha1.PullRequestMonitor{
		ObjectMeta: metav1.ObjectMeta{Name: "monitor", Namespace: "default"},
		Spec: triggersv1alpha1.PullRequestMonitorSpec{
			GitHubRepositoryRef: &corev1.LocalObjectReference{Name: "repository"},
		},
		Status: triggersv1alpha1.PullRequestMonitorStatus{
			HeadSHA:        "head",
			ReviewDecision: triggersv1alpha1.PullRequestReviewDecisionUnknown,
			Checks:         triggersv1alpha1.PullRequestMonitorHeadRollup{HeadSHA: "head", State: gitHubRollupPending, Count: 1},
		},
	}
	requests := (&GitHubRepositoryReconciler{}).mapPullRequestMonitorToRepository(context.Background(), monitor)
	if len(requests) != 1 || requests[0].Name != "repository" || requests[0].Namespace != "default" {
		t.Fatalf("mapped requests = %#v", requests)
	}

	reviewed := monitor.DeepCopy()
	reviewed.Status.ReviewDecision = triggersv1alpha1.PullRequestReviewDecisionApproved
	if !pullRequestMonitorProjectionChanged(monitor, reviewed) {
		t.Fatal("review decision change did not enqueue work-item projection")
	}
	checked := monitor.DeepCopy()
	checked.Status.Checks.State = gitHubRollupSuccess
	if !pullRequestMonitorProjectionChanged(monitor, checked) {
		t.Fatal("check rollup change did not enqueue work-item projection")
	}
	reviewFeedback := monitor.DeepCopy()
	reviewFeedback.Status.LastReviewCursor = &triggersv1alpha1.GitHubObjectCursor{ID: 41}
	if !pullRequestMonitorProjectionChanged(monitor, reviewFeedback) {
		t.Fatal("new review did not enqueue work-item projection")
	}
	commentFeedback := monitor.DeepCopy()
	commentFeedback.Status.LastIssueCommentCursor = &triggersv1alpha1.GitHubObjectCursor{ID: 42}
	if !pullRequestMonitorProjectionChanged(monitor, commentFeedback) {
		t.Fatal("new PR comment did not enqueue work-item projection")
	}
	heartbeat := monitor.DeepCopy()
	heartbeat.Status.Checks.ObservedAt = metav1.Now()
	if pullRequestMonitorProjectionChanged(monitor, heartbeat) {
		t.Fatal("observation heartbeat alone enqueued a semantic projection")
	}
}
