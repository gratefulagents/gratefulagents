package main

import (
	"reflect"
	"testing"
)

func TestLoadRunConfigParsesOAuthFallbacks(t *testing.T) {
	t.Setenv("WORKSPACE_DIR", "/workspace")
	t.Setenv("REPO_URL", "https://github.com/example/repo")
	t.Setenv("POD_NAMESPACE", "gratefulagents-system")
	t.Setenv("GH_PAT", "ghp-test")
	t.Setenv("PLANTASK_NAME", "run-1")
	t.Setenv("PLANTASK_UID", "uid-1")
	t.Setenv("MODEL", "openai/gpt-5-codex")
	t.Setenv("AI_PROVIDER", "openai")
	t.Setenv("OPENAI_AUTH_MODE", "oauth")
	t.Setenv("OPENAI_OAUTH_FALLBACK_AUTH_JSON_PATHS",
		"/var/run/gratefulagents/oauth/fallback/openai/1/auth.json,/var/run/gratefulagents/oauth/fallback/openai/2/auth.json")
	t.Setenv("OPENAI_OAUTH_FALLBACK_ACCOUNT_ID_PATHS",
		"/var/run/gratefulagents/oauth/fallback/openai/1/account-id")
	t.Setenv("ANTHROPIC_OAUTH_FALLBACK_AUTH_JSON_PATHS",
		"/var/run/gratefulagents/oauth/fallback/anthropic/1/auth.json")

	cfg, err := loadRunConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]oauthFallbackCred{
		"openai": {
			{AuthJSONPath: "/var/run/gratefulagents/oauth/fallback/openai/1/auth.json", AccountIDPath: "/var/run/gratefulagents/oauth/fallback/openai/1/account-id"},
			// Account-id list shorter than the auth list: missing entries stay empty.
			{AuthJSONPath: "/var/run/gratefulagents/oauth/fallback/openai/2/auth.json"},
		},
		"anthropic": {
			{AuthJSONPath: "/var/run/gratefulagents/oauth/fallback/anthropic/1/auth.json"},
		},
	}
	if !reflect.DeepEqual(cfg.OAuthFallbacks, want) {
		t.Fatalf("OAuthFallbacks = %#v, want %#v", cfg.OAuthFallbacks, want)
	}
}

func TestLoadRunConfigIgnoresOAuthFallbacksWithoutOAuthMode(t *testing.T) {
	t.Setenv("WORKSPACE_DIR", "/workspace")
	t.Setenv("REPO_URL", "https://github.com/example/repo")
	t.Setenv("POD_NAMESPACE", "gratefulagents-system")
	t.Setenv("GH_PAT", "ghp-test")
	t.Setenv("PLANTASK_NAME", "run-1")
	t.Setenv("PLANTASK_UID", "uid-1")
	t.Setenv("MODEL", "openai/gpt-5-codex")
	t.Setenv("AI_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("OPENAI_OAUTH_FALLBACK_AUTH_JSON_PATHS", "/var/run/gratefulagents/oauth/fallback/openai/1/auth.json")

	cfg, err := loadRunConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OAuthFallbacks != nil {
		t.Fatalf("OAuthFallbacks = %#v, want nil outside oauth mode", cfg.OAuthFallbacks)
	}
}

func TestOAuthFallbackRoutesBuildsNamedSubscriptionRoutes(t *testing.T) {
	cfg := runConfig{
		Provider: "openai",
		OAuthFallbacks: map[string][]oauthFallbackCred{
			"openai": {
				{AuthJSONPath: "/fb/openai/1/auth.json", AccountIDPath: "/fb/openai/1/account-id"},
				{AuthJSONPath: "/fb/openai/2/auth.json", AccountIDPath: "/fb/openai/2/account-id"},
			},
			"anthropic": {
				{AuthJSONPath: "/fb/anthropic/1/auth.json"},
			},
			"copilot": {
				{AuthJSONPath: "/fb/copilot/1/auth.json"},
			},
		},
	}
	routes := oauthFallbackRoutes(cfg)
	if len(routes) != 4 {
		t.Fatalf("len(routes) = %d, want 4: %#v", len(routes), routes)
	}
	if routes[0].Prefix != "openai-sub2" || routes[0].Provider != "openai" || routes[0].AuthMode != "oauth" ||
		routes[0].OpenAIOAuthPath != "/fb/openai/1/auth.json" || routes[0].OpenAIOAuthAccountIDPath != "/fb/openai/1/account-id" {
		t.Fatalf("routes[0] = %#v", routes[0])
	}
	if routes[1].Prefix != "openai-sub3" || routes[1].OpenAIOAuthPath != "/fb/openai/2/auth.json" || routes[1].OpenAIOAuthAccountIDPath != "/fb/openai/2/account-id" {
		t.Fatalf("routes[1] = %#v", routes[1])
	}
	if routes[2].Prefix != "anthropic-sub2" || routes[2].Provider != "anthropic" || routes[2].AuthMode != "oauth" ||
		routes[2].AnthropicOAuthPath != "/fb/anthropic/1/auth.json" || routes[2].OpenAIOAuthPath != "" {
		t.Fatalf("routes[2] = %#v", routes[2])
	}
	if routes[3].Prefix != "copilot-sub2" || routes[3].Provider != "copilot" || routes[3].AuthMode != "oauth" ||
		routes[3].CopilotOAuthPath != "/fb/copilot/1/auth.json" {
		t.Fatalf("routes[3] = %#v", routes[3])
	}
	for _, route := range routes {
		if route.BaseURL != "" {
			t.Fatalf("route %q BaseURL = %q, want empty (SDK defaults)", route.Prefix, route.BaseURL)
		}
	}

	runtimeCfg := sdkRuntimeProviderConfig(cfg, "openai/gpt-5-codex")
	if !reflect.DeepEqual(runtimeCfg.Routes, routes) {
		t.Fatalf("sdkRuntimeProviderConfig Routes = %#v, want %#v", runtimeCfg.Routes, routes)
	}
}

func TestSubscriptionFallbackModels(t *testing.T) {
	cfg := runConfig{
		Provider: "openai",
		OAuthFallbacks: map[string][]oauthFallbackCred{
			"openai": {
				{AuthJSONPath: "/fb/openai/1/auth.json"},
				{AuthJSONPath: "/fb/openai/2/auth.json"},
			},
			"anthropic": {
				{AuthJSONPath: "/fb/anthropic/1/auth.json"},
			},
		},
	}

	// Prefixed model: the startup provider prefix is stripped.
	got := subscriptionFallbackModels(cfg, "openai/gpt-5-codex")
	want := []string{"openai-sub2/gpt-5-codex", "openai-sub3/gpt-5-codex"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subscriptionFallbackModels(prefixed) = %#v, want %#v", got, want)
	}

	// Bare model: used as-is.
	got = subscriptionFallbackModels(cfg, "gpt-5-codex")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subscriptionFallbackModels(bare) = %#v, want %#v", got, want)
	}

	// Model routed to a different provider than the startup provider: no
	// subscription fallbacks (only cfg.Provider's subscriptions apply).
	if got := subscriptionFallbackModels(cfg, "anthropic/claude-sonnet-4-6"); got != nil {
		t.Fatalf("subscriptionFallbackModels(other provider) = %#v, want nil", got)
	}

	// No fallbacks configured for the startup provider.
	if got := subscriptionFallbackModels(runConfig{Provider: "copilot", OAuthFallbacks: cfg.OAuthFallbacks}, "copilot/gpt-4.1"); got != nil {
		t.Fatalf("subscriptionFallbackModels(no creds) = %#v, want nil", got)
	}
}

func TestMergedFallbackModelsPutsSubscriptionFailoverFirst(t *testing.T) {
	cfg := runConfig{
		Provider: "openai",
		OAuthFallbacks: map[string][]oauthFallbackCred{
			"openai": {{AuthJSONPath: "/fb/openai/1/auth.json"}},
		},
	}
	got := mergedFallbackModels(cfg, "openai/gpt-5-codex", []string{"anthropic/claude-sonnet-4-6", "copilot/gpt-4.1"})
	want := []string{"openai-sub2/gpt-5-codex", "anthropic/claude-sonnet-4-6", "copilot/gpt-4.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergedFallbackModels = %#v, want %#v", got, want)
	}

	// Without subscriptions, template fallbacks pass through unchanged.
	got = mergedFallbackModels(runConfig{Provider: "openai"}, "openai/gpt-5-codex", []string{"copilot/gpt-4.1"})
	if !reflect.DeepEqual(got, []string{"copilot/gpt-4.1"}) {
		t.Fatalf("mergedFallbackModels(no subs) = %#v", got)
	}
}
