/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// RoleInstructionSpec defines the behavioral prompt for an agent role.
// The CR name MUST match the role name in AgentCatalog
// (e.g. "executor", "debugger", "code-reviewer").
// Convention: role name = RoleInstruction CR name. No explicit refs needed.
type RoleInstructionSpec struct {
	// Instructions is the full structured prompt injected into this role's
	// system context. Supports XML-structured sections like <identity>,
	// <constraints>, <delegation>, <execution_loop>, <style>, etc.
	Instructions string `json:"instructions"`

	// Description is a short one-line summary used for HandoffDescription
	// and tool catalog entries.
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
// Each CR defines the full behavioral prompt for one agent role.
// The CR name must match the role name (e.g. "executor", "debugger").
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
