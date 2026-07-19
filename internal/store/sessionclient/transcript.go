package sessionclient

import (
	"context"
	"fmt"
)

// SaveTranscriptBlob upserts the session's serialized transcript snapshot.
// The blob is opaque to the store layer; the agent loop owns the encoding
// (versioned JSON + gzip, size-capped). Empty data clears the snapshot so a
// resume falls back to the durable conversation tail.
func (c *Client) SaveTranscriptBlob(ctx context.Context, data []byte, itemCount int) error {
	if len(data) == 0 {
		return c.ClearTranscriptBlob(ctx)
	}
	if err := c.store.UpsertSessionTranscript(ctx, c.sessionID, data, int32(itemCount)); err != nil {
		return fmt.Errorf("saving transcript snapshot: %w", err)
	}
	return nil
}

// LoadTranscriptBlob returns the stored transcript snapshot, or (nil, nil)
// when none exists.
func (c *Client) LoadTranscriptBlob(ctx context.Context) ([]byte, error) {
	data, err := c.store.GetSessionTranscript(ctx, c.sessionID)
	if err != nil {
		return nil, fmt.Errorf("loading transcript snapshot: %w", err)
	}
	return data, nil
}

// ClearTranscriptBlob deletes the stored transcript snapshot.
func (c *Client) ClearTranscriptBlob(ctx context.Context) error {
	if err := c.store.DeleteSessionTranscript(ctx, c.sessionID); err != nil {
		return fmt.Errorf("clearing transcript snapshot: %w", err)
	}
	return nil
}
