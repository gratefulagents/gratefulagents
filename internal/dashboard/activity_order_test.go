package dashboard

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func runningRun(name string) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
}

// TestActivityLogStaysOnPostgresWhenSessionHasNoEvents: a run whose session
// exists but has no events yet must be served from Postgres (empty), not
// from the pod's events.jsonl: flipping between sources made clients rebuild
// the timeline from a different history with incomparable event ids.
func TestActivityLogStaysOnPostgresWhenSessionHasNoEvents(t *testing.T) {
	srv, _, _ := newActivityLogTestServer(t, "run-empty", nil)
	resp, source := srv.getAgentRunActivityLogSourced(context.Background(), runningRun("run-empty"))
	if source != activityLogSourcePostgres {
		t.Fatalf("source = %q, want postgres", source)
	}
	if len(resp.Entries) != 0 || !resp.EventIdsDurable {
		t.Fatalf("resp = %+v, want empty durable response", resp)
	}
}

// TestActivityMemoIgnoresOtherSession: the memo is keyed by run name; a memo
// left by a deleted run whose name was reused must never be served or
// extended with the new session's events.
func TestActivityMemoIgnoresOtherSession(t *testing.T) {
	srv, _, sessID := newActivityLogTestServer(t, "run-reused", []store.ActivityEvent{
		toolResultEvent(10, "new", "in", "out"),
	})
	stale := buildPostgresActivityLogResponse([]*platform.ActivityEntry{{EventId: 99, Type: "assistant_text", Message: "previous run"}}, false, "run-reused")
	srv.storeActivityMemo("default/run-reused", &activityMemoEntry{
		sessionID:   uuid.New(),
		lastEventID: 99,
		entries:     stale.Entries,
		resp:        stale,
	})
	resp, _ := srv.getAgentRunActivityLogSourced(context.Background(), runningRun("run-reused"))
	if len(resp.Entries) != 1 || resp.Entries[0].EventId != 10 {
		t.Fatalf("entries = %+v, want only the current session's event 10", resp.Entries)
	}
	srv.activityMemoMu.Lock()
	memo := srv.activityMemo["default/run-reused"]
	srv.activityMemoMu.Unlock()
	if memo == nil || memo.sessionID != sessID {
		t.Fatalf("memo session = %v, want %s", memo, sessID)
	}
}

func TestActivityEntriesCarryDatabaseOrderTime(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 678_000_000, time.UTC)
	detail, _ := json.Marshal(map[string]any{"type": "assistant_text", "message": "hi", "ts": at.Add(-3 * time.Second)})
	e := activityEventToActivityEntry(store.ActivityEvent{ID: 1, EventType: "assistant_text", Detail: detail, CreatedAt: at})
	if e.OrderUnixMs != at.UnixMilli() {
		t.Fatalf("OrderUnixMs = %d, want database time %d", e.OrderUnixMs, at.UnixMilli())
	}
	plain := activityEventToActivityEntry(store.ActivityEvent{ID: 2, EventType: "turn_interrupted", Summary: "stopped", CreatedAt: at})
	if plain.OrderUnixMs != at.UnixMilli() {
		t.Fatalf("plain OrderUnixMs = %d, want %d", plain.OrderUnixMs, at.UnixMilli())
	}
}

// TestWatchActivityLogDeltaResumeFlag: only the first frame of a stream
// opened with a durable since_event_id cursor is marked resume; the client
// appends exactly those frames and replaces on every other reset.
func TestWatchActivityLogDeltaResumeFlag(t *testing.T) {
	for _, tc := range []struct {
		name       string
		since      int64
		wantResume bool
	}{
		{"fresh stream", 0, false},
		{"resumed stream", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := newActivityLogTestServer(t, "run-resume", []store.ActivityEvent{
				toolResultEvent(1, "t1", "in1", "out1"),
				toolResultEvent(2, "t2", "in2", "out2"),
			})
			conn := &recordingActivityLogConn{ch: make(chan *platform.GetActivityLogResponse, 8)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				_ = srv.WatchActivityLog(ctx, &platform.GetActivityLogRequest{
					Namespace: "default", Name: "run-resume", Delta: true, SinceEventId: tc.since,
				}, newActivityLogServerStream(conn))
			}()
			var first *platform.GetActivityLogResponse
			select {
			case first = <-conn.ch:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for initial frame")
			}
			if !first.Reset_ || first.Resume != tc.wantResume || !first.EventIdsDurable {
				t.Fatalf("frame reset=%t resume=%t durable=%t, want true/%t/true", first.Reset_, first.Resume, first.EventIdsDurable, tc.wantResume)
			}
			if tc.since > 0 && (len(first.Entries) != 1 || first.Entries[0].EventId != 2) {
				t.Fatalf("resumed entries = %+v, want only event 2", first.Entries)
			}
		})
	}
}

func TestConversationKeepsRecoveredMessageInPlace(t *testing.T) {
	claimed := time.Date(2026, 1, 2, 3, 4, 5, 250_000_000, time.UTC)
	created := claimed.Add(-time.Second)
	msgs := []store.Message{
		{ID: 1, Role: "user", Content: "first", DeliveryState: "completed", CreatedAt: created, ClaimedAt: &created},
		// Handed back to the queue after its pod died: still pending, but
		// it already joined the conversation.
		{ID: 2, Role: "user", Content: "recovered", DeliveryState: "pending", DeliverySequence: 7, CreatedAt: created, ClaimedAt: &claimed,
			Metadata: json.RawMessage(`{"client_message_id":"cid-2"}`)},
		{ID: 3, Role: "user", Content: "queued", DeliveryState: "pending", CreatedAt: created},
	}
	out := conversationFromMessages(msgs, "")
	if out[1].Pending {
		t.Fatal("a recovered (previously claimed) message must stay in the transcript")
	}
	if out[1].DeliveredAtUnixMs != claimed.UnixMilli() || out[1].TimestampUnixMs != created.UnixMilli() {
		t.Fatalf("ms stamps = %d/%d, want %d/%d", out[1].DeliveredAtUnixMs, out[1].TimestampUnixMs, claimed.UnixMilli(), created.UnixMilli())
	}
	if out[1].ClientMessageId != "cid-2" {
		t.Fatalf("ClientMessageId = %q, want cid-2", out[1].ClientMessageId)
	}
	if !out[2].Pending {
		t.Fatal("a never-claimed queued message must stay pending")
	}
}
