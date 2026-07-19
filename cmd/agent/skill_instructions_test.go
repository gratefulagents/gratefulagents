package main

import (
	"context"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSkillInstructionsForRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	inline := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "grafana-runbook", Namespace: "ns"},
		Spec: platformv1alpha1.SkillSpec{
			Source: platformv1alpha1.SkillSource{
				Inline: &platformv1alpha1.SkillInlineSource{Instructions: "Prefer rate() before histogram_quantile."},
			},
		},
	}
	// Git-sourced skill: only the controller-resolved content counts.
	gitResolved := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "pdf", Namespace: "ns"},
		Spec: platformv1alpha1.SkillSpec{
			Source: platformv1alpha1.SkillSource{
				Git: &platformv1alpha1.SkillGitSource{URL: "https://github.com/anthropics/skills", Path: "document-skills/pdf"},
			},
		},
		Status: platformv1alpha1.SkillStatus{
			Phase:    "Ready",
			Resolved: &platformv1alpha1.SkillResolved{Name: "pdf", Instructions: "Use pdfplumber for extraction."},
		},
	}
	// Git-sourced skill that has not resolved yet contributes nothing.
	gitPending := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "ns"},
		Spec: platformv1alpha1.SkillSpec{
			Source: platformv1alpha1.SkillSource{
				Git: &platformv1alpha1.SkillGitSource{URL: "https://github.com/anthropics/skills", Path: "x/pending"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inline, gitResolved, gitPending).Build()

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: platformv1alpha1.AgentRunSpec{
			SkillRefs: []platformv1alpha1.NamedRef{{Name: "grafana-runbook"}, {Name: "pdf"}, {Name: "pending"}, {Name: "missing"}},
		},
	}
	got := skillInstructionsForRun(context.Background(), c, run)
	if !strings.Contains(got, "a skill is not itself callable") || !strings.Contains(got, "environment block") {
		t.Fatalf("instructions missing skill/tool usage guidance: %q", got)
	}
	if !strings.Contains(got, "## Skill: grafana-runbook") || !strings.Contains(got, "histogram_quantile") {
		t.Fatalf("instructions missing inline skill block: %q", got)
	}
	if !strings.Contains(got, "## Skill: pdf") || !strings.Contains(got, "pdfplumber") {
		t.Fatalf("instructions missing resolved git skill block: %q", got)
	}
	if strings.Contains(got, "pending") {
		t.Fatalf("unresolved git skill must not contribute: %q", got)
	}

	if got := skillInstructionsForRun(context.Background(), c, &platformv1alpha1.AgentRun{}); got != "" {
		t.Fatalf("expected empty for run without refs, got %q", got)
	}
}

// TestSkillInstructionsSkipInvalidPhase pins that skills the controller
// marked Invalid contribute nothing: the inline fallback must not resurrect
// content that failed validation (e.g. an ambiguous inline+git source).
func TestSkillInstructionsSkipInvalidPhase(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	invalid := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: "ns"},
		Spec: platformv1alpha1.SkillSpec{
			Source: platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: "do not show this"}},
		},
		Status: platformv1alpha1.SkillStatus{Phase: "Invalid"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(invalid).Build()
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec:       platformv1alpha1.AgentRunSpec{SkillRefs: []platformv1alpha1.NamedRef{{Name: "broken"}}},
	}
	if got := skillInstructionsForRun(context.Background(), c, run); got != "" {
		t.Fatalf("Invalid skill contributed instructions: %q", got)
	}
}

// TestSkillInstructionsUseResolvedName pins that the prompt header uses the
// controller-resolved skill name (SKILL.md frontmatter) when it differs from
// the reference name.
func TestSkillInstructionsUseResolvedName(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	skill := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "cr-name", Namespace: "ns"},
		Spec: platformv1alpha1.SkillSpec{
			Source: platformv1alpha1.SkillSource{Git: &platformv1alpha1.SkillGitSource{URL: "https://github.com/o/r", Path: "pdf"}},
		},
		Status: platformv1alpha1.SkillStatus{
			Phase:    "Ready",
			Resolved: &platformv1alpha1.SkillResolved{Name: "pdf", Instructions: "use pdfplumber"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(skill).Build()
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec:       platformv1alpha1.AgentRunSpec{SkillRefs: []platformv1alpha1.NamedRef{{Name: "cr-name"}}},
	}
	got := skillInstructionsForRun(context.Background(), c, run)
	if !strings.Contains(got, "## Skill: pdf") {
		t.Fatalf("resolved name not used in header: %q", got)
	}
}
