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
	return "Dispatch one already-triaged repository issue by applying a ModeTemplate label through GitHub trigger ingress, subject to active and daily caps. Post the required evidence-backed decision comment separately before calling this tool. The optional note is posted only after labeling and must not be used as the pre-dispatch decision record."
}
func (t *dispatchIssueTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"issue_number":{"type":"integer","minimum":1},"mode":{"type":"string"},"note":{"type":"string"}},"required":["issue_number","mode"]}`)
}
func (t *dispatchIssueTool) IsReadOnly() bool                      { return false }
func (t *dispatchIssueTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *dispatchIssueTool) NeedsApproval() bool                   { return false }
func (t *dispatchIssueTool) TimeoutSeconds() int                   { return 0 }

func (t *dispatchIssueTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
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
	current, err := t.currentRun(ctx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	repository, err := t.repository(ctx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	fleet, err := t.fleetRuns(ctx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	maxConcurrent, maxDaily := maintainerDispatchCaps(repository)
	active := 0
	for i := range fleet {
		if !maintainerIsReviewer(&fleet[i]) && !maintainerTerminal(fleet[i].Status.Phase) {
			active++
		}
	}
	if active >= int(maxConcurrent) {
		return Result{Content: fmt.Sprintf("dispatch cap reached (%d active of %d); wait for a fleet transition with wait_for_repo_events before dispatching another issue", active, maxConcurrent), IsError: true}, nil
	}
	ledger := parseMaintainerLedger(current, time.Now())
	if ledger.Count >= int(maxDaily) {
		return Result{Content: fmt.Sprintf("daily dispatch cap reached (%d of %d); wait until the next UTC day", ledger.Count, maxDaily), IsError: true}, nil
	}
	if err := t.validateMode(ctx, mode); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	wd, err := resolveLocalGitRepositoryWorkDir(workDir, "")
	if err != nil {
		return Result{Content: fmt.Sprintf("workspace repository unavailable: %v", err), IsError: true}, nil
	}
	runner := t.runner
	if runner == nil {
		runner = prReviewExecRunner{}
	}
	issueNumber := strconv.Itoa(in.IssueNumber)
	out, err := runner.RunGH(ctx, wd, "issue", "edit", issueNumber, "--add-label", mode)
	if err != nil {
		return Result{Content: fmt.Sprintf("gh issue edit failed: %v\n%s", err, out), IsError: true}, nil
	}
	if note := strings.TrimSpace(in.Note); note != "" {
		payload, _ := json.Marshal(map[string]string{"body": attributeGitHubComment(note)})
		out, err = runner.RunGHWithInput(ctx, wd, string(payload), "api", "--method", "POST", "repos/{owner}/{repo}/issues/"+issueNumber+"/comments", "--input", "-")
		if err != nil {
			return Result{Content: fmt.Sprintf("gh issue comment failed: %v\n%s", err, out), IsError: true}, nil
		}
	}
	if err := t.recordDispatch(ctx, in.IssueNumber); err != nil {
		return Result{Content: fmt.Sprintf("issue was labeled but failed to record the dispatch ledger: %v", err), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("Issue #%d was labeled %q. Trigger ingress will create its AgentRun shortly; find it with get_fleet_runs (externalRef identifier %d), then return to wait_for_repo_events.", in.IssueNumber, mode, in.IssueNumber)}, nil
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

func (t *dispatchIssueTool) recordDispatch(ctx context.Context, issueNumber int) error {
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
		ledger.Count++
		ledger.Issues = append(ledger.Issues, issueNumber)
		encoded, err := json.Marshal(ledger)
		if err != nil {
			return err
		}
		patch := client.MergeFrom(run.DeepCopy())
		if run.Annotations == nil {
			run.Annotations = map[string]string{}
		}
		run.Annotations[triggersv1alpha1.MaintainerDispatchLedgerAnnotation] = string(encoded)
		return t.k8sClient.Patch(ctx, run, patch)
	})
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
