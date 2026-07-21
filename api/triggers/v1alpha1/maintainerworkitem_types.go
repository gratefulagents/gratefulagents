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
// +kubebuilder:validation:Enum=PendingTriage;Triaged
type MaintainerWorkItemPhase string

const (
	// MaintainerWorkItemPhasePendingTriage indicates that the issue awaits triage.
	MaintainerWorkItemPhasePendingTriage MaintainerWorkItemPhase = "PendingTriage"
	// MaintainerWorkItemPhaseTriaged indicates that accepted triage intent is recorded.
	MaintainerWorkItemPhaseTriaged MaintainerWorkItemPhase = "Triaged"
)

// MaintainerAcceptedScope records the scope accepted by triage.
type MaintainerAcceptedScope struct {
	// +optional
	Statement string `json:"statement,omitempty"`
	// +listType=atomic
	// +optional
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
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

// MaintainerWorkItemStatus defines the observed issue state and triage progress.
type MaintainerWorkItemStatus struct {
	// +optional
	Phase MaintainerWorkItemPhase `json:"phase,omitempty"`
	// +optional
	IssueObservation *MaintainerIssueObservation `json:"issueObservation,omitempty"`
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
// +kubebuilder:validation:Enum=TriageIssue
type MaintainerWorkItemCommandType string

const (
	// MaintainerWorkItemCommandTypeTriageIssue applies triage intent to an issue.
	MaintainerWorkItemCommandTypeTriageIssue MaintainerWorkItemCommandType = "TriageIssue"
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

// MaintainerWorkItemCommandSpec defines an immutable, idempotent command request.
// +kubebuilder:validation:XValidation:rule="self.type != 'TriageIssue' || has(self.triage)",message="triage is required when type is TriageIssue"
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

// MaintainerWorkItemCommandPayloadHash returns the canonical SHA-256 hash of
// the typed payload and its projection preconditions. Idempotency compares this
// value, so command producers and the controller must use this shared encoding.
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
