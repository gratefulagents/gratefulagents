package sessionclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

func TestResumeControlIsMessageFreeAndReplaySafe(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	ss := &metadataTestStore{session: &store.Session{ID: id}}
	c := &Client{store: ss, sessionID: id}
	if err := RequestResume(ctx, ss, id, "retry-1", "pending-1"); err != nil {
		t.Fatal(err)
	}
	req, err := c.PendingResume(ctx)
	if err != nil || req == nil || req.ID != "retry-1" || req.PendingRequestID != "pending-1" || req.RequestedAt.IsZero() {
		t.Fatalf("resume=%+v err=%v", req, err)
	}
	if err := c.AcknowledgeResume(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := RequestResume(ctx, ss, id, "retry-1", "pending-1"); err != nil {
		t.Fatal(err)
	}
	if req, err := c.PendingResume(ctx); err != nil || req != nil {
		t.Fatalf("replayed resume=%+v err=%v", req, err)
	}
	if err := RequestResume(ctx, ss, id, "retry-2", "pending-2"); err != nil {
		t.Fatal(err)
	}
	if req, err := c.PendingResume(ctx); err != nil || req == nil || req.ID != "retry-2" {
		t.Fatalf("new resume=%+v err=%v", req, err)
	}
}

type transientResumeStore struct {
	*metadataTestStore
	failures int
	reads    int
}

func (s *transientResumeStore) GetSession(ctx context.Context, id uuid.UUID) (*store.Session, error) {
	s.reads++
	if s.failures > 0 {
		s.failures--
		return nil, errors.New("transient metadata failure")
	}
	return s.metadataTestStore.GetSession(ctx, id)
}
func TestResumePollRetriesMetadataAndLeavesRequestPending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	id := uuid.New()
	ss := &transientResumeStore{metadataTestStore: &metadataTestStore{session: &store.Session{ID: id}}}
	c := &Client{store: ss, sessionID: id}
	if err := RequestResume(ctx, ss, id, "retry", ""); err != nil {
		t.Fatal(err)
	}
	ss.failures = 1
	msgs, err := c.PollForUserMessages(ctx, time.Millisecond)
	if err != nil || len(msgs) != 1 || msgs[0].Resume == nil || ss.reads < 3 {
		t.Fatalf("msgs=%v reads=%d err=%v", msgs, ss.reads, err)
	}
	if req, err := c.PendingResume(ctx); err != nil || req == nil {
		t.Fatalf("poll consumed resume: %v %v", req, err)
	}
	old := msgs[0].Resume
	if err := RequestResume(ctx, ss, id, "newer", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.AcknowledgeResume(ctx, old); err != nil {
		t.Fatal(err)
	}
	if req, err := c.PendingResume(ctx); err != nil || req == nil || req.ID != "newer" {
		t.Fatalf("newer request lost: %v %v", req, err)
	}
	ss.failures = 10000
	cancel()
	if _, err := c.PollForUserMessages(ctx, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
