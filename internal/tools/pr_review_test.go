package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRegisterPRReviewTools(t *testing.T) {
	registry := &Registry{tools: map[string]Tool{}}
	RegisterPRReviewTools(registry, "/workspace")

	for _, name := range []string{
		"get_pull_request",
		"update_pull_request",
		"list_review_threads",
		"submit_pull_request_review",
		"reply_to_review_thread",
		"resolve_review_thread",
		"request_re_review",
		"get_pull_request_checks",
	} {
		if registry.Get(name) == nil {
			t.Fatalf("expected registered tool %q", name)
		}
	}
}

func TestGetPullRequestToolFetchesMetadataFilesAndDiff(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakePRReviewRunner{
		ghOut: map[string]string{
			"pr view 7 --json number,url,title,body,state,author,headRefName,baseRefName,mergeable,reviewDecision": `{"number":7,"url":"https://github.com/acme/repo/pull/7","title":"Fix","body":"Body","state":"OPEN","author":{"login":"octo"},"headRefName":"fix","baseRefName":"main","mergeable":"MERGEABLE","reviewDecision":"REVIEW_REQUIRED"}`,
			"api repos/{owner}/{repo}/pulls/7/files --paginate --slurp":                                            `[[{"filename":"main.go","status":"modified","additions":2,"deletions":1,"changes":3,"patch":"@@ -1 +1 @@"}]]`,
			"pr diff 7": "diff --git a/main.go b/main.go\n",
		},
	}
	tool := &getPullRequestTool{prReviewToolBase: prReviewToolBase{runner: runner}}

	result, err := tool.Execute(context.Background(), prReviewMustJSON(t, map[string]any{"pr_number": 7}), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"title":"Fix"`) || !strings.Contains(result.Content, `"filename":"main.go"`) || !strings.Contains(result.Content, `diff --git`) {
		t.Fatalf("result content = %s", result.Content)
	}
	wantCalls := []string{
		"pr view 7 --json number,url,title,body,state,author,headRefName,baseRefName,mergeable,reviewDecision",
		"api repos/{owner}/{repo}/pulls/7/files --paginate --slurp",
		"pr diff 7",
	}
	if !reflect.DeepEqual(runner.ghCalls, wantCalls) {
		t.Fatalf("gh calls = %#v, want %#v", runner.ghCalls, wantCalls)
	}
}

func TestGetPullRequestToolSurfacesGHFailure(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakePRReviewRunner{
		ghErr: map[string]error{
			"pr view 7 --json number,url,title,body,state,author,headRefName,baseRefName,mergeable,reviewDecision": errors.New("boom"),
		},
		ghOut: map[string]string{
			"pr view 7 --json number,url,title,body,state,author,headRefName,baseRefName,mergeable,reviewDecision": "stderr",
		},
	}
	tool := &getPullRequestTool{prReviewToolBase: prReviewToolBase{runner: runner}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"pr_number":7}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "gh pr view failed") || !strings.Contains(result.Content, "stderr") {
		t.Fatalf("result = %+v", result)
	}
}

func TestUpdatePullRequestToolBuildsRESTPayload(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakePRReviewRunner{
		ghInputOut: map[string]string{
			"api --method PATCH repos/{owner}/{repo}/pulls/7 --input -": `{"number":7,"html_url":"https://github.com/acme/repo/pull/7","title":"New title","body":"New body","state":"open"}`,
		},
	}
	tool := &updatePullRequestTool{prReviewToolBase: prReviewToolBase{runner: runner}}

	result, err := tool.Execute(context.Background(), prReviewMustJSON(t, map[string]any{"pr_number": 7, "title": "New title", "body": "New body"}), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	wantCalls := []string{"api --method PATCH repos/{owner}/{repo}/pulls/7 --input -"}
	if !reflect.DeepEqual(runner.ghInputCalls, wantCalls) {
		t.Fatalf("input calls = %#v, want %#v", runner.ghInputCalls, wantCalls)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(runner.ghInputs[0]), &payload); err != nil {
		t.Fatalf("payload json: %v", err)
	}
	if !reflect.DeepEqual(payload, map[string]string{"title": "New title", "body": "New body"}) {
		t.Fatalf("payload = %#v", payload)
	}
	if !strings.Contains(result.Content, `"url":"https://github.com/acme/repo/pull/7"`) || !strings.Contains(result.Content, `"title":"New title"`) {
		t.Fatalf("result content = %s", result.Content)
	}
}

func TestUpdatePullRequestToolSendsOnlyProvidedFields(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakePRReviewRunner{
		ghInputOut: map[string]string{
			"api --method PATCH repos/{owner}/{repo}/pulls/7 --input -": `{"number":7,"html_url":"https://github.com/acme/repo/pull/7","title":"Old","body":"","state":"open"}`,
		},
	}
	tool := &updatePullRequestTool{prReviewToolBase: prReviewToolBase{runner: runner}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"pr_number":7,"body":""}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(runner.ghInputs[0]), &payload); err != nil {
		t.Fatalf("payload json: %v", err)
	}
	if !reflect.DeepEqual(payload, map[string]string{"body": ""}) {
		t.Fatalf("payload = %#v, want body-only payload", payload)
	}
}

func TestUpdatePullRequestToolRejectsInvalidInput(t *testing.T) {
	repo := testGitRepoDir(t)
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "missing title and body", input: `{"pr_number":7}`, wantErr: "provide title and/or body"},
		{name: "blank title", input: `{"pr_number":7,"title":"  "}`, wantErr: "title cannot be blank"},
		{name: "missing pr number", input: `{"title":"New"}`, wantErr: "pr_number must be greater than zero"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakePRReviewRunner{}
			tool := &updatePullRequestTool{prReviewToolBase: prReviewToolBase{runner: runner}}
			result, err := tool.Execute(context.Background(), json.RawMessage(tt.input), repo)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if !result.IsError || !strings.Contains(result.Content, tt.wantErr) {
				t.Fatalf("result = %+v, want error containing %q", result, tt.wantErr)
			}
			if len(runner.ghInputCalls) != 0 {
				t.Fatalf("expected no gh calls, got %#v", runner.ghInputCalls)
			}
		})
	}
}

func TestListReviewThreadsToolParsesThreadsAndBuildsGraphQLPayload(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakePRReviewRunner{
		ghOut: map[string]string{
			"repo view --json owner,name": `{"name":"repo","owner":{"login":"acme"}}`,
		},
		ghInputOut: map[string]string{
			"api graphql --input -": `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"THREAD","isResolved":false,"isOutdated":true,"path":"main.go","line":12,"comments":{"nodes":[{"databaseId":99,"body":"Please fix","author":{"login":"reviewer"}}]}}]}}}}}`,
		},
	}
	tool := &listReviewThreadsTool{prReviewToolBase: prReviewToolBase{runner: runner}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"pr_number":7}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"id":"THREAD"`) || !strings.Contains(result.Content, `"databaseId":99`) {
		t.Fatalf("result content = %s", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(runner.ghInputs[0]), &payload); err != nil {
		t.Fatalf("payload json: %v", err)
	}
	variables := payload["variables"].(map[string]any)
	if variables["owner"] != "acme" || variables["repo"] != "repo" || variables["number"].(float64) != 7 {
		t.Fatalf("variables = %#v", variables)
	}
	if !strings.Contains(payload["query"].(string), "reviewThreads") {
		t.Fatalf("query = %s", payload["query"])
	}
}

func TestSubmitPullRequestReviewToolBuildsRESTPayload(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakePRReviewRunner{
		ghInputOut: map[string]string{
			"api --method POST repos/{owner}/{repo}/pulls/7/reviews --input -": `{"id":123,"state":"APPROVED"}`,
		},
	}
	tool := &submitPullRequestReviewTool{prReviewToolBase: prReviewToolBase{runner: runner}}
	input := prReviewMustJSON(t, map[string]any{
		"pr_number": 7,
		"event":     "APPROVE",
		"body":      "Looks good",
		"comments": []map[string]any{{
			"path": "main.go",
			"line": 12,
			"side": "RIGHT",
			"body": "Nice",
		}},
	})

	result, err := tool.Execute(context.Background(), input, repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	wantCalls := []string{"api --method POST repos/{owner}/{repo}/pulls/7/reviews --input -"}
	if !reflect.DeepEqual(runner.ghInputCalls, wantCalls) {
		t.Fatalf("input calls = %#v, want %#v", runner.ghInputCalls, wantCalls)
	}
	var payload struct {
		Event    string               `json:"event"`
		Body     string               `json:"body"`
		Comments []pullRequestComment `json:"comments"`
	}
	if err := json.Unmarshal([]byte(runner.ghInputs[0]), &payload); err != nil {
		t.Fatalf("payload json: %v", err)
	}
	if payload.Event != "APPROVE" || payload.Body != "Looks good\n\n"+githubAppAuthorizationFooter || payload.Comments[0].Path != "main.go" || payload.Comments[0].Body != "Nice\n\n"+githubAppAuthorizationFooter {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestSubmitPullRequestReviewToolRejectsInvalidEvent(t *testing.T) {
	repo := testGitRepoDir(t)
	tool := &submitPullRequestReviewTool{prReviewToolBase: prReviewToolBase{runner: &fakePRReviewRunner{}}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"pr_number":7,"event":"MERGE","body":"no"}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "event must be") {
		t.Fatalf("result = %+v", result)
	}
}

func TestReplyToReviewThreadToolBuildsGraphQLPayload(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakePRReviewRunner{
		ghInputOut: map[string]string{
			"api graphql --input -": `{"data":{"addPullRequestReviewThreadReply":{"comment":{"databaseId":5,"body":"Fixed","author":{"login":"agent"}}}}}`,
		},
	}
	tool := &replyToReviewThreadTool{prReviewToolBase: prReviewToolBase{runner: runner}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"thread_id":"THREAD","body":"Fixed"}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	assertGraphQLPayload(t, runner.ghInputs[0], "addPullRequestReviewThreadReply", map[string]any{"threadID": "THREAD", "body": "Fixed\n\n" + githubAppAuthorizationFooter})
}

func TestResolveReviewThreadToolBuildsResolveAndUnresolvePayloads(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakePRReviewRunner{
		ghInputOut: map[string]string{
			"api graphql --input -": `{"data":{"resolveReviewThread":{"thread":{"id":"THREAD","isResolved":true}}}}`,
		},
	}
	tool := &resolveReviewThreadTool{prReviewToolBase: prReviewToolBase{runner: runner}}

	if result, err := tool.Execute(context.Background(), json.RawMessage(`{"thread_id":"THREAD"}`), repo); err != nil || result.IsError {
		t.Fatalf("resolve result = %+v, err = %v", result, err)
	}
	if result, err := tool.Execute(context.Background(), json.RawMessage(`{"thread_id":"THREAD","unresolve":true}`), repo); err != nil || result.IsError {
		t.Fatalf("unresolve result = %+v, err = %v", result, err)
	}
	assertGraphQLPayload(t, runner.ghInputs[0], "resolveReviewThread", map[string]any{"threadID": "THREAD"})
	assertGraphQLPayload(t, runner.ghInputs[1], "unresolveReviewThread", map[string]any{"threadID": "THREAD"})
}

func TestRequestReReviewToolBuildsRESTPayload(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakePRReviewRunner{
		ghInputOut: map[string]string{
			"api --method POST repos/{owner}/{repo}/pulls/7/requested_reviewers --input -": `{"users":[{"login":"reviewer"}]}`,
		},
	}
	tool := &requestReReviewTool{prReviewToolBase: prReviewToolBase{runner: runner}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"pr_number":7,"reviewer":"reviewer"}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	var payload map[string][]string
	if err := json.Unmarshal([]byte(runner.ghInputs[0]), &payload); err != nil {
		t.Fatalf("payload json: %v", err)
	}
	if !reflect.DeepEqual(payload["reviewers"], []string{"reviewer"}) {
		t.Fatalf("payload = %#v", payload)
	}
}

type fakePRReviewRunner struct {
	ghOut        map[string]string
	ghInputOut   map[string]string
	ghErr        map[string]error
	ghInputErr   map[string]error
	ghCalls      []string
	ghInputCalls []string
	ghInputs     []string
	dirs         []string
}

func (r *fakePRReviewRunner) RunGH(_ context.Context, workDir string, args ...string) (string, error) {
	key := strings.Join(args, " ")
	r.ghCalls = append(r.ghCalls, key)
	r.dirs = append(r.dirs, workDir)
	return r.ghOut[key], r.ghErr[key]
}

func (r *fakePRReviewRunner) RunGHWithInput(_ context.Context, workDir string, input string, args ...string) (string, error) {
	key := strings.Join(args, " ")
	r.ghInputCalls = append(r.ghInputCalls, key)
	r.ghInputs = append(r.ghInputs, input)
	r.dirs = append(r.dirs, workDir)
	return r.ghInputOut[key], r.ghInputErr[key]
}

func prReviewMustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertGraphQLPayload(t *testing.T, raw, wantQuerySubstring string, wantVariables map[string]any) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("payload json: %v", err)
	}
	if !strings.Contains(payload["query"].(string), wantQuerySubstring) {
		t.Fatalf("query = %s, want substring %q", payload["query"], wantQuerySubstring)
	}
	variables := payload["variables"].(map[string]any)
	for key, want := range wantVariables {
		got := variables[key]
		if got != want {
			t.Fatalf("variables[%q] = %#v, want %#v; all variables %#v", key, got, want, variables)
		}
	}
}

func testWorkspaceWithAttachedRepo(t *testing.T) (workspace, attached string) {
	t.Helper()
	workspace = testGitRepoDir(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos", "lib", ".git"), 0o755); err != nil {
		t.Fatalf("mkdir attached repo: %v", err)
	}
	resolved, err := resolveLocalGitRepositoryWorkDir(workspace, "repos/lib")
	if err != nil {
		t.Fatalf("resolve attached repo: %v", err)
	}
	return workspace, resolved
}

func TestPRReviewToolsHonorRepoPath(t *testing.T) {
	tests := []struct {
		name string
		tool func(base prReviewToolBase) interface {
			Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error)
		}
		input      map[string]any
		ghOut      map[string]string
		ghInputOut map[string]string
	}{
		{
			name: "get_pull_request",
			tool: func(base prReviewToolBase) interface {
				Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error)
			} {
				return &getPullRequestTool{prReviewToolBase: base}
			},
			input: map[string]any{"pr_number": 7, "repo_path": "repos/lib"},
			ghOut: map[string]string{
				"pr view 7 --json number,url,title,body,state,author,headRefName,baseRefName,mergeable,reviewDecision": `{"number":7,"title":"Fix"}`,
				"api repos/{owner}/{repo}/pulls/7/files --paginate --slurp":                                            `[]`,
				"pr diff 7": "diff",
			},
		},
		{
			name: "update_pull_request",
			tool: func(base prReviewToolBase) interface {
				Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error)
			} {
				return &updatePullRequestTool{prReviewToolBase: base}
			},
			input: map[string]any{"pr_number": 7, "title": "New title", "repo_path": "repos/lib"},
			ghInputOut: map[string]string{
				"api --method PATCH repos/{owner}/{repo}/pulls/7 --input -": `{"number":7}`,
			},
		},
		{
			name: "list_review_threads",
			tool: func(base prReviewToolBase) interface {
				Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error)
			} {
				return &listReviewThreadsTool{prReviewToolBase: base}
			},
			input: map[string]any{"pr_number": 7, "repo_path": "repos/lib"},
			ghOut: map[string]string{
				"repo view --json owner,name": `{"name":"repo","owner":{"login":"acme"}}`,
			},
			ghInputOut: map[string]string{
				"api graphql --input -": `{"data":{}}`,
			},
		},
		{
			name: "submit_pull_request_review",
			tool: func(base prReviewToolBase) interface {
				Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error)
			} {
				return &submitPullRequestReviewTool{prReviewToolBase: base}
			},
			input: map[string]any{"pr_number": 7, "event": "APPROVE", "body": "LGTM", "repo_path": "repos/lib"},
			ghInputOut: map[string]string{
				"api --method POST repos/{owner}/{repo}/pulls/7/reviews --input -": `{"id":1}`,
			},
		},
		{
			name: "reply_to_review_thread",
			tool: func(base prReviewToolBase) interface {
				Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error)
			} {
				return &replyToReviewThreadTool{prReviewToolBase: base}
			},
			input: map[string]any{"thread_id": "THREAD", "body": "Fixed", "repo_path": "repos/lib"},
			ghInputOut: map[string]string{
				"api graphql --input -": `{"data":{}}`,
			},
		},
		{
			name: "resolve_review_thread",
			tool: func(base prReviewToolBase) interface {
				Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error)
			} {
				return &resolveReviewThreadTool{prReviewToolBase: base}
			},
			input: map[string]any{"thread_id": "THREAD", "repo_path": "repos/lib"},
			ghInputOut: map[string]string{
				"api graphql --input -": `{"data":{}}`,
			},
		},
		{
			name: "request_re_review",
			tool: func(base prReviewToolBase) interface {
				Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error)
			} {
				return &requestReReviewTool{prReviewToolBase: base}
			},
			input: map[string]any{"pr_number": 7, "reviewer": "octo", "repo_path": "repos/lib"},
			ghInputOut: map[string]string{
				"api --method POST repos/{owner}/{repo}/pulls/7/requested_reviewers --input -": `{"users":[]}`,
			},
		},
		{
			name: "get_pull_request_checks",
			tool: func(base prReviewToolBase) interface {
				Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error)
			} {
				return &getPullRequestChecksTool{prReviewToolBase: base}
			},
			input: map[string]any{"pr_number": 7, "repo_path": "repos/lib"},
			ghOut: map[string]string{
				"pr view 7 --json headRefOid":                                   `{"headRefOid":"abc123"}`,
				"api repos/{owner}/{repo}/commits/abc123/check-runs --paginate": `{"check_runs":[]}`,
				"api repos/{owner}/{repo}/commits/abc123/status":                `{"statuses":[]}`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace, attached := testWorkspaceWithAttachedRepo(t)
			runner := &fakePRReviewRunner{ghOut: tt.ghOut, ghInputOut: tt.ghInputOut}
			tool := tt.tool(prReviewToolBase{runner: runner, workDir: workspace})

			result, err := tool.Execute(context.Background(), prReviewMustJSON(t, tt.input), "/ignored")
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if result.IsError {
				t.Fatalf("Execute() returned error result: %s", result.Content)
			}
			if len(runner.dirs) == 0 {
				t.Fatal("expected gh commands to run")
			}
			for i, dir := range runner.dirs {
				if dir != attached {
					t.Fatalf("gh call %d ran in %q, want %q", i, dir, attached)
				}
			}
		})
	}
}

func TestPRReviewToolsRejectRepoPathEscapingWorkspace(t *testing.T) {
	workspace, _ := testWorkspaceWithAttachedRepo(t)
	runner := &fakePRReviewRunner{}
	tool := &getPullRequestTool{prReviewToolBase: prReviewToolBase{runner: runner, workDir: workspace}}

	result, err := tool.Execute(context.Background(), prReviewMustJSON(t, map[string]any{"pr_number": 7, "repo_path": "../outside"}), "/ignored")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "repo_path rejected") {
		t.Fatalf("result = %+v", result)
	}
	if len(runner.dirs) != 0 {
		t.Fatalf("expected no gh calls, got %#v", runner.dirs)
	}
}

func TestGetPullRequestChecksToolSummarizesChecksAndStatuses(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakePRReviewRunner{
		ghOut: map[string]string{
			"pr view 7 --json headRefOid": `{"headRefOid":"abc123"}`,
			"api repos/{owner}/{repo}/commits/abc123/check-runs --paginate": `{"total_count":3,"check_runs":[{"name":"build","status":"completed","conclusion":"success","details_url":"https://ci/1","started_at":"2024-01-01T00:00:00Z","completed_at":"2024-01-01T00:05:00Z"},{"name":"test","status":"completed","conclusion":"failure","details_url":"https://ci/2","started_at":"2024-01-01T00:00:00Z","completed_at":"2024-01-01T00:06:00Z"}]}
{"total_count":3,"check_runs":[{"name":"lint","status":"in_progress","conclusion":null,"details_url":"https://ci/3","started_at":"2024-01-01T00:00:00Z","completed_at":null}]}`,
			"api repos/{owner}/{repo}/commits/abc123/status": `{"state":"pending","statuses":[{"context":"legacy/ci","state":"success","target_url":"https://legacy/1","description":"ok"},{"context":"legacy/deploy","state":"pending","target_url":"https://legacy/2","description":"waiting"}]}`,
		},
	}
	tool := &getPullRequestChecksTool{prReviewToolBase: prReviewToolBase{runner: runner}}

	result, err := tool.Execute(context.Background(), prReviewMustJSON(t, map[string]any{"pr_number": 7}), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}

	var out struct {
		HeadSHA  string                    `json:"head_sha"`
		Checks   []pullRequestCheckRun     `json:"checks"`
		Statuses []pullRequestCommitStatus `json:"statuses"`
		Summary  pullRequestChecksSummary  `json:"summary"`
	}
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("result json: %v", err)
	}
	if out.HeadSHA != "abc123" {
		t.Fatalf("head_sha = %q", out.HeadSHA)
	}
	if len(out.Checks) != 3 || out.Checks[0].Name != "build" || out.Checks[2].Name != "lint" {
		t.Fatalf("checks = %#v", out.Checks)
	}
	if len(out.Statuses) != 2 || out.Statuses[0].Context != "legacy/ci" {
		t.Fatalf("statuses = %#v", out.Statuses)
	}
	want := pullRequestChecksSummary{Total: 5, Passed: 2, Failed: 1, Pending: 2}
	if out.Summary != want {
		t.Fatalf("summary = %#v, want %#v", out.Summary, want)
	}
	wantCalls := []string{
		"pr view 7 --json headRefOid",
		"api repos/{owner}/{repo}/commits/abc123/check-runs --paginate",
		"api repos/{owner}/{repo}/commits/abc123/status",
	}
	if !reflect.DeepEqual(runner.ghCalls, wantCalls) {
		t.Fatalf("gh calls = %#v, want %#v", runner.ghCalls, wantCalls)
	}
}
