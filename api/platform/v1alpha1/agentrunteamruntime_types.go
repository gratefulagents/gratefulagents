/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TaskInstanceState represents the lifecycle state of a task within the runtime substrate.
// +kubebuilder:validation:Enum=declared;ready;claimed;running;blocked;completed;failed;cancelled;retry_pending;expired;reassigned
type TaskInstanceState string

const (
	TaskStateDeclared     TaskInstanceState = "declared"
	TaskStateReady        TaskInstanceState = "ready"
	TaskStateClaimed      TaskInstanceState = "claimed"
	TaskStateRunning      TaskInstanceState = "running"
	TaskStateBlocked      TaskInstanceState = "blocked"
	TaskStateCompleted    TaskInstanceState = "completed"
	TaskStateFailed       TaskInstanceState = "failed"
	TaskStateCancelled    TaskInstanceState = "cancelled"
	TaskStateRetryPending TaskInstanceState = "retry_pending"
	TaskStateExpired      TaskInstanceState = "expired"
	TaskStateReassigned   TaskInstanceState = "reassigned"
)

// DiagnosticCode classifies the reason a task is in a non-healthy state.
// +kubebuilder:validation:Enum=none;stale_heartbeat;blocked_without_update;lease_expired;retry_exhausted;dependency_stalled;artifact_missing
type DiagnosticCode string

const (
	DiagnosticNone                 DiagnosticCode = "none"
	DiagnosticStaleHeartbeat       DiagnosticCode = "stale_heartbeat"
	DiagnosticBlockedWithoutUpdate DiagnosticCode = "blocked_without_update"
	DiagnosticLeaseExpired         DiagnosticCode = "lease_expired"
	DiagnosticRetryExhausted       DiagnosticCode = "retry_exhausted"
	DiagnosticDependencyStalled    DiagnosticCode = "dependency_stalled"
	DiagnosticArtifactMissing      DiagnosticCode = "artifact_missing"
)

// TeamRuntimeTaskInstance represents one executable task derived from spec.team.steps[].tasks[].
type TeamRuntimeTaskInstance struct {
	// Name of the task (matches AgentRunTeamTask.Name).
	Name string `json:"name"`
	// StepName is the parent step this task belongs to.
	StepName string `json:"stepName"`
	// Role from the task spec (e.g., "executor", "code-reviewer").
	// +optional
	Role string `json:"role,omitempty"`

	// State is the current lifecycle state of this task instance.
	// +optional
	State TaskInstanceState `json:"state,omitempty"`
	// StateVersion is a monotonic counter incremented on every state transition (CAS boundary).
	// +optional
	StateVersion int64 `json:"stateVersion,omitempty"`

	// DependsOn lists task names that must complete before this task becomes ready.
	// +listType=atomic
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
	// ArtifactContract declares the artifact the task must produce.
	// +optional
	ArtifactContract string `json:"artifactContract,omitempty"`
	// ArtifactSatisfied indicates the artifact contract has been verified.
	// +optional
	ArtifactSatisfied bool `json:"artifactSatisfied,omitempty"`

	// MaxRetries is the retry budget for this task.
	// +optional
	MaxRetries int32 `json:"maxRetries,omitempty"`
	// AttemptCount tracks how many times this task has been attempted.
	// +optional
	AttemptCount int32 `json:"attemptCount,omitempty"`

	// --- Claim/Lease fields ---

	// ClaimOwner is the namespace/name of the child AgentRun that owns this task.
	// +optional
	ClaimOwner string `json:"claimOwner,omitempty"`
	// LeaseToken is an opaque token proving ownership of the current lease.
	// +optional
	LeaseToken string `json:"leaseToken,omitempty"`
	// LeaseExpiresAt is when the current lease expires if not refreshed.
	// +optional
	LeaseExpiresAt *metav1.Time `json:"leaseExpiresAt,omitempty"`
	// LastHeartbeatAt is when the owner last signaled liveness.
	// +optional
	LastHeartbeatAt *metav1.Time `json:"lastHeartbeatAt,omitempty"`

	// --- Diagnostics ---

	// Diagnostic classifies the current non-healthy state, if any.
	// +optional
	Diagnostic DiagnosticCode `json:"diagnostic,omitempty"`
	// DiagnosticMessage is a human-readable explanation of the diagnostic.
	// +optional
	DiagnosticMessage string `json:"diagnosticMessage,omitempty"`
	// SuggestedAction is the recommended operator action for the current diagnostic.
	// +optional
	SuggestedAction string `json:"suggestedAction,omitempty"`

	// --- Timestamps ---

	// ClaimedAt records when the task was first claimed.
	// +optional
	ClaimedAt *metav1.Time `json:"claimedAt,omitempty"`
	// CompletedAt records when the task reached a terminal state.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// TeamRuntimeEventCheckpoint tracks the cursor position in the canonical event stream.
type TeamRuntimeEventCheckpoint struct {
	// StreamID identifies the event stream (e.g., "team/<namespace>/<name>").
	// +optional
	StreamID string `json:"streamID,omitempty"`
	// Sequence is the monotonic event sequence number.
	// +optional
	Sequence int64 `json:"sequence,omitempty"`
	// RuntimeStateVersion is the substrate resourceVersion at the time of this checkpoint.
	// +optional
	RuntimeStateVersion string `json:"runtimeStateVersion,omitempty"`
	// PostgresEventID is the corresponding durable event ID in Postgres.
	// +optional
	PostgresEventID string `json:"postgresEventID,omitempty"`
}

// AgentRunTeamRuntimeSpec defines the desired state of the team runtime substrate.
type AgentRunTeamRuntimeSpec struct {
	// ParentRef identifies the parent AgentRun that owns this runtime.
	ParentRef TeamRuntimeParentRef `json:"parentRef"`
	// Generation ties this runtime to a specific parent spec generation.
	// +optional
	Generation int64 `json:"generation,omitempty"`
	// Tasks are the executable task instances derived from the parent's team spec.
	// +listType=map
	// +listMapKey=name
	// +optional
	Tasks []TeamRuntimeTaskInstance `json:"tasks,omitempty"`
	// DelegationPolicy is copied from the parent team spec for local enforcement.
	// +optional
	DelegationPolicy *AgentRunDelegationPolicy `json:"delegationPolicy,omitempty"`
}

// TeamRuntimeParentRef identifies the parent AgentRun.
type TeamRuntimeParentRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// UID of the parent AgentRun for ownership validation.
	// +optional
	UID string `json:"uid,omitempty"`
}

// TeamRuntimePhase represents the overall phase of the runtime substrate.
// +kubebuilder:validation:Enum=BootstrapPending;BootstrapReady;Running;Completed;Failed;Cancelled
type TeamRuntimePhase string

const (
	TeamRuntimePhaseBootstrapPending TeamRuntimePhase = "BootstrapPending"
	TeamRuntimePhaseBootstrapReady   TeamRuntimePhase = "BootstrapReady"
	TeamRuntimePhaseRunning          TeamRuntimePhase = "Running"
	TeamRuntimePhaseCompleted        TeamRuntimePhase = "Completed"
	TeamRuntimePhaseFailed           TeamRuntimePhase = "Failed"
	TeamRuntimePhaseCancelled        TeamRuntimePhase = "Cancelled"
)

// AgentRunTeamRuntimeStatus defines the observed state of the team runtime substrate.
type AgentRunTeamRuntimeStatus struct {
	// Phase is the overall lifecycle phase of this runtime.
	// +optional
	Phase TeamRuntimePhase `json:"phase,omitempty"`
	// Tasks mirrors spec.tasks with authoritative runtime state.
	// +listType=map
	// +listMapKey=name
	// +optional
	Tasks []TeamRuntimeTaskInstance `json:"tasks,omitempty"`
	// EventCheckpoint is the latest cursor position in the event stream.
	// +optional
	EventCheckpoint *TeamRuntimeEventCheckpoint `json:"eventCheckpoint,omitempty"`

	// --- Aggregate counters for quick projection ---

	// +optional
	TotalTasks int32 `json:"totalTasks,omitempty"`
	// +optional
	ReadyTasks int32 `json:"readyTasks,omitempty"`
	// +optional
	RunningTasks int32 `json:"runningTasks,omitempty"`
	// +optional
	CompletedTasks int32 `json:"completedTasks,omitempty"`
	// +optional
	FailedTasks int32 `json:"failedTasks,omitempty"`
	// +optional
	BlockedTasks int32 `json:"blockedTasks,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=`.spec.parentRef.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Tasks",type=integer,JSONPath=`.status.totalTasks`
// +kubebuilder:printcolumn:name="Running",type=integer,JSONPath=`.status.runningTasks`
// +kubebuilder:printcolumn:name="Completed",type=integer,JSONPath=`.status.completedTasks`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentRunTeamRuntime is the internal runtime substrate for team-mode orchestration.
// It owns executable task instances, claim/lease state, dependency readiness,
// retry history, and event stream cursors. It is NOT a user-facing product surface —
// parent AgentRun remains the product anchor with projected summary/children.
type AgentRunTeamRuntime struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec AgentRunTeamRuntimeSpec `json:"spec"`

	// +optional
	Status AgentRunTeamRuntimeStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AgentRunTeamRuntimeList contains a list of AgentRunTeamRuntime.
type AgentRunTeamRuntimeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AgentRunTeamRuntime `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRunTeamRuntime{}, &AgentRunTeamRuntimeList{})
}
