package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

// RegisterGitHubIssueManagementTools registers tools for viewing and managing
// existing GitHub issues. Issue creation remains separately registered because
// it records the created issue as an AgentRun artifact.
func RegisterGitHubIssueManagementTools(registry *Registry, workDir string) {
	if registry == nil {
		return
	}
	runner := prReviewExecRunner{}
	base := githubIssueToolBase{runner: runner, workDir: workDir}
	registry.Register(&getGitHubIssueTool{githubIssueToolBase: base})
	registry.Register(&updateGitHubIssueTool{githubIssueToolBase: base})
	registry.Register(&closeGitHubIssueTool{githubIssueToolBase: base})
	registry.Register(&updateGitHubIssueLabelsTool{githubIssueToolBase: base})
	registry.Register(&addGitHubIssueCommentTool{githubIssueToolBase: base})
}

const issueRepoPathSchemaDescription = "Workspace-relative path to the git repository the issue belongs to, for example repos/<alias>. Defaults to the workspace root repository."

type githubIssueToolBase struct {
	runner  prReviewRunner
	workDir string
}

func (b githubIssueToolBase) effectiveRunner() prReviewRunner {
	if b.runner != nil {
		return b.runner
	}
	return prReviewExecRunner{}
}

func (b githubIssueToolBase) resolveWorkDir(workDir, repoPath string) (string, error) {
	if b.workDir != "" {
		workDir = b.workDir
	}
	return resolveLocalGitRepositoryWorkDir(workDir, repoPath)
}

func requireIssueNumber(number int) error {
	if number <= 0 {
		return fmt.Errorf("issue_number must be greater than zero")
	}
	return nil
}

func githubIssueSuccess(value any) (Result, error) {
	b, _ := json.Marshal(value)
	return Result{Content: string(b)}, nil
}

func githubIssueError(msg string) (Result, error) {
	if strings.Contains(msg, "Not Found") || strings.Contains(msg, "Could not resolve to an Issue") {
		msg += "\nhint: the issue number was not found in the selected repository — if it belongs to another workspace repository, pass repo_path (workspace root by default, attached repositories under repos/<alias>)."
	}
	b, _ := json.Marshal(map[string]string{"status": "error", "error": msg})
	return Result{Content: string(b), IsError: true}, nil
}

func parseGitHubJSON(out, operation string) (any, error) {
	var parsed any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("parse %s output: %w", operation, err)
	}
	return parsed, nil
}

func fetchGitHubIssue(ctx context.Context, runner prReviewRunner, workDir string, issueNumber int) (any, error) {
	number := strconv.Itoa(issueNumber)
	out, err := runner.RunGH(ctx, workDir, "issue", "view", number, "--json", "number,url,title,body,state,stateReason,author,assignees,labels,milestone,createdAt,updatedAt,closedAt")
	if err != nil {
		return nil, fmt.Errorf("gh issue view failed: %s\n%s", err, out)
	}
	var issue map[string]any
	if err := json.Unmarshal([]byte(out), &issue); err != nil {
		return nil, fmt.Errorf("parse gh issue view output: %w", err)
	}

	commentsOut, err := runner.RunGH(ctx, workDir, "api", "repos/{owner}/{repo}/issues/"+number+"/comments", "--paginate", "--slurp")
	if err != nil {
		return nil, fmt.Errorf("gh api issue comments failed: %s\n%s", err, commentsOut)
	}
	comments, err := parsePaginatedGitHubObjects(commentsOut)
	if err != nil {
		return nil, fmt.Errorf("parse issue comments output: %w", err)
	}
	issue["comments"] = comments
	return issue, nil
}

func parsePaginatedGitHubObjects(out string) ([]any, error) {
	var pages [][]any
	if err := json.Unmarshal([]byte(out), &pages); err == nil {
		var objects []any
		for _, page := range pages {
			objects = append(objects, page...)
		}
		return objects, nil
	}
	var objects []any
	if err := json.Unmarshal([]byte(out), &objects); err != nil {
		return nil, err
	}
	return objects, nil
}

// ensureGitHubIssue rejects pull request numbers before using GitHub's shared
// issues REST routes, which otherwise allow issue tools to mutate PRs.
func ensureGitHubIssue(ctx context.Context, runner prReviewRunner, workDir string, issueNumber int) error {
	out, err := runner.RunGH(ctx, workDir, "api", "repos/{owner}/{repo}/issues/"+strconv.Itoa(issueNumber))
	if err != nil {
		return fmt.Errorf("gh api get issue failed: %s\n%s", err, out)
	}
	var resource struct {
		PullRequest json.RawMessage `json:"pull_request"`
	}
	if err := json.Unmarshal([]byte(out), &resource); err != nil {
		return fmt.Errorf("parse get issue output: %w", err)
	}
	if len(resource.PullRequest) > 0 && string(resource.PullRequest) != "null" {
		return fmt.Errorf("number %d identifies a pull request, not an issue", issueNumber)
	}
	return nil
}

type getGitHubIssueTool struct{ githubIssueToolBase }

func (t *getGitHubIssueTool) Name() string { return "get_github_issue" }
func (t *getGitHubIssueTool) Description() string {
	return "View a GitHub issue, including its metadata, labels, assignees, and comments, in the repository at repo_path (defaults to the workspace root repository)."
}
func (t *getGitHubIssueTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"issue_number": {"type": "integer", "description": "Issue number in the repository at repo_path."},
			"repo_path": {"type": "string", "description": "` + issueRepoPathSchemaDescription + `"}
		},
		"required": ["issue_number"]
	}`)
}
func (t *getGitHubIssueTool) IsReadOnly() bool                      { return true }
func (t *getGitHubIssueTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *getGitHubIssueTool) NeedsApproval() bool                   { return false }
func (t *getGitHubIssueTool) TimeoutSeconds() int                   { return 0 }

type githubIssueNumberInput struct {
	IssueNumber int    `json:"issue_number"`
	RepoPath    string `json:"repo_path"`
}

func (t *getGitHubIssueTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in githubIssueNumberInput
	if err := json.Unmarshal(input, &in); err != nil {
		return githubIssueError("Invalid input: " + err.Error())
	}
	if err := requireIssueNumber(in.IssueNumber); err != nil {
		return githubIssueError(err.Error())
	}
	wd, err := t.resolveWorkDir(workDir, in.RepoPath)
	if err != nil {
		return githubIssueError("repo_path rejected: " + err.Error())
	}
	issue, err := fetchGitHubIssue(ctx, t.effectiveRunner(), wd, in.IssueNumber)
	if err != nil {
		return githubIssueError(err.Error())
	}
	return githubIssueSuccess(map[string]any{"issue": issue})
}

type updateGitHubIssueTool struct{ githubIssueToolBase }

func (t *updateGitHubIssueTool) Name() string { return "update_github_issue" }
func (t *updateGitHubIssueTool) Description() string {
	return "Edit the title and/or body of an existing GitHub issue in the repository at repo_path. Only fields provided by the caller are changed."
}
func (t *updateGitHubIssueTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"issue_number": {"type": "integer", "description": "Issue number in the repository at repo_path."},
			"title": {"type": "string", "description": "New issue title. Omit to keep the current title."},
			"body": {"type": "string", "description": "New issue body in markdown. An empty string clears it; omit to keep it unchanged."},
			"repo_path": {"type": "string", "description": "` + issueRepoPathSchemaDescription + `"}
		},
		"required": ["issue_number"]
	}`)
}
func (t *updateGitHubIssueTool) IsReadOnly() bool                      { return false }
func (t *updateGitHubIssueTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *updateGitHubIssueTool) NeedsApproval() bool                   { return false }
func (t *updateGitHubIssueTool) TimeoutSeconds() int                   { return 0 }

type updateGitHubIssueInput struct {
	IssueNumber int     `json:"issue_number"`
	Title       *string `json:"title"`
	Body        *string `json:"body"`
	RepoPath    string  `json:"repo_path"`
}

func (t *updateGitHubIssueTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in updateGitHubIssueInput
	if err := json.Unmarshal(input, &in); err != nil {
		return githubIssueError("Invalid input: " + err.Error())
	}
	if err := requireIssueNumber(in.IssueNumber); err != nil {
		return githubIssueError(err.Error())
	}
	if in.Title == nil && in.Body == nil {
		return githubIssueError("provide title and/or body to update")
	}
	if in.Title != nil && strings.TrimSpace(*in.Title) == "" {
		return githubIssueError("title cannot be blank; omit it to keep the current title")
	}
	payload := map[string]string{}
	if in.Title != nil {
		payload["title"] = *in.Title
	}
	if in.Body != nil {
		payload["body"] = *in.Body
	}
	return t.patchIssue(ctx, workDir, in.RepoPath, in.IssueNumber, payload, "update")
}

func (t *updateGitHubIssueTool) patchIssue(ctx context.Context, workDir, repoPath string, issueNumber int, payload any, operation string) (Result, error) {
	wd, err := t.resolveWorkDir(workDir, repoPath)
	if err != nil {
		return githubIssueError("repo_path rejected: " + err.Error())
	}
	runner := t.effectiveRunner()
	if err := ensureGitHubIssue(ctx, runner, wd, issueNumber); err != nil {
		return githubIssueError(err.Error())
	}
	body, _ := json.Marshal(payload)
	out, err := runner.RunGHWithInput(ctx, wd, string(body), "api", "--method", "PATCH", "repos/{owner}/{repo}/issues/"+strconv.Itoa(issueNumber), "--input", "-")
	if err != nil {
		return githubIssueError(fmt.Sprintf("gh api %s issue failed: %s\n%s", operation, err, out))
	}
	issue, err := parseGitHubJSON(out, operation+" issue")
	if err != nil {
		return githubIssueError(err.Error())
	}
	return githubIssueSuccess(map[string]any{"issue": issue})
}

type closeGitHubIssueTool struct{ githubIssueToolBase }

func (t *closeGitHubIssueTool) Name() string { return "close_github_issue" }
func (t *closeGitHubIssueTool) Description() string {
	return "Close a GitHub issue as completed or not planned in the repository at repo_path (defaults to the workspace root repository)."
}
func (t *closeGitHubIssueTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"issue_number": {"type": "integer", "description": "Issue number in the repository at repo_path."},
			"reason": {"type": "string", "enum": ["completed", "not_planned"], "description": "Closure reason. Defaults to completed."},
			"repo_path": {"type": "string", "description": "` + issueRepoPathSchemaDescription + `"}
		},
		"required": ["issue_number"]
	}`)
}
func (t *closeGitHubIssueTool) IsReadOnly() bool                      { return false }
func (t *closeGitHubIssueTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *closeGitHubIssueTool) NeedsApproval() bool                   { return false }
func (t *closeGitHubIssueTool) TimeoutSeconds() int                   { return 0 }

type closeGitHubIssueInput struct {
	IssueNumber int    `json:"issue_number"`
	Reason      string `json:"reason"`
	RepoPath    string `json:"repo_path"`
}

func (t *closeGitHubIssueTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in closeGitHubIssueInput
	if err := json.Unmarshal(input, &in); err != nil {
		return githubIssueError("Invalid input: " + err.Error())
	}
	if err := requireIssueNumber(in.IssueNumber); err != nil {
		return githubIssueError(err.Error())
	}
	if in.Reason == "" {
		in.Reason = "completed"
	}
	if in.Reason != "completed" && in.Reason != "not_planned" {
		return githubIssueError("reason must be completed or not_planned")
	}
	updater := updateGitHubIssueTool{githubIssueToolBase: t.githubIssueToolBase}
	return updater.patchIssue(ctx, workDir, in.RepoPath, in.IssueNumber, map[string]string{"state": "closed", "state_reason": in.Reason}, "close")
}

type updateGitHubIssueLabelsTool struct{ githubIssueToolBase }

func (t *updateGitHubIssueLabelsTool) Name() string { return "update_github_issue_labels" }
func (t *updateGitHubIssueLabelsTool) Description() string {
	return "Add and/or remove labels on a GitHub issue. Labels must already exist in the selected repository."
}
func (t *updateGitHubIssueLabelsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"issue_number": {"type": "integer", "description": "Issue number in the repository at repo_path."},
			"add_labels": {"type": "array", "items": {"type": "string"}, "description": "Existing repository labels to add."},
			"remove_labels": {"type": "array", "items": {"type": "string"}, "description": "Labels to remove."},
			"repo_path": {"type": "string", "description": "` + issueRepoPathSchemaDescription + `"}
		},
		"required": ["issue_number"]
	}`)
}
func (t *updateGitHubIssueLabelsTool) IsReadOnly() bool                      { return false }
func (t *updateGitHubIssueLabelsTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *updateGitHubIssueLabelsTool) NeedsApproval() bool                   { return false }
func (t *updateGitHubIssueLabelsTool) TimeoutSeconds() int                   { return 0 }

type updateGitHubIssueLabelsInput struct {
	IssueNumber  int      `json:"issue_number"`
	AddLabels    []string `json:"add_labels"`
	RemoveLabels []string `json:"remove_labels"`
	RepoPath     string   `json:"repo_path"`
}

func (t *updateGitHubIssueLabelsTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in updateGitHubIssueLabelsInput
	if err := json.Unmarshal(input, &in); err != nil {
		return githubIssueError("Invalid input: " + err.Error())
	}
	if err := requireIssueNumber(in.IssueNumber); err != nil {
		return githubIssueError(err.Error())
	}
	in.AddLabels = nonBlankUnique(in.AddLabels)
	in.RemoveLabels = nonBlankUnique(in.RemoveLabels)
	if len(in.AddLabels) == 0 && len(in.RemoveLabels) == 0 {
		return githubIssueError("provide at least one nonblank add_labels or remove_labels entry")
	}
	wd, err := t.resolveWorkDir(workDir, in.RepoPath)
	if err != nil {
		return githubIssueError("repo_path rejected: " + err.Error())
	}
	runner := t.effectiveRunner()
	if err := ensureGitHubIssue(ctx, runner, wd, in.IssueNumber); err != nil {
		return githubIssueError(err.Error())
	}
	args := []string{"issue", "edit", strconv.Itoa(in.IssueNumber)}
	for _, label := range in.AddLabels {
		args = append(args, "--add-label", label)
	}
	for _, label := range in.RemoveLabels {
		args = append(args, "--remove-label", label)
	}
	out, err := runner.RunGH(ctx, wd, args...)
	if err != nil {
		return githubIssueError(fmt.Sprintf("gh issue edit labels failed: %s\n%s", err, out))
	}
	issue, err := fetchGitHubIssue(ctx, runner, wd, in.IssueNumber)
	if err != nil {
		return githubIssueError(err.Error())
	}
	return githubIssueSuccess(map[string]any{"issue": issue})
}

func nonBlankUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

type addGitHubIssueCommentTool struct{ githubIssueToolBase }

func (t *addGitHubIssueCommentTool) Name() string { return "add_github_issue_comment" }
func (t *addGitHubIssueCommentTool) Description() string {
	return "Add a markdown comment to a GitHub issue in the repository at repo_path (defaults to the workspace root repository). The gratefulagents GitHub App authorization footer is added automatically."
}
func (t *addGitHubIssueCommentTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"issue_number": {"type": "integer", "description": "Issue number in the repository at repo_path."},
			"body": {"type": "string", "description": "Comment body in markdown."},
			"repo_path": {"type": "string", "description": "` + issueRepoPathSchemaDescription + `"}
		},
		"required": ["issue_number", "body"]
	}`)
}
func (t *addGitHubIssueCommentTool) IsReadOnly() bool                      { return false }
func (t *addGitHubIssueCommentTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *addGitHubIssueCommentTool) NeedsApproval() bool                   { return false }
func (t *addGitHubIssueCommentTool) TimeoutSeconds() int                   { return 0 }

type addGitHubIssueCommentInput struct {
	IssueNumber int    `json:"issue_number"`
	Body        string `json:"body"`
	RepoPath    string `json:"repo_path"`
}

func (t *addGitHubIssueCommentTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	var in addGitHubIssueCommentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return githubIssueError("Invalid input: " + err.Error())
	}
	if err := requireIssueNumber(in.IssueNumber); err != nil {
		return githubIssueError(err.Error())
	}
	if strings.TrimSpace(in.Body) == "" {
		return githubIssueError("body must not be blank")
	}
	wd, err := t.resolveWorkDir(workDir, in.RepoPath)
	if err != nil {
		return githubIssueError("repo_path rejected: " + err.Error())
	}
	runner := t.effectiveRunner()
	if err := ensureGitHubIssue(ctx, runner, wd, in.IssueNumber); err != nil {
		return githubIssueError(err.Error())
	}
	payload, _ := json.Marshal(map[string]string{"body": attributeGitHubComment(in.Body)})
	out, err := runner.RunGHWithInput(ctx, wd, string(payload), "api", "--method", "POST", "repos/{owner}/{repo}/issues/"+strconv.Itoa(in.IssueNumber)+"/comments", "--input", "-")
	if err != nil {
		return githubIssueError(fmt.Sprintf("gh api add issue comment failed: %s\n%s", err, out))
	}
	comment, err := parseGitHubJSON(out, "add issue comment")
	if err != nil {
		return githubIssueError(err.Error())
	}
	return githubIssueSuccess(map[string]any{"comment": comment})
}
