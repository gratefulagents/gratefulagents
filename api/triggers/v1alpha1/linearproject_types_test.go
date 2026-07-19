package v1alpha1

import (
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func TestNormalizeProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "defaults to openai", input: "", expected: ProviderOpenAI},
		{name: "anthropic", input: "Anthropic", expected: ProviderAnthropic},
		{name: "gemini", input: "gemini", expected: ProviderGemini},
		{name: "openrouter", input: "openrouter", expected: ProviderOpenRouter},
		{name: "groq", input: "groq", expected: ProviderGroq},
		{name: "xai", input: "xai", expected: ProviderXAI},
		{name: "copilot", input: "copilot", expected: ProviderCopilot},
		{name: "unknown falls back to openai", input: "custom", expected: ProviderOpenAI},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeProvider(tt.input); got != tt.expected {
				t.Fatalf("NormalizeProvider(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestProviderModelQualification(t *testing.T) {
	t.Parallel()

	splitTests := []struct {
		name         string
		model        string
		wantProvider string
		wantModel    string
	}{
		{name: "recognized provider", model: "anthropic/claude-sonnet-4-6", wantProvider: ProviderAnthropic, wantModel: "claude-sonnet-4-6"},
		{name: "nested OpenRouter model", model: "openrouter/z-ai/glm-4.7", wantProvider: ProviderOpenRouter, wantModel: "z-ai/glm-4.7"},
		{name: "vendor namespace is not provider", model: "z-ai/glm-4.7", wantModel: "z-ai/glm-4.7"},
	}
	for _, tt := range splitTests {
		t.Run("split "+tt.name, func(t *testing.T) {
			provider, model := SplitProviderModel(tt.model)
			if provider != tt.wantProvider || model != tt.wantModel {
				t.Fatalf("SplitProviderModel(%q) = %q, %q; want %q, %q", tt.model, provider, model, tt.wantProvider, tt.wantModel)
			}
		})
	}

	prefixTests := []struct {
		name     string
		model    string
		provider string
		want     string
	}{
		{name: "bare OpenAI model stays bare", model: "gpt-5.4", provider: ProviderOpenAI, want: "gpt-5.4"},
		{name: "OpenAI vendor model is disambiguated", model: "z-ai/glm-4.7", provider: ProviderOpenAI, want: "openai/z-ai/glm-4.7"},
		{name: "OpenRouter vendor model is preserved", model: "z-ai/glm-4.7", provider: ProviderOpenRouter, want: "openrouter/z-ai/glm-4.7"},
		{name: "existing provider prefix is idempotent", model: "openrouter/z-ai/glm-4.7", provider: ProviderOpenRouter, want: "openrouter/z-ai/glm-4.7"},
		{name: "recognized vendor namespace stays nested", model: "openai/gpt-5", provider: ProviderOpenRouter, want: "openrouter/openai/gpt-5"},
	}
	for _, tt := range prefixTests {
		t.Run("prefix "+tt.name, func(t *testing.T) {
			if got := PrefixModelWithProvider(tt.model, tt.provider); got != tt.want {
				t.Fatalf("PrefixModelWithProvider(%q, %q) = %q, want %q", tt.model, tt.provider, got, tt.want)
			}
		})
	}
}

func TestResolveOpenAIBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		override string
		expected string
	}{
		{name: "openai default", provider: ProviderOpenAI, expected: DefaultOpenAIBaseURL},
		{name: "gemini default", provider: ProviderGemini, expected: DefaultGeminiBaseURL},
		{name: "openrouter default", provider: ProviderOpenRouter, expected: DefaultOpenRouterBaseURL},
		{name: "groq default", provider: ProviderGroq, expected: DefaultGroqBaseURL},
		{name: "xai default", provider: ProviderXAI, expected: DefaultXAIBaseURL},
		{name: "copilot default", provider: ProviderCopilot, expected: DefaultCopilotBaseURL},
		{name: "anthropic has no openai base", provider: ProviderAnthropic, expected: ""},
		{name: "override wins", provider: ProviderGemini, override: "https://example.com/v1", expected: "https://example.com/v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveOpenAIBaseURL(tt.provider, tt.override); got != tt.expected {
				t.Fatalf("ResolveOpenAIBaseURL(%q, %q) = %q, want %q", tt.provider, tt.override, got, tt.expected)
			}
		})
	}
}

func TestResolveOpenAIBaseURLWithAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		override string
		authMode platformv1alpha1.AgentRunAuthMode
		expected string
	}{
		{name: "api-key openai", provider: ProviderOpenAI, authMode: platformv1alpha1.AgentRunAuthModeAPIKey, expected: DefaultOpenAIBaseURL},
		{name: "oauth openai", provider: ProviderOpenAI, authMode: platformv1alpha1.AgentRunAuthModeOAuth, expected: DefaultOpenAIOAuthBaseURL},
		{name: "oauth with override", provider: ProviderOpenAI, override: "https://custom.example.com/api", authMode: platformv1alpha1.AgentRunAuthModeOAuth, expected: "https://custom.example.com/api"},
		{name: "oauth non-openai falls back", provider: ProviderGemini, authMode: platformv1alpha1.AgentRunAuthModeOAuth, expected: DefaultGeminiBaseURL},
		{name: "oauth copilot uses copilot api", provider: ProviderCopilot, authMode: platformv1alpha1.AgentRunAuthModeOAuth, expected: DefaultCopilotBaseURL},
		{name: "anthropic unaffected", provider: ProviderAnthropic, authMode: platformv1alpha1.AgentRunAuthModeAPIKey, expected: ""},
		{name: "anthropic oauth has no openai base", provider: ProviderAnthropic, authMode: platformv1alpha1.AgentRunAuthModeOAuth, expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveOpenAIBaseURLWithAuth(tt.provider, tt.override, tt.authMode); got != tt.expected {
				t.Fatalf("ResolveOpenAIBaseURLWithAuth(%q, %q, %q) = %q, want %q", tt.provider, tt.override, tt.authMode, got, tt.expected)
			}
		})
	}
}

func TestDefaultMainModelForProvider(t *testing.T) {
	tests := map[string]string{
		ProviderOpenAI:     "gpt-5.6-sol",
		ProviderCopilot:    "gpt-5.6-sol",
		ProviderAnthropic:  "claude-opus-4-6",
		ProviderOpenRouter: "",
	}
	for provider, want := range tests {
		if got := DefaultMainModelForProvider(provider); got != want {
			t.Fatalf("DefaultMainModelForProvider(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestNormalizeAuthMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "default api key", input: "", want: "api-key"},
		{name: "oauth", input: "oauth", want: "oauth"},
		{name: "oauth uppercase", input: "OAUTH", want: "oauth"},
		{name: "invalid falls back", input: "token", want: "api-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := string(NormalizeAuthMode(tt.input)); got != tt.want {
				t.Fatalf("NormalizeAuthMode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateProviderAuthMode(t *testing.T) {
	t.Parallel()

	if err := ValidateProviderAuthMode(ProviderOpenAI, NormalizeAuthMode("oauth")); err != nil {
		t.Fatalf("ValidateProviderAuthMode(openai, oauth) error = %v, want nil", err)
	}
	if err := ValidateProviderAuthMode(ProviderAnthropic, NormalizeAuthMode("oauth")); err != nil {
		t.Fatalf("ValidateProviderAuthMode(anthropic, oauth) error = %v, want nil", err)
	}
	if err := ValidateProviderAuthMode(ProviderCopilot, NormalizeAuthMode("oauth")); err != nil {
		t.Fatalf("ValidateProviderAuthMode(copilot, oauth) error = %v, want nil", err)
	}
	if err := ValidateProviderAuthMode(ProviderGemini, NormalizeAuthMode("oauth")); err == nil {
		t.Fatal("ValidateProviderAuthMode(gemini, oauth) error = nil, want error")
	}
}

func TestRequiresOpenAIOAuthSecret(t *testing.T) {
	t.Parallel()

	if !RequiresOpenAIOAuthSecret(ProviderOpenAI, NormalizeAuthMode("oauth")) {
		t.Fatal("RequiresOpenAIOAuthSecret(openai, oauth) = false, want true")
	}
	if !RequiresOpenAIOAuthSecret(ProviderAnthropic, NormalizeAuthMode("oauth")) {
		t.Fatal("RequiresOpenAIOAuthSecret(anthropic, oauth) = false, want true")
	}
	if !RequiresOpenAIOAuthSecret(ProviderCopilot, NormalizeAuthMode("oauth")) {
		t.Fatal("RequiresOpenAIOAuthSecret(copilot, oauth) = false, want true")
	}
	if RequiresOpenAIOAuthSecret(ProviderGemini, NormalizeAuthMode("oauth")) {
		t.Fatal("RequiresOpenAIOAuthSecret(gemini, oauth) = true, want false")
	}
	if RequiresOpenAIOAuthSecret(ProviderOpenAI, NormalizeAuthMode("api-key")) {
		t.Fatal("RequiresOpenAIOAuthSecret(openai, api-key) = true, want false")
	}
}

func TestNormalizeOpenAIAPIForProvider(t *testing.T) {
	t.Parallel()

	if got := NormalizeOpenAIAPIForProvider(ProviderCopilot, ""); got != OpenAIAPIResponses {
		t.Fatalf("NormalizeOpenAIAPIForProvider(copilot, empty) = %q, want responses", got)
	}
	if got := NormalizeOpenAIAPIForProvider(ProviderOpenAI, ""); got != OpenAIAPIResponses {
		t.Fatalf("NormalizeOpenAIAPIForProvider(openai, empty) = %q, want responses", got)
	}
	if got := NormalizeOpenAIAPIForProvider(ProviderCopilot, OpenAIAPIResponses); got != OpenAIAPIResponses {
		t.Fatalf("NormalizeOpenAIAPIForProvider(copilot, responses) = %q, want responses", got)
	}
	if got := NormalizeOpenAIAPIForProvider(ProviderOpenAI, OpenAIAPIResponses); got != OpenAIAPIResponses {
		t.Fatalf("NormalizeOpenAIAPIForProvider(openai, responses) = %q, want responses", got)
	}
}

func TestResolveWorkflowMode(t *testing.T) {
	t.Parallel()

	if got := (AgentRunDefaults{}).ResolveWorkflowMode(); got != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("ResolveWorkflowMode() = %q, want auto default", got)
	}
	if got := (AgentRunDefaults{WorkflowMode: platformv1alpha1.WorkflowModeChat}).ResolveWorkflowMode(); got != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("ResolveWorkflowMode() = %q, want legacy chat normalized to auto", got)
	}
	if got := (AgentRunDefaults{WorkflowMode: platformv1alpha1.WorkflowModeAuto}).ResolveWorkflowMode(); got != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("ResolveWorkflowMode() = %q, want explicit auto", got)
	}
}
