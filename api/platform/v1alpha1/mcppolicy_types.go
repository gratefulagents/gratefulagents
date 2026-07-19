/*
Copyright 2026.

SPDX-License-Identifier: GPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// MCPDefaultAction is the policy default for unlisted MCP capabilities.
// +kubebuilder:validation:Enum=Allow;Deny
type MCPDefaultAction string

const (
	MCPDefaultActionAllow MCPDefaultAction = "Allow"
	MCPDefaultActionDeny  MCPDefaultAction = "Deny"
)

// MCPAllowedServer defines an allowlisted MCP server and optional tool subset.
type MCPAllowedServer struct {
	Name string `json:"name"`
	// +listType=atomic
	// +optional
	Tools []string `json:"tools,omitempty"`
}

// MCPBreakGlass defines exceptional-access posture.
type MCPBreakGlass struct {
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// +optional
	RequireAuditReason bool `json:"requireAuditReason,omitempty"`
	// +optional
	AdminMediated bool `json:"adminMediated,omitempty"`
}

// MCPPolicySpec defines the desired state of MCPPolicy.
type MCPPolicySpec struct {
	// +optional
	DefaultAction MCPDefaultAction `json:"defaultAction,omitempty"`
	// +listType=atomic
	// +optional
	AllowedServers []MCPAllowedServer `json:"allowedServers,omitempty"`
	// +optional
	BreakGlass *MCPBreakGlass `json:"breakGlass,omitempty"`
}

// MCPPolicyStatus defines the observed state of MCPPolicy.
type MCPPolicyStatus struct {
	// +optional
	Phase string `json:"phase,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Default",type=string,JSONPath=`.spec.defaultAction`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPPolicy is the Schema for the mcppolicies API.
type MCPPolicy struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec MCPPolicySpec `json:"spec"`

	// +optional
	Status MCPPolicyStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MCPPolicyList contains a list of MCPPolicy.
type MCPPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MCPPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPPolicy{}, &MCPPolicyList{})
}
