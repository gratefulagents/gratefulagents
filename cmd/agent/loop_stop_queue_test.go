package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
)

// stopQueueFakeStore implements just enough of store.StateStore (via interface
// embedding) for queuedUserMessageWaiting's peek path.
type stopQueueFakeStore struct {
	store.StateStore
	session *store.Session
	pending []store.Message
}

func (s *stopQueueFakeStore) GetSessionByRun(context.Context, string, string) (*store.Session, error) {
	return s.session, nil
}

func (s *stopQueueFakeStore) GetResourceOwner(context.Context, string, string, string) (*store.ResourceOwnership, error) {
	return nil, nil
}

func (s *stopQueueFakeStore) PollNewUserMessages(context.Context, uuid.UUID) ([]store.Message, error) {
	return append([]store.Message(nil), s.pending...), nil
}

func newStopQueueTestClient(t *testing.T, pending []store.Message) *sessionclient.Client {
	t.Helper()
	fake := &stopQueueFakeStore{session: &store.Session{ID: uuid.New()}, pending: pending}
	sc, err := sessionclient.New(context.Background(), fake, nil, "run", "ns", "running", "")
	if err != nil {
		t.Fatalf("sessionclient.New: %v", err)
	}
	return sc
}

func immediateMetadata(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"mode": "immediate"})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestQueuedUserMessageWaiting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		pending          []store.Message
		handledImmediate map[int64]struct{}
		want             bool
	}{
		{
			name:    "steering message queued before the stop continues directly",
			pending: []store.Message{{ID: 7, Content: "do this instead", Metadata: json.RawMessage(`{"mode":"immediate"}`)}},
			want:    true,
		},
		{
			name:    "queued (enqueue) message continues directly",
			pending: []store.Message{{ID: 8, Content: "next task"}},
			want:    true,
		},
		{
			name:             "steering message already folded into the turn does not count",
			pending:          []store.Message{{ID: 7, Content: "do this instead", Metadata: json.RawMessage(`{"mode":"immediate"}`)}},
			handledImmediate: map[int64]struct{}{7: {}},
			want:             false,
		},
		{
			name:    "blank message does not count",
			pending: []store.Message{{ID: 9, Content: "   "}},
			want:    false,
		},
		{
			name:    "control slash command still parks the session",
			pending: []store.Message{{ID: 10, Content: "/stop"}},
			want:    false,
		},
		{
			name: "no pending messages",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc := newStopQueueTestClient(t, tc.pending)
			handled := tc.handledImmediate
			if handled == nil {
				handled = map[int64]struct{}{}
			}
			if got := queuedUserMessageWaiting(context.Background(), sc, handled); got != tc.want {
				t.Fatalf("queuedUserMessageWaiting() = %v, want %v", got, tc.want)
			}
		})
	}
}
