package dashboard

import (
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

func TestEffectiveModelForProviderOpenAI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		model    string
		provider string
		want     string
		wantErr  bool
	}{
		{
			name:     "empty openai model uses quality default",
			model:    "",
			provider: "openai",
			want:     "gpt-5.6-sol",
		},
		{
			name:     "empty anthropic model uses quality default",
			model:    "",
			provider: "anthropic",
			want:     "claude-opus-4-6",
		},
		{
			name:     "small alias is rejected",
			model:    "small",
			provider: "openai",
			wantErr:  true,
		},
		{
			name:     "medium alias is rejected",
			model:    "medium",
			provider: "openai",
			wantErr:  true,
		},
		{
			name:     "codex model is preserved",
			model:    "gpt-5.3-codex",
			provider: "openai",
			want:     "gpt-5.3-codex",
		},
		{
			name:     "explicit chat model is preserved",
			model:    "gpt-4.1-mini",
			provider: "openai",
			want:     "gpt-4.1-mini",
		},
		{
			name:     "non-openai provider unchanged",
			model:    "claude-sonnet-4-6",
			provider: "anthropic",
			want:     "claude-sonnet-4-6",
		},
		{
			name:     "gemini explicit model is preserved",
			model:    "gemini-2.5-pro",
			provider: "gemini",
			want:     "gemini-2.5-pro",
		},
		{
			name:     "openrouter explicit model is preserved",
			model:    "openai/gpt-5",
			provider: "openrouter",
			want:     "openai/gpt-5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := effectiveModelForProvider(tt.model, tt.provider)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("effectiveModelForProvider(%q, %q) expected error, got nil", tt.model, tt.provider)
				}
				return
			}
			if err != nil {
				t.Fatalf("effectiveModelForProvider(%q, %q) error = %v", tt.model, tt.provider, err)
			}
			if got != tt.want {
				t.Fatalf("effectiveModelForProvider(%q, %q) = %q, want %q", tt.model, tt.provider, got, tt.want)
			}
		})
	}
}

func TestResolveProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		override string
		fallback string
		want     string
		wantErr  bool
	}{
		{
			name:     "uses openrouter override",
			override: triggersv1alpha1.ProviderOpenRouter,
			fallback: triggersv1alpha1.ProviderOpenAI,
			want:     triggersv1alpha1.ProviderOpenRouter,
		},
		{
			name:     "uses copilot override",
			override: triggersv1alpha1.ProviderCopilot,
			fallback: triggersv1alpha1.ProviderOpenAI,
			want:     triggersv1alpha1.ProviderCopilot,
		},
		{
			name:     "falls back to default provider",
			override: "",
			fallback: "",
			want:     triggersv1alpha1.ProviderOpenAI,
		},
		{
			name:     "rejects unsupported provider",
			override: "custom",
			fallback: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveProvider(tt.override, tt.fallback)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveProvider(%q, %q) expected error, got nil", tt.override, tt.fallback)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveProvider(%q, %q) returned error: %v", tt.override, tt.fallback, err)
			}
			if got != tt.want {
				t.Fatalf("resolveProvider(%q, %q) = %q, want %q", tt.override, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestResolveRunModelKeepsOpenRouterVendorNamespace(t *testing.T) {
	t.Parallel()

	model, provider, err := resolveRunModelAndProvider("z-ai/glm-4.7", triggersv1alpha1.ProviderOpenRouter)
	if err != nil {
		t.Fatalf("resolveRunModelAndProvider() error = %v", err)
	}
	if model != "z-ai/glm-4.7" || provider != triggersv1alpha1.ProviderOpenRouter {
		t.Fatalf("resolveRunModelAndProvider() = %q, %q; want %q, %q", model, provider, "z-ai/glm-4.7", triggersv1alpha1.ProviderOpenRouter)
	}
	if got := prefixedModel(model, provider); got != "openrouter/z-ai/glm-4.7" {
		t.Fatalf("prefixedModel() = %q, want %q", got, "openrouter/z-ai/glm-4.7")
	}
}

func TestGenerateAgentRunNameUsesChatPrefix(t *testing.T) {
	t.Parallel()

	got := generateRunName("payments-app", "chat")
	if !strings.HasPrefix(got, "chat-payments-app-") {
		t.Fatalf("generateRunName(payments-app, chat) = %q, want chat-payments-app-<suffix>", got)
	}
}

func TestGenerateAgentRunNameUsesAutoPrefix(t *testing.T) {
	t.Parallel()

	got := generateRunName("payments-app", "auto")
	if !strings.HasPrefix(got, "auto-payments-app-") {
		t.Fatalf("generateRunName(payments-app, auto) = %q, want auto-payments-app-<suffix>", got)
	}
}

func TestGenerateAgentRunNameIsRandomPerCall(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		name := generateRunName("app", "chat")
		if seen[name] {
			t.Fatalf("generateRunName produced duplicate name %q", name)
		}
		seen[name] = true
	}
}

func TestGenerateAgentRunNameWithoutSourceFallsBack(t *testing.T) {
	t.Parallel()

	got := generateRunName("", "chat")
	if !strings.HasPrefix(got, "chat-task-") {
		t.Fatalf("generateRunName(\"\", chat) = %q, want chat-task-<suffix>", got)
	}
}
