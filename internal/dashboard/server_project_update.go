package dashboard

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// UpdateProject edits the user-facing Project defaults used for future runs.
// Credential edits are reference-only unless use_saved_credentials is set; this
// RPC never creates provider credential Secrets.
func (s *Server) UpdateProject(ctx context.Context, req *platform.UpdateProjectRequest) (*platform.Project, error) {
	namespace := strings.TrimSpace(req.GetNamespace())
	name := strings.TrimSpace(req.GetName())
	if namespace == "" || name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace and name are required"))
	}
	if err := s.requireResourceAccess(ctx, projectResourceType, name, namespace, AccessCollaborator, "update this project"); err != nil {
		return nil, err
	}

	existing := &triggersv1alpha1.Project{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, existing); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get Project %s/%s", namespace, name), err)
	}
	if req.KubernetesAdmin != nil && req.GetKubernetesAdmin() != existing.Spec.KubernetesAdmin && !actorIsAdmin(requestActorFromContext(ctx)) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("changing kubernetes_admin requires an admin"))
	}

	displayName := strings.TrimSpace(req.GetDisplayName())
	if displayName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("display_name is required"))
	}
	provider, err := resolveProvider(req.GetProvider(), existing.Spec.Defaults.Provider)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	authMode := triggersv1alpha1.NormalizeAuthMode(req.GetAuthMode())
	if provider == triggersv1alpha1.ProviderCopilot {
		authMode = platformv1alpha1.AgentRunAuthModeOAuth
	}
	if err := triggersv1alpha1.ValidateProviderAuthMode(provider, authMode); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	reasoningLevel, err := resolveReasoningLevel(req.GetReasoningLevel())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	additionalRepos, err := normalizeAdditionalRepoURLs(req.GetAdditionalRepoUrls(), req.GetRepoUrl())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	var timeout metav1.Duration
	if value := strings.TrimSpace(req.GetTimeout()); value != "" {
		if err := timeout.UnmarshalJSON([]byte("\"" + value + "\"")); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid timeout %q: %w", req.GetTimeout(), err))
		}
	}

	secrets := triggersv1alpha1.AgentRunSecrets{}
	if req.GetUseSavedCredentials() {
		if err := s.applyProjectSavedCredentials(ctx, namespace, provider, authMode, &secrets); err != nil {
			return nil, err
		}
	} else {
		secrets = triggersv1alpha1.AgentRunSecrets{
			ClaudeApiKey:      strings.TrimSpace(req.GetClaudeApiKeySecret()),
			OpenAIOAuthSecret: strings.TrimSpace(req.GetOpenaiOauthSecret()),
			GithubToken:       strings.TrimSpace(req.GetGithubTokenSecret()),
			ProviderKeys:      providerKeysFromProto(req.GetProviderKeys()),
		}
	}
	// Heal provider/OAuth-secret wiring mismatches (e.g. provider switched to
	// anthropic while the form still carried the Copilot saved-credential
	// secret): carried-over usercred-* refs are repointed to the caller's
	// saved credentials for the new provider, anything else fails here with
	// an actionable error instead of at agent startup.
	if authMode == platformv1alpha1.AgentRunAuthModeOAuth {
		authMode, err = s.healProjectOAuthSecretForProvider(ctx, namespace, provider, authMode, &secrets)
		if err != nil {
			return nil, err
		}
	}
	// "Use my saved provider credentials" intentionally rewires provider
	// auth, but must not silently drop the project's GitHub token ref when
	// the caller has no saved GitHub credential to replace it with.
	if req.GetUseSavedCredentials() && strings.TrimSpace(secrets.GithubToken) == "" {
		secrets.GithubToken = strings.TrimSpace(existing.Spec.Defaults.Secrets.GithubToken)
	}
	if err := validateProjectDefaultCredentials(provider, authMode, secrets); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
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
		return nil, err
	}

	updated, err := s.patchProjectWithRetry(ctx, namespace, name, func(fresh *triggersv1alpha1.Project) error {
		fresh.Spec.DisplayName = displayName
		if req.ReviewLoopDisabled != nil {
			fresh.Spec.ReviewLoop = &triggersv1alpha1.ProjectReviewLoopSpec{Disabled: req.GetReviewLoopDisabled()}
		}
		if req.KubernetesAdmin != nil {
			fresh.Spec.KubernetesAdmin = req.GetKubernetesAdmin()
		}
		fresh.Spec.Defaults.RepoURL = strings.TrimSpace(req.GetRepoUrl())
		fresh.Spec.Defaults.AdditionalRepos = additionalRepos
		fresh.Spec.Defaults.BaseBranch = strings.TrimSpace(req.GetBaseBranch())
		fresh.Spec.Defaults.Model = strings.TrimSpace(req.GetModel())
		fresh.Spec.Defaults.Image = strings.TrimSpace(req.GetImage())
		fresh.Spec.Defaults.Timeout = timeout
		fresh.Spec.Defaults.CustomInstructions = strings.TrimSpace(req.GetCustomInstructions())
		fresh.Spec.Defaults.Provider = provider
		fresh.Spec.Defaults.AllowedModels = append([]string(nil), req.GetAllowedModels()...)
		fresh.Spec.Defaults.AuthMode = authMode
		fresh.Spec.Defaults.ReasoningLevel = reasoningLevel
		if req.ModeRef != nil {
			fresh.Spec.Defaults.ModeRef = nil
			if modeName := strings.TrimSpace(req.GetModeRef()); modeName != "" {
				fresh.Spec.Defaults.ModeRef = &platformv1alpha1.ModeRef{Name: modeName}
			}
		}
		fresh.Spec.Defaults.Secrets = secrets
		fresh.Spec.Defaults.RuntimeProfileRef = runtimeProfileRef
		fresh.Spec.Defaults.MCPPolicyRef = mcpPolicyRef
		fresh.Spec.Defaults.MCPServerRefs = namedRefsFromNames(req.GetMcpServerRefs())
		fresh.Spec.Defaults.SkillRefs = namedRefsFromNames(req.GetSkillRefs())
		return nil
	})
	if err != nil {
		if runtimeProfileCreated {
			s.cleanupRuntimeProfile(ctx, namespace, runtimeProfileRef.Name)
		}
		if mcpPolicyCreated {
			s.cleanupMCPPolicy(ctx, namespace, mcpPolicyRef.Name)
		}
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, mapK8sError("update Project", err)
	}

	return s.enrichProjectProto(ctx, k8sProjectToProto(updated)), nil
}

func (s *Server) patchProjectWithRetry(ctx context.Context, namespace, name string, mutate func(*triggersv1alpha1.Project) error) (*triggersv1alpha1.Project, error) {
	key := client.ObjectKey{Namespace: namespace, Name: name}
	var updated *triggersv1alpha1.Project
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.Project{}
		if err := s.k8sClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
		if err := mutate(fresh); err != nil {
			return err
		}
		if err := s.k8sClient.Patch(ctx, fresh, patch); err != nil {
			return err
		}
		updated = fresh.DeepCopy()
		return nil
	})
	return updated, err
}

func providerKeysFromProto(keys []*platform.ProviderKeyRef) []platformv1alpha1.ProviderKeyRef {
	out := make([]platformv1alpha1.ProviderKeyRef, 0, len(keys))
	for _, key := range keys {
		if key == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(key.GetProvider()))
		secretName := strings.TrimSpace(key.GetSecretName())
		if provider == "" || secretName == "" {
			continue
		}
		out = append(out, platformv1alpha1.ProviderKeyRef{
			Provider:   provider,
			SecretName: secretName,
			SecretKey:  strings.TrimSpace(key.GetSecretKey()),
		})
	}
	return out
}

func validateProjectDefaultCredentials(provider string, authMode platformv1alpha1.AgentRunAuthMode, secrets triggersv1alpha1.AgentRunSecrets) error {
	if err := validateProviderAuthConfiguration(provider, authMode, secrets.ClaudeApiKey, secrets.OpenAIOAuthSecret, secrets.ProviderKeys); err != nil {
		return err
	}
	if triggersv1alpha1.RequiresOpenAIOAuthSecret(provider, authMode) {
		return nil
	}
	if providerKeyConfigured(secrets.ProviderKeys, provider) {
		return nil
	}
	if provider == triggersv1alpha1.ProviderAnthropic && strings.TrimSpace(secrets.ClaudeApiKey) != "" {
		return nil
	}
	return fmt.Errorf("provider %q with authMode %q requires a provider key Secret ref", provider, authMode)
}

func providerKeyConfigured(keys []platformv1alpha1.ProviderKeyRef, provider string) bool {
	for _, key := range keys {
		if strings.EqualFold(strings.TrimSpace(key.Provider), provider) && strings.TrimSpace(key.SecretName) != "" {
			return true
		}
	}
	return false
}
