/*
Copyright 2026.

SPDX-License-Identifier: GPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// SlackWorkspaceSpec defines the desired state of SlackWorkspace: one shared
// Slack app installed in a single Slack workspace, served by exactly one
// connector Deployment. Socket Mode load-balances events across an app's open
// sockets, so a shared app's events must be consumed by a single process that
// routes them to member SlackAgents.
type SlackWorkspaceSpec struct {
	// tokensSecret is the name of a K8s Secret in the same namespace holding the
	// shared app's tokens. Expected keys: bot-token (xoxb-) and app-token
	// (xapp-, required for Socket Mode). No user token: a shared app has no
	// per-user OAuth material.
	// +kubebuilder:validation:MinLength=1
	TokensSecret string `json:"tokensSecret"`

	// teamId optionally pins the Slack workspace (team) this app may serve.
	// When set, the connector drops events from any other team and refuses to
	// start if auth.test resolves a different team. When empty, the team
	// resolved on first connect is recorded in status and enforced thereafter.
	// +optional
	TeamID string `json:"teamId,omitempty"`

	// suspend pauses the connector (scales the Deployment to zero) while keeping
	// status readable. Member SlackAgents become unavailable while suspended.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// image overrides the connector pod image. When omitted, the controller uses
	// its built-in default worker image.
	// +optional
	Image string `json:"image,omitempty"`
}

// SlackWorkspaceStatus defines the observed state of SlackWorkspace.
type SlackWorkspaceStatus struct {
	// botUserId is the resolved Slack user ID of the shared bot (from auth.test).
	// +optional
	BotUserID string `json:"botUserId,omitempty"`

	// teamId is the resolved Slack workspace/team ID (from auth.test).
	// +optional
	TeamID string `json:"teamId,omitempty"`

	// deploymentName is the connector Deployment managed by this SlackWorkspace.
	// +optional
	DeploymentName string `json:"deploymentName,omitempty"`

	// memberCount is the number of SlackAgents currently bound to this
	// workspace via spec.workspaceRef.
	// +optional
	MemberCount int32 `json:"memberCount,omitempty"`

	// lastError contains the message from the most recent failed operation.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// conditions represent the current state of the SlackWorkspace.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for SlackWorkspace.
const (
	// ConditionSlackWorkspaceReady indicates the shared connector Deployment is
	// provisioned and ready.
	ConditionSlackWorkspaceReady = "Ready"

	// ConditionSlackWorkspaceTokenValid indicates the shared Slack tokens were
	// found (presence-checked; the connector performs live auth.test).
	ConditionSlackWorkspaceTokenValid = "TokenValid"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Team",type=string,JSONPath=`.status.teamId`
// +kubebuilder:printcolumn:name="Members",type=integer,JSONPath=`.status.memberCount`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SlackWorkspace is the Schema for the slackworkspaces API. It represents one
// shared Slack app (installed in a single Slack workspace) whose connector
// serves every SlackAgent that references it via spec.workspaceRef, routing
// events to the sending user's agent. This is the multi-user alternative to
// each user creating a dedicated Slack app.
type SlackWorkspace struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SlackWorkspaceSpec `json:"spec"`

	// +optional
	Status SlackWorkspaceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SlackWorkspaceList contains a list of SlackWorkspace.
type SlackWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SlackWorkspace `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SlackWorkspace{}, &SlackWorkspaceList{})
}
