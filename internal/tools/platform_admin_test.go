package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type platformActivityStore struct {
	*planToolStore
	session  *store.Session
	activity []store.ActivityEvent
	limit    int32
}

func (s *platformActivityStore) GetSessionByRun(_ context.Context, name, namespace string) (*store.Session, error) {
	if name != s.session.AgentRunName || namespace != s.session.AgentRunNS {
		return nil, context.Canceled
	}
	return s.session, nil
}

func (s *platformActivityStore) GetRecentActivity(_ context.Context, sessionID uuid.UUID, limit int32) ([]store.ActivityEvent, error) {
	s.limit = limit
	if sessionID != s.session.ID {
		return nil, context.Canceled
	}
	return s.activity, nil
}

func TestRegisterPlatformAdminToolsReadOnly(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	registry := NewRegistry(t.TempDir(), WithReadOnlyTools())
	RegisterPlatformAdminTools(registry, crfake.NewClientBuilder().WithScheme(scheme).Build(), k8sfake.NewSimpleClientset(), "default")

	for _, name := range []string{"platform_list_runs", "platform_get_run", "platform_run_activity", "platform_run_trace", "platform_list_pods", "platform_pod_logs"} {
		tool := registry.Get(name)
		if tool == nil {
			t.Fatalf("registry missing %s", name)
		}
		if !tool.IsReadOnly() {
			t.Fatalf("tool %s IsReadOnly=false, want true", name)
		}
	}
}

func TestDecodeRecentJSONLLinesReturnsLatestChronologically(t *testing.T) {
	t.Parallel()

	events, truncated := decodeRecentJSONLLines([]string{
		`{"message":"oldest"}`,
		`{"message":"middle"}`,
		`{"message":"newest"}`,
	}, 2, false)
	if !truncated || len(events) != 2 {
		t.Fatalf("decodeRecentJSONLLines() = (%+v, %v), want two truncated events", events, truncated)
	}
	first := events[0].(map[string]any)
	second := events[1].(map[string]any)
	if first["message"] != "middle" || second["message"] != "newest" {
		t.Fatalf("events = %+v, want latest window in chronological order", events)
	}
}

func TestPostgresActivityEventsNormalizesDetails(t *testing.T) {
	t.Parallel()

	createdAt := time.Unix(4, 0).UTC()
	events, truncated := postgresActivityEvents([]store.ActivityEvent{{
		ID: 4, EventType: "tool_result", Summary: "fallback summary",
		Detail: json.RawMessage(`null`), CreatedAt: createdAt,
	}}, 1)
	if truncated || len(events) != 1 {
		t.Fatalf("postgresActivityEvents() = (%+v, %v), want one event", events, truncated)
	}
	event := events[0].(map[string]any)
	if event["type"] != "tool_result" || event["message"] != "fallback summary" || event["ts"] != createdAt {
		t.Fatalf("normalized event = %+v", event)
	}
}

func TestPlatformRunActivityFallsBackToPostgres(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run-a", Namespace: "default"}}
	crdClient := crfake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	sessionID := uuid.New()
	stateStore := &platformActivityStore{
		planToolStore: newPlanToolStore(),
		session:       &store.Session{ID: sessionID, AgentRunName: "run-a", AgentRunNS: "default"},
		activity: []store.ActivityEvent{
			{ID: 3, SessionID: sessionID, EventType: "assistant", Detail: json.RawMessage(`{"type":"assistant","message":"newest"}`), CreatedAt: time.Unix(3, 0)},
			{ID: 2, SessionID: sessionID, EventType: "tool", Detail: json.RawMessage(`{"type":"tool","message":"middle"}`), CreatedAt: time.Unix(2, 0)},
			{ID: 1, SessionID: sessionID, EventType: "user", Detail: json.RawMessage(`{"type":"user","message":"oldest"}`), CreatedAt: time.Unix(1, 0)},
		},
	}
	tool := &platformRunActivityTool{platformAdminToolBase: platformAdminToolBase{
		crdClient: crdClient, stateStore: stateStore, currentNamespace: "default",
	}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"run-a","limit":2}`), "")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%+v, %v), want success", result, err)
	}
	if stateStore.limit != 3 {
		t.Fatalf("GetRecentActivity limit = %d, want 3", stateStore.limit)
	}
	var got struct {
		Source    string           `json:"source"`
		Events    []map[string]any `json:"events"`
		Truncated bool             `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(result.Content), &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got.Source != "postgres" || !got.Truncated || len(got.Events) != 2 {
		t.Fatalf("result = %+v, want postgres source with two truncated events", got)
	}
	if got.Events[0]["message"] != "middle" || got.Events[1]["message"] != "newest" {
		t.Fatalf("events = %+v, want selected window in chronological order", got.Events)
	}
}
