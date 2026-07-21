package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/agentinfra"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	agentpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
	oauth "github.com/gratefulagents/sdk/pkg/agentsdk/providers/oauth"
)

// runConfig holds all environment-driven configuration for the agent.
type runConfig struct {
	RepoURL                  string
	AdditionalRepoURLs       []string // extra repos cloned at startup into repos/<name> under RepoDir
	Repoless                 bool     // true when no repository is attached (plain chat in an empty sandbox)
	BaseBranch               string
	Model                    string
	Provider                 string
	BaseURL                  string
	APIKey                   string
	AuthMode                 string
	APIMode                  string
	ProviderAPIKeys          map[string]string
	ProviderBaseURLs         map[string]string
	ProviderAPIModes         map[string]string
	OpenAIOAuthPath          string
	OpenAIOAuthAccountID     string
	OpenAIOAuthAccountIDPath string
	CopilotOAuthPath         string // mounted Copilot auth.json; enables SDK self-refresh of the ~30-min API token
	AnthropicOAuthPath       string // mounted Anthropic OAuth auth.json; enables SDK per-request re-read + self-refresh of the access token
	Namespace                string
	WorkspaceDir             string
	RepoDir                  string
	// WorkspaceSnapshotKey is a runtime-only per-run encryption key loaded
	// from private session metadata. Workspace checkpoints are encrypted before
	// they are written to object storage.
	WorkspaceSnapshotKey      []byte
	WorkspaceCheckpointStore  workspaceObjectStore
	WorkspaceCheckpointPrefix string
	WorkspaceCheckpoint       *workspaceCheckpointManifest
	GithubToken               string
	TaskName                  string
	TaskUID                   string
	ModelFallbacks            []string                   // ordered fallback models for OpenRouter-style providers
	AutoMode                  bool                       // true for autonomous mode (no user questions) — resolved from CRD
	DelegatedChild            bool                       // true if this run was created by a parent team run — resolved from CRD
	KubernetesAdmin           bool                       // true when this run has cluster-admin RBAC and platform introspection tools
	TaskContext               string                     // Operator task context injected into system prompt.
	Debug                     bool                       // verbose logging (full instructions, tool I/O)
	PermissionMode            agentpolicy.PermissionMode // resolved from RuntimeProfile; defaults to read-only
	// GitRemoteWrites is resolved from RuntimeProfile and defaults to enabled for compatibility.
	GitRemoteWrites agentpolicy.GitRemoteWrites
	// PermissionModeDegraded means read-only resulted from a startup failure/race, not explicit config.
	PermissionModeDegraded bool
	PermissionModeReason   string // human-readable reason when the pod's base mode is read-only
}

func loadRunConfig() (runConfig, error) {
	workspace := agentinfra.EnvOrDefault("WORKSPACE_DIR", "/workspace")
	// REPO_URL is optional: an empty value means a repoless chat that runs in an
	// empty sandbox (no clone, no working branch).
	repoURL := strings.TrimSpace(os.Getenv("REPO_URL"))
	// ADDITIONAL_REPO_URLS lists extra repositories cloned at startup into
	// repos/<name> under the primary repo dir (the attach_repository store).
	additionalRepoURLs := splitCommaList(os.Getenv("ADDITIONAL_REPO_URLS"))
	namespace, err := agentinfra.MustEnv("POD_NAMESPACE")
	if err != nil {
		return runConfig{}, err
	}
	// GH_PAT is optional: repoless chats run in an empty sandbox with no git
	// remote, so they never receive a GitHub token. Require it only when a
	// repository is attached (clone/push need credentials).
	ghPAT := strings.TrimSpace(os.Getenv("GH_PAT"))
	if (repoURL != "" || len(additionalRepoURLs) > 0) && ghPAT == "" {
		return runConfig{}, fmt.Errorf("required env var GH_PAT is not set")
	}
	taskName, err := agentinfra.MustEnv("PLANTASK_NAME")
	if err != nil {
		return runConfig{}, err
	}
	taskUID, err := agentinfra.MustEnv("PLANTASK_UID")
	if err != nil {
		return runConfig{}, err
	}
	model, err := agentinfra.MustEnv("MODEL")
	if err != nil {
		return runConfig{}, err
	}
	provider := deriveProvider(model, agentinfra.EnvOrDefault("AI_PROVIDER", "openai"))
	baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	authMode := strings.TrimSpace(firstNonEmpty(os.Getenv("AI_AUTH_MODE"), os.Getenv("OPENAI_AUTH_MODE")))
	apiMode := strings.TrimSpace(os.Getenv("OPENAI_API_MODE"))
	providerAPIKeys := providerAPIKeysFromEnv(provider)
	oauthMode := strings.EqualFold(authMode, string(platformv1alpha1.AgentRunAuthModeOAuth))
	copilotOAuthPath := ""
	anthropicOAuthPath := ""
	if oauthMode {
		// Load every mounted provider's OAuth material, not just the startup
		// provider's: additional mounts (spec.secrets.providerOAuthSecrets)
		// let the run live-switch providers mid-run. Only the startup
		// provider's material is a hard requirement.
		if strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_AUTH_JSON_PATH")) != "" || strings.EqualFold(provider, "anthropic") {
			token, err := anthropicOAuthAccessTokenFromEnv()
			switch {
			case err == nil:
				providerAPIKeys["anthropic"] = token
				// Hand the mounted auth.json path to the SDK so the provider
				// resolves the bearer token per request — picking up secrets
				// rotated by the operator's refresher and self-refreshing near
				// expiry — instead of pinning the startup token, which
				// Anthropic expires within hours.
				anthropicOAuthPath = strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_AUTH_JSON_PATH"))
			case strings.EqualFold(provider, "anthropic"):
				return runConfig{}, err
			default:
				log.Printf("WARN: mounted Anthropic OAuth material unusable (live switch to anthropic disabled): %v", err)
			}
		}
		if strings.TrimSpace(os.Getenv("COPILOT_OAUTH_AUTH_JSON_PATH")) != "" || strings.EqualFold(provider, "copilot") {
			token, err := copilotOAuthAccessTokenFromEnv()
			switch {
			case err == nil:
				providerAPIKeys["copilot"] = token
				// Hand the mounted auth.json path to the SDK so the provider
				// self-refreshes the short-lived (~30 min) Copilot API token
				// instead of pinning the startup token for the life of the pod.
				copilotOAuthPath = strings.TrimSpace(os.Getenv("COPILOT_OAUTH_AUTH_JSON_PATH"))
			case strings.EqualFold(provider, "copilot"):
				return runConfig{}, err
			default:
				log.Printf("WARN: mounted Copilot OAuth material unusable (live switch to copilot disabled): %v", err)
			}
		}
	}
	providerBaseURLs := providerBaseURLsFromSelected(provider, baseURL)
	// A non-openai OAuth run with OpenAI OAuth material mounted can
	// live-switch to openai: route openai traffic to the OAuth backend.
	// backend, which is what OAuth (ChatGPT plan) credentials authenticate
	// against — not the standard OpenAI API.
	if oauthMode && !strings.EqualFold(provider, "openai") && strings.TrimSpace(os.Getenv("OPENAI_OAUTH_AUTH_JSON_PATH")) != "" {
		providerBaseURLs["openai"] = triggersv1alpha1.DefaultOpenAIOAuthBaseURL
	}
	modelFallbacks := splitCommaList(os.Getenv("MODEL_FALLBACKS"))
	providerAPIModes := providerAPIModesFromSelected(provider, apiMode, len(modelFallbacks) > 0)

	debugVal := strings.TrimSpace(strings.ToLower(agentinfra.EnvOrDefault("AI_DEBUG", "")))
	debug := debugVal == "1" || debugVal == "true"

	return runConfig{
		RepoURL:                  repoURL,
		AdditionalRepoURLs:       additionalRepoURLs,
		Repoless:                 repoURL == "",
		BaseBranch:               agentinfra.EnvOrDefault("BASE_BRANCH", "main"),
		Model:                    model,
		Provider:                 provider,
		BaseURL:                  baseURL,
		APIKey:                   providerValue(providerAPIKeys, provider),
		AuthMode:                 authMode,
		APIMode:                  apiMode,
		ProviderAPIKeys:          providerAPIKeys,
		ProviderBaseURLs:         providerBaseURLs,
		ProviderAPIModes:         providerAPIModes,
		OpenAIOAuthPath:          strings.TrimSpace(os.Getenv("OPENAI_OAUTH_AUTH_JSON_PATH")),
		OpenAIOAuthAccountID:     strings.TrimSpace(os.Getenv("OPENAI_OAUTH_ACCOUNT_ID")),
		OpenAIOAuthAccountIDPath: strings.TrimSpace(os.Getenv("OPENAI_OAUTH_ACCOUNT_ID_PATH")),
		CopilotOAuthPath:         copilotOAuthPath,
		AnthropicOAuthPath:       anthropicOAuthPath,
		Namespace:                namespace,
		WorkspaceDir:             workspace,
		RepoDir:                  filepath.Join(workspace, "repo"),
		GithubToken:              ghPAT,
		TaskName:                 taskName,
		TaskUID:                  taskUID,
		ModelFallbacks:           modelFallbacks,
		Debug:                    debug,
		// AutoMode is resolved from the CRD in doRun, not from env vars.
	}, nil
}

// splitCommaList parses a comma-separated env value into trimmed non-empty items.
func splitCommaList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// deriveProvider extracts the provider from a prefixed model name (e.g.
// "anthropic/claude-sonnet-4-6" → "anthropic"), falling back to the given
// default when no prefix is present.
func deriveProvider(model, fallback string) string {
	prefix, _ := agent.ParseModelPrefix(model)
	if prefix != "" {
		return strings.ToLower(strings.TrimSpace(prefix))
	}
	return strings.ToLower(strings.TrimSpace(fallback))
}

func providerAPIKeysFromEnv(selectedProvider string) map[string]string {
	providers := []string{"openai", "anthropic", "openrouter", "gemini", "groq", "xai", "copilot", selectedProvider}
	keys := make(map[string]string)
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			continue
		}
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		if value := strings.TrimSpace(os.Getenv(providerAPIKeyEnvName(provider))); value != "" {
			keys[provider] = value
		}
	}
	return keys
}

func providerAPIKeyEnvName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	case "xai":
		return "XAI_API_KEY"
	case "copilot":
		return "COPILOT_API_KEY"
	default:
		return strings.ToUpper(strings.TrimSpace(provider)) + "_API_KEY"
	}
}

func providerValuesFromSelected(provider, value string) map[string]string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	value = strings.TrimSpace(value)
	if provider == "" || value == "" {
		return nil
	}
	return map[string]string{provider: value}
}

func providerBaseURLsFromSelected(provider, value string) map[string]string {
	values := map[string]string{
		"openai":     triggersv1alpha1.DefaultOpenAIBaseURL,
		"gemini":     triggersv1alpha1.DefaultGeminiBaseURL,
		"openrouter": triggersv1alpha1.DefaultOpenRouterBaseURL,
		"groq":       triggersv1alpha1.DefaultGroqBaseURL,
		"xai":        triggersv1alpha1.DefaultXAIBaseURL,
		"copilot":    triggersv1alpha1.DefaultCopilotBaseURL,
	}
	for key, val := range providerValuesFromSelected(provider, value) {
		values[key] = val
	}
	return values
}

func providerAPIModesFromSelected(provider, value string, hasModelFallbacks bool) map[string]string {
	values := map[string]string{
		"openai":     triggersv1alpha1.OpenAIAPIResponses,
		"gemini":     triggersv1alpha1.OpenAIAPIChatCompletions,
		"openrouter": triggersv1alpha1.OpenAIAPIResponses,
		"groq":       triggersv1alpha1.OpenAIAPIChatCompletions,
		"xai":        triggersv1alpha1.OpenAIAPIResponses,
		"copilot":    triggersv1alpha1.OpenAIAPIResponses,
	}
	for key, val := range providerValuesFromSelected(provider, value) {
		values[key] = val
	}
	// OpenRouter's ordered `models` fallback array is a Chat Completions
	// feature. Prefer its Responses API Beta for normal runs, but keep fallback
	// routing functional when MODEL_FALLBACKS is configured.
	if strings.EqualFold(strings.TrimSpace(provider), "openrouter") && hasModelFallbacks {
		values["openrouter"] = triggersv1alpha1.OpenAIAPIChatCompletions
	}
	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func anthropicOAuthAccessTokenFromEnv() (string, error) {
	authPath := strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_AUTH_JSON_PATH"))
	if authPath == "" {
		return "", fmt.Errorf("ANTHROPIC_OAUTH_AUTH_JSON_PATH is required for Anthropic OAuth")
	}
	authJSON, err := os.ReadFile(authPath)
	if err != nil {
		return "", fmt.Errorf("read Anthropic OAuth auth json: %w", err)
	}
	auth, err := oauth.ParseAnthropicAuthJSON(authJSON)
	if err != nil {
		if material := oauthMaterialKind(authJSON); material != "" && material != "anthropic" {
			return "", fmt.Errorf("mounted Anthropic OAuth auth.json actually holds %s OAuth material — the run's provider/OAuth-secret wiring is mismatched; fix the project's Provider and OAuth Secret settings: %w", material, err)
		}
		return "", fmt.Errorf("parse Anthropic OAuth auth json: %w", err)
	}
	if strings.TrimSpace(auth.AccessToken) == "" {
		return "", fmt.Errorf("Anthropic OAuth auth json is missing access token")
	}
	return strings.TrimSpace(auth.AccessToken), nil
}

// oauthMaterialKind sniffs mounted OAuth material for markers identifying its
// actual provider, so wiring mismatches (e.g. a Copilot secret mounted as the
// Anthropic auth.json) fail with a pointed message instead of a bare parse error.
func oauthMaterialKind(authJSON []byte) string {
	var root map[string]any
	if err := json.Unmarshal(authJSON, &root); err != nil {
		return ""
	}
	if typ, _ := root["type"].(string); typ != "" {
		switch strings.ToLower(strings.TrimSpace(typ)) {
		case "copilot":
			return "copilot"
		case "claude", "anthropic":
			return "anthropic"
		case "openai", "chatgpt", "codex":
			return "openai"
		}
	}
	if _, ok := root["claudeAiOauth"]; ok {
		return "anthropic"
	}
	if token, _ := root["oauth_token"].(string); strings.TrimSpace(token) != "" {
		return "copilot"
	}
	if tokens, ok := root["tokens"].(map[string]any); ok {
		if id, _ := tokens["id_token"].(string); strings.TrimSpace(id) != "" {
			return "openai"
		}
	}
	if refresh, _ := root["refresh_token"].(string); strings.TrimSpace(refresh) != "" {
		return "anthropic"
	}
	return ""
}

func copilotOAuthAccessTokenFromEnv() (string, error) {
	authPath := strings.TrimSpace(os.Getenv("COPILOT_OAUTH_AUTH_JSON_PATH"))
	if authPath == "" {
		return "", fmt.Errorf("COPILOT_OAUTH_AUTH_JSON_PATH is required for Copilot OAuth")
	}
	authJSON, err := os.ReadFile(authPath)
	if err != nil {
		return "", fmt.Errorf("read Copilot OAuth auth json: %w", err)
	}
	auth, err := oauth.ParseCopilotAuthJSON(authJSON)
	if err != nil {
		if material := oauthMaterialKind(authJSON); material != "" && material != "copilot" {
			return "", fmt.Errorf("mounted Copilot OAuth auth.json actually holds %s OAuth material — the run's provider/OAuth-secret wiring is mismatched; fix the project's Provider and OAuth Secret settings: %w", material, err)
		}
		return "", fmt.Errorf("parse Copilot OAuth auth json: %w", err)
	}
	if strings.TrimSpace(auth.Token) == "" {
		return "", fmt.Errorf("Copilot OAuth auth json is missing Copilot API token")
	}
	return strings.TrimSpace(auth.Token), nil
}

func providerValue(values map[string]string, provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return ""
	}
	for key, value := range values {
		if strings.EqualFold(strings.TrimSpace(key), provider) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
