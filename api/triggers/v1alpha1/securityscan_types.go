/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
	// SecurityScanMaxFindingsAnnotation is the scan's effective
	// budgets.maxFindings (spec merged with the policy pack). Absent or "0"
	// means unlimited. The agent-side finding tools refuse to persist
	// findings once this cap is reached, so the platform-authored value is
	// the hard cap regardless of model output.
	SecurityScanMaxFindingsAnnotation = "security.gratefulagents.dev/max-findings"
)

// SecurityScanRunNowAnnotation is set on a SecurityScan (not its runs) by the
// dashboard to request an immediate manual run without editing the spec. Its
// value is an opaque request token; the controller creates at most one run per
// token and records the consumed token in status.lastManualRunToken, so the
// request is idempotent and durable across controller restarts.
const SecurityScanRunNowAnnotation = "security.gratefulagents.dev/run-now"

// SecurityScanResumeAnnotation is set on a SecurityScan (not its runs) to
// resume a failed deterministic execution. Its value is an opaque request
// token; the controller resets failed tasks for a fresh attempt at most once
// per token and records the consumed token in
// status.lastExecution.lastResumeToken, so the request is idempotent and
// durable across controller restarts.
const SecurityScanResumeAnnotation = "security.gratefulagents.dev/resume-scan"

// SecurityScanEventAnnotation is set on a SecurityScan (not its runs) by the
// GitHub webhook ingress when an authorized pull_request or push delivery
// matches the scan's spec.triggers. Its value is a JSON-encoded
// SecurityScanTriggerEvent whose token is derived deterministically from
// (repository, event kind, head SHA), so redeliveries carry the same token.
// The controller creates at most one run per token and records the consumed
// token in status.lastEventToken, making event processing idempotent and
// durable across controller restarts. The revision inside the payload is
// stamped by the platform from the webhook payload and is never
// model-controlled.
const SecurityScanEventAnnotation = "security.gratefulagents.dev/scan-event"

// SecurityScanStatusRefreshAnnotation is stamped on a SecurityScan by the
// dashboard after finding triage so the controller re-reconciles, refreshes
// finding counts, and re-publishes the GitHub check with the post-triage
// conclusion. Its value is an opaque timestamp; only inequality matters.
const SecurityScanStatusRefreshAnnotation = "security.gratefulagents.dev/status-refresh"

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
	// DefaultSecurityWorkflow(). Mutually exclusive with workflowRef: setting
	// both makes the controller report Ready=False with reason InvalidSpec.
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	// +optional
	Workflow []SecurityScanTask `json:"workflow,omitempty"`

	// workflowRef references a reusable SecurityWorkflow in the scan's
	// namespace whose tasks replace an inline workflow. The referenced
	// content is resolved and snapshotted when each run is created, so later
	// edits to the SecurityWorkflow never change historical runs. Mutually
	// exclusive with workflow.
	// +optional
	WorkflowRef *SecurityResourceRef `json:"workflowRef,omitempty"`

	// parallelism caps how many workflow tasks may run concurrently.
	// +kubebuilder:default=4
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16
	// +optional
	Parallelism int32 `json:"parallelism,omitempty"`

	// execution controls how the workflow DAG is executed.
	// +optional
	Execution *SecurityScanExecution `json:"execution,omitempty"`

	// parameterValues are values substituted for double-brace params.<name>
	// references in task objectives. The accepted names are declared by the
	// referenced SecurityWorkflow's parameters, or free-form for inline
	// workflows.
	// +kubebuilder:validation:MaxProperties=32
	// +optional
	ParameterValues map[string]string `json:"parameterValues,omitempty"`

	// severityRankers hold operator-authored ranking rule text concatenated
	// into the scan prompt and passed to submit_security_scan_report.
	// +listType=atomic
	// +optional
	SeverityRankers []SecurityScanRanker `json:"severityRankers,omitempty"`

	// rankerRefs reference reusable SecurityRanker resources in the scan's
	// namespace. Their rules are APPENDED after the inline severityRankers.
	// The referenced content is resolved and snapshotted when each run is
	// created.
	// +listType=atomic
	// +optional
	RankerRefs []SecurityResourceRef `json:"rankerRefs,omitempty"`

	// postScripts are per-finding validation, proof-of-concept, or report
	// prompts executed after the workflow produces findings.
	// +listType=atomic
	// +optional
	PostScripts []SecurityScanPostScript `json:"postScripts,omitempty"`

	// postScriptRefs reference reusable SecurityPostScript resources in the
	// scan's namespace. They are APPENDED after the inline postScripts. The
	// referenced content is resolved and snapshotted when each run is
	// created.
	// +listType=atomic
	// +optional
	PostScriptRefs []SecurityResourceRef `json:"postScriptRefs,omitempty"`

	// policyPackRef references a SecurityPolicyPack in the scan's namespace.
	// The pack supplies defaults (precedence: platform defaults < policy
	// pack < scan configuration), enforced floors the scan may not relax,
	// and governed finding suppressions. It is resolved and snapshotted at
	// run-creation time; a scan violating an enforced pack field is rejected
	// with Ready=False reason PolicyViolation and no run is created.
	// +optional
	PolicyPackRef *SecurityResourceRef `json:"policyPackRef,omitempty"`

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

	// budgets caps what each scan run may consume. Unset fields inherit the
	// referenced policy pack's budgets; when the pack lists "budgets" in
	// enforced, this scan may not raise any limit the pack sets. Enforcement
	// is entirely platform-side: what the run can self-limit (runtime, cost)
	// is written into the created AgentRun's limits, everything else (model
	// jobs, tokens, findings, validation jobs) is monitored by the
	// controller from platform-observed usage and the run is cancelled when
	// a hard limit is exceeded. Completed work is preserved.
	// +optional
	Budgets *SecurityScanBudgets `json:"budgets,omitempty"`

	// triggers configures repository-event driven scan runs: authorized
	// GitHub pull_request and push webhook deliveries for the referenced
	// GitHubRepository create scan runs pinned to the event's head commit.
	// +optional
	Triggers *SecurityScanTriggers `json:"triggers,omitempty"`

	// checks configures publishing a GitHub check on the scanned commit
	// after each scan run with a recorded revision reaches a terminal phase.
	// +optional
	Checks *SecurityScanChecks `json:"checks,omitempty"`

	// notifications are rules that send Slack messages and/or create
	// GitHub/Linear issues for new or regressed findings at or above a
	// severity threshold. Each (finding fingerprint, rule, channel) notifies
	// at most once; the sent marker is persisted in the findings store.
	// +listType=atomic
	// +optional
	Notifications []SecurityScanNotificationRule `json:"notifications,omitempty"`
}

// SecurityScanTriggers configures repository-event driven scan runs.
type SecurityScanTriggers struct {
	// repositoryRef names the GitHubRepository in the scan's namespace whose
	// webhook deliveries trigger this scan and whose credentials publish
	// checks and read diffs. Required when onPullRequest or onPush is set.
	// +optional
	RepositoryRef *SecurityResourceRef `json:"repositoryRef,omitempty"`

	// onPullRequest creates a scan run for pull_request opened, reopened,
	// ready_for_review, and synchronize deliveries, pinned to the PR head SHA.
	// +optional
	OnPullRequest bool `json:"onPullRequest,omitempty"`

	// onPush creates a scan run for push deliveries, pinned to the pushed
	// head SHA.
	// +optional
	OnPush bool `json:"onPush,omitempty"`

	// branches restricts push triggers to branches matching any of these
	// glob patterns (path.Match syntax; a trailing "*" acts as a prefix
	// match). Empty matches every branch.
	// +listType=atomic
	// +optional
	Branches []string `json:"branches,omitempty"`

	// diffScope scopes event-triggered scans to the files changed between
	// the merge base and the head (pull requests) or the push's
	// before..after range. When the diff cannot be computed the scan falls
	// back to the full repository and the fallback is stated in the run
	// prompt and the scan condition.
	// +optional
	DiffScope bool `json:"diffScope,omitempty"`

	// allowForks permits scan runs for pull requests whose head repository
	// differs from the base repository. Fork runs never receive the
	// repository's GitHub credentials: the configured GitHub token secret is
	// stripped from the run so untrusted contributions cannot exfiltrate
	// write tokens. Default false: fork PRs are skipped with an observable
	// condition and event.
	// +optional
	AllowForks bool `json:"allowForks,omitempty"`
}

// SecurityScanChecks configures GitHub check publishing for scan runs.
type SecurityScanChecks struct {
	// enabled turns on check publishing. Requires spec.triggers.repositoryRef
	// for credentials.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// includeFindingSummaries opts in to listing finding titles and file
	// locations in the check summary. The default summary contains only
	// severity counts and a dashboard link; evidence and proof-of-concept
	// content is never published in either mode.
	// +optional
	IncludeFindingSummaries bool `json:"includeFindingSummaries,omitempty"`

	// uploadSARIF opts in to uploading the scan's stored SARIF report
	// artifact to GitHub code scanning for the scanned commit.
	// +optional
	UploadSARIF bool `json:"uploadSARIF,omitempty"`
}

// SecurityScanNotificationRule routes new/regressed findings at or above a
// severity threshold to Slack, GitHub issues, and/or Linear issues.
type SecurityScanNotificationRule struct {
	// name identifies the rule; it keys the persisted per-finding dedupe
	// marker, so renaming a rule re-notifies.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// minSeverity is the lowest severity that notifies. Defaults to "high".
	// +kubebuilder:validation:Enum=critical;high;medium;low;info
	// +optional
	MinSeverity string `json:"minSeverity,omitempty"`

	// notifyOn selects which baseline states notify. Defaults to
	// "new-and-regressed".
	// +kubebuilder:validation:Enum=new;regressed;new-and-regressed
	// +optional
	NotifyOn string `json:"notifyOn,omitempty"`

	// slack, when set, posts one message per run summarizing the newly
	// notified findings to a Slack incoming webhook.
	// +optional
	Slack *SecurityScanSlackNotification `json:"slack,omitempty"`

	// githubIssues, when set, creates one GitHub issue per newly notified
	// finding using the referenced repository's credentials. Issue bodies
	// contain identifying metadata and a dashboard link, never evidence.
	// +optional
	GitHubIssues *SecurityScanGitHubIssueNotification `json:"githubIssues,omitempty"`

	// linear, when set, creates one Linear issue per newly notified finding.
	// +optional
	Linear *SecurityScanLinearNotification `json:"linear,omitempty"`
}

// SecurityScanSlackNotification posts to a Slack incoming webhook.
type SecurityScanSlackNotification struct {
	// webhookSecretRef names a Secret in the scan's namespace holding the
	// Slack incoming-webhook URL under the key "url".
	// +kubebuilder:validation:MinLength=1
	WebhookSecretRef string `json:"webhookSecretRef"`
}

// SecurityScanGitHubIssueNotification creates GitHub issues for findings.
type SecurityScanGitHubIssueNotification struct {
	// repositoryRef names the GitHubRepository (same namespace) whose
	// credentials create the issues. When empty, spec.triggers.repositoryRef
	// is used.
	// +optional
	RepositoryRef *SecurityResourceRef `json:"repositoryRef,omitempty"`
}

// SecurityScanLinearNotification creates Linear issues for findings.
type SecurityScanLinearNotification struct {
	// apiKeySecretRef names a Secret in the scan's namespace holding a
	// Linear API key under the key "api-key".
	// +kubebuilder:validation:MinLength=1
	APIKeySecretRef string `json:"apiKeySecretRef"`

	// teamID is the Linear team the issues are created in.
	// +kubebuilder:validation:MinLength=1
	TeamID string `json:"teamID"`
}

// EffectiveMinSeverity returns the rule's minSeverity, defaulting to "high".
func (r SecurityScanNotificationRule) EffectiveMinSeverity() string {
	if r.MinSeverity == "" {
		return "high"
	}
	return r.MinSeverity
}

// EffectiveNotifyOn returns the rule's notifyOn, defaulting to
// "new-and-regressed".
func (r SecurityScanNotificationRule) EffectiveNotifyOn() string {
	if r.NotifyOn == "" {
		return "new-and-regressed"
	}
	return r.NotifyOn
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

	// maxRetries is this task's retry budget in deterministic execution:
	// how many times a failed attempt is rescheduled before the task is
	// marked Failed. Nil inherits spec.execution.taskMaxRetries (default 1).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	// +optional
	MaxRetries *int32 `json:"maxRetries,omitempty"`

	// timeout is a hard per-task runtime limit in deterministic execution,
	// mapped to the task run's spec.limits.maxRuntime. Zero means no
	// task-level limit.
	// +optional
	Timeout metav1.Duration `json:"timeout,omitempty"`

	// maxTurns is a hard per-task model-turn budget in deterministic
	// execution, mapped to the task run's spec.limits.maxTurns. Zero means
	// no task-level limit.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxTurns int32 `json:"maxTurns,omitempty"`

	// maxCostUSD is a decimal USD ceiling (e.g. "5" or "2.50") on this
	// task's LLM spend in deterministic execution, mapped to the task run's
	// spec.limits.maxCostUsd. Empty means no task-level ceiling.
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?)?$`
	// +optional
	MaxCostUSD string `json:"maxCostUSD,omitempty"`

	// tools narrows which tools this task's run may use in deterministic
	// execution, mapped to the task run's spec.toolPolicy. It can only
	// narrow, never widen, tool access.
	// +optional
	Tools *SecurityScanTaskTools `json:"tools,omitempty"`

	// outputSchema is an optional JSON Schema (object form) contract for
	// this task's structured output. Tasks with a schema must publish their
	// output via the submit_task_output tool; dependents consume it through
	// double-brace tasks.<name>... template references.
	// +kubebuilder:validation:MaxLength=16384
	// +optional
	OutputSchema string `json:"outputSchema,omitempty"`

	// forEach names a dependency task; this task fans out with one instance
	// per record of that task's JSON-array structured output. Records are
	// exposed through double-brace item or item.<field> references. The
	// named task must be listed in dependsOn and must declare outputSchema.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	ForEach string `json:"forEach,omitempty"`

	// maxInstances caps forEach fan-out instances. Zero defaults to 10.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=50
	// +optional
	MaxInstances int32 `json:"maxInstances,omitempty"`

	// repeats configures ensemble execution: run this many identical
	// instances of the task and let dependents consume all of their
	// outputs. Zero or one means a single instance.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=5
	// +optional
	Repeats int32 `json:"repeats,omitempty"`
}

// SecurityScanTaskTools narrows which tools a task's run may use in
// deterministic execution. It is mapped to the task run's spec.toolPolicy
// and can only narrow, never widen, tool access.
type SecurityScanTaskTools struct {
	// allowed, when non-empty, restricts the task's run to these tool names
	// (unknown names are ignored).
	// +listType=atomic
	// +optional
	Allowed []string `json:"allowed,omitempty"`

	// denied tools are removed even when allowed; deny wins.
	// +listType=atomic
	// +optional
	Denied []string `json:"denied,omitempty"`
}

// Execution modes accepted by SecurityScanExecution.Mode.
const (
	// SecurityScanExecutionModeCoordinator seeds a single orchestrating run
	// that delegates to in-process sub-agents.
	SecurityScanExecutionModeCoordinator = "coordinator"
	// SecurityScanExecutionModeDeterministic compiles the workflow into
	// controller-scheduled per-task AgentRuns.
	SecurityScanExecutionModeDeterministic = "deterministic"
)

// SecurityScanExecution controls how the workflow DAG is executed.
type SecurityScanExecution struct {
	// mode selects the execution engine. "coordinator" (default) seeds a
	// single orchestrating run that delegates to in-process sub-agents;
	// "deterministic" compiles the workflow into controller-scheduled
	// per-task AgentRuns with platform-enforced dependencies, retries,
	// budgets, and concurrency.
	// +kubebuilder:validation:Enum=coordinator;deterministic
	// +optional
	Mode string `json:"mode,omitempty"`

	// taskMaxRetries is the default per-task retry budget for tasks that do
	// not set maxRetries (default 1).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	// +optional
	TaskMaxRetries *int32 `json:"taskMaxRetries,omitempty"`

	// retryBackoff is the base delay before a failed task attempt is
	// rescheduled; it doubles per attempt and is capped at 15 minutes.
	// Default 30s. Rate-limited failures always wait at least this long.
	// +optional
	RetryBackoff metav1.Duration `json:"retryBackoff,omitempty"`
}

// SecurityScanRanker is operator-authored severity-ranking rule text.
type SecurityScanRanker struct {
	// name identifies the ranker.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// rules is the ranking rule text concatenated into the scan prompt and
	// passed to submit_security_scan_report.
	// +kubebuilder:validation:MinLength=1
	Rules string `json:"rules"`
}

// SecurityScanPostScript is a per-finding validation, proof-of-concept, or report
// prompt executed after findings are collected.
type SecurityScanPostScript struct {
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

// SecurityScanBudgets caps what one scan run may consume. Zero/empty fields
// are unlimited (or inherit the referenced policy pack's value). All limits
// derive from the CRD spec and are enforced platform-side: what the AgentRun
// supports natively (maxRuntime, maxCostUSD) is written into the created
// run's limits; everything else is monitored by the controller from
// platform-observed usage data, never from model output.
type SecurityScanBudgets struct {
	// maxModelJobs caps how many sub-agent runs (child AgentRuns) the scan
	// run may spawn. Monitored controller-side from the run's child status.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxModelJobs int32 `json:"maxModelJobs,omitempty"`

	// maxCostUSD is a decimal USD ceiling (e.g. "5" or "2.50") on the scan
	// run's LLM spend. Written into the created AgentRun's
	// limits.maxCostUsd and re-checked controller-side from the run's usage
	// metrics.
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?)?$`
	// +optional
	MaxCostUSD string `json:"maxCostUSD,omitempty"`

	// maxTokens caps the scan run's total LLM tokens (input + output).
	// Monitored controller-side from the run's usage metrics.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxTokens int64 `json:"maxTokens,omitempty"`

	// maxRuntime caps the scan run's wall-clock runtime. Written into the
	// created AgentRun's limits.maxRuntime; the smallest of this,
	// spec.maxRuntime, and defaults.timeout wins.
	// +optional
	MaxRuntime metav1.Duration `json:"maxRuntime,omitempty"`

	// maxFindings caps how many findings the scan may persist. Enforced by
	// controller-side monitoring of the persisted finding count (the cap is
	// also stated in the run prompt as guidance, but model output is never
	// trusted for enforcement).
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxFindings int32 `json:"maxFindings,omitempty"`

	// maxValidationJobs caps how many post-script (validation /
	// proof-of-concept) sub-agent runs the scan run may spawn. Monitored
	// controller-side from the run's child status.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxValidationJobs int32 `json:"maxValidationJobs,omitempty"`
}

// IsZero reports whether no budget field is set.
func (b SecurityScanBudgets) IsZero() bool {
	return b == SecurityScanBudgets{}
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

// Task states reported in SecurityScanTaskExecutionStatus.State.
const (
	// SecurityScanTaskStatePending means the task is ready to be scheduled.
	SecurityScanTaskStatePending = "Pending"
	// SecurityScanTaskStateBlocked means the task waits on dependencies.
	SecurityScanTaskStateBlocked = "Blocked"
	// SecurityScanTaskStateRunning means the task's AgentRun is active.
	SecurityScanTaskStateRunning = "Running"
	// SecurityScanTaskStateSucceeded means the task finished successfully.
	SecurityScanTaskStateSucceeded = "Succeeded"
	// SecurityScanTaskStateFailed means the task exhausted its retry budget.
	SecurityScanTaskStateFailed = "Failed"
	// SecurityScanTaskStateSkipped means the task never ran (e.g. a
	// dependency failed).
	SecurityScanTaskStateSkipped = "Skipped"
)

// Failure classes reported in SecurityScanTaskAttempt.Class.
const (
	// SecurityScanTaskFailureRetryable marks a failure worth retrying.
	SecurityScanTaskFailureRetryable = "retryable"
	// SecurityScanTaskFailureNonRetryable marks a failure that consumes the
	// task immediately regardless of remaining retry budget.
	SecurityScanTaskFailureNonRetryable = "non-retryable"
)

// Execution phases reported in SecurityScanExecutionStatus.Phase.
const (
	// SecurityScanExecutionPhaseRunning means tasks are still executing.
	SecurityScanExecutionPhaseRunning = "Running"
	// SecurityScanExecutionPhaseSucceeded means every task succeeded.
	SecurityScanExecutionPhaseSucceeded = "Succeeded"
	// SecurityScanExecutionPhaseFailed means at least one task failed
	// terminally.
	SecurityScanExecutionPhaseFailed = "Failed"
	// SecurityScanExecutionPhaseResuming is reserved for a resume request
	// that is resetting failed tasks. The controller does not set it today:
	// consuming a resume token flips a Failed execution straight back to
	// Running.
	SecurityScanExecutionPhaseResuming = "Resuming"
)

// SecurityScanTaskAttempt records one finished attempt of a task instance in
// deterministic execution.
type SecurityScanTaskAttempt struct {
	// runName is the AgentRun that served this attempt.
	// +optional
	RunName string `json:"runName,omitempty"`

	// startedAt is when the attempt's run was created.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// finishedAt is when the attempt reached a terminal phase.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// reason summarizes why the attempt failed.
	// +optional
	Reason string `json:"reason,omitempty"`

	// class is the failure classification: retryable or non-retryable.
	// +optional
	Class string `json:"class,omitempty"`
}

// SecurityScanTaskExecutionStatus is the observed state of one task instance
// in deterministic execution. State is one of Pending, Blocked, Running,
// Succeeded, Failed, or Skipped.
type SecurityScanTaskExecutionStatus struct {
	// name is the workflow task name.
	// +optional
	Name string `json:"name,omitempty"`

	// instance distinguishes forEach fan-out and ensemble-repeat instances
	// of the same task (0-based).
	// +optional
	Instance int32 `json:"instance,omitempty"`

	// state is the task instance's current state: Pending, Blocked,
	// Running, Succeeded, Failed, or Skipped.
	// +optional
	State string `json:"state,omitempty"`

	// runName is the AgentRun currently or most recently serving this task
	// instance.
	// +optional
	RunName string `json:"runName,omitempty"`

	// attempts is how many attempts have started for this task instance.
	// It is cumulative across resume cycles so budgets.maxModelJobs
	// accounting never forgets prior runs.
	// +optional
	Attempts int32 `json:"attempts,omitempty"`

	// resumeBaselineAttempts is the value of attempts when the execution
	// was last resumed. The per-cycle retry budget is attempts minus this
	// baseline, so resuming refreshes retries without resetting the
	// durable attempts counter.
	// +optional
	ResumeBaselineAttempts int32 `json:"resumeBaselineAttempts,omitempty"`

	// retries records the finished failed attempts.
	// +listType=atomic
	// +optional
	Retries []SecurityScanTaskAttempt `json:"retries,omitempty"`

	// nextRetryTime is when the next attempt may be scheduled after a
	// retryable failure.
	// +optional
	NextRetryTime *metav1.Time `json:"nextRetryTime,omitempty"`

	// lastError summarizes the most recent failure.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// startedAt is when the first attempt started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// finishedAt is when the task instance reached a terminal state.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

// SecurityScanExecutionStatus is the observed state of one deterministic
// workflow execution.
type SecurityScanExecutionStatus struct {
	// id is the external id / run-suffix of the scan invocation this
	// execution belongs to.
	// +optional
	ID string `json:"id,omitempty"`

	// mode is the execution engine used: coordinator or deterministic.
	// +optional
	Mode string `json:"mode,omitempty"`

	// phase is the execution's coarse state: Running, Succeeded, or Failed
	// (resuming a failed execution sets it back to Running).
	// +optional
	Phase string `json:"phase,omitempty"`

	// effectiveParallelism is the concurrency bound actually applied.
	// +optional
	EffectiveParallelism int32 `json:"effectiveParallelism,omitempty"`

	// effectiveParallelismNote explains how the bound was derived (e.g.
	// clamped by the mode template's sub-agent ceiling).
	// +optional
	EffectiveParallelismNote string `json:"effectiveParallelismNote,omitempty"`

	// tasks is the per-task-instance execution state.
	// +listType=atomic
	// +optional
	Tasks []SecurityScanTaskExecutionStatus `json:"tasks,omitempty"`

	// startedAt is when the execution started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// completedAt is when the execution reached a terminal phase.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// lastResumeToken is the most recent resume-scan annotation token the
	// controller has processed, making resume requests idempotent.
	// +optional
	LastResumeToken string `json:"lastResumeToken,omitempty"`
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

	// lastManualRunToken is the most recent run-now annotation token the
	// controller has processed. A token equal to this value never creates
	// another run, making manual run requests idempotent across controller
	// restarts.
	// +optional
	LastManualRunToken string `json:"lastManualRunToken,omitempty"`

	// manualRunsCreated is the cumulative number of AgentRuns created from
	// run-now requests (a subset of runsCreated).
	// +optional
	ManualRunsCreated int32 `json:"manualRunsCreated,omitempty"`

	// lastEventToken is the most recent scan-event annotation token the
	// controller has processed. A token equal to this value never creates
	// another run, making repository-event triggers idempotent across
	// webhook redeliveries and controller restarts.
	// +optional
	LastEventToken string `json:"lastEventToken,omitempty"`

	// lastEventRevision is the platform-stamped head SHA of the most recent
	// repository event that created (or attempted) a scan run. It matches
	// the commit any published check is reported on.
	// +optional
	LastEventRevision string `json:"lastEventRevision,omitempty"`

	// eventRunsCreated is the cumulative number of AgentRuns created from
	// repository events (a subset of runsCreated).
	// +optional
	EventRunsCreated int32 `json:"eventRunsCreated,omitempty"`

	// lastCheck records the most recent GitHub check publish attempt.
	// +optional
	LastCheck *SecurityScanCheckStatus `json:"lastCheck,omitempty"`

	// lastNotifications records the most recent notification delivery
	// attempt.
	// +optional
	LastNotifications *SecurityScanNotificationStatus `json:"lastNotifications,omitempty"`

	// lastExecution is the observed state of the most recent deterministic
	// workflow execution.
	// +optional
	LastExecution *SecurityScanExecutionStatus `json:"lastExecution,omitempty"`

	// findings summarizes persisted findings for the most recent scan run.
	// +optional
	Findings *SecurityScanFindingCounts `json:"findings,omitempty"`

	// retention reports the most recent retention sweep run for this scan's
	// policy pack retention configuration.
	// +optional
	Retention *SecurityScanRetentionStatus `json:"retention,omitempty"`

	// budget reports the scan's effective budgets (spec merged with the
	// policy pack) and whether any hard limit is exceeded. It is computed
	// from platform-observed usage data before and during each run, so the
	// dashboard can warn ahead of a launch.
	// +optional
	Budget *SecurityScanBudgetStatus `json:"budget,omitempty"`

	// lastResolvedRefs records the reusable security resources
	// (SecurityWorkflow, SecurityRanker, SecurityPostScript) that were
	// resolved and snapshotted into the most recently created run, including
	// the resource generation and a content hash of the resolved spec. Later
	// edits to the referenced resources never change historical runs.
	// +listType=atomic
	// +optional
	LastResolvedRefs []SecurityScanResolvedRef `json:"lastResolvedRefs,omitempty"`

	// lastError contains the error message from the most recent failed operation.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// conditions represent the current state of the SecurityScan trigger.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// SecurityScanRetentionStatus reports the retention purge sweep. Counters
// are cumulative for this SecurityScan resource.
type SecurityScanRetentionStatus struct {
	// lastSweepTime is when the most recent purge batch ran.
	// +optional
	LastSweepTime *metav1.Time `json:"lastSweepTime,omitempty"`

	// scansPurged counts deleted scan run records (incl. observation rows).
	// +optional
	ScansPurged int64 `json:"scansPurged,omitempty"`

	// findingsPurged counts deleted finding rows.
	// +optional
	FindingsPurged int64 `json:"findingsPurged,omitempty"`

	// reportsPurged counts deleted report artifacts (markdown/SARIF).
	// +optional
	ReportsPurged int64 `json:"reportsPurged,omitempty"`

	// evidenceRedacted counts findings whose evidence was redacted in place.
	// +optional
	EvidenceRedacted int64 `json:"evidenceRedacted,omitempty"`

	// pocRedacted counts findings whose PoC content was redacted in place.
	// +optional
	PoCRedacted int64 `json:"pocRedacted,omitempty"`

	// auditEventsPurged counts deleted finding audit events.
	// +optional
	AuditEventsPurged int64 `json:"auditEventsPurged,omitempty"`

	// moreWork reports whether the last batch hit its bound; the controller
	// requeues promptly while true.
	// +optional
	MoreWork bool `json:"moreWork,omitempty"`

	// lastError is the most recent sweep failure, empty after a clean batch.
	// +optional
	LastError string `json:"lastError,omitempty"`
}

// SecurityScanBudgetStatus reports the effective budgets and their
// evaluation against platform-observed usage.
type SecurityScanBudgetStatus struct {
	// effective is the budget set actually enforced: scan spec budgets
	// merged with the policy pack's defaults.
	// +optional
	Effective *SecurityScanBudgets `json:"effective,omitempty"`

	// exceeded reports whether any hard budget limit is exceeded.
	// +optional
	Exceeded bool `json:"exceeded,omitempty"`

	// message explains which limit is exceeded and by how much.
	// +optional
	Message string `json:"message,omitempty"`

	// lastCheckedTime is when the budgets were last evaluated.
	// +optional
	LastCheckedTime *metav1.Time `json:"lastCheckedTime,omitempty"`
}

// Condition types for SecurityScan.
const (
	ConditionSecurityScanReady = "Ready"
)

// SecurityScanCheckStatus records the most recent GitHub check publish
// attempt for a scan run.
type SecurityScanCheckStatus struct {
	// runName is the AgentRun the check reports on.
	// +optional
	RunName string `json:"runName,omitempty"`

	// revision is the commit SHA the check was published on.
	// +optional
	Revision string `json:"revision,omitempty"`

	// conclusion is the published check conclusion: success, failure, or
	// neutral.
	// +optional
	Conclusion string `json:"conclusion,omitempty"`

	// url links to the published check run or commit status target.
	// +optional
	URL string `json:"url,omitempty"`

	// publishedAt is when the check was last successfully published.
	// +optional
	PublishedAt *metav1.Time `json:"publishedAt,omitempty"`

	// stateHash fingerprints the published (run, revision, conclusion,
	// counts) tuple; a differing desired state triggers a re-publish, e.g.
	// after findings are triaged.
	// +optional
	StateHash string `json:"stateHash,omitempty"`

	// error is the most recent publish failure; empty after a successful
	// publish. Failures are retried on subsequent reconciles.
	// +optional
	Error string `json:"error,omitempty"`

	// sarifUploaded reports whether the run's SARIF artifact was uploaded to
	// GitHub code scanning.
	// +optional
	SARIFUploaded bool `json:"sarifUploaded,omitempty"`

	// sarifError is the most recent SARIF upload failure, if any.
	// +optional
	SARIFError string `json:"sarifError,omitempty"`
}

// SecurityScanNotificationStatus records the most recent notification
// delivery attempt.
type SecurityScanNotificationStatus struct {
	// lastRunName is the AgentRun whose findings were last evaluated.
	// +optional
	LastRunName string `json:"lastRunName,omitempty"`

	// sent is the cumulative number of notifications delivered.
	// +optional
	Sent int32 `json:"sent,omitempty"`

	// suppressed is the cumulative number of findings skipped because their
	// (rule, channel, fingerprint) marker was already persisted.
	// +optional
	Suppressed int32 `json:"suppressed,omitempty"`

	// lastError is the most recent delivery failure; empty after a fully
	// successful evaluation. Failed deliveries release their dedupe claim
	// and are retried on subsequent reconciles.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// lastNotifiedAt is when a notification was last delivered.
	// +optional
	LastNotifiedAt *metav1.Time `json:"lastNotifiedAt,omitempty"`
}

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

// EffectiveExecutionMode returns spec.execution.mode, defaulting to
// "coordinator".
func (s SecurityScanSpec) EffectiveExecutionMode() string {
	if s.Execution == nil || s.Execution.Mode == "" {
		return SecurityScanExecutionModeCoordinator
	}
	return s.Execution.Mode
}

// EffectiveTaskMaxRetries returns the task's retry budget: task.maxRetries
// when set, otherwise spec.execution.taskMaxRetries, otherwise 1.
func (s SecurityScanSpec) EffectiveTaskMaxRetries(task SecurityScanTask) int32 {
	if task.MaxRetries != nil {
		return *task.MaxRetries
	}
	if s.Execution != nil && s.Execution.TaskMaxRetries != nil {
		return *s.Execution.TaskMaxRetries
	}
	return 1
}

// EffectiveRetryBackoff returns spec.execution.retryBackoff, defaulting to
// 30 seconds.
func (s SecurityScanSpec) EffectiveRetryBackoff() time.Duration {
	if s.Execution == nil || s.Execution.RetryBackoff.Duration <= 0 {
		return 30 * time.Second
	}
	return s.Execution.RetryBackoff.Duration
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

// EffectiveMaxInstances returns the task's forEach fan-out cap, defaulting to
// 10 when maxInstances is zero.
func (t SecurityScanTask) EffectiveMaxInstances() int32 {
	if t.MaxInstances == 0 {
		return 10
	}
	return t.MaxInstances
}

// EffectiveRepeats returns how many identical instances of the task run as
// an ensemble. Repeats <= 1 means a single instance.
func (t SecurityScanTask) EffectiveRepeats() int32 {
	if t.Repeats <= 1 {
		return 1
	}
	return t.Repeats
}

// EffectiveRunOn returns the post-script's runOn, defaulting to "all".
func (p SecurityScanPostScript) EffectiveRunOn() string {
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
