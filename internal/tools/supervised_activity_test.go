package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type supervisedActivityTestStore struct {
	store.StateStore
	session  *store.Session
	messages []store.Message
	events   []store.ActivityEvent
}

func (s *supervisedActivityTestStore) GetSessionByRun(_ context.Context, name, namespace string) (*store.Session, error) {
	return s.session, nil
}

func (s *supervisedActivityTestStore) GetMessagesSince(_ context.Context, _ uuid.UUID, afterID int64) ([]store.Message, error) {
	var out []store.Message
	for _, message := range s.messages {
		if message.ID > afterID {
			out = append(out, message)
		}
	}
	return out, nil
}

func (s *supervisedActivityTestStore) GetActivityEventsSince(_ context.Context, _ uuid.UUID, afterID int64) ([]store.ActivityEvent, error) {
	var out []store.ActivityEvent
	for _, event := range s.events {
		if event.ID > afterID {
			out = append(out, event)
		}
	}
	return out, nil
}

func newSupervisedActivityTool(t *testing.T) (*supervisedActivityTool, *platformv1alpha1.AgentRun, *supervisedActivityTestStore) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	primary := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "primary", Namespace: "default", UID: types.UID("primary-uid")},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseQuestion, ModeName: "review", ModeRevision: 7,
		},
	}
	controller := true
	overseer := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name: "primary-overseer", Namespace: "default", UID: types.UID("overseer-uid"),
		Labels: map[string]string{
			orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleOverseer,
			orchestration.SupervisedRunLabel:   primary.Name,
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: primary.Name,
			UID: primary.UID, Controller: &controller,
		}},
	}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(primary, overseer).Build()
	sessionID := uuid.New()
	now := time.Now().UTC()
	stateStore := &supervisedActivityTestStore{
		session: &store.Session{
			ID: sessionID, AgentRunName: primary.Name, AgentRunNS: primary.Namespace,
			PendingRequestID: "request-1", PendingInputType: "choice", PendingQuestion: "Choose the next mode",
			PendingActions: json.RawMessage(`[{"id":"continue-review","label":"Continue review","mode":"review"},{"id":"switch-auto","label":"Switch to auto","mode":"auto"}]`),
		},
		messages: []store.Message{
			{ID: 1, SessionID: sessionID, Role: "user", Content: "task", CreatedAt: now},
			{ID: 2, SessionID: sessionID, Role: "assistant", Content: "working", CreatedAt: now},
			{ID: 3, SessionID: sessionID, Role: "assistant", Content: "verified", CreatedAt: now},
		},
		events: []store.ActivityEvent{
			{ID: 10, SessionID: sessionID, EventType: "tool_start", Summary: "go test", CreatedAt: now},
			{ID: 11, SessionID: sessionID, EventType: "tool_end", Summary: "passed", CreatedAt: now},
			{ID: 12, SessionID: sessionID, EventType: "assistant_text", Summary: "done", CreatedAt: now},
		},
	}
	tool := &supervisedActivityTool{
		stateStore: stateStore, k8sClient: k8sClient,
		currentRunName: overseer.Name, currentRunNamespace: overseer.Namespace,
		supervisedRunName: primary.Name, supervisedRunNamespace: primary.Namespace,
	}
	return tool, overseer, stateStore
}

func TestSupervisedActivityToolPagesBothStreams(t *testing.T) {
	t.Parallel()
	tool, _, _ := newSupervisedActivityTool(t)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"limit":2}`), "")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%#v, %v)", result, err)
	}
	var output supervisedActivityOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Messages) != 2 || len(output.Activity) != 2 || output.NextMessageCursor != 2 || output.NextActivityCursor != 11 || !output.HasMore {
		t.Fatalf("unexpected first page: %#v", output)
	}
	request := output.State.UserInputRequest
	if output.State.Phase != platformv1alpha1.AgentRunPhaseQuestion || output.State.Mode != "review" || output.State.ModeRevision != 7 || request == nil {
		t.Fatalf("unexpected supervised state: %#v", output.State)
	}
	expected := orchestration.PendingUserInputForSession(tool.stateStore.(*supervisedActivityTestStore).session)
	if request.ID != expected.ID || request.Type != "choice" || request.Message != "Choose the next mode" || len(request.Actions) != 2 ||
		request.Actions[0].ID != "continue-review" || request.Actions[0].Mode != "review" ||
		request.Actions[1].ID != "switch-auto" || request.Actions[1].Mode != "auto" {
		t.Fatalf("unexpected pending input request: %#v", request)
	}

	result, err = tool.Execute(context.Background(), json.RawMessage(`{"message_cursor":2,"activity_cursor":11,"limit":2}`), "")
	if err != nil || result.IsError {
		t.Fatalf("second Execute() = (%#v, %v)", result, err)
	}
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Messages) != 1 || output.Messages[0].ID != 3 || len(output.Activity) != 1 || output.Activity[0].ID != 12 || output.HasMore {
		t.Fatalf("unexpected second page: %#v", output)
	}
	if output.State.UserInputRequest == nil || output.State.UserInputRequest.ID != expected.ID {
		t.Fatalf("pending input was not refreshed independently of cursors: %#v", output.State)
	}
}

func TestSupervisedActivityToolOmitsEmptyPendingInput(t *testing.T) {
	t.Parallel()
	tool, _, stateStore := newSupervisedActivityTool(t)
	stateStore.session.PendingInputType = ""

	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`), "")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%#v, %v)", result, err)
	}
	var output struct {
		State map[string]json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatal(err)
	}
	if _, ok := output.State["user_input_request"]; ok {
		t.Fatalf("empty pending input emitted a request: %s", result.Content)
	}
}

func TestSupervisedActivityToolRejectsSpoofedOwner(t *testing.T) {
	t.Parallel()
	tool, overseer, _ := newSupervisedActivityTool(t)
	overseer.OwnerReferences[0].UID = types.UID("wrong-uid")
	if err := tool.k8sClient.Update(context.Background(), overseer); err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`), "")
	if err != nil {
		t.Fatalf("Execute() transport error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("spoofed owner was authorized: %#v", result)
	}
}

func TestSupervisedActivityToolRejectsInvalidCursor(t *testing.T) {
	t.Parallel()
	tool, _, _ := newSupervisedActivityTool(t)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"message_cursor":-1}`), "")
	if err != nil || !result.IsError {
		t.Fatalf("Execute() = (%#v, %v), want tool error", result, err)
	}
}
