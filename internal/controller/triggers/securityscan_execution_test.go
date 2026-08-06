package triggers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSecurityScanDeterministicExecutionSchedulesDependenciesWithinParallelismBound(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "a", Objective: "inspect a"},
		{Name: "b", Objective: "inspect b"},
		{Name: "c", Objective: "join results", DependsOn: []string{"a", "b"}},
	}, 1)
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

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
	if run.Spec.ToolPolicy == nil || strings.Join(run.Spec.ToolPolicy.AllowedTools, ",") != wantAllowed || strings.Join(run.Spec.ToolPolicy.DeniedTools, ",") != "Bash" {
		t.Fatalf("ToolPolicy = %#v, want allowed %q and denied Bash", run.Spec.ToolPolicy, wantAllowed)
	}
	if run.Annotations[triggersv1alpha1.SecurityScanMaxFindingsAnnotation] != "3" {
		t.Fatalf("max-findings annotation = %q, want 3", run.Annotations[triggersv1alpha1.SecurityScanMaxFindingsAnnotation])
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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
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
	reconciler, k8sClient, stateStore := newSecurityScanReconciler(t, now, scan)

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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, workflow)

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
		reconciler, k8sClient, stateStore := newSecurityScanReconciler(t, now, scan, workflow)
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
		reconciler, k8sClient, stateStore := newSecurityScanReconciler(t, now, scan, workflow)
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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, repo, webhookSecret)
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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, workflow)
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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
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
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
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
	reconciler, _, _ := newSecurityScanReconciler(t, now.Time, scan)

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
