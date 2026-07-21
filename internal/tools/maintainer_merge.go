package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

type mergePullRequestTool struct {
	maintainerToolBase
	runner prReviewRunner
}

type mergePullRequestInput struct {
	PRNumber    int    `json:"pr_number"`
	MergeMethod string `json:"merge_method,omitempty"`
}

type mergePullRequestView struct {
	State          string `json:"state"`
	IsDraft        bool   `json:"isDraft"`
	Mergeable      string `json:"mergeable"`
	ReviewDecision string `json:"reviewDecision"`
	URL            string `json:"url"`
	HeadRefName    string `json:"headRefName"`
	HeadRefOid     string `json:"headRefOid"`
}

func (t *mergePullRequestTool) Name() string { return "merge_pull_request" }
func (t *mergePullRequestTool) Description() string {
	return "Dangerously merge one approved, non-draft, non-conflicting pull request when repository policy explicitly permits maintainer merges."
}
func (t *mergePullRequestTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pr_number":{"type":"integer","minimum":1},"merge_method":{"type":"string","enum":["squash","merge","rebase"]}},"required":["pr_number"]}`)
}
func (t *mergePullRequestTool) IsReadOnly() bool                      { return false }
func (t *mergePullRequestTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *mergePullRequestTool) NeedsApproval() bool                   { return false }
func (t *mergePullRequestTool) TimeoutSeconds() int                   { return 0 }

func (t *mergePullRequestTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in mergePullRequestInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if in.PRNumber <= 0 {
		return Result{Content: "pr_number must be greater than zero", IsError: true}, nil
	}
	method := strings.ToLower(strings.TrimSpace(in.MergeMethod))
	if method == "" {
		method = "squash"
	}
	if method != "squash" && method != "merge" && method != "rebase" {
		return Result{Content: `merge_method must be "squash", "merge", or "rebase"`, IsError: true}, nil
	}
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	repository, err := t.repository(ctx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if repository.Spec.Maintainer == nil || !repository.Spec.Maintainer.AllowPullRequestMerge {
		return Result{Content: "merge_pull_request is not enabled for this repository (spec.maintainer.allowPullRequestMerge); merging stays human", IsError: true}, nil
	}
	wd, err := resolveLocalGitRepositoryWorkDir(workDir, "")
	if err != nil {
		return Result{Content: fmt.Sprintf("workspace repository unavailable: %v", err), IsError: true}, nil
	}
	runner := t.runner
	if runner == nil {
		runner = prReviewExecRunner{}
	}
	number := strconv.Itoa(in.PRNumber)
	out, err := runner.RunGH(ctx, wd, "pr", "view", number, "--json", "state,isDraft,mergeable,reviewDecision,url,headRefName,headRefOid")
	if err != nil {
		return Result{Content: fmt.Sprintf("gh pr view failed: %v\n%s", err, out), IsError: true}, nil
	}
	var view mergePullRequestView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		return Result{Content: fmt.Sprintf("parse gh pr view output: %v", err), IsError: true}, nil
	}
	if view.State != "OPEN" {
		return Result{Content: fmt.Sprintf("pull request #%d is not open (state: %s)", in.PRNumber, view.State), IsError: true}, nil
	}
	if view.IsDraft {
		return Result{Content: fmt.Sprintf("pull request #%d is a draft", in.PRNumber), IsError: true}, nil
	}
	if view.ReviewDecision != "APPROVED" {
		return Result{Content: fmt.Sprintf("pull request #%d is not approved (review decision: %s)", in.PRNumber, view.ReviewDecision), IsError: true}, nil
	}
	if view.Mergeable == "CONFLICTING" {
		return Result{Content: fmt.Sprintf("pull request #%d has merge conflicts", in.PRNumber), IsError: true}, nil
	}
	if strings.TrimSpace(view.HeadRefOid) == "" {
		return Result{Content: fmt.Sprintf("pull request #%d has no head commit SHA", in.PRNumber), IsError: true}, nil
	}
	checksOut, err := runner.RunGH(ctx, wd, "api", "repos/{owner}/{repo}/commits/"+view.HeadRefOid+"/check-runs", "--paginate")
	if err != nil {
		return Result{Content: fmt.Sprintf("gh api check-runs failed: %v\n%s", err, checksOut), IsError: true}, nil
	}
	checks, err := parseCheckRunPages(checksOut)
	if err != nil {
		return Result{Content: fmt.Sprintf("parse check-runs output: %v", err), IsError: true}, nil
	}
	statusOut, err := runner.RunGH(ctx, wd, "api", "repos/{owner}/{repo}/commits/"+view.HeadRefOid+"/status")
	if err != nil {
		return Result{Content: fmt.Sprintf("gh api commit status failed: %v\n%s", err, statusOut), IsError: true}, nil
	}
	var combinedStatus struct {
		Statuses []pullRequestCommitStatus `json:"statuses"`
	}
	if err := json.Unmarshal([]byte(statusOut), &combinedStatus); err != nil {
		return Result{Content: fmt.Sprintf("parse commit status output: %v", err), IsError: true}, nil
	}
	checksSummary := maintainerPullRequestChecksSummary(checks, combinedStatus.Statuses)
	if checksSummary.Pending > 0 {
		return Result{Content: fmt.Sprintf("pull request #%d still has %d pending checks or commit statuses", in.PRNumber, checksSummary.Pending), IsError: true}, nil
	}
	if checksSummary.Failed > 0 {
		return Result{Content: fmt.Sprintf("pull request #%d has %d failing checks or commit statuses", in.PRNumber, checksSummary.Failed), IsError: true}, nil
	}
	out, err = runner.RunGH(ctx, wd, "pr", "merge", number, "--"+method, "--match-head-commit", view.HeadRefOid)
	if err != nil {
		return Result{Content: fmt.Sprintf("gh pr merge failed: %v\n%s", err, out), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("Merged pull request %s with %s after verifying %d checks and commit statuses. Verify the merge, mark its implementer succeeded if needed, close the linked issue as completed, then consider dispatching dependent work.", view.URL, method, checksSummary.Total)}, nil
}
