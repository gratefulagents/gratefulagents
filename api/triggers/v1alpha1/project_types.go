/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProjectSpec defines the desired state of Project.
type ProjectSpec struct {
	// displayName is a human-readable name for this project.
	// +kubebuilder:validation:MinLength=1
	DisplayName string `json:"displayName"`

	// auth gates who can trigger AgentRun creation from this project.
	// +optional
	Auth *TriggerAuth `json:"auth,omitempty"`

	// reviewLoop configures autonomous PR reviews for runs created from this
	// project. The policy is copied onto each run so it also applies to pull
	// requests opened in additional repositories. The loop is disabled when
	// this field is omitted; set disabled to false to opt in.
	// +optional
	ReviewLoop *ProjectReviewLoopSpec `json:"reviewLoop,omitempty"`

	// kubernetesAdmin grants dashboard runs created from this project
	// cluster-admin RBAC plus read-only platform introspection tools. This is
	// admin-gated by the dashboard API. Trigger CRDs grant the same to their
	// runs via the kubectl-only defaults.kubernetesAdmin field.
	// +optional
	KubernetesAdmin bool `json:"kubernetesAdmin,omitempty"`

	// defaults holds the fields used when creating AgentRuns from this project.
	Defaults AgentRunDefaults `json:"defaults"`

	// triggers declares the named external entry points for this project.
	// +listType=map
	// +listMapKey=name
	// +optional
	Triggers []ProjectTrigger `json:"triggers,omitempty"`
}

// ProjectReviewLoopSpec configures autonomous PR reviews for Project-created
// runs. It is intentionally project-wide: one run may open PRs in any of its
// configured repositories.
type ProjectReviewLoopSpec struct {
	// disabled turns off automatic reviewer runs for PRs opened by this
	// project's runs.
	// +optional
	Disabled bool `json:"disabled,omitempty"`
}

// ConnectionRef references a Connection in the same namespace.
type ConnectionRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ProjectTriggerType identifies a source that can create AgentRuns.
type ProjectTriggerType string

const (
	ProjectTriggerTypeGitHub ProjectTriggerType = "github"
	ProjectTriggerTypeSlack  ProjectTriggerType = "slack"
	ProjectTriggerTypeCron   ProjectTriggerType = "cron"
	ProjectTriggerTypeLinear ProjectTriggerType = "linear"
)

// GitHubProjectTriggerConfig configures GitHub issue and comment ingress.
type GitHubProjectTriggerConfig struct {
	ConnectionRef ConnectionRef `json:"connectionRef"`

	// +kubebuilder:validation:MinLength=1
	Owner string `json:"owner"`

	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// issues enables issue events.
	// +optional
	Issues bool `json:"issues,omitempty"`

	// comments enables issue-comment events.
	// +optional
	Comments bool `json:"comments,omitempty"`

	// triggerKeyword is the bot mention keyword for comment-based triggering.
	// +kubebuilder:default="@agent"
	// +optional
	TriggerKeyword string `json:"triggerKeyword,omitempty"`

	// pollInterval is how often to poll GitHub for events.
	// +kubebuilder:default="60s"
	// +optional
	PollInterval metav1.Duration `json:"pollInterval,omitempty"`

	// auth restricts the GitHub actors that may create runs through this trigger.
	// +optional
	Auth *TriggerAuth `json:"auth,omitempty"`

	// maintainer configures a standing supervisor for this repository trigger.
	// The configuration is copied to the generated GitHubRepository child.
	// +optional
	Maintainer *MaintainerSpec `json:"maintainer,omitempty"`
}

// SlackProjectTriggerConfig configures Slack channel ingress.
type SlackProjectTriggerConfig struct {
	ConnectionRef ConnectionRef `json:"connectionRef"`

	// channel is the Slack conversation ID (C…/G…/D…) this trigger is scoped
	// to. Set it to an empty string to respond in any conversation the bot is
	// invited to. It is always serialized for compatibility with existing CRDs.
	// +optional
	Channel string `json:"channel"`

	// channelReplyMode controls whether channel replies need approval.
	// +kubebuilder:validation:Enum=require-approval;auto
	// +kubebuilder:default="require-approval"
	// +optional
	ChannelReplyMode SlackChannelReplyMode `json:"channelReplyMode,omitempty"`

	// commanders lists additional Slack users allowed to command this trigger.
	// +listType=set
	// +optional
	Commanders []string `json:"commanders,omitempty"`

	// sessionIdleMinutes controls how long a Slack conversation reuses its run.
	// +optional
	SessionIdleMinutes *int32 `json:"sessionIdleMinutes,omitempty"`
}

// CronProjectTriggerConfig configures scheduled ingress.
type CronProjectTriggerConfig struct {
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`

	// +kubebuilder:default="UTC"
	// +optional
	TimeZone string `json:"timeZone,omitempty"`

	// +kubebuilder:validation:Enum=Allow;Forbid
	// +kubebuilder:default="Forbid"
	// +optional
	ConcurrencyPolicy CronConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	// +kubebuilder:validation:MinLength=1
	Prompt string `json:"prompt"`
}

// LinearProjectTriggerConfig configures Linear issue ingress.
type LinearProjectTriggerConfig struct {
	ConnectionRef ConnectionRef `json:"connectionRef"`

	// +kubebuilder:validation:MinLength=1
	ProjectID string `json:"projectId"`

	// +kubebuilder:validation:MinLength=1
	TeamID string `json:"teamId"`

	// +kubebuilder:default="ai-approved"
	// +optional
	ApprovedLabel string `json:"approvedLabel,omitempty"`

	// +kubebuilder:default="30s"
	// +optional
	PollInterval metav1.Duration `json:"pollInterval,omitempty"`

	// autoCreate controls whether approved Linear issues automatically create runs.
	// +optional
	AutoCreate bool `json:"autoCreate,omitempty"`
}

// ProjectTrigger is a named, source-specific Project entry point.
// +kubebuilder:validation:XValidation:rule="self.name != 'manual'",message="name 'manual' is reserved"
// +kubebuilder:validation:XValidation:rule="(self.type == 'github' && has(self.github) && !has(self.slack) && !has(self.cron) && !has(self.linear)) || (self.type == 'slack' && !has(self.github) && has(self.slack) && !has(self.cron) && !has(self.linear)) || (self.type == 'cron' && !has(self.github) && !has(self.slack) && has(self.cron) && !has(self.linear)) || (self.type == 'linear' && !has(self.github) && !has(self.slack) && !has(self.cron) && has(self.linear))",message="exactly one configuration matching type must be set"
type ProjectTrigger struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// +kubebuilder:validation:Enum=github;slack;cron;linear
	Type ProjectTriggerType `json:"type"`

	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// +optional
	GitHub *GitHubProjectTriggerConfig `json:"github,omitempty"`

	// +optional
	Slack *SlackProjectTriggerConfig `json:"slack,omitempty"`

	// +optional
	Cron *CronProjectTriggerConfig `json:"cron,omitempty"`

	// +optional
	Linear *LinearProjectTriggerConfig `json:"linear,omitempty"`
}

// ProjectTriggerStatus reports the normalized observed state of a trigger.
type ProjectTriggerStatus struct {
	Name string             `json:"name"`
	Type ProjectTriggerType `json:"type"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	LastActivityTime *metav1.Time `json:"lastActivityTime,omitempty"`

	// +optional
	NextActivityTime *metav1.Time `json:"nextActivityTime,omitempty"`

	// +optional
	LastError string `json:"lastError,omitempty"`

	// maintainer mirrors the generated GitHubRepository child's standing
	// maintainer status (github triggers with a configured maintainer only).
	// +optional
	Maintainer *MaintainerStatus `json:"maintainer,omitempty"`
}

// ProjectStatus defines the observed state of Project.
type ProjectStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +listType=map
	// +listMapKey=name
	// +optional
	Triggers []ProjectTriggerStatus `json:"triggers,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="DisplayName",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Project is the Schema for the projects API. It holds defaults and source
// configuration for AgentRuns created through the dashboard and triggers.
type Project struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec ProjectSpec `json:"spec"`

	// +optional
	Status ProjectStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ProjectList contains a list of Project.
type ProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Project `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Project{}, &ProjectList{})
}

// ProjectGeneratedChildName returns the deterministic name of the child
// resource the Project controller generates for one trigger (GitHubRepository,
// SlackAgent, Cron, or LinearProject).
func ProjectGeneratedChildName(projectName, triggerName string) string {
	return projectGeneratedDNSName("project-" + projectName + "-" + triggerName)
}

// projectGeneratedDNSName lowercases and squashes the input into a DNS-1123
// compatible resource name capped at 63 characters, disambiguating truncated
// names with a content hash. Must stay in sync with nothing: the Project
// controller delegates here.
func projectGeneratedDNSName(value string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "project"
	}
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(value))
	suffix := hex.EncodeToString(sum[:])[:8]
	return strings.TrimRight(name[:63-len(suffix)-1], "-") + "-" + suffix
}
