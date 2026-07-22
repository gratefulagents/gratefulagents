/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PullRequestMonitorState is the review-loop state associated with a monitored pull request.
// +kubebuilder:validation:Enum=pending;open;resolving;approved;blocked;merged;closed;cancelled;inactive
type PullRequestMonitorState string

const (
	PullRequestMonitorStatePending   PullRequestMonitorState = "pending"
	PullRequestMonitorStateOpen      PullRequestMonitorState = "open"
	PullRequestMonitorStateResolving PullRequestMonitorState = "resolving"
	PullRequestMonitorStateApproved  PullRequestMonitorState = "approved"
	PullRequestMonitorStateBlocked   PullRequestMonitorState = "blocked"
	PullRequestMonitorStateMerged    PullRequestMonitorState = "merged"
	PullRequestMonitorStateClosed    PullRequestMonitorState = "closed"
	PullRequestMonitorStateCancelled PullRequestMonitorState = "cancelled"
	PullRequestMonitorStateInactive  PullRequestMonitorState = "inactive"

	ConditionPullRequestMonitorReady = "Ready"
)

// GitHubObjectCursor identifies the latest processed GitHub object.
type GitHubObjectCursor struct {
	Timestamp metav1.Time `json:"timestamp"`
	ID        int64       `json:"id"`
}

// PullRequestMonitorETags holds conditional-request validators that are safe to
// reuse without interfering with paginated event processing.
type PullRequestMonitorETags struct {
	// +optional
	Pull string `json:"pull,omitempty"`
}

// PullRequestLifecycle is the GitHub lifecycle independently observed for a pull request.
// +kubebuilder:validation:Enum=open;draft;merged;closed
type PullRequestLifecycle string

const (
	PullRequestLifecycleOpen   PullRequestLifecycle = "open"
	PullRequestLifecycleDraft  PullRequestLifecycle = "draft"
	PullRequestLifecycleMerged PullRequestLifecycle = "merged"
	PullRequestLifecycleClosed PullRequestLifecycle = "closed"
)

// PullRequestMergeability is GitHub's current mergeability result.
// +kubebuilder:validation:Enum=unknown;mergeable;conflicting
type PullRequestMergeability string

const (
	PullRequestMergeabilityUnknown     PullRequestMergeability = "unknown"
	PullRequestMergeabilityMergeable   PullRequestMergeability = "mergeable"
	PullRequestMergeabilityConflicting PullRequestMergeability = "conflicting"
)

// PullRequestReviewDecision is the aggregate latest review result.
// +kubebuilder:validation:Enum=unknown;approved;changes_requested;review_required
type PullRequestReviewDecision string

const (
	PullRequestReviewDecisionUnknown          PullRequestReviewDecision = "unknown"
	PullRequestReviewDecisionApproved         PullRequestReviewDecision = "approved"
	PullRequestReviewDecisionChangesRequested PullRequestReviewDecision = "changes_requested"
	PullRequestReviewDecisionReviewRequired   PullRequestReviewDecision = "review_required"
)

// PullRequestMonitorHeadRollup is a check or commit-status result bound to one head SHA.
type PullRequestMonitorHeadRollup struct {
	HeadSHA    string      `json:"headSHA,omitempty"`
	State      string      `json:"state,omitempty"`
	Count      int32       `json:"count,omitempty"`
	ObservedAt metav1.Time `json:"observedAt,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// PullRequestMonitorSpec defines the immutable identity of a pull request monitor.
// +kubebuilder:validation:XValidation:rule="size(self.implementerRef.name) > 0",message="implementerRef.name is required"
type PullRequestMonitorSpec struct {
	ImplementerRef corev1.LocalObjectReference `json:"implementerRef"`
	// +kubebuilder:validation:Pattern=`^[^/[:space:]]+/[^/[:space:]]+$`
	Repository string `json:"repository"`
	// +kubebuilder:validation:Minimum=1
	Number int32 `json:"number"`
	// +kubebuilder:validation:Pattern=`^https://github\.com/[^/]+/[^/]+/pull/[1-9][0-9]*$`
	URL string `json:"url"`

	// +optional
	GitHubRepositoryRef *corev1.LocalObjectReference `json:"githubRepositoryRef,omitempty"`

	DiscoveredAt metav1.Time `json:"discoveredAt"`
}

// PullRequestMonitorStatus defines the observed state of a pull request monitor.
type PullRequestMonitorStatus struct {
	// +optional
	State PullRequestMonitorState `json:"state,omitempty"`
	// +optional
	OpenedDispatched bool `json:"openedDispatched,omitempty"`
	// +optional
	Title string `json:"title,omitempty"`
	// +optional
	HeadRef string `json:"headRef,omitempty"`
	// +optional
	HeadSHA string `json:"headSHA,omitempty"`
	// +optional
	BaseRef string `json:"baseRef,omitempty"`
	// +optional
	AuthorLogin string `json:"authorLogin,omitempty"`
	// +optional
	Lifecycle PullRequestLifecycle `json:"lifecycle,omitempty"`
	// +optional
	MergedAt metav1.Time `json:"mergedAt,omitempty"`
	// +optional
	Mergeability PullRequestMergeability `json:"mergeability,omitempty"`
	// +optional
	ReviewDecision PullRequestReviewDecision `json:"reviewDecision,omitempty"`
	// +optional
	PullObservedAt metav1.Time `json:"pullObservedAt,omitempty"`
	// +optional
	PullError string `json:"pullError,omitempty"`
	// +optional
	ReviewsObservedAt metav1.Time `json:"reviewsObservedAt,omitempty"`
	// +optional
	ReviewsError string `json:"reviewsError,omitempty"`
	// +optional
	CommentsObservedAt metav1.Time `json:"commentsObservedAt,omitempty"`
	// +optional
	CommentsError string `json:"commentsError,omitempty"`
	// +optional
	Checks PullRequestMonitorHeadRollup `json:"checks,omitempty"`
	// +optional
	Statuses PullRequestMonitorHeadRollup `json:"statuses,omitempty"`
	// +optional
	LastPollTime *metav1.Time `json:"lastPollTime,omitempty"`
	// +optional
	LastReviewCursor *GitHubObjectCursor `json:"lastReviewCursor,omitempty"`
	// +optional
	LastIssueCommentCursor *GitHubObjectCursor `json:"lastIssueCommentCursor,omitempty"`
	// +optional
	ETags PullRequestMonitorETags `json:"etags,omitempty"`
	// +optional
	LastError string `json:"lastError,omitempty"`
	// +optional
	ConsecutiveErrors int32 `json:"consecutiveErrors,omitempty"`
	// +optional
	RetryAfter *metav1.Time `json:"retryAfter,omitempty"`
	// +optional
	RateLimitRemaining int32 `json:"rateLimitRemaining,omitempty"`
	// +optional
	RateLimitReset *metav1.Time `json:"rateLimitReset,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="spec is immutable"
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repository`
// +kubebuilder:printcolumn:name="PR",type=integer,JSONPath=`.spec.number`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="LastPoll",type=date,JSONPath=`.status.lastPollTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PullRequestMonitor is restart-safe polling state for an AgentRun-owned pull request.
type PullRequestMonitor struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec PullRequestMonitorSpec `json:"spec"`

	// +optional
	Status PullRequestMonitorStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PullRequestMonitorList contains a list of PullRequestMonitor.
type PullRequestMonitorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PullRequestMonitor `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PullRequestMonitor{}, &PullRequestMonitorList{})
}
