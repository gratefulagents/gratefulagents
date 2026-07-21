package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
			Source: platformv1alpha1.SkillSource{
				Git: &platformv1alpha1.SkillGitSource{URL: "https://github.com/anthropics/skills", Path: "document-skills/pdf"},
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

	tool := RegisterLoadSkillTool(context.Background(), registry, k8sClient, run)
	if tool == nil || registry.Get("load_skill") == nil {
		t.Fatal("load_skill was not registered")
	}
	description := tool.Description()
	if !strings.Contains(description, "grafana-runbook: Prometheus and Grafana query guidance.") ||
		!strings.Contains(description, "pdf: Create and inspect PDF documents.") {
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
	tool := RegisterLoadSkillTool(context.Background(), NewRegistry(t.TempDir()), k8sClient, run)

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
	if tool := RegisterLoadSkillTool(context.Background(), registry, nil, &platformv1alpha1.AgentRun{}); tool != nil {
		t.Fatalf("RegisterLoadSkillTool() = %v, want nil", tool)
	}
	if registry.Get("load_skill") != nil {
		t.Fatal("load_skill registered without enabled skills")
	}
}
