/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GitHubRepositorySpec defines the desired state of GitHubRepository.
type GitHubRepositorySpec struct {
	// githubTokenSecret is the name of the K8s Secret holding the GitHub API
	// token under the key "token". Exactly one of githubTokenSecret or
	// githubApp must be set.
	// +optional
	GitHubTokenSecret string `json:"githubTokenSecret,omitempty"`

	// githubApp configures GitHub App installation authentication. Exactly one
	// of githubTokenSecret or githubApp must be set.
	// +optional
	GitHubApp *GitHubAppAuth `json:"githubApp,omitempty"`

	// owner is the GitHub repository owner (user or organization).
	// +kubebuilder:validation:MinLength=1
	Owner string `json:"owner"`

	// repo is the GitHub repository name.
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// pollInterval is how often to poll GitHub for issues with mode labels.
	// +kubebuilder:default="60s"
	// +optional
	PollInterval metav1.Duration `json:"pollInterval,omitempty"`

	// webhookSecret is the name of the K8s Secret holding the GitHub webhook
	// secret under the key "secret". Used to validate X-Hub-Signature-256.
	// When empty, webhook validation is skipped (poll-only mode).
	// +optional
	WebhookSecret string `json:"webhookSecret,omitempty"`

	// triggerKeyword is the bot mention keyword for comment-based triggering.
	// When a user comments on an issue with this keyword, an AgentRun is created.
	// +kubebuilder:default="@agent"
	// +optional
	TriggerKeyword string `json:"triggerKeyword,omitempty"`

	// cancelRunsOnIssueClose, when true, closing/deleting an issue requests
	// graceful cancellation of its active runs.
	// +optional
	CancelRunsOnIssueClose bool `json:"cancelRunsOnIssueClose,omitempty"`

	// auth gates who can trigger AgentRun creation from this repository.
	// +optional
	Auth *TriggerAuth `json:"auth,omitempty"`

	// reviewLoop configures the autonomous PR review loop. When an agent-created
	// pull request is opened, a reviewer run critiques it; request-changes
	// verdicts wake the implementer run to resolve the feedback, and the cycle
	// repeats until approval or the round cap. Disabled by default; set
	// reviewLoop to opt in (and leave reviewLoop.disabled false).
	// +optional
	ReviewLoop *ReviewLoopSpec `json:"reviewLoop,omitempty"`

	// maintainer configures a standing supervisor AgentRun attached to this
	// repository. The maintainer triages the issue backlog, dispatches
	// implementer runs by applying ModeTemplate labels through the existing
	// trigger ingress, watches the dispatched fleet, and reports. Disabled by
	// default; set maintainer to opt in (and leave maintainer.disabled false).
	// +optional
	Maintainer *MaintainerSpec `json:"maintainer,omitempty"`

	// defaults holds the fields used when creating AgentRuns.
	Defaults AgentRunDefaults `json:"defaults"`
}

// MaintainerWorkItemCutoverMode controls the rollbackable waiter and delivery-authority migration.
// +kubebuilder:validation:Enum=Legacy;DualRead;Controller
type MaintainerWorkItemCutoverMode string

const (
	// MaintainerWorkItemCutoverLegacy keeps legacy waiter polling available during rollback.
	MaintainerWorkItemCutoverLegacy MaintainerWorkItemCutoverMode = "Legacy"
	// MaintainerWorkItemCutoverDualRead compares semantic work-item events with the legacy snapshot.
	MaintainerWorkItemCutoverDualRead MaintainerWorkItemCutoverMode = "DualRead"
	// MaintainerWorkItemCutoverController makes durable semantic work-item observations authoritative.
	MaintainerWorkItemCutoverController MaintainerWorkItemCutoverMode = "Controller"
)

// MaintainerSpec configures the standing maintainer run for a repository.
type MaintainerSpec struct {
	// disabled turns off the maintainer without removing its configuration.
	// +optional
	Disabled bool `json:"disabled,omitempty"`

	// modeRef overrides the ModeTemplate used by the maintainer run.
	// Defaults to the "maintainer" template when it exists.
	// +optional
	ModeRef *platformv1alpha1.ModeRef `json:"modeRef,omitempty"`

	// model overrides the model used by the maintainer run. Defaults to the
	// repository defaults' model.
	// +optional
	Model string `json:"model,omitempty"`

	// maxConcurrentDispatches caps how many maintainer-dispatched runs may be
	// active at once. Defaults to 2.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxConcurrentDispatches int32 `json:"maxConcurrentDispatches,omitempty"`

	// maxDispatchesPerDay caps maintainer dispatches per UTC day. Defaults to 10.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxDispatchesPerDay int32 `json:"maxDispatchesPerDay,omitempty"`

	// standupInterval is the periodic wake cadence for the maintainer when no
	// backlog or fleet events occur. Defaults to 12h.
	// +optional
	StandupInterval *metav1.Duration `json:"standupInterval,omitempty"`

	// allowPullRequestMerge permits the controller-executed RequestMerge command
	// to merge approved, non-draft pull requests in this repository. Disabled by
	// default; merging stays human otherwise.
	// +optional
	AllowPullRequestMerge bool `json:"allowPullRequestMerge,omitempty"`

	// workItemCutover selects the rollbackable maintainer waiter migration mode.
	// Legacy retains direct polling, DualRead compares legacy and semantic state,
	// and Controller uses only durable work-item issue observations/projections.
	// +kubebuilder:default=Controller
	// +optional
	WorkItemCutover MaintainerWorkItemCutoverMode `json:"workItemCutover,omitempty"`
}

// MaintainerStatus reports the observed state of the standing maintainer run.
type MaintainerStatus struct {
	// runName is the name of the standing maintainer AgentRun.
	// +optional
	RunName string `json:"runName,omitempty"`

	// lastWakeTime is when the maintainer was last woken by the engine.
	// +optional
	LastWakeTime *metav1.Time `json:"lastWakeTime,omitempty"`

	// dispatchesToday mirrors the maintainer's dispatch ledger for the current
	// UTC day.
	// +optional
	DispatchesToday int32 `json:"dispatchesToday,omitempty"`

	// lastReportTime is when the maintainer last submitted a report.
	// +optional
	LastReportTime *metav1.Time `json:"lastReportTime,omitempty"`

	// lastReportState is the state declared by the latest maintainer report.
	// +optional
	LastReportState string `json:"lastReportState,omitempty"`

	// lastReportSummary is the summary from the latest maintainer report.
	// +optional
	LastReportSummary string `json:"lastReportSummary,omitempty"`
}

// ReviewLoopSpec configures the autonomous PR review loop.
type ReviewLoopSpec struct {
	// disabled turns off automatic reviewer runs for agent-created PRs.
	// +optional
	Disabled bool `json:"disabled,omitempty"`

	// maxRounds caps automatic review/resolve cycles per pull request before
	// the loop blocks for human input. Defaults to 3.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxRounds int32 `json:"maxRounds,omitempty"`

	// reviewerModeRef overrides the ModeTemplate used for reviewer runs.
	// Defaults to the "review" template when it exists.
	// +optional
	ReviewerModeRef *platformv1alpha1.ModeRef `json:"reviewerModeRef,omitempty"`

	// reviewerDefaults optionally overrides the model, provider credentials,
	// runtime, tools, instructions, and policy references used by reviewer
	// runs. When omitted, reviewers inherit spec.defaults for compatibility.
	// Repository and GitHub authentication remain tied to this repository.
	// +optional
	ReviewerDefaults *AgentRunDefaults `json:"reviewerDefaults,omitempty"`
}

// GitHubAppAuth configures GitHub App installation token authentication.
type GitHubAppAuth struct {
	// appID is the GitHub App ID.
	// +kubebuilder:validation:Minimum=1
	AppID int64 `json:"appID"`

	// installationID is the GitHub App installation ID for this repository.
	// +kubebuilder:validation:Minimum=1
	InstallationID int64 `json:"installationID"`

	// privateKeySecret is the name of the K8s Secret holding the GitHub App
	// private key under the key "private-key.pem".
	// +kubebuilder:validation:MinLength=1
	PrivateKeySecret string `json:"privateKeySecret"`
}

// GitHubRepositoryStatus defines the observed state of GitHubRepository.
type GitHubRepositoryStatus struct {
	// lastPollTime is when GitHub was last polled successfully.
	// +optional
	LastPollTime *metav1.Time `json:"lastPollTime,omitempty"`

	// issuesProcessed is the cumulative number of issues that have been turned
	// into AgentRuns by this controller.
	// +optional
	IssuesProcessed int32 `json:"issuesProcessed,omitempty"`

	// lastError contains the error message from the most recent failed operation.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// processedIssueIDs contains recent issue IDs whose AgentRuns have not been
	// explicitly deleted, preventing duplicate runs while those records remain.
	// +listType=atomic
	// +optional
	ProcessedIssueIDs []string `json:"processedIssueIDs,omitempty"`

	// maintainer reports the observed state of the standing maintainer run.
	// +optional
	Maintainer *MaintainerStatus `json:"maintainer,omitempty"`

	// conditions represent the current state of the GitHubRepository.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for GitHubRepository.
const (
	ConditionGitHubRepositoryReady = "Ready"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="(has(self.spec.githubTokenSecret) && self.spec.githubTokenSecret != '') != has(self.spec.githubApp)",message="exactly one of spec.githubTokenSecret or spec.githubApp must be set"
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Repo",type=string,JSONPath=`.spec.repo`
// +kubebuilder:printcolumn:name="Processed",type=integer,JSONPath=`.status.issuesProcessed`
// +kubebuilder:printcolumn:name="LastPoll",type=date,JSONPath=`.status.lastPollTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GitHubRepository is the Schema for the githubrepositories API.
type GitHubRepository struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec GitHubRepositorySpec `json:"spec"`

	// +optional
	Status GitHubRepositoryStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GitHubRepositoryList contains a list of GitHubRepository.
type GitHubRepositoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GitHubRepository `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GitHubRepository{}, &GitHubRepositoryList{})
}

// PR review loop labels stamped on AgentRuns by the loop engine. Canonical
// here so packages on both sides of the trigger controllers (e.g. the GitHub
// App token minter/refresher) can identify reviewer runs without import
// cycles.
const (
	// PRLoopRoleLabelKey marks reviewer AgentRuns created by the loop.
	PRLoopRoleLabelKey = "triggers.gratefulagents.dev/pr-loop-role"
	// PRLoopRoleReviewerValue is the PRLoopRoleLabelKey value for reviewer runs.
	PRLoopRoleReviewerValue = "reviewer"
)

// Maintainer annotations recorded on the standing maintainer AgentRun and
// consumed by the maintainer engine. Canonical here so both the trigger
// controllers and the agent-side tools can share them without import cycles.
const (
	// MaintainerReportAnnotation carries the latest maintainer report as JSON
	// {"summary": string, "state": string, "time": RFC3339}.
	MaintainerReportAnnotation = "triggers.gratefulagents.dev/maintainer-report"
	// MaintainerDispatchLedgerAnnotation carries the maintainer's dispatch
	// audit ledger as JSON {"day": "YYYY-MM-DD", "count": int, "issues": [int]}.
	MaintainerDispatchLedgerAnnotation = "triggers.gratefulagents.dev/maintainer-dispatches"
)

// Maintainer report states stored in MaintainerReportAnnotation.
const (
	MaintainerReportStateHealthy   = "healthy"
	MaintainerReportStateAttention = "needs_attention"
	MaintainerReportStateBlocked   = "blocked"
)
