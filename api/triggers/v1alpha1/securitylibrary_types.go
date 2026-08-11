/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// SecurityScanResolvedRefsAnnotation is set by the SecurityScan controller on
// every scan AgentRun whose SecurityScan references reusable security
// resources. Its value is a JSON array of SecurityScanResolvedRef objects
// recording the exact generation and content hash of each referenced resource
// at run-creation time, so a run's provenance survives later edits to the
// library resources.
const SecurityScanResolvedRefsAnnotation = "security.gratefulagents.dev/resolved-refs"

// SecurityResourceRef names a reusable security resource in the same
// namespace as the referencing SecurityScan.
type SecurityResourceRef struct {
	// name of the referenced resource.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// SecurityScanResolvedRef records one reusable security resource resolved and
// snapshotted into a scan run at run-creation time.
type SecurityScanResolvedRef struct {
	// kind of the referenced resource: SecurityWorkflow, SecurityRanker,
	// SecurityPostScript, SecurityPolicyPack, or SecurityProgram.
	Kind string `json:"kind"`

	// name of the referenced resource.
	Name string `json:"name"`

	// generation is metadata.generation of the referenced resource when it
	// was resolved.
	// +optional
	Generation int64 `json:"generation,omitempty"`

	// hash is a sha256 hex digest of the referenced resource's resolved spec
	// content.
	// +optional
	Hash string `json:"hash,omitempty"`
}

// SecurityWorkflowParameter declares one scan-time input substituted for
// double-brace params.<name> references in task objectives.
type SecurityWorkflowParameter struct {
	// name identifies the parameter and uses double-brace params.<name> syntax.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-zA-Z_][a-zA-Z0-9_]*$`
	Name string `json:"name"`

	// description explains what the parameter controls.
	// +optional
	Description string `json:"description,omitempty"`

	// default is the value used when a scan omits the parameter.
	// +optional
	Default string `json:"default,omitempty"`

	// required, when true, means a scan must supply a value.
	// +optional
	Required bool `json:"required,omitempty"`
}

// SecurityWorkflowSpec defines a reusable security scan research plan.
type SecurityWorkflowSpec struct {
	// description explains what this workflow hunts for.
	// +optional
	Description string `json:"description,omitempty"`

	// tasks is the ordered/parallel research plan executed as focused
	// vulnerability-hunting sub-agents (same schema as SecurityScan
	// spec.workflow).
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	Tasks []SecurityScanTask `json:"tasks"`

	// parameters declares the scan-time inputs substituted for double-brace
	// params.<name> references in task objectives. Scans supply values
	// via spec.parameterValues.
	// +kubebuilder:validation:MaxItems=32
	// +listType=atomic
	// +optional
	Parameters []SecurityWorkflowParameter `json:"parameters,omitempty"`

	// parallelism optionally caps how many tasks may run concurrently in
	// scans using this workflow, overriding the scan's parallelism.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16
	// +optional
	Parallelism int32 `json:"parallelism,omitempty"`
}

// SecurityLibraryResourceStatus is the shared observed state of the reusable
// security library resources.
type SecurityLibraryResourceStatus struct {
	// observedGeneration is the spec generation most recently validated.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// referencedBy is the number of SecurityScans in the namespace that
	// reference this resource.
	// +optional
	ReferencedBy int32 `json:"referencedBy,omitempty"`

	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ConditionSecurityLibraryReady is the Ready condition type shared by
// reusable security library resources.
const ConditionSecurityLibraryReady = "Ready"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ReferencedBy",type=integer,JSONPath=`.status.referencedBy`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SecurityWorkflow is a reusable, namespace-scoped security scan workflow
// referenced by SecurityScan spec.workflowRef. Referenced content is resolved
// and snapshotted at run-creation time, so edits never change historical
// runs.
type SecurityWorkflow struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SecurityWorkflowSpec `json:"spec"`

	// +optional
	Status SecurityLibraryResourceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SecurityWorkflowList contains a list of SecurityWorkflow.
type SecurityWorkflowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SecurityWorkflow `json:"items"`
}

// SecurityRankerSpec defines reusable severity-ranking rules using the same
// rules language as SecurityScan spec.severityRankers[].rules.
type SecurityRankerSpec struct {
	// description explains what this ranker prioritizes.
	// +optional
	Description string `json:"description,omitempty"`

	// rules are ranking rule lines (directives such as "severity-floor:
	// injection=high" plus free-form prose) concatenated into the scan
	// prompt and passed to submit_security_scan_report.
	// +kubebuilder:validation:MinItems=1
	// +listType=atomic
	Rules []string `json:"rules"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ReferencedBy",type=integer,JSONPath=`.status.referencedBy`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SecurityRanker is a reusable, namespace-scoped severity ranker referenced
// by SecurityScan spec.rankerRefs. Referenced content is resolved and
// snapshotted at run-creation time.
type SecurityRanker struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SecurityRankerSpec `json:"spec"`

	// +optional
	Status SecurityLibraryResourceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SecurityRankerList contains a list of SecurityRanker.
type SecurityRankerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SecurityRanker `json:"items"`
}

// SecurityPostScriptSpec defines a reusable per-finding post-script.
type SecurityPostScriptSpec struct {
	// description explains what this post-script does.
	// +optional
	Description string `json:"description,omitempty"`

	// prompt is executed once per matching finding.
	// +kubebuilder:validation:MinLength=1
	Prompt string `json:"prompt"`

	// runOn selects which findings this post-script runs against.
	// +kubebuilder:validation:Enum=all;confirmed;high-and-above
	// +kubebuilder:default="all"
	// +optional
	RunOn string `json:"runOn,omitempty"`
}

// EffectiveRunOn returns spec.runOn, defaulting to "all".
func (s SecurityPostScriptSpec) EffectiveRunOn() string {
	if s.RunOn == "" {
		return "all"
	}
	return s.RunOn
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="RunOn",type=string,JSONPath=`.spec.runOn`
// +kubebuilder:printcolumn:name="ReferencedBy",type=integer,JSONPath=`.status.referencedBy`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SecurityPostScript is a reusable, namespace-scoped per-finding post-script
// referenced by SecurityScan spec.postScriptRefs. Referenced content is
// resolved and snapshotted at run-creation time.
type SecurityPostScript struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SecurityPostScriptSpec `json:"spec"`

	// +optional
	Status SecurityLibraryResourceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SecurityPostScriptList contains a list of SecurityPostScript.
type SecurityPostScriptList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SecurityPostScript `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&SecurityWorkflow{}, &SecurityWorkflowList{},
		&SecurityRanker{}, &SecurityRankerList{},
		&SecurityPostScript{}, &SecurityPostScriptList{},
	)
}
