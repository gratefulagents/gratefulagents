/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// MaintainerWorkItemRepositoryLabelKey identifies the repository of a work item.
	MaintainerWorkItemRepositoryLabelKey = "triggers.gratefulagents.dev/repository"
	// MaintainerWorkItemIssueNumberLabelKey identifies the issue number of a work item.
	MaintainerWorkItemIssueNumberLabelKey = "triggers.gratefulagents.dev/issue-number"
	// MaintainerWorkItemNameLabelKey identifies a work item by its Kubernetes name.
	MaintainerWorkItemNameLabelKey = "triggers.gratefulagents.dev/maintainer-work-item-name"
	// MaintainerWorkItemUIDLabelKey identifies a work item by its immutable Kubernetes UID.
	MaintainerWorkItemUIDLabelKey = "triggers.gratefulagents.dev/maintainer-work-item-uid"
	// MaintainerDispatchReservationsAnnotation is the repository-scoped atomic capacity ledger.
	MaintainerDispatchReservationsAnnotation = "triggers.gratefulagents.dev/maintainer-dispatch-reservations"
	// MaintainerCommandLockAnnotation serializes cross-work-item graph mutations.
	MaintainerCommandLockAnnotation = "triggers.gratefulagents.dev/maintainer-command-lock"
	// MaintainerCommandFailureCountAnnotation counts failed processing attempts
	// so persistently failing commands are terminally rejected instead of being
	// retried on every reconcile forever.
	MaintainerCommandFailureCountAnnotation = "triggers.gratefulagents.dev/maintainer-command-failures"
	// MaintainerCommandCapabilitySecretKey is the data key holding the per-run HMAC capability.
	MaintainerCommandCapabilitySecretKey = "key"
	// MaintainerCommandCapabilityRepositoryNameKey binds a capability to one repository name.
	MaintainerCommandCapabilityRepositoryNameKey = "repository-name"
	// MaintainerCommandCapabilityRepositoryUIDKey binds a capability to one immutable repository UID.
	MaintainerCommandCapabilityRepositoryUIDKey = "repository-uid"

	// ConditionMaintainerWorkItemObservationFresh reports whether the issue observation is current.
	ConditionMaintainerWorkItemObservationFresh = "ObservationFresh"
	// ConditionMaintainerWorkItemCommandAccepted reports whether a command was accepted.
	ConditionMaintainerWorkItemCommandAccepted = "CommandAccepted"
	// ConditionMaintainerWorkItemDependenciesReady reports whether every dependency is delivered.
	ConditionMaintainerWorkItemDependenciesReady = "DependenciesReady"
	// ConditionMaintainerWorkItemReadyToMerge reports the fail-closed delivery predicate.
	ConditionMaintainerWorkItemReadyToMerge = "ReadyToMerge"
)

// MaintainerWorkItemDisposition is the accepted triage disposition for an issue.
// +kubebuilder:validation:Enum=NotActionable;Bounded;Decomposable;Discovery;Escalated
type MaintainerWorkItemDisposition string

const (
	// MaintainerWorkItemDispositionNotActionable indicates the issue should not be acted on.
	MaintainerWorkItemDispositionNotActionable MaintainerWorkItemDisposition = "NotActionable"
	// MaintainerWorkItemDispositionBounded indicates the issue has bounded implementation scope.
	MaintainerWorkItemDispositionBounded MaintainerWorkItemDisposition = "Bounded"
	// MaintainerWorkItemDispositionDecomposable indicates the issue should be split into smaller work.
	MaintainerWorkItemDispositionDecomposable MaintainerWorkItemDisposition = "Decomposable"
	// MaintainerWorkItemDispositionDiscovery indicates more discovery is needed before implementation.
	MaintainerWorkItemDispositionDiscovery MaintainerWorkItemDisposition = "Discovery"
	// MaintainerWorkItemDispositionEscalated indicates the issue requires maintainer escalation.
	MaintainerWorkItemDispositionEscalated MaintainerWorkItemDisposition = "Escalated"
)

// MaintainerWorkItemCloseReason is the GitHub close reason selected by triage.
// +kubebuilder:validation:Enum=not_planned;completed
type MaintainerWorkItemCloseReason string

const (
	// MaintainerWorkItemCloseReasonNotPlanned closes an issue as not planned.
	MaintainerWorkItemCloseReasonNotPlanned MaintainerWorkItemCloseReason = "not_planned"
	// MaintainerWorkItemCloseReasonCompleted closes an issue as completed.
	MaintainerWorkItemCloseReasonCompleted MaintainerWorkItemCloseReason = "completed"
)

// MaintainerIssueState is the observed GitHub issue state.
// +kubebuilder:validation:Enum=open;closed
type MaintainerIssueState string

const (
	// MaintainerIssueStateOpen indicates an open GitHub issue.
	MaintainerIssueStateOpen MaintainerIssueState = "open"
	// MaintainerIssueStateClosed indicates a closed GitHub issue.
	MaintainerIssueStateClosed MaintainerIssueState = "closed"
)

// MaintainerWorkItemPhase is the lifecycle phase of a work item.
// +kubebuilder:validation:Enum=PendingTriage;Triaged;AwaitingDecision;ReadyToDispatch;Dispatched;Implementing;ReadyToMerge;Delivered
type MaintainerWorkItemPhase string

const (
	// MaintainerWorkItemPhasePendingTriage indicates that the issue awaits triage.
	MaintainerWorkItemPhasePendingTriage MaintainerWorkItemPhase = "PendingTriage"
	// MaintainerWorkItemPhaseTriaged indicates that accepted triage intent is recorded.
	MaintainerWorkItemPhaseTriaged MaintainerWorkItemPhase = "Triaged"
	// MaintainerWorkItemPhaseAwaitingDecision indicates that an authenticated human answer is required.
	MaintainerWorkItemPhaseAwaitingDecision MaintainerWorkItemPhase = "AwaitingDecision"
	// MaintainerWorkItemPhaseReadyToDispatch indicates that dependencies permit implementation dispatch.
	MaintainerWorkItemPhaseReadyToDispatch MaintainerWorkItemPhase = "ReadyToDispatch"
	// MaintainerWorkItemPhaseDispatched indicates that implementation dispatch has been accepted.
	MaintainerWorkItemPhaseDispatched MaintainerWorkItemPhase = "Dispatched"
	// MaintainerWorkItemPhaseImplementing indicates that an implementer is active.
	MaintainerWorkItemPhaseImplementing MaintainerWorkItemPhase = "Implementing"
	// MaintainerWorkItemPhaseReadyToMerge indicates that the required pull requests are ready for merge.
	MaintainerWorkItemPhaseReadyToMerge MaintainerWorkItemPhase = "ReadyToMerge"
	// MaintainerWorkItemPhaseDelivered indicates that the work item's delivery predicate is satisfied.
	MaintainerWorkItemPhaseDelivered MaintainerWorkItemPhase = "Delivered"
)

// MaintainerAcceptedScope records the scope accepted by triage.
type MaintainerAcceptedScope struct {
	// +optional
	Statement string `json:"statement,omitempty"`
	// +listType=atomic
	// +optional
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
}

// MaintainerWorkItemReference identifies a work item in the same namespace.
type MaintainerWorkItemReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	UID types.UID `json:"uid,omitempty"`
}

// MaintainerRequiredPullRequestIntent identifies a pull request required for delivery.
type MaintainerRequiredPullRequestIntent struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// MaintainerWorkItemSpec defines the stable issue identity and accepted triage intent.
// +kubebuilder:validation:XValidation:rule="self.repositoryRef == oldSelf.repositoryRef && self.issueNumber == oldSelf.issueNumber",message="repositoryRef and issueNumber are immutable"
// +kubebuilder:validation:XValidation:rule="!has(self.disposition) || self.disposition != 'NotActionable' || has(self.closeReason)",message="closeReason is required when disposition is NotActionable"
// +kubebuilder:validation:XValidation:rule="!has(self.closeReason) || (has(self.disposition) && self.disposition == 'NotActionable')",message="closeReason is only valid when disposition is NotActionable"
type MaintainerWorkItemSpec struct {
	RepositoryRef corev1.LocalObjectReference `json:"repositoryRef"`
	// +kubebuilder:validation:Minimum=1
	IssueNumber int32 `json:"issueNumber"`

	// +optional
	Disposition MaintainerWorkItemDisposition `json:"disposition,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +optional
	EvidenceSummary string `json:"evidenceSummary,omitempty"`
	// +optional
	AcceptedScope *MaintainerAcceptedScope `json:"acceptedScope,omitempty"`
	// +optional
	CloseReason *MaintainerWorkItemCloseReason `json:"closeReason,omitempty"`
	// +optional
	TriagedByCommand *corev1.LocalObjectReference `json:"triagedByCommand,omitempty"`
	// +listType=map
	// +listMapKey=name
	// +optional
	Dependencies []MaintainerWorkItemReference `json:"dependencies,omitempty"`
	// +listType=map
	// +listMapKey=name
	// +optional
	Children []MaintainerWorkItemReference `json:"children,omitempty"`
	// +listType=map
	// +listMapKey=name
	// +optional
	RequiredPullRequests []MaintainerRequiredPullRequestIntent `json:"requiredPullRequests,omitempty"`
}

// MaintainerIssueObservation is a durable observation of a GitHub issue.
type MaintainerIssueObservation struct {
	Number      int32                `json:"number"`
	URL         string               `json:"url"`
	Title       string               `json:"title"`
	BodyHash    string               `json:"bodyHash"`
	AuthorLogin string               `json:"authorLogin"`
	State       MaintainerIssueState `json:"state"`
	// +listType=atomic
	Labels          []string    `json:"labels"`
	GitHubUpdatedAt metav1.Time `json:"githubUpdatedAt"`
	ObservedAt      metav1.Time `json:"observedAt"`
}

// MaintainerWorkItemAgentRunRole identifies the role of a projected AgentRun.
// +kubebuilder:validation:Enum=Implementer;Reviewer
type MaintainerWorkItemAgentRunRole string

const (
	MaintainerWorkItemAgentRunRoleImplementer MaintainerWorkItemAgentRunRole = "Implementer"
	MaintainerWorkItemAgentRunRoleReviewer    MaintainerWorkItemAgentRunRole = "Reviewer"
)

// MaintainerWorkItemPullRequestState is the observed GitHub lifecycle state of a pull request.
// +kubebuilder:validation:Enum=open;closed;merged
type MaintainerWorkItemPullRequestState string

const (
	MaintainerWorkItemPullRequestStateOpen   MaintainerWorkItemPullRequestState = "open"
	MaintainerWorkItemPullRequestStateClosed MaintainerWorkItemPullRequestState = "closed"
	MaintainerWorkItemPullRequestStateMerged MaintainerWorkItemPullRequestState = "merged"
)

// MaintainerWorkItemCheckState summarizes CI and commit-status observations for an exact head.
// +kubebuilder:validation:Enum=Unknown;Pending;Passing;Failing
type MaintainerWorkItemCheckState string

const (
	MaintainerWorkItemCheckStateUnknown MaintainerWorkItemCheckState = "Unknown"
	MaintainerWorkItemCheckStatePending MaintainerWorkItemCheckState = "Pending"
	MaintainerWorkItemCheckStatePassing MaintainerWorkItemCheckState = "Passing"
	MaintainerWorkItemCheckStateFailing MaintainerWorkItemCheckState = "Failing"
)

// MaintainerWorkItemChildProjection summarizes a child work item.
type MaintainerWorkItemChildProjection struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	UID types.UID `json:"uid,omitempty"`
	// +optional
	Phase MaintainerWorkItemPhase `json:"phase,omitempty"`
	// +optional
	Delivered bool `json:"delivered,omitempty"`
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
}

// MaintainerWorkItemDependencyProjection summarizes a dependency work item.
type MaintainerWorkItemDependencyProjection struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	UID types.UID `json:"uid,omitempty"`
	// +optional
	Phase MaintainerWorkItemPhase `json:"phase,omitempty"`
	// +optional
	Delivered bool `json:"delivered,omitempty"`
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
}

// MaintainerWorkItemAgentRunProjection summarizes an AgentRun associated through work-item identity labels.
type MaintainerWorkItemAgentRunProjection struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	UID  types.UID                      `json:"uid,omitempty"`
	Role MaintainerWorkItemAgentRunRole `json:"role"`
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	PRLoopState string `json:"prLoopState,omitempty"`
	// +optional
	PRLoopRound int32 `json:"prLoopRound,omitempty"`
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
}

// MaintainerWorkItemPullRequestProjection summarizes the monitor facts required for readiness.
type MaintainerWorkItemPullRequestProjection struct {
	// IntentName is the stable required-PR key (normally the monitor name).
	// +kubebuilder:validation:MinLength=1
	IntentName string `json:"intentName"`
	// +kubebuilder:validation:Pattern=`^[^/[:space:]]+/[^/[:space:]]+$`
	// +optional
	Repository string `json:"repository,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	Number int32 `json:"number,omitempty"`
	// +optional
	MonitorRef *corev1.LocalObjectReference `json:"monitorRef,omitempty"`
	// +optional
	URL string `json:"url,omitempty"`
	// +optional
	HeadSHA string `json:"headSHA,omitempty"`
	// +optional
	State MaintainerWorkItemPullRequestState `json:"state,omitempty"`
	// +optional
	Draft bool `json:"draft,omitempty"`
	// +optional
	MergedAt *metav1.Time `json:"mergedAt,omitempty"`
	// +optional
	Mergeable *bool `json:"mergeable,omitempty"`
	// +optional
	ReviewDecision string `json:"reviewDecision,omitempty"`
	// +optional
	CheckState MaintainerWorkItemCheckState `json:"checkState,omitempty"`
	// +optional
	HeadObservedAt *metav1.Time `json:"headObservedAt,omitempty"`
	// +optional
	ReviewObservedAt *metav1.Time `json:"reviewObservedAt,omitempty"`
	// +optional
	ChecksObservedAt *metav1.Time `json:"checksObservedAt,omitempty"`
	// +optional
	StatusesObservedAt *metav1.Time `json:"statusesObservedAt,omitempty"`
	// Fresh is true only when every authoritative monitor source is complete for HeadSHA.
	// +optional
	Fresh bool `json:"fresh,omitempty"`
	// +optional
	ObservationError string `json:"observationError,omitempty"`
}

// MaintainerWorkItemReadiness records controller-computed dispatch and merge readiness.
type MaintainerWorkItemReadiness struct {
	// +optional
	ReadyToDispatch bool `json:"readyToDispatch,omitempty"`
	// +optional
	ReadyToMerge bool `json:"readyToMerge,omitempty"`
	// +listType=atomic
	// +optional
	UnmetRequirements []string `json:"unmetRequirements,omitempty"`
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
}

// MaintainerResolvedDecision audits the authenticated answer that cleared pendingDecision.
type MaintainerResolvedDecision struct {
	ID                string                      `json:"id"`
	HumanSubject      string                      `json:"humanSubject"`
	Answer            string                      `json:"answer"`
	ResolvedAt        metav1.Time                 `json:"resolvedAt"`
	ResolvedByCommand corev1.LocalObjectReference `json:"resolvedByCommand"`
}

// MaintainerPendingDecision records an unresolved authorized decision request.
type MaintainerPendingDecision struct {
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`
	// +kubebuilder:validation:MinLength=1
	Question string `json:"question"`
	// +listType=atomic
	// +optional
	Options     []string    `json:"options,omitempty"`
	RequestedAt metav1.Time `json:"requestedAt"`
	// +optional
	RequestedByCommand *corev1.LocalObjectReference `json:"requestedByCommand,omitempty"`
}

// MaintainerDispatchReservation records capacity reserved before dispatch side effects.
type MaintainerDispatchReservation struct {
	// +kubebuilder:validation:MinLength=1
	ID         string                      `json:"id"`
	CommandRef corev1.LocalObjectReference `json:"commandRef"`
	ReservedAt metav1.Time                 `json:"reservedAt"`
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
	// +optional
	AgentRunRef *corev1.LocalObjectReference `json:"agentRunRef,omitempty"`
}

// MaintainerVerifiedPullRequestMerge records a controller-verified pull request merge.
type MaintainerVerifiedPullRequestMerge struct {
	// +kubebuilder:validation:Pattern=`^[^/[:space:]]+/[^/[:space:]]+$`
	Repository string `json:"repository"`
	// +kubebuilder:validation:Minimum=1
	PullRequestNumber int32 `json:"pullRequestNumber"`
	// +kubebuilder:validation:MinLength=1
	HeadSHA    string                      `json:"headSHA"`
	MergedAt   metav1.Time                 `json:"mergedAt"`
	CommandRef corev1.LocalObjectReference `json:"commandRef"`
}

// MaintainerDeliveryAttestation records authenticated semantic delivery evidence and finalization side effects.
type MaintainerDeliveryAttestation struct {
	Issuer MaintainerWorkItemCommandIssuer `json:"issuer"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	AcceptedScopeHash string `json:"acceptedScopeHash"`
	// +kubebuilder:validation:MinLength=1
	DeliverySummary string `json:"deliverySummary"`
	// +kubebuilder:validation:MinLength=1
	DeliveryEvidence string `json:"deliveryEvidence"`
	// +listType=map
	// +listMapKey=name
	// +optional
	RunSuccessRequestedRefs []MaintainerWorkItemReference `json:"runSuccessRequestedRefs,omitempty"`
	// +optional
	RunSuccessRequestedAt *metav1.Time `json:"runSuccessRequestedAt,omitempty"`
	// +optional
	IssueClosedAt *metav1.Time `json:"issueClosedAt,omitempty"`
	// +optional
	CompletedAt        *metav1.Time                `json:"completedAt,omitempty"`
	FinalizedByCommand corev1.LocalObjectReference `json:"finalizedByCommand"`
}

// MaintainerWorkItemCommandObservation projects the latest durable command receipt into waiter-v2.
type MaintainerWorkItemCommandObservation struct {
	Name    string                         `json:"name"`
	Type    MaintainerWorkItemCommandType  `json:"type"`
	Phase   MaintainerWorkItemCommandPhase `json:"phase"`
	Applied bool                           `json:"applied,omitempty"`
	// +optional
	Message    string      `json:"message,omitempty"`
	ObservedAt metav1.Time `json:"observedAt"`
}

// MaintainerWorkItemStatus defines the observed issue state and execution progress.
type MaintainerWorkItemStatus struct {
	// +optional
	Phase MaintainerWorkItemPhase `json:"phase,omitempty"`
	// +optional
	IssueObservation *MaintainerIssueObservation `json:"issueObservation,omitempty"`
	// +listType=map
	// +listMapKey=name
	// +optional
	Children []MaintainerWorkItemChildProjection `json:"children,omitempty"`
	// +listType=map
	// +listMapKey=name
	// +optional
	Dependencies []MaintainerWorkItemDependencyProjection `json:"dependencies,omitempty"`
	// +listType=map
	// +listMapKey=name
	// +optional
	AgentRuns []MaintainerWorkItemAgentRunProjection `json:"agentRuns,omitempty"`
	// +listType=map
	// +listMapKey=intentName
	// +optional
	PullRequests []MaintainerWorkItemPullRequestProjection `json:"pullRequests,omitempty"`
	// +listType=map
	// +listMapKey=repository
	// +listMapKey=pullRequestNumber
	// +optional
	VerifiedMerges []MaintainerVerifiedPullRequestMerge `json:"verifiedMerges,omitempty"`
	// +optional
	DeliveryAttestation *MaintainerDeliveryAttestation `json:"deliveryAttestation,omitempty"`
	// +optional
	LatestCommand *MaintainerWorkItemCommandObservation `json:"latestCommand,omitempty"`
	// +optional
	Readiness *MaintainerWorkItemReadiness `json:"readiness,omitempty"`
	// +optional
	PendingDecision *MaintainerPendingDecision `json:"pendingDecision,omitempty"`
	// +optional
	ResolvedDecision *MaintainerResolvedDecision `json:"resolvedDecision,omitempty"`
	// +optional
	DispatchReservation *MaintainerDispatchReservation `json:"dispatchReservation,omitempty"`
	// +optional
	ProjectionSequence int64 `json:"projectionSequence,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repositoryRef.name`
// +kubebuilder:printcolumn:name="Issue",type=integer,JSONPath=`.spec.issueNumber`
// +kubebuilder:printcolumn:name="Disposition",type=string,JSONPath=`.spec.disposition`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MaintainerWorkItem is durable triage state for a repository issue.
type MaintainerWorkItem struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec MaintainerWorkItemSpec `json:"spec"`

	// +optional
	Status MaintainerWorkItemStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MaintainerWorkItemList contains a list of MaintainerWorkItem.
type MaintainerWorkItemList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MaintainerWorkItem `json:"items"`
}

// MaintainerWorkItemCommandType identifies the command payload type.
// +kubebuilder:validation:Enum=TriageIssue;BreakdownIssue;RequestDecision;ResolveDecision;DispatchWorkItem;RequestMerge;FinalizeWorkItem
type MaintainerWorkItemCommandType string

const (
	// MaintainerWorkItemCommandTypeTriageIssue applies triage intent to an issue.
	MaintainerWorkItemCommandTypeTriageIssue MaintainerWorkItemCommandType = "TriageIssue"
	// MaintainerWorkItemCommandTypeBreakdownIssue records child and dependency intent.
	MaintainerWorkItemCommandTypeBreakdownIssue MaintainerWorkItemCommandType = "BreakdownIssue"
	// MaintainerWorkItemCommandTypeRequestDecision records a question for a human maintainer.
	MaintainerWorkItemCommandTypeRequestDecision MaintainerWorkItemCommandType = "RequestDecision"
	// MaintainerWorkItemCommandTypeResolveDecision records an authenticated human answer.
	MaintainerWorkItemCommandTypeResolveDecision MaintainerWorkItemCommandType = "ResolveDecision"
	// MaintainerWorkItemCommandTypeDispatchWorkItem reserves capacity and dispatches implementation.
	MaintainerWorkItemCommandTypeDispatchWorkItem MaintainerWorkItemCommandType = "DispatchWorkItem"
	// MaintainerWorkItemCommandTypeRequestMerge requests a guarded merge of a ready pull request.
	MaintainerWorkItemCommandTypeRequestMerge MaintainerWorkItemCommandType = "RequestMerge"
	// MaintainerWorkItemCommandTypeFinalizeWorkItem requests final delivery side effects.
	MaintainerWorkItemCommandTypeFinalizeWorkItem MaintainerWorkItemCommandType = "FinalizeWorkItem"
)

// MaintainerWorkItemMergeMethod identifies the GitHub merge method.
// +kubebuilder:validation:Enum=squash;merge;rebase
type MaintainerWorkItemMergeMethod string

const (
	MaintainerWorkItemMergeMethodSquash MaintainerWorkItemMergeMethod = "squash"
	MaintainerWorkItemMergeMethodMerge  MaintainerWorkItemMergeMethod = "merge"
	MaintainerWorkItemMergeMethodRebase MaintainerWorkItemMergeMethod = "rebase"
)

// MaintainerWorkItemCommandPhase is the command receipt phase.
// +kubebuilder:validation:Enum=Pending;Accepted;Succeeded;Rejected;Failed
type MaintainerWorkItemCommandPhase string

const (
	// MaintainerWorkItemCommandPhasePending indicates that a command has not been processed.
	MaintainerWorkItemCommandPhasePending MaintainerWorkItemCommandPhase = "Pending"
	// MaintainerWorkItemCommandPhaseAccepted indicates that a command passed validation.
	MaintainerWorkItemCommandPhaseAccepted MaintainerWorkItemCommandPhase = "Accepted"
	// MaintainerWorkItemCommandPhaseSucceeded indicates that a command was applied.
	MaintainerWorkItemCommandPhaseSucceeded MaintainerWorkItemCommandPhase = "Succeeded"
	// MaintainerWorkItemCommandPhaseRejected indicates that a command was rejected.
	MaintainerWorkItemCommandPhaseRejected MaintainerWorkItemCommandPhase = "Rejected"
	// MaintainerWorkItemCommandPhaseFailed indicates that command processing failed.
	MaintainerWorkItemCommandPhaseFailed MaintainerWorkItemCommandPhase = "Failed"
)

// MaintainerWorkItemCommandIssuer identifies the run that issued a command.
type MaintainerWorkItemCommandIssuer struct {
	// +kubebuilder:validation:MinLength=1
	RunName string    `json:"runName"`
	UID     types.UID `json:"uid"`
	// Proof is an HMAC from the issuer run's private command capability.
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	Proof string `json:"proof"`
}

// MaintainerWorkItemCommandPreconditions identify the projection expected by a command.
type MaintainerWorkItemCommandPreconditions struct {
	// +kubebuilder:validation:MinLength=1
	WorkItemName string `json:"workItemName"`
	// WorkItemUID prevents accepted/retryable commands from surviving target recreation.
	WorkItemUID types.UID `json:"workItemUID"`
	// +kubebuilder:validation:Minimum=0
	ProjectionSequence int64 `json:"projectionSequence"`
	// +kubebuilder:validation:MinLength=1
	ResourceVersion string `json:"resourceVersion"`
}

// MaintainerTriageCommand is the typed payload for a TriageIssue command.
// +kubebuilder:validation:XValidation:rule="!has(self.disposition) || self.disposition != 'NotActionable' || has(self.closeReason)",message="closeReason is required when disposition is NotActionable"
// +kubebuilder:validation:XValidation:rule="!has(self.closeReason) || (has(self.disposition) && self.disposition == 'NotActionable')",message="closeReason is only valid when disposition is NotActionable"
type MaintainerTriageCommand struct {
	// +kubebuilder:validation:Minimum=1
	IssueNumber int32                         `json:"issueNumber"`
	Disposition MaintainerWorkItemDisposition `json:"disposition"`
	// +kubebuilder:validation:MinLength=1
	EvidenceSummary string                  `json:"evidenceSummary"`
	AcceptedScope   MaintainerAcceptedScope `json:"acceptedScope"`
	// +optional
	CloseReason *MaintainerWorkItemCloseReason `json:"closeReason,omitempty"`
}

// MaintainerBreakdownCommand is the typed payload for a BreakdownIssue command.
type MaintainerBreakdownCommand struct {
	// +kubebuilder:validation:Minimum=1
	IssueNumber int32 `json:"issueNumber"`
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Children []MaintainerWorkItemReference `json:"children"`
	// +listType=map
	// +listMapKey=name
	// +optional
	Dependencies []MaintainerWorkItemReference `json:"dependencies,omitempty"`
}

// MaintainerRequestDecisionCommand is the typed payload for a RequestDecision command.
type MaintainerRequestDecisionCommand struct {
	// +kubebuilder:validation:Minimum=1
	IssueNumber int32 `json:"issueNumber"`
	// +kubebuilder:validation:MinLength=1
	DecisionID string `json:"decisionID"`
	// +kubebuilder:validation:MinLength=1
	Question string `json:"question"`
	// +listType=atomic
	// +optional
	Options []string `json:"options,omitempty"`
}

// MaintainerAuthenticatedHumanAnswer records an answer only after the controller authenticates its human subject.
type MaintainerAuthenticatedHumanAnswer struct {
	// +kubebuilder:validation:MinLength=1
	Subject string `json:"subject"`
	// +kubebuilder:validation:MinLength=1
	Answer string `json:"answer"`
}

// MaintainerResolveDecisionCommand is the typed payload for an authenticated ResolveDecision command.
type MaintainerResolveDecisionCommand struct {
	// +kubebuilder:validation:Minimum=1
	IssueNumber int32 `json:"issueNumber"`
	// +kubebuilder:validation:MinLength=1
	DecisionID  string                             `json:"decisionID"`
	HumanAnswer MaintainerAuthenticatedHumanAnswer `json:"humanAnswer"`
}

// MaintainerDispatchWorkItemCommand is the typed payload for a DispatchWorkItem command.
type MaintainerDispatchWorkItemCommand struct {
	// +kubebuilder:validation:Minimum=1
	IssueNumber int32 `json:"issueNumber"`
	// Mode is the ModeTemplate label applied only after capacity is reserved.
	// +kubebuilder:validation:MinLength=1
	Mode string `json:"mode"`
	// +listType=map
	// +listMapKey=name
	// +optional
	RequiredPullRequests []MaintainerRequiredPullRequestIntent `json:"requiredPullRequests,omitempty"`
}

// MaintainerRequestMergeCommand is the typed payload for a RequestMerge command.
type MaintainerRequestMergeCommand struct {
	// +kubebuilder:validation:Minimum=1
	IssueNumber int32 `json:"issueNumber"`
	// +kubebuilder:validation:Pattern=`^[^/[:space:]]+/[^/[:space:]]+$`
	Repository string `json:"repository"`
	// +kubebuilder:validation:Minimum=1
	PullRequestNumber int32 `json:"pullRequestNumber"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{40}$`
	ExpectedHeadSHA string                        `json:"expectedHeadSHA"`
	MergeMethod     MaintainerWorkItemMergeMethod `json:"mergeMethod"`
}

// MaintainerFinalizeWorkItemCommand is the typed payload for a FinalizeWorkItem command.
type MaintainerFinalizeWorkItemCommand struct {
	// +kubebuilder:validation:Minimum=1
	IssueNumber int32 `json:"issueNumber"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	AcceptedScopeHash string `json:"acceptedScopeHash"`
	// +kubebuilder:validation:MinLength=1
	DeliverySummary string `json:"deliverySummary"`
	// +kubebuilder:validation:MinLength=1
	DeliveryEvidence string `json:"deliveryEvidence"`
	// +listType=set
	// +kubebuilder:validation:items:MinLength=1
	// +optional
	ImplementerRunNames []string `json:"implementerRunNames,omitempty"`
}

// MaintainerWorkItemCommandSpec defines an immutable, idempotent command request.
// +kubebuilder:validation:XValidation:rule="self.preconditions.workItemUID.size() > 0",message="workItemUID is required"
// +kubebuilder:validation:XValidation:rule="(self.type == 'TriageIssue') == has(self.triage)",message="triage is required only when type is TriageIssue"
// +kubebuilder:validation:XValidation:rule="(self.type == 'BreakdownIssue') == has(self.breakdown)",message="breakdown is required only when type is BreakdownIssue"
// +kubebuilder:validation:XValidation:rule="(self.type == 'RequestDecision') == has(self.requestDecision)",message="requestDecision is required only when type is RequestDecision"
// +kubebuilder:validation:XValidation:rule="(self.type == 'ResolveDecision') == has(self.resolveDecision)",message="resolveDecision is required only when type is ResolveDecision"
// +kubebuilder:validation:XValidation:rule="(self.type == 'DispatchWorkItem') == has(self.dispatch)",message="dispatch is required only when type is DispatchWorkItem"
// +kubebuilder:validation:XValidation:rule="(self.type == 'RequestMerge') == has(self.requestMerge)",message="requestMerge is required only when type is RequestMerge"
// +kubebuilder:validation:XValidation:rule="(self.type == 'FinalizeWorkItem') == has(self.finalize)",message="finalize is required only when type is FinalizeWorkItem"
type MaintainerWorkItemCommandSpec struct {
	RepositoryRef corev1.LocalObjectReference `json:"repositoryRef"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9][A-Za-z0-9._:-]*$`
	IdempotencyKey string `json:"idempotencyKey"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	PayloadHash   string                                 `json:"payloadHash"`
	Issuer        MaintainerWorkItemCommandIssuer        `json:"issuer"`
	Preconditions MaintainerWorkItemCommandPreconditions `json:"preconditions"`
	Type          MaintainerWorkItemCommandType          `json:"type"`
	// +optional
	Triage *MaintainerTriageCommand `json:"triage,omitempty"`
	// +optional
	Breakdown *MaintainerBreakdownCommand `json:"breakdown,omitempty"`
	// +optional
	RequestDecision *MaintainerRequestDecisionCommand `json:"requestDecision,omitempty"`
	// +optional
	ResolveDecision *MaintainerResolveDecisionCommand `json:"resolveDecision,omitempty"`
	// +optional
	Dispatch *MaintainerDispatchWorkItemCommand `json:"dispatch,omitempty"`
	// +optional
	RequestMerge *MaintainerRequestMergeCommand `json:"requestMerge,omitempty"`
	// +optional
	Finalize *MaintainerFinalizeWorkItemCommand `json:"finalize,omitempty"`
}

// MaintainerWorkItemName returns the deterministic DNS-safe identity for a
// repository issue.
func MaintainerWorkItemName(repositoryName string, issueNumber int32) string {
	raw := strings.ToLower(strings.TrimSpace(repositoryName)) + "-" + strconv.FormatInt(int64(issueNumber), 10)
	base := maintainerDNSName(raw)
	if base == "" {
		base = "issue"
	}
	const prefix = "mwi-"
	sum := sha256.Sum256([]byte(repositoryName + "\x00" + strconv.FormatInt(int64(issueNumber), 10)))
	hash := hex.EncodeToString(sum[:])[:10]
	maxBase := 63 - len(prefix) - len(hash) - 1
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	return prefix + base + "-" + hash
}

// MaintainerWorkItemCommandName returns the deterministic DNS-safe identity
// for a repository command idempotency key.
func MaintainerWorkItemCommandName(repositoryName, idempotencyKey string) string {
	base := "triage-" + maintainerDNSName(repositoryName) + "-" + maintainerDNSName(idempotencyKey)
	sum := sha256.Sum256([]byte(repositoryName + "\x00" + idempotencyKey))
	hash := hex.EncodeToString(sum[:])[:8]
	maxBase := 63 - len(hash) - 1
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	if base == "" {
		base = "triage"
	}
	return base + "-" + hash
}

func maintainerDNSName(value string) string {
	var name strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			name.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && name.Len() > 0 {
			name.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(name.String(), "-")
}

// MaintainerWorkItemCommandPayloadHash returns the phase-one canonical hash for
// a triage payload and its projection preconditions. It remains available for
// existing triage command producers.
func MaintainerWorkItemCommandPayloadHash(commandType MaintainerWorkItemCommandType, triage *MaintainerTriageCommand, preconditions MaintainerWorkItemCommandPreconditions) string {
	payload := struct {
		Type          MaintainerWorkItemCommandType          `json:"type"`
		Triage        *MaintainerTriageCommand               `json:"triage"`
		Preconditions MaintainerWorkItemCommandPreconditions `json:"preconditions"`
	}{Type: commandType, Triage: triage, Preconditions: preconditions}
	encoded, _ := json.Marshal(payload) // The typed payload contains no fallible JSON values.
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// MaintainerWorkItemCommandSpecPayloadHash returns the canonical SHA-256 hash
// of any typed command payload and its projection preconditions.
func MaintainerWorkItemCommandSpecPayloadHash(spec MaintainerWorkItemCommandSpec) string {
	payload := struct {
		Type            MaintainerWorkItemCommandType          `json:"type"`
		Triage          *MaintainerTriageCommand               `json:"triage"`
		Breakdown       *MaintainerBreakdownCommand            `json:"breakdown"`
		RequestDecision *MaintainerRequestDecisionCommand      `json:"requestDecision"`
		ResolveDecision *MaintainerResolveDecisionCommand      `json:"resolveDecision"`
		Dispatch        *MaintainerDispatchWorkItemCommand     `json:"dispatch"`
		RequestMerge    *MaintainerRequestMergeCommand         `json:"requestMerge"`
		Finalize        *MaintainerFinalizeWorkItemCommand     `json:"finalize"`
		Preconditions   MaintainerWorkItemCommandPreconditions `json:"preconditions"`
	}{
		Type:            spec.Type,
		Triage:          spec.Triage,
		Breakdown:       spec.Breakdown,
		RequestDecision: spec.RequestDecision,
		ResolveDecision: spec.ResolveDecision,
		Dispatch:        spec.Dispatch,
		RequestMerge:    spec.RequestMerge,
		Finalize:        spec.Finalize,
		Preconditions:   spec.Preconditions,
	}
	encoded, _ := json.Marshal(payload) // The typed payload contains no fallible JSON values.
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// MaintainerCommandCapabilitySecretName returns the deterministic Secret name
// containing the private command capability for a standing maintainer run.
func MaintainerCommandCapabilitySecretName(runName string) string {
	const suffix = "-maintainer-command"
	base := strings.Trim(strings.ToLower(runName), "-")
	if len(base)+len(suffix) <= 63 {
		return base + suffix
	}
	sum := sha256.Sum256([]byte(runName))
	hash := hex.EncodeToString(sum[:])[:8]
	base = strings.TrimRight(base[:63-len(suffix)-len(hash)-1], "-")
	return base + "-" + hash + suffix
}

// MaintainerWorkItemCommandProof authenticates a command to the private
// capability of its declared issuer run.
func MaintainerWorkItemCommandProof(key []byte, repositoryName string, repositoryUID types.UID, idempotencyKey, payloadHash, runName string, uid types.UID) string {
	message, _ := json.Marshal(struct {
		RepositoryName string    `json:"repositoryName"`
		RepositoryUID  types.UID `json:"repositoryUID"`
		IdempotencyKey string    `json:"idempotencyKey"`
		PayloadHash    string    `json:"payloadHash"`
		RunName        string    `json:"runName"`
		UID            types.UID `json:"uid"`
	}{repositoryName, repositoryUID, idempotencyKey, payloadHash, runName, uid})
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}

// MaintainerWorkItemCommandResult records the externally visible command result.
type MaintainerWorkItemCommandResult struct {
	WorkItemRef corev1.LocalObjectReference `json:"workItemRef"`
	Applied     bool                        `json:"applied"`
	Message     string                      `json:"message"`
	// +optional
	CommentURL string `json:"commentURL,omitempty"`
	// +optional
	IssueState MaintainerIssueState `json:"issueState,omitempty"`
	// +listType=map
	// +listMapKey=name
	// +optional
	ChildRefs []MaintainerWorkItemReference `json:"childRefs,omitempty"`
	// +optional
	DispatchReservation *MaintainerDispatchReservation `json:"dispatchReservation,omitempty"`
	// +optional
	AgentRunRef *corev1.LocalObjectReference `json:"agentRunRef,omitempty"`
	// MergeAttemptedAt is reserved before the irreversible GitHub call. Retries
	// only verify the expected head and never automatically submit it again.
	// +optional
	MergeAttemptedAt *metav1.Time `json:"mergeAttemptedAt,omitempty"`
	// +optional
	VerifiedMerge *MaintainerVerifiedPullRequestMerge `json:"verifiedMerge,omitempty"`
	// +optional
	DeliveryAttestation *MaintainerDeliveryAttestation `json:"deliveryAttestation,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// MaintainerWorkItemCommandStatus defines the durable receipt for a command.
type MaintainerWorkItemCommandStatus struct {
	// +optional
	Phase MaintainerWorkItemCommandPhase `json:"phase,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Result *MaintainerWorkItemCommandResult `json:"result,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="spec is immutable"
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repositoryRef.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="WorkItem",type=string,JSONPath=`.status.result.workItemRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MaintainerWorkItemCommand is an immutable command submitted for maintainer triage.
type MaintainerWorkItemCommand struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec MaintainerWorkItemCommandSpec `json:"spec"`

	// +optional
	Status MaintainerWorkItemCommandStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MaintainerWorkItemCommandList contains a list of MaintainerWorkItemCommand.
type MaintainerWorkItemCommandList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MaintainerWorkItemCommand `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&MaintainerWorkItem{},
		&MaintainerWorkItemList{},
		&MaintainerWorkItemCommand{},
		&MaintainerWorkItemCommandList{},
	)
}
