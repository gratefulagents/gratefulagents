package triggers

import (
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

// applyPolicyRefs copies RuntimeProfileRef, MCPPolicyRef, MCPServerRefs,
// SkillRefs, the command-sandbox opt-out, the Kubernetes-admin grant, and the
// run timeout from AgentRunDefaults onto the AgentRunSpec.
func applyPolicyRefs(spec *platformv1alpha1.AgentRunSpec, defaults triggersv1alpha1.AgentRunDefaults) {
	if defaults.RuntimeProfileRef != nil {
		spec.RuntimeProfileRef = defaults.RuntimeProfileRef.DeepCopy()
	}
	if defaults.DisableCommandSandbox {
		spec.DisableCommandSandbox = true
	}
	if defaults.KubernetesAdmin {
		spec.KubernetesAdmin = true
	}
	// defaults.timeout documents "the maximum duration for created AgentRuns";
	// map it onto the run's enforced limit unless the caller already set one.
	if defaults.Timeout.Duration > 0 && (spec.Limits == nil || spec.Limits.MaxRuntime.Duration == 0) {
		if spec.Limits == nil {
			spec.Limits = &platformv1alpha1.AgentRunLimits{}
		}
		spec.Limits.MaxRuntime = defaults.Timeout
	}
	if defaults.MCPPolicyRef != nil {
		spec.MCPPolicyRef = defaults.MCPPolicyRef.DeepCopy()
	}
	if len(defaults.MCPServerRefs) > 0 {
		refs := make([]platformv1alpha1.NamedRef, len(defaults.MCPServerRefs))
		copy(refs, defaults.MCPServerRefs)
		spec.MCPServerRefs = refs
	}
	if len(defaults.SkillRefs) > 0 {
		refs := make([]platformv1alpha1.NamedRef, len(defaults.SkillRefs))
		copy(refs, defaults.SkillRefs)
		spec.SkillRefs = refs
	}
}
