package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

type maintainerMessagesTestStore struct {
	store.StateStore
	sessions                               map[string]*store.Session
	messages                               []store.Message
	activity                               []store.ActivityEvent
	cancelErr                              error
	appendErr                              error
	cancelledMessage                       int64
	activityReadCalls                      int
	getMessagesCalls                       int
	getMessagesIncludingCancelledReadCalls int
}

func (s *maintainerMessagesTestStore) GetSessionByRun(_ context.Context, name, namespace string) (*store.Session, error) {
	return s.sessions[namespace+"/"+name], nil
}

func (s *maintainerMessagesTestStore) GetMessages(_ context.Context, _ uuid.UUID) ([]store.Message, error) {
	s.getMessagesCalls++
	messages := make([]store.Message, 0, len(s.messages))
	for _, message := range s.messages {
		var metadata map[string]json.RawMessage
		if message.Role == "user" && json.Unmarshal(message.Metadata, &metadata) == nil && metadata["cancelled_at_unix"] != nil {
			continue
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func (s *maintainerMessagesTestStore) GetMessagesIncludingCancelled(_ context.Context, _ uuid.UUID) ([]store.Message, error) {
	s.getMessagesIncludingCancelledReadCalls++
	return append([]store.Message(nil), s.messages...), nil
}

func (s *maintainerMessagesTestStore) CancelUndeliveredUserMessage(_ context.Context, _ uuid.UUID, messageID int64) error {
	s.cancelledMessage = messageID
	return s.cancelErr
}

func (s *maintainerMessagesTestStore) AppendMessage(_ context.Context, sessionID uuid.UUID, role, content string, metadata json.RawMessage) (*store.Message, error) {
	if s.appendErr != nil {
		return nil, s.appendErr
	}
	message := store.Message{ID: int64(len(s.messages) + 1), SessionID: sessionID, Role: role, Content: content, Metadata: metadata}
	s.messages = append(s.messages, message)
	return &message, nil
}

func (s *maintainerMessagesTestStore) GetActivityEventsSince(_ context.Context, _ uuid.UUID, _ int64) ([]store.ActivityEvent, error) {
	s.activityReadCalls++
	return append([]store.ActivityEvent(nil), s.activity...), nil
}

func newMaintainerMessagesToolBase(t *testing.T, runs ...*platformv1alpha1.AgentRun) (maintainerToolBase, *maintainerMessagesTestStore) {
	t.Helper()
	base, _, _ := newMaintainerToolBase(t, runs...)
	stateStore := &maintainerMessagesTestStore{sessions: map[string]*store.Session{}}
	for _, run := range runs {
		stateStore.sessions[run.Namespace+"/"+run.Name] = &store.Session{ID: uuid.New(), AgentRunName: run.Name, AgentRunNS: run.Namespace}
	}
	base.stateStore = stateStore
	return base, stateStore
}

func TestGetRunMessagesStatesAndPaging(t *testing.T) {
	t.Parallel()
	maintainer, target := maintainerRun(), fleetRun("target", platformv1alpha1.AgentRunPhaseRunning)
	base, stateStore := newMaintainerMessagesToolBase(t, maintainer, target)
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	stateStore.messages = []store.Message{
		{ID: 1, Role: "user", Content: "kickoff", Metadata: json.RawMessage(`{"mode":"enqueue","source":"human"}`), DeliveryState: "pending", CreatedAt: created},
		{ID: 2, Role: "assistant", Content: "working", CreatedAt: created},
		{ID: 3, Role: "user", Content: "steer", Metadata: json.RawMessage(`{"mode":"immediate","source":"maintainer"}`), DeliveryState: "pending", CreatedAt: created},
		{ID: 4, Role: "user", Content: "queued", Metadata: json.RawMessage(`{"source":"human"}`), DeliveryState: "", CreatedAt: created},
		{ID: 5, Role: "user", Content: "cancelled", Metadata: json.RawMessage(`{"cancelled_at_unix":1,"source":"human"}`), DeliveryState: "cancelled", CreatedAt: created},
		{ID: 6, Role: "user", Content: "delivered", Metadata: json.RawMessage(`{"delivered_at_unix":2,"source":"human"}`), DeliveryState: "completed", CreatedAt: created},
	}
	tool := &getRunMessagesTool{maintainerToolBase: base}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"run_name":"target"}`), "")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%#v, %v)", result, err)
	}
	var out getRunMessagesOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 6 || out.Messages[0].State != "delivered" || out.Messages[2].State != "steering" || out.Messages[3].State != "queued" || out.Messages[4].State != "cancelled" || out.Messages[5].State != "delivered" || out.Messages[3].Source != "human" {
		t.Fatalf("messages = %#v", out.Messages)
	}
	if stateStore.getMessagesCalls != 0 || stateStore.getMessagesIncludingCancelledReadCalls != 1 {
		t.Fatalf("message reads = normal:%d including-cancelled:%d", stateStore.getMessagesCalls, stateStore.getMessagesIncludingCancelledReadCalls)
	}

	result, err = tool.Execute(context.Background(), json.RawMessage(`{"run_name":"target","pending_only":true}`), "")
	if err != nil || result.IsError {
		t.Fatalf("pending Execute() = (%#v, %v)", result, err)
	}
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 2 || out.Messages[0].ID != 3 || out.Messages[1].ID != 4 || out.HasMore {
		t.Fatalf("pending messages = %#v", out)
	}

	result, err = tool.Execute(context.Background(), json.RawMessage(`{"run_name":"target","limit":2}`), "")
	if err != nil || result.IsError {
		t.Fatalf("paged Execute() = (%#v, %v)", result, err)
	}
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 2 || out.NextCursor != 2 || !out.HasMore {
		t.Fatalf("first page = %#v", out)
	}
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"run_name":"target","cursor":2,"limit":2}`), "")
	if err != nil || result.IsError {
		t.Fatalf("second page Execute() = (%#v, %v)", result, err)
	}
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 2 || out.Messages[0].ID != 3 || out.Messages[1].ID != 4 || out.NextCursor != 4 || !out.HasMore {
		t.Fatalf("second page = %#v", out)
	}
}

func TestCancelRunMessageValidationAndErrors(t *testing.T) {
	t.Parallel()
	maintainer, target := maintainerRun(), fleetRun("target", platformv1alpha1.AgentRunPhaseSucceeded)
	base, stateStore := newMaintainerMessagesToolBase(t, maintainer, target)
	stateStore.messages = []store.Message{{ID: 1, Role: "user", Content: "kickoff"}, {ID: 2, Role: "assistant", Content: "answer"}, {ID: 3, Role: "user", Content: "queued"}}
	tool := &cancelRunMessageTool{maintainerToolBase: base}
	for _, input := range []string{`{"run_name":"target","message_id":1}`, `{"run_name":"target","message_id":2}`} {
		result, err := tool.Execute(context.Background(), json.RawMessage(input), "")
		if err != nil || !result.IsError {
			t.Fatalf("Execute(%s) = (%#v, %v)", input, result, err)
		}
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{store.ErrMessageDelivered, "already consumed"},
		{store.ErrMessageNotFound, "message not found"},
	} {
		stateStore.cancelErr = tc.err
		result, err := tool.Execute(context.Background(), json.RawMessage(`{"run_name":"target","message_id":3}`), "")
		if err != nil || !result.IsError || !strings.Contains(result.Content, tc.want) {
			t.Fatalf("Execute(%v) = (%#v, %v)", tc.err, result, err)
		}
	}
	stateStore.cancelErr = nil
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"run_name":"target","message_id":3}`), "")
	if err != nil || result.IsError || stateStore.cancelledMessage != 3 {
		t.Fatalf("Execute() = (%#v, %v), cancelled=%d", result, err, stateStore.cancelledMessage)
	}
}

func TestEditRunMessageMetadataAndAppendFailure(t *testing.T) {
	t.Parallel()
	maintainer, target := maintainerRun(), fleetRun("target", platformv1alpha1.AgentRunPhaseRunning)
	base, stateStore := newMaintainerMessagesToolBase(t, maintainer, target)
	stateStore.messages = []store.Message{{ID: 1, Role: "user", Content: "kickoff"}, {ID: 2, Role: "user", Content: "old", Metadata: json.RawMessage(`{"source":"github/webhook","mode":"immediate"}`)}}
	tool := &editRunMessageTool{maintainerToolBase: base}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"run_name":"target","message_id":2,"content":"  exact replacement  "}`), "")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%#v, %v)", result, err)
	}
	if len(stateStore.messages) != 3 || stateStore.messages[2].Content != "  exact replacement  " {
		t.Fatalf("messages = %#v", stateStore.messages)
	}
	var metadata map[string]any
	if err := json.Unmarshal(stateStore.messages[2].Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["source"] != "github/webhook" || metadata["maintainer_run"] != "repo-maintainer" || metadata["maintainer_edited"] != true || metadata["edited_from"] != float64(2) {
		t.Fatalf("metadata = %#v", metadata)
	}
	var out editRunMessageOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil || out.OldMessageID != 2 || out.NewMessageID != 3 {
		t.Fatalf("result = %#v, err = %v", out, err)
	}

	stateStore.appendErr = errors.New("database unavailable")
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"run_name":"target","message_id":2,"content":"replacement"}`), "")
	if err != nil || !result.IsError || !strings.Contains(result.Content, "old message was already cancelled") {
		t.Fatalf("append failure = (%#v, %v)", result, err)
	}
}

func TestGetRunTranscriptPendingAndActivityIsolation(t *testing.T) {
	t.Parallel()
	maintainer, target := maintainerRun(), fleetRun("target", platformv1alpha1.AgentRunPhaseRunning)
	base, stateStore := newMaintainerMessagesToolBase(t, maintainer, target)
	stateStore.messages = []store.Message{
		{ID: 1, Role: "user", Content: "kickoff", DeliveryState: "pending"},
		{ID: 2, Role: "user", Content: "queued", DeliveryState: "pending"},
		{ID: 3, Role: "user", Content: "delivered", Metadata: json.RawMessage(`{"delivered_at_unix":1}`), DeliveryState: "completed"},
		{ID: 4, Role: "user", Content: "cancelled", Metadata: json.RawMessage(`{"cancelled_at_unix":1}`), DeliveryState: "cancelled"},
	}
	stateStore.activity = []store.ActivityEvent{{ID: 10, EventType: "tool_use", Summary: "Ran bash", Detail: json.RawMessage(`{"tool":"bash"}`)}}
	tool := &getRunTranscriptTool{maintainerToolBase: base}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"run_name":"target"}`), "")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%#v, %v)", result, err)
	}
	var out getRunTranscriptOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatal(err)
	}
	if stateStore.activityReadCalls != 0 || stateStore.getMessagesCalls != 1 || stateStore.getMessagesIncludingCancelledReadCalls != 0 || len(out.Activity) != 0 || len(out.Messages) != 3 || out.Messages[0].Pending == nil || *out.Messages[0].Pending || out.Messages[1].Pending == nil || !*out.Messages[1].Pending || out.Messages[2].Pending == nil || *out.Messages[2].Pending {
		t.Fatalf("transcript = %#v, activity reads=%d", out, stateStore.activityReadCalls)
	}
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"run_name":"target","include_activity":true}`), "")
	if err != nil || result.IsError {
		t.Fatalf("activity Execute() = (%#v, %v)", result, err)
	}
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatal(err)
	}
	if stateStore.activityReadCalls != 1 || len(out.Activity) != 1 || out.Activity[0].ID != 10 {
		t.Fatalf("activity transcript = %#v, reads=%d", out, stateStore.activityReadCalls)
	}
}

func TestMaintainerMessageToolsAuthorizeReviewerAndRejectStanding(t *testing.T) {
	t.Parallel()
	maintainer := maintainerRun()
	reviewer := fleetRun("reviewer", platformv1alpha1.AgentRunPhaseRunning)
	reviewer.Labels = map[string]string{triggersv1alpha1.PRLoopRoleLabelKey: triggersv1alpha1.PRLoopRoleReviewerValue}
	base, stateStore := newMaintainerMessagesToolBase(t, maintainer, reviewer)
	stateStore.messages = []store.Message{{ID: 1, Role: "user", Content: "kickoff"}}
	result, err := (&getRunMessagesTool{maintainerToolBase: base}).Execute(context.Background(), json.RawMessage(`{"run_name":"reviewer"}`), "")
	if err != nil || result.IsError {
		t.Fatalf("reviewer Execute() = (%#v, %v)", result, err)
	}

	standing := fleetRun("standing", platformv1alpha1.AgentRunPhaseRunning)
	standing.Labels = map[string]string{orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleMaintainer}
	base, _ = newMaintainerMessagesToolBase(t, maintainer, standing)
	result, err = (&getRunMessagesTool{maintainerToolBase: base}).Execute(context.Background(), json.RawMessage(`{"run_name":"standing"}`), "")
	if err != nil || !result.IsError {
		t.Fatalf("standing Execute() = (%#v, %v)", result, err)
	}
}
