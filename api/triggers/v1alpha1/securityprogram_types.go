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
	MaxSecurityProgramScanTargets       = 256
	MaxSecurityProgramTargetParameters  = 32
	MaxSecurityProgramParameterName     = 63
	MaxSecurityProgramParameterValue    = 4096

	MaxSecurityProgramImpacts           = 256
	MaxSecurityProgramImpactLength      = 1024
	MaxSecurityProgramExclusions        = 256
	MaxSecurityProgramAssets            = 256
	MaxSecurityProgramKnownIssues       = 256
	MaxSecurityProgramProhibitedTesting = 64
)

// SecurityProgramSeveritySystem names the severity ladder a program is judged
// under. Severity labels are never translated between systems: an Immunefi
// critical and an Ethereum Foundation critical are different objects.
type SecurityProgramSeveritySystem string

const (
	SeveritySystemImmunefiV23        SecurityProgramSeveritySystem = "immunefi-v2.3"
	SeveritySystemCode4rena          SecurityProgramSeveritySystem = "code4rena"
	SeveritySystemSherlock           SecurityProgramSeveritySystem = "sherlock"
	SeveritySystemCantina            SecurityProgramSeveritySystem = "cantina"
	SeveritySystemEthereumFoundation SecurityProgramSeveritySystem = "ethereum-foundation"
	SeveritySystemCustom             SecurityProgramSeveritySystem = "custom"
)

// SecurityProgramPrimacy records whether the program judges a report by the
// impact it demonstrates or strictly by the assets its rules list.
type SecurityProgramPrimacy string

const (
	// PrimacyImpact judges a report on demonstrated impact even when the
	// affected asset is not itemized in scope.
	PrimacyImpact SecurityProgramPrimacy = "impact"
	// PrimacyRules restricts eligibility to the itemized assets.
	PrimacyRules SecurityProgramPrimacy = "rules"
)

// SecurityProgramPoCEnvironment records the proof-of-concept environment the
// program accepts.
type SecurityProgramPoCEnvironment string

const (
	PoCEnvironmentMainnetFork      SecurityProgramPoCEnvironment = "mainnet-fork"
	PoCEnvironmentProjectTestSuite SecurityProgramPoCEnvironment = "project-test-suite"
	// PoCEnvironmentLocalDevnet covers programs that require the proof to run
	// against a locally started network - Ethereum Foundation demands local
	// testnets, and several chain programs forbid touching a public cluster.
	PoCEnvironmentLocalDevnet SecurityProgramPoCEnvironment = "local-devnet"
	PoCEnvironmentEither      SecurityProgramPoCEnvironment = "either"
)

// SecurityProgramImpact is one impact clause transcribed verbatim from the
// program's published in-scope impact list, with the severity the program's
// own system assigns it. A report must select one of these clauses; inventing
// an impact or asset is a rules violation on every major platform.
type SecurityProgramImpact struct {
	// impact is the verbatim impact clause as published by the program.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Impact string `json:"impact"`

	// level is the severity the program's own system assigns this impact.
	// +kubebuilder:validation:Enum=critical;high;medium;low
	Level string `json:"level"`

	// assetType is the program's own asset category for the clause, such as
	// "Smart Contract", "Blockchain/DLT", or "Websites and Applications".
	// +optional
	// +kubebuilder:validation:MaxLength=200
	AssetType string `json:"assetType,omitempty"`
}

// SecurityProgramAsset binds an in-scope asset to what is actually deployed.
// Several of the highest-impact vulnerability classes (uninitialized proxies,
// wrong token decimals, oracle wiring) are properties of deployed state rather
// than of the repository.
type SecurityProgramAsset struct {
	// chainID is the EIP-155 chain identifier, or another chain's canonical
	// network identifier, for a deployed asset.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	ChainID string `json:"chainID,omitempty"`

	// address is the deployed contract or program address.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	Address string `json:"address,omitempty"`

	// repositoryURL is the HTTPS source repository for the asset.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^(https://.*)?$`
	RepositoryURL string `json:"repositoryURL,omitempty"`

	// displayName is the program's own name for the asset.
	// +optional
	// +kubebuilder:validation:MaxLength=200
	DisplayName string `json:"displayName,omitempty"`

	// addedOn is the date the program listed the asset, as published.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	AddedOn string `json:"addedOn,omitempty"`
}

// SecurityProgramKnownIssue records something the program already knows about.
// Contest platforms treat prior-audit findings, acknowledged README issues and
// automated-tool output as ineligible, along with their duplicates.
type SecurityProgramKnownIssue struct {
	// source describes where the known issue comes from, such as a prior
	// audit, a repository README acknowledgement, or a bot tool.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=200
	Source string `json:"source"`

	// summary describes the known issue in the program's own words.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	Summary string `json:"summary"`

	// reference is an optional HTTPS link to the published known issue.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^(https://.*)?$`
	Reference string `json:"reference,omitempty"`
}

// SecurityProgramSubmissionBudget rations reports. Submission is a scarce,
// reputation-gated resource on every major platform, so volume is a liability
// rather than an asset: precision decides whether reports are read at all.
type SecurityProgramSubmissionBudget struct {
	// maxPerPeriod is the maximum number of submissions allowed per period.
	// Zero means the program publishes no explicit limit.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10000
	MaxPerPeriod int32 `json:"maxPerPeriod,omitempty"`

	// periodDays is the length of the budget period in days. Zero means the
	// limit applies per engagement rather than per rolling window.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=365
	PeriodDays int32 `json:"periodDays,omitempty"`

	// unrestrictedRequiresReputation records that the program only lifts the
	// submission cap for researchers above a reputation or identity threshold.
	// +optional
	UnrestrictedRequiresReputation bool `json:"unrestrictedRequiresReputation,omitempty"`
}

// SecurityProgramScanTarget describes a suggested SecurityScan configuration
// for a program.
// +kubebuilder:validation:XValidation:rule="(has(self.repositoryURL) && self.repositoryURL.size() > 0) != (has(self.targetURL) && self.targetURL.size() > 0)",message="exactly one of repositoryURL or targetURL must be set"
type SecurityProgramScanTarget struct {
	// repositoryURL is the HTTPS URL of the repository to scan. It is mutually
	// exclusive with targetURL.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^(https://.*)?$`
	RepositoryURL string `json:"repositoryURL,omitempty"`

	// targetURL is the HTTP(S) URL or bare domain to examine in a repoless web
	// security scan. It is mutually exclusive with repositoryURL.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^([A-Za-z0-9.-]+(:[0-9]+)?|https?://[A-Za-z0-9.\[\]:-]+([/?][^#@,[:space:]]*)?)?$`
	TargetURL string `json:"targetURL,omitempty"`

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

	// parameterValues are scan-time workflow values copied into imported
	// SecurityScan spec.parameterValues.
	// +optional
	// +kubebuilder:validation:MaxProperties=32
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.size() <= 63 && k.matches('^[a-zA-Z_][a-zA-Z0-9_]*$'))",message="parameter names must be identifiers of at most 63 characters"
	// +kubebuilder:validation:XValidation:rule="self.all(k, self[k].size() <= 4096)",message="parameter values must be at most 4096 characters"
	ParameterValues map[string]string `json:"parameterValues,omitempty"`
}

// SecurityProgramSpec is an operator-verified snapshot of a vulnerability
// disclosure or bug bounty program's scope. ProgramURL records provenance
// only: it is never fetched and does not authorize network access.
// +kubebuilder:validation:XValidation:rule="!(has(self.scanTarget) && has(self.scanTargets) && self.scanTargets.size() > 0)",message="scanTarget and scanTargets cannot both be set"
type SecurityProgramSpec struct {
	// scanTargets describe suggested SecurityScan configurations for the
	// repositories and websites covered by this program.
	// +optional
	// +kubebuilder:validation:MaxItems=256
	// +listType=map
	// +listMapKey=scanName
	ScanTargets []SecurityProgramScanTarget `json:"scanTargets,omitempty"`

	// scanTarget is the deprecated single-target form. New resources should
	// use scanTargets. It remains readable for backward compatibility.
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

	// severitySystem names the severity ladder this program is judged under.
	// Consumers must rank findings with that system's own table and must never
	// translate a severity label from one system into another.
	// +optional
	// +kubebuilder:validation:Enum=immunefi-v2.3;code4rena;sherlock;cantina;ethereum-foundation;custom
	SeveritySystem string `json:"severitySystem,omitempty"`

	// inScopeImpacts are the program's published impact clauses, transcribed
	// verbatim. A submission must select one of these; a report that invents
	// an impact or asset is rejected, and misrepresenting them is a rules
	// violation rather than a downgrade.
	// +optional
	// +kubebuilder:validation:MaxItems=256
	// +listType=atomic
	InScopeImpacts []SecurityProgramImpact `json:"inScopeImpacts,omitempty"`

	// outOfScope are the program's published exclusions, transcribed verbatim.
	// +optional
	// +kubebuilder:validation:MaxItems=256
	// +listType=atomic
	OutOfScope []string `json:"outOfScope,omitempty"`

	// primacy records whether the program judges by demonstrated impact or
	// strictly by its itemized asset rules.
	// +optional
	// +kubebuilder:validation:Enum=impact;rules
	Primacy string `json:"primacy,omitempty"`

	// pocRequired records that the program requires a runnable proof of
	// concept before a report is eligible.
	// +optional
	PoCRequired bool `json:"pocRequired,omitempty"`

	// pocEnvironment records the proof-of-concept environment the program
	// accepts.
	// +optional
	// +kubebuilder:validation:Enum=mainnet-fork;project-test-suite;local-devnet;either
	PoCEnvironment string `json:"pocEnvironment,omitempty"`

	// assets bind in-scope assets to deployed state so findings can be checked
	// against what is actually running rather than only against source.
	// +optional
	// +kubebuilder:validation:MaxItems=256
	// +listType=atomic
	Assets []SecurityProgramAsset `json:"assets,omitempty"`

	// knownIssues are issues the program already knows about. They, and
	// findings sharing their root cause, are not reportable.
	// +optional
	// +kubebuilder:validation:MaxItems=256
	// +listType=atomic
	KnownIssues []SecurityProgramKnownIssue `json:"knownIssues,omitempty"`

	// prohibitedTesting are the program's published testing prohibitions,
	// transcribed verbatim. Violating them typically forfeits every report.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	ProhibitedTesting []string `json:"prohibitedTesting,omitempty"`

	// submissionBudget rations how many reports may be submitted.
	// +optional
	SubmissionBudget *SecurityProgramSubmissionBudget `json:"submissionBudget,omitempty"`

	// kycRequired records that the program requires identity verification
	// before it pays a reward.
	// +optional
	KYCRequired bool `json:"kycRequired,omitempty"`
}

// ImpactLevel returns the program's own severity level for a verbatim impact
// clause and reports whether the clause is in the program's published list.
// An empty impact list means the program's impacts have not been transcribed,
// which callers must treat as inconclusive rather than as an exclusion.
func (spec SecurityProgramSpec) ImpactLevel(impact string) (string, bool) {
	for _, entry := range spec.InScopeImpacts {
		if entry.Impact == impact {
			return entry.Level, true
		}
	}
	return "", false
}

// EffectiveScanTargets returns the canonical target list, falling back to the
// deprecated single-target field for resources created before scanTargets was
// introduced.
func (spec SecurityProgramSpec) EffectiveScanTargets() []SecurityProgramScanTarget {
	if len(spec.ScanTargets) != 0 {
		return spec.ScanTargets
	}
	if spec.ScanTarget != nil {
		return []SecurityProgramScanTarget{*spec.ScanTarget}
	}
	return nil
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
