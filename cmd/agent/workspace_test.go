package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newBareOrigin creates a bare origin named <name>.git with one seeded commit
// on main and returns its path.
func newBareOrigin(t *testing.T, name string) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), name+".git")
	mustGit(t, "", "init", "--bare", "--quiet", origin)
	mustGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")

	seed := filepath.Join(t.TempDir(), "seed-"+name)
	mustGit(t, "", "clone", "--quiet", origin, seed)
	mustGit(t, seed, "checkout", "--quiet", "-b", "main")
	writeFile(t, seed, "README.md", name+"\n")
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "commit", "--quiet", "-m", "seed")
	mustGit(t, seed, "push", "--quiet", "origin", "main")
	return origin
}

func TestCloneAdditionalRepos(t *testing.T) {
	requireGit(t)

	primaryOrigin := newBareOrigin(t, "app")
	libOrigin := newBareOrigin(t, "lib")
	toolsOrigin := newBareOrigin(t, "tools")

	repoDir := filepath.Join(t.TempDir(), "repo")
	mustGit(t, "", "clone", "--quiet", primaryOrigin, repoDir)

	cfg := runConfig{
		RepoDir:            repoDir,
		TaskName:           "chat-run-1",
		AdditionalRepoURLs: []string{libOrigin, toolsOrigin},
	}
	if err := cloneAdditionalRepos(&cfg); err != nil {
		t.Fatalf("cloneAdditionalRepos() error = %v", err)
	}

	for _, name := range []string{"lib", "tools"} {
		dest := filepath.Join(repoDir, extraRepoStoreDirName, name)
		if info, err := os.Stat(filepath.Join(dest, ".git")); err != nil || !info.IsDir() {
			t.Fatalf("expected git clone at %s: err=%v", dest, err)
		}
		// Extra repos work on the same branch as the primary repository.
		if got := strings.TrimSpace(mustGit(t, dest, "rev-parse", "--abbrev-ref", "HEAD")); got != "chat-run-1" {
			t.Fatalf("%s branch = %q, want %q", name, got, "chat-run-1")
		}
	}
	// The store must stay out of the primary repository's status output.
	exclude := readFile(t, repoDir, filepath.Join(".git", "info", "exclude"))
	if !strings.Contains(exclude, extraRepoStoreDirName+"/") {
		t.Fatalf("info/exclude = %q, want %q entry", exclude, extraRepoStoreDirName+"/")
	}

	// Existing clones are kept (e.g. after a snapshot restore): a marker file
	// must survive a second invocation.
	marker := filepath.Join(repoDir, extraRepoStoreDirName, "lib", "marker.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cloneAdditionalRepos(&cfg); err != nil {
		t.Fatalf("cloneAdditionalRepos() second run error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("existing clone was clobbered: %v", err)
	}
}

func TestCloneAdditionalReposResumesPushedWorkBranch(t *testing.T) {
	requireGit(t)

	libOrigin := newBareOrigin(t, "lib")

	// A previous pod of this run already pushed extra-repo work.
	seed := filepath.Join(t.TempDir(), "pushed")
	mustGit(t, "", "clone", "--quiet", libOrigin, seed)
	mustGit(t, seed, "checkout", "--quiet", "-b", "chat-run-1")
	writeFile(t, seed, "work.txt", "wip\n")
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "commit", "--quiet", "-m", "wip")
	mustGit(t, seed, "push", "--quiet", "origin", "chat-run-1")

	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := runConfig{
		RepoDir:            repoDir,
		Repoless:           true,
		TaskName:           "chat-run-1",
		AdditionalRepoURLs: []string{libOrigin},
	}
	if err := cloneAdditionalRepos(&cfg); err != nil {
		t.Fatalf("cloneAdditionalRepos() error = %v", err)
	}

	dest := filepath.Join(repoDir, extraRepoStoreDirName, "lib")
	if got := strings.TrimSpace(mustGit(t, dest, "rev-parse", "--abbrev-ref", "HEAD")); got != "chat-run-1" {
		t.Fatalf("branch = %q, want %q", got, "chat-run-1")
	}
	if got := readFile(t, dest, "work.txt"); got != "wip\n" {
		t.Fatalf("work.txt = %q, want previously pushed work restored", got)
	}
	// The resumed branch must not track itself: extra repos are diffed
	// against @{upstream} (falling back to origin/HEAD), so an upstream of
	// origin/chat-run-1 would make every run diff empty.
	if upstream := strings.TrimSpace(mustGit(t, dest, "for-each-ref", "--format=%(upstream)", "refs/heads/chat-run-1")); upstream != "" {
		t.Fatalf("upstream = %q, want none", upstream)
	}
}

func TestCloneAdditionalReposRepoless(t *testing.T) {
	requireGit(t)

	libOrigin := newBareOrigin(t, "lib")
	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := runConfig{
		RepoDir:            repoDir,
		Repoless:           true,
		TaskName:           "chat-run-2",
		AdditionalRepoURLs: []string{libOrigin},
	}
	if err := cloneAdditionalRepos(&cfg); err != nil {
		t.Fatalf("cloneAdditionalRepos() error = %v", err)
	}
	dest := filepath.Join(repoDir, extraRepoStoreDirName, "lib")
	if info, err := os.Stat(filepath.Join(dest, ".git")); err != nil || !info.IsDir() {
		t.Fatalf("expected git clone at %s: err=%v", dest, err)
	}
	// The working branch matches the run even without a primary repository,
	// mirroring the attach_repository tool's default branch.
	if got := strings.TrimSpace(mustGit(t, dest, "rev-parse", "--abbrev-ref", "HEAD")); got != "chat-run-2" {
		t.Fatalf("branch = %q, want %q", got, "chat-run-2")
	}
	// No primary repository: no .git metadata may be fabricated in the
	// scratch workspace root.
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git in repoless workspace root: err=%v, want not exist", err)
	}
}

func TestAdditionalRepoContextLine(t *testing.T) {
	if got := additionalRepoContextLine(nil); got != "" {
		t.Fatalf("additionalRepoContextLine(nil) = %q, want empty", got)
	}
	got := additionalRepoContextLine([]string{"https://github.com/example/lib.git", "git@github.com:example/tools.git"})
	for _, want := range []string{"repos/lib", "repos/tools", "https://github.com/example/lib.git"} {
		if !strings.Contains(got, want) {
			t.Fatalf("additionalRepoContextLine() = %q, want containing %q", got, want)
		}
	}
}
