package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (h *PlatformServiceConnectHandler) GetAgentRunPullRequests(ctx context.Context, req *connect.Request[platform.GetAgentRunPullRequestsRequest]) (*connect.Response[platform.GetAgentRunPullRequestsResponse], error) {
	resp, err := h.srv.GetAgentRunPullRequests(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// GetAgentRunPullRequests returns the pull requests a run created (from
// status.artifacts) enriched with live GitHub data: CI check runs, legacy
// commit statuses, and review threads. Works for both GitHub App and
// token-secret onboarded repositories; repositories without a
// GitHubRepository resource fall back to a live GitHub App installation
// lookup and then to the run's own GitHub token Secret, so PRs on
// never-onboarded repositories (e.g. attached mid-run) still render checks
// and review threads. Per-PR failures (no usable credentials, GitHub API
// error) are reported via the error field on that entry instead of failing
// the whole RPC.
func (s *Server) GetAgentRunPullRequests(ctx context.Context, req *platform.GetAgentRunPullRequestsRequest) (*platform.GetAgentRunPullRequestsResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, run); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get AgentRun %s/%s", req.Namespace, req.Name), err)
	}

	var urls []string
	var legacy string
	if run.Status.Artifacts != nil {
		urls = run.Status.Artifacts.PullRequestURLs
		legacy = run.Status.Artifacts.PullRequestURL
	}
	prURLs := dedupePullRequestURLs(urls, legacy)
	if len(prURLs) == 0 {
		return &platform.GetAgentRunPullRequestsResponse{}, nil
	}

	installations, err := s.githubRepositoryAuths(ctx)
	if err != nil {
		return nil, err
	}

	var privateKey []byte
	var privateKeyErr error
	lazyPrivateKey := func() ([]byte, error) {
		if privateKey == nil && privateKeyErr == nil {
			privateKey, privateKeyErr = s.githubAppPrivateKey(ctx)
		}
		return privateKey, privateKeyErr
	}
	resolver := &pullRequestClientResolver{
		srv:        s,
		run:        run,
		onboarded:  installations,
		privateKey: lazyPrivateKey,
		discovered: map[string]int64{},
	}
	out := make([]*platform.PullRequestDetails, 0, len(prURLs))
	for _, prURL := range prURLs {
		detail := &platform.PullRequestDetails{Url: prURL}
		out = append(out, detail)

		ref, err := parsePullRequestURL(prURL)
		if err != nil {
			detail.Error = err.Error()
			continue
		}
		detail.Repository = ref.owner + "/" + ref.repo
		detail.Number = int32(ref.number)

		ghClient, err := resolver.clientFor(ctx, ref)
		if err != nil {
			detail.Error = err.Error()
			continue
		}
		if err := fillPullRequestDetails(ctx, ghClient, ref, detail); err != nil {
			detail.Error = err.Error()
		}
	}
	return &platform.GetAgentRunPullRequestsResponse{PullRequests: out}, nil
}

// pullRequestClientResolver picks GitHub credentials for a PR's repository.
// Onboarded repositories (GitHubRepository resource) use their configured
// auth. Repositories without one fall back to, in order:
//  1. a GitHub App installation discovered live from the GitHub API (the App
//     is installed on the repository but nobody onboarded it), then
//  2. the run's own GitHub token Secret (spec.secrets.githubTokenSecret) —
//     the credential that created the PR in the first place.
//
// Review threads and checks are fetched from GitHub regardless of who
// authored them (humans, other bots, or other harnesses); only credential
// resolution gates what this RPC can show.
type pullRequestClientResolver struct {
	srv        *Server
	run        *platformv1alpha1.AgentRun
	onboarded  map[string]githubRepoAuth
	privateKey func() ([]byte, error)

	// discovered caches GitHub App installation lookups per lowercased
	// owner/repo; 0 means the App is not installed on that repository.
	discovered   map[string]int64
	runClient    *github.Client
	runClientErr error
	runResolved  bool
}

func (r *pullRequestClientResolver) clientFor(ctx context.Context, ref pullRequestRef) (*github.Client, error) {
	repoKey := strings.ToLower(ref.owner + "/" + ref.repo)
	if auth, ok := r.onboarded[repoKey]; ok {
		return r.srv.githubRepositoryClient(ctx, auth, r.privateKey)
	}

	appClient, appErr := r.appInstallationClient(ctx, repoKey, ref)
	if appClient != nil {
		return appClient, nil
	}
	tokenClient, tokenErr := r.runTokenClient(ctx)
	if tokenClient != nil {
		return tokenClient, nil
	}

	msg := fmt.Sprintf("repository %s is not onboarded (no GitHubRepository resource) and no fallback GitHub credentials are available", ref.owner+"/"+ref.repo)
	if appErr != nil {
		msg += "; GitHub App lookup failed: " + appErr.Error()
	}
	if tokenErr != nil {
		msg += "; run token fallback failed: " + tokenErr.Error()
	}
	return nil, errors.New(msg)
}

// appInstallationClient discovers the GitHub App installation covering the
// repository and returns an installation-token client. Returns (nil, nil)
// when the App is not configured or not installed on that repository.
func (r *pullRequestClientResolver) appInstallationClient(ctx context.Context, repoKey string, ref pullRequestRef) (*github.Client, error) {
	if !r.srv.githubApp.configured() {
		return nil, nil
	}
	key, err := r.privateKey()
	if err != nil {
		return nil, err
	}
	installationID, cached := r.discovered[repoKey]
	if !cached {
		jwtClient, err := r.srv.githubAppJWTClient(key)
		if err != nil {
			return nil, err
		}
		installation, resp, err := jwtClient.Apps.FindRepositoryInstallation(ctx, ref.owner, ref.repo)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				r.discovered[repoKey] = 0
				return nil, nil
			}
			return nil, fmt.Errorf("find GitHub App installation for %s: %w", repoKey, err)
		}
		installationID = installation.GetID()
		r.discovered[repoKey] = installationID
	}
	if installationID == 0 {
		return nil, nil
	}
	return r.srv.githubInstallationClient(ctx, installationID, key)
}

// runTokenClient builds a client from the run's own GitHub token Secret.
// Returns (nil, nil) when the run references no token Secret.
func (r *pullRequestClientResolver) runTokenClient(ctx context.Context) (*github.Client, error) {
	if r.runResolved {
		return r.runClient, r.runClientErr
	}
	r.runResolved = true
	if r.run == nil || r.run.Spec.Secrets == nil {
		return nil, nil
	}
	secretName := strings.TrimSpace(r.run.Spec.Secrets.GitHubTokenSecret)
	if secretName == "" {
		return nil, nil
	}
	secret := &corev1.Secret{}
	if err := r.srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: r.run.Namespace, Name: secretName}, secret); err != nil {
		r.runClientErr = fmt.Errorf("get run GitHub token Secret %s/%s: %w", r.run.Namespace, secretName, err)
		return nil, r.runClientErr
	}
	token := strings.TrimSpace(string(secret.Data[githubapp.TokenSecretKey]))
	if token == "" {
		r.runClientErr = fmt.Errorf("run GitHub token Secret %s/%s missing key %q", r.run.Namespace, secretName, githubapp.TokenSecretKey)
		return nil, r.runClientErr
	}
	r.runClient, r.runClientErr = r.srv.githubClient(token)
	return r.runClient, r.runClientErr
}

// githubRepoAuth captures how an onboarded GitHubRepository authenticates:
// exactly one of a GitHub App installation or a plain token Secret.
type githubRepoAuth struct {
	installationID int64
	tokenSecret    string // Secret holding the token under key "token"
	namespace      string // namespace of the GitHubRepository resource
}

// githubRepositoryAuths maps lowercased "owner/repo" to the auth mode of the
// onboarded GitHubRepository resource (App installation or token Secret).
func (s *Server) githubRepositoryAuths(ctx context.Context) (map[string]githubRepoAuth, error) {
	repos := &triggersv1alpha1.GitHubRepositoryList{}
	if err := s.k8sClient.List(ctx, repos); err != nil {
		return nil, mapK8sError("list GitHubRepositories", err)
	}
	out := make(map[string]githubRepoAuth, len(repos.Items))
	for _, repo := range repos.Items {
		key := strings.ToLower(repo.Spec.Owner + "/" + repo.Spec.Repo)
		switch {
		case repo.Spec.GitHubApp != nil:
			out[key] = githubRepoAuth{installationID: repo.Spec.GitHubApp.InstallationID}
		case strings.TrimSpace(repo.Spec.GitHubTokenSecret) != "":
			out[key] = githubRepoAuth{
				tokenSecret: strings.TrimSpace(repo.Spec.GitHubTokenSecret),
				namespace:   repo.Namespace,
			}
		}
	}
	return out, nil
}

// githubRepositoryClient builds a GitHub client for the repository's auth
// mode: an App installation token, or the token stored in the repository's
// spec.githubTokenSecret Secret.
func (s *Server) githubRepositoryClient(ctx context.Context, auth githubRepoAuth, privateKey func() ([]byte, error)) (*github.Client, error) {
	if auth.installationID != 0 {
		key, err := privateKey()
		if err != nil {
			return nil, err
		}
		return s.githubInstallationClient(ctx, auth.installationID, key)
	}
	secret := &corev1.Secret{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: auth.namespace, Name: auth.tokenSecret}, secret); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get GitHub token Secret %s/%s", auth.namespace, auth.tokenSecret), err)
	}
	token := strings.TrimSpace(string(secret.Data[githubapp.TokenSecretKey]))
	if token == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("GitHub token Secret %s/%s missing key %q", auth.namespace, auth.tokenSecret, githubapp.TokenSecretKey))
	}
	return s.githubClient(token)
}

type pullRequestRef struct {
	owner  string
	repo   string
	number int
}

// parsePullRequestURL extracts owner/repo/number from a github.com pull
// request URL, tolerating trailing path segments (e.g. /files), query strings,
// and fragments. Non-GitHub URLs are rejected.
func parsePullRequestURL(raw string) (pullRequestRef, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return pullRequestRef{}, fmt.Errorf("invalid pull request URL %q", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return pullRequestRef{}, fmt.Errorf("unsupported pull request host %q (only github.com is supported)", u.Hostname())
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return pullRequestRef{}, fmt.Errorf("URL %q is not a GitHub pull request URL", raw)
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return pullRequestRef{}, fmt.Errorf("URL %q has an invalid pull request number", raw)
	}
	return pullRequestRef{owner: parts[0], repo: parts[1], number: number}, nil
}

// dedupePullRequestURLs merges the pullRequestUrls list with the legacy
// single pullRequestUrl field, preserving order and dropping duplicates.
func dedupePullRequestURLs(urls []string, legacy string) []string {
	seen := make(map[string]bool, len(urls)+1)
	var out []string
	for _, u := range append(append([]string(nil), urls...), legacy) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

func fillPullRequestDetails(ctx context.Context, ghClient *github.Client, ref pullRequestRef, detail *platform.PullRequestDetails) error {
	pr, _, err := ghClient.PullRequests.Get(ctx, ref.owner, ref.repo, ref.number)
	if err != nil {
		return fmt.Errorf("get pull request: %w", err)
	}
	detail.Title = pr.GetTitle()
	detail.State = pr.GetState()
	if pr.GetMerged() {
		detail.State = "merged"
	}
	detail.HeadRef = pr.GetHead().GetRef()
	detail.BaseRef = pr.GetBase().GetRef()
	detail.HeadSha = pr.GetHead().GetSHA()

	reviews, err := listAllReviews(ctx, ghClient, ref)
	if err != nil {
		return fmt.Errorf("list reviews: %w", err)
	}
	detail.ReviewDecision = reviewDecision(reviews)

	if detail.HeadSha != "" {
		checks, err := listAllCheckRuns(ctx, ghClient, ref, detail.HeadSha)
		if err != nil {
			return fmt.Errorf("list check runs: %w", err)
		}
		// CI published through the legacy commit-status API (rather than the
		// Checks API) would otherwise be invisible; merge those contexts in
		// so required statuses show up alongside check runs.
		statuses, err := listCombinedStatusChecks(ctx, ghClient, ref, detail.HeadSha)
		if err != nil {
			return fmt.Errorf("get combined status: %w", err)
		}
		detail.Checks = append(checks, statuses...)
	}

	comments, err := listAllReviewComments(ctx, ghClient, ref)
	if err != nil {
		return fmt.Errorf("list review comments: %w", err)
	}
	detail.ReviewThreads = groupReviewThreads(comments)
	return nil
}

func listAllReviews(ctx context.Context, ghClient *github.Client, ref pullRequestRef) ([]*github.PullRequestReview, error) {
	var out []*github.PullRequestReview
	opts := &github.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := ghClient.PullRequests.ListReviews(ctx, ref.owner, ref.repo, ref.number, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, reviews...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func listAllCheckRuns(ctx context.Context, ghClient *github.Client, ref pullRequestRef, sha string) ([]*platform.PullRequestCheck, error) {
	var out []*platform.PullRequestCheck
	opts := &github.ListCheckRunsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		result, resp, err := ghClient.Checks.ListCheckRunsForRef(ctx, ref.owner, ref.repo, sha, opts)
		if err != nil {
			return nil, err
		}
		for _, run := range result.CheckRuns {
			check := &platform.PullRequestCheck{
				Name:       run.GetName(),
				Status:     run.GetStatus(),
				Conclusion: run.GetConclusion(),
				DetailsUrl: run.GetDetailsURL(),
			}
			if ts := run.GetStartedAt(); !ts.IsZero() {
				check.StartedAt = ts.Format("2006-01-02T15:04:05Z07:00")
			}
			if ts := run.GetCompletedAt(); !ts.IsZero() {
				check.CompletedAt = ts.Format("2006-01-02T15:04:05Z07:00")
			}
			out = append(out, check)
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// listCombinedStatusChecks maps legacy commit statuses for sha into check
// entries: pending stays non-completed; success/failure/error become
// completed with the state as the conclusion. GetCombinedStatus already
// returns only the latest status per context.
func listCombinedStatusChecks(ctx context.Context, ghClient *github.Client, ref pullRequestRef, sha string) ([]*platform.PullRequestCheck, error) {
	var out []*platform.PullRequestCheck
	opts := &github.ListOptions{PerPage: 100}
	for {
		combined, resp, err := ghClient.Repositories.GetCombinedStatus(ctx, ref.owner, ref.repo, sha, opts)
		if err != nil {
			return nil, err
		}
		for _, status := range combined.Statuses {
			check := &platform.PullRequestCheck{
				Name:       status.GetContext(),
				DetailsUrl: status.GetTargetURL(),
			}
			if state := status.GetState(); state == "pending" {
				check.Status = "pending"
			} else {
				check.Status = "completed"
				check.Conclusion = state // success, failure, error
				if ts := status.GetUpdatedAt(); !ts.IsZero() {
					check.CompletedAt = ts.Format("2006-01-02T15:04:05Z07:00")
				}
			}
			if ts := status.GetCreatedAt(); !ts.IsZero() {
				check.StartedAt = ts.Format("2006-01-02T15:04:05Z07:00")
			}
			out = append(out, check)
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func listAllReviewComments(ctx context.Context, ghClient *github.Client, ref pullRequestRef) ([]*github.PullRequestComment, error) {
	var out []*github.PullRequestComment
	opts := &github.PullRequestListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		comments, resp, err := ghClient.PullRequests.ListComments(ctx, ref.owner, ref.repo, ref.number, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, comments...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// reviewDecision approximates GitHub's GraphQL reviewDecision from the REST
// review list: the latest non-comment review per author wins; any outstanding
// CHANGES_REQUESTED takes precedence over approvals.
func reviewDecision(reviews []*github.PullRequestReview) string {
	latest := map[string]string{}
	for _, review := range reviews {
		state := review.GetState()
		author := review.GetUser().GetLogin()
		switch state {
		case "APPROVED", "CHANGES_REQUESTED":
			latest[author] = state
		case "DISMISSED":
			delete(latest, author)
		}
	}
	decision := ""
	for _, state := range latest {
		if state == "CHANGES_REQUESTED" {
			return "CHANGES_REQUESTED"
		}
		decision = "APPROVED"
	}
	return decision
}

// groupReviewThreads groups flat REST review comments into threads by
// following InReplyTo chains back to the root comment. The resolved and
// outdated flags are only exposed by GitHub's GraphQL API, which we don't use
// here, so they are always false.
func groupReviewThreads(comments []*github.PullRequestComment) []*platform.PullRequestReviewThread {
	byID := make(map[int64]*github.PullRequestComment, len(comments))
	for _, c := range comments {
		byID[c.GetID()] = c
	}
	rootOf := func(c *github.PullRequestComment) int64 {
		for c.GetInReplyTo() != 0 {
			parent, ok := byID[c.GetInReplyTo()]
			if !ok {
				return c.GetInReplyTo()
			}
			c = parent
		}
		return c.GetID()
	}

	threads := map[int64]*platform.PullRequestReviewThread{}
	var order []int64
	for _, c := range comments {
		rootID := rootOf(c)
		thread, ok := threads[rootID]
		if !ok {
			thread = &platform.PullRequestReviewThread{
				Id: strconv.FormatInt(rootID, 10),
			}
			if root, ok := byID[rootID]; ok {
				thread.Path = root.GetPath()
				thread.Line = int32(root.GetLine())
			} else {
				thread.Path = c.GetPath()
				thread.Line = int32(c.GetLine())
			}
			threads[rootID] = thread
			order = append(order, rootID)
		}
		comment := &platform.PullRequestReviewComment{
			Author: c.GetUser().GetLogin(),
			Body:   c.GetBody(),
			Url:    c.GetHTMLURL(),
		}
		if ts := c.GetCreatedAt(); !ts.IsZero() {
			comment.CreatedAt = ts.Format("2006-01-02T15:04:05Z07:00")
		}
		thread.Comments = append(thread.Comments, comment)
	}

	out := make([]*platform.PullRequestReviewThread, 0, len(order))
	for _, rootID := range order {
		out = append(out, threads[rootID])
	}
	return out
}
