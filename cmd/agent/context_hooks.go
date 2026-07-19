package main

import (
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

// contextUsageHooks captures the prompt-side token count of the main agent's
// most recent generation — i.e. the run's current context size — so status
// reporting doesn't depend on the tracing pipeline. Sub-agent generations are
// ignored: they run in their own context windows.
type contextUsageHooks struct {
	agent.NoOpRunHooks
	mainAgentName string
}

func (h *contextUsageHooks) OnLLMEnd(_ *agent.RunContext, a *agent.Agent, resp *agent.ModelResponse) {
	if resp == nil || a == nil || a.Name != h.mainAgentName {
		return
	}
	// ContextTokens is the runner's provider-normalized prompt-side size.
	// Summing usage fields here double-counts the cached prompt on
	// OpenAI-style providers (input_tokens already include cached tokens):
	// the context gauge read ~91% while real usage was ~46%, oscillating
	// with cache hits (run chat-gf-all-xa9h7s). The sum remains only as a
	// fallback for older SDKs that do not stamp ContextTokens.
	context := resp.ContextTokens
	if context <= 0 {
		context = resp.Usage.InputTokens + resp.Usage.CacheReadTokens + resp.Usage.CacheCreateTokens
	}
	if context > 0 {
		currentContextTokens.Store(context)
	}
}
