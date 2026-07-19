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
	out, err := runner.RunGH(ctx, wd, "pr", "view", number, "--json", "state,isDraft,mergeable,reviewDecision,url,headRefName")
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
	out, err = runner.RunGH(ctx, wd, "pr", "merge", number, "--"+method)
	if err != nil {
		return Result{Content: fmt.Sprintf("gh pr merge failed: %v\n%s", err, out), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("Merged pull request %s with %s. Consider dispatching the next issue.", view.URL, method)}, nil
}
