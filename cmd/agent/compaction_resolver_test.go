package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkopenai "github.com/gratefulagents/sdk/pkg/agentsdk/providers/openai"
)

// The resolver must be disabled entirely when ops pin thresholds (or disable
// compaction) via env, so those overrides are never clobbered by catalog or
// static lookups at request time.
func TestNewCompactionModelResolverEnvOverridesDisableIt(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"trigger override", "ENGG_OPERATOR_COMPACTION_TRIGGER_TOKENS", "12345"},
		{"target override", "ENGG_OPERATOR_COMPACTION_TARGET_TOKENS", "6789"},
		{"compaction disabled", "ENGG_OPERATOR_DISABLE_CONTEXT_COMPACTION", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if r := newCompactionModelResolver(runConfig{Provider: "anthropic"}, nil); r != nil {
				t.Fatalf("expected nil resolver with %s set", tc.key)
			}
		})
	}
}

// Without a catalog provider or backend metadata, the resolver must still
// resolve via the static per-model table so sub-agents pinned to other models
// never inherit the parent model's thresholds. An empty provider skips the
// models.dev lookup, keeping this hermetic (no network).
func TestNewCompactionModelResolverStaticFallback(t *testing.T) {
	r := newCompactionModelResolver(runConfig{Provider: ""}, nil)
	if r == nil {
		t.Fatal("expected a resolver")
	}
	trigger, target, ok := r(context.Background(), "claude-opus-4-6")
	if !ok || trigger <= 0 || target <= 0 || target >= trigger {
		t.Fatalf("expected static thresholds, got trigger=%d target=%d ok=%v", trigger, target, ok)
	}
}

// fakeCodexModelsServer serves the ChatGPT-backend /models shape.
func fakeCodexModelsServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.5","context_window":272000,"max_context_window":272000}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func testCodexMetadataResolver(t *testing.T) *openAIModelMetadataResolver {
	t.Helper()
	server := fakeCodexModelsServer(t)
	return sdkopenai.NewCompactionMetadataResolver(server.URL, sdkopenai.NewAPIKeyAuthSession("test-key"))
}

// A run that starts on copilot and switches to openai/gpt-5.5 mid-run must
// resolve the backend's real window (272K → trigger 244.8K), not the
// models.dev API-key numbers (1.05M → trigger 945K shown by the context bar
// in run chat-gf-all-i8fml9). The OAuth-routed leg skips the catalog, so this
// stays hermetic.
func TestNewCompactionModelResolverMidRunSwitchUsesCodexMetadata(t *testing.T) {
	metadata := testCodexMetadataResolver(t)
	r := newCompactionModelResolver(runConfig{Provider: "copilot"}, metadata)
	if r == nil {
		t.Fatal("expected a resolver")
	}
	trigger, target, ok := r(context.Background(), "openai/gpt-5.5")
	if !ok || trigger != 244800 || target != 136000 {
		t.Fatalf("openai/gpt-5.5 on copilot-start run = trigger=%d target=%d ok=%v, want 244800/136000 from codex metadata", trigger, target, ok)
	}
}

// Bare model names inherit the startup provider: on an openai-start run they
// route to backend metadata, on other runs they keep their catalog/static path.
func TestNewCompactionModelResolverBareNameFollowsStartupProvider(t *testing.T) {
	metadata := testCodexMetadataResolver(t)
	r := newCompactionModelResolver(runConfig{Provider: "openai"}, metadata)
	if r == nil {
		t.Fatal("expected a resolver")
	}
	trigger, target, ok := r(context.Background(), "gpt-5.5")
	if !ok || trigger != 244800 || target != 136000 {
		t.Fatalf("bare gpt-5.5 on openai-start run = trigger=%d target=%d ok=%v, want codex metadata", trigger, target, ok)
	}
}

// resolveCompactionConfig must scope the backend metadata to models that
// actually route to that backend: the fable leg of a mixed run keeps its
// static thresholds, the gpt-5.5 leg gets the backend window.
func TestResolveCompactionConfigPerLegOfModelSwitch(t *testing.T) {
	metadata := testCodexMetadataResolver(t)

	fable := resolveCompactionConfig(context.Background(), "copilot/claude-fable-5", "copilot", metadata)
	staticTrigger, staticTarget := agent.CompactionDefaultsForModel("copilot/claude-fable-5")
	if fable.TriggerTokens != staticTrigger || fable.TargetTokens != staticTarget {
		t.Fatalf("fable leg = %d/%d, want static %d/%d (codex metadata must not apply)",
			fable.TriggerTokens, fable.TargetTokens, staticTrigger, staticTarget)
	}

	codex := resolveCompactionConfig(context.Background(), "openai/gpt-5.5", "openai", metadata)
	if codex.TriggerTokens != 244800 || codex.TargetTokens != 136000 {
		t.Fatalf("gpt-5.5 leg = %d/%d, want 244800/136000 from codex metadata", codex.TriggerTokens, codex.TargetTokens)
	}
}
