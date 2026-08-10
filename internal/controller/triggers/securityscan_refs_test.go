package triggers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func securityTestWorkflow(namespace string) *triggersv1alpha1.SecurityWorkflow {
	return &triggersv1alpha1.SecurityWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-workflow", Namespace: namespace, Generation: 3},
		Spec: triggersv1alpha1.SecurityWorkflowSpec{
			Description: "payments-focused plan",
			Parallelism: 2,
			Tasks: []triggersv1alpha1.SecurityScanTask{
				{Name: "payments-injection", Objective: "hunt payment injections", Category: "injection"},
				{Name: "payments-triage", Objective: "triage payment findings", Role: "finding-triager", DependsOn: []string{"payments-injection"}},
			},
		},
	}
}

func securityTestRanker(namespace string) *triggersv1alpha1.SecurityRanker {
	return &triggersv1alpha1.SecurityRanker{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-ranker", Namespace: namespace, Generation: 2},
		Spec: triggersv1alpha1.SecurityRankerSpec{
			Rules: []string{"severity-floor: injection=high", "auth bypass is always critical"},
		},
	}
}

func securityTestPostScript(namespace string) *triggersv1alpha1.SecurityPostScript {
	return &triggersv1alpha1.SecurityPostScript{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-poc", Namespace: namespace, Generation: 5},
		Spec: triggersv1alpha1.SecurityPostScriptSpec{
			Prompt: "write a proof of concept for the finding",
			RunOn:  "high-and-above",
		},
	}
}

func securityScanSeedMessage(t *testing.T, stateStore *seedTestStore, namespace, runName string) string {
	t.Helper()
	sessionID := stateStore.sessions[namespace+"/"+runName]
	messages := stateStore.messages[sessionID]
	if len(messages) != 1 {
		t.Fatalf("seed messages = %d, want 1", len(messages))
	}
	return messages[0].Content
}

func TestSecurityScanResolvesRefsAndSnapshotsProvenance(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.WorkflowRef = &triggersv1alpha1.SecurityResourceRef{Name: "payments-workflow"}
	scan.Spec.RankerRefs = []triggersv1alpha1.SecurityResourceRef{{Name: "payments-ranker"}}
	scan.Spec.PostScriptRefs = []triggersv1alpha1.SecurityResourceRef{{Name: "payments-poc"}}
	workflow := securityTestWorkflow(scan.Namespace)
	ranker := securityTestRanker(scan.Namespace)
	script := securityTestPostScript(scan.Namespace)
	reconciler, k8sClient, stateStore := newSecurityScanReconciler(t, now, scan, workflow, ranker, script)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	prompt := securityScanSeedMessage(t, stateStore, scan.Namespace, runs[0].Name)
	for _, want := range []string{
		`Task "payments-injection"`,
		`Task "payments-triage"`,
		"never more than 2 at a time", // workflow parallelism override
		"severity-floor: injection=high\nauth bypass is always critical",
		`Post-script "payments-poc" (runs on: high-and-above findings): write a proof of concept for the finding`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	refsJSON := runs[0].Annotations[triggersv1alpha1.SecurityScanResolvedRefsAnnotation]
	if refsJSON == "" {
		t.Fatal("run is missing the resolved-refs annotation")
	}
	var refs []triggersv1alpha1.SecurityScanResolvedRef
	if err := json.Unmarshal([]byte(refsJSON), &refs); err != nil {
		t.Fatalf("resolved-refs annotation is not JSON: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("resolved refs = %d, want 3: %s", len(refs), refsJSON)
	}
	wantRefs := []triggersv1alpha1.SecurityScanResolvedRef{
		resolvedSecurityRef("SecurityWorkflow", workflow.Name, workflow.Generation, workflow.Spec),
		resolvedSecurityRef("SecurityRanker", ranker.Name, ranker.Generation, ranker.Spec),
		resolvedSecurityRef("SecurityPostScript", script.Name, script.Generation, script.Spec),
	}
	for i, want := range wantRefs {
		if refs[i] != want {
			t.Fatalf("resolved ref[%d] = %+v, want %+v", i, refs[i], want)
		}
		if refs[i].Hash == "" || refs[i].Generation == 0 {
			t.Fatalf("resolved ref[%d] has empty hash or generation: %+v", i, refs[i])
		}
	}

	updated := getSecurityScan(t, k8sClient, scan)
	if len(updated.Status.LastResolvedRefs) != 3 {
		t.Fatalf("status.lastResolvedRefs = %+v, want 3 entries", updated.Status.LastResolvedRefs)
	}
	for i, want := range wantRefs {
		if updated.Status.LastResolvedRefs[i] != want {
			t.Fatalf("status.lastResolvedRefs[%d] = %+v, want %+v", i, updated.Status.LastResolvedRefs[i], want)
		}
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionTrue, "ScanStarted")
}

func TestSecurityScanUnresolvedReferenceCreatesNoRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*triggersv1alpha1.SecurityScan){
		"workflow": func(s *triggersv1alpha1.SecurityScan) {
			s.Spec.WorkflowRef = &triggersv1alpha1.SecurityResourceRef{Name: "missing"}
		},
		"ranker": func(s *triggersv1alpha1.SecurityScan) {
			s.Spec.RankerRefs = []triggersv1alpha1.SecurityResourceRef{{Name: "missing"}}
		},
		"postScript": func(s *triggersv1alpha1.SecurityScan) {
			s.Spec.PostScriptRefs = []triggersv1alpha1.SecurityResourceRef{{Name: "missing"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			scan := securityScanTestScan()
			mutate(scan)
			reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

			if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
				t.Fatalf("AgentRuns = %d, want 0", len(runs))
			}
			updated := getSecurityScan(t, k8sClient, scan)
			assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "UnresolvedReference")
			if !strings.Contains(updated.Status.LastError, `"missing" not found`) {
				t.Fatalf("LastError = %q, want unresolved reference message", updated.Status.LastError)
			}
		})
	}
}

func TestSecurityScanMissingDefaultWorkflowCreatesNoRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
	defaultWorkflow := &triggersv1alpha1.SecurityWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: triggersv1alpha1.DefaultSecurityWorkflowName, Namespace: scan.Namespace},
	}
	if err := k8sClient.Delete(context.Background(), defaultWorkflow); err != nil {
		t.Fatalf("Delete(default workflow): %v", err)
	}

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
		t.Fatalf("AgentRuns = %d, want 0", len(runs))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "UnresolvedReference")
	if !strings.Contains(updated.Status.LastError, `SecurityWorkflow "default-deep-scan" not found`) {
		t.Fatalf("LastError = %q, want missing default workflow message", updated.Status.LastError)
	}
}

func TestSecurityScanWorkflowRefWithInlineWorkflowIsInvalidSpec(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.WorkflowRef = &triggersv1alpha1.SecurityResourceRef{Name: "payments-workflow"}
	scan.Spec.Workflow = []triggersv1alpha1.SecurityScanTask{{Name: "inline", Objective: "inline task"}}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, securityTestWorkflow(scan.Namespace))

	// The rejection is deterministic: reconcile twice, never a run.
	for range 2 {
		if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
		t.Fatalf("AgentRuns = %d, want 0", len(runs))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "InvalidSpec")
	if !strings.Contains(updated.Status.LastError, "mutually exclusive") {
		t.Fatalf("LastError = %q, want mutual-exclusion message", updated.Status.LastError)
	}
}

func TestSecurityScanRefRankersAndPostScriptsAppendAfterInline(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.SeverityRankers = []triggersv1alpha1.SecurityScanRanker{{Name: "inline-ranker", Rules: "inline rules first"}}
	scan.Spec.PostScripts = []triggersv1alpha1.SecurityScanPostScript{{Name: "inline-script", Prompt: "inline prompt first"}}
	scan.Spec.RankerRefs = []triggersv1alpha1.SecurityResourceRef{{Name: "payments-ranker"}}
	scan.Spec.PostScriptRefs = []triggersv1alpha1.SecurityResourceRef{{Name: "payments-poc"}}
	reconciler, k8sClient, stateStore := newSecurityScanReconciler(t, now, scan,
		securityTestRanker(scan.Namespace), securityTestPostScript(scan.Namespace))

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	prompt := securityScanSeedMessage(t, stateStore, scan.Namespace, runs[0].Name)

	inlineRanker := strings.Index(prompt, `Ranker "inline-ranker"`)
	refRanker := strings.Index(prompt, `Ranker "payments-ranker"`)
	if inlineRanker < 0 || refRanker < 0 || refRanker < inlineRanker {
		t.Fatalf("rankers not appended after inline (inline=%d ref=%d):\n%s", inlineRanker, refRanker, prompt)
	}
	inlineScript := strings.Index(prompt, `Post-script "inline-script"`)
	refScript := strings.Index(prompt, `Post-script "payments-poc"`)
	if inlineScript < 0 || refScript < 0 || refScript < inlineScript {
		t.Fatalf("post-scripts not appended after inline (inline=%d ref=%d):\n%s", inlineScript, refScript, prompt)
	}
}

func TestSecurityScanHistoricalRunsImmuneToLibraryEdits(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.WorkflowRef = &triggersv1alpha1.SecurityResourceRef{Name: "payments-workflow"}
	workflow := securityTestWorkflow(scan.Namespace)
	reconciler, k8sClient, stateStore := newSecurityScanReconciler(t, now, scan, workflow)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	before := securityScanSeedMessage(t, stateStore, scan.Namespace, runs[0].Name)
	if !strings.Contains(before, "hunt payment injections") {
		t.Fatalf("prompt missing original objective:\n%s", before)
	}

	// Edit the referenced workflow after the run was created.
	fresh := &triggersv1alpha1.SecurityWorkflow{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(workflow), fresh); err != nil {
		t.Fatalf("Get(SecurityWorkflow) error = %v", err)
	}
	fresh.Spec.Tasks[0].Objective = "EDITED objective that historical runs must never see"
	if err := k8sClient.Update(context.Background(), fresh); err != nil {
		t.Fatalf("Update(SecurityWorkflow) error = %v", err)
	}

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want still 1", len(runs))
	}
	after := securityScanSeedMessage(t, stateStore, scan.Namespace, runs[0].Name)
	if after != before {
		t.Fatalf("historical run prompt changed after library edit:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if strings.Contains(after, "EDITED objective") {
		t.Fatal("historical run prompt picked up the edited workflow")
	}
}
