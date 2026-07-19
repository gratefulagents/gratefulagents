// Package mcpattach resolves which MCPServer resources are attached to an
// AgentRun: the run's explicit mcpServerRefs plus the servers required by its
// attached skills (the "auto-attach" linkage). Shared by the run-pod builder
// (secret env injection) and the agent bootstrap (.mcp.json assembly) so both
// always agree on the effective server set.
package mcpattach

import (
	"context"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

// EffectiveMCPServerRefs returns the MCPServer names attached to a run:
// spec.mcpServerRefs plus the requires.mcpServers of every skill in
// spec.skillRefs, deduped by name in first-seen order. Skills that cannot be
// fetched contribute nothing (refs to missing resources are skipped at
// consumption time, matching the platform's degrade-don't-block convention).
func EffectiveMCPServerRefs(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) []platformv1alpha1.NamedRef {
	if run == nil {
		return nil
	}
	seen := make(map[string]bool)
	var out []platformv1alpha1.NamedRef
	add := func(refs []platformv1alpha1.NamedRef) {
		for _, ref := range refs {
			name := strings.TrimSpace(ref.Name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, platformv1alpha1.NamedRef{Name: name})
		}
	}
	add(run.Spec.MCPServerRefs)
	if c == nil {
		return out
	}
	for _, ref := range run.Spec.SkillRefs {
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			continue
		}
		skill := &platformv1alpha1.Skill{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, skill); err != nil {
			continue
		}
		if skill.Spec.Requires != nil {
			add(skill.Spec.Requires.MCPServers)
		}
	}
	return out
}
