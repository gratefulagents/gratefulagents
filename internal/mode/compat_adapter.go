package mode

import (
	"context"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InferSystemTemplate maps legacy WorkflowMode + ExecutionMode to a system template.
// Used for backward compatibility: runs without an explicit modeRef get an inferred template.
// Always reads the live CRD.
func InferSystemTemplate(
	ctx context.Context,
	c client.Reader,
	_ platformv1alpha1.AgentRunWorkflowMode,
	execution platformv1alpha1.AgentRunExecutionMode,
) *platformv1alpha1.ModeTemplateSpec {
	lookup := func(key string) *platformv1alpha1.ModeTemplateSpec {
		var crd platformv1alpha1.ModeTemplate
		if err := c.Get(ctx, client.ObjectKey{Name: key}, &crd); err != nil {
			return nil
		}
		spec := crd.Spec.DeepCopy()
		spec.Autonomous = true
		return spec
	}

	if execution == platformv1alpha1.ExecutionModeTeam {
		if tmpl := lookup("team-chat"); tmpl != nil {
			return tmpl
		}
	}

	// WorkflowModeChat is retained on the API only so existing resources still
	// decode. Runs without an explicit ModeTemplate use the interactive default.
	if tmpl := lookup("interactive"); tmpl != nil {
		return tmpl
	}
	// Preserve compatibility during upgrades where the new bundled template has
	// not been installed yet.
	if tmpl := lookup("autopilot"); tmpl != nil {
		return tmpl
	}
	return nil
}
