package dashboard

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func TestConversationFromMessagesQueueStates(t *testing.T) {
	msgs := []store.Message{
		{ID: 1, Role: "user", Content: "kickoff request", CreatedAt: time.Unix(100, 0)},
		{ID: 2, Role: "assistant", Content: "ack", CreatedAt: time.Unix(110, 0)},
		{ID: 3, Role: "user", Content: "legacy consumed", Metadata: json.RawMessage(`{"mode":"enqueue"}`), CreatedAt: time.Unix(120, 0)},
		{ID: 4, Role: "assistant", Content: "reply to legacy", CreatedAt: time.Unix(130, 0)},
		{ID: 5, Role: "user", Content: "steer now", Metadata: json.RawMessage(`{"mode":"immediate","delivered_at_unix":140}`), CreatedAt: time.Unix(135, 0)},
		{ID: 6, Role: "user", Content: "queued next", Metadata: json.RawMessage(`{"mode":"enqueue"}`), CreatedAt: time.Unix(150, 0)},
	}

	conv := conversationFromMessages(msgs, "Running")
	if len(conv) != len(msgs) {
		t.Fatalf("len(conv) = %d, want %d", len(conv), len(msgs))
	}

	// The kickoff message is never pending, even without a delivery stamp.
	if conv[0].Pending {
		t.Fatal("kickoff message must not be pending")
	}
	if conv[0].QueueMode != "enqueue" {
		t.Fatalf("kickoff QueueMode = %q, want enqueue", conv[0].QueueMode)
	}

	// Assistant messages carry no queue state.
	if conv[1].Pending || conv[1].QueueMode != "" {
		t.Fatalf("assistant message state = pending=%v mode=%q, want none", conv[1].Pending, conv[1].QueueMode)
	}

	// Delivery is explicit: an unstamped follow-up remains pending even when
	// a later assistant row exists. Message IDs cannot prove causality because
	// users can queue follow-ups while an older turn is still running.
	if !conv[2].Pending {
		t.Fatal("unstamped follow-up must remain pending")
	}

	// Delivered steering message: not pending, stamp surfaced.
	if conv[4].Pending {
		t.Fatal("delivered steering message must not be pending")
	}
	if conv[4].QueueMode != "immediate" || conv[4].DeliveredAtUnix != 140 {
		t.Fatalf("steering message state = mode=%q delivered=%d, want immediate/140", conv[4].QueueMode, conv[4].DeliveredAtUnix)
	}

	// Undelivered queued message after the latest assistant reply is pending.
	if !conv[5].Pending {
		t.Fatal("undelivered queued message must be pending")
	}
	if conv[5].QueueMode != "enqueue" || conv[5].DeliveredAtUnix != 0 {
		t.Fatalf("queued message state = mode=%q delivered=%d, want enqueue/0", conv[5].QueueMode, conv[5].DeliveredAtUnix)
	}
}

func TestConversationFromMessagesTerminalRunPreservesUndelivered(t *testing.T) {
	msgs := []store.Message{
		{ID: 1, Role: "user", Content: "kickoff", CreatedAt: time.Unix(100, 0)},
		{ID: 2, Role: "assistant", Content: "ack", CreatedAt: time.Unix(110, 0)},
		{ID: 3, Role: "user", Content: "never picked up", Metadata: json.RawMessage(`{"mode":"enqueue"}`), CreatedAt: time.Unix(120, 0)},
	}

	for _, phase := range []string{"Running", "Succeeded", "Failed", "Cancelled"} {
		conv := conversationFromMessages(msgs, phase)
		if !conv[2].Pending {
			t.Fatalf("phase %s: unstamped follow-up must remain undelivered", phase)
		}
	}
}

func TestGetActivityLogIncludesDurableMaintainerReport(t *testing.T) {
	detail := `{"state":"healthy","summary":"fleet is clear","decisions":"triaged issue #1","time":"2026-01-02T03:04:05Z"}`
	srv, _, _ := newActivityLogTestServer(t, "run-report", []store.ActivityEvent{{
		ID:        42,
		EventType: "maintainer_report",
		Summary:   "fleet is clear",
		Detail:    json.RawMessage(detail),
		CreatedAt: time.Unix(100, 0),
	}})

	resp, err := srv.GetActivityLog(context.Background(), &platform.GetActivityLogRequest{Namespace: "default", Name: "run-report"})
	if err != nil {
		t.Fatalf("GetActivityLog() error = %v", err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(resp.Entries))
	}
	entry := resp.Entries[0]
	if entry.Type != "maintainer_report" || entry.Message != "fleet is clear" || entry.InputRaw != detail || entry.TimestampUnix != 100 || entry.EventId != 42 {
		t.Fatalf("entry = %#v", entry)
	}

	got, err := srv.GetActivityEntryDetail(context.Background(), &platform.GetActivityEntryDetailRequest{Namespace: "default", Name: "run-report", EventId: 42})
	if err != nil {
		t.Fatalf("GetActivityEntryDetail() error = %v", err)
	}
	if got.InputRaw != detail {
		t.Fatalf("detail = %q, want %q", got.InputRaw, detail)
	}
}

func TestConversationFromMessagesCancelledAndIDs(t *testing.T) {
	msgs := []store.Message{
		{ID: 1, Role: "user", Content: "kickoff", CreatedAt: time.Unix(100, 0)},
		{ID: 2, Role: "assistant", Content: "ack", CreatedAt: time.Unix(110, 0)},
		{ID: 3, Role: "user", Content: "withdrawn", Metadata: json.RawMessage(`{"mode":"enqueue","cancelled_at_unix":150}`), CreatedAt: time.Unix(120, 0)},
		{ID: 4, Role: "user", Content: "still queued", Metadata: json.RawMessage(`{"mode":"enqueue"}`), CreatedAt: time.Unix(130, 0)},
	}

	conv := conversationFromMessages(msgs, "Running")
	if len(conv) != 3 {
		t.Fatalf("len(conv) = %d, want 3 (cancelled message hidden)", len(conv))
	}
	for _, cm := range conv {
		if cm.Content == "withdrawn" {
			t.Fatal("cancelled message must not render")
		}
	}
	// Durable ids surface so pending messages can be cancelled from the UI.
	if conv[0].Id != 1 || conv[1].Id != 2 || conv[2].Id != 4 {
		t.Fatalf("conversation ids = %d,%d,%d, want 1,2,4", conv[0].Id, conv[1].Id, conv[2].Id)
	}
	if !conv[2].Pending {
		t.Fatal("undelivered queued message must remain pending")
	}
}
