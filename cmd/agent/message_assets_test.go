package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
)

type messageAssetStore struct {
	store.StateStore
	item             store.ProjectContent
	version          store.ProjectContentVersion
	requestedVersion int
}

func (s *messageAssetStore) ListContent(context.Context, string, string, bool) ([]store.ProjectContent, error) {
	return []store.ProjectContent{s.item}, nil
}
func (s *messageAssetStore) GetContent(context.Context, uuid.UUID) (*store.ProjectContent, error) {
	item := s.item
	return &item, nil
}
func (s *messageAssetStore) GetContentVersion(_ context.Context, _ uuid.UUID, versionNumber int) (*store.ProjectContentVersion, error) {
	s.requestedVersion = versionNumber
	version := s.version
	return &version, nil
}
func (s *messageAssetStore) CreateContent(context.Context, store.CreateContentOptions) (*store.ProjectContent, error) {
	return nil, nil
}
func (s *messageAssetStore) UpdateContent(context.Context, uuid.UUID, store.UpdateContentOptions) (*store.ProjectContent, error) {
	return nil, nil
}
func (s *messageAssetStore) DuplicateContent(context.Context, uuid.UUID, store.DuplicateContentOptions) (*store.ProjectContent, error) {
	return nil, nil
}
func (s *messageAssetStore) RestoreContent(context.Context, uuid.UUID, int, store.RestoreContentOptions) (*store.ProjectContent, error) {
	return nil, nil
}
func (s *messageAssetStore) SoftDeleteContent(context.Context, uuid.UUID, store.SoftDeleteContentOptions) error {
	return nil
}
func (s *messageAssetStore) ListContentVersions(context.Context, uuid.UUID) ([]store.ProjectContentVersion, error) {
	return nil, nil
}
func (s *messageAssetStore) RecordContentAudit(context.Context, uuid.UUID, string, string, json.RawMessage) error {
	return nil
}

func projectRunForAssetTest() *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "team"},
		Spec: platformv1alpha1.AgentRunSpec{Context: &platformv1alpha1.AgentRunContext{
			ProjectRef: &platformv1alpha1.ProjectRef{Kind: "Project", Name: "briefs"},
		}},
	}
}

func TestMaterializeMessageAssetsWritesReferencedS3AssetIntoWorkspace(t *testing.T) {
	id := uuid.New()
	assetPath := "chat-attachments/run-1/image.png"
	stateStore := &messageAssetStore{
		item:    store.ProjectContent{ID: id, ProjectNamespace: "team", ProjectName: "briefs", Path: assetPath, CurrentVersion: 1, ScanStatus: store.ScanStatusClean},
		version: store.ProjectContentVersion{ContentID: id, Version: 1, Content: []byte("image bytes"), SHA256: "pinned-hash"},
	}
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	images := []sessionclient.MessageImage{{AssetID: id.String(), AssetVersion: 1, AssetSHA256: "pinned-hash", AssetPath: assetPath, ProjectName: "briefs"}}
	paths, err := materializeMessageAssets(context.Background(), workDir, projectRunForAssetTest(), stateStore, images)
	if err != nil {
		t.Fatalf("materializeMessageAssets() error = %v", err)
	}
	wantPath := ".gratefulagents/assets/chat-attachments/run-1/image.png"
	if len(paths) != 1 || paths[0] != wantPath {
		t.Fatalf("paths = %v, want [%s]", paths, wantPath)
	}
	if stateStore.requestedVersion != 1 {
		t.Fatalf("requested version = %d, want pinned version 1", stateStore.requestedVersion)
	}
	got, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(wantPath)))
	if err != nil || string(got) != "image bytes" {
		t.Fatalf("workspace asset = %q, %v", got, err)
	}
	exclude, err := os.ReadFile(filepath.Join(workDir, ".git", "info", "exclude"))
	if err != nil || !strings.Contains(string(exclude), messageAssetWorkspaceDir+"/") {
		t.Fatalf("Git exclude = %q, %v", exclude, err)
	}
	stateStore.item.CurrentVersion = 2
	stateStore.item.Path = "renamed.png"
	if _, err := materializeMessageAssets(context.Background(), workDir, projectRunForAssetTest(), stateStore, images); err != nil {
		t.Fatalf("materializing pinned version after asset update: %v", err)
	}
	if stateStore.requestedVersion != 1 {
		t.Fatalf("updated asset requested version = %d, want pinned version 1", stateStore.requestedVersion)
	}
	materializedPath := filepath.Join(workDir, filepath.FromSlash(wantPath))
	if err := os.Chmod(materializedPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(materializedPath, []byte("local edits"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeMessageAssets(context.Background(), workDir, projectRunForAssetTest(), stateStore, images); err == nil {
		t.Fatal("materialization overwrote a locally modified asset")
	}
	got, err = os.ReadFile(materializedPath)
	if err != nil || string(got) != "local edits" {
		t.Fatalf("modified asset = %q, %v; expected local edits to remain", got, err)
	}
}

func TestMaterializeMessageAssetsRejectsCrossProjectReference(t *testing.T) {
	id := uuid.New()
	stateStore := &messageAssetStore{
		item: store.ProjectContent{ID: id, ProjectNamespace: "team", ProjectName: "other", Path: "image.png", ScanStatus: store.ScanStatusClean},
	}
	_, err := materializeMessageAssets(context.Background(), t.TempDir(), projectRunForAssetTest(), stateStore, []sessionclient.MessageImage{{
		AssetID: id.String(), AssetVersion: 1, AssetSHA256: "pinned-hash", AssetPath: "image.png", ProjectName: "briefs",
	}})
	if err == nil {
		t.Fatal("materializeMessageAssets() accepted a cross-project asset")
	}
}

func TestEnsureMessageAssetExcludeDoesNotFollowGitSymlinkOutsideWorkspace(t *testing.T) {
	workDir := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(outside, "info", "exclude")
	if err := os.WriteFile(exclude, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workDir, ".git")); err != nil {
		t.Fatal(err)
	}
	ensureMessageAssetExclude(workDir)
	got, err := os.ReadFile(exclude)
	if err != nil || string(got) != "keep\n" {
		t.Fatalf("outside exclude = %q, %v", got, err)
	}
}

func TestAppendMessageAssetNotice(t *testing.T) {
	got := appendMessageAssetNotice("Inspect this", []string{".gratefulagents/assets/image.png"})
	if !strings.Contains(got, "Inspect this\n\nAttached project assets") || !strings.Contains(got, "`.gratefulagents/assets/image.png`") {
		t.Fatalf("notice = %q", got)
	}
}
