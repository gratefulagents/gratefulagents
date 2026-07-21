package dashboard

import (
	"context"
	"fmt"
	"log"
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

const (
	gratefulAgentsProjectName = "gratefulagents"
	gratefulAgentsRepoURL     = "https://github.com/gratefulagents/gratefulagents.git"
	gratefulAgentsSDKRepoURL  = "https://github.com/gratefulagents/sdk.git"
	gratefulAgentsModeName    = "gratefulagents"
)

func (s *Server) CreateProject(ctx context.Context, req *platform.CreateProjectRequest) (*platform.Project, error) {
	// Projects always live in the caller's personal namespace; the client-supplied
	// namespace (if any) is ignored.
	actor := requestActorFromContext(ctx)
	if req.GetKubernetesAdmin() && !actorIsAdmin(actor) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("kubernetes_admin requires an admin"))
	}
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("display_name is required"))
	}
	repoURL := strings.TrimSpace(req.RepoUrl)
	baseBranch := strings.TrimSpace(req.BaseBranch)
	// Repository is optional: a project may start with no repo (repoless), and
	// repos can be cloned into a run at runtime. When a repo is provided without a
	// branch, the CRD's baseBranch default applies on apply.
	additionalRepos, err := normalizeAdditionalRepoURLs(req.GetAdditionalRepoUrls(), repoURL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	provider, err := resolveProvider(req.Provider, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	authMode := triggersv1alpha1.NormalizeAuthMode(req.AuthMode)
	if err := triggersv1alpha1.ValidateProviderAuthMode(provider, authMode); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	reasoningLevel, err := resolveReasoningLevel(req.GetReasoningLevel())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	var timeout metav1.Duration
	if strings.TrimSpace(req.Timeout) != "" {
		if err := timeout.UnmarshalJSON([]byte("\"" + strings.TrimSpace(req.Timeout) + "\"")); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid timeout %q: %w", req.Timeout, err))
		}
	}

	existingProject := &triggersv1alpha1.Project{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, existingProject); err == nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("Project %s/%s already exists", namespace, name))
	} else if !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("read Project", err)
	}

	secretName := projectCredentialsSecretName(name)
	secrets := triggersv1alpha1.AgentRunSecrets{}
	var secret *corev1.Secret

	if req.GetUseSavedCredentials() {
		// Wire the project to the caller's saved per-provider credentials, which
		// live in this same (personal) namespace, so no per-project Secret is created.
		if err := s.applyProjectSavedCredentials(ctx, namespace, provider, authMode, &secrets); err != nil {
			return nil, err
		}
	} else {
		githubToken := strings.TrimSpace(req.GithubToken)
		anthropicAPIKey := strings.TrimSpace(req.AnthropicApiKey)
		openAIAPIKey := strings.TrimSpace(req.OpenaiApiKey)
		openAIOAuthSecret := strings.TrimSpace(req.OpenaiOauthSecret)
		if authMode == platformv1alpha1.AgentRunAuthModeOAuth {
			if openAIOAuthSecret == "" {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("provider %q with authMode %q requires openai_oauth_secret", provider, authMode))
			}
		} else {
			switch provider {
			case triggersv1alpha1.ProviderAnthropic:
				if anthropicAPIKey == "" {
					return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("provider %q with authMode %q requires anthropic_api_key", provider, authMode))
				}
			case triggersv1alpha1.ProviderOpenAI:
				if openAIAPIKey == "" {
					return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("provider %q with authMode %q requires openai_api_key", provider, authMode))
				}
			}
		}

		if openAIOAuthSecret != "" {
			// Reject OAuth secrets that provably hold another provider's
			// material before wiring them into the project.
			if err := s.validateOAuthSecretProvider(ctx, namespace, openAIOAuthSecret, provider); err != nil {
				return nil, err
			}
			secrets.OpenAIOAuthSecret = openAIOAuthSecret
		}
		if githubToken != "" || anthropicAPIKey != "" || openAIAPIKey != "" {
			secret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
				},
				StringData: map[string]string{},
			}
			if githubToken != "" {
				secret.StringData[githubTokenSecretKey] = githubToken
				secrets.GithubToken = secretName
			}
			if anthropicAPIKey != "" {
				secret.StringData[anthropicAPIKeySecretKey] = anthropicAPIKey
				secrets.ClaudeApiKey = secretName //nolint:staticcheck // legacy field retained for the inline anthropic API-key path
			}
			if openAIAPIKey != "" {
				secret.StringData[openAIAPIKeySecretKey] = openAIAPIKey
				secrets.ProviderKeys = append(secrets.ProviderKeys, platformv1alpha1.ProviderKeyRef{
					Provider:   triggersv1alpha1.ProviderOpenAI,
					SecretName: secretName,
					SecretKey:  openAIAPIKeySecretKey,
				})
			}
			if err := s.k8sClient.Create(ctx, secret); err != nil {
				if k8serrors.IsAlreadyExists(err) {
					return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("secret %s/%s already exists", namespace, secretName))
				}
				return nil, mapK8sError("create Project credentials Secret", err)
			}
		}
	}

	runtimeProfileRef, runtimeProfileCreated, err := s.applyConfiguredRuntimeProfile(
		ctx,
		namespace,
		projectRuntimeProfileName(name),
		req.GetConfigureRuntimeProfile(),
		req.GetRuntimeProfileRef(),
		req.GetPermissionMode(),
		req.GetEgressMode(),
	)
	if err != nil {
		if secret != nil {
			s.cleanupProjectCredentialSecret(ctx, namespace, secretName)
		}
		return nil, err
	}
	mcpPolicyRef, mcpPolicyCreated, err := s.applyConfiguredMCPPolicy(
		ctx,
		namespace,
		projectMCPPolicyName(name),
		req.GetConfigureMcpPolicy(),
		req.GetMcpPolicyRef(),
		req.GetMcpPolicyDefaultAction(),
		req.GetMcpPolicyAllowedServers(),
	)
	if err != nil {
		if runtimeProfileCreated {
			s.cleanupRuntimeProfile(ctx, namespace, runtimeProfileRef.Name)
		}
		if secret != nil {
			s.cleanupProjectCredentialSecret(ctx, namespace, secretName)
		}
		return nil, err
	}

	reviewLoopDisabled := true
	if req.ReviewLoopDisabled != nil {
		reviewLoopDisabled = req.GetReviewLoopDisabled()
	}

	var modeRef *platformv1alpha1.ModeRef
	if req.ModeRef != nil {
		if name := strings.TrimSpace(req.GetModeRef()); name != "" {
			modeRef = &platformv1alpha1.ModeRef{Name: name}
		}
	}

	project := &triggersv1alpha1.Project{
		TypeMeta: metav1.TypeMeta{
			APIVersion: triggersv1alpha1.GroupVersion.String(),
			Kind:       "Project",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName:     displayName,
			ReviewLoop:      &triggersv1alpha1.ProjectReviewLoopSpec{Disabled: reviewLoopDisabled},
			KubernetesAdmin: req.GetKubernetesAdmin(),
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:            repoURL,
				AdditionalRepos:    additionalRepos,
				BaseBranch:         baseBranch,
				Model:              strings.TrimSpace(req.Model),
				Image:              strings.TrimSpace(req.Image),
				Timeout:            timeout,
				Secrets:            secrets,
				CustomInstructions: req.CustomInstructions,
				Provider:           provider,
				AllowedModels:      append([]string(nil), req.AllowedModels...),
				AuthMode:           authMode,
				ReasoningLevel:     reasoningLevel,
				ModeRef:            modeRef,
				RuntimeProfileRef:  runtimeProfileRef,
				MCPPolicyRef:       mcpPolicyRef,
				MCPServerRefs:      namedRefsFromNames(req.GetMcpServerRefs()),
				SkillRefs:          namedRefsFromNames(req.GetSkillRefs()),
			},
		},
	}
	if req.ModeRef == nil && isGratefulAgentsBootstrapProject(name, repoURL, additionalRepos) {
		project.Spec.Defaults.ModeRef = &platformv1alpha1.ModeRef{Name: gratefulAgentsModeName}
	}

	if err := s.k8sClient.Create(ctx, project); err != nil {
		if runtimeProfileCreated {
			s.cleanupRuntimeProfile(ctx, namespace, runtimeProfileRef.Name)
		}
		if mcpPolicyCreated {
			s.cleanupMCPPolicy(ctx, namespace, mcpPolicyRef.Name)
		}
		if secret != nil {
			s.cleanupProjectCredentialSecret(ctx, namespace, secretName)
		}
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("Project %s/%s already exists", namespace, name))
		}
		return nil, mapK8sError("create Project", err)
	}

	// Record resource ownership. This must succeed: an unowned project is
	// treated as system-created and becomes visible to every authenticated
	// user, so a silently dropped ownership record would leak the project's
	// configuration.
	if s.stateStore != nil && actor.Subject != "" {
		if err := s.stateStore.SetResourceOwner(ctx, projectResourceType, project.Name, project.Namespace, actor.Subject); err != nil {
			_ = s.k8sClient.Delete(ctx, project)
			if runtimeProfileCreated {
				s.cleanupRuntimeProfile(ctx, namespace, runtimeProfileRef.Name)
			}
			if mcpPolicyCreated {
				s.cleanupMCPPolicy(ctx, namespace, mcpPolicyRef.Name)
			}
			if secret != nil {
				s.cleanupProjectCredentialSecret(ctx, namespace, secretName)
			}
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("record ownership for Project %s/%s: %w", project.Namespace, project.Name, err))
		}
	}

	return s.enrichProjectProto(ctx, k8sProjectToProto(project)), nil
}

func isGratefulAgentsBootstrapProject(name, repoURL string, additionalRepos []string) bool {
	return name == gratefulAgentsProjectName &&
		repoURL == gratefulAgentsRepoURL &&
		len(additionalRepos) == 1 && additionalRepos[0] == gratefulAgentsSDKRepoURL
}

func (s *Server) cleanupProjectCredentialSecret(ctx context.Context, namespace, secretName string) {
	if cleanupErr := s.k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace}}); cleanupErr != nil && !k8serrors.IsNotFound(cleanupErr) {
		log.Printf("WARN: failed to clean up orphaned credentials secret %s/%s: %v", namespace, secretName, cleanupErr)
	}
}

func (s *Server) cleanupRuntimeProfile(ctx context.Context, namespace, name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	if cleanupErr := s.k8sClient.Delete(ctx, &platformv1alpha1.RuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}); cleanupErr != nil && !k8serrors.IsNotFound(cleanupErr) {
		log.Printf("WARN: failed to clean up RuntimeProfile %s/%s: %v", namespace, name, cleanupErr)
	}
}

func (s *Server) cleanupMCPPolicy(ctx context.Context, namespace, name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	if cleanupErr := s.k8sClient.Delete(ctx, &platformv1alpha1.MCPPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}); cleanupErr != nil && !k8serrors.IsNotFound(cleanupErr) {
		log.Printf("WARN: failed to clean up MCPPolicy %s/%s: %v", namespace, name, cleanupErr)
	}
}
