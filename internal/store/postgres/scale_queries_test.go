package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

func TestListSessionsByRuns(t *testing.T) {
	s := setupTestStore(t)
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	a, _ := s.CreateSession(ctx, "by-runs-a", "default", "running", "")
	b, _ := s.CreateSession(ctx, "by-runs-b", "default", "running", "")
	_, _ = s.CreateSession(ctx, "by-runs-c", "default", "running", "")
	_, _ = s.CreateSession(ctx, "by-runs-a", "other", "running", "") // same name, other namespace

	lister, ok := s.(store.SessionsByRunsLister)
	if !ok {
		t.Fatal("store does not implement SessionsByRunsLister")
	}
	got, err := lister.ListSessionsByRuns(ctx, []store.AgentRunKey{
		{Namespace: "default", Name: "by-runs-a"},
		{Namespace: "default", Name: "by-runs-b"},
		{Namespace: "default", Name: "missing"},
	})
	if err != nil {
		t.Fatalf("ListSessionsByRuns: %v", err)
	}
	ids := map[uuid.UUID]bool{}
	for _, sess := range got {
		ids[sess.ID] = true
	}
	if len(got) != 2 || !ids[a.ID] || !ids[b.ID] {
		t.Fatalf("ListSessionsByRuns = %d sessions %v, want exactly a and b", len(got), ids)
	}
	if empty, err := lister.ListSessionsByRuns(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("ListSessionsByRuns(nil) = (%v, %v), want empty", empty, err)
	}
}

func TestListSessionMetricsByNamespace(t *testing.T) {
	s := setupTestStore(t)
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	withMetrics := func(name, ns string, cost float64) {
		sess, _ := s.CreateSession(ctx, name, ns, "running", "")
		meta, _ := json.Marshal(map[string]any{
			"metrics": map[string]any{"cost_usd": cost, "input_tokens": 10, "output_tokens": 5, "tool_call_count": 2},
			"other":   "ignored",
		})
		if err := s.UpdateMetadata(ctx, sess.ID, meta); err != nil {
			t.Fatalf("UpdateMetadata: %v", err)
		}
	}
	withMetrics("metrics-a", "default", 1.25)
	withMetrics("metrics-b", "other", 2.5)
	noMetrics, _ := s.CreateSession(ctx, "metrics-none", "default", "running", "")
	if err := s.UpdateMetadata(ctx, noMetrics.ID, json.RawMessage(`{"other":"x"}`)); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}

	lister, ok := s.(store.SessionMetricsByNamespaceLister)
	if !ok {
		t.Fatal("store does not implement SessionMetricsByNamespaceLister")
	}
	scoped, err := lister.ListSessionMetricsByNamespace(ctx, "default")
	if err != nil {
		t.Fatalf("ListSessionMetricsByNamespace(default): %v", err)
	}
	if len(scoped) != 1 || scoped[0].AgentRunName != "metrics-a" || scoped[0].CostUSD != 1.25 || scoped[0].InputTokens != 10 || scoped[0].ToolCallCount != 2 {
		t.Fatalf("scoped metrics = %+v, want only metrics-a", scoped)
	}
	all, err := s.ListAllSessionMetrics(ctx)
	if err != nil {
		t.Fatalf("ListAllSessionMetrics: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all metrics = %+v, want both namespaces", all)
	}
}

func TestGetActivityEventAndLatestSummaryOmitsDetail(t *testing.T) {
	s := setupTestStore(t)
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	sess, _ := s.CreateSession(ctx, "event-get", "default", "running", "")
	other, _ := s.CreateSession(ctx, "event-get-other", "default", "running", "")
	detail := json.RawMessage(`{"type":"tool_end","output":"big payload"}`)
	ev, err := s.WriteActivityEvent(ctx, sess.ID, "tool_end", "ran ls", detail)
	if err != nil {
		t.Fatalf("WriteActivityEvent: %v", err)
	}

	getter, ok := s.(store.ActivityEventGetter)
	if !ok {
		t.Fatal("store does not implement ActivityEventGetter")
	}
	got, err := getter.GetActivityEvent(ctx, sess.ID, ev.ID)
	if err != nil {
		t.Fatalf("GetActivityEvent: %v", err)
	}
	var gotDetail map[string]any
	if err := json.Unmarshal(got.Detail, &gotDetail); err != nil {
		t.Fatalf("GetActivityEvent detail = %q: %v", got.Detail, err)
	}
	if got.Summary != "ran ls" || gotDetail["output"] != "big payload" {
		t.Fatalf("GetActivityEvent = %+v, want the written row with detail", got)
	}
	// Scoped to the session: another session cannot read the row.
	if _, err := getter.GetActivityEvent(ctx, other.ID, ev.ID); !errors.Is(err, store.ErrActivityEventNotFound) {
		t.Fatalf("GetActivityEvent(other session) error = %v, want ErrActivityEventNotFound", err)
	}

	bulk, ok := s.(interface {
		GetLatestActivityBySessions(context.Context, []uuid.UUID) (map[uuid.UUID]store.ActivityEvent, error)
	})
	if !ok {
		t.Fatal("store does not implement GetLatestActivityBySessions")
	}
	latest, err := bulk.GetLatestActivityBySessions(ctx, []uuid.UUID{sess.ID})
	if err != nil {
		t.Fatalf("GetLatestActivityBySessions: %v", err)
	}
	if latest[sess.ID].ID != ev.ID || latest[sess.ID].Summary != "ran ls" {
		t.Fatalf("latest = %+v, want the written event's summary", latest[sess.ID])
	}
	if len(latest[sess.ID].Detail) != 0 {
		t.Fatalf("latest detail = %q, want omitted (fleet summaries never render detail)", latest[sess.ID].Detail)
	}
}
