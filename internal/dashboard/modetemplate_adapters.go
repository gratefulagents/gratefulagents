package dashboard

import (
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func k8sModeTemplateToProto(tmpl *platformv1alpha1.ModeTemplate) *platform.ModeTemplate {
	if tmpl == nil {
		return nil
	}
	return modeTemplateSpecToProto(&tmpl.Spec, tmpl.Name, tmpl.Namespace)
}

func modeTemplateSpecToProto(spec *platformv1alpha1.ModeTemplateSpec, k8sName, k8sNamespace string) *platform.ModeTemplate {
	if spec == nil {
		return nil
	}

	pb := &platform.ModeTemplate{
		Name:                 spec.Name,
		Version:              spec.Version,
		DisplayName:          spec.DisplayName,
		Description:          spec.Description,
		Category:             string(spec.Category),
		ExecutionStrategy:    string(spec.ExecutionStrategy),
		K8SName:              k8sName,
		K8SNamespace:         k8sNamespace,
		Autonomous:           spec.Autonomous,
		PermissionMode:       string(spec.PermissionMode),
		AllowedMutatingTools: append([]string(nil), spec.AllowedMutatingTools...),
	}

	if spec.Constraints != nil {
		pb.Constraints = &platform.ModeConstraints{
			MaxTurns:               spec.Constraints.MaxTurns,
			MaxRuntimeMinutes:      spec.Constraints.MaxRuntimeMinutes,
			MaxRetries:             spec.Constraints.MaxRetries,
			SubagentMaxTurns:       spec.Constraints.SubAgentMaxTurns,
			MaxConcurrentSubagents: spec.Constraints.MaxConcurrentSubAgents,
		}
	}

	for _, ref := range spec.DefaultMCPServerRefs {
		pb.DefaultMcpServerRefs = append(pb.DefaultMcpServerRefs, ref.Name)
	}
	for _, ref := range spec.DefaultSkillRefs {
		pb.DefaultSkillRefs = append(pb.DefaultSkillRefs, ref.Name)
	}
	pb.Instructions = spec.Instructions

	return pb
}
