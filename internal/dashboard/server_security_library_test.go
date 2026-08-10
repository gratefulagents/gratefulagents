package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func testSecurityWorkflowResource(namespace string) *platform.SecurityWorkflowResource {
	retries := int32(2)
	return &platform.SecurityWorkflowResource{
		Namespace:   namespace,
		Name:        "payments-workflow",
		Description: "payments-focused plan",
		Parallelism: 2,
		Tasks: []*platform.SecurityScanTaskConfig{
			{Name: "injection", Objective: "hunt injections in {{params.target_env}}", Category: "injection",
				SkillRefs: []string{"api-authz-hunting"}, OutputSchema: `{"type":"array","items":{"type":"object"}}`},
			{Name: "triage", Objective: "triage findings", Role: "finding-triager", Model: "gpt-5.2",
				DependsOn: []string{"injection"}, MaxRetries: &retries, Timeout: "30m", MaxTurns: 40,
				MaxCostUsd: "1.25", ForEach: "injection", MaxInstances: 8,
				Tools: &platform.SecurityScanTaskTools{Allowed: []string{"grep"}, Denied: []string{"web_fetch"}}},
		},
		Parameters: []*platform.SecurityWorkflowParameter{
			{Name: "target_env", Description: "environment to scan", Default: "prod"},
			{Name: "focus", Required: true},
		},
	}
}

func TestSecurityWorkflowCRUDLifecycle(t *testing.T) {
	srv, c := newCronTestServer(t)
	ns := testUserNS()
	ctx := projectActorCtx()

	created, err := srv.CreateSecurityWorkflow(ctx, &platform.CreateSecurityWorkflowRequest{Workflow: testSecurityWorkflowResource("")})
	if err != nil {
		t.Fatalf("CreateSecurityWorkflow() error = %v", err)
	}
	if created.Namespace != ns || created.Name != "payments-workflow" || len(created.Tasks) != 2 {
		t.Fatalf("created = %+v", created)
	}

	cr := &triggersv1alpha1.SecurityWorkflow{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "payments-workflow"}, cr); err != nil {
		t.Fatalf("Get(SecurityWorkflow) error = %v", err)
	}
	if cr.Spec.Parallelism != 2 || cr.Spec.Tasks[1].Role != "finding-triager" || cr.Spec.Tasks[1].DependsOn[0] != "injection" {
		t.Fatalf("spec = %+v", cr.Spec)
	}
	if refs := cr.Spec.Tasks[0].SkillRefs; len(refs) != 1 || refs[0].Name != "api-authz-hunting" {
		t.Fatalf("task skillRefs = %+v", refs)
	}
	task := cr.Spec.Tasks[1]
	if task.MaxRetries == nil || *task.MaxRetries != 2 || task.Timeout.Duration != 30*time.Minute ||
		task.MaxTurns != 40 || task.MaxCostUSD != "1.25" || task.ForEach != "injection" || task.MaxInstances != 8 ||
		task.Tools == nil || task.Tools.Allowed[0] != "grep" || task.Tools.Denied[0] != "web_fetch" {
		t.Fatalf("task execution fields = %+v", task)
	}
	if len(cr.Spec.Parameters) != 2 || cr.Spec.Parameters[0].Name != "target_env" ||
		cr.Spec.Parameters[0].Default != "prod" || !cr.Spec.Parameters[1].Required {
		t.Fatalf("parameters = %+v", cr.Spec.Parameters)
	}

	got, err := srv.GetSecurityWorkflow(ctx, &platform.GetSecurityWorkflowRequest{Name: "payments-workflow"})
	if err != nil {
		t.Fatalf("GetSecurityWorkflow() error = %v", err)
	}
	if got.UsageCount != 0 || got.Description != "payments-focused plan" {
		t.Fatalf("got = %+v", got)
	}
	if refs := got.Tasks[0].SkillRefs; len(refs) != 1 || refs[0] != "api-authz-hunting" {
		t.Fatalf("proto skillRefs = %+v", refs)
	}
	pt := got.Tasks[1]
	if pt.MaxRetries == nil || *pt.MaxRetries != 2 || pt.Timeout != "30m0s" || pt.MaxTurns != 40 ||
		pt.MaxCostUsd != "1.25" || pt.ForEach != "injection" || pt.MaxInstances != 8 ||
		pt.Tools == nil || len(pt.Tools.Allowed) != 1 || len(pt.Tools.Denied) != 1 {
		t.Fatalf("proto task = %+v", pt)
	}
	if len(got.Parameters) != 2 || got.Parameters[0].Name != "target_env" || got.Parameters[0].Default != "prod" ||
		got.Parameters[0].Description != "environment to scan" || !got.Parameters[1].Required {
		t.Fatalf("proto parameters = %+v", got.Parameters)
	}

	update := testSecurityWorkflowResource("")
	update.Description = "updated"
	update.Tasks = update.Tasks[:1]
	update.Tasks[0].DependsOn = nil
	updated, err := srv.UpdateSecurityWorkflow(ctx, &platform.UpdateSecurityWorkflowRequest{Workflow: update})
	if err != nil {
		t.Fatalf("UpdateSecurityWorkflow() error = %v", err)
	}
	if updated.Description != "updated" || len(updated.Tasks) != 1 {
		t.Fatalf("updated = %+v", updated)
	}

	list, err := srv.ListSecurityWorkflows(ctx, &platform.ListSecurityWorkflowsRequest{})
	if err != nil {
		t.Fatalf("ListSecurityWorkflows() error = %v", err)
	}
	if len(list.Workflows) != 1 || list.Workflows[0].Name != "payments-workflow" {
		t.Fatalf("list = %+v", list.Workflows)
	}

	if _, err := srv.DeleteSecurityWorkflow(ctx, &platform.DeleteSecurityWorkflowRequest{Name: "payments-workflow"}); err != nil {
		t.Fatalf("DeleteSecurityWorkflow() error = %v", err)
	}
	if _, err := srv.GetSecurityWorkflow(ctx, &platform.GetSecurityWorkflowRequest{Name: "payments-workflow"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetSecurityWorkflow after delete = %v, want NotFound", err)
	}
}

func TestCreateSecurityWorkflowValidationErrors(t *testing.T) {
	srv, _ := newCronTestServer(t)
	ctx := projectActorCtx()

	for name, mutate := range map[string]func(*platform.SecurityWorkflowResource){
		"empty workflow":  func(w *platform.SecurityWorkflowResource) { w.Tasks = nil },
		"duplicate names": func(w *platform.SecurityWorkflowResource) { w.Tasks[1].Name = w.Tasks[0].Name },
		"dangling dep":    func(w *platform.SecurityWorkflowResource) { w.Tasks[1].DependsOn = []string{"ghost"} },
		"self dep":        func(w *platform.SecurityWorkflowResource) { w.Tasks[0].DependsOn = []string{"injection"} },
		"cycle": func(w *platform.SecurityWorkflowResource) {
			w.Tasks[0].DependsOn = []string{"triage"}
		},
		"invalid name":        func(w *platform.SecurityWorkflowResource) { w.Tasks[0].Name = "Bad Name!" },
		"missing objective":   func(w *platform.SecurityWorkflowResource) { w.Tasks[0].Objective = " " },
		"invalid role":        func(w *platform.SecurityWorkflowResource) { w.Tasks[0].Role = "Not A Role" },
		"invalid parallelism":  func(w *platform.SecurityWorkflowResource) { w.Parallelism = 42 },
		"bad timeout":          func(w *platform.SecurityWorkflowResource) { w.Tasks[0].Timeout = "banana" },
		"negative max_turns":   func(w *platform.SecurityWorkflowResource) { w.Tasks[0].MaxTurns = -1 },
		"bad max_cost_usd":     func(w *platform.SecurityWorkflowResource) { w.Tasks[0].MaxCostUsd = "$5" },
		"max_retries too high": func(w *platform.SecurityWorkflowResource) { retries := int32(11); w.Tasks[0].MaxRetries = &retries },
		"tool with whitespace": func(w *platform.SecurityWorkflowResource) {
			w.Tasks[0].Tools = &platform.SecurityScanTaskTools{Allowed: []string{"bad tool"}}
		},
		"forEach with repeats": func(w *platform.SecurityWorkflowResource) { w.Tasks[1].Repeats = 3 },
		"bad output schema":    func(w *platform.SecurityWorkflowResource) { w.Tasks[0].OutputSchema = "[1,2]" },
		"bad parameter name": func(w *platform.SecurityWorkflowResource) {
			w.Parameters = []*platform.SecurityWorkflowParameter{{Name: "not a name"}}
		},
		"duplicate parameter names": func(w *platform.SecurityWorkflowResource) {
			w.Parameters = []*platform.SecurityWorkflowParameter{{Name: "p"}, {Name: "p"}}
		},
		"required parameter with default": func(w *platform.SecurityWorkflowResource) {
			w.Parameters = []*platform.SecurityWorkflowParameter{{Name: "p", Required: true, Default: "x"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			workflow := testSecurityWorkflowResource("")
			mutate(workflow)
			_, err := srv.CreateSecurityWorkflow(ctx, &platform.CreateSecurityWorkflowRequest{Workflow: workflow})
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("CreateSecurityWorkflow(%s) error = %v, want InvalidArgument", name, err)
			}
		})
	}
}

func TestValidateSecurityWorkflowReturnsStructuredErrors(t *testing.T) {
	srv, _ := newCronTestServer(t)
	ctx := projectActorCtx()

	resp, err := srv.ValidateSecurityWorkflow(ctx, &platform.ValidateSecurityWorkflowRequest{
		Tasks: []*platform.SecurityScanTaskConfig{
			{Name: "a", Objective: "task a", DependsOn: []string{"b"}},
			{Name: "b", Objective: "task b", DependsOn: []string{"a"}},
			{Name: "b", Objective: "", Timeout: "banana"},
		},
		Parallelism: 99,
		Parameters: []*platform.SecurityWorkflowParameter{
			{Name: "ok_param"},
			{Name: "bad param"},
		},
	})
	if err != nil {
		t.Fatalf("ValidateSecurityWorkflow() error = %v", err)
	}
	if resp.Valid {
		t.Fatal("Valid = true, want false")
	}
	fields := map[string]bool{}
	for _, e := range resp.Errors {
		fields[e.Field] = true
	}
	for _, want := range []string{"tasks[2].name", "tasks[2].objective", "tasks[2].timeout", "parallelism", "parameters[1].name"} {
		if !fields[want] {
			t.Fatalf("errors missing field %q: %+v", want, resp.Errors)
		}
	}

	ok, err := srv.ValidateSecurityWorkflow(ctx, &platform.ValidateSecurityWorkflowRequest{
		Tasks:      []*platform.SecurityScanTaskConfig{{Name: "a", Objective: "task a"}},
		Parameters: []*platform.SecurityWorkflowParameter{{Name: "target_env", Default: "prod"}},
	})
	if err != nil {
		t.Fatalf("ValidateSecurityWorkflow(valid) error = %v", err)
	}
	if !ok.Valid || len(ok.Errors) != 0 {
		t.Fatalf("valid workflow rejected: %+v", ok)
	}
}

func TestValidateSecurityWorkflowDetectsLongCycle(t *testing.T) {
	srv, _ := newCronTestServer(t)
	resp, err := srv.ValidateSecurityWorkflow(projectActorCtx(), &platform.ValidateSecurityWorkflowRequest{
		Tasks: []*platform.SecurityScanTaskConfig{
			{Name: "a", Objective: "t", DependsOn: []string{"c"}},
			{Name: "b", Objective: "t", DependsOn: []string{"a"}},
			{Name: "c", Objective: "t", DependsOn: []string{"b"}},
		},
	})
	if err != nil {
		t.Fatalf("ValidateSecurityWorkflow() error = %v", err)
	}
	if resp.Valid || len(resp.Errors) == 0 || !strings.Contains(resp.Errors[0].Message, "cycle") {
		t.Fatalf("cycle not detected: %+v", resp)
	}
}

func TestSecurityRankerCRUDAndValidation(t *testing.T) {
	srv, _ := newCronTestServer(t)
	ctx := projectActorCtx()
	ns := testUserNS()

	_, err := srv.CreateSecurityRanker(ctx, &platform.CreateSecurityRankerRequest{
		Ranker: &platform.SecurityRankerResource{Name: "payments", Rules: []string{"  ", ""}},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("CreateSecurityRanker(empty rules) error = %v, want InvalidArgument", err)
	}

	created, err := srv.CreateSecurityRanker(ctx, &platform.CreateSecurityRankerRequest{
		Ranker: &platform.SecurityRankerResource{
			Name:        "payments",
			Description: "payment rules",
			Rules:       []string{"severity-floor: injection=high", "auth bypass is always critical"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSecurityRanker() error = %v", err)
	}
	if created.Namespace != ns || len(created.Rules) != 2 {
		t.Fatalf("created = %+v", created)
	}

	got, err := srv.GetSecurityRanker(ctx, &platform.GetSecurityRankerRequest{Name: "payments"})
	if err != nil || got.Description != "payment rules" {
		t.Fatalf("GetSecurityRanker() = %+v, %v", got, err)
	}

	updatedReq := &platform.SecurityRankerResource{Name: "payments", Rules: []string{"only prose now"}}
	updated, err := srv.UpdateSecurityRanker(ctx, &platform.UpdateSecurityRankerRequest{Ranker: updatedReq})
	if err != nil || len(updated.Rules) != 1 {
		t.Fatalf("UpdateSecurityRanker() = %+v, %v", updated, err)
	}

	list, err := srv.ListSecurityRankers(ctx, &platform.ListSecurityRankersRequest{})
	if err != nil || len(list.Rankers) != 1 {
		t.Fatalf("ListSecurityRankers() = %+v, %v", list, err)
	}

	if _, err := srv.DeleteSecurityRanker(ctx, &platform.DeleteSecurityRankerRequest{Name: "payments"}); err != nil {
		t.Fatalf("DeleteSecurityRanker() error = %v", err)
	}
}

func TestSecurityPostScriptCRUDAndValidation(t *testing.T) {
	srv, _ := newCronTestServer(t)
	ctx := projectActorCtx()

	_, err := srv.CreateSecurityPostScript(ctx, &platform.CreateSecurityPostScriptRequest{
		PostScript: &platform.SecurityPostScriptResource{Name: "poc", Prompt: "write a poc", RunOn: "sometimes"},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("CreateSecurityPostScript(bad runOn) error = %v, want InvalidArgument", err)
	}
	_, err = srv.CreateSecurityPostScript(ctx, &platform.CreateSecurityPostScriptRequest{
		PostScript: &platform.SecurityPostScriptResource{Name: "poc", Prompt: "  "},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("CreateSecurityPostScript(empty prompt) error = %v, want InvalidArgument", err)
	}

	created, err := srv.CreateSecurityPostScript(ctx, &platform.CreateSecurityPostScriptRequest{
		PostScript: &platform.SecurityPostScriptResource{Name: "poc", Prompt: "write a poc", RunOn: "high-and-above"},
	})
	if err != nil || created.RunOn != "high-and-above" {
		t.Fatalf("CreateSecurityPostScript() = %+v, %v", created, err)
	}

	updated, err := srv.UpdateSecurityPostScript(ctx, &platform.UpdateSecurityPostScriptRequest{
		PostScript: &platform.SecurityPostScriptResource{Name: "poc", Prompt: "verify the fix", RunOn: "confirmed"},
	})
	if err != nil || updated.Prompt != "verify the fix" {
		t.Fatalf("UpdateSecurityPostScript() = %+v, %v", updated, err)
	}

	list, err := srv.ListSecurityPostScripts(ctx, &platform.ListSecurityPostScriptsRequest{})
	if err != nil || len(list.PostScripts) != 1 {
		t.Fatalf("ListSecurityPostScripts() = %+v, %v", list, err)
	}

	if _, err := srv.DeleteSecurityPostScript(ctx, &platform.DeleteSecurityPostScriptRequest{Name: "poc"}); err != nil {
		t.Fatalf("DeleteSecurityPostScript() error = %v", err)
	}
}

// referencedLibraryScan creates a SecurityScan in ns referencing the library
// resources by name.
func referencedLibraryScan(ns, name string) *triggersv1alpha1.SecurityScan {
	return &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: triggersv1alpha1.SecurityScanSpec{
			RepoURL:        "https://github.com/example/app.git",
			WorkflowRef:    &triggersv1alpha1.SecurityResourceRef{Name: "payments-workflow"},
			RankerRefs:     []triggersv1alpha1.SecurityResourceRef{{Name: "payments"}},
			PostScriptRefs: []triggersv1alpha1.SecurityResourceRef{{Name: "poc"}},
		},
	}
}

func TestSecurityLibraryUsageCountsAndDeleteGuard(t *testing.T) {
	ns := testUserNS()
	workflow := &triggersv1alpha1.SecurityWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-workflow", Namespace: ns},
		Spec: triggersv1alpha1.SecurityWorkflowSpec{
			Tasks: []triggersv1alpha1.SecurityScanTask{{Name: "a", Objective: "t"}},
		},
	}
	ranker := &triggersv1alpha1.SecurityRanker{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns},
		Spec:       triggersv1alpha1.SecurityRankerSpec{Rules: []string{"rule"}},
	}
	script := &triggersv1alpha1.SecurityPostScript{
		ObjectMeta: metav1.ObjectMeta{Name: "poc", Namespace: ns},
		Spec:       triggersv1alpha1.SecurityPostScriptSpec{Prompt: "poc"},
	}
	srv, c := newCronTestServer(t, workflow, ranker, script,
		referencedLibraryScan(ns, "scan-a"), referencedLibraryScan(ns, "scan-b"))
	ctx := projectActorCtx()

	got, err := srv.GetSecurityWorkflow(ctx, &platform.GetSecurityWorkflowRequest{Name: "payments-workflow"})
	if err != nil {
		t.Fatalf("GetSecurityWorkflow() error = %v", err)
	}
	if got.UsageCount != 2 || len(got.ReferencingScans) != 2 || got.ReferencingScans[0] != "scan-a" {
		t.Fatalf("usage = %d %v, want 2 [scan-a scan-b]", got.UsageCount, got.ReferencingScans)
	}
	list, err := srv.ListSecurityRankers(ctx, &platform.ListSecurityRankersRequest{})
	if err != nil || len(list.Rankers) != 1 || list.Rankers[0].UsageCount != 2 {
		t.Fatalf("ListSecurityRankers() = %+v, %v", list, err)
	}

	// Delete is blocked with FailedPrecondition naming the referencing scans.
	for name, del := range map[string]func() error{
		"workflow": func() error {
			_, err := srv.DeleteSecurityWorkflow(ctx, &platform.DeleteSecurityWorkflowRequest{Name: "payments-workflow"})
			return err
		},
		"ranker": func() error {
			_, err := srv.DeleteSecurityRanker(ctx, &platform.DeleteSecurityRankerRequest{Name: "payments"})
			return err
		},
		"postScript": func() error {
			_, err := srv.DeleteSecurityPostScript(ctx, &platform.DeleteSecurityPostScriptRequest{Name: "poc"})
			return err
		},
	} {
		err := del()
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("Delete(%s) error = %v, want FailedPrecondition", name, err)
		}
		if msg := err.Error(); !strings.Contains(msg, "scan-a") || !strings.Contains(msg, "scan-b") {
			t.Fatalf("Delete(%s) error %q does not list referencing scans", name, msg)
		}
	}

	// Detaching the scans unblocks deletion.
	for _, scanName := range []string{"scan-a", "scan-b"} {
		scan := &triggersv1alpha1.SecurityScan{}
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: scanName}, scan); err != nil {
			t.Fatalf("Get(SecurityScan) error = %v", err)
		}
		scan.Spec.WorkflowRef = nil
		scan.Spec.RankerRefs = nil
		scan.Spec.PostScriptRefs = nil
		if err := c.Update(context.Background(), scan); err != nil {
			t.Fatalf("Update(SecurityScan) error = %v", err)
		}
	}
	if _, err := srv.DeleteSecurityWorkflow(ctx, &platform.DeleteSecurityWorkflowRequest{Name: "payments-workflow"}); err != nil {
		t.Fatalf("DeleteSecurityWorkflow after detach error = %v", err)
	}
	if _, err := srv.DeleteSecurityRanker(ctx, &platform.DeleteSecurityRankerRequest{Name: "payments"}); err != nil {
		t.Fatalf("DeleteSecurityRanker after detach error = %v", err)
	}
	if _, err := srv.DeleteSecurityPostScript(ctx, &platform.DeleteSecurityPostScriptRequest{Name: "poc"}); err != nil {
		t.Fatalf("DeleteSecurityPostScript after detach error = %v", err)
	}
}

func TestSecurityLibraryCrossNamespaceDeniedForNonAdmins(t *testing.T) {
	srv, _ := newCronTestServer(t)
	ctx := actorContext("mallory", "member", "", "")
	const other = "someone-elses-namespace"

	calls := map[string]func() error{
		"ListSecurityWorkflows": func() error {
			_, err := srv.ListSecurityWorkflows(ctx, &platform.ListSecurityWorkflowsRequest{Namespace: other})
			return err
		},
		"GetSecurityWorkflow": func() error {
			_, err := srv.GetSecurityWorkflow(ctx, &platform.GetSecurityWorkflowRequest{Namespace: other, Name: "x"})
			return err
		},
		"CreateSecurityWorkflow": func() error {
			w := testSecurityWorkflowResource(other)
			_, err := srv.CreateSecurityWorkflow(ctx, &platform.CreateSecurityWorkflowRequest{Workflow: w})
			return err
		},
		"UpdateSecurityWorkflow": func() error {
			w := testSecurityWorkflowResource(other)
			_, err := srv.UpdateSecurityWorkflow(ctx, &platform.UpdateSecurityWorkflowRequest{Workflow: w})
			return err
		},
		"DeleteSecurityWorkflow": func() error {
			_, err := srv.DeleteSecurityWorkflow(ctx, &platform.DeleteSecurityWorkflowRequest{Namespace: other, Name: "x"})
			return err
		},
		"ListSecurityRankers": func() error {
			_, err := srv.ListSecurityRankers(ctx, &platform.ListSecurityRankersRequest{Namespace: other})
			return err
		},
		"GetSecurityRanker": func() error {
			_, err := srv.GetSecurityRanker(ctx, &platform.GetSecurityRankerRequest{Namespace: other, Name: "x"})
			return err
		},
		"CreateSecurityRanker": func() error {
			_, err := srv.CreateSecurityRanker(ctx, &platform.CreateSecurityRankerRequest{
				Ranker: &platform.SecurityRankerResource{Namespace: other, Name: "x", Rules: []string{"r"}}})
			return err
		},
		"UpdateSecurityRanker": func() error {
			_, err := srv.UpdateSecurityRanker(ctx, &platform.UpdateSecurityRankerRequest{
				Ranker: &platform.SecurityRankerResource{Namespace: other, Name: "x", Rules: []string{"r"}}})
			return err
		},
		"DeleteSecurityRanker": func() error {
			_, err := srv.DeleteSecurityRanker(ctx, &platform.DeleteSecurityRankerRequest{Namespace: other, Name: "x"})
			return err
		},
		"ListSecurityPostScripts": func() error {
			_, err := srv.ListSecurityPostScripts(ctx, &platform.ListSecurityPostScriptsRequest{Namespace: other})
			return err
		},
		"GetSecurityPostScript": func() error {
			_, err := srv.GetSecurityPostScript(ctx, &platform.GetSecurityPostScriptRequest{Namespace: other, Name: "x"})
			return err
		},
		"CreateSecurityPostScript": func() error {
			_, err := srv.CreateSecurityPostScript(ctx, &platform.CreateSecurityPostScriptRequest{
				PostScript: &platform.SecurityPostScriptResource{Namespace: other, Name: "x", Prompt: "p"}})
			return err
		},
		"UpdateSecurityPostScript": func() error {
			_, err := srv.UpdateSecurityPostScript(ctx, &platform.UpdateSecurityPostScriptRequest{
				PostScript: &platform.SecurityPostScriptResource{Namespace: other, Name: "x", Prompt: "p"}})
			return err
		},
		"DeleteSecurityPostScript": func() error {
			_, err := srv.DeleteSecurityPostScript(ctx, &platform.DeleteSecurityPostScriptRequest{Namespace: other, Name: "x"})
			return err
		},
	}
	for name, call := range calls {
		if err := call(); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("%s cross-namespace error = %v, want PermissionDenied", name, err)
		}
	}
}

func TestCreateSecurityScanWithRefsRoundTripsAndRejectsBothWorkflows(t *testing.T) {
	srv, c := newCronTestServer(t)
	srv.stateStore = newMockStateStore()
	ns := testUserNS()
	ctx := projectActorCtx()

	spec := fullSecurityScanSpec()
	spec.Workflow = nil
	spec.WorkflowRef = "payments-workflow"
	spec.RankerRefs = []string{"payments-ranker"}
	spec.PostScriptRefs = []string{"payments-poc"}

	created, err := srv.CreateSecurityScan(ctx, &platform.CreateSecurityScanRequest{Name: "ref-scan", Spec: spec})
	if err != nil {
		t.Fatalf("CreateSecurityScan() error = %v", err)
	}
	if created.Spec.WorkflowRef != "payments-workflow" ||
		len(created.Spec.RankerRefs) != 1 || created.Spec.RankerRefs[0] != "payments-ranker" ||
		len(created.Spec.PostScriptRefs) != 1 || created.Spec.PostScriptRefs[0] != "payments-poc" {
		t.Fatalf("created spec refs = %+v", created.Spec)
	}

	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "ref-scan"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if cr.Spec.WorkflowRef == nil || cr.Spec.WorkflowRef.Name != "payments-workflow" ||
		len(cr.Spec.RankerRefs) != 1 || len(cr.Spec.PostScriptRefs) != 1 {
		t.Fatalf("CRD refs = %+v", cr.Spec)
	}

	// Both an inline workflow and a workflowRef is rejected up front.
	both := fullSecurityScanSpec()
	both.WorkflowRef = "payments-workflow"
	_, err = srv.CreateSecurityScan(ctx, &platform.CreateSecurityScanRequest{Name: "both-scan", Spec: both})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("CreateSecurityScan(both) error = %v, want InvalidArgument", err)
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error %q missing mutual-exclusion message", err.Error())
	}
}
