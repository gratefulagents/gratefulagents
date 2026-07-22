package triggers

import (
	"context"
	"strings"

	"github.com/google/go-github/v68/github"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

// maintainerGitHubDeliveryClient is the narrow, replaceable GitHub surface used
// immediately around irreversible maintainer delivery commands. It deliberately
// re-reads every gate instead of trusting an asynchronous projection.
type maintainerMergePolicy struct {
	RequiredReviews bool
	RequiredChecks  bool
	CanMerge        bool
	ActorCanBypass  bool
}

type maintainerGitHubDeliveryClient interface {
	GetPullRequest(context.Context, string, string, int, string) (*polledPullRequest, gitHubPollResponse, error)
	GetReviewDecision(context.Context, string, string, int) (triggersv1alpha1.PullRequestReviewDecision, gitHubPollResponse, error)
	ListCheckRuns(context.Context, string, string, string) (polledHeadRollup, gitHubPollResponse, error)
	GetCommitStatus(context.Context, string, string, string) (polledHeadRollup, gitHubPollResponse, error)
	GetMergePolicy(context.Context, string, string, string) (maintainerMergePolicy, error)
	MergePullRequest(context.Context, string, string, int, string, string) (*github.PullRequestMergeResult, error)
}

type goGitHubMaintainerDeliveryClient struct {
	poller       pullRequestGitHubPoller
	pulls        *github.PullRequestsService
	repositories *github.RepositoriesService
}

func newMaintainerGitHubDeliveryClient(client *github.Client) maintainerGitHubDeliveryClient {
	return &goGitHubMaintainerDeliveryClient{poller: newPullRequestGitHubPoller(client), pulls: client.PullRequests, repositories: client.Repositories}
}

func (c *goGitHubMaintainerDeliveryClient) GetPullRequest(ctx context.Context, owner, repo string, number int, etag string) (*polledPullRequest, gitHubPollResponse, error) {
	return c.poller.GetPullRequest(ctx, owner, repo, number, etag)
}

func (c *goGitHubMaintainerDeliveryClient) GetReviewDecision(ctx context.Context, owner, repo string, number int) (triggersv1alpha1.PullRequestReviewDecision, gitHubPollResponse, error) {
	return c.poller.GetReviewDecision(ctx, owner, repo, number)
}

func (c *goGitHubMaintainerDeliveryClient) ListCheckRuns(ctx context.Context, owner, repo, head string) (polledHeadRollup, gitHubPollResponse, error) {
	return c.poller.ListCheckRuns(ctx, owner, repo, head)
}

func (c *goGitHubMaintainerDeliveryClient) GetCommitStatus(ctx context.Context, owner, repo, head string) (polledHeadRollup, gitHubPollResponse, error) {
	return c.poller.GetCommitStatus(ctx, owner, repo, head)
}

func (c *goGitHubMaintainerDeliveryClient) GetMergePolicy(ctx context.Context, owner, repo, branch string) (maintainerMergePolicy, error) {
	repository, _, err := c.repositories.Get(ctx, owner, repo)
	if err != nil {
		return maintainerMergePolicy{}, err
	}
	protection, _, err := c.repositories.GetBranchProtection(ctx, owner, repo, branch)
	if err != nil {
		return maintainerMergePolicy{}, err
	}
	rulesets, _, err := c.repositories.GetAllRulesets(ctx, owner, repo, true)
	if err != nil {
		return maintainerMergePolicy{}, err
	}
	reviews := protection.RequiredPullRequestReviews
	checks := protection.RequiredStatusChecks
	actorCanBypass := repository.GetPermissions()["admin"]
	if reviews != nil && reviews.BypassPullRequestAllowances != nil {
		allowances := reviews.BypassPullRequestAllowances
		actorCanBypass = actorCanBypass || len(allowances.Users) > 0 || len(allowances.Teams) > 0 || len(allowances.Apps) > 0
	}
	for _, ruleset := range rulesets {
		if ruleset != nil && strings.EqualFold(ruleset.Enforcement, "active") && len(ruleset.BypassActors) > 0 {
			// Conservatively reject any active bypass allowance. This avoids
			// assuming an actor identity or custom role is non-bypass.
			actorCanBypass = true
		}
	}
	permissions := repository.GetPermissions()
	return maintainerMergePolicy{
		RequiredReviews: reviews != nil && reviews.RequiredApprovingReviewCount > 0,
		RequiredChecks:  checks != nil && len(checks.GetContexts())+len(checks.GetChecks()) > 0,
		CanMerge:        permissions["push"] || permissions["maintain"] || permissions["admin"],
		ActorCanBypass:  actorCanBypass,
	}, nil
}

func (c *goGitHubMaintainerDeliveryClient) MergePullRequest(ctx context.Context, owner, repo string, number int, head, method string) (*github.PullRequestMergeResult, error) {
	result, _, err := c.pulls.Merge(ctx, owner, repo, number, "", &github.PullRequestOptions{SHA: head, MergeMethod: method})
	return result, err
}
