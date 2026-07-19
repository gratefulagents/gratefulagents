package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

type getRunMessagesTool struct{ maintainerToolBase }

type getRunMessagesInput struct {
	RunName     string `json:"run_name"`
	PendingOnly bool   `json:"pending_only,omitempty"`
	Cursor      int64  `json:"cursor,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type maintainerMessage struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Truncated bool      `json:"truncated"`
	CreatedAt time.Time `json:"created_at"`
	State     string    `json:"state,omitempty"`
	Source    string    `json:"source,omitempty"`
}

type getRunMessagesOutput struct {
	Messages   []maintainerMessage `json:"messages"`
	NextCursor int64               `json:"next_cursor"`
	HasMore    bool                `json:"has_more"`
}

func (t *getRunMessagesTool) Name() string { return "get_run_messages" }
func (t *getRunMessagesTool) Description() string {
	return "Read cursor-paged messages for one authorized fleet AgentRun, including queued, steering, delivered, and cancelled user-message state."
}
func (t *getRunMessagesTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"run_name":{"type":"string"},"pending_only":{"type":"boolean","default":false},"cursor":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":200,"default":50}},"required":["run_name"]}`)
}
func (t *getRunMessagesTool) IsReadOnly() bool                      { return true }
func (t *getRunMessagesTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *getRunMessagesTool) NeedsApproval() bool                   { return false }
func (t *getRunMessagesTool) TimeoutSeconds() int                   { return 0 }

func (t *getRunMessagesTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in getRunMessagesInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if strings.TrimSpace(in.RunName) == "" || in.Cursor < 0 || in.Limit < 0 || in.Limit > 200 {
		return Result{Content: "run_name is required; cursor must be non-negative and limit must be between 1 and 200", IsError: true}, nil
	}
	limit := in.Limit
	if limit == 0 {
		limit = 50
	}

	_, messages, err := t.runMessages(ctx, in.RunName, true)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	firstUserID := maintainerFirstUserMessageID(messages)
	candidates := make([]maintainerMessage, 0, len(messages))
	for _, message := range messages {
		if message.ID <= in.Cursor {
			continue
		}
		entry := maintainerMessageFromStore(message, firstUserID, 2000)
		if in.PendingOnly && (message.Role != "user" || message.ID == firstUserID || entry.State == "cancelled" || (entry.State != "queued" && entry.State != "steering")) {
			continue
		}
		candidates = append(candidates, entry)
	}

	out := getRunMessagesOutput{Messages: make([]maintainerMessage, 0, min(limit, len(candidates))), NextCursor: in.Cursor}
	budget := supervisedActivityStreamBudget
	for _, candidate := range candidates {
		if len(out.Messages) == limit {
			break
		}
		cost := maintainerMessageCost(candidate)
		if len(out.Messages) > 0 && cost > budget {
			break
		}
		if cost > budget {
			candidate.Content, candidate.Truncated = maintainerTruncateContent(candidate.Content, candidate.Truncated, max(0, budget-len(candidate.Role)-len(candidate.Source)-112))
			cost = maintainerMessageCost(candidate)
		}
		out.Messages = append(out.Messages, candidate)
		out.NextCursor = candidate.ID
		budget -= min(cost, budget)
	}
	out.HasMore = len(out.Messages) < len(candidates)
	encoded, err := json.Marshal(out)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}

type cancelRunMessageTool struct{ maintainerToolBase }

type cancelRunMessageInput struct {
	RunName   string `json:"run_name"`
	MessageID int64  `json:"message_id"`
}

func (t *cancelRunMessageTool) Name() string { return "cancel_run_message" }
func (t *cancelRunMessageTool) Description() string {
	return "Cancel one undelivered non-kickoff user message for an authorized fleet AgentRun."
}
func (t *cancelRunMessageTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"run_name":{"type":"string"},"message_id":{"type":"integer","minimum":1}},"required":["run_name","message_id"]}`)
}
func (t *cancelRunMessageTool) IsReadOnly() bool                      { return false }
func (t *cancelRunMessageTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *cancelRunMessageTool) NeedsApproval() bool                   { return false }
func (t *cancelRunMessageTool) TimeoutSeconds() int                   { return 0 }

func (t *cancelRunMessageTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in cancelRunMessageInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if strings.TrimSpace(in.RunName) == "" || in.MessageID <= 0 {
		return Result{Content: "run_name is required and message_id must be greater than zero", IsError: true}, nil
	}
	session, messages, err := t.runMessages(ctx, in.RunName, false)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if _, err := maintainerCanReplaceMessage(messages, in.MessageID); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if err := t.stateStore.CancelUndeliveredUserMessage(ctx, session.ID, in.MessageID); err != nil {
		switch {
		case errors.Is(err, store.ErrMessageDelivered):
			return Result{Content: "message was already consumed by the run", IsError: true}, nil
		case errors.Is(err, store.ErrMessageNotFound):
			return Result{Content: "message not found", IsError: true}, nil
		default:
			return Result{Content: fmt.Sprintf("failed to cancel message: %v", err), IsError: true}, nil
		}
	}
	return Result{Content: fmt.Sprintf("Cancelled message %d.", in.MessageID)}, nil
}

type editRunMessageTool struct{ maintainerToolBase }

type editRunMessageInput struct {
	RunName   string `json:"run_name"`
	MessageID int64  `json:"message_id"`
	Content   string `json:"content"`
}

type editRunMessageOutput struct {
	OldMessageID int64 `json:"old_message_id"`
	NewMessageID int64 `json:"new_message_id"`
}

func (t *editRunMessageTool) Name() string { return "edit_run_message" }
func (t *editRunMessageTool) Description() string {
	return "Replace one undelivered non-kickoff user message for an authorized fleet AgentRun; the replacement is re-queued at the end of the queue and does not retain steering mode."
}
func (t *editRunMessageTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"run_name":{"type":"string"},"message_id":{"type":"integer","minimum":1},"content":{"type":"string","minLength":1,"maxLength":8000}},"required":["run_name","message_id","content"]}`)
}
func (t *editRunMessageTool) IsReadOnly() bool                      { return false }
func (t *editRunMessageTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *editRunMessageTool) NeedsApproval() bool                   { return false }
func (t *editRunMessageTool) TimeoutSeconds() int                   { return 0 }

func (t *editRunMessageTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in editRunMessageInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if strings.TrimSpace(in.RunName) == "" || in.MessageID <= 0 || strings.TrimSpace(in.Content) == "" {
		return Result{Content: "run_name, message_id, and non-blank content are required", IsError: true}, nil
	}
	if utf8.RuneCountInString(in.Content) > 8000 {
		return Result{Content: "content must be at most 8000 characters", IsError: true}, nil
	}
	session, messages, err := t.runMessages(ctx, in.RunName, false)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	message, err := maintainerCanReplaceMessage(messages, in.MessageID)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if err := t.stateStore.CancelUndeliveredUserMessage(ctx, session.ID, in.MessageID); err != nil {
		switch {
		case errors.Is(err, store.ErrMessageDelivered):
			return Result{Content: "message was already consumed by the run", IsError: true}, nil
		case errors.Is(err, store.ErrMessageNotFound):
			return Result{Content: "message not found", IsError: true}, nil
		default:
			return Result{Content: fmt.Sprintf("failed to cancel message: %v", err), IsError: true}, nil
		}
	}

	source := maintainerMessageSource(message.Metadata)
	if source == "" {
		source = "maintainer"
	}
	metadata := map[string]any{
		"source":         source,
		"maintainer_run": t.currentRunName,
		"edited_from":    message.ID,
	}
	if maintainerMessageSource(message.Metadata) != "" {
		metadata["maintainer_edited"] = true
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return Result{}, err
	}
	newMessage, err := t.stateStore.AppendMessage(ctx, session.ID, "user", in.Content, encodedMetadata)
	if err != nil {
		return Result{Content: fmt.Sprintf("old message was already cancelled, but failed to append replacement: %v", err), IsError: true}, nil
	}
	encoded, err := json.Marshal(editRunMessageOutput{OldMessageID: message.ID, NewMessageID: newMessage.ID})
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}

type getRunTranscriptTool struct{ maintainerToolBase }

type getRunTranscriptInput struct {
	RunName         string `json:"run_name"`
	Cursor          int64  `json:"cursor,omitempty"`
	ActivityCursor  int64  `json:"activity_cursor,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	IncludeActivity bool   `json:"include_activity,omitempty"`
}

type maintainerTranscriptMessage struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Truncated bool      `json:"truncated"`
	CreatedAt time.Time `json:"created_at"`
	Pending   *bool     `json:"pending,omitempty"`
}

type maintainerTranscriptEvent struct {
	ID        int64           `json:"id"`
	EventType string          `json:"event_type"`
	Summary   string          `json:"summary"`
	Detail    json.RawMessage `json:"detail,omitempty"`
	Truncated bool            `json:"truncated"`
	CreatedAt time.Time       `json:"created_at"`
}

type getRunTranscriptOutput struct {
	Messages           []maintainerTranscriptMessage `json:"messages"`
	Activity           []maintainerTranscriptEvent   `json:"activity"`
	NextCursor         int64                         `json:"next_cursor"`
	NextActivityCursor int64                         `json:"next_activity_cursor"`
	HasMore            bool                          `json:"has_more"`
}

func (t *getRunTranscriptTool) Name() string { return "get_run_transcript" }
func (t *getRunTranscriptTool) Description() string {
	return "Read cursor-paged transcript messages for one authorized fleet AgentRun and, when requested, its independent cursor-paged activity log."
}
func (t *getRunTranscriptTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"run_name":{"type":"string"},"cursor":{"type":"integer","minimum":0},"activity_cursor":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":200,"default":100},"include_activity":{"type":"boolean","default":false}},"required":["run_name"]}`)
}
func (t *getRunTranscriptTool) IsReadOnly() bool                      { return true }
func (t *getRunTranscriptTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *getRunTranscriptTool) NeedsApproval() bool                   { return false }
func (t *getRunTranscriptTool) TimeoutSeconds() int                   { return 0 }

func (t *getRunTranscriptTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in getRunTranscriptInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if strings.TrimSpace(in.RunName) == "" || in.Cursor < 0 || in.ActivityCursor < 0 || in.Limit < 0 || in.Limit > 200 {
		return Result{Content: "run_name is required; cursors must be non-negative and limit must be between 1 and 200", IsError: true}, nil
	}
	limit := in.Limit
	if limit == 0 {
		limit = 100
	}
	session, messages, err := t.runMessages(ctx, in.RunName, false)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}

	firstUserID := maintainerFirstUserMessageID(messages)
	messageCandidates := make([]maintainerTranscriptMessage, 0, len(messages))
	for _, message := range messages {
		if message.ID <= in.Cursor || (message.Role == "user" && sessionclient.UserMessageCancelled(message.Metadata)) {
			continue
		}
		content, truncated := maintainerTruncateContent(message.Content, false, 4000)
		entry := maintainerTranscriptMessage{ID: message.ID, Role: message.Role, Content: content, Truncated: truncated, CreatedAt: message.CreatedAt}
		if message.Role == "user" {
			_, deliveredAt := sessionclient.UserMessageStateFromMetadata(message.Metadata)
			pending := (message.DeliveryState == "pending" || message.DeliveryState == "" && deliveredAt == 0) && message.ID != firstUserID
			entry.Pending = &pending
		}
		messageCandidates = append(messageCandidates, entry)
	}
	out := getRunTranscriptOutput{
		Messages:           make([]maintainerTranscriptMessage, 0, min(limit, len(messageCandidates))),
		Activity:           []maintainerTranscriptEvent{},
		NextCursor:         in.Cursor,
		NextActivityCursor: in.ActivityCursor,
	}
	messageBudget := 32 * 1024
	for _, candidate := range messageCandidates {
		if len(out.Messages) == limit {
			break
		}
		cost := len(candidate.Content) + len(candidate.Role) + 96
		if len(out.Messages) > 0 && cost > messageBudget {
			break
		}
		if cost > messageBudget {
			candidate.Content, candidate.Truncated = maintainerTruncateContent(candidate.Content, candidate.Truncated, max(0, messageBudget-len(candidate.Role)-96))
			cost = len(candidate.Content) + len(candidate.Role) + 96
		}
		out.Messages = append(out.Messages, candidate)
		out.NextCursor = candidate.ID
		messageBudget -= min(cost, messageBudget)
	}

	activityCandidates := []maintainerTranscriptEvent{}
	if in.IncludeActivity {
		events, err := t.stateStore.GetActivityEventsSince(ctx, session.ID, in.ActivityCursor)
		if err != nil {
			return Result{Content: fmt.Sprintf("failed to read fleet activity: %v", err), IsError: true}, nil
		}
		activityCandidates = make([]maintainerTranscriptEvent, 0, len(events))
		for _, event := range events {
			summary, truncated := maintainerTruncateContent(event.Summary, false, 4000)
			detail := event.Detail
			if len(detail) > 4000 {
				detail = json.RawMessage(`{"truncated":true}`)
				truncated = true
			}
			activityCandidates = append(activityCandidates, maintainerTranscriptEvent{ID: event.ID, EventType: event.EventType, Summary: summary, Detail: detail, Truncated: truncated, CreatedAt: event.CreatedAt})
		}
		out.Activity = make([]maintainerTranscriptEvent, 0, min(limit, len(activityCandidates)))
		activityBudget := 32 * 1024
		for _, candidate := range activityCandidates {
			if len(out.Activity) == limit {
				break
			}
			cost := len(candidate.Summary) + len(candidate.Detail) + len(candidate.EventType) + 112
			if len(out.Activity) > 0 && cost > activityBudget {
				break
			}
			if cost > activityBudget {
				candidate.Summary, candidate.Truncated = maintainerTruncateContent(candidate.Summary, candidate.Truncated, max(0, activityBudget-len(candidate.Detail)-len(candidate.EventType)-112))
				cost = len(candidate.Summary) + len(candidate.Detail) + len(candidate.EventType) + 112
			}
			out.Activity = append(out.Activity, candidate)
			out.NextActivityCursor = candidate.ID
			activityBudget -= min(cost, activityBudget)
		}
	}
	out.HasMore = len(out.Messages) < len(messageCandidates) || len(out.Activity) < len(activityCandidates)
	encoded, err := json.Marshal(out)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}

func (t maintainerToolBase) runMessages(ctx context.Context, runName string, includeCancelled bool) (*store.Session, []store.Message, error) {
	if _, err := t.currentRun(ctx); err != nil {
		return nil, nil, err
	}
	run, err := t.fleetRun(ctx, strings.TrimSpace(runName))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to verify fleet AgentRun: %v", err)
	}
	session, err := t.stateStore.GetSessionByRun(ctx, run.Name, run.Namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve fleet session: %v", err)
	}
	if session == nil {
		return nil, nil, fmt.Errorf("fleet session not found")
	}
	var messages []store.Message
	if includeCancelled {
		messages, err = t.stateStore.GetMessagesIncludingCancelled(ctx, session.ID)
	} else {
		messages, err = t.stateStore.GetMessages(ctx, session.ID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read fleet messages: %v", err)
	}
	return session, messages, nil
}

func maintainerFirstUserMessageID(messages []store.Message) int64 {
	for _, message := range messages {
		if message.Role == "user" {
			return message.ID
		}
	}
	return 0
}

func maintainerMessageFromStore(message store.Message, firstUserID int64, maxContentBytes int) maintainerMessage {
	content, truncated := maintainerTruncateContent(message.Content, false, maxContentBytes)
	entry := maintainerMessage{ID: message.ID, Role: message.Role, Content: content, Truncated: truncated, CreatedAt: message.CreatedAt}
	if message.Role != "user" {
		return entry
	}
	entry.Source = maintainerMessageSource(message.Metadata)
	mode, deliveredAt := sessionclient.UserMessageStateFromMetadata(message.Metadata)
	switch {
	case sessionclient.UserMessageCancelled(message.Metadata):
		entry.State = "cancelled"
	case message.ID == firstUserID || deliveredAt > 0 || (message.DeliveryState != "pending" && message.DeliveryState != ""):
		entry.State = "delivered"
	case mode == sessionclient.UserMessageModeImmediate:
		entry.State = "steering"
	default:
		entry.State = "queued"
	}
	return entry
}

func maintainerMessageSource(metadata json.RawMessage) string {
	var values map[string]any
	if json.Unmarshal(metadata, &values) != nil {
		return ""
	}
	source, _ := values["source"].(string)
	return source
}

func maintainerMessageCost(message maintainerMessage) int {
	return len(message.Content) + len(message.Role) + len(message.Source) + 112
}

func maintainerTruncateContent(content string, alreadyTruncated bool, maxBytes int) (string, bool) {
	if len(content) <= maxBytes {
		return content, alreadyTruncated
	}
	return truncateUTF8(content, maxBytes), true
}

func maintainerCanReplaceMessage(messages []store.Message, messageID int64) (*store.Message, error) {
	var selected *store.Message
	for i := range messages {
		if messages[i].ID == messageID {
			selected = &messages[i]
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("message not found")
	}
	if selected.Role != "user" {
		return nil, fmt.Errorf("only user messages can be changed")
	}
	if selected.ID == maintainerFirstUserMessageID(messages) {
		return nil, fmt.Errorf("kickoff message cannot be changed")
	}
	return selected, nil
}
