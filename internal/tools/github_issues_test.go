package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	issueViewCall     = "issue view 7 --json number,url,title,body,state,stateReason,author,assignees,labels,milestone,createdAt,updatedAt,closedAt"
	issueCommentsCall = "api repos/{owner}/{repo}/issues/7/comments --paginate --slurp"
	issueGuardCall    = "api repos/{owner}/{repo}/issues/7"
)

func TestRegisterGitHubIssueManagementTools(t *testing.T) {
	registry := &Registry{tools: map[string]Tool{}}
	RegisterGitHubIssueManagementTools(registry, "/workspace")
	for _, name := range []string{
		"get_github_issue",
		"update_github_issue",
		"close_github_issue",
		"update_github_issue_labels",
		"add_github_issue_comment",
	} {
		if registry.Get(name) == nil {
			t.Fatalf("expected registered tool %q", name)
		}
	}
	if !registry.Get("get_github_issue").IsReadOnly() {
		t.Fatal("get_github_issue should be read-only")
	}
}

func TestGetGitHubIssueToolReturnsStructuredIssue(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakePRReviewRunner{ghOut: map[string]string{
		issueViewCall:     `{"number":7,"url":"https://github.com/acme/repo/issues/7","title":"Bug","state":"OPEN","labels":[{"name":"bug"}]}`,
		issueCommentsCall: `[[{"body":"first"}],[{"body":"latest"}]]`,
	}}
	tool := &getGitHubIssueTool{githubIssueToolBase: githubIssueToolBase{runner: runner}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"issue_number":7}`), repo)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content, `"title":"Bug"`) || !strings.Contains(result.Content, `"body":"latest"`) {
		t.Fatalf("result = %+v", result)
	}
	if !reflect.DeepEqual(runner.ghCalls, []string{issueViewCall, issueCommentsCall}) {
		t.Fatalf("gh calls = %#v", runner.ghCalls)
	}
}

func TestUpdateGitHubIssueToolUsesOnlyProvidedFields(t *testing.T) {
	repo := testGitRepoDir(t)
	call := "api --method PATCH repos/{owner}/{repo}/issues/7 --input -"
	runner := &fakePRReviewRunner{
		ghOut: map[string]string{issueGuardCall: `{"number":7}`},
		ghInputOut: map[string]string{
			call: `{"number":7,"html_url":"https://github.com/acme/repo/issues/7","title":"Bug","body":"","state":"open"}`,
		},
	}
	tool := &updateGitHubIssueTool{githubIssueToolBase: githubIssueToolBase{runner: runner}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"issue_number":7,"body":""}`), repo)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("result = %+v", result)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(runner.ghInputs[0]), &payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload, map[string]string{"body": ""}) {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestCloseGitHubIssueToolSetsStateAndReason(t *testing.T) {
	repo := testGitRepoDir(t)
	call := "api --method PATCH repos/{owner}/{repo}/issues/7 --input -"
	runner := &fakePRReviewRunner{
		ghOut:      map[string]string{issueGuardCall: `{"number":7}`},
		ghInputOut: map[string]string{call: `{"number":7,"state":"closed","state_reason":"not_planned"}`},
	}
	tool := &closeGitHubIssueTool{githubIssueToolBase: githubIssueToolBase{runner: runner}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"issue_number":7,"reason":"not_planned"}`), repo)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("result = %+v", result)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(runner.ghInputs[0]), &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"state": "closed", "state_reason": "not_planned"}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("payload = %#v, want %#v", payload, want)
	}
}

func TestUpdateGitHubIssueLabelsToolAddsRemovesAndReturnsIssue(t *testing.T) {
	repo := testGitRepoDir(t)
	editCall := "issue edit 7 --add-label bug --add-label sdk --remove-label triage"
	runner := &fakePRReviewRunner{ghOut: map[string]string{
		issueGuardCall:    `{"number":7}`,
		editCall:          "https://github.com/acme/repo/issues/7\n",
		issueViewCall:     `{"number":7,"labels":[{"name":"bug"},{"name":"sdk"}]}`,
		issueCommentsCall: `[]`,
	}}
	tool := &updateGitHubIssueLabelsTool{githubIssueToolBase: githubIssueToolBase{runner: runner}}
	input := prReviewMustJSON(t, map[string]any{
		"issue_number":  7,
		"add_labels":    []string{"bug", "sdk", "bug", " "},
		"remove_labels": []string{"triage"},
	})

	result, err := tool.Execute(context.Background(), input, repo)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content, `"labels"`) {
		t.Fatalf("result = %+v", result)
	}
	want := []string{issueGuardCall, editCall, issueViewCall, issueCommentsCall}
	if !reflect.DeepEqual(runner.ghCalls, want) {
		t.Fatalf("gh calls = %#v, want %#v", runner.ghCalls, want)
	}
}

func TestAddGitHubIssueCommentToolUsesRESTPayload(t *testing.T) {
	repo := testGitRepoDir(t)
	call := "api --method POST repos/{owner}/{repo}/issues/7/comments --input -"
	runner := &fakePRReviewRunner{
		ghOut: map[string]string{issueGuardCall: `{"number":7}`},
		ghInputOut: map[string]string{
			call: `{"id":44,"html_url":"https://github.com/acme/repo/issues/7#issuecomment-44","body":"Fixed"}`,
		},
	}
	tool := &addGitHubIssueCommentTool{githubIssueToolBase: githubIssueToolBase{runner: runner}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"issue_number":7,"body":"Fixed"}`), repo)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content, `issuecomment-44`) {
		t.Fatalf("result = %+v", result)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(runner.ghInputs[0]), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["body"] != "Fixed\n\n"+githubAppAuthorizationFooter {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestGitHubIssueMutationsRejectPullRequestNumbers(t *testing.T) {
	repo := testGitRepoDir(t)
	tests := []struct {
		name  string
		tool  Tool
		input string
	}{
		{"update", &updateGitHubIssueTool{}, `{"issue_number":7,"title":"Changed"}`},
		{"close", &closeGitHubIssueTool{}, `{"issue_number":7}`},
		{"labels", &updateGitHubIssueLabelsTool{}, `{"issue_number":7,"add_labels":["bug"]}`},
		{"comment", &addGitHubIssueCommentTool{}, `{"issue_number":7,"body":"note"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakePRReviewRunner{ghOut: map[string]string{
				issueGuardCall: `{"number":7,"pull_request":{"url":"https://api.github.com/repos/acme/repo/pulls/7"}}`,
			}}
			switch tool := tt.tool.(type) {
			case *updateGitHubIssueTool:
				tool.githubIssueToolBase.runner = runner
			case *closeGitHubIssueTool:
				tool.githubIssueToolBase.runner = runner
			case *updateGitHubIssueLabelsTool:
				tool.githubIssueToolBase.runner = runner
			case *addGitHubIssueCommentTool:
				tool.githubIssueToolBase.runner = runner
			}
			result, err := tt.tool.Execute(context.Background(), json.RawMessage(tt.input), repo)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || !strings.Contains(result.Content, "identifies a pull request") {
				t.Fatalf("result = %+v", result)
			}
			if len(runner.ghInputCalls) != 0 {
				t.Fatalf("mutation executed: %#v", runner.ghInputCalls)
			}
			if !reflect.DeepEqual(runner.ghCalls, []string{issueGuardCall}) {
				t.Fatalf("gh calls = %#v", runner.ghCalls)
			}
		})
	}
}

func TestGitHubIssueToolsValidateInputs(t *testing.T) {
	repo := testGitRepoDir(t)
	tests := []struct {
		name  string
		tool  Tool
		input string
		want  string
	}{
		{"get number", &getGitHubIssueTool{}, `{}`, "issue_number must be greater than zero"},
		{"update fields", &updateGitHubIssueTool{}, `{"issue_number":7}`, "provide title and/or body"},
		{"blank title", &updateGitHubIssueTool{}, `{"issue_number":7,"title":" "}`, "title cannot be blank"},
		{"close reason", &closeGitHubIssueTool{}, `{"issue_number":7,"reason":"duplicate"}`, "reason must be"},
		{"labels operations", &updateGitHubIssueLabelsTool{}, `{"issue_number":7}`, "provide at least one"},
		{"blank comment", &addGitHubIssueCommentTool{}, `{"issue_number":7,"body":" "}`, "body must not be blank"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.tool.Execute(context.Background(), json.RawMessage(tt.input), repo)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || !strings.Contains(result.Content, tt.want) {
				t.Fatalf("result = %+v, want %q", result, tt.want)
			}
		})
	}
}

func TestGetGitHubIssueToolUsesRepoPathAndSurfacesFailures(t *testing.T) {
	workspace, attached := testWorkspaceWithAttachedRepo(t)
	runner := &fakePRReviewRunner{
		ghOut: map[string]string{issueViewCall: "Not Found"},
		ghErr: map[string]error{issueViewCall: errors.New("exit status 1")},
	}
	tool := &getGitHubIssueTool{githubIssueToolBase: githubIssueToolBase{runner: runner}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"issue_number":7,"repo_path":"repos/lib"}`), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Content, "pass repo_path") {
		t.Fatalf("result = %+v", result)
	}
	if len(runner.dirs) == 0 || runner.dirs[0] != attached {
		t.Fatalf("dirs = %#v, want first dir %q", runner.dirs, attached)
	}
}
