package tools

import (
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	"github.com/gratefulagents/sdk/pkg/agentsdk/policy"
	sdktools "github.com/gratefulagents/sdk/pkg/agentsdk/tools"
	sdkvision "github.com/gratefulagents/sdk/pkg/agentsdk/tools/vision"
	sdkweb "github.com/gratefulagents/sdk/pkg/agentsdk/tools/web"
)

// Result from tool execution.
type Result = agentsdk.ToolResult

// Tool defines a single tool that can be called by the model.
type Tool = agentsdk.Tool

// ToolDef is a serializable tool definition for the API.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Registry holds all available tools.
type Registry struct {
	tools               map[string]Tool
	permissionMode      policy.PermissionMode
	signals             bool
	allowMutating       map[string]struct{}
	gitRemoteWrites     policy.GitRemoteWrites
	browser             bool
	interactiveTerminal bool
	vision              bool
	visionAnalyze       sdkvision.AnalyzeFn
	closers             []io.Closer
}

// RegistryOption configures the registry.
type RegistryOption func(*Registry)

// WithReadOnlyTools restricts the registry to only read-only tools.
func WithReadOnlyTools() RegistryOption {
	return WithPermissionMode(policy.PermissionModeReadOnly)
}

// WithPermissionMode configures the registry's mutability mode.
func WithPermissionMode(mode policy.PermissionMode) RegistryOption {
	return func(r *Registry) { r.permissionMode = policy.NormalizePermissionMode(string(mode)) }
}

// WithGitRemoteWrites configures whether tools and shell commands may mutate
// Git remotes. Disabled mode still permits workspace edits and local commits.
func WithGitRemoteWrites(mode policy.GitRemoteWrites) RegistryOption {
	return func(r *Registry) { r.gitRemoteWrites = policy.NormalizeGitRemoteWrites(mode) }
}

// WithSignalTools enables the AskUserQuestion signal tool.
func WithSignalTools() RegistryOption {
	return func(r *Registry) { r.signals = true }
}

// WithBrowserTools enables the headless browser tool in the registry.
func WithBrowserTools() RegistryOption {
	return func(r *Registry) { r.browser = true }
}

// WithInteractiveTerminal enables the persistent PTY terminal tool for
// interactive programs (Unix only; requires a write permission mode).
func WithInteractiveTerminal() RegistryOption {
	return func(r *Registry) { r.interactiveTerminal = true }
}

// WithVisionTools enables the image analysis tool in the registry. A nil
// analyzer registers the tool shell so the SDK runtime can attach the selected
// provider's vision implementation while building the agent.
func WithVisionTools(analyzeFn func(ctx context.Context, imageData []byte, mimeType, prompt string) (string, error)) RegistryOption {
	return func(r *Registry) {
		r.vision = true
		r.visionAnalyze = sdkvision.AnalyzeFn(analyzeFn)
	}
}

// WithAllowedMutatingTools allows specific non-read-only tools to remain
// available even when the registry permission mode is read-only.
func WithAllowedMutatingTools(names ...string) RegistryOption {
	return func(r *Registry) {
		if r.allowMutating == nil {
			r.allowMutating = make(map[string]struct{}, len(names))
		}
		for _, name := range names {
			if name == "" {
				continue
			}
			r.allowMutating[name] = struct{}{}
		}
	}
}

// NewRegistry creates a registry with all default tools.
func NewRegistry(workDir string, opts ...RegistryOption) *Registry {
	r := &Registry{
		tools:           make(map[string]Tool),
		permissionMode:  policy.PermissionModeWorkspaceWrite,
		gitRemoteWrites: policy.GitRemoteWritesEnabled,
	}
	for _, opt := range opts {
		opt(r)
	}

	sdkOpts := []sdktools.RegistryOption{
		sdktools.WithPermissionMode(r.permissionMode),
		sdktools.WithGitRemoteWrites(r.gitRemoteWrites),
		// Think scratchpad (SDK v0.0.9): read-only reasoning log between actions.
		sdktools.WithThinkTool(),
	}
	if r.signals {
		sdkOpts = append(sdkOpts, sdktools.WithSignalTools())
	}
	if r.browser {
		// Chromium cannot safely confine redirects, DNS changes, and page
		// subresources to public-only destinations. Enabling Browser is the
		// explicit opt-in to its unrestricted/private networking requirement;
		// the RuntimeProfile egress policy remains the outer boundary.
		sdkOpts = append(sdkOpts,
			sdktools.WithBrowserTools(),
			sdktools.WithPrivateNetworkURLs(true),
		)
	}
	if r.interactiveTerminal {
		sdkOpts = append(sdkOpts, sdktools.WithInteractiveTerminal())
	}
	if r.vision {
		sdkOpts = append(sdkOpts, sdktools.WithVisionTools(r.visionAnalyze))
	}
	if len(r.allowMutating) > 0 {
		names := make([]string, 0, len(r.allowMutating))
		for name := range r.allowMutating {
			names = append(names, name)
		}
		sdkOpts = append(sdkOpts, sdktools.WithAllowedMutatingTools(names...))
	}

	sdkRegistry := sdktools.NewRegistry(workDir, sdkOpts...)
	for _, tool := range sdkRegistry.Tools() {
		// The SDK uses one private-network gate to register Browser, WebFetch,
		// and vision. Browser needs that gate because Chromium cannot enforce
		// destination-by-destination checks, but the URL-based tools can and
		// should retain their public-only default.
		switch typed := tool.(type) {
		case *sdkweb.FetchTool:
			typed.AllowPrivateNetworkURLs = false
		case *sdkvision.Tool:
			typed.AllowPrivateNetworkURLs = false
		}
		r.Register(tool)
	}
	r.closers = sdkRegistry.Closers()

	return r
}

// Closers returns resources (e.g. the PTY terminal manager) that should be
// closed when the agent shuts down.
func (r *Registry) Closers() []io.Closer {
	if r == nil {
		return nil
	}
	return r.closers
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	if t == nil {
		return
	}
	if r.gitRemoteWrites == policy.GitRemoteWritesDisabled && isGitRemoteWriteTool(t) {
		return
	}
	// Control-flow tools are always registered regardless of permission mode.
	// They are needed for phase transitions, finishing, mode switching, etc.
	if !t.IsReadOnly() && !isRegistryControlFlowTool(t.Name()) && !r.permissionMode.AllowsWriteTools() {
		if _, ok := r.allowMutating[t.Name()]; !ok {
			return
		}
	}
	r.tools[t.Name()] = t
}

// Remove unregisters a tool after contextual authorization has narrowed the
// effective mode (for example, controller-cutover maintainer delivery).
func (r *Registry) Remove(name string) {
	if r == nil {
		return
	}
	delete(r.tools, name)
}

// isRegistryControlFlowTool returns true for tools that must always be
// registered regardless of permission mode. These are orchestration tools
// (not data-mutating) that the agent needs to function at all.
func isRegistryControlFlowTool(name string) bool {
	switch name {
	case "finish", "save_plan", "get_plan", "RequestMCPBreakGlass":
		return true
	}
	return false
}

func isGitRemoteWriteTool(tool Tool) bool {
	return sdktools.WritesGitRemote(tool)
}

// Get returns a tool by name, or nil if not found.
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// GetToolDefinitions returns tool definitions for the Anthropic API.
func (r *Registry) GetToolDefinitions() []ToolDef {
	defs := make([]ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	// Sort for stable prompt caching.
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Name < defs[j].Name
	})
	return defs
}

// Names returns all registered tool names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Tools returns all registered SDK tools sorted by name.
func (r *Registry) Tools() []agentsdk.Tool {
	names := r.Names()
	out := make([]agentsdk.Tool, 0, len(names))
	for _, name := range names {
		if tool := r.Get(name); tool != nil {
			out = append(out, tool)
		}
	}
	return out
}

// ToolSummaries returns a formatted string of tool names and descriptions,
// excluding basic tools (bash, read, write, edit) whose behavior is self-evident.
// This is included in the system prompt so the agent knows what tools are available
// and what each one does — critical when conversation resets across mode transitions.
func (r *Registry) ToolSummaries() string {
	skipTools := map[string]bool{
		"bash": true, "read": true, "write": true, "edit": true,
		"list_directory": true,
	}
	type entry struct {
		name string
		desc string
	}
	var entries []entry
	for name, tool := range r.tools {
		if skipTools[name] {
			continue
		}
		desc := tool.Description()
		if desc == "" {
			continue
		}
		// Truncate long descriptions.
		if len(desc) > 200 {
			desc = desc[:197] + "..."
		}
		entries = append(entries, entry{name, desc})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	var lines []string
	for _, e := range entries {
		lines = append(lines, "- **"+e.name+"**: "+e.desc)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// PermissionMode returns the resolved registry permission mode.
func (r *Registry) PermissionMode() policy.PermissionMode {
	if r == nil {
		return policy.PermissionModeWorkspaceWrite
	}
	return policy.NormalizePermissionMode(string(r.permissionMode))
}
