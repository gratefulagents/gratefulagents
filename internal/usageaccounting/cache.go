// Package usageaccounting holds the single classification of provider token
// accounting semantics shared by every server-side usage consumer.
//
// Providers disagree about whether the reported input token count already
// covers the cached prompt: OpenAI-style APIs report cached tokens as a subset
// of input_tokens, while Anthropic-style APIs report them as separate additive
// fields. Getting this wrong either doubles or erases the cached prompt, which
// on a warm run is nearly the whole input — so the answer must not be decided
// independently in each reader.
package usageaccounting

import "strings"

// NormalizedIdentity lowercases a provider or model identity and strips the
// separators installations vary on, so "GitHub-Copilot" and "github copilot"
// classify identically.
func NormalizedIdentity(value string) string {
	return strings.NewReplacer("-", "", "_", "", " ", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(value)))
}

// ProviderInputIncludesCache reports whether a provider's input token count
// already includes cached prompt tokens.
func ProviderInputIncludesCache(provider string) bool {
	switch NormalizedIdentity(provider) {
	case "openai", "copilot", "githubcopilot", "azure", "azureopenai", "openrouter", "xai", "openaicompatible":
		return true
	default:
		return false
	}
}

// ModelInputIncludesCache classifies a provider-qualified model name for
// records that carry no separate provider field.
func ModelInputIncludesCache(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"openai/", "copilot/", "github-copilot/", "azure/", "azure-openai/", "openrouter/", "xai/"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// InputIncludesCache answers the question for one usage record. The recorded
// flags win when the producer knew the answer; otherwise the provider (then
// the model identity) decides, defaulting to additive cache fields.
func InputIncludesCache(includeKnown, include bool, provider, model string) bool {
	if includeKnown {
		return include
	}
	return ProviderInputIncludesCache(provider) || ModelInputIncludesCache(model)
}
