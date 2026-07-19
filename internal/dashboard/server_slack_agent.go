package dashboard

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"google.golang.org/protobuf/types/known/emptypb"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// slackTokensLabel marks the Slack tokens Secret for discoverability.
	slackTokensLabel = "platform.gratefulagents.dev/slack-tokens"
	// slackTokensAgentLabel records which SlackAgent a token Secret belongs to.
	slackTokensAgentLabel = "platform.gratefulagents.dev/slack-agent"
	// slackGitHubTokenLabel marks a per-agent GitHub token Secret that overrides
	// the owner's saved GitHub token for runs the agent creates.
	slackGitHubTokenLabel = "platform.gratefulagents.dev/slack-github-token"

	slackResourceType = "slackagent"
	slackTriggerKind  = "SlackAgent"
)

func normalizeSlackAgentName(raw string) (string, error) {
	name := sanitizeDNSLabel(raw)
	if name == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}
	if len(name) > maxDNSLabelLen {
		name = strings.Trim(name[:maxDNSLabelLen], "-")
	}
	if name == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}
	return name, nil
}

func slackTokensSecretName(agentName string) string {
	return defaultManagedResourceName(agentName, "slack-tokens")
}

func slackRuntimeProfileName(agentName string) string {
	return defaultManagedResourceName(agentName, "slack-runtime")
}

func slackMCPPolicyName(agentName string) string {
	return defaultManagedResourceName(agentName, "slack-policy")
}

// slackGitHubSecretName is the per-agent GitHub token Secret. Its "token" key
// matches both the controller's githubTokenEnv mount and the saved-credential
// secret shape, so AgentRun.Spec.Secrets.GitHubTokenSecret can point at either.
func slackGitHubSecretName(agentName string) string {
	return defaultManagedResourceName(agentName, "slack-github")
}

// ListSlackAgents returns all Slack agents configured in the caller's namespace.
// Token values are never returned, only presence flags.
func (s *Server) ListSlackAgents(ctx context.Context, _ *platform.ListSlackAgentsRequest) (*platform.ListSlackAgentsResponse, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}

	list := &triggersv1alpha1.SlackAgentList{}
	if err := s.k8sClient.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, mapK8sError("list SlackAgents", err)
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Name < list.Items[j].Name
	})

	out := &platform.ListSlackAgentsResponse{Namespace: namespace}
	for i := range list.Items {
		out.Agents = append(out.Agents, s.slackAgentStateFromCR(ctx, namespace, &list.Items[i]))
	}
	return out, nil
}

func (s *Server) slackAgentState(ctx context.Context, namespace, name string) (*platform.SlackAgent, error) {
	out := &platform.SlackAgent{
		Namespace:        namespace,
		Name:             name,
		BotTokenPresent:  s.secretKeyPresent(ctx, namespace, slackTokensSecretName(name), triggersv1alpha1.SlackBotTokenKey),
		UserTokenPresent: s.secretKeyPresent(ctx, namespace, slackTokensSecretName(name), triggersv1alpha1.SlackUserTokenKey),
		AppTokenPresent:  s.secretKeyPresent(ctx, namespace, slackTokensSecretName(name), triggersv1alpha1.SlackAppTokenKey),
	}

	agent := &triggersv1alpha1.SlackAgent{}
	err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, agent)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return out, nil
		}
		return nil, mapK8sError("read SlackAgent", err)
	}

	return s.slackAgentStateFromCR(ctx, namespace, agent), nil
}

func (s *Server) slackAgentStateFromCR(ctx context.Context, namespace string, agent *triggersv1alpha1.SlackAgent) *platform.SlackAgent {
	secretName := strings.TrimSpace(agent.Spec.TokensSecret)
	if secretName == "" {
		secretName = slackTokensSecretName(agent.Name)
	}
	out := &platform.SlackAgent{
		Namespace:        namespace,
		Name:             agent.Name,
		BotTokenPresent:  s.secretKeyPresent(ctx, namespace, secretName, triggersv1alpha1.SlackBotTokenKey),
		UserTokenPresent: s.secretKeyPresent(ctx, namespace, secretName, triggersv1alpha1.SlackUserTokenKey),
		AppTokenPresent:  s.secretKeyPresent(ctx, namespace, secretName, triggersv1alpha1.SlackAppTokenKey),
	}
	out.Configured = true
	if agent.UsesWorkspace() {
		wsNamespace, wsName := agent.ResolvedWorkspaceRef()
		out.WorkspaceRefName = wsName
		out.WorkspaceRefNamespace = wsNamespace
		// Members have no own tokens; presence reflects the shared workspace's.
		ws := &triggersv1alpha1.SlackWorkspace{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: wsNamespace, Name: wsName}, ws); err == nil {
			wsSecret := strings.TrimSpace(ws.Spec.TokensSecret)
			out.BotTokenPresent = s.secretKeyPresent(ctx, wsNamespace, wsSecret, triggersv1alpha1.SlackBotTokenKey)
			out.AppTokenPresent = s.secretKeyPresent(ctx, wsNamespace, wsSecret, triggersv1alpha1.SlackAppTokenKey)
			out.UserTokenPresent = false
		}
	}
	out.SlackUserId = agent.Spec.SlackUserID
	out.GithubTokenPresent = s.secretKeyPresent(ctx, namespace, slackGitHubSecretName(agent.Name), userCredGithubTokenKey)
	out.ChannelReplyMode = string(normalizeSlackChannelReplyMode(string(agent.Spec.ChannelReplyMode)))
	out.Commanders = agent.Spec.Commanders
	if ah := agent.Spec.AppHome; ah != nil {
		out.AppHomeHeader = ah.Header
		out.AppHomeText = ah.Text
	}
	if agent.Spec.SessionIdleMinutes != nil {
		out.SessionIdleMinutes = *agent.Spec.SessionIdleMinutes
	}
	out.Suspended = agent.Spec.Suspend
	out.Model = agent.Spec.Defaults.Model
	out.ReasoningLevel = string(agent.Spec.Defaults.ReasoningLevel)
	out.AdditionalRepoUrls = append([]string(nil), agent.Spec.Defaults.AdditionalRepos...)
	out.Image = strings.TrimSpace(agent.Spec.Image)
	out.Provider = agent.Spec.Defaults.Provider //nolint:staticcheck // stored provider echoed back to the settings form
	out.TeamId = agent.Status.TeamID
	out.BotUserId = agent.Status.BotUserID
	out.LastError = agent.Status.LastError
	out.Ready = slackConditionTrue(agent, triggersv1alpha1.ConditionSlackAgentReady)
	out.TokenValid = slackConditionTrue(agent, triggersv1alpha1.ConditionSlackAgentTokenValid)
	out.Connected = slackConditionTrue(agent, triggersv1alpha1.ConditionSlackAgentConnected)
	if ref := agent.Spec.Defaults.RuntimeProfileRef; ref != nil {
		out.RuntimeProfileRef = ref.Name
		out.PermissionMode, out.EgressMode = s.runtimeProfileModes(ctx, namespace, ref.Name)
	}
	if ref := agent.Spec.Defaults.MCPPolicyRef; ref != nil {
		out.McpPolicyRef = ref.Name
		out.McpPolicyDefaultAction, out.McpPolicyAllowedServers = s.mcpPolicyConfig(ctx, namespace, ref.Name)
	}
	for _, ref := range agent.Spec.Defaults.MCPServerRefs {
		out.McpServerRefs = append(out.McpServerRefs, ref.Name)
	}
	for _, ref := range agent.Spec.Defaults.SkillRefs {
		out.SkillRefs = append(out.SkillRefs, ref.Name)
	}
	return out
}

func (s *Server) runtimeProfileModes(ctx context.Context, namespace, name string) (permission, egress string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ""
	}
	profile := &platformv1alpha1.RuntimeProfile{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, profile); err != nil {
		return "", ""
	}
	if profile.Spec.Security == nil {
		return "", ""
	}
	return string(profile.Spec.Security.PermissionMode), string(profile.Spec.Security.EgressMode)
}

func (s *Server) mcpPolicyConfig(ctx context.Context, namespace, name string) (defaultAction string, allowedServers []string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	policy := &platformv1alpha1.MCPPolicy{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, policy); err != nil {
		return "", nil
	}
	defaultAction = string(policy.Spec.DefaultAction)
	for _, server := range policy.Spec.AllowedServers {
		if trimmed := strings.TrimSpace(server.Name); trimmed != "" {
			allowedServers = append(allowedServers, trimmed)
		}
	}
	return defaultAction, allowedServers
}

func slackConditionTrue(agent *triggersv1alpha1.SlackAgent, condType string) bool {
	return meta.IsStatusConditionTrue(agent.Status.Conditions, condType)
}

// UpdateSlackAgent creates or updates a named Slack agent in the caller's
// namespace. Tokens are isolated per agent in a dedicated Secret.
func (s *Server) UpdateSlackAgent(ctx context.Context, req *platform.UpdateSlackAgentRequest) (*platform.SlackAgent, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	name, err := normalizeSlackAgentName(req.GetName())
	if err != nil {
		return nil, err
	}

	model := strings.TrimSpace(req.GetModel())
	if model == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("model is required"))
	}

	// Shared-workspace mode: the agent joins an existing SlackWorkspace app
	// instead of bringing its own tokens.
	var workspaceRef *triggersv1alpha1.SlackWorkspaceRef
	if wsName := strings.TrimSpace(req.GetWorkspaceName()); wsName != "" {
		wsNamespace := strings.TrimSpace(req.GetWorkspaceNamespace())
		if wsNamespace == "" {
			wsNamespace = namespace
		}
		ws := &triggersv1alpha1.SlackWorkspace{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: wsNamespace, Name: wsName}, ws); err != nil {
			if k8serrors.IsNotFound(err) {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("SlackWorkspace %s/%s not found", wsNamespace, wsName))
			}
			return nil, mapK8sError("read SlackWorkspace", err)
		}
		if strings.TrimSpace(req.GetSlackUserId()) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("slack_user_id is required when joining a workspace (the connector routes your messages by it)"))
		}
		workspaceRef = &triggersv1alpha1.SlackWorkspaceRef{Name: wsName, Namespace: wsNamespace}
	}

	provider, err := resolveProvider(req.GetProvider(), "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	authMode := triggersv1alpha1.NormalizeAuthMode(req.GetAuthMode())
	// Copilot authenticates only via OAuth material; coerce so saved Copilot
	// credentials (stored as OAuth) validate for created runs.
	if provider == triggersv1alpha1.ProviderCopilot {
		authMode = platformv1alpha1.AgentRunAuthModeOAuth
	}
	if err := triggersv1alpha1.ValidateProviderAuthMode(provider, authMode); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if workspaceRef == nil {
		if err := s.writeSlackTokens(ctx, namespace, name, req); err != nil {
			return nil, err
		}
	}

	// Agent-specific GitHub token (both connection modes): written before
	// credential resolution so it can satisfy the GitHub token requirement.
	if err := s.writeSlackGitHubToken(ctx, namespace, name, req); err != nil {
		return nil, err
	}

	// Resolve run credentials from the user's saved provider credentials, saving
	// any inline keys provided in this request first so they become "saved".
	if !req.GetUseSavedCredentials() {
		if k := strings.TrimSpace(req.GetAnthropicApiKey()); k != "" {
			if err := s.applyCredentialValue(ctx, namespace, triggersv1alpha1.ProviderAnthropic, userCredAPIKeyKey, k, false); err != nil {
				return nil, err
			}
		}
		if k := strings.TrimSpace(req.GetOpenaiApiKey()); k != "" {
			keyProvider := triggersv1alpha1.ProviderOpenAI
			// The Slack RPC retains one OpenAI-compatible inline key field. Store
			// that key under providers with dedicated saved credential slots so
			// the resolver mounts the same one-off key it just received.
			if provider == triggersv1alpha1.ProviderOpenRouter || provider == triggersv1alpha1.ProviderXAI {
				keyProvider = provider
			}
			if err := s.applyCredentialValue(ctx, namespace, keyProvider, userCredAPIKeyKey, k, false); err != nil {
				return nil, err
			}
		}
	}
	secrets := triggersv1alpha1.AgentRunSecrets{}
	if err := s.applySlackSavedCredentials(ctx, namespace, name, provider, authMode, &secrets); err != nil {
		return nil, err
	}

	runtimeProfileRef, _, err := s.applyConfiguredRuntimeProfile(
		ctx,
		namespace,
		slackRuntimeProfileName(name),
		req.GetConfigureRuntimeProfile(),
		req.GetRuntimeProfileRef(),
		req.GetPermissionMode(),
		req.GetEgressMode(),
	)
	if err != nil {
		return nil, err
	}
	mcpPolicyRef, _, err := s.applyConfiguredMCPPolicy(
		ctx,
		namespace,
		slackMCPPolicyName(name),
		req.GetConfigureMcpPolicy(),
		req.GetMcpPolicyRef(),
		req.GetMcpPolicyDefaultAction(),
		req.GetMcpPolicyAllowedServers(),
	)
	if err != nil {
		return nil, err
	}

	if err := s.requireSlackGitHubCredentialSecret(ctx, namespace, name); err != nil {
		return nil, err
	}

	if err := s.applySlackAgentCR(ctx, namespace, name, req, model, provider, authMode, secrets, runtimeProfileRef, mcpPolicyRef, workspaceRef); err != nil {
		return nil, err
	}
	if err := s.syncSlackAgentRuns(ctx, namespace, name, model, provider, authMode, secrets); err != nil {
		return nil, err
	}

	if s.stateStore != nil && actor.Subject != "" {
		if err := s.stateStore.SetResourceOwner(ctx, slackResourceType, name, namespace, actor.Subject); err != nil {
			// Ownership is best-effort; the agent is already created.
			_ = err
		}
	}

	return s.slackAgentState(ctx, namespace, name)
}

// applySlackSavedCredentials wires Slack-created runs to every saved API-key
// provider credential up front. That lets a running Slack thread switch provider
// via the live AgentRun model without needing a pod restart, as long as the
// target provider uses API-key auth.
func (s *Server) applySlackSavedCredentials(ctx context.Context, namespace, agentName, provider string, authMode platformv1alpha1.AgentRunAuthMode, secrets *triggersv1alpha1.AgentRunSecrets) error {
	state := s.userCredentialState(ctx, namespace)

	// An agent-specific GitHub token beats the owner's saved one.
	switch {
	case s.secretKeyPresent(ctx, namespace, slackGitHubSecretName(agentName), userCredGithubTokenKey):
		secrets.GithubToken = slackGitHubSecretName(agentName)
	case state.githubToken:
		secrets.GithubToken = userCredentialSecretName(credentialGitHub)
	}

	if state.anthropicAPIKey {
		secrets.ProviderKeys = append(secrets.ProviderKeys, platformv1alpha1.ProviderKeyRef{
			Provider:   triggersv1alpha1.ProviderAnthropic,
			SecretName: userCredentialSecretName(triggersv1alpha1.ProviderAnthropic),
			SecretKey:  userCredAPIKeyKey,
		})
	}
	if state.openaiAPIKey {
		openAISecret := userCredentialSecretName(triggersv1alpha1.ProviderOpenAI)
		for _, p := range []string{
			triggersv1alpha1.ProviderOpenAI,
			triggersv1alpha1.ProviderGemini,
			triggersv1alpha1.ProviderGroq,
		} {
			secrets.ProviderKeys = append(secrets.ProviderKeys, platformv1alpha1.ProviderKeyRef{
				Provider:   p,
				SecretName: openAISecret,
				SecretKey:  userCredAPIKeyKey,
			})
		}
	}
	for _, saved := range []struct {
		provider string
		present  bool
	}{
		{triggersv1alpha1.ProviderOpenRouter, state.openrouterAPIKey},
		{triggersv1alpha1.ProviderXAI, state.xaiAPIKey},
	} {
		if saved.present {
			secrets.ProviderKeys = append(secrets.ProviderKeys, platformv1alpha1.ProviderKeyRef{
				Provider:   saved.provider,
				SecretName: userCredentialSecretName(saved.provider),
				SecretKey:  userCredAPIKeyKey,
			})
		}
	}

	switch {
	case provider == triggersv1alpha1.ProviderCopilot:
		if !state.copilotOAuth {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no saved Copilot credentials; add them in Settings"))
		}
		secrets.OpenAIOAuthSecret = userCredentialSecretName(triggersv1alpha1.ProviderCopilot)
	case provider == triggersv1alpha1.ProviderAnthropic && authMode == platformv1alpha1.AgentRunAuthModeOAuth:
		if !state.anthropicOAuth {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no saved Anthropic OAuth credentials; add them in Settings"))
		}
		secrets.OpenAIOAuthSecret = userCredentialSecretName(triggersv1alpha1.ProviderAnthropic)
	case provider == triggersv1alpha1.ProviderAnthropic:
		if !providerKeyConfigured(secrets.ProviderKeys, triggersv1alpha1.ProviderAnthropic) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no saved Anthropic API key; add it in Settings"))
		}
	case authMode == platformv1alpha1.AgentRunAuthModeOAuth:
		if !state.openaiOAuth {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no saved %s OAuth credentials; add them in Settings", provider))
		}
		secrets.OpenAIOAuthSecret = userCredentialSecretName(triggersv1alpha1.ProviderOpenAI)
	default:
		if !providerKeyConfigured(secrets.ProviderKeys, provider) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no saved %s API key; add it in Settings", provider))
		}
	}
	return nil
}

func (s *Server) syncSlackAgentRuns(ctx context.Context, namespace, agentName, model, provider string, authMode platformv1alpha1.AgentRunAuthMode, secrets triggersv1alpha1.AgentRunSecrets) error {
	list := &platformv1alpha1.AgentRunList{}
	if err := s.k8sClient.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return mapK8sError("list Slack AgentRuns", err)
	}

	storedModel := runtimeConfigStoredModel(model, provider)
	openAIBaseURL := triggersv1alpha1.ResolveOpenAIBaseURLWithAuth(provider, "", authMode)
	runSecrets := slackAgentRunSecrets(secrets)

	for i := range list.Items {
		run := &list.Items[i]
		if run.Spec.Trigger.Kind != slackTriggerKind || run.Spec.Trigger.Name != agentName {
			continue
		}
		if isTerminalAgentRunPhase(run.Status.Phase) {
			continue
		}

		_, err := s.patchAgentRunWithRetry(ctx, namespace, run.Name, func(fresh *platformv1alpha1.AgentRun) error {
			if fresh.Spec.Trigger.Kind != slackTriggerKind || fresh.Spec.Trigger.Name != agentName {
				return nil
			}
			if isTerminalAgentRunPhase(fresh.Status.Phase) {
				return nil
			}
			fresh.Spec.Model = storedModel
			fresh.Spec.AuthMode = authMode
			fresh.Spec.OpenAIBaseURL = openAIBaseURL
			fresh.Spec.Secrets = runSecrets.DeepCopy()
			return nil
		})
		if err != nil {
			if connect.CodeOf(err) != connect.CodeUnknown {
				return err
			}
			return mapK8sError("sync Slack AgentRun runtime", err)
		}
	}
	return nil
}

func slackAgentRunSecrets(secrets triggersv1alpha1.AgentRunSecrets) *platformv1alpha1.AgentRunSecrets {
	return &platformv1alpha1.AgentRunSecrets{
		ClaudeAPIKeySecret: secrets.ClaudeApiKey,
		OpenAIOAuthSecret:  secrets.OpenAIOAuthSecret,
		GitHubTokenSecret:  secrets.GithubToken,
		ProviderKeys:       append([]platformv1alpha1.ProviderKeyRef(nil), secrets.ProviderKeys...),
	}
}

func (s *Server) requireSlackGitHubCredentialSecret(ctx context.Context, namespace, agentName string) error {
	if s.secretKeyPresent(ctx, namespace, slackGitHubSecretName(agentName), userCredGithubTokenKey) {
		return nil
	}
	if s.secretKeyPresent(ctx, namespace, userCredentialSecretName(credentialGitHub), userCredGithubTokenKey) {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no GitHub token; save one in Settings or set an agent-specific token"))
}

// writeSlackGitHubToken writes or clears the agent-specific GitHub token
// Secret. A non-empty github_token sets it; clear: ["github-token"] removes it
// (falling back to the owner's saved GitHub token); otherwise it is untouched.
func (s *Server) writeSlackGitHubToken(ctx context.Context, namespace, agentName string, req *platform.UpdateSlackAgentRequest) error {
	clearRequested := false
	for _, c := range req.GetClear() {
		if strings.ToLower(strings.TrimSpace(c)) == "github-token" {
			clearRequested = true
		}
	}
	token := strings.TrimSpace(req.GetGithubToken())
	secretName := slackGitHubSecretName(agentName)
	key := client.ObjectKey{Namespace: namespace, Name: secretName}

	if clearRequested {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace}}
		if err := s.k8sClient.Delete(ctx, secret); err != nil && !k8serrors.IsNotFound(err) {
			return mapK8sError("delete Slack GitHub token secret", err)
		}
		return nil
	}
	if token == "" {
		return nil
	}

	secret := &corev1.Secret{}
	err := s.k8sClient.Get(ctx, key, secret)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return mapK8sError("read Slack GitHub token secret", err)
		}
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
				Labels: map[string]string{
					slackGitHubTokenLabel: "true",
					slackTokensAgentLabel: agentName,
				},
			},
			Data: map[string][]byte{userCredGithubTokenKey: []byte(token)},
		}
		if err := s.k8sClient.Create(ctx, secret); err != nil {
			return mapK8sError("create Slack GitHub token secret", err)
		}
		return nil
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data[userCredGithubTokenKey] = []byte(token)
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels[slackGitHubTokenLabel] = "true"
	secret.Labels[slackTokensAgentLabel] = agentName
	if err := s.k8sClient.Update(ctx, secret); err != nil {
		return mapK8sError("update Slack GitHub token secret", err)
	}
	return nil
}

func (s *Server) applySlackAgentCR(
	ctx context.Context,
	namespace string,
	name string,
	req *platform.UpdateSlackAgentRequest,
	model, provider string,
	authMode platformv1alpha1.AgentRunAuthMode,
	secrets triggersv1alpha1.AgentRunSecrets,
	runtimeProfileRef *platformv1alpha1.NamedRef,
	mcpPolicyRef *platformv1alpha1.NamedRef,
	workspaceRef *triggersv1alpha1.SlackWorkspaceRef,
) error {
	agent := &triggersv1alpha1.SlackAgent{}
	err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, agent)
	if err != nil && !k8serrors.IsNotFound(err) {
		return mapK8sError("read SlackAgent", err)
	}
	creating := k8serrors.IsNotFound(err)

	if creating {
		agent = &triggersv1alpha1.SlackAgent{
			TypeMeta: metav1.TypeMeta{
				APIVersion: triggersv1alpha1.GroupVersion.String(),
				Kind:       "SlackAgent",
			},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		}
	}

	if workspaceRef != nil {
		// Workspace members bring no tokens; the shared connector serves them.
		agent.Spec.TokensSecret = ""
		agent.Spec.WorkspaceRef = workspaceRef
	} else {
		agent.Spec.TokensSecret = slackTokensSecretName(name)
		agent.Spec.WorkspaceRef = nil
	}
	agent.Spec.SlackUserID = strings.TrimSpace(req.GetSlackUserId())
	agent.Spec.Suspend = req.GetSuspend()
	agent.Spec.ChannelReplyMode = normalizeSlackChannelReplyMode(req.GetChannelReplyMode())
	agent.Spec.Commanders = normalizeSlackUserIDs(req.GetCommanders())
	appHome, err := buildAppHomeSpec(req.GetAppHomeHeader(), req.GetAppHomeText())
	if err != nil {
		return err
	}
	agent.Spec.AppHome = appHome
	if m := req.GetSessionIdleMinutes(); m > 0 {
		agent.Spec.SessionIdleMinutes = &m
	} else {
		agent.Spec.SessionIdleMinutes = nil
	}
	// Worker image for the connector pod and the runs it creates; the form
	// manages it wholesale (empty = operator default worker image).
	image := strings.TrimSpace(req.GetImage())
	agent.Spec.Image = image
	reasoningLevel, err := resolveReasoningLevel(req.GetReasoningLevel())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	// Slack agents create repoless runs by default, so there is no primary
	// repository to dedupe the additional repos against.
	additionalRepos, err := normalizeAdditionalRepoURLs(req.GetAdditionalRepoUrls(), "")
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	// Defaults are fully managed by this form, so assign them as a whole (a
	// composite literal also avoids the deprecated-field selector warning),
	// then restore the admin-only flags the form cannot manage.
	priorDefaults := agent.Spec.Defaults
	agent.Spec.Defaults = triggersv1alpha1.AgentRunDefaults{
		Model:             model,
		Provider:          provider,
		AuthMode:          authMode,
		ReasoningLevel:    reasoningLevel,
		AdditionalRepos:   additionalRepos,
		Secrets:           secrets,
		RuntimeProfileRef: runtimeProfileRef,
		MCPPolicyRef:      mcpPolicyRef,
		MCPServerRefs:     namedRefsFromNames(req.GetMcpServerRefs()),
		SkillRefs:         namedRefsFromNames(req.GetSkillRefs()),
		Image:             image,
	}
	preserveAdminOnlyTriggerDefaults(&agent.Spec.Defaults, priorDefaults)

	if creating {
		if err := s.k8sClient.Create(ctx, agent); err != nil {
			return mapK8sError("create SlackAgent", err)
		}
		return nil
	}
	if err := s.k8sClient.Update(ctx, agent); err != nil {
		return mapK8sError("update SlackAgent", err)
	}
	return nil
}

// namedRefsFromNames converts dashboard-form package names into
// NamedRefs, trimming and de-duplicating.
func namedRefsFromNames(names []string) []platformv1alpha1.NamedRef {
	var refs []platformv1alpha1.NamedRef
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		refs = append(refs, platformv1alpha1.NamedRef{Name: n})
	}
	return refs
}

// normalizeSlackChannelReplyMode maps the form value to the CRD enum, defaulting
// unknown or empty values to require-approval (the safe default).
func normalizeSlackChannelReplyMode(mode string) triggersv1alpha1.SlackChannelReplyMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case string(triggersv1alpha1.SlackChannelReplyAuto):
		return triggersv1alpha1.SlackChannelReplyAuto
	default:
		return triggersv1alpha1.SlackChannelReplyRequireApproval
	}
}

// normalizeSlackUserIDs trims, de-duplicates, and drops empties from a list of
// Slack user IDs so allow/deny lists stay clean.
func normalizeSlackUserIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// buildAppHomeSpec validates and builds the optional App Home copy override.
// Empty header and text keep the connector's built-in defaults (nil spec).
func buildAppHomeSpec(header, text string) (*triggersv1alpha1.SlackAppHomeSpec, error) {
	header = strings.TrimSpace(header)
	text = strings.TrimSpace(text)
	if header == "" && text == "" {
		return nil, nil
	}
	if n := len([]rune(header)); n > 150 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("app home header must be at most 150 characters (Slack's header limit), got %d", n))
	}
	if n := len([]rune(text)); n > 1000 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("app home text must be at most 1000 characters, got %d", n))
	}
	return &triggersv1alpha1.SlackAppHomeSpec{Header: header, Text: text}, nil
}

// slackDraftLister is the subset of the state store the drafts list needs. The
// Postgres store implements it; when the backend does not (or is nil) the list
// simply returns no drafts.
type slackDraftLister interface {
	ListSlackDrafts(ctx context.Context, namespace, slackAgent, status string, limit int32) ([]store.SlackDraft, error)
}

// ListSlackDrafts returns reply drafts for a Slack agent, scoped to the caller's
// namespace so drafts never cross tenant boundaries.
func (s *Server) ListSlackDrafts(ctx context.Context, req *platform.ListSlackDraftsRequest) (*platform.ListSlackDraftsResponse, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	name, err := normalizeSlackAgentName(req.GetName())
	if err != nil {
		return nil, err
	}

	out := &platform.ListSlackDraftsResponse{Namespace: namespace}
	lister, ok := s.stateStore.(slackDraftLister)
	if !ok || lister == nil {
		return out, nil // no durable backend → no drafts
	}

	limit := req.GetLimit()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	drafts, err := lister.ListSlackDrafts(ctx, namespace, name, strings.TrimSpace(req.GetStatus()), limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing slack drafts: %w", err))
	}
	for i := range drafts {
		out.Drafts = append(out.Drafts, slackDraftToProto(drafts[i]))
	}
	return out, nil
}

func slackDraftToProto(d store.SlackDraft) *platform.SlackDraft {
	pd := &platform.SlackDraft{
		Id:            d.ID.String(),
		ChannelId:     d.ChannelID,
		TargetUser:    d.TargetUser,
		IncomingText:  d.IncomingText,
		DraftText:     d.DraftText,
		Status:        d.Status,
		CreatedAtUnix: d.CreatedAt.Unix(),
	}
	if d.DecidedAt != nil {
		pd.DecidedAtUnix = d.DecidedAt.Unix()
	}
	return pd
}

// writeSlackTokens merges the supplied tokens into the named agent's Slack
// tokens Secret. Non-empty values are written; keys named in clear are removed;
// the Secret is deleted when it becomes empty.
func (s *Server) writeSlackTokens(ctx context.Context, namespace, agentName string, req *platform.UpdateSlackAgentRequest) error {
	clears := map[string]bool{}
	for _, c := range req.GetClear() {
		clears[strings.ToLower(strings.TrimSpace(c))] = true
	}

	set := map[string][]byte{}
	del := map[string]bool{}
	for _, kv := range []struct {
		key   string
		value string
	}{
		{triggersv1alpha1.SlackBotTokenKey, req.GetBotToken()},
		{triggersv1alpha1.SlackUserTokenKey, req.GetUserToken()},
		{triggersv1alpha1.SlackAppTokenKey, req.GetAppToken()},
	} {
		if clears[kv.key] {
			del[kv.key] = true
			continue
		}
		if v := strings.TrimSpace(kv.value); v != "" {
			set[kv.key] = []byte(v)
		}
	}
	if len(set) == 0 && len(del) == 0 {
		return nil
	}

	secretName := slackTokensSecretName(agentName)
	secret := &corev1.Secret{}
	err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return mapK8sError("read Slack tokens secret", err)
		}
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
				Labels: map[string]string{
					slackTokensLabel:      "true",
					slackTokensAgentLabel: agentName,
				},
			},
			Data: set,
		}
		if err := s.k8sClient.Create(ctx, secret); err != nil {
			return mapK8sError("create Slack tokens secret", err)
		}
		return nil
	}

	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	maps.Copy(secret.Data, set)
	for k := range del {
		delete(secret.Data, k)
	}
	if len(secret.Data) == 0 {
		if err := s.k8sClient.Delete(ctx, secret); err != nil && !k8serrors.IsNotFound(err) {
			return mapK8sError("delete Slack tokens secret", err)
		}
		return nil
	}
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels[slackTokensLabel] = "true"
	secret.Labels[slackTokensAgentLabel] = agentName
	if err := s.k8sClient.Update(ctx, secret); err != nil {
		return mapK8sError("update Slack tokens secret", err)
	}
	return nil
}

// DeleteSlackAgent removes a named Slack agent and its tokens Secret.
func (s *Server) DeleteSlackAgent(ctx context.Context, req *platform.DeleteSlackAgentRequest) (*emptypb.Empty, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	name, err := normalizeSlackAgentName(req.GetName())
	if err != nil {
		return nil, err
	}

	agent := &triggersv1alpha1.SlackAgent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if err := s.k8sClient.Delete(ctx, agent); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete SlackAgent", err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: slackTokensSecretName(name), Namespace: namespace}}
	if err := s.k8sClient.Delete(ctx, secret); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete Slack tokens secret", err)
	}
	githubSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: slackGitHubSecretName(name), Namespace: namespace}}
	if err := s.k8sClient.Delete(ctx, githubSecret); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete Slack GitHub token secret", err)
	}
	return &emptypb.Empty{}, nil
}
