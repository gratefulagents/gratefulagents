package dashboard

import (
	"testing"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// baseSlackAgentGitHubRequest is a minimal valid create/update request for the
// per-agent GitHub token tests.
func baseSlackAgentGitHubRequest(name string) *platform.UpdateSlackAgentRequest {
	return &platform.UpdateSlackAgentRequest{
		Name:                name,
		BotToken:            "xb",
		AppToken:            "xa",
		SlackUserId:         "U1",
		Model:               "claude-sonnet-4-6",
		Provider:            "anthropic",
		AuthMode:            "api-key",
		UseSavedCredentials: false,
		AnthropicApiKey:     "sk-test",
	}
}

func TestUpdateSlackAgentAgentGitHubTokenSatisfiesRequirement(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()

	// Deliberately NO saved GitHub credential: the agent-specific token alone
	// must satisfy the GitHub token requirement.
	req := baseSlackAgentGitHubRequest("bot")
	req.GithubToken = "agent-gh-tok"
	resp, err := srv.UpdateSlackAgent(ctx, req)
	if err != nil {
		t.Fatalf("UpdateSlackAgent() error = %v", err)
	}
	if !resp.GithubTokenPresent {
		t.Fatal("GithubTokenPresent = false, want true")
	}
	namespace := resp.Namespace

	secret := &corev1.Secret{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: slackGitHubSecretName("bot")}, secret); err != nil {
		t.Fatalf("Get(agent GitHub secret) error = %v", err)
	}
	if got := string(secret.Data[userCredGithubTokenKey]); got != "agent-gh-tok" {
		t.Fatalf("token = %q, want agent-gh-tok", got)
	}
	if secret.Labels[slackGitHubTokenLabel] != "true" || secret.Labels[slackTokensAgentLabel] != "bot" {
		t.Fatalf("labels = %v, want slack-github-token + agent labels", secret.Labels)
	}

	agent := &triggersv1alpha1.SlackAgent{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "bot"}, agent); err != nil {
		t.Fatalf("Get(SlackAgent) error = %v", err)
	}
	if got := agent.Spec.Defaults.Secrets.GithubToken; got != slackGitHubSecretName("bot") {
		t.Fatalf("Defaults.Secrets.GithubToken = %q, want %q", got, slackGitHubSecretName("bot"))
	}
}

func TestUpdateSlackAgentAgentGitHubTokenBeatsSavedToken(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)

	req := baseSlackAgentGitHubRequest("bot")
	req.GithubToken = "agent-gh-tok"
	resp, err := srv.UpdateSlackAgent(ctx, req)
	if err != nil {
		t.Fatalf("UpdateSlackAgent() error = %v", err)
	}
	if !resp.GithubTokenPresent {
		t.Fatal("GithubTokenPresent = false, want true")
	}

	agent := &triggersv1alpha1.SlackAgent{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: resp.Namespace, Name: "bot"}, agent); err != nil {
		t.Fatalf("Get(SlackAgent) error = %v", err)
	}
	if got := agent.Spec.Defaults.Secrets.GithubToken; got != slackGitHubSecretName("bot") {
		t.Fatalf("Defaults.Secrets.GithubToken = %q, want the per-agent secret to beat the saved one", got)
	}
}

func TestUpdateSlackAgentClearGitHubTokenFallsBackToSaved(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)

	req := baseSlackAgentGitHubRequest("bot")
	req.GithubToken = "agent-gh-tok"
	if _, err := srv.UpdateSlackAgent(ctx, req); err != nil {
		t.Fatalf("UpdateSlackAgent(create) error = %v", err)
	}

	upd := baseSlackAgentGitHubRequest("bot")
	upd.Clear = []string{"github-token"}
	resp, err := srv.UpdateSlackAgent(ctx, upd)
	if err != nil {
		t.Fatalf("UpdateSlackAgent(clear) error = %v", err)
	}
	if resp.GithubTokenPresent {
		t.Fatal("GithubTokenPresent = true after clear, want false")
	}

	namespace := resp.Namespace
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: slackGitHubSecretName("bot")}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("agent GitHub secret after clear: err = %v, want NotFound", err)
	}
	agent := &triggersv1alpha1.SlackAgent{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "bot"}, agent); err != nil {
		t.Fatalf("Get(SlackAgent) error = %v", err)
	}
	if got := agent.Spec.Defaults.Secrets.GithubToken; got != userCredentialSecretName(credentialGitHub) {
		t.Fatalf("Defaults.Secrets.GithubToken = %q, want fallback to saved credential", got)
	}
}

func TestUpdateSlackAgentClearGitHubTokenWithoutSavedFails(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()

	req := baseSlackAgentGitHubRequest("bot")
	req.GithubToken = "agent-gh-tok"
	if _, err := srv.UpdateSlackAgent(ctx, req); err != nil {
		t.Fatalf("UpdateSlackAgent(create) error = %v", err)
	}

	upd := baseSlackAgentGitHubRequest("bot")
	upd.Clear = []string{"github-token"}
	_, err := srv.UpdateSlackAgent(ctx, upd)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("UpdateSlackAgent(clear without saved) code = %v, want FailedPrecondition (err=%v)", connect.CodeOf(err), err)
	}
}

func TestUpdateSlackAgentGitHubTokenSyncsActiveRuns(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)

	if _, err := srv.UpdateSlackAgent(ctx, baseSlackAgentGitHubRequest("bot")); err != nil {
		t.Fatalf("UpdateSlackAgent(create) error = %v", err)
	}
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}
	active := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "conv-1", Namespace: namespace},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:  platformv1alpha1.TriggerRef{Kind: slackTriggerKind, Name: "bot"},
			Model:    "claude-sonnet-4-6",
			AuthMode: platformv1alpha1.AgentRunAuthModeAPIKey,
			Secrets: &platformv1alpha1.AgentRunSecrets{
				GitHubTokenSecret: userCredentialSecretName(credentialGitHub),
			},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	if err := srv.k8sClient.Create(ctx, active); err != nil {
		t.Fatalf("Create(AgentRun) error = %v", err)
	}

	upd := baseSlackAgentGitHubRequest("bot")
	upd.GithubToken = "agent-gh-tok"
	if _, err := srv.UpdateSlackAgent(ctx, upd); err != nil {
		t.Fatalf("UpdateSlackAgent(set token) error = %v", err)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "conv-1"}, updated); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if updated.Spec.Secrets == nil {
		t.Fatal("AgentRun secrets nil after sync")
	}
	if got := updated.Spec.Secrets.GitHubTokenSecret; got != slackGitHubSecretName("bot") {
		t.Fatalf("active run GitHubTokenSecret = %q, want per-agent secret", got)
	}
}

func TestDeleteSlackAgentRemovesGitHubTokenSecret(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()

	req := baseSlackAgentGitHubRequest("bot")
	req.GithubToken = "agent-gh-tok"
	resp, err := srv.UpdateSlackAgent(ctx, req)
	if err != nil {
		t.Fatalf("UpdateSlackAgent() error = %v", err)
	}
	namespace := resp.Namespace

	if _, err := srv.DeleteSlackAgent(ctx, &platform.DeleteSlackAgentRequest{Name: "bot"}); err != nil {
		t.Fatalf("DeleteSlackAgent() error = %v", err)
	}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: slackGitHubSecretName("bot")}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("agent GitHub secret after delete: err = %v, want NotFound", err)
	}
}
