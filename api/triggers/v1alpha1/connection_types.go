/*
Copyright 2026.

SPDX-License-Identifier: GPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ConnectionType identifies an external integration.
type ConnectionType string

const (
	ConnectionTypeGitHub ConnectionType = "github"
	ConnectionTypeSlack  ConnectionType = "slack"
	ConnectionTypeLinear ConnectionType = "linear"
)

// GitHubConnectionConfig stores GitHub connection references and installation identity.
type GitHubConnectionConfig struct {
	// tokenSecret is the name of a Secret holding a GitHub token.
	// +optional
	TokenSecret string `json:"tokenSecret,omitempty"`

	// appID is the GitHub App ID.
	// +optional
	AppID int64 `json:"appId,omitempty"`

	// installationID is the GitHub App installation ID.
	// +optional
	InstallationID int64 `json:"installationId,omitempty"`

	// privateKeySecret is the name of a Secret holding the GitHub App private key.
	// +optional
	PrivateKeySecret string `json:"privateKeySecret,omitempty"`
}

// SlackConnectionConfig stores Slack credential references and workspace identity.
type SlackConnectionConfig struct {
	// tokensSecret is the name of a Secret holding Slack tokens.
	TokensSecret string `json:"tokensSecret"`

	// teamID is the Slack workspace/team ID.
	// +optional
	TeamID string `json:"teamId,omitempty"`
}

// LinearConnectionConfig stores Linear credential references and workspace identity.
type LinearConnectionConfig struct {
	// apiKeySecret is the name of a Secret holding the Linear API key.
	APIKeySecret string `json:"apiKeySecret"`

	// workspaceID is the Linear workspace ID.
	// +optional
	WorkspaceID string `json:"workspaceId,omitempty"`
}

// ConnectionSpec defines a reusable external connection.
// +kubebuilder:validation:XValidation:rule="(self.type == 'github' && has(self.github) && !has(self.slack) && !has(self.linear)) || (self.type == 'slack' && !has(self.github) && has(self.slack) && !has(self.linear)) || (self.type == 'linear' && !has(self.github) && !has(self.slack) && has(self.linear))",message="exactly one configuration matching type must be set"
type ConnectionSpec struct {
	// +kubebuilder:validation:Enum=github;slack;linear
	Type ConnectionType `json:"type"`

	// +optional
	GitHub *GitHubConnectionConfig `json:"github,omitempty"`

	// +optional
	Slack *SlackConnectionConfig `json:"slack,omitempty"`

	// +optional
	Linear *LinearConnectionConfig `json:"linear,omitempty"`
}

// ConnectionStatus defines the normalized observed state of Connection.
type ConnectionStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	LastError string `json:"lastError,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Connection is a reusable external integration that Projects may reference.
type Connection struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec ConnectionSpec `json:"spec"`

	// +optional
	Status ConnectionStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ConnectionList contains a list of Connection.
type ConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Connection `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Connection{}, &ConnectionList{})
}
