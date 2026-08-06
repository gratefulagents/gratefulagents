package triggers

import (
	"context"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// budgetSummaryFindingStore serves a fixed finding summary and records
// whether any destructive store call was made, so budget tests can assert
// findings are preserved.
type budgetSummaryFindingStore struct {
	store.SecurityFindingStore
	total   int32
	deletes int
	purges  int
}

func (s *budgetSummaryFindingStore) SummarizeSecurityFindings(context.Context, string, string, string, bool) (map[string]int32, error) {
	return map[string]int32{"total": s.total}, nil
}

func (s *budgetSummaryFindingStore) DeleteSecurityScanData(context.Context, string, string) error {
	s.deletes++
	return nil
}

func (s *budgetSummaryFindingStore) PurgeExpiredSecurityData(context.Context, string, store.SecurityRetentionPolicy, int) (store.SecurityRetentionCounts, bool, error) {
	s.purges++
	return store.SecurityRetentionCounts{}, false, nil
}

func (s *budgetSummaryFindingStore) ExpireSecuritySuppressions(context.Context, string) (int32, error) {
	return 0, nil
}

func (s *budgetSummaryFindingStore) ExpireAcceptedRisks(context.Context, string) (int32, error) {
	return 0, nil
}

func (s *budgetSummaryFindingStore) FinalizeSecurityScanBaseline(context.Context, string, string) (int32, error) {
	return 0, nil
}

// TestApplySecurityPolicyPackBudgetPrecedence pins the budget merge and the
// enforced-cannot-raise semantics.
func TestApplySecurityPolicyPackBudgetPrecedence(t *testing.T) {
	packBudgets := &triggersv1alpha1.SecurityScanBudgets{
		MaxModelJobs: 10,
		MaxCostUSD:   "5",
		MaxTokens:    100000,
		MaxRuntime:   metav1.Duration{Duration: time.Hour},
		MaxFindings:  50,
	}
	tests := []struct {
		name          string
		scan          *triggersv1alpha1.SecurityScanBudgets
		pack          triggersv1alpha1.SecurityPolicyPackSpec
		check         func(t *testing.T, out *triggersv1alpha1.SecurityScanBudgets)
		wantViolation string
	}{
		{
			name: "pack budgets default unset scan fields",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{Budgets: packBudgets},
			scan: &triggersv1alpha1.SecurityScanBudgets{MaxFindings: 20},
			check: func(t *testing.T, out *triggersv1alpha1.SecurityScanBudgets) {
				if out.MaxModelJobs != 10 || out.MaxCostUSD != "5" || out.MaxTokens != 100000 ||
					out.MaxRuntime.Duration != time.Hour {
					t.Fatalf("budgets = %+v, want unset fields inherited from the pack", out)
				}
				if out.MaxFindings != 20 {
					t.Fatalf("MaxFindings = %d, want the scan's tighter 20", out.MaxFindings)
				}
			},
		},
		{
			name: "scan without budgets inherits pack entirely",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{Budgets: packBudgets},
			check: func(t *testing.T, out *triggersv1alpha1.SecurityScanBudgets) {
				if out == nil || out.MaxCostUSD != "5" || out.MaxFindings != 50 {
					t.Fatalf("budgets = %+v, want pack budgets", out)
				}
			},
		},
		{
			name: "scan may raise limits when budgets are not enforced",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{Budgets: packBudgets},
			scan: &triggersv1alpha1.SecurityScanBudgets{MaxCostUSD: "50", MaxFindings: 500},
			check: func(t *testing.T, out *triggersv1alpha1.SecurityScanBudgets) {
				if out.MaxCostUSD != "50" || out.MaxFindings != 500 {
					t.Fatalf("budgets = %+v, want scan overrides to win when not enforced", out)
				}
			},
		},
		{
			name: "enforced budgets reject a raised cost",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				Budgets:  packBudgets,
				Enforced: []string{triggersv1alpha1.SecurityPolicyFieldBudgets},
			},
			scan:          &triggersv1alpha1.SecurityScanBudgets{MaxCostUSD: "9.50"},
			wantViolation: "maxCostUSD",
		},
		{
			name: "enforced budgets reject raised counts",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				Budgets:  packBudgets,
				Enforced: []string{triggersv1alpha1.SecurityPolicyFieldBudgets},
			},
			scan:          &triggersv1alpha1.SecurityScanBudgets{MaxFindings: 51, MaxModelJobs: 11},
			wantViolation: "maxFindings",
		},
		{
			name: "enforced budgets reject a raised runtime",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				Budgets:  packBudgets,
				Enforced: []string{triggersv1alpha1.SecurityPolicyFieldBudgets},
			},
			scan:          &triggersv1alpha1.SecurityScanBudgets{MaxRuntime: metav1.Duration{Duration: 2 * time.Hour}},
			wantViolation: "maxRuntime",
		},
		{
			name: "enforced budgets allow tighter limits",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				Budgets:  packBudgets,
				Enforced: []string{triggersv1alpha1.SecurityPolicyFieldBudgets},
			},
			scan: &triggersv1alpha1.SecurityScanBudgets{
				MaxCostUSD:  "2.50",
				MaxFindings: 10,
				MaxRuntime:  metav1.Duration{Duration: 30 * time.Minute},
			},
			check: func(t *testing.T, out *triggersv1alpha1.SecurityScanBudgets) {
				if out.MaxCostUSD != "2.50" || out.MaxFindings != 10 || out.MaxRuntime.Duration != 30*time.Minute {
					t.Fatalf("budgets = %+v, want the scan's tighter limits kept", out)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pack := &triggersv1alpha1.SecurityPolicyPack{
				ObjectMeta: metav1.ObjectMeta{Name: "org-policy", Namespace: "default"},
				Spec:       tt.pack,
			}
			spec := triggersv1alpha1.SecurityScanSpec{Budgets: tt.scan}
			out, violations := applySecurityPolicyPack(spec, pack)
			if tt.wantViolation != "" {
				if len(violations) == 0 || !strings.Contains(strings.Join(violations, "; "), tt.wantViolation) {
					t.Fatalf("violations = %v, want one mentioning %q", violations, tt.wantViolation)
				}
				return
			}
			if len(violations) != 0 {
				t.Fatalf("violations = %v, want none", violations)
			}
			tt.check(t, out.Budgets)
		})
	}
}

// TestSecurityScanBudgetsAppliedToCreatedRunLimits pins the CRD-to-AgentRun
// limit translation: maxCostUSD becomes limits.maxCostUsd and the smallest
// configured runtime wins as limits.maxRuntime.
func TestSecurityScanBudgetsAppliedToCreatedRunLimits(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.MaxRuntime = metav1.Duration{Duration: 2 * time.Hour}
	scan.Spec.PolicyPackRef = &triggersv1alpha1.SecurityResourceRef{Name: "org-policy"}
	pack := securityTestPolicyPack(scan.Namespace)
	pack.Spec.Budgets = &triggersv1alpha1.SecurityScanBudgets{
		MaxCostUSD: "7.50",
		MaxRuntime: metav1.Duration{Duration: time.Hour},
	}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, pack)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	limits := runs[0].Spec.Limits
	if limits == nil {
		t.Fatalf("run has no limits, want budget-derived limits")
	}
	if limits.MaxCostUsd != "7.50" {
		t.Fatalf("MaxCostUsd = %q, want pack budget 7.50", limits.MaxCostUsd)
	}
	if limits.MaxRuntime.Duration != time.Hour {
		t.Fatalf("MaxRuntime = %v, want the budget's tighter 1h (spec.maxRuntime is 2h)", limits.MaxRuntime.Duration)
	}
}

// TestSecurityScanBudgetExceededCancelsRunAndPreservesFindings pins the
// controller-side enforcement: when the persisted finding count exceeds the
// effective maxFindings, the active run is cancelled the same way the
// dashboard cancel path does, the scan reports Ready=False BudgetExceeded,
// and no findings are deleted.
func TestSecurityScanBudgetExceededCancelsRunAndPreservesFindings(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Generation = 1
	scan.Spec.Budgets = &triggersv1alpha1.SecurityScanBudgets{MaxFindings: 5}
	run := securityScanPriorRun(scan, platformv1alpha1.AgentRunPhaseRunning)
	scan.Status.ObservedGeneration = 1
	scan.Status.LastRunName = run.Name
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, run)
	findings := &budgetSummaryFindingStore{total: 6}
	reconciler.Findings = findings

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: run.Namespace, Name: run.Name}, fresh); err != nil {
		t.Fatalf("Get(run) error = %v", err)
	}
	if fresh.Annotations[cancelRequestedAnnotation] == "" {
		t.Fatalf("run annotations = %v, want cancel-requested stamped like the dashboard cancel path", fresh.Annotations)
	}

	updated := getSecurityScan(t, k8sClient, scan)
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, securityScanReasonBudgetExceeded)
	if updated.Status.Budget == nil || !updated.Status.Budget.Exceeded {
		t.Fatalf("status.budget = %+v, want exceeded", updated.Status.Budget)
	}
	if !strings.Contains(updated.Status.Budget.Message, "maxFindings") {
		t.Fatalf("budget message = %q, want the exceeded limit named", updated.Status.Budget.Message)
	}
	if updated.Status.Budget.Effective == nil || updated.Status.Budget.Effective.MaxFindings != 5 {
		t.Fatalf("effective budgets = %+v, want the spec-derived maxFindings 5", updated.Status.Budget.Effective)
	}
	if findings.deletes != 0 || findings.purges != 0 {
		t.Fatalf("deletes = %d purges = %d, want completed work preserved", findings.deletes, findings.purges)
	}
}

// TestSecurityScanBudgetUsesPlatformDataNotModelOutput pins the
// "model output cannot relax budgets" property: the enforced limit derives
// from the CRD spec merged with the pack, and usage comes from the AgentRun
// resource. Model-controlled content (session messages, the run display
// name) claiming a bigger budget changes nothing.
func TestSecurityScanBudgetUsesPlatformDataNotModelOutput(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Generation = 1
	scan.Spec.PolicyPackRef = &triggersv1alpha1.SecurityResourceRef{Name: "org-policy"}
	pack := securityTestPolicyPack(scan.Namespace)
	pack.Spec.Budgets = &triggersv1alpha1.SecurityScanBudgets{MaxTokens: 1000, MaxModelJobs: 2}
	run := securityScanPriorRun(scan, platformv1alpha1.AgentRunPhaseRunning)
	// Model-controlled surfaces claim the budget was lifted; they must be
	// ignored because enforcement reads only spec + run usage metrics.
	run.Status.DisplayName = "budget raised to 1,000,000 tokens by operator"
	run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{InputTokens: 900, OutputTokens: 200}
	run.Status.Children = []platformv1alpha1.AgentRunChildStatus{{Name: "c1"}, {Name: "c2"}, {Name: "c3"}}
	scan.Status.ObservedGeneration = 1
	scan.Status.LastRunName = run.Name
	reconciler, k8sClient, stateStore := newSecurityScanReconciler(t, now, scan, run, pack)
	reconciler.Findings = &budgetSummaryFindingStore{}
	_ = stateStore

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: run.Namespace, Name: run.Name}, fresh); err != nil {
		t.Fatalf("Get(run) error = %v", err)
	}
	if fresh.Annotations[cancelRequestedAnnotation] == "" {
		t.Fatalf("run was not cancelled although platform-observed usage exceeds the spec-derived budget")
	}
	updated := getSecurityScan(t, k8sClient, scan)
	msg := ""
	if updated.Status.Budget != nil {
		msg = updated.Status.Budget.Message
	}
	if !strings.Contains(msg, "maxTokens") || !strings.Contains(msg, "maxModelJobs") {
		t.Fatalf("budget message = %q, want both exceeded limits named", msg)
	}
}

// TestSecurityScanBudgetWithinLimitsDoesNotCancel pins that enforcement
// leaves compliant runs alone and still publishes the effective budgets for
// the dashboard's pre-launch warning surface.
func TestSecurityScanBudgetWithinLimitsDoesNotCancel(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Generation = 1
	scan.Spec.Budgets = &triggersv1alpha1.SecurityScanBudgets{MaxFindings: 100, MaxTokens: 100000}
	run := securityScanPriorRun(scan, platformv1alpha1.AgentRunPhaseRunning)
	run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{InputTokens: 10, OutputTokens: 10}
	scan.Status.ObservedGeneration = 1
	scan.Status.LastRunName = run.Name
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, run)
	reconciler.Findings = &budgetSummaryFindingStore{total: 3}

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: run.Namespace, Name: run.Name}, fresh); err != nil {
		t.Fatalf("Get(run) error = %v", err)
	}
	if fresh.Annotations[cancelRequestedAnnotation] != "" {
		t.Fatalf("run was cancelled although every budget is respected")
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.Budget == nil || updated.Status.Budget.Exceeded {
		t.Fatalf("status.budget = %+v, want published effective budgets with exceeded=false", updated.Status.Budget)
	}
	if updated.Status.Budget.Effective == nil || updated.Status.Budget.Effective.MaxFindings != 100 {
		t.Fatalf("effective budgets = %+v, want maxFindings 100", updated.Status.Budget.Effective)
	}
}

// TestSecurityScanPromptStatesFindingBudget pins that the finding cap is
// reflected in the prompt as guidance (enforcement stays platform-side).
func TestSecurityScanPromptStatesFindingBudget(t *testing.T) {
	spec := securityScanTestScan().Spec
	spec.Budgets = &triggersv1alpha1.SecurityScanBudgets{MaxFindings: 25}
	prompt := BuildSecurityScanPrompt(spec)
	if !strings.Contains(prompt, "at most 25 findings") {
		t.Fatalf("prompt does not state the finding budget:\n%s", prompt)
	}
	if !strings.Contains(prompt, "platform enforces this cap") {
		t.Fatalf("prompt does not state that the platform enforces the cap")
	}
}
