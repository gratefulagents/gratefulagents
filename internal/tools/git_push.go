package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkgit "github.com/gratefulagents/sdk/pkg/agentsdk/tools/git"
)

// RegisterGitPushTool registers a push tool that refuses to push protected
// branches (main/master/remote default).
func RegisterGitPushTool(registry *Registry) {
	if registry == nil {
		return
	}
	registry.Register(NewGitPushTool(nil))
}

// NewGitPushTool constructs git_push. The runner is injectable for tests.
func NewGitPushTool(runner sdkgit.CommandRunner) *GitPushTool {
	return &GitPushTool{Runner: runner}
}

// GitPushTool pushes the current branch of a workspace repository to origin.
type GitPushTool struct {
	Runner sdkgit.CommandRunner
}

func (t *GitPushTool) Name() string { return "git_push" }

func (t *GitPushTool) Description() string {
	return "Push the current branch of a workspace repository to origin. Refuses to push protected branches (main/master/default). Use repo_path for attached repositories under repos/<alias>."
}

func (t *GitPushTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"repo_path": {
				"type": "string",
				"description": "Workspace-relative path to the git repository, for example repos/<alias>. Defaults to the workspace root."
			}
		}
	}`)
}

func (t *GitPushTool) IsReadOnly() bool      { return false }
func (t *GitPushTool) WritesGitRemote() bool { return true }
func (t *GitPushTool) IsEnabled(ctx *agentsdk.RunContext) bool {
	return ctx == nil || ctx.ToolAccessLevel != agentsdk.ToolAccessLevelReadOnly
}
func (t *GitPushTool) NeedsApproval() bool { return false }
func (t *GitPushTool) TimeoutSeconds() int { return 0 }

type gitPushInput struct {
	RepoPath string `json:"repo_path"`
}

type gitPushOutput struct {
	Status string `json:"status"`
	Branch string `json:"branch,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (t *GitPushTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (agentsdk.ToolResult, error) {
	var in gitPushInput
	if err := json.Unmarshal(input, &in); err != nil {
		return gitPushError("Invalid input: " + err.Error())
	}

	repoDir, err := resolveLocalGitRepositoryWorkDir(workDir, in.RepoPath)
	if err != nil {
		return gitPushError("repo_path rejected: " + err.Error())
	}

	runner := t.runner()
	branch, err := pushableBranch(ctx, runner, repoDir)
	if err != nil {
		return gitPushError(err.Error())
	}

	if out, err := runner.RunGit(ctx, repoDir, "push", "--no-verify", "-u", "origin", "HEAD"); err != nil {
		return gitPushError(fmt.Sprintf("git push failed: %v\n%s", err, out))
	}
	return gitPushSuccess(gitPushOutput{Status: "pushed", Branch: branch})
}

func (t *GitPushTool) runner() sdkgit.CommandRunner {
	if t.Runner != nil {
		return t.Runner
	}
	return localGitCommandRunner{}
}

// pushableBranch returns the current branch in repoDir, or an error if the
// branch is protected (main/master/remote default), detached, or cannot be
// determined.
func pushableBranch(ctx context.Context, runner sdkgit.CommandRunner, repoDir string) (string, error) {
	out, err := runner.RunGit(ctx, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("cannot determine current branch: %v\n%s", err, out)
	}
	branch := strings.TrimSpace(out)
	if branch == "" {
		return "", fmt.Errorf("cannot determine current branch")
	}
	if branch == "HEAD" {
		return "", fmt.Errorf("refusing to push: HEAD is detached")
	}
	if branch == "main" || branch == "master" {
		return "", fmt.Errorf("refusing to push protected branch %q", branch)
	}
	if defOut, err := runner.RunGit(ctx, repoDir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		defaultBranch := strings.TrimPrefix(strings.TrimSpace(defOut), "origin/")
		if defaultBranch != "" && branch == defaultBranch {
			return "", fmt.Errorf("refusing to push default branch %q", branch)
		}
	}
	return branch, nil
}

// branchGuardedGitRunner wraps a git runner and blocks push commands when the
// current branch in the target workDir is protected, detached, or unknown.
type branchGuardedGitRunner struct {
	inner sdkgit.CommandRunner
}

func newBranchGuardedGitRunner(inner sdkgit.CommandRunner) sdkgit.CommandRunner {
	return branchGuardedGitRunner{inner: inner}
}

func (r branchGuardedGitRunner) RunGit(ctx context.Context, workDir string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "push" {
		if _, err := pushableBranch(ctx, r.runner(), workDir); err != nil {
			return "", err
		}
	}
	return r.runner().RunGit(ctx, workDir, args...)
}

func (r branchGuardedGitRunner) RunGH(ctx context.Context, workDir string, args ...string) (string, error) {
	return r.runner().RunGH(ctx, workDir, args...)
}

func (r branchGuardedGitRunner) runner() sdkgit.CommandRunner {
	if r.inner != nil {
		return r.inner
	}
	return localGitCommandRunner{}
}

func gitPushSuccess(out gitPushOutput) (agentsdk.ToolResult, error) {
	b, _ := json.Marshal(out)
	return agentsdk.ToolResult{Content: string(b)}, nil
}

func gitPushError(msg string) (agentsdk.ToolResult, error) {
	out := gitPushOutput{Status: "error", Error: msg}
	b, _ := json.Marshal(out)
	return agentsdk.ToolResult{Content: string(b), IsError: true}, nil
}
