package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// staleSnapshotStore serves a frozen copy of one session from GetSessionByRun
// while every write goes to the live mock. It models a second collaborator
// whose tab still shows a prompt that another click already consumed.
type staleSnapshotStore struct {
	*mockStateStore
	snapshot *store.Session
}

func (s *staleSnapshotStore) GetSessionByRun(ctx context.Context, name, ns string) (*store.Session, error) {
	if s.snapshot != nil {
		copied := *s.snapshot
		return &copied, nil
	}
	return s.mockStateStore.GetSessionByRun(ctx, name, ns)
}

func newModeActionTestServer(t *testing.T, runName string, pendingActions string) (*Server, *mockStateStore, *store.Session, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: runName, Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:    platformv1alpha1.AgentRunPhaseQuestion,
			ModeName: "plan",
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{
				Name: "plan", Version: "v1",
				Category:       platformv1alpha1.ModeCategoryDirect,
				PermissionMode: platformv1alpha1.PermissionModeReadOnly,
			},
		},
	}
	autopilot := &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "autopilot"},
		Spec:       platformv1alpha1.ModeTemplateSpec{Name: "autopilot", Version: "v1", Category: platformv1alpha1.ModeCategoryOrchestrated, Autonomous: true},
	}
	chat := &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "chat"},
		Spec:       platformv1alpha1.ModeTemplateSpec{Name: "chat", Version: "v1", Category: platformv1alpha1.ModeCategoryDirect},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, autopilot, chat).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), runName, "default", "question", "awaiting-user")
	sess.PendingRequestID = "req-1"
	sess.PendingInputType = "action"
	sess.PendingActions = json.RawMessage(pendingActions)
	return &Server{k8sClient: c, scheme: scheme, stateStore: ms}, ms, sess, c
}

func TestSendAgentRunMessageConcurrentModeClicksSwitchOnce(t *testing.T) {
	srv, ms, sess, c := newModeActionTestServer(t, "run-race",
		`[{"id":"go_auto","label":"Autopilot","mode":"autopilot"},{"id":"go_chat","label":"Chat","mode":"chat"}]`)

	// Both collaborators loaded the same prompt.
	stale := *sess
	stale.PendingActions = append(json.RawMessage(nil), sess.PendingActions...)

	if _, err := srv.SendAgentRunMessage(actorContext("admin-1", "admin", "", ""), &platform.SendAgentRunMessageRequest{
		Namespace: "default", Name: "run-race", Message: "__action:go_auto",
	}); err != nil {
		t.Fatalf("first click error = %v", err)
	}
	if sess.PendingRequestID != "" {
		t.Fatalf("PendingRequestID = %q after winning click, want consumed", sess.PendingRequestID)
	}

	// The loser still sees the prompt; its click must lose on the nonce
	// before the mode switch runs.
	srv.stateStore = &staleSnapshotStore{mockStateStore: ms, snapshot: &stale}
	_, err := srv.SendAgentRunMessage(actorContext("admin-2", "admin", "", ""), &platform.SendAgentRunMessageRequest{
		Namespace: "default", Name: "run-race", Message: "__action:go_chat",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("second click error = %v, want FailedPrecondition", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-race", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(run): %v", err)
	}
	if updated.Status.ModeName != "autopilot" {
		t.Fatalf("ModeName = %q, want autopilot (loser must not switch the mode)", updated.Status.ModeName)
	}
	switches := 0
	users := 0
	for _, m := range ms.messagesFor(sess.ID) {
		if m.Role == "system" && strings.Contains(m.Content, "Switched to") {
			switches++
		}
		if m.Role == "user" {
			users++
			if !strings.Contains(string(m.Metadata), `"pending_request_id":"req-1"`) {
				t.Fatalf("continuation metadata = %s, want pending_request_id", m.Metadata)
			}
		}
	}
	if switches != 1 || users != 1 {
		t.Fatalf("switch notices = %d, user messages = %d, want exactly one of each", switches, users)
	}
}

func TestSendAgentRunMessageModeActionDeniedAfterConsumingRequestExplains(t *testing.T) {
	srv, ms, sess, c := newModeActionTestServer(t, "run-denied",
		`[{"id":"go_missing","label":"Missing","mode":"does-not-exist"}]`)

	_, err := srv.SendAgentRunMessage(actorContext("admin-1", "admin", "", ""), &platform.SendAgentRunMessageRequest{
		Namespace: "default", Name: "run-denied", Message: "__action:go_missing",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("error = %v, want FailedPrecondition", err)
	}
	if sess.PendingRequestID != "" {
		t.Fatalf("PendingRequestID = %q, want consumed before the switch was attempted", sess.PendingRequestID)
	}
	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 || msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "denied") || !strings.Contains(msgs[0].Content, "consumed") {
		t.Fatalf("messages = %#v, want one system note explaining the denied switch and consumed prompt", msgs)
	}
	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-denied", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(run): %v", err)
	}
	if updated.Status.ModeName != "plan" {
		t.Fatalf("ModeName = %q, want plan unchanged", updated.Status.ModeName)
	}
}

func newChatTestServer(t *testing.T, runName string) (*Server, *mockStateStore, *store.Session) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: runName, Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), runName, "default", "running", "chatting")
	return &Server{k8sClient: c, scheme: scheme, stateStore: ms}, ms, sess
}

func TestSendAgentRunMessageRecordsClientMessageIDAndReturnsMessageID(t *testing.T) {
	srv, ms, sess := newChatTestServer(t, "run-cmid")

	resp, err := srv.SendAgentRunMessage(context.Background(), &platform.SendAgentRunMessageRequest{
		Namespace: "default", Name: "run-cmid", Message: "hello", ClientMessageId: "  tab-1:0001 ",
	})
	if err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}
	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 || resp.MessageId != msgs[0].ID || resp.MessageId == 0 || resp.Deduplicated {
		t.Fatalf("resp = %+v, messages = %#v, want message_id of the stored message", resp, msgs)
	}
	var meta map[string]any
	if err := json.Unmarshal(msgs[0].Metadata, &meta); err != nil {
		t.Fatalf("metadata %s: %v", msgs[0].Metadata, err)
	}
	if meta["client_message_id"] != "tab-1:0001" || meta["mode"] == nil {
		t.Fatalf("metadata = %s, want trimmed client_message_id alongside mode", msgs[0].Metadata)
	}

	// Pending-answer path keeps the key too.
	sess.PendingRequestID = "req-9"
	sess.PendingInputType = "question"
	resp, err = srv.SendAgentRunMessage(context.Background(), &platform.SendAgentRunMessageRequest{
		Namespace: "default", Name: "run-cmid", Message: "answer", ClientMessageId: "tab-1:0002",
	})
	if err != nil {
		t.Fatalf("SendAgentRunMessage(answer) error = %v", err)
	}
	msgs = ms.messagesFor(sess.ID)
	if len(msgs) != 2 || resp.MessageId != msgs[1].ID {
		t.Fatalf("resp = %+v, messages = %#v, want message_id of the answer", resp, msgs)
	}
	if !strings.Contains(string(msgs[1].Metadata), `"client_message_id":"tab-1:0002"`) || !strings.Contains(string(msgs[1].Metadata), `"pending_request_id":"req-9"`) {
		t.Fatalf("answer metadata = %s, want client_message_id and pending_request_id", msgs[1].Metadata)
	}
}

func TestValidateClientMessageID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", true},
		{"  ", "", true},
		{"abc-123", "abc-123", true},
		{"with space", "with space", true},
		{strings.Repeat("x", 128), strings.Repeat("x", 128), true},
		{strings.Repeat("x", 129), "", false},
		{"bad\x00id", "", false},
		{"tab\tid", "", false},
		{"\xff\xfe", "", false},
	} {
		got, err := validateClientMessageID(tc.in)
		if tc.ok != (err == nil) || got != tc.want {
			t.Errorf("validateClientMessageID(%q) = (%q, %v), want (%q, ok=%t)", tc.in, got, err, tc.want, tc.ok)
		}
		if err != nil && connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("validateClientMessageID(%q) code = %v, want InvalidArgument", tc.in, connect.CodeOf(err))
		}
	}
	if _, found, err := (&Server{stateStore: newMockStateStore()}).findMessageByClientID(context.Background(), uuid.New(), "k"); err != nil || found {
		t.Fatalf("findMessageByClientID without a pool = (found=%t, %v), want not found without error", found, err)
	}
}

func TestSendAgentRunMessageDistinguishesMissingSessionFromStoreOutage(t *testing.T) {
	srv, ms, _ := newChatTestServer(t, "run-outage")
	ms.getSessionByRunErr = errors.New("dial tcp: connection refused")
	_, err := srv.SendAgentRunMessage(context.Background(), &platform.SendAgentRunMessageRequest{
		Namespace: "default", Name: "run-outage", Message: "hello",
	})
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("outage error = %v, want Unavailable", err)
	}
	ms.getSessionByRunErr = store.ErrSessionNotFound
	_, err = srv.SendAgentRunMessage(context.Background(), &platform.SendAgentRunMessageRequest{
		Namespace: "default", Name: "run-outage", Message: "hello",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "starting up") {
		t.Fatalf("missing-session error = %v, want FailedPrecondition 'still starting up'", err)
	}
}

// messageGetterStore adds the optional single-message lookup and counts full
// transcript loads so the cancel path can be shown to avoid them.
type messageGetterStore struct {
	*mockStateStore
	fullLoads atomic.Int32
}

func (s *messageGetterStore) GetMessage(_ context.Context, sessionID uuid.UUID, messageID int64) (*store.Message, error) {
	for _, m := range s.messagesFor(sessionID) {
		if m.ID == messageID {
			copied := m
			return &copied, nil
		}
	}
	return nil, store.ErrMessageNotFound
}

func (s *messageGetterStore) GetMessagesIncludingCancelled(ctx context.Context, sessionID uuid.UUID) ([]store.Message, error) {
	s.fullLoads.Add(1)
	return s.mockStateStore.GetMessagesIncludingCancelled(ctx, sessionID)
}

func TestCancelAgentRunMessageUsesMessageGetter(t *testing.T) {
	srv, ms, sess := newChatTestServer(t, "run-cancel-getter")
	gs := &messageGetterStore{mockStateStore: ms}
	srv.stateStore = gs
	pending, _ := ms.AppendMessage(context.Background(), sess.ID, "user", "queued", json.RawMessage(`{"mode":"enqueue"}`))

	if _, err := srv.CancelAgentRunMessage(context.Background(), &platform.CancelAgentRunMessageRequest{
		Namespace: "default", Name: "run-cancel-getter", MessageId: pending.ID,
	}); err != nil {
		t.Fatalf("CancelAgentRunMessage() error = %v", err)
	}
	if !strings.Contains(string(ms.messagesFor(sess.ID)[0].Metadata), "cancelled_at_unix") {
		t.Fatalf("metadata = %s, want cancelled_at_unix stamp", ms.messagesFor(sess.ID)[0].Metadata)
	}
	if gs.fullLoads.Load() != 0 {
		t.Fatalf("GetMessagesIncludingCancelled calls = %d, want 0 with MessageGetter available", gs.fullLoads.Load())
	}
	_, err := srv.CancelAgentRunMessage(context.Background(), &platform.CancelAgentRunMessageRequest{
		Namespace: "default", Name: "run-cancel-getter", MessageId: 9999,
	})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown message error = %v, want NotFound", err)
	}
}
