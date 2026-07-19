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
	viewKey := "pr view 7 --json state,isDraft,mergeable,reviewDecision,url,headRefName"
	approved := `{"state":"OPEN","isDraft":false,"mergeable":"MERGEABLE","reviewDecision":"APPROVED","url":"https://example.test/pull/7","headRefName":"feature"}`
	for _, tc := range []struct {
		name      string
		enabled   bool
		view      string
		method    string
		wantError string
		mergeCall string
	}{
		{name: "flag off", enabled: false, wantError: "not enabled"},
		{name: "draft", enabled: true, view: `{"state":"OPEN","isDraft":true,"mergeable":"MERGEABLE","reviewDecision":"APPROVED"}`, wantError: "is a draft"},
		{name: "not approved", enabled: true, view: `{"state":"OPEN","isDraft":false,"mergeable":"MERGEABLE","reviewDecision":"REVIEW_REQUIRED"}`, wantError: "not approved"},
		{name: "conflicting", enabled: true, view: `{"state":"OPEN","isDraft":false,"mergeable":"CONFLICTING","reviewDecision":"APPROVED"}`, wantError: "merge conflicts"},
		{name: "squash", enabled: true, view: approved, method: "squash", mergeCall: "pr merge 7 --squash"},
		{name: "merge", enabled: true, view: approved, method: "merge", mergeCall: "pr merge 7 --merge"},
		{name: "rebase", enabled: true, view: approved, method: "rebase", mergeCall: "pr merge 7 --rebase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, k8sClient, _ := newMaintainerToolBase(t, maintainerRun())
			setMaintainerMergeEnabled(t, k8sClient, tc.enabled)
			runner := &maintainerFakeRunner{out: map[string]string{viewKey: tc.view}}
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
			if len(runner.calls) != 2 || runner.calls[1] != tc.mergeCall {
				t.Fatalf("gh calls = %v, want merge %q", runner.calls, tc.mergeCall)
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
