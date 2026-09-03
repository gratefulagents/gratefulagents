package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

func TestEarlyStopCoverageGap(t *testing.T) {
	cases := []struct {
		name       string
		conditions map[string]any
		toolCalls  int32
		maxTurns   int32
		want       string
	}{
		{
			name:       "under threshold without exhaustion",
			conditions: map[string]any{"ready": true, "surface_exhausted": false, "hypotheses_examined": float64(6)},
			toolCalls:  60, maxTurns: 250,
			want: `task "hunt" stopped early: 60 of 250 turns used without surface_exhausted`,
		},
		{
			name:       "exactly at threshold is not early",
			conditions: map[string]any{"surface_exhausted": false, "hypotheses_examined": float64(6)},
			toolCalls:  100, maxTurns: 250,
		},
		{
			name:       "surface exhausted",
			conditions: map[string]any{"surface_exhausted": true},
			toolCalls:  10, maxTurns: 250,
		},
		{
			name:       "static mode",
			conditions: map[string]any{"static_mode": true},
			toolCalls:  10, maxTurns: 250,
		},
		{
			name:       "unknown max turns",
			conditions: map[string]any{},
			toolCalls:  10, maxTurns: 0,
		},
		{
			name:       "budget below minimum",
			conditions: map[string]any{},
			toolCalls:  5, maxTurns: 49,
		},
		{
			name:       "budget at minimum",
			conditions: map[string]any{"hypotheses_examined": float64(6)},
			toolCalls:  5, maxTurns: 50,
			want: `task "hunt" stopped early: 5 of 50 turns used without surface_exhausted`,
		},
		{
			name:       "missing surface_exhausted treated as not exhausted",
			conditions: map[string]any{"ready": true, "hypotheses_examined": float64(2)},
			toolCalls:  0, maxTurns: 100,
			want: `task "hunt" stopped early: 0 of 100 turns used without surface_exhausted`,
		},
		{
			name:       "short-by-design handoff without breadth counters",
			conditions: map[string]any{"ready": true, "report_ready": true},
			toolCalls:  5, maxTurns: 250,
		},
		{
			name:       "blocked handoff already carries its blocker",
			conditions: map[string]any{"ready": false, "reason": "runtime_preflight_blocked", "hypotheses_examined": float64(0)},
			toolCalls:  0, maxTurns: 250,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := earlyStopCoverageGap("hunt", tc.conditions, tc.toolCalls, tc.maxTurns)
			if ok != (tc.want != "") || got != tc.want {
				t.Fatalf("earlyStopCoverageGap = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.want != "")
			}
		})
	}
}

func TestReconcileHandoffCounters(t *testing.T) {
	cases := []struct {
		name        string
		conditions  map[string]any
		hypotheses  int
		experiments int
		want        []string
	}{
		{
			name:        "both counters inflated",
			conditions:  map[string]any{"hypotheses_examined": float64(6), "dynamic_experiments": float64(3)},
			hypotheses:  2,
			experiments: 0,
			want: []string{
				`task "hunt" reported 6 hypotheses_examined but 2 durable hypothesis record(s) exist`,
				`task "hunt" reported 3 dynamic_experiments but 0 durable dynamic experiment record(s) exist`,
			},
		},
		{
			name:        "counters match",
			conditions:  map[string]any{"hypotheses_examined": float64(2), "dynamic_experiments": float64(1)},
			hypotheses:  2,
			experiments: 1,
		},
		{
			name:        "durable exceeds reported",
			conditions:  map[string]any{"hypotheses_examined": float64(1)},
			hypotheses:  4,
			experiments: -1,
		},
		{
			name:        "evidence unreadable is skipped",
			conditions:  map[string]any{"hypotheses_examined": float64(6), "dynamic_experiments": float64(3)},
			hypotheses:  -1,
			experiments: -1,
		},
		{
			name:        "counters absent",
			conditions:  map[string]any{"ready": true},
			hypotheses:  0,
			experiments: 0,
		},
		{
			name:        "non-numeric counter ignored",
			conditions:  map[string]any{"hypotheses_examined": "six"},
			hypotheses:  0,
			experiments: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reconcileHandoffCounters("hunt", tc.conditions, tc.hypotheses, tc.experiments)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("reconcileHandoffCounters = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSecurityScanDynamicExperimentCount(t *testing.T) {
	coverage := []store.SecurityResearchCoverage{
		{Bounds: json.RawMessage(`{"experiment":{"kind":"new_test"}}`)},
		{Bounds: json.RawMessage(`{"experiment":{"kind":"fuzz","target":"x"}}`)},
		{Bounds: json.RawMessage(`{"experiment":{"kind":"read_code"}}`)},
		{Bounds: json.RawMessage(`{"scope":"static"}`)},
		{Bounds: json.RawMessage(`{"experiment":"mutant"}`)},
		{Bounds: nil},
		{Bounds: json.RawMessage(`not json`)},
	}
	if got := securityScanDynamicExperimentCount(coverage); got != 2 {
		t.Fatalf("dynamic experiment count = %d, want 2", got)
	}
}

func TestSecurityScanHandoffConditions(t *testing.T) {
	if _, ok := securityScanHandoffConditions(`{"version":2,"conditions":{"ready":true}}`); ok {
		t.Fatal("version 2 handoff must not yield conditions")
	}
	if _, ok := securityScanHandoffConditions(`{"version":1}`); ok {
		t.Fatal("handoff without conditions must not yield conditions")
	}
	if _, ok := securityScanHandoffConditions(`[{"recordIndex":0,"result":{"version":1,"conditions":{}}}]`); ok {
		t.Fatal("chunk envelope must not yield conditions")
	}
	conditions, ok := securityScanHandoffConditions(`{"version":1,"conditions":{"ready":true,"hypotheses_examined":6}}`)
	if !ok || conditions["ready"] != true {
		t.Fatalf("conditions = %#v, %v", conditions, ok)
	}
}

func TestSecurityScanEffectiveMaxTurns(t *testing.T) {
	run := &platformv1alpha1.AgentRun{}
	if got := securityScanEffectiveMaxTurns(run); got != 0 {
		t.Fatalf("bare run max turns = %d, want 0", got)
	}
	run.Status.ModeSnapshot = &platformv1alpha1.ModeTemplateSpec{Constraints: &platformv1alpha1.ModeConstraints{MaxTurns: 250}}
	if got := securityScanEffectiveMaxTurns(run); got != 250 {
		t.Fatalf("mode snapshot max turns = %d, want 250", got)
	}
	run.Spec.Limits = &platformv1alpha1.AgentRunLimits{MaxTurns: 120}
	if got := securityScanEffectiveMaxTurns(run); got != 120 {
		t.Fatalf("spec limit max turns = %d, want 120", got)
	}
}

type fakeResearchEvidenceStore struct {
	store.StateStore
	hypotheses map[string]int
	coverage   map[string][]store.SecurityResearchCoverage
	err        error
	actors     []string
}

func (f *fakeResearchEvidenceStore) CountSecurityResearchHypothesesByActor(_ context.Context, _ string, actor string) (int, error) {
	f.actors = append(f.actors, actor)
	return f.hypotheses[actor], f.err
}

func (f *fakeResearchEvidenceStore) ListSecurityResearchCoverageByActor(_ context.Context, _ string, actor string) ([]store.SecurityResearchCoverage, error) {
	f.actors = append(f.actors, actor)
	return f.coverage[actor], f.err
}

func TestRecordHandoffConditionGapsReconcilesAgainstDurableEvidence(t *testing.T) {
	evidence := &fakeResearchEvidenceStore{
		hypotheses: map[string]int{"run-a": 2},
		coverage: map[string][]store.SecurityResearchCoverage{"run-a": {
			{Actor: "run-a", Bounds: json.RawMessage(`{"experiment":{"kind":"mutant"}}`)},
			{Actor: "run-a", Bounds: json.RawMessage(`{"scope":"read"}`)},
		}},
	}
	exec := &triggersv1alpha1.SecurityScanExecutionStatus{}
	engine := &securityScanExecutionEngine{
		r:    &SecurityScanReconciler{StateStore: evidence},
		scan: &triggersv1alpha1.SecurityScan{ObjectMeta: metav1.ObjectMeta{Namespace: "sec"}},
		exec: exec,
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run-a", Namespace: "sec"}}
	run.Spec.Limits = &platformv1alpha1.AgentRunLimits{MaxTurns: 250}
	run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{ToolCallCount: 70}
	run.Status.StructuredOutput = `{"version":1,"artifact_ids":[],"conditions":{"ready":true,"hypotheses_examined":6,"dynamic_experiments":3,"experiment_methods":2,"surface_exhausted":false}}`

	engine.recordHandoffConditionGaps(context.Background(), &triggersv1alpha1.SecurityScanTaskExecutionStatus{Name: "hunt", RunName: "run-a"}, run)

	want := []string{
		`task "hunt" stopped early: 70 of 250 turns used without surface_exhausted`,
		`task "hunt" reported 6 hypotheses_examined but 2 durable hypothesis record(s) exist`,
		`task "hunt" reported 3 dynamic_experiments but 1 durable dynamic experiment record(s) exist`,
	}
	if !reflect.DeepEqual(exec.CoverageGaps, want) {
		t.Fatalf("coverage gaps = %#v, want %#v", exec.CoverageGaps, want)
	}
	if !reflect.DeepEqual(evidence.actors, []string{"run-a", "run-a"}) {
		t.Fatalf("evidence queried for actors %#v, want the run name twice", evidence.actors)
	}
}

func TestRecordHandoffConditionGapsWithoutEvidenceStoreOnlyChecksTurns(t *testing.T) {
	exec := &triggersv1alpha1.SecurityScanExecutionStatus{}
	engine := &securityScanExecutionEngine{
		r:    &SecurityScanReconciler{},
		scan: &triggersv1alpha1.SecurityScan{ObjectMeta: metav1.ObjectMeta{Namespace: "sec"}},
		exec: exec,
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run-b"}}
	run.Status.ModeSnapshot = &platformv1alpha1.ModeTemplateSpec{Constraints: &platformv1alpha1.ModeConstraints{MaxTurns: 250}}
	run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{ToolCallCount: 240}
	run.Status.StructuredOutput = `{"version":1,"conditions":{"hypotheses_examined":9,"surface_exhausted":false}}`

	engine.recordHandoffConditionGaps(context.Background(), &triggersv1alpha1.SecurityScanTaskExecutionStatus{Name: "hunt"}, run)
	if len(exec.CoverageGaps) != 0 {
		t.Fatalf("coverage gaps = %#v, want none without an evidence store and with most turns used", exec.CoverageGaps)
	}
}

func TestRecordHandoffConditionGapsSkipsCountersWhenEvidenceReadFails(t *testing.T) {
	evidence := &fakeResearchEvidenceStore{err: errors.New("db down")}
	exec := &triggersv1alpha1.SecurityScanExecutionStatus{}
	engine := &securityScanExecutionEngine{
		r:    &SecurityScanReconciler{StateStore: evidence},
		scan: &triggersv1alpha1.SecurityScan{ObjectMeta: metav1.ObjectMeta{Namespace: "sec"}},
		exec: exec,
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run-c"}}
	run.Status.StructuredOutput = `{"version":1,"conditions":{"hypotheses_examined":9,"dynamic_experiments":4,"surface_exhausted":true}}`

	engine.recordHandoffConditionGaps(context.Background(), &triggersv1alpha1.SecurityScanTaskExecutionStatus{Name: "hunt"}, run)
	if len(exec.CoverageGaps) != 0 {
		t.Fatalf("coverage gaps = %#v, want none when durable evidence cannot be read", exec.CoverageGaps)
	}
}
