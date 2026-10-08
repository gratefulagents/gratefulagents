/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// RoleInstructionSpec defines one sub-agent role the parent agent can delegate
// to. The CR name is the role name (e.g. "general", "explore", "reviewer").
type RoleInstructionSpec struct {
	// Instructions is the role's prompt. The runtime adds it after the shared
	// sub-agent base prompt. Keep it short: the parent's task message carries
	// the task-specific instructions.
	Instructions string `json:"instructions"`

	// Description is shown to the parent agent when it picks a sub-agent: what
	// the role can do and when to use it.
	// +optional
	Description string `json:"description,omitempty"`

	// ToolAccess controls which tools this role can use.
	// +kubebuilder:validation:Enum=full;read-only;analysis;execution
	// +optional
	ToolAccess string `json:"toolAccess,omitempty"`

	// Model is a legacy provider-independent value retained for API
	// compatibility. Runtime role routing uses ModelsByProvider; when the active
	// provider has no entry, the role inherits the parent run's model.
	// +optional
	Model string `json:"model,omitempty"`

	// ModelsByProvider maps provider names (for example "openai", "anthropic",
	// or "copilot") to the model this role should use with that provider. When
	// the active provider has no entry, the role inherits the parent run's model.
	// +optional
	ModelsByProvider map[string]string `json:"modelsByProvider,omitempty"`

	// ReasoningLevel controls reasoning effort for this role. When empty, the
	// role inherits the parent run's reasoning level.
	// +optional
	ReasoningLevel ModeReasoningLevel `json:"reasoningLevel,omitempty"`
}

// RoleInstructionStatus defines the observed state of RoleInstruction.
type RoleInstructionStatus struct {
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
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RoleInstruction is the Schema for the roleinstructions API.
// Each CR defines one sub-agent role; the CR name is the role name.
// The runtime always offers a built-in "general" role, which a CR of that
// name replaces.
type RoleInstruction struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec RoleInstructionSpec `json:"spec"`

	// +optional
	Status RoleInstructionStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RoleInstructionList contains a list of RoleInstruction.
type RoleInstructionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []RoleInstruction `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RoleInstruction{}, &RoleInstructionList{})
}
