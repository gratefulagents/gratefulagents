package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkgit "github.com/gratefulagents/sdk/pkg/agentsdk/tools/git"
)

// RegisterGitSyncTools registers the branch-synchronization tools agents need
// for PR upkeep beyond commit/push: inspecting working-tree state, pulling the
// latest remote changes for the current branch, merging the base branch to
// resolve PR merge conflicts, and aborting a conflicted merge.
func RegisterGitSyncTools(registry *Registry) {
	if registry == nil {
		return
	}
	registry.Register(NewGitStatusTool(nil))
	registry.Register(NewGitPullTool(nil))
	registry.Register(NewGitMergeTool(nil))
	registry.Register(NewGitMergeAbortTool(nil))
}

const gitSyncRepoPathDescription = "Workspace-relative path to the git repository, for example repos/<alias>. Defaults to the workspace root."

// mergeConflictGuidance tells the agent how to finish or abandon a conflicted
// merge using the tools it already has.
const mergeConflictGuidance = "Merge left conflicts in the working tree. Open each conflicted file, resolve the <<<<<<</=======/>>>>>>> markers, then run git_commit (with all=true or the resolved paths) to create the merge commit, and git_push. To abandon the merge instead, run git_merge_abort."

type gitSyncOutput struct {
	Status          string   `json:"status"`
	Branch          string   `json:"branch,omitempty"`
	MergedFrom      string   `json:"merged_from,omitempty"`
	CommitSHA       string   `json:"commit_sha,omitempty"`
	ConflictedFiles []string `json:"conflicted_files,omitempty"`
	Guidance        string   `json:"guidance,omitempty"`
	Output          string   `json:"output,omitempty"`
	Error           string   `json:"error,omitempty"`
}

func gitSyncSuccess(out gitSyncOutput) (agentsdk.ToolResult, error) {
	b, _ := json.Marshal(out)
	return agentsdk.ToolResult{Content: string(b)}, nil
}

func gitSyncError(msg string) (agentsdk.ToolResult, error) {
	b, _ := json.Marshal(gitSyncOutput{Status: "error", Error: msg})
	return agentsdk.ToolResult{Content: string(b), IsError: true}, nil
}

func gitSyncRunner(runner sdkgit.CommandRunner) sdkgit.CommandRunner {
	if runner != nil {
		return runner
	}
	return localGitCommandRunner{}
}

// currentNonDetachedBranch returns the checked-out branch, rejecting detached
// HEAD states where pull/merge would leave commits nowhere useful.
func currentNonDetachedBranch(ctx context.Context, runner sdkgit.CommandRunner, repoDir string) (string, error) {
	out, err := runner.RunGit(ctx, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("cannot determine current branch: %v\n%s", err, out)
	}
	branch := strings.TrimSpace(out)
	if branch == "" {
		return "", fmt.Errorf("cannot determine current branch")
	}
	if branch == "HEAD" {
		return "", fmt.Errorf("HEAD is detached; check out a branch first")
	}
	return branch, nil
}

// gitRefExists reports whether a ref (e.g. MERGE_HEAD, refs/heads/x) resolves.
func gitRefExists(ctx context.Context, runner sdkgit.CommandRunner, repoDir, ref string) bool {
	out, err := runner.RunGit(ctx, repoDir, "rev-parse", "-q", "--verify", ref)
	return err == nil && strings.TrimSpace(out) != ""
}

// listConflictedFiles returns paths that are unmerged in the index.
func listConflictedFiles(ctx context.Context, runner sdkgit.CommandRunner, repoDir string) []string {
	out, err := runner.RunGit(ctx, repoDir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files
}

// fetchRemoteBranch force-updates the remote-tracking ref for branch so a
// following merge sees the latest origin state even in single-branch clones.
func fetchRemoteBranch(ctx context.Context, runner sdkgit.CommandRunner, repoDir, branch string) (string, error) {
	return runner.RunGit(ctx, repoDir, "fetch", "origin",
		"+refs/heads/"+branch+":refs/remotes/origin/"+branch)
}

func isMissingRemoteRefError(output string) bool {
	return strings.Contains(output, "couldn't find remote ref")
}

// mergeRefIntoCurrentBranch merges ref into the checked-out branch. Its merge
// commit message always credits the gratefulagents GitHub App as a co-author.
// On conflicts the merge is left in progress and a structured result is returned.
func mergeRefIntoCurrentBranch(ctx context.Context, runner sdkgit.CommandRunner, repoDir, branch, ref string) (agentsdk.ToolResult, error) {
	message, _ := ensureRequiredCoAuthor(fmt.Sprintf("Merge %s into %s", ref, branch))
	mergeOut, err := runner.RunGit(ctx, repoDir, "merge", "--no-edit", "-m", message, ref)
	if err != nil {
		if files := listConflictedFiles(ctx, runner, repoDir); len(files) > 0 {
			return gitSyncSuccess(gitSyncOutput{
				Status:          "conflicts",
				Branch:          branch,
				MergedFrom:      ref,
				ConflictedFiles: files,
				Guidance:        mergeConflictGuidance,
				Output:          strings.TrimSpace(mergeOut),
			})
		}
		msg := fmt.Sprintf("git merge failed: %v\n%s", err, mergeOut)
		if strings.Contains(mergeOut, "would be overwritten") {
			msg += "\nCommit your local changes first (git_commit), then retry."
		}
		return gitSyncError(msg)
	}

	status := "merged"
	if strings.Contains(mergeOut, "Already up to date") {
		status = "up_to_date"
	}
	sha, _ := runner.RunGit(ctx, repoDir, "rev-parse", "HEAD")
	return gitSyncSuccess(gitSyncOutput{
		Status:     status,
		Branch:     branch,
		MergedFrom: ref,
		CommitSHA:  strings.TrimSpace(sha),
		Output:     strings.TrimSpace(mergeOut),
	})
}

// ---------------------------------------------------------------------------
// git_pull

// NewGitPullTool constructs git_pull. The runner is injectable for tests.
func NewGitPullTool(runner sdkgit.CommandRunner) *GitPullTool {
	return &GitPullTool{Runner: runner}
}

// GitPullTool fetches and merges the latest origin state of the current branch.
type GitPullTool struct {
	Runner sdkgit.CommandRunner
}

func (t *GitPullTool) Name() string { return "git_pull" }

func (t *GitPullTool) Description() string {
	return "Fetch and merge the latest origin commits for the current branch (e.g. when the remote PR branch gained commits from a reviewer or another session). Merge only — never rebases or force-pushes. On conflicts it reports the conflicted files so you can resolve them, git_commit, and git_push."
}

func (t *GitPullTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"repo_path": {
				"type": "string",
				"description": "` + gitSyncRepoPathDescription + `"
			}
		}
	}`)
}

func (t *GitPullTool) IsReadOnly() bool { return false }
func (t *GitPullTool) IsEnabled(ctx *agentsdk.RunContext) bool {
	return ctx == nil || ctx.ToolAccessLevel != agentsdk.ToolAccessLevelReadOnly
}
func (t *GitPullTool) NeedsApproval() bool { return false }
func (t *GitPullTool) TimeoutSeconds() int { return 0 }

type gitPullInput struct {
	RepoPath string `json:"repo_path"`
}

func (t *GitPullTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (agentsdk.ToolResult, error) {
	var in gitPullInput
	if err := json.Unmarshal(input, &in); err != nil {
		return gitSyncError("Invalid input: " + err.Error())
	}
	repoDir, err := resolveLocalGitRepositoryWorkDir(workDir, in.RepoPath)
	if err != nil {
		return gitSyncError("repo_path rejected: " + err.Error())
	}

	runner := gitSyncRunner(t.Runner)
	branch, err := currentNonDetachedBranch(ctx, runner, repoDir)
	if err != nil {
		return gitSyncError(err.Error())
	}

	if out, err := fetchRemoteBranch(ctx, runner, repoDir, branch); err != nil {
		if isMissingRemoteRefError(out) {
			return gitSyncSuccess(gitSyncOutput{
				Status: "no_remote_branch",
				Branch: branch,
				Output: fmt.Sprintf("origin has no branch %q; nothing to pull", branch),
			})
		}
		return gitSyncError(fmt.Sprintf("git fetch failed: %v\n%s", err, out))
	}

	return mergeRefIntoCurrentBranch(ctx, runner, repoDir, branch, "origin/"+branch)
}

// ---------------------------------------------------------------------------
// git_merge

// NewGitMergeTool constructs git_merge. The runner is injectable for tests.
func NewGitMergeTool(runner sdkgit.CommandRunner) *GitMergeTool {
	return &GitMergeTool{Runner: runner}
}

// GitMergeTool merges another branch (typically the PR base) into the current
// branch, the safe way to clear "this branch has conflicts" on a PR without
// rewriting history.
type GitMergeTool struct {
	Runner sdkgit.CommandRunner
}

func (t *GitMergeTool) Name() string { return "git_merge" }

func (t *GitMergeTool) Description() string {
	return "Merge another branch (typically the PR base branch, e.g. main) into the current branch — the way to fix a PR that has merge conflicts with its base. Fetches origin/<branch> first, falling back to the local branch. On conflicts it reports the conflicted files: resolve the markers, then git_commit to complete the merge and git_push."
}

func (t *GitMergeTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["branch"],
		"properties": {
			"branch": {
				"type": "string",
				"description": "Branch to merge into the current branch, e.g. main or origin/main."
			},
			"repo_path": {
				"type": "string",
				"description": "` + gitSyncRepoPathDescription + `"
			}
		}
	}`)
}

func (t *GitMergeTool) IsReadOnly() bool { return false }
func (t *GitMergeTool) IsEnabled(ctx *agentsdk.RunContext) bool {
	return ctx == nil || ctx.ToolAccessLevel != agentsdk.ToolAccessLevelReadOnly
}
func (t *GitMergeTool) NeedsApproval() bool { return false }
func (t *GitMergeTool) TimeoutSeconds() int { return 0 }

type gitMergeInput struct {
	Branch   string `json:"branch"`
	RepoPath string `json:"repo_path"`
}

func (t *GitMergeTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (agentsdk.ToolResult, error) {
	var in gitMergeInput
	if err := json.Unmarshal(input, &in); err != nil {
		return gitSyncError("Invalid input: " + err.Error())
	}
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(in.Branch), "origin/"))
	if name == "" {
		return gitSyncError("branch is required")
	}
	repoDir, err := resolveLocalGitRepositoryWorkDir(workDir, in.RepoPath)
	if err != nil {
		return gitSyncError("repo_path rejected: " + err.Error())
	}

	runner := gitSyncRunner(t.Runner)
	branch, err := currentNonDetachedBranch(ctx, runner, repoDir)
	if err != nil {
		return gitSyncError(err.Error())
	}

	ref := "origin/" + name
	if out, err := fetchRemoteBranch(ctx, runner, repoDir, name); err != nil {
		if !isMissingRemoteRefError(out) {
			return gitSyncError(fmt.Sprintf("git fetch failed: %v\n%s", err, out))
		}
		if !gitRefExists(ctx, runner, repoDir, "refs/heads/"+name) {
			return gitSyncError(fmt.Sprintf("branch %q not found on origin or locally", name))
		}
		ref = name
	}

	return mergeRefIntoCurrentBranch(ctx, runner, repoDir, branch, ref)
}

// ---------------------------------------------------------------------------
// git_merge_abort

// NewGitMergeAbortTool constructs git_merge_abort. The runner is injectable
// for tests.
func NewGitMergeAbortTool(runner sdkgit.CommandRunner) *GitMergeAbortTool {
	return &GitMergeAbortTool{Runner: runner}
}

// GitMergeAbortTool aborts an in-progress merge, rebase, or cherry-pick.
type GitMergeAbortTool struct {
	Runner sdkgit.CommandRunner
}

func (t *GitMergeAbortTool) Name() string { return "git_merge_abort" }

func (t *GitMergeAbortTool) Description() string {
	return "Abort an in-progress merge, rebase, or cherry-pick and restore the working tree to its previous state. Use to abandon a conflicted git_pull/git_merge instead of resolving it."
}

func (t *GitMergeAbortTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"repo_path": {
				"type": "string",
				"description": "` + gitSyncRepoPathDescription + `"
			}
		}
	}`)
}

func (t *GitMergeAbortTool) IsReadOnly() bool { return false }
func (t *GitMergeAbortTool) IsEnabled(ctx *agentsdk.RunContext) bool {
	return ctx == nil || ctx.ToolAccessLevel != agentsdk.ToolAccessLevelReadOnly
}
func (t *GitMergeAbortTool) NeedsApproval() bool { return false }
func (t *GitMergeAbortTool) TimeoutSeconds() int { return 0 }

type gitMergeAbortInput struct {
	RepoPath string `json:"repo_path"`
}

func (t *GitMergeAbortTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (agentsdk.ToolResult, error) {
	var in gitMergeAbortInput
	if err := json.Unmarshal(input, &in); err != nil {
		return gitSyncError("Invalid input: " + err.Error())
	}
	repoDir, err := resolveLocalGitRepositoryWorkDir(workDir, in.RepoPath)
	if err != nil {
		return gitSyncError("repo_path rejected: " + err.Error())
	}

	runner := gitSyncRunner(t.Runner)
	operations := []struct {
		ref  string
		name string
		args []string
	}{
		{ref: "MERGE_HEAD", name: "merge", args: []string{"merge", "--abort"}},
		{ref: "REBASE_HEAD", name: "rebase", args: []string{"rebase", "--abort"}},
		{ref: "CHERRY_PICK_HEAD", name: "cherry-pick", args: []string{"cherry-pick", "--abort"}},
	}
	for _, op := range operations {
		if !gitRefExists(ctx, runner, repoDir, op.ref) {
			continue
		}
		if out, err := runner.RunGit(ctx, repoDir, op.args...); err != nil {
			return gitSyncError(fmt.Sprintf("git %s failed: %v\n%s", strings.Join(op.args, " "), err, out))
		}
		return gitSyncSuccess(gitSyncOutput{
			Status: "aborted",
			Output: "aborted in-progress " + op.name,
		})
	}
	return gitSyncError("no merge, rebase, or cherry-pick in progress")
}

// ---------------------------------------------------------------------------
// git_status

// NewGitStatusTool constructs git_status. The runner is injectable for tests.
func NewGitStatusTool(runner sdkgit.CommandRunner) *GitStatusTool {
	return &GitStatusTool{Runner: runner}
}

// GitStatusTool reports branch/sync/conflict state for a workspace repository.
type GitStatusTool struct {
	Runner sdkgit.CommandRunner
}

func (t *GitStatusTool) Name() string { return "git_status" }

func (t *GitStatusTool) Description() string {
	return "Show git state for a workspace repository: current branch, upstream, ahead/behind counts, staged/unstaged/untracked files, merge conflicts, and any in-progress merge/rebase/cherry-pick. Use it to check whether a pull or base-branch merge is needed and to track conflict resolution progress."
}

func (t *GitStatusTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"repo_path": {
				"type": "string",
				"description": "` + gitSyncRepoPathDescription + `"
			}
		}
	}`)
}

func (t *GitStatusTool) IsReadOnly() bool                      { return true }
func (t *GitStatusTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *GitStatusTool) NeedsApproval() bool                   { return false }
func (t *GitStatusTool) TimeoutSeconds() int                   { return 0 }

type gitStatusInput struct {
	RepoPath string `json:"repo_path"`
}

type gitStatusOutput struct {
	Status          string   `json:"status"`
	Branch          string   `json:"branch,omitempty"`
	Upstream        string   `json:"upstream,omitempty"`
	Ahead           int      `json:"ahead"`
	Behind          int      `json:"behind"`
	Clean           bool     `json:"clean"`
	Operation       string   `json:"operation,omitempty"`
	StagedFiles     []string `json:"staged_files,omitempty"`
	UnstagedFiles   []string `json:"unstaged_files,omitempty"`
	UntrackedFiles  []string `json:"untracked_files,omitempty"`
	ConflictedFiles []string `json:"conflicted_files,omitempty"`
	Error           string   `json:"error,omitempty"`
}

func (t *GitStatusTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (agentsdk.ToolResult, error) {
	var in gitStatusInput
	if err := json.Unmarshal(input, &in); err != nil {
		return gitSyncError("Invalid input: " + err.Error())
	}
	repoDir, err := resolveLocalGitRepositoryWorkDir(workDir, in.RepoPath)
	if err != nil {
		return gitSyncError("repo_path rejected: " + err.Error())
	}

	runner := gitSyncRunner(t.Runner)
	statusOut, err := runner.RunGit(ctx, repoDir, "status", "--porcelain=v2", "--branch", "--no-renames")
	if err != nil {
		return gitSyncError(fmt.Sprintf("git status failed: %v\n%s", err, statusOut))
	}

	out := parseGitStatusV2(statusOut)
	out.Status = "ok"
	switch {
	case gitRefExists(ctx, runner, repoDir, "MERGE_HEAD"):
		out.Operation = "merge"
	case gitRefExists(ctx, runner, repoDir, "REBASE_HEAD"):
		out.Operation = "rebase"
	case gitRefExists(ctx, runner, repoDir, "CHERRY_PICK_HEAD"):
		out.Operation = "cherry-pick"
	}

	b, _ := json.Marshal(out)
	return agentsdk.ToolResult{Content: string(b)}, nil
}

// parseGitStatusV2 parses `git status --porcelain=v2 --branch --no-renames`.
func parseGitStatusV2(raw string) gitStatusOutput {
	out := gitStatusOutput{}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			out.Branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.upstream "):
			out.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
			fmt.Sscanf(strings.TrimPrefix(line, "# branch.ab "), "+%d -%d", &out.Ahead, &out.Behind)
		case strings.HasPrefix(line, "1 "):
			parts := strings.SplitN(line, " ", 9)
			if len(parts) != 9 {
				continue
			}
			xy, path := parts[1], parts[8]
			if len(xy) == 2 {
				if xy[0] != '.' {
					out.StagedFiles = append(out.StagedFiles, path)
				}
				if xy[1] != '.' {
					out.UnstagedFiles = append(out.UnstagedFiles, path)
				}
			}
		case strings.HasPrefix(line, "u "):
			parts := strings.SplitN(line, " ", 11)
			if len(parts) == 11 {
				out.ConflictedFiles = append(out.ConflictedFiles, parts[10])
			}
		case strings.HasPrefix(line, "? "):
			out.UntrackedFiles = append(out.UntrackedFiles, strings.TrimPrefix(line, "? "))
		}
	}
	out.Clean = len(out.StagedFiles) == 0 && len(out.UnstagedFiles) == 0 &&
		len(out.UntrackedFiles) == 0 && len(out.ConflictedFiles) == 0
	return out
}
