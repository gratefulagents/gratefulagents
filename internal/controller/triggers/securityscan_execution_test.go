package triggers

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSecurityScanOmittedExecutionModeUsesDeterministicScheduler(t *testing.T) {
	for _, tc := range []struct {
		name      string
		execution *triggersv1alpha1.SecurityScanExecution
	}{
		{name: "nil execution"},
		{name: "empty mode", execution: &triggersv1alpha1.SecurityScanExecution{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{
				Name:      "inspect",
				Objective: "inspect the repository",
			}}, 1)
			scan.Spec.Execution = tc.execution
			reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

			reconcileDeterministicSecurityScan(t, reconciler, scan)

			updated := getSecurityScan(t, k8sClient, scan)
			if updated.Status.LastExecution == nil || updated.Status.LastExecution.Mode != triggersv1alpha1.SecurityScanExecutionModeDeterministic {
				t.Fatalf("LastExecution = %#v, want deterministic execution", updated.Status.LastExecution)
			}
			runs := securityScanRuns(t, k8sClient, scan.Namespace)
			if len(runs) != 1 || runs[0].Labels[securityScanTaskLabel] != "inspect" {
				t.Fatalf("runs = %#v, want one deterministic task run", runs)
			}
		})
	}
}

func TestSecurityScanDeterministicExecutionSchedulesDependenciesWithinParallelismBound(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "a", Objective: "inspect a"},
		{Name: "b", Objective: "inspect b"},
		{Name: "c", Objective: "join results", DependsOn: []string{"a", "b"}},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	updated := getSecurityScan(t, k8sClient, scan)
	exec := updated.Status.LastExecution
	if exec == nil || exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning || exec.EffectiveParallelism != 1 {
		t.Fatalf("LastExecution = %#v, want running execution with parallelism 1", exec)
	}
	assertExecutionTaskState(t, exec, "a", 0, triggersv1alpha1.SecurityScanTaskStateRunning)
	assertExecutionTaskState(t, exec, "b", 0, triggersv1alpha1.SecurityScanTaskStatePending)
	assertExecutionTaskState(t, exec, "c", 0, triggersv1alpha1.SecurityScanTaskStateBlocked)
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 || runs[0].Labels[securityScanTaskLabel] != "a" {
		t.Fatalf("first runs = %#v, want only task a", runs)
	}

	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, runs[0].Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	updated = getSecurityScan(t, k8sClient, scan)
	assertExecutionTaskState(t, updated.Status.LastExecution, "a", 0, triggersv1alpha1.SecurityScanTaskStateSucceeded)
	assertExecutionTaskState(t, updated.Status.LastExecution, "b", 0, triggersv1alpha1.SecurityScanTaskStateRunning)
	assertExecutionTaskState(t, updated.Status.LastExecution, "c", 0, triggersv1alpha1.SecurityScanTaskStateBlocked)
	runs = securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 2 || taskRunByTask(t, runs, "b").Name == "" {
		t.Fatalf("runs after a succeeds = %#v, want task b only as second run", runs)
	}

	bRun := taskRunByTask(t, runs, "b")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, bRun.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	updated = getSecurityScan(t, k8sClient, scan)
	assertExecutionTaskState(t, updated.Status.LastExecution, "c", 0, triggersv1alpha1.SecurityScanTaskStateRunning)
	cRun := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "c")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, cRun.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	updated = getSecurityScan(t, k8sClient, scan)
	exec = updated.Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseSucceeded || exec.CompletedAt == nil {
		t.Fatalf("execution = %#v, want succeeded execution with completion time", exec)
	}
	for _, task := range []string{"a", "b", "c"} {
		assertExecutionTaskState(t, exec, task, 0, triggersv1alpha1.SecurityScanTaskStateSucceeded)
	}
}

func TestSecurityScanDeterministicTaskRunAppliesTaskConfiguration(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{
		Name:         "focused",
		Objective:    "inspect the focused surface",
		Timeout:      metav1.Duration{Duration: 15 * time.Minute},
		MaxTurns:     7,
		MaxCostUSD:   "1.25",
		MaxFindings:  3,
		OutputSchema: `{"type":"object"}`,
		Tools:        &triggersv1alpha1.SecurityScanTaskTools{Allowed: []string{"read_file"}, Denied: []string{"Bash"}},
	}}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	run := runs[0]
	if run.Labels[securityScanLabel] != securityScanLabelValue(scan.Name) || run.Labels[securityScanTaskLabel] != "focused" {
		t.Fatalf("Labels = %#v, want scan and task labels", run.Labels)
	}
	if len(run.OwnerReferences) != 1 || run.OwnerReferences[0].Name != scan.Name {
		t.Fatalf("OwnerReferences = %#v, want SecurityScan owner", run.OwnerReferences)
	}
	if run.Spec.ModeRef == nil || run.Spec.ModeRef.Name != securityScanTaskModeTemplate {
		t.Fatalf("ModeRef = %#v, want %q", run.Spec.ModeRef, securityScanTaskModeTemplate)
	}
	if run.Spec.Limits == nil || run.Spec.Limits.MaxRuntime.Duration != 15*time.Minute || run.Spec.Limits.MaxTurns != 7 || run.Spec.Limits.MaxCostUsd != "1.25" {
		t.Fatalf("Limits = %#v, want timeout, turns, and cost task limits", run.Spec.Limits)
	}
	// The allow-list keeps the user's tools and auto-appends the platform
	// contract tools (the task declares an outputSchema and is the DAG's
	// sink, so submit_task_output and submit_security_scan_report are due).
	wantAllowed := "read_file,report_security_finding,update_security_finding,submit_task_output,submit_security_scan_report"
	// The default role is read-only, so the repository- and forge-mutating
	// tools are denied on top of the task's own denials.
	wantDenied := append([]string{"Bash"}, securityScanRoleWriteTools...)
	if run.Spec.ToolPolicy == nil || strings.Join(run.Spec.ToolPolicy.AllowedTools, ",") != wantAllowed || strings.Join(run.Spec.ToolPolicy.DeniedTools, ",") != strings.Join(wantDenied, ",") {
		t.Fatalf("ToolPolicy = %#v, want allowed %q and denied %q", run.Spec.ToolPolicy, wantAllowed, strings.Join(wantDenied, ","))
	}
	// The scan declares no budgets, so only the task's own cap is stamped —
	// it must never masquerade as the scan-wide budget.
	if got, ok := run.Annotations[triggersv1alpha1.SecurityScanMaxFindingsAnnotation]; ok {
		t.Fatalf("scan max-findings annotation = %q, want unset without scan budgets", got)
	}
	if run.Annotations[triggersv1alpha1.SecurityScanTaskMaxFindingsAnnotation] != "3" {
		t.Fatalf("task max-findings annotation = %q, want 3", run.Annotations[triggersv1alpha1.SecurityScanTaskMaxFindingsAnnotation])
	}
	if run.Annotations[securityScanTaskOutputSchemaAnnotation] != `{"type":"object"}` {
		t.Fatalf("output-schema annotation = %q, want task schema", run.Annotations[securityScanTaskOutputSchemaAnnotation])
	}
}

func TestSecurityScanDeterministicExecutionRetriesRetryableFailuresUntilBudgetIsExhausted(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := now
	one := int32(1)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "a", Objective: "inspect", MaxRetries: &one}}, 1)
	scan.Spec.Execution.RetryBackoff = metav1.Duration{Duration: 5 * time.Second}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconciler.Now = func() time.Time { return clock }

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	first := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "a")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, first.Name, platformv1alpha1.AgentRunPhaseFailed, "", "temporary connection timeout")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	entry := executionTask(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "a", 0)
	if entry.State != triggersv1alpha1.SecurityScanTaskStatePending || entry.NextRetryTime == nil || !entry.NextRetryTime.Time.Equal(now.Add(5*time.Second)) {
		t.Fatalf("task after retryable failure = %#v, want pending retry at %s", entry, now.Add(5*time.Second))
	}
	if len(entry.Retries) != 1 || entry.Retries[0].Class != triggersv1alpha1.SecurityScanTaskFailureRetryable {
		t.Fatalf("retry history = %#v, want one retryable attempt", entry.Retries)
	}

	clock = clock.Add(5 * time.Second)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	second := taskRunByName(t, securityScanRuns(t, k8sClient, scan.Namespace), executionTask(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "a", 0).RunName)
	if second.Name == first.Name || !strings.Contains(second.Name, "-r2") {
		t.Fatalf("retry run name = %q, want distinct -r2 name from %q", second.Name, first.Name)
	}
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, second.Name, platformv1alpha1.AgentRunPhaseFailed, "", "temporary connection timeout")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	entry = executionTask(t, exec, "a", 0)
	if entry.State != triggersv1alpha1.SecurityScanTaskStateFailed || len(entry.Retries) != 2 {
		t.Fatalf("task after exhausted retry budget = %#v, want failed after two attempts", entry)
	}
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		t.Fatalf("execution phase = %q, want Failed", exec.Phase)
	}
}

func TestSecurityScanRetryBackoffDoublesAndCaps(t *testing.T) {
	base := 2 * time.Second
	cases := []struct {
		attempt int32
		want    time.Duration
	}{
		{attempt: 1, want: 2 * time.Second},
		{attempt: 2, want: 4 * time.Second},
		{attempt: 3, want: 8 * time.Second},
		{attempt: 20, want: securityScanTaskRetryBackoffCap},
	}
	for _, tc := range cases {
		t.Run("attempt", func(t *testing.T) {
			if got := securityScanRetryBackoff(base, tc.attempt); got != tc.want {
				t.Fatalf("securityScanRetryBackoff(%s, %d) = %s, want %s", base, tc.attempt, got, tc.want)
			}
		})
	}
}

func TestClassifySecurityScanTaskFailureClassifiesTransientAndPermanentReasons(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{reason: "request timed out", want: triggersv1alpha1.SecurityScanTaskFailureRetryable},
		{reason: "rate limit 429", want: triggersv1alpha1.SecurityScanTaskFailureRetryable},
		{reason: "service unavailable", want: triggersv1alpha1.SecurityScanTaskFailureRetryable},
		{reason: "unauthorized credential", want: triggersv1alpha1.SecurityScanTaskFailureNonRetryable},
		{reason: "forbidden by policy", want: triggersv1alpha1.SecurityScanTaskFailureNonRetryable},
		{reason: "invalid output", want: triggersv1alpha1.SecurityScanTaskFailureNonRetryable},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if got := classifySecurityScanTaskFailure(tc.reason); got != tc.want {
				t.Fatalf("classifySecurityScanTaskFailure(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

func TestSecurityScanDeterministicFailureSkipsTransitiveDependents(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	zero := int32(0)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "inspect", MaxRetries: &zero},
		{Name: "middle", Objective: "use source", DependsOn: []string{"source"}},
		{Name: "leaf", Objective: "use middle", DependsOn: []string{"middle"}},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	run := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, run.Name, platformv1alpha1.AgentRunPhaseFailed, "", "unauthorized")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	updated := getSecurityScan(t, k8sClient, scan)
	exec := updated.Status.LastExecution
	assertExecutionTaskState(t, exec, "source", 0, triggersv1alpha1.SecurityScanTaskStateFailed)
	assertExecutionTaskState(t, exec, "middle", 0, triggersv1alpha1.SecurityScanTaskStateSkipped)
	assertExecutionTaskState(t, exec, "leaf", 0, triggersv1alpha1.SecurityScanTaskStateSkipped)
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		t.Fatalf("execution phase = %q, want Failed", exec.Phase)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "ExecutionFailed")
}

func TestSecurityScanDeterministicResumeRestartsFailedAndSkippedTasks(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	zero := int32(0)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "inspect", MaxRetries: &zero},
		{Name: "join", Objective: "join", DependsOn: []string{"source"}},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	first := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, first.Name, platformv1alpha1.AgentRunPhaseFailed, "", "unauthorized")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	updated := getSecurityScan(t, k8sClient, scan)
	updated.Annotations = map[string]string{triggersv1alpha1.SecurityScanResumeAnnotation: "resume-1"}
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(SecurityScan resume annotation): %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	updated = getSecurityScan(t, k8sClient, scan)
	exec := updated.Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning || exec.LastResumeToken != "resume-1" {
		t.Fatalf("resumed execution = %#v, want running execution that consumed token", exec)
	}
	assertExecutionTaskState(t, exec, "source", 0, triggersv1alpha1.SecurityScanTaskStatePending)
	assertExecutionTaskState(t, exec, "join", 0, triggersv1alpha1.SecurityScanTaskStatePending)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	resumed := taskRunByName(t, securityScanRuns(t, k8sClient, scan.Namespace), executionTask(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "source", 0).RunName)
	if resumed.Name == first.Name || !strings.Contains(resumed.Name, "-z") {
		t.Fatalf("resumed run = %q, want distinct resume-token name from %q", resumed.Name, first.Name)
	}
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, resumed.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	join := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "join")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, join.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if phase := getSecurityScan(t, k8sClient, scan).Status.LastExecution.Phase; phase != triggersv1alpha1.SecurityScanExecutionPhaseSucceeded {
		t.Fatalf("resumed execution phase = %q, want Succeeded", phase)
	}
}

func TestSecurityScanDeterministicResumeConsumesTokenWithoutChangingLiveExecution(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "a", Objective: "inspect"}}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	updated := getSecurityScan(t, k8sClient, scan)
	updated.Annotations = map[string]string{triggersv1alpha1.SecurityScanResumeAnnotation: "resume-live"}
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(SecurityScan resume annotation): %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	updated = getSecurityScan(t, k8sClient, scan)
	exec := updated.Status.LastExecution
	if exec.LastResumeToken != "resume-live" || exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning {
		t.Fatalf("live execution after resume token = %#v, want token consumed without phase change", exec)
	}
	assertExecutionTaskState(t, exec, "a", 0, triggersv1alpha1.SecurityScanTaskStateRunning)
}

func TestSecurityScanDeterministicExecutionExpandsFanOutAndRendersOutputs(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "produce targets", OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect {{item.field}}", DependsOn: []string{"source"}, ForEach: "source", OutputSchema: `{"type":"object"}`},
		{Name: "join", Objective: "combine {{tasks.fan.output}}", DependsOn: []string{"fan"}},
	}, 2)
	reconciler, k8sClient, stateStore := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	source := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, source.Name, platformv1alpha1.AgentRunPhaseSucceeded, `[{"field":"first"},{"field":"second"}]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	fanRuns := taskRunsByTask(securityScanRuns(t, k8sClient, scan.Namespace), "fan")
	if len(fanRuns) != 2 {
		t.Fatalf("fan runs = %d, want 2", len(fanRuns))
	}
	seeds := []string{
		securityScanSeedMessage(t, stateStore, scan.Namespace, fanRuns[0].Name),
		securityScanSeedMessage(t, stateStore, scan.Namespace, fanRuns[1].Name),
	}
	orderedMatch := strings.Contains(seeds[0], "inspect first") && strings.Contains(seeds[1], "inspect second")
	reversedMatch := strings.Contains(seeds[0], "inspect second") && strings.Contains(seeds[1], "inspect first")
	if !orderedMatch && !reversedMatch {
		t.Fatalf("fan-out seed messages = %#v, want each rendered item field", seeds)
	}
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, fanRuns[0].Name, platformv1alpha1.AgentRunPhaseSucceeded, `{"result":"first"}`, "")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, fanRuns[1].Name, platformv1alpha1.AgentRunPhaseSucceeded, `{"result":"second"}`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	join := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "join")
	joinSeed := securityScanSeedMessage(t, stateStore, scan.Namespace, join.Name)
	if !strings.Contains(joinSeed, `combine [{"result":"first"},{"result":"second"}]`) {
		t.Fatalf("join seed = %q, want aggregate fan-out outputs", joinSeed)
	}
}

func TestSecurityScanDeterministicExecutionFailsFanOutForNonArrayOutput(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		// A schema without "type" passes validation (it may be loose), so
		// the non-array output is only caught at expansion time.
		{Name: "source", Objective: "produce target", OutputSchema: `{"properties":{"field":{"type":"string"}}}`},
		{Name: "fan", Objective: "inspect {{item.field}}", DependsOn: []string{"source"}, ForEach: "source"},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	source := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, source.Name, platformv1alpha1.AgentRunPhaseSucceeded, `{"field":"one"}`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	fan := executionTask(t, exec, "fan", 0)
	if fan.State != triggersv1alpha1.SecurityScanTaskStateFailed || !strings.Contains(fan.LastError, "not a JSON array") {
		t.Fatalf("fan task = %#v, want non-retryable non-array failure", fan)
	}
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		t.Fatalf("execution phase = %q, want Failed", exec.Phase)
	}
}

func TestSecurityScanDeterministicExecutionLimitsFanOutInstances(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "produce targets", OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect {{item.field}}", DependsOn: []string{"source"}, ForEach: "source", MaxInstances: 1},
	}, 2)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	source := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, source.Name, platformv1alpha1.AgentRunPhaseSucceeded, `[{"field":"one"},{"field":"two"}]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	if got := len(taskRunsByTask(securityScanRuns(t, k8sClient, scan.Namespace), "fan")); got != 1 {
		t.Fatalf("fan runs = %d, want maxInstances cap of 1", got)
	}
}

func TestSecurityScanDeterministicExecutionFailsTaskWithoutRequiredStructuredOutput(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "source", Objective: "produce output", OutputSchema: `{"type":"object"}`}}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	run := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, run.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	entry := executionTask(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "source", 0)
	if entry.State != triggersv1alpha1.SecurityScanTaskStateFailed || len(entry.Retries) != 1 || entry.Retries[0].Class != triggersv1alpha1.SecurityScanTaskFailureNonRetryable {
		t.Fatalf("output-contract task = %#v, want immediate non-retryable failure", entry)
	}
}

func TestSecurityScanDeterministicExecutionRejectsMissingRequiredWorkflowParameter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan(nil, 1)
	scan.Spec.WorkflowRef = &triggersv1alpha1.SecurityResourceRef{Name: "parameterized"}
	workflow := parameterizedSecurityWorkflow(scan.Namespace)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan, workflow)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	updated := getSecurityScan(t, k8sClient, scan)
	if !strings.Contains(updated.Status.LastError, "missing required workflow parameter values: required") {
		t.Fatalf("LastError = %q, want missing required parameter message", updated.Status.LastError)
	}
	if got := len(securityScanRuns(t, k8sClient, scan.Namespace)); got != 0 {
		t.Fatalf("AgentRuns = %d, want none for rejected parameters", got)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, securityScanReasonInvalidSpec)
}

func TestSecurityScanWorkflowParametersRenderProvidedValuesAndDefaultsInBothModes(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	workflow := parameterizedSecurityWorkflow("default")

	t.Run("deterministic", func(t *testing.T) {
		scan := deterministicSecurityScan(nil, 1)
		scan.Spec.WorkflowRef = &triggersv1alpha1.SecurityResourceRef{Name: workflow.Name}
		scan.Spec.ParameterValues = map[string]string{"required": "payments"}
		reconciler, k8sClient, stateStore := newDeterministicSecurityScanReconciler(t, now, scan, workflow)
		reconcileDeterministicSecurityScan(t, reconciler, scan)
		run := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "inspect")
		seed := securityScanSeedMessage(t, stateStore, scan.Namespace, run.Name)
		if !strings.Contains(seed, "inspect payments in default-scope") {
			t.Fatalf("deterministic seed = %q, want provided and default parameters", seed)
		}
	})

	t.Run("coordinator", func(t *testing.T) {
		scan := securityScanTestScan()
		scan.Spec.WorkflowRef = &triggersv1alpha1.SecurityResourceRef{Name: workflow.Name}
		scan.Spec.ParameterValues = map[string]string{"required": "payments"}
		reconciler, k8sClient, stateStore := newDeterministicSecurityScanReconciler(t, now, scan, workflow)
		if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		runs := securityScanRuns(t, k8sClient, scan.Namespace)
		if len(runs) != 1 {
			t.Fatalf("AgentRuns = %d, want 1", len(runs))
		}
		seed := securityScanSeedMessage(t, stateStore, scan.Namespace, runs[0].Name)
		if !strings.Contains(seed, "inspect payments in default-scope") {
			t.Fatalf("coordinator seed = %q, want provided and default parameters", seed)
		}
	})
}

func TestSecurityScanDeterministicExecutionFailsWhenModelJobBudgetPreventsNextTask(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "a", Objective: "inspect a"},
		{Name: "b", Objective: "inspect b"},
	}, 1)
	scan.Spec.Budgets = &triggersv1alpha1.SecurityScanBudgets{MaxModelJobs: 1}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	a := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "a")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, a.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed || !strings.Contains(executionTask(t, exec, "b", 0).LastError, securityScanReasonBudgetExceeded) {
		t.Fatalf("execution after job-budget exhaustion = %#v, want budget failure", exec)
	}
	if got := len(securityScanRuns(t, k8sClient, scan.Namespace)); got != 1 {
		t.Fatalf("AgentRuns = %d, want no task run past model-job budget", got)
	}
}

func TestSecurityScanDeterministicExecutionConsumesManualRequestWhenLiveExecutionForbidsConcurrency(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "a", Objective: "inspect"}}, 1)
	scan.Annotations = map[string]string{triggersv1alpha1.SecurityScanRunNowAnnotation: "first"}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	updated := getSecurityScan(t, k8sClient, scan)
	updated.Annotations[triggersv1alpha1.SecurityScanRunNowAnnotation] = "second"
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(SecurityScan manual token): %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	updated = getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastManualRunToken != "second" {
		t.Fatalf("LastManualRunToken = %q, want consumed second token", updated.Status.LastManualRunToken)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "ConcurrencyBlocked")
	if got := len(securityScanRuns(t, k8sClient, scan.Namespace)); got != 1 {
		t.Fatalf("AgentRuns = %d, want one live task run", got)
	}
}

func TestSecurityScanDeterministicExecutionPublishesCheckAndNotificationOnce(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "report", Objective: "report"}}, 1)
	scan.Spec.Revision = "abc123"
	scan.Spec.Triggers = &triggersv1alpha1.SecurityScanTriggers{RepositoryRef: &triggersv1alpha1.SecurityResourceRef{Name: "widget-repo"}}
	scan.Spec.Checks = &triggersv1alpha1.SecurityScanChecks{Enabled: true}
	scan.Spec.Notifications = []triggersv1alpha1.SecurityScanNotificationRule{slackRule("alerts")}
	repo := securityScanEventTestRepo(scan.Namespace)
	webhookSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "slack-webhook", Namespace: scan.Namespace},
		Data:       map[string][]byte{"url": []byte("https://hooks.example.test/security")},
	}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan, repo, webhookSecret)
	publisher := &fakeSecurityCheckPublisher{}
	notifier := &fakeSecurityScanNotifier{}
	findings := &executionSideEffectFindingStore{notifyTestFindingStore: newNotifyTestFindingStore(notifyTestFinding("fp", "critical", store.SecurityFindingBaselineNew))}
	reconciler.CheckPublisher = publisher
	reconciler.Notifier = notifier
	reconciler.Findings = findings

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	run := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "report")
	if len(publisher.checks) != 0 || len(notifier.slackTexts) != 0 {
		t.Fatalf("side effects before completion: checks=%d notifications=%d, want none", len(publisher.checks), len(notifier.slackTexts))
	}
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, run.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if len(publisher.checks) != 0 || len(notifier.slackTexts) != 0 {
		t.Fatalf("side effects on task completion reconcile: checks=%d notifications=%d, want deferred terminal effects", len(publisher.checks), len(notifier.slackTexts))
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if len(publisher.checks) != 1 || len(notifier.slackTexts) != 1 {
		t.Fatalf("terminal side effects: checks=%d notifications=%d, want one each", len(publisher.checks), len(notifier.slackTexts))
	}
}

func TestRenderSecurityScanTaskObjectiveResolvesSupportedReferences(t *testing.T) {
	tctx := &securityScanTaskTemplateContext{
		params: map[string]string{"scope": "payments"},
		item:   []byte(`{"name":"checkout","nested":{"ok":true}}`),
		output: func(name string) (string, error) {
			if name != "source" {
				t.Fatalf("output requested for %q, want source", name)
			}
			return `{"name":"upstream","count":2}`, nil
		},
	}
	got, err := renderSecurityScanTaskObjective("{{params.scope}} {{tasks.source.output}} {{tasks.source.output.name}} {{item}} {{item.name}} {{item.nested}}", tctx)
	if err != nil {
		t.Fatalf("renderSecurityScanTaskObjective() error = %v", err)
	}
	want := `payments {"name":"upstream","count":2} upstream {"name":"checkout","nested":{"ok":true}} checkout {"ok":true}`
	if got != want {
		t.Fatalf("rendered objective = %q, want %q", got, want)
	}
}

func TestResolveSecurityScanTemplateRefRejectsUnknownParameterAndUnavailableItem(t *testing.T) {
	tctx := &securityScanTaskTemplateContext{params: map[string]string{}, output: func(name string) (string, error) {
		if name == "unknown" {
			return "", fmt.Errorf("unknown task %q", name)
		}
		return "{}", nil
	}}
	if _, _, err := resolveSecurityScanTemplateRef("params.missing", tctx); err == nil || !strings.Contains(err.Error(), "has no value") {
		t.Fatalf("missing parameter error = %v, want value error", err)
	}
	if _, _, err := resolveSecurityScanTemplateRef("item.name", tctx); err == nil || !strings.Contains(err.Error(), "only available") {
		t.Fatalf("unavailable item error = %v, want context error", err)
	}
	if _, _, err := resolveSecurityScanTemplateRef("tasks.unknown.output", tctx); err == nil || !strings.Contains(err.Error(), "unknown task") {
		t.Fatalf("unknown task reference error = %v, want unknown task error", err)
	}
	if got, ok, err := resolveSecurityScanTemplateRef("unknown.ref", tctx); err != nil || ok || got != "" {
		t.Fatalf("unknown ref = (%q, %t, %v), want unresolved passthrough", got, ok, err)
	}
}

func TestSecurityScanTaskRunNameIsUniqueAndFitsObjectNameLimit(t *testing.T) {
	first := securityScanTaskRunName("scan", "execution", "task", 1, 0, "")
	retry := securityScanTaskRunName("scan", "execution", "task", 2, 0, "")
	instance := securityScanTaskRunName("scan", "execution", "task", 1, 1, "")
	resumed := securityScanTaskRunName("scan", "execution", "task", 1, 0, "resume")
	seen := map[string]bool{}
	for _, name := range []string{first, retry, instance, resumed} {
		if seen[name] {
			t.Fatalf("duplicate task run name %q", name)
		}
		seen[name] = true
	}
	long := securityScanTaskRunName(strings.Repeat("scan", 20), strings.Repeat("execution", 20), strings.Repeat("task", 20), 2, 1, "resume")
	if len(long) > 63 {
		t.Fatalf("long task run name length = %d, want <= 63: %q", len(long), long)
	}
	if long == securityScanTaskRunName(strings.Repeat("scan", 20), strings.Repeat("execution", 20), strings.Repeat("other", 20), 2, 1, "resume") {
		t.Fatal("truncated task run names must retain a uniqueness hash")
	}
}

func TestPlanSecurityScanExecutionExpandsRepeatsAndDefersFanOut(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	exec := planSecurityScanExecution([]triggersv1alpha1.SecurityScanTask{
		{Name: "repeat", Objective: "repeat", Repeats: 2},
		{Name: "fan", Objective: "fan", ForEach: "repeat", DependsOn: []string{"repeat"}},
	}, "manual-1", 3, now)
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning || exec.EffectiveParallelism != 3 || exec.StartedAt == nil {
		t.Fatalf("planned execution = %#v, want running plan with metadata", exec)
	}
	if len(exec.Tasks) != 3 {
		t.Fatalf("planned task entries = %#v, want two repeats and one fan-out placeholder", exec.Tasks)
	}
	assertExecutionTaskState(t, exec, "repeat", 0, triggersv1alpha1.SecurityScanTaskStatePending)
	assertExecutionTaskState(t, exec, "repeat", 1, triggersv1alpha1.SecurityScanTaskStatePending)
	assertExecutionTaskState(t, exec, "fan", 0, triggersv1alpha1.SecurityScanTaskStatePending)
	// The plan records the workflow's graph shape once per task (not per
	// instance) so DAG consumers stay truthful after the source workflow —
	// referenced, inline, or the built-in default — is edited.
	if len(exec.Plan) != 2 {
		t.Fatalf("plan = %#v, want one node per workflow task", exec.Plan)
	}
	if exec.Plan[0].Name != "repeat" || len(exec.Plan[0].DependsOn) != 0 || exec.Plan[0].ForEach != "" {
		t.Fatalf("plan[0] = %#v, want the repeat root node", exec.Plan[0])
	}
	if exec.Plan[1].Name != "fan" || len(exec.Plan[1].DependsOn) != 1 ||
		exec.Plan[1].DependsOn[0] != "repeat" || exec.Plan[1].ForEach != "repeat" {
		t.Fatalf("plan[1] = %#v, want the fan-out node with its edge and source", exec.Plan[1])
	}
}

// newDeterministicSecurityScanReconciler seeds the cluster-scoped
// RoleInstructions every deterministic dispatch resolves before it creates a
// task run, so these tests exercise the scheduler instead of a missing asset.
func newDeterministicSecurityScanReconciler(t *testing.T, now time.Time, objects ...client.Object) (*SecurityScanReconciler, client.Client, *seedTestStore) {
	t.Helper()
	return newSecurityScanReconciler(t, now, append(securityScanTestRoles(), objects...)...)
}

// securityScanTestRoles mirrors the shipped security RoleInstruction assets:
// each role carries a distinct contract (instructions, tool access, model
// routing, reasoning) so tests can prove the dispatch applies the right one.
func securityScanTestRoles() []client.Object {
	return []client.Object{
		securityScanTestRole(triggersv1alpha1.DefaultSecurityScanRole, platformv1alpha1.RoleInstructionSpec{
			Instructions: "Review code for vulnerabilities.",
			ToolAccess:   "read-only",
		}),
		securityScanTestRole("threat-modeler", platformv1alpha1.RoleInstructionSpec{
			Instructions:     "Model the attack surface and trust boundaries.",
			Description:      "Threat modelling specialist",
			ToolAccess:       "analysis",
			ModelsByProvider: map[string]string{"openai": "gpt-5.4-threat", "anthropic": "claude-threat"},
			ReasoningLevel:   platformv1alpha1.ReasoningHigh,
		}),
		securityScanTestRole("exploit-validator", platformv1alpha1.RoleInstructionSpec{
			Instructions: "Build a proof-of-concept for each candidate vulnerability.",
			Description:  "Exploit validation specialist",
			ToolAccess:   "execution",
		}),
		securityScanTestRole("finding-triager", platformv1alpha1.RoleInstructionSpec{
			Instructions:   "Triage reported findings and rank them.",
			Description:    "Finding triage specialist",
			ToolAccess:     "read-only",
			ReasoningLevel: platformv1alpha1.ReasoningLow,
		}),
	}
}

func securityScanTestRole(name string, spec platformv1alpha1.RoleInstructionSpec) client.Object {
	return &platformv1alpha1.RoleInstruction{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
}

func deterministicSecurityScan(tasks []triggersv1alpha1.SecurityScanTask, parallelism int32) *triggersv1alpha1.SecurityScan {
	scan := securityScanTestScan()
	scan.Generation = 1
	scan.Spec.Workflow = tasks
	scan.Spec.Parallelism = parallelism
	scan.Spec.Execution = &triggersv1alpha1.SecurityScanExecution{Mode: triggersv1alpha1.SecurityScanExecutionModeDeterministic}
	return scan
}

func reconcileDeterministicSecurityScan(t *testing.T, reconciler *SecurityScanReconciler, scan *triggersv1alpha1.SecurityScan) {
	t.Helper()
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

func markSecurityScanTaskRun(t *testing.T, k8sClient client.Client, namespace, name string, phase platformv1alpha1.AgentRunPhase, structuredOutput, lastError string) {
	t.Helper()
	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, run); err != nil {
		t.Fatalf("Get(AgentRun %s): %v", name, err)
	}
	run.Status.Phase = phase
	run.Status.StructuredOutput = structuredOutput
	run.Status.LastError = lastError
	if err := k8sClient.Status().Update(context.Background(), run); err != nil {
		t.Fatalf("Status().Update(AgentRun %s): %v", name, err)
	}
}

func executionTask(t *testing.T, exec *triggersv1alpha1.SecurityScanExecutionStatus, name string, instance int32) triggersv1alpha1.SecurityScanTaskExecutionStatus {
	t.Helper()
	if exec == nil {
		t.Fatal("execution is nil")
	}
	for _, entry := range exec.Tasks {
		if entry.Name == name && entry.Instance == instance {
			return entry
		}
	}
	t.Fatalf("task %s[%d] missing from %#v", name, instance, exec.Tasks)
	return triggersv1alpha1.SecurityScanTaskExecutionStatus{}
}

func assertExecutionTaskState(t *testing.T, exec *triggersv1alpha1.SecurityScanExecutionStatus, name string, instance int32, want string) {
	t.Helper()
	if got := executionTask(t, exec, name, instance).State; got != want {
		t.Fatalf("task %s[%d] state = %q, want %q", name, instance, got, want)
	}
}

func taskRunsByTask(runs []platformv1alpha1.AgentRun, task string) []platformv1alpha1.AgentRun {
	var matches []platformv1alpha1.AgentRun
	for _, run := range runs {
		if run.Labels[securityScanTaskLabel] == task {
			matches = append(matches, run)
		}
	}
	return matches
}

func taskRunByTask(t *testing.T, runs []platformv1alpha1.AgentRun, task string) platformv1alpha1.AgentRun {
	t.Helper()
	matches := taskRunsByTask(runs, task)
	if len(matches) != 1 {
		t.Fatalf("runs for task %q = %#v, want exactly one", task, matches)
	}
	return matches[0]
}

func taskRunByName(t *testing.T, runs []platformv1alpha1.AgentRun, name string) platformv1alpha1.AgentRun {
	t.Helper()
	for _, run := range runs {
		if run.Name == name {
			return run
		}
	}
	t.Fatalf("run %q missing from %#v", name, runs)
	return platformv1alpha1.AgentRun{}
}

func parameterizedSecurityWorkflow(namespace string) *triggersv1alpha1.SecurityWorkflow {
	return &triggersv1alpha1.SecurityWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "parameterized", Namespace: namespace},
		Spec: triggersv1alpha1.SecurityWorkflowSpec{
			Parameters: []triggersv1alpha1.SecurityWorkflowParameter{
				{Name: "required", Required: true},
				{Name: "optional", Default: "default-scope"},
			},
			Tasks: []triggersv1alpha1.SecurityScanTask{{Name: "inspect", Objective: "inspect {{params.required}} in {{params.optional}}"}},
		},
	}
}

type executionSideEffectFindingStore struct {
	*notifyTestFindingStore
}

func (s *executionSideEffectFindingStore) ExpireAcceptedRisks(context.Context, string) (int32, error) {
	return 0, nil
}

func (s *executionSideEffectFindingStore) ExpireSecuritySuppressions(context.Context, string) (int32, error) {
	return 0, nil
}

func (s *executionSideEffectFindingStore) RevokeSecuritySuppressions(context.Context, string, string, []store.SecuritySuppressionRule) (int32, error) {
	return 0, nil
}

func TestSecurityScanDeterministicExecutionFailsWhenWorkflowGainsTaskMidExecution(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "a", Objective: "inspect a"},
		{Name: "c", Objective: "join", DependsOn: []string{"a"}},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	assertExecutionTaskState(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "a", 0, triggersv1alpha1.SecurityScanTaskStateRunning)

	updated := getSecurityScan(t, k8sClient, scan)
	updated.Spec.Workflow = append(updated.Spec.Workflow, triggersv1alpha1.SecurityScanTask{
		Name: "b", Objective: "inspect b", DependsOn: []string{"a"},
	})
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(SecurityScan) error = %v", err)
	}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	final := getSecurityScan(t, k8sClient, scan)
	exec := final.Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		t.Fatalf("execution phase = %q, want Failed after mid-execution workflow drift", exec.Phase)
	}
	const wantMsg = "workflow changed while the execution was in progress; re-run the scan"
	if c := executionTask(t, exec, "c", 0); c.State != triggersv1alpha1.SecurityScanTaskStateSkipped || !strings.Contains(c.LastError, wantMsg) {
		t.Fatalf("blocked task c = %#v, want Skipped with drift error %q", c, wantMsg)
	}
	if !strings.Contains(final.Status.LastError, wantMsg) {
		t.Fatalf("scan LastError = %q, want drift message %q", final.Status.LastError, wantMsg)
	}
}

// failingWorkflowGetClient simulates a transient API failure when resolving
// the referenced SecurityWorkflow.
type failingWorkflowGetClient struct {
	client.Client
	fail bool
}

func (c *failingWorkflowGetClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*triggersv1alpha1.SecurityWorkflow); ok && c.fail {
		return fmt.Errorf("transient: connection refused")
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func TestSecurityScanDeterministicExecutionRequeuesOnTransientRefErrorAndFailsOnMissingRef(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan(nil, 1)
	scan.Spec.Workflow = nil
	scan.Spec.WorkflowRef = &triggersv1alpha1.SecurityResourceRef{Name: "wf"}
	workflow := &triggersv1alpha1.SecurityWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: scan.Namespace},
		Spec: triggersv1alpha1.SecurityWorkflowSpec{Tasks: []triggersv1alpha1.SecurityScanTask{
			{Name: "a", Objective: "inspect a"},
		}},
	}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan, workflow)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	assertExecutionTaskState(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "a", 0, triggersv1alpha1.SecurityScanTaskStateRunning)

	// A transient API error while resolving refs must requeue (Reconcile
	// returns the error) without failing the execution.
	flaky := &failingWorkflowGetClient{Client: k8sClient, fail: true}
	reconciler.Client = flaky
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err == nil || !strings.Contains(err.Error(), "transient") {
		t.Fatalf("Reconcile() error = %v, want the transient resolution error returned for requeue", err)
	}
	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning {
		t.Fatalf("execution phase = %q, want still Running after transient ref error", exec.Phase)
	}

	// A deterministic not-found reference fails the execution.
	flaky.fail = false
	if err := k8sClient.Delete(context.Background(), workflow); err != nil {
		t.Fatalf("Delete(SecurityWorkflow) error = %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		t.Fatalf("execution phase = %q, want Failed after the referenced workflow disappeared", exec.Phase)
	}
}

// staleLastExecutionClient simulates an informer cache that has not observed
// the status.lastExecution write yet: the first Get returning a scan with a
// recorded execution strips it once.
type staleLastExecutionClient struct {
	client.Client
	armed bool
}

func (c *staleLastExecutionClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if scan, ok := obj.(*triggersv1alpha1.SecurityScan); ok && c.armed && scan.Status.LastExecution != nil {
		scan.Status.LastExecution = nil
		c.armed = false
	}
	return nil
}

func TestSecurityScanStartDeterministicExecutionToleratesStaleCacheAfterStatusWrite(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "a", Objective: "inspect a"}}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	stale := &staleLastExecutionClient{Client: reconciler.Client, armed: true}
	reconciler.Client = stale

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if stale.armed {
		t.Fatal("stale Get was never exercised; the test no longer covers the stale-cache path")
	}
	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec == nil || exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning {
		t.Fatalf("LastExecution = %#v, want a running execution despite the stale re-read", exec)
	}
	assertExecutionTaskState(t, exec, "a", 0, triggersv1alpha1.SecurityScanTaskStateRunning)
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want the first task launched off the freshly written execution", len(runs))
	}
}

func TestSecurityScanDeterministicTaskRunAllowListAppendsOnlyDueContractTools(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		// "a" is not a sink (b depends on it) and declares no outputSchema,
		// so only the finding tools are due; report_security_finding is
		// already allowed and must not be duplicated.
		{Name: "a", Objective: "inspect", Tools: &triggersv1alpha1.SecurityScanTaskTools{Allowed: []string{"read_file", "report_security_finding"}}},
		{Name: "b", Objective: "aggregate", DependsOn: []string{"a"}},
	}, 2)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	run := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "a")
	want := "read_file,report_security_finding,update_security_finding"
	if run.Spec.ToolPolicy == nil || strings.Join(run.Spec.ToolPolicy.AllowedTools, ",") != want {
		t.Fatalf("AllowedTools = %#v, want %q", run.Spec.ToolPolicy, want)
	}
}

func TestSecurityScanExpandFanOutsTruncatesToExecutionEntryCeiling(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	source := triggersv1alpha1.SecurityScanTask{Name: "source", Objective: "list", OutputSchema: `{"type":"array"}`}
	fan := triggersv1alpha1.SecurityScanTask{Name: "fan", Objective: "inspect {{item}}", DependsOn: []string{"source"}, ForEach: "source", MaxInstances: 50}
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{source, fan}, 1)
	reconciler, _, _ := newDeterministicSecurityScanReconciler(t, now.Time, scan)

	exec := &triggersv1alpha1.SecurityScanExecutionStatus{
		ID:   "cap",
		Mode: triggersv1alpha1.SecurityScanExecutionModeDeterministic,
	}
	exec.Tasks = append(exec.Tasks, triggersv1alpha1.SecurityScanTaskExecutionStatus{
		Name: "source", State: triggersv1alpha1.SecurityScanTaskStateSucceeded, RunName: "src-run",
	})
	for i := range securityScanExecutionMaxTaskEntries - 4 {
		exec.Tasks = append(exec.Tasks, triggersv1alpha1.SecurityScanTaskExecutionStatus{
			Name: "pad", Instance: int32(i), State: triggersv1alpha1.SecurityScanTaskStateSucceeded,
		})
	}
	exec.Tasks = append(exec.Tasks, triggersv1alpha1.SecurityScanTaskExecutionStatus{
		Name: "fan", State: triggersv1alpha1.SecurityScanTaskStatePending,
	})

	records := make([]string, 10)
	for i := range records {
		records[i] = fmt.Sprintf(`{"n":%d}`, i)
	}
	engine := &securityScanExecutionEngine{
		r:     reconciler,
		scan:  scan,
		exec:  exec,
		now:   now,
		order: []triggersv1alpha1.SecurityScanTask{source, fan},
		tasks: map[string]triggersv1alpha1.SecurityScanTask{"source": source, "fan": fan},
		runs: map[string]*platformv1alpha1.AgentRun{"src-run": {
			Status: platformv1alpha1.AgentRunStatus{
				Phase:            platformv1alpha1.AgentRunPhaseSucceeded,
				StructuredOutput: "[" + strings.Join(records, ",") + "]",
			},
		}},
	}
	engine.expandFanOuts(context.Background())

	// 198 entries before expansion leave a budget of 3 fan instances.
	if got := len(engine.taskEntries("fan")); got != 3 {
		t.Fatalf("fan entries after capped expansion = %d, want 3", got)
	}
	if len(exec.Tasks) != securityScanExecutionMaxTaskEntries {
		t.Fatalf("total execution entries = %d, want the ceiling %d", len(exec.Tasks), securityScanExecutionMaxTaskEntries)
	}
}

func TestRenderSecurityScanTaskObjectiveRejectsOversizedRendering(t *testing.T) {
	big := strings.Repeat("x", securityScanMaxRenderedObjectiveBytes)
	_, err := renderSecurityScanTaskObjective("use {{tasks.big.output}}", &securityScanTaskTemplateContext{
		params: map[string]string{},
		output: func(string) (string, error) { return big, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "rendered objective exceeds 256KiB") {
		t.Fatalf("error = %v, want oversized-rendering rejection", err)
	}
	if out, err := renderSecurityScanTaskObjective("use {{tasks.big.output}}", &securityScanTaskTemplateContext{
		params: map[string]string{},
		output: func(string) (string, error) { return big[:securityScanMaxRenderedObjectiveBytes-4], nil },
	}); err != nil || len(out) != securityScanMaxRenderedObjectiveBytes {
		t.Fatalf("at-limit rendering = (%d bytes, %v), want success at exactly the cap", len(out), err)
	}
}

func TestTruncateSecurityScanErrorCapsAt160Characters(t *testing.T) {
	long := strings.Repeat("e", 500)
	if got := truncateSecurityScanError(long); len(got) != 160 || !strings.HasSuffix(got, "...") {
		t.Fatalf("truncated length = %d (suffix %q), want 160 with ellipsis", len(got), got[len(got)-3:])
	}
	if got := truncateSecurityScanError("short"); got != "short" {
		t.Fatalf("short string = %q, want unchanged", got)
	}
}

func TestSecurityScanDeterministicTaskCostCapsNeverLoosenScanBudgetsAndFindingCapsStaySeparate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "looser", Objective: "inspect widely", MaxCostUSD: "5.00", MaxFindings: 40},
		{Name: "tighter", Objective: "inspect narrowly", MaxCostUSD: "0.50", MaxFindings: 5},
	}, 2)
	scan.Spec.Budgets = &triggersv1alpha1.SecurityScanBudgets{MaxCostUSD: "2.00", MaxFindings: 10}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	looser := taskRunByTask(t, runs, "looser")
	if looser.Spec.Limits == nil || looser.Spec.Limits.MaxCostUsd != "2.00" {
		t.Fatalf("looser task limits = %#v, want scan-wide maxCostUSD 2.00 (task cap must not loosen it)", looser.Spec.Limits)
	}
	// The two finding budgets are separate ceilings: the per-task cap lands on
	// its own annotation unmodified and never rewrites the scan-wide one every
	// task of the execution shares.
	for _, run := range []platformv1alpha1.AgentRun{looser, taskRunByTask(t, runs, "tighter")} {
		if got := run.Annotations[triggersv1alpha1.SecurityScanMaxFindingsAnnotation]; got != "10" {
			t.Fatalf("%s scan max-findings annotation = %q, want scan-wide 10", run.Labels[securityScanTaskLabel], got)
		}
	}
	if got := looser.Annotations[triggersv1alpha1.SecurityScanTaskMaxFindingsAnnotation]; got != "40" {
		t.Fatalf("looser task max-findings annotation = %q, want unmodified 40", got)
	}
	tighter := taskRunByTask(t, runs, "tighter")
	if tighter.Spec.Limits == nil || tighter.Spec.Limits.MaxCostUsd != "0.50" {
		t.Fatalf("tighter task limits = %#v, want narrowed maxCostUSD 0.50", tighter.Spec.Limits)
	}
	if got := tighter.Annotations[triggersv1alpha1.SecurityScanTaskMaxFindingsAnnotation]; got != "5" {
		t.Fatalf("tighter task max-findings annotation = %q, want 5", got)
	}
}

func TestSecurityScanDeterministicResumeKeepsAttemptsForModelJobBudget(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	zero := int32(0)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "a", Objective: "inspect", MaxRetries: &zero}}, 1)
	scan.Spec.Budgets = &triggersv1alpha1.SecurityScanBudgets{MaxModelJobs: 1}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	first := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "a")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, first.Name, platformv1alpha1.AgentRunPhaseFailed, "", "unauthorized")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if phase := getSecurityScan(t, k8sClient, scan).Status.LastExecution.Phase; phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		t.Fatalf("execution phase = %q, want Failed before resume", phase)
	}

	updated := getSecurityScan(t, k8sClient, scan)
	updated.Annotations = map[string]string{triggersv1alpha1.SecurityScanResumeAnnotation: "resume-budget"}
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(SecurityScan resume annotation): %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	entry := executionTask(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "a", 0)
	if entry.Attempts != 1 || entry.ResumeBaselineAttempts != 1 {
		t.Fatalf("resumed task = %#v, want cumulative attempts 1 with resume baseline 1", entry)
	}

	// The resumed cycle would need a second task run, but the durable
	// attempts counter already consumed budgets.maxModelJobs.
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		t.Fatalf("execution phase after resumed budget check = %q, want Failed", exec.Phase)
	}
	entry = executionTask(t, exec, "a", 0)
	if !strings.Contains(entry.LastError, securityScanReasonBudgetExceeded) {
		t.Fatalf("task error = %q, want %s", entry.LastError, securityScanReasonBudgetExceeded)
	}
	if got := len(securityScanRuns(t, k8sClient, scan.Namespace)); got != 1 {
		t.Fatalf("AgentRuns = %d, want no second run past the model-job budget after resume", got)
	}
}

func TestSecurityScanDeterministicResumeRefreshesRetryBudgetPerCycle(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	zero := int32(0)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "a", Objective: "inspect", MaxRetries: &zero}}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	first := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "a")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, first.Name, platformv1alpha1.AgentRunPhaseFailed, "", "temporary connection timeout")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if state := executionTask(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "a", 0).State; state != triggersv1alpha1.SecurityScanTaskStateFailed {
		t.Fatalf("task state = %q, want Failed with maxRetries 0", state)
	}

	updated := getSecurityScan(t, k8sClient, scan)
	updated.Annotations = map[string]string{triggersv1alpha1.SecurityScanResumeAnnotation: "resume-retry"}
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(SecurityScan resume annotation): %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	entry := executionTask(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "a", 0)
	if entry.State != triggersv1alpha1.SecurityScanTaskStateRunning || entry.Attempts != 2 || entry.ResumeBaselineAttempts != 1 {
		t.Fatalf("resumed task = %#v, want a second (cumulative) attempt running", entry)
	}
	second := taskRunByName(t, securityScanRuns(t, k8sClient, scan.Namespace), entry.RunName)
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, second.Name, platformv1alpha1.AgentRunPhaseFailed, "", "temporary connection timeout")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	entry = executionTask(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "a", 0)
	if entry.State != triggersv1alpha1.SecurityScanTaskStateFailed {
		t.Fatalf("task after resumed cycle exhausted its retry budget = %#v, want Failed (maxRetries 0 per cycle)", entry)
	}
}

func TestSecurityScanDeterministicExecutionFailsWhenSpecBecomesInvalidMidExecution(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "a", Objective: "inspect a"},
		{Name: "b", Objective: "join", DependsOn: []string{"a"}},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	assertExecutionTaskState(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "a", 0, triggersv1alpha1.SecurityScanTaskStateRunning)

	updated := getSecurityScan(t, k8sClient, scan)
	updated.Spec.Workflow[1].DependsOn = []string{"missing"}
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(SecurityScan) error = %v", err)
	}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	final := getSecurityScan(t, k8sClient, scan)
	exec := final.Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed || exec.CompletedAt == nil {
		t.Fatalf("execution = %#v, want Failed with completion time instead of a wedged Running execution", exec)
	}
	const wantMsg = "spec became invalid while the execution was running"
	if b := executionTask(t, exec, "b", 0); b.State != triggersv1alpha1.SecurityScanTaskStateSkipped || !strings.Contains(b.LastError, wantMsg) {
		t.Fatalf("blocked task b = %#v, want Skipped with %q", b, wantMsg)
	}
	if !strings.Contains(final.Status.LastError, "spec.workflow is invalid") {
		t.Fatalf("scan LastError = %q, want the invalid-spec message", final.Status.LastError)
	}
	assertSecurityScanCondition(t, final, metav1.ConditionFalse, securityScanReasonInvalidSpec)

	// Repeated reconciles under the still-invalid spec stay terminal.
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if phase := getSecurityScan(t, k8sClient, scan).Status.LastExecution.Phase; phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		t.Fatalf("execution phase after re-reconcile = %q, want Failed", phase)
	}
}

func TestSecurityScanDeterministicTaskRunsStampExecutionIdentityPerExecution(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "a", Objective: "inspect a"},
		{Name: "b", Objective: "inspect b"},
	}, 2)
	scan.Annotations = map[string]string{triggersv1alpha1.SecurityScanRunNowAnnotation: "first"}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	aRun, bRun := taskRunByTask(t, runs, "a"), taskRunByTask(t, runs, "b")
	execID := getSecurityScan(t, k8sClient, scan).Status.LastExecution.ID
	for _, run := range []platformv1alpha1.AgentRun{aRun, bRun} {
		if got := run.Annotations[triggersv1alpha1.SecurityScanExecutionIDAnnotation]; got != execID {
			t.Fatalf("run %s execution-id annotation = %q, want %q", run.Name, got, execID)
		}
	}
	if a, b := aRun.Annotations[triggersv1alpha1.SecurityScanTaskNameAnnotation], bRun.Annotations[triggersv1alpha1.SecurityScanTaskNameAnnotation]; a != "a" || b != "b" {
		t.Fatalf("task-name annotations = %q/%q, want a/b", a, b)
	}

	for _, run := range []platformv1alpha1.AgentRun{aRun, bRun} {
		markSecurityScanTaskRun(t, k8sClient, scan.Namespace, run.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	updated := getSecurityScan(t, k8sClient, scan)
	updated.Annotations[triggersv1alpha1.SecurityScanRunNowAnnotation] = "second"
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(SecurityScan manual token): %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	nextID := getSecurityScan(t, k8sClient, scan).Status.LastExecution.ID
	if nextID == execID {
		t.Fatalf("second execution id = %q, want a new id (findings must never mix across executions)", nextID)
	}
	for _, run := range securityScanRuns(t, k8sClient, scan.Namespace) {
		stamped := run.Annotations[triggersv1alpha1.SecurityScanExecutionIDAnnotation]
		if stamped != execID && stamped != nextID {
			t.Fatalf("run %s execution-id annotation = %q, want one of the two executions", run.Name, stamped)
		}
	}
}

func TestSecurityScanDeterministicTaskRunsApplyDistinctRoleContracts(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "model", Objective: "map the attack surface", Role: "threat-modeler"},
		{Name: "validate", Objective: "prove exploitability", Role: "exploit-validator"},
		{Name: "triage", Objective: "rank findings", Role: "finding-triager"},
	}, 3)
	scan.Spec.Defaults.CustomInstructions = "Scan-level house rules."
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	modelRun := taskRunByTask(t, runs, "model")
	validateRun := taskRunByTask(t, runs, "validate")
	triageRun := taskRunByTask(t, runs, "triage")

	for role, run := range map[string]platformv1alpha1.AgentRun{
		"threat-modeler":    modelRun,
		"exploit-validator": validateRun,
		"finding-triager":   triageRun,
	} {
		if got := run.Annotations[triggersv1alpha1.SecurityScanTaskRoleAnnotation]; got != role {
			t.Fatalf("run %s role annotation = %q, want %q", run.Name, got, role)
		}
		instructions := securityScanRunInstructions(t, k8sClient, scan.Namespace, run)
		if !strings.Contains(instructions, "Scan-level house rules.") {
			t.Fatalf("run %s instructions dropped the scan-level instructions: %q", run.Name, instructions)
		}
		if !strings.Contains(instructions, "## Role: "+role) {
			t.Fatalf("run %s instructions = %q, want a delimited %q section", run.Name, instructions, role)
		}
	}
	if got := securityScanRunInstructions(t, k8sClient, scan.Namespace, modelRun); !strings.Contains(got, "trust boundaries") {
		t.Fatalf("threat-modeler instructions = %q, want its own role prompt", got)
	}
	if got := securityScanRunInstructions(t, k8sClient, scan.Namespace, validateRun); !strings.Contains(got, "proof-of-concept") {
		t.Fatalf("exploit-validator instructions = %q, want its own role prompt", got)
	}

	// threat-modeler routes its own provider model and reasoning level; the
	// execution role keeps the scan's model and full tool access.
	if modelRun.Spec.Model != "gpt-5.4-threat" || modelRun.Spec.ReasoningLevel != platformv1alpha1.ReasoningHigh {
		t.Fatalf("threat-modeler run model/reasoning = %q/%q, want the role's routing", modelRun.Spec.Model, modelRun.Spec.ReasoningLevel)
	}
	if validateRun.Spec.Model != "gpt-5.4" || validateRun.Spec.ToolPolicy != nil {
		t.Fatalf("exploit-validator run model/toolPolicy = %q/%#v, want the scan model and no narrowing", validateRun.Spec.Model, validateRun.Spec.ToolPolicy)
	}
	if triageRun.Spec.ReasoningLevel != platformv1alpha1.ReasoningLow {
		t.Fatalf("finding-triager reasoning = %q, want the role's low level", triageRun.Spec.ReasoningLevel)
	}
	for _, run := range []platformv1alpha1.AgentRun{modelRun, triageRun} {
		if run.Spec.ToolPolicy == nil || !slices.Contains(run.Spec.ToolPolicy.DeniedTools, "Write") || !slices.Contains(run.Spec.ToolPolicy.DeniedTools, "git_push") {
			t.Fatalf("run %s tool policy = %#v, want write tools denied for a read-only role", run.Name, run.Spec.ToolPolicy)
		}
	}
}

func TestSecurityScanDeterministicTaskDispatchFailsWhenRoleCannotBeResolved(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "ghost", Objective: "inspect", Role: "no-such-role"},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)

	updated := getSecurityScan(t, k8sClient, scan)
	entry := executionTask(t, updated.Status.LastExecution, "ghost", 0)
	if entry.State != triggersv1alpha1.SecurityScanTaskStateFailed {
		t.Fatalf("task state = %q, want Failed for an unresolvable role", entry.State)
	}
	if !strings.Contains(entry.LastError, `"ghost"`) || !strings.Contains(entry.LastError, "no-such-role") {
		t.Fatalf("task lastError = %q, want it to name the task and the missing role", entry.LastError)
	}
	if got := len(securityScanRuns(t, k8sClient, scan.Namespace)); got != 0 {
		t.Fatalf("AgentRuns = %d, want no run dispatched without a role contract", got)
	}
}

// securityScanRunInstructions reads the custom instructions materialised for a
// run through its instructions ConfigMap reference.
func securityScanRunInstructions(t *testing.T, k8sClient client.Client, namespace string, run platformv1alpha1.AgentRun) string {
	t.Helper()
	name := run.Annotations["platform.gratefulagents.dev/instructions-configmap-ref"]
	if name == "" {
		t.Fatalf("run %s has no instructions ConfigMap reference", run.Name)
	}
	cm := &corev1.ConfigMap{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, cm); err != nil {
		t.Fatalf("Get(ConfigMap %s): %v", name, err)
	}
	return cm.Data["instructions.md"]
}

func TestSecurityScanCoordinatorRunStampsItsOwnExecutionID(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Annotations = map[string]string{triggersv1alpha1.SecurityScanRunNowAnnotation: "tok-1"}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want the coordinator run", len(runs))
	}
	execID := getSecurityScan(t, k8sClient, scan).Status.LastExecution.ID
	if got := runs[0].Annotations[triggersv1alpha1.SecurityScanExecutionIDAnnotation]; got == "" || got != execID {
		t.Fatalf("coordinator execution-id annotation = %q, want the run's execution %q", got, execID)
	}
}

// postScriptFindingStore serves the findings a post-script matrix is
// materialized from and reloaded against, and counts list calls so tests can
// prove the matrix is computed exactly once.
type postScriptFindingStore struct {
	store.SecurityFindingStore
	findings  []store.SecurityFindingRecord
	listCalls int
	filters   []store.SecurityFindingFilter
}

func (s *postScriptFindingStore) ListSecurityFindings(_ context.Context, f store.SecurityFindingFilter) ([]store.SecurityFindingRecord, error) {
	s.listCalls++
	s.filters = append(s.filters, f)
	out := append([]store.SecurityFindingRecord(nil), s.findings...)
	// The real store caps the result at the requested limit; honouring it here
	// is what lets a test observe truncation of the matrix.
	if f.Limit > 0 && len(out) > int(f.Limit) {
		out = out[:f.Limit]
	}
	return out, nil
}

func (s *postScriptFindingStore) GetSecurityFinding(_ context.Context, _ string, id uuid.UUID) (*store.SecurityFindingRecord, error) {
	for i := range s.findings {
		if s.findings[i].ID == id {
			rec := s.findings[i]
			return &rec, nil
		}
	}
	return nil, nil
}

func (s *postScriptFindingStore) SummarizeSecurityFindings(context.Context, string, string, string, bool) (map[string]int32, error) {
	return map[string]int32{}, nil
}

func (s *postScriptFindingStore) setStatus(t *testing.T, fingerprint, status string) {
	t.Helper()
	for i := range s.findings {
		if s.findings[i].Fingerprint == fingerprint {
			s.findings[i].Status = status
			return
		}
	}
	t.Fatalf("finding %q not in the store", fingerprint)
}

// postScriptTestFinding builds an open finding; tests that need another
// status mutate the record after construction.
func postScriptTestFinding(id, fingerprint, severity string) store.SecurityFindingRecord {
	return store.SecurityFindingRecord{
		ID:          uuid.MustParse(id),
		Fingerprint: fingerprint,
		Title:       "finding " + fingerprint,
		Severity:    severity,
		Status:      store.SecurityFindingStatusOpen,
		FilePath:    "internal/auth/session.go",
		StartLine:   42,
		Description: "unauthenticated session reuse",
	}
}

// postScriptSecurityScan builds a research -> report DAG (report is the sink)
// with the given post-scripts.
func postScriptSecurityScan(scripts []triggersv1alpha1.SecurityScanPostScript, parallelism int32) *triggersv1alpha1.SecurityScan {
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "research", Objective: "inspect the surface"},
		{Name: "report", Objective: "summarize", DependsOn: []string{"research"}},
	}, parallelism)
	scan.Spec.PostScripts = scripts
	return scan
}

func postScriptJob(t *testing.T, exec *triggersv1alpha1.SecurityScanExecutionStatus, script, fingerprint string) triggersv1alpha1.SecurityScanPostScriptJobStatus {
	t.Helper()
	if exec == nil {
		t.Fatal("execution is nil")
	}
	for _, job := range exec.PostScriptJobs {
		if job.Script == script && job.Fingerprint == fingerprint {
			return job
		}
	}
	t.Fatalf("post-script job %s/%s missing from %#v", script, fingerprint, exec.PostScriptJobs)
	return triggersv1alpha1.SecurityScanPostScriptJobStatus{}
}

func postScriptRun(t *testing.T, runs []platformv1alpha1.AgentRun, script, fingerprint string) platformv1alpha1.AgentRun {
	t.Helper()
	for _, run := range runs {
		if run.Annotations[triggersv1alpha1.SecurityScanPostScriptAnnotation] == script &&
			run.Annotations[triggersv1alpha1.SecurityScanPostScriptFindingAnnotation] == fingerprint {
			return run
		}
	}
	t.Fatalf("post-script run %s/%s missing from %#v", script, fingerprint, runs)
	return platformv1alpha1.AgentRun{}
}

func TestSecurityScanPostScriptsMaterializeOncePerFindingInScriptOrder(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", Prompt: "Build a proof of concept."},
		{Name: "triage", Prompt: "Assign a final status."},
	}, 4)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	findings := &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000b1", "fp-beta", "high"),
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical"),
	}}
	reconciler.Findings = findings

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if !exec.PostScriptsMaterialized || len(exec.PostScriptJobs) != 4 {
		t.Fatalf("post-script jobs = %#v, want a materialized 2x2 matrix", exec.PostScriptJobs)
	}
	// Findings sort by fingerprint and each finding's scripts follow spec
	// order, so a later script always observes the earlier script's verdict.
	wantOrder := []string{"fp-alpha/validate", "fp-alpha/triage", "fp-beta/validate", "fp-beta/triage"}
	for i, want := range wantOrder {
		job := exec.PostScriptJobs[i]
		if got := job.Fingerprint + "/" + job.Script; got != want {
			t.Fatalf("job %d = %q, want %q", i, got, want)
		}
	}
	if exec.PostScriptJobs[0].State != triggersv1alpha1.SecurityScanPostScriptStateRunning ||
		exec.PostScriptJobs[1].State != triggersv1alpha1.SecurityScanPostScriptStatePending {
		t.Fatalf("per-finding serialization broken: %#v", exec.PostScriptJobs[:2])
	}
	if exec.PostScriptJobs[2].State != triggersv1alpha1.SecurityScanPostScriptStateRunning {
		t.Fatalf("second finding did not start in parallel: %#v", exec.PostScriptJobs[2])
	}
	// The sink may not launch while verdicts are outstanding.
	assertExecutionTaskState(t, exec, "report", 0, triggersv1alpha1.SecurityScanTaskStatePending)
	if runs := taskRunsByTask(securityScanRuns(t, k8sClient, scan.Namespace), "report"); len(runs) != 0 {
		t.Fatalf("report runs = %#v, want none while post-script jobs are pending", runs)
	}
	run := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate", "fp-alpha")
	if run.Annotations[triggersv1alpha1.SecurityScanExecutionIDAnnotation] != exec.ID {
		t.Fatalf("post-script run annotations = %#v, want the execution id", run.Annotations)
	}
	if run.Spec.ModeRef == nil || run.Spec.ModeRef.Name != securityScanTaskModeTemplate {
		t.Fatalf("ModeRef = %#v, want %q", run.Spec.ModeRef, securityScanTaskModeTemplate)
	}
	if len(run.OwnerReferences) != 1 || run.OwnerReferences[0].Name != scan.Name {
		t.Fatalf("OwnerReferences = %#v, want SecurityScan owner", run.OwnerReferences)
	}
	if run.Spec.ToolPolicy == nil || !slices.Contains(run.Spec.ToolPolicy.DeniedTools, "submit_security_scan_report") {
		t.Fatalf("ToolPolicy = %#v, want submit_security_scan_report denied", run.Spec.ToolPolicy)
	}

	for _, fingerprint := range []string{"fp-alpha", "fp-beta"} {
		first := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate", fingerprint)
		markSecurityScanTaskRun(t, k8sClient, scan.Namespace, first.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if job := postScriptJob(t, exec, "validate", "fp-alpha"); job.State != triggersv1alpha1.SecurityScanPostScriptStateSucceeded ||
		!strings.Contains(job.Result, `finding status is "open"`) {
		t.Fatalf("succeeded job = %#v, want the reloaded finding status as its result", job)
	}
	if job := postScriptJob(t, exec, "triage", "fp-alpha"); job.State != triggersv1alpha1.SecurityScanPostScriptStateRunning {
		t.Fatalf("second script = %#v, want Running once the first one finished", job)
	}
	if findings.listCalls != 1 {
		t.Fatalf("finding list calls = %d, want the matrix materialized exactly once", findings.listCalls)
	}
	if got := findings.filters[0]; got.ExecutionID != exec.ID || got.Namespace != scan.Namespace || got.ScanName != scan.Name {
		t.Fatalf("finding filter = %#v, want this execution's findings", got)
	}

	for _, fingerprint := range []string{"fp-alpha", "fp-beta"} {
		second := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "triage", fingerprint)
		markSecurityScanTaskRun(t, k8sClient, scan.Namespace, second.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	assertExecutionTaskState(t, exec, "report", 0, triggersv1alpha1.SecurityScanTaskStateRunning)
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning {
		t.Fatalf("execution phase = %q, want Running until the sink completes", exec.Phase)
	}
	report := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "report")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, report.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if phase := getSecurityScan(t, k8sClient, scan).Status.LastExecution.Phase; phase != triggersv1alpha1.SecurityScanExecutionPhaseSucceeded {
		t.Fatalf("execution phase = %q, want Succeeded once every job and task finished", phase)
	}
}

func TestSecurityScanPostScriptsReevaluateRunOnAgainstTheReloadedFinding(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", Prompt: "Build a proof of concept."},
		{Name: "exploit", RunOn: "confirmed", Prompt: "Weaponize the confirmed issue."},
	}, 4)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	findings := &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical"),
		postScriptTestFinding("00000000-0000-0000-0000-0000000000b1", "fp-beta", "high"),
	}}
	reconciler.Findings = findings

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	// The first script confirms one finding and leaves the other open; the
	// second script's runOn is decided by THAT state, not by the state the
	// matrix was materialized from.
	findings.setStatus(t, "fp-alpha", store.SecurityFindingStatusConfirmed)
	for _, fingerprint := range []string{"fp-alpha", "fp-beta"} {
		run := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate", fingerprint)
		markSecurityScanTaskRun(t, k8sClient, scan.Namespace, run.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if job := postScriptJob(t, exec, "exploit", "fp-alpha"); job.State != triggersv1alpha1.SecurityScanPostScriptStateRunning {
		t.Fatalf("confirmed finding job = %#v, want Running", job)
	}
	skipped := postScriptJob(t, exec, "exploit", "fp-beta")
	if skipped.State != triggersv1alpha1.SecurityScanPostScriptStateSkipped ||
		!strings.Contains(skipped.Result, `runOn "confirmed"`) || !strings.Contains(skipped.Result, `status "open"`) {
		t.Fatalf("unconfirmed finding job = %#v, want Skipped stating the unmatched status", skipped)
	}
	// Skipping is a normal outcome, not incomplete coverage.
	if len(exec.CoverageGaps) != 0 {
		t.Fatalf("coverage gaps = %#v, want none for a runOn skip", exec.CoverageGaps)
	}
}

func TestSecurityScanFailedPostScriptRecordsCoverageGapAndReleasesTheSink(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	noRetries := int32(0)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", Prompt: "Build a proof of concept."},
	}, 4)
	scan.Spec.Execution.TaskMaxRetries = &noRetries
	reconciler, k8sClient, stateStore := newDeterministicSecurityScanReconciler(t, now, scan)
	reconciler.Findings = &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical"),
	}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	job := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate", "fp-alpha")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, job.Name, platformv1alpha1.AgentRunPhaseFailed, "", "invalid post-script tool call")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if entry := postScriptJob(t, exec, "validate", "fp-alpha"); entry.State != triggersv1alpha1.SecurityScanPostScriptStateFailed {
		t.Fatalf("job = %#v, want Failed after a non-retryable failure", entry)
	}
	if len(exec.CoverageGaps) != 1 || !strings.Contains(exec.CoverageGaps[0], `post-script "validate" did not complete for finding fp-alpha`) {
		t.Fatalf("coverage gaps = %#v, want the failed post-script recorded", exec.CoverageGaps)
	}
	// A failed job is terminal: it must not block the report forever, but the
	// report must state the gap.
	assertExecutionTaskState(t, exec, "report", 0, triggersv1alpha1.SecurityScanTaskStateRunning)
	report := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "report")
	prompt := securityScanSeedMessage(t, stateStore, scan.Namespace, report.Name)
	if !strings.Contains(prompt, "## Incomplete coverage") || !strings.Contains(prompt, exec.CoverageGaps[0]) {
		t.Fatalf("sink prompt does not disclose the coverage gap:\n%s", prompt)
	}
}

func TestSecurityScanPostScriptsDoNotBlockTheScanWithoutAFindingStore(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", Prompt: "Build a proof of concept."},
	}, 4)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if !exec.PostScriptsMaterialized || len(exec.PostScriptJobs) != 0 {
		t.Fatalf("execution = %#v, want an empty materialized matrix without a finding store", exec.PostScriptJobs)
	}
	if len(exec.CoverageGaps) != 1 || !strings.Contains(exec.CoverageGaps[0], "no finding store is configured") {
		t.Fatalf("coverage gaps = %#v, want the missing finding store recorded", exec.CoverageGaps)
	}
	assertExecutionTaskState(t, exec, "report", 0, triggersv1alpha1.SecurityScanTaskStateRunning)
}

func TestSecurityScanFanOutTruncationIsRecordedAsCoverageGap(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "produce targets", OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect {{item.field}}", DependsOn: []string{"source"}, ForEach: "source", MaxInstances: 1},
	}, 2)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	source := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, source.Name, platformv1alpha1.AgentRunPhaseSucceeded, `[{"field":"one"},{"field":"two"}]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if len(exec.CoverageGaps) != 1 || !strings.Contains(exec.CoverageGaps[0], `task "fan" fan-out truncated from 2 to 1 instances`) {
		t.Fatalf("coverage gaps = %#v, want the truncated inventory recorded", exec.CoverageGaps)
	}
}

// postScriptCreateRejector fails every post-script AgentRun create the way a
// permanent admission rejection or an exhausted quota does: the error text
// carries no marker, so the engine classifies it retryable.
type postScriptCreateRejector struct {
	client.Client
}

func (c postScriptCreateRejector) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if run, ok := obj.(*platformv1alpha1.AgentRun); ok && run.Annotations[triggersv1alpha1.SecurityScanPostScriptAnnotation] != "" {
		return fmt.Errorf("admission webhook denied the request: post-script runs are not permitted")
	}
	return c.Client.Create(ctx, obj, opts...)
}

func TestSecurityScanPostScriptDispatchFailureConsumesAttemptsAndReleasesTheSink(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	oneRetry := int32(1)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", Prompt: "Build a proof of concept."},
	}, 4)
	scan.Spec.Execution.TaskMaxRetries = &oneRetry
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconciler.Client = postScriptCreateRejector{Client: reconciler.Client}
	reconciler.Findings = &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical"),
	}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")

	// A dispatch that never lands still consumes an attempt, so the retry
	// budget runs out instead of re-dispatching forever behind the gate.
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	job := postScriptJob(t, exec, "validate", "fp-alpha")
	if job.Attempts != 1 || job.State != triggersv1alpha1.SecurityScanPostScriptStatePending {
		t.Fatalf("job after a failed dispatch = %#v, want attempts 1 still pending for its last retry", job)
	}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	job = postScriptJob(t, exec, "validate", "fp-alpha")
	if job.Attempts != 2 || job.State != triggersv1alpha1.SecurityScanPostScriptStateFailed {
		t.Fatalf("job after the retry budget ran out = %#v, want attempts 2 and Failed", job)
	}
	if len(exec.CoverageGaps) != 1 || !strings.Contains(exec.CoverageGaps[0], `post-script "validate" did not complete for finding fp-alpha`) {
		t.Fatalf("coverage gaps = %#v, want the undispatchable post-script recorded", exec.CoverageGaps)
	}
	// The sink gate is open again: a job that can never dispatch must not
	// deadlock the report.
	assertExecutionTaskState(t, exec, "report", 0, triggersv1alpha1.SecurityScanTaskStateRunning)
}

func TestSecurityScanResumeKeepsCoverageGapsItCannotRederive(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	noRetries := int32(0)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "produce targets", OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect {{item.field}}", DependsOn: []string{"source"}, ForEach: "source", MaxInstances: 1},
		{Name: "report", Objective: "summarize", DependsOn: []string{"fan"}},
	}, 4)
	scan.Spec.Execution.TaskMaxRetries = &noRetries
	scan.Spec.PostScripts = []triggersv1alpha1.SecurityScanPostScript{{Name: "validate", Prompt: "Build a proof of concept."}}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconciler.Findings = &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical"),
	}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	source := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, source.Name, platformv1alpha1.AgentRunPhaseSucceeded, `[{"field":"one"},{"field":"two"}]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	fan := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "fan")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, fan.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	postScript := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate", "fp-alpha")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, postScript.Name, platformv1alpha1.AgentRunPhaseFailed, "", "unauthorized")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	report := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "report")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, report.Name, platformv1alpha1.AgentRunPhaseFailed, "", "unauthorized")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed || len(exec.CoverageGaps) != 2 {
		t.Fatalf("execution before resume = phase %q gaps %#v, want Failed with the fan-out and post-script gaps", exec.Phase, exec.CoverageGaps)
	}

	updated := getSecurityScan(t, k8sClient, scan)
	updated.Annotations = map[string]string{triggersv1alpha1.SecurityScanResumeAnnotation: "resume-gaps"}
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(SecurityScan resume annotation): %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	// The reset job's gap is retracted (it gets another attempt); the fan-out
	// truncation is NOT re-derivable — expandFanOuts skips expanded tasks — so
	// erasing it would hand the sink a partial scan presented as complete.
	if len(exec.CoverageGaps) != 1 || !strings.Contains(exec.CoverageGaps[0], `task "fan" fan-out truncated from 2 to 1 instances`) {
		t.Fatalf("coverage gaps after resume = %#v, want only the fan-out truncation kept", exec.CoverageGaps)
	}
	if job := postScriptJob(t, exec, "validate", "fp-alpha"); job.State != triggersv1alpha1.SecurityScanPostScriptStatePending &&
		job.State != triggersv1alpha1.SecurityScanPostScriptStateRunning {
		t.Fatalf("resumed post-script job = %#v, want it retried", job)
	}
}

func TestSecurityScanAllSinkWorkflowKeepsProsePostScriptsAndDisclosesTheGap(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "audit", Objective: "review the whole surface"},
	}, 2)
	scan.Spec.PostScripts = []triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", Prompt: "Build a proof of concept.", RunOn: "confirmed"},
	}
	reconciler, k8sClient, stateStore := newDeterministicSecurityScanReconciler(t, now, scan)
	reconciler.Findings = &postScriptFindingStore{}

	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if len(exec.PostScriptJobs) != 0 {
		t.Fatalf("post-script jobs = %#v, want none for an all-sink workflow", exec.PostScriptJobs)
	}
	if len(exec.CoverageGaps) != 1 || !strings.Contains(exec.CoverageGaps[0], "every workflow task is a terminal (sink) task") {
		t.Fatalf("coverage gaps = %#v, want the un-run post-script matrix disclosed", exec.CoverageGaps)
	}
	audit := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "audit")
	prompt := securityScanSeedMessage(t, stateStore, scan.Namespace, audit.Name)
	if strings.Contains(prompt, "The post-scripts already ran") {
		t.Fatalf("sink prompt claims platform-executed post-scripts that never ran:\n%s", prompt)
	}
	if !strings.Contains(prompt, "running them is YOUR responsibility") ||
		!strings.Contains(prompt, `Post-script "validate" (runs on: confirmed findings): Build a proof of concept.`) {
		t.Fatalf("sink prompt does not carry the prose post-script instructions:\n%s", prompt)
	}
	if !strings.Contains(prompt, exec.CoverageGaps[0]) {
		t.Fatalf("sink prompt does not disclose the coverage gap:\n%s", prompt)
	}
}

func TestSecurityScanPostScriptMatrixTruncationBeyondTheJobCapIsRecorded(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", Prompt: "Build a proof of concept."},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	// One script means the finding count and the job cap coincide: listing
	// exactly the cap cannot tell "exactly full" from "truncated".
	findings := &postScriptFindingStore{}
	for i := range triggersv1alpha1.MaxSecurityScanPostScriptJobs + 5 {
		findings.findings = append(findings.findings, postScriptTestFinding(
			fmt.Sprintf("00000000-0000-0000-0000-%012d", i), fmt.Sprintf("fp-%04d", i), "high"))
	}
	reconciler.Findings = findings

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	if got := findings.filters[0].Limit; got != int32(triggersv1alpha1.MaxSecurityScanPostScriptJobs)+1 {
		t.Fatalf("finding list limit = %d, want one row past the job cap", got)
	}
	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if len(exec.PostScriptJobs) != triggersv1alpha1.MaxSecurityScanPostScriptJobs {
		t.Fatalf("post-script jobs = %d, want the matrix capped at %d", len(exec.PostScriptJobs), triggersv1alpha1.MaxSecurityScanPostScriptJobs)
	}
	if len(exec.CoverageGaps) != 1 || !strings.Contains(exec.CoverageGaps[0], "post-scripts ran on 200 of at least 201 eligible findings") {
		t.Fatalf("coverage gaps = %#v, want the uncovered findings disclosed", exec.CoverageGaps)
	}
}

func TestSecurityScanPostScriptDispatchStopsAtTheModelJobBudget(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", Prompt: "Build a proof of concept."},
	}, 4)
	// research (1 run) + one post-script job + the reserved report run
	// exhausts the allowance.
	scan.Spec.Budgets = &triggersv1alpha1.SecurityScanBudgets{MaxModelJobs: 3}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconciler.Findings = &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical"),
		postScriptTestFinding("00000000-0000-0000-0000-0000000000b1", "fp-beta", "high"),
	}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning {
		t.Fatalf("execution phase = %q, want Running: the budget must stop dispatch, not fail the execution", exec.Phase)
	}
	if job := postScriptJob(t, exec, "validate", "fp-alpha"); job.State != triggersv1alpha1.SecurityScanPostScriptStateRunning {
		t.Fatalf("first job = %#v, want it dispatched inside the budget", job)
	}
	// The over-budget job is terminal, not Pending: pending jobs would hold
	// the sink gate closed forever, since no later pass frees budget.
	beta := postScriptJob(t, exec, "validate", "fp-beta")
	if beta.State != triggersv1alpha1.SecurityScanPostScriptStateSkipped || beta.Attempts != 0 {
		t.Fatalf("over-budget job = %#v, want it skipped without an attempt", beta)
	}
	if len(exec.CoverageGaps) != 1 || !strings.Contains(exec.CoverageGaps[0], "1 post-script job(s) never ran") {
		t.Fatalf("coverage gaps = %#v, want the budget-skipped jobs disclosed", exec.CoverageGaps)
	}

	// The slot dispatch left unspent belongs to the sink: the report must
	// still run and disclose the gap instead of the execution failing for
	// budget with nothing submitted.
	alpha := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate", "fp-alpha")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, alpha.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	assertExecutionTaskState(t, exec, "report", 0, triggersv1alpha1.SecurityScanTaskStateRunning)

	// Reaching the cap with the sink already running is not schedulable work.
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning {
		t.Fatalf("execution phase = %q, want Running: the sink's reserved run must not fail the budget", exec.Phase)
	}
	report := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "report")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, report.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseSucceeded {
		t.Fatalf("execution phase = %q, want Succeeded once the reserved report finished", exec.Phase)
	}
}

func setSecurityScanRunCost(t *testing.T, k8sClient client.Client, namespace, name, costUSD string, tokens int64) {
	t.Helper()
	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, run); err != nil {
		t.Fatalf("Get(AgentRun %s): %v", name, err)
	}
	run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{CostUsd: costUSD, InputTokens: tokens}
	if err := k8sClient.Status().Update(context.Background(), run); err != nil {
		t.Fatalf("Status().Update(AgentRun %s): %v", name, err)
	}
}

func TestSecurityScanPostScriptRetriesAllCountTowardTheCostBudget(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := now
	oneRetry := int32(1)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", Prompt: "Build a proof of concept."},
	}, 4)
	scan.Spec.Execution.TaskMaxRetries = &oneRetry
	scan.Spec.Execution.RetryBackoff = metav1.Duration{Duration: 5 * time.Second}
	scan.Spec.Budgets = &triggersv1alpha1.SecurityScanBudgets{MaxCostUSD: "1.00"}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconciler.Now = func() time.Time { return clock }
	reconciler.Findings = &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical"),
	}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	// Attempt 1 burns $0.60 and fails retryably; job.RunName is overwritten
	// by the retry, so only the enumerated names keep it in the accounting.
	first := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate", "fp-alpha")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, first.Name, platformv1alpha1.AgentRunPhaseFailed, "", "temporary connection timeout")
	setSecurityScanRunCost(t, k8sClient, scan.Namespace, first.Name, "0.60", 100)
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	clock = clock.Add(5 * time.Second)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	job := postScriptJob(t, exec, "validate", "fp-alpha")
	if job.Attempts != 2 || job.RunName == first.Name {
		t.Fatalf("job after the retry = %#v, want a second attempt on a new run name", job)
	}
	// Attempt 2 alone stays under maxCostUSD; both attempts together do not.
	setSecurityScanRunCost(t, k8sClient, scan.Namespace, job.RunName, "0.60", 100)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		t.Fatalf("execution phase = %q, want Failed: both post-script attempts must be summed", exec.Phase)
	}
	if got := executionTask(t, exec, "report", 0).LastError; !strings.Contains(got, "execution cost $1.20 exceeds budgets.maxCostUSD 1.00") {
		t.Fatalf("budget failure = %q, want the summed cost of both post-script attempts", got)
	}
}

func TestSecurityScanPostScriptRunInheritsTheSinkRoleToolNarrowing(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", Prompt: "Build a proof of concept."},
	}, 4)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconciler.Findings = &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical"),
	}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	run := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate", "fp-alpha")
	if run.Spec.ToolPolicy == nil {
		t.Fatal("post-script run has no tool policy")
	}
	// The sink task's read-only role governed post-scripts while they ran
	// inside the sink run; dispatching them separately must not restore the
	// write tools that role denies.
	for _, tool := range []string{"submit_security_scan_report", "Write", "Edit", "create_pull_request"} {
		if !slices.Contains(run.Spec.ToolPolicy.DeniedTools, tool) {
			t.Fatalf("post-script denied tools = %#v, want %q denied", run.Spec.ToolPolicy.DeniedTools, tool)
		}
	}
}

func TestSecurityScanPostScriptRunNameKeysOnFindingIdentity(t *testing.T) {
	// A fingerprint is unique only within (namespace, scan, repository), and
	// CreateTriggerRun swallows AlreadyExists: two same-fingerprint findings
	// must never bind to one AgentRun.
	a := securityScanPostScriptRunName("scan", "exec", "validate", "00000000-0000-0000-0000-0000000000a1", 1, "")
	b := securityScanPostScriptRunName("scan", "exec", "validate", "00000000-0000-0000-0000-0000000000b1", 1, "")
	if a == b {
		t.Fatalf("post-script run names collide across findings: %q", a)
	}
	if retry := securityScanPostScriptRunName("scan", "exec", "validate", "00000000-0000-0000-0000-0000000000a1", 2, ""); retry == a {
		t.Fatalf("retry run name = %q, want it distinct from the attempt it replaces", retry)
	}
	if again := securityScanPostScriptRunName("scan", "exec", "validate", "00000000-0000-0000-0000-0000000000a1", 1, ""); again != a {
		t.Fatalf("run name is not deterministic: %q != %q", again, a)
	}
	for _, name := range []string{a, b} {
		if len(name) > 63 {
			t.Fatalf("run name %q exceeds 63 characters", name)
		}
	}
}
