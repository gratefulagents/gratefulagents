package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
)

type workspaceSnapshotMetadataStore struct {
	store.StateStore
	session *store.Session
	merges  int
}

func (s *workspaceSnapshotMetadataStore) GetSessionByRun(context.Context, string, string) (*store.Session, error) {
	return s.session, nil
}

func (s *workspaceSnapshotMetadataStore) GetSession(context.Context, uuid.UUID) (*store.Session, error) {
	return s.session, nil
}

func (s *workspaceSnapshotMetadataStore) GetResourceOwner(context.Context, string, string, string) (*store.ResourceOwnership, error) {
	return nil, nil
}

func (s *workspaceSnapshotMetadataStore) MergeSessionMetadata(_ context.Context, _ uuid.UUID, key string, value json.RawMessage) error {
	var metadata map[string]json.RawMessage
	if len(s.session.Metadata) > 0 {
		if err := json.Unmarshal(s.session.Metadata, &metadata); err != nil {
			return err
		}
	}
	if metadata == nil {
		metadata = make(map[string]json.RawMessage)
	}
	metadata[key] = append(json.RawMessage(nil), value...)
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	s.session.Metadata = encoded
	s.merges++
	return nil
}

func newWorkspaceSnapshotSessionClient(t *testing.T) (*sessionclient.Client, *workspaceSnapshotMetadataStore) {
	t.Helper()
	fake := &workspaceSnapshotMetadataStore{session: &store.Session{ID: uuid.New()}}
	sc, err := sessionclient.New(context.Background(), fake, nil, "run", "ns", "running", "")
	if err != nil {
		t.Fatalf("sessionclient.New() error = %v", err)
	}
	return sc, fake
}

func TestWorkspaceSnapshotEncryptionKeyPersistsAndReloads(t *testing.T) {
	sc, fake := newWorkspaceSnapshotSessionClient(t)
	first, err := loadOrCreateWorkspaceSnapshotKey(context.Background(), sc)
	if err != nil {
		t.Fatalf("loadOrCreateWorkspaceSnapshotKey() error = %v", err)
	}
	if len(first) != workspaceSnapshotKeyBytes {
		t.Fatalf("key length = %d, want %d", len(first), workspaceSnapshotKeyBytes)
	}
	if fake.merges != 1 {
		t.Fatalf("metadata merges = %d, want 1", fake.merges)
	}
	second, err := loadOrCreateWorkspaceSnapshotKey(context.Background(), sc)
	if err != nil {
		t.Fatalf("reload key error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("reloaded workspace snapshot key changed")
	}
	if fake.merges != 1 {
		t.Fatalf("reload unexpectedly rewrote metadata; merges = %d", fake.merges)
	}
}

func TestWorkspaceCheckpointEncryptsRepositoryPayloadInObjectStore(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	writeFile(t, work, "new/private-design.txt", "plaintext-marker-that-must-not-be-in-object-store\n")

	if err := newSnapshotter(work, store).snapshotLocked(testCtx(t), "encrypted-test"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	manifest := loadTestCheckpoint(t, store)
	payload, found, err := store.Get(testCtx(t), manifest.Repositories[0].ObjectKey)
	if err != nil || !found {
		t.Fatalf("reading encrypted payload: found=%v err=%v", found, err)
	}
	if bytes.Contains(payload, []byte("plaintext-marker-that-must-not-be-in-object-store")) || bytes.Contains(payload, []byte("private-design.txt")) {
		t.Fatal("object-store checkpoint exposed plaintext content or filename")
	}
	if !bytes.HasPrefix(payload, []byte(encryptedWorkspaceArchiveMagic)) {
		t.Fatal("object-store payload lacks encrypted envelope")
	}
	if refs := mustGit(t, origin, "for-each-ref", "refs/gratefulagents"); refs != "" {
		t.Fatalf("checkpoint wrote hidden remote refs:\n%s", refs)
	}
}

func TestWorkspaceSnapshotRestoresStagedNewAndRenameDestination(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	mustGit(t, work, "mv", "doomed.txt", "renamed.txt")
	writeFile(t, work, "src/new_handler.go", "package src\n")
	mustGit(t, work, "add", "src/new_handler.go")

	if err := newSnapshotter(work, store).snapshotLocked(testCtx(t), "staged-new-and-rename"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	resumed := cloneAndCheckout(t, origin, false)
	if err := restorePrimaryForTest(t, resumed, store, false, testWorkspaceSnapshotKey); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readFile(t, resumed, "renamed.txt"); got != "delete me\n" {
		t.Fatalf("rename destination = %q", got)
	}
	if got := readFile(t, resumed, "src/new_handler.go"); got != "package src\n" {
		t.Fatalf("staged new file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(resumed, "doomed.txt")); !os.IsNotExist(err) {
		t.Fatalf("rename source exists after restore: %v", err)
	}
	status := mustGit(t, resumed, "status", "--porcelain=v1", "--untracked-files=all")
	for _, want := range []string{"doomed.txt", "renamed.txt", "src/new_handler.go"} {
		if !strings.Contains(status, want) {
			t.Errorf("restored status missing %q:\n%s", want, status)
		}
	}
}

func TestWorkspaceSnapshotWrongKeyFailsClosed(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	writeFile(t, work, "new.go", "package new\n")
	if err := newSnapshotter(work, store).snapshotLocked(testCtx(t), "wrong-key"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	resumed := cloneAndCheckout(t, origin, false)
	wrongKey := []byte("abcdef0123456789abcdef0123456789")
	err := restorePrimaryForTest(t, resumed, store, false, wrongKey)
	if err == nil || !strings.Contains(err.Error(), "authenticating encrypted workspace archive") {
		t.Fatalf("restore error = %v, want authenticated-decryption failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(resumed, "new.go")); !os.IsNotExist(statErr) {
		t.Fatalf("wrong-key restore wrote untrusted content: %v", statErr)
	}
}

func TestWorkspaceSnapshotCleanupRetainsUntrackedWork(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	mustGit(t, work, "push", "--quiet", "-u", "origin", runBranch)
	writeFile(t, work, "new.go", "package new\n")

	s := newSnapshotter(work, store)
	finalizeWorkspaceSnapshot(runResult{Status: "succeeded"}, s)
	if _, found, _ := store.Get(testCtx(t), workspaceCheckpointLatestKey(testCheckpointPrefix)); !found {
		t.Fatal("successful run with untracked work deleted its checkpoint")
	}
	resumed := cloneAndCheckout(t, origin, true)
	if err := restorePrimaryForTest(t, resumed, store, true, testWorkspaceSnapshotKey); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readFile(t, resumed, "new.go"); got != "package new\n" {
		t.Fatalf("retained untracked work = %q", got)
	}
}

func TestWorkspaceSnapshotArchiveRejectsTraversalBeforeExtraction(t *testing.T) {
	var payload bytes.Buffer
	zw := gzip.NewWriter(&payload)
	tw := tar.NewWriter(zw)
	body := []byte("escape")
	if err := tw.WriteHeader(&tar.Header{Name: "../outside.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	_, err := walkWorkspaceArchive(root, payload.Bytes(), false)
	if err == nil || !strings.Contains(err.Error(), "unsafe untracked workspace path") {
		t.Fatalf("archive validation error = %v, want traversal rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "outside.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("traversal archive wrote outside workspace: %v", statErr)
	}
}

func TestToolMayMutateWorkspace(t *testing.T) {
	for _, name := range []string{"Write", "Edit", "Bash", "git_commit", "attach_repository", "subagent", "multi_tool_use.parallel"} {
		if !toolMayMutateWorkspace(name) {
			t.Errorf("toolMayMutateWorkspace(%q) = false", name)
		}
	}
	for _, name := range []string{"read_file", "grep", "git_status", "platform_get_run"} {
		if toolMayMutateWorkspace(name) {
			t.Errorf("toolMayMutateWorkspace(%q) = true", name)
		}
	}
}

func TestUntrackedWorkspaceArchiveSizeSelection(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	for _, tt := range []struct {
		name    string
		files   map[string]int
		want    []string
		warning []string
	}{
		{
			name:  "deep oversized folder retains sibling source",
			files: map[string]int{"project/generated/one": 7, "project/generated/two": 6, "project/source.go": 4, "note": 2},
			want:  []string{"note", "project/source.go"}, warning: []string{`"project/generated/" (13 remaining input bytes`},
		},
		{
			name:  "aggregate largest folder first",
			files: map[string]int{"alpha/one": 4, "alpha/two": 4, "beta/source": 5, "root": 3},
			want:  []string{"beta/source", "root"}, warning: []string{`"alpha/" (8 remaining input bytes`},
		},
		{
			name:  "aggregate ties lexical and multiple removals",
			files: map[string]int{"a/file": 6, "b/file": 6, "c/file": 6, "d/file": 6, "root": 1},
			want:  []string{"d/file", "root"}, warning: []string{`"a/" (6 remaining input bytes`, `"b/" (6 remaining input bytes`, `"c/" (6 remaining input bytes`},
		},
		{
			name:  "oversized root file",
			files: map[string]int{"huge": 13, "src/file": 4},
			want:  []string{"src/file"}, warning: []string{`"huge" (13 remaining input bytes`},
		},
		{
			name:  "root files aggregate",
			files: map[string]int{"a": 8, "b": 5},
			want:  []string{"b"}, warning: []string{`"a" (8 remaining input bytes`},
		},
		{
			name:  "exact budget no cache name exclusions",
			files: map[string]int{"node_modules/a": 5, ".cache/b": 4, "src/c": 3, "empty": 0},
			want:  []string{".cache/b", "empty", "node_modules/a", "src/c"},
		},
		{
			name:    "all skipped",
			files:   map[string]int{"data/a": 13, "root": 14},
			warning: []string{`"data/" (13 remaining input bytes`, `"root" (14 remaining input bytes`},
		},
		{
			name:  "multiple deep folders do not discard parent",
			files: map[string]int{"p/a/file": 13, "p/b/file": 14, "p/source": 3},
			want:  []string{"p/source"}, warning: []string{`"p/a/" (13 remaining input bytes`, `"p/b/" (14 remaining input bytes`},
		},
		{
			name:  "parent still oversized after pruning",
			files: map[string]int{"p/a/file": 13, "p/one": 7, "p/two": 6, "root": 1},
			want:  []string{"root"}, warning: []string{`"p/a/" (13 remaining input bytes`, `"p/" (13 remaining input bytes`},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			work := cloneAndCheckout(t, origin, false)
			for path, size := range tt.files {
				writeFile(t, work, path, strings.Repeat("x", size))
			}
			var warnings bytes.Buffer
			oldOutput := log.Writer()
			log.SetOutput(&warnings)
			t.Cleanup(func() { log.SetOutput(oldOutput) })
			archive, hash, count, err := buildUntrackedWorkspaceArchiveWithBudget(testCtx(t), work, 12)
			if err != nil {
				t.Fatal(err)
			}
			if count != len(tt.want) {
				t.Fatalf("file count = %d, want %d", count, len(tt.want))
			}
			for _, want := range tt.warning {
				if !strings.Contains(warnings.String(), want) {
					t.Errorf("warnings missing %q: %s", want, warnings.String())
				}
			}
			if got := strings.Count(warnings.String(), "WARN:"); got != len(tt.warning) {
				t.Errorf("warning count = %d, want %d: %s", got, len(tt.warning), warnings.String())
			}
			if len(tt.warning) > 0 && (!strings.Contains(warnings.String(), "NOT durable or restored") || !strings.Contains(warnings.String(), "budget 12 bytes")) {
				t.Errorf("missing durability/budget warning: %s", warnings.String())
			}
			second, secondHash, secondCount, err := buildUntrackedWorkspaceArchiveWithBudget(testCtx(t), work, 12)
			if err != nil || !bytes.Equal(archive, second) || hash != secondHash || count != secondCount {
				t.Fatalf("repeated archive differs: %v", err)
			}
			if len(tt.want) == 0 {
				if len(archive) != 0 || hash != "" {
					t.Fatal("all-skipped archive should be absent")
				}
				return
			}
			envelope, err := encryptWorkspaceArchive(testWorkspaceSnapshotKey, archive)
			if err != nil {
				t.Fatal(err)
			}
			restored := t.TempDir()
			if n, err := restoreEncryptedWorkspaceArchive(restored, testWorkspaceSnapshotKey, envelope); err != nil || n != count {
				t.Fatalf("restore count=%d error=%v", n, err)
			}
			var got []string
			for path, size := range tt.files {
				data, err := os.ReadFile(filepath.Join(restored, path))
				if os.IsNotExist(err) {
					continue
				}
				if err != nil || string(data) != strings.Repeat("x", size) {
					t.Fatalf("restored %q differs: %v", path, err)
				}
				got = append(got, path)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("restored paths = %v, want %v", got, tt.want)
			}
			for path, size := range tt.files {
				if got := readFile(t, work, path); got != strings.Repeat("x", size) {
					t.Fatalf("local file %q was modified", path)
				}
			}
		})
	}
}

func TestUntrackedWorkspaceArchiveBudgetIncludesStagedNewFiles(t *testing.T) {
	requireGit(t)
	work := cloneAndCheckout(t, newOriginWithSeed(t), false)
	writeFile(t, work, "generated/staged", strings.Repeat("x", 13))
	writeFile(t, work, "src/staged", "source")
	writeFile(t, work, ".gitignore", "ignored/\n")
	mustGit(t, work, "add", ".gitignore")
	mustGit(t, work, "commit", "-m", "ignore generated data")
	writeFile(t, work, "ignored/large", strings.Repeat("x", 20))
	mustGit(t, work, "add", "generated/staged", "src/staged")
	if err := os.Symlink("src/staged", filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}
	before := mustGit(t, work, "ls-files", "--stage")
	archive, _, count, err := buildUntrackedWorkspaceArchiveWithBudget(testCtx(t), work, 12)
	if err != nil || count != 2 {
		t.Fatalf("archive count=%d error=%v", count, err)
	}
	if after := mustGit(t, work, "ls-files", "--stage"); after != before {
		t.Fatal("checkpoint changed user's index")
	}
	head := mustGit(t, work, "rev-parse", "HEAD")
	tree, err := writeWorkingTree(testCtx(t), work, head)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, work, "ls-tree", "-r", "--name-only", tree); strings.Contains(got, "staged") {
		t.Fatalf("staged additions leaked into tracked tree: %s", got)
	}
	restored := t.TempDir()
	if n, err := walkWorkspaceArchive(restored, archive, true); err != nil || n != 2 {
		t.Fatalf("restore count=%d error=%v", n, err)
	}
	if got := readFile(t, restored, "src/staged"); got != "source" {
		t.Fatalf("restored staged content = %q", got)
	}
	if target, err := os.Readlink(filepath.Join(restored, "link")); err != nil || target != "src/staged" {
		t.Fatalf("restored symlink = %q error=%v", target, err)
	}
	for _, path := range []string{"generated/staged", "ignored/large"} {
		if _, err := os.Lstat(filepath.Join(restored, path)); !os.IsNotExist(err) {
			t.Fatalf("excluded path %q restored: %v", path, err)
		}
	}
}

func TestWorkspaceArchivePreflightDoesNotHideErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "oversized/data", strings.Repeat("x", 13))
	if err := os.Mkdir(filepath.Join(dir, "oversized/special"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ path, want string }{
		{"oversized/missing", "reading untracked workspace path"},
		{"oversized/special", "unsupported file type"},
		{"oversized/../escape", "unsafe untracked workspace path"},
		{"oversized/.git/config", "Git metadata"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			_, err := selectWorkspaceArchiveEntries(dir, []string{"oversized/data", tt.path}, 12)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
