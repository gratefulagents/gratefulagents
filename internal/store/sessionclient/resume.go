package sessionclient

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

const metadataKeyResume = "resume_request"
const metadataKeyResumeConsumed = "resume_consumed"

// ResumeRequest continues persisted work without adding a conversation message.
type ResumeRequest struct {
	ID               string    `json:"id"`
	PendingRequestID string    `json:"pending_request_id,omitempty"`
	RequestedAt      time.Time `json:"requested_at"`
}

func RequestResume(ctx context.Context, ss store.StateStore, sessionID uuid.UUID, id, pendingRequestID string) error {
	if id == "" {
		id = uuid.NewString()
	}
	sess, err := ss.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	var metadata map[string]json.RawMessage
	if len(sess.Metadata) > 0 {
		if err := json.Unmarshal(sess.Metadata, &metadata); err != nil {
			return err
		}
	}
	var previous ResumeRequest
	if raw := metadata[metadataKeyResume]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &previous); err != nil {
			return err
		}
		if previous.ID == id {
			return nil
		}
	}
	encoded, err := json.Marshal(ResumeRequest{ID: id, PendingRequestID: pendingRequestID, RequestedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	return ss.MergeSessionMetadata(ctx, sessionID, metadataKeyResume, encoded)
}

func (c *Client) PendingResume(ctx context.Context) (*ResumeRequest, error) {
	metadata, err := c.readMetadataObject(ctx)
	if err != nil {
		return nil, err
	}
	raw := metadata[metadataKeyResume]
	if len(raw) == 0 {
		return nil, nil
	}
	var req ResumeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	var consumed string
	if raw := metadata[metadataKeyResumeConsumed]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &consumed); err != nil {
			return nil, err
		}
	}
	if req.ID == "" || req.ID == consumed {
		return nil, nil
	}
	return &req, nil
}

// AcknowledgeResume retires only the request whose work was durably handled.
func (c *Client) AcknowledgeResume(ctx context.Context, req *ResumeRequest) error {
	if req == nil {
		return nil
	}
	// A separate acknowledgement cannot erase a newer request written concurrently.
	encoded, _ := json.Marshal(req.ID)
	return c.store.MergeSessionMetadata(ctx, c.sessionID, metadataKeyResumeConsumed, encoded)
}
