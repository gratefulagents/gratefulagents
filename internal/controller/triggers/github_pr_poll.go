package triggers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v68/github"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

const gitHubPollPageLimit = 10

var errGitHubPollHistoryLimit = errors.New("github poll history exceeds page limit")

type gitHubPollHistoryLimitError struct {
	collection string
	pages      int
}

func (e *gitHubPollHistoryLimitError) Error() string {
	return fmt.Sprintf("%s: %s history exceeds %d pages", errGitHubPollHistoryLimit, e.collection, e.pages)
}

func (e *gitHubPollHistoryLimitError) Unwrap() error {
	return errGitHubPollHistoryLimit
}

func (e *gitHubPollHistoryLimitError) Retryable() bool {
	return true
}

type polledPullRequest struct {
	Number         int
	Title          string
	URL            string
	State          string
	Draft          bool
	Merged         bool
	MergedAt       time.Time
	MergeableKnown bool
	Mergeable      bool
	MergeableState string
	HeadRef        string
	HeadSHA        string
	BaseRef        string
	AuthorLogin    string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type polledHeadRollup struct {
	HeadSHA string
	State   string
	Count   int
}

type polledPullRequestReview struct {
	ID                int64
	State             string
	Body              string
	AuthorLogin       string
	AuthorAssociation string
	SubmittedAt       time.Time
}

type polledIssueComment struct {
	ID                int64
	Body              string
	AuthorLogin       string
	AuthorAssociation string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type gitHubPollResponse struct {
	StatusCode    int
	ETag          string
	NextPage      int
	LastPage      int
	RateLimit     int
	RateRemaining int
	RateReset     time.Time
	RetryAfter    time.Duration
}

type pullRequestGitHubPoller interface {
	GetPullRequest(context.Context, string, string, int, string) (*polledPullRequest, gitHubPollResponse, error)
	ListReviews(context.Context, string, string, int, time.Time) ([]polledPullRequestReview, gitHubPollResponse, error)
	GetReviewDecision(context.Context, string, string, int) (triggersv1alpha1.PullRequestReviewDecision, gitHubPollResponse, error)
	ListIssueComments(context.Context, string, string, int, time.Time) ([]polledIssueComment, gitHubPollResponse, error)
	ListCheckRuns(context.Context, string, string, string) (polledHeadRollup, gitHubPollResponse, error)
	GetCommitStatus(context.Context, string, string, string) (polledHeadRollup, gitHubPollResponse, error)
}

type goGitHubPullRequestPoller struct {
	client *github.Client
}

func newPullRequestGitHubPoller(client *github.Client) pullRequestGitHubPoller {
	return &goGitHubPullRequestPoller{client: client}
}

func (p *goGitHubPullRequestPoller) GetPullRequest(ctx context.Context, owner, repo string, number int, etag string) (*polledPullRequest, gitHubPollResponse, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	req, err := p.client.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, gitHubPollResponse{}, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	var value github.PullRequest
	resp, err := p.client.Do(ctx, req, &value)
	metadata := pollResponseFromGitHub(resp)
	if metadata.StatusCode == http.StatusNotModified {
		return nil, metadata, nil
	}
	if err != nil {
		return nil, metadata, err
	}

	return &polledPullRequest{
		Number:         value.GetNumber(),
		Title:          value.GetTitle(),
		URL:            value.GetHTMLURL(),
		State:          value.GetState(),
		Draft:          value.GetDraft(),
		Merged:         value.GetMerged(),
		MergedAt:       value.GetMergedAt().Time,
		MergeableKnown: value.Mergeable != nil,
		Mergeable:      value.GetMergeable(),
		MergeableState: value.GetMergeableState(),
		HeadRef:        value.GetHead().GetRef(),
		HeadSHA:        value.GetHead().GetSHA(),
		BaseRef:        value.GetBase().GetRef(),
		AuthorLogin:    value.GetUser().GetLogin(),
		CreatedAt:      value.GetCreatedAt().Time,
		UpdatedAt:      value.GetUpdatedAt().Time,
	}, metadata, nil
}

func (p *goGitHubPullRequestPoller) ListReviews(ctx context.Context, owner, repo string, number int, after time.Time) ([]polledPullRequestReview, gitHubPollResponse, error) {
	owner, repo = url.PathEscape(owner), url.PathEscape(repo)
	first, resp, err := p.client.PullRequests.ListReviews(ctx, owner, repo, number, &github.ListOptions{Page: 1, PerPage: 100})
	metadata := pollResponseFromGitHub(resp)
	metadata.ETag = ""
	if err != nil {
		return nil, metadata, err
	}

	lastPage := metadata.LastPage
	if lastPage < 1 {
		lastPage = 1
	}
	page := lastPage
	requestsRead := 1
	var reviews []polledPullRequestReview
	for page >= 1 {
		var values []*github.PullRequestReview
		if page == 1 {
			values = first
		} else {
			if requestsRead == gitHubPollPageLimit {
				return nil, metadata, &gitHubPollHistoryLimitError{collection: "pull request review", pages: gitHubPollPageLimit}
			}
			values, resp, err = p.client.PullRequests.ListReviews(ctx, owner, repo, number, &github.ListOptions{Page: page, PerPage: 100})
			requestsRead++
			metadata = mergeGitHubPollResponse(metadata, pollResponseFromGitHub(resp))
			metadata.ETag = ""
			if err != nil {
				return nil, metadata, err
			}
		}

		reachedCursor := false
		for _, value := range values {
			if value == nil {
				continue
			}
			submittedAt := value.GetSubmittedAt().Time
			if submittedAt.Before(after) {
				reachedCursor = true
				continue
			}
			if submittedAt.Equal(after) {
				reachedCursor = true
			}
			reviews = append(reviews, polledPullRequestReview{
				ID:                value.GetID(),
				State:             value.GetState(),
				Body:              value.GetBody(),
				AuthorLogin:       value.GetUser().GetLogin(),
				AuthorAssociation: value.GetAuthorAssociation(),
				SubmittedAt:       value.GetSubmittedAt().Time,
			})
		}
		if reachedCursor || page == 1 {
			break
		}
		page--
	}

	sort.Slice(reviews, func(i, j int) bool {
		if reviews[i].SubmittedAt.Equal(reviews[j].SubmittedAt) {
			return reviews[i].ID < reviews[j].ID
		}
		return reviews[i].SubmittedAt.Before(reviews[j].SubmittedAt)
	})
	metadata.NextPage = 0
	metadata.ETag = ""
	return reviews, metadata, nil
}

func (p *goGitHubPullRequestPoller) GetReviewDecision(ctx context.Context, owner, repo string, number int) (triggersv1alpha1.PullRequestReviewDecision, gitHubPollResponse, error) {
	body := map[string]any{
		"query":     `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){reviewDecision}}}`,
		"variables": map[string]any{"owner": owner, "repo": repo, "number": number},
	}
	req, err := p.client.NewRequest(http.MethodPost, "graphql", body)
	if err != nil {
		return triggersv1alpha1.PullRequestReviewDecisionUnknown, gitHubPollResponse{}, err
	}
	var result struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewDecision string `json:"reviewDecision"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	resp, err := p.client.Do(ctx, req, &result)
	metadata := pollResponseFromGitHub(resp)
	if err != nil {
		return triggersv1alpha1.PullRequestReviewDecisionUnknown, metadata, err
	}
	if len(result.Errors) > 0 {
		return triggersv1alpha1.PullRequestReviewDecisionUnknown, metadata, fmt.Errorf("GitHub GraphQL review decision: %s", result.Errors[0].Message)
	}
	switch strings.ToUpper(strings.TrimSpace(result.Data.Repository.PullRequest.ReviewDecision)) {
	case "APPROVED":
		return triggersv1alpha1.PullRequestReviewDecisionApproved, metadata, nil
	case "CHANGES_REQUESTED":
		return triggersv1alpha1.PullRequestReviewDecisionChangesRequested, metadata, nil
	case "REVIEW_REQUIRED":
		return triggersv1alpha1.PullRequestReviewDecisionReviewRequired, metadata, nil
	default:
		return triggersv1alpha1.PullRequestReviewDecisionUnknown, metadata, nil
	}
}

func (p *goGitHubPullRequestPoller) ListIssueComments(ctx context.Context, owner, repo string, number int, after time.Time) ([]polledIssueComment, gitHubPollResponse, error) {
	owner, repo = url.PathEscape(owner), url.PathEscape(repo)
	since := after.Add(-time.Second)
	opts := &github.IssueListCommentsOptions{
		Since: &since,
		ListOptions: github.ListOptions{
			Page:    1,
			PerPage: 100,
		},
	}

	var comments []polledIssueComment
	var metadata gitHubPollResponse
	for pagesRead := 0; ; pagesRead++ {
		values, resp, err := p.client.Issues.ListComments(ctx, owner, repo, number, opts)
		pageMetadata := pollResponseFromGitHub(resp)
		pageMetadata.ETag = ""
		metadata = mergeGitHubPollResponse(metadata, pageMetadata)
		metadata.ETag = ""
		if err != nil {
			return nil, metadata, err
		}
		for _, value := range values {
			if value == nil || value.GetCreatedAt().Time.Before(after) {
				continue
			}
			comments = append(comments, polledIssueComment{
				ID:                value.GetID(),
				Body:              value.GetBody(),
				AuthorLogin:       value.GetUser().GetLogin(),
				AuthorAssociation: value.GetAuthorAssociation(),
				CreatedAt:         value.GetCreatedAt().Time,
				UpdatedAt:         value.GetUpdatedAt().Time,
			})
		}
		if pageMetadata.NextPage == 0 {
			break
		}
		if pagesRead+1 == gitHubPollPageLimit {
			return nil, metadata, &gitHubPollHistoryLimitError{collection: "issue comment", pages: gitHubPollPageLimit}
		}
		opts.Page = pageMetadata.NextPage
	}

	sort.Slice(comments, func(i, j int) bool {
		if comments[i].CreatedAt.Equal(comments[j].CreatedAt) {
			return comments[i].ID < comments[j].ID
		}
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})
	metadata.NextPage = 0
	metadata.ETag = ""
	return comments, metadata, nil
}

func (p *goGitHubPullRequestPoller) ListCheckRuns(ctx context.Context, owner, repo, headSHA string) (polledHeadRollup, gitHubPollResponse, error) {
	owner, repo = url.PathEscape(owner), url.PathEscape(repo)
	opts := &github.ListCheckRunsOptions{Filter: github.String("latest"), ListOptions: github.ListOptions{Page: 1, PerPage: 100}}
	rollup := polledHeadRollup{HeadSHA: headSHA, State: "success"}
	var metadata gitHubPollResponse
	seen := false
	for pagesRead := 0; ; pagesRead++ {
		values, resp, err := p.client.Checks.ListCheckRunsForRef(ctx, owner, repo, headSHA, opts)
		metadata = mergeGitHubPollResponse(metadata, pollResponseFromGitHub(resp))
		if err != nil {
			return polledHeadRollup{HeadSHA: headSHA}, metadata, err
		}
		if values != nil {
			for _, value := range values.CheckRuns {
				if value == nil {
					continue
				}
				seen = true
				rollup.Count++
				if !strings.EqualFold(value.GetStatus(), "completed") {
					if rollup.State != "failure" {
						rollup.State = "pending"
					}
					continue
				}
				switch strings.ToLower(value.GetConclusion()) {
				case "success", "neutral", "skipped":
				case "failure", "cancelled", "timed_out", "action_required", "startup_failure", "stale":
					rollup.State = "failure"
				default:
					if rollup.State != "failure" {
						rollup.State = "pending"
					}
				}
			}
			if resp == nil || resp.NextPage == 0 {
				break
			}
			if pagesRead+1 == gitHubPollPageLimit {
				return polledHeadRollup{HeadSHA: headSHA}, metadata, &gitHubPollHistoryLimitError{collection: "check run", pages: gitHubPollPageLimit}
			}
			opts.Page = resp.NextPage
		} else {
			break
		}
	}
	if !seen {
		rollup.State = "none"
	}
	return rollup, metadata, nil
}

func (p *goGitHubPullRequestPoller) GetCommitStatus(ctx context.Context, owner, repo, headSHA string) (polledHeadRollup, gitHubPollResponse, error) {
	owner, repo = url.PathEscape(owner), url.PathEscape(repo)
	value, resp, err := p.client.Repositories.GetCombinedStatus(ctx, owner, repo, headSHA, &github.ListOptions{PerPage: 100})
	metadata := pollResponseFromGitHub(resp)
	if err != nil {
		return polledHeadRollup{HeadSHA: headSHA}, metadata, err
	}
	count := len(value.Statuses)
	state := value.GetState()
	if count == 0 {
		state = "none"
	}
	return polledHeadRollup{HeadSHA: headSHA, State: state, Count: count}, metadata, nil
}

func pollResponseFromGitHub(resp *github.Response) gitHubPollResponse {
	if resp == nil {
		return gitHubPollResponse{}
	}
	result := gitHubPollResponse{
		NextPage:      resp.NextPage,
		LastPage:      resp.LastPage,
		RateLimit:     resp.Rate.Limit,
		RateRemaining: resp.Rate.Remaining,
		RateReset:     resp.Rate.Reset.Time,
	}
	if resp.Response != nil {
		result.StatusCode = resp.StatusCode
		result.ETag = resp.Header.Get("ETag")
		if seconds, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64); err == nil {
			result.RetryAfter = time.Duration(seconds) * time.Second
		}
	}
	return result
}

func mergeGitHubPollResponse(current, next gitHubPollResponse) gitHubPollResponse {
	if next.StatusCode != 0 {
		current.StatusCode = next.StatusCode
	}
	if next.ETag != "" {
		current.ETag = next.ETag
	}
	current.NextPage = next.NextPage
	if next.LastPage > current.LastPage {
		current.LastPage = next.LastPage
	}
	if next.RateLimit != 0 {
		current.RateLimit = next.RateLimit
	}
	if next.RateRemaining != 0 || next.RateLimit != 0 {
		current.RateRemaining = next.RateRemaining
	}
	if !next.RateReset.IsZero() {
		current.RateReset = next.RateReset
	}
	if next.RetryAfter > current.RetryAfter {
		current.RetryAfter = next.RetryAfter
	}
	return current
}
