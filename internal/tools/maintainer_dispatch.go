package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type dispatchIssueTool struct {
	maintainerToolBase
	runner prReviewRunner
}

type dispatchIssueInput struct {
	IssueNumber int    `json:"issue_number"`
	Mode        string `json:"mode"`
	Note        string `json:"note,omitempty"`
}

func (t *dispatchIssueTool) Name() string { return "dispatch_issue" }
func (t *dispatchIssueTool) Description() string {
	return "Dispatch one already-triaged repository issue by atomically reserving capacity on the maintainer ledger before applying a ModeTemplate label through GitHub trigger ingress. Post the required evidence-backed decision comment separately before calling this tool. The optional note is posted only after labeling and must not be used as the pre-dispatch decision record."
}
func (t *dispatchIssueTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"issue_number":{"type":"integer","minimum":1},"mode":{"type":"string"},"note":{"type":"string"}},"required":["issue_number","mode"]}`)
}
func (t *dispatchIssueTool) IsReadOnly() bool                      { return false }
func (t *dispatchIssueTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *dispatchIssueTool) NeedsApproval() bool                   { return false }
func (t *dispatchIssueTool) TimeoutSeconds() int                   { return 0 }

func (t *dispatchIssueTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	if err := t.requireLegacyMutationAuthority(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	var in dispatchIssueInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if in.IssueNumber <= 0 {
		return Result{Content: "issue_number must be greater than zero", IsError: true}, nil
	}
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode == "" {
		return Result{Content: "mode is required", IsError: true}, nil
	}
	if strings.ContainsAny(mode, " \t\n\r") {
		return Result{Content: "mode must be a lowercase trimmed ModeTemplate name", IsError: true}, nil
	}
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	repository, err := t.repository(ctx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if err := t.validateMode(ctx, mode); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	// Phase-2 repositories always have a work item. Route the legacy surface
	// through the typed controller command so it shares the repository-scoped
	// atomic reservation ledger. The direct path below remains only for
	// pre-work-item migration compatibility.
	workItem := &triggersv1alpha1.MaintainerWorkItem{}
	workItemKey := client.ObjectKey{Namespace: t.repositoryNamespace, Name: maintainerWorkItemName(t.repositoryName, int32(in.IssueNumber))}
	if getErr := t.k8sClient.Get(ctx, workItemKey, workItem); getErr == nil {
		sequence := workItem.Status.ProjectionSequence
		typed := dispatchWorkItemInput{maintainerCommandInput: maintainerCommandInput{IssueNumber: int32(in.IssueNumber), IdempotencyKey: fmt.Sprintf("legacy-dispatch-%d-%s", in.IssueNumber, mode), ExpectedProjectionSequence: &sequence, ExpectedResourceVersion: workItem.ResourceVersion}, Mode: mode}
		encoded, marshalErr := json.Marshal(typed)
		if marshalErr != nil {
			return Result{}, marshalErr
		}
		return (&dispatchWorkItemTool{maintainerToolBase: t.maintainerToolBase}).Execute(ctx, encoded, workDir)
	} else if !apierrors.IsNotFound(getErr) {
		return Result{Content: fmt.Sprintf("getting maintainer work item: %v", getErr), IsError: true}, nil
	}
	wd, err := resolveLocalGitRepositoryWorkDir(workDir, "")
	if err != nil {
		return Result{Content: fmt.Sprintf("workspace repository unavailable: %v", err), IsError: true}, nil
	}
	fleet, err := t.fleetRuns(ctx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	maxConcurrent, maxDaily := maintainerDispatchCaps(repository)
	if err := t.reserveDispatch(ctx, in.IssueNumber, mode, fleet, maxConcurrent, maxDaily); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}

	runner := t.runner
	if runner == nil {
		runner = prReviewExecRunner{}
	}
	issueNumber := strconv.Itoa(in.IssueNumber)
	out, err := runner.RunGH(ctx, wd, "issue", "edit", issueNumber, "--add-label", mode)
	if err != nil {
		if isDefiniteIssueLabelFailure(out, err) {
			if releaseErr := t.releaseDispatchReservation(ctx, in.IssueNumber); releaseErr != nil {
				return Result{Content: fmt.Sprintf("gh issue edit failed: %v\n%s\nfailed to release the dispatch reservation: %v", err, out, releaseErr), IsError: true}, nil
			}
			return Result{Content: fmt.Sprintf("gh issue edit failed without applying the label: %v\n%s", err, out), IsError: true}, nil
		}
		return Result{Content: fmt.Sprintf("gh issue edit may have applied the label but its result was ambiguous; the dispatch reservation is retained to prevent replay: %v\n%s", err, out), IsError: true}, nil
	}
	if note := strings.TrimSpace(in.Note); note != "" {
		payload, _ := json.Marshal(map[string]string{"body": attributeGitHubComment(note)})
		out, err = runner.RunGHWithInput(ctx, wd, string(payload), "api", "--method", "POST", "repos/{owner}/{repo}/issues/"+issueNumber+"/comments", "--input", "-")
		if err != nil {
			return Result{Content: fmt.Sprintf("issue was labeled but gh issue comment failed: %v\n%s", err, out), IsError: true}, nil
		}
	}
	return Result{Content: fmt.Sprintf("Issue #%d was labeled %q. Its dispatch reservation remains until trigger ingress is visible in get_fleet_runs (externalRef identifier %d), then return to wait_for_repo_events.", in.IssueNumber, mode, in.IssueNumber)}, nil
}

func (t *dispatchIssueTool) validateMode(ctx context.Context, mode string) error {
	template := &platformv1alpha1.ModeTemplate{}
	err := t.k8sClient.Get(ctx, client.ObjectKey{Name: mode}, template)
	if err == nil || apierrors.IsForbidden(err) {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to validate ModeTemplate %q: %w", mode, err)
	}
	var templates platformv1alpha1.ModeTemplateList
	if listErr := t.k8sClient.List(ctx, &templates); listErr != nil {
		if apierrors.IsForbidden(listErr) || apierrors.IsNotFound(listErr) {
			return nil
		}
		return fmt.Errorf("failed to validate ModeTemplate %q: %w", mode, listErr)
	}
	for i := range templates.Items {
		if templates.Items[i].Name == mode {
			return nil
		}
	}
	return fmt.Errorf("ModeTemplate %q was not found", mode)
}

func (t *dispatchIssueTool) reserveDispatch(ctx context.Context, issueNumber int, mode string, fleet []platformv1alpha1.AgentRun, maxConcurrent, maxDaily int32) error {
	active := 0
	for i := range fleet {
		if !maintainerIsReviewer(&fleet[i]) && !maintainerTerminal(fleet[i].Status.Phase) {
			active++
		}
	}
	key := client.ObjectKey{Name: t.currentRunName, Namespace: t.currentRunNamespace}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := &platformv1alpha1.AgentRun{}
		if err := t.k8sClient.Get(ctx, key, run); err != nil {
			return err
		}
		if _, err := t.currentRunForLedger(run); err != nil {
			return err
		}
		ledger := parseMaintainerLedger(run, time.Now())
		ledger.Pending = pendingDispatchReservations(ledger.Pending, fleet)
		if maintainerLedgerHasIssue(ledger, issueNumber) {
			return fmt.Errorf("issue #%d was already dispatched or is still reserved today; do not replay dispatch_issue", issueNumber)
		}
		if active+len(ledger.Pending) >= int(maxConcurrent) {
			return fmt.Errorf("dispatch cap reached (%d active or reserved of %d); wait for a fleet transition with wait_for_repo_events before dispatching another issue", active+len(ledger.Pending), maxConcurrent)
		}
		if ledger.Count >= int(maxDaily) {
			return fmt.Errorf("daily dispatch cap reached (%d of %d); wait until the next UTC day", ledger.Count, maxDaily)
		}
		ledger.Count++
		ledger.Issues = append(ledger.Issues, issueNumber)
		ledger.Pending = append(ledger.Pending, maintainerDispatchReservation{Issue: issueNumber, Mode: mode})
		return t.patchDispatchLedger(ctx, run, ledger)
	})
}

func (t *dispatchIssueTool) releaseDispatchReservation(ctx context.Context, issueNumber int) error {
	key := client.ObjectKey{Name: t.currentRunName, Namespace: t.currentRunNamespace}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := &platformv1alpha1.AgentRun{}
		if err := t.k8sClient.Get(ctx, key, run); err != nil {
			return err
		}
		if _, err := t.currentRunForLedger(run); err != nil {
			return err
		}
		ledger := parseMaintainerLedger(run, time.Now())
		pending := make([]maintainerDispatchReservation, 0, len(ledger.Pending))
		found := false
		for _, reservation := range ledger.Pending {
			if reservation.Issue == issueNumber {
				found = true
				continue
			}
			pending = append(pending, reservation)
		}
		if !found {
			return nil
		}
		ledger.Pending = pending
		ledger.Issues = removeMaintainerLedgerIssue(ledger.Issues, issueNumber)
		ledger.Count--
		return t.patchDispatchLedger(ctx, run, ledger)
	})
}

func (t *dispatchIssueTool) patchDispatchLedger(ctx context.Context, run *platformv1alpha1.AgentRun, ledger maintainerDispatchLedger) error {
	encoded, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	patch := client.MergeFromWithOptions(run.DeepCopy(), client.MergeFromWithOptimisticLock{})
	if run.Annotations == nil {
		run.Annotations = map[string]string{}
	}
	run.Annotations[triggersv1alpha1.MaintainerDispatchLedgerAnnotation] = string(encoded)
	return t.k8sClient.Patch(ctx, run, patch)
}

func pendingDispatchReservations(pending []maintainerDispatchReservation, fleet []platformv1alpha1.AgentRun) []maintainerDispatchReservation {
	remaining := make([]maintainerDispatchReservation, 0, len(pending))
	for _, reservation := range pending {
		identifier := strconv.Itoa(reservation.Issue)
		delivered := false
		for i := range fleet {
			ref := fleet[i].Spec.Trigger.ExternalRef
			if !maintainerIsReviewer(&fleet[i]) && ref != nil && (strings.TrimPrefix(strings.TrimSpace(ref.Identifier), "#") == identifier || strings.TrimSpace(ref.ID) == identifier) {
				delivered = true
				break
			}
		}
		if !delivered {
			remaining = append(remaining, reservation)
		}
	}
	return remaining
}

func removeMaintainerLedgerIssue(issues []int, issueNumber int) []int {
	for i, issue := range issues {
		if issue == issueNumber {
			return append(issues[:i:i], issues[i+1:]...)
		}
	}
	return issues
}

func isDefiniteIssueLabelFailure(output string, err error) bool {
	return strings.Contains(strings.ToLower(output+"\n"+err.Error()), "http 4")
}

func (t *dispatchIssueTool) currentRunForLedger(run *platformv1alpha1.AgentRun) (*platformv1alpha1.AgentRun, error) {
	if run == nil || run.Namespace != t.repositoryNamespace || run.Labels[orchestration.StandingRunRoleLabel] != orchestration.StandingRunRoleMaintainer || run.Labels[orchestration.SupervisedRunLabel] != t.repositoryName {
		return nil, fmt.Errorf("current AgentRun is no longer authorized as a maintainer")
	}
	for _, owner := range run.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.Kind == "GitHubRepository" && owner.Name == t.repositoryName {
			return run, nil
		}
	}
	return nil, fmt.Errorf("current AgentRun is no longer owned by the maintained GitHubRepository")
}
