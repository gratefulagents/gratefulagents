package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

// fakeBugReportStore is an in-memory store.AgentBugReportStore keyed by
// (namespace, fingerprint), mirroring the Postgres dedupe semantics the tool
// depends on.
type fakeBugReportStore struct {
	reports map[string]*store.AgentBugReportRecord
	err     error
}

func newFakeBugReportStore() *fakeBugReportStore {
	return &fakeBugReportStore{reports: map[string]*store.AgentBugReportRecord{}}
}

func (f *fakeBugReportStore) UpsertAgentBugReport(_ context.Context, rec *store.AgentBugReportRecord) (*store.AgentBugReportRecord, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	key := rec.Namespace + "/" + rec.Fingerprint
	if existing, ok := f.reports[key]; ok {
		existing.Occurrences++
		existing.RunName = rec.RunName
		existing.SessionID = rec.SessionID
		existing.Body = rec.Body
		if existing.Status == store.AgentBugReportStatusResolved {
			existing.Status = store.AgentBugReportStatusOpen
		}
		out := *existing
		return &out, false, nil
	}
	stored := *rec
	stored.ID = uuid.New()
	stored.Occurrences = 1
	stored.Status = store.AgentBugReportStatusOpen
	f.reports[key] = &stored
	out := stored
	return &out, true, nil
}

func (f *fakeBugReportStore) GetAgentBugReport(_ context.Context, namespace string, id uuid.UUID) (*store.AgentBugReportRecord, error) {
	for _, rec := range f.reports {
		if rec.Namespace == namespace && rec.ID == id {
			out := *rec
			return &out, nil
		}
	}
	return nil, nil
}

func (f *fakeBugReportStore) ListAgentBugReports(_ context.Context, filter store.AgentBugReportFilter) ([]store.AgentBugReportRecord, error) {
	var out []store.AgentBugReportRecord
	for _, rec := range f.reports {
		if rec.Namespace == filter.Namespace {
			out = append(out, *rec)
		}
	}
	return out, nil
}

func (f *fakeBugReportStore) SetAgentBugReportStatus(_ context.Context, namespace string, id uuid.UUID, status, actor, note string) error {
	for _, rec := range f.reports {
		if rec.Namespace == namespace && rec.ID == id {
			rec.Status, rec.StatusActor, rec.StatusNote = status, actor, note
			return nil
		}
	}
	return store.ErrAgentBugReportNotFound
}

func executeReportBug(t *testing.T, tool Tool, input string) map[string]any {
	t.Helper()
	result, err := tool.Execute(context.Background(), json.RawMessage(input), "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned tool error: %s", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("decoding result %q: %v", result.Content, err)
	}
	return payload
}

func TestReportBugToolRegistration(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	RegisterReportBugTool(registry, nil, "ns", "run-1", uuid.New())
	if registry.Get(reportBugToolName) != nil {
		t.Fatal("report_bug must stay unregistered without a store")
	}
	RegisterReportBugTool(registry, newFakeBugReportStore(), "ns", "run-1", uuid.New())
	if registry.Get(reportBugToolName) == nil {
		t.Fatal("report_bug not registered with a store")
	}
}

func TestReportBugCreateAndDedupe(t *testing.T) {
	fake := newFakeBugReportStore()
	sessionID := uuid.New()
	tool := &reportBugTool{store: fake, namespace: "ns", runName: "run-1", sessionID: sessionID}

	payload := executeReportBug(t, tool, `{"title":"ApplyPatch fails on rename hunks","body":"Expected the patch to apply, got 'invalid hunk header' on every rename.","category":"bug","tool_name":"ApplyPatch"}`)
	if payload["created"] != true || payload["occurrences"] != float64(1) {
		t.Fatalf("first report: got %v, want created=true occurrences=1", payload)
	}
	if len(fake.reports) != 1 {
		t.Fatalf("stored reports = %d, want 1", len(fake.reports))
	}
	for _, rec := range fake.reports {
		if rec.Namespace != "ns" || rec.RunName != "run-1" || rec.SessionID == nil || *rec.SessionID != sessionID {
			t.Fatalf("stored record missing run identity: %+v", rec)
		}
	}

	// Same title from another run (whitespace/case variations) merges.
	tool2 := &reportBugTool{store: fake, namespace: "ns", runName: "run-2", sessionID: uuid.New()}
	payload = executeReportBug(t, tool2, `{"title":"  applypatch FAILS   on rename hunks ","body":"Still broken: rename hunks are rejected with 'invalid hunk header'.","category":"bug","tool_name":"applypatch"}`)
	if payload["duplicate"] != true || payload["occurrences"] != float64(2) {
		t.Fatalf("second report: got %v, want duplicate=true occurrences=2", payload)
	}
	if len(fake.reports) != 1 {
		t.Fatalf("stored reports after dedupe = %d, want 1", len(fake.reports))
	}
}

func TestReportBugDefaultsAndValidation(t *testing.T) {
	fake := newFakeBugReportStore()
	tool := &reportBugTool{store: fake, namespace: "ns", runName: "run-1"}

	// Category defaults to bug; feature and complaint are accepted.
	executeReportBug(t, tool, `{"title":"Terminal tool loses scrollback","body":"Scrollback disappears after every command, making output unreadable."}`)
	for _, rec := range fake.reports {
		if rec.Category != store.AgentBugReportCategoryBug {
			t.Fatalf("default category = %q, want bug", rec.Category)
		}
	}
	executeReportBug(t, tool, `{"title":"Wish: allow attaching files to task comments","body":"Task comments cannot carry artifacts, so handoffs lose evidence.","category":"feature"}`)

	for name, input := range map[string]string{
		"short title":      `{"title":"short","body":"long enough body describing the problem in detail"}`,
		"short body":       `{"title":"A real stable title","body":"too short"}`,
		"invalid category": `{"title":"A real stable title","body":"long enough body describing the problem","category":"rant"}`,
		"long tool name":   `{"title":"A real stable title","body":"long enough body describing the problem","tool_name":"` + strings.Repeat("x", 121) + `"}`,
		"bad json":         `{`,
	} {
		result, err := tool.Execute(context.Background(), json.RawMessage(input), "")
		if err != nil {
			t.Fatalf("%s: Execute() error = %v", name, err)
		}
		if !result.IsError {
			t.Fatalf("%s: expected tool error, got %q", name, result.Content)
		}
	}
}

func TestBugReportFingerprint(t *testing.T) {
	a := BugReportFingerprint("bug", "ApplyPatch", "Patch  fails ON rename")
	b := BugReportFingerprint("bug", "applypatch", "patch fails on rename")
	if a != b {
		t.Fatal("fingerprint must normalize case and whitespace")
	}
	if a == BugReportFingerprint("complaint", "ApplyPatch", "Patch fails on rename") {
		t.Fatal("different categories must not collide")
	}
	if a == BugReportFingerprint("bug", "", "Patch fails on rename") {
		t.Fatal("different tool names must not collide")
	}
}
