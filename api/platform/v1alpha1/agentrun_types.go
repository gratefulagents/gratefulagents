/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentRunWorkflowMode is retained for persisted-resource compatibility. New
// runs use auto; legacy chat values are normalized to autonomous pacing.
// +kubebuilder:validation:Enum=chat;auto
type AgentRunWorkflowMode string

const (
	// WorkflowModeChat is a deprecated compatibility value treated as auto.
	WorkflowModeChat AgentRunWorkflowMode = "chat"
	WorkflowModeAuto AgentRunWorkflowMode = "auto"
)

// AgentRunExecutionMode controls how a run is orchestrated internally.
// +kubebuilder:validation:Enum=linear;team
type AgentRunExecutionMode string

const (
	ExecutionModeLinear AgentRunExecutionMode = "linear"
	ExecutionModeTeam   AgentRunExecutionMode = "team"
)

// AgentRunAuthMode controls provider credential mode.
// +kubebuilder:validation:Enum=api-key;oauth
type AgentRunAuthMode string

const (
	AgentRunAuthModeAPIKey AgentRunAuthMode = "api-key"
	AgentRunAuthModeOAuth  AgentRunAuthMode = "oauth"
)

// AgentRunPhase is the observed lifecycle of an AgentRun.
// +kubebuilder:validation:Enum=Pending;Admitted;WaitingApproval;Provisioning;Running;Question;Blocked;Paused;Succeeded;Failed;Cancelled
type AgentRunPhase string

const (
	AgentRunPhasePending         AgentRunPhase = "Pending"
	AgentRunPhaseAdmitted        AgentRunPhase = "Admitted"
	AgentRunPhaseWaitingApproval AgentRunPhase = "WaitingApproval"
	AgentRunPhaseProvisioning    AgentRunPhase = "Provisioning"
	AgentRunPhaseRunning         AgentRunPhase = "Running"
	AgentRunPhaseQuestion        AgentRunPhase = "Question"
	AgentRunPhaseBlocked         AgentRunPhase = "Blocked"
	AgentRunPhasePaused          AgentRunPhase = "Paused"
	AgentRunPhaseSucceeded       AgentRunPhase = "Succeeded"
	AgentRunPhaseFailed          AgentRunPhase = "Failed"
	AgentRunPhaseCancelled       AgentRunPhase = "Cancelled"
)

// UserInputRequestType classifies what kind of input the agent needs from the user.
// +kubebuilder:validation:Enum=question;approval;plan_review;turn_limit;idle;circuit_breaker;stopped
type UserInputRequestType string

const (
	UserInputQuestion     UserInputRequestType = "question"
	UserInputApproval     UserInputRequestType = "approval"
	UserInputPlanReview   UserInputRequestType = "plan_review"
	UserInputTurnLimit    UserInputRequestType = "turn_limit"
	UserInputIdle         UserInputRequestType = "idle"
	UserInputCircuitBreak UserInputRequestType = "circuit_breaker"
	UserInputStopped      UserInputRequestType = "stopped"
)

// Overseer annotations carry lifecycle state and the verdict context emitted
// by an overseer run.
const (
	OverseerVerdictAnnotation       = "platform.gratefulagents.dev/overseer-verdict"
	OverseerGuidanceAnnotation      = "platform.gratefulagents.dev/overseer-guidance"
	OverseerSummaryAnnotation       = "platform.gratefulagents.dev/overseer-summary"
	OverseerInputResponseAnnotation = "platform.gratefulagents.dev/overseer-input-response"
	OverseerDetachingAnnotation     = "platform.gratefulagents.dev/overseer-detaching"
)

// Overseer verdict values stored in OverseerVerdictAnnotation.
const (
	OverseerVerdictAllClear         = "all_clear"
	OverseerVerdictSteer            = "steer"
	OverseerVerdictRejectCompletion = "reject_completion"
	OverseerVerdictResolveInput     = "resolve_input"
	OverseerVerdictEscalate         = "escalate"
)

// OverseerInputResponse binds a controller-mediated response to the exact
// pending request observed by the overseer. It is serialized in
// OverseerInputResponseAnnotation on the standing overseer run.
type OverseerInputResponse struct {
	RequestID string `json:"request_id"`
	ActionID  string `json:"action_id,omitempty"`
	Response  string `json:"response,omitempty"`
}

// Autonomous PR-review loop annotations. The reviewer run records its verdict
// via the submit_review_verdict tool; the loop engine reads it on completion.
const (
	// ReviewVerdictAnnotation holds the reviewer run's verdict:
	// "approve" or "request_changes".
	ReviewVerdictAnnotation = "platform.gratefulagents.dev/review-verdict"
	// ReviewSummaryAnnotation holds the reviewer run's one-paragraph summary.
	ReviewSummaryAnnotation = "platform.gratefulagents.dev/review-summary"
)

// Git commit annotations. Stamped on user-created runs from the creating
// user's saved git settings and inherited by team child runs.
const (
	// GitAuthorNameAnnotation holds the commit author name for the run.
	GitAuthorNameAnnotation = "platform.gratefulagents.dev/git-author-name"
	// GitAuthorEmailAnnotation holds the commit author email for the run.
	GitAuthorEmailAnnotation = "platform.gratefulagents.dev/git-author-email"
)

// AgentRunCleanupFinalizer keeps AgentRun resources around long enough for the
// controller to delete all run-scoped database state and cluster resources.
const AgentRunCleanupFinalizer = "platform.gratefulagents.dev/cleanup"

// Review verdict values stored in ReviewVerdictAnnotation.
const (
	ReviewVerdictApprove        = "approve"
	ReviewVerdictRequestChanges = "request_changes"
)

// AgentRunTeamStepType is the bounded phase-1 team step contract.
// +kubebuilder:validation:Enum=serial;parallel;approval_gate
type AgentRunTeamStepType string

const (
	TeamStepTypeSerial       AgentRunTeamStepType = "serial"
	TeamStepTypeParallel     AgentRunTeamStepType = "parallel"
	TeamStepTypeApprovalGate AgentRunTeamStepType = "approval_gate"
)

// ArtifactRef references a keyed object-backed artifact.
type ArtifactRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// +optional
	Key string `json:"key,omitempty"`
}

// NamedRef references another namespaced resource by name.
type NamedRef struct {
	Name string `json:"name"`
}

// ExternalRef carries source-system identity for a run trigger.
type ExternalRef struct {
	// +optional
	ID string `json:"id,omitempty"`
	// +optional
	Identifier string `json:"identifier,omitempty"`
	// +optional
	URL string `json:"url,omitempty"`
}

// TriggerRef describes the source that created the run.
type TriggerRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	// +optional
	ExternalRef *ExternalRef `json:"externalRef,omitempty"`
}

// RepositoryContext identifies the target repo state.
type RepositoryContext struct {
	URL string `json:"url"`
	// +optional
	BaseBranch string `json:"baseBranch,omitempty"`
	// +optional
	BranchName string `json:"branchName,omitempty"`
	// +optional
	Revision string `json:"revision,omitempty"`
	// AdditionalRepos lists extra git repository URLs cloned into the run's
	// sandbox at startup, under repos/<name> next to the primary repository.
	// +listType=atomic
	// +optional
	AdditionalRepos []string `json:"additionalRepos,omitempty"`
}

// ProjectRef carries app/project-level context for chat-first requests.
type ProjectRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// AgentRunContext carries non-repository scoped context.
type AgentRunContext struct {
	// +optional
	ProjectRef *ProjectRef `json:"projectRef,omitempty"`
}

// AgentRunLimits defines bounded runtime limits.
type AgentRunLimits struct {
	// +optional
	MaxTurns int32 `json:"maxTurns,omitempty"`
	// +optional
	MaxRuntime metav1.Duration `json:"maxRuntime,omitempty"`
	// +optional
	MaxRetries int32 `json:"maxRetries,omitempty"`
	// maxCostUsd is a decimal USD ceiling (e.g. "5" or "2.50") on LLM spend
	// for one provisioning session of this run. When the tracked cost reaches
	// the cap the agent pauses the run before the next turn; raise the cap to
	// resume. Empty or invalid values disable the ceiling.
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?)?$`
	// +optional
	MaxCostUsd string `json:"maxCostUsd,omitempty"`
}

// ProviderKeyRef references a K8s Secret holding an API key for a specific provider.
type ProviderKeyRef struct {
	// provider is the provider name (e.g. "anthropic", "openai", "openrouter", "gemini", "groq", "xai", "copilot").
	Provider string `json:"provider"`
	// secretName is the name of the K8s Secret.
	SecretName string `json:"secretName"`
	// secretKey is the key within the K8s Secret (default: "api-key").
	// +optional
	SecretKey string `json:"secretKey,omitempty"`
}

// ProviderOAuthSecretRef references a K8s Secret holding OAuth material
// (auth.json, optionally account-id) for a specific OAuth-capable provider.
type ProviderOAuthSecretRef struct {
	// provider is the OAuth-capable provider name ("openai", "anthropic", "copilot").
	Provider string `json:"provider"`
	// secretName is the name of the K8s Secret.
	SecretName string `json:"secretName"`
}

// AgentRunSecrets holds credential secret references needed by execution.
type AgentRunSecrets struct {
	// Deprecated: use providerKeys instead for explicit per-provider credential mapping.
	// +optional
	ClaudeAPIKeySecret string `json:"claudeApiKeySecret,omitempty"`
	// +optional
	OpenAIOAuthSecret string `json:"openaiOAuthSecret,omitempty"`
	// +optional
	GitHubTokenSecret string `json:"githubTokenSecret,omitempty"`
	// slackTokensSecret optionally names a Secret holding Slack tokens
	// (bot-token, user-token). When set, the run pod receives read-only Slack
	// credentials (SLACK_BOT_TOKEN / SLACK_USER_TOKEN) so the agent can use
	// Slack read tools (threads, history, search). Sends still go through the
	// connector's approval flow.
	// +optional
	SlackTokensSecret string `json:"slackTokensSecret,omitempty"`
	// providerKeys is a list of per-provider API key secret references.
	// Each entry mounts the referenced secret as the correct env var for
	// the specified provider (e.g. ANTHROPIC_API_KEY, OPENAI_API_KEY).
	// +optional
	ProviderKeys []ProviderKeyRef `json:"providerKeys,omitempty"`
	// providerOAuthSecrets lists additional per-provider OAuth secret
	// references mounted into the run pod (alongside the run's own
	// openaiOAuthSecret) so the run can switch to these providers mid-run
	// without a compute restart.
	// +listType=atomic
	// +optional
	ProviderOAuthSecrets []ProviderOAuthSecretRef `json:"providerOAuthSecrets,omitempty"`
}

// AgentRunDelegationPolicy defines the parent's child-run limits.
type AgentRunDelegationPolicy struct {
	// +optional
	MaxChildren int32 `json:"maxChildren,omitempty"`
	// +optional
	MaxDepth int32 `json:"maxDepth,omitempty"`
	// +optional
	ParentOnly bool `json:"parentOnly,omitempty"`
}

// AgentRunCompletionPolicy defines the gates required before success.
type AgentRunCompletionPolicy struct {
	// +optional
	RequireApproval bool `json:"requireApproval,omitempty"`
}

// AgentRunTeamTask defines one predeclared worker assignment inside a TeamStep.
type AgentRunTeamTask struct {
	Name string `json:"name"`
	// +optional
	Role string `json:"role,omitempty"`
	// +optional
	Objective string `json:"objective,omitempty"`
	// +optional
	RuntimeProfileRef *NamedRef `json:"runtimeProfileRef,omitempty"`
	// +listType=atomic
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
	// +optional
	MaxRetries int32 `json:"maxRetries,omitempty"`
	// +optional
	ArtifactContract string `json:"artifactContract,omitempty"`
}

// AgentRunTeamStep defines one orchestration step in team mode.
type AgentRunTeamStep struct {
	Name string               `json:"name"`
	Type AgentRunTeamStepType `json:"type"`
	// +listType=atomic
	// +optional
	Tasks []AgentRunTeamTask `json:"tasks,omitempty"`
}

// AgentRunTeamSpec defines the predeclared team-mode orchestration contract.
type AgentRunTeamSpec struct {
	// +listType=atomic
	// +optional
	Steps []AgentRunTeamStep `json:"steps,omitempty"`
	// +optional
	DelegationPolicy *AgentRunDelegationPolicy `json:"delegationPolicy,omitempty"`
	// +optional
	CompletionPolicy *AgentRunCompletionPolicy `json:"completionPolicy,omitempty"`
}

// ModeRef references a ModeTemplate by name, version, and channel.
type ModeRef struct {
	Name string `json:"name"`
	// +optional
	Version string `json:"version,omitempty"`
	// +optional
	Channel string `json:"channel,omitempty"`
}

// AgentRunOverseerAuthority controls how an overseer may affect its target run.
// +kubebuilder:validation:Enum=observe;advise;enforce
type AgentRunOverseerAuthority string

const (
	AgentRunOverseerAuthorityObserve AgentRunOverseerAuthority = "observe"
	AgentRunOverseerAuthorityAdvise  AgentRunOverseerAuthority = "advise"
	AgentRunOverseerAuthorityEnforce AgentRunOverseerAuthority = "enforce"

	// Dashboard and admission bounds keep cadence conversion inside
	// time.Duration and prevent effectively unbounded intervention policies.
	AgentRunOverseerMaxIntervalMinutes int32 = 24 * 60
	AgentRunOverseerMaxInterventions   int32 = 100
)

// AgentRunOverseerSpec configures oversight for a run.
type AgentRunOverseerSpec struct {
	// ModeRef selects an admin-managed, overseer-safe ModeTemplate. Change it
	// by detaching and reattaching the overseer so the durable standing run can
	// be recreated with a fresh immutable mode snapshot.
	// +optional
	ModeRef *ModeRef `json:"modeRef,omitempty"`
	// Model overrides the primary run's model. Change it by detaching and
	// reattaching the overseer; authority, cadence, and caps remain live-tunable.
	// +optional
	Model string `json:"model,omitempty"`
	// +kubebuilder:default=advise
	// +optional
	Authority AgentRunOverseerAuthority `json:"authority,omitempty"`
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1440
	// +optional
	IntervalMinutes int32 `json:"intervalMinutes,omitempty"`
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	MaxInterventions int32 `json:"maxInterventions,omitempty"`
}

// ModeTransitionResult is the outcome of a mode switch.
// +kubebuilder:validation:Enum=applied;denied;noop;rolled_back
type ModeTransitionResult string

const (
	TransitionApplied    ModeTransitionResult = "applied"
	TransitionDenied     ModeTransitionResult = "denied"
	TransitionNoop       ModeTransitionResult = "noop"
	TransitionRolledBack ModeTransitionResult = "rolled_back"
)

// ModeTransitionEvent records a single mode transition attempt.
type ModeTransitionEvent struct {
	FromMode string               `json:"fromMode"`
	ToMode   string               `json:"toMode"`
	Result   ModeTransitionResult `json:"result"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	Actor string `json:"actor,omitempty"`
	// +optional
	Source    string      `json:"source,omitempty"`
	Timestamp metav1.Time `json:"timestamp"`
}

// AgentRunRoleModelOverride snapshots one user's provider-specific model
// preferences for a specialist role onto a run. Platform RoleInstruction
// defaults remain authoritative for providers not present in this map.
type AgentRunRoleModelOverride struct {
	// Role is the RoleInstruction name.
	// +kubebuilder:validation:MaxLength=253
	Role string `json:"role"`
	// ModelsByProvider maps normalized provider names to model identifiers.
	// +kubebuilder:validation:MaxProperties=6
	// +optional
	ModelsByProvider map[string]string `json:"modelsByProvider,omitempty"`
}

// AgentRunSpec defines the desired state of AgentRun.
type AgentRunSpec struct {
	Trigger    TriggerRef        `json:"trigger"`
	Repository RepositoryContext `json:"repository"`
	// +optional
	Context *AgentRunContext `json:"context,omitempty"`
	// WorkflowMode is retained for compatibility. New runs use auto and legacy
	// chat values resolve to the same autonomous, finish-gated pacing.
	// +optional
	WorkflowMode AgentRunWorkflowMode `json:"workflowMode,omitempty"`
	// +optional
	ExecutionMode AgentRunExecutionMode `json:"executionMode,omitempty"`
	// +optional
	Team *AgentRunTeamSpec `json:"team,omitempty"`
	// +optional
	Overseer *AgentRunOverseerSpec `json:"overseer,omitempty"`
	// +optional
	ModeRef *ModeRef `json:"modeRef,omitempty"`
	// +optional
	Model string `json:"model,omitempty"`
	// RoleModelOverrides snapshots the creating user's personal specialist-model
	// preferences. Missing roles/providers inherit cluster RoleInstruction defaults.
	// +listType=map
	// +listMapKey=role
	// +kubebuilder:validation:MaxItems=100
	// +optional
	RoleModelOverrides []AgentRunRoleModelOverride `json:"roleModelOverrides,omitempty"`
	// ReasoningLevel sets the main agent's model reasoning effort. It defaults
	// to max; individual roles may override it in their RoleInstruction.
	// +kubebuilder:default=max
	// +optional
	ReasoningLevel ModeReasoningLevel `json:"reasoningLevel,omitempty"`
	// +optional
	AuthMode AgentRunAuthMode `json:"authMode,omitempty"`
	// +optional
	OpenAIBaseURL string `json:"openaiBaseURL,omitempty"`
	// +optional
	Image string `json:"image,omitempty"`
	// +optional
	SpecArtifactRef *ArtifactRef `json:"specArtifactRef,omitempty"`
	// +optional
	RuntimeProfileRef *NamedRef `json:"runtimeProfileRef,omitempty"`
	// +optional
	MCPPolicyRef *NamedRef `json:"mcpPolicyRef,omitempty"`
	// +optional
	GuardrailPolicyRef *NamedRef `json:"guardrailPolicyRef,omitempty"`
	// MCPServerRefs lists MCPServer resources (same namespace) whose MCP
	// server configs are attached to this run.
	// +listType=atomic
	// +optional
	MCPServerRefs []NamedRef `json:"mcpServerRefs,omitempty"`
	// SkillRefs lists Skill resources (same namespace) whose instructions ride
	// with the run's system prompt. A skill's required MCP servers are
	// auto-attached to the run.
	// +listType=atomic
	// +optional
	SkillRefs []NamedRef      `json:"skillRefs,omitempty"`
	Limits    *AgentRunLimits `json:"limits,omitempty"`
	// +optional
	Secrets *AgentRunSecrets `json:"secrets,omitempty"`
	// DisableCommandSandbox completely disables the bubblewrap (bwrap)
	// subprocess sandbox for this run: model-controlled commands and MCP
	// stdio servers execute directly in the worker container instead of
	// inside the enforcing bwrap boundary. The pod/container isolation still
	// applies, but workspace write containment and read-only permission mode
	// become advisory only. Admin-only escape hatch for toolchains that are
	// incompatible with bubblewrap; it is intentionally not exposed through
	// the dashboard API and is inherited from trigger defaults
	// (spec.defaults.disableCommandSandbox).
	// +optional
	DisableCommandSandbox bool `json:"disableCommandSandbox,omitempty"`
	// KubernetesAdmin grants this run's worker service account cluster-admin
	// RBAC and exposes read-only platform introspection tools. This is inherited
	// from an admin-gated Project option.
	// +optional
	KubernetesAdmin bool `json:"kubernetesAdmin,omitempty"`
	// Debug enables verbose agent pod logging (full instructions, tool I/O, conversation items).
	// +optional
	Debug bool `json:"debug,omitempty"`
	// WakeRequests is a monotonic counter incremented to wake a completed run.
	// The controller handles values greater than status.wakeRequestsHandled.
	// +optional
	WakeRequests int64 `json:"wakeRequests,omitempty"`
	// RestartRequests is a monotonic counter incremented to restart a
	// non-terminal run's compute so spec changes that need a fresh pod (e.g.
	// switched provider credentials) take effect. Session state lives in the
	// store, so the re-provisioned pod resumes the run. The controller
	// handles values greater than status.restartRequestsHandled.
	// +optional
	RestartRequests int64 `json:"restartRequests,omitempty"`
}

// AgentRunQueueStatus exposes admission/blocked state.
type AgentRunQueueStatus struct {
	// +optional
	State string `json:"state,omitempty"`
	// +optional
	BlockedReason string `json:"blockedReason,omitempty"`
	// +optional
	AdmittedAt *metav1.Time `json:"admittedAt,omitempty"`
}

// AgentRunSandboxStatus links the run to provisioned execution resources.
type AgentRunSandboxStatus struct {
	// +optional
	Provider string `json:"provider,omitempty"`
	// +optional
	ClaimRef *NamedRef `json:"claimRef,omitempty"`
	// +optional
	SandboxRef *NamedRef `json:"sandboxRef,omitempty"`
}

// AgentRunArtifacts contains durable output references.
type AgentRunArtifacts struct {
	// +optional
	PlanRef *ArtifactRef `json:"planRef,omitempty"`
	// +optional
	ReviewSummaryRef *ArtifactRef `json:"reviewSummaryRef,omitempty"`
	// +optional
	ActivityLogURL string `json:"activityLogURL,omitempty"`
	// EventsLogURL is the S3 URL of the thin event stream (events.jsonl).
	// Preferred over ActivityLogURL when present. Structural observability
	// data lives in OTel spans, not in this file.
	// +optional
	EventsLogURL string `json:"eventsLogURL,omitempty"`
	// TraceID is the OTel trace ID for Jaeger/Tempo lookup.
	// +optional
	TraceID string `json:"traceID,omitempty"`
	// +optional
	DiffURL string `json:"diffURL,omitempty"`
	// +optional
	PullRequestURL string `json:"pullRequestURL,omitempty"`
	// PullRequestURLs lists every pull request created during the run, in
	// creation order (runs may open one PR per workspace repository).
	// PullRequestURL stays the most recent one for compatibility.
	// +listType=atomic
	// +optional
	PullRequestURLs []string `json:"pullRequestURLs,omitempty"`
	// +optional
	IssueURL string `json:"issueURL,omitempty"`
	// MetaHarnessTraceRef references the encrypted Meta-Harness execution
	// trace archive uploaded to the workspace object store when the run
	// finalizes (Kind "S3Object", Name the bucket, Key the object key). The
	// archive is encrypted with the run's workspace snapshot key and is only
	// present when Meta-Harness capture was enabled for the run.
	// +optional
	MetaHarnessTraceRef *ArtifactRef `json:"metaHarnessTraceRef,omitempty"`
}

// AgentRunResolvedPolicy reports the effective governance applied to the run.
type AgentRunResolvedPolicy struct {
	// +optional
	ResolvedPermissionMode string `json:"resolvedPermissionMode,omitempty"`
	// +listType=atomic
	// +optional
	ResolvedAgentKinds []string `json:"resolvedAgentKinds,omitempty"`
	// +listType=atomic
	// +optional
	ResolvedSkills []string `json:"resolvedSkills,omitempty"`
	// +listType=atomic
	// +optional
	ResolvedMCPServers []string `json:"resolvedMcpServers,omitempty"`
}

// AgentRunMetrics holds lightweight run counters.
type AgentRunMetrics struct {
	// +optional
	CostUsd string `json:"costUsd,omitempty"`
	// +optional
	InputTokens int64 `json:"inputTokens,omitempty"`
	// +optional
	OutputTokens int64 `json:"outputTokens,omitempty"`
	// +optional
	ToolCallCount int32 `json:"toolCallCount,omitempty"`
}

type AgentRunChatMessage struct {
	Role string `json:"role"`
	// +optional
	Content string `json:"content,omitempty"`
	// +optional
	Timestamp metav1.Time `json:"timestamp,omitempty"`
}

type AgentRunActivity struct {
	Timestamp metav1.Time `json:"timestamp"`
	// +optional
	EventType string `json:"eventType,omitempty"`
	// +optional
	Summary string `json:"summary,omitempty"`
}

// AgentRunChildStatus summarizes one child run in team mode.
type AgentRunChildStatus struct {
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +optional
	Step string `json:"step,omitempty"`
	// +optional
	Role string `json:"role,omitempty"`
	// +optional
	Phase AgentRunPhase `json:"phase,omitempty"`
	// +optional
	BlockedReason string `json:"blockedReason,omitempty"`
}

// AgentRunTeamSummary captures the parent-visible team orchestration state.
type AgentRunTeamSummary struct {
	// +optional
	CurrentStepIndex int32 `json:"currentStepIndex,omitempty"`
	// +optional
	CurrentStep string `json:"currentStep,omitempty"`
	// +optional
	ApprovalState string `json:"approvalState,omitempty"`
	// +optional
	TotalChildren int32 `json:"totalChildren,omitempty"`
	// +optional
	PendingChildren int32 `json:"pendingChildren,omitempty"`
	// +optional
	RunningChildren int32 `json:"runningChildren,omitempty"`
	// +optional
	SucceededChildren int32 `json:"succeededChildren,omitempty"`
	// +optional
	FailedChildren int32 `json:"failedChildren,omitempty"`
	// +optional
	PausedChildren int32 `json:"pausedChildren,omitempty"`
	// +optional
	CancelledChildren int32 `json:"cancelledChildren,omitempty"`
	// +optional
	BlockedReason string `json:"blockedReason,omitempty"`
}

// AgentRunOverseerStatus captures the latest oversight state for a run.
type AgentRunOverseerStatus struct {
	// +optional
	RunName string `json:"runName,omitempty"`
	// +optional
	State string `json:"state,omitempty"`
	// +optional
	CheckpointsHandled int64 `json:"checkpointsHandled,omitempty"`
	// +optional
	InterventionsUsed int32 `json:"interventionsUsed,omitempty"`
	// +optional
	CompletionRejectionsUsed int32 `json:"completionRejectionsUsed,omitempty"`
	// +optional
	LastVerdict string `json:"lastVerdict,omitempty"`
	// +optional
	LastSummary string `json:"lastSummary,omitempty"`
	// +optional
	LastVerdictTime *metav1.Time `json:"lastVerdictTime,omitempty"`
}

// AgentRunStatus defines the cluster-visible execution state of AgentRun.
// Durable session data such as conversation, pending questions, recent
// activity, and session mode live in Postgres instead of CRD status.
type AgentRunStatus struct {
	// +optional
	Phase AgentRunPhase `json:"phase,omitempty"`
	// DisplayName is a short human-readable label for the run, shown in the UI
	// instead of the generated resource name. It is set by the agent (via the
	// set_display_name tool) or by the user (via the RenameAgentRun RPC).
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +optional
	Queue *AgentRunQueueStatus `json:"queue,omitempty"`
	// +optional
	Sandbox *AgentRunSandboxStatus `json:"sandbox,omitempty"`
	// +optional
	Artifacts *AgentRunArtifacts `json:"artifacts,omitempty"`
	// +optional
	Policy *AgentRunResolvedPolicy `json:"policy,omitempty"`
	// +optional
	Metrics *AgentRunMetrics `json:"metrics,omitempty"`
	// +optional
	CurrentStep string `json:"currentStep,omitempty"`
	// +optional
	SessionNumber int32 `json:"sessionNumber,omitempty"`
	// +optional
	AgentCount int32 `json:"agentCount,omitempty"`
	// +optional
	RetryCount int32 `json:"retryCount,omitempty"`
	// +optional
	LastError string `json:"lastError,omitempty"`
	// +optional
	TeamSummary *AgentRunTeamSummary `json:"teamSummary,omitempty"`
	// +optional
	OverseerSummary *AgentRunOverseerStatus `json:"overseerSummary,omitempty"`
	// +listType=atomic
	// +optional
	Children []AgentRunChildStatus `json:"children,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// WakeRequestsHandled is the latest spec.wakeRequests value consumed by the controller.
	// +optional
	WakeRequestsHandled int64 `json:"wakeRequestsHandled,omitempty"`
	// RestartRequestsHandled is the latest spec.restartRequests value consumed by the controller.
	// +optional
	RestartRequestsHandled int64 `json:"restartRequestsHandled,omitempty"`
	// LastWakeTime records when the controller last accepted a wake request.
	// +optional
	LastWakeTime *metav1.Time `json:"lastWakeTime,omitempty"`
	// LastWakeReason records why the run was most recently woken.
	// +optional
	LastWakeReason string `json:"lastWakeReason,omitempty"`

	// Mode system fields — populated by controller from resolved ModeTemplate.
	// +optional
	ModeSnapshot *ModeTemplateSpec `json:"modeSnapshot,omitempty"`
	// +optional
	ModeName string `json:"modeName,omitempty"`
	// +optional
	ModeVersion string `json:"modeVersion,omitempty"`
	// +optional
	ModeRevision int64 `json:"modeRevision,omitempty"`
	// +optional
	ModeDeniedCount int32 `json:"modeDeniedCount,omitempty"`
	// +optional
	ModeNoopCount int32 `json:"modeNoopCount,omitempty"`

	// Completion fields.
	// CompletionRequested is set to true by the finish tool when the agent
	// signals it has finished work. Used by the runner to detect completion.
	// +optional
	CompletionRequested bool `json:"completionRequested,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Workflow",type=string,JSONPath=`.spec.workflowMode`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Step",type=string,JSONPath=`.status.currentStep`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentRun is the Schema for the agentruns API.
type AgentRun struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec AgentRunSpec `json:"spec"`

	// +optional
	Status AgentRunStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AgentRunList contains a list of AgentRun.
type AgentRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AgentRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRun{}, &AgentRunList{})
}
