package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcppolicy"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// mockStateStore is a minimal in-memory StateStore for tests.
type mockStateStore struct {
	mu                         sync.Mutex
	sessions                   map[uuid.UUID]*store.Session
	messages                   map[uuid.UUID][]store.Message
	owners                     map[string]*store.ResourceOwnership
	msgSeq                     int64
	createSessionErr           error
	appendMessageErr           error
	getSessionByRunErr         error
	setResourceOwnerErr        error
	getMessagesBySession       map[uuid.UUID][]store.Message
	getMessagesErr             error
	getRecentActivityBySession map[uuid.UUID][]store.ActivityEvent
	getRecentActivityErr       error
	allActivityBySession       map[uuid.UUID][]store.ActivityEvent
	getArtifact                *store.Artifact
	getArtifactErr             error
	mergedMetadata             map[uuid.UUID]map[string]json.RawMessage
	deletedAgentRunData        []agentRunDataDeleteCall
	observabilityQuery         store.ObservabilityQuery
	observabilityResult        *store.ObservabilityOverview
	observabilityErr           error
}

type agentRunDataDeleteCall struct {
	name      string
	namespace string
	projectID string
}

func newMockStateStore() *mockStateStore {
	return &mockStateStore{
		sessions: make(map[uuid.UUID]*store.Session),
		messages: make(map[uuid.UUID][]store.Message),
		owners:   make(map[string]*store.ResourceOwnership),
	}
}

func (m *mockStateStore) CreateSession(_ context.Context, name, ns, phase, currentStep string) (*store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createSessionErr != nil {
		return nil, m.createSessionErr
	}
	id := uuid.New()
	s := &store.Session{ID: id, AgentRunName: name, AgentRunNS: ns, Phase: phase, CurrentStep: currentStep, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	m.sessions[id] = s
	return s, nil
}

func (m *mockStateStore) GetSession(_ context.Context, id uuid.UUID) (*store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("session not found")
}

func (m *mockStateStore) GetSessionByRun(_ context.Context, name, ns string) (*store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getSessionByRunErr != nil {
		return nil, m.getSessionByRunErr
	}
	for _, s := range m.sessions {
		if s.AgentRunName == name && s.AgentRunNS == ns {
			return s, nil
		}
	}
	return nil, fmt.Errorf("session not found for %s/%s", ns, name)
}

func (m *mockStateStore) ListSessionsByNamespace(_ context.Context, namespace string) ([]store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Session
	for _, s := range m.sessions {
		if namespace == "" || s.AgentRunNS == namespace {
			out = append(out, *s)
		}
	}
	return out, nil
}

func (m *mockStateStore) AppendMessage(_ context.Context, sessionID uuid.UUID, role, content string, metadata json.RawMessage) (*store.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.appendMessageErr != nil {
		return nil, m.appendMessageErr
	}
	m.msgSeq++
	msg := store.Message{ID: m.msgSeq, SessionID: sessionID, Role: role, Content: content, Metadata: metadata, CreatedAt: time.Now()}
	m.messages[sessionID] = append(m.messages[sessionID], msg)
	return &msg, nil
}

func (m *mockStateStore) messagesFor(sessionID uuid.UUID) []store.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.messages[sessionID]
}

// Stubs for unused methods.
func (m *mockStateStore) UpdatePhase(context.Context, uuid.UUID, string, string) error {
	return nil
}
func (m *mockStateStore) SetPendingQuestion(_ context.Context, id uuid.UUID, phase, question, inputType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Phase = phase
		s.PendingQuestion = question
		s.PendingInputType = inputType
		s.PendingActions = json.RawMessage(`[]`)
		return nil
	}
	return fmt.Errorf("session not found")
}
func (m *mockStateStore) ClearPendingQuestion(_ context.Context, id uuid.UUID, phase string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Phase = phase
		s.PendingQuestion = ""
		s.PendingInputType = ""
		s.PendingActions = json.RawMessage(`[]`)
		return nil
	}
	return fmt.Errorf("session not found")
}
func (m *mockStateStore) SetPendingAction(_ context.Context, id uuid.UUID, phase, question string, actions json.RawMessage, inputType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Phase = phase
		s.PendingQuestion = question
		s.PendingInputType = inputType
		s.PendingActions = append(json.RawMessage(nil), actions...)
		return nil
	}
	return fmt.Errorf("session not found")
}
func (m *mockStateStore) ClearPendingAction(_ context.Context, id uuid.UUID, phase string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Phase = phase
		s.PendingQuestion = ""
		s.PendingInputType = ""
		s.PendingActions = json.RawMessage(`[]`)
		return nil
	}
	return fmt.Errorf("session not found")
}
func (m *mockStateStore) ClearPendingInputIfID(_ context.Context, id uuid.UUID, requestID, phase string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok && s.PendingRequestID == requestID {
		s.Phase = phase
		s.PendingQuestion = ""
		s.PendingInputType = ""
		s.PendingRequestID = ""
		s.PendingActions = json.RawMessage(`[]`)
		return true, nil
	}
	return false, nil
}
func (m *mockStateStore) UpdateMetadata(context.Context, uuid.UUID, json.RawMessage) error {
	return nil
}
func (m *mockStateStore) MergeSessionMetadata(_ context.Context, id uuid.UUID, key string, value json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mergedMetadata == nil {
		m.mergedMetadata = map[uuid.UUID]map[string]json.RawMessage{}
	}
	if m.mergedMetadata[id] == nil {
		m.mergedMetadata[id] = map[string]json.RawMessage{}
	}
	m.mergedMetadata[id][key] = append(json.RawMessage(nil), value...)
	return nil
}
func (m *mockStateStore) ListAllSessionMetrics(context.Context) ([]store.SessionMetricsEntry, error) {
	return nil, nil
}
func (m *mockStateStore) GetObservabilityOverview(_ context.Context, query store.ObservabilityQuery) (*store.ObservabilityOverview, error) {
	m.observabilityQuery = query
	if m.observabilityResult == nil {
		m.observabilityResult = &store.ObservabilityOverview{}
	}
	return m.observabilityResult, m.observabilityErr
}
func (m *mockStateStore) DeleteAgentRunData(_ context.Context, name, namespace, projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletedAgentRunData = append(m.deletedAgentRunData, agentRunDataDeleteCall{name: name, namespace: namespace, projectID: projectID})
	for id, s := range m.sessions {
		if s.AgentRunName == name && s.AgentRunNS == namespace {
			delete(m.sessions, id)
			delete(m.messages, id)
		}
	}
	return nil
}
func (m *mockStateStore) GetMessages(_ context.Context, sessionID uuid.UUID) ([]store.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getMessagesErr != nil {
		return nil, m.getMessagesErr
	}
	if m.getMessagesBySession != nil {
		return append([]store.Message(nil), m.getMessagesBySession[sessionID]...), nil
	}
	return append([]store.Message(nil), m.messages[sessionID]...), nil
}
func (m *mockStateStore) GetMessagesIncludingCancelled(ctx context.Context, sessionID uuid.UUID) ([]store.Message, error) {
	return m.GetMessages(ctx, sessionID)
}
func (m *mockStateStore) GetMessagesSince(context.Context, uuid.UUID, int64) ([]store.Message, error) {
	return nil, nil
}
func (m *mockStateStore) PollNewUserMessages(context.Context, uuid.UUID, int64) ([]store.Message, error) {
	return nil, nil
}
func (m *mockStateStore) MarkMessagesDelivered(context.Context, uuid.UUID, []int64) error {
	return nil
}

func (m *mockStateStore) CancelUndeliveredUserMessage(_ context.Context, sessionID uuid.UUID, messageID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.messages[sessionID]
	for i, msg := range msgs {
		if msg.ID != messageID || msg.Role != "user" {
			continue
		}
		var meta map[string]any
		if len(msg.Metadata) > 0 {
			_ = json.Unmarshal(msg.Metadata, &meta)
		}
		if meta == nil {
			meta = map[string]any{}
		}
		if cancelled, _ := meta["cancelled_at_unix"].(float64); cancelled > 0 {
			return store.ErrMessageNotFound
		}
		if delivered, _ := meta["delivered_at_unix"].(float64); delivered > 0 {
			return store.ErrMessageDelivered
		}
		meta["cancelled_at_unix"] = time.Now().Unix()
		encoded, _ := json.Marshal(meta)
		msgs[i].Metadata = encoded
		return nil
	}
	return store.ErrMessageNotFound
}
func (m *mockStateStore) UpsertSessionTranscript(context.Context, uuid.UUID, []byte, int32) error {
	return nil
}
func (m *mockStateStore) GetSessionTranscript(context.Context, uuid.UUID) ([]byte, error) {
	return nil, nil
}
func (m *mockStateStore) DeleteSessionTranscript(context.Context, uuid.UUID) error {
	return nil
}
func (m *mockStateStore) WriteActivityEvent(context.Context, uuid.UUID, string, string, json.RawMessage) (*store.ActivityEvent, error) {
	return nil, nil
}
func (m *mockStateStore) GetRecentActivity(_ context.Context, sessionID uuid.UUID, _ int32) ([]store.ActivityEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getRecentActivityErr != nil {
		return nil, m.getRecentActivityErr
	}
	if m.getRecentActivityBySession == nil {
		return nil, nil
	}
	return append([]store.ActivityEvent(nil), m.getRecentActivityBySession[sessionID]...), nil
}
func (m *mockStateStore) GetAllActivity(_ context.Context, sessionID uuid.UUID) ([]store.ActivityEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.allActivityBySession == nil {
		return nil, nil
	}
	return append([]store.ActivityEvent(nil), m.allActivityBySession[sessionID]...), nil
}
func (m *mockStateStore) GetActivityEventsSince(_ context.Context, sessionID uuid.UUID, afterID int64) ([]store.ActivityEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.ActivityEvent
	for _, ev := range m.allActivityBySession[sessionID] {
		if ev.ID > afterID {
			out = append(out, ev)
		}
	}
	return out, nil
}
func (m *mockStateStore) GetSessionFingerprint(_ context.Context, sessionID uuid.UUID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionID]
	if !ok {
		return "", fmt.Errorf("session %s not found", sessionID)
	}
	var maxMsgID, maxEventID int64
	for _, msg := range m.messages[sessionID] {
		if msg.ID > maxMsgID {
			maxMsgID = msg.ID
		}
	}
	for _, msg := range m.getMessagesBySession[sessionID] {
		if msg.ID > maxMsgID {
			maxMsgID = msg.ID
		}
	}
	for _, ev := range m.allActivityBySession[sessionID] {
		if ev.ID > maxEventID {
			maxEventID = ev.ID
		}
	}
	for _, ev := range m.getRecentActivityBySession[sessionID] {
		if ev.ID > maxEventID {
			maxEventID = ev.ID
		}
	}
	return fmt.Sprintf("%d|%d|%s|%d", maxMsgID, maxEventID, sess.PendingInputType, len(sess.PendingActions)), nil
}
func (m *mockStateStore) UpsertArtifact(context.Context, uuid.UUID, string, string, string, string, json.RawMessage) (*store.Artifact, error) {
	return nil, nil
}
func (m *mockStateStore) GetArtifact(context.Context, uuid.UUID, string) (*store.Artifact, error) {
	if m.getArtifactErr != nil {
		return nil, m.getArtifactErr
	}
	return m.getArtifact, nil
}
func (m *mockStateStore) GetArtifacts(context.Context, uuid.UUID) ([]store.Artifact, error) {
	return nil, nil
}
func (m *mockStateStore) Close() error { return nil }

// Collaboration stubs
func (m *mockStateStore) SetResourceOwner(_ context.Context, resourceType, resourceID, resourceNamespace, ownerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.setResourceOwnerErr != nil {
		return m.setResourceOwnerErr
	}
	if m.sessions == nil {
		m.sessions = make(map[uuid.UUID]*store.Session)
	}
	if m.messages == nil {
		m.messages = make(map[uuid.UUID][]store.Message)
	}
	if resourceType == "" || resourceID == "" {
		return nil
	}
	if m.owners == nil {
		m.owners = make(map[string]*store.ResourceOwnership)
	}
	m.owners[resourceType+"/"+resourceNamespace+"/"+resourceID] = &store.ResourceOwnership{
		ResourceType:      resourceType,
		ResourceID:        resourceID,
		ResourceNamespace: resourceNamespace,
		OwnerID:           ownerID,
	}
	return nil
}
func (m *mockStateStore) GetResourceOwner(_ context.Context, resourceType, resourceID, resourceNamespace string) (*store.ResourceOwnership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.owners == nil {
		return nil, nil
	}
	return m.owners[resourceType+"/"+resourceNamespace+"/"+resourceID], nil
}
func (m *mockStateStore) ListOwnedResources(context.Context, string, string) ([]store.ResourceOwnership, error) {
	return nil, nil
}
func (m *mockStateStore) ShareResource(context.Context, *store.ResourceShare) (*store.ResourceShare, error) {
	return nil, nil
}
func (m *mockStateStore) RevokeShare(context.Context, string) error                   { return nil }
func (m *mockStateStore) UpdateSharePermission(context.Context, string, string) error { return nil }
func (m *mockStateStore) ListSharesForResource(context.Context, string, string, string) ([]store.ResourceShare, error) {
	return nil, nil
}
func (m *mockStateStore) ListSharedWithMe(context.Context, string, string) ([]store.ResourceShare, error) {
	return nil, nil
}
func (m *mockStateStore) GetSharePermission(context.Context, string, string, string, string) (*store.ResourceShare, error) {
	return nil, nil
}
func (m *mockStateStore) CreateNotification(context.Context, *store.Notification) error { return nil }
func (m *mockStateStore) HasUnreadNotification(context.Context, string, string, string, string) (bool, error) {
	return false, nil
}
func (m *mockStateStore) ListNotifications(context.Context, string, bool, int32) ([]store.Notification, error) {
	return nil, nil
}
func (m *mockStateStore) MarkNotificationRead(context.Context, string) error     { return nil }
func (m *mockStateStore) MarkAllNotificationsRead(context.Context, string) error { return nil }
func (m *mockStateStore) GetUnreadNotificationCount(context.Context, string) (int32, error) {
	return 0, nil
}

func actorContext(subject, role, _, _ string) context.Context {
	// Inject the actor directly: production requests get it from verified JWT
	// claims via the RPC interceptor, and the header fallback is disabled
	// unless DASHBOARD_INSECURE_HEADER_AUTH is set.
	return context.WithValue(context.Background(), requestActorContextKey{}, requestActor{
		Subject: subject,
		Role:    role,
	})
}

func sessionCommandActorContext(role, org, workspace string) context.Context {
	return actorContext("", role, org, workspace)
}

func TestDeleteAgentRunAllowsOwnerToDeleteActiveRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-active", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-active", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	ctx := actorContext("user-1", "", "", "")
	if _, err := srv.DeleteAgentRun(ctx, &platform.DeleteAgentRunRequest{
		Namespace: "default",
		Name:      "run-active",
	}); err != nil {
		t.Fatalf("DeleteAgentRun() error = %v", err)
	}

	got := &platformv1alpha1.AgentRun{}
	err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-active"}, got)
	if err == nil {
		t.Fatal("expected run to be deleted")
	}
}

func TestDeleteAgentRunAllowsAdminToDeleteActiveRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-admin", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-admin", "default", "owner-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	ctx := actorContext("", "admin", "", "")
	if _, err := srv.DeleteAgentRun(ctx, &platform.DeleteAgentRunRequest{
		Namespace: "default",
		Name:      "run-admin",
	}); err != nil {
		t.Fatalf("DeleteAgentRun() error = %v", err)
	}
}

// TestDeleteAgentRunFallsBackToTriggerOwner covers runs created by a trigger
// connector (here a SlackAgent) that never recorded their own agent_run
// ownership: the trigger resource's owner must still be able to manage
// (delete) the runs their agent spawned, while strangers stay rejected.
func TestDeleteAgentRunFallsBackToTriggerOwner(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "slack-run", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{Kind: "SlackAgent", Name: "my-agent"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	// Owner recorded for the SlackAgent, but not for the run itself.
	if err := ms.SetResourceOwner(context.Background(), "slackagent", "my-agent", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.DeleteAgentRun(actorContext("someone-else", "", "", ""), &platform.DeleteAgentRunRequest{
		Namespace: "default",
		Name:      "slack-run",
	}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("stranger delete: connect.CodeOf(err) = %v, want PermissionDenied (err=%v)", connect.CodeOf(err), err)
	}

	if _, err := srv.DeleteAgentRun(actorContext("user-1", "", "", ""), &platform.DeleteAgentRunRequest{
		Namespace: "default",
		Name:      "slack-run",
	}); err != nil {
		t.Fatalf("DeleteAgentRun() as SlackAgent owner error = %v", err)
	}

	got := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "slack-run"}, got); err == nil {
		t.Fatal("expected run to be deleted")
	}
}

// TestDeleteAgentRunOwnRecordBeatsTriggerOwner pins the precedence: a run with
// its own ownership record is not manageable by the trigger resource's owner.
func TestDeleteAgentRunOwnRecordBeatsTriggerOwner(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "slack-run-owned", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{Kind: "SlackAgent", Name: "my-agent"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "slack-run-owned", "default", "user-2"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	if err := ms.SetResourceOwner(context.Background(), "slackagent", "my-agent", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.DeleteAgentRun(actorContext("user-1", "", "", ""), &platform.DeleteAgentRunRequest{
		Namespace: "default",
		Name:      "slack-run-owned",
	}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("connect.CodeOf(err) = %v, want PermissionDenied (err=%v)", connect.CodeOf(err), err)
	}

	if _, err := srv.DeleteAgentRun(actorContext("user-2", "", "", ""), &platform.DeleteAgentRunRequest{
		Namespace: "default",
		Name:      "slack-run-owned",
	}); err != nil {
		t.Fatalf("DeleteAgentRun() as run owner error = %v", err)
	}
}

func TestDeleteAgentRunRejectsNonOwner(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-viewer", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-viewer", "default", "owner-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	ctx := actorContext("viewer-1", "", "", "")
	_, err := srv.DeleteAgentRun(ctx, &platform.DeleteAgentRunRequest{
		Namespace: "default",
		Name:      "run-viewer",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("connect.CodeOf(err) = %v, want PermissionDenied (err=%v)", connect.CodeOf(err), err)
	}
}

func TestCancelAgentRunAllowsOwnerToRequestCancellation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-cancel", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-cancel", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.CancelAgentRun(actorContext("user-1", "", "", ""), &platform.CancelAgentRunRequest{
		Namespace: "default",
		Name:      "run-cancel",
	})
	if err != nil {
		t.Fatalf("CancelAgentRun() error = %v", err)
	}
	if resp.Name != "run-cancel" {
		t.Fatalf("response name = %q, want run-cancel", resp.Name)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-cancel"}, updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	ts := updated.Annotations[cancelRequestedAnnotation]
	if ts == "" {
		t.Fatal("cancel annotation is empty")
	}
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Fatalf("cancel annotation = %q, want RFC3339 timestamp: %v", ts, err)
	}
}

func TestCancelAgentRunRejectsNonOwner(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-cancel-viewer", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-cancel-viewer", "default", "owner-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.CancelAgentRun(actorContext("viewer-1", "", "", ""), &platform.CancelAgentRunRequest{
		Namespace: "default",
		Name:      "run-cancel-viewer",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("connect.CodeOf(err) = %v, want PermissionDenied (err=%v)", connect.CodeOf(err), err)
	}
}

func TestCancelAgentRunRejectsTerminalRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-cancel-done", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseSucceeded,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-cancel-done", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.CancelAgentRun(actorContext("user-1", "", "", ""), &platform.CancelAgentRunRequest{
		Namespace: "default",
		Name:      "run-cancel-done",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("connect.CodeOf(err) = %v, want FailedPrecondition (err=%v)", connect.CodeOf(err), err)
	}
}

func TestRetryAgentRunAllowsOwnerToWakeFailedRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-retry", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WakeRequests: 2,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseFailed,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-retry", "default", "failed", "failed")
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-retry", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.RetryAgentRun(actorContext("user-1", "", "", ""), &platform.RetryAgentRunRequest{
		Namespace: "default",
		Name:      "run-retry",
	})
	if err != nil {
		t.Fatalf("RetryAgentRun() error = %v", err)
	}
	if resp.Name != "run-retry" {
		t.Fatalf("response name = %q, want run-retry", resp.Name)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-retry"}, updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Spec.WakeRequests != 3 {
		t.Fatalf("wakeRequests = %d, want 3", updated.Spec.WakeRequests)
	}
	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 {
		t.Fatalf("messages appended = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "Retry requested — continue from where the run failed." {
		t.Fatalf("appended message = (%q, %q), want default retry user message", msgs[0].Role, msgs[0].Content)
	}
}

func TestRetryAgentRunAllowsOwnerToResumeStoppedRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-resume", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WakeRequests: 4,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseCancelled,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-resume", "default", "stopped", "stopped")
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-resume", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.RetryAgentRun(actorContext("user-1", "", "", ""), &platform.RetryAgentRunRequest{
		Namespace: "default",
		Name:      "run-resume",
	}); err != nil {
		t.Fatalf("RetryAgentRun() error = %v", err)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-resume"}, updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Spec.WakeRequests != 5 {
		t.Fatalf("wakeRequests = %d, want 5", updated.Spec.WakeRequests)
	}
	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 {
		t.Fatalf("messages appended = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "Resume requested — continue from where the run stopped." {
		t.Fatalf("appended message = (%q, %q), want default resume user message", msgs[0].Role, msgs[0].Content)
	}
}

func TestRetryAgentRunRejectsRunningRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-retry-running", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-retry-running", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.RetryAgentRun(actorContext("user-1", "", "", ""), &platform.RetryAgentRunRequest{
		Namespace: "default",
		Name:      "run-retry-running",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("connect.CodeOf(err) = %v, want FailedPrecondition (err=%v)", connect.CodeOf(err), err)
	}
}

func TestRetryAgentRunRejectsNonOwner(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-retry-viewer", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseFailed,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-retry-viewer", "default", "owner-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.RetryAgentRun(actorContext("viewer-1", "", "", ""), &platform.RetryAgentRunRequest{
		Namespace: "default",
		Name:      "run-retry-viewer",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("connect.CodeOf(err) = %v, want PermissionDenied (err=%v)", connect.CodeOf(err), err)
	}
}

func TestExtendAgentRunRuntimeExtendsFromElapsedPausedRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	started := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-paused", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Limits: &platformv1alpha1.AgentRunLimits{MaxRuntime: metav1.Duration{Duration: 30 * time.Minute}},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhasePaused,
			StartedAt: &started,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-paused", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.ExtendAgentRunRuntime(actorContext("user-1", "", "", ""), &platform.ExtendAgentRunRuntimeRequest{
		Namespace:         "default",
		Name:              "run-paused",
		AdditionalRuntime: "1h",
	})
	if err != nil {
		t.Fatalf("ExtendAgentRunRuntime() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-paused"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if got := updated.Spec.Limits.MaxRuntime.Duration; got < 3*time.Hour || got > 3*time.Hour+5*time.Second {
		t.Fatalf("MaxRuntime = %s, want about 3h", got)
	}
	if resp.MaxRuntime == "" {
		t.Fatalf("response MaxRuntime is empty")
	}
}

func TestExtendAgentRunRuntimeRejectsTerminalRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-done", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseSucceeded,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.ExtendAgentRunRuntime(context.Background(), &platform.ExtendAgentRunRuntimeRequest{
		Namespace:         "default",
		Name:              "run-done",
		AdditionalRuntime: "1h",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("connect.CodeOf(err) = %v, want FailedPrecondition (err=%v)", connect.CodeOf(err), err)
	}
}

func TestExtendAgentRunRuntimeRejectsNonCollaborator(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-owned", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhasePaused,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-owned", "default", "owner-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.ExtendAgentRunRuntime(actorContext("viewer-1", "", "", ""), &platform.ExtendAgentRunRuntimeRequest{
		Namespace:         "default",
		Name:              "run-owned",
		AdditionalRuntime: "1h",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("connect.CodeOf(err) = %v, want PermissionDenied (err=%v)", connect.CodeOf(err), err)
	}
}

func TestUpdateAgentRunRuntimeConfigSwitchesProviderModelAndReasoning(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-runtime", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Model:    "anthropic/claude-sonnet-4-6",
			AuthMode: platformv1alpha1.AgentRunAuthModeAPIKey,
			Secrets: &platformv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{
				{Provider: "anthropic", SecretName: "anthropic-key"},
				{Provider: "openrouter", SecretName: "openrouter-key"},
			}},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-runtime", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.UpdateAgentRunRuntimeConfig(actorContext("user-1", "", "", ""), &platform.UpdateAgentRunRuntimeConfigRequest{
		Namespace:            "default",
		Name:                 "run-runtime",
		Provider:             "openrouter",
		Model:                "openai/gpt-5",
		UpdateReasoningLevel: true,
		ReasoningLevel:       "high",
	})
	if err != nil {
		t.Fatalf("UpdateAgentRunRuntimeConfig() error = %v", err)
	}
	if resp.Model != "openrouter/openai/gpt-5" || resp.ResolvedReasoningLevel != "high" {
		t.Fatalf("response model/reasoning = %q/%q, want openrouter/openai/gpt-5/high", resp.Model, resp.ResolvedReasoningLevel)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-runtime"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Spec.Model != "openrouter/openai/gpt-5" {
		t.Fatalf("Spec.Model = %q, want openrouter/openai/gpt-5", updated.Spec.Model)
	}
	if updated.Spec.OpenAIBaseURL != triggersv1alpha1.DefaultOpenRouterBaseURL {
		t.Fatalf("Spec.OpenAIBaseURL = %q, want %q", updated.Spec.OpenAIBaseURL, triggersv1alpha1.DefaultOpenRouterBaseURL)
	}
	if updated.Spec.ReasoningLevel != platformv1alpha1.ReasoningHigh {
		t.Fatalf("Spec.ReasoningLevel = %q, want high", updated.Spec.ReasoningLevel)
	}
	if updated.Spec.RestartRequests != 0 {
		t.Fatalf("Spec.RestartRequests = %d, want 0 (mounted-key switches apply live without a compute restart)", updated.Spec.RestartRequests)
	}
	if _, ok := updated.Annotations["platform.gratefulagents.dev/runtime-config-updated-at"]; ok {
		t.Fatalf("runtime-config-updated-at annotation is set; runtime config updates should use spec fields only")
	}
}

func TestUpdateAgentRunRuntimeConfigClearsReasoningOverride(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-reasoning", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Model:          "gpt-5.4",
			ReasoningLevel: platformv1alpha1.ReasoningHigh,
			AuthMode:       platformv1alpha1.AgentRunAuthModeAPIKey,
			Secrets:        &platformv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: "openai", SecretName: "openai-key"}}},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-reasoning", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.UpdateAgentRunRuntimeConfig(actorContext("user-1", "", "", ""), &platform.UpdateAgentRunRuntimeConfigRequest{
		Namespace:            "default",
		Name:                 "run-reasoning",
		UpdateReasoningLevel: true,
	})
	if err != nil {
		t.Fatalf("UpdateAgentRunRuntimeConfig() error = %v", err)
	}
	if resp.ResolvedReasoningLevel != "" {
		t.Fatalf("response ResolvedReasoningLevel = %q, want empty", resp.ResolvedReasoningLevel)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-reasoning"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Spec.ReasoningLevel != "" {
		t.Fatalf("Spec.ReasoningLevel = %q, want empty", updated.Spec.ReasoningLevel)
	}
}

func TestUpdateAgentRunRuntimeConfigRejectsMissingMountedProviderKey(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-missing-key", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Model:    "anthropic/claude-sonnet-4-6",
			AuthMode: platformv1alpha1.AgentRunAuthModeAPIKey,
			Secrets:  &platformv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: "anthropic", SecretName: "anthropic-key"}}},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-missing-key", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.UpdateAgentRunRuntimeConfig(actorContext("user-1", "", "", ""), &platform.UpdateAgentRunRuntimeConfigRequest{
		Namespace: "default",
		Name:      "run-missing-key",
		Provider:  "openrouter",
		Model:     "openai/gpt-5",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("connect.CodeOf(err) = %v, want FailedPrecondition (err=%v)", connect.CodeOf(err), err)
	}
}

func TestUpdateAgentRunRuntimeConfigRejectsTerminalRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-terminal-runtime", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{Model: "gpt-5.4"},
		Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.UpdateAgentRunRuntimeConfig(context.Background(), &platform.UpdateAgentRunRuntimeConfigRequest{
		Namespace:            "default",
		Name:                 "run-terminal-runtime",
		UpdateReasoningLevel: true,
		ReasoningLevel:       "low",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("connect.CodeOf(err) = %v, want FailedPrecondition (err=%v)", connect.CodeOf(err), err)
	}
}

func TestUpdateAgentRunRuntimeConfigStripsRepeatedProviderPrefix(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-mismatch", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Model:    "gpt-5.4",
			AuthMode: platformv1alpha1.AgentRunAuthModeAPIKey,
			Secrets:  &platformv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: "openrouter", SecretName: "openrouter-key"}}},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-mismatch", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.UpdateAgentRunRuntimeConfig(actorContext("user-1", "", "", ""), &platform.UpdateAgentRunRuntimeConfigRequest{
		Namespace: "default",
		Name:      "run-mismatch",
		Provider:  "openrouter",
		Model:     "openrouter/openai/gpt-5.4",
	})
	if err != nil {
		t.Fatalf("UpdateAgentRunRuntimeConfig() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-mismatch"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Spec.Model != "openrouter/openai/gpt-5.4" {
		t.Fatalf("Spec.Model = %q, want openrouter/openai/gpt-5.4", updated.Spec.Model)
	}
}

func TestUpdateAgentRunRuntimeConfigLiveSwitchesToMountedOAuthProvider(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-oauth-live", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Model:    "anthropic/claude-sonnet-4-6",
			AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
			Secrets: &platformv1alpha1.AgentRunSecrets{
				OpenAIOAuthSecret: "usercred-anthropic",
				ProviderOAuthSecrets: []platformv1alpha1.ProviderOAuthSecretRef{
					{Provider: "openai", SecretName: "usercred-openai"},
					{Provider: "anthropic", SecretName: "usercred-anthropic"},
				},
			},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-oauth-live", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.UpdateAgentRunRuntimeConfig(actorContext("user-1", "", "", ""), &platform.UpdateAgentRunRuntimeConfigRequest{
		Namespace: "default",
		Name:      "run-oauth-live",
		Provider:  "openai",
		Model:     "gpt-5",
	}); err != nil {
		t.Fatalf("UpdateAgentRunRuntimeConfig() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-oauth-live"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Spec.RestartRequests != 0 {
		t.Fatalf("Spec.RestartRequests = %d, want 0 (mounted OAuth material switches live)", updated.Spec.RestartRequests)
	}
	if updated.Spec.Model != "gpt-5" {
		t.Fatalf("Spec.Model = %q, want gpt-5", updated.Spec.Model)
	}
	if updated.Spec.AuthMode != platformv1alpha1.AgentRunAuthModeOAuth {
		t.Fatalf("Spec.AuthMode = %q, want oauth", updated.Spec.AuthMode)
	}
	if updated.Spec.Secrets.OpenAIOAuthSecret != "usercred-openai" {
		t.Fatalf("Spec.Secrets.OpenAIOAuthSecret = %q, want repointed to usercred-openai", updated.Spec.Secrets.OpenAIOAuthSecret)
	}
	if updated.Spec.OpenAIBaseURL != triggersv1alpha1.DefaultOpenAIOAuthBaseURL {
		t.Fatalf("Spec.OpenAIBaseURL = %q, want %q", updated.Spec.OpenAIBaseURL, triggersv1alpha1.DefaultOpenAIOAuthBaseURL)
	}
}

func TestAppendAllSavedProviderCredentialsMountsEverything(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(corev1): %v", err)
	}

	objects := []client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "usercred-anthropic", Namespace: "default"},
			Data:       map[string][]byte{"api-key": []byte("sk-ant"), "auth.json": []byte(`{"access_token":"a"}`)},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "usercred-openai", Namespace: "default"},
			Data:       map[string][]byte{"api-key": []byte("sk-oai"), "auth.json": []byte(`{"access_token":"o"}`)},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "usercred-openrouter", Namespace: "default"},
			Data:       map[string][]byte{"api-key": []byte("openrouter-test-key")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "usercred-xai", Namespace: "default"},
			Data:       map[string][]byte{"api-key": []byte("xai-test-key")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "usercred-copilot", Namespace: "default"},
			Data:       map[string][]byte{"auth.json": []byte(`{"oauth_token":"g"}`)},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	secrets := &platformv1alpha1.AgentRunSecrets{
		OpenAIOAuthSecret: "usercred-anthropic",
		ProviderKeys: []platformv1alpha1.ProviderKeyRef{
			{Provider: "openai", SecretName: "existing-openai-key", SecretKey: "api-key"},
		},
	}
	srv.appendAllSavedProviderCredentials(context.Background(), "default", secrets)

	keyFor := map[string]string{}
	for _, pk := range secrets.ProviderKeys {
		keyFor[pk.Provider] = pk.SecretName
	}
	if keyFor["openai"] != "existing-openai-key" {
		t.Fatalf("existing openai key overwritten: %+v", secrets.ProviderKeys)
	}
	if keyFor["anthropic"] != "usercred-anthropic" {
		t.Fatalf("ProviderKeys missing saved anthropic key: %+v", secrets.ProviderKeys)
	}
	for _, p := range []string{"gemini", "groq"} {
		if keyFor[p] != "usercred-openai" {
			t.Fatalf("ProviderKeys missing openai-backed %s key: %+v", p, secrets.ProviderKeys)
		}
	}
	if keyFor["openrouter"] != "usercred-openrouter" || keyFor["xai"] != "usercred-xai" {
		t.Fatalf("ProviderKeys missing dedicated OpenRouter/xAI keys: %+v", secrets.ProviderKeys)
	}

	oauthFor := map[string]string{}
	for _, ref := range secrets.ProviderOAuthSecrets {
		oauthFor[ref.Provider] = ref.SecretName
	}
	if oauthFor["openai"] != "usercred-openai" || oauthFor["anthropic"] != "usercred-anthropic" || oauthFor["copilot"] != "usercred-copilot" {
		t.Fatalf("ProviderOAuthSecrets = %+v, want all three saved OAuth credentials", secrets.ProviderOAuthSecrets)
	}

	// Idempotent: appending twice must not duplicate entries.
	before := len(secrets.ProviderKeys) + len(secrets.ProviderOAuthSecrets)
	srv.appendAllSavedProviderCredentials(context.Background(), "default", secrets)
	if after := len(secrets.ProviderKeys) + len(secrets.ProviderOAuthSecrets); after != before {
		t.Fatalf("appendAllSavedProviderCredentials not idempotent: %d -> %d entries", before, after)
	}
}

func TestUpdateAgentRunRuntimeConfigSwitchesOAuthProviderViaRestart(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(corev1): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-oauth-switch", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Model:    "anthropic/claude-sonnet-4-6",
			AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
			Secrets:  &platformv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "usercred-anthropic"},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	savedOpenAIOAuth := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "usercred-openai", Namespace: "default"},
		Data:       map[string][]byte{"auth.json": []byte(`{"access_token":"tok"}`)},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, savedOpenAIOAuth).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-oauth-switch", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.UpdateAgentRunRuntimeConfig(actorContext("user-1", "", "", ""), &platform.UpdateAgentRunRuntimeConfigRequest{
		Namespace: "default",
		Name:      "run-oauth-switch",
		Provider:  "openai",
		Model:     "gpt-5",
	}); err != nil {
		t.Fatalf("UpdateAgentRunRuntimeConfig() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-oauth-switch"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Spec.Model != "gpt-5" {
		t.Fatalf("Spec.Model = %q, want gpt-5", updated.Spec.Model)
	}
	if updated.Spec.AuthMode != platformv1alpha1.AgentRunAuthModeOAuth {
		t.Fatalf("Spec.AuthMode = %q, want oauth", updated.Spec.AuthMode)
	}
	if updated.Spec.Secrets == nil || updated.Spec.Secrets.OpenAIOAuthSecret != "usercred-openai" {
		t.Fatalf("Spec.Secrets.OpenAIOAuthSecret = %+v, want usercred-openai", updated.Spec.Secrets)
	}
	if updated.Spec.OpenAIBaseURL != triggersv1alpha1.DefaultOpenAIOAuthBaseURL {
		t.Fatalf("Spec.OpenAIBaseURL = %q, want %q", updated.Spec.OpenAIBaseURL, triggersv1alpha1.DefaultOpenAIOAuthBaseURL)
	}
	if updated.Spec.RestartRequests != 1 {
		t.Fatalf("Spec.RestartRequests = %d, want 1 (OAuth switches need a compute restart to remount credentials)", updated.Spec.RestartRequests)
	}
}

func TestUpdateAgentRunRuntimeConfigSwitchesOAuthRunToSavedAPIKeyViaRestart(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(corev1): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-key-switch", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Model:    "anthropic/claude-sonnet-4-6",
			AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
			Secrets:  &platformv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "usercred-anthropic"},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	savedOpenRouterKey := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "usercred-openrouter", Namespace: "default"},
		Data:       map[string][]byte{"api-key": []byte("openrouter-test-key")},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, savedOpenRouterKey).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-key-switch", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.UpdateAgentRunRuntimeConfig(actorContext("user-1", "", "", ""), &platform.UpdateAgentRunRuntimeConfigRequest{
		Namespace: "default",
		Name:      "run-key-switch",
		Provider:  "openrouter",
		Model:     "openai/gpt-5",
	}); err != nil {
		t.Fatalf("UpdateAgentRunRuntimeConfig() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-key-switch"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Spec.Model != "openrouter/openai/gpt-5" {
		t.Fatalf("Spec.Model = %q, want openrouter/openai/gpt-5", updated.Spec.Model)
	}
	if updated.Spec.AuthMode != platformv1alpha1.AgentRunAuthModeAPIKey {
		t.Fatalf("Spec.AuthMode = %q, want api-key", updated.Spec.AuthMode)
	}
	if updated.Spec.Secrets == nil || updated.Spec.Secrets.OpenAIOAuthSecret != "" {
		t.Fatalf("Spec.Secrets.OpenAIOAuthSecret = %+v, want cleared (api-key targets must not keep foreign OAuth material)", updated.Spec.Secrets)
	}
	foundKey := false
	for _, pk := range updated.Spec.Secrets.ProviderKeys {
		if pk.Provider == "openrouter" && pk.SecretName == "usercred-openrouter" && pk.SecretKey == "api-key" {
			foundKey = true
		}
	}
	if !foundKey {
		t.Fatalf("Spec.Secrets.ProviderKeys = %+v, want openrouter key from saved credentials", updated.Spec.Secrets.ProviderKeys)
	}
	if updated.Spec.OpenAIBaseURL != triggersv1alpha1.DefaultOpenRouterBaseURL {
		t.Fatalf("Spec.OpenAIBaseURL = %q, want %q", updated.Spec.OpenAIBaseURL, triggersv1alpha1.DefaultOpenRouterBaseURL)
	}
	if updated.Spec.RestartRequests != 1 {
		t.Fatalf("Spec.RestartRequests = %d, want 1 (unmounted-key switches need a compute restart)", updated.Spec.RestartRequests)
	}
}

func TestSendAgentRunMessageWritesToPostgres(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-1", "default", "running", "chatting")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.SendAgentRunMessage(context.Background(), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-1",
		Message:   "Please refine this plan",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 || msgs[0].Content != "Please refine this plan" || msgs[0].Role != "user" {
		t.Fatalf("messages = %#v, want single user message", msgs)
	}
	if got := strings.TrimSpace(string(msgs[0].Metadata)); got != `{"mode":"enqueue"}` {
		t.Fatalf("message metadata = %s, want enqueue mode", got)
	}
}

func TestSendAgentRunMessageAllowsBootstrapSessionState(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-bootstrap", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseRunning,
			CurrentStep: "implement",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-bootstrap", "default", "pending", "setup")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.SendAgentRunMessage(context.Background(), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-bootstrap",
		Message:   "continue",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 || msgs[0].Content != "continue" || msgs[0].Role != "user" {
		t.Fatalf("messages = %#v, want single queued user message", msgs)
	}
}

func TestSendAgentRunMessagePersistsImmediateModeMetadata(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-immediate", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-immediate", "default", "running", "chatting")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.SendAgentRunMessage(context.Background(), &platform.SendAgentRunMessageRequest{
		Namespace:   "default",
		Name:        "run-immediate",
		Message:     "please pivot immediately",
		MessageMode: platform.AgentRunMessageMode_AGENT_RUN_MESSAGE_MODE_IMMEDIATE,
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v, want one message", msgs)
	}
	if got := strings.TrimSpace(string(msgs[0].Metadata)); got != `{"mode":"immediate"}` {
		t.Fatalf("message metadata = %s, want immediate mode", got)
	}
}

func TestSendAgentRunMessageChoiceCarriesAnsweredRequestID(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-choice", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseQuestion,
			CurrentStep: "awaiting-user",
			Queue:       &platformv1alpha1.AgentRunQueueStatus{State: "Question", BlockedReason: "question"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-choice", "default", "question", "awaiting-user")
	sess.PendingRequestID = "question-request-1"
	sess.PendingQuestion = "Choose a wait strategy"
	sess.PendingInputType = string(platformv1alpha1.UserInputQuestion)
	sess.PendingActions = json.RawMessage(`[{"id":"blocking","label":"Blocking tool"}]`)
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.SendAgentRunMessage(context.Background(), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-choice",
		Message:   "__action:blocking",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 || msgs[0].Role != "user" || msgs[0].Content != "Blocking tool" {
		t.Fatalf("messages = %#v, want the selected choice as one user message", msgs)
	}
	var metadata struct {
		Mode             string `json:"mode"`
		PendingRequestID string `json:"pending_request_id"`
	}
	if err := json.Unmarshal(msgs[0].Metadata, &metadata); err != nil {
		t.Fatalf("Unmarshal(message metadata) error = %v", err)
	}
	if metadata.Mode != "enqueue" || metadata.PendingRequestID != "question-request-1" {
		t.Fatalf("message metadata = %#v, want enqueue mode linked to question-request-1", metadata)
	}
	if sess.PendingRequestID != "" || sess.PendingInputType != "" {
		t.Fatalf("pending request was not consumed: %#v", sess)
	}
}

func TestSendAgentRunMessageRejectActionDoesNotEnqueueUserReply(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-reject", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseQuestion,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-reject", "default", "question", "awaiting-user")
	sess.PendingActions = json.RawMessage(`[{"id":"reject","label":"Reject"}]`)
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.SendAgentRunMessage(context.Background(), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-reject",
		Message:   "__action:reject",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v, want one system message", msgs)
	}
	if msgs[0].Role != "system" {
		t.Fatalf("message role = %q, want system", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "Plan rejected") {
		t.Fatalf("message content = %q, want plan rejection note", msgs[0].Content)
	}
}

func TestSendAgentRunMessageApproveActionGrantsMCPBreakGlassForAdmin(t *testing.T) {
	longReason := strings.Repeat("x", 1200)
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "run-mcp-approve",
			Namespace:   "default",
			Annotations: map[string]string{},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			MCPPolicyRef: &platformv1alpha1.NamedRef{Name: "policy"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseWaitingApproval,
		},
	}
	if err := mcppolicy.SetPendingRequest(run.Annotations, mcppolicy.BreakGlassRequest{
		ID:     "mcp-request-1",
		Server: "github",
		Tool:   "create_issue",
		Reason: longReason,
	}); err != nil {
		t.Fatalf("SetPendingRequest() error = %v", err)
	}
	policy := &platformv1alpha1.MCPPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
		Spec: platformv1alpha1.MCPPolicySpec{
			DefaultAction: platformv1alpha1.MCPDefaultActionDeny,
			BreakGlass: &platformv1alpha1.MCPBreakGlass{
				Enabled:       true,
				AdminMediated: true,
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, policy).
		Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-mcp-approve", "default", "waitingapproval", "awaiting-user")
	sess.PendingRequestID = "input-approve-1"
	sess.PendingQuestion = "Approve MCP break-glass?"
	sess.PendingInputType = string(platformv1alpha1.UserInputApproval)
	sess.PendingActions = json.RawMessage(`[{"id":"approve","label":"Approve"},{"id":"reject","label":"Reject"}]`)
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.SendAgentRunMessage(actorContext("admin-1", "admin", "", ""), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-mcp-approve",
		Message:   "__action:approve",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2 (system + user)", len(msgs))
	}
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "MCP break-glass approved") {
		t.Fatalf("first message = %#v, want system approval note", msgs[0])
	}
	if msgs[1].Role != "user" || !strings.Contains(msgs[1].Content, "MCP break-glass approved") {
		t.Fatalf("second message = %#v, want user resume message", msgs[1])
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-mcp-approve", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseRunning {
		t.Fatalf("Phase = %q, want Running", updated.Status.Phase)
	}
	if pending, err := mcppolicy.PendingRequest(&updated); err != nil {
		t.Fatalf("PendingRequest(updated) error = %v", err)
	} else if pending != nil {
		t.Fatalf("PendingRequest(updated) = %#v, want nil", pending)
	}
	grants, err := mcppolicy.GrantedGrants(&updated)
	if err != nil {
		t.Fatalf("GrantedGrants(updated) error = %v", err)
	}
	if len(grants) != 1 || grants[0].Server != "github" || grants[0].Tool != "create_issue" {
		t.Fatalf("GrantedGrants(updated) = %#v, want github/create_issue grant", grants)
	}
	if grants[0].Reason == longReason || len(grants[0].Reason) > 1003 {
		t.Fatalf("grant reason was not bounded: len=%d", len(grants[0].Reason))
	}
}

func TestSendAgentRunMessageApproveActionRejectsMCPBreakGlassForNonAdmin(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "run-mcp-nonadmin",
			Namespace:   "default",
			Annotations: map[string]string{},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			MCPPolicyRef: &platformv1alpha1.NamedRef{Name: "policy"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseWaitingApproval,
		},
	}
	if err := mcppolicy.SetPendingRequest(run.Annotations, mcppolicy.BreakGlassRequest{
		ID:     "mcp-request-1",
		Server: "github",
		Tool:   "create_issue",
		Reason: "Need to create the tracked issue",
	}); err != nil {
		t.Fatalf("SetPendingRequest() error = %v", err)
	}
	policy := &platformv1alpha1.MCPPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
		Spec: platformv1alpha1.MCPPolicySpec{
			DefaultAction: platformv1alpha1.MCPDefaultActionDeny,
			BreakGlass: &platformv1alpha1.MCPBreakGlass{
				Enabled:       true,
				AdminMediated: true,
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, policy).
		Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-mcp-nonadmin", "default", "waitingapproval", "awaiting-user")
	sess.PendingRequestID = "input-nonadmin-1"
	sess.PendingQuestion = "Approve MCP break-glass?"
	sess.PendingInputType = string(platformv1alpha1.UserInputApproval)
	sess.PendingActions = json.RawMessage(`[{"id":"approve","label":"Approve"},{"id":"reject","label":"Reject"}]`)
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.SendAgentRunMessage(actorContext("member-1", "member", "", ""), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-mcp-nonadmin",
		Message:   "__action:approve",
	})
	if err == nil {
		t.Fatal("SendAgentRunMessage() error = nil, want permission denied")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodePermissionDenied {
		t.Fatalf("error = %v, want connect permission denied", err)
	}

	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 0 {
		t.Fatalf("messages = %#v, want none on denied approval", msgs)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-mcp-nonadmin", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseWaitingApproval {
		t.Fatalf("Phase = %q, want WaitingApproval", updated.Status.Phase)
	}
	if pending, err := mcppolicy.PendingRequest(&updated); err != nil {
		t.Fatalf("PendingRequest(updated) error = %v", err)
	} else if pending == nil {
		t.Fatal("PendingRequest(updated) = nil, want request to remain pending")
	}
}

func TestSendAgentRunMessageRejectActionDeniesMCPBreakGlass(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "run-mcp-reject",
			Namespace:   "default",
			Annotations: map[string]string{},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			MCPPolicyRef: &platformv1alpha1.NamedRef{Name: "policy"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseWaitingApproval,
		},
	}
	if err := mcppolicy.SetPendingRequest(run.Annotations, mcppolicy.BreakGlassRequest{
		ID:     "mcp-request-1",
		Server: "github",
		Tool:   "create_issue",
		Reason: "Need to create the tracked issue",
	}); err != nil {
		t.Fatalf("SetPendingRequest() error = %v", err)
	}
	policy := &platformv1alpha1.MCPPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
		Spec: platformv1alpha1.MCPPolicySpec{
			DefaultAction: platformv1alpha1.MCPDefaultActionDeny,
			BreakGlass:    &platformv1alpha1.MCPBreakGlass{Enabled: true},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, policy).
		Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-mcp-reject", "default", "waitingapproval", "awaiting-user")
	sess.PendingRequestID = "input-reject-1"
	sess.PendingQuestion = "Approve MCP break-glass?"
	sess.PendingInputType = string(platformv1alpha1.UserInputApproval)
	sess.PendingActions = json.RawMessage(`[{"id":"approve","label":"Approve"},{"id":"reject","label":"Reject"}]`)
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.SendAgentRunMessage(actorContext("user-1", "member", "", ""), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-mcp-reject",
		Message:   "__action:reject",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2 (system + user)", len(msgs))
	}
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "MCP break-glass was denied") {
		t.Fatalf("first message = %#v, want system denial note", msgs[0])
	}
	if msgs[1].Role != "user" || !strings.Contains(msgs[1].Content, "MCP break-glass denied") {
		t.Fatalf("second message = %#v, want user denial resume message", msgs[1])
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-mcp-reject", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseRunning {
		t.Fatalf("Phase = %q, want Running", updated.Status.Phase)
	}
	if pending, err := mcppolicy.PendingRequest(&updated); err != nil {
		t.Fatalf("PendingRequest(updated) error = %v", err)
	} else if pending != nil {
		t.Fatalf("PendingRequest(updated) = %#v, want nil", pending)
	}
}

func TestSendAgentRunMessageRoutesToChatExecutionRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-chat", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-chat", "default", "running", "chatting")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.SendAgentRunMessage(context.Background(), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-chat",
		Message:   "Continue and fix the flaky test",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 || msgs[0].Content != "Continue and fix the flaky test" {
		t.Fatalf("messages = %#v, want single user message", msgs)
	}
}

func TestSendAgentRunMessagePlanCommandSwitchesToPlanMode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-chat", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			Context:      &platformv1alpha1.AgentRunContext{},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:    platformv1alpha1.AgentRunPhaseBlocked,
			ModeName: "chat",
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{
				Name:     "chat",
				Version:  "v1",
				Category: platformv1alpha1.ModeCategoryDirect,
			},
		},
	}
	planTmpl := &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "plan"},
		Spec: platformv1alpha1.ModeTemplateSpec{
			Name:     "plan",
			Version:  "v1",
			Category: platformv1alpha1.ModeCategoryDirect,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, planTmpl).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-chat", "default", "running", "chatting")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	ctx := sessionCommandActorContext("member", "", "")
	if _, err := srv.SendAgentRunMessage(ctx, &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-chat",
		Message:   "/plan",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	// The run's mode is switched to the plan ModeTemplate.
	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-chat", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.ModeName != "plan" {
		t.Fatalf("ModeName = %q, want plan", updated.Status.ModeName)
	}
	if updated.Status.ModeSnapshot == nil || updated.Status.ModeSnapshot.Name != "plan" {
		t.Fatalf("ModeSnapshot = %#v, want plan snapshot", updated.Status.ModeSnapshot)
	}
	// The raw slash command is never persisted as a user chat bubble; only a
	// subtle system notice marks the transition.
	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 {
		t.Fatalf("messages count = %d, want 1 (system mode-change notice only); got %#v", len(msgs), msgs)
	}
	if msgs[0].Role != "system" {
		t.Fatalf("notice role = %q, want system", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "Switched to plan mode") {
		t.Fatalf("notice content = %q, want plan-mode transition", msgs[0].Content)
	}
	for _, m := range msgs {
		if m.Content == "/plan" {
			t.Fatalf("raw /plan command must not be persisted as a message")
		}
	}
}

func TestSendAgentRunMessageExitPlanCommandSwitchesToAutopilot(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-chat", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			Context:      &platformv1alpha1.AgentRunContext{},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:    platformv1alpha1.AgentRunPhaseRunning,
			ModeName: "plan",
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{
				Name:           "plan",
				Version:        "v1",
				Category:       platformv1alpha1.ModeCategoryDirect,
				PermissionMode: platformv1alpha1.PermissionModeReadOnly,
			},
		},
	}
	autopilotTmpl := &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "autopilot"},
		Spec: platformv1alpha1.ModeTemplateSpec{
			Name:       "autopilot",
			Version:    "v1",
			Category:   platformv1alpha1.ModeCategoryOrchestrated,
			Autonomous: true,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, autopilotTmpl).Build()
	ms := newMockStateStore()
	ms.CreateSession(context.Background(), "run-chat", "default", "running", "chatting")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	ctx := sessionCommandActorContext("member", "", "")
	if _, err := srv.SendAgentRunMessage(ctx, &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-chat",
		Message:   "/exit-plan",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-chat", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.ModeName != "autopilot" {
		t.Fatalf("ModeName = %q, want autopilot", updated.Status.ModeName)
	}
}

func TestSendAgentRunMessageApprovePlanContinuesInCurrentMode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-plan-accept", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:    platformv1alpha1.AgentRunPhaseQuestion,
			ModeName: "plan",
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{
				Name:           "plan",
				Version:        "v1",
				Category:       platformv1alpha1.ModeCategoryDirect,
				PermissionMode: platformv1alpha1.PermissionModeReadOnly,
			},
		},
	}
	planTmpl := &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "plan"},
		Spec:       platformv1alpha1.ModeTemplateSpec{Name: "plan", Version: "v2", Category: platformv1alpha1.ModeCategoryDirect},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, planTmpl).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-plan-accept", "default", "question", "awaiting-user")
	ms.getArtifact = &store.Artifact{ID: uuid.New(), SessionID: sess.ID, Kind: "plan", Content: "# Plan\n\nBuild it."}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.SendAgentRunMessage(actorContext("member-1", "member", "", ""), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-plan-accept",
		Message:   "__action:accept_plan",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-plan-accept", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.ModeName != "plan" {
		t.Fatalf("ModeName = %q, want plan", updated.Status.ModeName)
	}
	if updated.Status.ModeSnapshot == nil || updated.Status.ModeSnapshot.PermissionMode == platformv1alpha1.PermissionModeReadOnly || updated.Status.ModeVersion != "v2" {
		t.Fatalf("ModeSnapshot = %#v version %q, want refreshed write-capable plan v2", updated.Status.ModeSnapshot, updated.Status.ModeVersion)
	}
	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v, want one resume message", msgs)
	}
	if msgs[0].Role != "user" || !strings.Contains(msgs[0].Content, "Plan approved. Continue with implementation.") {
		t.Fatalf("message = %#v, want plan approval resume message", msgs[0])
	}
	if strings.Contains(msgs[0].Content, "__action:accept_plan") {
		t.Fatalf("raw action command must not be persisted: %q", msgs[0].Content)
	}
	if got := strings.TrimSpace(string(sess.PendingActions)); got != "[]" {
		t.Fatalf("PendingActions = %s, want []", got)
	}
}

func TestSendAgentRunMessageLegacyAcceptBuildAutoContinuesInCurrentMode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-plan-auto", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseQuestion,
			ModeName:    "chat",
			ModeVersion: "v1",
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{
				Name:     "chat",
				Version:  "v1",
				Category: platformv1alpha1.ModeCategoryDirect,
			},
		},
	}
	autopilot := &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "autopilot"},
		Spec: platformv1alpha1.ModeTemplateSpec{
			Name:       "autopilot",
			Version:    "v1",
			Category:   platformv1alpha1.ModeCategoryOrchestrated,
			Autonomous: true,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, autopilot).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-plan-auto", "default", "question", "awaiting-user")
	sess.PendingActions = json.RawMessage(`[{"id":"accept_build_auto","label":"Build on auto mode","mode":"autopilot"}]`)
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.SendAgentRunMessage(actorContext("admin-1", "admin", "", ""), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-plan-auto",
		Message:   "__action:accept_build_auto",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	if got := strings.TrimSpace(string(sess.PendingActions)); got != "[]" {
		t.Fatalf("PendingActions = %s, want []", got)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-plan-auto", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.ModeName != "chat" {
		t.Fatalf("ModeName = %q, want chat", updated.Status.ModeName)
	}

	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v, want one resume message", msgs)
	}
	if msgs[0].Role != "user" || !strings.Contains(msgs[0].Content, "Plan approved. Continue with implementation.") {
		t.Fatalf("message = %#v, want in-place plan resume message", msgs[0])
	}
}

// Agent-authored plan buttons may carry a stale or nonexistent target mode. A
// plan-review approval ignores that field and continues in the current mode.
func TestSendAgentRunMessagePlanActionIgnoresTargetMode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-plan-action", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:    platformv1alpha1.AgentRunPhaseQuestion,
			ModeName: "plan",
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{
				Name: "plan", Version: "v1", Category: platformv1alpha1.ModeCategoryDirect,
			},
		},
	}
	planTmpl := &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "plan"},
		Spec:       platformv1alpha1.ModeTemplateSpec{Name: "plan", Version: "v1", Category: platformv1alpha1.ModeCategoryDirect},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, planTmpl).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-plan-action", "default", "question", "awaiting-user")
	sess.PendingInputType = string(platformv1alpha1.UserInputPlanReview)
	sess.PendingActions = json.RawMessage(`[{"id":"implement_pr","label":"Implement & open PR","mode":"build"}]`)
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.SendAgentRunMessage(actorContext("admin-1", "admin", "", ""), &platform.SendAgentRunMessageRequest{
		Namespace: "default", Name: "run-plan-action", Message: "__action:implement_pr",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-plan-action", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.ModeName != "plan" {
		t.Fatalf("ModeName = %q, want plan", updated.Status.ModeName)
	}
	if got := strings.TrimSpace(string(sess.PendingActions)); got != "[]" {
		t.Fatalf("PendingActions = %s, want []", got)
	}
	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 || msgs[0].Role != "user" || !strings.Contains(msgs[0].Content, "Plan approved. Continue with implementation.") {
		t.Fatalf("messages = %#v, want one in-place plan approval message", msgs)
	}
}

// Non-plan actions with an explicit mode retain the generic mode-switch
// behavior; only plan-review approval is guaranteed to continue in place.
func TestSendAgentRunMessageModeActionAppliedSwitchesMode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-mode-action-applied", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:    platformv1alpha1.AgentRunPhaseQuestion,
			ModeName: "plan",
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{
				Name:           "plan",
				Version:        "v1",
				Category:       platformv1alpha1.ModeCategoryDirect,
				PermissionMode: platformv1alpha1.PermissionModeReadOnly,
			},
		},
	}
	autopilot := &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "autopilot"},
		Spec: platformv1alpha1.ModeTemplateSpec{
			Name:       "autopilot",
			Version:    "v1",
			Category:   platformv1alpha1.ModeCategoryOrchestrated,
			Autonomous: true,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, autopilot).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-mode-action-applied", "default", "question", "awaiting-user")
	sess.PendingActions = json.RawMessage(`[{"id":"implement_pr","label":"Implement & open PR","mode":"autopilot"}]`)
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	if _, err := srv.SendAgentRunMessage(actorContext("admin-1", "admin", "", ""), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-mode-action-applied",
		Message:   "__action:implement_pr",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	if got := strings.TrimSpace(string(sess.PendingActions)); got != "[]" {
		t.Fatalf("PendingActions = %s, want []", got)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-mode-action-applied", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.ModeName != "autopilot" {
		t.Fatalf("ModeName = %q, want autopilot", updated.Status.ModeName)
	}

	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 3 {
		t.Fatalf("messages = %#v, want switch notice + label + confirmation", msgs)
	}
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "Switched to autopilot mode") {
		t.Fatalf("first message = %#v, want mode-switch notice", msgs[0])
	}
	if msgs[1].Role != "user" || !strings.Contains(msgs[1].Content, "Implement & open PR") {
		t.Fatalf("second message = %#v, want action label as user message", msgs[1])
	}
}

func TestSendAgentRunMessagePlanCommandNoopWhenAlreadyInTargetMode(t *testing.T) {
	tests := []struct {
		name        string
		initialMode string
		command     string
	}{
		{name: "plan noop when already plan", initialMode: "plan", command: "/plan"},
		{name: "exit-plan noop when already chat", initialMode: "chat", command: "/exit-plan"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := platformv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatalf("AddToScheme(platform): %v", err)
			}

			run := &platformv1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{Name: "run-chat", Namespace: "default"},
				Spec: platformv1alpha1.AgentRunSpec{
					WorkflowMode: platformv1alpha1.WorkflowModeChat,
					Context:      &platformv1alpha1.AgentRunContext{},
				},
				Status: platformv1alpha1.AgentRunStatus{
					Phase:    platformv1alpha1.AgentRunPhaseRunning,
					ModeName: tt.initialMode,
					ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{
						Name:     tt.initialMode,
						Version:  "v1",
						Category: platformv1alpha1.ModeCategoryDirect,
					},
				},
			}
			tmpl := &platformv1alpha1.ModeTemplate{
				ObjectMeta: metav1.ObjectMeta{Name: tt.initialMode},
				Spec: platformv1alpha1.ModeTemplateSpec{
					Name:     tt.initialMode,
					Version:  "v1",
					Category: platformv1alpha1.ModeCategoryDirect,
				},
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, tmpl).Build()
			ms := newMockStateStore()
			ms.CreateSession(context.Background(), "run-chat", "default", "running", "chatting")
			srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

			ctx := sessionCommandActorContext("member", "", "")
			if _, err := srv.SendAgentRunMessage(ctx, &platform.SendAgentRunMessageRequest{
				Namespace: "default",
				Name:      "run-chat",
				Message:   tt.command,
			}); err != nil {
				t.Fatalf("SendAgentRunMessage() error = %v", err)
			}

			var updated platformv1alpha1.AgentRun
			if err := c.Get(context.Background(), client.ObjectKey{Name: "run-chat", Namespace: "default"}, &updated); err != nil {
				t.Fatalf("Get(updated run) error = %v", err)
			}
			if updated.Status.ModeName != tt.initialMode {
				t.Fatalf("ModeName = %q, want unchanged %q", updated.Status.ModeName, tt.initialMode)
			}
			if updated.Status.ModeRevision != 0 {
				t.Fatalf("ModeRevision = %d, want unchanged 0", updated.Status.ModeRevision)
			}
		})
	}
}

func TestSendAgentRunMessageSessionModeCommandDenied(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-chat", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			Context:      &platformv1alpha1.AgentRunContext{},
		},
	}
	planTmpl := &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "plan"},
		Spec: platformv1alpha1.ModeTemplateSpec{
			Name:     "plan",
			Version:  "v1",
			Category: platformv1alpha1.ModeCategoryDirect,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run, planTmpl).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-chat", "default", "running", "chatting")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	// Viewers cannot switch modes: the switch is denied by mode RBAC and the
	// denial surfaces as an inline system notice (mirroring /mode and
	// /autopilot), not an RPC error.
	ctx := sessionCommandActorContext("viewer", "", "")
	if _, err := srv.SendAgentRunMessage(ctx, &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-chat",
		Message:   "/plan",
	}); err != nil {
		t.Fatalf("SendAgentRunMessage() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-chat", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.ModeName == "plan" {
		t.Fatalf("ModeName = %q, want mode switch denied for viewer", updated.Status.ModeName)
	}
	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 || msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "Mode switch denied") {
		t.Fatalf("messages = %#v, want one mode-switch denial system message", msgs)
	}
}

func TestSendAgentRunMessageSessionModeCommandUnauthenticated(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-chat", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run).Build()
	ms := newMockStateStore()
	ms.CreateSession(context.Background(), "run-chat", "default", "running", "chatting")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	// Identity-less requests are rejected as unauthenticated before mode RBAC
	// runs.
	ctx := sessionCommandActorContext("", "", "")
	_, err := srv.SendAgentRunMessage(ctx, &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-chat",
		Message:   "/plan",
	})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("connect.CodeOf(err) = %v, want %v (err=%v)", connect.CodeOf(err), connect.CodeUnauthenticated, err)
	}
}

func TestSendAgentRunMessageReturnsFailedPreconditionWhenSessionMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-chat", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.SendAgentRunMessage(context.Background(), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-chat",
		Message:   "hello",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("connect.CodeOf(err) = %v, want %v (err=%v)", connect.CodeOf(err), connect.CodeFailedPrecondition, err)
	}
}

func TestSendAgentRunMessageActionReturnsFailedPreconditionWhenSessionMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-chat", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.SendAgentRunMessage(context.Background(), &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-chat",
		Message:   "__action:approve",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("connect.CodeOf(err) = %v, want %v (err=%v)", connect.CodeOf(err), connect.CodeFailedPrecondition, err)
	}
}

func TestSendAgentRunMessageSessionModeCommandReturnsFailedPreconditionWhenSessionMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-chat", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			Context:      &platformv1alpha1.AgentRunContext{},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run).Build()
	ms := newMockStateStore()
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	ctx := sessionCommandActorContext("member", "", "")
	_, err := srv.SendAgentRunMessage(ctx, &platform.SendAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-chat",
		Message:   "/plan",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("connect.CodeOf(err) = %v, want %v (err=%v)", connect.CodeOf(err), connect.CodeFailedPrecondition, err)
	}
}

func TestRenameAgentRunSetsDisplayName(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-rename", Namespace: "default"},
		Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-rename", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.RenameAgentRun(actorContext("user-1", "", "", ""), &platform.RenameAgentRunRequest{
		Namespace:   "default",
		Name:        "run-rename",
		DisplayName: "  Fix the retry race  ",
	})
	if err != nil {
		t.Fatalf("RenameAgentRun() error = %v", err)
	}
	if resp.DisplayName != "Fix the retry race" {
		t.Fatalf("response DisplayName = %q, want %q", resp.DisplayName, "Fix the retry race")
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-rename"}, updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.DisplayName != "Fix the retry race" {
		t.Fatalf("status.displayName = %q, want %q", updated.Status.DisplayName, "Fix the retry race")
	}
}

func TestRenameAgentRunRejectsEmptyDisplayName(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-rename-empty", Namespace: "default"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-rename-empty", "default", "user-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.RenameAgentRun(actorContext("user-1", "", "", ""), &platform.RenameAgentRunRequest{
		Namespace:   "default",
		Name:        "run-rename-empty",
		DisplayName: "   ",
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("connect.CodeOf(err) = %v, want InvalidArgument (err=%v)", connect.CodeOf(err), err)
	}
}

func TestRenameAgentRunRejectsNonCollaborator(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-rename-owned", Namespace: "default"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(run).Build()
	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-rename-owned", "default", "owner-1"); err != nil {
		t.Fatalf("SetResourceOwner() error = %v", err)
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.RenameAgentRun(actorContext("viewer-1", "", "", ""), &platform.RenameAgentRunRequest{
		Namespace:   "default",
		Name:        "run-rename-owned",
		DisplayName: "hijacked",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("connect.CodeOf(err) = %v, want PermissionDenied (err=%v)", connect.CodeOf(err), err)
	}
}

func TestCancelAgentRunMessage(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-1", "default", "running", "chatting")
	assetID := uuid.New()
	assetStore := &contentStoreStub{
		StateStore: ms,
		item: store.ProjectContent{
			ID: assetID, ProjectNamespace: "default", ProjectName: "briefs",
			Path: "chat-attachments/run-1/image.png", CurrentVersion: 1,
		},
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: assetStore}

	pendingMetadata := sessionclient.EncodeUserMessageMetadataWithImages(sessionclient.UserMessageModeEnqueue, []sessionclient.MessageImage{{
		MediaType: "image/png", Data: "AQID", AssetID: assetID.String(), AssetVersion: 1,
		AssetSHA256: "hash", AssetPath: assetStore.item.Path, ProjectName: assetStore.item.ProjectName,
	}})
	pendingMsg, _ := ms.AppendMessage(context.Background(), sess.ID, "user", "queued follow-up", pendingMetadata)
	deliveredMsg, _ := ms.AppendMessage(context.Background(), sess.ID, "user", "already consumed", json.RawMessage(`{"mode":"immediate","delivered_at_unix":42}`))

	// Cancelling a pending message stamps cancellation metadata.
	if _, err := srv.CancelAgentRunMessage(context.Background(), &platform.CancelAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-1",
		MessageId: pendingMsg.ID,
	}); err != nil {
		t.Fatalf("CancelAgentRunMessage() error = %v", err)
	}
	msgs := ms.messagesFor(sess.ID)
	if !strings.Contains(string(msgs[0].Metadata), "cancelled_at_unix") {
		t.Fatalf("metadata = %s, want cancelled_at_unix stamp", msgs[0].Metadata)
	}
	if !assetStore.deleted.Confirmed || assetStore.deleted.ExpectedVersion != 1 {
		t.Fatalf("generated asset delete options = %+v", assetStore.deleted)
	}

	// Cancelled messages disappear from the rendered conversation.
	conv := conversationFromMessages(msgs, "Running")
	for _, cm := range conv {
		if cm.Content == "queued follow-up" {
			t.Fatal("cancelled message must not render in the conversation")
		}
	}

	// A second cancel of the same message reports NotFound.
	if _, err := srv.CancelAgentRunMessage(context.Background(), &platform.CancelAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-1",
		MessageId: pendingMsg.ID,
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("re-cancel: connect.CodeOf(err) = %v, want NotFound (err=%v)", connect.CodeOf(err), err)
	}

	// Messages the agent already consumed cannot be cancelled.
	if _, err := srv.CancelAgentRunMessage(context.Background(), &platform.CancelAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-1",
		MessageId: deliveredMsg.ID,
	}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("delivered: connect.CodeOf(err) = %v, want FailedPrecondition (err=%v)", connect.CodeOf(err), err)
	}

	// Unknown ids report NotFound.
	if _, err := srv.CancelAgentRunMessage(context.Background(), &platform.CancelAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-1",
		MessageId: 9999,
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing: connect.CodeOf(err) = %v, want NotFound (err=%v)", connect.CodeOf(err), err)
	}
}

func TestCancelAgentRunMessageDeniedForStranger(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	_ = ms.SetResourceOwner(context.Background(), "agent_run", "run-1", "default", "alice")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.CancelAgentRunMessage(actorContext("mallory", "member", "", ""), &platform.CancelAgentRunMessageRequest{
		Namespace: "default",
		Name:      "run-1",
		MessageId: 1,
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("connect.CodeOf(err) = %v, want PermissionDenied (err=%v)", connect.CodeOf(err), err)
	}
}
