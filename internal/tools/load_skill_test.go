package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	pdfSkillName     = "pdf"
	pendingSkillName = "pending"
)

func TestLoadSkillToolProgressivelyLoadsInstructions(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	inline := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "grafana-runbook", Namespace: "ns"},
		Spec: platformv1alpha1.SkillSpec{
			Description: "Prometheus and Grafana query guidance.",
			Source: platformv1alpha1.SkillSource{
				Inline: &platformv1alpha1.SkillInlineSource{Instructions: "Prefer rate() before histogram_quantile."},
			},
		},
	}
	resolved := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: pdfSkillName, Namespace: "ns"},
		Spec: platformv1alpha1.SkillSpec{
			Description: "PDF guidance from Example Security (MIT).",
			Source: platformv1alpha1.SkillSource{
				Git: &platformv1alpha1.SkillGitSource{URL: "https://github.com/anthropics/skills", Ref: "abc123", Path: "document-skills/pdf"},
			},
		},
		Status: platformv1alpha1.SkillStatus{
			Phase: "Ready",
			Resolved: &platformv1alpha1.SkillResolved{
				Name:         "pdf-documents",
				Description:  "Create and inspect PDF documents.",
				Instructions: "Use pdfplumber for extraction.",
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inline, resolved).Build()
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: platformv1alpha1.AgentRunSpec{
			SkillRefs: []platformv1alpha1.NamedRef{{Name: pdfSkillName}, {Name: "grafana-runbook"}, {Name: pdfSkillName}},
		},
	}
	registry := NewRegistry(t.TempDir())

	tool, _ := RegisterLoadSkillTool(context.Background(), registry, k8sClient, run)
	if tool == nil || registry.Get("load_skill") == nil {
		t.Fatal("load_skill was not registered")
	}
	description := tool.Description()
	if !strings.Contains(description, "grafana-runbook: Prometheus and Grafana query guidance.") ||
		!strings.Contains(description, "pdf: PDF guidance from Example Security (MIT).") {
		t.Fatalf("description does not advertise skill summaries: %q", description)
	}
	if strings.Contains(description, "histogram_quantile") || strings.Contains(description, "pdfplumber") {
		t.Fatalf("description eagerly exposed full skill instructions: %q", description)
	}

	var schema struct {
		Properties struct {
			Name struct {
				Enum []string `json:"enum"`
			} `json:"name"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if got := strings.Join(schema.Properties.Name.Enum, ","); got != "grafana-runbook,pdf" {
		t.Fatalf("skill enum = %q, want sorted deduplicated names", got)
	}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"pdf"}`), "call-1")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}
	if !strings.Contains(result.Content, `Skill "pdf" loaded`) || strings.Contains(result.Content, "pdfplumber") {
		t.Fatalf("tool result should confirm loading without carrying instructions: %q", result.Content)
	}
	loadedInstructions := tool.LoadedInstructions()
	if !strings.Contains(loadedInstructions, "## Skill: pdf") || !strings.Contains(loadedInstructions, "pdfplumber") {
		t.Fatalf("loaded skill was not installed into subsequent model context: %q", loadedInstructions)
	}
	if !strings.Contains(loadedInstructions, "https://github.com/anthropics/skills @ abc123") ||
		!strings.Contains(loadedInstructions, "Skill description: PDF guidance from Example Security (MIT)") {
		t.Fatalf("loaded external skill omitted provenance or license attribution: %q", loadedInstructions)
	}
	if strings.Contains(loadedInstructions, "histogram_quantile") {
		t.Fatalf("unselected skill instructions were loaded: %q", loadedInstructions)
	}
	if strings.Contains(tool.Description(), "pdfplumber") {
		t.Fatalf("full instructions leaked into skill discovery metadata: %q", tool.Description())
	}
}

func TestLoadSkillToolRejectsUnavailableSkills(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	pending := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: pendingSkillName, Namespace: "ns"},
		Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{
			Git: &platformv1alpha1.SkillGitSource{URL: "https://github.com/o/r"},
		}},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pending).Build()
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns"},
		Spec:       platformv1alpha1.AgentRunSpec{SkillRefs: []platformv1alpha1.NamedRef{{Name: pendingSkillName}}},
	}
	tool, _ := RegisterLoadSkillTool(context.Background(), NewRegistry(t.TempDir()), k8sClient, run)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"other"}`), "call-1")
	if err != nil || !result.IsError || !strings.Contains(result.Content, "not enabled") {
		t.Fatalf("unapproved skill Execute() = %+v, %v", result, err)
	}
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"name":"pending"}`), "call-2")
	if err != nil || !result.IsError || !strings.Contains(result.Content, "no resolved instructions") {
		t.Fatalf("pending skill Execute() = %+v, %v", result, err)
	}
}

func TestRegisterLoadSkillToolSkipsRunsWithoutSkills(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	if tool, _ := RegisterLoadSkillTool(context.Background(), registry, nil, &platformv1alpha1.AgentRun{}); tool != nil {
		t.Fatalf("RegisterLoadSkillTool() = %v, want nil", tool)
	}
	if registry.Get("load_skill") != nil {
		t.Fatal("load_skill registered without enabled skills")
	}
}

func TestRegisterLoadSkillToolOffersInstalledCompanionSkills(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	companion := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: ComputerUseSkillName, Namespace: "ns"},
		Spec: platformv1alpha1.SkillSpec{
			Description: "How to operate the user's Mac through computer_use.",
			Source:      platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: "observe, then act."}},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(companion).Build()

	// No skillRefs on the run: the companion alone makes load_skill available.
	registry := NewRegistry(t.TempDir())
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: "ns"}}
	tool, _ := RegisterLoadSkillTool(context.Background(), registry, k8sClient, run, ComputerUseSkillName, "not-installed", " ")
	if tool == nil || registry.Get("load_skill") == nil {
		t.Fatal("companion skill did not register load_skill")
	}
	description := tool.Description()
	if !strings.Contains(description, ComputerUseSkillName+": How to operate") {
		t.Fatalf("companion summary missing from description: %s", description)
	}
	if strings.Contains(description, "not-installed") {
		t.Fatalf("uninstalled companion advertised: %s", description)
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"computer-use"}`), "call-1")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}
	if !strings.Contains(tool.LoadedInstructions(), "observe, then act.") {
		t.Fatalf("companion instructions not loaded: %q", tool.LoadedInstructions())
	}

	// A companion whose Skill is absent must not create the tool on its own.
	registry = NewRegistry(t.TempDir())
	if tool, _ := RegisterLoadSkillTool(context.Background(), registry, k8sClient, run, "not-installed"); tool != nil || registry.Get("load_skill") != nil {
		t.Fatal("load_skill registered for an uninstalled companion")
	}

	// Explicit refs and companions merge without duplicates.
	run.Spec.SkillRefs = []platformv1alpha1.NamedRef{{Name: ComputerUseSkillName}}
	tool, _ = RegisterLoadSkillTool(context.Background(), NewRegistry(t.TempDir()), k8sClient, run, ComputerUseSkillName)
	if got := strings.Count(tool.Description(), "\n- "+ComputerUseSkillName); got != 1 {
		t.Fatalf("companion listed %d times, want 1: %s", got, tool.Description())
	}
}

func TestRegisterLoadSkillToolSkipsUnusableSkills(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	invalid := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid", Namespace: "ns"},
		Spec:       platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{Git: &platformv1alpha1.SkillGitSource{URL: "https://gitlab.com/a/b"}}},
		Status: platformv1alpha1.SkillStatus{
			Phase:      "Invalid",
			Conditions: []metav1.Condition{{Type: "Resolved", Status: metav1.ConditionFalse, Reason: "InvalidSource", Message: "not a github.com repository"}},
		},
	}
	neverFetched := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "never-fetched", Namespace: "ns"},
		Spec:       platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{Git: &platformv1alpha1.SkillGitSource{URL: "https://github.com/a/b"}}},
		Status: platformv1alpha1.SkillStatus{
			Phase:      "Error",
			Conditions: []metav1.Condition{{Type: "Resolved", Status: metav1.ConditionFalse, Reason: "FetchFailed", Message: "rate limited"}},
		},
	}
	staleButUsable := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "stale", Namespace: "ns"},
		Spec:       platformv1alpha1.SkillSpec{Description: "Still useful", Source: platformv1alpha1.SkillSource{Git: &platformv1alpha1.SkillGitSource{URL: "https://github.com/a/b"}}},
		Status: platformv1alpha1.SkillStatus{
			Phase:    "Error",
			Resolved: &platformv1alpha1.SkillResolved{Instructions: "Last good content."},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(invalid, neverFetched, staleButUsable).Build()
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: platformv1alpha1.AgentRunSpec{SkillRefs: []platformv1alpha1.NamedRef{
			{Name: "invalid"}, {Name: "never-fetched"}, {Name: "stale"}, {Name: "missing"},
		}},
	}
	tool, skipped := RegisterLoadSkillTool(context.Background(), NewRegistry(t.TempDir()), k8sClient, run)
	if tool == nil {
		t.Fatal("usable skill should still register load_skill")
		return
	}
	if got := strings.Join(tool.names, ","); got != "stale" {
		t.Fatalf("offered skills = %q, want only the usable one", got)
	}
	reasons := make(map[string]string, len(skipped))
	for _, item := range skipped {
		reasons[item.Name] = item.Reason
	}
	if len(reasons) != 3 {
		t.Fatalf("skipped = %+v, want invalid, never-fetched, missing", skipped)
	}
	if !strings.Contains(reasons["invalid"], "Invalid") || !strings.Contains(reasons["invalid"], "not a github.com repository") {
		t.Fatalf("invalid reason = %q", reasons["invalid"])
	}
	if !strings.Contains(reasons["never-fetched"], "no resolved instructions") || !strings.Contains(reasons["never-fetched"], "rate limited") {
		t.Fatalf("never-fetched reason = %q", reasons["never-fetched"])
	}
	if !strings.Contains(reasons["missing"], "not found") {
		t.Fatalf("missing reason = %q", reasons["missing"])
	}

	// Every ref unusable: no tool, but the reasons still surface.
	run.Spec.SkillRefs = []platformv1alpha1.NamedRef{{Name: "missing"}}
	registry := NewRegistry(t.TempDir())
	tool, skipped = RegisterLoadSkillTool(context.Background(), registry, k8sClient, run)
	if tool != nil || registry.Get("load_skill") != nil || len(skipped) != 1 {
		t.Fatalf("tool = %v, skipped = %+v", tool, skipped)
	}
}

func TestLoadSkillToolPersistsAndRestoresLoadedSkills(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	skill := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: pdfSkillName, Namespace: "ns"},
		Spec:       platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: "Use pdfplumber."}}},
	}
	other := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns"},
		Spec:       platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: "Other guidance."}}},
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec:       platformv1alpha1.AgentRunSpec{SkillRefs: []platformv1alpha1.NamedRef{{Name: pdfSkillName}, {Name: "other"}}},
		Status:     platformv1alpha1.AgentRunStatus{Policy: &platformv1alpha1.AgentRunResolvedPolicy{ResolvedPermissionMode: "read-only"}},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(skill, other, run).WithStatusSubresource(run).Build()

	tool, _ := RegisterLoadSkillTool(context.Background(), NewRegistry(t.TempDir()), k8sClient, run)
	if tool.LoadedInstructions() != "" {
		t.Fatalf("nothing should be loaded on a fresh run, got %q", tool.LoadedInstructions())
	}
	if result, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"pdf"}`), "call-1"); err != nil || result.IsError {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}

	stored := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), stored); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Status.Policy == nil || strings.Join(stored.Status.Policy.ResolvedSkills, ",") != "pdf" {
		t.Fatalf("resolvedSkills = %+v, want [pdf]", stored.Status.Policy)
	}
	if stored.Status.Policy.ResolvedPermissionMode != "read-only" {
		t.Fatalf("other policy fields were clobbered: %+v", stored.Status.Policy)
	}

	// A replacement pod registers the tool from the stored run and gets the
	// guidance back without another load_skill call.
	replacement, _ := RegisterLoadSkillTool(context.Background(), NewRegistry(t.TempDir()), k8sClient, stored)
	restored := replacement.LoadedInstructions()
	if !strings.Contains(restored, "## Skill: pdf") || !strings.Contains(restored, "Use pdfplumber.") {
		t.Fatalf("loaded skill was not restored: %q", restored)
	}
	if strings.Contains(restored, "Other guidance.") {
		t.Fatalf("unloaded skill was restored: %q", restored)
	}
	if got := strings.Join(replacement.LoadedNames(), ","); got != "pdf" {
		t.Fatalf("LoadedNames() = %q", got)
	}
}
