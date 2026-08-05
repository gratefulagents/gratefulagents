/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDefaultSecurityWorkflowShape(t *testing.T) {
	workflow := DefaultSecurityWorkflow()

	required := []string{
		"attack-surface-mapping",
		"authn-authz",
		"injection-and-input-handling",
		"secrets-and-credentials",
		"crypto-and-randomness",
		"ssrf-and-network",
		"deserialization-and-parsing",
		"access-control-and-multitenancy",
		"dependency-and-supply-chain",
		"infrastructure-and-configuration",
		"business-logic",
		"triage-and-report",
	}
	byName := map[string]SecurityScanTask{}
	for _, task := range workflow {
		if _, dup := byName[task.Name]; dup {
			t.Fatalf("duplicate task name %q", task.Name)
		}
		if task.Objective == "" {
			t.Fatalf("task %q has empty objective", task.Name)
		}
		if task.EffectiveRole() == "" {
			t.Fatalf("task %q has no effective role", task.Name)
		}
		byName[task.Name] = task
	}
	for _, task := range workflow {
		for _, dependency := range task.DependsOn {
			if _, ok := byName[dependency]; !ok {
				t.Fatalf("task %q depends on unknown task %q", task.Name, dependency)
			}
		}
	}
	for _, name := range required {
		if _, ok := byName[name]; !ok {
			t.Fatalf("DefaultSecurityWorkflow() missing task %q", name)
		}
	}

	triage := workflow[len(workflow)-1]
	if triage.Name != "triage-and-report" {
		t.Fatalf("last task = %q, want triage-and-report", triage.Name)
	}
	if len(triage.DependsOn) != len(workflow)-1 {
		t.Fatalf("triage DependsOn len = %d, want %d", len(triage.DependsOn), len(workflow)-1)
	}
	deps := map[string]bool{}
	for _, dep := range triage.DependsOn {
		deps[dep] = true
	}
	for _, task := range workflow[:len(workflow)-1] {
		if !deps[task.Name] {
			t.Fatalf("triage DependsOn missing %q", task.Name)
		}
	}
}

func TestSecurityScanSpecEffectiveWorkflow(t *testing.T) {
	var spec SecurityScanSpec
	if got, want := len(spec.EffectiveWorkflow()), len(DefaultSecurityWorkflow()); got != want {
		t.Fatalf("EffectiveWorkflow() len = %d, want default workflow len %d", got, want)
	}

	spec.Workflow = []SecurityScanTask{{Name: "only", Objective: "check one thing"}}
	if got := spec.EffectiveWorkflow(); len(got) != 1 || got[0].Name != "only" {
		t.Fatalf("EffectiveWorkflow() = %v, want the configured workflow", got)
	}
}

func TestSecurityScanSpecEffectiveDefaults(t *testing.T) {
	var spec SecurityScanSpec
	if got := spec.EffectiveBaseBranch(); got != "main" {
		t.Fatalf("EffectiveBaseBranch() = %q, want main", got)
	}
	if got := spec.EffectiveMinSeverity(); got != "low" {
		t.Fatalf("EffectiveMinSeverity() = %q, want low", got)
	}
	spec.BaseBranch = "develop"
	spec.MinSeverity = "high"
	if got := spec.EffectiveBaseBranch(); got != "develop" {
		t.Fatalf("EffectiveBaseBranch() = %q, want develop", got)
	}
	if got := spec.EffectiveMinSeverity(); got != "high" {
		t.Fatalf("EffectiveMinSeverity() = %q, want high", got)
	}
}

func TestSecurityScanSpecPreservesFailOnSeverity(t *testing.T) {
	spec := SecurityScanSpec{FailOnSeverity: "high"}
	if got := spec.FailOnSeverity; got != "high" {
		t.Fatalf("FailOnSeverity = %q, want high", got)
	}
}

func TestSecurityScanSpecEffectiveParallelism(t *testing.T) {
	cases := []struct {
		in   int32
		want int32
	}{
		{0, 4},
		{-3, 1},
		{1, 1},
		{9, 9},
		{16, 16},
		{40, 16},
	}
	for _, tc := range cases {
		spec := SecurityScanSpec{Parallelism: tc.in}
		if got := spec.EffectiveParallelism(); got != tc.want {
			t.Fatalf("EffectiveParallelism(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSecurityScanSpecDedupeDefaults(t *testing.T) {
	var spec SecurityScanSpec
	if !spec.DedupeEnabled() {
		t.Fatal("DedupeEnabled() = false, want true by default")
	}
	if got := spec.DedupeSimilarityThresholdPermille(); got != 820 {
		t.Fatalf("DedupeSimilarityThresholdPermille() = %d, want 820", got)
	}

	disabled := false
	spec.Dedupe = &SecurityScanDedupe{Enabled: &disabled, SimilarityThresholdPermille: 900}
	if spec.DedupeEnabled() {
		t.Fatal("DedupeEnabled() = true, want false")
	}
	if got := spec.DedupeSimilarityThresholdPermille(); got != 900 {
		t.Fatalf("DedupeSimilarityThresholdPermille() = %d, want 900", got)
	}
}

func TestSecurityScanTaskEffectiveRole(t *testing.T) {
	var task SecurityScanTask
	if got := task.EffectiveRole(); got != DefaultSecurityScanRole {
		t.Fatalf("EffectiveRole() = %q, want %q", got, DefaultSecurityScanRole)
	}
	task.Role = "pentester"
	if got := task.EffectiveRole(); got != "pentester" {
		t.Fatalf("EffectiveRole() = %q, want pentester", got)
	}
}

func TestSecurityPostScriptEffectiveRunOn(t *testing.T) {
	var script SecurityPostScript
	if got := script.EffectiveRunOn(); got != "all" {
		t.Fatalf("EffectiveRunOn() = %q, want all", got)
	}
	script.RunOn = "confirmed"
	if got := script.EffectiveRunOn(); got != "confirmed" {
		t.Fatalf("EffectiveRunOn() = %q, want confirmed", got)
	}
}

func TestSecurityScanDeepCopyCoversNewTypes(t *testing.T) {
	enabled := true
	scan := &SecurityScan{
		Spec: SecurityScanSpec{
			RepoURL:         "https://github.com/example/repo.git",
			AdditionalRepos: []string{"https://github.com/example/dep.git"},
			Scope:           &SecurityScanScope{IncludePaths: []string{"internal/"}},
			Workflow:        []SecurityScanTask{{Name: "a", Objective: "x", DependsOn: []string{"b"}}},
			SeverityRankers: []SecurityRanker{{Name: "r", Rules: "min-severity: high"}},
			PostScripts:     []SecurityPostScript{{Name: "p", Prompt: "validate"}},
			Dedupe:          &SecurityScanDedupe{Enabled: &enabled},
			MaxRuntime:      metav1.Duration{Duration: time.Hour},
		},
		Status: SecurityScanStatus{
			Findings: &SecurityScanFindingCounts{Total: 3, Critical: 1},
		},
	}
	clone := scan.DeepCopy()
	clone.Spec.Workflow[0].DependsOn[0] = "changed"
	clone.Status.Findings.Critical = 9
	*clone.Spec.Dedupe.Enabled = false
	if scan.Spec.Workflow[0].DependsOn[0] != "b" {
		t.Fatal("DeepCopy() shares workflow dependsOn slice")
	}
	if scan.Status.Findings.Critical != 1 {
		t.Fatal("DeepCopy() shares findings counts")
	}
	if !*scan.Spec.Dedupe.Enabled {
		t.Fatal("DeepCopy() shares dedupe enabled pointer")
	}
}
