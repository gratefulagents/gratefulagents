package dashboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestSafeRepoPath(t *testing.T) {
	t.Run("accepts repo relative paths", func(t *testing.T) {
		path, err := safeRepoPath("dir/file.txt")
		if err != nil {
			t.Fatalf("safeRepoPath() error = %v", err)
		}
		if path != repoDir+"/dir/file.txt" {
			t.Fatalf("safeRepoPath() = %q, want %q", path, repoDir+"/dir/file.txt")
		}
	})

	t.Run("rejects traversal outside repo", func(t *testing.T) {
		if _, err := safeRepoPath("../../etc/passwd"); err == nil {
			t.Fatal("expected safeRepoPath() to reject traversal")
		}
	})
}

func TestIsTransientPodStartupExecError(t *testing.T) {
	err := errors.New(`exec into pod gratefulagents-system/run-chat: unable to upgrade connection: container not found ("worker") (stderr: )`)
	if !isTransientPodStartupExecError(err) {
		t.Fatal("expected container-not-found exec failure to be classified as transient startup")
	}
	if isTransientPodStartupExecError(errors.New("permission denied")) {
		t.Fatal("expected unrelated exec failure to remain warnable")
	}
}

func TestExecGetDiffTruncatesLargeOutput(t *testing.T) {
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, podName, namespace string, command []string) (string, error) {
		if podName != "sandbox-1" || namespace != "default" {
			t.Fatalf("execInPodFunc pod/namespace = %s/%s", namespace, podName)
		}
		want := []string{"git", "-C", repoDir, "diff", "origin/main"}
		if len(command) != len(want) {
			t.Fatalf("execInPodFunc command len = %d, want %d (%#v)", len(command), len(want), command)
		}
		for i := range want {
			if command[i] != want[i] {
				t.Fatalf("execInPodFunc command[%d] = %q, want %q (%#v)", i, command[i], want[i], command)
			}
		}
		return strings.Repeat("x", maxDiffSize+128), nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	diff, truncated, err := execGetDiff(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", "main", "")
	if err != nil {
		t.Fatalf("execGetDiff() error = %v", err)
	}
	if !truncated {
		t.Fatal("expected execGetDiff() to truncate oversized output")
	}
	if len(diff) != maxDiffSize {
		t.Fatalf("len(diff) = %d, want %d", len(diff), maxDiffSize)
	}
}

func TestResolveWorkspaceRepoPath(t *testing.T) {
	t.Run("empty selects the primary repo", func(t *testing.T) {
		dir, primary, err := resolveWorkspaceRepoPath("")
		if err != nil || !primary || dir != repoDir {
			t.Fatalf("resolveWorkspaceRepoPath(\"\") = (%q, %v, %v), want (%q, true, nil)", dir, primary, err, repoDir)
		}
	})

	t.Run("explicit primary path selects the primary repo", func(t *testing.T) {
		dir, primary, err := resolveWorkspaceRepoPath(repoDir)
		if err != nil || !primary || dir != repoDir {
			t.Fatalf("resolveWorkspaceRepoPath(repoDir) = (%q, %v, %v), want (%q, true, nil)", dir, primary, err, repoDir)
		}
	})

	t.Run("accepts dashboard-cloned repos", func(t *testing.T) {
		dir, primary, err := resolveWorkspaceRepoPath("/workspace/widgets")
		if err != nil || primary || dir != "/workspace/widgets" {
			t.Fatalf("resolveWorkspaceRepoPath() = (%q, %v, %v)", dir, primary, err)
		}
	})

	t.Run("accepts attach_repository store repos", func(t *testing.T) {
		dir, primary, err := resolveWorkspaceRepoPath(extraRepoStoreDir + "/helm-charts")
		if err != nil || primary || dir != extraRepoStoreDir+"/helm-charts" {
			t.Fatalf("resolveWorkspaceRepoPath() = (%q, %v, %v)", dir, primary, err)
		}
	})

	t.Run("rejects traversal, flags, and non-workspace paths", func(t *testing.T) {
		for _, bad := range []string{
			"/workspace/../etc",
			"/workspace/repo/repos/../../secrets",
			"/workspace/-flag",
			"/workspace/.hidden",
			"/workspace/a/b",
			"/etc/passwd",
			"relative/path",
			"/workspace/widgets/",
		} {
			if _, _, err := resolveWorkspaceRepoPath(bad); err == nil {
				t.Fatalf("resolveWorkspaceRepoPath(%q) succeeded, want error", bad)
			}
		}
	})
}

func TestExecGetDiffUsesExtraRepoScriptForNonPrimaryRepos(t *testing.T) {
	origExec := execInPodFunc
	var gotCommand []string
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _, _ string, command []string) (string, error) {
		gotCommand = command
		return "diff --git a/x b/x", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	diff, truncated, err := execGetDiff(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", "main", extraRepoStoreDir+"/widgets")
	if err != nil {
		t.Fatalf("execGetDiff() error = %v", err)
	}
	if truncated || diff == "" {
		t.Fatalf("execGetDiff() = (%q, %v)", diff, truncated)
	}
	if len(gotCommand) != 3 || gotCommand[0] != "sh" || gotCommand[1] != "-c" {
		t.Fatalf("command = %#v, want sh -c script", gotCommand)
	}
	script := gotCommand[2]
	for _, want := range []string{extraRepoStoreDir + "/widgets", "@{upstream}", "origin/HEAD", "merge-base", "git -C \"$d\" diff HEAD"} {
		if !strings.Contains(script, want) {
			t.Fatalf("extra repo diff script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "origin/main") {
		t.Fatalf("extra repo diff must not use the primary repo's base branch:\n%s", script)
	}
}

func TestExecListNewFilesReturnsPathsWithoutReadingContent(t *testing.T) {
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _, _ string, command []string) (string, error) {
		if len(command) != 5 || command[0] != "sh" || command[1] != "-c" || command[4] != repoDir {
			t.Fatalf("command = %#v", command)
		}
		for _, want := range []string{"ls-files -z --others --exclude-standard", fmt.Sprintf("head -c %d", maxNewFileListBytes+1)} {
			if !strings.Contains(command[2], want) {
				t.Fatalf("script missing %q: %s", want, command[2])
			}
		}
		return "docs/new.md\x00src/new.go\x00", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	files, truncated, err := execListNewFiles(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", "")
	if err != nil {
		t.Fatalf("execListNewFiles() error = %v", err)
	}
	if truncated || strings.Join(files, ",") != "docs/new.md,src/new.go" {
		t.Fatalf("execListNewFiles() = (%#v, %v)", files, truncated)
	}
}

func TestExecListNewFilesPreservesWhitespace(t *testing.T) {
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _, _ string, _ []string) (string, error) {
		return " leading.txt\x00line\nbreak.txt\x00", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	files, truncated, err := execListNewFiles(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", "")
	if err != nil || truncated || len(files) != 2 || files[0] != " leading.txt" || files[1] != "line\nbreak.txt" {
		t.Fatalf("execListNewFiles() = (%#v, %v, %v)", files, truncated, err)
	}
}

func TestExecListNewFilesCapsResults(t *testing.T) {
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _, _ string, _ []string) (string, error) {
		var out strings.Builder
		for i := 0; i <= maxNewFiles; i++ {
			out.WriteString(fmt.Sprintf("file-%04d.txt\x00", i))
		}
		return out.String(), nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	files, truncated, err := execListNewFiles(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", "")
	if err != nil {
		t.Fatalf("execListNewFiles() error = %v", err)
	}
	if !truncated || len(files) != maxNewFiles {
		t.Fatalf("execListNewFiles() returned %d files, truncated=%v", len(files), truncated)
	}
}

func TestExecGetDiffRejectsInvalidRepoPath(t *testing.T) {
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _, _ string, _ []string) (string, error) {
		t.Fatal("execInPodFunc must not run for invalid repo paths")
		return "", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	if _, _, err := execGetDiff(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", "main", "/workspace/../etc"); err == nil {
		t.Fatal("expected execGetDiff() to reject traversal path")
	}
}

func TestRepoListScriptScansAttachRepositoryStore(t *testing.T) {
	if !strings.Contains(repoListScript, "find /workspace/repo/repos -mindepth 2 -maxdepth 2 -name .git") {
		t.Fatalf("repoListScript must scan the attach_repository store:\n%s", repoListScript)
	}
}

func TestExecReadFileUsesSelectedRepository(t *testing.T) {
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _ string, _ string, command []string) (string, error) {
		if len(command) != 6 || command[0] != "sh" || command[1] != "-c" || command[4] != extraRepoStoreDir+"/widgets" || command[5] != extraRepoStoreDir+"/widgets/src/new.ts" {
			t.Fatalf("command = %#v", command)
		}
		for _, want := range []string{"test ! -L", "rev-parse --show-toplevel", "realpath -e", "mktemp", "head -n 1001", fmt.Sprintf(`head -c %d "$target" >"$tmp" || exit 66`, maxReadFileBytes+1)} {
			if !strings.Contains(command[2], want) {
				t.Fatalf("script missing %q: %s", want, command[2])
			}
		}
		return "export {};\n", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	content, truncated, err := execReadFile(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", extraRepoStoreDir+"/widgets", "src/new.ts", 0)
	if err != nil || truncated || content != "export {};" {
		t.Fatalf("execReadFile() = (%q, %v, %v)", content, truncated, err)
	}
}

func TestExecReadFileTruncatesByLineCount(t *testing.T) {
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _ string, _ string, command []string) (string, error) {
		if len(command) != 6 || command[4] != repoDir || command[5] != repoDir+"/notes.txt" || !strings.Contains(command[2], "head -n 3") {
			t.Fatalf("command = %#v", command)
		}
		return "line1\nline2\nline3\n", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	content, truncated, err := execReadFile(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", "", "notes.txt", 2)
	if err != nil {
		t.Fatalf("execReadFile() error = %v", err)
	}
	if !truncated {
		t.Fatal("expected execReadFile() to report truncation")
	}
	if content != "line1\nline2" {
		t.Fatalf("execReadFile() = %q, want first two lines", content)
	}
}

func TestExecReadFileCapsRequestedLinesAndBytes(t *testing.T) {
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _ string, _ string, command []string) (string, error) {
		if !strings.Contains(command[2], fmt.Sprintf("head -n %d", maxReadFileLines+1)) {
			t.Fatalf("line cap missing from script: %s", command[2])
		}
		return strings.Repeat("x", maxReadFileBytes+1), nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	content, truncated, err := execReadFile(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", "", "large.txt", maxReadFileLines+500)
	if err != nil || !truncated || len(content) != maxReadFileBytes {
		t.Fatalf("execReadFile() bytes=%d truncated=%v err=%v", len(content), truncated, err)
	}
}

func TestExecReadFileRejectsInvalidUTF8BeforeTruncationBoundary(t *testing.T) {
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _ string, _ string, _ []string) (string, error) {
		out := strings.Repeat("x", maxReadFileBytes+1)
		return out[:100] + "\xff" + out[101:], nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	if _, _, err := execReadFile(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", "", "binary.dat", 0); err == nil {
		t.Fatal("expected invalid UTF-8 content to be rejected")
	}
}

func TestExecListWorkspaceFilesParsesAndCaps(t *testing.T) {
	t.Run("passes through formatted paths and skips blanks", func(t *testing.T) {
		origExec := execInPodFunc
		execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, podName, namespace string, command []string) (string, error) {
			if podName != "sandbox-1" || namespace != "default" {
				t.Fatalf("execInPodFunc pod/namespace = %s/%s", namespace, podName)
			}
			if len(command) != 3 || command[0] != "sh" || command[1] != "-c" {
				t.Fatalf("expected sh -c invocation, got %#v", command)
			}
			script := command[2]
			// The script scans both the primary repo and the wider workspace.
			if !strings.Contains(script, repoDir) || !strings.Contains(script, workspaceRoot) {
				t.Fatalf("script should scan repo + workspace: %q", script)
			}
			// Ensure heavy directories are pruned by name.
			for _, name := range []string{".git", "node_modules"} {
				if !strings.Contains(script, "-name "+name) {
					t.Fatalf("expected script to prune %q: %q", name, script)
				}
			}
			// Primary repo paths are repo-relative; additional repos are absolute.
			return "main.go\npkg/util.go\n\n/workspace/widgets/src/index.ts\n", nil
		}
		t.Cleanup(func() { execInPodFunc = origExec })

		paths, truncated, err := execListWorkspaceFiles(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", 0)
		if err != nil {
			t.Fatalf("execListWorkspaceFiles() error = %v", err)
		}
		if truncated {
			t.Fatal("did not expect truncation")
		}
		want := []string{"main.go", "pkg/util.go", "/workspace/widgets/src/index.ts"}
		if len(paths) != len(want) {
			t.Fatalf("paths = %#v, want %#v", paths, want)
		}
		for i := range want {
			if paths[i] != want[i] {
				t.Fatalf("paths[%d] = %q, want %q", i, paths[i], want[i])
			}
		}
	})

	t.Run("caps at limit and reports truncation", func(t *testing.T) {
		origExec := execInPodFunc
		execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _ string, _ string, _ []string) (string, error) {
			var b strings.Builder
			for i := range 5 {
				b.WriteString("f")
				b.WriteByte(byte('0' + i))
				b.WriteString(".txt\n")
			}
			return b.String(), nil
		}
		t.Cleanup(func() { execInPodFunc = origExec })

		paths, truncated, err := execListWorkspaceFiles(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", 3)
		if err != nil {
			t.Fatalf("execListWorkspaceFiles() error = %v", err)
		}
		if !truncated {
			t.Fatal("expected truncation when results exceed the limit")
		}
		if len(paths) != 3 {
			t.Fatalf("len(paths) = %d, want 3", len(paths))
		}
	})
}

func TestExecListFilesParsesFindOutput(t *testing.T) {
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _ string, _ string, command []string) (string, error) {
		want := []string{"find", repoDir + "/subdir", "-maxdepth", "1", "-mindepth", "1", "-printf", "%f\\t%y\\t%s\\n"}
		if len(command) != len(want) {
			t.Fatalf("execInPodFunc command len = %d, want %d (%#v)", len(command), len(want), command)
		}
		for i := range want {
			if command[i] != want[i] {
				t.Fatalf("execInPodFunc command[%d] = %q, want %q (%#v)", i, command[i], want[i], command)
			}
		}
		return "dir\td\t0\nfile.txt\tf\t42\n", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	entries, err := execListFiles(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default", "subdir")
	if err != nil {
		t.Fatalf("execListFiles() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if !entries[0].IsDir || entries[0].Name != "dir" {
		t.Fatalf("entries[0] = %#v", entries[0])
	}
	if entries[1].IsDir || entries[1].SizeBytes != 42 {
		t.Fatalf("entries[1] = %#v", entries[1])
	}
}

func TestExecCloneRepoBuildsGitCommand(t *testing.T) {
	t.Run("with base branch", func(t *testing.T) {
		origExec := execInPodFunc
		var got [][]string
		execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, podName, namespace string, command []string) (string, error) {
			if podName != "sandbox-1" || namespace != "default" {
				t.Fatalf("pod/namespace = %s/%s", namespace, podName)
			}
			got = append(got, command)
			if len(command) == 3 && command[0] == "sh" {
				return "chat-gf-all-1\n", nil
			}
			return "", nil
		}
		t.Cleanup(func() { execInPodFunc = origExec })

		repo, err := execCloneRepo(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default",
			"https://github.com/owner/widgets.git", "develop", "widgets")
		if err != nil {
			t.Fatalf("execCloneRepo() error = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("exec calls = %d (%#v), want clone then branch alignment", len(got), got)
		}
		want := []string{"git", "clone", "--branch", "develop", "https://github.com/owner/widgets.git", workspaceRoot + "/widgets"}
		if strings.Join(got[0], " ") != strings.Join(want, " ") {
			t.Fatalf("command = %#v, want %#v", got[0], want)
		}
		wantAlign := []string{"sh", "-c", alignCloneBranchScript(workspaceRoot + "/widgets")}
		if strings.Join(got[1], "\x00") != strings.Join(wantAlign, "\x00") {
			t.Fatalf("command = %#v, want %#v", got[1], wantAlign)
		}
		if repo.Path != workspaceRoot+"/widgets" || repo.Name != "widgets" || repo.IsPrimary {
			t.Fatalf("repo = %#v", repo)
		}
		// The clone reports the work branch it ended up on, not the clone base.
		if repo.Branch != "chat-gf-all-1" {
			t.Fatalf("repo.Branch = %q, want %q", repo.Branch, "chat-gf-all-1")
		}
	})

	t.Run("without base branch omits --branch", func(t *testing.T) {
		origExec := execInPodFunc
		var got []string
		execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _ string, _ string, command []string) (string, error) {
			if len(got) == 0 {
				got = command
			}
			return "", nil
		}
		t.Cleanup(func() { execInPodFunc = origExec })

		if _, err := execCloneRepo(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "p", "n",
			"https://github.com/owner/widgets.git", "", "widgets"); err != nil {
			t.Fatalf("execCloneRepo() error = %v", err)
		}
		for _, arg := range got {
			if arg == "--branch" {
				t.Fatalf("did not expect --branch: %#v", got)
			}
		}
	})

	t.Run("branch alignment failure keeps the clone", func(t *testing.T) {
		origExec := execInPodFunc
		execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _ string, _ string, command []string) (string, error) {
			if len(command) == 3 && command[0] == "sh" {
				return "", errors.New("exec into pod failed")
			}
			return "", nil
		}
		t.Cleanup(func() { execInPodFunc = origExec })

		repo, err := execCloneRepo(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "p", "n",
			"https://github.com/owner/widgets.git", "develop", "widgets")
		if err != nil {
			t.Fatalf("execCloneRepo() error = %v", err)
		}
		if repo.Branch != "develop" {
			t.Fatalf("repo.Branch = %q, want clone base %q", repo.Branch, "develop")
		}
	})
}

func TestAlignCloneBranchScript(t *testing.T) {
	script := alignCloneBranchScript(workspaceRoot + "/widgets")
	// The work branch comes from the run's primary repository.
	if !strings.Contains(script, "git -C "+repoDir+" rev-parse --abbrev-ref HEAD") {
		t.Fatalf("script must read the primary repo branch:\n%s", script)
	}
	// A previously pushed work branch (resumed run) must be fetched and
	// resumed rather than recreated at the cloned base.
	if !strings.Contains(script, `fetch -q origin "refs/heads/$wb:refs/remotes/origin/$wb"`) {
		t.Fatalf("script must fetch a previously pushed work branch:\n%s", script)
	}
	// --no-track: the work branch must never become its own diff base.
	if !strings.Contains(script, `checkout -q --no-track -B "$wb" $start`) {
		t.Fatalf("script must check out the work branch without tracking it:\n%s", script)
	}
	// Diffing relies on the cloned branch staying upstream (extraRepoDiffScript).
	if !strings.Contains(script, `--set-upstream-to "origin/$cb"`) {
		t.Fatalf("script must keep the cloned branch as upstream:\n%s", script)
	}
}

// TestAlignCloneBranchScriptEndToEnd runs the production alignment script
// (with only the primary repo path substituted) against real git
// repositories, covering the fresh-clone, resumed-run, and no-primary paths.
func TestAlignCloneBranchScriptEndToEnd(t *testing.T) {
	for _, bin := range []string{"git", "sh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s binary not available", bin)
		}
	}
	// Hermetic git: no user/system/env config may leak into the test repos.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_CONFIG_COUNT", "0")

	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.local",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.local")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	newOrigin := func(name string) string {
		t.Helper()
		origin := filepath.Join(t.TempDir(), name+".git")
		git("", "init", "--bare", "--quiet", origin)
		git(origin, "symbolic-ref", "HEAD", "refs/heads/main")
		seed := filepath.Join(t.TempDir(), "seed-"+name)
		git("", "clone", "--quiet", origin, seed)
		git(seed, "checkout", "--quiet", "-b", "main")
		if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(seed, "add", "-A")
		git(seed, "commit", "--quiet", "-m", "seed")
		git(seed, "push", "--quiet", "origin", "main")
		return origin
	}
	runScript := func(primary, clone string) string {
		t.Helper()
		script := strings.ReplaceAll(alignCloneBranchScript(clone), repoDir, primary)
		out, err := exec.Command("sh", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("alignment script: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}

	primaryOrigin := newOrigin("app")
	extraOrigin := newOrigin("widgets")

	primary := filepath.Join(t.TempDir(), "repo")
	git("", "clone", "--quiet", primaryOrigin, primary)
	git(primary, "checkout", "--quiet", "-b", "chat-run-1")

	t.Run("fresh clone lands on the work branch with cloned upstream", func(t *testing.T) {
		clone := filepath.Join(t.TempDir(), "widgets")
		git("", "clone", "--quiet", extraOrigin, clone)
		base := git(clone, "rev-parse", "HEAD")

		if got := runScript(primary, clone); got != "chat-run-1" {
			t.Fatalf("script output = %q, want chat-run-1", got)
		}
		if head := git(clone, "rev-parse", "HEAD"); head != base {
			t.Fatalf("HEAD moved: %s, want cloned base %s", head, base)
		}
		if upstream := git(clone, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); upstream != "origin/main" {
			t.Fatalf("upstream = %q, want origin/main", upstream)
		}
	})

	t.Run("resumed run keeps previously pushed work-branch commits", func(t *testing.T) {
		// A previous pod pushed run work for this extra repo.
		pushed := filepath.Join(t.TempDir(), "pushed")
		git("", "clone", "--quiet", extraOrigin, pushed)
		git(pushed, "checkout", "--quiet", "-b", "chat-run-1")
		if err := os.WriteFile(filepath.Join(pushed, "work.txt"), []byte("wip\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(pushed, "add", "-A")
		git(pushed, "commit", "--quiet", "-m", "wip")
		git(pushed, "push", "--quiet", "origin", "chat-run-1")
		want := git(pushed, "rev-parse", "HEAD")

		clone := filepath.Join(t.TempDir(), "widgets")
		git("", "clone", "--quiet", extraOrigin, clone)
		if got := runScript(primary, clone); got != "chat-run-1" {
			t.Fatalf("script output = %q, want chat-run-1", got)
		}
		if head := git(clone, "rev-parse", "HEAD"); head != want {
			t.Fatalf("HEAD = %s, want previously pushed %s", head, want)
		}
		if upstream := git(clone, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); upstream != "origin/main" {
			t.Fatalf("upstream = %q, want origin/main", upstream)
		}
	})

	t.Run("no primary repo keeps the cloned branch", func(t *testing.T) {
		clone := filepath.Join(t.TempDir(), "widgets")
		git("", "clone", "--quiet", extraOrigin, clone)
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		if got := runScript(missing, clone); got != "main" {
			t.Fatalf("script output = %q, want main", got)
		}
	})
}

func TestExecListReposParsesAndSortsPrimaryFirst(t *testing.T) {
	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _ string, _ string, command []string) (string, error) {
		if len(command) != 3 || command[0] != "sh" || command[1] != "-c" {
			t.Fatalf("expected sh -c invocation, got %#v", command)
		}
		return "/workspace/zeta\thttps://github.com/o/zeta.git\tmain\n" +
			"/workspace/repo\thttps://github.com/o/primary.git\trun-branch\n" +
			"\n", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	repos, err := execListRepos(context.Background(), &kubernetes.Clientset{}, &rest.Config{}, "sandbox-1", "default")
	if err != nil {
		t.Fatalf("execListRepos() error = %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("len(repos) = %d, want 2", len(repos))
	}
	if !repos[0].IsPrimary || repos[0].Name != "repo" || repos[0].Branch != "run-branch" {
		t.Fatalf("repos[0] = %#v", repos[0])
	}
	if repos[1].IsPrimary || repos[1].Name != "zeta" || repos[1].RemoteUrl != "https://github.com/o/zeta.git" {
		t.Fatalf("repos[1] = %#v", repos[1])
	}
}

func TestDeriveRepoDirName(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/my-repo.git": "my-repo",
		"https://github.com/owner/my-repo":     "my-repo",
		"git@github.com:owner/widgets.git":     "widgets",
		"https://github.com/owner/repo/":       "repo",
	}
	for url, want := range cases {
		got, err := deriveRepoDirName(url)
		if err != nil {
			t.Fatalf("deriveRepoDirName(%q) error = %v", url, err)
		}
		if got != want {
			t.Fatalf("deriveRepoDirName(%q) = %q, want %q", url, got, want)
		}
	}

	for _, bad := range []string{"", "https://github.com/owner/..", "https://github.com/owner/."} {
		if _, err := deriveRepoDirName(bad); err == nil {
			t.Fatalf("deriveRepoDirName(%q) expected error", bad)
		}
	}
}

func TestValidateCloneURL(t *testing.T) {
	for _, ok := range []string{"https://github.com/o/r.git", "http://x/y", "git@github.com:o/r.git", "ssh://git@h/o/r"} {
		if err := validateCloneURL(ok); err != nil {
			t.Fatalf("validateCloneURL(%q) error = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "--upload-pack=evil", "file:///etc/passwd", "ftp://x/y"} {
		if err := validateCloneURL(bad); err == nil {
			t.Fatalf("validateCloneURL(%q) expected error", bad)
		}
	}
}

func TestWorkspaceListScript(t *testing.T) {
	script := workspaceListScript()
	for _, want := range []string{
		repoDir,                 // primary repo root
		workspaceRoot,           // scans the wider workspace for cloned repos
		"-name .git",            // discovers additional repos by their .git dir
		`-name node_modules`,    // prunes heavy directories
		`-printf "$p%P\n"`,      // prints with the per-repo prefix
		`if [ "$d" = "$repo" ]`, // primary repo stays unprefixed
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("workspaceListScript() missing %q in:\n%s", want, script)
		}
	}
}
