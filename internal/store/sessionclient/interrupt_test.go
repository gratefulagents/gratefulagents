package sessionclient

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

func TestInterruptRequestRoundTrip(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	testStore := &metadataTestStore{
		session: &store.Session{ID: sessionID},
	}
	client := &Client{store: testStore, sessionID: sessionID}
	ctx := context.Background()

	// No request pending initially.
	if req, err := client.PendingInterrupt(ctx); err != nil || req != nil {
		t.Fatalf("PendingInterrupt() = (%v, %v), want (nil, nil)", req, err)
	}

	// Dashboard-side write.
	if err := RequestInterrupt(ctx, testStore, sessionID, "user-1"); err != nil {
		t.Fatalf("RequestInterrupt() error = %v", err)
	}

	req, err := client.PendingInterrupt(ctx)
	if err != nil {
		t.Fatalf("PendingInterrupt() error = %v", err)
	}
	if req == nil {
		t.Fatal("PendingInterrupt() = nil, want request")
	}
	if req.RequestedBy != "user-1" {
		t.Fatalf("RequestedBy = %q, want user-1", req.RequestedBy)
	}
	if req.RequestedAt.IsZero() {
		t.Fatal("RequestedAt is zero, want set")
	}

	// Consume claims exactly once.
	claimed, err := client.ConsumeInterrupt(ctx)
	if err != nil {
		t.Fatalf("ConsumeInterrupt() error = %v", err)
	}
	if claimed == nil || claimed.RequestedBy != "user-1" {
		t.Fatalf("ConsumeInterrupt() = %v, want request from user-1", claimed)
	}
	if again, err := client.ConsumeInterrupt(ctx); err != nil || again != nil {
		t.Fatalf("second ConsumeInterrupt() = (%v, %v), want (nil, nil)", again, err)
	}
}

func TestInterruptDoesNotClobberOtherMetadataSections(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	testStore := &metadataTestStore{
		session: &store.Session{ID: sessionID},
	}
	client := &Client{store: testStore, sessionID: sessionID}
	ctx := context.Background()

	if err := client.UpdateWorkingState(ctx, func(state *WorkingState) error {
		state.Goal = "keep this goal"
		return nil
	}); err != nil {
		t.Fatalf("UpdateWorkingState() error = %v", err)
	}

	if err := RequestInterrupt(ctx, testStore, sessionID, "user-2"); err != nil {
		t.Fatalf("RequestInterrupt() error = %v", err)
	}
	if err := client.ClearInterrupt(ctx); err != nil {
		t.Fatalf("ClearInterrupt() error = %v", err)
	}

	state, err := client.ReadWorkingState(ctx)
	if err != nil {
		t.Fatalf("ReadWorkingState() error = %v", err)
	}
	if state.Goal != "keep this goal" {
		t.Fatalf("Goal = %q, want preserved", state.Goal)
	}
	if req, err := client.PendingInterrupt(ctx); err != nil || req != nil {
		t.Fatalf("PendingInterrupt() after clear = (%v, %v), want (nil, nil)", req, err)
	}
}

func TestClearInterruptWithoutPendingRequestIsNoop(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	testStore := &metadataTestStore{
		session: &store.Session{ID: sessionID},
	}
	client := &Client{store: testStore, sessionID: sessionID}

	if err := client.ClearInterrupt(context.Background()); err != nil {
		t.Fatalf("ClearInterrupt() error = %v", err)
	}
	if req, err := client.PendingInterrupt(context.Background()); err != nil || req != nil {
		t.Fatalf("PendingInterrupt() = (%v, %v), want (nil, nil)", req, err)
	}
}
