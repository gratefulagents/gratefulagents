package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
	"github.com/gratefulagents/sdk/pkg/agentsdk/policy"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeMCPManager struct {
	descriptors []sdkmcp.ToolDescriptor
	servers     []string
	resources   []sdkmcp.ResourceDescriptor
	callResult  *mcpsdk.CallToolResult
	callCount   int
	readServer  string
	readURI     string
}

func (f *fakeMCPManager) ToolDescriptors() []sdkmcp.ToolDescriptor {
	return append([]sdkmcp.ToolDescriptor(nil), f.descriptors...)
}

func (f *fakeMCPManager) ConnectedServerNames() []string {
	return append([]string(nil), f.servers...)
}

func (f *fakeMCPManager) HasResources() bool { return len(f.resources) > 0 }

func (f *fakeMCPManager) CallTool(ctx context.Context, qualifiedName string, args map[string]any) (*mcpsdk.CallToolResult, error) {
	f.callCount++
	return f.callResult, nil
}

func (f *fakeMCPManager) ListResources(ctx context.Context, serverName string) ([]sdkmcp.ResourceDescriptor, error) {
	return f.resources, nil
}

func (f *fakeMCPManager) ReadResource(ctx context.Context, serverName, uri string) (*mcpsdk.ReadResourceResult, error) {
	f.readServer, f.readURI = serverName, uri
	return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{URI: uri, Text: "resource content"}}}, nil
}

func TestRegisterMCPToolsAllowsConfiguredToolsAndResources(t *testing.T) {
	t.Parallel()
	for _, mode := range []policy.PermissionMode{policy.PermissionModeWorkspaceWrite, policy.PermissionModeReadOnly} {
		t.Run(string(mode), func(t *testing.T) {
			manager := &fakeMCPManager{
				descriptors: []sdkmcp.ToolDescriptor{
					{QualifiedName: "mcp__github__get_issue", ServerName: "github", ToolName: "get_issue", ReadOnly: true},
					{QualifiedName: "mcp__github__create_issue", ServerName: "github", ToolName: "create_issue"},
				},
				servers: []string{"github", "files"},
				resources: []sdkmcp.ResourceDescriptor{
					{Server: "github", URI: "issue://1"},
					{Server: "files", URI: "file://notes"},
				},
				callResult: &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "issue result"}}},
			}
			registry := NewRegistry(t.TempDir())
			RegisterMCPTools(registry, manager, mode)
			if registry.Get("RequestMCPBreakGlass") != nil {
				t.Fatal("obsolete break-glass tool registered")
			}
			for _, name := range []string{"mcp__github__get_issue", "mcp__github__create_issue"} {
				tool := registry.Get(name)
				if mode == policy.PermissionModeReadOnly && name == "mcp__github__create_issue" {
					if tool != nil {
						t.Fatal("mutating MCP tool registered in read-only mode")
					}
					continue
				}
				if tool == nil {
					t.Fatalf("configured tool %q not registered", name)
				}
				result, err := tool.Execute(context.Background(), json.RawMessage(`{}`), t.TempDir())
				if err != nil || result.IsError || result.Content != "issue result" || result.ShouldPause {
					t.Fatalf("Execute(%s) = %+v, %v", name, result, err)
				}
			}
			wantCalls := 2
			if mode == policy.PermissionModeReadOnly {
				wantCalls = 1
			}
			if manager.callCount != wantCalls {
				t.Fatalf("CallTool count = %d, want %d", manager.callCount, wantCalls)
			}
			list := registry.Get("ListMcpResourcesTool")
			read := registry.Get("ReadMcpResourceTool")
			if list == nil || read == nil {
				t.Fatal("resource tools not registered")
			}
			result, err := list.Execute(context.Background(), nil, t.TempDir())
			if err != nil || result.IsError || !strings.Contains(result.Content, "issue://1") || !strings.Contains(result.Content, "file://notes") {
				t.Fatalf("ListResources = %+v, %v", result, err)
			}
			result, err = read.Execute(context.Background(), json.RawMessage(`{"server":"files","uri":"file://notes"}`), t.TempDir())
			if err != nil || result.IsError || !strings.Contains(result.Content, "resource content") {
				t.Fatalf("ReadResource = %+v, %v", result, err)
			}
			if manager.readServer != "files" || manager.readURI != "file://notes" {
				t.Fatalf("ReadResource target = %q, %q", manager.readServer, manager.readURI)
			}
		})
	}
}

func TestRegisterMCPToolsPreservesRegistryFilters(t *testing.T) {
	t.Parallel()
	manager := &fakeMCPManager{descriptors: []sdkmcp.ToolDescriptor{
		{QualifiedName: "mcp__github__get_issue", ReadOnly: true},
		{QualifiedName: "mcp__github__create_issue"},
	}}
	registry := NewRegistry(t.TempDir(), WithReadOnlyTools(), WithToolNameFilter(nil, []string{"mcp__github__get_issue"}))
	RegisterMCPTools(registry, manager, policy.PermissionModeWorkspaceWrite)
	for _, desc := range manager.descriptors {
		if registry.Get(desc.QualifiedName) != nil {
			t.Errorf("MCP tool %q bypassed registry filter", desc.QualifiedName)
		}
	}
}
