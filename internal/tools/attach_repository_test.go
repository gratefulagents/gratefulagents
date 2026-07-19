package tools

import (
	"strings"
	"testing"
)

func TestRegisterAttachRepositoryTool(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	RegisterAttachRepositoryTool(registry, "main", "agent-run-1")

	tool := registry.Get("attach_repository")
	if tool == nil {
		t.Fatalf("attach_repository was not registered; names=%v", registry.Names())
	}
	if tool.IsReadOnly() {
		t.Fatalf("attach_repository reported read-only")
	}
	schema := string(tool.InputSchema())
	if strings.Contains(schema, "primary") {
		t.Fatalf("attach_repository schema still exposes primary: %s", schema)
	}
	if !strings.Contains(tool.Description(), "repos/<alias>") {
		t.Fatalf("attach_repository description does not mention repo list path: %s", tool.Description())
	}
}
