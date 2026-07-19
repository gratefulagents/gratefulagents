package mode

import (
	"context"
	"os"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestMain populates the DefaultRegistry with test fixtures before running tests.
func TestMain(m *testing.M) {
	registerTestFixtures()
	os.Exit(m.Run())
}

// mustGet retrieves a template from the registry or fails the test.
func mustGet(t *testing.T, key string) *platformv1alpha1.ModeTemplateSpec {
	t.Helper()
	tmpl, ok := DefaultRegistry.Get(key)
	if !ok {
		t.Fatalf("template %q not found in registry", key)
	}
	return tmpl
}

// registerTestFixtures loads minimal but representative mode templates for testing.
func registerTestFixtures() {
	fixtures := map[string]*platformv1alpha1.ModeTemplateSpec{
		"chat": {
			Name: "chat", Version: "v1", DisplayName: "Chat",
			Category:     platformv1alpha1.ModeCategoryDirect,
			Instructions: "CHAT MODE — Interactive Development\n\nYou are an interactive coding assistant.",
		},
		"auto": {
			Name: "auto", Version: "v1", DisplayName: "Auto",
			Category: platformv1alpha1.ModeCategoryDirect, Autonomous: true,
			Instructions: "AUTO MODE — Autonomous Execution",
		},
		"eco": {
			Name: "eco", Version: "v1", DisplayName: "Eco",
			Category:     platformv1alpha1.ModeCategoryDirect,
			Instructions: "ECO MODE — Token-Efficient Execution",
		},
		"plan": {
			Name: "plan", Version: "v1", DisplayName: "Plan",
			Category:     platformv1alpha1.ModeCategoryDirect,
			Instructions: "PLAN MODE — Analysis and Planning Only",
		},
		"tdd": {
			Name: "tdd", Version: "v1", DisplayName: "TDD",
			Category:     platformv1alpha1.ModeCategoryDirect,
			Instructions: "TDD MODE — Test-Driven Development\n\nRED: Write failing tests FIRST.\nGREEN: Make tests pass.\nREFACTOR: Clean up.",
		},
		"review": {
			Name: "review", Version: "v1", DisplayName: "Review",
			Category:     platformv1alpha1.ModeCategoryDirect,
			Instructions: "REVIEW MODE — Code Review Analysis",
		},
		"research": {
			Name: "research", Version: "v1", DisplayName: "Research",
			Category:     platformv1alpha1.ModeCategoryDirect,
			Instructions: "RESEARCH MODE — Deep Analysis",
		},
		"debug": {
			Name: "debug", Version: "v1", DisplayName: "Debug",
			Category:     platformv1alpha1.ModeCategoryDirect,
			Instructions: "DEBUG MODE — Systematic Root-Cause Analysis",
		},
		"interactive": {
			Name: "interactive", Version: "v1", DisplayName: "Interactive",
			Category: platformv1alpha1.ModeCategoryOrchestrated, Autonomous: true,
			Constraints:  &platformv1alpha1.ModeConstraints{MaxTurns: 120, MaxRuntimeMinutes: 180},
			Instructions: "INTERACTIVE MODE — Collaborative Execution",
		},
		"autopilot": {
			Name: "autopilot", Version: "v1", DisplayName: "Autopilot",
			Category: platformv1alpha1.ModeCategoryOrchestrated, Autonomous: true,
			Constraints:  &platformv1alpha1.ModeConstraints{MaxTurns: 120, MaxRuntimeMinutes: 180},
			Instructions: "AUTOPILOT MODE — Full Autonomous Pipeline",
		},
		"ultrawork": {
			Name: "ultrawork", Version: "v1", DisplayName: "Ultrawork",
			Category: platformv1alpha1.ModeCategoryOrchestrated, Autonomous: true,
			ExecutionStrategy: platformv1alpha1.ExecutionStrategyParallel,
			Constraints:       &platformv1alpha1.ModeConstraints{MaxConcurrentSubAgents: 6, MaxRuntimeMinutes: 180},
			Instructions:      "ULTRAWORK MODE — Parallel Decomposition and Execution",
		},
		"team-chat": {
			Name: "team-chat", Version: "v1", DisplayName: "Team Chat",
			Category:     platformv1alpha1.ModeCategoryOrchestrated,
			Instructions: "TEAM CHAT MODE — Interactive Team Orchestration",
		},
		"pipeline": {
			Name: "pipeline", Version: "v1", DisplayName: "Pipeline",
			Category: platformv1alpha1.ModeCategoryOrchestrated, Autonomous: true,
			Instructions: "PIPELINE MODE — Gated Sequential Processing",
		},
		"ralph": {
			Name: "ralph", Version: "v1", DisplayName: "Ralph",
			Category: platformv1alpha1.ModeCategoryOrchestrated, Autonomous: true,
			Constraints:  &platformv1alpha1.ModeConstraints{MaxRetries: 10, MaxRuntimeMinutes: 360},
			Instructions: "RALPH MODE — Relentless Autonomous Loop for Persistent Hardening\n\nPhilosophy: Never give up. Never declare partial success. Loop until FULLY verified with evidence.\n\nEXECUTE: Implement.\nVERIFY: Run checks.\nSelf-Healing Rules: If stuck after 3 retries, try different approach.\nAttempt N/10.",
		},
		"ralplan": {
			Name: "ralplan", Version: "v1", DisplayName: "Ralplan",
			Category: platformv1alpha1.ModeCategoryOrchestrated, Autonomous: true,
			Constraints:  &platformv1alpha1.ModeConstraints{MaxRetries: 5, MaxRuntimeMinutes: 60},
			Instructions: "RALPLAN MODE — Iterative Planning with Consensus",
		},
		"ideal": {
			Name: "ideal", Version: "v1", DisplayName: "Ideal",
			Category: platformv1alpha1.ModeCategoryOrchestrated, Autonomous: true,
			Constraints:  &platformv1alpha1.ModeConstraints{MaxRetries: 10, MaxRuntimeMinutes: 480},
			Instructions: "IDEAL MODE — Interview → Plan → Execute\n\nThe complete feature development lifecycle. Interview deeply, plan rigorously, execute relentlessly.",
		},
	}
	for key, spec := range fixtures {
		DefaultRegistry.Register(key, spec)
	}
}

// testScheme returns a runtime.Scheme with the platform API types registered.
func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = platformv1alpha1.AddToScheme(s)
	return s
}

// testFakeClient builds a fake controller-runtime client pre-loaded with
// ModeTemplate CRDs matching the test fixtures in DefaultRegistry.
func testFakeClient() client.WithWatch {
	var objs []client.Object
	for key, spec := range DefaultRegistry.All() {
		objs = append(objs, &platformv1alpha1.ModeTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: key},
			Spec:       *spec,
		})
	}
	return fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(objs...).Build()
}

func TestRegistryTemplatesExist(t *testing.T) {
	expected := []string{
		"chat", "auto", "eco", "plan", "tdd",
		"review", "research", "debug",
		"interactive", "autopilot", "ultrawork", "team-chat", "pipeline", "ralph", "ideal",
	}
	for _, name := range expected {
		tmpl, ok := DefaultRegistry.Get(name)
		if !ok {
			t.Errorf("template %q not found in registry", name)
			continue
		}
		if tmpl.Name == "" {
			t.Errorf("template %q has empty Name", name)
		}
		if tmpl.Version == "" {
			t.Errorf("template %q has empty Version", name)
		}
		if tmpl.Category == "" {
			t.Errorf("template %q has empty Category", name)
		}
	}
}

func TestTemplateCategories(t *testing.T) {
	directModes := []string{"chat", "auto", "eco", "plan", "tdd", "review", "research", "debug"}
	orchestrated := []string{"interactive", "autopilot", "ultrawork", "team-chat", "pipeline", "ralph"}

	for _, name := range directModes {
		tmpl := mustGet(t, name)
		if tmpl.Category != platformv1alpha1.ModeCategoryDirect {
			t.Errorf("template %q should be direct, got %q", name, tmpl.Category)
		}
	}
	for _, name := range orchestrated {
		tmpl := mustGet(t, name)
		if tmpl.Category != platformv1alpha1.ModeCategoryOrchestrated {
			t.Errorf("template %q should be orchestrated, got %q", name, tmpl.Category)
		}
	}
}

func TestTemplateKey(t *testing.T) {
	if got := TemplateKey("chat", "v1"); got != "chat" {
		t.Errorf("TemplateKey(chat, v1) = %q, want chat", got)
	}
	if got := TemplateKey("chat", ""); got != "chat" {
		t.Errorf("TemplateKey(chat, '') = %q, want chat", got)
	}
}

func TestResolverAliasesLegacyChatToAutopilot(t *testing.T) {
	resolver := NewResolver(testFakeClient())
	tmpl, err := resolver.Resolve(context.Background(), &platformv1alpha1.ModeRef{Name: "chat"}, "default")
	if err != nil {
		t.Fatalf("Resolve(chat) error = %v", err)
	}
	if tmpl.Name != "autopilot" || !tmpl.Autonomous {
		t.Fatalf("Resolve(chat) = %#v, want autonomous autopilot", tmpl)
	}
}

func TestResolverForcesAutonomousPacingForSpecializedModes(t *testing.T) {
	resolver := NewResolver(testFakeClient())
	tmpl, err := resolver.Resolve(context.Background(), &platformv1alpha1.ModeRef{Name: "plan"}, "default")
	if err != nil {
		t.Fatalf("Resolve(plan) error = %v", err)
	}
	if !tmpl.Autonomous {
		t.Fatalf("Resolve(plan).Autonomous = false, want true")
	}
}

func TestInferSystemTemplate(t *testing.T) {
	c := testFakeClient()
	ctx := context.Background()

	tests := []struct {
		workflow  platformv1alpha1.AgentRunWorkflowMode
		execution platformv1alpha1.AgentRunExecutionMode
		wantName  string
	}{
		{platformv1alpha1.WorkflowModeChat, platformv1alpha1.ExecutionModeLinear, "interactive"},
		{platformv1alpha1.WorkflowModeAuto, platformv1alpha1.ExecutionModeLinear, "interactive"},
		{platformv1alpha1.WorkflowModeChat, platformv1alpha1.ExecutionModeTeam, "team-chat"},
		{platformv1alpha1.WorkflowModeAuto, platformv1alpha1.ExecutionModeTeam, "team-chat"},
		// Unknown legacy values also use the interactive default template.
		{"unknown", "", "interactive"},
	}

	for _, tt := range tests {
		tmpl := InferSystemTemplate(ctx, c, tt.workflow, tt.execution)
		if tmpl == nil {
			t.Errorf("InferSystemTemplate(%q, %q) returned nil", tt.workflow, tt.execution)
			continue
		}
		if tmpl.Name != tt.wantName {
			t.Errorf("InferSystemTemplate(%q, %q) = %q, want %q", tt.workflow, tt.execution, tmpl.Name, tt.wantName)
		}
	}
}

func TestRalphConstraints(t *testing.T) {
	ralph := mustGet(t, "ralph")
	if ralph.Constraints == nil {
		t.Fatal("ralph-v1 should have constraints")
	}
	if ralph.Constraints.MaxRetries != 10 {
		t.Errorf("ralph-v1 maxRetries = %d, want 10", ralph.Constraints.MaxRetries)
	}
}

func TestUltraworkParallel(t *testing.T) {
	uw := mustGet(t, "ultrawork")
	if uw.ExecutionStrategy != platformv1alpha1.ExecutionStrategyParallel {
		t.Errorf("ultrawork-v1 strategy = %q, want parallel", uw.ExecutionStrategy)
	}
	if uw.Constraints == nil || uw.Constraints.MaxConcurrentSubAgents == 0 {
		t.Error("ultrawork-v1 should have maxConcurrentSubAgents set")
	}
}

// --- Evaluator tests ---

func TestEvaluate_AppliedNewMode(t *testing.T) {
	current := mustGet(t, "chat")
	target := mustGet(t, "auto")
	result := Evaluate(current, target)
	if result.Result != ResultApplied {
		t.Errorf("expected applied, got %q", result.Result)
	}
	if result.Target == nil {
		t.Fatal("expected non-nil target")
	}
	if result.Target.Name != "auto" {
		t.Errorf("target name = %q, want auto", result.Target.Name)
	}
}

func TestEvaluate_Noop(t *testing.T) {
	current := mustGet(t, "chat")
	target := mustGet(t, "chat")
	result := Evaluate(current, target)
	if result.Result != ResultNoop {
		t.Errorf("expected noop, got %q", result.Result)
	}
}

func TestEvaluate_Denied_NotFound(t *testing.T) {
	current := mustGet(t, "chat")
	result := Evaluate(current, nil)
	if result.Result != ResultDenied {
		t.Errorf("expected denied, got %q", result.Result)
	}
	if result.Reason == "" {
		t.Error("expected denial reason")
	}
}

func TestEvaluate_FromNilCurrent(t *testing.T) {
	target := mustGet(t, "eco")
	result := Evaluate(nil, target)
	if result.Result != ResultApplied {
		t.Errorf("expected applied (from nil), got %q", result.Result)
	}
}

func TestEvaluate_CrossCategory(t *testing.T) {
	// Switch from direct to orchestrated mode.
	current := mustGet(t, "chat")
	target := mustGet(t, "autopilot")
	result := Evaluate(current, target)
	if result.Result != ResultApplied {
		t.Errorf("expected applied for cross-category switch, got %q", result.Result)
	}
	if result.Target.Category != platformv1alpha1.ModeCategoryOrchestrated {
		t.Errorf("target category = %q, want orchestrated", result.Target.Category)
	}
}

// --- RBAC tests ---

func TestAuthorizeTransition_MemberDirectMode(t *testing.T) {
	ctx := context.Background()
	k8s := testFakeClient()
	result := AuthorizeTransition(ctx, k8s, "chat", RoleMember)
	if result != nil {
		t.Errorf("member should be allowed to switch to chat, got: %+v", result)
	}
}

func TestAuthorizeTransition_MemberOrchestratedAllowed(t *testing.T) {
	ctx := context.Background()
	k8s := testFakeClient()
	// "autopilot" is orchestrated — system templates are member-accessible.
	result := AuthorizeTransition(ctx, k8s, "autopilot", RoleMember)
	if result != nil {
		t.Errorf("member should be allowed to switch to autopilot, got: %+v", result)
	}
}

func TestAuthorizeTransition_AdminOrchestratedAllowed(t *testing.T) {
	ctx := context.Background()
	k8s := testFakeClient()
	result := AuthorizeTransition(ctx, k8s, "autopilot", RoleAdmin)
	if result != nil {
		t.Errorf("admin should be allowed to switch to autopilot, got: %+v", result)
	}
}

func TestAuthorizeTransition_SystemAlwaysAllowed(t *testing.T) {
	ctx := context.Background()
	k8s := testFakeClient()
	result := AuthorizeTransition(ctx, k8s, "ralph", RoleSystem)
	if result != nil {
		t.Errorf("system should always be allowed, got: %+v", result)
	}
}

func TestAuthorizeTransition_ViewerDenied(t *testing.T) {
	ctx := context.Background()
	k8s := testFakeClient()
	result := AuthorizeTransition(ctx, k8s, "chat", RoleViewer)
	if result == nil {
		t.Fatal("viewer should NOT be allowed to switch modes")
	}
}

func TestAuthorizeTransition_UnknownModeRequiresAdmin(t *testing.T) {
	ctx := context.Background()
	k8s := testFakeClient()
	// Unknown mode falls back to requiring admin.
	result := AuthorizeTransition(ctx, k8s, "custom-mode-xyz", RoleMember)
	if result == nil {
		t.Fatal("unknown mode should require admin")
	}
	result2 := AuthorizeTransition(ctx, k8s, "custom-mode-xyz", RoleAdmin)
	if result2 != nil {
		t.Errorf("admin should be allowed for unknown mode, got: %+v", result2)
	}
}

// --- Denial code tests ---

func TestFormatDenialReason_WithCode(t *testing.T) {
	gr := &GateResult{Gate: "approval", Passed: false, Reason: "not approved", DenyCode: DenyGateFailed}
	got := FormatDenialReason(gr)
	if got == "" {
		t.Error("expected non-empty denial reason")
	}
	if !strings.Contains(got, DenyGateFailed) {
		t.Errorf("expected denial reason to contain %q, got %q", DenyGateFailed, got)
	}
}

func TestFormatDenialReason_Nil(t *testing.T) {
	if got := FormatDenialReason(nil); got != "" {
		t.Errorf("expected empty string for nil, got %q", got)
	}
}

// --- Evaluate with RBAC opts ---

func TestEvaluate_WithRBAC_MemberAllowedOrchestrated(t *testing.T) {
	current := mustGet(t, "chat")
	target := mustGet(t, "autopilot")
	result := Evaluate(current, target, EvaluateOpts{
		Run:       &platformv1alpha1.AgentRun{},
		ActorRole: RoleMember,
		Source:    "ui",
	})
	if result.Result != ResultApplied {
		t.Errorf("expected applied for member→autopilot, got %q (reason: %s)", result.Result, result.Reason)
	}
}

func TestEvaluate_WithRBAC_ViewerDenied(t *testing.T) {
	current := mustGet(t, "chat")
	target := mustGet(t, "autopilot")
	result := Evaluate(current, target, EvaluateOpts{
		Run:       &platformv1alpha1.AgentRun{},
		ActorRole: RoleViewer,
		Source:    "ui",
	})
	if result.Result != ResultDenied {
		t.Errorf("expected denied for viewer→autopilot, got %q", result.Result)
	}
	if result.DenyCode != DenyRBACDenied {
		t.Errorf("deny code = %q, want RBAC_DENIED", result.DenyCode)
	}
}

func TestEvaluate_WithRBAC_AdminAllowedOrchestrated(t *testing.T) {
	current := mustGet(t, "chat")
	target := mustGet(t, "autopilot")
	result := Evaluate(current, target, EvaluateOpts{
		Run:       &platformv1alpha1.AgentRun{},
		ActorRole: RoleAdmin,
		Source:    "ui",
	})
	if result.Result != ResultApplied {
		t.Errorf("expected applied for admin→autopilot, got %q (reason: %s)", result.Result, result.Reason)
	}
}

// --- Agent catalog tests ---

// --- M9: Instructions tests ---

func TestAllRegisteredTemplatesHaveInstructions(t *testing.T) {
	for name, tmpl := range DefaultRegistry.All() {
		if tmpl.Instructions == "" {
			t.Errorf("template %q has no Instructions", name)
		}
	}
}

func TestRalphInstructionsContent(t *testing.T) {
	ralph := mustGet(t, "ralph")
	if ralph == nil {
		t.Fatal("ralph-v1 template not found")
	}

	requiredPhrases := []string{
		"Never give up",
		"Never declare partial success",
		"evidence",
		"EXECUTE",
		"VERIFY",
		"Attempt",
		"Self-Healing",
	}
	for _, phrase := range requiredPhrases {
		if !strings.Contains(strings.ToLower(ralph.Instructions), strings.ToLower(phrase)) {
			t.Errorf("ralph instructions missing phrase %q", phrase)
		}
	}
}

func TestTDDInstructionsContent(t *testing.T) {
	tdd := mustGet(t, "tdd")
	if tdd == nil {
		t.Fatal("tdd-v1 template not found")
	}

	requiredPhrases := []string{
		"RED",
		"GREEN",
		"REFACTOR",
		"test",
		"failing",
	}
	for _, phrase := range requiredPhrases {
		if !strings.Contains(strings.ToUpper(tdd.Instructions), strings.ToUpper(phrase)) {
			t.Errorf("tdd instructions missing phrase %q", phrase)
		}
	}
}
