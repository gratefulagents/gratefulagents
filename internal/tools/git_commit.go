package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkgit "github.com/gratefulagents/sdk/pkg/agentsdk/tools/git"
)

const (
	requiredCoAuthorName    = "gratefulagents[bot]"
	requiredCoAuthorEmail   = "292420648+gratefulagents[bot]@users.noreply.github.com"
	requiredCoAuthorTrailer = "Co-authored-by: " + requiredCoAuthorName + " <" + requiredCoAuthorEmail + ">"

	gitCommandTimeout = 60 * time.Second
)

// RegisterGitCommitTool registers a commit tool that applies the mandatory
// GitHub App co-author credit policy.
func RegisterGitCommitTool(registry *Registry) {
	if registry == nil {
		return
	}
	registry.Register(NewGitCommitTool(nil))
}

// NewGitCommitTool constructs git_commit. The runner is injectable for tests.
func NewGitCommitTool(runner sdkgit.CommandRunner) *GitCommitTool {
	return &GitCommitTool{Runner: runner}
}

// GitCommitTool creates a commit with mandatory GitHub App co-author credit.
type GitCommitTool struct {
	Runner sdkgit.CommandRunner
}

func (t *GitCommitTool) Name() string { return "git_commit" }

func (t *GitCommitTool) Description() string {
	return "Create a git commit after optionally staging files. The gratefulagents GitHub App Co-authored-by trailer is added automatically if missing."
}

func (t *GitCommitTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["message"],
		"properties": {
			"message": {
				"type": "string",
				"description": "Commit message subject and optional body. The run's configured commit-attribution policy is applied automatically."
			},
			"repo_path": {
				"type": "string",
				"description": "Workspace-relative path to the git repository, for example repos/<alias>. Defaults to the workspace root."
			},
			"all": {
				"type": "boolean",
				"description": "Stage all changes with git add -A before committing. Defaults to false."
			},
			"paths": {
				"type": "array",
				"items": { "type": "string" },
				"description": "Specific repo-relative paths to stage before committing. Cannot be combined with all=true."
			},
			"no_verify": {
				"type": "boolean",
				"description": "Pass --no-verify to git commit. Defaults to false."
			}
		}
	}`)
}

func (t *GitCommitTool) IsReadOnly() bool { return false }
func (t *GitCommitTool) IsEnabled(ctx *agentsdk.RunContext) bool {
	return ctx == nil || ctx.ToolAccessLevel != agentsdk.ToolAccessLevelReadOnly
}
func (t *GitCommitTool) NeedsApproval() bool { return false }
func (t *GitCommitTool) TimeoutSeconds() int { return 0 }

type gitCommitInput struct {
	Message  string   `json:"message"`
	RepoPath string   `json:"repo_path"`
	All      bool     `json:"all"`
	Paths    []string `json:"paths"`
	NoVerify bool     `json:"no_verify"`
}

type gitCommitOutput struct {
	Status        string `json:"status"`
	CommitSHA     string `json:"commit_sha,omitempty"`
	CoAuthorAdded bool   `json:"co_author_added"`
	Error         string `json:"error,omitempty"`
}

func (t *GitCommitTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (agentsdk.ToolResult, error) {
	var in gitCommitInput
	if err := json.Unmarshal(input, &in); err != nil {
		return gitCommitError("Invalid input: " + err.Error())
	}
	message := strings.TrimSpace(in.Message)
	if message == "" {
		return gitCommitError("message is required")
	}
	if in.All && len(in.Paths) > 0 {
		return gitCommitError("all=true cannot be combined with paths")
	}

	repoDir, err := resolveLocalGitRepositoryWorkDir(workDir, in.RepoPath)
	if err != nil {
		return gitCommitError("repo_path rejected: " + err.Error())
	}

	runner := t.runner()
	if in.All {
		if out, err := runner.RunGit(ctx, repoDir, "add", "-A"); err != nil {
			return gitCommitError(fmt.Sprintf("git add failed: %v\n%s", err, out))
		}
	} else if len(in.Paths) > 0 {
		args := []string{"add", "--"}
		for _, p := range in.Paths {
			path := strings.TrimSpace(p)
			if err := validateGitCommitPath(path); err != nil {
				return gitCommitError(err.Error())
			}
			args = append(args, path)
		}
		if out, err := runner.RunGit(ctx, repoDir, args...); err != nil {
			return gitCommitError(fmt.Sprintf("git add failed: %v\n%s", err, out))
		}
	}

	commitMessage, added := ensureRequiredCoAuthor(message)
	args := []string{"commit"}
	if in.NoVerify {
		args = append(args, "--no-verify")
	}
	args = append(args, "-m", commitMessage)
	if out, err := runner.RunGit(ctx, repoDir, args...); err != nil {
		return gitCommitError(fmt.Sprintf("git commit failed: %v\n%s", err, out))
	}

	sha, err := runner.RunGit(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return gitCommitError("git rev-parse failed after commit: " + err.Error())
	}
	return gitCommitSuccess(gitCommitOutput{
		Status:        "committed",
		CommitSHA:     strings.TrimSpace(sha),
		CoAuthorAdded: added,
	})
}

func (t *GitCommitTool) runner() sdkgit.CommandRunner {
	if t.Runner != nil {
		return t.Runner
	}
	return localGitCommandRunner{}
}

type coAuthoringGitRunner struct {
	inner sdkgit.CommandRunner
}

func newCoAuthoringGitRunner(inner sdkgit.CommandRunner) sdkgit.CommandRunner {
	return coAuthoringGitRunner{inner: inner}
}

func (r coAuthoringGitRunner) RunGit(ctx context.Context, workDir string, args ...string) (string, error) {
	return r.runner().RunGit(ctx, workDir, addRequiredCoAuthorToCommitArgs(args)...)
}

func (r coAuthoringGitRunner) RunGH(ctx context.Context, workDir string, args ...string) (string, error) {
	return r.runner().RunGH(ctx, workDir, args...)
}

func (r coAuthoringGitRunner) runner() sdkgit.CommandRunner {
	if r.inner != nil {
		return r.inner
	}
	return localGitCommandRunner{}
}

type localGitCommandRunner struct{}

func (localGitCommandRunner) RunGit(ctx context.Context, workDir string, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "git", args...)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (localGitCommandRunner) RunGH(ctx context.Context, workDir string, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "gh", args...)
	cmd.Dir = workDir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String() + stderr.String(), err
	}
	return stdout.String(), nil
}

func addRequiredCoAuthorToCommitArgs(args []string) []string {
	if len(args) == 0 || args[0] != "commit" {
		return args
	}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "-m" || arg == "--message" {
			if i+1 < len(args) && messageHasRequiredCoAuthor(args[i+1]) {
				return args
			}
			i++
			continue
		}
		if strings.HasPrefix(arg, "--message=") && messageHasRequiredCoAuthor(strings.TrimPrefix(arg, "--message=")) {
			return args
		}
	}
	out := append([]string(nil), args...)
	return append(out, "-m", requiredCoAuthorTrailer)
}

func ensureRequiredCoAuthor(message string) (string, bool) {
	message = strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n"))
	message = strings.ReplaceAll(message, "\r", "\n")
	if messageHasRequiredCoAuthor(message) {
		return message, false
	}
	return message + "\n\n" + requiredCoAuthorTrailer, true
}

func messageHasRequiredCoAuthor(message string) bool {
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		if strings.EqualFold(line, requiredCoAuthorTrailer) {
			return true
		}
	}
	return false
}

func resolveLocalGitRepositoryWorkDir(workDir, repoPath string) (string, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	workspaceRoot, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	if evaluated, err := filepath.EvalSymlinks(workspaceRoot); err == nil {
		workspaceRoot = evaluated
	}

	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" || repoPath == "." {
		if !isLocalGitRepository(workspaceRoot) {
			return "", fmt.Errorf("workspace root is not a git repository")
		}
		return workspaceRoot, nil
	}
	if filepath.IsAbs(repoPath) {
		return "", fmt.Errorf("repo_path must be workspace-relative")
	}
	clean := filepath.Clean(repoPath)
	if clean == "." || clean == string(filepath.Separator) {
		return workspaceRoot, nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("repo_path resolves outside the workspace")
	}
	resolved := filepath.Join(workspaceRoot, clean)
	if evaluated, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = evaluated
	}
	rel, err := filepath.Rel(workspaceRoot, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("repo_path resolves outside the workspace")
	}
	if !isLocalGitRepository(resolved) {
		return "", fmt.Errorf("%s is not a git repository", repoPath)
	}
	return resolved, nil
}

func validateGitCommitPath(path string) error {
	if path == "" {
		return fmt.Errorf("paths must not contain empty entries")
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("paths must be repo-relative")
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("paths must not resolve outside the repository")
	}
	return nil
}

func isLocalGitRepository(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return info.IsDir() || info.Mode().IsRegular()
}

func gitCommitSuccess(out gitCommitOutput) (agentsdk.ToolResult, error) {
	b, _ := json.Marshal(out)
	return agentsdk.ToolResult{Content: string(b)}, nil
}

func gitCommitError(msg string) (agentsdk.ToolResult, error) {
	out := gitCommitOutput{Status: "error", Error: msg}
	b, _ := json.Marshal(out)
	return agentsdk.ToolResult{Content: string(b), IsError: true}, nil
}
