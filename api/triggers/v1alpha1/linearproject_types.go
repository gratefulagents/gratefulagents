/*
Copyright 2026.

SPDX-License-Identifier: GPL-3.0-only
*/

package v1alpha1

import (
	"fmt"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentRunSecrets references K8s Secrets containing credentials.
type AgentRunSecrets struct {
	// Deprecated: use providerKeys instead for explicit per-provider credential mapping.
	// The field name is kept for backward compatibility.
	// +optional
	ClaudeApiKey string `json:"claudeApiKey,omitempty"`

	// openaiOAuthSecret is the name of the K8s Secret containing provider OAuth
	// material for authMode=oauth. The field name is kept for backward
	// compatibility; it is used by providers that support OAuth.
	// +optional
	OpenAIOAuthSecret string `json:"openaiOAuthSecret,omitempty"`

	// githubToken is the name of the K8s Secret containing the GitHub token.
	// It is optional for GitHubRepository triggers that use GitHub App auth.
	// +optional
	GithubToken string `json:"githubToken,omitempty"`

	// providerKeys is a list of per-provider API key secret references.
	// Each entry is propagated to the AgentRun and mounted as the correct
	// env var for the specified provider (e.g. ANTHROPIC_API_KEY, OPENAI_API_KEY).
	// +optional
	ProviderKeys []platformv1alpha1.ProviderKeyRef `json:"providerKeys,omitempty"`
}

// AgentRunDefaults holds the fields copied into every AgentRun created by
// this LinearProject.
type AgentRunDefaults struct {
	// repoURL is the git repository URL to clone. Optional: when empty, runs start
	// without a repository (repoless) and repos can be cloned at runtime.
	// +optional
	RepoURL string `json:"repoURL,omitempty"`

	// additionalRepos lists extra git repository URLs cloned into each run's
	// sandbox at startup, under repos/<name> next to the primary repository.
	// +listType=atomic
	// +optional
	AdditionalRepos []string `json:"additionalRepos,omitempty"`

	// baseBranch is the branch to fork from.
	// +kubebuilder:default="main"
	// +optional
	BaseBranch string `json:"baseBranch,omitempty"`

	// image is the worker pod image.
	// When omitted, the controller uses its built-in default image.
	// +optional
	Image string `json:"image,omitempty"`

	// model is the model identifier for the selected provider.
	// May include a provider prefix (e.g. "anthropic/claude-sonnet-4-6"),
	// in which case the separate provider field is ignored.
	// +optional
	Model string `json:"model,omitempty"`

	// allowedModels optionally constrains which provider models may be surfaced
	// for runs created from this source.
	// When empty, all provider-returned models remain eligible for selection.
	// +listType=set
	// +optional
	AllowedModels []string `json:"allowedModels,omitempty"`

	// Deprecated: use a prefixed model value (e.g. "anthropic/claude-sonnet-4-6") instead.
	// provider selects which LLM runtime provider to use.
	// Supported values:
	// - "anthropic" (Anthropic Messages API)
	// - "openai" (OpenAI-compatible API with OpenAI default URL)
	// - "gemini" (OpenAI-compatible API with Gemini default URL)
	// - "openrouter" (OpenAI-compatible API with OpenRouter default URL)
	// - "groq" (OpenAI-compatible API with Groq default URL)
	// - "xai" (OpenAI-compatible API with xAI default URL)
// - "copilot" (GitHub OAuth provider)
	//
	// When omitted, "openai" is used.
	// +kubebuilder:validation:Enum=anthropic;openai;gemini;openrouter;groq;xai;copilot
	// +kubebuilder:default="openai"
	// +optional
	Provider string `json:"provider,omitempty"`

	// authMode controls provider credential mode:
	// - "api-key" (default): use defaults.secrets.claudeApiKey or providerKeys
	// - "oauth": supported for provider "openai", "anthropic", and "copilot"; requires
	//   defaults.secrets.openaiOAuthSecret
	// +kubebuilder:validation:Enum=api-key;oauth
	// +kubebuilder:default="api-key"
	// +optional
	AuthMode platformv1alpha1.AgentRunAuthMode `json:"authMode,omitempty"`

	// reasoningLevel optionally sets the default model reasoning effort for
	// runs created from this source (one of: none, low, medium, high, xhigh, max).
	// Empty uses the provider/model default. Individual runs may override it.
	// +kubebuilder:validation:Enum=none;low;medium;high;xhigh;max
	// +optional
	ReasoningLevel platformv1alpha1.ModeReasoningLevel `json:"reasoningLevel,omitempty"`

	// openaiBaseURL overrides the OpenAI-compatible API base URL used for
	// created tasks.
	// If omitted for an OpenAI-compatible provider, a provider-specific default
	// URL is applied automatically.
	// +optional
	OpenAIBaseURL string `json:"openaiBaseURL,omitempty"`

	// openaiApi selects which OpenAI endpoint family to use when provider is
	// OpenAI-compatible:
	// - "responses": force /v1/responses (default)
	// - "chat-completions": force /v1/chat/completions
	// Copilot selects the supported endpoint per model in the SDK
	// (/v1/messages, /responses, or /chat/completions).
	// +kubebuilder:validation:Enum=responses;chat-completions
	// +kubebuilder:default="responses"
	// +optional
	OpenAIAPI string `json:"openaiApi,omitempty"`

	// timeout is the maximum duration for created AgentRuns.
	// +kubebuilder:default="30m"
	// +optional
	Timeout metav1.Duration `json:"timeout,omitempty"`

	// secrets references K8s Secrets for credentials.
	Secrets AgentRunSecrets `json:"secrets"`

	// customInstructions is operator-managed instructions injected into every
	// task's CLAUDE.md for this repo. Prepended before the repo's own CLAUDE.md
	// content so repo-specific rules can override platform defaults.
	// +optional
	CustomInstructions string `json:"customInstructions,omitempty"`

	// executionMode controls the orchestration model for created AgentRuns.
	// When omitted but team is configured, team mode is implied.
	// +optional
	ExecutionMode platformv1alpha1.AgentRunExecutionMode `json:"executionMode,omitempty"`

	// team configures the predeclared team-mode orchestration contract for
	// created AgentRuns.
	// +optional
	Team *platformv1alpha1.AgentRunTeamSpec `json:"team,omitempty"`

	// modeRef selects a ModeTemplate for created AgentRuns.
	// When set, the controller pins the resolved template snapshot on the run's status.
	// When omitted, the mode is inferred from workflowMode + executionMode via the compat adapter.
	// +optional
	ModeRef *platformv1alpha1.ModeRef `json:"modeRef,omitempty"`

	// workflowMode is retained for compatibility. Created runs always use auto;
	// legacy chat values are normalized to finish-gated autonomous pacing.
	// +kubebuilder:validation:Enum=auto;chat
	// +optional
	WorkflowMode platformv1alpha1.AgentRunWorkflowMode `json:"workflowMode,omitempty"`

	// runtimeProfileRef references a RuntimeProfile in the same namespace.
	// The profile controls permission mode, allowed tools, and other runtime policies.
	// +optional
	RuntimeProfileRef *platformv1alpha1.NamedRef `json:"runtimeProfileRef,omitempty"`

	// disableCommandSandbox completely disables the bubblewrap (bwrap)
	// subprocess sandbox for runs created from this trigger: model-controlled
	// commands and MCP stdio servers execute directly in the worker container
	// instead of inside the enforcing bwrap boundary. The pod/container
	// isolation still applies, but workspace write containment and read-only
	// permission mode become advisory only. Admin-only escape hatch for
	// toolchains that are incompatible with bubblewrap; it is intentionally
	// not exposed through the dashboard API, so it can only be set directly
	// on the trigger resource (kubectl/GitOps).
	// +optional
	DisableCommandSandbox bool `json:"disableCommandSandbox,omitempty"`

	// kubernetesAdmin grants runs created from this trigger cluster-admin
	// RBAC plus read-only platform introspection tools. Admin-only: it is
	// intentionally not exposed through the dashboard trigger APIs, so it can
	// only be set directly on the trigger resource (kubectl/GitOps). Projects
	// additionally expose the admin-gated spec.kubernetesAdmin field for
	// dashboard-created runs.
	// +optional
	KubernetesAdmin bool `json:"kubernetesAdmin,omitempty"`

	// mcpPolicyRef references an MCPPolicy in the same namespace.
	// The policy controls which MCP servers are allowed or denied.
	// When omitted, the agent defaults to deny-all for MCP tools (zero trust).
	// +optional
	MCPPolicyRef *platformv1alpha1.NamedRef `json:"mcpPolicyRef,omitempty"`

	// mcpServerRefs lists MCPServer resources in the same namespace to inject
	// into created AgentRuns.
	// +listType=atomic
	// +optional
	MCPServerRefs []platformv1alpha1.NamedRef `json:"mcpServerRefs,omitempty"`

	// skillRefs lists Skill resources in the same namespace to inject into
	// created AgentRuns (their required MCP servers are auto-attached).
	// +listType=atomic
	// +optional
	SkillRefs []platformv1alpha1.NamedRef `json:"skillRefs,omitempty"`
}

const (
	ProviderAnthropic  = "anthropic"
	ProviderOpenAI     = "openai"
	ProviderGemini     = "gemini"
	ProviderOpenRouter = "openrouter"
	ProviderGroq       = "groq"
	ProviderXAI        = "xai"
	ProviderCopilot    = "copilot"

	OpenAIAPIResponses       = "responses"
	OpenAIAPIChatCompletions = "chat-completions"

	DefaultOpenAIBaseURL      = "https://api.openai.com/v1"
	DefaultOpenAIOAuthBaseURL = "https://chatgpt.com/backend-api/codex"
	DefaultGeminiBaseURL      = "https://generativelanguage.googleapis.com/v1beta/openai"
	DefaultOpenRouterBaseURL  = "https://openrouter.ai/api/v1/chat/completions"
	DefaultGroqBaseURL        = "https://api.groq.com/openai/v1"
	DefaultXAIBaseURL         = "https://api.x.ai/v1"
	DefaultCopilotBaseURL     = "https://api.individual.githubcopilot.com"
)

// NormalizeProvider maps user input to a supported provider and defaults to
// OpenAI when empty.
func NormalizeProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderAnthropic:
		return ProviderAnthropic
	case ProviderGemini:
		return ProviderGemini
	case ProviderOpenRouter:
		return ProviderOpenRouter
	case ProviderGroq:
		return ProviderGroq
	case ProviderXAI:
		return ProviderXAI
	case ProviderCopilot:
		return ProviderCopilot
	default:
		return ProviderOpenAI
	}
}

// IsSupportedProvider reports whether provider names a runtime provider known
// to the platform. Unlike NormalizeProvider, it does not default unknown input
// to OpenAI, so it is safe to use when disambiguating provider-qualified model
// names from provider-native model IDs such as "z-ai/glm-4.7".
func IsSupportedProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderAnthropic,
		ProviderOpenAI,
		ProviderGemini,
		ProviderOpenRouter,
		ProviderGroq,
		ProviderCopilot:
		return true
	default:
		return false
	}
}

// SplitProviderModel extracts a recognized platform provider prefix from a
// model name. Unknown prefixes are preserved as part of the model because
// OpenAI-compatible providers commonly use vendor-qualified IDs (for example,
// OpenRouter's "z-ai/glm-4.7").
func SplitProviderModel(model string) (provider, bareModel string) {
	model = strings.TrimSpace(model)
	if i := strings.Index(model, "/"); i > 0 {
		prefix := strings.ToLower(strings.TrimSpace(model[:i]))
		if IsSupportedProvider(prefix) {
			return prefix, model[i+1:]
		}
	}
	return "", model
}

// PrefixModelWithProvider returns the unambiguous model name stored on an
// AgentRun. Provider-native model IDs may themselves contain slashes, so only
// an existing prefix matching provider is removed before the canonical prefix
// is applied. Bare OpenAI model IDs retain their legacy prefix-free form.
func PrefixModelWithProvider(model, provider string) string {
	model = strings.TrimSpace(model)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = ProviderOpenAI
	}
	if prefix, bare := SplitProviderModel(model); prefix == provider {
		model = bare
	}
	if provider == ProviderOpenAI && !strings.Contains(model, "/") {
		return model
	}
	return provider + "/" + model
}

// DefaultMainModelForProvider returns the quality-first model used when a
// source omits its main-agent model. Providers without a platform-vetted
// default keep requiring an explicit model.
func DefaultMainModelForProvider(provider string) string {
	switch NormalizeProvider(provider) {
	case ProviderOpenAI, ProviderCopilot:
		return "gpt-5.6-sol"
	case ProviderAnthropic:
		return "claude-opus-4-6"
	default:
		return ""
	}
}

// NormalizeAuthMode resolves configured auth mode and defaults to api-key.
func NormalizeAuthMode(mode string) platformv1alpha1.AgentRunAuthMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case string(platformv1alpha1.AgentRunAuthModeOAuth):
		return platformv1alpha1.AgentRunAuthModeOAuth
	default:
		return platformv1alpha1.AgentRunAuthModeAPIKey
	}
}

// OAuthSupportedForProvider reports whether provider supports OAuth in v1.
func OAuthSupportedForProvider(provider string) bool {
	switch NormalizeProvider(provider) {
	case ProviderOpenAI, ProviderAnthropic, ProviderCopilot:
		return true
	default:
		return false
	}
}

// ValidateProviderAuthMode validates whether auth mode is supported for provider.
func ValidateProviderAuthMode(provider string, mode platformv1alpha1.AgentRunAuthMode) error {
	authMode := NormalizeAuthMode(string(mode))
	if authMode == platformv1alpha1.AgentRunAuthModeOAuth && !OAuthSupportedForProvider(provider) {
		return fmt.Errorf("authMode %q is only supported when provider is %q, %q, or %q", authMode, ProviderOpenAI, ProviderAnthropic, ProviderCopilot)
	}
	return nil
}

// RequiresOpenAIOAuthSecret reports whether the legacy openaiOAuthSecret field
// is required for provider OAuth. The field name is retained for compatibility.
func RequiresOpenAIOAuthSecret(provider string, mode platformv1alpha1.AgentRunAuthMode) bool {
	return OAuthSupportedForProvider(provider) && NormalizeAuthMode(string(mode)) == platformv1alpha1.AgentRunAuthModeOAuth
}

// IsOpenAICompatibleProvider reports whether the provider uses the
// OpenAI-compatible runtime.
func IsOpenAICompatibleProvider(provider string) bool {
	return NormalizeProvider(provider) != ProviderAnthropic
}

// DefaultOpenAIBaseURLForProvider returns the default OpenAI-compatible API
// base URL for a provider.
func DefaultOpenAIBaseURLForProvider(provider string) string {
	switch NormalizeProvider(provider) {
	case ProviderGemini:
		return DefaultGeminiBaseURL
	case ProviderOpenRouter:
		return DefaultOpenRouterBaseURL
	case ProviderGroq:
		return DefaultGroqBaseURL
	case ProviderXAI:
		return DefaultXAIBaseURL
	case ProviderCopilot:
		return DefaultCopilotBaseURL
	default:
		return DefaultOpenAIBaseURL
	}
}

// ResolveOpenAIBaseURL returns the configured override when present; otherwise
// it returns the provider default for OpenAI-compatible providers.
func ResolveOpenAIBaseURL(provider, override string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}
	if !IsOpenAICompatibleProvider(provider) {
		return ""
	}
	return DefaultOpenAIBaseURLForProvider(provider)
}

// ResolveOpenAIBaseURLWithAuth is like ResolveOpenAIBaseURL but routes to the
// ChatGPT backend API when provider=openai and authMode=oauth (unless an
// explicit override is set).
func ResolveOpenAIBaseURLWithAuth(provider, override string, authMode platformv1alpha1.AgentRunAuthMode) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}
	if NormalizeProvider(provider) == ProviderOpenAI && RequiresOpenAIOAuthSecret(provider, authMode) {
		return DefaultOpenAIOAuthBaseURL
	}
	return ResolveOpenAIBaseURL(provider, override)
}

// NormalizeOpenAIAPI resolves configured endpoint mode and defaults to responses.
func NormalizeOpenAIAPI(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case OpenAIAPIResponses:
		return OpenAIAPIResponses
	case OpenAIAPIChatCompletions:
		return OpenAIAPIChatCompletions
	default:
		return OpenAIAPIResponses
	}
}

// NormalizeOpenAIAPIForProvider resolves configured endpoint mode and applies
// provider-specific endpoint support.
func NormalizeOpenAIAPIForProvider(provider, mode string) string {
	if NormalizeProvider(provider) == ProviderCopilot {
		return NormalizeOpenAIAPI(mode)
	}
	return NormalizeOpenAIAPI(mode)
}

// ResolveExecutionMode returns the explicit execution mode when present and
// otherwise infers team mode whenever a team spec is configured.
func (in AgentRunDefaults) ResolveExecutionMode() platformv1alpha1.AgentRunExecutionMode {
	if in.ExecutionMode != "" {
		return in.ExecutionMode
	}
	if in.Team != nil {
		return platformv1alpha1.ExecutionModeTeam
	}
	return platformv1alpha1.ExecutionModeLinear
}

// ResolveWorkflowMode always selects autonomous execution. WorkflowMode remains
// in the versioned API so existing resources decode, but chat no longer changes
// runtime pacing.
func (in AgentRunDefaults) ResolveWorkflowMode() platformv1alpha1.AgentRunWorkflowMode {
	return platformv1alpha1.WorkflowModeAuto
}

// LinearProjectSpec defines the desired state of LinearProject.
type LinearProjectSpec struct {
	// linearApiKeySecret is the name of the K8s Secret that holds the Linear
	// API key under the key "api-key".
	// +kubebuilder:validation:MinLength=1
	LinearAPIKeySecret string `json:"linearApiKeySecret"`

	// projectId is the Linear project ID to watch.
	// +kubebuilder:validation:MinLength=1
	ProjectID string `json:"projectId"`

	// teamId is the Linear team ID used for label lookups.
	// +kubebuilder:validation:MinLength=1
	TeamID string `json:"teamId"`

	// pollInterval is how often to poll Linear for new approved issues.
	// +kubebuilder:default="30s"
	// +optional
	PollInterval metav1.Duration `json:"pollInterval,omitempty"`

	// approvedLabel is the label name that triggers AgentRun creation.
	// +kubebuilder:default="ai-approved"
	// +optional
	ApprovedLabel string `json:"approvedLabel,omitempty"`

	// autoCreateTasks controls whether polling automatically creates AgentRuns
	// from Linear issues with the approved label. When false (default), polling
	// still runs but does not create tasks. When true, tasks are created
	// automatically and require dashboard approval (via spec.approved) before
	// execution begins.
	// +optional
	AutoCreateTasks bool `json:"autoCreateTasks,omitempty"`

	// defaults holds the fields used when creating AgentRuns.
	Defaults AgentRunDefaults `json:"defaults"`
}

// LinearProjectStatus defines the observed state of LinearProject.
type LinearProjectStatus struct {
	// lastPollTime is when Linear was last polled successfully.
	// +optional
	LastPollTime *metav1.Time `json:"lastPollTime,omitempty"`

	// issuesProcessed is the cumulative number of issues that have been turned
	// into AgentRuns by this controller.
	// +optional
	IssuesProcessed int32 `json:"issuesProcessed,omitempty"`

	// lastError contains the error message from the most recent failed poll.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// conditions represent the current state of the LinearProject.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for LinearProject.
const (
	// ConditionLinearProjectReady indicates the API key is valid and the project
	// is accessible.
	ConditionLinearProjectReady = "Ready"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.spec.projectId`
// +kubebuilder:printcolumn:name="Processed",type=integer,JSONPath=`.status.issuesProcessed`
// +kubebuilder:printcolumn:name="LastPoll",type=date,JSONPath=`.status.lastPollTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// LinearProject is the Schema for the linearprojects API.
type LinearProject struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec LinearProjectSpec `json:"spec"`

	// +optional
	Status LinearProjectStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// LinearProjectList contains a list of LinearProject.
type LinearProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []LinearProject `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LinearProject{}, &LinearProjectList{})
}
