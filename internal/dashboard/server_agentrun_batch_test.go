package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// noBulkStore hides every method beyond store.StateStore (in particular
// ListResourceOwnersByType), forcing the per-run enrichment path.
type noBulkStore struct {
	store.StateStore
}

// countingBatchStore wraps collaborationStateStore and counts per-run vs bulk
// store calls made during list enrichment.
type countingBatchStore struct {
	*collaborationStateStore

	getSessionByRunCalls    int
	getResourceOwnerCalls   int
	getSharePermissionCalls int

	listSessionsCalls   int
	listOwnersCalls     int
	listSharedCalls     int
	latestActivityCalls int

	bulkErr error
}

func (c *countingBatchStore) GetSessionByRun(ctx context.Context, name, ns string) (*store.Session, error) {
	c.getSessionByRunCalls++
	return c.collaborationStateStore.GetSessionByRun(ctx, name, ns)
}

func (c *countingBatchStore) GetResourceOwner(ctx context.Context, resourceType, resourceID, resourceNS string) (*store.ResourceOwnership, error) {
	c.getResourceOwnerCalls++
	return c.collaborationStateStore.GetResourceOwner(ctx, resourceType, resourceID, resourceNS)
}

func (c *countingBatchStore) GetSharePermission(ctx context.Context, resourceType, resourceID, resourceNS, userID string) (*store.ResourceShare, error) {
	c.getSharePermissionCalls++
	return c.collaborationStateStore.GetSharePermission(ctx, resourceType, resourceID, resourceNS, userID)
}

func (c *countingBatchStore) ListSessionsByNamespace(ctx context.Context, namespace string) ([]store.Session, error) {
	c.listSessionsCalls++
	if c.bulkErr != nil {
		return nil, c.bulkErr
	}
	return c.collaborationStateStore.ListSessionsByNamespace(ctx, namespace)
}

func (c *countingBatchStore) ListResourceOwnersByType(ctx context.Context, resourceType string) ([]store.ResourceOwnership, error) {
	c.listOwnersCalls++
	if c.bulkErr != nil {
		return nil, c.bulkErr
	}
	return c.collaborationStateStore.ListResourceOwnersByType(ctx, resourceType)
}

func (c *countingBatchStore) ListSharedWithMe(ctx context.Context, userID, resourceType string) ([]store.ResourceShare, error) {
	c.listSharedCalls++
	if c.bulkErr != nil {
		return nil, c.bulkErr
	}
	return c.collaborationStateStore.ListSharedWithMe(ctx, userID, resourceType)
}

func (c *countingBatchStore) GetLatestActivityBySessions(_ context.Context, sessionIDs []uuid.UUID) (map[uuid.UUID]store.ActivityEvent, error) {
	c.latestActivityCalls++
	if c.bulkErr != nil {
		return nil, c.bulkErr
	}
	out := make(map[uuid.UUID]store.ActivityEvent)
	for _, sessionID := range sessionIDs {
		events := c.getRecentActivityBySession[sessionID]
		if len(events) > 0 {
			out[sessionID] = events[0]
		}
	}
	return out, nil
}

func batchTestRun(name, triggerKind, triggerName string) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{Kind: triggerKind, Name: triggerName},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
}

// newBatchTestState builds a collaborationStateStore holding sessions, owners,
// shares, and a trigger owner for three runs:
//   - run-a: owned by alice, has a session with a pending question
//   - run-b: owned by bob, shared with alice as viewer, has a plain session
//   - run-c: no run owner; Cron trigger "nightly" owned by carol
func newBatchTestState(t *testing.T) *collaborationStateStore {
	t.Helper()
	ms := newCollaborationStateStore()
	sessA, err := ms.CreateSession(context.Background(), "run-a", "default", "running", "implement")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := ms.SetPendingQuestion(context.Background(), sessA.ID, "running", "which db?", "question"); err != nil {
		t.Fatalf("SetPendingQuestion: %v", err)
	}
	ms.getRecentActivityBySession = map[uuid.UUID][]store.ActivityEvent{
		sessA.ID: {{SessionID: sessA.ID, EventType: "tool_use", Summary: "edited schema", CreatedAt: time.Unix(200, 0)}},
	}
	if _, err := ms.CreateSession(context.Background(), "run-b", "default", "running", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	addCollaborationOwner(t, ms, "agent_run", "default", "run-a", "alice")
	addCollaborationOwner(t, ms, "agent_run", "default", "run-b", "bob")
	addCollaborationOwner(t, ms, "cron", "default", "nightly", "carol")
	addCollaborationShare(ms, "share-1", "agent_run", "default", "run-b", "alice", "bob", "viewer")
	return ms
}

func newBatchTestServer(t *testing.T, ss store.StateStore) *Server {
	t.Helper()
	scheme := newDashboardTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		batchTestRun("run-a", "ProjectChat", "chat"),
		batchTestRun("run-b", "ProjectChat", "chat"),
		batchTestRun("run-c", "Cron", "nightly"),
	).Build()
	return &Server{
		k8sClient:  c,
		scheme:     scheme,
		stateStore: ss,
		authStore: &collaborationAuthStore{users: []*auth.User{
			{ID: "alice", Email: "alice@example.com", Name: "Alice", Picture: "alice.png"},
			{ID: "bob", Email: "bob@example.com", Name: "Bob"},
			{ID: "carol", Email: "carol@example.com", Name: "Carol"},
		}},
	}
}

func TestListAgentRunsBatchMatchesPerRunEnrichment(t *testing.T) {
	batchSrv := newBatchTestServer(t, newBatchTestState(t))
	perRunSrv := newBatchTestServer(t, noBulkStore{newBatchTestState(t)})

	ctx := actorContext("alice", "member", "", "")
	batchResp, err := batchSrv.ListAgentRuns(ctx, &platform.ListAgentRunsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListAgentRuns (batch): %v", err)
	}
	perRunResp, err := perRunSrv.ListAgentRuns(ctx, &platform.ListAgentRunsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListAgentRuns (per-run): %v", err)
	}

	if len(batchResp.Runs) != 2 || len(perRunResp.Runs) != 2 {
		t.Fatalf("run counts = %d (batch), %d (per-run), want 2", len(batchResp.Runs), len(perRunResp.Runs))
	}
	for i, want := range perRunResp.Runs {
		got := batchResp.Runs[i]
		if !proto.Equal(got, want) {
			t.Errorf("run %d differs:\nbatch:   %v\nper-run: %v", i, got, want)
		}
	}

	// Spot-check the semantics both paths must agree on.
	byName := map[string]*platform.AgentRun{}
	for _, r := range batchResp.Runs {
		byName[r.Name] = r
	}
	if r := byName["run-a"]; r.GetMyPermission() != "owner" || r.GetOwner().GetName() != "Alice" {
		t.Errorf("run-a = perm %q owner %v, want owner/Alice", r.GetMyPermission(), r.GetOwner())
	}
	if r := byName["run-a"]; r.GetUserInputRequest().GetMessage() != "which db?" {
		t.Errorf("run-a user input = %v, want pending question", r.GetUserInputRequest())
	}
	if r := byName["run-a"]; len(r.GetRecentActivity()) != 1 || r.GetRecentActivity()[0].GetSummary() != "edited schema" {
		t.Errorf("run-a recent activity = %v, want latest edited-schema event", r.GetRecentActivity())
	}
	if r := byName["run-b"]; r.GetMyPermission() != "viewer" || r.GetOwner().GetName() != "Bob" {
		t.Errorf("run-b = perm %q owner %v, want viewer/Bob", r.GetMyPermission(), r.GetOwner())
	}
	if byName["run-c"] != nil {
		t.Errorf("run-c should be hidden because its trigger is owned by carol")
	}
}

func TestListAgentRunsUsesBulkQueriesNotPerRunLookups(t *testing.T) {
	cs := &countingBatchStore{collaborationStateStore: newBatchTestState(t)}
	srv := newBatchTestServer(t, cs)

	resp, err := srv.ListAgentRuns(actorContext("alice", "member", "", ""), &platform.ListAgentRunsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListAgentRuns: %v", err)
	}
	if len(resp.Runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(resp.Runs))
	}

	if cs.getSessionByRunCalls != 0 {
		t.Errorf("GetSessionByRun calls = %d, want 0", cs.getSessionByRunCalls)
	}
	if cs.getResourceOwnerCalls != 0 {
		t.Errorf("GetResourceOwner calls = %d, want 0", cs.getResourceOwnerCalls)
	}
	if cs.getSharePermissionCalls != 0 {
		t.Errorf("GetSharePermission calls = %d, want 0", cs.getSharePermissionCalls)
	}
	if cs.listSessionsCalls != 1 {
		t.Errorf("ListSessionsByNamespace calls = %d, want 1", cs.listSessionsCalls)
	}
	if cs.latestActivityCalls != 1 {
		t.Errorf("GetLatestActivityBySessions calls = %d, want 1", cs.latestActivityCalls)
	}
	// Visibility and enrichment each load agent_run plus every distinct
	// trigger resource type — independent of run count.
	if want := 2 + 2*len(agentRunTriggerResourceTypes); cs.listOwnersCalls != want {
		t.Errorf("ListResourceOwnersByType calls = %d, want %d", cs.listOwnersCalls, want)
	}
	// One for the visibility filter, one for the batch.
	if cs.listSharedCalls != 2 {
		t.Errorf("ListSharedWithMe calls = %d, want 2", cs.listSharedCalls)
	}
}

func TestListAgentRunsFallsBackToPerRunWhenBulkUnavailable(t *testing.T) {
	check := func(t *testing.T, srv *Server, wantPerRun bool, cs *countingBatchStore) {
		t.Helper()
		resp, err := srv.ListAgentRuns(actorContext("alice", "member", "", ""), &platform.ListAgentRunsRequest{Namespace: "default"})
		if err != nil {
			t.Fatalf("ListAgentRuns: %v", err)
		}
		byName := map[string]*platform.AgentRun{}
		for _, r := range resp.Runs {
			byName[r.Name] = r
		}
		if r := byName["run-a"]; r.GetMyPermission() != "owner" || r.GetOwner().GetUserId() != "alice" {
			t.Errorf("run-a = perm %q owner %v, want owner/alice", r.GetMyPermission(), r.GetOwner())
		}
		if r := byName["run-b"]; r.GetMyPermission() != "viewer" || r.GetOwner().GetUserId() != "bob" {
			t.Errorf("run-b = perm %q owner %v, want viewer/bob", r.GetMyPermission(), r.GetOwner())
		}
		if byName["run-c"] != nil {
			t.Errorf("run-c should be hidden because its trigger is owned by carol")
		}
		if wantPerRun && cs != nil && cs.getSessionByRunCalls == 0 {
			t.Errorf("expected per-run GetSessionByRun fallback calls, got 0")
		}
	}

	t.Run("store without bulk interface", func(t *testing.T) {
		srv := newBatchTestServer(t, noBulkStore{newBatchTestState(t)})
		check(t, srv, false, nil)
	})

	t.Run("bulk queries erroring", func(t *testing.T) {
		cs := &countingBatchStore{
			collaborationStateStore: newBatchTestState(t),
			bulkErr:                 errors.New("bulk unavailable"),
		}
		srv := newBatchTestServer(t, cs)
		check(t, srv, true, cs)
	})
}
