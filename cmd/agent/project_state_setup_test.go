package main

import (
	"context"
	"strings"
	"testing"
)

func TestSetupProjectStateDisabledWhenFlagIsFalse(t *testing.T) {
	t.Setenv("ENABLE_MEMORY", "false")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("OPENAI_API_KEY", "sk-test")

	store, pool, status := setupProjectState(context.Background(), runConfig{Namespace: "ns", TaskName: "run-1"})
	if store != nil {
		t.Fatal("setupProjectState() store = non-nil, want nil")
	}
	if pool != nil {
		t.Fatal("setupProjectState() pool = non-nil, want nil")
	}
	if status.enabled {
		t.Fatal("setupProjectState() enabled = true, want false")
	}
	if status.message != "durable project state disabled" {
		t.Fatalf("setupProjectState() message = %q, want %q", status.message, "durable project state disabled")
	}
}

func TestSetupProjectStateEnabledByDefault(t *testing.T) {
	// Unset flag defaults to enabled (graceful degradation when no DB).
	t.Setenv("ENABLE_MEMORY", "")
	t.Setenv("DATABASE_URL", "")

	store, pool, status := setupProjectState(context.Background(), runConfig{Namespace: "ns", TaskName: "run-1"})
	if store != nil || pool != nil {
		t.Fatal("setupProjectState() without DATABASE_URL should not build a store")
	}
	if status.err == nil {
		t.Fatal("setupProjectState() err = nil, want DATABASE_URL error (proves default-on)")
	}
	if !strings.Contains(status.message, "DATABASE_URL not set") {
		t.Fatalf("setupProjectState() message = %q, want DATABASE_URL failure", status.message)
	}
}

func TestSetupProjectStateErrorsWhenDatabaseURLMissing(t *testing.T) {
	t.Setenv("ENABLE_MEMORY", "true")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "sk-test")

	store, pool, status := setupProjectState(context.Background(), runConfig{Namespace: "ns", TaskName: "run-1"})
	if store != nil {
		t.Fatal("setupProjectState() store = non-nil, want nil")
	}
	if pool != nil {
		t.Fatal("setupProjectState() pool = non-nil, want nil")
	}
	if status.enabled {
		t.Fatal("setupProjectState() enabled = true, want false")
	}
	if status.err == nil {
		t.Fatal("setupProjectState() err = nil, want non-nil")
	}
	if !strings.Contains(status.message, "DATABASE_URL not set") {
		t.Fatalf("setupProjectState() message = %q, want DATABASE_URL failure", status.message)
	}
}

func TestSetupProjectStateAllowsMissingEmbedderAuth(t *testing.T) {
	// Without OpenAI auth the store still comes up; recall degrades to lexical.
	t.Setenv("ENABLE_MEMORY", "true")
	t.Setenv("DATABASE_URL", "postgres://127.0.0.1:5432/test?connect_timeout=1&sslmode=disable")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_AUTH_MODE", "")

	store, pool, status := setupProjectState(context.Background(), runConfig{Namespace: "ns", TaskName: "run-1"})
	if status.err != nil {
		t.Fatalf("setupProjectState() err = %v, want nil", status.err)
	}
	if store == nil {
		t.Fatal("setupProjectState() store = nil, want non-nil")
	}
	if pool == nil {
		t.Fatal("setupProjectState() pool = nil, want non-nil")
	}
	defer pool.Close()
	if !status.enabled {
		t.Fatal("setupProjectState() enabled = false, want true")
	}
	if !strings.Contains(status.message, "lexical recall") {
		t.Fatalf("setupProjectState() message = %q, want lexical recall note", status.message)
	}
}

func TestProjectStateID(t *testing.T) {
	canonical := projectStateID(runConfig{Namespace: "team-a", RepoURL: "https://github.com/acme/widgets.git"})
	for _, repository := range []string{
		"https://github.com/acme/widgets.git/",
		"git@github.com:acme/widgets.git",
		"ssh://git@github.com/acme/widgets/",
	} {
		if got := projectStateID(runConfig{Namespace: "team-a", RepoURL: repository}); got != canonical {
			t.Errorf("projectStateID(%q) = %q, want canonical %q", repository, got, canonical)
		}
	}
	if !strings.HasPrefix(canonical, "team-a-github-com-acme-widgets-") {
		t.Fatalf("projectStateID() = %q, want readable prefix and hash", canonical)
	}
	if got := projectStateID(runConfig{Namespace: "team-a"}); got != "team-a-chat" {
		t.Fatalf("repoless projectStateID() = %q, want team-a-chat", got)
	}

	first := projectStateID(runConfig{Namespace: "team-a", RepoURL: "https://example.com/a/b"})
	second := projectStateID(runConfig{Namespace: "team-a", RepoURL: "https://example.com/a-b"})
	if first == second {
		t.Fatalf("sanitization collision shared project ID %q", first)
	}
}

func TestCompactionOverridesRequirePositiveOrderedValues(t *testing.T) {
	clearCompactionEnv(t)
	baseline := resolveCompactionConfig(context.Background(), "", "", nil)

	t.Setenv("ENGG_OPERATOR_COMPACTION_TRIGGER_TOKENS", "1000")
	t.Setenv("ENGG_OPERATOR_COMPACTION_TARGET_TOKENS", "500")
	valid := resolveCompactionConfig(context.Background(), "", "", nil)
	if valid.TriggerTokens != 1000 || valid.TargetTokens != 500 {
		t.Fatalf("valid overrides = (%d, %d), want (1000, 500)", valid.TriggerTokens, valid.TargetTokens)
	}

	t.Setenv("ENGG_OPERATOR_COMPACTION_TRIGGER_TOKENS", "0")
	t.Setenv("ENGG_OPERATOR_COMPACTION_TARGET_TOKENS", "-1")
	invalid := resolveCompactionConfig(context.Background(), "", "", nil)
	if invalid.TriggerTokens != baseline.TriggerTokens || invalid.TargetTokens != baseline.TargetTokens {
		t.Fatalf("non-positive overrides = (%d, %d), want defaults (%d, %d)", invalid.TriggerTokens, invalid.TargetTokens, baseline.TriggerTokens, baseline.TargetTokens)
	}

	t.Setenv("ENGG_OPERATOR_COMPACTION_TRIGGER_TOKENS", "500")
	t.Setenv("ENGG_OPERATOR_COMPACTION_TARGET_TOKENS", "500")
	unordered := resolveCompactionConfig(context.Background(), "", "", nil)
	if unordered.TriggerTokens != baseline.TriggerTokens || unordered.TargetTokens != baseline.TargetTokens {
		t.Fatalf("unordered overrides = (%d, %d), want defaults (%d, %d)", unordered.TriggerTokens, unordered.TargetTokens, baseline.TriggerTokens, baseline.TargetTokens)
	}
}

func TestHandoffOverridesRequirePositiveOrderedValues(t *testing.T) {
	t.Setenv("ENGG_OPERATOR_DISABLE_NESTED_HANDOFF_HISTORY", "")
	t.Setenv("ENGG_OPERATOR_HANDOFF_HISTORY_MAX_TOKENS", "9000")
	t.Setenv("ENGG_OPERATOR_HANDOFF_HISTORY_TARGET_TOKENS", "4000")
	valid := resolveHandoffHistoryConfig()
	if valid.MaxTokens != 9000 || valid.TargetTokens != 4000 {
		t.Fatalf("valid overrides = (%d, %d), want (9000, 4000)", valid.MaxTokens, valid.TargetTokens)
	}

	t.Setenv("ENGG_OPERATOR_HANDOFF_HISTORY_MAX_TOKENS", "-2")
	t.Setenv("ENGG_OPERATOR_HANDOFF_HISTORY_TARGET_TOKENS", "0")
	invalid := resolveHandoffHistoryConfig()
	if invalid.MaxTokens != 6000 || invalid.TargetTokens != 3000 {
		t.Fatalf("non-positive overrides = (%d, %d), want defaults", invalid.MaxTokens, invalid.TargetTokens)
	}

	t.Setenv("ENGG_OPERATOR_HANDOFF_HISTORY_MAX_TOKENS", "2000")
	t.Setenv("ENGG_OPERATOR_HANDOFF_HISTORY_TARGET_TOKENS", "3000")
	unordered := resolveHandoffHistoryConfig()
	if unordered.MaxTokens != 6000 || unordered.TargetTokens != 3000 {
		t.Fatalf("unordered overrides = (%d, %d), want defaults", unordered.MaxTokens, unordered.TargetTokens)
	}
}

func clearCompactionEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ENGG_OPERATOR_DISABLE_CONTEXT_COMPACTION", "")
	t.Setenv("ENGG_OPERATOR_COMPACTION_TRIGGER_TOKENS", "")
	t.Setenv("ENGG_OPERATOR_COMPACTION_TARGET_TOKENS", "")
}

func TestSplitCommaList(t *testing.T) {
	got := splitCommaList(" openai/gpt-5 , anthropic/claude-4 ,, ")
	if len(got) != 2 || got[0] != "openai/gpt-5" || got[1] != "anthropic/claude-4" {
		t.Fatalf("splitCommaList() = %#v, want two trimmed entries", got)
	}
	if splitCommaList("") != nil {
		t.Fatal("splitCommaList(\"\") should be nil")
	}
}

func TestEnvFlagEnabled(t *testing.T) {
	tests := []struct {
		value    string
		fallback bool
		expect   bool
	}{
		{"", true, true},
		{"", false, false},
		{"true", false, true},
		{"TRUE", false, true},
		{"1", false, true},
		{"false", true, false},
		{"no", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv("TEST_FLAG_X", tt.value)
			if got := envFlagEnabled("TEST_FLAG_X", tt.fallback); got != tt.expect {
				t.Fatalf("envFlagEnabled(%q, %v) = %v, want %v", tt.value, tt.fallback, got, tt.expect)
			}
		})
	}
}
