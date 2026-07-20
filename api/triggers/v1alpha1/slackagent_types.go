/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Secret data keys expected in spec.tokensSecret for a SlackAgent. The
// connector reads these to authenticate to Slack. Socket Mode authenticates the
// outbound WebSocket with the app-level token, so no signing secret is needed.
const (
	// SlackBotTokenKey holds the bot token (xoxb-): the agent's own identity,
	// used for posting in the bot<->owner control DM and Block Kit interactions.
	SlackBotTokenKey = "bot-token"

	// SlackUserTokenKey holds the owner's user token (xoxp-). Optional: it only
	// powers the agent's slack_search read tool (Slack permits search.messages
	// with user tokens only) and lets the connector resolve the owner's Slack
	// user ID via auth.test when spec.slackUserId is not set.
	SlackUserTokenKey = "user-token"

	// SlackAppTokenKey holds the app-level token (xapp-, connections:write)
	// required to open the Socket Mode WebSocket.
	SlackAppTokenKey = "app-token"
)

// SlackChannelReplyMode controls how the agent's conversational replies to
// public surfaces (channels, private channels, group DMs) are delivered.
// Replies in the owner's 1:1 DM and assistant pane are always posted directly.
type SlackChannelReplyMode string

const (
	// SlackChannelReplyRequireApproval holds the agent's reply as a draft and
	// asks the owner to approve it (in the owner's control DM) before anything
	// is posted. This is the safe default.
	SlackChannelReplyRequireApproval SlackChannelReplyMode = "require-approval"

	// SlackChannelReplyAuto lets the agent post channel replies directly,
	// without owner approval.
	SlackChannelReplyAuto SlackChannelReplyMode = "auto"
)

// SlackWorkspaceRef points a SlackAgent at a shared SlackWorkspace app instead
// of a dedicated per-user Slack app.
type SlackWorkspaceRef struct {
	// name of the SlackWorkspace.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// namespace of the SlackWorkspace. Defaults to the SlackAgent's namespace
	// when empty.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// SlackAppHomeSpec customizes the static copy the connector publishes to the
// app's App Home tab. The Home tab is visible to any workspace member who
// opens the app, so it only ever renders this static copy — never live agent
// state (connection status, configuration, or held replies).
type SlackAppHomeSpec struct {
	// header overrides the App Home header line. Defaults to the agent name.
	// Rendered as Slack plain text; Slack caps header blocks at 150 characters.
	// +kubebuilder:validation:MaxLength=150
	// +optional
	Header string `json:"header,omitempty"`

	// text overrides the short info line under the header. Rendered as Slack
	// mrkdwn. Defaults to a generic note pointing at the owner's dashboard.
	// +kubebuilder:validation:MaxLength=1000
	// +optional
	Text string `json:"text,omitempty"`
}

// SlackAgentSpec defines the desired state of SlackAgent.
type SlackAgentSpec struct {
	// tokensSecret is the name of a K8s Secret in the same namespace holding the
	// Slack tokens for a dedicated per-user Slack app. Expected keys: bot-token
	// (xoxb-), user-token (xoxp-), and app-token (xapp-, required for Socket
	// Mode). Exactly one of tokensSecret or workspaceRef must be set.
	// +optional
	TokensSecret string `json:"tokensSecret,omitempty"`

	// workspaceRef binds this agent to a shared SlackWorkspace app instead of a
	// dedicated one. The workspace's single connector serves this agent, routing
	// the owner's messages here; slackUserId is required so the connector can
	// map Slack users to member agents. Exactly one of tokensSecret or
	// workspaceRef must be set.
	// +optional
	WorkspaceRef *SlackWorkspaceRef `json:"workspaceRef,omitempty"`

	// slackUserId is the owner's Slack user ID (e.g. "U0123ABC"). Used to
	// distinguish the owner's own messages from messages other people send them
	// and to avoid self-reply loops. When empty, the connector resolves it from
	// the user token via auth.test.
	// +optional
	SlackUserID string `json:"slackUserId,omitempty"`

	// suspend pauses the connector (scales the Deployment to zero) while keeping
	// status readable.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// image overrides the connector pod image. When omitted, the controller uses
	// its built-in default worker image.
	// +optional
	Image string `json:"image,omitempty"`

	// channelReplyMode controls whether the agent's replies to public surfaces
	// (channels, private channels, group DMs) require the owner's approval
	// before they are posted. Defaults to require-approval; set to auto to let
	// the agent post directly. 1:1 DM and assistant-pane replies are always
	// direct.
	// +kubebuilder:validation:Enum=require-approval;auto
	// +kubebuilder:default="require-approval"
	// +optional
	ChannelReplyMode SlackChannelReplyMode `json:"channelReplyMode,omitempty"`

	// commanders lists who may command the agent via channel @mentions besides
	// the owner (always allowed). Fail-closed: when empty, only the owner can
	// command it. Non-commanders are silently ignored (logged, never answered).
	// +listType=set
	// +optional
	Commanders []string `json:"commanders,omitempty"`

	// sessionIdleMinutes controls conversation continuity: an incoming message
	// reuses the conversation's existing AgentRun when the last activity was
	// within this window, otherwise a fresh run starts. Keeps a long-lived
	// conversation's context (and cost) from growing without bound. Defaults to
	// 720 (12h) when unset or non-positive.
	// +optional
	SessionIdleMinutes *int32 `json:"sessionIdleMinutes,omitempty"`

	// appHome customizes the static App Home tab copy (header and info line).
	// When nil, the connector renders its built-in defaults.
	// +optional
	AppHome *SlackAppHomeSpec `json:"appHome,omitempty"`

	// defaults holds the fields used when creating child AgentRuns for heavy
	// tasks (model, provider, credentials, mode, optional repository).
	Defaults AgentRunDefaults `json:"defaults"`
}

// SlackAgentStatus defines the observed state of SlackAgent.
type SlackAgentStatus struct {
	// botUserId is the resolved Slack user ID of the bot (from auth.test).
	// +optional
	BotUserID string `json:"botUserId,omitempty"`

	// teamId is the resolved Slack workspace/team ID (from auth.test).
	// +optional
	TeamID string `json:"teamId,omitempty"`

	// deploymentName is the connector Deployment managed by this SlackAgent.
	// +optional
	DeploymentName string `json:"deploymentName,omitempty"`

	// eventsProcessed is the cumulative number of Slack events handled.
	// +optional
	EventsProcessed int32 `json:"eventsProcessed,omitempty"`

	// lastEventTime is when the connector last processed a Slack event.
	// +optional
	LastEventTime *metav1.Time `json:"lastEventTime,omitempty"`

	// lastError contains the message from the most recent failed operation.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// conditions represent the current state of the SlackAgent.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for SlackAgent.
const (
	// ConditionSlackAgentReady indicates the connector Deployment is provisioned
	// and ready.
	ConditionSlackAgentReady = "Ready"

	// ConditionSlackAgentTokenValid indicates the Slack tokens were validated
	// via auth.test.
	ConditionSlackAgentTokenValid = "TokenValid"

	// ConditionSlackAgentConnected indicates the connector reports a live Socket
	// Mode connection.
	ConditionSlackAgentConnected = "Connected"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="(has(self.spec.tokensSecret) && self.spec.tokensSecret != '') != has(self.spec.workspaceRef)",message="exactly one of spec.tokensSecret or spec.workspaceRef must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.workspaceRef) || (has(self.spec.slackUserId) && self.spec.slackUserId != '')",message="spec.slackUserId is required when spec.workspaceRef is set"
// +kubebuilder:printcolumn:name="Team",type=string,JSONPath=`.status.teamId`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="Events",type=integer,JSONPath=`.status.eventsProcessed`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SlackAgent is the Schema for the slackagents API. Each SlackAgent provisions a
// named Slack connector that opens an outbound Socket Mode WebSocket and runs
// the agent loop in-process, spawning child AgentRuns for heavy tasks.
type SlackAgent struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SlackAgentSpec `json:"spec"`

	// +optional
	Status SlackAgentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SlackAgentList contains a list of SlackAgent.
type SlackAgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SlackAgent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SlackAgent{}, &SlackAgentList{})
}

// UsesWorkspace reports whether this agent is served by a shared SlackWorkspace
// connector instead of its own dedicated one.
func (a *SlackAgent) UsesWorkspace() bool {
	return a.Spec.WorkspaceRef != nil && a.Spec.WorkspaceRef.Name != ""
}

// ResolvedWorkspaceRef returns the workspace name/namespace this agent binds
// to, defaulting the namespace to the agent's own. Returns empty values when
// the agent uses a dedicated app.
func (a *SlackAgent) ResolvedWorkspaceRef() (namespace, name string) {
	if !a.UsesWorkspace() {
		return "", ""
	}
	ns := a.Spec.WorkspaceRef.Namespace
	if ns == "" {
		ns = a.Namespace
	}
	return ns, a.Spec.WorkspaceRef.Name
}
