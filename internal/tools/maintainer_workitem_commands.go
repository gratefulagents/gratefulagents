package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type maintainerCommandInput struct {
	IssueNumber                int32  `json:"issue_number"`
	IdempotencyKey             string `json:"idempotency_key"`
	ExpectedProjectionSequence *int64 `json:"expected_projection_sequence"`
	ExpectedResourceVersion    string `json:"expected_resource_version"`
}

type breakdownIssueTool struct{ maintainerToolBase }
type breakdownIssueInput struct {
	maintainerCommandInput
	ChildIssueNumbers      []int32 `json:"child_issue_numbers"`
	DependencyIssueNumbers []int32 `json:"dependency_issue_numbers,omitempty"`
}

func (t *breakdownIssueTool) Name() string { return "breakdown_issue" }
func (t *breakdownIssueTool) Description() string {
	return "Submit an authenticated, idempotent command that records existing child work items and validated acyclic dependencies."
}
func (t *breakdownIssueTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"issue_number":{"type":"integer","minimum":1},"child_issue_numbers":{"type":"array","minItems":1,"uniqueItems":true,"items":{"type":"integer","minimum":1}},"dependency_issue_numbers":{"type":"array","uniqueItems":true,"items":{"type":"integer","minimum":1}},"idempotency_key":{"type":"string","minLength":1,"maxLength":128},"expected_projection_sequence":{"type":"integer","minimum":0},"expected_resource_version":{"type":"string","minLength":1}},"required":["issue_number","child_issue_numbers","idempotency_key","expected_projection_sequence","expected_resource_version"]}`)
}
func (t *breakdownIssueTool) IsReadOnly() bool                    { return false }
func (t *breakdownIssueTool) IsEnabled(*agentsdk.RunContext) bool { return true }
func (t *breakdownIssueTool) NeedsApproval() bool                 { return false }
func (t *breakdownIssueTool) TimeoutSeconds() int                 { return 0 }
func (t *breakdownIssueTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (Result, error) {
	var in breakdownIssueInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return maintainerCommandError("invalid input: %v", err)
	}
	if len(in.ChildIssueNumbers) == 0 {
		return maintainerCommandError("child_issue_numbers is required")
	}
	item, repository, current, preconditions, err := t.commandContext(ctx, in.maintainerCommandInput)
	if err != nil {
		return maintainerCommandError("%v", err)
	}
	children, err := t.workItemRefs(ctx, repository.Name, repository.Namespace, in.ChildIssueNumbers)
	if err != nil {
		return maintainerCommandError("%v", err)
	}
	dependencies, err := t.workItemRefs(ctx, repository.Name, repository.Namespace, in.DependencyIssueNumbers)
	if err != nil {
		return maintainerCommandError("%v", err)
	}
	spec := triggersv1alpha1.MaintainerWorkItemCommandSpec{RepositoryRef: item.Spec.RepositoryRef, IdempotencyKey: in.IdempotencyKey, Preconditions: preconditions, Type: triggersv1alpha1.MaintainerWorkItemCommandTypeBreakdownIssue, Breakdown: &triggersv1alpha1.MaintainerBreakdownCommand{IssueNumber: in.IssueNumber, Children: children, Dependencies: dependencies}}
	return t.submitCommand(ctx, repository, current, item, spec)
}

type requestDecisionTool struct{ maintainerToolBase }
type requestDecisionInput struct {
	maintainerCommandInput
	DecisionID string   `json:"decision_id"`
	Question   string   `json:"question"`
	Options    []string `json:"options,omitempty"`
}

func (t *requestDecisionTool) Name() string { return "request_decision" }
func (t *requestDecisionTool) Description() string {
	return "Submit an authenticated decision request that durably blocks the work item pending an authorized answer."
}
func (t *requestDecisionTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"issue_number":{"type":"integer","minimum":1},"decision_id":{"type":"string","minLength":1},"question":{"type":"string","minLength":1},"options":{"type":"array","items":{"type":"string"}},"idempotency_key":{"type":"string","minLength":1,"maxLength":128},"expected_projection_sequence":{"type":"integer","minimum":0},"expected_resource_version":{"type":"string","minLength":1}},"required":["issue_number","decision_id","question","idempotency_key","expected_projection_sequence","expected_resource_version"]}`)
}
func (t *requestDecisionTool) IsReadOnly() bool                    { return false }
func (t *requestDecisionTool) IsEnabled(*agentsdk.RunContext) bool { return true }
func (t *requestDecisionTool) NeedsApproval() bool                 { return false }
func (t *requestDecisionTool) TimeoutSeconds() int                 { return 0 }
func (t *requestDecisionTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (Result, error) {
	var in requestDecisionInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return maintainerCommandError("invalid input: %v", err)
	}
	if strings.TrimSpace(in.DecisionID) == "" || strings.TrimSpace(in.Question) == "" {
		return maintainerCommandError("decision_id and question are required")
	}
	item, repository, current, preconditions, err := t.commandContext(ctx, in.maintainerCommandInput)
	if err != nil {
		return maintainerCommandError("%v", err)
	}
	spec := triggersv1alpha1.MaintainerWorkItemCommandSpec{RepositoryRef: item.Spec.RepositoryRef, IdempotencyKey: in.IdempotencyKey, Preconditions: preconditions, Type: triggersv1alpha1.MaintainerWorkItemCommandTypeRequestDecision, RequestDecision: &triggersv1alpha1.MaintainerRequestDecisionCommand{IssueNumber: in.IssueNumber, DecisionID: in.DecisionID, Question: in.Question, Options: append([]string(nil), in.Options...)}}
	return t.submitCommand(ctx, repository, current, item, spec)
}

type answerDecisionTool struct{ maintainerToolBase }
type answerDecisionInput struct {
	maintainerCommandInput
	DecisionID   string `json:"decision_id"`
	HumanSubject string `json:"human_subject"`
	Answer       string `json:"answer"`
}

func (t *answerDecisionTool) Name() string { return "answer_decision" }
func (t *answerDecisionTool) Description() string {
	return "Resolve pendingDecision through the platform's authenticated human-approval gate; the approved subject and answer are signed by the authorized standing maintainer and durably audited."
}
func (t *answerDecisionTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"issue_number":{"type":"integer","minimum":1},"decision_id":{"type":"string","minLength":1},"human_subject":{"type":"string","minLength":1},"answer":{"type":"string","minLength":1},"idempotency_key":{"type":"string","minLength":1,"maxLength":128},"expected_projection_sequence":{"type":"integer","minimum":0},"expected_resource_version":{"type":"string","minLength":1}},"required":["issue_number","decision_id","human_subject","answer","idempotency_key","expected_projection_sequence","expected_resource_version"]}`)
}
func (t *answerDecisionTool) IsReadOnly() bool                    { return false }
func (t *answerDecisionTool) IsEnabled(*agentsdk.RunContext) bool { return true }

// NeedsApproval uses the platform's authenticated human approval channel as
// the trust boundary; the maintainer AgentRun cannot clear pendingDecision alone.
func (t *answerDecisionTool) NeedsApproval() bool { return true }
func (t *answerDecisionTool) TimeoutSeconds() int { return 0 }
func (t *answerDecisionTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (Result, error) {
	var in answerDecisionInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return maintainerCommandError("invalid input: %v", err)
	}
	if strings.TrimSpace(in.DecisionID) == "" || strings.TrimSpace(in.HumanSubject) == "" || strings.TrimSpace(in.Answer) == "" {
		return maintainerCommandError("decision_id, human_subject, and answer are required")
	}
	item, repository, current, preconditions, err := t.commandContext(ctx, in.maintainerCommandInput)
	if err != nil {
		return maintainerCommandError("%v", err)
	}
	spec := triggersv1alpha1.MaintainerWorkItemCommandSpec{RepositoryRef: item.Spec.RepositoryRef, IdempotencyKey: in.IdempotencyKey, Preconditions: preconditions, Type: triggersv1alpha1.MaintainerWorkItemCommandTypeResolveDecision, ResolveDecision: &triggersv1alpha1.MaintainerResolveDecisionCommand{IssueNumber: in.IssueNumber, DecisionID: in.DecisionID, HumanAnswer: triggersv1alpha1.MaintainerAuthenticatedHumanAnswer{Subject: in.HumanSubject, Answer: in.Answer}}}
	return t.submitCommand(ctx, repository, current, item, spec)
}

type dispatchWorkItemTool struct{ maintainerToolBase }
type dispatchWorkItemInput struct {
	maintainerCommandInput
	Mode                 string   `json:"mode"`
	RequiredPullRequests []string `json:"required_pull_requests,omitempty"`
}

func (t *dispatchWorkItemTool) Name() string { return "dispatch_work_item" }
func (t *dispatchWorkItemTool) Description() string {
	return "Submit an authenticated work-item dispatch command. The controller atomically reserves daily/concurrent capacity before applying the GitHub trigger label."
}
func (t *dispatchWorkItemTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"issue_number":{"type":"integer","minimum":1},"mode":{"type":"string","minLength":1},"required_pull_requests":{"type":"array","uniqueItems":true,"items":{"type":"string","minLength":1}},"idempotency_key":{"type":"string","minLength":1,"maxLength":128},"expected_projection_sequence":{"type":"integer","minimum":0},"expected_resource_version":{"type":"string","minLength":1}},"required":["issue_number","mode","idempotency_key","expected_projection_sequence","expected_resource_version"]}`)
}
func (t *dispatchWorkItemTool) IsReadOnly() bool                    { return false }
func (t *dispatchWorkItemTool) IsEnabled(*agentsdk.RunContext) bool { return true }
func (t *dispatchWorkItemTool) NeedsApproval() bool                 { return false }
func (t *dispatchWorkItemTool) TimeoutSeconds() int                 { return 0 }
func (t *dispatchWorkItemTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (Result, error) {
	var in dispatchWorkItemInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return maintainerCommandError("invalid input: %v", err)
	}
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode == "" || strings.ContainsAny(mode, " \t\n\r") {
		return maintainerCommandError("mode must be a lowercase trimmed ModeTemplate name")
	}
	item, repository, current, preconditions, err := t.commandContext(ctx, in.maintainerCommandInput)
	if err != nil {
		return maintainerCommandError("%v", err)
	}
	intents := make([]triggersv1alpha1.MaintainerRequiredPullRequestIntent, 0, len(in.RequiredPullRequests))
	for _, name := range in.RequiredPullRequests {
		name = strings.TrimSpace(name)
		if name == "" {
			return maintainerCommandError("required_pull_requests cannot contain empty names")
		}
		intents = append(intents, triggersv1alpha1.MaintainerRequiredPullRequestIntent{Name: name})
	}
	spec := triggersv1alpha1.MaintainerWorkItemCommandSpec{RepositoryRef: item.Spec.RepositoryRef, IdempotencyKey: in.IdempotencyKey, Preconditions: preconditions, Type: triggersv1alpha1.MaintainerWorkItemCommandTypeDispatchWorkItem, Dispatch: &triggersv1alpha1.MaintainerDispatchWorkItemCommand{IssueNumber: in.IssueNumber, Mode: mode, RequiredPullRequests: intents}}
	return t.submitCommand(ctx, repository, current, item, spec)
}

func (t maintainerToolBase) commandContext(ctx context.Context, in maintainerCommandInput) (*triggersv1alpha1.MaintainerWorkItem, *triggersv1alpha1.GitHubRepository, *platformv1alpha1.AgentRun, triggersv1alpha1.MaintainerWorkItemCommandPreconditions, error) {
	if in.IssueNumber < 1 || strings.TrimSpace(in.IdempotencyKey) == "" || len(in.IdempotencyKey) > 128 || !maintainerIdempotencyValid.MatchString(in.IdempotencyKey) || in.ExpectedProjectionSequence == nil || *in.ExpectedProjectionSequence < 0 || strings.TrimSpace(in.ExpectedResourceVersion) == "" {
		return nil, nil, nil, triggersv1alpha1.MaintainerWorkItemCommandPreconditions{}, fmt.Errorf("issue_number, valid idempotency_key, expected_projection_sequence, and expected_resource_version are required")
	}
	current, err := t.currentRun(ctx)
	if err != nil {
		return nil, nil, nil, triggersv1alpha1.MaintainerWorkItemCommandPreconditions{}, err
	}
	repository, err := t.repository(ctx)
	if err != nil {
		return nil, nil, nil, triggersv1alpha1.MaintainerWorkItemCommandPreconditions{}, err
	}
	item := &triggersv1alpha1.MaintainerWorkItem{}
	name := maintainerWorkItemName(repository.Name, in.IssueNumber)
	if err := t.k8sClient.Get(ctx, client.ObjectKey{Namespace: repository.Namespace, Name: name}, item); err != nil {
		return nil, nil, nil, triggersv1alpha1.MaintainerWorkItemCommandPreconditions{}, fmt.Errorf("failed to get maintainer work item: %w", err)
	}
	return item, repository, current, triggersv1alpha1.MaintainerWorkItemCommandPreconditions{WorkItemName: item.Name, ProjectionSequence: *in.ExpectedProjectionSequence, ResourceVersion: in.ExpectedResourceVersion}, nil
}

func (t maintainerToolBase) workItemRefs(ctx context.Context, repository, namespace string, issues []int32) ([]triggersv1alpha1.MaintainerWorkItemReference, error) {
	refs := make([]triggersv1alpha1.MaintainerWorkItemReference, 0, len(issues))
	seen := map[int32]bool{}
	for _, issue := range issues {
		if issue < 1 || seen[issue] {
			return nil, fmt.Errorf("work-item issue numbers must be positive and unique")
		}
		seen[issue] = true
		item := &triggersv1alpha1.MaintainerWorkItem{}
		if err := t.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: maintainerWorkItemName(repository, issue)}, item); err != nil {
			return nil, fmt.Errorf("work item for issue #%d is unavailable: %w", issue, err)
		}
		refs = append(refs, triggersv1alpha1.MaintainerWorkItemReference{Name: item.Name, UID: item.UID})
	}
	return refs, nil
}

func (t maintainerToolBase) submitCommand(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, run *platformv1alpha1.AgentRun, item *triggersv1alpha1.MaintainerWorkItem, spec triggersv1alpha1.MaintainerWorkItemCommandSpec) (Result, error) {
	if run == nil {
		return maintainerCommandError("authorized maintainer run is unavailable")
	}
	spec.PayloadHash = triggersv1alpha1.MaintainerWorkItemCommandSpecPayloadHash(spec)
	capability := &corev1.Secret{}
	if err := t.k8sClient.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: triggersv1alpha1.MaintainerCommandCapabilitySecretName(run.Name)}, capability); err != nil {
		return maintainerCommandError("failed to read maintainer command capability: %v", err)
	}
	if !metav1.IsControlledBy(capability, run) || string(capability.Data[triggersv1alpha1.MaintainerCommandCapabilityRepositoryNameKey]) != repository.Name || string(capability.Data[triggersv1alpha1.MaintainerCommandCapabilityRepositoryUIDKey]) != string(repository.UID) || len(capability.Data[triggersv1alpha1.MaintainerCommandCapabilitySecretKey]) < 32 {
		return maintainerCommandError("maintainer command capability is invalid")
	}
	spec.Issuer = triggersv1alpha1.MaintainerWorkItemCommandIssuer{RunName: run.Name, UID: run.UID, Proof: triggersv1alpha1.MaintainerWorkItemCommandProof(capability.Data[triggersv1alpha1.MaintainerCommandCapabilitySecretKey], repository.Name, repository.UID, spec.IdempotencyKey, spec.PayloadHash, run.Name, run.UID)}
	command := &triggersv1alpha1.MaintainerWorkItemCommand{ObjectMeta: metav1.ObjectMeta{Name: MaintainerWorkItemCommandName(repository.Name, spec.IdempotencyKey), Namespace: repository.Namespace, Labels: map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name, triggersv1alpha1.MaintainerWorkItemIssueNumberLabelKey: strconv.Itoa(int(item.Spec.IssueNumber))}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(repository, triggersv1alpha1.GroupVersion.WithKind("GitHubRepository"))}}, Spec: spec}
	replayed := false
	if err := t.k8sClient.Create(ctx, command); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return maintainerCommandError("failed to create work-item command: %v", err)
		}
		replayed = true
		existing := &triggersv1alpha1.MaintainerWorkItemCommand{}
		if err := t.k8sClient.Get(ctx, client.ObjectKeyFromObject(command), existing); err != nil {
			return maintainerCommandError("failed to get existing command: %v", err)
		}
		if existing.Spec.PayloadHash != spec.PayloadHash || existing.Spec.Issuer.UID != run.UID {
			return maintainerCommandError("idempotency payload mismatch for command %q", command.Name)
		}
		command = existing
	}
	phase := command.Status.Phase
	if phase == "" {
		phase = triggersv1alpha1.MaintainerWorkItemCommandPhasePending
	}
	encoded, err := json.Marshal(map[string]any{"command_name": command.Name, "phase": phase, "replayed": replayed, "payload_hash": command.Spec.PayloadHash, "work_item": map[string]any{"name": item.Name, "resource_version": item.ResourceVersion, "projection_sequence": item.Status.ProjectionSequence}, "result": command.Status.Result})
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}

func maintainerCommandError(format string, args ...any) (Result, error) {
	return Result{Content: fmt.Sprintf(format, args...), IsError: true}, nil
}
