package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maintainerTestCloseIssueTool   = "close_github_issue"
	maintainerTestDispatchWorkTool = "dispatch_work_item"
)

func TestRegisterMaintainerToolsRegistersTypedWorkItemCommands(t *testing.T) {
	t.Parallel()

	base, _, stateStore := newMaintainerToolBase(t, maintainerRun())
	registry := NewRegistry(t.TempDir())
	RegisterMaintainerTools(registry, stateStore, base.k8sClient, base.currentRunName, base.currentRunNamespace, base.repositoryName, base.repositoryNamespace)
	for _, name := range []string{"triage_issue", "breakdown_issue", "request_decision", maintainerTestDispatchWorkTool, requestMergeToolName, finalizeWorkItemToolName} {
		tool := registry.Get(name)
		if tool == nil || tool.IsReadOnly() {
			t.Fatalf("%s = %#v", name, tool)
		}
	}
	if registry.Get("answer_decision") != nil {
		t.Fatal("agent runtime must not expose a decision-answer command")
	}
}

func TestControllerCutoverRemovesGenericMaintainerMutations(t *testing.T) {
	base, k8sClient, stateStore := newMaintainerToolBase(t, maintainerRun())
	repository := &triggersv1alpha1.GitHubRepository{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: base.repositoryName, Namespace: base.repositoryNamespace}, repository); err != nil {
		t.Fatal(err)
	}
	repository.Spec.Maintainer.WorkItemCutover = triggersv1alpha1.MaintainerWorkItemCutoverController
	if err := k8sClient.Update(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(t.TempDir())
	registry.Register(&closeGitHubIssueTool{})
	RegisterMaintainerTools(registry, stateStore, base.k8sClient, base.currentRunName, base.currentRunNamespace, base.repositoryName, base.repositoryNamespace)
	for _, name := range []string{"merge_pull_request", "mark_run_succeeded", maintainerTestCloseIssueTool, "dispatch_issue"} {
		if registry.Get(name) != nil {
			t.Fatalf("controller cutover retained forbidden tool %s", name)
		}
	}
	for _, name := range []string{requestMergeToolName, finalizeWorkItemToolName, maintainerTestDispatchWorkTool} {
		if registry.Get(name) == nil {
			t.Fatalf("controller cutover omitted typed tool %s", name)
		}
	}
}

func TestMaintainerWorkItemCommandNameIsDeterministicAndDNSSafe(t *testing.T) {
	t.Parallel()

	longKey := strings.Repeat("A complicated/key:", 12)
	first := MaintainerWorkItemCommandName("Repository.Name", longKey)
	second := MaintainerWorkItemCommandName("Repository.Name", longKey)
	other := MaintainerWorkItemCommandName("Repository.Name", longKey+"-other")
	if first != second {
		t.Fatalf("command name is not deterministic: %q != %q", first, second)
	}
	if first == other {
		t.Fatalf("distinct idempotency keys produced the same command name %q", first)
	}
	if len(first) > 63 || !regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`).MatchString(first) {
		t.Fatalf("command name is not a DNS label: %q", first)
	}
}

func TestTriageIssueCreatesImmutableCommandWithoutChangingWorkItem(t *testing.T) {
	t.Parallel()

	base, k8sClient, _ := newMaintainerToolBase(t, maintainerRun())
	workItem := createMaintainerWorkItem(t, k8sClient, maintainerTestRepositoryName, 42, 7)
	before := workItem.DeepCopy()
	tool := &triageIssueTool{maintainerToolBase: base}
	result, err := tool.Execute(context.Background(), triageIssueInputJSON(t, 42, "Evidence from the repository", "stale-resource-version", 3, "stable-key"), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("result = %#v", result)
	}

	var out triageIssueOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatal(err)
	}
	if out.Phase != triggersv1alpha1.MaintainerWorkItemCommandPhasePending || out.Replayed || out.WorkItem.Name != workItem.Name || out.WorkItem.ProjectionSequence != 7 {
		t.Fatalf("receipt = %#v", out)
	}
	command := &triggersv1alpha1.MaintainerWorkItemCommand{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: out.CommandName, Namespace: maintainerTestNamespace}, command); err != nil {
		t.Fatal(err)
	}
	if command.Spec.Triage == nil || len(command.Spec.Triage.AcceptedScope.AcceptanceCriteria) != 1 || command.Spec.Triage.AcceptedScope.AcceptanceCriteria[0] != "criterion" {
		t.Fatalf("command accepted scope = %#v", command.Spec.Triage)
	}
	if command.Spec.Type != triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue || command.Spec.Preconditions.ProjectionSequence != 3 || command.Spec.Preconditions.ResourceVersion != "stale-resource-version" || command.Spec.Preconditions.WorkItemName != workItem.Name {
		t.Fatalf("command preconditions = %#v", command.Spec)
	}
	expectedProof := triggersv1alpha1.MaintainerWorkItemCommandProof([]byte("01234567890123456789012345678901"), maintainerTestRepositoryName, "repo-uid", command.Spec.IdempotencyKey, command.Spec.PayloadHash, maintainerTestRunName, command.Spec.Issuer.UID)
	if command.Spec.Issuer.RunName != maintainerTestRunName || string(command.Spec.Issuer.UID) != maintainerTestRunUID || command.Spec.Issuer.Proof != expectedProof || command.Spec.RepositoryRef.Name != maintainerTestRepositoryName {
		t.Fatalf("command issuer/repository = %#v", command.Spec)
	}
	if command.Labels[triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey] != maintainerTestRepositoryName || command.Labels[triggersv1alpha1.MaintainerWorkItemIssueNumberLabelKey] != "42" {
		t.Fatalf("command labels = %#v", command.Labels)
	}
	if len(command.OwnerReferences) != 1 || command.OwnerReferences[0].APIVersion != triggersv1alpha1.GroupVersion.String() || command.OwnerReferences[0].Kind != maintainerTestRepositoryKind || command.OwnerReferences[0].Name != maintainerTestRepositoryName || command.OwnerReferences[0].Controller == nil || !*command.OwnerReferences[0].Controller {
		t.Fatalf("command owner references = %#v", command.OwnerReferences)
	}

	after := &triggersv1alpha1.MaintainerWorkItem{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(workItem), after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("work item changed:\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestTriageIssueReplayAndPayloadMismatch(t *testing.T) {
	t.Parallel()

	base, k8sClient, _ := newMaintainerToolBase(t, maintainerRun())
	workItem := createMaintainerWorkItem(t, k8sClient, maintainerTestRepositoryName, 7, 4)
	tool := &triageIssueTool{maintainerToolBase: base}
	input := triageIssueInputJSON(t, 7, "Evidence", "rv", 4, "same-key")
	first, err := tool.Execute(context.Background(), input, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.IsError {
		t.Fatalf("first result = %#v", first)
	}
	var submitted triageIssueOutput
	if err := json.Unmarshal([]byte(first.Content), &submitted); err != nil {
		t.Fatal(err)
	}
	command := &triggersv1alpha1.MaintainerWorkItemCommand{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: submitted.CommandName, Namespace: maintainerTestNamespace}, command); err != nil {
		t.Fatal(err)
	}
	command.Status.Phase = triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded
	command.Status.Result = &triggersv1alpha1.MaintainerWorkItemCommandResult{WorkItemRef: triggersRepositoryRef(workItem.Name), Applied: true, Message: "triage intent recorded"}
	if err := k8sClient.Update(context.Background(), command); err != nil {
		t.Fatal(err)
	}

	second, err := tool.Execute(context.Background(), input, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.IsError {
		t.Fatalf("second result = %#v", second)
	}
	var receipt triageIssueOutput
	if err := json.Unmarshal([]byte(second.Content), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Replayed || receipt.Phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded || receipt.Result == nil || receipt.Result.Message != "triage intent recorded" {
		t.Fatalf("replay receipt = %#v", receipt)
	}

	mismatch, err := tool.Execute(context.Background(), triageIssueInputJSON(t, 7, "Changed evidence", "rv", 4, "same-key"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !mismatch.IsError || !strings.Contains(mismatch.Content, "idempotency payload mismatch") {
		t.Fatalf("mismatch result = %#v", mismatch)
	}
}

func TestTriageIssueAuthorizesMaintainerRun(t *testing.T) {
	t.Parallel()

	unauthorized := maintainerRun()
	unauthorized.Labels[orchestration.StandingRunRoleLabel] = maintainerTestOther
	base, k8sClient, _ := newMaintainerToolBase(t, unauthorized)
	createMaintainerWorkItem(t, k8sClient, maintainerTestRepositoryName, 3, 0)
	result, err := (&triageIssueTool{maintainerToolBase: base}).Execute(context.Background(), triageIssueInputJSON(t, 3, "Evidence", "rv", 0, "key"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Content, "not authorized as a maintainer") {
		t.Fatalf("result = %#v", result)
	}
}

func createMaintainerWorkItem(t *testing.T, k8sClient client.Client, repository string, issueNumber int32, sequence int64) *triggersv1alpha1.MaintainerWorkItem {
	t.Helper()
	workItem := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{Name: maintainerWorkItemName(repository, issueNumber), Namespace: maintainerTestNamespace},
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{
			RepositoryRef: triggersRepositoryRef(repository),
			IssueNumber:   issueNumber,
		},
		Status: triggersv1alpha1.MaintainerWorkItemStatus{ProjectionSequence: sequence},
	}
	if err := k8sClient.Create(context.Background(), workItem); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(workItem), workItem); err != nil {
		t.Fatal(err)
	}
	return workItem
}

func triggersRepositoryRef(name string) corev1.LocalObjectReference {
	return corev1.LocalObjectReference{Name: name}
}

func triageIssueInputJSON(t *testing.T, issueNumber int, evidence, resourceVersion string, sequence int64, idempotencyKey string) json.RawMessage {
	t.Helper()
	input := map[string]any{
		"issue_number":                 issueNumber,
		"disposition":                  "Bounded",
		"evidence_summary":             evidence,
		"accepted_scope":               map[string]any{"statement": "scope", "acceptance_criteria": []string{"criterion"}},
		"idempotency_key":              idempotencyKey,
		"expected_projection_sequence": sequence,
		"expected_resource_version":    resourceVersion,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
