package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
