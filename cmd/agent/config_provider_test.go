package main

import (
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

func TestProviderAPIModesPreferResponsesWhereSupported(t *testing.T) {
	modes := providerAPIModesFromSelected("openrouter", "", false)
	if got := modes[triggersv1alpha1.ProviderOpenRouter]; got != triggersv1alpha1.OpenAIAPIResponses {
		t.Fatalf("openrouter mode = %q, want responses", got)
	}
	if got := modes[triggersv1alpha1.ProviderXAI]; got != triggersv1alpha1.OpenAIAPIResponses {
		t.Fatalf("xai mode = %q, want responses", got)
	}
	if got := modes[triggersv1alpha1.ProviderGroq]; got != triggersv1alpha1.OpenAIAPIChatCompletions {
		t.Fatalf("groq mode = %q, want chat-completions", got)
	}
}

func TestProviderAPIModesKeepOpenRouterFallbacksOnChat(t *testing.T) {
	modes := providerAPIModesFromSelected("openrouter", triggersv1alpha1.OpenAIAPIResponses, true)
	if got := modes[triggersv1alpha1.ProviderOpenRouter]; got != triggersv1alpha1.OpenAIAPIChatCompletions {
		t.Fatalf("openrouter fallback mode = %q, want chat-completions", got)
	}
}
