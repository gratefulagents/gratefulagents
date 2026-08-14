package dashboard

import (
	"context"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// mockBugReportStore layers an in-memory store.AgentBugReportStore on the
// base mock state store so the bug report handlers see a capable backend.
type mockBugReportStore struct {
	*mockStateStore
	mu      sync.Mutex
	reports map[uuid.UUID]*store.AgentBugReportRecord
}

func newMockBugReportStore() *mockBugReportStore {
	return &mockBugReportStore{
		mockStateStore: newMockStateStore(),
		reports:        map[uuid.UUID]*store.AgentBugReportRecord{},
	}
}

func (m *mockBugReportStore) UpsertAgentBugReport(_ context.Context, rec *store.AgentBugReportRecord) (*store.AgentBugReportRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := *rec
	stored.ID = uuid.New()
	if stored.Status == "" {
		stored.Status = store.AgentBugReportStatusOpen
	}
	if stored.Occurrences == 0 {
		stored.Occurrences = 1
	}
	m.reports[stored.ID] = &stored
	out := stored
	return &out, true, nil
}

func (m *mockBugReportStore) GetAgentBugReport(_ context.Context, namespace string, id uuid.UUID) (*store.AgentBugReportRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.reports[id]
	if !ok || rec.Namespace != namespace {
		return nil, nil
	}
	out := *rec
	return &out, nil
}

func (m *mockBugReportStore) ListAgentBugReports(_ context.Context, f store.AgentBugReportFilter) ([]store.AgentBugReportRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.AgentBugReportRecord
	for _, rec := range m.reports {
		if rec.Namespace != f.Namespace {
			continue
		}
		if f.Status != "" && rec.Status != f.Status {
			continue
		}
		if f.Category != "" && rec.Category != f.Category {
			continue
		}
		out = append(out, *rec)
	}
	return out, nil
}

func (m *mockBugReportStore) SetAgentBugReportStatus(_ context.Context, namespace string, id uuid.UUID, status, actor, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.reports[id]
	if !ok || rec.Namespace != namespace {
		return store.ErrAgentBugReportNotFound
	}
	rec.Status, rec.StatusActor, rec.StatusNote = status, actor, note
	return nil
}

var _ store.AgentBugReportStore = (*mockBugReportStore)(nil)

func seedBugReport(t *testing.T, m *mockBugReportStore, namespace, title string) *store.AgentBugReportRecord {
	t.Helper()
	rec, _, err := m.UpsertAgentBugReport(context.Background(), &store.AgentBugReportRecord{
		Namespace:   namespace,
		RunName:     "run-1",
		Category:    store.AgentBugReportCategoryBug,
		Title:       title,
		Body:        "expected X, got Y",
		Fingerprint: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("seeding bug report: %v", err)
	}
	return rec
}

func TestBugReportHandlersRequireCapableStore(t *testing.T) {
	srv := newSecurityTestServer(t, newMockStateStore())
	ctx := actorContext("alice", "admin", "", "")
	if _, err := srv.ListBugReports(ctx, &platform.ListBugReportsRequest{Namespace: "default"}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("ListBugReports error = %v, want FailedPrecondition", err)
	}
	if _, err := srv.UpdateBugReportStatus(ctx, &platform.UpdateBugReportStatusRequest{Namespace: "default", Id: uuid.NewString(), Status: "resolved"}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("UpdateBugReportStatus error = %v, want FailedPrecondition", err)
	}
}

func TestListBugReports(t *testing.T) {
	mock := newMockBugReportStore()
	srv := newSecurityTestServer(t, mock)
	ctx := actorContext("alice", "admin", "", "")
	seedBugReport(t, mock, "default", "ApplyPatch fails on rename hunks")
	other := seedBugReport(t, mock, "other-ns", "unrelated report")

	resp, err := srv.ListBugReports(ctx, &platform.ListBugReportsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListBugReports() error = %v", err)
	}
	if len(resp.GetReports()) != 1 {
		t.Fatalf("reports = %d, want 1 (namespace-scoped)", len(resp.GetReports()))
	}
	got := resp.GetReports()[0]
	if got.GetTitle() != "ApplyPatch fails on rename hunks" || got.GetStatus() != "open" || got.GetOccurrences() != 1 {
		t.Fatalf("unexpected report: %+v", got)
	}
	if got.GetId() == other.ID.String() {
		t.Fatal("report from another namespace leaked")
	}

	if _, err := srv.ListBugReports(ctx, &platform.ListBugReportsRequest{Namespace: "default", Status: "bogus"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid status filter error = %v, want InvalidArgument", err)
	}
	if _, err := srv.ListBugReports(ctx, &platform.ListBugReportsRequest{Namespace: "default", Category: "bogus"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid category filter error = %v, want InvalidArgument", err)
	}
}

func TestUpdateBugReportStatus(t *testing.T) {
	mock := newMockBugReportStore()
	srv := newSecurityTestServer(t, mock)
	ctx := actorContext("alice", "admin", "", "")
	rec := seedBugReport(t, mock, "default", "ApplyPatch fails on rename hunks")

	updated, err := srv.UpdateBugReportStatus(ctx, &platform.UpdateBugReportStatusRequest{
		Namespace: "default", Id: rec.ID.String(), Status: "resolved", Note: "fixed in v1.2",
	})
	if err != nil {
		t.Fatalf("UpdateBugReportStatus() error = %v", err)
	}
	if updated.GetStatus() != "resolved" || updated.GetStatusNote() != "fixed in v1.2" || updated.GetStatusActor() != "alice" {
		t.Fatalf("unexpected updated report: %+v", updated)
	}

	if _, err := srv.UpdateBugReportStatus(ctx, &platform.UpdateBugReportStatusRequest{Namespace: "default", Id: rec.ID.String(), Status: "bogus"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid status error = %v, want InvalidArgument", err)
	}
	if _, err := srv.UpdateBugReportStatus(ctx, &platform.UpdateBugReportStatusRequest{Namespace: "default", Id: "not-a-uuid", Status: "resolved"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid id error = %v, want InvalidArgument", err)
	}
	if _, err := srv.UpdateBugReportStatus(ctx, &platform.UpdateBugReportStatusRequest{Namespace: "default", Id: uuid.NewString(), Status: "resolved"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing report error = %v, want NotFound", err)
	}
	// A report in another namespace must be indistinguishable from a missing one.
	other := seedBugReport(t, mock, "other-ns", "unrelated report")
	if _, err := srv.UpdateBugReportStatus(ctx, &platform.UpdateBugReportStatusRequest{Namespace: "default", Id: other.ID.String(), Status: "resolved"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("cross-namespace update error = %v, want NotFound", err)
	}
}
