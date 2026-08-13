/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	MaxSecurityProgramProviderLength    = 100
	MaxSecurityProgramDisplayNameLength = 200
	MaxSecurityProgramURLLength         = 2048
	MaxSecurityProgramScopePolicyLength = 131072
)

// SecurityProgramScanTarget describes a suggested SecurityScan configuration
// for a program.
type SecurityProgramScanTarget struct {
	// repositoryURL is the HTTPS URL of the repository to scan.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https://`
	RepositoryURL string `json:"repositoryURL"`

	// baseBranch is the repository's verified default branch used by imported
	// SecurityScans. Legacy targets that omit it fall back to main.
	// +optional
	// +kubebuilder:default="main"
	// +kubebuilder:validation:MaxLength=255
	BaseBranch string `json:"baseBranch,omitempty"`

	// workflowRef names the SecurityWorkflow to use.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	WorkflowRef string `json:"workflowRef"`

	// policyPackRef names the SecurityPolicyPack to use.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	PolicyPackRef string `json:"policyPackRef"`

	// scanName is the suggested SecurityScan name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	ScanName string `json:"scanName"`

	// displayName is the human-readable scan target name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=200
	DisplayName string `json:"displayName"`

	// priority controls the target's display order, with lower values first.
	// +kubebuilder:validation:Minimum=0
	Priority int32 `json:"priority"`

	// featured includes the target in featured target catalogs.
	Featured bool `json:"featured"`
}

// SecurityProgramSpec is an operator-verified snapshot of a vulnerability
// disclosure or bug bounty program's scope. ProgramURL records provenance
// only: it is never fetched and does not authorize network access.
type SecurityProgramSpec struct {
	// scanTarget optionally describes a suggested SecurityScan configuration.
	// +optional
	ScanTarget *SecurityProgramScanTarget `json:"scanTarget,omitempty"`

	// provider identifies the program platform or operator.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Provider string `json:"provider"`

	// displayName is the human-readable program name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=200
	DisplayName string `json:"displayName"`

	// programURL is the HTTPS provenance URL for the program. The controller
	// never fetches it and it grants no authorization to contact any target.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https://`
	ProgramURL string `json:"programURL"`

	// scopePolicy is the operator-verified scope snapshot supplied to scan
	// prompts as quoted, untrusted data.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=131072
	ScopePolicy string `json:"scopePolicy"`

	// verifiedAt is when an operator last verified scopePolicy against the
	// program's authoritative source.
	VerifiedAt metav1.Time `json:"verifiedAt"`
}

// SecurityProgramStatus is the observed validation and usage state.
type SecurityProgramStatus struct {
	// observedGeneration is the spec generation most recently validated.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// contentDigest is the SHA-256 digest of the validated spec snapshot.
	// +optional
	ContentDigest string `json:"contentDigest,omitempty"`

	// referencedBy is the number of SecurityScans in the namespace that
	// reference this program.
	// +optional
	ReferencedBy int32 `json:"referencedBy,omitempty"`

	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="ReferencedBy",type=integer,JSONPath=`.status.referencedBy`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SecurityProgram is a namespace-scoped, operator-verified program scope
// snapshot referenced by SecurityScan spec.securityProgramRef.
type SecurityProgram struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SecurityProgramSpec `json:"spec"`

	// +optional
	Status SecurityProgramStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SecurityProgramList contains a list of SecurityProgram.
type SecurityProgramList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SecurityProgram `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SecurityProgram{}, &SecurityProgramList{})
}
