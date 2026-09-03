package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
)

// stopQueueFakeStore implements just enough of store.StateStore (via interface
// embedding) plus the MessageClaimer and MetadataSectionUpdater capabilities
// for the stop-path helpers: peek, claim, complete claims, working state.
type stopQueueFakeStore struct {
	store.StateStore
	session  *store.Session
	pending  []store.Message
	metadata map[string]json.RawMessage

	// cancelledIDs lose their claim (ClaimUserMessage reports won=false),
	// modelling a message the user cancelled after it was peeked.
	cancelledIDs map[int64]bool

	claimedIDs          []int64
	completeClaimsCalls int
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

func (s *stopQueueFakeStore) ClaimUserMessage(_ context.Context, _ uuid.UUID, messageID int64, _ uuid.UUID) (*store.Message, bool, error) {
	if s.cancelledIDs[messageID] {
		return nil, false, nil
	}
	for i, msg := range s.pending {
		if msg.ID == messageID {
			s.claimedIDs = append(s.claimedIDs, messageID)
			s.pending = append(s.pending[:i:i], s.pending[i+1:]...)
			claimed := msg
			return &claimed, true, nil
		}
	}
	return nil, false, nil
}

func (s *stopQueueFakeStore) AppendAssistantAndCompleteClaims(context.Context, uuid.UUID, uuid.UUID, string) (*store.Message, error) {
	panic("unexpected call")
}

func (s *stopQueueFakeStore) CompleteClaims(context.Context, uuid.UUID, uuid.UUID) error {
	s.completeClaimsCalls++
	return nil
}

func (s *stopQueueFakeStore) RecoverClaimedUserMessages(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (s *stopQueueFakeStore) UpdateSessionMetadataSection(_ context.Context, _ uuid.UUID, key string, mutate func(json.RawMessage) (json.RawMessage, error)) error {
	if s.metadata == nil {
		s.metadata = map[string]json.RawMessage{}
	}
	updated, err := mutate(s.metadata[key])
	if err != nil {
		return err
	}
	s.metadata[key] = updated
	return nil
}

func (s *stopQueueFakeStore) GetSession(context.Context, uuid.UUID) (*store.Session, error) {
	encoded, err := json.Marshal(s.metadata)
	if err != nil {
		return nil, err
	}
	session := *s.session
	session.Metadata = encoded
	return &session, nil
}

func newStopQueueTestStore(pending []store.Message) *stopQueueFakeStore {
	return &stopQueueFakeStore{session: &store.Session{ID: uuid.New()}, pending: pending}
}

func newStopQueueTestClient(t *testing.T, fake *stopQueueFakeStore) *sessionclient.Client {
	t.Helper()
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
		stoppedMessageID int64
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
			name:             "the stopped prompt handed back as pending does not count",
			pending:          []store.Message{{ID: 5, Content: "the prompt the user stopped"}},
			stoppedMessageID: 5,
			want:             false,
		},
		{
			name:             "an earlier queued message survives a later stopped steer",
			pending:          []store.Message{{ID: 30, Content: "queued earlier"}},
			stoppedMessageID: 31,
			want:             true,
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
			sc := newStopQueueTestClient(t, newStopQueueTestStore(tc.pending))
			handled := tc.handledImmediate
			if handled == nil {
				handled = map[int64]struct{}{}
			}
			if got := queuedUserMessageWaiting(context.Background(), sc, tc.stoppedMessageID, handled); got != tc.want {
				t.Fatalf("queuedUserMessageWaiting() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Every user-stop path (mid-turn interrupt, pre-turn stop, /stop in auto
// mode, stop racing the natural end) records the stop through recordUserStop:
// it must complete this pod's claims so the stopped prompt is marked
// delivered instead of being handed back to a replacement pod as pending,
// and persist the stopped-message floor.
func TestRecordUserStopCompletesClaimsAndPersistsFloor(t *testing.T) {
	t.Parallel()
	fake := newStopQueueTestStore(nil)
	sc := newStopQueueTestClient(t, fake)

	recordUserStop(context.Background(), sc, 42)

	if fake.completeClaimsCalls != 1 {
		t.Fatalf("CompleteClaims calls = %d, want 1", fake.completeClaimsCalls)
	}
	state, err := sc.ReadWorkingState(context.Background())
	if err != nil {
		t.Fatalf("ReadWorkingState: %v", err)
	}
	if state.LastStoppedUserMessageID != 42 {
		t.Fatalf("LastStoppedUserMessageID = %d, want 42", state.LastStoppedUserMessageID)
	}
}

// A steer sent while the sandbox was still provisioning lands in the queue
// behind the seeded kickoff request before the worker's first poll. The
// kickoff must open the turn (it is the request that started the run, and the
// UI already renders it as delivered), and the steer must reach the same turn
// through the immediate-input poll — not overtake the kickoff and leave it
// pending until the agent finishes, which re-ran it as a second turn.
func TestStartupSteerDoesNotOvertakeSeededKickoff(t *testing.T) {
	t.Parallel()
	fake := newStopQueueTestStore([]store.Message{
		{ID: 1, Content: "seeded kickoff request"},
		{ID: 2, Content: "steer typed during startup", Metadata: immediateMetadata(t)},
	})
	sc := newStopQueueTestClient(t, fake)
	handled := map[int64]struct{}{}
	ctx := context.Background()

	prompt, err := waitForNextUserReply(ctx, sc, 0, time.Second, handled)
	if err != nil {
		t.Fatalf("waitForNextUserReply: %v", err)
	}
	if prompt.ID != 1 {
		t.Fatalf("turn prompt ID = %d, want the kickoff message 1", prompt.ID)
	}
	if _, marked := handled[2]; marked {
		t.Fatal("the unclaimed steer must not be marked handled")
	}

	items, err := pollImmediateInputs(ctx, sc, t.TempDir(), nil, 0, handled)
	if err != nil {
		t.Fatalf("pollImmediateInputs: %v", err)
	}
	if len(items) != 1 || items[0].Message == nil || items[0].Message.Text != "steer typed during startup" {
		t.Fatalf("immediate items = %#v, want the startup steer folded into the turn", items)
	}
	if got := fake.claimedIDs; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("claimed IDs = %v, want [1 2]", got)
	}
	if len(fake.pending) != 0 {
		t.Fatalf("pending after the turn opened = %v, want nothing left for a post-finish turn", fake.pending)
	}
}

func TestClaimQueuedUserMessage(t *testing.T) {
	t.Parallel()

	t.Run("claims the queued message", func(t *testing.T) {
		t.Parallel()
		fake := newStopQueueTestStore([]store.Message{{ID: 8, Content: "next task"}})
		sc := newStopQueueTestClient(t, fake)

		msg, ok, err := claimQueuedUserMessage(context.Background(), sc, 0, map[int64]struct{}{})
		if err != nil || !ok {
			t.Fatalf("claimQueuedUserMessage() = ok=%v err=%v, want claimed", ok, err)
		}
		if msg.ID != 8 {
			t.Fatalf("claimed ID = %d, want 8", msg.ID)
		}
	})

	t.Run("cancelled message reports nothing claimable and unmarks the immediate", func(t *testing.T) {
		t.Parallel()
		fake := newStopQueueTestStore([]store.Message{{ID: 7, Content: "steer", Metadata: json.RawMessage(`{"mode":"immediate"}`)}})
		fake.cancelledIDs = map[int64]bool{7: true}
		sc := newStopQueueTestClient(t, fake)
		handled := map[int64]struct{}{}

		_, ok, err := claimQueuedUserMessage(context.Background(), sc, 0, handled)
		if err != nil || ok {
			t.Fatalf("claimQueuedUserMessage() = ok=%v err=%v, want not claimed", ok, err)
		}
		if _, marked := handled[7]; marked {
			t.Fatal("lost claim must not leave the message marked as handled")
		}
	})

	t.Run("retires the stopped prompt instead of starting a turn from it", func(t *testing.T) {
		t.Parallel()
		fake := newStopQueueTestStore([]store.Message{{ID: 5, Content: "stopped prompt"}})
		sc := newStopQueueTestClient(t, fake)

		_, ok, err := claimQueuedUserMessage(context.Background(), sc, 5, map[int64]struct{}{})
		if err != nil || ok {
			t.Fatalf("claimQueuedUserMessage() = ok=%v err=%v, want not claimed", ok, err)
		}
		// The stale pending row is claimed and completed in place so it
		// neither re-enters model context nor reappears in every poll.
		if len(fake.claimedIDs) != 1 || fake.claimedIDs[0] != 5 {
			t.Fatalf("claimed IDs = %v, want [5] (retired)", fake.claimedIDs)
		}
		if fake.completeClaimsCalls != 1 {
			t.Fatalf("CompleteClaims calls = %d, want 1", fake.completeClaimsCalls)
		}
	})
}
