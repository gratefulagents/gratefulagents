package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	"github.com/gratefulagents/sdk/pkg/agentsdk/policy"
	sdkvision "github.com/gratefulagents/sdk/pkg/agentsdk/tools/vision"
	sdkweb "github.com/gratefulagents/sdk/pkg/agentsdk/tools/web"
)

func TestNewRegistry_DefaultTools(t *testing.T) {
	r := NewRegistry("/tmp/test")
	expectedTools := []string{"Bash", "Edit", "LSP", "WebFetch", "Write", "glob", "grep", "list_files", "read_file", "think"}
	names := r.Names()

	if len(names) != len(expectedTools) {
		t.Fatalf("len(Names()) = %d, want %d; got %v", len(names), len(expectedTools), names)
	}
	for i, name := range names {
		if name != expectedTools[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, name, expectedTools[i])
		}
	}
}

func TestNewRegistry_WithBrowserToolsRegistersBrowser(t *testing.T) {
	r := NewRegistry("/tmp/test", WithBrowserTools())
	if tool := r.Get("Browser"); tool == nil {
		t.Fatalf("browser-enabled registry missing Browser; names=%v", r.Names())
	}
	fetch, ok := r.Get("WebFetch").(*sdkweb.FetchTool)
	if !ok {
		t.Fatalf("WebFetch has unexpected type %T", r.Get("WebFetch"))
	}
	if fetch.AllowPrivateNetworkURLs {
		t.Fatal("enabling Browser must not enable private-network URLs for WebFetch")
	}
}

func TestNewRegistry_BrowserDoesNotRelaxVisionURLs(t *testing.T) {
	r := NewRegistry("/tmp/test", WithBrowserTools(), WithVisionTools(func(context.Context, []byte, string, string) (string, error) {
		return "", nil
	}))
	vision, ok := r.Get("AnalyzeImage").(*sdkvision.Tool)
	if !ok {
		t.Fatalf("AnalyzeImage has unexpected type %T", r.Get("AnalyzeImage"))
	}
	if vision.AllowPrivateNetworkURLs {
		t.Fatal("enabling Browser must not enable private-network URLs for vision")
	}
}

func TestNewRegistry_WithProviderWiredVisionTool(t *testing.T) {
	r := NewRegistry("/tmp/test", WithVisionTools(nil))
	vision, ok := r.Get("AnalyzeImage").(*sdkvision.Tool)
	if !ok {
		t.Fatalf("provider-wired registry missing AnalyzeImage; names=%v", r.Names())
	}
	if vision.AnalyzeFn != nil || vision.AnalyzeWithDetailFn != nil {
		t.Fatal("provider-wired vision tool should leave analyzer attachment to the SDK runtime")
	}
}

func TestNewRegistry_ReadOnlyWithSignalTools(t *testing.T) {
	r := NewRegistry("/tmp/test", WithReadOnlyTools(), WithSignalTools())

	// Read-only + signals should include only read tools and signal tools.
	expectedTools := []string{"AskUserQuestion", "Bash", "LSP", "WebFetch", "glob", "grep", "list_files", "present_plan", "read_file", "think"}
	names := r.Names()

	if len(names) != len(expectedTools) {
		t.Fatalf("len(Names()) = %d, want %d; got %v", len(names), len(expectedTools), names)
	}
	for i, name := range names {
		if name != expectedTools[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, name, expectedTools[i])
		}
	}
	for _, name := range names {
		tool := r.Get(name)
		if tool == nil {
			t.Fatalf("Get(%q) = nil", name)
		}
		if !tool.IsReadOnly() {
			t.Errorf("tool %q should be read-only", name)
		}
	}
}

func TestNewRegistry_ReadOnly(t *testing.T) {
	r := NewRegistry("/tmp/test", WithReadOnlyTools())
	names := r.Names()

	// Read-only should exclude Bash, Edit, Write.
	for _, name := range names {
		tool := r.Get(name)
		if tool == nil {
			t.Errorf("Get(%q) = nil", name)
			continue
		}
		if !tool.IsReadOnly() {
			t.Errorf("read-only registry contains non-read-only tool %q", name)
		}
	}

	// Should include Bash (ReadOnlyBashTool) and LSP.
	readOnlyTools := []string{"Bash", "LSP"}
	for _, name := range readOnlyTools {
		if r.Get(name) == nil {
			t.Errorf("read-only registry missing tool %q", name)
		}
	}

	// Should NOT include write tools or signal tools.
	excludedTools := []string{"Edit", "Write", "AskUserQuestion"}
	for _, name := range excludedTools {
		if r.Get(name) != nil {
			t.Errorf("read-only registry should not contain %q", name)
		}
	}
}

func TestNewRegistry_DangerFullAccess(t *testing.T) {
	r := NewRegistry("/tmp/test", WithPermissionMode(policy.PermissionModeDangerFullAccess))

	if got := r.PermissionMode(); got != policy.PermissionModeDangerFullAccess {
		t.Fatalf("PermissionMode() = %q, want %q", got, policy.PermissionModeDangerFullAccess)
	}
	for _, name := range []string{"Bash", "Edit", "Write"} {
		if r.Get(name) == nil {
			t.Fatalf("danger-full-access registry missing %q", name)
		}
	}
}

func TestNewRegistry_WorkspaceWriteUsesRestrictedBash(t *testing.T) {
	r := NewRegistry("/tmp/test", WithPermissionMode(policy.PermissionModeWorkspaceWrite))
	if got := r.PermissionMode(); got != policy.PermissionModeWorkspaceWrite {
		t.Fatalf("PermissionMode() = %q, want %q", got, policy.PermissionModeWorkspaceWrite)
	}
	bash := r.Get("Bash")
	if bash == nil {
		t.Fatal("workspace-write registry missing Bash")
	}
	if bash.IsReadOnly() {
		t.Fatalf("expected workspace-write Bash to allow writes, got read-only %T", bash)
	}
}

type nonReadOnlyTestTool struct{}

func (t *nonReadOnlyTestTool) Name() string        { return "NonReadOnlyTestTool" }
func (t *nonReadOnlyTestTool) Description() string { return "test tool" }
func (t *nonReadOnlyTestTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (t *nonReadOnlyTestTool) Execute(_ context.Context, _ json.RawMessage, _ string) (Result, error) {
	return Result{}, nil
}
func (t *nonReadOnlyTestTool) IsReadOnly() bool { return false }
func (t *nonReadOnlyTestTool) IsEnabled(ctx *agentsdk.RunContext) bool {
	return ctx == nil || ctx.ToolAccessLevel != agentsdk.ToolAccessLevelReadOnly
}
func (t *nonReadOnlyTestTool) NeedsApproval() bool { return false }
func (t *nonReadOnlyTestTool) TimeoutSeconds() int { return 0 }

func TestRegistry_Register(t *testing.T) {
	r := NewRegistry("/tmp/test")
	before := len(r.Names())

	// Re-register an existing tool name; count should not change.
	r.Register(&duplicateNameTool{name: "Bash", readOnly: false})
	after := len(r.Names())

	if after != before {
		t.Errorf("re-registering existing tool changed count: %d -> %d", before, after)
	}
}

type duplicateNameTool struct {
	name     string
	readOnly bool
}

func (t *duplicateNameTool) Name() string        { return t.name }
func (t *duplicateNameTool) Description() string { return "duplicate test tool" }
func (t *duplicateNameTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (t *duplicateNameTool) Execute(_ context.Context, _ json.RawMessage, _ string) (Result, error) {
	return Result{}, nil
}
func (t *duplicateNameTool) IsReadOnly() bool { return t.readOnly }
func (t *duplicateNameTool) IsEnabled(ctx *agentsdk.RunContext) bool {
	return t.readOnly || ctx == nil || ctx.ToolAccessLevel != agentsdk.ToolAccessLevelReadOnly
}
func (t *duplicateNameTool) NeedsApproval() bool { return false }
func (t *duplicateNameTool) TimeoutSeconds() int { return 0 }

func TestRegistry_RegisterSkipsNonReadOnlyToolInReadOnlyMode(t *testing.T) {
	r := NewRegistry("/tmp/test", WithReadOnlyTools())
	r.Register(&nonReadOnlyTestTool{})
	if r.Get("NonReadOnlyTestTool") != nil {
		t.Fatal("expected read-only registry to reject non-read-only dynamic tool")
	}
}

// controlFlowTestTool simulates a control-flow tool like finish or save_plan.
type controlFlowTestTool struct{ name string }

func (t *controlFlowTestTool) Name() string        { return t.name }
func (t *controlFlowTestTool) Description() string { return "control-flow test tool" }
func (t *controlFlowTestTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (t *controlFlowTestTool) Execute(_ context.Context, _ json.RawMessage, _ string) (Result, error) {
	return Result{}, nil
}
func (t *controlFlowTestTool) IsReadOnly() bool                      { return false }
func (t *controlFlowTestTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *controlFlowTestTool) NeedsApproval() bool                   { return false }
func (t *controlFlowTestTool) TimeoutSeconds() int                   { return 0 }

func TestRegistry_RegisterKeepsControlFlowToolsInReadOnlyMode(t *testing.T) {
	r := NewRegistry("/tmp/test", WithReadOnlyTools())

	for _, name := range []string{"finish", "save_plan", "get_plan"} {
		r.Register(&controlFlowTestTool{name: name})
		if r.Get(name) == nil {
			t.Errorf("expected read-only registry to keep control-flow tool %q", name)
		}
	}

	// Non-control-flow mutating tool should still be rejected.
	r.Register(&nonReadOnlyTestTool{})
	if r.Get("NonReadOnlyTestTool") != nil {
		t.Error("expected read-only registry to reject non-control-flow mutating tool")
	}
}

func TestRegistry_RegisterAllowsExplicitMutatingToolException(t *testing.T) {
	r := NewRegistry("/tmp/test", WithReadOnlyTools(), WithAllowedMutatingTools("NonReadOnlyTestTool"))
	r.Register(&nonReadOnlyTestTool{})
	if r.Get("NonReadOnlyTestTool") == nil {
		t.Fatal("expected read-only registry to keep explicitly allowlisted mutating tool")
	}
}

func TestRegistry_ReviewerToolsSurviveReadOnlyClamp(t *testing.T) {
	r := NewRegistry("/tmp/test", WithReadOnlyTools(), WithAllowedMutatingTools(ReviewerMutatingToolNames()...))
	RegisterPRReviewTools(r, "/tmp/test")
	readOnlyCtx := &agentsdk.RunContext{ToolAccessLevel: agentsdk.ToolAccessLevelReadOnly}
	for _, name := range []string{"submit_pull_request_review", "reply_to_review_thread", "resolve_review_thread", "request_re_review"} {
		tool := r.Get(name)
		if tool == nil {
			t.Errorf("expected reviewer tool %q to survive the read-only clamp", name)
			continue
		}
		if !tool.IsEnabled(readOnlyCtx) {
			t.Errorf("expected reviewer tool %q to remain enabled in a read-only turn", name)
		}
	}
	if r.Get("update_pull_request") != nil {
		t.Error("expected non-allowlisted PR mutation update_pull_request to remain filtered")
	}
	// Plain mutating tools must still be filtered.
	r.Register(&nonReadOnlyTestTool{})
	if r.Get("NonReadOnlyTestTool") != nil {
		t.Error("expected non-reviewer mutating tool to remain filtered under the clamp")
	}
}

func TestRegistry_Get(t *testing.T) {
	r := NewRegistry("/tmp/test")

	if tool := r.Get("Bash"); tool == nil {
		t.Error("Get(Bash) = nil")
	} else if tool.Name() != "Bash" {
		t.Errorf("Get(Bash).Name() = %q", tool.Name())
	}

	if tool := r.Get("NonExistent"); tool != nil {
		t.Errorf("Get(NonExistent) = %v, want nil", tool)
	}
}

func TestRegistry_GetToolDefinitions(t *testing.T) {
	r := NewRegistry("/tmp/test")
	defs := r.GetToolDefinitions()

	if len(defs) == 0 {
		t.Fatal("GetToolDefinitions() returned empty")
	}

	// Verify sorted by name.
	for i := 1; i < len(defs); i++ {
		if defs[i].Name < defs[i-1].Name {
			t.Errorf("defs not sorted: %q comes after %q", defs[i].Name, defs[i-1].Name)
		}
	}

	// Verify each def has required fields.
	for _, def := range defs {
		if def.Name == "" {
			t.Error("tool def has empty name")
		}
		if def.Description == "" {
			t.Errorf("tool %q has empty description", def.Name)
		}
		if len(def.InputSchema) == 0 {
			t.Errorf("tool %q has empty input schema", def.Name)
		}
	}
}
