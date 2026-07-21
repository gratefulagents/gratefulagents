package dashboard

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// githubTriggerTokenSecretSuffix names the per-trigger Secret created when an
// explicit token is provided to CreateGitHubRepositoryFromToken. The suffix
// can never collide with the "usercred-<provider>" saved-credential names.
const githubTriggerTokenSecretSuffix = "-github-token"

// githubRepositoryCreateInput carries the auth-method-agnostic fields shared
// by the GitHubRepository create RPCs.
type githubRepositoryCreateInput struct {
	owner               string
	repo                string
	namespace           string
	name                string
	model               string
	image               string
	timeout             string
	provider            string
	authMode            string
	allowedModels       []string
	claudeAPIKeySecret  string
	openaiOAuthSecret   string
	providerKeys        []*platform.ProviderKeyRef
	customInstructions  string
	useSavedCredentials bool
	// defaults, when set, carries the full AgentRunDefaults message and takes
	// precedence over the legacy flattened fields above.
	defaults *platform.AgentRunDefaults
}

// resolvedGitHubRepositoryCreate is the validated result of
// resolveGitHubRepositoryCreate: the target namespace/name and the run
// defaults (without BaseBranch, which each auth path fills in itself).
type resolvedGitHubRepositoryCreate struct {
	namespace string
	name      string
	owner     string
	repo      string
	defaults  triggersv1alpha1.AgentRunDefaults
}

// authorizeGitHubRepositoryNamespace resolves the namespace a GitHubRepository
// create targets: the caller's personal namespace by default, with
// authorizeRequestNamespace's policy for explicit namespaces (admins anywhere;
// shared namespaces for members; foreign personal namespaces denied). Internal
// invocations that never passed the RPC interceptor keep the requested
// namespace, falling back to the GitHub App namespace.
func (s *Server) authorizeGitHubRepositoryNamespace(ctx context.Context, requested string) (string, error) {
	if _, recorded := requestActorFromContextOK(ctx); !recorded {
		if ns := strings.TrimSpace(requested); ns != "" {
			return ns, nil
		}
		if ns := strings.TrimSpace(s.githubApp.Namespace); ns != "" {
			return ns, nil
		}
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace is required"))
	}
	return s.authorizeRequestNamespace(ctx, requested, nil)
}

// resolveGitHubRepositoryCreate validates the shared create fields and builds
// the trigger's AgentRunDefaults, wiring the caller's saved credentials when
// requested.
func (s *Server) resolveGitHubRepositoryCreate(ctx context.Context, in githubRepositoryCreateInput) (*resolvedGitHubRepositoryCreate, error) {
	owner := strings.TrimSpace(in.owner)
	repo := strings.TrimSpace(in.repo)
	if owner == "" || repo == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("owner and repo are required"))
	}
	namespace, err := s.authorizeGitHubRepositoryNamespace(ctx, in.namespace)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.name)
	if name == "" {
		name = sanitizeRunName(owner + "-" + repo)
	}
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}

	if in.defaults != nil {
		defaults, provider, authMode, err := protoDefaultsToCRD(in.defaults)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		// Trigger-created runs require an explicit model
		// (validateTriggerRunDefaults in the controller); reject early instead
		// of onboarding a trigger whose runs would all fail.
		if strings.TrimSpace(defaults.Model) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("model is required"))
		}
		// The trigger always targets the onboarded repository: the derived URL
		// wins over any repo_url carried in the defaults message, and the
		// additional repos are re-deduped against it so the onboarded repo is
		// never cloned twice.
		defaults.RepoURL = fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
		if defaults.AdditionalRepos, err = normalizeAdditionalRepoURLs(defaults.AdditionalRepos, defaults.RepoURL); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if in.useSavedCredentials {
			secrets := triggersv1alpha1.AgentRunSecrets{}
			if err := s.applyProjectSavedCredentials(ctx, namespace, provider, authMode, &secrets); err != nil {
				return nil, err
			}
			defaults.Secrets = secrets
		} else if err := validateProviderAuthConfiguration(provider, authMode, defaults.Secrets.ClaudeApiKey, defaults.Secrets.OpenAIOAuthSecret, defaults.Secrets.ProviderKeys); err != nil { //nolint:staticcheck // legacy field retained for the explicit anthropic API-key path
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return &resolvedGitHubRepositoryCreate{
			namespace: namespace,
			name:      name,
			owner:     owner,
			repo:      repo,
			defaults:  defaults,
		}, nil
	}

	provider, err := resolveProvider(in.provider, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	authMode := triggersv1alpha1.NormalizeAuthMode(in.authMode)
	var providerKeys []platformv1alpha1.ProviderKeyRef
	for _, key := range in.providerKeys {
		if key == nil {
			continue
		}
		providerKeys = append(providerKeys, platformv1alpha1.ProviderKeyRef{
			Provider:   strings.TrimSpace(key.Provider),
			SecretName: strings.TrimSpace(key.SecretName),
			SecretKey:  strings.TrimSpace(key.SecretKey),
		})
	}

	secrets := triggersv1alpha1.AgentRunSecrets{
		ClaudeApiKey:      strings.TrimSpace(in.claudeAPIKeySecret), //nolint:staticcheck // legacy field retained for the explicit anthropic API-key path
		OpenAIOAuthSecret: strings.TrimSpace(in.openaiOAuthSecret),
		ProviderKeys:      providerKeys,
	}
	if in.useSavedCredentials {
		// Wire the trigger to the caller's saved per-provider credentials
		// (and saved GitHub token), which live in the target namespace when
		// it is the caller's personal one.
		secrets = triggersv1alpha1.AgentRunSecrets{}
		if err := s.applyProjectSavedCredentials(ctx, namespace, provider, authMode, &secrets); err != nil {
			return nil, err
		}
	} else if err := validateProviderAuthConfiguration(provider, authMode, in.claudeAPIKeySecret, in.openaiOAuthSecret, providerKeys); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	var timeout metav1.Duration
	if strings.TrimSpace(in.timeout) != "" {
		if err := timeout.UnmarshalJSON([]byte("\"" + strings.TrimSpace(in.timeout) + "\"")); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid timeout %q: %w", in.timeout, err))
		}
	}

	return &resolvedGitHubRepositoryCreate{
		namespace: namespace,
		name:      name,
		owner:     owner,
		repo:      repo,
		defaults: triggersv1alpha1.AgentRunDefaults{
			RepoURL:            fmt.Sprintf("https://github.com/%s/%s.git", owner, repo),
			Model:              strings.TrimSpace(in.model),
			Image:              strings.TrimSpace(in.image),
			Timeout:            timeout,
			Provider:           provider,
			AllowedModels:      append([]string(nil), in.allowedModels...),
			AuthMode:           authMode,
			CustomInstructions: in.customInstructions,
			Secrets:            secrets,
		},
	}, nil
}

// createGitHubRepositoryCR creates the CR, records ownership fail-closed (an
// unowned trigger is treated as system-created and becomes visible to every
// authenticated user), and assembles the response proto. cleanup funcs run
// when creation does not survive, so callers can roll back companion objects.
func (s *Server) createGitHubRepositoryCR(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, cleanup ...func()) (*platform.GitHubRepository, error) {
	rollback := func() {
		for _, fn := range cleanup {
			fn()
		}
	}
	if err := s.k8sClient.Create(ctx, gh); err != nil {
		rollback()
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("GitHubRepository %s/%s already exists", gh.Namespace, gh.Name))
		}
		return nil, mapK8sError("create GitHubRepository", err)
	}

	if s.stateStore != nil {
		if actor := requestActorFromContext(ctx); actor.Subject != "" {
			if err := s.stateStore.SetResourceOwner(ctx, githubRepositoryResourceType, gh.Name, gh.Namespace, actor.Subject); err != nil {
				_ = s.k8sClient.Delete(ctx, gh)
				rollback()
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("record ownership for GitHubRepository %s/%s: %w", gh.Namespace, gh.Name, err))
			}
		}
	}

	pb := s.githubRepositoryProto(ctx, gh, nil)
	pb.ResourceOwner, pb.MyPermission = s.resourceACL(ctx, githubRepositoryResourceType, gh.Name, gh.Namespace)
	return pb, nil
}

// CreateGitHubRepositoryFromToken onboards a repository authenticated by a
// GitHub token instead of a GitHub App installation: an explicit token from
// the request (stored in a Secret dedicated to this trigger) or the caller's
// saved GitHub token. Repository access is verified against the GitHub API
// before anything is created.
func (s *Server) CreateGitHubRepositoryFromToken(ctx context.Context, req *platform.CreateGitHubRepositoryFromTokenRequest) (*platform.GitHubRepository, error) {
	if err := requireMemberActor(ctx, "create GitHubRepository"); err != nil {
		return nil, err
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

	inlineToken := strings.TrimSpace(req.GithubToken)
	token := inlineToken
	tokenSecretName := resolved.name + githubTriggerTokenSecretSuffix
	if inlineToken == "" {
		tokenSecretName = userCredentialSecretName(credentialGitHub)
		token, err = s.savedGitHubTokenValue(ctx, resolved.namespace)
		if err != nil {
			return nil, err
		}
	}

	defaultBranch, err := s.verifyGitHubTokenRepoAccess(ctx, token, resolved.owner, resolved.repo)
	if err != nil {
		return nil, err
	}
	baseBranch := strings.TrimSpace(req.DefaultBranch)
	if baseBranch == "" {
		baseBranch = defaultBranch
	}
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
	// Keep the run defaults aligned with the trigger credential for legacy
	// consumers that read the default secret directly.
	resolved.defaults.Secrets.GithubToken = tokenSecretName

	cleanup := policyCleanup
	if inlineToken != "" {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tokenSecretName,
				Namespace: resolved.namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "gratefulagents-dashboard",
				},
			},
			Type:       corev1.SecretTypeOpaque,
			StringData: map[string]string{userCredGithubTokenKey: inlineToken},
		}
		if err := s.k8sClient.Create(ctx, secret); err != nil {
			for _, fn := range policyCleanup {
				fn()
			}
			if k8serrors.IsAlreadyExists(err) {
				return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("Secret %s/%s already exists", resolved.namespace, tokenSecretName))
			}
			return nil, mapK8sError("create GitHub token Secret", err)
		}
		// Cleanup must survive request cancellation to avoid leaking the
		// dedicated token Secret when a later step fails.
		secretCleanupCtx := context.WithoutCancel(ctx)
		cleanup = append(cleanup, func() {
			if err := s.k8sClient.Delete(secretCleanupCtx, secret); err != nil && !k8serrors.IsNotFound(err) {
				log.Printf("WARN: failed to clean up GitHub token Secret %s/%s: %v", resolved.namespace, tokenSecretName, err)
			}
		})
	}

	gh := &triggersv1alpha1.GitHubRepository{
		TypeMeta: metav1.TypeMeta{
			APIVersion: triggersv1alpha1.GroupVersion.String(),
			Kind:       "GitHubRepository",
		},
		ObjectMeta: metav1.ObjectMeta{Name: resolved.name, Namespace: resolved.namespace},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner:             resolved.owner,
			Repo:              resolved.repo,
			TriggerKeyword:    "@agent",
			GitHubTokenSecret: tokenSecretName,
			Defaults:          resolved.defaults,
		},
	}
	pb, err := s.createGitHubRepositoryCR(ctx, gh, cleanup...)
	if err != nil {
		return nil, err
	}
	if inlineToken != "" {
		// Tie the per-trigger token Secret to the CR so deleting the trigger
		// garbage-collects it. Best-effort: the trigger works without it.
		s.adoptGitHubTriggerSecret(ctx, gh, tokenSecretName)
	}
	return pb, nil
}

// savedGitHubTokenValue reads the caller's saved GitHub token from the
// usercred Secret in namespace.
func (s *Server) savedGitHubTokenValue(ctx context.Context, namespace string) (string, error) {
	name := userCredentialSecretName(credentialGitHub)
	missing := connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("no saved GitHub token in %s: save one in Settings or provide github_token", namespace))
	secret := &corev1.Secret{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret); err != nil {
		if k8serrors.IsNotFound(err) {
			return "", missing
		}
		return "", mapK8sError(fmt.Sprintf("read Secret %s/%s", namespace, name), err)
	}
	token := strings.TrimSpace(string(secret.Data[userCredGithubTokenKey]))
	if token == "" {
		return "", missing
	}
	return token, nil
}

// verifyGitHubTokenRepoAccess confirms the token can read owner/repo and
// returns the repository's default branch.
func (s *Server) verifyGitHubTokenRepoAccess(ctx context.Context, token, owner, repo string) (string, error) {
	ghClient, err := s.githubClient(token)
	if err != nil {
		return "", err
	}
	repository, resp, err := ghClient.Repositories.Get(ctx, owner, repo)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		switch status {
		case http.StatusNotFound:
			return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("repository %s/%s not found or the GitHub token cannot access it", owner, repo))
		case http.StatusUnauthorized:
			return "", connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("GitHub rejected the token"))
		case http.StatusForbidden:
			return "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("the GitHub token is not authorized to access %s/%s", owner, repo))
		}
		return "", connect.NewError(connect.CodeUnavailable, fmt.Errorf("verify access to %s/%s: %w", owner, repo, err))
	}
	return repository.GetDefaultBranch(), nil
}

// adoptGitHubTriggerSecret sets the CR as owner of the per-trigger token
// Secret so it is garbage-collected with the trigger.
func (s *Server) adoptGitHubTriggerSecret(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, secretName string) {
	secret := &corev1.Secret{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: gh.Namespace, Name: secretName}, secret); err != nil {
		log.Printf("WARN: failed to read GitHub token Secret %s/%s for owner reference: %v", gh.Namespace, secretName, err)
		return
	}
	secret.OwnerReferences = append(secret.OwnerReferences, metav1.OwnerReference{
		APIVersion: triggersv1alpha1.GroupVersion.String(),
		Kind:       "GitHubRepository",
		Name:       gh.Name,
		UID:        gh.UID,
	})
	if err := s.k8sClient.Update(ctx, secret); err != nil {
		log.Printf("WARN: failed to set owner reference on GitHub token Secret %s/%s: %v", gh.Namespace, secretName, err)
	}
}
