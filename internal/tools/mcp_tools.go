package tools

import (
	"context"

	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
	"github.com/gratefulagents/sdk/pkg/agentsdk/policy"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpManager interface {
	ToolDescriptors() []sdkmcp.ToolDescriptor
	ConnectedServerNames() []string
	HasResources() bool
	CallTool(ctx context.Context, qualifiedName string, args map[string]any) (*mcpsdk.CallToolResult, error)
	ListResources(ctx context.Context, serverName string) ([]sdkmcp.ResourceDescriptor, error)
	ReadResource(ctx context.Context, serverName, uri string) (*mcpsdk.ReadResourceResult, error)
}

// RegisterMCPTools registers all available MCP tools into the registry.
func RegisterMCPTools(registry *Registry, manager mcpManager, permissionMode policy.PermissionMode) {
	if registry == nil || manager == nil {
		return
	}

	permissionManager := &permissionMCPManager{
		mcpManager: manager,
		mode:       policy.NormalizePermissionMode(string(permissionMode)),
	}
	for _, tool := range sdkmcp.BuildTools(permissionManager) {
		registry.Register(tool)
	}
}

type permissionMCPManager struct {
	mcpManager
	mode policy.PermissionMode
}

func (m *permissionMCPManager) ToolDescriptors() []sdkmcp.ToolDescriptor {
	descriptors := m.mcpManager.ToolDescriptors()
	out := make([]sdkmcp.ToolDescriptor, 0, len(descriptors))
	for _, desc := range descriptors {
		if m.mode.AllowsMCPTool(desc.ReadOnly) {
			out = append(out, desc)
		}
	}
	return out
}
