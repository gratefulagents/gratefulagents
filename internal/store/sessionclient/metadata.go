package sessionclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	metadataKeyMetrics            = "metrics"
	metadataKeyWorkingState       = "working_state"
	metadataKeySubAgentCheckpoint = "subagent_checkpoint"

	maxRecentTurnSummaries = 6
)

// SessionMetrics stores durable token/cost metadata without clobbering other session state.
type SessionMetrics struct {
	CostUSD       float64 `json:"cost_usd,omitempty"`
	InputTokens   int64   `json:"input_tokens,omitempty"`
	OutputTokens  int64   `json:"output_tokens,omitempty"`
	ToolCallCount int32   `json:"tool_call_count,omitempty"`
	// ContextTokens is the prompt-side size of the main agent's most recent
	// generation — the run's current context usage. ContextTriggerTokens is the
	// estimated context size at which the run's history compaction kicks in for
	// the active model; ContextTargetTokens is what compaction shrinks the
	// history to. Zero when unknown/disabled. The dashboard renders these as
	// the run's context-usage bar.
	ContextTokens        int64 `json:"context_tokens,omitempty"`
	ContextTriggerTokens int64 `json:"context_trigger_tokens,omitempty"`
	ContextTargetTokens  int64 `json:"context_target_tokens,omitempty"`
}

// WorkingState stores the compact, durable state used to rebuild context across long runs.
type WorkingState struct {
	Goal                  string   `json:"goal,omitempty"`
	CurrentMode           string   `json:"current_mode,omitempty"`
	CurrentPhase          string   `json:"current_phase,omitempty"`
	LastUserMessage       string   `json:"last_user_message,omitempty"`
	LastAssistantSummary  string   `json:"last_assistant_summary,omitempty"`
	RecentTurnSummaries   []string `json:"recent_turn_summaries,omitempty"`
	HistoryFloorMessageID int64    `json:"history_floor_message_id,omitempty"`
	LastResponseID        string   `json:"last_response_id,omitempty"`
	// LastStoppedUserMessageID is the durable cursor floor for a turn the user
	// explicitly stopped. Replacement pods must not auto-run that same prompt;
	// only a genuinely newer user message may resume the session.
	LastStoppedUserMessageID int64     `json:"last_stopped_user_message_id,omitempty"`
	UpdatedAt                time.Time `json:"updated_at,omitempty"`
}

func (w *WorkingState) normalize() {
	w.Goal = strings.TrimSpace(w.Goal)
	w.CurrentMode = strings.TrimSpace(w.CurrentMode)
	w.CurrentPhase = strings.TrimSpace(w.CurrentPhase)
	w.LastUserMessage = strings.TrimSpace(w.LastUserMessage)
	w.LastAssistantSummary = strings.TrimSpace(w.LastAssistantSummary)
	w.LastResponseID = strings.TrimSpace(w.LastResponseID)

	normalized := make([]string, 0, len(w.RecentTurnSummaries))
	for _, summary := range w.RecentTurnSummaries {
		summary = strings.TrimSpace(summary)
		if summary == "" {
			continue
		}
		normalized = append(normalized, summary)
	}
	if len(normalized) > maxRecentTurnSummaries {
		normalized = normalized[len(normalized)-maxRecentTurnSummaries:]
	}
	w.RecentTurnSummaries = normalized
	w.UpdatedAt = time.Now().UTC()
}

// ReadWorkingState loads the durable working state from session metadata.
func (c *Client) ReadWorkingState(ctx context.Context) (WorkingState, error) {
	metadata, err := c.readMetadataObject(ctx)
	if err != nil {
		return WorkingState{}, err
	}
	var state WorkingState
	if err := decodeMetadataSection(metadata, metadataKeyWorkingState, &state); err != nil {
		return WorkingState{}, err
	}
	return state, nil
}

// UpdateWorkingState applies a mutation to the durable working state.
func (c *Client) UpdateWorkingState(ctx context.Context, mutate func(*WorkingState) error) error {
	return c.updateMetadataSection(ctx, metadataKeyWorkingState, func(raw json.RawMessage) (json.RawMessage, error) {
		var state WorkingState
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &state); err != nil {
				return nil, fmt.Errorf("decoding session metadata %q: %w", metadataKeyWorkingState, err)
			}
		}
		if err := mutate(&state); err != nil {
			return nil, err
		}
		state.normalize()

		encoded, err := json.Marshal(state)
		if err != nil {
			return nil, fmt.Errorf("marshaling working state: %w", err)
		}
		return encoded, nil
	})
}

// ReadSubAgentCheckpoint loads the host-defined async sub-agent scheduler
// checkpoint from durable session metadata. A nil result means no scheduler
// state has been persisted for this session yet. The session client treats the
// payload as opaque JSON so SDK checkpoint schema changes remain SDK-owned.
func (c *Client) ReadSubAgentCheckpoint(ctx context.Context) (json.RawMessage, error) {
	metadata, err := c.readMetadataObject(ctx)
	if err != nil {
		return nil, err
	}
	raw := metadata[metadataKeySubAgentCheckpoint]
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("decoding session metadata %q: invalid JSON", metadataKeySubAgentCheckpoint)
	}
	return append(json.RawMessage(nil), raw...), nil
}

// WriteSubAgentCheckpoint atomically replaces the durable scheduler checkpoint
// without clobbering working state, metrics, interrupts, or encryption keys.
func (c *Client) WriteSubAgentCheckpoint(ctx context.Context, checkpoint json.RawMessage) error {
	if len(checkpoint) == 0 {
		checkpoint = json.RawMessage("null")
	}
	if !json.Valid(checkpoint) {
		return fmt.Errorf("marshaling session metadata %q: invalid JSON", metadataKeySubAgentCheckpoint)
	}
	if err := c.store.MergeSessionMetadata(ctx, c.sessionID, metadataKeySubAgentCheckpoint, checkpoint); err != nil {
		return fmt.Errorf("merging session metadata %q: %w", metadataKeySubAgentCheckpoint, err)
	}
	return nil
}

// ResetConversationWindow advances the durable history floor to the latest
// stored message so future prompt assembly ignores prior turns.
func (c *Client) ResetConversationWindow(ctx context.Context) error {
	messages, err := c.store.GetMessages(ctx, c.sessionID)
	if err != nil {
		return fmt.Errorf("loading messages: %w", err)
	}
	var floor int64
	if len(messages) > 0 {
		floor = messages[len(messages)-1].ID
	}
	return c.UpdateWorkingState(ctx, func(state *WorkingState) error {
		state.Goal = ""
		state.LastUserMessage = ""
		state.LastAssistantSummary = ""
		state.RecentTurnSummaries = nil
		state.LastResponseID = ""
		state.HistoryFloorMessageID = floor
		return nil
	})
}

func (c *Client) readMetadataObject(ctx context.Context) (map[string]json.RawMessage, error) {
	session, err := c.store.GetSession(ctx, c.sessionID)
	if err != nil {
		return nil, fmt.Errorf("loading session metadata: %w", err)
	}
	if len(session.Metadata) == 0 {
		return map[string]json.RawMessage{}, nil
	}

	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(session.Metadata, &metadata); err != nil {
		return nil, fmt.Errorf("decoding session metadata: %w", err)
	}
	if metadata == nil {
		metadata = map[string]json.RawMessage{}
	}
	return metadata, nil
}

func (c *Client) updateMetadataSection(ctx context.Context, key string, mutate func(json.RawMessage) (json.RawMessage, error)) error {
	metadata, err := c.readMetadataObject(ctx)
	if err != nil {
		return err
	}
	encoded, err := mutate(metadata[key])
	if err != nil {
		return err
	}
	if err := c.store.MergeSessionMetadata(ctx, c.sessionID, key, encoded); err != nil {
		return fmt.Errorf("merging session metadata %q: %w", key, err)
	}
	return nil
}

func decodeMetadataSection[T any](metadata map[string]json.RawMessage, key string, out *T) error {
	raw := metadata[key]
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decoding session metadata %q: %w", key, err)
	}
	return nil
}
