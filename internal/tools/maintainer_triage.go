package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var maintainerIdempotencyValid = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

type triageIssueTool struct{ maintainerToolBase }

type triageIssueAcceptedScopeInput struct {
	Statement          string   `json:"statement"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
}

type triageIssueInput struct {
	IssueNumber                int32                                           `json:"issue_number"`
	Disposition                triggersv1alpha1.MaintainerWorkItemDisposition  `json:"disposition"`
	EvidenceSummary            string                                          `json:"evidence_summary"`
	AcceptedScope              *triageIssueAcceptedScopeInput                  `json:"accepted_scope"`
	CloseReason                *triggersv1alpha1.MaintainerWorkItemCloseReason `json:"close_reason,omitempty"`
	IdempotencyKey             string                                          `json:"idempotency_key"`
	ExpectedProjectionSequence *int64                                          `json:"expected_projection_sequence"`
	ExpectedResourceVersion    string                                          `json:"expected_resource_version"`
}

type triageIssueWorkItemOutput struct {
	Name               string `json:"name"`
	ResourceVersion    string `json:"resource_version"`
	ProjectionSequence int64  `json:"projection_sequence"`
}

type triageIssueOutput struct {
	CommandName string                                            `json:"command_name"`
	Phase       triggersv1alpha1.MaintainerWorkItemCommandPhase   `json:"phase"`
	Replayed    bool                                              `json:"replayed"`
	PayloadHash string                                            `json:"payload_hash"`
	WorkItem    triageIssueWorkItemOutput                         `json:"work_item"`
	Result      *triggersv1alpha1.MaintainerWorkItemCommandResult `json:"result,omitempty"`
}

func (t *triageIssueTool) Name() string { return "triage_issue" }
func (t *triageIssueTool) Description() string {
	return "Submit an immutable, idempotent triage command for one maintained repository issue."
}
func (t *triageIssueTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"issue_number":{"type":"integer","minimum":1},"disposition":{"type":"string","enum":["NotActionable","Bounded","Decomposable","Discovery","Escalated"]},"evidence_summary":{"type":"string","minLength":1},"accepted_scope":{"type":"object","properties":{"statement":{"type":"string"},"acceptance_criteria":{"type":"array","items":{"type":"string"}}}},"close_reason":{"type":"string","enum":["not_planned","completed"]},"idempotency_key":{"type":"string","minLength":1,"maxLength":128,"pattern":"^[A-Za-z0-9][A-Za-z0-9._:-]*$"},"expected_projection_sequence":{"type":"integer","minimum":0},"expected_resource_version":{"type":"string","minLength":1}},"required":["issue_number","disposition","evidence_summary","accepted_scope","idempotency_key","expected_projection_sequence","expected_resource_version"]}`)
}
func (t *triageIssueTool) IsReadOnly() bool                      { return false }
func (t *triageIssueTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *triageIssueTool) NeedsApproval() bool                   { return false }
func (t *triageIssueTool) TimeoutSeconds() int                   { return 0 }

func (t *triageIssueTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in triageIssueInput
	if err := json.Unmarshal(input, &in); err != nil {
		return triageIssueError("invalid input: %v", err)
	}
	if err := validateTriageIssueInput(in); err != nil {
		return triageIssueError("%v", err)
	}

	current, err := t.currentRun(ctx)
	if err != nil {
		return triageIssueError("%v", err)
	}
	repository, err := t.repository(ctx)
	if err != nil {
		return triageIssueError("%v", err)
	}

	workItemName := maintainerWorkItemName(repository.Name, in.IssueNumber)
	workItem := &triggersv1alpha1.MaintainerWorkItem{}
	if err := t.k8sClient.Get(ctx, client.ObjectKey{Name: workItemName, Namespace: repository.Namespace}, workItem); err != nil {
		return triageIssueError("failed to get maintainer work item %q: %v", workItemName, err)
	}
	if workItem.Spec.RepositoryRef.Name != repository.Name || workItem.Spec.IssueNumber != in.IssueNumber {
		return triageIssueError("maintainer work item %q does not belong to repository %q issue #%d", workItem.Name, repository.Name, in.IssueNumber)
	}

	triage := &triggersv1alpha1.MaintainerTriageCommand{
		IssueNumber:     in.IssueNumber,
		Disposition:     in.Disposition,
		EvidenceSummary: in.EvidenceSummary,
		AcceptedScope: triggersv1alpha1.MaintainerAcceptedScope{
			Statement:          in.AcceptedScope.Statement,
			AcceptanceCriteria: append([]string(nil), in.AcceptedScope.AcceptanceCriteria...),
		},
		CloseReason: in.CloseReason,
	}
	preconditions := triggersv1alpha1.MaintainerWorkItemCommandPreconditions{
		WorkItemName:       workItem.Name,
		ProjectionSequence: *in.ExpectedProjectionSequence,
		ResourceVersion:    in.ExpectedResourceVersion,
	}
	payloadHash, err := triageIssuePayloadHash(triage, preconditions)
	if err != nil {
		return triageIssueError("failed to hash triage command payload: %v", err)
	}
	capability := &corev1.Secret{}
	capabilityName := triggersv1alpha1.MaintainerCommandCapabilitySecretName(current.Name)
	if err := t.k8sClient.Get(ctx, client.ObjectKey{Namespace: current.Namespace, Name: capabilityName}, capability); err != nil {
		return triageIssueError("failed to read maintainer command capability: %v", err)
	}
	if !metav1.IsControlledBy(capability, current) {
		return triageIssueError("maintainer command capability is not owned by the current AgentRun")
	}
	if string(capability.Data[triggersv1alpha1.MaintainerCommandCapabilityRepositoryNameKey]) != repository.Name || string(capability.Data[triggersv1alpha1.MaintainerCommandCapabilityRepositoryUIDKey]) != string(repository.UID) {
		return triageIssueError("maintainer command capability is bound to a different GitHubRepository")
	}
	capabilityKey := capability.Data[triggersv1alpha1.MaintainerCommandCapabilitySecretKey]
	if len(capabilityKey) < 32 {
		return triageIssueError("maintainer command capability is invalid")
	}
	proof := triggersv1alpha1.MaintainerWorkItemCommandProof(capabilityKey, repository.Name, repository.UID, in.IdempotencyKey, payloadHash, current.Name, current.UID)
	command := &triggersv1alpha1.MaintainerWorkItemCommand{
		ObjectMeta: metav1.ObjectMeta{
			Name:      MaintainerWorkItemCommandName(repository.Name, in.IdempotencyKey),
			Namespace: repository.Namespace,
			Labels: map[string]string{
				triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey:  repository.Name,
				triggersv1alpha1.MaintainerWorkItemIssueNumberLabelKey: strconv.Itoa(int(in.IssueNumber)),
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(repository, triggersv1alpha1.GroupVersion.WithKind("GitHubRepository"))},
		},
		Spec: triggersv1alpha1.MaintainerWorkItemCommandSpec{
			RepositoryRef:  workItem.Spec.RepositoryRef,
			IdempotencyKey: in.IdempotencyKey,
			PayloadHash:    payloadHash,
			Issuer: triggersv1alpha1.MaintainerWorkItemCommandIssuer{
				RunName: current.Name,
				UID:     current.UID,
				Proof:   proof,
			},
			Preconditions: preconditions,
			Type:          triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue,
			Triage:        triage,
		},
	}
	if err := t.k8sClient.Create(ctx, command); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return triageIssueError("failed to create triage command: %v", err)
		}
		existing := &triggersv1alpha1.MaintainerWorkItemCommand{}
		if err := t.k8sClient.Get(ctx, client.ObjectKeyFromObject(command), existing); err != nil {
			return triageIssueError("failed to get existing triage command: %v", err)
		}
		if existing.Spec.IdempotencyKey != in.IdempotencyKey || existing.Spec.PayloadHash != payloadHash || existing.Spec.Issuer.UID != current.UID || existing.Spec.Issuer.Proof != proof || existing.Spec.RepositoryRef.Name != workItem.Spec.RepositoryRef.Name {
			return triageIssueError("idempotency payload mismatch for triage command %q", command.Name)
		}
		return triageIssueResult(existing, workItem, true)
	}
	return triageIssueResult(command, workItem, false)
}

func validateTriageIssueInput(in triageIssueInput) error {
	if in.IssueNumber < 1 {
		return fmt.Errorf("issue_number must be greater than zero")
	}
	switch in.Disposition {
	case triggersv1alpha1.MaintainerWorkItemDispositionNotActionable,
		triggersv1alpha1.MaintainerWorkItemDispositionBounded,
		triggersv1alpha1.MaintainerWorkItemDispositionDecomposable,
		triggersv1alpha1.MaintainerWorkItemDispositionDiscovery,
		triggersv1alpha1.MaintainerWorkItemDispositionEscalated:
	default:
		return fmt.Errorf("disposition must be an accepted MaintainerWorkItemDisposition")
	}
	if strings.TrimSpace(in.EvidenceSummary) == "" {
		return fmt.Errorf("evidence_summary is required")
	}
	if in.AcceptedScope == nil {
		return fmt.Errorf("accepted_scope is required")
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" || len(in.IdempotencyKey) > 128 || !maintainerIdempotencyValid.MatchString(in.IdempotencyKey) {
		return fmt.Errorf("idempotency_key must be a valid non-empty command idempotency key")
	}
	if in.ExpectedProjectionSequence == nil || *in.ExpectedProjectionSequence < 0 {
		return fmt.Errorf("expected_projection_sequence is required and must be non-negative")
	}
	if strings.TrimSpace(in.ExpectedResourceVersion) == "" {
		return fmt.Errorf("expected_resource_version is required")
	}
	if in.Disposition == triggersv1alpha1.MaintainerWorkItemDispositionNotActionable {
		if in.CloseReason == nil {
			return fmt.Errorf("close_reason is required when disposition is NotActionable")
		}
	} else if in.CloseReason != nil {
		return fmt.Errorf("close_reason is only allowed when disposition is NotActionable")
	}
	if in.CloseReason != nil && *in.CloseReason != triggersv1alpha1.MaintainerWorkItemCloseReasonNotPlanned && *in.CloseReason != triggersv1alpha1.MaintainerWorkItemCloseReasonCompleted {
		return fmt.Errorf("close_reason must be an accepted MaintainerWorkItemCloseReason")
	}
	return nil
}

func triageIssuePayloadHash(triage *triggersv1alpha1.MaintainerTriageCommand, preconditions triggersv1alpha1.MaintainerWorkItemCommandPreconditions) (string, error) {
	return triggersv1alpha1.MaintainerWorkItemCommandPayloadHash(triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue, triage, preconditions), nil
}

func triageIssueResult(command *triggersv1alpha1.MaintainerWorkItemCommand, workItem *triggersv1alpha1.MaintainerWorkItem, replayed bool) (Result, error) {
	phase := command.Status.Phase
	if phase == "" {
		phase = triggersv1alpha1.MaintainerWorkItemCommandPhasePending
	}
	encoded, err := json.Marshal(triageIssueOutput{
		CommandName: command.Name,
		Phase:       phase,
		Replayed:    replayed,
		PayloadHash: command.Spec.PayloadHash,
		WorkItem: triageIssueWorkItemOutput{
			Name:               workItem.Name,
			ResourceVersion:    workItem.ResourceVersion,
			ProjectionSequence: workItem.Status.ProjectionSequence,
		},
		Result: command.Status.Result,
	})
	if err != nil {
		return triageIssueError("failed to encode triage command receipt: %v", err)
	}
	return Result{Content: string(encoded)}, nil
}

func triageIssueError(format string, args ...any) (Result, error) {
	return Result{Content: fmt.Sprintf(format, args...), IsError: true}, nil
}

func maintainerWorkItemName(repositoryName string, issueNumber int32) string {
	return triggersv1alpha1.MaintainerWorkItemName(repositoryName, issueNumber)
}

// MaintainerWorkItemCommandName returns the deterministic DNS-safe command name.
func MaintainerWorkItemCommandName(repositoryName, idempotencyKey string) string {
	return triggersv1alpha1.MaintainerWorkItemCommandName(repositoryName, idempotencyKey)
}
