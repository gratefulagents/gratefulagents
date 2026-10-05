// Package mcpattach resolves which MCPServer resources are attached to an
// AgentRun: the run's explicit mcpServerRefs plus the servers required by its
// attached skills (the "auto-attach" linkage). Shared by the run-pod builder
// (secret env injection) and the agent bootstrap (.mcp.json assembly) so both
// always agree on the effective server set.
package mcpattach

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

// EffectiveMCPServerRefs returns the MCPServer names attached to a run:
// spec.mcpServerRefs plus the requires.mcpServers of every skill in
// spec.skillRefs, deduped by name in first-seen order. Skills that do not
// exist contribute nothing (refs to missing resources are skipped at
// consumption time, matching the platform's degrade-don't-block convention).
// Any other failure to read a skill is returned, because silently dropping
// its servers would hide tools from the run for the whole pod lifetime.
func EffectiveMCPServerRefs(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) ([]platformv1alpha1.NamedRef, error) {
	if run == nil {
		return nil, nil
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
		return out, nil
	}
	for _, ref := range run.Spec.SkillRefs {
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			continue
		}
		skill := &platformv1alpha1.Skill{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, skill); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("reading Skill %s/%s for required MCP servers: %w", run.Namespace, name, err)
		}
		if skill.Spec.Requires != nil {
			add(skill.Spec.Requires.MCPServers)
		}
	}
	return out, nil
}
