package dashboard

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"

	"connectrpc.com/connect"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/projectstate"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// CreateAgentRun creates a direct AgentRun ingress object for the new control plane.
func (s *Server) CreateAgentRun(ctx context.Context, req *platform.CreateAgentRunRequest) (*platform.AgentRun, error) {
	if req.Source == nil || req.Source.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source with name is required"))
	}
	return s.createAgentRunFromRequest(ctx, req, createRunOptions{})
}

const (
	projectReviewLoopAnnotation = "triggers.gratefulagents.dev/review-loop"
	projectReviewLoopEnabled    = "enabled"
	projectReviewLoopDisabled   = "disabled"
)

// createRunOptions carries run-shape toggles that are not part of the base
// CreateAgentRunRequest proto.
type createRunOptions struct {
	// repoless creates the run with no repository attached; the repository
	// URL/branch are forced empty regardless of any source defaults.
	repoless bool
	// planMode starts the run in the plan-first ModeTemplate.
	planMode bool
}

// resolveRunModelAndProvider resolves the effective model and provider for a run.
// If the model carries a provider prefix (e.g. "anthropic/claude-sonnet-4-6"),
// that provider wins and the prefix is stripped; otherwise defaultProvider is used.
func resolveRunModelAndProvider(model, defaultProvider string) (string, string, error) {
	prefixProvider, bareModel := resolveProviderFromModel(model)
	var provider string
	var err error
	if prefixProvider != "" {
		if provider, err = resolveProvider(prefixProvider, ""); err != nil {
			return "", "", err
		}
		model = bareModel
	} else if provider, err = resolveProvider("", defaultProvider); err != nil {
		return "", "", err
	}
	model, err = effectiveModelForProvider(model, provider)
	if err != nil {
		return "", "", err
	}
	return model, provider, nil
}

// requestHasProviderCredentials reports whether the create request carries any
// explicit provider credential references (which always win over defaults).
func requestHasProviderCredentials(req *platform.CreateAgentRunRequest) bool {
	return strings.TrimSpace(req.ClaudeApiKeySecret) != "" ||
		strings.TrimSpace(req.OpenaiOauthSecret) != "" ||
		len(req.ProviderKeys) > 0
}

// maybeRemapRunCredentialsForProvider re-resolves a run's provider credentials
// when the requested provider differs from the source's provider and the
// request carries no explicit credential references. The source's stored
// credentials, auth mode, and base URL belong to its own provider (e.g. an
// openai/-prefixed model on a copilot project must not inherit the copilot
// OAuth secret); replacements come from a matching source providerKeys entry
// (api-key) when present, otherwise the caller's saved credentials. It returns
// the effective auth mode.
func (s *Server) maybeRemapRunCredentialsForProvider(ctx context.Context, namespace, provider string, req *platform.CreateAgentRunRequest, defaults *triggersv1alpha1.AgentRunDefaults, authMode platformv1alpha1.AgentRunAuthMode) (platformv1alpha1.AgentRunAuthMode, error) {
	if provider == triggersv1alpha1.NormalizeProvider(defaults.Provider) || requestHasProviderCredentials(req) { //nolint:staticcheck // defaults.Provider identifies which provider the stored credentials belong to
		return authMode, nil
	}
	defaults.Secrets.ClaudeApiKey = "" //nolint:staticcheck // clearing the legacy field for the old provider
	defaults.Secrets.OpenAIOAuthSecret = ""
	defaults.OpenAIBaseURL = ""
	if _, _, ok := providerKeyFor(defaults.Secrets.ProviderKeys, provider); ok && resolveAuthMode(req.AuthMode, "") != platformv1alpha1.AgentRunAuthModeOAuth {
		return platformv1alpha1.AgentRunAuthModeAPIKey, nil
	}
	creds, err := s.resolveSavedProviderCredentials(ctx, namespace, provider, req.AuthMode)
	if err != nil {
		return "", err
	}
	defaults.Secrets.OpenAIOAuthSecret = creds.oauthSecretName
	defaults.Secrets.ProviderKeys = creds.providerKeys
	return creds.authMode, nil
}

// resolveRunSecrets resolves provider/auth secrets for a run, merging request
// values with source defaults and validating the resulting auth configuration.
// Repoless chats have no git remote, so any GitHub token is cleared.
func resolveRunSecrets(req *platform.CreateAgentRunRequest, defaults triggersv1alpha1.AgentRunDefaults, provider string, authMode platformv1alpha1.AgentRunAuthMode, repoless bool) (*platformv1alpha1.AgentRunSecrets, error) {
	claudeSecret := req.ClaudeApiKeySecret
	if claudeSecret == "" {
		claudeSecret = defaults.Secrets.ClaudeApiKey //nolint:staticcheck // intentional legacy fallback; providerKeys take precedence
	}
	openAIOAuthSecret := req.OpenaiOauthSecret
	if openAIOAuthSecret == "" {
		openAIOAuthSecret = defaults.Secrets.OpenAIOAuthSecret
	}
	githubSecret := req.GithubTokenSecret
	if githubSecret == "" {
		githubSecret = defaults.Secrets.GithubToken
	}
	if repoless && req.GithubTokenSecret == "" {
		githubSecret = ""
	}
	providerKeys := make([]platformv1alpha1.ProviderKeyRef, 0, len(req.ProviderKeys))
	for _, pk := range req.ProviderKeys {
		providerKeys = append(providerKeys, platformv1alpha1.ProviderKeyRef{
			Provider:   pk.Provider,
			SecretName: pk.SecretName,
			SecretKey:  pk.SecretKey,
		})
	}
	if len(providerKeys) == 0 {
		providerKeys = defaults.Secrets.ProviderKeys
	}
	if err := validateProviderAuthConfiguration(provider, authMode, claudeSecret, openAIOAuthSecret, providerKeys); err != nil {
		return nil, err
	}
	return &platformv1alpha1.AgentRunSecrets{
		ClaudeAPIKeySecret: claudeSecret,
		OpenAIOAuthSecret:  openAIOAuthSecret,
		GitHubTokenSecret:  githubSecret,
		ProviderKeys:       providerKeys,
	}, nil
}

// createAgentRunFromRequest builds and creates an AgentRun from a create request.
// When opts.repoless is true the run is created with no repository attached (it
// executes in an empty sandbox); the repository URL/branch are forced empty
// regardless of any source defaults.
func (s *Server) createAgentRunFromRequest(ctx context.Context, req *platform.CreateAgentRunRequest, opts createRunOptions) (*platform.AgentRun, error) {
	overseer, err := agentRunOverseerSpecFromProto(req.Overseer)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Resolve and authorize the target namespace: regular users may not
	// create runs inside another user's personal namespace (unless the source
	// there is explicitly shared with them), which would let the run attach
	// that user's MCP servers, skills, and saved credentials.
	namespace, err := s.authorizeRequestNamespace(ctx, req.Namespace, req.Source)
	if err != nil {
		return nil, err
	}

	runName := strings.TrimSpace(req.Name)
	if runName == "" {
		runName = generateRunName(req.GetSource().GetName(), "auto")
	}

	defaults, owner, err := s.resolveSourceDefaults(ctx, namespace, req.Source)
	if err != nil {
		return nil, err
	}

	repoURL := req.RepoUrl
	if repoURL == "" {
		repoURL = defaults.RepoURL
	}
	baseBranch := req.BaseBranch
	if baseBranch == "" {
		baseBranch = defaults.BaseBranch
	}
	additionalRepos := req.GetAdditionalRepoUrls()
	if len(additionalRepos) == 0 {
		additionalRepos = defaults.AdditionalRepos
	}
	if opts.repoless {
		repoURL = ""
		baseBranch = ""
		additionalRepos = nil
	}
	additionalRepos, err = normalizeAdditionalRepoURLs(additionalRepos, repoURL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	model := req.Model
	if model == "" {
		model = defaults.Model
	}
	model, provider, err := resolveRunModelAndProvider(model, defaults.Provider) //nolint:staticcheck // defaults.Provider is an intentional legacy fallback; prefixed models take precedence
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	image := req.Image
	if image == "" {
		image = defaults.Image
	}
	authMode := resolveAuthMode(req.AuthMode, defaults.AuthMode)
	authMode, err = s.maybeRemapRunCredentialsForProvider(ctx, namespace, provider, req, &defaults, authMode)
	if err != nil {
		return nil, err
	}
	openAIBaseURL := triggersv1alpha1.ResolveOpenAIBaseURLWithAuth(provider, defaults.OpenAIBaseURL, authMode)
	// Explicit run-level reasoning wins; otherwise inherit the source default.
	requestedReasoning := strings.TrimSpace(req.ReasoningLevel)
	if requestedReasoning == "" {
		requestedReasoning = string(defaults.ReasoningLevel)
	}
	if requestedReasoning == "" {
		requestedReasoning = string(platformv1alpha1.ReasoningMax)
	}
	reasoningLevel, err := resolveReasoningLevel(requestedReasoning)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	secrets, err := resolveRunSecrets(req, defaults, provider, authMode, opts.repoless)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// Self-heal provider/OAuth-secret wiring drift: a project whose provider
	// changed while its stored OAuth secret kept the old provider's material
	// would otherwise produce a run that crashes at pod startup.
	authMode, err = s.repairRunOAuthSecretForProvider(ctx, namespace, provider, authMode, secrets, strings.TrimSpace(req.OpenaiOauthSecret) != "")
	if err != nil {
		return nil, err
	}
	openAIBaseURL = triggersv1alpha1.ResolveOpenAIBaseURLWithAuth(provider, defaults.OpenAIBaseURL, authMode)
	// Mount every saved provider credential alongside the run's own so the
	// run can switch providers mid-run without a compute restart.
	s.appendAllSavedProviderCredentials(ctx, namespace, secrets)
	userRequest := strings.TrimSpace(req.UserRequest)

	// Every run uses autonomous pacing. Mode templates may still specialize
	// instructions and permissions, but a plain
	// model response never yields the run; only an explicit input request,
	// safety stop, or finish does.
	workflowMode := platformv1alpha1.WorkflowModeAuto
	var modeRef *platformv1alpha1.ModeRef
	switch {
	case opts.planMode:
		modeRef = &platformv1alpha1.ModeRef{Name: planModeName}
	default:
		if defaults.ModeRef != nil {
			modeRef = defaults.ModeRef.DeepCopy()
		} else {
			modeRef = &platformv1alpha1.ModeRef{Name: "interactive"}
		}
	}

	trigger := toPlatformAgentRunTrigger(req.Source)
	runContext := &platformv1alpha1.AgentRunContext{
		ProjectRef: &platformv1alpha1.ProjectRef{
			Kind: req.Source.Kind,
			Name: req.Source.Name,
		},
	}
	if project, ok := owner.(*triggersv1alpha1.Project); ok {
		trigger = platformv1alpha1.TriggerRef{Kind: "Project", Name: "manual", Type: "manual"}
		runContext.ProjectRef = &platformv1alpha1.ProjectRef{Kind: "Project", Name: project.Name}
	}

	run := &platformv1alpha1.AgentRun{
		TypeMeta: metav1.TypeMeta{
			APIVersion: platformv1alpha1.GroupVersion.String(),
			Kind:       "AgentRun",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       runName,
			Namespace:  namespace,
			Finalizers: []string{platformv1alpha1.AgentRunCleanupFinalizer},
			Annotations: map[string]string{
				"platform.gratefulagents.dev/direct-ingress": "true",
			},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: trigger,
			Repository: platformv1alpha1.RepositoryContext{
				URL:             repoURL,
				BaseBranch:      baseBranch,
				AdditionalRepos: additionalRepos,
			},
			Context:        runContext,
			ExecutionMode:  defaults.ResolveExecutionMode(),
			WorkflowMode:   workflowMode,
			ModeRef:        modeRef,
			Overseer:       overseer,
			Model:          prefixedModel(model, provider),
			ReasoningLevel: reasoningLevel,
			AuthMode:       authMode,
			OpenAIBaseURL:  openAIBaseURL,
			Image:          image,
			Secrets:        secrets,
		},
	}
	if defaults.Team != nil {
		run.Spec.Team = defaults.Team.DeepCopy()
	}
	if project, ok := owner.(*triggersv1alpha1.Project); ok {
		if project.Spec.ReviewLoop != nil && !project.Spec.ReviewLoop.Disabled {
			run.Annotations[projectReviewLoopAnnotation] = projectReviewLoopEnabled
		} else {
			run.Annotations[projectReviewLoopAnnotation] = projectReviewLoopDisabled
		}
	}
	if opts.repoless {
		run.Annotations["platform.gratefulagents.dev/repoless"] = "true"
	}
	if triggersv1alpha1.IsOpenAICompatibleProvider(provider) {
		run.Annotations[openAIApiModeAnnotation] = triggersv1alpha1.NormalizeOpenAIAPIForProvider(provider, defaults.OpenAIAPI)
	}
	// Snapshot the creating user's git author identity.
	if err := s.stampGitIdentityAnnotations(ctx, run); err != nil {
		return nil, err
	}
	// Personal role model preferences are immutable run inputs: snapshot them
	// now so later edits affect new runs without changing in-flight work.
	if err := s.stampRoleModelOverrides(ctx, run); err != nil {
		return nil, err
	}
	profile, profileRef, err := s.resolveRuntimeProfile(ctx, namespace, defaults.RuntimeProfileRef)
	if err != nil {
		return nil, err
	}
	applyRuntimeProfileDefaultsToAgentRun(run, profile, profileRef)
	mcpPolicy, mcpPolicyRef, err := s.resolveMCPPolicy(ctx, namespace, defaults.MCPPolicyRef)
	if err != nil {
		return nil, err
	}
	applyMCPPolicyDefaultsToAgentRun(run, mcpPolicy, mcpPolicyRef)
	// Attach the source's MCP servers and skills, mirroring the trigger run
	// builder's applyPolicyRefs.
	if len(defaults.MCPServerRefs) > 0 {
		refs := make([]platformv1alpha1.NamedRef, len(defaults.MCPServerRefs))
		copy(refs, defaults.MCPServerRefs)
		run.Spec.MCPServerRefs = refs
	}
	if len(defaults.SkillRefs) > 0 {
		refs := make([]platformv1alpha1.NamedRef, len(defaults.SkillRefs))
		copy(refs, defaults.SkillRefs)
		run.Spec.SkillRefs = refs
	}
	// Admin-set trigger opt-out of the bubblewrap command sandbox. Only
	// sourced from the trigger CRD defaults (kubectl/GitOps); never from the
	// caller's request.
	if defaults.DisableCommandSandbox {
		run.Spec.DisableCommandSandbox = true
	}
	// Kubernetes admin comes from admin-controlled source config only, never
	// from the caller's request: either the source's defaults-level flag
	// (kubectl/GitOps, shared with trigger-created runs) or the Project-level
	// option gated behind the admin role at Project create/update time.
	if defaults.KubernetesAdmin {
		run.Spec.KubernetesAdmin = true
	}
	if p, ok := owner.(*triggersv1alpha1.Project); ok && p.Spec.KubernetesAdmin {
		run.Spec.KubernetesAdmin = true
	}
	// Project runs are retained as immutable history after Project deletion.
	// Standalone legacy sources keep their existing owner-reference lifecycle.
	if _, isProject := owner.(*triggersv1alpha1.Project); !isProject {
		if err := ctrl.SetControllerReference(owner, run, s.scheme); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("setting owner reference on AgentRun: %w", err))
		}
	}
	if err := s.k8sClient.Create(ctx, run); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("AgentRun %s/%s already exists", namespace, runName))
		}
		return nil, mapK8sError("create AgentRun", err)
	}
	if err := s.initializeDirectIngressStatus(ctx, run); err != nil {
		return nil, err
	}

	// Create Postgres session and seed the user request as the first message.
	var seededImages []sessionclient.MessageImage
	if s.stateStore != nil {
		sess, err := s.stateStore.CreateSession(ctx, run.Name, run.Namespace, "pending", "setup")
		if err != nil {
			s.rollbackCreatedAgentRun(ctx, run)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session for %s/%s: %w", run.Namespace, run.Name, err))
		}
		if userRequest != "" || len(req.GetImageDataUrls()) > 0 {
			seedImages, err := sessionclient.ParseImageDataURLs(req.GetImageDataUrls())
			if err != nil {
				s.rollbackCreatedAgentRun(ctx, run)
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid image attachment: %w", err))
			}
			seedImages, err = s.persistMessageImageAssets(ctx, run, seedImages)
			if err != nil {
				s.rollbackCreatedAgentRun(ctx, run)
				return nil, messageAssetError(err)
			}
			metadata := sessionclient.EncodeUserMessageMetadataWithImages(sessionclient.UserMessageModeEnqueue, seedImages)
			if _, err := s.stateStore.AppendMessage(ctx, sess.ID, "user", userRequest, metadata); err != nil {
				s.deleteMessageImageAssets(ctx, seedImages)
				s.rollbackCreatedAgentRun(ctx, run)
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("seed initial user request for %s/%s: %w", run.Namespace, run.Name, err))
			}
			seededImages = seedImages
		}
	}

	// Record resource ownership. This must succeed: an unowned run is treated
	// as system-created and becomes visible to every authenticated user, so a
	// silently dropped ownership record would leak a user's private run.
	if s.stateStore != nil {
		actor := requestActorFromContext(ctx)
		if actor.Subject != "" {
			if err := s.stateStore.SetResourceOwner(ctx, "agent_run", run.Name, run.Namespace, actor.Subject); err != nil {
				s.deleteMessageImageAssets(ctx, seededImages)
				s.rollbackCreatedAgentRun(ctx, run)
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("record ownership for AgentRun %s/%s: %w", run.Namespace, run.Name, err))
			}
		}
	}

	return k8sAgentRunToProto(run), nil
}

// rollbackCreatedAgentRun removes database state for a just-created run before
// hard-deleting it when post-create setup fails. The normal finalizer cannot
// run in this path because setup failed before the run should be exposed, so do
// the state-store cleanup explicitly and then remove finalizers to unblock the
// rollback delete.
func (s *Server) rollbackCreatedAgentRun(ctx context.Context, run *platformv1alpha1.AgentRun) {
	if run == nil {
		return
	}
	key := client.ObjectKeyFromObject(run)
	fresh := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, key, fresh); err != nil {
		return
	}
	if s.stateStore != nil {
		if err := s.stateStore.DeleteAgentRunData(ctx, fresh.Name, fresh.Namespace, projectStateIDForAgentRun(fresh)); err != nil {
			_ = s.k8sClient.Delete(ctx, fresh)
			return
		}
	}
	if len(fresh.Finalizers) > 0 {
		patch := client.MergeFrom(fresh.DeepCopy())
		fresh.Finalizers = nil
		_ = s.k8sClient.Patch(ctx, fresh, patch)
	}
	_ = s.k8sClient.Delete(ctx, fresh)
}

func projectStateIDForAgentRun(run *platformv1alpha1.AgentRun) string {
	if run == nil {
		return ""
	}
	return projectstate.ProjectID(run.Namespace, run.Spec.Repository.URL)
}

// generateRunName builds a run name from the source (project/trigger) name
// plus a short random suffix, e.g. "chat-app-x7k2mq". The user's request text
// is deliberately never used: the run name doubles as the git branch name, so
// prompt-derived names would leak request text into branch names pushed to
// GitHub. Prompt context belongs in the run display name instead.
func generateRunName(sourceName, startMode string) string {
	base := sanitizeRunName(sourceName)
	if base == "" {
		if startMode == "chat" {
			base = "chat-task"
		} else if startMode == "auto" {
			base = "auto-task"
		} else {
			base = "plan-task"
		}
	}
	if startMode == "chat" {
		if !strings.HasPrefix(base, "chat-") && base != "chat" {
			base = "chat-" + base
		}
	} else if startMode == "auto" {
		if !strings.HasPrefix(base, "auto-") && base != "auto" {
			base = "auto-" + base
		}
	} else if !strings.HasPrefix(base, "plan-") && base != "plan" {
		base = "plan-" + base
	}

	suffix := randomRunNameSuffix(6)

	// Kubernetes metadata.name max length is 63 for DNS labels.
	maxBaseLen := 63 - 1 - len(suffix)
	if len(base) > maxBaseLen {
		base = strings.Trim(base[:maxBaseLen], "-")
	}
	if base == "" {
		if startMode == "auto" {
			base = "auto"
		} else {
			base = "plan"
		}
	}
	return fmt.Sprintf("%s-%s", base, suffix)
}

// randomRunNameSuffix returns n random lowercase base36 characters, safe for
// use in a DNS-1123 label.
func randomRunNameSuffix(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return string(buf)
}

func toPlatformAgentRunTrigger(src *platform.SourceRef) platformv1alpha1.TriggerRef {
	if src == nil {
		return platformv1alpha1.TriggerRef{}
	}
	trigger := platformv1alpha1.TriggerRef{
		Kind: src.Kind,
		Name: src.Name,
	}
	if src.IssueId != "" || src.IssueIdentifier != "" || src.IssueUrl != "" {
		trigger.ExternalRef = &platformv1alpha1.ExternalRef{
			ID:         src.IssueId,
			Identifier: src.IssueIdentifier,
			URL:        src.IssueUrl,
		}
	}
	return trigger
}

func sanitizeRunName(input string) string {
	trimmed := strings.ToLower(strings.TrimSpace(input))
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	prevDash := false
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			prevDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func resolveAuthMode(override string, fallback platformv1alpha1.AgentRunAuthMode) platformv1alpha1.AgentRunAuthMode {
	mode := strings.TrimSpace(override)
	if mode == "" {
		mode = string(fallback)
	}
	return triggersv1alpha1.NormalizeAuthMode(mode)
}

// resolveReasoningLevel validates an optional run-level reasoning setting. An
// empty value is allowed and means "use the provider/model default".
func resolveReasoningLevel(value string) (platformv1alpha1.ModeReasoningLevel, error) {
	level := strings.ToLower(strings.TrimSpace(value))
	switch platformv1alpha1.ModeReasoningLevel(level) {
	case "":
		return "", nil
	case platformv1alpha1.ReasoningNone,
		platformv1alpha1.ReasoningLow,
		platformv1alpha1.ReasoningMedium,
		platformv1alpha1.ReasoningHigh,
		platformv1alpha1.ReasoningXHigh,
		platformv1alpha1.ReasoningMax:
		return platformv1alpha1.ModeReasoningLevel(level), nil
	default:
		return "", fmt.Errorf("invalid reasoning level %q (want one of none, low, medium, high, xhigh, max)", value)
	}
}

func validateProviderAuthConfiguration(provider string, authMode platformv1alpha1.AgentRunAuthMode, apiKeySecret, openAIOAuthSecret string, providerKeys []platformv1alpha1.ProviderKeyRef) error {
	if err := triggersv1alpha1.ValidateProviderAuthMode(provider, authMode); err != nil {
		return err
	}
	apiKeySecret = strings.TrimSpace(apiKeySecret)
	openAIOAuthSecret = strings.TrimSpace(openAIOAuthSecret)

	if !triggersv1alpha1.OAuthSupportedForProvider(provider) && openAIOAuthSecret != "" {
		return fmt.Errorf("openaiOAuthSecret is only supported for OAuth-capable providers")
	}
	if triggersv1alpha1.RequiresOpenAIOAuthSecret(provider, authMode) {
		if openAIOAuthSecret == "" {
			return fmt.Errorf("provider %q with authMode %q requires openaiOAuthSecret", provider, authMode)
		}
		return nil
	}
	if apiKeySecret == "" && len(providerKeys) == 0 {
		return fmt.Errorf("provider %q with authMode %q requires claudeApiKeySecret or providerKeys", provider, authMode)
	}
	return nil
}

func resolveProvider(override, fallback string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(override))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(fallback))
	}
	if provider == "" {
		provider = triggersv1alpha1.ProviderOpenAI
	}
	switch provider {
	case triggersv1alpha1.ProviderAnthropic,
		triggersv1alpha1.ProviderOpenAI,
		triggersv1alpha1.ProviderGemini,
		triggersv1alpha1.ProviderOpenRouter,
		triggersv1alpha1.ProviderGroq,
		triggersv1alpha1.ProviderXAI,
		triggersv1alpha1.ProviderCopilot:
		return provider, nil
	default:
		return "", fmt.Errorf("unsupported provider %q (expected anthropic, openai, gemini, openrouter, groq, xai, or copilot)", provider)
	}
}

// resolveProviderFromModel extracts a recognized platform-provider prefix from
// a model name like "anthropic/claude-sonnet-4-6". Provider-native namespaces
// such as OpenRouter's "z-ai/glm-4.7" remain part of the model ID.
func resolveProviderFromModel(model string) (provider, bareModel string) {
	return triggersv1alpha1.SplitProviderModel(model)
}

// prefixedModel stores an unambiguous provider/model route while preserving
// provider-native model IDs that contain slashes.
func prefixedModel(model, provider string) string {
	return triggersv1alpha1.PrefixModelWithProvider(model, provider)
}

func initializeDirectIngressAgentRunStatus(run *platformv1alpha1.AgentRun) {
	if run == nil {
		return
	}
	// Creation only means the API accepted the run. The controller has not yet
	// admitted a sandbox and no worker pod is running, so publish an honest
	// queued state until reconciliation confirms readiness.
	run.Status.Phase = platformv1alpha1.AgentRunPhasePending
	run.Status.CurrentStep = "starting"
	run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Queued"}
}

func (s *Server) initializeDirectIngressStatus(ctx context.Context, run *platformv1alpha1.AgentRun) error {
	if run == nil {
		return nil
	}

	key := client.ObjectKeyFromObject(run)
	var updatedStatus platformv1alpha1.AgentRunStatus
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		target := &platformv1alpha1.AgentRun{}
		if err := s.k8sClient.Get(ctx, key, target); err != nil {
			if k8serrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		initializeDirectIngressAgentRunStatus(target)
		if err := s.k8sClient.Status().Update(ctx, target); err != nil {
			if k8serrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		updatedStatus = target.Status
		return nil
	})
	if err != nil {
		return mapK8sError("update AgentRun status", err)
	}
	run.Status = updatedStatus
	return nil
}

func effectiveModelForProvider(model, provider string) (string, error) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		trimmed = triggersv1alpha1.DefaultMainModelForProvider(provider)
	}
	if trimmed == "" {
		return "", fmt.Errorf("model is required for provider %q; set source defaults.model or provide create request model explicitly", provider)
	}
	switch strings.ToLower(trimmed) {
	case "small", "medium", "large":
		return "", fmt.Errorf("model alias %q is not allowed; set an explicit model id", trimmed)
	}
	return trimmed, nil
}
