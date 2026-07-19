package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcppolicy"
	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeMCPManager struct {
	descriptors []sdkmcp.ToolDescriptor
	servers     []string
	callResult  *mcpsdk.CallToolResult
	callCount   int
}

func (f *fakeMCPManager) ToolDescriptors() []sdkmcp.ToolDescriptor {
	return append([]sdkmcp.ToolDescriptor(nil), f.descriptors...)
}

func (f *fakeMCPManager) ConnectedServerNames() []string {
	return append([]string(nil), f.servers...)
}

func (f *fakeMCPManager) HasResources() bool { return false }

func (f *fakeMCPManager) CallTool(ctx context.Context, qualifiedName string, args map[string]any) (*mcpsdk.CallToolResult, error) {
	f.callCount++
	return f.callResult, nil
}

func (f *fakeMCPManager) ListResources(ctx context.Context, serverName string) ([]sdkmcp.ResourceDescriptor, error) {
	return nil, nil
}

func (f *fakeMCPManager) ReadResource(ctx context.Context, serverName, uri string) (*mcpsdk.ReadResourceResult, error) {
	return nil, nil
}

type recordingMCPSessionClient struct {
	inputType platformv1alpha1.UserInputRequestType
	message   string
	actions   json.RawMessage
	activity  []string
}

func (r *recordingMCPSessionClient) SetUserInputRequest(ctx context.Context, inputType platformv1alpha1.UserInputRequestType, message string, actions json.RawMessage) error {
	r.inputType = inputType
	r.message = message
	r.actions = append(json.RawMessage(nil), actions...)
	return nil
}

func (r *recordingMCPSessionClient) WriteActivity(ctx context.Context, eventType, summary string, detail json.RawMessage) error {
	r.activity = append(r.activity, eventType)
	return nil
}

func TestRequestMCPBreakGlassCreatesPendingApproval(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-mcp-request", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			MCPPolicyRef: &platformv1alpha1.NamedRef{Name: "policy"},
		},
	}
	policy := &platformv1alpha1.MCPPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
		Spec: platformv1alpha1.MCPPolicySpec{
			DefaultAction: platformv1alpha1.MCPDefaultActionDeny,
			BreakGlass: &platformv1alpha1.MCPBreakGlass{
				Enabled:            true,
				RequireAuditReason: true,
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, policy).
		Build()

	session := &recordingMCPSessionClient{}
	manager := &fakeMCPManager{
		descriptors: []sdkmcp.ToolDescriptor{{
			QualifiedName: "mcp__github__create_issue",
			ServerName:    "github",
			ToolName:      "create_issue",
		}},
		servers: []string{"github"},
	}
	tool := &sdkmcp.RequestBreakGlassTool{Sink: &requestMCPBreakGlassSink{
		manager: manager,
		policyRuntime: &mcpPolicyRuntime{
			crdClient: k8sClient,
			namespace: "default",
			agentRun:  "run-mcp-request",
		},
		sessionClient: session,
	}}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{
		"server":"github",
		"tool":"create_issue",
		"reason":"Need to file the tracked issue"
	}`), t.TempDir())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !res.ShouldPause {
		t.Fatal("ShouldPause = false, want true")
	}
	if session.inputType != platformv1alpha1.UserInputApproval {
		t.Fatalf("inputType = %q, want approval", session.inputType)
	}
	if !strings.Contains(session.message, `server "github" tool "create_issue"`) {
		t.Fatalf("message = %q, want target detail", session.message)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "run-mcp-request", Namespace: "default"}, updated); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	request, err := mcppolicy.PendingRequest(updated)
	if err != nil {
		t.Fatalf("PendingRequest() error = %v", err)
	}
	if request == nil {
		t.Fatal("PendingRequest() = nil, want request")
	}
	if request.ID == "" || request.Server != "github" || request.Tool != "create_issue" {
		t.Fatalf("PendingRequest() = %#v, want identified github/create_issue request", request)
	}
}

func TestDynamicMCPToolBlockedByPolicySuggestsBreakGlass(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-mcp-blocked", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			MCPPolicyRef: &platformv1alpha1.NamedRef{Name: "policy"},
		},
	}
	policy := &platformv1alpha1.MCPPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
		Spec: platformv1alpha1.MCPPolicySpec{
			DefaultAction: platformv1alpha1.MCPDefaultActionDeny,
			BreakGlass:    &platformv1alpha1.MCPBreakGlass{Enabled: true},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, policy).
		Build()

	desc := sdkmcp.ToolDescriptor{
		QualifiedName: "mcp__github__create_issue",
		ServerName:    "github",
		ToolName:      "create_issue",
	}
	manager := &fakeMCPManager{descriptors: []sdkmcp.ToolDescriptor{desc}}
	policyManager := &policyMCPManager{
		inner: manager,
		policyRuntime: &mcpPolicyRuntime{
			crdClient: k8sClient,
			namespace: "default",
			agentRun:  "run-mcp-blocked",
		},
		mode:          "workspace-write",
		evaluator:     mcppolicy.NewEvaluator(run, policy),
		exposeBlocked: true,
	}
	tool := &sdkmcp.DynamicTool{Manager: policyManager, Descriptor: desc}

	res, err := tool.Execute(context.Background(), nil, t.TempDir())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !res.IsError {
		t.Fatal("IsError = false, want true")
	}
	if !strings.Contains(res.Content, "RequestMCPBreakGlass") {
		t.Fatalf("Content = %q, want break-glass hint", res.Content)
	}
	if manager.callCount != 0 {
		t.Fatalf("CallTool count = %d, want 0", manager.callCount)
	}
}

func TestDynamicMCPToolAllowsGrantedBreakGlass(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "run-mcp-granted",
			Namespace:   "default",
			Annotations: map[string]string{},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			MCPPolicyRef: &platformv1alpha1.NamedRef{Name: "policy"},
		},
	}
	if err := mcppolicy.SetGrantedGrants(run.Annotations, []mcppolicy.BreakGlassGrant{{
		Server: "github",
		Tool:   "create_issue",
		Reason: "Need to file the issue",
	}}); err != nil {
		t.Fatalf("SetGrantedGrants() error = %v", err)
	}
	policy := &platformv1alpha1.MCPPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
		Spec: platformv1alpha1.MCPPolicySpec{
			DefaultAction: platformv1alpha1.MCPDefaultActionDeny,
			BreakGlass:    &platformv1alpha1.MCPBreakGlass{Enabled: true},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, policy).
		Build()

	desc := sdkmcp.ToolDescriptor{
		QualifiedName: "mcp__github__create_issue",
		ServerName:    "github",
		ToolName:      "create_issue",
	}
	manager := &fakeMCPManager{
		descriptors: []sdkmcp.ToolDescriptor{desc},
		callResult: &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: "issue created"},
			},
		},
	}
	policyManager := &policyMCPManager{
		inner: manager,
		policyRuntime: &mcpPolicyRuntime{
			crdClient: k8sClient,
			namespace: "default",
			agentRun:  "run-mcp-granted",
		},
		mode:          "workspace-write",
		evaluator:     mcppolicy.NewEvaluator(run, policy),
		exposeBlocked: true,
	}
	tool := &sdkmcp.DynamicTool{Manager: policyManager, Descriptor: desc}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"title":"issue"}`), t.TempDir())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false (content=%q)", res.Content)
	}
	if res.Content != "issue created" {
		t.Fatalf("Content = %q, want issue created", res.Content)
	}
	if manager.callCount != 1 {
		t.Fatalf("CallTool count = %d, want 1", manager.callCount)
	}
}
