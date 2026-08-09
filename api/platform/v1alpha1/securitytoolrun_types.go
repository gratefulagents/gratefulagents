/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// SecurityToolRunPhasePending is set before the execution Job exists.
	SecurityToolRunPhasePending = "Pending"
	// SecurityToolRunPhaseRunning is set while the execution Job runs.
	SecurityToolRunPhaseRunning = "Running"
	// SecurityToolRunPhaseSucceeded means the Job finished and produced a
	// typed result document. It does NOT mean the scan found nothing: the
	// scanner verdict lives in status.result.status.
	SecurityToolRunPhaseSucceeded = "Succeeded"
	// SecurityToolRunPhaseFailed means no trustworthy result was produced.
	SecurityToolRunPhaseFailed = "Failed"
)

// SecurityToolTarget identifies what a deterministic security tool runs
// against. Content that lives inside the requesting agent's workspace is
// staged through object storage; the Job verifies the recorded digest before
// the scanner sees it.
type SecurityToolTarget struct {
	// type is the registry target type (for example repository, directory,
	// url, image, or openapi).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Type string `json:"type"`
	// locator is the target reference. For staged content it is the logical
	// path recorded for replay; the Job substitutes the materialized path.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	Locator string `json:"locator"`
	// revision pins the target version (commit SHA, tag, or image digest).
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Revision string `json:"revision,omitempty"`
	// digest is the sha256 digest of the staged artifact (or of the target
	// itself when nothing is staged). The Job refuses to run when a staged
	// artifact does not match it.
	// +kubebuilder:validation:Pattern=`^(sha256:[0-9a-f]{64})?$`
	// +optional
	Digest string `json:"digest,omitempty"`
	// stagedObjectKey is the object-storage key of the staged target archive
	// (tar.gz) uploaded by the requester. Empty for network targets.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	StagedObjectKey string `json:"stagedObjectKey,omitempty"`
	// mediaType describes the staged artifact.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	MediaType string `json:"mediaType,omitempty"`
}

// SecurityToolArgument is one typed argument. Names and values are validated
// against the compiled tool registry; free-form scanner flags are impossible.
type SecurityToolArgument struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Value string `json:"value,omitempty"`
}

// SecurityToolRunSpec is an immutable request to execute one registered
// security tool in a dedicated Kubernetes Job. There is deliberately no image,
// command, or raw-argument field: the controller derives all of those from the
// pinned registry and operator configuration.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="securityToolRun spec is immutable"
type SecurityToolRunSpec struct {
	// tool is the registered tool name (for example gitleaks or trivy).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Tool string `json:"tool"`
	// +required
	Target SecurityToolTarget `json:"target"`
	// arguments are typed registry arguments.
	// +kubebuilder:validation:MaxItems=32
	// +listType=atomic
	// +optional
	Arguments []SecurityToolArgument `json:"arguments,omitempty"`
	// seed pins randomized tool behavior for replay.
	// +optional
	Seed *int64 `json:"seed,omitempty"`
	// scope records the authorized assets for this execution.
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	// +optional
	Scope []string `json:"scope,omitempty"`
	// sensitiveFields lists argument names whose values must be redacted from
	// replay metadata and artifacts.
	// +kubebuilder:validation:MaxItems=32
	// +listType=atomic
	// +optional
	SensitiveFields []string `json:"sensitiveFields,omitempty"`
	// requestedBy identifies the AgentRun that asked for this execution. It is
	// informational; ownership is enforced through owner references.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	RequestedBy string `json:"requestedBy,omitempty"`
}

// SecurityToolRunArtifact references one raw artifact produced by the Job.
type SecurityToolRunArtifact struct {
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	MediaType string `json:"mediaType,omitempty"`
	// +optional
	Digest string `json:"digest,omitempty"`
	// +optional
	Size int64 `json:"size,omitempty"`
	// objectKey is the object-storage key holding the artifact bytes.
	// +optional
	ObjectKey string `json:"objectKey,omitempty"`
}

// SecurityToolRunResult is the typed outcome summary. The full normalized
// document (findings, coverage, replay) stays in object storage.
type SecurityToolRunResult struct {
	// status is the deterministic verdict: pass, findings, error, timeout,
	// partial, or not_applicable. A failed or timed-out Job never becomes a
	// pass.
	// +kubebuilder:validation:Enum=pass;findings;error;timeout;partial;not_applicable
	// +optional
	Status string `json:"status,omitempty"`
	// +optional
	FindingCount int32 `json:"findingCount,omitempty"`
	// resultObjectKey is the object-storage key of result.json.
	// +optional
	ResultObjectKey string `json:"resultObjectKey,omitempty"`
	// resultDigest is the sha256 digest of result.json.
	// +optional
	ResultDigest string `json:"resultDigest,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	// +optional
	Artifacts []SecurityToolRunArtifact `json:"artifacts,omitempty"`
	// +kubebuilder:validation:MaxItems=32
	// +listType=atomic
	// +optional
	Errors []string `json:"errors,omitempty"`
}

// SecurityToolRunStatus is the observed state of a SecurityToolRun.
type SecurityToolRunStatus struct {
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// jobName is the execution Job created for this request.
	// +optional
	JobName string `json:"jobName,omitempty"`
	// image is the resolved, digest-pinned security-tools image used.
	// +optional
	Image string `json:"image,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// +optional
	Result *SecurityToolRunResult `json:"result,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tool",type=string,JSONPath=`.spec.tool`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Result",type=string,JSONPath=`.status.result.status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SecurityToolRun is one deterministic security-tool execution carried out by
// a short-lived Kubernetes Job running the pinned security-tools image.
type SecurityToolRun struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SecurityToolRunSpec `json:"spec"`

	// +optional
	Status SecurityToolRunStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SecurityToolRunList contains a list of SecurityToolRun.
type SecurityToolRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SecurityToolRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SecurityToolRun{}, &SecurityToolRunList{})
}
