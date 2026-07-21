package dashboard

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func resourceActorContext(subject, role, name string) context.Context {
	return context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: subject, Role: role, Name: name})
}

func TestRuntimeProfileCRUDUsesPersonalNamespace(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ctx := resourceActorContext("alice-id", "member", "Alice Smith")

	created, err := srv.CreateRuntimeProfile(ctx, &platform.CreateRuntimeProfileRequest{Profile: &platform.RuntimeProfile{
		Name: "default", PermissionMode: "workspace-write", GitRemoteWrites: ptr.To("disabled"), EgressMode: "restricted", DefaultTimeout: "5m",
		SandboxTemplateRef: "browser-template", RuntimeClassName: "gvisor", WarmPoolRef: "browser-pool",
		PersistWorkspace: true, WorkspaceSize: "10Gi", EnablePrivateProcfs: true,
		CommandPath: []string{"/usr/bin"}, CommandPathPrepend: []string{"/opt/bin"}, CommandPathAppend: []string{"/workspace/repo/node_modules/.bin"},
		ExtraReadOnlyPaths: []string{"/opt/toolchain"}, ExtraWritablePaths: []string{" /cache/go/ ", "/cache/npm", "/cache/go"}, CommandEnv: map[string]string{"LANG": "C.UTF-8"},
		ResourceRequests: map[string]string{"cpu": "500m"}, ResourceLimits: map[string]string{"memory": "2Gi"},
		MaxConcurrentRuns: 4, PerNamespaceMaxConcurrentRuns: 2, StaleRunTimeout: "30m",
	}})
	if err != nil {
		t.Fatalf("CreateRuntimeProfile() error = %v", err)
	}
	wantNS := deriveUserNamespaceName("Alice Smith", "alice-id")
	if created.Namespace != wantNS {
		t.Fatalf("namespace = %q, want %q", created.Namespace, wantNS)
	}
	var saved platformv1alpha1.RuntimeProfile
	if err := c.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: wantNS}, &saved); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if saved.Spec.Sandbox == nil || !saved.Spec.Sandbox.EnablePrivateProcfs || saved.Spec.Sandbox.SandboxTemplateRef == nil || saved.Spec.Sandbox.SandboxTemplateRef.Name != "browser-template" || saved.Spec.Sandbox.RuntimeClassName != "gvisor" || saved.Spec.Sandbox.WarmPoolRef == nil || saved.Spec.Sandbox.WarmPoolRef.Name != "browser-pool" || saved.Spec.Sandbox.CommandSandbox == nil || len(saved.Spec.Sandbox.CommandSandbox.ExtraWritablePaths) != 2 || saved.Spec.Sandbox.CommandSandbox.ExtraWritablePaths[0] != "/cache/go" || saved.Spec.Sandbox.CommandSandbox.Env["LANG"] != "C.UTF-8" {
		t.Fatalf("saved sandbox = %#v, want all dashboard sandbox values", saved.Spec.Sandbox)
	}
	if saved.Spec.Security == nil || saved.Spec.Security.GitRemoteWrites != platformv1alpha1.GitRemoteWritesDisabled || created.GetGitRemoteWrites() != "disabled" {
		t.Fatalf("Git remote writes did not round-trip: saved=%#v created=%q", saved.Spec.Security, created.GetGitRemoteWrites())
	}
	if saved.Spec.Resources == nil || saved.Spec.Resources.Requests.Cpu().String() != "500m" || saved.Spec.Resources.Limits.Memory().String() != "2Gi" {
		t.Fatalf("saved resources = %#v, want requests and limits", saved.Spec.Resources)
	}
	if saved.Spec.Admission == nil || saved.Spec.Admission.MaxConcurrentRuns != 4 || saved.Spec.Admission.PerNamespaceMaxConcurrentRuns != 2 || saved.Spec.Admission.StaleRunTimeout.Duration != 30*time.Minute {
		t.Fatalf("saved admission = %#v, want all concurrency values", saved.Spec.Admission)
	}
	if !created.EnablePrivateProcfs || created.SandboxTemplateRef != "browser-template" || created.CommandEnv["LANG"] != "C.UTF-8" || created.ResourceRequests["cpu"] != "500m" || created.MaxConcurrentRuns != 4 {
		t.Fatalf("created profile did not round-trip all fields: %#v", created)
	}
	if len(created.ExtraWritablePaths) != 2 || created.ExtraWritablePaths[1] != "/cache/npm" {
		t.Fatalf("created writable paths = %#v", created.ExtraWritablePaths)
	}
	other, err := srv.ListRuntimeProfiles(resourceActorContext("bob-id", "member", "Bob Jones"), &platform.ListRuntimeProfilesRequest{})
	if err != nil {
		t.Fatalf("ListRuntimeProfiles() error = %v", err)
	}
	if len(other.Profiles) != 0 {
		t.Fatalf("other user saw %d profiles", len(other.Profiles))
	}
	_, err = srv.CreateMCPPolicy(ctx, &platform.CreateMCPPolicyRequest{Policy: &platform.MCPPolicy{Namespace: "another-tenant", Name: "deny-all", DefaultAction: "Deny"}})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("caller-selected namespace: want InvalidArgument, got %v", err)
	}
}

func TestUpdatesReplaceCompleteRuntimeAndMCPPolicySpecs(t *testing.T) {
	scheme := testProjectScheme(t)
	ctx := resourceActorContext("alice-id", "member", "Alice Smith")
	ns := deriveUserNamespaceName("Alice Smith", "alice-id")
	runtime := &platformv1alpha1.RuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns}, Spec: platformv1alpha1.RuntimeProfileSpec{
		Security: &platformv1alpha1.RuntimeProfileSecurity{GitRemoteWrites: platformv1alpha1.GitRemoteWritesDisabled},
		Sandbox: &platformv1alpha1.RuntimeProfileSandbox{
			RuntimeClassName:    "gvisor",
			EnablePrivateProcfs: true,
			CommandSandbox:      &platformv1alpha1.RuntimeProfileCommandSandbox{Path: []string{"/usr/bin"}, ExtraWritablePaths: []string{"/cache/old"}},
		},
		Resources: &corev1.ResourceRequirements{Claims: []corev1.ResourceClaim{{Name: "externally-managed"}}},
		Admission: &platformv1alpha1.RuntimeProfileAdmission{MaxConcurrentRuns: 7},
	}}
	mcp := &platformv1alpha1.MCPPolicy{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns}, Spec: platformv1alpha1.MCPPolicySpec{BreakGlass: &platformv1alpha1.MCPBreakGlass{Enabled: true}}}
	mode := &platformv1alpha1.ModeTemplate{ObjectMeta: metav1.ObjectMeta{Name: "direct"}, Spec: platformv1alpha1.ModeTemplateSpec{Name: "direct", Version: "v1", Category: platformv1alpha1.ModeCategoryDirect, ExecutionStrategy: platformv1alpha1.ExecutionStrategySerial, Constraints: &platformv1alpha1.ModeConstraints{MaxTurns: 42}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(userNamespaceObj(ns), runtime, mcp, mode).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	if _, err := srv.UpdateRuntimeProfile(ctx, &platform.UpdateRuntimeProfileRequest{Profile: &platform.RuntimeProfile{Name: "default", PermissionMode: "read-only", EgressMode: "disabled", RuntimeClassName: "kata", CommandPath: []string{"/bin"}, ExtraWritablePaths: []string{"/cache/new"}, MaxConcurrentRuns: 3, ReplaceSpec: true}}); err != nil {
		t.Fatalf("UpdateRuntimeProfile() error = %v", err)
	}
	updatedMCP, err := srv.UpdateMCPPolicy(ctx, &platform.UpdateMCPPolicyRequest{Policy: &platform.MCPPolicy{Name: "default", DefaultAction: "Deny", BreakGlass: &platform.MCPBreakGlass{RequireAuditReason: true, AdminMediated: true}, ReplaceSpec: true}})
	if err != nil {
		t.Fatalf("UpdateMCPPolicy() error = %v", err)
	}
	if updatedMCP.BreakGlass == nil || !updatedMCP.BreakGlass.RequireAuditReason || !updatedMCP.BreakGlass.AdminMediated {
		t.Fatalf("UpdateMCPPolicy() response did not round-trip break glass: %#v", updatedMCP.BreakGlass)
	}
	admin := resourceActorContext("admin-id", "admin", "Admin")
	if _, err := srv.UpdateModeTemplate(admin, &platform.UpdateModeTemplateRequest{Template: &platform.ModeTemplate{Name: "direct", Version: "v2", Category: "direct", ExecutionStrategy: "serial"}}); err != nil {
		t.Fatalf("UpdateModeTemplate() error = %v", err)
	}
	var gotRuntime platformv1alpha1.RuntimeProfile
	_ = c.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: ns}, &gotRuntime)
	if gotRuntime.Spec.Security == nil || gotRuntime.Spec.Security.GitRemoteWrites != platformv1alpha1.GitRemoteWritesDisabled {
		t.Fatalf("presence-absent Git remote policy was not preserved: %#v", gotRuntime.Spec.Security)
	}
	if gotRuntime.Spec.Sandbox.RuntimeClassName != "kata" || gotRuntime.Spec.Sandbox.EnablePrivateProcfs || gotRuntime.Spec.Sandbox.CommandSandbox == nil || gotRuntime.Spec.Admission == nil || gotRuntime.Spec.Admission.MaxConcurrentRuns != 3 {
		t.Fatalf("RuntimeProfile spec was not replaced from dashboard values: %#v", gotRuntime.Spec)
	}
	if len(gotRuntime.Spec.Sandbox.CommandSandbox.Path) != 1 || gotRuntime.Spec.Sandbox.CommandSandbox.Path[0] != "/bin" || len(gotRuntime.Spec.Sandbox.CommandSandbox.ExtraWritablePaths) != 1 || gotRuntime.Spec.Sandbox.CommandSandbox.ExtraWritablePaths[0] != "/cache/new" {
		t.Fatalf("RuntimeProfile command sandbox = %#v, want dashboard values", gotRuntime.Spec.Sandbox.CommandSandbox)
	}
	if gotRuntime.Spec.Resources == nil || len(gotRuntime.Spec.Resources.Claims) != 1 || gotRuntime.Spec.Resources.Claims[0].Name != "externally-managed" {
		t.Fatalf("RuntimeProfile externally managed resource claims were not preserved: %#v", gotRuntime.Spec.Resources)
	}
	var gotMCP platformv1alpha1.MCPPolicy
	_ = c.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: ns}, &gotMCP)
	if gotMCP.Spec.BreakGlass == nil || gotMCP.Spec.BreakGlass.Enabled || !gotMCP.Spec.BreakGlass.RequireAuditReason || !gotMCP.Spec.BreakGlass.AdminMediated {
		t.Fatalf("MCPPolicy break glass was not updated: %#v", gotMCP.Spec.BreakGlass)
	}
	var gotMode platformv1alpha1.ModeTemplate
	_ = c.Get(context.Background(), types.NamespacedName{Name: "direct"}, &gotMode)
	if gotMode.Spec.Constraints == nil || gotMode.Spec.Constraints.MaxTurns != 42 {
		t.Fatal("ModeTemplate GitOps-managed constraints were not preserved")
	}
}

func TestLegacyPolicyUpdatesPreserveNewFields(t *testing.T) {
	scheme := testProjectScheme(t)
	ctx := resourceActorContext("alice-id", "member", "Alice Smith")
	ns := deriveUserNamespaceName("Alice Smith", "alice-id")
	runtime := &platformv1alpha1.RuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns}, Spec: platformv1alpha1.RuntimeProfileSpec{
		Sandbox: &platformv1alpha1.RuntimeProfileSandbox{
			RuntimeClassName: "gvisor", EnablePrivateProcfs: true,
			CommandSandbox: &platformv1alpha1.RuntimeProfileCommandSandbox{Path: []string{"/usr/bin"}, ExtraWritablePaths: []string{"/cache/old"}},
		},
		Admission: &platformv1alpha1.RuntimeProfileAdmission{MaxConcurrentRuns: 7},
	}}
	mcp := &platformv1alpha1.MCPPolicy{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns}, Spec: platformv1alpha1.MCPPolicySpec{BreakGlass: &platformv1alpha1.MCPBreakGlass{Enabled: true}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(userNamespaceObj(ns), runtime, mcp).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	if _, err := srv.UpdateRuntimeProfile(ctx, &platform.UpdateRuntimeProfileRequest{Profile: &platform.RuntimeProfile{Name: "default", PermissionMode: "read-only", EgressMode: "disabled", ExtraWritablePaths: []string{"/cache/new"}}}); err != nil {
		t.Fatalf("legacy UpdateRuntimeProfile() error = %v", err)
	}
	if _, err := srv.UpdateMCPPolicy(ctx, &platform.UpdateMCPPolicyRequest{Policy: &platform.MCPPolicy{Name: "default", DefaultAction: "Deny"}}); err != nil {
		t.Fatalf("legacy UpdateMCPPolicy() error = %v", err)
	}

	var gotRuntime platformv1alpha1.RuntimeProfile
	_ = c.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: ns}, &gotRuntime)
	if gotRuntime.Spec.Sandbox.RuntimeClassName != "gvisor" || !gotRuntime.Spec.Sandbox.EnablePrivateProcfs || gotRuntime.Spec.Admission == nil || gotRuntime.Spec.Admission.MaxConcurrentRuns != 7 || len(gotRuntime.Spec.Sandbox.CommandSandbox.Path) != 1 || len(gotRuntime.Spec.Sandbox.CommandSandbox.ExtraWritablePaths) != 1 || gotRuntime.Spec.Sandbox.CommandSandbox.ExtraWritablePaths[0] != "/cache/new" {
		t.Fatalf("legacy RuntimeProfile update did not preserve new fields: %#v", gotRuntime.Spec)
	}
	var gotMCP platformv1alpha1.MCPPolicy
	_ = c.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: ns}, &gotMCP)
	if gotMCP.Spec.BreakGlass == nil || !gotMCP.Spec.BreakGlass.Enabled {
		t.Fatalf("legacy MCPPolicy update did not preserve break glass: %#v", gotMCP.Spec)
	}
}

func TestResourceCRDValidation(t *testing.T) {
	scheme := testProjectScheme(t)
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme}
	ctx := resourceActorContext("alice", "member", "Alice")
	cases := []error{}
	_, err := srv.CreateRuntimeProfile(ctx, &platform.CreateRuntimeProfileRequest{Profile: &platform.RuntimeProfile{Name: "Bad Name", PermissionMode: "root", EgressMode: "restricted"}})
	cases = append(cases, err)
	_, err = srv.CreateRuntimeProfile(ctx, &platform.CreateRuntimeProfileRequest{Profile: &platform.RuntimeProfile{Name: "bad-git-policy", PermissionMode: "workspace-write", GitRemoteWrites: ptr.To("sometimes"), EgressMode: "restricted"}})
	cases = append(cases, err)
	_, err = srv.CreateRuntimeProfile(ctx, &platform.CreateRuntimeProfileRequest{Profile: &platform.RuntimeProfile{Name: "cache", PermissionMode: "workspace-write", EgressMode: "restricted", ExtraWritablePaths: []string{"/usr/local/cache"}}})
	cases = append(cases, err)
	_, err = srv.CreateRuntimeProfile(ctx, &platform.CreateRuntimeProfileRequest{Profile: &platform.RuntimeProfile{Name: "relative-cache", PermissionMode: "workspace-write", EgressMode: "restricted", ExtraWritablePaths: []string{"cache/go"}}})
	cases = append(cases, err)
	_, err = srv.CreateRuntimeProfile(ctx, &platform.CreateRuntimeProfileRequest{Profile: &platform.RuntimeProfile{Name: "negative-resource", PermissionMode: "workspace-write", EgressMode: "restricted", ResourceLimits: map[string]string{"cpu": "-1"}}})
	cases = append(cases, err)
	_, err = srv.CreateRuntimeProfile(ctx, &platform.CreateRuntimeProfileRequest{Profile: &platform.RuntimeProfile{Name: "request-over-limit", PermissionMode: "workspace-write", EgressMode: "restricted", ResourceRequests: map[string]string{"cpu": "2"}, ResourceLimits: map[string]string{"cpu": "1"}}})
	cases = append(cases, err)
	_, err = srv.CreateRuntimeProfile(ctx, &platform.CreateRuntimeProfileRequest{Profile: &platform.RuntimeProfile{Name: "duplicate-claims", PermissionMode: "workspace-write", EgressMode: "restricted", ResourceClaims: []*platform.RuntimeResourceClaim{{Name: "gpu"}, {Name: "gpu"}}}})
	cases = append(cases, err)
	_, err = srv.CreateGuardrailPolicy(ctx, &platform.CreateGuardrailPolicyRequest{Policy: &platform.GuardrailPolicy{Name: "rules", Rules: []*platform.GuardrailRule{{Name: "bad", Type: "tool-input", Action: "block", Regex: "["}}}})
	cases = append(cases, err)
	_, err = srv.CreateModeTemplate(context.Background(), &platform.CreateModeTemplateRequest{Template: &platform.ModeTemplate{Name: "mode", Category: "invalid", ExecutionStrategy: "serial"}})
	cases = append(cases, err)
	for i, err := range cases {
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("case %d: want InvalidArgument, got %v", i, err)
		}
	}
}

func TestCatalogAuthorization(t *testing.T) {
	scheme := testProjectScheme(t)
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme}
	if _, err := srv.ListModeTemplates(context.WithValue(context.Background(), requestActorContextKey{}, requestActor{}), &platform.ListModeTemplatesRequest{}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("empty recorded actor: got %v", err)
	}
	member := resourceActorContext("alice", "member", "Alice")
	if _, err := srv.ListRoleInstructions(member, &platform.ListRoleInstructionsRequest{}); err != nil {
		t.Fatalf("authenticated list error = %v", err)
	}
	if _, err := srv.CreateRoleInstruction(member, &platform.CreateRoleInstructionRequest{Instruction: &platform.RoleInstruction{Name: "executor", Instructions: "do work", ToolAccess: "execution"}}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("member create: got %v", err)
	}
	admin := resourceActorContext("admin", "admin", "Admin")
	if _, err := srv.CreateRoleInstruction(admin, &platform.CreateRoleInstructionRequest{Instruction: &platform.RoleInstruction{Name: "executor", Instructions: "do work", ToolAccess: "execution"}}); err != nil {
		t.Fatalf("admin create error = %v", err)
	}
}

func TestRoleInstructionCRUDPreservesProviderModels(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	admin := resourceActorContext("admin", "admin", "Admin")

	created, err := srv.CreateRoleInstruction(admin, &platform.CreateRoleInstructionRequest{Instruction: &platform.RoleInstruction{
		Name:           "analyst",
		Instructions:   "analyze",
		ToolAccess:     "analysis",
		Model:          " fallback-model ",
		ReasoningLevel: "max",
		ModelsByProvider: map[string]string{
			" OpenAI ": " gpt-5.6-sol ",
			"copilot":  "gpt-5.4",
		},
	}})
	if err != nil {
		t.Fatalf("CreateRoleInstruction: %v", err)
	}
	if created.Model != "fallback-model" || created.ModelsByProvider["openai"] != "gpt-5.6-sol" || created.ModelsByProvider["copilot"] != "gpt-5.4" || created.ReasoningLevel != "max" {
		t.Fatalf("created role routing = model %q, providers %#v, reasoning %q", created.Model, created.ModelsByProvider, created.ReasoningLevel)
	}

	stored := &platformv1alpha1.RoleInstruction{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "analyst"}, stored); err != nil {
		t.Fatalf("get stored RoleInstruction: %v", err)
	}
	if stored.Spec.Model != "fallback-model" || stored.Spec.ModelsByProvider["openai"] != "gpt-5.6-sol" || stored.Spec.ReasoningLevel != platformv1alpha1.ReasoningMax {
		t.Fatalf("stored role routing = model %q, providers %#v, reasoning %q", stored.Spec.Model, stored.Spec.ModelsByProvider, stored.Spec.ReasoningLevel)
	}

	listed, err := srv.ListRoleInstructions(admin, &platform.ListRoleInstructionsRequest{})
	if err != nil {
		t.Fatalf("ListRoleInstructions: %v", err)
	}
	if len(listed.Instructions) != 1 || listed.Instructions[0].ModelsByProvider["copilot"] != "gpt-5.4" || listed.Instructions[0].ReasoningLevel != "max" {
		t.Fatalf("listed roles = %#v", listed.Instructions)
	}

	updated, err := srv.UpdateRoleInstruction(admin, &platform.UpdateRoleInstructionRequest{Instruction: &platform.RoleInstruction{
		Name:         "analyst",
		Instructions: "analyze updated requirements",
		ToolAccess:   "analysis",
	}})
	if err != nil {
		t.Fatalf("UpdateRoleInstruction: %v", err)
	}
	if updated.Model != "" || len(updated.ModelsByProvider) != 0 || updated.ReasoningLevel != "" {
		t.Fatalf("updated role retained cleared routing: model %q, providers %#v, reasoning %q", updated.Model, updated.ModelsByProvider, updated.ReasoningLevel)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "analyst"}, stored); err != nil {
		t.Fatalf("get updated RoleInstruction: %v", err)
	}
	if stored.Spec.Model != "" || len(stored.Spec.ModelsByProvider) != 0 || stored.Spec.ReasoningLevel != "" {
		t.Fatalf("stored role retained cleared routing: model %q, providers %#v, reasoning %q", stored.Spec.Model, stored.Spec.ModelsByProvider, stored.Spec.ReasoningLevel)
	}
}

func TestRoleInstructionRejectsInvalidProviderModels(t *testing.T) {
	tests := []map[string]string{
		{"openai": "  "},
		{"openai": "model-a", " OpenAI ": "model-b"},
	}
	for _, models := range tests {
		_, err := roleSpec(&platform.RoleInstruction{
			Name:             "analyst",
			Instructions:     "analyze",
			ToolAccess:       "analysis",
			ModelsByProvider: models,
		})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("roleSpec(%#v) error = %v, want InvalidArgument", models, err)
		}
	}
}

func TestRoleInstructionRejectsInvalidReasoningLevel(t *testing.T) {
	_, err := roleSpec(&platform.RoleInstruction{
		Name: "analyst", Instructions: "analyze", ToolAccess: "analysis", ReasoningLevel: "ultra",
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("roleSpec invalid reasoning error = %v, want InvalidArgument", err)
	}
}
