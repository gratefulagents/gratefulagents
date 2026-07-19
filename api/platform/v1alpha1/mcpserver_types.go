/*
Copyright 2026.

SPDX-License-Identifier: GPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// MCPServerSecretEnv names one environment variable sourced from a Secret
// key that the run pod must expose so the MCP server can authenticate. Values
// never appear in the CRD; the platform injects them as optional secretKeyRef
// envs when a run references this server, and the agent bridges each value
// into the server subprocess env at run start (secretEnv names pass the SDK
// env filter automatically — no allowEnv pairing needed).
type MCPServerSecretEnv struct {
	// name is the environment variable to set (e.g. GRAFANA_SERVICE_ACCOUNT_TOKEN).
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// secretName is the Secret (in the run's namespace) holding the value.
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName"`
	// secretKey is the key within the Secret.
	// +kubebuilder:validation:MinLength=1
	SecretKey string `json:"secretKey"`
	// optional makes a missing Secret/key non-fatal for pod startup (the env
	// is simply absent). Defaults to true semantics at injection time; set
	// false to require the credential.
	// +optional
	Optional *bool `json:"optional,omitempty"`
}

// MCPServerConfig defines how to run the MCP server.
type MCPServerConfig struct {
	// type is the transport. Only "stdio" is supported today; the enum grows
	// when a "remote" (HTTP/OAuth) transport is added.
	// +kubebuilder:validation:Enum=stdio
	// +optional
	Type string `json:"type,omitempty"`
	// +required
	Command string `json:"command"`
	// +listType=atomic
	// +optional
	Args []string `json:"args,omitempty"`
	// +optional
	Env map[string]string `json:"env,omitempty"`
	// AllowEnv is the explicit opt-in list of environment variable names that
	// may pass through to the MCP server subprocess even though they match the
	// SDK credential denylist (e.g. names containing TOKEN/SECRET/PASSWORD).
	// Only list names the server genuinely needs; everything else is filtered.
	// +listType=atomic
	// +optional
	AllowEnv []string `json:"allowEnv,omitempty"`
	// SecretEnv lists environment variables sourced from Secrets that runs
	// referencing this server need (API tokens, endpoint URLs). Injected into
	// the run pod as secretKeyRef envs — use instead of Env for anything
	// sensitive.
	// +listType=atomic
	// +optional
	SecretEnv []MCPServerSecretEnv `json:"secretEnv,omitempty"`
	// TrustReadOnlyHint opts in to trusting this server's per-tool read-only
	// hints. When false (the default), the server's tools are treated as
	// mutating and are filtered out in read-only permission mode. Enable only
	// for servers you trust to label their tools honestly.
	// +optional
	TrustReadOnlyHint bool `json:"trustReadOnlyHint,omitempty"`
	// AllowNetwork opts this cluster-managed server into the run pod's network
	// namespace. The RuntimeProfile's egress policy remains the outer boundary.
	// Repository .mcp.json files cannot request this capability.
	// +optional
	AllowNetwork bool `json:"allowNetwork,omitempty"`
}

// MCPServerSpec defines the desired state of MCPServer.
// An MCPServer is a named, cluster-managed MCP server config that can be
// attached to AgentRuns through spec.mcpServerRefs, or pulled in automatically
// by a Skill that requires it.
type MCPServerSpec struct {
	// +optional
	Version string `json:"version,omitempty"`
	// +required
	MCPServerConfig *MCPServerConfig `json:"mcpServerConfig"`
	// description is a short human-readable summary shown when browsing
	// servers (e.g. in the dashboard selector).
	// +optional
	Description string `json:"description,omitempty"`
}

// MCPServerStatus defines the observed state of MCPServer.
type MCPServerStatus struct {
	// +optional
	Phase string `json:"phase,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPServer is the Schema for cluster-managed MCP server configs.
type MCPServer struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec MCPServerSpec `json:"spec"`

	// +optional
	Status MCPServerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MCPServerList contains a list of MCPServer.
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MCPServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPServer{}, &MCPServerList{})
}
