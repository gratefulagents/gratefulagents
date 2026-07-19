package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

const prReviewCommandTimeout = 60 * time.Second

type prReviewRunner interface {
	RunGH(ctx context.Context, workDir string, args ...string) (string, error)
	RunGHWithInput(ctx context.Context, workDir, input string, args ...string) (string, error)
}

type prReviewExecRunner struct{}

func (prReviewExecRunner) RunGH(ctx context.Context, workDir string, args ...string) (string, error) {
	return runGHCommand(ctx, workDir, "", false, args...)
}

func (prReviewExecRunner) RunGHWithInput(ctx context.Context, workDir, input string, args ...string) (string, error) {
	return runGHCommand(ctx, workDir, input, true, args...)
}

func runGHCommand(ctx context.Context, workDir, input string, withInput bool, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, prReviewCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "gh", args...)
	cmd.Dir = workDir
	if withInput {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String() + stderr.String(), err
	}
	return stdout.String(), nil
}

// ReviewerMutatingToolNames lists the GitHub review tools a read-only-clamped
// reviewer run still needs: its only legitimate outputs are review comments,
// thread replies/resolutions, and the recorded verdict.
func ReviewerMutatingToolNames() []string {
	return []string{
		"submit_pull_request_review",
		"reply_to_review_thread",
		"resolve_review_thread",
		"request_re_review",
		"submit_review_verdict",
	}
}

// RegisterPRReviewTools registers GitHub pull request review tools for agents.
func RegisterPRReviewTools(registry *Registry, workDir string) {
	if registry == nil {
		return
	}
	runner := prReviewExecRunner{}
	base := prReviewToolBase{runner: runner, workDir: workDir}
	registry.Register(&getPullRequestTool{prReviewToolBase: base})
	registry.Register(&updatePullRequestTool{prReviewToolBase: base})
	registry.Register(&listReviewThreadsTool{prReviewToolBase: base})
	registry.Register(&submitPullRequestReviewTool{prReviewToolBase: base})
	registry.Register(&replyToReviewThreadTool{prReviewToolBase: base})
	registry.Register(&resolveReviewThreadTool{prReviewToolBase: base})
	registry.Register(&requestReReviewTool{prReviewToolBase: base})
	registry.Register(&getPullRequestChecksTool{prReviewToolBase: base})
}

const repoPathSchemaDescription = "Workspace-relative path to the git repository the PR belongs to, for example repos/<alias>. Defaults to the workspace root repository."

func (b prReviewToolBase) resolveWorkDir(workDir, repoPath string) (string, error) {
	return resolveLocalGitRepositoryWorkDir(b.effectiveWorkDir(workDir), repoPath)
}

type prReviewToolBase struct {
	runner  prReviewRunner
	workDir string
}

func (b prReviewToolBase) effectiveRunner() prReviewRunner {
	if b.runner != nil {
		return b.runner
	}
	return prReviewExecRunner{}
}

func (b prReviewToolBase) effectiveWorkDir(workDir string) string {
	if b.workDir != "" {
		return b.workDir
	}
	return workDir
}

type repoViewOutput struct {
	Name  string `json:"name"`
	Owner struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	} `json:"owner"`
}

func currentRepo(ctx context.Context, runner prReviewRunner, workDir string) (owner, repo string, err error) {
	out, err := runner.RunGH(ctx, workDir, "repo", "view", "--json", "owner,name")
	if err != nil {
		return "", "", fmt.Errorf("gh repo view failed: %w\n%s", err, out)
	}
	var parsed repoViewOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return "", "", fmt.Errorf("parse gh repo view output: %w", err)
	}
	owner = parsed.Owner.Login
	if owner == "" {
		owner = parsed.Owner.Name
	}
	if owner == "" || parsed.Name == "" {
		return "", "", fmt.Errorf("gh repo view returned missing owner or repo: %s", strings.TrimSpace(out))
	}
	return owner, parsed.Name, nil
}

func prReviewSuccess(value any) (Result, error) {
	b, _ := json.Marshal(value)
	return Result{Content: string(b)}, nil
}

func prReviewError(msg string) (Result, error) {
	// gh reports a wrong-repository PR number as an opaque GraphQL error;
	// agents in multi-repo workspaces hit this when repo_path is omitted.
	if strings.Contains(msg, "Could not resolve to a PullRequest") {
		msg += "\nhint: the PR number was not found in the selected repository — if it belongs to another workspace repository, pass repo_path (workspace root by default, attached repositories under repos/<alias>)."
	}
	b, _ := json.Marshal(map[string]string{"status": "error", "error": msg})
	return Result{Content: string(b), IsError: true}, nil
}

func requirePRNumber(number int) error {
	if number <= 0 {
		return fmt.Errorf("pr_number must be greater than zero")
	}
	return nil
}

type getPullRequestTool struct {
	prReviewToolBase
}

func (t *getPullRequestTool) Name() string { return "get_pull_request" }
func (t *getPullRequestTool) Description() string {
	return "Fetch PR metadata, changed files, and unified diff for a PR in the repository at repo_path (defaults to the workspace root repository). Use before reviewing a PR or addressing feedback on your own PR."
}
func (t *getPullRequestTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pr_number": {"type": "integer", "description": "Pull request number in the repository at repo_path."},
			"repo_path": {"type": "string", "description": "` + repoPathSchemaDescription + `"}
		},
		"required": ["pr_number"]
	}`)
}
func (t *getPullRequestTool) IsReadOnly() bool                      { return true }
func (t *getPullRequestTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *getPullRequestTool) NeedsApproval() bool                   { return false }
func (t *getPullRequestTool) TimeoutSeconds() int                   { return 0 }

type getPullRequestInput struct {
	PRNumber int    `json:"pr_number"`
	RepoPath string `json:"repo_path"`
}

type pullRequestMetadata struct {
	Number         int             `json:"number"`
	URL            string          `json:"url"`
	Title          string          `json:"title"`
	Body           string          `json:"body"`
	State          string          `json:"state"`
	Author         json.RawMessage `json:"author"`
	HeadRefName    string          `json:"head_ref_name"`
	BaseRefName    string          `json:"base_ref_name"`
	Mergeable      string          `json:"mergeable"`
	ReviewDecision string          `json:"review_decision"`
}

type pullRequestFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Changes   int    `json:"changes"`
	Patch     string `json:"patch,omitempty"`
}

func (t *getPullRequestTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in getPullRequestInput
	if err := json.Unmarshal(input, &in); err != nil {
		return prReviewError("Invalid input: " + err.Error())
	}
	if err := requirePRNumber(in.PRNumber); err != nil {
		return prReviewError(err.Error())
	}

	runner := t.effectiveRunner()
	wd, err := t.resolveWorkDir(workDir, in.RepoPath)
	if err != nil {
		return prReviewError("repo_path rejected: " + err.Error())
	}
	number := strconv.Itoa(in.PRNumber)
	viewOut, err := runner.RunGH(ctx, wd, "pr", "view", number, "--json", "number,url,title,body,state,author,headRefName,baseRefName,mergeable,reviewDecision")
	if err != nil {
		return prReviewError(fmt.Sprintf("gh pr view failed: %s\n%s", err, viewOut))
	}
	var metadata pullRequestMetadata
	if err := json.Unmarshal([]byte(viewOut), &metadata); err != nil {
		return prReviewError("parse gh pr view output: " + err.Error())
	}

	filesOut, err := runner.RunGH(ctx, wd, "api", "repos/{owner}/{repo}/pulls/"+number+"/files", "--paginate", "--slurp")
	if err != nil {
		return prReviewError(fmt.Sprintf("gh api pull files failed: %s\n%s", err, filesOut))
	}
	files, err := parsePullRequestFiles(filesOut)
	if err != nil {
		return prReviewError("parse pull request files output: " + err.Error())
	}

	diffOut, err := runner.RunGH(ctx, wd, "pr", "diff", number)
	if err != nil {
		return prReviewError(fmt.Sprintf("gh pr diff failed: %s\n%s", err, diffOut))
	}

	return prReviewSuccess(map[string]any{
		"pull_request": metadata,
		"files":        files,
		"diff":         diffOut,
	})
}

func parsePullRequestFiles(out string) ([]pullRequestFile, error) {
	var pages [][]pullRequestFile
	if err := json.Unmarshal([]byte(out), &pages); err == nil {
		var files []pullRequestFile
		for _, page := range pages {
			files = append(files, page...)
		}
		return files, nil
	}
	var files []pullRequestFile
	if err := json.Unmarshal([]byte(out), &files); err != nil {
		return nil, err
	}
	return files, nil
}

type updatePullRequestTool struct {
	prReviewToolBase
}

func (t *updatePullRequestTool) Name() string { return "update_pull_request" }
func (t *updatePullRequestTool) Description() string {
	return "Update the title and/or description (body) of an existing PR in the repository at repo_path (defaults to the workspace root repository). Use to fix or expand your own PR's title/description after the work evolves. Only the provided fields change."
}
func (t *updatePullRequestTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pr_number": {"type": "integer", "description": "Pull request number in the repository at repo_path."},
			"title": {"type": "string", "description": "New PR title. Omit to keep the current title."},
			"body": {"type": "string", "description": "New PR description in markdown. Replaces the entire existing description; an empty string clears it. Omit to keep the current description."},
			"repo_path": {"type": "string", "description": "` + repoPathSchemaDescription + `"}
		},
		"required": ["pr_number"]
	}`)
}
func (t *updatePullRequestTool) IsReadOnly() bool                      { return false }
func (t *updatePullRequestTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *updatePullRequestTool) NeedsApproval() bool                   { return false }
func (t *updatePullRequestTool) TimeoutSeconds() int                   { return 0 }

type updatePullRequestInput struct {
	PRNumber int     `json:"pr_number"`
	Title    *string `json:"title"`
	Body     *string `json:"body"`
	RepoPath string  `json:"repo_path"`
}

func (t *updatePullRequestTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in updatePullRequestInput
	if err := json.Unmarshal(input, &in); err != nil {
		return prReviewError("Invalid input: " + err.Error())
	}
	if err := requirePRNumber(in.PRNumber); err != nil {
		return prReviewError(err.Error())
	}
	if in.Title == nil && in.Body == nil {
		return prReviewError("provide title and/or body to update")
	}
	if in.Title != nil && strings.TrimSpace(*in.Title) == "" {
		return prReviewError("title cannot be blank; omit it to keep the current title")
	}

	fields := map[string]string{}
	if in.Title != nil {
		fields["title"] = *in.Title
	}
	if in.Body != nil {
		fields["body"] = *in.Body
	}
	payload, _ := json.Marshal(fields)

	wd, err := t.resolveWorkDir(workDir, in.RepoPath)
	if err != nil {
		return prReviewError("repo_path rejected: " + err.Error())
	}
	out, err := t.effectiveRunner().RunGHWithInput(ctx, wd, string(payload), "api", "--method", "PATCH", "repos/{owner}/{repo}/pulls/"+strconv.Itoa(in.PRNumber), "--input", "-")
	if err != nil {
		return prReviewError(fmt.Sprintf("gh api update pull request failed: %s\n%s", err, out))
	}
	var updated struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		State   string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &updated); err != nil {
		return prReviewError("parse update pull request output: " + err.Error())
	}
	return prReviewSuccess(map[string]any{
		"pull_request": map[string]any{
			"number": updated.Number,
			"url":    updated.HTMLURL,
			"title":  updated.Title,
			"body":   updated.Body,
			"state":  updated.State,
		},
	})
}

type listReviewThreadsTool struct {
	prReviewToolBase
}

func (t *listReviewThreadsTool) Name() string { return "list_review_threads" }
func (t *listReviewThreadsTool) Description() string {
	return "List PR review threads and comments for a PR in the repository at repo_path (defaults to the workspace root repository). Use to find unresolved or outdated feedback before updating, replying to, or resolving review comments."
}
func (t *listReviewThreadsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pr_number": {"type": "integer", "description": "Pull request number in the repository at repo_path."},
			"repo_path": {"type": "string", "description": "` + repoPathSchemaDescription + `"}
		},
		"required": ["pr_number"]
	}`)
}
func (t *listReviewThreadsTool) IsReadOnly() bool                      { return true }
func (t *listReviewThreadsTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *listReviewThreadsTool) NeedsApproval() bool                   { return false }
func (t *listReviewThreadsTool) TimeoutSeconds() int                   { return 0 }

const listReviewThreadsQuery = `query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100) {
        nodes {
          id
          isResolved
          isOutdated
          path
          line
          comments(first: 100) {
            nodes {
              databaseId
              body
              author { login }
            }
          }
        }
      }
    }
  }
}`

type listReviewThreadsInput struct {
	PRNumber int    `json:"pr_number"`
	RepoPath string `json:"repo_path"`
}

type listReviewThreadsResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					Nodes []reviewThread `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

type reviewThread struct {
	ID         string `json:"id"`
	IsResolved bool   `json:"isResolved"`
	IsOutdated bool   `json:"isOutdated"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Comments   struct {
		Nodes []reviewThreadComment `json:"nodes"`
	} `json:"comments"`
}

type reviewThreadComment struct {
	DatabaseID int    `json:"databaseId"`
	Body       string `json:"body"`
	Author     struct {
		Login string `json:"login"`
	} `json:"author"`
}

func (t *listReviewThreadsTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in listReviewThreadsInput
	if err := json.Unmarshal(input, &in); err != nil {
		return prReviewError("Invalid input: " + err.Error())
	}
	if err := requirePRNumber(in.PRNumber); err != nil {
		return prReviewError(err.Error())
	}

	runner := t.effectiveRunner()
	wd, err := t.resolveWorkDir(workDir, in.RepoPath)
	if err != nil {
		return prReviewError("repo_path rejected: " + err.Error())
	}
	owner, repo, err := currentRepo(ctx, runner, wd)
	if err != nil {
		return prReviewError(err.Error())
	}
	payload, _ := json.Marshal(map[string]any{
		"query": listReviewThreadsQuery,
		"variables": map[string]any{
			"owner":  owner,
			"repo":   repo,
			"number": in.PRNumber,
		},
	})
	out, err := runner.RunGHWithInput(ctx, wd, string(payload), "api", "graphql", "--input", "-")
	if err != nil {
		return prReviewError(fmt.Sprintf("gh api graphql failed: %s\n%s", err, out))
	}
	var parsed listReviewThreadsResponse
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return prReviewError("parse review threads output: " + err.Error())
	}
	return prReviewSuccess(map[string]any{
		"threads": parsed.Data.Repository.PullRequest.ReviewThreads.Nodes,
	})
}

type submitPullRequestReviewTool struct {
	prReviewToolBase
}

func (t *submitPullRequestReviewTool) Name() string { return "submit_pull_request_review" }
func (t *submitPullRequestReviewTool) Description() string {
	return "Submit APPROVE, REQUEST_CHANGES, or COMMENT review on a PR in the repository at repo_path (defaults to the workspace root repository), optionally with inline comments. The gratefulagents GitHub App authorization footer is added automatically to every posted body. Use when reviewing someone else's PR."
}
func (t *submitPullRequestReviewTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pr_number": {"type": "integer", "description": "Pull request number in the repository at repo_path."},
			"repo_path": {"type": "string", "description": "` + repoPathSchemaDescription + `"},
			"event": {"type": "string", "enum": ["APPROVE", "REQUEST_CHANGES", "COMMENT"], "description": "Review event to submit."},
			"body": {"type": "string", "description": "Review body in markdown."},
			"comments": {
				"type": "array",
				"description": "Optional inline comments on changed lines.",
				"items": {
					"type": "object",
					"properties": {
						"path": {"type": "string"},
						"line": {"type": "integer"},
						"side": {"type": "string", "enum": ["LEFT", "RIGHT"]},
						"body": {"type": "string"}
					},
					"required": ["path", "line", "side", "body"]
				}
			}
		},
		"required": ["pr_number", "event", "body"]
	}`)
}
func (t *submitPullRequestReviewTool) IsReadOnly() bool                      { return false }
func (t *submitPullRequestReviewTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *submitPullRequestReviewTool) NeedsApproval() bool                   { return false }
func (t *submitPullRequestReviewTool) TimeoutSeconds() int                   { return 0 }

type submitPullRequestReviewInput struct {
	PRNumber int                  `json:"pr_number"`
	RepoPath string               `json:"repo_path"`
	Event    string               `json:"event"`
	Body     string               `json:"body"`
	Comments []pullRequestComment `json:"comments,omitempty"`
}

type pullRequestComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

func (t *submitPullRequestReviewTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in submitPullRequestReviewInput
	if err := json.Unmarshal(input, &in); err != nil {
		return prReviewError("Invalid input: " + err.Error())
	}
	if err := requirePRNumber(in.PRNumber); err != nil {
		return prReviewError(err.Error())
	}
	if !validReviewEvent(in.Event) {
		return prReviewError("event must be APPROVE, REQUEST_CHANGES, or COMMENT")
	}
	for i, comment := range in.Comments {
		if strings.TrimSpace(comment.Path) == "" || comment.Line <= 0 || strings.TrimSpace(comment.Side) == "" || strings.TrimSpace(comment.Body) == "" {
			return prReviewError(fmt.Sprintf("comments[%d] requires path, positive line, side, and body", i))
		}
		in.Comments[i].Body = attributeGitHubComment(comment.Body)
	}

	body, _ := json.Marshal(struct {
		Event    string               `json:"event"`
		Body     string               `json:"body"`
		Comments []pullRequestComment `json:"comments,omitempty"`
	}{
		Event:    in.Event,
		Body:     attributeGitHubComment(in.Body),
		Comments: in.Comments,
	})
	runner := t.effectiveRunner()
	wd, err := t.resolveWorkDir(workDir, in.RepoPath)
	if err != nil {
		return prReviewError("repo_path rejected: " + err.Error())
	}
	out, err := runner.RunGHWithInput(ctx, wd, string(body), "api", "--method", "POST", "repos/{owner}/{repo}/pulls/"+strconv.Itoa(in.PRNumber)+"/reviews", "--input", "-")
	if err != nil {
		return prReviewError(fmt.Sprintf("gh api submit review failed: %s\n%s", err, out))
	}
	var parsed any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return prReviewError("parse submit review output: " + err.Error())
	}
	return prReviewSuccess(map[string]any{"review": parsed})
}

func validReviewEvent(event string) bool {
	switch event {
	case "APPROVE", "REQUEST_CHANGES", "COMMENT":
		return true
	default:
		return false
	}
}

type replyToReviewThreadTool struct {
	prReviewToolBase
}

func (t *replyToReviewThreadTool) Name() string { return "reply_to_review_thread" }
func (t *replyToReviewThreadTool) Description() string {
	return "Reply to an existing PR review thread by node id in the repository at repo_path (defaults to the workspace root repository). The gratefulagents GitHub App authorization footer is added automatically. Use to acknowledge or explain fixes for review feedback on your PR."
}
func (t *replyToReviewThreadTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"thread_id": {"type": "string", "description": "PullRequestReviewThread GraphQL node id."},
			"body": {"type": "string", "description": "Reply body in markdown."},
			"repo_path": {"type": "string", "description": "` + repoPathSchemaDescription + `"}
		},
		"required": ["thread_id", "body"]
	}`)
}
func (t *replyToReviewThreadTool) IsReadOnly() bool                      { return false }
func (t *replyToReviewThreadTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *replyToReviewThreadTool) NeedsApproval() bool                   { return false }
func (t *replyToReviewThreadTool) TimeoutSeconds() int                   { return 0 }

const replyReviewThreadMutation = `mutation($threadID: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $threadID, body: $body}) {
    comment {
      databaseId
      body
      author { login }
    }
  }
}`

type replyToReviewThreadInput struct {
	ThreadID string `json:"thread_id"`
	Body     string `json:"body"`
	RepoPath string `json:"repo_path"`
}

func (t *replyToReviewThreadTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in replyToReviewThreadInput
	if err := json.Unmarshal(input, &in); err != nil {
		return prReviewError("Invalid input: " + err.Error())
	}
	if strings.TrimSpace(in.ThreadID) == "" || strings.TrimSpace(in.Body) == "" {
		return prReviewError("thread_id and body are required")
	}
	wd, err := t.resolveWorkDir(workDir, in.RepoPath)
	if err != nil {
		return prReviewError("repo_path rejected: " + err.Error())
	}
	out, err := t.runGraphQL(ctx, wd, replyReviewThreadMutation, map[string]any{
		"threadID": in.ThreadID,
		"body":     attributeGitHubComment(in.Body),
	})
	if err != nil {
		return prReviewError(err.Error())
	}
	var parsed any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return prReviewError("parse reply output: " + err.Error())
	}
	return prReviewSuccess(parsed)
}

func (t *replyToReviewThreadTool) runGraphQL(ctx context.Context, workDir, query string, variables map[string]any) (string, error) {
	payload, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	out, err := t.effectiveRunner().RunGHWithInput(ctx, workDir, string(payload), "api", "graphql", "--input", "-")
	if err != nil {
		return "", fmt.Errorf("gh api graphql failed: %w\n%s", err, out)
	}
	return out, nil
}

type resolveReviewThreadTool struct {
	prReviewToolBase
}

func (t *resolveReviewThreadTool) Name() string { return "resolve_review_thread" }
func (t *resolveReviewThreadTool) Description() string {
	return "Resolve or unresolve a PR review thread by node id in the repository at repo_path (defaults to the workspace root repository) after feedback has been addressed or needs reopening."
}
func (t *resolveReviewThreadTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"thread_id": {"type": "string", "description": "PullRequestReviewThread GraphQL node id."},
			"unresolve": {"type": "boolean", "description": "Set true to unresolve instead of resolve. Defaults to false."},
			"repo_path": {"type": "string", "description": "` + repoPathSchemaDescription + `"}
		},
		"required": ["thread_id"]
	}`)
}
func (t *resolveReviewThreadTool) IsReadOnly() bool                      { return false }
func (t *resolveReviewThreadTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *resolveReviewThreadTool) NeedsApproval() bool                   { return false }
func (t *resolveReviewThreadTool) TimeoutSeconds() int                   { return 0 }

const resolveReviewThreadMutation = `mutation($threadID: ID!) {
  resolveReviewThread(input: {threadId: $threadID}) {
    thread { id isResolved }
  }
}`

const unresolveReviewThreadMutation = `mutation($threadID: ID!) {
  unresolveReviewThread(input: {threadId: $threadID}) {
    thread { id isResolved }
  }
}`

type resolveReviewThreadInput struct {
	ThreadID  string `json:"thread_id"`
	Unresolve bool   `json:"unresolve"`
	RepoPath  string `json:"repo_path"`
}

func (t *resolveReviewThreadTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in resolveReviewThreadInput
	if err := json.Unmarshal(input, &in); err != nil {
		return prReviewError("Invalid input: " + err.Error())
	}
	if strings.TrimSpace(in.ThreadID) == "" {
		return prReviewError("thread_id is required")
	}
	query := resolveReviewThreadMutation
	if in.Unresolve {
		query = unresolveReviewThreadMutation
	}
	payload, _ := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"threadID": in.ThreadID},
	})
	wd, err := t.resolveWorkDir(workDir, in.RepoPath)
	if err != nil {
		return prReviewError("repo_path rejected: " + err.Error())
	}
	out, err := t.effectiveRunner().RunGHWithInput(ctx, wd, string(payload), "api", "graphql", "--input", "-")
	if err != nil {
		return prReviewError(fmt.Sprintf("gh api graphql failed: %s\n%s", err, out))
	}
	var parsed any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return prReviewError("parse resolve output: " + err.Error())
	}
	return prReviewSuccess(parsed)
}

type requestReReviewTool struct {
	prReviewToolBase
}

func (t *requestReReviewTool) Name() string { return "request_re_review" }
func (t *requestReReviewTool) Description() string {
	return "Re-request review from a GitHub user on a PR in the repository at repo_path (defaults to the workspace root repository). Use after addressing that reviewer's feedback on your PR."
}
func (t *requestReReviewTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pr_number": {"type": "integer", "description": "Pull request number in the repository at repo_path."},
			"reviewer": {"type": "string", "description": "GitHub login to request review from."},
			"repo_path": {"type": "string", "description": "` + repoPathSchemaDescription + `"}
		},
		"required": ["pr_number", "reviewer"]
	}`)
}
func (t *requestReReviewTool) IsReadOnly() bool                      { return false }
func (t *requestReReviewTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *requestReReviewTool) NeedsApproval() bool                   { return false }
func (t *requestReReviewTool) TimeoutSeconds() int                   { return 0 }

type requestReReviewInput struct {
	PRNumber int    `json:"pr_number"`
	Reviewer string `json:"reviewer"`
	RepoPath string `json:"repo_path"`
}

func (t *requestReReviewTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in requestReReviewInput
	if err := json.Unmarshal(input, &in); err != nil {
		return prReviewError("Invalid input: " + err.Error())
	}
	if err := requirePRNumber(in.PRNumber); err != nil {
		return prReviewError(err.Error())
	}
	if strings.TrimSpace(in.Reviewer) == "" {
		return prReviewError("reviewer is required")
	}
	body, _ := json.Marshal(map[string][]string{"reviewers": {in.Reviewer}})
	wd, err := t.resolveWorkDir(workDir, in.RepoPath)
	if err != nil {
		return prReviewError("repo_path rejected: " + err.Error())
	}
	out, err := t.effectiveRunner().RunGHWithInput(ctx, wd, string(body), "api", "--method", "POST", "repos/{owner}/{repo}/pulls/"+strconv.Itoa(in.PRNumber)+"/requested_reviewers", "--input", "-")
	if err != nil {
		return prReviewError(fmt.Sprintf("gh api request re-review failed: %s\n%s", err, out))
	}
	var parsed any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return prReviewError("parse request re-review output: " + err.Error())
	}
	return prReviewSuccess(map[string]any{"requested_reviewers": parsed})
}

type getPullRequestChecksTool struct {
	prReviewToolBase
}

func (t *getPullRequestChecksTool) Name() string { return "get_pull_request_checks" }
func (t *getPullRequestChecksTool) Description() string {
	return "List CI check runs and commit statuses for a PR's head commit. Use to see whether checks are passing before requesting review or merging."
}
func (t *getPullRequestChecksTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pr_number": {"type": "integer", "description": "Pull request number in the repository at repo_path."},
			"repo_path": {"type": "string", "description": "` + repoPathSchemaDescription + `"}
		},
		"required": ["pr_number"]
	}`)
}
func (t *getPullRequestChecksTool) IsReadOnly() bool                      { return true }
func (t *getPullRequestChecksTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *getPullRequestChecksTool) NeedsApproval() bool                   { return false }
func (t *getPullRequestChecksTool) TimeoutSeconds() int                   { return 0 }

type getPullRequestChecksInput struct {
	PRNumber int    `json:"pr_number"`
	RepoPath string `json:"repo_path"`
}

type pullRequestCheckRun struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	DetailsURL  string `json:"details_url"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
}

type pullRequestCommitStatus struct {
	Context     string `json:"context"`
	State       string `json:"state"`
	TargetURL   string `json:"target_url"`
	Description string `json:"description"`
}

type pullRequestChecksSummary struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
}

func (t *getPullRequestChecksTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in getPullRequestChecksInput
	if err := json.Unmarshal(input, &in); err != nil {
		return prReviewError("Invalid input: " + err.Error())
	}
	if err := requirePRNumber(in.PRNumber); err != nil {
		return prReviewError(err.Error())
	}

	runner := t.effectiveRunner()
	wd, err := t.resolveWorkDir(workDir, in.RepoPath)
	if err != nil {
		return prReviewError("repo_path rejected: " + err.Error())
	}

	number := strconv.Itoa(in.PRNumber)
	viewOut, err := runner.RunGH(ctx, wd, "pr", "view", number, "--json", "headRefOid")
	if err != nil {
		return prReviewError(fmt.Sprintf("gh pr view failed: %s\n%s", err, viewOut))
	}
	var view struct {
		HeadRefOid string `json:"headRefOid"`
	}
	if err := json.Unmarshal([]byte(viewOut), &view); err != nil {
		return prReviewError("parse gh pr view output: " + err.Error())
	}
	if view.HeadRefOid == "" {
		return prReviewError("gh pr view returned no head commit SHA")
	}

	checksOut, err := runner.RunGH(ctx, wd, "api", "repos/{owner}/{repo}/commits/"+view.HeadRefOid+"/check-runs", "--paginate")
	if err != nil {
		return prReviewError(fmt.Sprintf("gh api check-runs failed: %s\n%s", err, checksOut))
	}
	checks, err := parseCheckRunPages(checksOut)
	if err != nil {
		return prReviewError("parse check-runs output: " + err.Error())
	}

	statusOut, err := runner.RunGH(ctx, wd, "api", "repos/{owner}/{repo}/commits/"+view.HeadRefOid+"/status")
	if err != nil {
		return prReviewError(fmt.Sprintf("gh api commit status failed: %s\n%s", err, statusOut))
	}
	var status struct {
		Statuses []pullRequestCommitStatus `json:"statuses"`
	}
	if err := json.Unmarshal([]byte(statusOut), &status); err != nil {
		return prReviewError("parse commit status output: " + err.Error())
	}

	summary := pullRequestChecksSummary{Total: len(checks) + len(status.Statuses)}
	for _, check := range checks {
		if check.Status != "completed" {
			summary.Pending++
			continue
		}
		switch check.Conclusion {
		case "success", "neutral", "skipped":
			summary.Passed++
		default:
			summary.Failed++
		}
	}
	for _, s := range status.Statuses {
		switch s.State {
		case "success":
			summary.Passed++
		case "pending":
			summary.Pending++
		default:
			summary.Failed++
		}
	}

	if checks == nil {
		checks = []pullRequestCheckRun{}
	}
	if status.Statuses == nil {
		status.Statuses = []pullRequestCommitStatus{}
	}
	return prReviewSuccess(map[string]any{
		"head_sha": view.HeadRefOid,
		"checks":   checks,
		"statuses": status.Statuses,
		"summary":  summary,
	})
}

func parseCheckRunPages(out string) ([]pullRequestCheckRun, error) {
	var checks []pullRequestCheckRun
	decoder := json.NewDecoder(strings.NewReader(out))
	for decoder.More() {
		var page struct {
			CheckRuns []pullRequestCheckRun `json:"check_runs"`
		}
		if err := decoder.Decode(&page); err != nil {
			return nil, err
		}
		checks = append(checks, page.CheckRuns...)
	}
	return checks, nil
}
