package dashboard

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/usercreds"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func TestOAuthMaterialProviderFromJSON(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"copilot typed", `{"oauth_token":"gho_x","token":"tid=1","type":"copilot"}`, "copilot"},
		{"copilot untyped flat", `{"oauth_token":"gho_x","token":"tid=1"}`, "copilot"},
		{"copilot hosts shape", `{"github.com":{"oauth_token":"gho_x"}}`, "copilot"},
		{"anthropic typed", `{"access_token":"at","refresh_token":"rt","type":"claude"}`, "anthropic"},
		{"anthropic flat untyped", `{"access_token":"at","refresh_token":"rt"}`, "anthropic"},
		{"claude credentials shape", `{"claudeAiOauth":{"accessToken":"at","refreshToken":"rt"}}`, "anthropic"},
		{"openai codex shape", `{"tokens":{"id_token":"idt","access_token":"at"},"last_refresh":"2026-01-01T00:00:00Z"}`, "openai"},
		{"ambiguous", `{"access_token":"at"}`, ""},
		{"garbage", `not-json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := oauthMaterialProviderFromJSON([]byte(tt.json)); got != tt.want {
				t.Fatalf("oauthMaterialProviderFromJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOAuthMaterialProviderPrefersLabel(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "usercred-copilot",
			Namespace: "user-ns",
			Labels:    map[string]string{usercreds.LabelCredentialProvider: "copilot"},
		},
		// Content alone would be ambiguous; the label decides.
		Data: map[string][]byte{"auth.json": []byte(`{"access_token":"at"}`)},
	}
	if got := oauthMaterialProvider(secret); got != "copilot" {
		t.Fatalf("oauthMaterialProvider() = %q, want copilot", got)
	}
}

func copilotMaterialSecret(namespace, name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string][]byte{"auth.json": []byte(`{"oauth_token":"gho_x","token":"tid=1","type":"copilot"}`)},
	}
}

func anthropicMaterialSecret(namespace, name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string][]byte{"auth.json": []byte(`{"access_token":"at","refresh_token":"rt","type":"claude"}`)},
	}
}

func TestValidateOAuthSecretProvider(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		copilotMaterialSecret("user-ns", "usercred-copilot"),
		&corev1.Secret{ // undetermined material
			ObjectMeta: metav1.ObjectMeta{Name: "custom-oauth", Namespace: "user-ns"},
			Data:       map[string][]byte{"auth.json": []byte(`{"access_token":"at"}`)},
		},
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ctx := context.Background()

	if err := srv.validateOAuthSecretProvider(ctx, "user-ns", "usercred-copilot", "copilot"); err != nil {
		t.Fatalf("matching provider: unexpected error %v", err)
	}
	if err := srv.validateOAuthSecretProvider(ctx, "user-ns", "missing-secret", "anthropic"); err != nil {
		t.Fatalf("missing secret should be tolerated, got %v", err)
	}
	if err := srv.validateOAuthSecretProvider(ctx, "user-ns", "custom-oauth", "anthropic"); err != nil {
		t.Fatalf("undetermined material should be tolerated, got %v", err)
	}
	err := srv.validateOAuthSecretProvider(ctx, "user-ns", "usercred-copilot", "anthropic")
	if err == nil {
		t.Fatal("mismatched material: expected error, got nil")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("mismatch error code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "copilot OAuth material") || !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("mismatch error not actionable: %v", err)
	}
}

func TestUpdateProjectRejectsMismatchedOAuthSecret(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Provider: triggersv1alpha1.ProviderCopilot,
				AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets:  triggersv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "usercred-copilot"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		existing, copilotMaterialSecret(ns, "usercred-copilot"),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	// Switching the provider to anthropic while the form still carries the
	// Copilot OAuth secret must fail with an actionable error instead of
	// persisting wiring that crashes agent pods at startup.
	_, err := srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:         ns,
		Name:              "payments",
		DisplayName:       "Payments",
		Model:             "claude-opus-4-6",
		Provider:          "anthropic",
		AuthMode:          "oauth",
		OpenaiOauthSecret: "usercred-copilot",
	})
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("error code = %v, want InvalidArgument (%v)", connect.CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "copilot OAuth material") {
		t.Fatalf("error should identify the material provider: %v", err)
	}
}

func TestUpdateProjectAcceptsMatchingOAuthSecret(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Provider: triggersv1alpha1.ProviderCopilot,
				AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets:  triggersv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "usercred-copilot"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		existing,
		copilotMaterialSecret(ns, "usercred-copilot"),
		anthropicMaterialSecret(ns, "usercred-anthropic"),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	// Switching provider with the matching provider's secret is accepted.
	resp, err := srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:         ns,
		Name:              "payments",
		DisplayName:       "Payments",
		Model:             "claude-opus-4-6",
		Provider:          "anthropic",
		AuthMode:          "oauth",
		OpenaiOauthSecret: "usercred-anthropic",
	})
	if err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	if resp.Provider != "anthropic" || resp.OpenaiOauthSecret != "usercred-anthropic" {
		t.Fatalf("unexpected project wiring: provider=%q oauthSecret=%q", resp.Provider, resp.OpenaiOauthSecret)
	}
}

func TestUpdateProjectRepointsCarriedOverSavedCredSecret(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Provider: triggersv1alpha1.ProviderCopilot,
				AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets:  triggersv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "usercred-copilot"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		existing,
		copilotMaterialSecret(ns, "usercred-copilot"),
		anthropicMaterialSecret(ns, "usercred-anthropic"),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	// Switching the provider to anthropic while the form still carries the
	// Copilot saved-credential secret is provably carry-over: it heals to the
	// caller's saved anthropic credentials instead of rejecting.
	resp, err := srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:         ns,
		Name:              "payments",
		DisplayName:       "Payments",
		Model:             "claude-opus-4-6",
		Provider:          "anthropic",
		AuthMode:          "oauth",
		OpenaiOauthSecret: "usercred-copilot",
	})
	if err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	if resp.Provider != "anthropic" || resp.OpenaiOauthSecret != "usercred-anthropic" {
		t.Fatalf("unexpected project wiring: provider=%q oauthSecret=%q, want anthropic/usercred-anthropic", resp.Provider, resp.OpenaiOauthSecret)
	}
	if resp.AuthMode != "oauth" {
		t.Fatalf("AuthMode = %q, want oauth", resp.AuthMode)
	}
}

func TestUpdateProjectRepointFlipsToAPIKeyWhenOnlyKeySaved(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Provider: triggersv1alpha1.ProviderCopilot,
				AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets:  triggersv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "usercred-copilot"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		existing,
		copilotMaterialSecret(ns, "usercred-copilot"),
		&corev1.Secret{ // saved anthropic API key, no OAuth material
			ObjectMeta: metav1.ObjectMeta{Name: "usercred-anthropic", Namespace: ns},
			Data:       map[string][]byte{"api-key": []byte("sk-ant-test")},
		},
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:         ns,
		Name:              "payments",
		DisplayName:       "Payments",
		Model:             "claude-opus-4-6",
		Provider:          "anthropic",
		AuthMode:          "oauth",
		OpenaiOauthSecret: "usercred-copilot",
	})
	if err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	if resp.AuthMode != "api-key" {
		t.Fatalf("AuthMode = %q, want api-key (only an API key is saved)", resp.AuthMode)
	}
	if resp.OpenaiOauthSecret != "" {
		t.Fatalf("OpenaiOauthSecret = %q, want cleared", resp.OpenaiOauthSecret)
	}
	var keyed bool
	for _, key := range resp.ProviderKeys {
		if key.Provider == "anthropic" && key.SecretName == "usercred-anthropic" {
			keyed = true
		}
	}
	if !keyed {
		t.Fatalf("ProviderKeys = %+v, want anthropic key ref to usercred-anthropic", resp.ProviderKeys)
	}
}

func TestUpdateProjectRejectsMismatchedCustomSecretEvenWithSavedCreds(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Provider: triggersv1alpha1.ProviderCopilot,
				AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets:  triggersv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "team-copilot-oauth"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		existing,
		copilotMaterialSecret(ns, "team-copilot-oauth"),
		anthropicMaterialSecret(ns, "usercred-anthropic"),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	// A custom-named secret was an explicit choice: never silently repoint it,
	// even when saved credentials for the new provider exist.
	_, err := srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:         ns,
		Name:              "payments",
		DisplayName:       "Payments",
		Model:             "claude-opus-4-6",
		Provider:          "anthropic",
		AuthMode:          "oauth",
		OpenaiOauthSecret: "team-copilot-oauth",
	})
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("error code = %v, want InvalidArgument (%v)", connect.CodeOf(err), err)
	}
}

func TestUpdateProjectKeepsGithubTokenWithSavedCredentials(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Provider: triggersv1alpha1.ProviderCopilot,
				AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					OpenAIOAuthSecret: "usercred-copilot",
					GithubToken:       "team-github-token",
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		existing,
		copilotMaterialSecret(ns, "usercred-copilot"),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	// The caller has no saved GitHub credential: "use my saved provider
	// credentials" must not silently drop the project's GitHub token ref.
	resp, err := srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:           ns,
		Name:                "payments",
		DisplayName:         "Payments",
		Model:               "gpt-5",
		Provider:            "copilot",
		AuthMode:            "oauth",
		UseSavedCredentials: true,
	})
	if err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	if resp.GithubTokenSecret != "team-github-token" {
		t.Fatalf("GithubTokenSecret = %q, want team-github-token preserved", resp.GithubTokenSecret)
	}
}

// anthropicDefaultsLinearProjectWithCopilotSecret models the drifted state the
// bug report hit: the source's provider was switched to anthropic while its
// stored OAuth secret still holds Copilot material.
func anthropicDefaultsLinearProjectWithCopilotSecret() *triggersv1alpha1.LinearProject {
	return &triggersv1alpha1.LinearProject{
		ObjectMeta: metav1.ObjectMeta{Name: "linear-proj", Namespace: "default"},
		Spec: triggersv1alpha1.LinearProjectSpec{
			LinearAPIKeySecret: "linear-secret",
			ProjectID:          "proj",
			TeamID:             "team",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:    "https://github.com/acme/payments",
				BaseBranch: "main",
				Image:      "agent:latest",
				Model:      "claude-opus-4-6",
				Provider:   triggersv1alpha1.ProviderAnthropic,
				AuthMode:   platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					OpenAIOAuthSecret: "usercred-copilot",
					GithubToken:       "github-token-secret",
				},
			},
		},
	}
}

func TestCreateAgentRunRepointsMismatchedInheritedOAuthSecret(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(
			anthropicDefaultsLinearProjectWithCopilotSecret(),
			copilotMaterialSecret("default", "usercred-copilot"),
			anthropicMaterialSecret("default", "usercred-anthropic"),
		).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "LinearProject", Name: "linear-proj"},
		UserRequest: "do the thing",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}

	run := fetchCreatedRun(t, c, resp.Name)
	if run.Spec.AuthMode != platformv1alpha1.AgentRunAuthModeOAuth {
		t.Fatalf("AuthMode = %q, want oauth", run.Spec.AuthMode)
	}
	if run.Spec.Secrets == nil || run.Spec.Secrets.OpenAIOAuthSecret != "usercred-anthropic" {
		t.Fatalf("Spec.Secrets.OpenAIOAuthSecret = %+v, want repointed to usercred-anthropic", run.Spec.Secrets)
	}
	if run.Spec.Model != "anthropic/claude-opus-4-6" {
		t.Fatalf("Spec.Model = %q, want anthropic/claude-opus-4-6", run.Spec.Model)
	}
}

func TestCreateAgentRunMismatchedInheritedOAuthSecretWithoutSavedCredsFails(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(
			anthropicDefaultsLinearProjectWithCopilotSecret(),
			copilotMaterialSecret("default", "usercred-copilot"),
		).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "LinearProject", Name: "linear-proj"},
		UserRequest: "do the thing",
	})
	if err == nil {
		t.Fatal("expected error creating run with mismatched OAuth material and no saved credentials")
	}
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("error code = %v, want FailedPrecondition (%v)", connect.CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "copilot OAuth material") || !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("error not actionable: %v", err)
	}
}

func TestCreateAgentRunKeepsMatchingInheritedOAuthSecret(t *testing.T) {
	scheme := testProjectScheme(t)
	project := anthropicDefaultsLinearProjectWithCopilotSecret()
	project.Spec.Defaults.Secrets.OpenAIOAuthSecret = "usercred-anthropic"
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project, anthropicMaterialSecret("default", "usercred-anthropic")).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "LinearProject", Name: "linear-proj"},
		UserRequest: "do the thing",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}
	run := fetchCreatedRun(t, c, resp.Name)
	if run.Spec.Secrets == nil || run.Spec.Secrets.OpenAIOAuthSecret != "usercred-anthropic" {
		t.Fatalf("Spec.Secrets.OpenAIOAuthSecret = %+v, want usercred-anthropic untouched", run.Spec.Secrets)
	}
}
