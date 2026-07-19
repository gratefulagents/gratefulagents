package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

type contentStoreStub struct {
	store.StateStore
	created store.CreateContentOptions
	item    store.ProjectContent
	version store.ProjectContentVersion
	deleted store.SoftDeleteContentOptions
}

func (s *contentStoreStub) ListContent(context.Context, string, string, bool) ([]store.ProjectContent, error) {
	return []store.ProjectContent{s.item}, nil
}
func (s *contentStoreStub) GetContent(context.Context, uuid.UUID) (*store.ProjectContent, error) {
	return &s.item, nil
}
func (s *contentStoreStub) GetContentVersion(context.Context, uuid.UUID, int) (*store.ProjectContentVersion, error) {
	return &s.version, nil
}
func (s *contentStoreStub) CreateContent(_ context.Context, opts store.CreateContentOptions) (*store.ProjectContent, error) {
	s.created = opts
	s.item.ProjectNamespace = opts.ProjectNamespace
	s.item.ProjectName = opts.ProjectName
	s.item.Kind = opts.Kind
	s.item.Path = opts.Path
	s.item.MediaType = opts.MediaType
	s.item.CurrentVersion = 1
	s.item.ContentHash = "hash"
	s.item.ScanStatus = opts.ScanStatus
	return &s.item, nil
}
func (s *contentStoreStub) UpdateContent(context.Context, uuid.UUID, store.UpdateContentOptions) (*store.ProjectContent, error) {
	return &s.item, nil
}
func (s *contentStoreStub) DuplicateContent(context.Context, uuid.UUID, store.DuplicateContentOptions) (*store.ProjectContent, error) {
	return &s.item, nil
}
func (s *contentStoreStub) RestoreContent(context.Context, uuid.UUID, int, store.RestoreContentOptions) (*store.ProjectContent, error) {
	return &s.item, nil
}
func (s *contentStoreStub) SoftDeleteContent(_ context.Context, _ uuid.UUID, opts store.SoftDeleteContentOptions) error {
	s.deleted = opts
	if !opts.Confirmed {
		return store.ErrContentConfirmationRequired
	}
	return nil
}
func (s *contentStoreStub) ListContentVersions(context.Context, uuid.UUID) ([]store.ProjectContentVersion, error) {
	return []store.ProjectContentVersion{s.version}, nil
}
func (s *contentStoreStub) RecordContentAudit(context.Context, uuid.UUID, string, string, json.RawMessage) error {
	return nil
}

func newContentServer(t *testing.T) (*Server, *contentStoreStub) {
	t.Helper()
	id := uuid.New()
	stub := &contentStoreStub{
		item:    store.ProjectContent{ID: id, ProjectNamespace: "team", ProjectName: "briefs", Kind: store.ProjectContentKindFile, Path: "notes/readme.md", MediaType: "text/markdown", CurrentVersion: 1},
		version: store.ProjectContentVersion{ContentID: id, Version: 1, Content: []byte("# Notes"), SHA256: "hash", Size: 7, Creator: "alice"},
	}
	scheme := testProjectScheme(t)
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "briefs", Namespace: "team"},
	}).Build()
	return &Server{stateStore: stub, k8sClient: k8s, scheme: scheme}, stub
}

func TestCreateProjectContentValidatesAndScans(t *testing.T) {
	server, stub := newContentServer(t)
	ctx := context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: "alice", Role: "admin"})
	got, err := server.CreateProjectContent(ctx, &platform.CreateProjectContentRequest{Namespace: "team", ProjectName: "briefs", Kind: "file", Path: "notes/readme.md", MediaType: "text/markdown; charset=utf-8", Content: []byte("# Notes"), MetadataJson: `{"title":"Notes"}`})
	if err != nil {
		t.Fatalf("CreateProjectContent: %v", err)
	}
	if got.Path != "notes/readme.md" || stub.created.Actor != "alice" || stub.created.ScanStatus != "clean" {
		t.Fatalf("unexpected result: %#v opts=%#v", got, stub.created)
	}
	if stub.created.MediaType != "text/markdown" {
		t.Fatalf("media type = %q", stub.created.MediaType)
	}
}

func TestCreateProjectContentRejectsDisallowedAndMalware(t *testing.T) {
	server, _ := newContentServer(t)
	ctx := context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Role: "admin"})
	for _, tc := range []struct {
		name    string
		path    string
		content []byte
	}{
		{name: "executable", path: "bad.exe", content: []byte("MZ")},
		{name: "eicar", path: "test.txt", content: eicarSignature},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := server.CreateProjectContent(ctx, &platform.CreateProjectContentRequest{Namespace: "team", ProjectName: "briefs", Kind: "file", Path: tc.path, Content: tc.content})
			if err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestDeleteProjectContentRequiresConfirmation(t *testing.T) {
	server, stub := newContentServer(t)
	ctx := context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: "alice", Role: "admin"})
	_, err := server.DeleteProjectContent(ctx, &platform.DeleteProjectContentRequest{Namespace: "team", ProjectName: "briefs", Id: stub.item.ID.String()})
	if err == nil || !errors.Is(err, store.ErrContentConfirmationRequired) {
		t.Fatalf("error = %v", err)
	}
	_, err = server.DeleteProjectContent(ctx, &platform.DeleteProjectContentRequest{Namespace: "team", ProjectName: "briefs", Id: stub.item.ID.String(), Confirm: true})
	if err != nil {
		t.Fatalf("confirmed delete: %v", err)
	}
	if !stub.deleted.Confirmed || stub.deleted.Actor != "alice" {
		t.Fatalf("delete opts = %#v", stub.deleted)
	}
}

func TestProjectContentRequiresExistingProject(t *testing.T) {
	server, _ := newContentServer(t)
	ctx := context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: "alice", Role: "admin"})
	_, err := server.CreateProjectContent(ctx, &platform.CreateProjectContentRequest{Namespace: "team", ProjectName: "missing", Kind: "file", Path: "notes/readme.md", Content: []byte("# Notes")})
	if err == nil {
		t.Fatal("expected rejection for nonexistent project")
	}
}
