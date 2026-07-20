/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package platform

import (
	"context"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func TestParseSkillMD(t *testing.T) {
	content := `---
name: git-release
description: Create consistent releases
license: MIT
metadata:
  audience: maintainers
---

## What I do
- Draft release notes
`
	fm, body, err := parseSkillMD(content)
	if err != nil {
		t.Fatalf("parseSkillMD() error = %v", err)
	}
	if fm.Name != "git-release" || fm.Description != "Create consistent releases" || fm.License != "MIT" {
		t.Fatalf("frontmatter = %+v", fm)
	}
	if fm.Metadata["audience"] != "maintainers" {
		t.Fatalf("metadata = %+v", fm.Metadata)
	}
	if !strings.HasPrefix(body, "## What I do") {
		t.Fatalf("body = %q", body)
	}

	if _, _, err := parseSkillMD("no frontmatter here"); err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
	if _, _, err := parseSkillMD("---\nname: x\ndescription: y"); err == nil {
		t.Fatal("expected error for unterminated frontmatter")
	}
}

func TestValidateSkillFrontmatter(t *testing.T) {
	ok := skillFrontmatter{Name: "pdf", Description: "Process PDFs"}
	if err := validateSkillFrontmatter(ok, "document-skills/pdf"); err != nil {
		t.Fatalf("valid frontmatter rejected: %v", err)
	}
	if err := validateSkillFrontmatter(ok, "document-skills/pdf/SKILL.md"); err != nil {
		t.Fatalf("SKILL.md-suffixed path rejected: %v", err)
	}
	if err := validateSkillFrontmatter(ok, "document-skills/other"); err == nil {
		t.Fatal("expected folder-name mismatch error")
	}
	cases := []skillFrontmatter{
		{Name: "", Description: "d"},
		{Name: "Bad-Name", Description: "d"},
		{Name: "double--hyphen", Description: "d"},
		{Name: "-leading", Description: "d"},
		{Name: "pdf", Description: ""},
		{Name: "pdf", Description: strings.Repeat("x", 1025)},
		{Name: strings.Repeat("a", 65), Description: "d"},
	}
	for i, fm := range cases {
		if err := validateSkillFrontmatter(fm, ""); err == nil {
			t.Fatalf("case %d: expected validation error for %+v", i, fm)
		}
	}
}

func TestParseGitHubRepoURL(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/anthropics/skills",
		"https://github.com/anthropics/skills/",
		"https://github.com/anthropics/skills.git",
		"github.com/anthropics/skills",
		"git@github.com:anthropics/skills.git",
	} {
		owner, repo, err := parseGitHubRepoURL(raw)
		if err != nil || owner != "anthropics" || repo != "skills" {
			t.Fatalf("parseGitHubRepoURL(%q) = %q/%q, %v", raw, owner, repo, err)
		}
	}
	for _, raw := range []string{"https://gitlab.com/a/b", "https://github.com/only-owner", ""} {
		if _, _, err := parseGitHubRepoURL(raw); err == nil {
			t.Fatalf("expected error for %q", raw)
		}
	}
}

type fakeFetcher struct {
	content string
	sha     string
	err     error
}

func (f fakeFetcher) FetchSkillMD(context.Context, platformv1alpha1.SkillGitSource) (string, string, error) {
	return f.content, f.sha, f.err
}

func newSkillTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&platformv1alpha1.Skill{}, &platformv1alpha1.MCPServer{}).Build()
}

func reconcileSkill(t *testing.T, c client.Client, fetcher SkillContentsFetcher, name string) *platformv1alpha1.Skill {
	t.Helper()
	r := &SkillReconciler{Client: c, Fetcher: fetcher}
	key := client.ObjectKey{Namespace: "ns", Name: name}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	skill := &platformv1alpha1.Skill{}
	if err := c.Get(context.Background(), key, skill); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	return skill
}

func TestSkillReconcilerInline(t *testing.T) {
	skill := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "grafana-runbook", Namespace: "ns", Generation: 1},
		Spec: platformv1alpha1.SkillSpec{
			Description: "Query discipline",
			Source: platformv1alpha1.SkillSource{
				Inline: &platformv1alpha1.SkillInlineSource{Instructions: "Always bound queries."},
			},
		},
	}
	got := reconcileSkill(t, newSkillTestClient(t, skill), nil, "grafana-runbook")
	if got.Status.Phase != "Ready" {
		t.Fatalf("phase = %q, conditions = %+v", got.Status.Phase, got.Status.Conditions)
	}
	res := got.Status.Resolved
	if res == nil || res.Name != "grafana-runbook" || res.Instructions != "Always bound queries." {
		t.Fatalf("resolved = %+v", res)
	}
	if got.Status.ObservedGeneration != 1 {
		t.Fatalf("observedGeneration = %d", got.Status.ObservedGeneration)
	}
}

func TestSkillReconcilerGitFetch(t *testing.T) {
	skillMD := "---\nname: pdf\ndescription: Process PDFs\n---\nUse pdfplumber."
	skill := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "pdf", Namespace: "ns", Generation: 1},
		Spec: platformv1alpha1.SkillSpec{
			Source: platformv1alpha1.SkillSource{
				Git: &platformv1alpha1.SkillGitSource{URL: "https://github.com/anthropics/skills", Path: "document-skills/pdf"},
			},
		},
	}
	got := reconcileSkill(t, newSkillTestClient(t, skill), fakeFetcher{content: skillMD, sha: "abc123"}, "pdf")
	if got.Status.Phase != "Ready" {
		t.Fatalf("phase = %q, conditions = %+v", got.Status.Phase, got.Status.Conditions)
	}
	res := got.Status.Resolved
	if res == nil || res.Name != "pdf" || res.Description != "Process PDFs" || res.SHA != "abc123" ||
		!strings.Contains(res.Instructions, "pdfplumber") {
		t.Fatalf("resolved = %+v", res)
	}
}

func TestSkillReconcilerGitFetchError(t *testing.T) {
	skill := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: "ns", Generation: 1},
		Spec: platformv1alpha1.SkillSpec{
			Source: platformv1alpha1.SkillSource{
				Git: &platformv1alpha1.SkillGitSource{URL: "https://github.com/x/y"},
			},
		},
	}
	got := reconcileSkill(t, newSkillTestClient(t, skill), fakeFetcher{err: fmt.Errorf("boom")}, "broken")
	if got.Status.Phase != "Error" {
		t.Fatalf("phase = %q", got.Status.Phase)
	}
	if got.Status.Resolved != nil {
		t.Fatalf("resolved should be nil on fetch error, got %+v", got.Status.Resolved)
	}
}

func TestSkillReconcilerAmbiguousSource(t *testing.T) {
	skill := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "both", Namespace: "ns", Generation: 1},
		Spec: platformv1alpha1.SkillSpec{
			Source: platformv1alpha1.SkillSource{
				Inline: &platformv1alpha1.SkillInlineSource{Instructions: "x"},
				Git:    &platformv1alpha1.SkillGitSource{URL: "https://github.com/x/y"},
			},
		},
	}
	got := reconcileSkill(t, newSkillTestClient(t, skill), nil, "both")
	if got.Status.Phase != "Invalid" {
		t.Fatalf("phase = %q", got.Status.Phase)
	}
}

func TestMCPServerReconcilerValidation(t *testing.T) {
	valid := &platformv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "fetch", Namespace: "ns"},
		Spec: platformv1alpha1.MCPServerSpec{
			MCPServerConfig: &platformv1alpha1.MCPServerConfig{Command: "npx"},
		},
	}
	invalid := &platformv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "no-command", Namespace: "ns"},
		Spec: platformv1alpha1.MCPServerSpec{
			MCPServerConfig: &platformv1alpha1.MCPServerConfig{},
		},
	}
	c := newSkillTestClient(t, valid, invalid)
	r := &MCPServerReconciler{Client: c}
	for name, wantPhase := range map[string]string{"fetch": "Ready", "no-command": "Invalid"} {
		key := client.ObjectKey{Namespace: "ns", Name: name}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("Reconcile(%s) error = %v", name, err)
		}
		srv := &platformv1alpha1.MCPServer{}
		if err := c.Get(context.Background(), key, srv); err != nil {
			t.Fatalf("Get(%s) error = %v", name, err)
		}
		if srv.Status.Phase != wantPhase {
			t.Fatalf("%s phase = %q, want %q", name, srv.Status.Phase, wantPhase)
		}
	}
}
