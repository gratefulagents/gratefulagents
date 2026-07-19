package dashboard

import (
	"context"
	"testing"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
)

func TestPersistMessageImageAssetsForProjectRun(t *testing.T) {
	server, stub := newContentServer(t)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-7", Namespace: "team"},
		Spec: platformv1alpha1.AgentRunSpec{Context: &platformv1alpha1.AgentRunContext{
			ProjectRef: &platformv1alpha1.ProjectRef{Kind: "Project", Name: "briefs"},
		}},
	}
	ctx := context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: "alice", Role: "admin"})
	images, err := server.persistMessageImageAssets(ctx, run, []sessionclient.MessageImage{{MediaType: "image/png", Data: "AQID"}})
	if err != nil {
		t.Fatalf("persistMessageImageAssets() error = %v", err)
	}
	if len(images) != 1 || images[0].AssetID != stub.item.ID.String() {
		t.Fatalf("images = %+v, want created asset ID %s", images, stub.item.ID)
	}
	if images[0].ProjectName != "briefs" || images[0].AssetPath == "" || images[0].AssetVersion != 1 || images[0].AssetSHA256 != "hash" {
		t.Fatalf("asset reference = %+v", images[0])
	}
	if stub.created.ProjectNamespace != "team" || stub.created.ProjectName != "briefs" || string(stub.created.Content) != string([]byte{1, 2, 3}) {
		t.Fatalf("create options = %+v", stub.created)
	}
	if stub.created.Actor != "alice" || stub.created.ScanStatus != "clean" {
		t.Fatalf("create actor/scan status = %+v", stub.created)
	}
}

type cleanupContextStore struct {
	*contentStoreStub
	getContextErr    error
	deleteContextErr error
}

func (s *cleanupContextStore) GetContent(ctx context.Context, id uuid.UUID) (*store.ProjectContent, error) {
	s.getContextErr = ctx.Err()
	return s.contentStoreStub.GetContent(ctx, id)
}

func (s *cleanupContextStore) SoftDeleteContent(ctx context.Context, id uuid.UUID, opts store.SoftDeleteContentOptions) error {
	s.deleteContextErr = ctx.Err()
	return s.contentStoreStub.SoftDeleteContent(ctx, id, opts)
}

func TestDeleteMessageImageAssetsUsesFreshBoundedContext(t *testing.T) {
	_, stub := newContentServer(t)
	stateStore := &cleanupContextStore{contentStoreStub: stub}
	server := &Server{stateStore: stateStore}
	actorCtx := context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: "alice"})
	cancelledCtx, cancel := context.WithCancel(actorCtx)
	cancel()

	server.deleteMessageImageAssets(cancelledCtx, []sessionclient.MessageImage{{
		AssetID: stub.item.ID.String(), AssetPath: stub.item.Path, ProjectName: stub.item.ProjectName,
	}})

	if stateStore.getContextErr != nil || stateStore.deleteContextErr != nil {
		t.Fatalf("cleanup reused cancelled context: get=%v delete=%v", stateStore.getContextErr, stateStore.deleteContextErr)
	}
	if !stub.deleted.Confirmed || stub.deleted.Actor != "alice" {
		t.Fatalf("delete options = %+v, want confirmed cleanup by alice", stub.deleted)
	}
}

func TestPersistMessageImageAssetsSkipsStandaloneRun(t *testing.T) {
	server, stub := newContentServer(t)
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run-7", Namespace: "team"}}
	input := []sessionclient.MessageImage{{MediaType: "image/png", Data: "AQID"}}
	images, err := server.persistMessageImageAssets(context.Background(), run, input)
	if err != nil {
		t.Fatalf("persistMessageImageAssets() error = %v", err)
	}
	if len(images) != 1 || images[0].AssetID != "" {
		t.Fatalf("standalone images = %+v", images)
	}
	if stub.created.Path != "" {
		t.Fatalf("standalone run unexpectedly created asset %q", stub.created.Path)
	}
}
