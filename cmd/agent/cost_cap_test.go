package main

import (
	"strconv"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

func TestCostCapUSD(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		run    *platformv1alpha1.AgentRun
		want   float64
		wantOK bool
	}{
		{name: "nil run", run: nil},
		{name: "no limits", run: &platformv1alpha1.AgentRun{}},
		{name: "empty cap", run: runWithCap("")},
		{name: "invalid cap", run: runWithCap("abc")},
		{name: "NaN cap", run: runWithCap("NaN")},
		{name: "infinite cap", run: runWithCap("+Inf")},
		{name: "zero cap", run: runWithCap("0")},
		{name: "negative cap", run: runWithCap("-2")},
		{name: "integer cap", run: runWithCap("5"), want: 5, wantOK: true},
		{name: "decimal cap", run: runWithCap("2.50"), want: 2.5, wantOK: true},
		{name: "whitespace cap", run: runWithCap(" 1.25 "), want: 1.25, wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := costCapUSD(tc.run)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("costCapUSD() = (%v, %v), want (%v, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestInvalidConfiguredCostCapFailsClosed(t *testing.T) {
	for _, raw := range []string{"abc", "NaN", "+Inf", "0", "-1"} {
		if _, configured, err := validatedCostCapUSD(runWithCap(raw)); !configured || err == nil {
			t.Errorf("validatedCostCapUSD(%q) = configured=%v err=%v, want configured error", raw, configured, err)
		}
	}
}

func TestBaselineCostUSD(t *testing.T) {
	t.Parallel()
	if got := baselineCostUSD(nil); got != 0 {
		t.Fatalf("nil run baseline = %v, want 0", got)
	}
	if got := baselineCostUSD(&platformv1alpha1.AgentRun{}); got != 0 {
		t.Fatalf("no metrics baseline = %v, want 0", got)
	}
	run := &platformv1alpha1.AgentRun{}
	run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{CostUsd: "3.1415"}
	if got := baselineCostUSD(run); got != 3.1415 {
		t.Fatalf("baseline = %v, want 3.1415", got)
	}
	run.Status.Metrics.CostUsd = "garbage"
	if got := baselineCostUSD(run); got != 0 {
		t.Fatalf("invalid metrics baseline = %v, want 0", got)
	}
}

func TestCumulativeProgressMetricsDoNotCompoundAcrossTicks(t *testing.T) {
	baseline := progressMetricsBaseline{CostUSD: 1.25, InputTokens: 100, OutputTokens: 20, ToolCallCount: 3}
	snap := agent.ProgressSnapshot{CostUsd: 0.5, InputTokens: 40, OutputTokens: 10, ToolCallCount: 2}

	first := cumulativeProgressMetrics(baseline, snap)
	second := cumulativeProgressMetrics(baseline, snap)
	want := progressMetricsBaseline{CostUSD: 1.75, InputTokens: 140, OutputTokens: 30, ToolCallCount: 5}
	if first != want || second != want {
		t.Fatalf("repeated ticks = %#v, %#v; want %#v", first, second, want)
	}

	currentContextTokens.Store(777)
	currentContextBudget.Store(&contextBudget{TriggerTokens: 900, TargetTokens: 600})
	t.Cleanup(func() {
		currentContextTokens.Store(0)
		currentContextBudget.Store(nil)
	})
	metrics := sessionMetricsFromSnapshot(baseline, snap)
	if metrics.CostUSD != want.CostUSD || metrics.InputTokens != want.InputTokens || metrics.OutputTokens != want.OutputTokens || metrics.ToolCallCount != want.ToolCallCount {
		t.Fatalf("session metrics = %#v, want cumulative %#v", metrics, want)
	}
	if metrics.ContextTokens != 777 || metrics.ContextTriggerTokens != 900 || metrics.ContextTargetTokens != 600 {
		t.Fatalf("context metrics = %#v, want current gauges", metrics)
	}
}

func TestCumulativeProgressMetricsAcrossThreePods(t *testing.T) {
	persisted := progressMetricsBaseline{}
	for pod := 0; pod < 3; pod++ {
		run := &platformv1alpha1.AgentRun{}
		run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{
			CostUsd:       formatCost(persisted.CostUSD),
			InputTokens:   persisted.InputTokens,
			OutputTokens:  persisted.OutputTokens,
			ToolCallCount: persisted.ToolCallCount,
		}
		baseline := progressMetricsBaselineFromRun(run)
		persisted = cumulativeProgressMetrics(baseline, agent.ProgressSnapshot{
			CostUsd: 0.25, InputTokens: 10, OutputTokens: 4, ToolCallCount: 1,
		})
	}
	want := progressMetricsBaseline{CostUSD: 0.75, InputTokens: 30, OutputTokens: 12, ToolCallCount: 3}
	if persisted != want {
		t.Fatalf("three-pod metrics = %#v, want %#v", persisted, want)
	}
}

func formatCost(cost float64) string {
	return strconv.FormatFloat(cost, 'f', 4, 64)
}

func runWithCap(capUSD string) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Limits: &platformv1alpha1.AgentRunLimits{MaxCostUsd: capUSD},
		},
	}
}

func TestEffectiveAllowedMutatingTools(t *testing.T) {
	t.Parallel()

	explicit := &platformv1alpha1.AgentRun{}
	explicit.Status.ModeSnapshot = &platformv1alpha1.ModeTemplateSpec{
		AllowedMutatingTools: []string{"custom_output"},
	}
	got := effectiveAllowedMutatingTools(explicit)
	if len(got) != 1 || got[0] != "custom_output" {
		t.Fatalf("explicit allowed tools = %v, want [custom_output]", got)
	}
	got[0] = "mutated_copy"
	if explicit.Status.ModeSnapshot.AllowedMutatingTools[0] != "custom_output" {
		t.Fatal("effective allowlist aliases the mode snapshot")
	}

	legacy := &platformv1alpha1.AgentRun{}
	legacy.Status.ModeName = reviewerModeName
	got = effectiveAllowedMutatingTools(legacy)
	want := map[string]bool{
		"submit_pull_request_review": true,
		"reply_to_review_thread":     true,
		"resolve_review_thread":      true,
		"request_re_review":          true,
		"submit_review_verdict":      true,
	}
	if len(got) != len(want) {
		t.Fatalf("legacy reviewer allowed tools = %v", got)
	}
	for _, name := range got {
		if !want[name] {
			t.Fatalf("legacy reviewer unexpectedly allows %q: %v", name, got)
		}
	}

	if got := effectiveAllowedMutatingTools(&platformv1alpha1.AgentRun{}); got != nil {
		t.Fatalf("ordinary run allowed tools = %v, want nil", got)
	}

	switched := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			ModeRef: &platformv1alpha1.ModeRef{Name: reviewerModeName},
		},
	}
	switched.Status.ModeName = "plan"
	switched.Status.ModeSnapshot = &platformv1alpha1.ModeTemplateSpec{Name: "plan"}
	if got := effectiveAllowedMutatingTools(switched); got != nil {
		t.Fatalf("run switched from review to plan retained review tools: %v", got)
	}
}

func TestSnapshotPermissionClampHelpers(t *testing.T) {
	t.Parallel()
	if got := snapshotPermissionMode(nil); got != "" {
		t.Fatalf("nil run snapshot mode = %q, want empty", got)
	}
	run := &platformv1alpha1.AgentRun{}
	if got := snapshotPermissionMode(run); got != "" {
		t.Fatalf("no snapshot mode = %q, want empty", got)
	}
	run.Status.ModeSnapshot = &platformv1alpha1.ModeTemplateSpec{
		PermissionMode:       platformv1alpha1.PermissionModeReadOnly,
		AllowedMutatingTools: []string{"submit_pull_request_review"},
	}
	if got := snapshotPermissionMode(run); got != platformv1alpha1.PermissionModeReadOnly {
		t.Fatalf("snapshot mode = %q, want read-only", got)
	}
	if tools := snapshotAllowedMutatingTools(run); len(tools) != 1 || tools[0] != "submit_pull_request_review" {
		t.Fatalf("allowed tools = %v", tools)
	}
}
