package triggers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
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

func securityTestProgram(namespace string) *triggersv1alpha1.SecurityProgram {
	program := &triggersv1alpha1.SecurityProgram{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-bounty", Namespace: namespace, Generation: 7},
		Spec: triggersv1alpha1.SecurityProgramSpec{
			Provider:    "HackerOne",
			DisplayName: "Acme Bug Bounty",
			ProgramURL:  "https://hackerone.com/acme",
			ScopePolicy: "Only acme/widget production code is in scope.",
			VerifiedAt:  metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		},
	}
	program.Status.ObservedGeneration = program.Generation
	program.Status.ContentDigest = securitySpecHash(program.Spec)
	program.Status.Conditions = []metav1.Condition{{
		Type:               triggersv1alpha1.ConditionSecurityLibraryReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: program.Generation,
		Reason:             "Validated",
	}}
	return program
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

func TestAutomaticSecurityScanWorkflowName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		spec triggersv1alpha1.SecurityScanSpec
		want string
	}{
		{name: "generic repository", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/widget.git"}, want: "default-deep-scan"},
		{name: "website target", spec: triggersv1alpha1.SecurityScanSpec{TargetURL: "https://app.example.test"}, want: "web-recon-passive"},
		{name: "generic smart contract wording is inconclusive", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/smart-contract-tools.git"}, want: "default-deep-scan"},
		{name: "solidity language", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/token.git", Scope: &triggersv1alpha1.SecurityScanScope{Languages: []string{"Solidity"}}}, want: "smart-contract-review"},
		{name: "solidity path", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/token.git", Scope: &triggersv1alpha1.SecurityScanScope{IncludePaths: []string{"contracts/**/*.sol"}}}, want: "smart-contract-review"},
		{name: "explicit EVM contract focus", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/token.git", Scope: &triggersv1alpha1.SecurityScanScope{Focus: "Review Ethereum smart contracts"}}, want: "smart-contract-review"},
		{name: "EVM client outranks contracts", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/ethereum-contracts.git", Scope: &triggersv1alpha1.SecurityScanScope{Focus: "execution client consensus"}}, want: "blockchain-protocol-audit"},
		{name: "Solana marker", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/solana-validator.git"}, want: "blockchain-protocol-audit"},
		{name: "supported chain in additional repository", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/frontend.git", AdditionalRepos: []string{"https://github.com/acme/substrate-node.git"}}, want: "blockchain-protocol-audit"},
		{name: "cross chain scope", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/bridge.git", Scope: &triggersv1alpha1.SecurityScanScope{Focus: "Cross-chain message verification"}}, want: "blockchain-protocol-audit"},
		{name: "Cosmos general audit", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/cosmos-sdk-app.git"}, want: "blockchain-protocol-audit"},
		{name: "ABCI alone is inconclusive", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/service.git", Scope: &triggersv1alpha1.SecurityScanScope{Focus: "ABCI halt review"}}, want: "default-deep-scan"},
		{name: "Cosmos ABCI without halt intent is broad", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/cosmos-app.git", Scope: &triggersv1alpha1.SecurityScanScope{Focus: "Review ABCI handlers"}}, want: "blockchain-protocol-audit"},
		{name: "narrow Cosmos ABCI halt", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/cosmos-app.git", Scope: &triggersv1alpha1.SecurityScanScope{Focus: "ABCI panic and chain halt paths"}}, want: "cosmos-abci-halt-review"},
		{name: "narrow Cosmos ABCI nondeterminism", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/cometbft-app.git", Scope: &triggersv1alpha1.SecurityScanScope{Focus: "ABCI non-determinism"}}, want: "cosmos-abci-halt-review"},
		{name: "mixed Cosmos and EVM is broad", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/cosmos-ethereum-bridge.git", Scope: &triggersv1alpha1.SecurityScanScope{Focus: "ABCI halt and Solidity contracts"}}, want: "blockchain-protocol-audit"},
		{name: "excluded marker is not evidence", spec: triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/widget.git", Scope: &triggersv1alpha1.SecurityScanScope{ExcludePaths: []string{"vendor/solidity/**"}}}, want: "default-deep-scan"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := automaticSecurityScanWorkflowName(tt.spec); got != tt.want {
				t.Fatalf("automaticSecurityScanWorkflowName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAutomaticSecurityScanWorkflowResolutionPrecedence(t *testing.T) {
	t.Parallel()
	workflow := func(namespace, name string) *triggersv1alpha1.SecurityWorkflow {
		return &triggersv1alpha1.SecurityWorkflow{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       triggersv1alpha1.SecurityWorkflowSpec{Tasks: []triggersv1alpha1.SecurityScanTask{{Name: name, Objective: name}}},
		}
	}
	tests := []struct {
		name     string
		scan     func() *triggersv1alpha1.SecurityScan
		wantTask string
		wantRef  string
	}{
		{
			name: "automatic route",
			scan: func() *triggersv1alpha1.SecurityScan {
				scan := securityScanTestScan()
				scan.Spec.Scope = &triggersv1alpha1.SecurityScanScope{Languages: []string{"solidity"}}
				return scan
			},
			wantTask: "smart-contract-review",
			wantRef:  "smart-contract-review",
		},
		{
			name: "explicit workflow ref wins",
			scan: func() *triggersv1alpha1.SecurityScan {
				scan := securityScanTestScan()
				scan.Spec.Scope = &triggersv1alpha1.SecurityScanScope{Languages: []string{"solidity"}}
				scan.Spec.WorkflowRef = &triggersv1alpha1.SecurityResourceRef{Name: "payments-workflow"}
				return scan
			},
			wantTask: "payments-injection",
			wantRef:  "payments-workflow",
		},
		{
			name: "inline workflow wins",
			scan: func() *triggersv1alpha1.SecurityScan {
				scan := securityScanTestScan()
				scan.Spec.Scope = &triggersv1alpha1.SecurityScanScope{Languages: []string{"solidity"}}
				scan.Spec.Workflow = []triggersv1alpha1.SecurityScanTask{{Name: "inline", Objective: "inline"}}
				return scan
			},
			wantTask: "inline",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scan := tt.scan()
			_, k8sClient, _ := newSecurityScanReconciler(t, time.Now(), scan,
				securityTestWorkflow(scan.Namespace), workflow(scan.Namespace, "smart-contract-review"))
			resolved, err := resolveSecurityScanRefs(context.Background(), k8sClient, scan)
			if err != nil {
				t.Fatalf("resolveSecurityScanRefs() error = %v", err)
			}
			if len(resolved.spec.Workflow) == 0 || resolved.spec.Workflow[0].Name != tt.wantTask {
				t.Fatalf("resolved workflow = %+v, want first task %q", resolved.spec.Workflow, tt.wantTask)
			}
			if tt.wantRef == "" {
				if len(resolved.refs) != 0 {
					t.Fatalf("resolved refs = %+v, want none for inline workflow", resolved.refs)
				}
				return
			}
			if len(resolved.refs) != 1 || resolved.refs[0].Name != tt.wantRef {
				t.Fatalf("resolved refs = %+v, want workflow %q", resolved.refs, tt.wantRef)
			}
		})
	}
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

func TestSecurityScanResolvesVerifiedProgramIntoPromptAndProvenance(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.SecurityProgramRef = &triggersv1alpha1.SecurityResourceRef{Name: "acme-bounty"}
	program := securityTestProgram(scan.Namespace)
	reconciler, k8sClient, stateStore := newSecurityScanReconciler(t, now, scan, program)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	prompt := securityScanSeedMessage(t, stateStore, scan.Namespace, runs[0].Name)
	for _, want := range []string{"Security program scope snapshot", program.Spec.ScopePolicy, "Do not fetch it"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	wantRef := resolvedSecurityRef("SecurityProgram", program.Name, program.Generation, program.Spec)
	var refs []triggersv1alpha1.SecurityScanResolvedRef
	if err := json.Unmarshal([]byte(runs[0].Annotations[triggersv1alpha1.SecurityScanResolvedRefsAnnotation]), &refs); err != nil {
		t.Fatalf("resolved refs annotation: %v", err)
	}
	if len(refs) < 1 || refs[0] != wantRef {
		t.Fatalf("resolved refs = %+v, want first %+v", refs, wantRef)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if len(updated.Status.LastResolvedRefs) < 1 || updated.Status.LastResolvedRefs[0] != wantRef {
		t.Fatalf("status.lastResolvedRefs = %+v, want first %+v", updated.Status.LastResolvedRefs, wantRef)
	}
}

func TestDeterministicSecurityScanFreezesProgramSnapshot(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.Execution.Mode = triggersv1alpha1.SecurityScanExecutionModeDeterministic
	scan.Spec.SecurityProgramRef = &triggersv1alpha1.SecurityResourceRef{Name: "acme-bounty"}
	program := securityTestProgram(scan.Namespace)
	role := &platformv1alpha1.RoleInstruction{
		ObjectMeta: metav1.ObjectMeta{Name: "security-reviewer"},
		Spec:       platformv1alpha1.RoleInstructionSpec{Instructions: "Review security boundaries."},
	}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, program, role)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	started := getSecurityScan(t, k8sClient, scan)
	if started.Status.LastExecution == nil || started.Status.LastExecution.SecurityProgramSnapshot == nil {
		t.Fatalf("lastExecution is missing program snapshot: %+v", started.Status.LastExecution)
	}
	if started.Status.LastExecution.SecurityProgramSnapshot.ScopePolicy != program.Spec.ScopePolicy ||
		started.Status.LastExecution.SecurityProgramResolvedRef == nil ||
		started.Status.LastExecution.SecurityProgramResolvedRef.Hash != securitySpecHash(program.Spec) {
		t.Fatalf("program snapshot/ref = %+v / %+v", started.Status.LastExecution.SecurityProgramSnapshot, started.Status.LastExecution.SecurityProgramResolvedRef)
	}
	if len(started.Status.LastResolvedRefs) == 0 || started.Status.LastResolvedRefs[0].Kind != "SecurityProgram" {
		t.Fatalf("status.lastResolvedRefs = %+v", started.Status.LastResolvedRefs)
	}

	freshProgram := &triggersv1alpha1.SecurityProgram{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(program), freshProgram); err != nil {
		t.Fatal(err)
	}
	freshProgram.Spec.ScopePolicy = "EDITED policy that must not enter the active execution"
	freshProgram.Generation++
	if err := k8sClient.Update(context.Background(), freshProgram); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() after program edit error = %v", err)
	}
	after := getSecurityScan(t, k8sClient, scan)
	if after.Status.LastExecution.SecurityProgramSnapshot.ScopePolicy != program.Spec.ScopePolicy {
		t.Fatalf("active execution snapshot changed: %+v", after.Status.LastExecution.SecurityProgramSnapshot)
	}
	if after.Status.LastExecution.Phase == triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		t.Fatalf("active execution failed after source program edit: %+v", after.Status.LastExecution)
	}
}

func TestDeterministicSecurityScanFreezesMissingProgramReference(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.Execution.Mode = triggersv1alpha1.SecurityScanExecutionModeDeterministic
	role := &platformv1alpha1.RoleInstruction{
		ObjectMeta: metav1.ObjectMeta{Name: "security-reviewer"},
		Spec:       platformv1alpha1.RoleInstructionSpec{Instructions: "Review security boundaries."},
	}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, role)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	started := getSecurityScan(t, k8sClient, scan)
	if started.Status.LastExecution == nil || started.Status.LastExecution.SecurityProgramSnapshot != nil {
		t.Fatalf("unexpected initial program snapshot: %+v", started.Status.LastExecution)
	}

	// A newly added, even unresolved, live reference must not affect the active
	// execution that snapshotted the absence of a program.
	started.Spec.SecurityProgramRef = &triggersv1alpha1.SecurityResourceRef{Name: "added-later"}
	if err := k8sClient.Update(context.Background(), started); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() after adding live program reference error = %v", err)
	}
	after := getSecurityScan(t, k8sClient, scan)
	if after.Status.LastExecution.SecurityProgramSnapshot != nil || after.Status.LastExecution.Phase == triggersv1alpha1.SecurityScanExecutionPhaseFailed {
		t.Fatalf("active execution changed after adding program reference: %+v", after.Status.LastExecution)
	}
}

func TestSecurityScanProgramReferenceFailsClosed(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		program    *triggersv1alpha1.SecurityProgram
		wantReason string
	}{
		"missing": {wantReason: "UnresolvedReference"},
		"not ready": {
			program: func() *triggersv1alpha1.SecurityProgram {
				p := securityTestProgram("default")
				p.Status.Conditions = nil
				return p
			}(),
			wantReason: "UnresolvedReference",
		},
		"digest mismatch": {
			program: func() *triggersv1alpha1.SecurityProgram {
				p := securityTestProgram("default")
				p.Status.ContentDigest = strings.Repeat("0", 64)
				return p
			}(),
			wantReason: "UnresolvedReference",
		},
		"invalid": {
			program: func() *triggersv1alpha1.SecurityProgram {
				p := securityTestProgram("default")
				p.Spec.ProgramURL = "http://example.com/program"
				return p
			}(),
			wantReason: "InvalidSpec",
		},
	} {
		t.Run(name, func(t *testing.T) {
			scan := securityScanTestScan()
			scan.Spec.SecurityProgramRef = &triggersv1alpha1.SecurityResourceRef{Name: "acme-bounty"}
			objects := []client.Object{scan}
			if tc.program != nil {
				objects = append(objects, tc.program)
			}
			reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, objects...)
			if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
				t.Fatalf("AgentRuns = %d, want 0", len(runs))
			}
			assertSecurityScanCondition(t, getSecurityScan(t, k8sClient, scan), metav1.ConditionFalse, tc.wantReason)
		})
	}
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
