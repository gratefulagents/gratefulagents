package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func slackAgentTestServer(t *testing.T) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	return &Server{k8sClient: c, scheme: scheme}
}

func slackActorContext() context.Context {
	return context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: "u1", Name: "Alice Example"})
}

func seedSlackGitHubCredential(t *testing.T, srv *Server, ctx context.Context) string {
	t.Helper()
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}
	secret := &corev1.Secret{}
	secret.Name = userCredentialSecretName(credentialGitHub)
	secret.Namespace = namespace
	secret.Data = map[string][]byte{userCredGithubTokenKey: []byte("gh-token")}
	ensureCredentialLabels(secret, credentialGitHub)
	if err := srv.k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("create GitHub credential secret: %v", err)
	}
	return namespace
}

func seedSlackAPIKeyCredential(t *testing.T, srv *Server, ctx context.Context, provider string) string {
	t.Helper()
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}
	if err := srv.applyCredentialValue(ctx, namespace, provider, userCredAPIKeyKey, "sk-"+provider, false); err != nil {
		t.Fatalf("seed %s API key credential: %v", provider, err)
	}
	return namespace
}

func TestUpdateSlackAgentCreatesSecretAndCR(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)

	resp, err := srv.UpdateSlackAgent(ctx, &platform.UpdateSlackAgentRequest{
		Name:                    "Support Triage",
		BotToken:                "xoxb-123",
		AppToken:                "xapp-123",
		SlackUserId:             "U0OWNER",
		Model:                   "claude-sonnet-4-6",
		Provider:                "anthropic",
		AuthMode:                "api-key",
		UseSavedCredentials:     false,
		AnthropicApiKey:         "sk-ant-test",
		RuntimeProfileRef:       "slack-runtime-custom",
		ConfigureRuntimeProfile: true,
		PermissionMode:          "workspace-write",
		EgressMode:              "unrestricted",
		McpPolicyRef:            "slack-policy",
		ConfigureMcpPolicy:      true,
		McpPolicyDefaultAction:  "Deny",
		McpPolicyAllowedServers: []string{"fetch", "github"},
	})
	if err != nil {
		t.Fatalf("UpdateSlackAgent() error = %v", err)
	}
	if !resp.Configured {
		t.Fatal("response Configured = false, want true")
	}
	if resp.Name != "support-triage" {
		t.Fatalf("response Name = %q, want support-triage", resp.Name)
	}
	if !resp.BotTokenPresent || !resp.AppTokenPresent {
		t.Fatalf("token presence wrong: bot=%v app=%v", resp.BotTokenPresent, resp.AppTokenPresent)
	}
	if resp.UserTokenPresent {
		t.Error("UserTokenPresent = true, want false")
	}
	if resp.Model != "claude-sonnet-4-6" || resp.Provider != "anthropic" {
		t.Fatalf("model/provider = %q/%q", resp.Model, resp.Provider)
	}

	ns := resp.Namespace
	// SlackAgent CR exists and references the tokens secret + saved creds.
	agent := &triggersv1alpha1.SlackAgent{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: "support-triage"}, agent); err != nil {
		t.Fatalf("Get(SlackAgent) error = %v", err)
	}
	if agent.Spec.TokensSecret != slackTokensSecretName("support-triage") {
		t.Errorf("TokensSecret = %q, want %q", agent.Spec.TokensSecret, slackTokensSecretName("support-triage"))
	}
	if agent.Spec.SlackUserID != "U0OWNER" {
		t.Errorf("SlackUserID = %q", agent.Spec.SlackUserID)
	}
	if len(agent.Spec.Defaults.Secrets.ProviderKeys) != 1 {
		t.Fatalf("ProviderKeys = %v, want 1 referencing saved creds", agent.Spec.Defaults.Secrets.ProviderKeys)
	}
	if agent.Spec.Defaults.RuntimeProfileRef == nil || agent.Spec.Defaults.RuntimeProfileRef.Name != "slack-runtime-custom" {
		t.Fatalf("RuntimeProfileRef = %v, want slack-runtime-custom", agent.Spec.Defaults.RuntimeProfileRef)
	}
	if agent.Spec.Defaults.MCPPolicyRef == nil || agent.Spec.Defaults.MCPPolicyRef.Name != "slack-policy" {
		t.Fatalf("MCPPolicyRef = %v, want slack-policy", agent.Spec.Defaults.MCPPolicyRef)
	}

	// A RuntimeProfile is provisioned only because the request explicitly asked
	// the dashboard to configure one.
	profile := &platformv1alpha1.RuntimeProfile{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: "slack-runtime-custom"}, profile); err != nil {
		t.Fatalf("Get(RuntimeProfile) error = %v", err)
	}
	if profile.Spec.Security == nil {
		t.Fatal("RuntimeProfile.Spec.Security is nil")
	}
	if profile.Spec.Security.EgressMode != platformv1alpha1.EgressMode("unrestricted") {
		t.Errorf("EgressMode = %q, want unrestricted", profile.Spec.Security.EgressMode)
	}
	if profile.Spec.Security.PermissionMode != platformv1alpha1.PermissionModeWorkspaceWrite {
		t.Errorf("PermissionMode = %q, want workspace-write", profile.Spec.Security.PermissionMode)
	}
	if resp.EgressMode != "unrestricted" {
		t.Errorf("response EgressMode = %q, want unrestricted", resp.EgressMode)
	}
	if resp.RuntimeProfileRef != "slack-runtime-custom" {
		t.Errorf("response RuntimeProfileRef = %q, want slack-runtime-custom", resp.RuntimeProfileRef)
	}

	policy := &platformv1alpha1.MCPPolicy{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: "slack-policy"}, policy); err != nil {
		t.Fatalf("Get(MCPPolicy) error = %v", err)
	}
	if policy.Spec.DefaultAction != platformv1alpha1.MCPDefaultActionDeny {
		t.Errorf("MCPPolicy defaultAction = %q, want Deny", policy.Spec.DefaultAction)
	}
	if got := len(policy.Spec.AllowedServers); got != 2 {
		t.Fatalf("AllowedServers len = %d, want 2", got)
	}
	if policy.Spec.AllowedServers[0].Name != "fetch" || policy.Spec.AllowedServers[1].Name != "github" {
		t.Fatalf("AllowedServers = %#v, want fetch/github", policy.Spec.AllowedServers)
	}
	if resp.McpPolicyRef != "slack-policy" {
		t.Errorf("response McpPolicyRef = %q, want slack-policy", resp.McpPolicyRef)
	}
	if resp.McpPolicyDefaultAction != "Deny" {
		t.Errorf("response McpPolicyDefaultAction = %q, want Deny", resp.McpPolicyDefaultAction)
	}

	// Tokens secret holds the bot + app tokens.
	secret := &corev1.Secret{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: slackTokensSecretName("support-triage")}, secret); err != nil {
		t.Fatalf("Get(tokens secret) error = %v", err)
	}
	if secret.Labels[slackTokensAgentLabel] != "support-triage" {
		t.Fatalf("tokens secret agent label = %q, want support-triage", secret.Labels[slackTokensAgentLabel])
	}
	if string(secret.Data[triggersv1alpha1.SlackBotTokenKey]) != "xoxb-123" {
		t.Errorf("bot token = %q", secret.Data[triggersv1alpha1.SlackBotTokenKey])
	}
	if string(secret.Data[triggersv1alpha1.SlackAppTokenKey]) != "xapp-123" {
		t.Errorf("app token = %q", secret.Data[triggersv1alpha1.SlackAppTokenKey])
	}

	githubSecret := &corev1.Secret{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: userCredentialSecretName(credentialGitHub)}, githubSecret); err != nil {
		t.Fatalf("Get(GitHub credential secret) error = %v", err)
	}
	if _, ok := githubSecret.Data[userCredGithubTokenKey]; !ok {
		t.Fatalf("GitHub credential secret missing %q key", userCredGithubTokenKey)
	}
	if got := string(githubSecret.Data[userCredGithubTokenKey]); got != "gh-token" {
		t.Fatalf("GitHub credential token = %q, want existing saved token", got)
	}
	if githubSecret.Labels[userCredentialLabel] != "true" || githubSecret.Labels[userCredentialProviderLabel] != credentialGitHub {
		t.Fatalf("GitHub credential labels = %#v", githubSecret.Labels)
	}
}

func TestUpdateSlackAgentMountsAllSavedAPIKeyCredentials(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)
	seedSlackAPIKeyCredential(t, srv, ctx, triggersv1alpha1.ProviderOpenAI)
	seedSlackAPIKeyCredential(t, srv, ctx, triggersv1alpha1.ProviderOpenRouter)
	seedSlackAPIKeyCredential(t, srv, ctx, triggersv1alpha1.ProviderXAI)

	resp, err := srv.UpdateSlackAgent(ctx, &platform.UpdateSlackAgentRequest{
		Name:                "support",
		BotToken:            "xoxb-123",
		AppToken:            "xapp-123",
		Model:               "claude-sonnet-4-6",
		Provider:            "anthropic",
		AuthMode:            "api-key",
		UseSavedCredentials: false,
		AnthropicApiKey:     "sk-ant-test",
	})
	if err != nil {
		t.Fatalf("UpdateSlackAgent() error = %v", err)
	}

	agent := &triggersv1alpha1.SlackAgent{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: "support"}, agent); err != nil {
		t.Fatalf("Get(SlackAgent) error = %v", err)
	}
	got := providerKeySecretNames(agent.Spec.Defaults.Secrets.ProviderKeys)
	want := map[string]string{
		triggersv1alpha1.ProviderAnthropic:  userCredentialSecretName(triggersv1alpha1.ProviderAnthropic),
		triggersv1alpha1.ProviderOpenAI:     userCredentialSecretName(triggersv1alpha1.ProviderOpenAI),
		triggersv1alpha1.ProviderGemini:     userCredentialSecretName(triggersv1alpha1.ProviderOpenAI),
		triggersv1alpha1.ProviderOpenRouter: userCredentialSecretName(triggersv1alpha1.ProviderOpenRouter),
		triggersv1alpha1.ProviderGroq:       userCredentialSecretName(triggersv1alpha1.ProviderOpenAI),
		triggersv1alpha1.ProviderXAI:        userCredentialSecretName(triggersv1alpha1.ProviderXAI),
	}
	if len(got) != len(want) {
		t.Fatalf("ProviderKeys = %#v, want providers %#v", agent.Spec.Defaults.Secrets.ProviderKeys, want)
	}
	for provider, secretName := range want {
		if got[provider] != secretName {
			t.Fatalf("ProviderKeys[%s] = %q, want %q (all keys: %#v)", provider, got[provider], secretName, got)
		}
	}
}

func TestUpdateSlackAgentSyncsActiveSlackAgentRuns(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	namespace := seedSlackGitHubCredential(t, srv, ctx)
	seedSlackAPIKeyCredential(t, srv, ctx, triggersv1alpha1.ProviderOpenAI)
	seedSlackAPIKeyCredential(t, srv, ctx, triggersv1alpha1.ProviderOpenRouter)

	active := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "slack-run", Namespace: namespace},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:       platformv1alpha1.TriggerRef{Kind: slackTriggerKind, Name: "support"},
			Model:         "anthropic/old-model",
			AuthMode:      platformv1alpha1.AgentRunAuthModeAPIKey,
			OpenAIBaseURL: "https://old.example.test",
			Secrets: &platformv1alpha1.AgentRunSecrets{
				ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: triggersv1alpha1.ProviderAnthropic, SecretName: "old-anthropic"}},
			},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	terminal := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "done-run", Namespace: namespace},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:       platformv1alpha1.TriggerRef{Kind: slackTriggerKind, Name: "support"},
			Model:         "anthropic/done-model",
			AuthMode:      platformv1alpha1.AgentRunAuthModeAPIKey,
			OpenAIBaseURL: "https://done.example.test",
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded},
	}
	if err := srv.k8sClient.Create(ctx, active); err != nil {
		t.Fatalf("create active AgentRun: %v", err)
	}
	if err := srv.k8sClient.Create(ctx, terminal); err != nil {
		t.Fatalf("create terminal AgentRun: %v", err)
	}

	if _, err := srv.UpdateSlackAgent(ctx, &platform.UpdateSlackAgentRequest{
		Name:                "support",
		BotToken:            "xoxb-123",
		AppToken:            "xapp-123",
		Model:               "gpt-5",
		Provider:            "openrouter",
		AuthMode:            "api-key",
		UseSavedCredentials: true,
	}); err != nil {
		t.Fatalf("UpdateSlackAgent() error = %v", err)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "slack-run"}, updated); err != nil {
		t.Fatalf("Get(active AgentRun) error = %v", err)
	}
	if updated.Spec.Model != "openrouter/gpt-5" {
		t.Fatalf("active Spec.Model = %q, want openrouter/gpt-5", updated.Spec.Model)
	}
	if updated.Spec.OpenAIBaseURL != triggersv1alpha1.DefaultOpenRouterBaseURL {
		t.Fatalf("active Spec.OpenAIBaseURL = %q, want %q", updated.Spec.OpenAIBaseURL, triggersv1alpha1.DefaultOpenRouterBaseURL)
	}
	if updated.Spec.AuthMode != platformv1alpha1.AgentRunAuthModeAPIKey {
		t.Fatalf("active Spec.AuthMode = %q, want api-key", updated.Spec.AuthMode)
	}
	if updated.Spec.Secrets == nil {
		t.Fatal("active Spec.Secrets = nil")
	}
	if updated.Spec.Secrets.GitHubTokenSecret != userCredentialSecretName(credentialGitHub) {
		t.Fatalf("GitHubTokenSecret = %q, want saved GitHub credential", updated.Spec.Secrets.GitHubTokenSecret)
	}
	providerKeys := providerKeySecretNames(updated.Spec.Secrets.ProviderKeys)
	if providerKeys[triggersv1alpha1.ProviderOpenRouter] != userCredentialSecretName(triggersv1alpha1.ProviderOpenRouter) {
		t.Fatalf("openrouter provider key = %q, want saved OpenRouter credential (keys=%#v)", providerKeys[triggersv1alpha1.ProviderOpenRouter], providerKeys)
	}

	done := &platformv1alpha1.AgentRun{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "done-run"}, done); err != nil {
		t.Fatalf("Get(terminal AgentRun) error = %v", err)
	}
	if done.Spec.Model != "anthropic/done-model" || done.Spec.OpenAIBaseURL != "https://done.example.test" {
		t.Fatalf("terminal run changed unexpectedly: model=%q baseURL=%q", done.Spec.Model, done.Spec.OpenAIBaseURL)
	}
}

func providerKeySecretNames(keys []platformv1alpha1.ProviderKeyRef) map[string]string {
	out := map[string]string{}
	for _, key := range keys {
		out[strings.ToLower(strings.TrimSpace(key.Provider))] = key.SecretName
	}
	return out
}

func TestUpdateSlackAgentPreservesInlineKeyForSelectedCompatibleProvider(t *testing.T) {
	for _, provider := range []string{triggersv1alpha1.ProviderOpenRouter, triggersv1alpha1.ProviderXAI} {
		t.Run(provider, func(t *testing.T) {
			srv := slackAgentTestServer(t)
			ctx := slackActorContext()
			seedSlackGitHubCredential(t, srv, ctx)

			resp, err := srv.UpdateSlackAgent(ctx, &platform.UpdateSlackAgentRequest{
				Name:                "support",
				BotToken:            "xoxb-test",
				AppToken:            "xapp-test",
				Model:               provider + "/test-model",
				Provider:            provider,
				AuthMode:            "api-key",
				UseSavedCredentials: false,
				OpenaiApiKey:        "inline-test-key",
			})
			if err != nil {
				t.Fatalf("UpdateSlackAgent() error = %v", err)
			}

			secretName := userCredentialSecretName(provider)
			secret := &corev1.Secret{}
			if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: secretName}, secret); err != nil {
				t.Fatalf("Get(%s) error = %v", secretName, err)
			}
			if got := string(secret.Data[userCredAPIKeyKey]); got != "inline-test-key" {
				t.Fatalf("saved key = %q, want inline key", got)
			}

			agent := &triggersv1alpha1.SlackAgent{}
			if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: "support"}, agent); err != nil {
				t.Fatalf("Get(SlackAgent) error = %v", err)
			}
			keys := providerKeySecretNames(agent.Spec.Defaults.Secrets.ProviderKeys)
			if got := keys[provider]; got != secretName {
				t.Fatalf("provider key secret = %q, want %q (keys=%#v)", got, secretName, keys)
			}
		})
	}
}

func TestUpdateSlackAgentCreatesMultipleNamedAgents(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)

	for _, name := range []string{"support", "alerts"} {
		if _, err := srv.UpdateSlackAgent(ctx, &platform.UpdateSlackAgentRequest{
			Name:                name,
			BotToken:            "xoxb-" + name,
			AppToken:            "xapp-" + name,
			Model:               "claude-sonnet-4-6",
			Provider:            "anthropic",
			AuthMode:            "api-key",
			UseSavedCredentials: false,
			AnthropicApiKey:     "sk-ant-test",
		}); err != nil {
			t.Fatalf("UpdateSlackAgent(%q) error = %v", name, err)
		}
	}

	resp, err := srv.ListSlackAgents(ctx, &platform.ListSlackAgentsRequest{})
	if err != nil {
		t.Fatalf("ListSlackAgents() error = %v", err)
	}
	if got := len(resp.Agents); got != 2 {
		t.Fatalf("agents len = %d, want 2", got)
	}
	if resp.Agents[0].Name != "alerts" || resp.Agents[1].Name != "support" {
		t.Fatalf("agents = %q, %q; want alerts, support", resp.Agents[0].Name, resp.Agents[1].Name)
	}
	for _, agent := range resp.Agents {
		secret := &corev1.Secret{}
		if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: slackTokensSecretName(agent.Name)}, secret); err != nil {
			t.Fatalf("Get(tokens secret %q) error = %v", agent.Name, err)
		}
		if got := string(secret.Data[triggersv1alpha1.SlackBotTokenKey]); got != "xoxb-"+agent.Name {
			t.Fatalf("%s bot token = %q", agent.Name, got)
		}
	}
}

func TestUpdateSlackAgentDoesNotCreatePolicyResourcesByDefault(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)

	resp, err := srv.UpdateSlackAgent(ctx, &platform.UpdateSlackAgentRequest{
		Name:                "support",
		BotToken:            "xoxb-123",
		AppToken:            "xapp-123",
		Model:               "claude-sonnet-4-6",
		Provider:            "anthropic",
		AuthMode:            "api-key",
		UseSavedCredentials: false,
		AnthropicApiKey:     "sk-ant-test",
	})
	if err != nil {
		t.Fatalf("UpdateSlackAgent() error = %v", err)
	}
	if resp.RuntimeProfileRef != "" {
		t.Fatalf("RuntimeProfileRef = %q, want empty", resp.RuntimeProfileRef)
	}
	if resp.McpPolicyRef != "" {
		t.Fatalf("McpPolicyRef = %q, want empty", resp.McpPolicyRef)
	}

	agent := &triggersv1alpha1.SlackAgent{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: "support"}, agent); err != nil {
		t.Fatalf("Get(SlackAgent) error = %v", err)
	}
	if agent.Spec.Defaults.RuntimeProfileRef != nil {
		t.Fatalf("RuntimeProfileRef = %v, want nil", agent.Spec.Defaults.RuntimeProfileRef)
	}
	if agent.Spec.Defaults.MCPPolicyRef != nil {
		t.Fatalf("MCPPolicyRef = %v, want nil", agent.Spec.Defaults.MCPPolicyRef)
	}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: slackRuntimeProfileName("support")}, &platformv1alpha1.RuntimeProfile{}); !apierrors.IsNotFound(err) {
		t.Fatalf("default RuntimeProfile lookup err = %v, want not found", err)
	}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: slackMCPPolicyName("support")}, &platformv1alpha1.MCPPolicy{}); !apierrors.IsNotFound(err) {
		t.Fatalf("default MCPPolicy lookup err = %v, want not found", err)
	}
}

func TestUpdateSlackAgentRequiresSavedGitHubToken(t *testing.T) {
	srv := slackAgentTestServer(t)
	_, err := srv.UpdateSlackAgent(slackActorContext(), &platform.UpdateSlackAgentRequest{
		Name:                "support",
		BotToken:            "xoxb-123",
		AppToken:            "xapp-123",
		Model:               "claude-sonnet-4-6",
		Provider:            "anthropic",
		AuthMode:            "api-key",
		UseSavedCredentials: false,
		AnthropicApiKey:     "sk-ant-test",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("UpdateSlackAgent() code = %v, want FailedPrecondition (err=%v)", connect.CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "no GitHub token") || !strings.Contains(err.Error(), "Settings") {
		t.Fatalf("error = %v, want GitHub token Settings guidance", err)
	}
}

func TestUpdateSlackAgentRequiresModel(t *testing.T) {
	srv := slackAgentTestServer(t)
	_, err := srv.UpdateSlackAgent(slackActorContext(), &platform.UpdateSlackAgentRequest{
		Name: "support", BotToken: "xoxb-1", AppToken: "xapp-1", Provider: "anthropic",
	})
	if err == nil {
		t.Fatal("UpdateSlackAgent without model should error")
	}
}

func TestUpdateSlackAgentRequiresName(t *testing.T) {
	srv := slackAgentTestServer(t)
	_, err := srv.UpdateSlackAgent(slackActorContext(), &platform.UpdateSlackAgentRequest{
		BotToken: "xoxb-1", AppToken: "xapp-1", Model: "claude-sonnet-4-6", Provider: "anthropic",
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("UpdateSlackAgent() code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

func TestListSlackAgentsUnconfigured(t *testing.T) {
	srv := slackAgentTestServer(t)
	resp, err := srv.ListSlackAgents(slackActorContext(), &platform.ListSlackAgentsRequest{})
	if err != nil {
		t.Fatalf("ListSlackAgents() error = %v", err)
	}
	if len(resp.Agents) != 0 {
		t.Fatalf("agents len = %d, want 0 for a fresh user", len(resp.Agents))
	}
	if resp.Namespace == "" {
		t.Error("Namespace should be provisioned even when unconfigured")
	}
}

func TestDeleteSlackAgentRemovesResources(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)
	if _, err := srv.UpdateSlackAgent(ctx, &platform.UpdateSlackAgentRequest{
		Name:     "support",
		BotToken: "xoxb-1", AppToken: "xapp-1", Model: "claude-sonnet-4-6",
		Provider: "anthropic", AuthMode: "api-key", AnthropicApiKey: "sk-ant",
		RuntimeProfileRef: "slack-runtime-custom", ConfigureRuntimeProfile: true,
		McpPolicyRef: "slack-policy", ConfigureMcpPolicy: true,
	}); err != nil {
		t.Fatalf("seed UpdateSlackAgent error = %v", err)
	}
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}
	if _, err := srv.DeleteSlackAgent(ctx, &platform.DeleteSlackAgentRequest{Name: "support"}); err != nil {
		t.Fatalf("DeleteSlackAgent() error = %v", err)
	}
	resp, err := srv.ListSlackAgents(ctx, &platform.ListSlackAgentsRequest{})
	if err != nil {
		t.Fatalf("ListSlackAgents() error = %v", err)
	}
	if len(resp.Agents) != 0 {
		t.Fatalf("agents len after delete = %d, want 0", len(resp.Agents))
	}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: slackTokensSecretName("support")}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("tokens secret lookup err = %v, want not found", err)
	}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "slack-runtime-custom"}, &platformv1alpha1.RuntimeProfile{}); err != nil {
		t.Fatalf("RuntimeProfile should remain after deleting SlackAgent: %v", err)
	}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "slack-policy"}, &platformv1alpha1.MCPPolicy{}); err != nil {
		t.Fatalf("MCPPolicy should remain after deleting SlackAgent: %v", err)
	}
}

// slackDraftFakeStore satisfies store.StateStore (via mockStateStore) and adds
// the narrow ListSlackDrafts method the drafts inbox depends on, recording its
// arguments so tests can assert tenant scoping.
type slackDraftFakeStore struct {
	*mockStateStore
	drafts    []store.SlackDraft
	gotNS     string
	gotAgent  string
	gotStatus string
	gotLimit  int32
}

func (s *slackDraftFakeStore) ListSlackDrafts(_ context.Context, namespace, slackAgent, status string, limit int32) ([]store.SlackDraft, error) {
	s.gotNS, s.gotAgent, s.gotStatus, s.gotLimit = namespace, slackAgent, status, limit
	return s.drafts, nil
}

func TestUpdateSlackAgentPersistsSessionIdle(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)

	resp, err := srv.UpdateSlackAgent(ctx, &platform.UpdateSlackAgentRequest{
		Name:                "triage",
		BotToken:            "xoxb-1",
		AppToken:            "xapp-1",
		Model:               "claude-sonnet-4-6",
		Provider:            "anthropic",
		AuthMode:            "api-key",
		UseSavedCredentials: false,
		AnthropicApiKey:     "sk-ant-test",
		SessionIdleMinutes:  180,
	})
	if err != nil {
		t.Fatalf("UpdateSlackAgent() error = %v", err)
	}
	if resp.SessionIdleMinutes != 180 {
		t.Fatalf("response SessionIdleMinutes = %d, want 180", resp.SessionIdleMinutes)
	}

	agent := &triggersv1alpha1.SlackAgent{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: "triage"}, agent); err != nil {
		t.Fatalf("Get(SlackAgent) error = %v", err)
	}
	if agent.Spec.SessionIdleMinutes == nil || *agent.Spec.SessionIdleMinutes != 180 {
		t.Fatalf("CR SessionIdleMinutes = %v, want 180", agent.Spec.SessionIdleMinutes)
	}
}

func TestUpdateSlackAgentPersistsReasoningAndAdditionalRepos(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)

	repoURL := "https://github.com/acme/tools.git"
	base := func() *platform.UpdateSlackAgentRequest {
		return &platform.UpdateSlackAgentRequest{
			Name:                "triage",
			BotToken:            "xoxb-a",
			AppToken:            "xapp-1",
			Model:               "claude-sonnet-4-6",
			Provider:            "anthropic",
			AuthMode:            "api-key",
			UseSavedCredentials: false,
			AnthropicApiKey:     "test-anthropic-key",
		}
	}

	req := base()
	req.ReasoningLevel = "High" // mixed case: the server normalizes
	req.AdditionalRepoUrls = []string{repoURL, " " + repoURL + " ", ""}
	resp, err := srv.UpdateSlackAgent(ctx, req)
	if err != nil {
		t.Fatalf("UpdateSlackAgent() error = %v", err)
	}
	if resp.ReasoningLevel != "high" {
		t.Fatalf("response ReasoningLevel = %q, want %q", resp.ReasoningLevel, "high")
	}
	if len(resp.AdditionalRepoUrls) != 1 || resp.AdditionalRepoUrls[0] != repoURL {
		t.Fatalf("response AdditionalRepoUrls = %v, want [%s]", resp.AdditionalRepoUrls, repoURL)
	}

	agent := &triggersv1alpha1.SlackAgent{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: "triage"}, agent); err != nil {
		t.Fatalf("Get(SlackAgent) error = %v", err)
	}
	if got := string(agent.Spec.Defaults.ReasoningLevel); got != "high" {
		t.Fatalf("CR Defaults.ReasoningLevel = %q, want %q", got, "high")
	}
	if len(agent.Spec.Defaults.AdditionalRepos) != 1 || agent.Spec.Defaults.AdditionalRepos[0] != repoURL {
		t.Fatalf("CR Defaults.AdditionalRepos = %v, want [%s]", agent.Spec.Defaults.AdditionalRepos, repoURL)
	}

	bad := base()
	bad.ReasoningLevel = "extreme"
	if _, err := srv.UpdateSlackAgent(ctx, bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid reasoning level: got error %v, want CodeInvalidArgument", err)
	}
}

func TestListSlackDraftsScopedToNamespace(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	decided := time.Unix(1_700_000_100, 0)
	fakeStore := &slackDraftFakeStore{
		mockStateStore: newMockStateStore(),
		drafts: []store.SlackDraft{
			{ID: uuid.New(), SlackAgent: "triage", ChannelID: "D1", TargetUser: "U9", DraftText: "hi", Status: "pending", CreatedAt: time.Unix(1_700_000_000, 0)},
			{ID: uuid.New(), SlackAgent: "triage", ChannelID: "D2", TargetUser: "U8", DraftText: "sent one", Status: "sent", CreatedAt: time.Unix(1_700_000_050, 0), DecidedAt: &decided},
		},
	}
	srv.stateStore = fakeStore

	resp, err := srv.ListSlackDrafts(ctx, &platform.ListSlackDraftsRequest{Name: "Triage", Status: "pending", Limit: 10})
	if err != nil {
		t.Fatalf("ListSlackDrafts() error = %v", err)
	}
	if len(resp.Drafts) != 2 {
		t.Fatalf("drafts len = %d, want 2", len(resp.Drafts))
	}
	if fakeStore.gotNS != resp.Namespace || fakeStore.gotNS == "" {
		t.Fatalf("store namespace = %q, response namespace = %q", fakeStore.gotNS, resp.Namespace)
	}
	if fakeStore.gotAgent != "triage" {
		t.Fatalf("store agent = %q, want normalized 'triage'", fakeStore.gotAgent)
	}
	if fakeStore.gotStatus != "pending" || fakeStore.gotLimit != 10 {
		t.Fatalf("store status/limit = %q/%d", fakeStore.gotStatus, fakeStore.gotLimit)
	}
	if resp.Drafts[1].DecidedAtUnix != decided.Unix() {
		t.Fatalf("DecidedAtUnix = %d, want %d", resp.Drafts[1].DecidedAtUnix, decided.Unix())
	}
}

func TestListSlackDraftsWithoutStoreIsEmpty(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	resp, err := srv.ListSlackDrafts(ctx, &platform.ListSlackDraftsRequest{Name: "triage"})
	if err != nil {
		t.Fatalf("ListSlackDrafts() error = %v", err)
	}
	if len(resp.Drafts) != 0 {
		t.Fatalf("drafts len = %d, want 0 without a durable store", len(resp.Drafts))
	}
}

func TestUpdateSlackAgentPersistsChannelReplyMode(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)

	base := func() *platform.UpdateSlackAgentRequest {
		return &platform.UpdateSlackAgentRequest{
			Name:            "replies",
			BotToken:        "xoxb-1",
			AppToken:        "xapp-1",
			Model:           "claude-sonnet-4-6",
			Provider:        "anthropic",
			AuthMode:        "api-key",
			AnthropicApiKey: "sk-ant-test",
		}
	}

	// Omitted mode defaults to require-approval (the safe default).
	resp, err := srv.UpdateSlackAgent(ctx, base())
	if err != nil {
		t.Fatalf("UpdateSlackAgent() error = %v", err)
	}
	if resp.ChannelReplyMode != string(triggersv1alpha1.SlackChannelReplyRequireApproval) {
		t.Fatalf("default ChannelReplyMode = %q, want require-approval", resp.ChannelReplyMode)
	}

	req := base()
	req.ChannelReplyMode = "auto"
	resp, err = srv.UpdateSlackAgent(ctx, req)
	if err != nil {
		t.Fatalf("UpdateSlackAgent(auto) error = %v", err)
	}
	if resp.ChannelReplyMode != string(triggersv1alpha1.SlackChannelReplyAuto) {
		t.Fatalf("ChannelReplyMode = %q, want auto", resp.ChannelReplyMode)
	}
	agent := &triggersv1alpha1.SlackAgent{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: "replies"}, agent); err != nil {
		t.Fatalf("Get(SlackAgent) error = %v", err)
	}
	if agent.Spec.ChannelReplyMode != triggersv1alpha1.SlackChannelReplyAuto {
		t.Fatalf("CR ChannelReplyMode = %q, want auto", agent.Spec.ChannelReplyMode)
	}

	// Unknown values fall back to require-approval rather than persisting junk.
	req = base()
	req.ChannelReplyMode = "yolo"
	resp, err = srv.UpdateSlackAgent(ctx, req)
	if err != nil {
		t.Fatalf("UpdateSlackAgent(yolo) error = %v", err)
	}
	if resp.ChannelReplyMode != string(triggersv1alpha1.SlackChannelReplyRequireApproval) {
		t.Fatalf("ChannelReplyMode = %q, want require-approval fallback", resp.ChannelReplyMode)
	}
}

func TestUpdateSlackAgentPersistsAppHomeCopy(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)

	base := func() *platform.UpdateSlackAgentRequest {
		return &platform.UpdateSlackAgentRequest{
			Name:            "apphome",
			BotToken:        "fake-bot",
			AppToken:        "fake-app",
			Model:           "claude-sonnet-4-6",
			Provider:        "anthropic",
			AuthMode:        "api-key",
			AnthropicApiKey: "fake-key",
		}
	}

	req := base()
	req.AppHomeHeader = "  Ops Butler  "
	req.AppHomeText = "Ping *#ops* for urgent things."
	resp, err := srv.UpdateSlackAgent(ctx, req)
	if err != nil {
		t.Fatalf("UpdateSlackAgent: %v", err)
	}
	if resp.AppHomeHeader != "Ops Butler" || resp.AppHomeText != "Ping *#ops* for urgent things." {
		t.Fatalf("echoed app home copy = %q / %q", resp.AppHomeHeader, resp.AppHomeText)
	}

	agent := &triggersv1alpha1.SlackAgent{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: "apphome"}, agent); err != nil {
		t.Fatalf("reading SlackAgent: %v", err)
	}
	if agent.Spec.AppHome == nil || agent.Spec.AppHome.Header != "Ops Butler" || agent.Spec.AppHome.Text != "Ping *#ops* for urgent things." {
		t.Fatalf("persisted app home = %+v", agent.Spec.AppHome)
	}

	// Clearing both fields removes the override entirely (nil spec).
	resp, err = srv.UpdateSlackAgent(ctx, base())
	if err != nil {
		t.Fatalf("UpdateSlackAgent (clear): %v", err)
	}
	if resp.AppHomeHeader != "" || resp.AppHomeText != "" {
		t.Fatalf("cleared app home copy = %q / %q", resp.AppHomeHeader, resp.AppHomeText)
	}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: "apphome"}, agent); err != nil {
		t.Fatalf("re-reading SlackAgent: %v", err)
	}
	if agent.Spec.AppHome != nil {
		t.Fatalf("app home spec should be nil after clearing, got %+v", agent.Spec.AppHome)
	}
}

func TestUpdateSlackAgentRejectsOversizeAppHomeCopy(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)

	base := func() *platform.UpdateSlackAgentRequest {
		return &platform.UpdateSlackAgentRequest{
			Name:            "apphome",
			BotToken:        "fake-bot",
			AppToken:        "fake-app",
			Model:           "claude-sonnet-4-6",
			Provider:        "anthropic",
			AuthMode:        "api-key",
			AnthropicApiKey: "fake-key",
		}
	}

	req := base()
	req.AppHomeHeader = strings.Repeat("h", 151)
	if _, err := srv.UpdateSlackAgent(ctx, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversize header: err = %v, want invalid argument", err)
	}

	req = base()
	req.AppHomeText = strings.Repeat("x", 1001)
	if _, err := srv.UpdateSlackAgent(ctx, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversize text: err = %v, want invalid argument", err)
	}
}
