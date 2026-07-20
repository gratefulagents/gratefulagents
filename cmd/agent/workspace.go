package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/agentinfra"
)

const workspaceScratchDir = "/workspace/scratch"

// setupWorkspace clones the repo and builds the task context.
func setupWorkspace(cfg *runConfig) error {
	log.Println("Setting up workspace...")

	if err := agentinfra.SetupGHAuth(cfg.GithubToken); err != nil {
		return fmt.Errorf("setting up gh auth: %w", err)
	}

	if cfg.Repoless {
		return setupRepolessWorkspace(cfg)
	}

	log.Printf("Cloning %s...", cfg.RepoURL)
	if err := agentinfra.GitExec("", "clone", "--branch", cfg.BaseBranch, cfg.RepoURL, cfg.RepoDir); err != nil {
		return fmt.Errorf("cloning repo: %w", err)
	}

	// Create or resume the working branch named after the AgentRun so PRs have a clean source branch.
	branchName := cfg.TaskName
	remoteExists := remoteBranchExists(cfg.RepoDir, branchName)
	if remoteExists {
		log.Printf("Checking out existing remote branch %s...", branchName)
	} else {
		log.Printf("Checking out new branch %s...", branchName)
	}
	for _, command := range checkoutBranchCommands(branchName, remoteExists) {
		if err := agentinfra.GitExec(cfg.RepoDir, command.args...); err != nil {
			return fmt.Errorf("%s branch %s: %w", command.action, branchName, err)
		}
	}

	// Re-apply any tracked WIP and local commits from the last object-storage checkpoint.
	if err := restorePrimaryWorkspaceCheckpoint(*cfg, remoteExists); err != nil {
		return fmt.Errorf("restoring workspace checkpoint: %w", err)
	}

	sessionModeNotes := "Session mode: interactive chat"
	if cfg.AutoMode {
		sessionModeNotes = "Session mode: autonomous"
	}

	parentRefJSON := fmt.Sprintf(`{"namespace":"%s","name":"%s"}`, cfg.Namespace, cfg.TaskName)

	mode := "chat"
	if cfg.AutoMode {
		mode = "auto"
	}
	cfg.TaskContext = fmt.Sprintf(`## Environment
- Base branch: %s
- Working branch: %s
- %s
- Workflow mode: %s
- Parent AgentRun: %s
- Working directory: %s%s
- Ephemeral scratch directory: %s
- %s

## Workspace Rules
All tools (bash, file read/write/edit, grep, glob) execute inside the workspace at %s.
Keep project work inside this directory and use relative file paths. The sole exception is
%s, a separately mounted, writable, non-checkpointed directory for large disposable
toolchains, dependency caches, downloaded archives, and build outputs. Its contents are
cleared when the pod is recreated. Never place source code or required deliverables there.

Repositories: the workspace holds a list of repositories, all equal peers.
The repository this run was launched with is checked out at the workspace
root; every other repository lives under repos/<alias>. Every git/GitHub
tool (git_commit, git_push, git_status, git_pull, git_merge, git_merge_abort,
create_pull_request, update_pull_request, create_github_issue,
get_github_issue, update_github_issue, close_github_issue,
update_github_issue_labels, add_github_issue_comment,
get_pull_request, get_pull_request_checks, list_review_threads,
submit_pull_request_review, reply_to_review_thread, resolve_review_thread,
request_re_review) accepts
repo_path to select the repository it acts on; repo_path defaults to the
workspace root. Use attach_repository to add another repository to the list.

You are on branch %q at the workspace root. Push to this branch when creating PRs.
Do NOT use the gh CLI, and do not push, commit, pull, or merge through raw git
in bash for GitHub work — use the built-in git/GitHub tools above. They enforce
the configured commit-attribution policy and prevent pushes to
main/master/default branches.
If your PR branch is behind origin, use git_pull; if it has merge conflicts
with its base branch, use git_merge with the base branch, resolve the conflict
markers in the reported files, then git_commit and git_push (git_merge_abort
abandons a conflicted merge; git_status shows sync/conflict state).%s`,
		cfg.BaseBranch, branchName, sessionModeNotes, mode, parentRefJSON, cfg.RepoDir,
		additionalRepoContextLine(cfg.AdditionalRepoURLs), workspaceScratchDir, parallelToolCallingOneLiner,
		cfg.RepoDir, workspaceScratchDir, branchName, kubernetesAdminPromptSection(cfg.KubernetesAdmin))

	log.Println("Setup complete.")
	return nil
}

func remoteBranchExists(repoDir, branchName string) bool {
	branchName = strings.TrimSpace(branchName)
	if branchName == "" {
		return false
	}
	cmd := exec.Command("git", "ls-remote", "--exit-code", "--heads", "origin", branchName)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run() == nil
}

func checkoutExistingRemoteBranchArgs(branchName string) []string {
	return []string{"checkout", "--track", "-b", branchName, "origin/" + branchName}
}

type workspaceGitCommand struct {
	action string
	args   []string
}

func checkoutBranchCommands(branchName string, remoteExists bool) []workspaceGitCommand {
	if remoteExists {
		return []workspaceGitCommand{
			{action: "fetching", args: []string{"fetch", "origin", "refs/heads/" + branchName + ":refs/remotes/origin/" + branchName}},
			{action: "checking out", args: checkoutExistingRemoteBranchArgs(branchName)},
			{action: "pulling", args: []string{"pull", "--ff-only", "origin", branchName}},
		}
	}
	return []workspaceGitCommand{
		{action: "creating", args: []string{"checkout", "-b", branchName}},
	}
}

// setupRepolessWorkspace prepares an empty sandbox for a plain chat that has no
// repository attached. File/shell/code tools still operate inside a real working
// directory; there is just nothing cloned and no working branch.
func setupRepolessWorkspace(cfg *runConfig) error {
	log.Println("No repository attached — preparing empty chat sandbox...")
	if err := os.MkdirAll(cfg.RepoDir, 0o755); err != nil {
		return fmt.Errorf("creating empty workspace %s: %w", cfg.RepoDir, err)
	}

	sessionModeNotes := "Session mode: interactive chat"
	if cfg.AutoMode {
		sessionModeNotes = "Session mode: autonomous"
	}

	parentRefJSON := fmt.Sprintf(`{"namespace":"%s","name":"%s"}`, cfg.Namespace, cfg.TaskName)

	mode := "chat"
	if cfg.AutoMode {
		mode = "auto"
	}
	cfg.TaskContext = fmt.Sprintf(`## Environment
- No repository attached (plain chat)
- %s
- Workflow mode: %s
- Parent AgentRun: %s
- Working directory: %s%s
- Ephemeral scratch directory: %s
- %s

## Workspace Rules
This is a plain chat with no repository. You have an empty project workspace at %s
where all tools (bash, file read/write/edit, grep, glob) execute. Keep project files
there. Use %s only for large disposable toolchains, dependency caches, downloaded
archives, and build outputs; it is a separately mounted writable directory that is
not checkpointed and is cleared when the pod is recreated. Never place required
deliverables there. There is no checked-out git repository and no working branch,
so do not attempt to push branches or open pull requests unless the user first
attaches a repository.
If the user asks you to work in a repository or gives you a GitHub repo URL/name,
use attach_repository. Attached repositories are cloned under repos/<alias> and
form the workspace repository list; use that path with file tools and pass
repo_path="repos/<alias>" to any git/GitHub tool (git_commit, git_push,
git_status, git_pull, git_merge, create_pull_request, update_pull_request,
create_github_issue, get_github_issue, update_github_issue, close_github_issue,
update_github_issue_labels, add_github_issue_comment, get_pull_request, and the
PR review tools) to select the repository it acts
on. Do NOT use the gh CLI — the built-in tools enforce the platform guardrails.%s`,
		sessionModeNotes, mode, parentRefJSON, cfg.RepoDir,
		additionalRepoContextLine(cfg.AdditionalRepoURLs), workspaceScratchDir, parallelToolCallingOneLiner,
		cfg.RepoDir, workspaceScratchDir, kubernetesAdminPromptSection(cfg.KubernetesAdmin))

	log.Println("Setup complete.")
	return nil
}

func kubernetesAdminPromptSection(enabled bool) string {
	if !enabled {
		return ""
	}
	return `

## Kubernetes Admin Access
This run is Kubernetes-admin enabled. You have read-only platform introspection
tools (` + "`platform_list_runs`, `platform_get_run`, `platform_run_activity`, `platform_run_trace`, `platform_list_pods`, `platform_pod_logs`" + `) and
the bash sandbox has KUBECONFIG set to this worker pod's service-account
kubeconfig. Use these privileges only for diagnostics and dogfooding; avoid
mutating cluster state unless the user explicitly asks.`
}

// additionalRepoContextLine renders the Environment bullet describing the
// run's pre-attached extra repositories; empty when the run has none.
func additionalRepoContextLine(urls []string) string {
	entries := make([]string, 0, len(urls))
	for _, repoURL := range urls {
		name, err := agentinfra.DeriveRepoDirName(repoURL)
		if err != nil {
			continue
		}
		entries = append(entries, fmt.Sprintf("repos/%s (%s)", name, repoURL))
	}
	if len(entries) == 0 {
		return ""
	}
	return "\n- Repository list (pre-cloned alongside the root checkout): " + strings.Join(entries, ", ")
}

// cloneAdditionalRepos clones the run's spec-declared extra repositories into
// the attach_repository store (repos/<name> under the primary repo dir) and
// checks each out to the run's working branch so every repository works on
// the same branch as the primary repo. It must run after restoreExtraRepos so
// snapshot-restored clones are kept: existing directories are skipped.
func cloneAdditionalRepos(cfg *runConfig) error {
	if len(cfg.AdditionalRepoURLs) == 0 {
		return nil
	}
	storeDir := filepath.Join(cfg.RepoDir, extraRepoStoreDirName)
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return fmt.Errorf("creating repo store %s: %w", storeDir, err)
	}
	for _, repoURL := range cfg.AdditionalRepoURLs {
		name, err := agentinfra.DeriveRepoDirName(repoURL)
		if err != nil {
			return fmt.Errorf("additional repo %q: %w", repoURL, err)
		}
		dest := filepath.Join(storeDir, name)
		exists, err := validateOrRemoveExtraRepoDest(context.Background(), dest, repoURL)
		if err != nil {
			return fmt.Errorf("additional repo %s: %w", name, err)
		}
		if exists {
			if err := checkoutWorkBranch(dest, cfg.TaskName); err != nil {
				return fmt.Errorf("additional repo %s: %w", name, err)
			}
			continue
		}
		log.Printf("Cloning additional repo %s into repos/%s...", repoURL, name)
		if err := agentinfra.GitExec("", "clone", repoURL, dest); err != nil {
			return fmt.Errorf("cloning additional repo %s: %w", repoURL, err)
		}
		if err := checkoutWorkBranch(dest, cfg.TaskName); err != nil {
			return fmt.Errorf("additional repo %s: %w", name, err)
		}
	}
	if !cfg.Repoless {
		// Keep the store out of the primary repository's status output,
		// mirroring the SDK's attach_repository behavior.
		ensureExtraRepoExclude(cfg.RepoDir)
	}
	return nil
}

// checkoutWorkBranch checks out the run's working branch in an extra
// repository so it matches the primary repo, resuming the remote branch when
// a previous pod of this run already pushed it. No-op without a branch name.
func checkoutWorkBranch(repoDir, branchName string) error {
	branchName = strings.TrimSpace(branchName)
	if branchName == "" {
		return nil
	}
	if current, err := gitOutput(context.Background(), repoDir, nil, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && current == branchName {
		return nil
	}
	remoteExists := remoteBranchExists(repoDir, branchName)
	if remoteExists {
		log.Printf("Checking out existing remote branch %s in %s...", branchName, repoDir)
	} else {
		log.Printf("Checking out new branch %s in %s...", branchName, repoDir)
	}
	for _, command := range checkoutWorkBranchCommands(branchName, remoteExists) {
		if err := agentinfra.GitExec(repoDir, command.args...); err != nil {
			return fmt.Errorf("%s branch %s: %w", command.action, branchName, err)
		}
	}
	return nil
}

// checkoutWorkBranchCommands returns the git commands that put an extra
// repository on the run's working branch. Unlike the primary repo's
// checkoutBranchCommands, resuming an existing remote branch must not make it
// the local branch's upstream: extra repositories are diffed against
// @{upstream} (falling back to origin/HEAD, see the dashboard's
// extraRepoDiffScript), so tracking the run branch itself would make every
// run diff empty.
func checkoutWorkBranchCommands(branchName string, remoteExists bool) []workspaceGitCommand {
	if remoteExists {
		return []workspaceGitCommand{
			{action: "fetching", args: []string{"fetch", "origin", "refs/heads/" + branchName + ":refs/remotes/origin/" + branchName}},
			{action: "checking out", args: []string{"checkout", "--no-track", "-b", branchName, "origin/" + branchName}},
		}
	}
	return []workspaceGitCommand{
		{action: "creating", args: []string{"checkout", "-b", branchName}},
	}
}
