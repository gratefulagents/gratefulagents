package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOAuthMaterialKind(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"copilot typed", `{"oauth_token":"gho_x","token":"tid=1","type":"copilot"}`, "copilot"},
		{"copilot untyped", `{"oauth_token":"gho_x","token":"tid=1"}`, "copilot"},
		{"anthropic typed", `{"access_token":"at","refresh_token":"rt","type":"claude"}`, "anthropic"},
		{"anthropic flat", `{"access_token":"at","refresh_token":"rt"}`, "anthropic"},
		{"claude credentials", `{"claudeAiOauth":{"accessToken":"at"}}`, "anthropic"},
		{"openai codex", `{"tokens":{"id_token":"idt","access_token":"at"}}`, "openai"},
		{"ambiguous", `{"access_token":"at"}`, ""},
		{"garbage", `nope`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := oauthMaterialKind([]byte(tt.json)); got != tt.want {
				t.Fatalf("oauthMaterialKind() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A Copilot secret mounted as the Anthropic auth.json (provider/secret wiring
// drift) must fail with a message naming the actual material, not a bare
// "missing both access and refresh tokens" parse error.
func TestAnthropicOAuthAccessTokenFromEnvExplainsCopilotMaterial(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"oauth_token":"gho_x","token":"tid=1;exp=2","type":"copilot"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_OAUTH_AUTH_JSON_PATH", authPath)

	_, err := anthropicOAuthAccessTokenFromEnv()
	if err == nil {
		t.Fatal("expected error for copilot material in anthropic mount")
	}
	if !strings.Contains(err.Error(), "holds copilot OAuth material") {
		t.Fatalf("error should name the actual material provider: %v", err)
	}
}

func TestCopilotOAuthAccessTokenFromEnvExplainsAnthropicMaterial(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"claudeAiOauth":{"expiresAt":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_OAUTH_AUTH_JSON_PATH", authPath)

	_, err := copilotOAuthAccessTokenFromEnv()
	if err == nil {
		t.Fatal("expected error for anthropic material in copilot mount")
	}
	if !strings.Contains(err.Error(), "holds anthropic OAuth material") {
		t.Fatalf("error should name the actual material provider: %v", err)
	}
}
