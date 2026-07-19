package triggers

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	issues, err := listOpenGitHubIssues(context.Background(), lister, "owner", "repo", logr.Discard())
	if err != nil {
		t.Fatalf("listOpenGitHubIssues() error = %v", err)
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
