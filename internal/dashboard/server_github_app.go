package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/go-github/v68/github"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"google.golang.org/protobuf/types/known/emptypb"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type GitHubAppConfig struct {
	AppID            int64
	AppSlug          string
	PrivateKeySecret string
	Namespace        string
}

func (c GitHubAppConfig) configured() bool {
	return c.AppID > 0 && strings.TrimSpace(c.AppSlug) != "" && strings.TrimSpace(c.PrivateKeySecret) != "" && strings.TrimSpace(c.Namespace) != ""
}

func (c GitHubAppConfig) installURL() string {
	if strings.TrimSpace(c.AppSlug) == "" {
		return ""
	}
	return "https://github.com/apps/" + strings.TrimSpace(c.AppSlug) + "/installations/new"
}

func (s *Server) GetGitHubAppConfig(context.Context, *emptypb.Empty) (*platform.GitHubAppConfig, error) {
	return &platform.GitHubAppConfig{
		Configured: s.githubApp.configured(),
		AppSlug:    s.githubApp.AppSlug,
		InstallUrl: s.githubApp.installURL(),
	}, nil
}

func (s *Server) ListGitHubAppInstallations(ctx context.Context, _ *emptypb.Empty) (*platform.ListGitHubAppInstallationsResponse, error) {
	if err := requireGitHubAppAdmin(ctx, "list GitHub App installations"); err != nil {
		return nil, err
	}
	privateKey, err := s.githubAppPrivateKey(ctx)
	if err != nil {
		return nil, err
	}
	client, err := s.githubAppJWTClient(privateKey)
	if err != nil {
		return nil, err
	}

	var out []*platform.GitHubAppInstallation
	opts := &github.ListOptions{PerPage: 100}
	for {
		installations, resp, err := client.Apps.ListInstallations(ctx, opts)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("list GitHub App installations: %w", err))
		}
		for _, inst := range installations {
			account := inst.GetAccount()
			out = append(out, &platform.GitHubAppInstallation{
				Id:           inst.GetID(),
				AccountLogin: account.GetLogin(),
				AccountType:  account.GetType(),
			})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return &platform.ListGitHubAppInstallationsResponse{Installations: out}, nil
}

func (s *Server) ListGitHubAppInstallationRepositories(ctx context.Context, req *platform.ListGitHubAppInstallationRepositoriesRequest) (*platform.ListGitHubAppInstallationRepositoriesResponse, error) {
	if err := requireGitHubAppAdmin(ctx, "list GitHub App installation repositories"); err != nil {
		return nil, err
	}
	if req.InstallationId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("installation_id is required"))
	}
	privateKey, err := s.githubAppPrivateKey(ctx)
	if err != nil {
		return nil, err
	}
	client, err := s.githubInstallationClient(ctx, req.InstallationId, privateKey)
	if err != nil {
		return nil, err
	}
	onboarded, err := s.onboardedGitHubRepositories(ctx)
	if err != nil {
		return nil, err
	}

	var out []*platform.GitHubAppInstallationRepository
	opts := &github.ListOptions{PerPage: 100}
	for {
		repos, resp, err := client.Apps.ListRepos(ctx, opts)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("list GitHub App installation repositories: %w", err))
		}
		for _, repo := range repos.Repositories {
			owner := repo.GetOwner().GetLogin()
			name := repo.GetName()
			out = append(out, &platform.GitHubAppInstallationRepository{
				Owner:            owner,
				Name:             name,
				FullName:         repo.GetFullName(),
				DefaultBranch:    repo.GetDefaultBranch(),
				Private:          repo.GetPrivate(),
				AlreadyOnboarded: onboarded[strings.ToLower(owner+"/"+name)],
			})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return &platform.ListGitHubAppInstallationRepositoriesResponse{Repositories: out}, nil
}

func (s *Server) CreateGitHubRepositoryFromInstallation(ctx context.Context, req *platform.CreateGitHubRepositoryFromInstallationRequest) (*platform.GitHubRepository, error) {
	if err := requireGitHubAppAdmin(ctx, "create a GitHubRepository from an installation"); err != nil {
		return nil, err
	}
	if req.InstallationId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("installation_id is required"))
	}
	resolved, err := s.resolveGitHubRepositoryCreate(ctx, githubRepositoryCreateInput{
		owner:               req.Owner,
		repo:                req.Repo,
		namespace:           req.Namespace,
		name:                req.Name,
		model:               req.Model,
		image:               req.Image,
		timeout:             req.Timeout,
		provider:            req.Provider,
		authMode:            req.AuthMode,
		allowedModels:       req.AllowedModels,
		claudeAPIKeySecret:  req.ClaudeApiKeySecret,
		openaiOAuthSecret:   req.OpenaiOauthSecret,
		providerKeys:        req.ProviderKeys,
		customInstructions:  req.CustomInstructions,
		useSavedCredentials: req.UseSavedCredentials,
		defaults:            req.Defaults,
	})
	if err != nil {
		return nil, err
	}
	privateKeySecret, err := s.ensureGitHubAppPrivateKeySecret(ctx, resolved.namespace)
	if err != nil {
		return nil, err
	}
	baseBranch := strings.TrimSpace(req.DefaultBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}
	if resolved.defaults.BaseBranch == "" {
		resolved.defaults.BaseBranch = baseBranch
	}

	policyCleanup, err := s.applyTriggerPolicies(ctx, resolved.namespace, resolved.name, req.GetPolicies(), &resolved.defaults)
	if err != nil {
		return nil, err
	}

	gh := &triggersv1alpha1.GitHubRepository{
		TypeMeta: metav1.TypeMeta{
			APIVersion: triggersv1alpha1.GroupVersion.String(),
			Kind:       "GitHubRepository",
		},
		ObjectMeta: metav1.ObjectMeta{Name: resolved.name, Namespace: resolved.namespace},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner:          resolved.owner,
			Repo:           resolved.repo,
			TriggerKeyword: "@agent",
			GitHubApp: &triggersv1alpha1.GitHubAppAuth{
				AppID:            s.githubApp.AppID,
				InstallationID:   req.InstallationId,
				PrivateKeySecret: privateKeySecret,
			},
			Defaults: resolved.defaults,
		},
	}
	return s.createGitHubRepositoryCR(ctx, gh, policyCleanup...)
}

func (s *Server) githubAppPrivateKey(ctx context.Context) ([]byte, error) {
	if !s.githubApp.configured() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("GitHub App not configured"))
	}
	secret := &corev1.Secret{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: s.githubApp.Namespace, Name: s.githubApp.PrivateKeySecret}, secret); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get GitHub App private key Secret %s/%s", s.githubApp.Namespace, s.githubApp.PrivateKeySecret), err)
	}
	privateKey := secret.Data[githubapp.PrivateKeySecretKey]
	if len(privateKey) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("GitHub App private key Secret %s/%s missing key %q", s.githubApp.Namespace, s.githubApp.PrivateKeySecret, githubapp.PrivateKeySecretKey))
	}
	return privateKey, nil
}

func (s *Server) ensureGitHubAppPrivateKeySecret(ctx context.Context, namespace string) (string, error) {
	if !s.githubApp.configured() {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("GitHub App not configured"))
	}
	if namespace == s.githubApp.Namespace {
		return s.githubApp.PrivateKeySecret, nil
	}
	privateKey, err := s.githubAppPrivateKey(ctx)
	if err != nil {
		return "", err
	}
	target := &corev1.Secret{}
	key := client.ObjectKey{Namespace: namespace, Name: s.githubApp.PrivateKeySecret}
	if err := s.k8sClient.Get(ctx, key, target); err == nil {
		if len(target.Data[githubapp.PrivateKeySecretKey]) == 0 {
			return "", connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("Secret %s/%s exists but is missing key %q", namespace, s.githubApp.PrivateKeySecret, githubapp.PrivateKeySecretKey))
		}
		return s.githubApp.PrivateKeySecret, nil
	} else if !k8serrors.IsNotFound(err) {
		return "", mapK8sError(fmt.Sprintf("get GitHub App private key Secret %s/%s", namespace, s.githubApp.PrivateKeySecret), err)
	}
	copy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.githubApp.PrivateKeySecret,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "gratefulagents-dashboard",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{githubapp.PrivateKeySecretKey: append([]byte(nil), privateKey...)},
	}
	if err := s.k8sClient.Create(ctx, copy); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return s.githubApp.PrivateKeySecret, nil
		}
		return "", mapK8sError("copy GitHub App private key Secret", err)
	}
	return s.githubApp.PrivateKeySecret, nil
}

func (s *Server) githubAppJWTClient(privateKey []byte) (*github.Client, error) {
	token, err := s.githubAppMinter.AppJWT(s.githubApp.AppID, privateKey)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return s.githubClient(token)
}

func (s *Server) githubInstallationClient(ctx context.Context, installationID int64, privateKey []byte) (*github.Client, error) {
	token, err := s.githubAppMinter.MintInstallationToken(ctx, s.githubApp.AppID, installationID, privateKey)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return s.githubClient(token)
}

func (s *Server) githubClient(token string) (*github.Client, error) {
	httpClient := s.githubHTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	client := github.NewClient(httpClient).WithAuthToken(token)
	if s.githubAPIBase != "" {
		base, err := url.Parse(s.githubAPIBase)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("invalid GitHub API base URL: %w", err))
		}
		client.BaseURL = base
	}
	return client, nil
}

func (s *Server) onboardedGitHubRepositories(ctx context.Context) (map[string]bool, error) {
	repos := &triggersv1alpha1.GitHubRepositoryList{}
	if err := s.k8sClient.List(ctx, repos); err != nil {
		return nil, mapK8sError("list GitHubRepositories", err)
	}
	out := make(map[string]bool, len(repos.Items))
	for _, repo := range repos.Items {
		out[strings.ToLower(repo.Spec.Owner+"/"+repo.Spec.Repo)] = true
	}
	return out, nil
}

// requireGitHubAppAdmin protects operations backed by the platform-wide
// GitHub App private key. Until installations have an explicit per-user or
// tenant binding, allowing members to choose an installation ID would let
// them enumerate and mint tokens for another user's private repositories.
func requireGitHubAppAdmin(ctx context.Context, action string) error {
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded {
		return nil // trusted internal invocation; external RPCs always carry an actor
	}
	if actor.Role == "admin" || actor.Role == "owner" {
		return nil
	}
	if actor.Subject == "" && actor.Role == "" {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required to %s", action))
	}
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only an admin may %s", action))
}

func requireMemberActor(ctx context.Context, action string) error {
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded {
		return nil
	}
	switch actor.Role {
	case "admin", "owner", "member":
		return nil
	}
	if actor.Subject == "" && actor.Role == "" {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required to %s", action))
	}
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("you do not have permission to %s", action))
}
