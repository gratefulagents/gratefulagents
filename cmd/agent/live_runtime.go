package main

import (
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

// restartPending reports whether the controller has an unprocessed compute
// restart request for the run (e.g. a provider switch that must remount
// credentials), meaning this pod is about to be replaced and must not start
// another turn with its stale credential environment.
func restartPending(run *platformv1alpha1.AgentRun) bool {
	return run != nil && run.Spec.RestartRequests > run.Status.RestartRequestsHandled
}

func liveRuntimeModelAndProvider(cfg runConfig, run *platformv1alpha1.AgentRun) (model, provider string) {
	model = strings.TrimSpace(cfg.Model)
	provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	if run != nil && strings.TrimSpace(run.Spec.Model) != "" {
		model = strings.TrimSpace(run.Spec.Model)
		provider = "openai"
	}

	if prefix, _ := agent.ParseModelPrefix(model); strings.TrimSpace(prefix) != "" {
		provider = strings.ToLower(strings.TrimSpace(prefix))
		return model, provider
	}
	if provider == "" {
		provider = "openai"
	}
	if !strings.EqualFold(provider, strings.TrimSpace(cfg.Provider)) {
		return provider + "/" + model, provider
	}
	return model, provider
}
