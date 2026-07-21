package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestMergePullRequestGateAndSafetyChecks(t *testing.T) {
	viewKey := "pr view 7 --json state,isDraft,mergeable,reviewDecision,url,headRefName,headRefOid"
	approved := `{"state":"OPEN","isDraft":false,"mergeable":"MERGEABLE","reviewDecision":"APPROVED","url":"https://example.test/pull/7","headRefName":"feature","headRefOid":"abc123"}`
	checksKey := "api repos/{owner}/{repo}/commits/abc123/check-runs --paginate"
	statusKey := "api repos/{owner}/{repo}/commits/abc123/status"
	for _, tc := range []struct {
		name      string
		enabled   bool
		view      string
		method    string
		wantError string
		mergeCall string
		checkRuns string
		statuses  string
	}{
		{name: "flag off", enabled: false, wantError: "not enabled"},
		{name: "draft", enabled: true, view: `{"state":"OPEN","isDraft":true,"mergeable":"MERGEABLE","reviewDecision":"APPROVED"}`, wantError: "is a draft"},
		{name: "not approved", enabled: true, view: `{"state":"OPEN","isDraft":false,"mergeable":"MERGEABLE","reviewDecision":"REVIEW_REQUIRED"}`, wantError: "not approved"},
		{name: "conflicting", enabled: true, view: `{"state":"OPEN","isDraft":false,"mergeable":"CONFLICTING","reviewDecision":"APPROVED"}`, wantError: "merge conflicts"},
		{name: "pending checks", enabled: true, view: approved, checkRuns: `{"check_runs":[{"name":"test","status":"in_progress"}]}`, statuses: `{"statuses":[]}`, wantError: "pending checks"},
		{name: "failing status", enabled: true, view: approved, checkRuns: `{"check_runs":[]}`, statuses: `{"statuses":[{"context":"ci","state":"failure"}]}`, wantError: "failing checks"},
		{name: "squash", enabled: true, view: approved, method: "squash", checkRuns: `{"check_runs":[{"name":"test","status":"completed","conclusion":"success"}]}`, statuses: `{"statuses":[]}`, mergeCall: "pr merge 7 --squash --match-head-commit abc123"},
		{name: "merge", enabled: true, view: approved, method: "merge", checkRuns: `{"check_runs":[]}`, statuses: `{"statuses":[{"context":"ci","state":"success"}]}`, mergeCall: "pr merge 7 --merge --match-head-commit abc123"},
		{name: "rebase", enabled: true, view: approved, method: "rebase", checkRuns: `{"check_runs":[]}`, statuses: `{"statuses":[]}`, mergeCall: "pr merge 7 --rebase --match-head-commit abc123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, k8sClient, _ := newMaintainerToolBase(t, maintainerRun())
			setMaintainerMergeEnabled(t, k8sClient, tc.enabled)
			runner := &maintainerFakeRunner{out: map[string]string{
				viewKey: tc.view, checksKey: tc.checkRuns, statusKey: tc.statuses,
			}}
			tool := &mergePullRequestTool{maintainerToolBase: base, runner: runner}
			input := map[string]any{"pr_number": 7}
			if tc.method != "" {
				input["merge_method"] = tc.method
			}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			result, err := tool.Execute(context.Background(), raw, maintainerTestGitRepoDir(t))
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" {
				if !result.IsError || !strings.Contains(result.Content, tc.wantError) {
					t.Fatalf("result = %#v, want error containing %q", result, tc.wantError)
				}
				if tc.name == "flag off" && len(runner.calls) != 0 {
					t.Fatalf("disabled merge called gh: %v", runner.calls)
				}
				return
			}
			if result.IsError || !strings.Contains(result.Content, "https://example.test/pull/7") {
				t.Fatalf("result = %#v", result)
			}
			if len(runner.calls) != 4 || runner.calls[1] != checksKey || runner.calls[2] != statusKey || runner.calls[3] != tc.mergeCall {
				t.Fatalf("gh calls = %v, want checks, statuses, then merge %q", runner.calls, tc.mergeCall)
			}
		})
	}
}

func setMaintainerMergeEnabled(t *testing.T, k8sClient client.Client, enabled bool) {
	t.Helper()
	repository := &triggersv1alpha1.GitHubRepository{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "repo", Namespace: "default"}, repository); err != nil {
		t.Fatal(err)
	}
	repository.Spec.Maintainer = &triggersv1alpha1.MaintainerSpec{AllowPullRequestMerge: enabled}
	if err := k8sClient.Update(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
}
