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
	return "Dangerously merge one approved, non-draft pull request only after confirming mergeability, current-head CI success, and repository policy; then re-read GitHub to distinguish a completed merge from a queued merge request."
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
	linked, err := t.pullRequestBelongsToFleet(ctx, view.URL)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to verify pull request linkage: %v", err), IsError: true}, nil
	}
	if !linked {
		return Result{Content: fmt.Sprintf("pull request #%d is not attached to an authorized maintainer fleet run", in.PRNumber), IsError: true}, nil
	}
	if view.State != "OPEN" {
		return Result{Content: fmt.Sprintf("pull request #%d is not open (state: %s)", in.PRNumber, view.State), IsError: true}, nil
	}
	if view.IsDraft {
		return Result{Content: fmt.Sprintf("pull request #%d is a draft", in.PRNumber), IsError: true}, nil
	}
	if strings.TrimSpace(view.ReviewDecision) == "" {
		// GitHub computes reviewDecision only when the base branch requires
		// pull-request reviews. Without that protection an approving review
		// cannot be verified, so the gate fails closed with the reason.
		return Result{Content: fmt.Sprintf("pull request #%d has no computed review decision: the base branch does not require pull-request reviews, so approval cannot be verified. Enable required reviews via branch protection or a ruleset, or leave merging to humans.", in.PRNumber), IsError: true}, nil
	}
	if view.ReviewDecision != "APPROVED" {
		return Result{Content: fmt.Sprintf("pull request #%d is not approved (review decision: %s)", in.PRNumber, view.ReviewDecision), IsError: true}, nil
	}
	if view.Mergeable != "MERGEABLE" {
		return Result{Content: fmt.Sprintf("pull request #%d mergeability is not confirmed (state: %s)", in.PRNumber, view.Mergeable), IsError: true}, nil
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
	postOut, err := runner.RunGH(ctx, wd, "pr", "view", number, "--json", "state,mergedAt,url,headRefOid")
	if err != nil {
		return Result{Content: fmt.Sprintf("merge command completed, but post-merge verification failed: %v\n%s", err, postOut), IsError: true}, nil
	}
	var post struct {
		State      string `json:"state"`
		MergedAt   string `json:"mergedAt"`
		URL        string `json:"url"`
		HeadRefOid string `json:"headRefOid"`
	}
	if err := json.Unmarshal([]byte(postOut), &post); err != nil {
		return Result{Content: fmt.Sprintf("parse post-merge pull request output: %v", err), IsError: true}, nil
	}
	if post.State != "MERGED" || strings.TrimSpace(post.MergedAt) == "" {
		return Result{Content: fmt.Sprintf("Merge request for %s was accepted but GitHub has not reported it merged yet (state: %s). Keep the work item active and wait for a pull-request event before finalizing the run or issue.", view.URL, post.State)}, nil
	}
	if post.HeadRefOid != view.HeadRefOid {
		return Result{Content: fmt.Sprintf("pull request %s merged an unexpected head SHA (verified %s, merged %s); do not finalize automatically", view.URL, view.HeadRefOid, post.HeadRefOid), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("Verified pull request %s merged at %s with %s after checking %d checks and commit statuses. Finalize only after confirming the accepted issue scope is delivered.", post.URL, post.MergedAt, method, checksSummary.Total)}, nil
}

func (t *mergePullRequestTool) pullRequestBelongsToFleet(ctx context.Context, rawURL string) (bool, error) {
	owner, repository, number, err := parseMaintainerPullRequestURL(rawURL)
	if err != nil {
		return false, err
	}
	fleet, err := t.fleetRuns(ctx)
	if err != nil {
		return false, err
	}
	for i := range fleet {
		for _, candidate := range waitPullRequestURLs(&fleet[i]) {
			candidateOwner, candidateRepository, candidateNumber, err := parseMaintainerPullRequestURL(candidate)
			if err != nil {
				continue
			}
			if owner == candidateOwner && repository == candidateRepository && number == candidateNumber {
				return true, nil
			}
		}
	}
	return false, nil
}
