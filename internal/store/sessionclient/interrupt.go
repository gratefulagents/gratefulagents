package sessionclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

// metadataKeyInterrupt is the session metadata section carrying a user's
// request to stop the run's in-flight turn. The dashboard writes it; the
// runner's per-turn watcher consumes it and cancels the turn context.
const metadataKeyInterrupt = "interrupt"

// InterruptRequest asks the runner to stop whatever the current turn is doing
// (model call, tools, sub-agents) without terminating the run. The session
// stays alive and the agent waits for the next user message.
type InterruptRequest struct {
	ID          int64     `json:"id,omitempty"`
	RequestedAt time.Time `json:"requested_at"`
	RequestedBy string    `json:"requested_by,omitempty"`
}

// RequestInterrupt records an interrupt request on the session so the runner
// can stop the in-flight turn. It is written by the dashboard on behalf of
// the user pressing the stop button.
func RequestInterrupt(ctx context.Context, ss store.StateStore, sessionID uuid.UUID, requestedBy string) error {
	if interrupts, ok := ss.(store.InterruptStore); ok {
		_, _, err := interrupts.AppendInterrupt(ctx, sessionID, requestedBy)
		return err
	}
	req := InterruptRequest{
		RequestedAt: time.Now().UTC(),
		RequestedBy: strings.TrimSpace(requestedBy),
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshaling interrupt request: %w", err)
	}
	if err := ss.MergeSessionMetadata(ctx, sessionID, metadataKeyInterrupt, encoded); err != nil {
		return fmt.Errorf("merging session metadata %q: %w", metadataKeyInterrupt, err)
	}
	return nil
}

// PendingInterrupt returns the pending interrupt request, or nil when none is
// set.
func (c *Client) PendingInterrupt(ctx context.Context) (*InterruptRequest, error) {
	if interrupts, ok := c.store.(store.InterruptStore); ok {
		id, requestedAt, requestedBy, found, err := interrupts.PeekInterrupt(ctx, c.sessionID)
		if err != nil || !found {
			return nil, err
		}
		return &InterruptRequest{ID: id, RequestedAt: requestedAt, RequestedBy: requestedBy}, nil
	}
	metadata, err := c.readMetadataObject(ctx)
	if err != nil {
		return nil, err
	}
	return decodeInterruptRequest(metadata)
}

// ConsumeInterrupt claims the pending interrupt request: it returns the
// request (nil when none is pending) and clears it so it is honored exactly
// once. The runner is the single consumer, so read-then-clear is safe.
func (c *Client) ConsumeInterrupt(ctx context.Context) (*InterruptRequest, error) {
	if interrupts, ok := c.store.(store.InterruptStore); ok {
		id, requestedAt, requestedBy, found, err := interrupts.ConsumeInterrupt(ctx, c.sessionID)
		if err != nil || !found {
			return nil, err
		}
		return &InterruptRequest{ID: id, RequestedAt: requestedAt, RequestedBy: requestedBy}, nil
	}
	req, err := c.PendingInterrupt(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, nil
	}
	if err := c.ClearInterrupt(ctx); err != nil {
		return nil, err
	}
	return req, nil
}

// ClearInterrupt drops any pending interrupt request, e.g. a stale flag left
// over from a stop that raced the natural end of a turn.
func (c *Client) DrainInterruptsThrough(ctx context.Context, cutoff time.Time) (*InterruptRequest, error) {
	var applicable *InterruptRequest
	for {
		req, err := c.PendingInterrupt(ctx)
		if err != nil || req == nil || req.RequestedAt.After(cutoff) {
			return applicable, err
		}
		consumed, err := c.ConsumeInterrupt(ctx)
		if err != nil {
			return applicable, err
		}
		applicable = consumed
	}
}

func (c *Client) ClearInterrupt(ctx context.Context) error {
	if interrupts, ok := c.store.(store.InterruptStore); ok {
		for {
			_, _, _, found, err := interrupts.ConsumeInterrupt(ctx, c.sessionID)
			if err != nil || !found {
				return err
			}
		}
	}
	if err := c.store.MergeSessionMetadata(ctx, c.sessionID, metadataKeyInterrupt, json.RawMessage("null")); err != nil {
		return fmt.Errorf("clearing session metadata %q: %w", metadataKeyInterrupt, err)
	}
	return nil
}

func decodeInterruptRequest(metadata map[string]json.RawMessage) (*InterruptRequest, error) {
	raw := metadata[metadataKeyInterrupt]
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var req InterruptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decoding session metadata %q: %w", metadataKeyInterrupt, err)
	}
	if req.RequestedAt.IsZero() {
		return nil, nil
	}
	return &req, nil
}
