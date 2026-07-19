package dashboard

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	oauth "github.com/gratefulagents/sdk/pkg/agentsdk/providers/oauth"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNormalizeOpenAIModelsBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		raw      string
		want     string
	}{
		{
			name:     "openai default",
			provider: triggersv1alpha1.ProviderOpenAI,
			raw:      "",
			want:     triggersv1alpha1.DefaultOpenAIBaseURL,
		},
		{
			name:     "gemini default",
			provider: triggersv1alpha1.ProviderGemini,
			raw:      "",
			want:     triggersv1alpha1.DefaultGeminiBaseURL,
		},
		{
			name:     "openrouter default strips chat completions path",
			provider: triggersv1alpha1.ProviderOpenRouter,
			raw:      "",
			want:     "https://openrouter.ai/api/v1",
		},
		{
			name:     "strips chat completions path",
			provider: triggersv1alpha1.ProviderOpenAI,
			raw:      "https://api.openai.com/v1/chat/completions",
			want:     "https://api.openai.com/v1",
		},
		{
			name:     "adds v1 when missing",
			provider: triggersv1alpha1.ProviderOpenAI,
			raw:      "https://example.com/openai",
			want:     "https://example.com/openai/v1",
		},
		{
			name:     "invalid override falls back",
			provider: triggersv1alpha1.ProviderOpenAI,
			raw:      "://bad-url",
			want:     triggersv1alpha1.DefaultOpenAIBaseURL,
		},
		{
			name:     "chatgpt backend preserved",
			provider: triggersv1alpha1.ProviderOpenAI,
			raw:      "https://chatgpt.com/backend-api/codex",
			want:     "https://chatgpt.com/backend-api/codex",
		},
		{
			name:     "chatgpt backend strips responses",
			provider: triggersv1alpha1.ProviderOpenAI,
			raw:      "https://chatgpt.com/backend-api/codex/responses",
			want:     "https://chatgpt.com/backend-api/codex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeOpenAIModelsBaseURL(tt.provider, tt.raw)
			if got != tt.want {
				t.Fatalf("normalizeOpenAIModelsBaseURL(%q, %q) = %q, want %q", tt.provider, tt.raw, got, tt.want)
			}
		})
	}
}

func TestUniqueSorted(t *testing.T) {
	t.Parallel()

	got := uniqueSorted([]string{"b", "a", "b", "c", "a"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueSorted() = %v, want %v", got, want)
	}
}

func TestCopilotModelHeadersNormalizeAuthScheme(t *testing.T) {
	t.Parallel()

	headers := copilotModelHeaders("Bearer token copilot-api-token")

	if got := headers["Authorization"]; got != "Bearer copilot-api-token" {
		t.Fatalf("Authorization = %q, want bearer with normalized token", got)
	}
	if got := headers["Editor-Version"]; got != copilotEditorVersion {
		t.Fatalf("Editor-Version = %q, want %q", got, copilotEditorVersion)
	}
	if got := headers["Editor-Plugin-Version"]; got != "copilot-chat/"+copilotChatVersion {
		t.Fatalf("Editor-Plugin-Version = %q, want SDK-aligned Copilot Chat version", got)
	}
	if got := headers["Openai-Intent"]; got != "conversation-edits" {
		t.Fatalf("Openai-Intent = %q, want conversation-edits", got)
	}
	if got := headers["X-GitHub-Api-Version"]; got != copilotGitHubAPIVersion {
		t.Fatalf("X-GitHub-Api-Version = %q, want %q", got, copilotGitHubAPIVersion)
	}
	if got := headers["X-Initiator"]; got != "user" {
		t.Fatalf("X-Initiator = %q, want user", got)
	}
}

func TestCopilotModelAuthSessionRefreshesOAuthSecret(t *testing.T) {
	var gotAuth string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"token":"copilot-api-token","expires_at":4070908800}`)
	}))
	defer tokenServer.Close()

	oldRefreshConfig := copilotModelOAuthRefreshConfig
	copilotModelOAuthRefreshConfig = func() oauth.RefreshConfig {
		return oauth.RefreshConfig{
			HTTPClient:                 tokenServer.Client(),
			CopilotTokenURL:            tokenServer.URL,
			CopilotEditorVersion:       "test-editor",
			CopilotEditorPluginVersion: "test-plugin",
			CopilotUserAgent:           "test-agent",
			CopilotAuthorizationScheme: "token",
			Now: func() time.Time {
				return time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
			},
		}
	}
	defer func() { copilotModelOAuthRefreshConfig = oldRefreshConfig }()

	scheme := testProjectScheme(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "test-ns",
			Name:      "copilot-oauth",
		},
		Data: map[string][]byte{
			oauth.AuthJSONKey: []byte(`{"github.com:Iv1.b507a08c87ecfe98":{"oauth_token":"github-oauth","user":"octocat"}}`),
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	session, err := srv.copilotModelAuthSession(context.Background(), modelQuery{
		namespace:       "test-ns",
		provider:        triggersv1alpha1.ProviderCopilot,
		authMode:        platformv1alpha1.AgentRunAuthModeOAuth,
		oauthSecretName: "copilot-oauth",
	})
	if err != nil {
		t.Fatalf("copilotModelAuthSession() error = %v", err)
	}

	headers, err := session.RequestHeaders(context.Background())
	if err != nil {
		t.Fatalf("RequestHeaders() error = %v", err)
	}
	if got := headers["Authorization"]; got != "Bearer copilot-api-token" {
		t.Fatalf("Authorization = %q, want refreshed Copilot API token", got)
	}
	if gotAuth != "token github-oauth" {
		t.Fatalf("token exchange Authorization = %q, want GitHub OAuth token exchange", gotAuth)
	}

	var updated corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "test-ns", Name: "copilot-oauth"}, &updated); err != nil {
		t.Fatalf("get updated secret: %v", err)
	}
	token, err := oauth.CopilotAPIToken(updated.Data[oauth.AuthJSONKey])
	if err != nil {
		t.Fatalf("updated Copilot auth JSON missing API token: %v", err)
	}
	if token != "copilot-api-token" {
		t.Fatalf("updated token = %q, want copilot-api-token", token)
	}
}

func TestListAvailableModelsUsesRequestProviderKeyRefs(t *testing.T) {
	var gotAuth string
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("model request path = %q, want /v1/models", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"gpt-b"},{"id":"gpt-a"}]}`)
	}))
	defer modelServer.Close()

	scheme := testProjectScheme(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "openai-key", Namespace: "test-ns"},
		Data:       map[string][]byte{"custom-key": []byte("sk-request")},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.ListAvailableModels(context.Background(), &platform.ListAvailableModelsRequest{
		Namespace:     "test-ns",
		Provider:      "openai",
		AuthMode:      "api-key",
		OpenaiBaseUrl: modelServer.URL + "/v1",
		ProviderKeys: []*platform.ProviderKeyRef{{
			Provider:   "openai",
			SecretName: "openai-key",
			SecretKey:  "custom-key",
		}},
	})
	if err != nil {
		t.Fatalf("ListAvailableModels() error = %v", err)
	}
	if gotAuth != "Bearer sk-request" {
		t.Fatalf("Authorization = %q, want Bearer sk-request", gotAuth)
	}
	wantModels := []string{"gpt-a", "gpt-b"}
	if !reflect.DeepEqual(resp.Models, wantModels) {
		t.Fatalf("models = %v, want %v", resp.Models, wantModels)
	}
}

func TestModelQueryForSecretsPrefersMatchingProviderKey(t *testing.T) {
	query := modelQueryForSecrets("test-ns", triggersv1alpha1.ProviderOpenRouter, platformv1alpha1.AgentRunAuthModeAPIKey, "legacy-key", "", []platformv1alpha1.ProviderKeyRef{
		{Provider: triggersv1alpha1.ProviderOpenAI, SecretName: "openai-key", SecretKey: "openai-secret-key"},
		{Provider: triggersv1alpha1.ProviderOpenRouter, SecretName: "openrouter-key", SecretKey: "openrouter-secret-key"},
	})

	if query.apiKeySecretName != "openrouter-key" || query.apiKeySecretKey != "openrouter-secret-key" {
		t.Fatalf("api key ref = %q/%q, want openrouter-key/openrouter-secret-key", query.apiKeySecretName, query.apiKeySecretKey)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// swapProviderModelHTTPClient intercepts outbound provider model requests for
// the duration of a test.
func swapProviderModelHTTPClient(t *testing.T, rt roundTripFunc) {
	t.Helper()
	old := providerModelHTTPClient
	providerModelHTTPClient = &http.Client{Transport: rt}
	t.Cleanup(func() { providerModelHTTPClient = old })
}

func userCredSecret(provider string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "user-ns", Name: userCredentialSecretName(provider)},
		Data:       data,
	}
}

func TestSavedCredentialModelQuery(t *testing.T) {
	const ns = "user-ns"

	tests := []struct {
		name        string
		provider    string
		secrets     []client.Object
		projectKeys []platformv1alpha1.ProviderKeyRef
		want        modelQuery
		wantErrCode connect.Code
		wantErrGrep string
	}{
		{
			name:     "copilot saved oauth",
			provider: triggersv1alpha1.ProviderCopilot,
			secrets:  []client.Object{userCredSecret("copilot", map[string][]byte{"auth.json": []byte("{}")})},
			want: modelQuery{
				namespace:       ns,
				provider:        triggersv1alpha1.ProviderCopilot,
				authMode:        platformv1alpha1.AgentRunAuthModeOAuth,
				oauthSecretName: "usercred-copilot",
				openAIBaseURL:   triggersv1alpha1.DefaultCopilotBaseURL,
			},
		},
		{
			name:     "openai prefers saved oauth",
			provider: triggersv1alpha1.ProviderOpenAI,
			secrets: []client.Object{userCredSecret("openai", map[string][]byte{
				"auth.json": []byte("{}"),
				"api-key":   []byte("sk-openai"),
			})},
			want: modelQuery{
				namespace:       ns,
				provider:        triggersv1alpha1.ProviderOpenAI,
				authMode:        platformv1alpha1.AgentRunAuthModeOAuth,
				oauthSecretName: "usercred-openai",
				openAIBaseURL:   triggersv1alpha1.DefaultOpenAIOAuthBaseURL,
			},
		},
		{
			name:     "openai api key fallback",
			provider: triggersv1alpha1.ProviderOpenAI,
			secrets:  []client.Object{userCredSecret("openai", map[string][]byte{"api-key": []byte("sk-openai")})},
			want: modelQuery{
				namespace:        ns,
				provider:         triggersv1alpha1.ProviderOpenAI,
				authMode:         platformv1alpha1.AgentRunAuthModeAPIKey,
				apiKeySecretName: "usercred-openai",
				apiKeySecretKey:  "api-key",
				openAIBaseURL:    triggersv1alpha1.DefaultOpenAIBaseURL,
			},
		},
		{
			name:     "anthropic prefers saved oauth",
			provider: triggersv1alpha1.ProviderAnthropic,
			secrets:  []client.Object{userCredSecret("anthropic", map[string][]byte{"auth.json": []byte("{}")})},
			want: modelQuery{
				namespace:       ns,
				provider:        triggersv1alpha1.ProviderAnthropic,
				authMode:        platformv1alpha1.AgentRunAuthModeOAuth,
				oauthSecretName: "usercred-anthropic",
				openAIBaseURL:   "",
			},
		},
		{
			name:     "gemini reuses saved openai api key",
			provider: triggersv1alpha1.ProviderGemini,
			secrets:  []client.Object{userCredSecret("openai", map[string][]byte{"api-key": []byte("sk-openai")})},
			want: modelQuery{
				namespace:        ns,
				provider:         triggersv1alpha1.ProviderGemini,
				authMode:         platformv1alpha1.AgentRunAuthModeAPIKey,
				apiKeySecretName: "usercred-openai",
				apiKeySecretKey:  "api-key",
				openAIBaseURL:    triggersv1alpha1.DefaultGeminiBaseURL,
			},
		},
		{
			name:     "source provider key wins over saved credentials",
			provider: triggersv1alpha1.ProviderOpenAI,
			secrets:  []client.Object{userCredSecret("openai", map[string][]byte{"auth.json": []byte("{}")})},
			projectKeys: []platformv1alpha1.ProviderKeyRef{
				{Provider: "openai", SecretName: "proj-openai", SecretKey: "custom"},
			},
			want: modelQuery{
				namespace:        ns,
				provider:         triggersv1alpha1.ProviderOpenAI,
				authMode:         platformv1alpha1.AgentRunAuthModeAPIKey,
				apiKeySecretName: "proj-openai",
				apiKeySecretKey:  "custom",
				openAIBaseURL:    triggersv1alpha1.DefaultOpenAIBaseURL,
			},
		},
		{
			name:        "missing saved openai credentials",
			provider:    triggersv1alpha1.ProviderOpenAI,
			wantErrCode: connect.CodeFailedPrecondition,
			wantErrGrep: "no saved OpenAI",
		},
		{
			name:        "missing saved copilot credentials",
			provider:    triggersv1alpha1.ProviderCopilot,
			wantErrCode: connect.CodeFailedPrecondition,
			wantErrGrep: "no saved Copilot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := testProjectScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.secrets...).Build()
			srv := &Server{k8sClient: c, scheme: scheme}

			got, err := srv.savedCredentialModelQuery(context.Background(), ns, tt.provider, "", tt.projectKeys)
			if tt.wantErrCode != 0 {
				if connect.CodeOf(err) != tt.wantErrCode {
					t.Fatalf("error code = %v (err=%v), want %v", connect.CodeOf(err), err, tt.wantErrCode)
				}
				if !strings.Contains(err.Error(), tt.wantErrGrep) {
					t.Fatalf("error = %q, want it to mention %q", err.Error(), tt.wantErrGrep)
				}
				return
			}
			if err != nil {
				t.Fatalf("savedCredentialModelQuery() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("query = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// copilotSourceProject returns a Project whose defaults hold copilot OAuth
// credentials, the regression scenario for provider overrides.
func copilotSourceProject(namespace string) *triggersv1alpha1.Project {
	return &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: namespace},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Web",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Model:    "gpt-5",
				Provider: triggersv1alpha1.ProviderCopilot,
				AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					OpenAIOAuthSecret: "usercred-copilot",
				},
			},
		},
	}
}

func TestListAvailableModelsSourceProviderOverrideUsesSavedCredentials(t *testing.T) {
	const ns = "user-ns"
	var gotAPIKey string
	swapProviderModelHTTPClient(t, func(r *http.Request) (*http.Response, error) {
		gotAPIKey = r.Header.Get("x-api-key")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"claude-b"},{"id":"claude-a"}]}`)),
		}, nil
	})

	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		copilotSourceProject(ns),
		userCredSecret("anthropic", map[string][]byte{"api-key": []byte("sk-ant-saved")}),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.ListAvailableModels(context.Background(), &platform.ListAvailableModelsRequest{
		Namespace: ns,
		Source:    &platform.SourceRef{Kind: "Project", Name: "web"},
		Provider:  "anthropic",
	})
	if err != nil {
		t.Fatalf("ListAvailableModels() error = %v", err)
	}
	if gotAPIKey != "sk-ant-saved" {
		t.Fatalf("x-api-key = %q, want saved anthropic key", gotAPIKey)
	}
	if want := []string{"claude-a", "claude-b"}; !reflect.DeepEqual(resp.Models, want) {
		t.Fatalf("models = %v, want %v", resp.Models, want)
	}
}

func TestListAvailableModelsSourceProviderOverrideMissingSavedCredsFails(t *testing.T) {
	const ns = "user-ns"
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(copilotSourceProject(ns)).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.ListAvailableModels(context.Background(), &platform.ListAvailableModelsRequest{
		Namespace: ns,
		Source:    &platform.SourceRef{Kind: "Project", Name: "web"},
		Provider:  "openai",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("error code = %v (err=%v), want failed precondition", connect.CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "no saved OpenAI") {
		t.Fatalf("error = %q, want a missing-saved-credentials message", err.Error())
	}
	// Regression: the copilot OAuth secret must not be parsed as OpenAI material.
	if strings.Contains(err.Error(), "usercred-copilot") {
		t.Fatalf("error = %q, must not reference the copilot credential secret", err.Error())
	}
}

func TestListAvailableModelsSavedCredentialAnthropic(t *testing.T) {
	const ns = "user-ns"
	var gotAPIKey string
	swapProviderModelHTTPClient(t, func(r *http.Request) (*http.Response, error) {
		gotAPIKey = r.Header.Get("x-api-key")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"claude-a"}]}`)),
		}, nil
	})

	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		userCredSecret("anthropic", map[string][]byte{"api-key": []byte("sk-ant-saved")}),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.ListAvailableModels(context.Background(), &platform.ListAvailableModelsRequest{
		Namespace: ns,
		Provider:  "anthropic",
	})
	if err != nil {
		t.Fatalf("ListAvailableModels() error = %v", err)
	}
	if gotAPIKey != "sk-ant-saved" {
		t.Fatalf("x-api-key = %q, want saved anthropic key", gotAPIKey)
	}
	if want := []string{"claude-a"}; !reflect.DeepEqual(resp.Models, want) {
		t.Fatalf("models = %v, want %v", resp.Models, want)
	}
}
