package dashboard

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	repoDir       = "/workspace/repo"
	workspaceRoot = "/workspace"
	// extraRepoStoreDir is where the SDK attach_repository tool (and spec
	// additionalRepos) clones extra repositories, nested inside the primary repo.
	extraRepoStoreDir   = repoDir + "/repos"
	maxDiffSize         = 1 << 20 // 1 MB
	maxNewFiles         = 1000
	maxReadFileLines    = 1000
	maxReadFileBytes    = 256 << 10 // 256 KB
	maxNewFileListBytes = 256 << 10
)

var execInPodFunc = execInPod

func isPodNotFoundExecError(err error) bool {
	if err == nil {
		return false
	}
	if k8serrors.IsNotFound(err) {
		return true
	}
	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, `pods "`) && strings.Contains(errMsg, "not found")
}

func isTransientPodStartupExecError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, "container not found") ||
		strings.Contains(errMsg, "podinitializing") ||
		strings.Contains(errMsg, "pod initializing") ||
		strings.Contains(errMsg, "containercreating") ||
		strings.Contains(errMsg, "container is not running")
}

// execInPod runs a command in a pod and returns stdout as a string.
func execInPod(ctx context.Context, clientset *kubernetes.Clientset, restConfig *rest.Config, podName, namespace string, command []string) (string, error) {
	req := clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: command,
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(restConfig, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("creating SPDY executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}); err != nil {
		return "", fmt.Errorf("exec into pod %s/%s: %w (stderr: %s)", namespace, podName, err, stderr.String())
	}

	return stdout.String(), nil
}

// workspaceRepoPathPattern matches the two places extra repositories can live:
// /workspace/<name> (dashboard CloneRepository) and /workspace/repo/repos/<name>
// (SDK attach_repository / spec additionalRepos). Single path segment, no
// leading '-' or '.', so the value can never be a git flag or a traversal.
var workspaceRepoPathPattern = regexp.MustCompile(`^/workspace(?:/repo/repos)?/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// resolveWorkspaceRepoPath validates a GetDiffRequest.repo_path and returns the
// repo directory to diff plus whether it is the run's primary repository.
// Empty input selects the primary repo.
func resolveWorkspaceRepoPath(repoPath string) (dir string, primary bool, err error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" || repoPath == repoDir {
		return repoDir, true, nil
	}
	if repoPath != filepath.Clean(repoPath) || !workspaceRepoPathPattern.MatchString(repoPath) {
		return "", false, fmt.Errorf("repo path %q is not a workspace repository", repoPath)
	}
	return repoPath, false, nil
}

// extraRepoDiffScript diffs an extra (non-primary) repository against the point
// where the agent's work diverged from its remote: the upstream of HEAD when
// set (dashboard-cloned repos keep the cloned branch as the work branch's
// upstream), else origin/HEAD (attach_repository and spec additionalRepos
// create a work branch with no upstream). Diffing against the merge-base keeps
// unrelated upstream drift out of the diff. With no usable remote ref it falls
// back to uncommitted changes only.
// dir must already be validated by resolveWorkspaceRepoPath.
func extraRepoDiffScript(dir string) string {
	return `d='` + dir + `'; ` +
		`ref=$(git -C "$d" rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null) || ref=; ` +
		`if [ -z "$ref" ]; then ref=$(git -C "$d" rev-parse --abbrev-ref origin/HEAD 2>/dev/null) || ref=; fi; ` +
		`base=; if [ -n "$ref" ]; then base=$(git -C "$d" merge-base HEAD "$ref" 2>/dev/null) || base=; fi; ` +
		`if [ -n "$base" ]; then git -C "$d" diff "$base"; else git -C "$d" diff HEAD; fi`
}

// execGetDiff runs a git diff in the sandbox pod for the repository at
// repoPath ("" = the primary repo at /workspace/repo).
// The primary repo diffs against origin/{baseBranch}; extra repositories
// resolve their base in-pod (see extraRepoDiffScript). Comparing against the
// working tree (not ..HEAD) includes tracked working-tree changes. Untracked
// files are listed separately so their contents are not loaded with the diff.
// Returns the unified diff text, truncated flag if over maxDiffSize.
// Returns empty diff (not error) when the ref is not yet available (e.g. during plan setup).
func execGetDiff(ctx context.Context, clientset *kubernetes.Clientset, restConfig *rest.Config, podName, namespace, baseBranch, repoPath string) (string, bool, error) {
	dir, primary, err := resolveWorkspaceRepoPath(repoPath)
	if err != nil {
		return "", false, err
	}
	command := []string{"git", "-C", dir, "diff", fmt.Sprintf("origin/%s", baseBranch)}
	if !primary {
		command = []string{"sh", "-c", extraRepoDiffScript(dir)}
	}
	out, err := execInPodFunc(ctx, clientset, restConfig, podName, namespace, command)
	if err != nil {
		// If git fails because the ref doesn't exist (repo not cloned yet, or
		// origin not fetched), return empty diff rather than propagating the error.
		// This avoids log spam during the plan/setup phase.
		errMsg := err.Error()
		if strings.Contains(errMsg, "unknown revision") || strings.Contains(errMsg, "exit code 128") {
			return "", false, nil
		}
		return "", false, err
	}

	if len(out) > maxDiffSize {
		return out[:maxDiffSize], true, nil
	}
	return out, false, nil
}

// execListNewFiles returns a bounded list of untracked, non-ignored files for
// one repository. It intentionally returns paths only; content is read later
// via execReadFile when a user selects a file.
func execListNewFiles(ctx context.Context, clientset *kubernetes.Clientset, restConfig *rest.Config, podName, namespace, repoPath string) ([]string, bool, error) {
	dir, _, err := resolveWorkspaceRepoPath(repoPath)
	if err != nil {
		return nil, false, err
	}

	// NUL delimiters preserve every valid Git path, including whitespace and
	// newlines. The in-pod byte cap prevents execInPod from buffering an
	// arbitrarily large untracked-file list.
	script := fmt.Sprintf(`git -C "$1" ls-files -z --others --exclude-standard | head -c %d`, maxNewFileListBytes+1)
	out, err := execInPodFunc(ctx, clientset, restConfig, podName, namespace,
		[]string{"sh", "-c", script, "sh", dir})
	if err != nil {
		return nil, false, err
	}

	byteTruncated := len(out) > maxNewFileListBytes
	if byteTruncated {
		out = out[:maxNewFileListBytes]
	}
	records := strings.Split(out, "\x00")
	if byteTruncated || (out != "" && !strings.HasSuffix(out, "\x00")) {
		// The final record may be partial when the byte cap was reached.
		records = records[:len(records)-1]
	}

	paths := make([]string, 0, min(len(records), maxNewFiles))
	for _, path := range records {
		if path == "" || !utf8.ValidString(path) {
			continue
		}
		if len(paths) == maxNewFiles {
			return paths, true, nil
		}
		paths = append(paths, path)
	}
	return paths, byteTruncated, nil
}

// execListFiles lists entries under path (relative to /workspace/repo) in the sandbox pod.
// Returns an error if path attempts directory traversal.
func execListFiles(ctx context.Context, clientset *kubernetes.Clientset, restConfig *rest.Config, podName, namespace, relativePath string) ([]*platform.FileEntry, error) {
	absPath, err := safeRepoPath(relativePath)
	if err != nil {
		return nil, err
	}

	// -1 forces one entry per line; --classify appends / to dirs; -s shows size in blocks.
	// We use find for machine-readable output instead.
	out, err := execInPodFunc(ctx, clientset, restConfig, podName, namespace,
		[]string{"find", absPath, "-maxdepth", "1", "-mindepth", "1", "-printf", "%f\\t%y\\t%s\\n"})
	if err != nil {
		return nil, err
	}

	var entries []*platform.FileEntry
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		name := parts[0]
		isDir := parts[1] == "d"
		size, _ := strconv.ParseInt(parts[2], 10, 64)
		entries = append(entries, &platform.FileEntry{
			Name:      name,
			IsDir:     isDir,
			SizeBytes: size,
		})
	}
	return entries, scanner.Err()
}

// pruneDirNames are directory names skipped when walking the workspace for the
// fuzzy file picker. These are heavy/noise directories that should never show up
// as selectable files (VCS metadata, dependency caches, build output).
var pruneDirNames = []string{
	".git",
	"node_modules",
	".next",
	"dist",
	"build",
	"out",
	"vendor",
	"target",
	".cache",
	".venv",
	"venv",
	"__pycache__",
	".gradle",
	".idea",
	".terraform",
	"coverage",
}

const defaultWorkspaceFileLimit = 20000

// execListWorkspaceFiles returns a flat, recursive list of file paths across the
// run's repositories for the composer "@" file picker. It scans the primary repo
// (/workspace/repo) plus any additional git repos cloned under /workspace. It uses
// a filesystem walk (find) rather than git so it works regardless of how many
// repositories live in the workspace, and prunes heavy directories (.git,
// node_modules, …) for speed.
//
// Paths for the primary repo are repo-relative (e.g. "src/main.go"); paths for
// additional repos are absolute (e.g. "/workspace/widgets/src/index.ts") so the
// inserted mention is unambiguous regardless of the agent's working directory.
// Results are capped at limit; the returned bool reports whether more files were
// available.
func execListWorkspaceFiles(ctx context.Context, clientset *kubernetes.Clientset, restConfig *rest.Config, podName, namespace string, limit int) ([]string, bool, error) {
	if limit <= 0 {
		limit = defaultWorkspaceFileLimit
	}

	out, err := execInPodFunc(ctx, clientset, restConfig, podName, namespace, []string{"sh", "-c", workspaceListScript()})
	if err != nil {
		return nil, false, err
	}

	var paths []string
	truncated := false
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if len(paths) >= limit {
			truncated = true
			break
		}
		paths = append(paths, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	return paths, truncated, nil
}

// workspaceListScript builds the shell script that lists files across the run's
// repositories. It always scans the primary repo (even when repoless, so files
// the agent created there still show up) plus any git repos cloned elsewhere
// under /workspace, pruning heavy directories. The primary repo's files are
// printed repo-relative; additional repos' files are printed as absolute paths.
func workspaceListScript() string {
	clauses := make([]string, 0, len(pruneDirNames))
	for _, name := range pruneDirNames {
		clauses = append(clauses, "-name "+name)
	}
	pruneExpr := `\( -type d \( ` + strings.Join(clauses, " -o ") + ` \) -prune \) -o -type f`

	return `repo=` + repoDir + `; roots=$repo; ` +
		`for g in $(find ` + workspaceRoot + ` -maxdepth 2 -name .git -type d -prune 2>/dev/null); do ` +
		`d=$(dirname "$g"); [ "$d" = "$repo" ] && continue; roots="$roots $d"; done; ` +
		`for d in $roots; do [ -d "$d" ] || continue; ` +
		`if [ "$d" = "$repo" ]; then p=; else p="$d/"; fi; ` +
		`find "$d" ` + pruneExpr + ` -printf "$p%P\n" 2>/dev/null; done`
}

// execCloneRepo clones repoURL into /workspace/<destName> inside the sandbox pod
// using the pod's existing git credentials, then checks the clone out to the
// same branch the run's primary repository is on so every repo in the workspace
// works on one branch. destName must already be validated by the caller (no
// path separators). repoURL/baseBranch are passed as argv elements (no shell)
// so they cannot be interpreted as shell metacharacters.
func execCloneRepo(ctx context.Context, clientset *kubernetes.Clientset, restConfig *rest.Config, podName, namespace, repoURL, baseBranch, destName string) (*platform.RepositoryInfo, error) {
	dest := workspaceRoot + "/" + destName
	args := []string{"git", "clone"}
	if baseBranch != "" {
		args = append(args, "--branch", baseBranch)
	}
	args = append(args, repoURL, dest)

	if _, err := execInPodFunc(ctx, clientset, restConfig, podName, namespace, args); err != nil {
		return nil, err
	}

	// Align the fresh clone with the primary repository's working branch.
	// Best-effort: the clone stays on its cloned branch when the primary
	// branch cannot be read (e.g. repoless runs) or the checkout fails.
	branch := baseBranch
	if out, err := execInPodFunc(ctx, clientset, restConfig, podName, namespace,
		[]string{"sh", "-c", alignCloneBranchScript(dest)}); err == nil {
		if b := strings.TrimSpace(out); b != "" && b != "HEAD" {
			branch = b
		}
	}
	return &platform.RepositoryInfo{
		Name:      destName,
		Path:      dest,
		RemoteUrl: repoURL,
		Branch:    branch,
		IsPrimary: false,
	}, nil
}

// alignCloneBranchScript checks a freshly cloned extra repository out to a
// work branch named after the primary repository's current branch. When a
// previous pod of this run already pushed that branch to the clone's origin
// (a resumed run), the work branch resumes from origin/<branch> instead of
// being recreated at the cloned base, so earlier run commits are kept. The
// checkout never tracks the work branch itself (--no-track); the cloned
// branch becomes the work branch's upstream so diffs still compare against
// the branch that was cloned (see extraRepoDiffScript). It prints the
// repository's final branch. Every step is best-effort: on failure the clone
// simply stays on its cloned branch. dir is a validated workspace path (see
// deriveRepoDirName) so it is safe to embed.
func alignCloneBranchScript(dir string) string {
	return `d='` + dir + `'; ` +
		`wb=$(git -C ` + repoDir + ` rev-parse --abbrev-ref HEAD 2>/dev/null) || wb=; ` +
		`cb=$(git -C "$d" rev-parse --abbrev-ref HEAD 2>/dev/null) || cb=; ` +
		`if [ -n "$wb" ] && [ "$wb" != HEAD ] && [ "$wb" != "$cb" ]; then ` +
		`start=; if git -C "$d" fetch -q origin "refs/heads/$wb:refs/remotes/origin/$wb" 2>/dev/null; then start="origin/$wb"; fi; ` +
		`if git -C "$d" checkout -q --no-track -B "$wb" $start 2>/dev/null; then ` +
		`if [ -n "$cb" ] && [ "$cb" != HEAD ]; then git -C "$d" branch -q --set-upstream-to "origin/$cb" 2>/dev/null || true; fi; ` +
		`fi; fi; ` +
		`git -C "$d" rev-parse --abbrev-ref HEAD 2>/dev/null || true`
}

// repoListScript walks the workspace for git working copies and prints one
// tab-separated "path\tremote\tbranch" line per repository. It scans both
// /workspace/<name> (dashboard clones) and the SDK attach_repository store at
// /workspace/repo/repos/<alias>.
const repoListScript = `{ find /workspace -maxdepth 2 -name .git -type d 2>/dev/null; ` +
	`find /workspace/repo/repos -mindepth 2 -maxdepth 2 -name .git -type d 2>/dev/null; } | sort -u | while read g; do ` +
	`d=$(dirname "$g"); ` +
	`u=$(git -C "$d" remote get-url origin 2>/dev/null || true); ` +
	`b=$(git -C "$d" rev-parse --abbrev-ref HEAD 2>/dev/null || true); ` +
	`printf '%s\t%s\t%s\n' "$d" "$u" "$b"; done`

// execListRepos lists git repositories under /workspace in the sandbox pod,
// reporting each repo's path, origin remote, and current branch. The original
// repo (/workspace/repo) is marked primary and sorted first.
func execListRepos(ctx context.Context, clientset *kubernetes.Clientset, restConfig *rest.Config, podName, namespace string) ([]*platform.RepositoryInfo, error) {
	out, err := execInPodFunc(ctx, clientset, restConfig, podName, namespace, []string{"sh", "-c", repoListScript})
	if err != nil {
		return nil, err
	}

	var repos []*platform.RepositoryInfo
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		path := strings.TrimSpace(parts[0])
		if path == "" {
			continue
		}
		repo := &platform.RepositoryInfo{
			Name:      filepath.Base(path),
			Path:      path,
			IsPrimary: path == repoDir,
		}
		if len(parts) > 1 {
			repo.RemoteUrl = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			repo.Branch = strings.TrimSpace(parts[2])
		}
		repos = append(repos, repo)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(repos, func(i, j int) bool {
		if repos[i].IsPrimary != repos[j].IsPrimary {
			return repos[i].IsPrimary
		}
		return repos[i].Name < repos[j].Name
	})
	return repos, nil
}

// execReadFile reads up to maxLines lines from a file in the selected repository.
// Returns the content and a truncation flag.
func execReadFile(ctx context.Context, clientset *kubernetes.Clientset, restConfig *rest.Config, podName, namespace, repoPath, relativePath string, maxLines int) (string, bool, error) {
	dir, _, err := resolveWorkspaceRepoPath(repoPath)
	if err != nil {
		return "", false, err
	}
	absPath, err := safePathWithin(dir, relativePath)
	if err != nil {
		return "", false, err
	}

	if maxLines <= 0 || maxLines > maxReadFileLines {
		maxLines = maxReadFileLines
	}

	// Require the selected directory itself (not a symlink target elsewhere) to
	// be the Git worktree root, then resolve the file and keep it beneath that
	// canonical root. Cap both lines and bytes before stdout is buffered.
	script := fmt.Sprintf(`test ! -L "$1" || exit 66; root=$(realpath -e "$1") || exit 66; top=$(git -C "$1" rev-parse --show-toplevel) || exit 66; test "$top" = "$1" || exit 66; target=$(realpath -e "$2") || exit 66; case "$target" in "$root"/*) ;; *) exit 66 ;; esac; test -f "$target" || exit 66; tmp=$(mktemp) || exit 66; trap 'rm -f "$tmp"' EXIT; head -c %d "$target" >"$tmp" || exit 66; head -n %d "$tmp"`, maxReadFileBytes+1, maxLines+1)
	out, err := execInPodFunc(ctx, clientset, restConfig, podName, namespace,
		[]string{"sh", "-c", script, "sh", dir, absPath})
	if err != nil {
		return "", false, err
	}

	byteTruncated := len(out) > maxReadFileBytes
	if byteTruncated {
		out = out[:maxReadFileBytes]
		// A byte cap can split only the final UTF-8 rune. Try at most the
		// maximum three incomplete trailing bytes; invalid data elsewhere is
		// rejected below instead of scanned repeatedly.
		for removed := 0; removed < utf8.UTFMax-1 && !utf8.ValidString(out); removed++ {
			out = out[:len(out)-1]
		}
	}
	if !utf8.ValidString(out) {
		return "", false, fmt.Errorf("file is not valid UTF-8 text")
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return "", byteTruncated, nil
	}
	lines := strings.Split(out, "\n")
	lineTruncated := len(lines) > maxLines
	if lineTruncated {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n"), byteTruncated || lineTruncated, nil
}

// safeRepoPath validates relativePath and returns the absolute path within /workspace/repo.
func safeRepoPath(relativePath string) (string, error) {
	return safePathWithin(repoDir, relativePath)
}

// safePathWithin validates a relative path and resolves it below root.
func safePathWithin(root, relativePath string) (string, error) {
	if filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("path %q must be relative", relativePath)
	}
	clean := filepath.Clean(filepath.Join(root, relativePath))
	if clean != root && !strings.HasPrefix(clean, root+"/") {
		return "", fmt.Errorf("path %q escapes workspace", relativePath)
	}
	return clean, nil
}
