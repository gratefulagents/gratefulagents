package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

// TestActivityIDsFollowCommitOrder: readers page activity by id, so for one
// session an event committed later must never get a lower id than one
// already visible. A writer that starts while another transaction holds the
// session row must draw its id only after that transaction commits.
// Without the commit-ordering trigger (migration 064) the waiting writer
// drew its id up front and the holder's later event got a HIGHER id that
// committed first, so an id cursor skipped the waiting writer's row.
func TestActivityIDsFollowCommitOrder(t *testing.T) {
	s, pool := setupPGStore(t)
	ctx := context.Background()
	sess, err := s.CreateSession(ctx, "commit-order", "default", "running", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `UPDATE agent_sessions SET current_step = 'holding' WHERE id = $1`, sess.ID); err != nil {
		t.Fatal(err)
	}

	type result struct {
		id  int64
		err error
	}
	waiting := make(chan result, 1)
	go func() {
		ids, err := s.WriteActivityEvents(ctx, sess.ID, []store.ActivityEventInput{{EventType: "late", Summary: "waiting writer"}})
		if err != nil || len(ids) != 1 {
			waiting <- result{err: err}
			return
		}
		waiting <- result{id: ids[0]}
	}()
	select {
	case r := <-waiting:
		t.Fatalf("writer did not wait for the session row lock: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}

	var holderID int64
	if err := holder.QueryRow(ctx, `
		INSERT INTO activity_events (session_id, event_type, summary, detail)
		VALUES ($1, 'early', 'lock holder', '{}') RETURNING id`, sess.ID).Scan(&holderID); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r := <-waiting
	if r.err != nil {
		t.Fatalf("waiting writer: %v", r.err)
	}
	if r.id <= holderID {
		t.Fatalf("waiting writer id %d <= earlier-committed id %d: an id cursor would skip it", r.id, holderID)
	}
}

// TestWriteActivityEventsIdempotentRetry: a batch retried after an ambiguous
// failure skips rows whose client_event_id already committed.
func TestWriteActivityEventsIdempotentRetry(t *testing.T) {
	s, _ := setupPGStore(t)
	ctx := context.Background()
	sess, err := s.CreateSession(ctx, "idempotent-batch", "default", "running", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	a, b := uuid.New(), uuid.New()
	first := []store.ActivityEventInput{
		{EventType: "tool_start", Summary: "a", ClientEventID: a},
		{EventType: "tool_end", Summary: "b", ClientEventID: b},
	}
	ids, err := s.WriteActivityEvents(ctx, sess.ID, first)
	if err != nil || len(ids) != 2 {
		t.Fatalf("first attempt = (%v, %v), want 2 ids", ids, err)
	}
	retry := append(first, store.ActivityEventInput{EventType: "assistant_text", Summary: "c", ClientEventID: uuid.New()})
	ids, err = s.WriteActivityEvents(ctx, sess.ID, retry)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("retry inserted %d rows, want only the new event", len(ids))
	}
	// Keyless rows are never deduplicated.
	if _, err := s.WriteActivityEvents(ctx, sess.ID, []store.ActivityEventInput{{EventType: "x"}, {EventType: "x"}}); err != nil {
		t.Fatal(err)
	}
	events, err := s.GetAllActivity(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("stored %d events, want 5 (a, b, c, x, x)", len(events))
	}
}

// TestRecoveredMessageKeepsDeliveryPosition: a message handed back to the
// queue after its runner died keeps claimed_at and delivery_sequence, and a
// reclaim reuses them, so the dashboard keeps it where it was.
func TestRecoveredMessageKeepsDeliveryPosition(t *testing.T) {
	s, _ := setupPGStore(t)
	ctx := context.Background()
	sess, err := s.CreateSession(ctx, "recover-position", "default", "running", "")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.AppendMessage(ctx, sess.ID, "user", "do it", json.RawMessage(`{"mode":"enqueue"}`))
	if err != nil {
		t.Fatal(err)
	}
	claimed, won, err := s.ClaimUserMessage(ctx, sess.ID, msg.ID, uuid.New())
	if err != nil || !won {
		t.Fatalf("claim = (%v, %v)", won, err)
	}
	if err := s.RecoverClaimedUserMessages(ctx, sess.ID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.GetMessage(ctx, sess.ID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.DeliveryState != "pending" {
		t.Fatalf("state = %q, want pending (redeliverable)", recovered.DeliveryState)
	}
	if recovered.ClaimedAt == nil || !recovered.ClaimedAt.Equal(*claimed.ClaimedAt) || recovered.DeliverySequence != claimed.DeliverySequence {
		t.Fatalf("recovered claimed_at/sequence = %v/%d, want %v/%d", recovered.ClaimedAt, recovered.DeliverySequence, claimed.ClaimedAt, claimed.DeliverySequence)
	}
	pending, err := s.PollNewUserMessages(ctx, sess.ID)
	if err != nil || len(pending) != 1 || pending[0].ID != msg.ID {
		t.Fatalf("pending = %+v (%v), want the recovered message", pending, err)
	}
	reclaimed, won, err := s.ClaimUserMessage(ctx, sess.ID, msg.ID, uuid.New())
	if err != nil || !won {
		t.Fatalf("reclaim = (%v, %v)", won, err)
	}
	if !reclaimed.ClaimedAt.Equal(*claimed.ClaimedAt) || reclaimed.DeliverySequence != claimed.DeliverySequence {
		t.Fatalf("reclaim moved the message: %v/%d, want %v/%d", reclaimed.ClaimedAt, reclaimed.DeliverySequence, claimed.ClaimedAt, claimed.DeliverySequence)
	}
}
