package main

import (
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	sdkmode "github.com/gratefulagents/sdk/pkg/agentsdk/mode"
)

func platformModeSnapshotForSDK(snapshot *platformv1alpha1.ModeTemplateSpec) *sdkmode.TemplateSpec {
	if snapshot == nil {
		return nil
	}
	spec := &sdkmode.TemplateSpec{
		Name:         snapshot.Name,
		Version:      snapshot.Version,
		DisplayName:  snapshot.DisplayName,
		Description:  snapshot.Description,
		Instructions: snapshot.Instructions,
		Constraints:  platformConstraintsForSDK(snapshot.Constraints),
	}
	// Read-only mode templates (plan, review) clamp the SDK run's tool
	// access so the runner adapts write tools per turn.
	if snapshot.PermissionMode == platformv1alpha1.PermissionModeReadOnly {
		spec.ToolAccess = "read-only"
	}
	return spec
}

func platformConstraintsForSDK(constraints *platformv1alpha1.ModeConstraints) *sdkmode.Constraints {
	if constraints == nil {
		return nil
	}
	return &sdkmode.Constraints{
		MaxTurns:               int(constraints.MaxTurns),
		SubAgentMaxTurns:       int(constraints.SubAgentMaxTurns),
		MaxConcurrentSubAgents: int(constraints.MaxConcurrentSubAgents),
		MaxRetries:             int(constraints.MaxRetries),
		MaxRuntimeMinutes:      int(constraints.MaxRuntimeMinutes),
	}
}
