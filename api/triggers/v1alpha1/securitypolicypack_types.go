/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Enforceable SecurityPolicyPack field names, valid entries of
// spec.enforced. A field listed there cannot be relaxed by a referencing
// SecurityScan; the precise "relax" definition per field lives on the
// SecurityPolicyPackSpec field docs.
const (
	SecurityPolicyFieldMinSeverity            = "minSeverity"
	SecurityPolicyFieldFailOnSeverity         = "failOnSeverity"
	SecurityPolicyFieldDedupe                 = "dedupe"
	SecurityPolicyFieldRequiredCategories     = "requiredCategories"
	SecurityPolicyFieldAllowedRuntimeProfiles = "allowedRuntimeProfiles"
	SecurityPolicyFieldBudgets                = "budgets"
)

// SecurityPolicyPackEnforceableFields lists every field name accepted in
// spec.enforced.
var SecurityPolicyPackEnforceableFields = []string{
	SecurityPolicyFieldMinSeverity,
	SecurityPolicyFieldFailOnSeverity,
	SecurityPolicyFieldDedupe,
	SecurityPolicyFieldRequiredCategories,
	SecurityPolicyFieldAllowedRuntimeProfiles,
	SecurityPolicyFieldBudgets,
}

// SecuritySuppressionMatcher selects the findings a suppression rule
// applies to. All set fields must match (AND); at least one field is
// required.
type SecuritySuppressionMatcher struct {
	// category matches the finding's category exactly.
	// +optional
	Category string `json:"category,omitempty"`

	// cwe matches findings whose CWE list contains this identifier
	// (e.g. "CWE-79").
	// +optional
	CWE string `json:"cwe,omitempty"`

	// pathGlob matches the finding's file path with a glob pattern where
	// '*' matches any run of characters and '?' matches one character.
	// +optional
	PathGlob string `json:"pathGlob,omitempty"`

	// fingerprint matches the finding's fingerprint exactly.
	// +optional
	Fingerprint string `json:"fingerprint,omitempty"`
}

// IsZero reports whether no matcher field is set.
func (m SecuritySuppressionMatcher) IsZero() bool {
	return m.Category == "" && m.CWE == "" && m.PathGlob == "" && m.Fingerprint == ""
}

// SecurityPolicySuppression is one governed suppression rule. Matching
// findings are marked suppressed in the findings store (never deleted): they
// drop out of failOnSeverity gating and default list/summary results but
// stay retrievable, and every application is audited.
type SecurityPolicySuppression struct {
	// name identifies the rule within the pack. The persisted suppression id
	// is "<pack name>/<rule name>".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// reason documents why the matched findings are suppressed.
	// +kubebuilder:validation:MinLength=1
	Reason string `json:"reason"`

	// owner is who is accountable for this suppression.
	// +kubebuilder:validation:MinLength=1
	Owner string `json:"owner"`

	// matcher selects the findings to suppress. At least one matcher field
	// is required.
	Matcher SecuritySuppressionMatcher `json:"matcher"`

	// expiresAt optionally bounds the suppression: past it, the suppression
	// is cleared by the expiry sweep and the finding counts again.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
}

// SecurityRetentionMaxDays bounds every retention day count (10 years).
const SecurityRetentionMaxDays = 3650

// SecurityPolicyPackRetention configures how long each class of persisted
// security data is kept, in days. 0 (or unset) keeps that class forever.
// Retention is enforced by a bounded, resumable purge sweep run from the
// SecurityScan controller for scans referencing the pack; the sweep is
// namespace-scoped. Evidence and PoC retention REDACT the expired content in
// place: the finding row, its identity, and its audit history are preserved.
type SecurityPolicyPackRetention struct {
	// scanDays is how long completed scan run records (and their per-run
	// observation rows) are kept. A scan run record is only removed once no
	// finding is attributed to it anymore.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3650
	// +optional
	ScanDays int32 `json:"scanDays,omitempty"`

	// findingDays is how long findings are kept after they were last
	// observed. Expired finding rows are deleted together with their audit
	// events.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3650
	// +optional
	FindingDays int32 `json:"findingDays,omitempty"`

	// reportDays is how long scan report artifacts (markdown and SARIF) are
	// kept after the scan run completed.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3650
	// +optional
	ReportDays int32 `json:"reportDays,omitempty"`

	// evidenceDays is how long finding evidence (code snippets and citations)
	// is kept after the finding was last observed. Expired evidence is
	// redacted in place: the finding row and its audit history remain.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3650
	// +optional
	EvidenceDays int32 `json:"evidenceDays,omitempty"`

	// pocDays is how long finding proof-of-concept / attack-vector narratives
	// are kept after the finding was last observed. Expired PoC content is
	// redacted in place: the finding row and its audit history remain.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3650
	// +optional
	PoCDays int32 `json:"pocDays,omitempty"`

	// auditEventDays is how long finding audit-trail events are kept.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3650
	// +optional
	AuditEventDays int32 `json:"auditEventDays,omitempty"`
}

// IsZero reports whether no retention class is configured.
func (r SecurityPolicyPackRetention) IsZero() bool {
	return r == SecurityPolicyPackRetention{}
}

// SecurityPolicyPackSpec defines an organization security policy applied to
// referencing SecurityScans. Precedence is: platform defaults < policy pack
// < scan configuration, EXCEPT for the fields listed in enforced, which a
// scan may not relax.
type SecurityPolicyPackSpec struct {
	// description explains what this policy pack governs.
	// +optional
	Description string `json:"description,omitempty"`

	// requiredCategories are workflow task categories every referencing
	// scan's effective workflow must cover. Checked only when
	// "requiredCategories" is listed in enforced.
	// +listType=atomic
	// +optional
	RequiredCategories []string `json:"requiredCategories,omitempty"`

	// minSeverity is the default minimum reported severity for referencing
	// scans. A scan may override it; when "minSeverity" is enforced, the
	// scan's effective minSeverity may not be raised above the pack's
	// (i.e. the scan cannot report less than the pack requires).
	// +kubebuilder:validation:Enum=critical;high;medium;low;info
	// +optional
	MinSeverity string `json:"minSeverity,omitempty"`

	// failOnSeverity is the default failure threshold for referencing
	// scans. A scan may override it; when "failOnSeverity" is enforced, the
	// scan's effective failOnSeverity may not be weakened (raised above the
	// pack's threshold) or removed.
	// +kubebuilder:validation:Enum=critical;high;medium;low;info
	// +optional
	FailOnSeverity string `json:"failOnSeverity,omitempty"`

	// dedupe is the default duplicate-finding suppression configuration for
	// referencing scans. A scan may override it; when "dedupe" is enforced,
	// the scan may not disable dedupe or lower the similarity threshold
	// below the pack's.
	// +optional
	Dedupe *SecurityScanDedupe `json:"dedupe,omitempty"`

	// allowedRuntimeProfiles are the RuntimeProfile names referencing scans
	// may run under. Checked only when "allowedRuntimeProfiles" is listed
	// in enforced: the scan's defaults.runtimeProfileRef must then name one
	// of these profiles.
	// +listType=atomic
	// +optional
	AllowedRuntimeProfiles []string `json:"allowedRuntimeProfiles,omitempty"`

	// defaultRankerRefs reference SecurityRanker resources appended to every
	// referencing scan's rankerRefs (refs the scan already lists are not
	// appended twice).
	// +listType=atomic
	// +optional
	DefaultRankerRefs []SecurityResourceRef `json:"defaultRankerRefs,omitempty"`

	// defaultPostScriptRefs reference SecurityPostScript resources appended
	// to every referencing scan's postScriptRefs (refs the scan already
	// lists are not appended twice).
	// +listType=atomic
	// +optional
	DefaultPostScriptRefs []SecurityResourceRef `json:"defaultPostScriptRefs,omitempty"`

	// enforced lists the pack fields above that referencing scans may NOT
	// relax: minSeverity, failOnSeverity, dedupe, requiredCategories,
	// allowedRuntimeProfiles, and/or budgets. A violating scan is rejected
	// with Ready=False reason PolicyViolation and no run is created.
	// +listType=atomic
	// +optional
	Enforced []string `json:"enforced,omitempty"`

	// suppressions are governed suppression rules applied to the findings of
	// referencing scans. Suppressed findings are never deleted: they are
	// marked, audited, excluded from gating and default listings, and
	// automatically unsuppressed when the rule's expiresAt passes.
	// +listType=atomic
	// +optional
	Suppressions []SecurityPolicySuppression `json:"suppressions,omitempty"`

	// retention configures how long each class of persisted security data
	// (scan runs, findings, reports, evidence, PoC content, audit events) is
	// kept, in days per class; 0 keeps a class forever. Enforced by a
	// bounded, namespace-scoped purge sweep in the platform.
	// +optional
	Retention *SecurityPolicyPackRetention `json:"retention,omitempty"`

	// budgets are the default scan budgets for referencing scans
	// (precedence: pack default < scan configuration). When "budgets" is
	// listed in enforced, a scan may not RAISE any limit the pack sets (nor
	// remove it); it may always set a lower one. All budget enforcement is
	// platform-side: limits are computed from the CRD spec before prompt
	// construction and re-checked against platform-observed usage, so model
	// output can never relax them.
	// +optional
	Budgets *SecurityScanBudgets `json:"budgets,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ReferencedBy",type=integer,JSONPath=`.status.referencedBy`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SecurityPolicyPack is a namespace-scoped organization security policy
// referenced by SecurityScan spec.policyPackRef. The pack supplies defaults,
// enforced floors a scan cannot relax, and governed finding suppressions.
// The referenced pack is resolved and snapshotted at run-creation time, and
// enforcement happens before prompt construction so model output can never
// affect it.
type SecurityPolicyPack struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SecurityPolicyPackSpec `json:"spec"`

	// +optional
	Status SecurityLibraryResourceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SecurityPolicyPackList contains a list of SecurityPolicyPack.
type SecurityPolicyPackList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SecurityPolicyPack `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SecurityPolicyPack{}, &SecurityPolicyPackList{})
}
