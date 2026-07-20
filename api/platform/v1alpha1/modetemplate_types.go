/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ModeCategory classifies execution complexity.
// +kubebuilder:validation:Enum=direct;orchestrated
type ModeCategory string

const (
	ModeCategoryDirect       ModeCategory = "direct"
	ModeCategoryOrchestrated ModeCategory = "orchestrated"
)

// ModeExecutionStrategy controls how work is distributed.
// +kubebuilder:validation:Enum=serial;parallel;pipeline
type ModeExecutionStrategy string

const (
	ExecutionStrategySerial   ModeExecutionStrategy = "serial"
	ExecutionStrategyParallel ModeExecutionStrategy = "parallel"
	ExecutionStrategyPipeline ModeExecutionStrategy = "pipeline"
)

// ModeReasoningLevel controls reasoning effort.
// +kubebuilder:validation:Enum=none;low;medium;high;xhigh;max
type ModeReasoningLevel string

const (
	ReasoningNone   ModeReasoningLevel = "none"
	ReasoningLow    ModeReasoningLevel = "low"
	ReasoningMedium ModeReasoningLevel = "medium"
	ReasoningHigh   ModeReasoningLevel = "high"
	ReasoningXHigh  ModeReasoningLevel = "xhigh"
	ReasoningMax    ModeReasoningLevel = "max"
)

// ModeConstraints defines runtime limits for a mode.
type ModeConstraints struct {
	// +optional
	MaxTurns int32 `json:"maxTurns,omitempty"`
	// SubAgentMaxTurns limits how many turns each specialist sub-agent can spend.
	// When 0, sub-agents use the platform default.
	// +optional
	SubAgentMaxTurns int32 `json:"subAgentMaxTurns,omitempty"`
	// +optional
	MaxRuntimeMinutes int32 `json:"maxRuntimeMinutes,omitempty"`
	// +optional
	MaxRetries int32 `json:"maxRetries,omitempty"`
	// MaxConcurrentSubAgents limits parallel in-process sub-agent tasks.
	// When 0, no limit is enforced.
	// +optional
	MaxConcurrentSubAgents int32 `json:"maxConcurrentSubAgents,omitempty"`
}

// ModeTemplateSpec defines the desired state of ModeTemplate.
type ModeTemplateSpec struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +optional
	Description string `json:"description,omitempty"`

	Category ModeCategory `json:"category"`

	// Autonomous indicates the mode runs without user interaction.
	// When true, the agent never asks clarifying questions and completes work end-to-end.
	// +optional
	Autonomous bool `json:"autonomous,omitempty"`

	ExecutionStrategy ModeExecutionStrategy `json:"executionStrategy,omitempty"`
	// +optional
	Constraints *ModeConstraints `json:"constraints,omitempty"`

	// PermissionMode optionally clamps the run's effective permission mode.
	// The effective mode is the most restrictive of this value and the
	// RuntimeProfile's security.permissionMode: a mode template can restrict
	// but never grant. The review mode sets read-only so reviewers cannot
	// edit code regardless of the repository's RuntimeProfile.
	// +optional
	PermissionMode PermissionMode `json:"permissionMode,omitempty"`

	// AllowedMutatingTools lists mutating tool names that stay registered
	// even when the effective permission mode is read-only (for example the
	// GitHub review tools a read-only reviewer still needs).
	// +listType=atomic
	// +optional
	AllowedMutatingTools []string `json:"allowedMutatingTools,omitempty"`

	// DefaultMCPServerRefs lists MCPServer names that are automatically
	// attached to any AgentRun using this mode. The agent pipeline resolves
	// these into MCP tools without requiring per-repo or per-run configuration.
	// +listType=atomic
	// +optional
	DefaultMCPServerRefs []NamedRef `json:"defaultMCPServerRefs,omitempty"`

	// DefaultSkillRefs lists Skill names that are automatically attached to
	// any AgentRun using this mode (their required MCP servers come along).
	// +listType=atomic
	// +optional
	DefaultSkillRefs []NamedRef `json:"defaultSkillRefs,omitempty"`

	// Instructions is a rich behavioral prompt loaded into the agent's system context.
	// Defines mode philosophy, workflow, constraints, and behavioral rules.
	// +optional
	Instructions string `json:"instructions,omitempty"`
}

// ModeTemplateStatus defines the observed state of ModeTemplate.
type ModeTemplateStatus struct {
	// +optional
	Phase string `json:"phase,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Category",type=string,JSONPath=`.spec.category`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ModeTemplate is the Schema for the modetemplates API.
type ModeTemplate struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec ModeTemplateSpec `json:"spec"`

	// +optional
	Status ModeTemplateStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ModeTemplateList contains a list of ModeTemplate.
type ModeTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ModeTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ModeTemplate{}, &ModeTemplateList{})
}
