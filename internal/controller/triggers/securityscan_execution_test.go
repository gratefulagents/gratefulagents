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

func TestSecurityScanDeterministicTaskSkillRefsMergeAcrossRetriesAndFanOut(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := now
	oneRetry := int32(1)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{
			Name:         "source",
			Objective:    "produce targets",
			OutputSchema: `{"type":"array"}`,
			MaxRetries:   &oneRetry,
			SkillRefs: []platformv1alpha1.NamedRef{
				{Name: "shared"},
				{Name: "source-skill"},
				{Name: "source-skill"},
			},
		},
		{
			Name:      "fan",
			Objective: "inspect {{item.target}}",
			DependsOn: []string{"source"},
			ForEach:   "source",
			SkillRefs: []platformv1alpha1.NamedRef{{Name: "fan-skill"}, {Name: "shared"}},
		},
	}, 2)
	scan.Spec.Defaults.SkillRefs = []platformv1alpha1.NamedRef{{Name: "base-skill"}, {Name: "shared"}}
	scan.Spec.Execution.RetryBackoff = metav1.Duration{Duration: time.Second}
	objects := []client.Object{scan}
	for _, name := range []string{"base-skill", "shared", "source-skill", "fan-skill"} {
		objects = append(objects, readySecurityScanSkill(name, scan.Namespace))
	}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, objects...)
	reconciler.Now = func() time.Time { return clock }

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	first := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	assertSkillRefNames(t, first.Spec.SkillRefs, "base-skill", "shared", "source-skill")
	if slices.ContainsFunc(first.Spec.SkillRefs, func(ref platformv1alpha1.NamedRef) bool { return ref.Name == "security-scan" }) {
		t.Fatal("task run redundantly hard-codes the security-scan skill supplied by its ModeTemplate")
	}

	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, first.Name, platformv1alpha1.AgentRunPhaseFailed, "", "temporary connection timeout")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	clock = clock.Add(time.Second)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	retryName := executionTask(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "source", 0).RunName
	retry := taskRunByName(t, securityScanRuns(t, k8sClient, scan.Namespace), retryName)
	assertSkillRefNames(t, retry.Spec.SkillRefs, "base-skill", "shared", "source-skill")

	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, retry.Name, platformv1alpha1.AgentRunPhaseSucceeded, `[{"target":"a"},{"target":"b"}]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	fanRuns := taskRunsByTask(securityScanRuns(t, k8sClient, scan.Namespace), "fan")
	if len(fanRuns) != 2 {
		t.Fatalf("fan runs = %d, want 2", len(fanRuns))
	}
	for _, run := range fanRuns {
		assertSkillRefNames(t, run.Spec.SkillRefs, "base-skill", "shared", "fan-skill")
	}
}

func TestSecurityScanDeterministicTaskMissingSkillFailsBeforeDispatch(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{
		Name:      "inspect",
		Objective: "inspect",
		SkillRefs: []platformv1alpha1.NamedRef{{Name: "missing-skill"}},
	}}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
		t.Fatalf("AgentRuns = %d, want none for a missing task Skill", len(runs))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, securityScanReasonUnresolvedReference)
	if !strings.Contains(updated.Status.LastError, `Skill "missing-skill" referenced by workflow task "inspect" not found`) {
		t.Fatalf("LastError = %q, want clear missing task Skill message", updated.Status.LastError)
	}
}

func TestSecurityScanDeterministicTaskUnreadySkillFailsBeforeDispatch(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{
		Name:      "inspect",
		Objective: "inspect",
		SkillRefs: []platformv1alpha1.NamedRef{{Name: "pending-skill"}},
	}}, 1)
	pending := &platformv1alpha1.Skill{ObjectMeta: metav1.ObjectMeta{Name: "pending-skill", Namespace: scan.Namespace}}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan, pending)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
		t.Fatalf("AgentRuns = %d, want none for an unready task Skill", len(runs))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, securityScanReasonUnresolvedReference)
	if !strings.Contains(updated.Status.LastError, `Skill "pending-skill" referenced by workflow task "inspect" is not ready`) {
		t.Fatalf("LastError = %q, want clear unready task Skill message", updated.Status.LastError)
	}
}

func TestSecurityScanExecutionIgnoresDeletedSkillForCompletedTask(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "source", SkillRefs: []platformv1alpha1.NamedRef{{Name: "source-skill"}}},
		{Name: "next", Objective: "next", DependsOn: []string{"source"}, SkillRefs: []platformv1alpha1.NamedRef{{Name: "next-skill"}}},
	}, 1)
	sourceSkill := readySecurityScanSkill("source-skill", scan.Namespace)
	nextSkill := readySecurityScanSkill("next-skill", scan.Namespace)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan, sourceSkill, nextSkill)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	sourceRun := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, sourceRun.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	if err := k8sClient.Delete(context.Background(), sourceSkill); err != nil {
		t.Fatalf("Delete(source Skill): %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	nextRun := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "next")
	assertSkillRefNames(t, nextRun.Spec.SkillRefs, "next-skill")
}

func readySecurityScanSkill(name, namespace string) *platformv1alpha1.Skill {
	return &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status: platformv1alpha1.SkillStatus{
			Phase:              "Ready",
			ObservedGeneration: 0,
			Resolved:           &platformv1alpha1.SkillResolved{Instructions: "test instructions"},
		},
	}
}

func assertSkillRefNames(t *testing.T, refs []platformv1alpha1.NamedRef, want ...string) {
	t.Helper()
	got := make([]string, len(refs))
	for i := range refs {
		got[i] = refs[i].Name
	}
	if !slices.Equal(got, want) {
		t.Fatalf("SkillRefs = %v, want %v", got, want)
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

func TestSecurityScanPausedTaskDoesNotRemainRunning(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	noRetries := int32(0)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "a", Objective: "inspect"}}, 1)
	scan.Spec.Execution.TaskMaxRetries = &noRetries
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	run := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "a")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, run.Name, platformv1alpha1.AgentRunPhasePaused, "", "stale worker error")
	paused := taskRunByName(t, securityScanRuns(t, k8sClient, scan.Namespace), run.Name)
	paused.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{
		State:         "Paused",
		BlockedReason: "paused after 2h0m0s timeout — extend maxRuntime to resume",
	}
	paused.Status.Sandbox = &platformv1alpha1.AgentRunSandboxStatus{
		Provider: "agent-sandbox",
		ClaimRef: &platformv1alpha1.NamedRef{Name: "draining-claim"},
	}
	if err := k8sClient.Status().Update(context.Background(), &paused); err != nil {
		t.Fatalf("record paused run with draining sandbox: %v", err)
	}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if entry := executionTask(t, exec, "a", 0); entry.State != triggersv1alpha1.SecurityScanTaskStateRunning {
		t.Fatalf("paused task with a draining sandbox = %#v, want Running", entry)
	}

	paused = taskRunByName(t, securityScanRuns(t, k8sClient, scan.Namespace), run.Name)
	paused.Status.Sandbox = nil
	if err := k8sClient.Status().Update(context.Background(), &paused); err != nil {
		t.Fatalf("clear drained sandbox: %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	entry := executionTask(t, exec, "a", 0)
	if entry.State != triggersv1alpha1.SecurityScanTaskStateFailed || !strings.Contains(entry.LastError, "paused after 2h0m0s timeout") {
		t.Fatalf("paused task = %#v, want terminal failure with the queue timeout reason", entry)
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
	if len(getSecurityScan(t, k8sClient, scan).Status.LastExecution.FanOuts) != 0 {
		t.Fatal("legacy maxInstances fan-out unexpectedly persisted a chunk plan")
	}
}

func TestSecurityScanTargetRunsMaterializesBalancedPlanBeforeDispatch(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	records := make([]string, 101)
	for i := range records {
		records[i] = fmt.Sprintf(`{"value":%d}`, i)
	}
	sourceOutput := " [" + strings.Join(records, ",") + "] "
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "produce targets", OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect {{items}} in [{{range.start}},{{range.end}})", DependsOn: []string{"source"}, ForEach: "source", TargetRuns: 16, OutputSchema: `{"type":"array","items":{"type":"object","properties":{"result":{"type":"object","required":["status"],"properties":{"status":{"type":"string"}}}}}}`},
	}, 16)
	reconciler, k8sClient, stateStore := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	source := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, source.Name, platformv1alpha1.AgentRunPhaseSucceeded, sourceOutput, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	if got := len(taskRunsByTask(securityScanRuns(t, k8sClient, scan.Namespace), "fan")); got != 0 {
		t.Fatalf("fan runs in materialization reconcile = %d, want 0", got)
	}
	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if len(exec.FanOuts) != 1 {
		t.Fatalf("fan-out plans = %#v, want one durable plan", exec.FanOuts)
	}
	plan := exec.FanOuts[0]
	if plan.Name != "fan" || plan.SourceTask != "source" || plan.SourceRunName != source.Name || plan.Strategy != "chunk-v1" ||
		plan.SourceOutputSHA256 != securityScanSHA256(sourceOutput) || plan.RecordCount != 101 || plan.ChunkCount != 16 {
		t.Fatalf("fan-out plan = %#v, want exact source-bound 101/16 plan", plan)
	}
	entries := make([]triggersv1alpha1.SecurityScanTaskExecutionStatus, 0, 16)
	for _, entry := range exec.Tasks {
		if entry.Name == "fan" {
			entries = append(entries, entry)
		}
	}
	if len(entries) != 16 {
		t.Fatalf("chunk entries = %d, want 16", len(entries))
	}
	start := int32(0)
	for i, entry := range entries {
		size := int32(6)
		if i < 5 {
			size = 7
		}
		if entry.Instance != int32(i) || entry.RecordStart != start || entry.RecordEnd != start+size || entry.InputSHA256 == "" {
			t.Fatalf("chunk %d = %#v, want contiguous balanced range [%d,%d)", i, entry, start, start+size)
		}
		start = entry.RecordEnd
	}
	if start != 101 {
		t.Fatalf("final chunk end = %d, want complete coverage through 101", start)
	}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	fanRuns := taskRunsByTask(securityScanRuns(t, k8sClient, scan.Namespace), "fan")
	if len(fanRuns) != 16 {
		t.Fatalf("fan runs after persisted plan = %d, want 16", len(fanRuns))
	}
	first := fanRuns[0]
	for _, run := range fanRuns {
		if run.Labels[securityScanTaskInstanceLabel] == "0" {
			first = run
			break
		}
	}
	seed := securityScanSeedMessage(t, stateStore, scan.Namespace, first.Name)
	if !strings.Contains(seed, `- Objective: inspect [{"recordIndex":0,"item":{"value":0}}`) || !strings.Contains(seed, "source record indexes [0,7)") {
		t.Fatalf("first chunk prompt does not expose rendered indexed items and range:\n%s", seed)
	}
	schema := first.Annotations[securityScanTaskOutputSchemaAnnotation]
	if strings.Contains(schema, `"allOf"`) || !strings.Contains(schema, `"minItems":7`) || !strings.Contains(schema, `"maximum":6`) ||
		!strings.Contains(schema, `"required":["status"]`) {
		t.Fatalf("chunk output-schema annotation = %q, want supported range constraints merged with the declared result schema", schema)
	}
}

func TestSecurityScanTargetRunsFlattensChunkResultsForDownstreamTask(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "produce targets", OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect {{items}}", DependsOn: []string{"source"}, ForEach: "source", TargetRuns: 2, OutputSchema: `{"type":"array"}`},
		{Name: "join", Objective: "combine {{tasks.fan.output}}", DependsOn: []string{"fan"}},
	}, 2)
	reconciler, k8sClient, stateStore := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	source := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, source.Name, platformv1alpha1.AgentRunPhaseSucceeded, `["a","b","c"]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	for _, run := range taskRunsByTask(securityScanRuns(t, k8sClient, scan.Namespace), "fan") {
		if run.Labels[securityScanTaskInstanceLabel] == "0" {
			markSecurityScanTaskRun(t, k8sClient, scan.Namespace, run.Name, platformv1alpha1.AgentRunPhaseSucceeded, `[{"recordIndex":1,"result":"B"},{"recordIndex":0,"result":"A"}]`, "")
		} else {
			markSecurityScanTaskRun(t, k8sClient, scan.Namespace, run.Name, platformv1alpha1.AgentRunPhaseSucceeded, `[{"recordIndex":2,"result":"C"}]`, "")
		}
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	join := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "join")
	if seed := securityScanSeedMessage(t, stateStore, scan.Namespace, join.Name); !strings.Contains(seed, `combine ["A","B","C"]`) {
		t.Fatalf("join seed = %q, want flattened absolute record order", seed)
	}
}

func TestValidateSecurityScanChunkOutputRejectsMalformedIndexes(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "missing", output: `[{"recordIndex":4,"result":true}]`, want: "want exactly 2"},
		{name: "duplicate", output: `[{"recordIndex":4,"result":true},{"recordIndex":4,"result":false}]`, want: "duplicate recordIndex 4"},
		{name: "foreign", output: `[{"recordIndex":4,"result":true},{"recordIndex":6,"result":false}]`, want: "foreign recordIndex 6"},
		{name: "noninteger", output: `[{"recordIndex":4,"result":true},{"recordIndex":5.0,"result":false}]`, want: "must be an integer"},
		{name: "extra field", output: `[{"recordIndex":4,"result":true},{"recordIndex":5,"result":false,"extra":1}]`, want: "exactly recordIndex and result"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateSecurityScanChunkOutput(tc.output, 4, 6); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateSecurityScanChunkOutput() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSecurityScanTargetRunsRejectsSourceOutputDriftBeforeLaunch(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "produce targets", OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect {{items}}", DependsOn: []string{"source"}, ForEach: "source", TargetRuns: 2},
	}, 2)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	source := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, source.Name, platformv1alpha1.AgentRunPhaseSucceeded, `["a","b"]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, source.Name, platformv1alpha1.AgentRunPhaseSucceeded, `[ "a", "b" ]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	if got := len(taskRunsByTask(securityScanRuns(t, k8sClient, scan.Namespace), "fan")); got != 0 {
		t.Fatalf("fan runs after source drift = %d, want 0", got)
	}
	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		t.Fatalf("execution phase = %q, want Failed after exact-byte drift", exec.Phase)
	}
	for _, entry := range exec.Tasks {
		if entry.Name == "fan" && (entry.State != triggersv1alpha1.SecurityScanTaskStateFailed || !strings.Contains(entry.LastError, "output drifted")) {
			t.Fatalf("drifted chunk entry = %#v, want non-launched drift failure", entry)
		}
	}
}

func TestSecurityScanPreUpgradePlanIsNotReinterpretedAsTargetRuns(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "produce targets", OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect", DependsOn: []string{"source"}, ForEach: "source", TargetRuns: 2},
	}, 2)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	updated := getSecurityScan(t, k8sClient, scan)
	for i := range updated.Status.LastExecution.Plan {
		updated.Status.LastExecution.Plan[i].TargetRuns = 0 // status written by a pre-upgrade controller
	}
	if err := k8sClient.Status().Update(context.Background(), updated); err != nil {
		t.Fatalf("persist pre-upgrade plan: %v", err)
	}
	source := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, source.Name, platformv1alpha1.AgentRunPhaseSucceeded, `["a","b"]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if len(exec.FanOuts) != 0 {
		t.Fatalf("pre-upgrade execution fan-out plans = %#v, want legacy expansion", exec.FanOuts)
	}
	if got := len(taskRunsByTask(securityScanRuns(t, k8sClient, scan.Namespace), "fan")); got != 2 {
		t.Fatalf("legacy fan runs = %d, want one per source record", got)
	}
}

func TestSecurityScanTargetRunsZeroRecordsCompletesVacuously(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "produce targets", OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect {{items}}", DependsOn: []string{"source"}, ForEach: "source", TargetRuns: 4, OutputSchema: `{"type":"array"}`},
		{Name: "join", Objective: "combine {{tasks.fan.output}}", DependsOn: []string{"fan"}},
	}, 2)
	reconciler, k8sClient, stateStore := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	source := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, source.Name, platformv1alpha1.AgentRunPhaseSucceeded, `[]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if got := len(taskRunsByTask(securityScanRuns(t, k8sClient, scan.Namespace), "fan")); got != 0 {
		t.Fatalf("zero-record fan runs = %d, want 0", got)
	}
	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if len(exec.FanOuts) != 1 || exec.FanOuts[0].RecordCount != 0 || exec.FanOuts[0].ChunkCount != 0 {
		t.Fatalf("zero-record fan-out plan = %#v", exec.FanOuts)
	}
	assertExecutionTaskState(t, exec, "fan", 0, triggersv1alpha1.SecurityScanTaskStateSucceeded)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	join := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "join")
	if seed := securityScanSeedMessage(t, stateStore, scan.Namespace, join.Name); !strings.Contains(seed, "combine []") {
		t.Fatalf("join seed = %q, want vacuous [] fan-out output", seed)
	}
}

func TestSecurityScanTargetRunsMalformedOutputFailsNonRetryably(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source", Objective: "produce targets", OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect {{items}}", DependsOn: []string{"source"}, ForEach: "source", TargetRuns: 1},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	source := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "source")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, source.Name, platformv1alpha1.AgentRunPhaseSucceeded, `["a","b"]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	fan := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "fan")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, fan.Name, platformv1alpha1.AgentRunPhaseSucceeded, `[{"recordIndex":0,"result":true},{"recordIndex":0,"result":false}]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	entry := executionTask(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "fan", 0)
	if entry.State != triggersv1alpha1.SecurityScanTaskStateFailed || len(entry.Retries) != 1 || entry.Retries[0].Class != triggersv1alpha1.SecurityScanTaskFailureNonRetryable || !strings.Contains(entry.LastError, "duplicate recordIndex") {
		t.Fatalf("malformed chunk entry = %#v, want immediate non-retryable contract failure", entry)
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

func TestSecurityScanDeterministicExecutionRejectsForEachSourceDrift(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "source-a", Objective: "list a", OutputSchema: `{"type":"array"}`},
		{Name: "source-b", Objective: "list b", OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect {{items}}", DependsOn: []string{"source-a", "source-b"}, ForEach: "source-a", TargetRuns: 2},
	}, 2)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	updated := getSecurityScan(t, k8sClient, scan)
	updated.Spec.Workflow[2].ForEach = "source-b"
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(SecurityScan) error = %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	final := getSecurityScan(t, k8sClient, scan)
	exec := final.Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseFailed ||
		!strings.Contains(final.Status.LastError, `changed forEach from "source-a" to "source-b"`) {
		t.Fatalf("execution after forEach drift = %#v (scan error %q), want explicit terminal drift failure", exec, final.Status.LastError)
	}
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

func TestSecurityScanTargetRunsFailsInsteadOfTruncatingAtExecutionEntryCeiling(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	source := triggersv1alpha1.SecurityScanTask{Name: "source", Objective: "list", OutputSchema: `{"type":"array"}`}
	fan := triggersv1alpha1.SecurityScanTask{Name: "fan", Objective: "inspect {{items}}", DependsOn: []string{"source"}, ForEach: "source", TargetRuns: 2}
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{source, fan}, 1)
	reconciler, _, _ := newDeterministicSecurityScanReconciler(t, now.Time, scan)

	exec := &triggersv1alpha1.SecurityScanExecutionStatus{
		ID: "target-cap", Mode: triggersv1alpha1.SecurityScanExecutionModeDeterministic,
		Plan: []triggersv1alpha1.SecurityScanExecutionPlanNode{{Name: "source"}, {Name: "fan", ForEach: "source", TargetRuns: 2}},
	}
	exec.Tasks = append(exec.Tasks, triggersv1alpha1.SecurityScanTaskExecutionStatus{Name: "source", State: triggersv1alpha1.SecurityScanTaskStateSucceeded, RunName: "src-run"})
	for i := range securityScanExecutionMaxTaskEntries - 2 {
		exec.Tasks = append(exec.Tasks, triggersv1alpha1.SecurityScanTaskExecutionStatus{Name: "pad", Instance: int32(i), State: triggersv1alpha1.SecurityScanTaskStateSucceeded})
	}
	exec.Tasks = append(exec.Tasks, triggersv1alpha1.SecurityScanTaskExecutionStatus{Name: "fan", State: triggersv1alpha1.SecurityScanTaskStatePending})
	engine := &securityScanExecutionEngine{
		r: reconciler, scan: scan, exec: exec, now: now,
		order: []triggersv1alpha1.SecurityScanTask{source, fan},
		tasks: map[string]triggersv1alpha1.SecurityScanTask{"source": source, "fan": fan},
		runs:  map[string]*platformv1alpha1.AgentRun{"src-run": {ObjectMeta: metav1.ObjectMeta{Name: "src-run"}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded, StructuredOutput: `[{"n":0},{"n":1}]`}}},
	}

	if !engine.expandFanOuts(context.Background()) {
		t.Fatal("targetRuns ceiling failure did not materialize durable source metadata")
	}
	entry := engine.taskEntries("fan")[0]
	if entry.State != triggersv1alpha1.SecurityScanTaskStateFailed || !strings.Contains(entry.LastError, "exceeding the execution cap") {
		t.Fatalf("targetRuns entry = %#v, want explicit ceiling failure", entry)
	}
	if len(exec.Tasks) != securityScanExecutionMaxTaskEntries || len(exec.CoverageGaps) != 0 || len(exec.FanOuts) != 1 || exec.FanOuts[0].ChunkCount != 2 {
		t.Fatalf("ceiling result tasks=%d gaps=%#v plans=%#v, want no truncation or gap", len(exec.Tasks), exec.CoverageGaps, exec.FanOuts)
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

// postScriptFindingStore serves the findings post-script pipelines are
// materialized from and reloaded against, and counts list calls so tests can
// prove the pipeline list is computed exactly once.
type postScriptFindingStore struct {
	securityScanRecordStubStore
	findings  []store.SecurityFindingRecord
	listCalls int
	filters   []store.SecurityFindingFilter
}

func (s *postScriptFindingStore) ListSecurityFindings(_ context.Context, f store.SecurityFindingFilter) ([]store.SecurityFindingRecord, error) {
	s.listCalls++
	s.filters = append(s.filters, f)
	out := append([]store.SecurityFindingRecord(nil), s.findings...)
	if f.Offset >= int32(len(out)) {
		return nil, nil
	}
	out = out[f.Offset:]
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

func postScriptJob(t *testing.T, exec *triggersv1alpha1.SecurityScanExecutionStatus, fingerprint string) triggersv1alpha1.SecurityScanPostScriptJobStatus {
	t.Helper()
	if exec == nil {
		t.Fatal("execution is nil")
	}
	for _, job := range exec.PostScriptJobs {
		if job.Fingerprint == fingerprint {
			return job
		}
	}
	t.Fatalf("post-script job for %s missing from %#v", fingerprint, exec.PostScriptJobs)
	return triggersv1alpha1.SecurityScanPostScriptJobStatus{}
}

func postScriptRun(t *testing.T, runs []platformv1alpha1.AgentRun, scripts, fingerprint string) platformv1alpha1.AgentRun {
	t.Helper()
	for _, run := range runs {
		if run.Annotations[triggersv1alpha1.SecurityScanPostScriptAnnotation] == scripts &&
			run.Annotations[triggersv1alpha1.SecurityScanPostScriptFindingAnnotation] == fingerprint {
			return run
		}
	}
	t.Fatalf("post-script run %s/%s missing from %#v", scripts, fingerprint, runs)
	return platformv1alpha1.AgentRun{}
}

func TestSecurityScanVacuousSinkFanOutStillMaterializesPostScripts(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "inventory", Objective: "list targets", OutputSchema: `{"type":"array"}`},
		{Name: "review", Objective: "review {{items}}", DependsOn: []string{"inventory"}, ForEach: "inventory", TargetRuns: 4},
	}, 2)
	scan.Spec.PostScripts = []triggersv1alpha1.SecurityScanPostScript{{Name: "validate", Prompt: "Validate it."}}
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconciler.Findings = &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical"),
	}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	inventory := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "inventory")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, inventory.Name, platformv1alpha1.AgentRunPhaseSucceeded, `[]`, "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning || exec.PostScriptsMaterialized {
		t.Fatalf("vacuous fan-out materialization = %#v, want Running before post-script barrier", exec)
	}
	if len(exec.FanOuts) != 1 || exec.FanOuts[0].ChunkCount != 0 {
		t.Fatalf("vacuous fan-out plan = %#v, want persisted zero-chunk plan", exec.FanOuts)
	}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning || !exec.PostScriptsMaterialized || len(exec.PostScriptJobs) != 1 {
		t.Fatalf("post-script materialization after vacuous sink = %#v", exec)
	}
}

func TestSecurityScanPostScriptsMaterializeOneOrderedPipelinePerFinding(t *testing.T) {
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
	if !exec.PostScriptsMaterialized || len(exec.PostScriptJobs) != 2 || exec.PostScriptJobs[0].State != triggersv1alpha1.SecurityScanPostScriptStatePending {
		t.Fatalf("materialization barrier status = %#v, want persisted pending pipelines", exec.PostScriptJobs)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); slices.ContainsFunc(runs, func(run platformv1alpha1.AgentRun) bool {
		return run.Annotations[triggersv1alpha1.SecurityScanPostScriptAnnotation] != ""
	}) {
		t.Fatalf("post-script runs = %#v, want none in the materialization reconcile", runs)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if !exec.PostScriptsMaterialized || len(exec.PostScriptJobs) != 2 {
		t.Fatalf("post-script jobs = %#v, want one materialized pipeline per finding", exec.PostScriptJobs)
	}
	wantOrder := []string{"fp-alpha", "fp-beta"}
	for i, want := range wantOrder {
		job := exec.PostScriptJobs[i]
		if job.Fingerprint != want || job.Script != "validate" || !slices.Equal(job.Scripts, []string{"validate", "triage"}) {
			t.Fatalf("job %d = %#v, want finding %q with the ordered validate/triage pipeline and legacy Script", i, job, want)
		}
	}
	if exec.PostScriptJobs[0].State != triggersv1alpha1.SecurityScanPostScriptStateRunning ||
		exec.PostScriptJobs[1].State != triggersv1alpha1.SecurityScanPostScriptStateRunning {
		t.Fatalf("per-finding pipelines did not start in parallel: %#v", exec.PostScriptJobs)
	}
	// The sink may not launch while verdicts are outstanding.
	assertExecutionTaskState(t, exec, "report", 0, triggersv1alpha1.SecurityScanTaskStatePending)
	if runs := taskRunsByTask(securityScanRuns(t, k8sClient, scan.Namespace), "report"); len(runs) != 0 {
		t.Fatalf("report runs = %#v, want none while post-script jobs are pending", runs)
	}
	run := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate,triage", "fp-alpha")
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
		pipeline := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate,triage", fingerprint)
		markSecurityScanTaskRun(t, k8sClient, scan.Namespace, pipeline.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if job := postScriptJob(t, exec, "fp-alpha"); job.State != triggersv1alpha1.SecurityScanPostScriptStateSucceeded ||
		!strings.Contains(job.Result, `finding status is "open"`) {
		t.Fatalf("succeeded job = %#v, want the reloaded finding status as its result", job)
	}
	if findings.listCalls != 1 {
		t.Fatalf("finding list calls = %d, want the pipelines materialized exactly once", findings.listCalls)
	}
	if got := findings.filters[0]; got.ExecutionID != exec.ID || got.Namespace != scan.Namespace || got.ScanName != scan.Name {
		t.Fatalf("finding filter = %#v, want this execution's findings", got)
	}

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

func TestSecurityScanPostScriptsPrefilterRunOnAtMaterialization(t *testing.T) {
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
	findings.setStatus(t, "fp-alpha", store.SecurityFindingStatusConfirmed)
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	alpha := postScriptJob(t, exec, "fp-alpha")
	if !slices.Equal(alpha.Scripts, []string{"validate", "exploit"}) {
		t.Fatalf("confirmed finding pipeline = %#v, want validate then exploit", alpha)
	}
	beta := postScriptJob(t, exec, "fp-beta")
	if !slices.Equal(beta.Scripts, []string{"validate"}) {
		t.Fatalf("open finding pipeline = %#v, want only validate", beta)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if got := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate,exploit", "fp-alpha").Annotations[triggersv1alpha1.SecurityScanPostScriptAnnotation]; got != "validate,exploit" {
		t.Fatalf("confirmed finding annotation = %q, want ordered comma-separated scripts", got)
	}
	if got := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate", "fp-beta").Annotations[triggersv1alpha1.SecurityScanPostScriptAnnotation]; got != "validate" {
		t.Fatalf("open finding annotation = %q, want only the matching script", got)
	}

	findings.setStatus(t, "fp-alpha", store.SecurityFindingStatusOpen)
	findings.setStatus(t, "fp-beta", store.SecurityFindingStatusConfirmed)
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if !slices.Equal(postScriptJob(t, exec, "fp-alpha").Scripts, []string{"validate", "exploit"}) ||
		!slices.Equal(postScriptJob(t, exec, "fp-beta").Scripts, []string{"validate"}) {
		t.Fatalf("materialized pipelines changed after finding statuses changed: %#v", exec.PostScriptJobs)
	}
	if len(exec.CoverageGaps) != 0 {
		t.Fatalf("coverage gaps = %#v, want none for scripts filtered out at materialization", exec.CoverageGaps)
	}
}

func TestSecurityScanLegacyPostScriptJobReevaluatesRunOnAtDispatch(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "exploit", RunOn: "confirmed", Prompt: "Weaponize the confirmed issue."},
	}, 4)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	findings := &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical"),
	}}
	findings.setStatus(t, "fp-alpha", store.SecurityFindingStatusConfirmed)
	reconciler.Findings = findings

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	updated := getSecurityScan(t, k8sClient, scan)
	updated.Status.LastExecution.PostScriptJobs[0].Scripts = nil
	if err := k8sClient.Status().Update(context.Background(), updated); err != nil {
		t.Fatalf("Status().Update(SecurityScan legacy job): %v", err)
	}
	findings.setStatus(t, "fp-alpha", store.SecurityFindingStatusOpen)
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	job := postScriptJob(t, exec, "fp-alpha")
	if job.State != triggersv1alpha1.SecurityScanPostScriptStateSkipped ||
		!strings.Contains(job.Result, `runOn "confirmed"`) || !strings.Contains(job.Result, `status "open"`) {
		t.Fatalf("legacy job = %#v, want dispatch-time runOn compatibility skip", job)
	}
	if got := securityScanPostScriptAttemptRunNames(scan.Name, exec.ID, triggersv1alpha1.SecurityScanPostScriptJobStatus{
		Script: "exploit", FindingID: job.FindingID, Attempts: 1,
	}, ""); !slices.Equal(got, []string{securityScanPostScriptRunName(scan.Name, exec.ID, "exploit", job.FindingID, 1, "")}) {
		t.Fatalf("legacy attempt run names = %#v, want the script-keyed legacy name", got)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); slices.ContainsFunc(runs, func(run platformv1alpha1.AgentRun) bool {
		return run.Annotations[triggersv1alpha1.SecurityScanPostScriptAnnotation] != ""
	}) {
		t.Fatalf("legacy runOn mismatch dispatched a post-script run: %#v", runs)
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
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	job := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate", "fp-alpha")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, job.Name, platformv1alpha1.AgentRunPhaseFailed, "", "invalid post-script tool call")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if entry := postScriptJob(t, exec, "fp-alpha"); entry.State != triggersv1alpha1.SecurityScanPostScriptStateFailed {
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

func TestSecurityScanPausedPostScriptWaitsForDrainThenReleasesTheSink(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	noRetries := int32(0)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", Prompt: "Build a proof of concept."},
	}, 4)
	scan.Spec.Execution.TaskMaxRetries = &noRetries
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconciler.Findings = &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical"),
	}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	run := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "validate", "fp-alpha")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, run.Name, platformv1alpha1.AgentRunPhasePaused, "", "stale worker error")
	paused := taskRunByName(t, securityScanRuns(t, k8sClient, scan.Namespace), run.Name)
	paused.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{
		State:         "Paused",
		BlockedReason: "paused after 2h0m0s timeout — extend maxRuntime to resume",
	}
	paused.Status.Sandbox = &platformv1alpha1.AgentRunSandboxStatus{
		Provider: "agent-sandbox",
		ClaimRef: &platformv1alpha1.NamedRef{Name: "draining-claim"},
	}
	if err := k8sClient.Status().Update(context.Background(), &paused); err != nil {
		t.Fatalf("record paused run with draining sandbox: %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if job := postScriptJob(t, exec, "fp-alpha"); job.State != triggersv1alpha1.SecurityScanPostScriptStateRunning {
		t.Fatalf("paused post-script with a draining sandbox = %#v, want Running", job)
	}
	assertExecutionTaskState(t, exec, "report", 0, triggersv1alpha1.SecurityScanTaskStatePending)

	paused = taskRunByName(t, securityScanRuns(t, k8sClient, scan.Namespace), run.Name)
	paused.Status.Sandbox = nil
	if err := k8sClient.Status().Update(context.Background(), &paused); err != nil {
		t.Fatalf("clear drained sandbox: %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	job := postScriptJob(t, exec, "fp-alpha")
	if job.State != triggersv1alpha1.SecurityScanPostScriptStateFailed || !strings.Contains(job.LastError, "paused after 2h0m0s timeout") {
		t.Fatalf("drained paused post-script = %#v, want terminal failure with the queue timeout reason", job)
	}
	if len(exec.CoverageGaps) != 1 || !strings.Contains(exec.CoverageGaps[0], "paused after 2h0m0s timeout") {
		t.Fatalf("coverage gaps = %#v, want the paused post-script timeout", exec.CoverageGaps)
	}
	assertExecutionTaskState(t, exec, "report", 0, triggersv1alpha1.SecurityScanTaskStateRunning)
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
		t.Fatalf("execution = %#v, want an empty materialized pipeline list without a finding store", exec.PostScriptJobs)
	}
	if len(exec.CoverageGaps) != 1 || !strings.Contains(exec.CoverageGaps[0], "no finding store is configured") {
		t.Fatalf("coverage gaps = %#v, want the missing finding store recorded", exec.CoverageGaps)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
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
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	// A dispatch that never lands still consumes an attempt, so the retry
	// budget runs out instead of re-dispatching forever behind the gate.
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	job := postScriptJob(t, exec, "fp-alpha")
	if job.Attempts != 1 || job.State != triggersv1alpha1.SecurityScanPostScriptStatePending {
		t.Fatalf("job after a failed dispatch = %#v, want attempts 1 still pending for its last retry", job)
	}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	job = postScriptJob(t, exec, "fp-alpha")
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
	if job := postScriptJob(t, exec, "fp-alpha"); job.State != triggersv1alpha1.SecurityScanPostScriptStatePending &&
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
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if len(exec.PostScriptJobs) != 0 {
		t.Fatalf("post-script jobs = %#v, want none for an all-sink workflow", exec.PostScriptJobs)
	}
	if len(exec.CoverageGaps) != 1 || !strings.Contains(exec.CoverageGaps[0], "every workflow task is a terminal (sink) task") {
		t.Fatalf("coverage gaps = %#v, want the un-run post-script pipelines disclosed", exec.CoverageGaps)
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

func TestSecurityScanPostScriptPipelineMembershipCapPaginatesWithoutSplitting(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "validate", RunOn: "high-and-above", Prompt: "Build a proof of concept."},
		{Name: "exploit", RunOn: "high-and-above", Prompt: "Confirm exploitability."},
		{Name: "triage", RunOn: "high-and-above", Prompt: "Assign a final status."},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	findings := &postScriptFindingStore{}
	for i := range triggersv1alpha1.MaxSecurityScanPostScriptJobs + 1 {
		findings.findings = append(findings.findings, postScriptTestFinding(
			fmt.Sprintf("00000000-0000-0000-0000-%012d", i), fmt.Sprintf("fp-low-%04d", i), "low"))
	}
	for i := range 70 {
		id := i + triggersv1alpha1.MaxSecurityScanPostScriptJobs + 1
		findings.findings = append(findings.findings, postScriptTestFinding(
			fmt.Sprintf("00000000-0000-0000-0000-%012d", id), fmt.Sprintf("fp-high-%04d", i), "high"))
	}
	reconciler.Findings = findings

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	wantPageSize := int32(triggersv1alpha1.MaxSecurityScanPostScriptJobs) + 1
	if len(findings.filters) != 2 || findings.filters[0].Limit != wantPageSize || findings.filters[0].Offset != 0 || findings.filters[1].Offset != wantPageSize {
		t.Fatalf("finding filters = %#v, want two %d-row pages at offsets 0 and %d", findings.filters, wantPageSize, wantPageSize)
	}
	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if len(exec.PostScriptJobs) != 66 {
		t.Fatalf("post-script jobs = %d, want 66 whole three-script pipelines under the 200-membership cap", len(exec.PostScriptJobs))
	}
	for _, job := range exec.PostScriptJobs {
		if !slices.Equal(job.Scripts, []string{"validate", "exploit", "triage"}) {
			t.Fatalf("pipeline was split at the membership cap: %#v", job)
		}
	}
	if len(exec.CoverageGaps) != 1 || !strings.Contains(exec.CoverageGaps[0], "post-scripts ran on 66 of 70 eligible findings") ||
		!strings.Contains(exec.CoverageGaps[0], "capped at 200 selected post-scripts") {
		t.Fatalf("coverage gaps = %#v, want the uncovered whole pipelines disclosed", exec.CoverageGaps)
	}
}

func TestSecurityScanPostScriptPipelineSplitsOversizedCombinedPrompts(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	largePrompt := strings.Repeat("x", 280*1024)
	scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{
		{Name: "first", Prompt: largePrompt},
		{Name: "second", Prompt: largePrompt},
		{Name: "third", Prompt: largePrompt},
	}, 2)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	reconciler.Findings = &postScriptFindingStore{findings: []store.SecurityFindingRecord{
		postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "high"),
	}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan) // persist chunks

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if len(exec.PostScriptJobs) != 3 {
		t.Fatalf("post-script jobs = %#v, want three bounded chunks", exec.PostScriptJobs)
	}
	for i, want := range []string{"first", "second", "third"} {
		job := exec.PostScriptJobs[i]
		if job.Order != int32(i) || !slices.Equal(job.Scripts, []string{want}) {
			t.Fatalf("chunk %d = %#v, want order %d containing only %q", i, job, i, want)
		}
	}

	reconcileDeterministicSecurityScan(t, reconciler, scan) // dispatch first chunk only
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.PostScriptJobs[0].State != triggersv1alpha1.SecurityScanPostScriptStateRunning ||
		exec.PostScriptJobs[1].State != triggersv1alpha1.SecurityScanPostScriptStatePending ||
		exec.PostScriptJobs[2].State != triggersv1alpha1.SecurityScanPostScriptStatePending {
		t.Fatalf("oversized chunks did not preserve per-finding order: %#v", exec.PostScriptJobs)
	}
	runCount := 0
	for _, run := range securityScanRuns(t, k8sClient, scan.Namespace) {
		if run.Annotations[triggersv1alpha1.SecurityScanPostScriptAnnotation] != "" {
			runCount++
		}
	}
	if runCount != 1 {
		t.Fatalf("post-script run count = %d, want only the first chunk dispatched", runCount)
	}

	first := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "first", "fp-alpha")
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, first.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.PostScriptJobs[1].State != triggersv1alpha1.SecurityScanPostScriptStateRunning {
		t.Fatalf("second chunk = %#v, want Running after the first settled", exec.PostScriptJobs[1])
	}
	second := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "second", "fp-alpha")
	if second.Name == first.Name {
		t.Fatalf("oversized chunks reused run name %q", first.Name)
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
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseRunning {
		t.Fatalf("execution phase = %q, want Running: the budget must stop dispatch, not fail the execution", exec.Phase)
	}
	if job := postScriptJob(t, exec, "fp-alpha"); job.State != triggersv1alpha1.SecurityScanPostScriptStateRunning {
		t.Fatalf("first job = %#v, want it dispatched inside the budget", job)
	}
	// The over-budget job is terminal, not Pending: pending jobs would hold
	// the sink gate closed forever, since no later pass frees budget.
	beta := postScriptJob(t, exec, "fp-beta")
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
	job := postScriptJob(t, exec, "fp-alpha")
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
	a := securityScanPostScriptRunName("scan", "exec", "pipeline", "00000000-0000-0000-0000-0000000000a1", 1, "")
	b := securityScanPostScriptRunName("scan", "exec", "pipeline", "00000000-0000-0000-0000-0000000000b1", 1, "")
	if a == b {
		t.Fatalf("post-script run names collide across findings: %q", a)
	}
	if retry := securityScanPostScriptRunName("scan", "exec", "pipeline", "00000000-0000-0000-0000-0000000000a1", 2, ""); retry == a {
		t.Fatalf("retry run name = %q, want it distinct from the attempt it replaces", retry)
	}
	if again := securityScanPostScriptRunName("scan", "exec", "pipeline", "00000000-0000-0000-0000-0000000000a1", 1, ""); again != a {
		t.Fatalf("run name is not deterministic: %q != %q", again, a)
	}
	for _, name := range []string{a, b} {
		if len(name) > 63 {
			t.Fatalf("run name %q exceeds 63 characters", name)
		}
	}
}

func TestSecurityScanDeterministicDispatchCreatesOneEagerScanRecord(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "inspect", Objective: "inspect"},
		{Name: "review", Objective: "review"},
	}, 4)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	records := map[string]*store.SecurityScanRecord{}
	reconciler.Findings = securityScanFindingStore{securityScanRecordStubStore: securityScanRecordStubStore{scanRecords: records}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)

	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want two dispatched task runs", len(runs))
	}
	// Exactly ONE eager record — keyed by the execution record name, not a
	// task run — anchors the execution in the scans list.
	execID := getSecurityScan(t, k8sClient, scan).Status.LastExecution.ID
	recordName := securityScanExecutionRecordName(scan.Name, execID)
	if len(records) != 1 {
		t.Fatalf("records = %v, want exactly one eager scan record per execution", records)
	}
	rec := records[scan.Namespace+"/"+recordName]
	if rec == nil {
		t.Fatalf("records = %v, want the record keyed by execution record name %q", records, recordName)
	}
	if rec.ScanName != scan.Name || rec.Status != "running" || rec.StartedAt == nil {
		t.Fatalf("eager record = %+v, want scanName=%q status=running with startedAt", rec, scan.Name)
	}
	// Every task run is stamped with the shared record key so its finding
	// tools report into the same row.
	for _, run := range runs {
		if got := run.Annotations[triggersv1alpha1.SecurityScanRecordNameAnnotation]; got != recordName {
			t.Fatalf("run %q record annotation = %q, want %q", run.Name, got, recordName)
		}
	}
	// Later reconciles never create additional records for the execution.
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	if len(records) != 1 {
		t.Fatalf("records = %v after second reconcile, want still one", records)
	}
}

func TestSecurityScanTerminalExecutionFinalizesScanRecords(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		runPhase platformv1alpha1.AgentRunPhase
		want     string
	}{
		{platformv1alpha1.AgentRunPhaseSucceeded, "completed"},
		{platformv1alpha1.AgentRunPhaseFailed, "failed"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			zero := int32(0)
			scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
				{Name: "inspect", Objective: "inspect", MaxRetries: &zero},
			}, 1)
			reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
			records := map[string]*store.SecurityScanRecord{}
			reconciler.Findings = securityScanFindingStore{securityScanRecordStubStore: securityScanRecordStubStore{scanRecords: records}}

			reconcileDeterministicSecurityScan(t, reconciler, scan)
			runs := securityScanRuns(t, k8sClient, scan.Namespace)
			if len(runs) != 1 {
				t.Fatalf("runs = %d, want one dispatched task run", len(runs))
			}
			// A legacy per-run row (created before the shared record key
			// existed) must also settle at execution end.
			legacyStarted := now
			records[scan.Namespace+"/"+runs[0].Name] = &store.SecurityScanRecord{
				Namespace: scan.Namespace, ScanName: scan.Name, RunName: runs[0].Name,
				Status: "running", StartedAt: &legacyStarted,
			}
			lastError := ""
			if tc.runPhase == platformv1alpha1.AgentRunPhaseFailed {
				lastError = "boom"
			}
			markSecurityScanTaskRun(t, k8sClient, scan.Namespace, runs[0].Name, tc.runPhase, "", lastError)
			reconcileDeterministicSecurityScan(t, reconciler, scan)
			// Records settle on the reconcile AFTER the terminal status was
			// persisted: finishTerminalExecution is the durable terminal
			// path, so a crash in between still converges.
			reconcileDeterministicSecurityScan(t, reconciler, scan)

			exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
			if !securityScanExecutionTerminal(exec.Phase) {
				t.Fatalf("execution phase = %q, want terminal", exec.Phase)
			}
			for _, key := range []string{
				scan.Namespace + "/" + securityScanExecutionRecordName(scan.Name, exec.ID),
				scan.Namespace + "/" + runs[0].Name,
			} {
				rec := records[key]
				if rec == nil {
					t.Fatalf("no scan record %q; records = %v", key, records)
				}
				// Rows still "running" inherit the execution outcome so
				// none lingers as running once the execution is over.
				if rec.Status != tc.want || rec.CompletedAt == nil {
					t.Fatalf("record %q = %+v, want status=%q with completedAt", key, rec, tc.want)
				}
			}
		})
	}
}

func TestSecurityScanResumeReopensFinalizedScanRecord(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	zero := int32(0)
	scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{
		{Name: "inspect", Objective: "inspect", MaxRetries: &zero},
	}, 1)
	reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
	records := map[string]*store.SecurityScanRecord{}
	reconciler.Findings = securityScanFindingStore{securityScanRecordStubStore: securityScanRecordStubStore{scanRecords: records}}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, runs[0].Name, platformv1alpha1.AgentRunPhaseFailed, "", "boom")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan) // durable terminal path settles the record

	exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
	key := scan.Namespace + "/" + securityScanExecutionRecordName(scan.Name, exec.ID)
	if rec := records[key]; rec == nil || rec.Status != "failed" || rec.CompletedAt == nil {
		t.Fatalf("record before resume = %+v, want finalized failed record", records[key])
	}

	updated := getSecurityScan(t, k8sClient, scan)
	updated.Annotations = map[string]string{triggersv1alpha1.SecurityScanResumeAnnotation: "resume-1"}
	if err := k8sClient.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update(SecurityScan resume annotation): %v", err)
	}
	reconcileDeterministicSecurityScan(t, reconciler, scan)

	// The resumed execution is running again, so its scans-list row must
	// not keep reading "failed" — and must be re-finalizable later.
	rec := records[key]
	if rec == nil || rec.Status != "running" || rec.CompletedAt != nil {
		t.Fatalf("record after resume = %+v, want reopened running record", rec)
	}

	reconcileDeterministicSecurityScan(t, reconciler, scan)
	resumedRun := executionTask(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, "inspect", 0).RunName
	markSecurityScanTaskRun(t, k8sClient, scan.Namespace, resumedRun, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
	reconcileDeterministicSecurityScan(t, reconciler, scan)
	reconcileDeterministicSecurityScan(t, reconciler, scan) // durable terminal path settles the record
	if rec := records[key]; rec == nil || rec.Status != "completed" || rec.CompletedAt == nil {
		t.Fatalf("record after resumed success = %+v, want completed", records[key])
	}
}

func TestSecurityScanCoordinatorRunCreatesEagerScanRecord(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
	records := map[string]*store.SecurityScanRecord{}
	reconciler.Findings = securityScanFindingStore{securityScanRecordStubStore: securityScanRecordStubStore{scanRecords: records}}

	for i := 0; i < 3; i++ {
		if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if getSecurityScan(t, k8sClient, scan).Status.LastRunName != "" {
			break
		}
	}
	runName := getSecurityScan(t, k8sClient, scan).Status.LastRunName
	if runName == "" {
		t.Fatal("coordinator run was not created")
	}
	rec := records[scan.Namespace+"/"+runName]
	if rec == nil {
		t.Fatalf("no eager scan record for coordinator run %q; records = %v", runName, records)
	}
	if rec.ScanName != scan.Name || rec.Status != "running" || rec.StartedAt == nil {
		t.Fatalf("eager record = %+v, want scanName=%q status=running with startedAt", rec, scan.Name)
	}
}

func TestSecurityScanTerminalRunFinalizesEagerScanRecord(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		phase platformv1alpha1.AgentRunPhase
		want  string
	}{
		{platformv1alpha1.AgentRunPhaseFailed, "failed"},
		{platformv1alpha1.AgentRunPhaseCancelled, "cancelled"},
		{platformv1alpha1.AgentRunPhaseSucceeded, "completed"},
	} {
		t.Run(string(tc.phase), func(t *testing.T) {
			scan := securityScanTestScan()
			reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
			records := map[string]*store.SecurityScanRecord{}
			reconciler.Findings = securityScanFindingStore{securityScanRecordStubStore: securityScanRecordStubStore{scanRecords: records}}

			if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			runName := getSecurityScan(t, k8sClient, scan).Status.LastRunName
			if runName == "" {
				t.Fatal("coordinator run was not created")
			}
			runs := securityScanRuns(t, k8sClient, scan.Namespace)
			run := runs[0].DeepCopy()
			run.Status.Phase = tc.phase
			if err := k8sClient.Status().Update(context.Background(), run); err != nil {
				t.Fatalf("Status().Update(AgentRun) error = %v", err)
			}
			if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
				t.Fatalf("post-run Reconcile() error = %v", err)
			}

			rec := records[scan.Namespace+"/"+runName]
			if rec == nil {
				t.Fatalf("no scan record for run %q", runName)
			}
			// The eager "running" row is finalized when the run terminates
			// without submitting a report, so it never lingers as running.
			if rec.Status != tc.want || rec.CompletedAt == nil {
				t.Fatalf("record = %+v, want status=%q with completedAt", rec, tc.want)
			}

			// A record the run already finalized (report submitted) is
			// never clobbered by later reconciles.
			done := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
			rec.Status = "completed"
			rec.Summary = "report"
			rec.CompletedAt = &done
			if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
				t.Fatalf("re-Reconcile() error = %v", err)
			}
			if got := records[scan.Namespace+"/"+runName]; got.Summary != "report" || !got.CompletedAt.Equal(done) {
				t.Fatalf("record = %+v, want report-written record preserved", got)
			}
		})
	}
}
