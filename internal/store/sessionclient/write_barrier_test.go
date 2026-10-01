package sessionclient

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

// TestWriteBarrierRunsBeforeOrderedWrites: activity notices, messages, and
// claims must drain the buffered activity stream first so they are recorded
// after the stream events emitted before them.
func TestWriteBarrierRunsBeforeOrderedWrites(t *testing.T) {
	testStore := &metadataTestStore{session: &store.Session{ID: uuid.New()}}
	client := &Client{store: testStore, sessionID: testStore.session.ID}

	calls := 0
	var sawDeadline bool
	client.SetWriteBarrier(func(ctx context.Context) {
		calls++
		_, sawDeadline = ctx.Deadline()
	})
	ctx := context.Background()
	_ = client.WriteActivity(ctx, "turn_interrupted", "stopped", nil)
	_, _ = client.AppendAssistantMessage(ctx, "done")
	_, _ = client.AppendSystemMessage(ctx, "note")
	_, _ = client.AppendUserMessage(ctx, "continue")
	client.MarkUserMessagesDelivered(ctx, 1)
	if calls != 5 {
		t.Fatalf("barrier calls = %d, want 5", calls)
	}
	if !sawDeadline {
		t.Fatal("barrier context must be bounded")
	}

	client.SetWriteBarrier(nil)
	_ = client.WriteActivity(ctx, "turn_interrupted", "stopped", nil)
	if calls != 5 {
		t.Fatalf("barrier ran after removal (calls = %d)", calls)
	}
}

func TestWriteBarrierIsBounded(t *testing.T) {
	testStore := &metadataTestStore{session: &store.Session{ID: uuid.New()}}
	client := &Client{store: testStore, sessionID: testStore.session.ID}
	client.SetWriteBarrier(func(ctx context.Context) { <-ctx.Done() })
	start := time.Now()
	_ = client.WriteActivity(context.Background(), "x", "y", nil)
	if elapsed := time.Since(start); elapsed > writeBarrierTimeout+time.Second {
		t.Fatalf("barrier wait %v exceeded its bound", elapsed)
	}
}
