package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenericToolCopiesStayOutOfOperator(t *testing.T) {
	root := repoRoot(t)
	forbiddenFiles := []string{
		"cmd/agent/role_instruction_cache.go",
		"cmd/agent/subagent_task_tools.go",
		"internal/agenttools/legacy.go",
		"internal/commandexec/executor.go",
		"internal/mcp/manager.go",
		"internal/metaharness/trace_writer.go",
		"internal/skillregistry/registry.go",
		"internal/tools/bash.go",
		"internal/tools/browser.go",
		"internal/tools/fileedit.go",
		"internal/tools/filewrite.go",
		"internal/tools/lsp.go",
		"internal/tools/pathutil.go",
		"internal/tools/plan_tools.go",
		"internal/tools/present_plan.go",
		"internal/tools/vision.go",
		"internal/tools/webfetch.go",
	}
	for _, rel := range forbiddenFiles {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Fatalf("generic SDK-owned code must not live in operator: %s", rel)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", rel, err)
		}
	}
}

func TestOperatorRegistryDoesNotReintroduceLegacyToolBoundary(t *testing.T) {
	root := repoRoot(t)
	registry, err := os.ReadFile(filepath.Join(root, "internal/tools/registry.go"))
	if err != nil {
		t.Fatalf("read registry.go: %v", err)
	}
	if strings.Contains(string(registry), "type Tool interface") {
		t.Fatal("operator registry reintroduced a local Tool interface; use agentsdk.Tool")
	}

	plan, err := os.ReadFile(filepath.Join(root, "cmd/agent/plan.go"))
	if err != nil {
		t.Fatalf("read plan.go: %v", err)
	}
	if strings.Contains(string(plan), "WrapLegacyTool") || strings.Contains(string(plan), "internal/agenttools") {
		t.Fatal("operator chat loop reintroduced legacy tool wrapping")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
