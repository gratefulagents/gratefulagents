package sessionclient

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type metadataTestStore struct {
	session      *store.Session
	messages     []store.Message
	clearResult  bool
	pollMessages []store.Message
	pollCalls    int
	deliveredIDs []int64

	transcript          []byte
	transcriptItemCount int32
}

func (s *metadataTestStore) CreateSession(context.Context, string, string, string, string) (*store.Session, error) {
	return s.session, nil
}

func (s *metadataTestStore) GetSession(context.Context, uuid.UUID) (*store.Session, error) {
	copySession := *s.session
	copySession.Metadata = append(json.RawMessage(nil), s.session.Metadata...)
	return &copySession, nil
}

func (s *metadataTestStore) GetSessionByRun(context.Context, string, string) (*store.Session, error) {
	return s.GetSession(context.Background(), s.session.ID)
}

func (s *metadataTestStore) ListSessionsByNamespace(context.Context, string) ([]store.Session, error) {
	return nil, nil
}

func (s *metadataTestStore) UpdatePhase(context.Context, uuid.UUID, string, string) error { return nil }
func (s *metadataTestStore) SetPendingQuestion(context.Context, uuid.UUID, string, string, string) error {
	return nil
}
func (s *metadataTestStore) ClearPendingQuestion(context.Context, uuid.UUID, string) error {
	return nil
}
func (s *metadataTestStore) SetPendingAction(context.Context, uuid.UUID, string, string, json.RawMessage, string) error {
	return nil
}
func (s *metadataTestStore) ClearPendingAction(context.Context, uuid.UUID, string) error { return nil }
func (s *metadataTestStore) ClearPendingInputIfID(_ context.Context, _ uuid.UUID, requestID, phase string) (bool, error) {
	if s.clearResult {
		s.session.PendingRequestID = ""
		s.session.PendingInputType = ""
		s.session.Phase = phase
	}
	return s.clearResult, nil
}

func (s *metadataTestStore) UpdateMetadata(_ context.Context, _ uuid.UUID, metadata json.RawMessage) error {
	s.session.Metadata = append(json.RawMessage(nil), metadata...)
	return nil
}

func (s *metadataTestStore) MergeSessionMetadata(_ context.Context, _ uuid.UUID, key string, value json.RawMessage) error {
	var metadata map[string]json.RawMessage
	if len(s.session.Metadata) > 0 {
		if err := json.Unmarshal(s.session.Metadata, &metadata); err != nil {
			return err
		}
	}
	if metadata == nil {
		metadata = map[string]json.RawMessage{}
	}
	metadata[key] = append(json.RawMessage(nil), value...)
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	s.session.Metadata = encoded
	return nil
}

func (s *metadataTestStore) ListAllSessionMetrics(context.Context) ([]store.SessionMetricsEntry, error) {
	return nil, nil
}

func (s *metadataTestStore) DeleteAgentRunData(context.Context, string, string, string) error {
	return nil
}

func (s *metadataTestStore) AppendMessage(context.Context, uuid.UUID, string, string, json.RawMessage) (*store.Message, error) {
	return nil, nil
}

func (s *metadataTestStore) GetMessages(context.Context, uuid.UUID) ([]store.Message, error) {
	return append([]store.Message(nil), s.messages...), nil
}
func (s *metadataTestStore) GetMessagesIncludingCancelled(ctx context.Context, sessionID uuid.UUID) ([]store.Message, error) {
	return s.GetMessages(ctx, sessionID)
}

func (s *metadataTestStore) GetMessagesSince(_ context.Context, _ uuid.UUID, afterID int64) ([]store.Message, error) {
	var out []store.Message
	for _, msg := range s.messages {
		if msg.ID > afterID {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (s *metadataTestStore) PollNewUserMessages(context.Context, uuid.UUID, int64) ([]store.Message, error) {
	s.pollCalls++
	return append([]store.Message(nil), s.pollMessages...), nil
}

func (s *metadataTestStore) MarkMessagesDelivered(_ context.Context, _ uuid.UUID, ids []int64) error {
	s.deliveredIDs = append(s.deliveredIDs, ids...)
	return nil
}

func (s *metadataTestStore) CancelUndeliveredUserMessage(context.Context, uuid.UUID, int64) error {
	return nil
}

func (s *metadataTestStore) UpsertSessionTranscript(_ context.Context, _ uuid.UUID, data []byte, itemCount int32) error {
	s.transcript = append([]byte(nil), data...)
	s.transcriptItemCount = itemCount
	return nil
}

func (s *metadataTestStore) GetSessionTranscript(context.Context, uuid.UUID) ([]byte, error) {
	if s.transcript == nil {
		return nil, nil
	}
	return append([]byte(nil), s.transcript...), nil
}

func (s *metadataTestStore) DeleteSessionTranscript(context.Context, uuid.UUID) error {
	s.transcript = nil
	s.transcriptItemCount = 0
	return nil
}

func (s *metadataTestStore) WriteActivityEvent(context.Context, uuid.UUID, string, string, json.RawMessage) (*store.ActivityEvent, error) {
	return nil, nil
}

func (s *metadataTestStore) GetRecentActivity(context.Context, uuid.UUID, int32) ([]store.ActivityEvent, error) {
	return nil, nil
}

func (s *metadataTestStore) GetAllActivity(context.Context, uuid.UUID) ([]store.ActivityEvent, error) {
	return nil, nil
}
func (s *metadataTestStore) GetActivityEventsSince(context.Context, uuid.UUID, int64) ([]store.ActivityEvent, error) {
	return nil, nil
}
func (s *metadataTestStore) GetSessionFingerprint(context.Context, uuid.UUID) (string, error) {
	return "", nil
}

func (s *metadataTestStore) UpsertArtifact(context.Context, uuid.UUID, string, string, string, string, json.RawMessage) (*store.Artifact, error) {
	return nil, nil
}

func (s *metadataTestStore) GetArtifact(context.Context, uuid.UUID, string) (*store.Artifact, error) {
	return nil, nil
}

func (s *metadataTestStore) GetArtifacts(context.Context, uuid.UUID) ([]store.Artifact, error) {
	return nil, nil
}

func (s *metadataTestStore) SetResourceOwner(context.Context, string, string, string, string) error {
	return nil
}
func (s *metadataTestStore) GetResourceOwner(context.Context, string, string, string) (*store.ResourceOwnership, error) {
	return nil, nil
}
func (s *metadataTestStore) ListOwnedResources(context.Context, string, string) ([]store.ResourceOwnership, error) {
	return nil, nil
}
func (s *metadataTestStore) ShareResource(context.Context, *store.ResourceShare) (*store.ResourceShare, error) {
	return nil, nil
}
func (s *metadataTestStore) RevokeShare(context.Context, string) error { return nil }
func (s *metadataTestStore) UpdateSharePermission(context.Context, string, string) error {
	return nil
}
func (s *metadataTestStore) ListSharesForResource(context.Context, string, string, string) ([]store.ResourceShare, error) {
	return nil, nil
}
func (s *metadataTestStore) ListSharedWithMe(context.Context, string, string) ([]store.ResourceShare, error) {
	return nil, nil
}
func (s *metadataTestStore) GetSharePermission(context.Context, string, string, string, string) (*store.ResourceShare, error) {
	return nil, nil
}
func (s *metadataTestStore) CreateNotification(context.Context, *store.Notification) error {
	return nil
}
func (s *metadataTestStore) HasUnreadNotification(context.Context, string, string, string, string) (bool, error) {
	return false, nil
}
func (s *metadataTestStore) ListNotifications(context.Context, string, bool, int32) ([]store.Notification, error) {
	return nil, nil
}
func (s *metadataTestStore) MarkNotificationRead(context.Context, string) error     { return nil }
func (s *metadataTestStore) MarkAllNotificationsRead(context.Context, string) error { return nil }
func (s *metadataTestStore) GetUnreadNotificationCount(context.Context, string) (int32, error) {
	return 0, nil
}
func (s *metadataTestStore) Close() error { return nil }

func TestClearIdleUserInputRequestClearsKickoffIdleBoundary(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	testStore := &metadataTestStore{
		session:     &store.Session{ID: sessionID, PendingInputType: "idle", PendingRequestID: "idle-request"},
		clearResult: true,
	}
	client := &Client{store: testStore, sessionID: sessionID}

	if err := client.ClearIdleUserInputRequest(context.Background()); err != nil {
		t.Fatalf("ClearIdleUserInputRequest() error = %v", err)
	}
	if testStore.session.PendingInputType != "" || testStore.session.PendingRequestID != "" {
		t.Fatalf("idle request was not cleared: %#v", testStore.session)
	}
}

func TestClearIdleUserInputRequestPreservesNonIdleRequest(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	testStore := &metadataTestStore{
		session:     &store.Session{ID: sessionID, PendingInputType: "question", PendingRequestID: "question-request"},
		clearResult: true,
	}
	client := &Client{store: testStore, sessionID: sessionID}

	if err := client.ClearIdleUserInputRequest(context.Background()); err != nil {
		t.Fatalf("ClearIdleUserInputRequest() error = %v", err)
	}
	if testStore.session.PendingInputType != "question" || testStore.session.PendingRequestID != "question-request" {
		t.Fatalf("non-idle request was cleared: %#v", testStore.session)
	}
}

func TestClearUserInputRequestIfIDRepairsCRDAfterDashboardAlreadyClearedRequest(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseQuestion,
			CurrentStep: "awaiting-user",
			Queue:       &platformv1alpha1.AgentRunQueueStatus{State: "Question", BlockedReason: "question"},
		},
	}
	crd := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	sessionID := uuid.New()
	testStore := &metadataTestStore{session: &store.Session{ID: sessionID}}
	client := &Client{store: testStore, crd: crd, sessionID: sessionID, runName: "run", runNS: "default"}

	if err := client.ClearUserInputRequestIfID(context.Background(), "already-cleared-request"); err != nil {
		t.Fatalf("ClearUserInputRequestIfID() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := crd.Get(context.Background(), types.NamespacedName{Name: "run", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.CurrentStep != "chat-followup" {
		t.Fatalf("CurrentStep = %q, want chat-followup", updated.Status.CurrentStep)
	}
	if updated.Status.Queue == nil || updated.Status.Queue.BlockedReason != "" {
		t.Fatalf("Queue = %#v, want running queue without blocked reason", updated.Status.Queue)
	}
}

func TestClearUserInputRequestIfIDRepairsAnsweredApproval(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseWaitingApproval,
			CurrentStep: "awaiting-user",
			Queue:       &platformv1alpha1.AgentRunQueueStatus{State: "WaitingApproval", BlockedReason: "approval"},
		},
	}
	crd := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	sessionID := uuid.New()
	testStore := &metadataTestStore{
		session:     &store.Session{ID: sessionID, PendingInputType: "approval", PendingRequestID: "approval-request"},
		clearResult: true,
	}
	client := &Client{store: testStore, crd: crd, sessionID: sessionID, runName: "run", runNS: "default"}

	if err := client.ClearUserInputRequestIfID(context.Background(), "approval-request"); err != nil {
		t.Fatalf("ClearUserInputRequestIfID() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := crd.Get(context.Background(), types.NamespacedName{Name: "run", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseRunning {
		t.Fatalf("Phase = %q, want Running after answered approval", updated.Status.Phase)
	}
	if updated.Status.CurrentStep != "chat-followup" {
		t.Fatalf("CurrentStep = %q, want chat-followup", updated.Status.CurrentStep)
	}
	if updated.Status.Queue == nil || updated.Status.Queue.State != "Running" || updated.Status.Queue.BlockedReason != "" {
		t.Fatalf("Queue = %#v, want running queue without blocked reason", updated.Status.Queue)
	}
}

func TestClearUserInputRequestIfIDPreservesControllerPausedStatus(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhasePaused,
			CurrentStep: "paused",
			Queue:       &platformv1alpha1.AgentRunQueueStatus{State: "Paused", BlockedReason: "max-cost"},
		},
	}
	crd := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	sessionID := uuid.New()
	testStore := &metadataTestStore{session: &store.Session{ID: sessionID}}
	client := &Client{store: testStore, crd: crd, sessionID: sessionID, runName: "run", runNS: "default"}

	if err := client.ClearUserInputRequestIfID(context.Background(), "already-cleared-request"); err != nil {
		t.Fatalf("ClearUserInputRequestIfID() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := crd.Get(context.Background(), types.NamespacedName{Name: "run", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhasePaused || updated.Status.CurrentStep != "paused" || updated.Status.Queue.BlockedReason != "max-cost" {
		t.Fatalf("paused status was overwritten by stale reply: %#v", updated.Status)
	}
}

func TestClearUserInputRequestIfIDPreservesReplacementRequest(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseQuestion,
			CurrentStep: "awaiting-user",
			Queue:       &platformv1alpha1.AgentRunQueueStatus{State: "Question", BlockedReason: "question"},
		},
	}
	crd := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	sessionID := uuid.New()
	testStore := &metadataTestStore{session: &store.Session{ID: sessionID, PendingRequestID: "replacement", PendingInputType: "question"}}
	client := &Client{store: testStore, crd: crd, sessionID: sessionID, runName: "run", runNS: "default"}

	if err := client.ClearUserInputRequestIfID(context.Background(), "old-request"); err != nil {
		t.Fatalf("ClearUserInputRequestIfID() error = %v", err)
	}

	var updated platformv1alpha1.AgentRun
	if err := crd.Get(context.Background(), types.NamespacedName{Name: "run", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get(updated run) error = %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseQuestion || updated.Status.Queue.BlockedReason != "question" {
		t.Fatalf("status was overwritten despite replacement request: %#v", updated.Status)
	}
}

func TestUpdateWorkingStateAndWriteMetricsPreserveMetadataSections(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	testStore := &metadataTestStore{
		session: &store.Session{ID: sessionID},
	}
	client := &Client{store: testStore, sessionID: sessionID}

	if err := client.UpdateWorkingState(context.Background(), func(state *WorkingState) error {
		state.Goal = "ship github auth end to end"
		state.RecentTurnSummaries = []string{"added callback plumbing"}
		state.LastStoppedUserMessageID = 77
		return nil
	}); err != nil {
		t.Fatalf("UpdateWorkingState() error = %v", err)
	}
	if err := client.WriteMetrics(context.Background(), SessionMetrics{
		CostUSD:              1.25,
		InputTokens:          321,
		OutputTokens:         123,
		ToolCallCount:        42,
		ContextTriggerTokens: 360000,
		ContextTargetTokens:  200000,
	}); err != nil {
		t.Fatalf("WriteMetrics() error = %v", err)
	}

	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(testStore.session.Metadata, &metadata); err != nil {
		t.Fatalf("json.Unmarshal(metadata) error = %v", err)
	}

	var workingState WorkingState
	if err := json.Unmarshal(metadata[metadataKeyWorkingState], &workingState); err != nil {
		t.Fatalf("json.Unmarshal(working_state) error = %v", err)
	}
	if workingState.Goal != "ship github auth end to end" {
		t.Fatalf("workingState.Goal = %q", workingState.Goal)
	}
	if workingState.LastStoppedUserMessageID != 77 {
		t.Fatalf("workingState.LastStoppedUserMessageID = %d, want 77", workingState.LastStoppedUserMessageID)
	}

	var metrics SessionMetrics
	if err := json.Unmarshal(metadata[metadataKeyMetrics], &metrics); err != nil {
		t.Fatalf("json.Unmarshal(metrics) error = %v", err)
	}
	if metrics.InputTokens != 321 || metrics.OutputTokens != 123 {
		t.Fatalf("metrics = %#v", metrics)
	}
	if metrics.ContextTriggerTokens != 360000 || metrics.ContextTargetTokens != 200000 {
		t.Fatalf("context budget not persisted: %#v", metrics)
	}
}

func TestSubAgentCheckpointPreservesOtherMetadataSections(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	testStore := &metadataTestStore{session: &store.Session{ID: sessionID}}
	client := &Client{store: testStore, sessionID: sessionID}

	if err := client.UpdateWorkingState(context.Background(), func(state *WorkingState) error {
		state.Goal = "keep this"
		return nil
	}); err != nil {
		t.Fatalf("UpdateWorkingState() error = %v", err)
	}
	checkpoint := json.RawMessage(`{"version":1,"tasks":[{"id":"task_1","status":"running"}]}`)
	if err := client.WriteSubAgentCheckpoint(context.Background(), checkpoint); err != nil {
		t.Fatalf("WriteSubAgentCheckpoint() error = %v", err)
	}
	got, err := client.ReadSubAgentCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("ReadSubAgentCheckpoint() error = %v", err)
	}
	if string(got) != string(checkpoint) {
		t.Fatalf("checkpoint = %s, want %s", got, checkpoint)
	}
	state, err := client.ReadWorkingState(context.Background())
	if err != nil {
		t.Fatalf("ReadWorkingState() error = %v", err)
	}
	if state.Goal != "keep this" {
		t.Fatalf("Goal = %q, want preserved value", state.Goal)
	}

	got[0] = '['
	again, err := client.ReadSubAgentCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("second ReadSubAgentCheckpoint() error = %v", err)
	}
	if string(again) != string(checkpoint) {
		t.Fatalf("stored checkpoint aliased caller: %s", again)
	}
	if err := client.WriteSubAgentCheckpoint(context.Background(), json.RawMessage(`not-json`)); err == nil {
		t.Fatal("WriteSubAgentCheckpoint() accepted invalid JSON")
	}
}

func TestResetConversationWindowMovesHistoryFloorAndClearsRecentSummaries(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	testStore := &metadataTestStore{
		session: &store.Session{ID: sessionID},
		messages: []store.Message{
			{ID: 10, Content: "first"},
			{ID: 22, Content: "latest"},
		},
	}

	client := &Client{store: testStore, sessionID: sessionID}

	if err := client.UpdateWorkingState(context.Background(), func(state *WorkingState) error {
		state.Goal = "old goal"
		state.LastAssistantSummary = "old summary"
		state.RecentTurnSummaries = []string{"one", "two"}
		return nil
	}); err != nil {
		t.Fatalf("UpdateWorkingState() error = %v", err)
	}
	if err := client.ResetConversationWindow(context.Background()); err != nil {
		t.Fatalf("ResetConversationWindow() error = %v", err)
	}

	state, err := client.ReadWorkingState(context.Background())
	if err != nil {
		t.Fatalf("ReadWorkingState() error = %v", err)
	}
	if state.HistoryFloorMessageID != 22 {
		t.Fatalf("HistoryFloorMessageID = %d, want 22", state.HistoryFloorMessageID)
	}
	if state.Goal != "" || state.LastAssistantSummary != "" {
		t.Fatalf("state = %#v, want cleared summaries", state)
	}
	if len(state.RecentTurnSummaries) != 0 {
		t.Fatalf("RecentTurnSummaries = %#v, want empty", state.RecentTurnSummaries)
	}
}

func TestPollForUserMessagesChecksImmediately(t *testing.T) {
	sessionID := uuid.New()
	testStore := &metadataTestStore{
		session:      &store.Session{ID: sessionID},
		pollMessages: []store.Message{{ID: 10, Role: "user", Content: "hello"}},
	}
	client := &Client{store: testStore, sessionID: sessionID}

	start := time.Now()
	msgs, err := client.PollForUserMessages(context.Background(), 0, time.Hour)
	if err != nil {
		t.Fatalf("PollForUserMessages() error = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("PollForUserMessages waited before initial check")
	}
	if len(msgs) != 1 || msgs[0].Content != "hello" {
		t.Fatalf("messages = %#v", msgs)
	}
	if testStore.pollCalls != 1 {
		t.Fatalf("pollCalls = %d, want 1", testStore.pollCalls)
	}
}
