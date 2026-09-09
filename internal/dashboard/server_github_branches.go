package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/go-github/v68/github"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (h *PlatformServiceConnectHandler) ListGitHubBranches(ctx context.Context, req *connect.Request[platform.ListGitHubBranchesRequest]) (*connect.Response[platform.ListGitHubBranchesResponse], error) {
	resp, err := h.srv.ListGitHubBranches(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) ListGitHubBranches(ctx context.Context, req *platform.ListGitHubBranchesRequest) (*platform.ListGitHubBranchesResponse, error) {
	owner, repo, err := parseGitHubRepositoryURL(strings.TrimRight(strings.TrimSpace(req.RepoUrl), "/"))
	if err != nil || req.Page < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a GitHub HTTPS repository URL and non-negative page are required"))
	}
	repos := &triggersv1alpha1.GitHubRepositoryList{}
	var opts []client.ListOption
	if req.Namespace != "" {
		opts = append(opts, client.InNamespace(req.Namespace))
	}
	if err := s.k8sClient.List(ctx, repos, opts...); err != nil {
		return nil, mapK8sError("list GitHubRepositories", err)
	}
	gh, err := s.githubClient("")
	if err != nil {
		return nil, err
	}
	visible := s.resourceVisibilityFilter(ctx, githubRepositoryResourceType, false)
	for _, r := range repos.Items {
		if !strings.EqualFold(r.Spec.Owner, owner) || !strings.EqualFold(r.Spec.Repo, repo) || !visible(r.Namespace, r.Name) {
			continue
		}
		auth := githubRepoAuth{namespace: r.Namespace, tokenSecret: strings.TrimSpace(r.Spec.GitHubTokenSecret)}
		if r.Spec.GitHubApp != nil {
			auth.installationID = r.Spec.GitHubApp.InstallationID
		}
		if auth.installationID == 0 && auth.tokenSecret == "" {
			continue
		}
		// An arbitrary URL must never trigger platform-wide installation discovery.
		gh, err = s.githubRepositoryClient(ctx, auth, func() ([]byte, error) { return s.githubAppPrivateKey(ctx) })
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("repository credentials unavailable"))
		}
		break
	}
	branches, resp, err := gh.Repositories.ListBranches(ctx, owner, repo, &github.BranchListOptions{
		ListOptions: github.ListOptions{Page: max(1, int(req.Page)), PerPage: 100},
	})
	if err != nil {
		code := connect.CodeUnavailable
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusNotFound:
				code = connect.CodeNotFound
			case http.StatusForbidden, http.StatusTooManyRequests:
				code = connect.CodeResourceExhausted
			}
		}
		return nil, connect.NewError(code, errors.New("GitHub branch suggestions unavailable"))
	}
	out := &platform.ListGitHubBranchesResponse{NextPage: int32(resp.NextPage)}
	for _, branch := range branches {
		out.Branches = append(out.Branches, branch.GetName())
	}
	return out, nil
}
