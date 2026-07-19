package dashboard

import (
	"context"
	"time"

	"fmt"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func thinkingDelta(eventID int64, ts int64, toolUseID, msg string) *platform.ActivityEntry {
	return &platform.ActivityEntry{
		EventId:       eventID,
		TimestampUnix: ts,
		Type:          "assistant_thinking_delta",
		ToolUseId:     toolUseID,
		Message:       msg,
		AgentName:     "agent",
		Session:       1,
		Phase:         "implement",
		Step:          "plan",
		TaskId:        "task-1",
	}
}

func thinkingFinal(eventID int64, ts int64, toolUseID, msg string) *platform.ActivityEntry {
	return &platform.ActivityEntry{
		EventId:       eventID,
		TimestampUnix: ts,
		Type:          "assistant_thinking",
		ToolUseId:     toolUseID,
		Message:       msg,
	}
}

func toolEntry(eventID int64, typ, toolUseID string) *platform.ActivityEntry {
	return &platform.ActivityEntry{EventId: eventID, Type: typ, Tool: "Bash", ToolUseId: toolUseID}
}

func TestCoalesceThinkingEntries(t *testing.T) {
	tests := []struct {
		name string
		in   []*platform.ActivityEntry
		want []*platform.ActivityEntry
	}{
		{
			name: "single stream merges in order with first timestamp and max event id",
			in: []*platform.ActivityEntry{
				thinkingDelta(1, 100, "a1", "foo "),
				thinkingDelta(2, 101, "a1", "bar "),
				thinkingDelta(3, 102, "a1", "baz"),
			},
			want: []*platform.ActivityEntry{
				func() *platform.ActivityEntry {
					e := thinkingDelta(3, 100, "a1", "foo bar baz")
					e.Type = "assistant_thinking"
					return e
				}(),
			},
		},
		{
			name: "final replaces accumulated text keeping first delta identity",
			in: []*platform.ActivityEntry{
				thinkingDelta(1, 100, "a1", "fo"),
				thinkingDelta(2, 101, "a1", "o"),
				thinkingFinal(3, 105, "a1", "full reasoning"),
			},
			want: []*platform.ActivityEntry{
				func() *platform.ActivityEntry {
					e := thinkingDelta(3, 100, "a1", "full reasoning")
					e.Type = "assistant_thinking"
					return e
				}(),
			},
		},
		{
			name: "multiple finals appended with blank line",
			in: []*platform.ActivityEntry{
				thinkingDelta(1, 100, "a1", "x"),
				thinkingFinal(2, 101, "a1", "first"),
				thinkingFinal(3, 102, "a1", "second"),
			},
			want: []*platform.ActivityEntry{
				func() *platform.ActivityEntry {
					e := thinkingDelta(3, 100, "a1", "first\n\nsecond")
					e.Type = "assistant_thinking"
					return e
				}(),
			},
		},
		{
			name: "deltas interleaved with other entries emit merged entry at newest constituent slot",
			in: []*platform.ActivityEntry{
				toolEntry(1, "tool_use", "t1"),
				thinkingDelta(2, 100, "a1", "th"),
				toolEntry(3, "tool_result", "t1"),
				thinkingDelta(4, 101, "a1", "ink"),
				toolEntry(5, "tool_use", "t2"),
			},
			want: []*platform.ActivityEntry{
				toolEntry(1, "tool_use", "t1"),
				toolEntry(3, "tool_result", "t1"),
				func() *platform.ActivityEntry {
					e := thinkingDelta(4, 100, "a1", "think")
					e.Type = "assistant_thinking"
					return e
				}(),
				toolEntry(5, "tool_use", "t2"),
			},
		},
		{
			name: "final after intervening entries emits merged entry at final slot",
			in: []*platform.ActivityEntry{
				toolEntry(1, "llm_attempt", "a1"),
				thinkingDelta(2, 100, "a1", "fo"),
				thinkingDelta(3, 101, "a1", "o"),
				toolEntry(4, "llm_attempt", "a1"),
				thinkingFinal(5, 105, "a1", "full reasoning"),
			},
			want: []*platform.ActivityEntry{
				toolEntry(1, "llm_attempt", "a1"),
				toolEntry(4, "llm_attempt", "a1"),
				func() *platform.ActivityEntry {
					e := thinkingDelta(5, 100, "a1", "full reasoning")
					e.Type = "assistant_thinking"
					return e
				}(),
			},
		},
		{
			name: "two concurrent streams group by tool use id",
			in: []*platform.ActivityEntry{
				thinkingDelta(1, 100, "a1", "one "),
				thinkingDelta(2, 101, "a2", "uno "),
				thinkingDelta(3, 102, "a1", "two"),
				thinkingDelta(4, 103, "a2", "dos"),
			},
			want: []*platform.ActivityEntry{
				func() *platform.ActivityEntry {
					e := thinkingDelta(3, 100, "a1", "one two")
					e.Type = "assistant_thinking"
					return e
				}(),
				func() *platform.ActivityEntry {
					e := thinkingDelta(4, 101, "a2", "uno dos")
					e.Type = "assistant_thinking"
					return e
				}(),
			},
		},
		{
			name: "empty tool use id deltas pass through individually with type rewrite",
			in: []*platform.ActivityEntry{
				thinkingDelta(1, 100, "", "a"),
				thinkingDelta(2, 101, "", "b"),
			},
			want: []*platform.ActivityEntry{
				func() *platform.ActivityEntry {
					e := thinkingDelta(1, 100, "", "a")
					e.Type = "assistant_thinking"
					return e
				}(),
				func() *platform.ActivityEntry {
					e := thinkingDelta(2, 101, "", "b")
					e.Type = "assistant_thinking"
					return e
				}(),
			},
		},
		{
			name: "plain assistant_thinking without deltas passes through",
			in: []*platform.ActivityEntry{
				thinkingFinal(1, 100, "", "legacy"),
				thinkingFinal(2, 101, "a9", "unmatched"),
			},
			want: []*platform.ActivityEntry{
				thinkingFinal(1, 100, "", "legacy"),
				thinkingFinal(2, 101, "a9", "unmatched"),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			originals := make([]*platform.ActivityEntry, len(tc.in))
			for i, e := range tc.in {
				originals[i] = proto.Clone(e).(*platform.ActivityEntry)
			}
			got := coalesceThinkingEntries(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d entries, want %d: %v", len(got), len(tc.want), got)
			}
			for i := range got {
				if !proto.Equal(got[i], tc.want[i]) {
					t.Errorf("entry %d = %v, want %v", i, got[i], tc.want[i])
				}
			}
			// Pagination (since/before/limit) and the delta-watch cursor
			// assume monotonic EventIds; coalescing must preserve them.
			for i := 1; i < len(got); i++ {
				if got[i].EventId <= got[i-1].EventId {
					t.Errorf("output not ordered by EventId at %d: %d then %d", i, got[i-1].EventId, got[i].EventId)
				}
			}
			for i, e := range tc.in {
				if !proto.Equal(e, originals[i]) {
					t.Errorf("input entry %d mutated: %v, want %v", i, e, originals[i])
				}
			}
			again := coalesceThinkingEntries(got)
			if len(again) != len(got) {
				t.Fatalf("idempotency: got %d entries, want %d", len(again), len(got))
			}
			for i := range again {
				if !proto.Equal(again[i], got[i]) {
					t.Errorf("idempotency: entry %d = %v, want %v", i, again[i], got[i])
				}
			}
		})
	}
}

func TestCoalesceThinkingEntriesPassthroughKeepsPointers(t *testing.T) {
	plain := thinkingFinal(1, 100, "a9", "unmatched")
	tool := toolEntry(2, "tool_use", "t1")
	in := []*platform.ActivityEntry{plain, tool, thinkingDelta(3, 101, "a1", "x")}
	got := coalesceThinkingEntries(in)
	if got[0] != plain {
		t.Error("unmatched assistant_thinking must pass through by pointer")
	}
	if got[1] != tool {
		t.Error("non-thinking entries must pass through by pointer")
	}
}

// TestCoalescedEntriesRespectPaginationOptions guards the EventId-order
// contract end to end: since_event_id and limit are implemented as ordered
// scans/tail slices, so a merged thinking entry carrying the max constituent
// id must sit at the position that id implies — otherwise a since cursor
// re-sends older entries and a tail limit drops the newest thinking text.
func TestCoalescedEntriesRespectPaginationOptions(t *testing.T) {
	in := []*platform.ActivityEntry{
		toolEntry(1, "tool_use", "t1"),
		thinkingDelta(2, 100, "a1", "th"),
		toolEntry(3, "tool_result", "t1"),
		thinkingDelta(4, 101, "a1", "ink"),
	}

	since := applyActivityLogRequestOptions(
		&platform.GetActivityLogResponse{Entries: coalesceThinkingEntries(in)},
		&platform.GetActivityLogRequest{SinceEventId: 3},
	)
	if len(since.Entries) != 1 || since.Entries[0].EventId != 4 || since.Entries[0].Type != "assistant_thinking" {
		t.Fatalf("since_event_id=3 entries = %v, want only the merged thinking entry (event 4)", since.Entries)
	}

	limited := applyActivityLogRequestOptions(
		&platform.GetActivityLogResponse{Entries: coalesceThinkingEntries(in)},
		&platform.GetActivityLogRequest{Limit: 1},
	)
	if len(limited.Entries) != 1 || limited.Entries[0].EventId != 4 || limited.Entries[0].Type != "assistant_thinking" {
		t.Fatalf("limit=1 entries = %v, want the newest entry to be the merged thinking entry", limited.Entries)
	}
}

func thinkingDeltaEvent(id int64, toolUseID, msg string) store.ActivityEvent {
	detail := fmt.Sprintf(`{"type":"assistant_thinking_delta","tool_use_id":%q,"message":%q}`, toolUseID, msg)
	return store.ActivityEvent{ID: id, EventType: "assistant_thinking_delta", Detail: []byte(detail)}
}

// TestWatchActivityLogDeltaResendsGrownThinkingEntry verifies that a merged
// thinking entry — whose EventId grows as new deltas are coalesced into it,
// moving it to its newest constituent's slot — is re-sent whenever its
// EventId passes the stream cursor.
func TestWatchActivityLogDeltaResendsGrownThinkingEntry(t *testing.T) {
	initial := []store.ActivityEvent{
		toolResultEvent(1, "t1", "in1", "out1"),
		thinkingDeltaEvent(2, "a1", "thinking "),
	}
	srv, ms, sessID := newActivityLogTestServer(t, "run-think", initial)

	conn := &recordingActivityLogConn{ch: make(chan *platform.GetActivityLogResponse, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.WatchActivityLog(ctx, &platform.GetActivityLogRequest{
			Namespace: "default", Name: "run-think", Delta: true,
		}, newActivityLogServerStream(conn))
	}()

	var first *platform.GetActivityLogResponse
	select {
	case first = <-conn.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial frame")
	}
	if !first.Reset_ || len(first.Entries) != 2 {
		t.Fatalf("initial frame reset=%t entries=%d, want true/2", first.Reset_, len(first.Entries))
	}
	if got := first.Entries[1]; got.Type != "assistant_thinking" || got.Message != "thinking " || got.EventId != 2 {
		t.Fatalf("initial merged entry = %v, want assistant_thinking %q event 2", got, "thinking ")
	}

	// More deltas for the same attempt plus an unrelated tool event arrive:
	// the merged entry's EventId grows to 3 and it moves to the newest
	// delta's slot, keeping the coalesced snapshot ordered by id.
	setMockActivity(ms, sessID, append(initial,
		thinkingDeltaEvent(3, "a1", "harder"),
		toolResultEvent(4, "t2", "in2", "out2"),
	))

	var second *platform.GetActivityLogResponse
	select {
	case second = <-conn.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for delta frame")
	}
	if second.Reset_ || !second.Delta {
		t.Fatalf("delta frame reset=%t delta=%t, want false/true", second.Reset_, second.Delta)
	}
	if len(second.Entries) != 2 {
		t.Fatalf("delta frame entries = %v, want grown thinking entry + new tool entry", second.Entries)
	}
	grown := second.Entries[0]
	if grown.Type != "assistant_thinking" || grown.ToolUseId != "a1" || grown.EventId != 3 || grown.Message != "thinking harder" {
		t.Fatalf("grown thinking entry = %v, want re-sent merged entry with event 3 and full text", grown)
	}
	if second.Entries[1].EventId != 4 {
		t.Fatalf("second delta entry = %v, want tool event 4", second.Entries[1])
	}
	if second.LastEventId != 4 {
		t.Fatalf("delta frame last_event_id = %d, want 4", second.LastEventId)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("WatchActivityLog returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for watch to stop")
	}
}
