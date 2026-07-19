package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcppolicy"
	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
	"github.com/gratefulagents/sdk/pkg/agentsdk/policy"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type mcpManager interface {
	ToolDescriptors() []sdkmcp.ToolDescriptor
	ConnectedServerNames() []string
	HasResources() bool
	CallTool(ctx context.Context, qualifiedName string, args map[string]any) (*mcpsdk.CallToolResult, error)
	ListResources(ctx context.Context, serverName string) ([]sdkmcp.ResourceDescriptor, error)
	ReadResource(ctx context.Context, serverName, uri string) (*mcpsdk.ReadResourceResult, error)
}

type mcpSessionClient interface {
	SetUserInputRequest(ctx context.Context, inputType platformv1alpha1.UserInputRequestType, message string, actions json.RawMessage) error
	WriteActivity(ctx context.Context, eventType, summary string, detail json.RawMessage) error
}

type mcpPolicyRuntime struct {
	crdClient client.Client
	namespace string
	agentRun  string
}

// RegisterMCPTools registers all available MCP tools into the registry.
func RegisterMCPTools(
	ctx context.Context,
	registry *Registry,
	manager mcpManager,
	permissionMode policy.PermissionMode,
	crdClient client.Client,
	namespace string,
	agentRunName string,
	sc mcpSessionClient,
) {
	if registry == nil || manager == nil {
		return
	}

	mode := policy.NormalizePermissionMode(string(permissionMode))
	policyRuntime := &mcpPolicyRuntime{
		crdClient: crdClient,
		namespace: namespace,
		agentRun:  agentRunName,
	}
	_, evaluator, err := policyRuntime.Resolve(ctx)
	if err != nil {
		log.Printf("WARN: failed to resolve MCPPolicy for %s/%s during tool registration: %v", namespace, agentRunName, err)
		evaluator = mcppolicy.NewEvaluator(nil, nil)
	}
	exposeBlockedTools := evaluator.BreakGlassEnabled()
	policyManager := &policyMCPManager{
		inner:          manager,
		policyRuntime:  policyRuntime,
		mode:           mode,
		evaluator:      evaluator,
		exposeBlocked:  exposeBlockedTools,
		resourceAccess: exposeBlockedTools || shouldRegisterMCPResources(manager, evaluator),
	}
	for _, tool := range sdkmcp.BuildTools(policyManager) {
		registry.Register(tool)
	}
	if evaluator.BreakGlassEnabled() && sc != nil && crdClient != nil && strings.TrimSpace(agentRunName) != "" {
		registry.Register(&sdkmcp.RequestBreakGlassTool{Sink: &requestMCPBreakGlassSink{
			manager:       manager,
			policyRuntime: policyRuntime,
			sessionClient: sc,
		}})
	}
}

type policyMCPManager struct {
	inner          mcpManager
	policyRuntime  *mcpPolicyRuntime
	mode           policy.PermissionMode
	evaluator      mcppolicy.Evaluator
	exposeBlocked  bool
	resourceAccess bool
}

func (m *policyMCPManager) ToolDescriptors() []sdkmcp.ToolDescriptor {
	if m == nil || m.inner == nil {
		return nil
	}
	descriptors := m.inner.ToolDescriptors()
	out := make([]sdkmcp.ToolDescriptor, 0, len(descriptors))
	for _, desc := range descriptors {
		if !m.mode.AllowsMCPTool(desc.ReadOnly) {
			continue
		}
		if !m.exposeBlocked && !m.evaluator.AllowsTool(desc.ServerName, desc.ToolName) {
			continue
		}
		out = append(out, desc)
	}
	return out
}

func (m *policyMCPManager) ConnectedServerNames() []string {
	if m == nil || m.inner == nil {
		return nil
	}
	return m.inner.ConnectedServerNames()
}

func (m *policyMCPManager) HasResources() bool {
	return m != nil && m.inner != nil && m.resourceAccess && m.inner.HasResources()
}

func (m *policyMCPManager) CallTool(ctx context.Context, qualifiedName string, args map[string]any) (*mcpsdk.CallToolResult, error) {
	if m == nil || m.inner == nil {
		return nil, fmt.Errorf("MCP manager is not initialized")
	}
	desc, ok := m.findDescriptor(qualifiedName)
	_, evaluator, err := m.policyRuntime.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve MCP policy: %w", err)
	}
	if ok && !evaluator.AllowsTool(desc.ServerName, desc.ToolName) {
		return nil, fmt.Errorf("%s", sdkmcp.BlockedToolMessage(desc.ServerName, desc.ToolName, evaluator.BreakGlassEnabled()))
	}
	return m.inner.CallTool(ctx, qualifiedName, args)
}

func (m *policyMCPManager) ListResources(ctx context.Context, serverName string) ([]sdkmcp.ResourceDescriptor, error) {
	if m == nil || m.inner == nil {
		return nil, fmt.Errorf("MCP manager is not initialized")
	}
	_, evaluator, err := m.policyRuntime.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve MCP policy: %w", err)
	}
	resources, err := m.inner.ListResources(ctx, serverName)
	if err != nil {
		return nil, err
	}
	filtered := resources[:0]
	for _, resource := range resources {
		if evaluator.AllowsServer(resource.Server) {
			filtered = append(filtered, resource)
		}
	}
	return filtered, nil
}

func (m *policyMCPManager) ReadResource(ctx context.Context, serverName, uri string) (*mcpsdk.ReadResourceResult, error) {
	if m == nil || m.inner == nil {
		return nil, fmt.Errorf("MCP manager is not initialized")
	}
	_, evaluator, err := m.policyRuntime.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve MCP policy: %w", err)
	}
	if !evaluator.AllowsServer(serverName) {
		return nil, fmt.Errorf("%s", sdkmcp.BlockedServerMessage(serverName, evaluator.BreakGlassEnabled()))
	}
	return m.inner.ReadResource(ctx, serverName, uri)
}

func (m *policyMCPManager) findDescriptor(qualifiedName string) (sdkmcp.ToolDescriptor, bool) {
	for _, desc := range m.inner.ToolDescriptors() {
		if desc.QualifiedName == qualifiedName {
			return desc, true
		}
	}
	return sdkmcp.ToolDescriptor{}, false
}

type requestMCPBreakGlassSink struct {
	manager       mcpManager
	policyRuntime *mcpPolicyRuntime
	sessionClient mcpSessionClient
}

func (t *requestMCPBreakGlassSink) RequestMCPBreakGlass(ctx context.Context, request sdkmcp.BreakGlassRequest) (sdkmcp.BreakGlassRequestResult, error) {
	server := strings.TrimSpace(request.Server)
	tool := strings.TrimSpace(request.Tool)
	reason := strings.TrimSpace(request.Reason)
	run, evaluator, err := t.policyRuntime.Resolve(ctx)
	if err != nil {
		return sdkmcp.BreakGlassRequestResult{Content: fmt.Sprintf("Failed to resolve MCP policy: %v", err), IsError: true}, nil
	}
	breakGlass := evaluator.BreakGlass()
	if !breakGlass.Enabled {
		return sdkmcp.BreakGlassRequestResult{Content: "MCP break-glass is disabled by policy", IsError: true}, nil
	}
	if breakGlass.RequireAuditReason && reason == "" {
		return sdkmcp.BreakGlassRequestResult{Content: "reason is required by MCP policy for break-glass requests", IsError: true}, nil
	}
	if tool != "" && evaluator.AllowsTool(server, tool) {
		return sdkmcp.BreakGlassRequestResult{Content: fmt.Sprintf("MCP tool %q on server %q is already allowed", tool, server)}, nil
	}
	if tool == "" && evaluator.AllowsServer(server) {
		return sdkmcp.BreakGlassRequestResult{Content: fmt.Sprintf("MCP server %q is already allowed", server)}, nil
	}

	catalog := sdkmcp.NewBreakGlassCatalog(t.manager.ToolDescriptors(), t.manager.ConnectedServerNames())
	if err := catalog.Validate(server, tool); err != nil {
		return sdkmcp.BreakGlassRequestResult{Content: err.Error(), IsError: true}, nil
	}
	pending, err := mcppolicy.PendingRequest(run)
	if err != nil {
		return sdkmcp.BreakGlassRequestResult{Content: fmt.Sprintf("Failed to read pending MCP break-glass request: %v", err), IsError: true}, nil
	}
	if pending != nil {
		return sdkmcp.BreakGlassRequestResult{Content: "An MCP break-glass request is already waiting for approval", IsError: true}, nil
	}

	pendingRequest := mcppolicy.BreakGlassRequest{
		ID:          uuid.NewString(),
		Server:      server,
		Tool:        tool,
		Reason:      reason,
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := t.patchPendingRequest(ctx, pendingRequest); err != nil {
		return sdkmcp.BreakGlassRequestResult{Content: fmt.Sprintf("Failed to persist MCP break-glass request: %v", err), IsError: true}, nil
	}

	actions := sdkmcp.BreakGlassActions()
	question := sdkmcp.BuildBreakGlassQuestion(sdkmcp.BreakGlassRequest{
		Server: server,
		Tool:   tool,
		Reason: reason,
	}, sdkmcp.BreakGlassPolicy{RequireAuditReason: breakGlass.RequireAuditReason})
	if err := t.sessionClient.SetUserInputRequest(ctx, platformv1alpha1.UserInputApproval, question, actions); err != nil {
		_ = t.clearPendingRequest(ctx)
		return sdkmcp.BreakGlassRequestResult{Content: fmt.Sprintf("Failed to pause for MCP break-glass approval: %v", err), IsError: true}, nil
	}

	detail, _ := json.Marshal(map[string]string{
		"server": server,
		"tool":   tool,
		"reason": reason,
	})
	_ = t.sessionClient.WriteActivity(ctx, "mcp_break_glass_requested", sdkmcp.SummarizeBreakGlassTarget("Requested", server, tool), detail)

	return sdkmcp.BreakGlassRequestResult{
		Content:     fmt.Sprintf("Requested MCP break-glass for %s. Waiting for approval.", sdkmcp.FormatBreakGlassTarget(server, tool)),
		ShouldPause: true,
	}, nil
}

func (t *requestMCPBreakGlassSink) patchPendingRequest(ctx context.Context, request mcppolicy.BreakGlassRequest) error {
	key := types.NamespacedName{Name: t.policyRuntime.agentRun, Namespace: t.policyRuntime.namespace}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := &platformv1alpha1.AgentRun{}
		if err := t.policyRuntime.crdClient.Get(ctx, key, run); err != nil {
			return err
		}
		patch := client.MergeFrom(run.DeepCopy())
		if run.Annotations == nil {
			run.Annotations = map[string]string{}
		}
		if err := mcppolicy.SetPendingRequest(run.Annotations, request); err != nil {
			return err
		}
		return t.policyRuntime.crdClient.Patch(ctx, run, patch)
	})
}

func (t *requestMCPBreakGlassSink) clearPendingRequest(ctx context.Context) error {
	key := types.NamespacedName{Name: t.policyRuntime.agentRun, Namespace: t.policyRuntime.namespace}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := &platformv1alpha1.AgentRun{}
		if err := t.policyRuntime.crdClient.Get(ctx, key, run); err != nil {
			return err
		}
		patch := client.MergeFrom(run.DeepCopy())
		if run.Annotations == nil {
			return nil
		}
		mcppolicy.ClearPendingRequest(run.Annotations)
		return t.policyRuntime.crdClient.Patch(ctx, run, patch)
	})
}

func (r *mcpPolicyRuntime) Resolve(ctx context.Context) (*platformv1alpha1.AgentRun, mcppolicy.Evaluator, error) {
	if r == nil || r.crdClient == nil || strings.TrimSpace(r.agentRun) == "" || strings.TrimSpace(r.namespace) == "" {
		return nil, mcppolicy.NewEvaluator(nil, nil), nil
	}

	run := &platformv1alpha1.AgentRun{}
	key := types.NamespacedName{Name: r.agentRun, Namespace: r.namespace}
	if err := r.crdClient.Get(ctx, key, run); err != nil {
		return nil, mcppolicy.NewEvaluator(nil, nil), err
	}

	var policyObj *platformv1alpha1.MCPPolicy
	if run.Spec.MCPPolicyRef != nil && strings.TrimSpace(run.Spec.MCPPolicyRef.Name) != "" {
		policy := &platformv1alpha1.MCPPolicy{}
		policyKey := types.NamespacedName{Name: run.Spec.MCPPolicyRef.Name, Namespace: r.namespace}
		if err := r.crdClient.Get(ctx, policyKey, policy); err == nil {
			policyObj = policy
		} else if !apierrors.IsNotFound(err) {
			return run, mcppolicy.Evaluator{}, err
		}
	}

	return run, mcppolicy.NewEvaluator(run, policyObj), nil
}

func shouldRegisterMCPResources(manager mcpManager, evaluator mcppolicy.Evaluator) bool {
	for _, server := range manager.ConnectedServerNames() {
		if evaluator.AllowsServer(server) {
			return true
		}
	}
	return false
}
