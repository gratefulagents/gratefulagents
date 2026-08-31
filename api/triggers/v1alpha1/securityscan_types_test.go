/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
	var script SecurityScanPostScript
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
			Workflow:        []SecurityScanTask{{Name: "a", Objective: "x", DependsOn: []string{"b"}, SkillRefs: []platformv1alpha1.NamedRef{{Name: "security-scan"}}}},
			SeverityRankers: []SecurityScanRanker{{Name: "r", Rules: "min-severity: high"}},
			PostScripts:     []SecurityScanPostScript{{Name: "p", Prompt: "validate"}},
			Dedupe:          &SecurityScanDedupe{Enabled: &enabled},
			MaxRuntime:      metav1.Duration{Duration: time.Hour},
		},
		Status: SecurityScanStatus{
			Findings: &SecurityScanFindingCounts{Total: 3, Critical: 1},
		},
	}
	clone := scan.DeepCopy()
	clone.Spec.Workflow[0].DependsOn[0] = "changed"
	clone.Spec.Workflow[0].SkillRefs[0].Name = "changed"
	clone.Status.Findings.Critical = 9
	*clone.Spec.Dedupe.Enabled = false
	if scan.Spec.Workflow[0].DependsOn[0] != "b" {
		t.Fatal("DeepCopy() shares workflow dependsOn slice")
	}
	if scan.Spec.Workflow[0].SkillRefs[0].Name != "security-scan" {
		t.Fatal("DeepCopy() shares workflow skillRefs slice")
	}
	if scan.Status.Findings.Critical != 1 {
		t.Fatal("DeepCopy() shares findings counts")
	}
	if !*scan.Spec.Dedupe.Enabled {
		t.Fatal("DeepCopy() shares dedupe enabled pointer")
	}
}

func TestSecurityScanSpecEffectiveExecutionMode(t *testing.T) {
	var spec SecurityScanSpec
	if got := spec.EffectiveExecutionMode(); got != SecurityScanExecutionModeDeterministic {
		t.Fatalf("EffectiveExecutionMode() = %q, want deterministic", got)
	}
	spec.Execution = &SecurityScanExecution{}
	if got := spec.EffectiveExecutionMode(); got != SecurityScanExecutionModeDeterministic {
		t.Fatalf("EffectiveExecutionMode() with empty mode = %q, want deterministic", got)
	}
	spec.Execution.Mode = SecurityScanExecutionModeCoordinator
	if got := spec.EffectiveExecutionMode(); got != SecurityScanExecutionModeCoordinator {
		t.Fatalf("EffectiveExecutionMode() = %q, want coordinator", got)
	}
}

func TestSecurityScanSpecEffectiveTaskMaxRetries(t *testing.T) {
	var spec SecurityScanSpec
	var task SecurityScanTask
	if got := spec.EffectiveTaskMaxRetries(task); got != 1 {
		t.Fatalf("EffectiveTaskMaxRetries() default = %d, want 1", got)
	}
	specDefault := int32(4)
	spec.Execution = &SecurityScanExecution{TaskMaxRetries: &specDefault}
	if got := spec.EffectiveTaskMaxRetries(task); got != 4 {
		t.Fatalf("EffectiveTaskMaxRetries() spec default = %d, want 4", got)
	}
	perTask := int32(0)
	task.MaxRetries = &perTask
	if got := spec.EffectiveTaskMaxRetries(task); got != 0 {
		t.Fatalf("EffectiveTaskMaxRetries() task override = %d, want 0", got)
	}
}

func TestSecurityScanSpecEffectiveRetryBackoff(t *testing.T) {
	var spec SecurityScanSpec
	if got := spec.EffectiveRetryBackoff(); got != 30*time.Second {
		t.Fatalf("EffectiveRetryBackoff() default = %v, want 30s", got)
	}
	spec.Execution = &SecurityScanExecution{}
	if got := spec.EffectiveRetryBackoff(); got != 30*time.Second {
		t.Fatalf("EffectiveRetryBackoff() zero backoff = %v, want 30s", got)
	}
	spec.Execution.RetryBackoff = metav1.Duration{Duration: 2 * time.Minute}
	if got := spec.EffectiveRetryBackoff(); got != 2*time.Minute {
		t.Fatalf("EffectiveRetryBackoff() = %v, want 2m", got)
	}
}

func TestSecurityScanTaskEffectiveMaxInstances(t *testing.T) {
	var task SecurityScanTask
	if got := task.EffectiveMaxInstances(); got != 10 {
		t.Fatalf("EffectiveMaxInstances() default = %d, want 10", got)
	}
	task.MaxInstances = 25
	if got := task.EffectiveMaxInstances(); got != 25 {
		t.Fatalf("EffectiveMaxInstances() = %d, want 25", got)
	}
}

func TestSecurityScanTaskEffectiveTargetRuns(t *testing.T) {
	var task SecurityScanTask
	if got := task.EffectiveTargetRuns(); got != 10 {
		t.Fatalf("EffectiveTargetRuns() legacy default = %d, want 10", got)
	}
	task.MaxInstances = 25
	if got := task.EffectiveTargetRuns(); got != 25 {
		t.Fatalf("EffectiveTargetRuns() legacy maxInstances = %d, want 25", got)
	}
	task.TargetRuns = 4
	if got := task.EffectiveTargetRuns(); got != 4 {
		t.Fatalf("EffectiveTargetRuns() targetRuns = %d, want 4", got)
	}
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(data), `"targetRuns":4`) {
		t.Fatalf("Marshal() = %s, want targetRuns field", data)
	}
}

func TestSecurityScanFanOutStatusJSONFields(t *testing.T) {
	execution := SecurityScanExecutionStatus{
		Tasks: []SecurityScanTaskExecutionStatus{{
			Name: "hunt", RecordStart: 2, RecordEnd: 5, InputSHA256: "input-sha",
		}},
		FanOuts: []SecurityScanFanOutExecutionStatus{{
			Name: "hunt", SourceTask: "recon", SourceRunName: "scan-recon-0",
			Strategy: "balanced", SourceOutputSHA256: "source-sha", RecordCount: 8, ChunkCount: 3,
		}},
	}
	data, err := json.Marshal(execution)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	jsonText := string(data)
	for _, field := range []string{
		`"recordStart":2`, `"recordEnd":5`, `"inputSHA256":"input-sha"`,
		`"fanOuts"`, `"sourceTask":"recon"`, `"sourceRunName":"scan-recon-0"`,
		`"strategy":"balanced"`, `"sourceOutputSHA256":"source-sha"`,
		`"recordCount":8`, `"chunkCount":3`,
	} {
		if !strings.Contains(jsonText, field) {
			t.Errorf("Marshal() = %s, want field %s", jsonText, field)
		}
	}
}

func TestSecurityScanTaskEffectiveRepeats(t *testing.T) {
	for _, tc := range []struct{ repeats, want int32 }{{0, 1}, {1, 1}, {3, 3}, {5, 5}} {
		task := SecurityScanTask{Repeats: tc.repeats}
		if got := task.EffectiveRepeats(); got != tc.want {
			t.Fatalf("EffectiveRepeats() with repeats=%d = %d, want %d", tc.repeats, got, tc.want)
		}
	}
}

func TestSecurityScanExecutionDerivedEvidenceOutcome(t *testing.T) {
	for _, tc := range []struct {
		name string
		exec SecurityScanExecutionStatus
		want SecurityScanEvidenceOutcome
	}{
		{name: "running", exec: SecurityScanExecutionStatus{Phase: SecurityScanExecutionPhaseRunning}},
		{name: "complete", exec: SecurityScanExecutionStatus{Phase: SecurityScanExecutionPhaseSucceeded}, want: SecurityScanEvidenceOutcomeComplete},
		{name: "partial", exec: SecurityScanExecutionStatus{Phase: SecurityScanExecutionPhaseSucceeded, CoverageGaps: []string{"tests unavailable"}}, want: SecurityScanEvidenceOutcomePartial},
		{name: "readiness blocked", exec: SecurityScanExecutionStatus{Phase: SecurityScanExecutionPhaseSucceeded, CoverageGaps: []string{"runtime readiness gate preflight did not pass"}}, want: SecurityScanEvidenceOutcomeBlocked},
		{name: "cancelled", exec: SecurityScanExecutionStatus{Phase: SecurityScanExecutionPhaseCancelled}, want: SecurityScanEvidenceOutcomeBlocked},
		{name: "failed", exec: SecurityScanExecutionStatus{Phase: SecurityScanExecutionPhaseFailed}, want: SecurityScanEvidenceOutcomeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.exec.DerivedEvidenceOutcome(); got != tc.want {
				t.Fatalf("DerivedEvidenceOutcome() = %q, want %q", got, tc.want)
			}
		})
	}
}
