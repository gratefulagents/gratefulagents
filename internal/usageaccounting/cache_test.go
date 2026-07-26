package usageaccounting

import "testing"

func TestInputIncludesCache(t *testing.T) {
	for _, tc := range []struct {
		name         string
		includeKnown bool
		include      bool
		provider     string
		model        string
		want         bool
	}{
		{name: "recorded flags win over provider", includeKnown: true, include: false, provider: "openai", model: "openai/gpt-5.6"},
		{name: "recorded inclusive flag wins over provider", includeKnown: true, include: true, provider: "anthropic", model: "anthropic/claude-opus-5", want: true},
		{name: "anthropic is additive", provider: "anthropic", model: "anthropic/claude-opus-5"},
		// Copilot serves Claude models over an OpenAI-compatible surface, so
		// the provider decides, not the model family.
		{name: "claude via copilot is inclusive", provider: "copilot", model: "copilot/claude-sonnet-4-5", want: true},
		{name: "separator and case variants normalize", provider: "GitHub-Copilot", want: true},
		{name: "model identity used when provider is absent", model: "openrouter/gpt-5.6", want: true},
		{name: "unknown provider defaults to additive", provider: "somegateway", model: "somegateway/model-x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := InputIncludesCache(tc.includeKnown, tc.include, tc.provider, tc.model); got != tc.want {
				t.Fatalf("InputIncludesCache(%v, %v, %q, %q) = %v, want %v", tc.includeKnown, tc.include, tc.provider, tc.model, got, tc.want)
			}
		})
	}
}
