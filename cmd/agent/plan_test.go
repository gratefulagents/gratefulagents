package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

func TestIsSessionModeSlashCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		message string
		want    bool
	}{
		{message: "/plan", want: true},
		{message: " /chat ", want: true},
		{message: "/exit-plan", want: true},
		{message: "/autopilot", want: true},
		{message: "/mode deep", want: true},
		{message: "/status", want: false},
		{message: "implement this", want: false},
	}

	for _, tt := range tests {
		if got := isControlSlashCommand(tt.message); got != tt.want {
			t.Fatalf("isControlSlashCommand(%q) = %v, want %v", tt.message, got, tt.want)
		}
	}
}

func TestCheckoutExistingRemoteBranchArgs(t *testing.T) {
	t.Parallel()

	gotCommands := checkoutBranchCommands("run-123", true)
	if len(gotCommands) != 3 {
		t.Fatalf("len(existing commands) = %d, want 3: %#v", len(gotCommands), gotCommands)
	}
	got := gotCommands[1].args
	want := []string{"checkout", "--track", "-b", "run-123", "origin/run-123"}
	if len(got) != len(want) {
		t.Fatalf("len(args) = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q (all args %#v)", i, got[i], want[i], got)
		}
	}

	newCommands := checkoutBranchCommands("run-123", false)
	if len(newCommands) != 1 || strings.Join(newCommands[0].args, " ") != "checkout -b run-123" {
		t.Fatalf("new branch commands = %#v, want checkout -b run-123", newCommands)
	}
}

func TestSetRuntimeParentMetadataEnvPreservesDelegatedParentIdentity(t *testing.T) {
	t.Setenv("AGENTRUN_PARENT_NAMESPACE", "gratefulagents-system")
	t.Setenv("AGENTRUN_PARENT_NAME", "parent-run")
	t.Setenv("AGENTRUN_PARENT_UID", "parent-uid")
	t.Setenv("RUN_NAMESPACE", "gratefulagents-system")
	t.Setenv("RUN_NAME", "parent-run")
	t.Setenv("RUN_UID", "parent-uid")

	setRuntimeParentMetadataEnv(runConfig{
		Namespace: "gratefulagents-system",
		TaskName:  "child-run",
		TaskUID:   "child-uid",
	})

	if got := strings.TrimSpace(os.Getenv("AGENTRUN_PARENT_NAME")); got != "parent-run" {
		t.Fatalf("AGENTRUN_PARENT_NAME = %q, want parent-run", got)
	}
	if got := strings.TrimSpace(os.Getenv("AGENTRUN_PARENT_UID")); got != "parent-uid" {
		t.Fatalf("AGENTRUN_PARENT_UID = %q, want parent-uid", got)
	}
}

func TestSetRuntimeParentMetadataEnvDefaultsParentIdentityWhenMissing(t *testing.T) {
	t.Setenv("AGENTRUN_PARENT_NAMESPACE", "")
	t.Setenv("AGENTRUN_PARENT_NAME", "")
	t.Setenv("AGENTRUN_PARENT_UID", "")
	t.Setenv("RUN_NAMESPACE", "")
	t.Setenv("RUN_NAME", "")
	t.Setenv("RUN_UID", "")

	setRuntimeParentMetadataEnv(runConfig{
		Namespace: "gratefulagents-system",
		TaskName:  "chat-run",
		TaskUID:   "chat-uid",
	})

	if got := strings.TrimSpace(os.Getenv("AGENTRUN_PARENT_NAME")); got != "chat-run" {
		t.Fatalf("AGENTRUN_PARENT_NAME = %q, want chat-run", got)
	}
	if got := strings.TrimSpace(os.Getenv("RUN_NAME")); got != "chat-run" {
		t.Fatalf("RUN_NAME = %q, want chat-run", got)
	}
}

func TestMarshalQuickActionsUsesIDField(t *testing.T) {
	t.Parallel()

	data := agent.MarshalQuickActions(
		agent.QuickAction{ID: "approve", Label: "Approve", Style: "primary"},
		agent.QuickAction{ID: "request_changes", Label: "Request Changes"},
	)

	var actions []map[string]string
	if err := json.Unmarshal(data, &actions); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("len(actions) = %d, want 2", len(actions))
	}
	if got := actions[0]["id"]; got != "approve" {
		t.Fatalf("actions[0][\"id\"] = %q, want approve", got)
	}
	if got := actions[1]["id"]; got != "request_changes" {
		t.Fatalf("actions[1][\"id\"] = %q, want request_changes", got)
	}
	if _, ok := actions[0]["value"]; ok {
		t.Fatalf("actions[0] unexpectedly serialized legacy \"value\" key: %#v", actions[0])
	}
}

func TestLoadRunConfigCarriesProviderAuthForSDK(t *testing.T) {
	t.Setenv("WORKSPACE_DIR", "/workspace")
	t.Setenv("REPO_URL", "https://github.com/example/repo")
	t.Setenv("POD_NAMESPACE", "gratefulagents-system")
	t.Setenv("GH_PAT", "ghp-test")
	t.Setenv("PLANTASK_NAME", "run-1")
	t.Setenv("PLANTASK_UID", "uid-1")
	t.Setenv("MODEL", "openai/gpt-5.5")
	t.Setenv("AI_PROVIDER", "openai")
	t.Setenv("OPENAI_AUTH_MODE", "oauth")
	t.Setenv("OPENAI_BASE_URL", "https://chatgpt.com/backend-api/codex")
	t.Setenv("OPENAI_API_MODE", "responses")
	t.Setenv("OPENAI_OAUTH_AUTH_JSON_PATH", "/var/run/openai/auth.json")
	t.Setenv("OPENAI_OAUTH_ACCOUNT_ID_PATH", "/var/run/openai/account-id")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	cfg, err := loadRunConfig()
	if err != nil {
		t.Fatal(err)
	}
	runtimeCfg := sdkRuntimeProviderConfig(cfg, cfg.Model)
	if runtimeCfg.Provider != "multi" || runtimeCfg.DefaultProvider != "openai" {
		t.Fatalf("provider config = %q/%q, want multi/openai", runtimeCfg.Provider, runtimeCfg.DefaultProvider)
	}
	if runtimeCfg.AuthMode != "oauth" || runtimeCfg.OpenAIOAuthPath != "/var/run/openai/auth.json" || runtimeCfg.OpenAIOAuthAccountIDPath != "/var/run/openai/account-id" {
		t.Fatalf("oauth config = %+v", runtimeCfg)
	}
	if runtimeCfg.ProviderAPIKeys["anthropic"] != "sk-ant-test" {
		t.Fatalf("ProviderAPIKeys = %#v", runtimeCfg.ProviderAPIKeys)
	}
	if runtimeCfg.ProviderBaseURLs["openai"] != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("ProviderBaseURLs = %#v", runtimeCfg.ProviderBaseURLs)
	}
	if runtimeCfg.ProviderAPIModes["openai"] != "responses" {
		t.Fatalf("ProviderAPIModes = %#v", runtimeCfg.ProviderAPIModes)
	}
	if runtimeCfg.ToolOutputDir != workspaceScratchDir {
		t.Fatalf("ToolOutputDir = %q, want scratch directory %q", runtimeCfg.ToolOutputDir, workspaceScratchDir)
	}
}

func TestLoadRunConfigSeedsProviderDefaultsForRuntimeSwitches(t *testing.T) {
	t.Setenv("WORKSPACE_DIR", "/workspace")
	t.Setenv("REPO_URL", "https://github.com/example/repo")
	t.Setenv("POD_NAMESPACE", "gratefulagents-system")
	t.Setenv("GH_PAT", "ghp-test")
	t.Setenv("PLANTASK_NAME", "run-openrouter")
	t.Setenv("PLANTASK_UID", "uid-openrouter")
	t.Setenv("MODEL", "openrouter/openai/gpt-5")
	t.Setenv("AI_PROVIDER", "openrouter")
	t.Setenv("OPENAI_BASE_URL", "https://openrouter.ai/api/v1/chat/completions")
	t.Setenv("OPENAI_API_MODE", "chat-completions")
	t.Setenv("OPENAI_API_KEY", "sk-openai-test")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")

	cfg, err := loadRunConfig()
	if err != nil {
		t.Fatal(err)
	}
	runtimeCfg := sdkRuntimeProviderConfig(cfg, cfg.Model)
	if runtimeCfg.ProviderBaseURLs["openai"] != "https://api.openai.com/v1" {
		t.Fatalf("openai ProviderBaseURL = %q, want OpenAI default", runtimeCfg.ProviderBaseURLs["openai"])
	}
	if runtimeCfg.ProviderBaseURLs["openrouter"] != "https://openrouter.ai/api/v1/chat/completions" {
		t.Fatalf("openrouter ProviderBaseURL = %q, want selected OpenRouter URL", runtimeCfg.ProviderBaseURLs["openrouter"])
	}
	if runtimeCfg.ProviderAPIModes["openai"] != "responses" {
		t.Fatalf("openai ProviderAPIMode = %q, want responses", runtimeCfg.ProviderAPIModes["openai"])
	}
	if runtimeCfg.ProviderAPIModes["openrouter"] != "chat-completions" {
		t.Fatalf("openrouter ProviderAPIMode = %q, want chat-completions", runtimeCfg.ProviderAPIModes["openrouter"])
	}
}

func TestLoadRunConfigLoadsAdditionalMountedOAuthProviders(t *testing.T) {
	t.Setenv("WORKSPACE_DIR", "/workspace")
	t.Setenv("REPO_URL", "https://github.com/example/repo")
	t.Setenv("POD_NAMESPACE", "gratefulagents-system")
	t.Setenv("GH_PAT", "ghp-test")
	t.Setenv("PLANTASK_NAME", "run-1")
	t.Setenv("PLANTASK_UID", "uid-1")
	t.Setenv("MODEL", "anthropic/claude-sonnet-4-6")
	t.Setenv("AI_PROVIDER", "anthropic")
	t.Setenv("OPENAI_AUTH_MODE", "oauth")

	anthropicPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(anthropicPath, []byte(`{"access_token":"anthropic-access","refresh_token":"r","expired":"2099-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_OAUTH_AUTH_JSON_PATH", anthropicPath)

	copilotPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(copilotPath, []byte(`{"oauth_token":"github-oauth","token":"copilot-api-token","expires_at":4070908800}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_OAUTH_AUTH_JSON_PATH", copilotPath)

	t.Setenv("OPENAI_OAUTH_AUTH_JSON_PATH", "/var/run/gratefulagents/oauth/openai/auth.json")

	cfg, err := loadRunConfig()
	if err != nil {
		t.Fatal(err)
	}
	runtimeCfg := sdkRuntimeProviderConfig(cfg, cfg.Model)
	if runtimeCfg.ProviderAPIKeys["anthropic"] != "anthropic-access" {
		t.Fatalf("ProviderAPIKeys[anthropic] = %#v, want startup provider oauth token", runtimeCfg.ProviderAPIKeys)
	}
	if runtimeCfg.AnthropicOAuthPath != anthropicPath {
		t.Fatalf("AnthropicOAuthPath = %q, want %q (SDK per-request re-read/self-refresh wiring)", runtimeCfg.AnthropicOAuthPath, anthropicPath)
	}
	// Additional mounted provider material loads too, enabling live switches.
	if runtimeCfg.ProviderAPIKeys["copilot"] != "copilot-api-token" {
		t.Fatalf("ProviderAPIKeys[copilot] = %#v, want additional copilot oauth token", runtimeCfg.ProviderAPIKeys)
	}
	if runtimeCfg.CopilotOAuthPath != copilotPath {
		t.Fatalf("CopilotOAuthPath = %q, want %q (SDK self-refresh wiring)", runtimeCfg.CopilotOAuthPath, copilotPath)
	}
	// Mounted OpenAI OAuth material routes traffic to the OAuth backend.
	if runtimeCfg.ProviderBaseURLs["openai"] != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("ProviderBaseURLs[openai] = %q, want codex backend", runtimeCfg.ProviderBaseURLs["openai"])
	}
}

func TestLoadRunConfigToleratesUnusableAdditionalOAuthMaterial(t *testing.T) {
	t.Setenv("WORKSPACE_DIR", "/workspace")
	t.Setenv("REPO_URL", "https://github.com/example/repo")
	t.Setenv("POD_NAMESPACE", "gratefulagents-system")
	t.Setenv("GH_PAT", "ghp-test")
	t.Setenv("PLANTASK_NAME", "run-1")
	t.Setenv("PLANTASK_UID", "uid-1")
	t.Setenv("MODEL", "anthropic/claude-sonnet-4-6")
	t.Setenv("AI_PROVIDER", "anthropic")
	t.Setenv("OPENAI_AUTH_MODE", "oauth")

	anthropicPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(anthropicPath, []byte(`{"access_token":"anthropic-access","refresh_token":"r","expired":"2099-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_OAUTH_AUTH_JSON_PATH", anthropicPath)
	// Unreadable additional material must not fail startup — the provider is
	// simply unavailable for live switches.
	t.Setenv("COPILOT_OAUTH_AUTH_JSON_PATH", filepath.Join(t.TempDir(), "missing.json"))

	cfg, err := loadRunConfig()
	if err != nil {
		t.Fatalf("loadRunConfig() error = %v, want nil (additional material is best-effort)", err)
	}
	if _, ok := cfg.ProviderAPIKeys["copilot"]; ok {
		t.Fatalf("ProviderAPIKeys[copilot] = %#v, want absent", cfg.ProviderAPIKeys)
	}
}

func TestLoadRunConfigReadsAnthropicOAuthAccessToken(t *testing.T) {
	t.Setenv("WORKSPACE_DIR", "/workspace")
	t.Setenv("REPO_URL", "https://github.com/example/repo")
	t.Setenv("POD_NAMESPACE", "gratefulagents-system")
	t.Setenv("GH_PAT", "ghp-test")
	t.Setenv("PLANTASK_NAME", "run-1")
	t.Setenv("PLANTASK_UID", "uid-1")
	t.Setenv("MODEL", "anthropic/claude-sonnet-4-6")
	t.Setenv("AI_PROVIDER", "anthropic")
	t.Setenv("OPENAI_AUTH_MODE", "oauth")
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"oauth-access","refresh_token":"oauth-refresh","expired":"2099-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_OAUTH_AUTH_JSON_PATH", authPath)

	cfg, err := loadRunConfig()
	if err != nil {
		t.Fatal(err)
	}
	runtimeCfg := sdkRuntimeProviderConfig(cfg, cfg.Model)
	if runtimeCfg.AuthMode != "oauth" {
		t.Fatalf("AuthMode = %q, want oauth", runtimeCfg.AuthMode)
	}
	if runtimeCfg.ProviderAPIKeys["anthropic"] != "oauth-access" {
		t.Fatalf("ProviderAPIKeys = %#v, want anthropic oauth access token", runtimeCfg.ProviderAPIKeys)
	}
	if runtimeCfg.APIKey != "oauth-access" {
		t.Fatalf("APIKey = %q, want oauth-access", runtimeCfg.APIKey)
	}
}

func TestLoadRunConfigReadsCopilotOAuthAccessToken(t *testing.T) {
	t.Setenv("WORKSPACE_DIR", "/workspace")
	t.Setenv("REPO_URL", "https://github.com/example/repo")
	t.Setenv("POD_NAMESPACE", "gratefulagents-system")
	t.Setenv("GH_PAT", "ghp-test")
	t.Setenv("PLANTASK_NAME", "run-1")
	t.Setenv("PLANTASK_UID", "uid-1")
	t.Setenv("MODEL", "copilot/gpt-4.1")
	t.Setenv("AI_PROVIDER", "copilot")
	t.Setenv("OPENAI_AUTH_MODE", "oauth")
	t.Setenv("OPENAI_API_MODE", "chat-completions")
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"oauth_token":"github-oauth","token":"copilot-api-token","expires_at":4070908800}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_OAUTH_AUTH_JSON_PATH", authPath)

	cfg, err := loadRunConfig()
	if err != nil {
		t.Fatal(err)
	}
	runtimeCfg := sdkRuntimeProviderConfig(cfg, cfg.Model)
	if runtimeCfg.AuthMode != "oauth" {
		t.Fatalf("AuthMode = %q, want oauth", runtimeCfg.AuthMode)
	}
	if runtimeCfg.ProviderAPIKeys["copilot"] != "copilot-api-token" {
		t.Fatalf("ProviderAPIKeys = %#v, want copilot oauth token", runtimeCfg.ProviderAPIKeys)
	}
	if runtimeCfg.APIKey != "copilot-api-token" {
		t.Fatalf("APIKey = %q, want copilot-api-token", runtimeCfg.APIKey)
	}
	if runtimeCfg.ProviderAPIModes["copilot"] != "chat-completions" {
		t.Fatalf("ProviderAPIModes = %#v", runtimeCfg.ProviderAPIModes)
	}
	if runtimeCfg.CopilotOAuthPath != authPath {
		t.Fatalf("CopilotOAuthPath = %q, want %q (SDK self-refresh wiring)", runtimeCfg.CopilotOAuthPath, authPath)
	}
}

func TestLoadRunConfigGitHubTokenOptional(t *testing.T) {
	base := func(t *testing.T) {
		t.Setenv("WORKSPACE_DIR", "/workspace")
		t.Setenv("POD_NAMESPACE", "gratefulagents-system")
		t.Setenv("PLANTASK_NAME", "run-1")
		t.Setenv("PLANTASK_UID", "uid-1")
		t.Setenv("MODEL", "openai/gpt-5.5")
		t.Setenv("GH_PAT", "")
	}

	for _, tc := range []struct {
		name               string
		repoURL            string
		additionalRepoURLs string
		wantRepoless       bool
	}{
		{name: "repoless", wantRepoless: true},
		{name: "primary repository", repoURL: "https://github.com/example/repo"},
		{name: "additional repository", additionalRepoURLs: "https://github.com/example/lib", wantRepoless: true},
	} {
		t.Run(tc.name+" without GH_PAT succeeds", func(t *testing.T) {
			base(t)
			t.Setenv("REPO_URL", tc.repoURL)
			t.Setenv("ADDITIONAL_REPO_URLS", tc.additionalRepoURLs)
			cfg, err := loadRunConfig()
			if err != nil {
				t.Fatalf("loadRunConfig() error = %v, want nil", err)
			}
			if cfg.Repoless != tc.wantRepoless {
				t.Fatalf("Repoless = %v, want %v", cfg.Repoless, tc.wantRepoless)
			}
			if cfg.GithubToken != "" {
				t.Fatalf("GithubToken = %q, want empty", cfg.GithubToken)
			}
		})
	}
}
