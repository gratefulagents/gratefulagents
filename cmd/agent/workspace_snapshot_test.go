package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_CONFIG_COUNT", "0")
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ident := []string{
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.local",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.local",
	}
	out, err := gitOutput(testCtx(t), dir, ident, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	file := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

func newOriginWithSeed(t *testing.T) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	mustGit(t, "", "init", "--bare", "--quiet", origin)
	mustGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")
	seed := filepath.Join(t.TempDir(), "seed")
	mustGit(t, "", "clone", "--quiet", origin, seed)
	mustGit(t, seed, "checkout", "--quiet", "-b", "main")
	writeFile(t, seed, "README.md", "hello\n")
	writeFile(t, seed, "src/app.go", "package app\n")
	writeFile(t, seed, "doomed.txt", "delete me\n")
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "commit", "--quiet", "-m", "seed")
	mustGit(t, seed, "push", "--quiet", "origin", "main")
	return origin
}

var testWorkspaceSnapshotKey = []byte("0123456789abcdef0123456789abcdef")

const (
	runBranch            = "run-branch"
	testCheckpointPrefix = "workspace-checkpoints/v1/test-ns/test-uid"
)

type memoryWorkspaceObjectStore struct {
	mu         sync.Mutex
	objects    map[string][]byte
	puts       map[string]int
	failPutKey string
}

func newMemoryWorkspaceObjectStore() *memoryWorkspaceObjectStore {
	return &memoryWorkspaceObjectStore{objects: make(map[string][]byte), puts: make(map[string]int)}
}

func (s *memoryWorkspaceObjectStore) Put(_ context.Context, key string, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == s.failPutKey {
		return fmt.Errorf("injected put failure")
	}
	s.objects[key] = append([]byte(nil), body...)
	s.puts[key]++
	return nil
}

func (s *memoryWorkspaceObjectStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.objects[key]
	return append([]byte(nil), body...), ok, nil
}

func (s *memoryWorkspaceObjectStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func testCheckpointConfig(repoDir string, store workspaceObjectStore) runConfig {
	return runConfig{
		RepoDir: repoDir, Namespace: "test-ns", TaskName: "test-run", TaskUID: "test-uid",
		WorkspaceSnapshotKey: testWorkspaceSnapshotKey, WorkspaceCheckpointStore: store,
		WorkspaceCheckpointPrefix: testCheckpointPrefix,
	}
}

func newSnapshotter(repoDir string, store workspaceObjectStore) *workspaceSnapshotter {
	return newWorkspaceSnapshotter(testCheckpointConfig(repoDir, store), nil)
}

func loadTestCheckpoint(t *testing.T, store workspaceObjectStore) *workspaceCheckpointManifest {
	t.Helper()
	manifest, err := loadWorkspaceCheckpoint(testCtx(t), store, testCheckpointPrefix, testWorkspaceSnapshotKey)
	if err != nil {
		t.Fatalf("loadWorkspaceCheckpoint: %v", err)
	}
	if manifest == nil {
		t.Fatal("workspace checkpoint was not published")
	}
	return manifest
}

func cloneAndCheckout(t *testing.T, origin string, remoteExists bool) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "work")
	mustGit(t, "", "clone", "--quiet", "--branch", "main", origin, dir)
	if remoteExists {
		mustGit(t, dir, "fetch", "--quiet", "origin", "refs/heads/"+runBranch+":refs/remotes/origin/"+runBranch)
		mustGit(t, dir, "checkout", "--quiet", "--track", "-b", runBranch, "origin/"+runBranch)
	} else {
		mustGit(t, dir, "checkout", "--quiet", "-b", runBranch)
	}
	return dir
}

func restorePrimaryForTest(t *testing.T, dest string, store workspaceObjectStore, pushed bool, key []byte) error {
	t.Helper()
	cfg := testCheckpointConfig(dest, store)
	cfg.WorkspaceSnapshotKey = key
	manifest, err := loadWorkspaceCheckpoint(testCtx(t), store, testCheckpointPrefix, key)
	if err != nil {
		return err
	}
	cfg.WorkspaceCheckpoint = manifest
	return restorePrimaryWorkspaceCheckpoint(cfg, pushed)
}

func TestWorkspaceCheckpointRoundtrip(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	mustGit(t, work, "push", "--quiet", "-u", "origin", runBranch)

	writeFile(t, work, "committed.txt", "local commit\n")
	mustGit(t, work, "add", "committed.txt")
	mustGit(t, work, "commit", "--quiet", "-m", "local work")
	writeFile(t, work, "src/app.go", "package app // edited\n")
	writeFile(t, work, "notes.md", "untracked scratch\n")
	writeFile(t, work, ".gitignore", "cache/\n")
	writeFile(t, work, "cache/blob.bin", "must not survive\n")
	if err := os.Remove(filepath.Join(work, "doomed.txt")); err != nil {
		t.Fatal(err)
	}

	if err := newSnapshotter(work, store).snapshotLocked(testCtx(t), "test"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if refs := mustGit(t, origin, "for-each-ref", "refs/gratefulagents"); refs != "" {
		t.Fatalf("checkpoint wrote repository refs:\n%s", refs)
	}

	resumed := cloneAndCheckout(t, origin, true)
	if err := restorePrimaryForTest(t, resumed, store, true, testWorkspaceSnapshotKey); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readFile(t, resumed, "src/app.go"); got != "package app // edited\n" {
		t.Errorf("modified file = %q", got)
	}
	if got := readFile(t, resumed, "notes.md"); got != "untracked scratch\n" {
		t.Errorf("untracked file = %q", got)
	}
	if got := readFile(t, resumed, "committed.txt"); got != "local commit\n" {
		t.Errorf("local commit content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(resumed, "doomed.txt")); !os.IsNotExist(err) {
		t.Error("deleted file came back")
	}
	if _, err := os.Stat(filepath.Join(resumed, "cache/blob.bin")); !os.IsNotExist(err) {
		t.Error("ignored file was checkpointed")
	}
	if subject := mustGit(t, resumed, "log", "-1", "--format=%s"); subject != "local work" {
		t.Errorf("HEAD subject = %q", subject)
	}
}

func TestWorkspaceCheckpointImportsAnchorWhenOriginNoLongerRetainsIt(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	advance := filepath.Join(t.TempDir(), "advance")
	mustGit(t, "", "clone", "--quiet", origin, advance)
	writeFile(t, advance, "second.txt", "non-root anchor\n")
	mustGit(t, advance, "add", "second.txt")
	mustGit(t, advance, "commit", "--quiet", "-m", "second commit")
	mustGit(t, advance, "push", "--quiet", "origin", "main")

	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	writeFile(t, work, "README.md", "checkpoint on old history\n")
	if err := newSnapshotter(work, store).snapshotLocked(testCtx(t), "test"); err != nil {
		t.Fatal(err)
	}
	manifest := loadTestCheckpoint(t, store)
	anchor := manifest.Repositories[0].Anchor
	if anchor == "" || manifest.Repositories[0].AnchorObjectKey == "" {
		t.Fatal("checkpoint did not publish a self-contained anchor payload")
	}
	if parent := mustGit(t, work, "rev-parse", anchor+"^"); parent == "" {
		t.Fatal("test anchor unexpectedly has no parent")
	}

	replacement := filepath.Join(t.TempDir(), "replacement")
	mustGit(t, "", "init", "--quiet", "-b", "main", replacement)
	writeFile(t, replacement, "README.md", "rewritten history\n")
	mustGit(t, replacement, "add", "README.md")
	mustGit(t, replacement, "commit", "--quiet", "-m", "replacement root")
	mustGit(t, replacement, "remote", "add", "origin", origin)
	mustGit(t, replacement, "push", "--quiet", "--force", "origin", "main")
	mustGit(t, origin, "reflog", "expire", "--expire=now", "--all")
	mustGit(t, origin, "gc", "--prune=now")

	resumed := cloneAndCheckout(t, origin, false)
	if gitObjectExists(testCtx(t), resumed, anchor) {
		t.Fatal("fresh clone unexpectedly contains the checkpoint anchor")
	}
	cfg := testCheckpointConfig(resumed, store)
	cfg.WorkspaceCheckpoint = manifest
	if err := restorePrimaryWorkspaceCheckpoint(cfg, false); err != nil {
		t.Fatalf("restore with S3 anchor fallback: %v", err)
	}
	if !gitObjectExists(testCtx(t), resumed, anchor) {
		t.Fatal("S3 anchor fallback was not imported")
	}
}

func TestRemoteRefExistsDistinguishesMissingFromTransportFailure(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	work := cloneAndCheckout(t, origin, false)
	exists, err := remoteRefExists(testCtx(t), work, "refs/heads/does-not-exist")
	if err != nil || exists {
		t.Fatalf("missing ref: exists=%v err=%v", exists, err)
	}
	mustGit(t, work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unreachable.git"))
	exists, err = remoteRefExists(testCtx(t), work, "refs/heads/main")
	if err == nil || exists {
		t.Fatalf("failed remote: exists=%v err=%v", exists, err)
	}
}

func TestWorkspaceCheckpointReusesUnchangedRepositoryObject(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	writeFile(t, work, "notes.md", "wip\n")
	s := newSnapshotter(work, store)
	if err := s.snapshotLocked(testCtx(t), "first"); err != nil {
		t.Fatal(err)
	}
	first := loadTestCheckpoint(t, store)
	objectKey := first.Repositories[0].ObjectKey
	if err := s.snapshotLocked(testCtx(t), "second"); err != nil {
		t.Fatal(err)
	}
	if got := store.puts[objectKey]; got != 1 {
		t.Fatalf("repository payload PUT count = %d, want 1", got)
	}
}

func TestWorkspaceCheckpointManifestPublicationIsAtomic(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	s := newSnapshotter(work, store)
	if err := s.snapshotLocked(testCtx(t), "first"); err != nil {
		t.Fatal(err)
	}
	before := loadTestCheckpoint(t, store).Generation
	writeFile(t, work, "README.md", "changed\n")

	// Predict the changed payload key by failing every object PUT through a wrapper.
	failing := &failObjectPutsStore{memoryWorkspaceObjectStore: store}
	s.store = failing
	if err := s.snapshotLocked(testCtx(t), "failed"); err == nil {
		t.Fatal("checkpoint unexpectedly succeeded")
	}
	if after := loadTestCheckpoint(t, store).Generation; after != before {
		t.Fatalf("failed payload upload published generation %s, previous %s", after, before)
	}
}

func TestWorkspaceCheckpointKeepsPreviousManifestWhenLatestPutFails(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	s := newSnapshotter(work, store)
	if err := s.snapshotLocked(testCtx(t), "first"); err != nil {
		t.Fatal(err)
	}
	before := loadTestCheckpoint(t, store).Generation
	writeFile(t, work, "README.md", "changed\n")
	s.store = &failLatestPutStore{memoryWorkspaceObjectStore: store}
	if err := s.snapshotLocked(testCtx(t), "failed-latest"); err == nil {
		t.Fatal("checkpoint unexpectedly succeeded")
	}
	if after := loadTestCheckpoint(t, store).Generation; after != before {
		t.Fatalf("failed latest PUT published generation %s, previous %s", after, before)
	}
}

func TestWorkspaceCheckpointRestoreFailsForMissingOrCorruptPublishedPayload(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	writeFile(t, work, "README.md", "wip\n")
	if err := newSnapshotter(work, store).snapshotLocked(testCtx(t), "test"); err != nil {
		t.Fatal(err)
	}
	manifest := loadTestCheckpoint(t, store)
	objectKey := manifest.Repositories[0].ObjectKey
	original := append([]byte(nil), store.objects[objectKey]...)
	delete(store.objects, objectKey)
	resumed := cloneAndCheckout(t, origin, false)
	cfg := testCheckpointConfig(resumed, store)
	cfg.WorkspaceCheckpoint = manifest
	if err := restorePrimaryWorkspaceCheckpoint(cfg, false); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("missing payload restore error = %v", err)
	}
	store.objects[objectKey] = append([]byte(nil), original...)
	store.objects[objectKey][len(store.objects[objectKey])-1] ^= 0xff
	if err := restorePrimaryWorkspaceCheckpoint(cfg, false); err == nil || !strings.Contains(err.Error(), "authenticating encrypted workspace archive") {
		t.Fatalf("corrupt payload restore error = %v", err)
	}
}

type failObjectPutsStore struct{ *memoryWorkspaceObjectStore }

type failLatestPutStore struct{ *memoryWorkspaceObjectStore }

func TestWorkspaceCheckpointHookFailsClosedWhenManifestPublishFails(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	writeFile(t, work, "wip.txt", "must survive\n")
	snapshotter := newSnapshotter(work, store)
	snapshotter.store = &failLatestPutStore{memoryWorkspaceObjectStore: store}
	hook := &workspaceCheckpointHooks{snapshotter: snapshotter}
	tool := &agent.FunctionTool{ToolName: "write", Schema: json.RawMessage(`{"type":"object"}`), Fn: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }}
	if err := hook.OnToolEndError(nil, nil, tool, agent.ToolCallData{}, agent.ToolResult{}); err == nil || !strings.Contains(err.Error(), "publishing encrypted workspace checkpoint") {
		t.Fatalf("hook error = %v, want fail-closed publication error", err)
	}
}

func (s *failLatestPutStore) Put(ctx context.Context, key string, body []byte) error {
	if strings.HasSuffix(key, "/"+workspaceLatestObject) {
		return fmt.Errorf("injected latest upload failure")
	}
	return s.memoryWorkspaceObjectStore.Put(ctx, key, body)
}

func (s *failObjectPutsStore) Put(ctx context.Context, key string, body []byte) error {
	if strings.Contains(key, "/objects/") {
		return fmt.Errorf("injected object upload failure")
	}
	return s.memoryWorkspaceObjectStore.Put(ctx, key, body)
}

func TestWorkspaceCheckpointSkipsDivergedPushedBranch(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	mustGit(t, work, "push", "--quiet", "-u", "origin", runBranch)
	writeFile(t, work, "notes.md", "stale wip\n")
	if err := newSnapshotter(work, store).snapshotLocked(testCtx(t), "test"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, work, "pushed.txt", "newer pushed work\n")
	mustGit(t, work, "add", "pushed.txt")
	mustGit(t, work, "commit", "--quiet", "-m", "newer")
	mustGit(t, work, "push", "--quiet", "origin", runBranch)

	resumed := cloneAndCheckout(t, origin, true)
	if err := restorePrimaryForTest(t, resumed, store, true, testWorkspaceSnapshotKey); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(resumed, "notes.md")); !os.IsNotExist(err) {
		t.Error("stale checkpoint was applied")
	}
	if got := readFile(t, resumed, "pushed.txt"); got != "newer pushed work\n" {
		t.Errorf("pushed work = %q", got)
	}
}

func TestWorkspaceCheckpointRestoresNeverPushedBranchAfterBaseAdvance(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	oldBase := mustGit(t, work, "rev-parse", "HEAD")
	writeFile(t, work, "README.md", "wip on old base\n")
	if err := newSnapshotter(work, store).snapshotLocked(testCtx(t), "test"); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(t.TempDir(), "base")
	mustGit(t, "", "clone", "--quiet", origin, base)
	writeFile(t, base, "upstream.txt", "advanced\n")
	mustGit(t, base, "add", "upstream.txt")
	mustGit(t, base, "commit", "--quiet", "-m", "advance")
	mustGit(t, base, "push", "--quiet", "origin", "main")

	resumed := cloneAndCheckout(t, origin, false)
	if err := restorePrimaryForTest(t, resumed, store, false, testWorkspaceSnapshotKey); err != nil {
		t.Fatal(err)
	}
	if head := mustGit(t, resumed, "rev-parse", "HEAD"); head != oldBase {
		t.Errorf("HEAD = %s, want %s", head, oldBase)
	}
	if got := readFile(t, resumed, "README.md"); got != "wip on old base\n" {
		t.Errorf("WIP = %q", got)
	}
}

func TestWorkspaceCheckpointExtraRepoRoundtrip(t *testing.T) {
	requireGit(t)
	mainOrigin, extraOrigin := newOriginWithSeed(t), newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, mainOrigin, false)
	extraDir := filepath.Join(work, extraRepoStoreDirName, "helper")
	mustGit(t, "", "clone", "--quiet", extraOrigin, extraDir)
	mustGit(t, extraDir, "checkout", "--quiet", "-B", runBranch)
	ensureExtraRepoExclude(work)
	writeFile(t, extraDir, "README.md", "extra wip\n")
	writeFile(t, extraDir, "new_helper.go", "package helper\n")
	if err := newSnapshotter(work, store).snapshotLocked(testCtx(t), "test"); err != nil {
		t.Fatal(err)
	}
	manifest := loadTestCheckpoint(t, store)
	if len(manifest.Repositories) != 2 {
		t.Fatalf("repositories = %d, want 2", len(manifest.Repositories))
	}

	resumed := cloneAndCheckout(t, mainOrigin, false)
	cfg := testCheckpointConfig(resumed, store)
	cfg.WorkspaceCheckpoint = manifest
	if err := restoreExtraRepos(testCtx(t), cfg, nil); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(resumed, extraRepoStoreDirName, "helper")
	if got := readFile(t, restored, "README.md"); got != "extra wip\n" {
		t.Errorf("extra WIP = %q", got)
	}
	if got := readFile(t, restored, "new_helper.go"); got != "package helper\n" {
		t.Errorf("extra untracked = %q", got)
	}
}

func TestWorkspaceCheckpointCleanupDeletesLatestOnlyWhenDurable(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	mustGit(t, work, "push", "--quiet", "-u", "origin", runBranch)
	s := newSnapshotter(work, store)
	if err := s.snapshotLocked(testCtx(t), "test"); err != nil {
		t.Fatal(err)
	}
	s.Cleanup(testCtx(t))
	if _, found, _ := store.Get(testCtx(t), workspaceCheckpointLatestKey(testCheckpointPrefix)); found {
		t.Fatal("clean, pushed workspace retained latest checkpoint")
	}

	writeFile(t, work, "README.md", "dirty\n")
	finalizeWorkspaceSnapshot(runResult{Status: "succeeded"}, s)
	if _, found, _ := store.Get(testCtx(t), workspaceCheckpointLatestKey(testCheckpointPrefix)); !found {
		t.Fatal("dirty workspace did not retain a checkpoint")
	}
}

func TestDiscoverWorkspaceRootReposExcludesScratchGitCheckout(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	for _, dir := range []string{
		repoDir,
		filepath.Join(root, filepath.Base(workspaceScratchDir), ".git"),
		filepath.Join(root, "widgets", ".git"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	repos, err := discoverWorkspaceRootReposStrict(repoDir)
	if err != nil {
		t.Fatalf("discoverWorkspaceRootReposStrict() error = %v", err)
	}
	if len(repos) != 1 || repos[0].alias != "widgets" {
		t.Fatalf("repositories = %#v, want only widgets (scratch must never be checkpointed)", repos)
	}
}
