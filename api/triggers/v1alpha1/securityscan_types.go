/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Annotations set by the SecurityScan controller on every scan AgentRun.
// Agent-side security tools read them to bind reported findings to the scan
// and to enforce the scan's reporting policy.
const (
	// SecurityScanNameAnnotation is the SecurityScan name that created the run.
	SecurityScanNameAnnotation = "security.gratefulagents.dev/scan-name"
	// SecurityScanRepositoryAnnotation is the scanned repository URL.
	SecurityScanRepositoryAnnotation = "security.gratefulagents.dev/repository"
	// SecurityScanRevisionAnnotation is the pinned revision, when set.
	SecurityScanRevisionAnnotation = "security.gratefulagents.dev/revision"
	// SecurityScanMinSeverityAnnotation is spec.minSeverity after defaulting
	// (EffectiveMinSeverity).
	SecurityScanMinSeverityAnnotation = "security.gratefulagents.dev/min-severity"
	// SecurityScanDedupePermilleAnnotation is the dedupe similarity threshold
	// in permille (DedupeSimilarityThresholdPermille), or "0" when dedupe is
	// disabled.
	SecurityScanDedupePermilleAnnotation = "security.gratefulagents.dev/dedupe-permille"
)

// SecurityScanSpec defines the desired state of SecurityScan.
type SecurityScanSpec struct {
	// repoURL is the git repository URL that is the target of the scan.
	// +kubebuilder:validation:MinLength=1
	RepoURL string `json:"repoURL"`

	// baseBranch is the branch of repoURL that is scanned.
	// +kubebuilder:default="main"
	// +optional
	BaseBranch string `json:"baseBranch,omitempty"`

	// revision optionally pins the scan to a specific commit. When empty, the
	// head of baseBranch at scan time is used.
	// +optional
	Revision string `json:"revision,omitempty"`

	// additionalRepos lists dependency repositories cloned and scanned
	// alongside the target repository.
	// +listType=atomic
	// +optional
	AdditionalRepos []string `json:"additionalRepos,omitempty"`

	// scope optionally narrows what the scan looks at.
	// +optional
	Scope *SecurityScanScope `json:"scope,omitempty"`

	// workflow is the ordered/parallel research plan executed as focused
	// vulnerability-hunting sub-agents. When empty, the controller uses
	// DefaultSecurityWorkflow().
	// +listType=atomic
	// +optional
	Workflow []SecurityScanTask `json:"workflow,omitempty"`

	// parallelism caps how many workflow tasks may run concurrently.
	// +kubebuilder:default=4
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16
	// +optional
	Parallelism int32 `json:"parallelism,omitempty"`

	// severityRankers hold operator-authored ranking rule text concatenated
	// into the scan prompt and passed to submit_security_scan_report.
	// +listType=atomic
	// +optional
	SeverityRankers []SecurityRanker `json:"severityRankers,omitempty"`

	// postScripts are per-finding validation, proof-of-concept, or report
	// prompts executed after the workflow produces findings.
	// +listType=atomic
	// +optional
	PostScripts []SecurityPostScript `json:"postScripts,omitempty"`

	// dedupe configures duplicate-finding suppression.
	// +optional
	Dedupe *SecurityScanDedupe `json:"dedupe,omitempty"`

	// minSeverity excludes findings below this severity from the report.
	// +kubebuilder:validation:Enum=critical;high;medium;low;info
	// +kubebuilder:default="low"
	// +optional
	MinSeverity string `json:"minSeverity,omitempty"`

	// failOnSeverity, when set, makes the controller report a Ready=False
	// condition with reason FindingsExceedThreshold whenever findings at or
	// above this severity exist.
	// +kubebuilder:validation:Enum=critical;high;medium;low;info
	// +optional
	FailOnSeverity string `json:"failOnSeverity,omitempty"`

	// schedule is an optional standard 5-field cron expression, or a
	// descriptor supported by robfig/cron such as "@daily". When empty, the
	// scan runs exactly once per spec generation.
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// timeZone is the IANA time zone used to evaluate schedule.
	// When omitted, UTC is used.
	// +kubebuilder:default="UTC"
	// +optional
	TimeZone string `json:"timeZone,omitempty"`

	// suspend pauses new AgentRun creation while keeping status readable.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// concurrencyPolicy controls whether a scheduled tick may create a new
	// AgentRun while a previous AgentRun from this scan is still active.
	// Empty defaults to Forbid for safety.
	// +kubebuilder:validation:Enum=Allow;Forbid
	// +optional
	ConcurrencyPolicy SecurityScanConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	// defaults holds the fields used when creating AgentRuns. The controller
	// forces defaults.repoURL and defaults.baseBranch from the scan target.
	// Because scans ingest untrusted third-party code, defaults requesting
	// disableCommandSandbox or kubernetesAdmin are rejected: the controller
	// refuses to create runs and reports Ready=False with reason
	// InsecureDefaults.
	Defaults AgentRunDefaults `json:"defaults"`

	// maxRuntime optionally caps the runtime of each scan AgentRun,
	// overriding defaults.timeout.
	// +optional
	MaxRuntime metav1.Duration `json:"maxRuntime,omitempty"`
}

// SecurityScanScope narrows what a scan looks at.
type SecurityScanScope struct {
	// focus is free-form guidance about what to prioritize during the scan.
	// +optional
	Focus string `json:"focus,omitempty"`

	// includePaths restricts the scan to these path globs when non-empty.
	// +listType=atomic
	// +optional
	IncludePaths []string `json:"includePaths,omitempty"`

	// excludePaths are path globs the scan should skip.
	// +listType=atomic
	// +optional
	ExcludePaths []string `json:"excludePaths,omitempty"`

	// languages restricts analysis to these languages when non-empty.
	// +listType=atomic
	// +optional
	Languages []string `json:"languages,omitempty"`
}

// SecurityScanTask is one focused research task in the scan workflow, executed
// by a dedicated sub-agent.
type SecurityScanTask struct {
	// name identifies the task and is referenced by dependsOn.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// objective is the focused prompt for this task's sub-agent.
	// +kubebuilder:validation:MinLength=1
	Objective string `json:"objective"`

	// category optionally tags the vulnerability class this task hunts for.
	// +optional
	Category string `json:"category,omitempty"`

	// dependsOn lists task names that must complete before this task starts.
	// +listType=atomic
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// role is the RoleInstruction name assumed by this task's sub-agent.
	// +kubebuilder:default="security-reviewer"
	// +optional
	Role string `json:"role,omitempty"`

	// model optionally overrides the model for this task's sub-agent.
	// +optional
	Model string `json:"model,omitempty"`

	// maxFindings caps how many findings this task may report. Zero means
	// unlimited.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxFindings int32 `json:"maxFindings,omitempty"`
}

// SecurityRanker is operator-authored severity-ranking rule text.
type SecurityRanker struct {
	// name identifies the ranker.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// rules is the ranking rule text concatenated into the scan prompt and
	// passed to submit_security_scan_report.
	// +kubebuilder:validation:MinLength=1
	Rules string `json:"rules"`
}

// SecurityPostScript is a per-finding validation, proof-of-concept, or report
// prompt executed after findings are collected.
type SecurityPostScript struct {
	// name identifies the post-script.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// prompt is executed once per matching finding.
	// +kubebuilder:validation:MinLength=1
	Prompt string `json:"prompt"`

	// runOn selects which findings this post-script runs against.
	// +kubebuilder:validation:Enum=all;confirmed;high-and-above
	// +kubebuilder:default="all"
	// +optional
	RunOn string `json:"runOn,omitempty"`
}

// SecurityScanDedupe configures duplicate-finding suppression.
type SecurityScanDedupe struct {
	// enabled toggles dedupe. Defaults to true.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// similarityThresholdPermille is the similarity score, in thousandths
	// (permille), at or above which two findings are treated as duplicates.
	// 820 means a similarity of 0.82.
	// +kubebuilder:default=820
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000
	// +optional
	SimilarityThresholdPermille int32 `json:"similarityThresholdPermille,omitempty"`
}

// SecurityScanFindingCounts summarizes findings by severity.
type SecurityScanFindingCounts struct {
	// +optional
	Total int32 `json:"total,omitempty"`
	// +optional
	Open int32 `json:"open,omitempty"`
	// +optional
	Critical int32 `json:"critical,omitempty"`
	// +optional
	High int32 `json:"high,omitempty"`
	// +optional
	Medium int32 `json:"medium,omitempty"`
	// +optional
	Low int32 `json:"low,omitempty"`
	// +optional
	Info int32 `json:"info,omitempty"`
}

// SecurityScanStatus defines the observed state of SecurityScan.
type SecurityScanStatus struct {
	// phase is a coarse human-readable state of the scan trigger.
	// +optional
	Phase string `json:"phase,omitempty"`

	// lastRunName is the AgentRun name most recently created by this scan.
	// +optional
	LastRunName string `json:"lastRunName,omitempty"`

	// lastScanTime is when the most recent scan AgentRun was created.
	// +optional
	LastScanTime *metav1.Time `json:"lastScanTime,omitempty"`

	// nextScheduleTime is the next schedule instant the controller expects to
	// process. Only set when spec.schedule is configured.
	// +optional
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`

	// observedSchedule is the schedule string last accepted by the controller.
	// +optional
	ObservedSchedule string `json:"observedSchedule,omitempty"`

	// observedTimeZone is the time zone last accepted by the controller.
	// +optional
	ObservedTimeZone string `json:"observedTimeZone,omitempty"`

	// observedGeneration is the spec generation most recently acted on. For
	// unscheduled scans it gates the run-once behavior: a new AgentRun is only
	// created when the spec generation changes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// runsCreated is the cumulative number of AgentRuns created by this scan.
	// +optional
	RunsCreated int32 `json:"runsCreated,omitempty"`

	// findings summarizes persisted findings for the most recent scan run.
	// +optional
	Findings *SecurityScanFindingCounts `json:"findings,omitempty"`

	// lastError contains the error message from the most recent failed operation.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// conditions represent the current state of the SecurityScan trigger.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for SecurityScan.
const (
	ConditionSecurityScanReady = "Ready"
)

// SecurityScanConcurrencyPolicy controls overlapping scheduled scan AgentRuns.
type SecurityScanConcurrencyPolicy string

const (
	SecurityScanConcurrencyAllow  SecurityScanConcurrencyPolicy = "Allow"
	SecurityScanConcurrencyForbid SecurityScanConcurrencyPolicy = "Forbid"
)

// DefaultSecurityScanRole is the RoleInstruction assumed by workflow tasks
// that do not set one.
const DefaultSecurityScanRole = "security-reviewer"

// DefaultSecurityWorkflow returns the built-in scan plan used when
// spec.workflow is empty: focused hunting tasks per vulnerability class plus a
// final triage-and-report task that depends on all of them.
func DefaultSecurityWorkflow() []SecurityScanTask {
	tasks := make([]SecurityScanTask, 0, 12)
	tasks = append(tasks,
		SecurityScanTask{Name: "attack-surface-mapping", Role: "threat-modeler", Category: "recon", Objective: "Map the application's attack surface: entry points, exposed endpoints, trust boundaries, privileged operations, and data flows from untrusted input to sensitive sinks."},
		SecurityScanTask{Name: "authn-authz", Role: "vulnerability-hunter", Category: "authn", Objective: "Hunt for authentication and authorization flaws: missing or bypassable auth checks, broken session handling, privilege escalation paths, and insecure token validation."},
		SecurityScanTask{Name: "injection-and-input-handling", Role: "vulnerability-hunter", Category: "injection", Objective: "Hunt for injection and unsafe input handling: SQL/NoSQL/command/template injection, XSS, path traversal, and unsanitized data reaching interpreters or shells."},
		SecurityScanTask{Name: "secrets-and-credentials", Role: "secrets-auditor", Category: "secrets", Objective: "Hunt for hardcoded secrets, credentials committed to the repository, secrets leaked into logs or errors, and insecure credential storage or transmission."},
		SecurityScanTask{Name: "crypto-and-randomness", Role: "vulnerability-hunter", Category: "crypto", Objective: "Hunt for cryptographic weaknesses: weak or misused algorithms, insecure randomness, missing integrity checks, improper key management, and TLS misconfiguration."},
		SecurityScanTask{Name: "ssrf-and-network", Role: "vulnerability-hunter", Category: "ssrf", Objective: "Hunt for server-side request forgery and unsafe network egress: user-controlled URLs, open redirects, DNS rebinding exposure, and missing egress restrictions."},
		SecurityScanTask{Name: "deserialization-and-parsing", Role: "vulnerability-hunter", Category: "deserialization", Objective: "Hunt for unsafe deserialization and parser abuse: untrusted data fed to deserializers, XML external entities, prototype pollution, and resource-exhausting parsers."},
		SecurityScanTask{Name: "access-control-and-multitenancy", Role: "vulnerability-hunter", Category: "authz", Objective: "Hunt for access-control and tenant-isolation flaws: insecure direct object references, missing ownership checks, and cross-tenant data or resource leakage."},
		SecurityScanTask{Name: "dependency-and-supply-chain", Role: "dependency-auditor", Category: "supply-chain", Objective: "Hunt for vulnerable or malicious dependencies, unpinned versions, typosquatting risk, and insecure build or release pipeline steps."},
		SecurityScanTask{Name: "infrastructure-and-configuration", Role: "vulnerability-hunter", Category: "misconfiguration", Objective: "Hunt for insecure infrastructure and configuration: overly permissive RBAC or IAM, exposed debug endpoints, missing security headers, and unsafe container or deployment settings."},
		SecurityScanTask{Name: "business-logic", Role: "vulnerability-hunter", Category: "logic-flaw", Objective: "Hunt for business-logic flaws: race conditions, state-machine bypasses, abuse of workflows (payments, quotas, invites), and missing server-side enforcement of invariants."},
	)
	dependsOn := make([]string, 0, len(tasks))
	for _, task := range tasks {
		dependsOn = append(dependsOn, task.Name)
	}
	return append(tasks, SecurityScanTask{
		Name:      "triage-and-report",
		Role:      "finding-triager",
		Category:  "triage",
		Objective: "Triage every reported finding: verify exploitability, remove false positives and duplicates, apply the ranking rules, then assemble and submit the final scan report.",
		DependsOn: dependsOn,
	})
}

// EffectiveWorkflow returns spec.workflow, or DefaultSecurityWorkflow() when
// it is empty.
func (s SecurityScanSpec) EffectiveWorkflow() []SecurityScanTask {
	if len(s.Workflow) > 0 {
		return s.Workflow
	}
	return DefaultSecurityWorkflow()
}

// EffectiveBaseBranch returns spec.baseBranch, defaulting to "main".
func (s SecurityScanSpec) EffectiveBaseBranch() string {
	if s.BaseBranch == "" {
		return "main"
	}
	return s.BaseBranch
}

// EffectiveParallelism returns spec.parallelism clamped to [1, 16], defaulting
// to 4 when unset.
func (s SecurityScanSpec) EffectiveParallelism() int32 {
	switch {
	case s.Parallelism == 0:
		return 4
	case s.Parallelism < 1:
		return 1
	case s.Parallelism > 16:
		return 16
	}
	return s.Parallelism
}

// EffectiveMinSeverity returns spec.minSeverity, defaulting to "low".
func (s SecurityScanSpec) EffectiveMinSeverity() string {
	if s.MinSeverity == "" {
		return "low"
	}
	return s.MinSeverity
}

// DedupeEnabled reports whether duplicate-finding suppression is on. Dedupe
// defaults to enabled.
func (s SecurityScanSpec) DedupeEnabled() bool {
	if s.Dedupe == nil || s.Dedupe.Enabled == nil {
		return true
	}
	return *s.Dedupe.Enabled
}

// DedupeSimilarityThresholdPermille returns the configured dedupe similarity
// threshold in permille, defaulting to 820 (0.82).
func (s SecurityScanSpec) DedupeSimilarityThresholdPermille() int32 {
	if s.Dedupe == nil || s.Dedupe.SimilarityThresholdPermille == 0 {
		return 820
	}
	return s.Dedupe.SimilarityThresholdPermille
}

// EffectiveRole returns the task's role, defaulting to DefaultSecurityScanRole.
func (t SecurityScanTask) EffectiveRole() string {
	if t.Role == "" {
		return DefaultSecurityScanRole
	}
	return t.Role
}

// EffectiveRunOn returns the post-script's runOn, defaulting to "all".
func (p SecurityPostScript) EffectiveRunOn() string {
	if p.RunOn == "" {
		return "all"
	}
	return p.RunOn
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repoURL`
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="LastScan",type=date,JSONPath=`.status.lastScanTime`
// +kubebuilder:printcolumn:name="Critical",type=integer,JSONPath=`.status.findings.critical`
// +kubebuilder:printcolumn:name="High",type=integer,JSONPath=`.status.findings.high`
// +kubebuilder:printcolumn:name="Findings",type=integer,JSONPath=`.status.findings.total`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SecurityScan is the Schema for the securityscans API.
type SecurityScan struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SecurityScanSpec `json:"spec"`

	// +optional
	Status SecurityScanStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SecurityScanList contains a list of SecurityScan.
type SecurityScanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SecurityScan `json:"items"`
}

// EffectiveProvider returns the normalized runtime provider for these run
// defaults: a provider prefix on model wins, with the legacy provider field
// as the fallback for specs that still set it.
func (d AgentRunDefaults) EffectiveProvider() string {
	if prefix, _ := SplitProviderModel(d.Model); prefix != "" {
		return prefix
	}
	return NormalizeProvider(d.Provider)
}

func init() {
	SchemeBuilder.Register(&SecurityScan{}, &SecurityScanList{})
}
