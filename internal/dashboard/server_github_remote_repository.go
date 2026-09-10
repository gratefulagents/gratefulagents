package dashboard

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/go-github/v68/github"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func (h *PlatformServiceConnectHandler) CreateGitHubRemoteRepository(ctx context.Context, req *connect.Request[platform.CreateGitHubRemoteRepositoryRequest]) (*connect.Response[platform.CreateGitHubRemoteRepositoryResponse], error) {
	resp, err := h.srv.CreateGitHubRemoteRepository(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

var githubRemoteRepositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
var githubRemoteOrganization = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

func (s *Server) CreateGitHubRemoteRepository(ctx context.Context, req *platform.CreateGitHubRemoteRepositoryRequest) (*platform.CreateGitHubRemoteRepositoryResponse, error) {
	if err := requireMemberActor(ctx, "create GitHub repository"); err != nil {
		return nil, err
	}
	name, org := strings.TrimSpace(req.Name), strings.TrimSpace(req.Organization)
	if !githubRemoteRepositoryName.MatchString(name) || name == "." || name == ".." || (org != "" && !githubRemoteOrganization.MatchString(org)) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a valid repository name and optional organization name are required"))
	}
	namespace, err := s.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		return nil, err
	}
	token, err := s.savedGitHubTokenValue(ctx, namespace)
	if err != nil {
		return nil, err
	}
	gh, err := s.githubClient(token)
	if err != nil {
		return nil, err
	}
	repo, resp, err := gh.Repositories.Create(ctx, org, &github.Repository{Name: github.Ptr(name), Private: github.Ptr(!req.Public), AutoInit: github.Ptr(true)})
	if err != nil {
		code, message := connect.CodeUnavailable, "GitHub repository creation could not be confirmed. Check GitHub before retrying; if it exists, use its URL."
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				code, message = connect.CodeFailedPrecondition, "GitHub rejected your saved token. Update it in Settings."
			case http.StatusForbidden, http.StatusNotFound:
				code, message = connect.CodePermissionDenied, "Your saved GitHub token cannot create repositories for this account or organization. Check its permissions in GitHub."
			case http.StatusUnprocessableEntity:
				code, message = connect.CodeInvalidArgument, "GitHub rejected the repository name or it already exists. Choose another name or use the existing repository URL."
			}
		}
		return nil, connect.NewError(code, errors.New(message))
	}
	return &platform.CreateGitHubRemoteRepositoryResponse{RepoUrl: repo.GetHTMLURL(), DefaultBranch: repo.GetDefaultBranch()}, nil
}
