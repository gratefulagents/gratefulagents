package main

import (
	"regexp"
	"strings"
	"testing"

	sdkprojectstate "github.com/gratefulagents/sdk/pkg/agentsdk/projectstate"
	sdkprojectstatetools "github.com/gratefulagents/sdk/pkg/agentsdk/tools/projectstate"
)

// TestProjectStateGuidanceReferencesRealTools guards the durable-state prompt
// block against drift: every tool name it teaches must exist in the SDK
// projectstate tool surface (or be the sub-agent tool it contrasts with), and
// the core tools must stay mentioned.
func TestProjectStateGuidanceReferencesRealTools(t *testing.T) {
	guidance := projectStateGuidance()
	if strings.TrimSpace(guidance) == "" {
		t.Fatal("projectStateGuidance() returned empty guidance")
	}

	store, err := sdkprojectstate.NewFilesystemStore(sdkprojectstate.FilesystemOptions{
		WorkDir:  t.TempDir(),
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewFilesystemStore() error = %v", err)
	}
	valid := map[string]bool{
		// The guidance contrasts durable tasks with ephemeral sub-agent
		// delegation; the subagent tool is registered by the SDK scheduler.
		"subagent": true,
	}
	for _, tool := range sdkprojectstatetools.Tools(store, "test") {
		valid[tool.Name()] = true
	}

	toolToken := regexp.MustCompile(`[a-z]+_[a-z_]+`)
	for _, name := range toolToken.FindAllString(guidance, -1) {
		if !valid[name] {
			t.Errorf("projectStateGuidance() references %q, which is not a durable-state tool name", name)
		}
	}

	for _, want := range []string{"task_create", "memory_remember", "memory_recall", "prime_context"} {
		if !strings.Contains(guidance, want) {
			t.Errorf("projectStateGuidance() no longer mentions %q", want)
		}
	}
}
