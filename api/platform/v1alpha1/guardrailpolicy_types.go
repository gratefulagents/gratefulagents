/*
Copyright 2026.

SPDX-License-Identifier: GPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// GuardrailPolicySpec defines enterprise guardrail rules.
type GuardrailPolicySpec struct {
	// Rules defines the list of guardrail rules.
	// +listType=atomic
	// +optional
	Rules []GuardrailRule `json:"rules,omitempty"`
}

// GuardrailRule defines a single guardrail rule.
type GuardrailRule struct {
	// Name is a human-readable identifier.
	Name string `json:"name"`
	// Type is "tool-input" or "tool-output".
	// +kubebuilder:validation:Enum=tool-input;tool-output
	Type string `json:"type"`
	// ToolPattern is a glob pattern matching tool names (e.g., "bash*", "*").
	// +optional
	ToolPattern string `json:"toolPattern,omitempty"`
	// Regex is the pattern to match against tool input/output.
	Regex string `json:"regex"`
	// Action is what to do when matched.
	// +kubebuilder:validation:Enum=block;warn;log
	Action string `json:"action"`
	// Message is a human-readable explanation shown when triggered.
	// +optional
	Message string `json:"message,omitempty"`
}

// GuardrailPolicyStatus defines the observed state.
type GuardrailPolicyStatus struct {
	// +optional
	Phase string `json:"phase,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Rules",type=integer,JSONPath=`.spec.rules`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GuardrailPolicy defines enterprise guardrail rules for agent runs.
type GuardrailPolicy struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec GuardrailPolicySpec `json:"spec"`

	// +optional
	Status GuardrailPolicyStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GuardrailPolicyList contains a list of GuardrailPolicy.
type GuardrailPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GuardrailPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GuardrailPolicy{}, &GuardrailPolicyList{})
}
