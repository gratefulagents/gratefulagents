package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcppolicy"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type overseerTestStore struct {
	store.StateStore
	sessions       map[string]*store.Session
	messages       map[uuid.UUID][]store.Message
	pending        map[uuid.UUID]string
	metadata       map[uuid.UUID]map[string]json.RawMessage
	nextMessage    int64
	appendCounts   map[string]int
	beforeReserve  func(*store.Session)
	releaseErrOnce error
}

func newOverseerTestStore() *overseerTestStore {
	return &overseerTestStore{
		sessions: map[string]*store.Session{}, messages: map[uuid.UUID][]store.Message{},
		pending: map[uuid.UUID]string{}, metadata: map[uuid.UUID]map[string]json.RawMessage{},
		appendCounts: map[string]int{},
	}
}

func overseerStoreKey(name, namespace string) string { return namespace + "/" + name }

func (s *overseerTestStore) CreateSession(_ context.Context, name, namespace, phase, step string) (*store.Session, error) {
	key := overseerStoreKey(name, namespace)
	if existing := s.sessions[key]; existing != nil {
		return existing, nil
	}
	session := &store.Session{ID: uuid.New(), AgentRunName: name, AgentRunNS: namespace, Phase: phase, CurrentStep: step}
	s.sessions[key] = session
	return session, nil
}

func (s *overseerTestStore) GetSessionByRun(_ context.Context, name, namespace string) (*store.Session, error) {
	if session := s.sessions[overseerStoreKey(name, namespace)]; session != nil {
		return session, nil
	}
	return nil, fmt.Errorf("session not found")
}

func (s *overseerTestStore) GetSession(_ context.Context, id uuid.UUID) (*store.Session, error) {
	if session := s.sessionByID(id); session != nil {
		return session, nil
	}
	return nil, fmt.Errorf("session not found")
}

func (s *overseerTestStore) GetMessages(_ context.Context, sessionID uuid.UUID) ([]store.Message, error) {
	return append([]store.Message(nil), s.messages[sessionID]...), nil
}

func (s *overseerTestStore) AppendMessage(_ context.Context, sessionID uuid.UUID, role, content string, metadata json.RawMessage) (*store.Message, error) {
	s.nextMessage++
	message := store.Message{ID: s.nextMessage, SessionID: sessionID, Role: role, Content: content, Metadata: append(json.RawMessage(nil), metadata...), CreatedAt: time.Now()}
	s.messages[sessionID] = append(s.messages[sessionID], message)
	for key, session := range s.sessions {
		if session.ID == sessionID {
			s.appendCounts[key]++
			break
		}
	}
	return &message, nil
}

func (s *overseerTestStore) SetPendingQuestion(_ context.Context, sessionID uuid.UUID, phase, question, inputType string) error {
	s.pending[sessionID] = question
	if session := s.sessionByID(sessionID); session != nil {
		session.Phase = phase
		session.PendingQuestion = question
		session.PendingActions = json.RawMessage(`[]`)
		session.PendingInputType = inputType
		session.PendingRequestID = uuid.NewString()
	}
	return nil
}

func (s *overseerTestStore) ClearPendingAction(_ context.Context, sessionID uuid.UUID, phase string) error {
	delete(s.pending, sessionID)
	if session := s.sessionByID(sessionID); session != nil {
		session.Phase = phase
		session.PendingQuestion = ""
		session.PendingActions = json.RawMessage(`[]`)
		session.PendingInputType = ""
		session.PendingRequestID = ""
	}
	return nil
}

func (s *overseerTestStore) ReservePendingInputResponse(ctx context.Context, sessionID uuid.UUID, resolution store.PendingInputResolution) (*store.Message, bool, error) {
	session := s.sessionByID(sessionID)
	if session == nil {
		return nil, false, fmt.Errorf("session not found")
	}
	if s.beforeReserve != nil {
		s.beforeReserve(session)
		s.beforeReserve = nil
	}
	var requestedMetadata map[string]any
	if err := json.Unmarshal(resolution.Metadata, &requestedMetadata); err != nil {
		return nil, false, err
	}
	deliveryID, _ := requestedMetadata["delivery_id"].(string)
	if session.PendingRequestID != resolution.RequestID {
		for i := range s.messages[sessionID] {
			var metadata map[string]any
			_ = json.Unmarshal(s.messages[sessionID][i].Metadata, &metadata)
			if metadata["delivery_id"] == deliveryID {
				message := s.messages[sessionID][i]
				return &message, true, nil
			}
		}
		return nil, false, nil
	}
	requestedMetadata["overseer_held"] = true
	encoded, _ := json.Marshal(requestedMetadata)
	message, err := s.AppendMessage(ctx, sessionID, resolution.Role, resolution.Content, encoded)
	if err != nil {
		return nil, false, err
	}
	session.Phase = resolution.Phase
	session.PendingQuestion = ""
	session.PendingActions = json.RawMessage(`[]`)
	session.PendingInputType = ""
	session.PendingRequestID = ""
	return message, true, nil
}

func (s *overseerTestStore) ReleasePendingInputResponse(_ context.Context, sessionID uuid.UUID, messageID int64, deliveryID string) error {
	if s.releaseErrOnce != nil {
		err := s.releaseErrOnce
		s.releaseErrOnce = nil
		return err
	}
	for i := range s.messages[sessionID] {
		message := &s.messages[sessionID][i]
		if message.ID != messageID {
			continue
		}
		var metadata map[string]any
		if err := json.Unmarshal(message.Metadata, &metadata); err != nil {
			return err
		}
		if metadata["delivery_id"] != deliveryID {
			return fmt.Errorf("delivery ID mismatch")
		}
		delete(metadata, "overseer_held")
		message.Metadata, _ = json.Marshal(metadata)
		return nil
	}
	return fmt.Errorf("message not found")
}

func (s *overseerTestStore) CancelPendingInputResponse(_ context.Context, sessionID uuid.UUID, messageID int64, deliveryID string) error {
	for i := range s.messages[sessionID] {
		message := &s.messages[sessionID][i]
		if message.ID != messageID {
			continue
		}
		var metadata map[string]any
		if err := json.Unmarshal(message.Metadata, &metadata); err != nil {
			return err
		}
		if metadata["delivery_id"] != deliveryID {
			return fmt.Errorf("delivery ID mismatch")
		}
		delete(metadata, "overseer_held")
		metadata["cancelled_at_unix"] = float64(1)
		message.Metadata, _ = json.Marshal(metadata)
		return nil
	}
	return fmt.Errorf("message not found")
}

func (s *overseerTestStore) ClearPendingInputIfID(_ context.Context, sessionID uuid.UUID, requestID, phase string) (bool, error) {
	session := s.sessionByID(sessionID)
	if session == nil || session.PendingRequestID != requestID {
		return false, nil
	}
	session.Phase = phase
	session.PendingQuestion = ""
	session.PendingActions = json.RawMessage(`[]`)
	session.PendingInputType = ""
	session.PendingRequestID = ""
	return true, nil
}

func (s *overseerTestStore) WriteActivityEvent(_ context.Context, sessionID uuid.UUID, eventType, summary string, detail json.RawMessage) (*store.ActivityEvent, error) {
	return &store.ActivityEvent{SessionID: sessionID, EventType: eventType, Summary: summary, Detail: detail}, nil
}

func (s *overseerTestStore) sessionByID(id uuid.UUID) *store.Session {
	for _, session := range s.sessions {
		if session.ID == id {
			return session
		}
	}
	return nil
}

func (s *overseerTestStore) MergeSessionMetadata(_ context.Context, sessionID uuid.UUID, key string, value json.RawMessage) error {
	if s.metadata[sessionID] == nil {
		s.metadata[sessionID] = map[string]json.RawMessage{}
	}
	s.metadata[sessionID][key] = append(json.RawMessage(nil), value...)
	return nil
}

func overseerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	addAgentSandboxSchemes(t, scheme)
	return scheme
}

func testOverseerMode() *platformv1alpha1.ModeTemplate {
	return &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: defaultOverseerModeName},
		Spec: platformv1alpha1.ModeTemplateSpec{
			Name: defaultOverseerModeName, Version: "v1", Category: platformv1alpha1.ModeCategoryOrchestrated,
			Autonomous: true, ExecutionStrategy: platformv1alpha1.ExecutionStrategySerial,
			PermissionMode:       platformv1alpha1.PermissionModeReadOnly,
			AllowedMutatingTools: []string{"submit_overseer_verdict"},
		},
	}
}

func testPrimaryRun(phase platformv1alpha1.AgentRunPhase, authority platformv1alpha1.AgentRunOverseerAuthority) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "primary", Namespace: "default", UID: types.UID("primary-uid")},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:      platformv1alpha1.TriggerRef{Kind: "Test", Name: "test"},
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main", BranchName: "feature"},
			WorkflowMode: platformv1alpha1.WorkflowModeAuto,
			Model:        "openai/gpt-test",
			Overseer: &platformv1alpha1.AgentRunOverseerSpec{
				Authority: authority, IntervalMinutes: 10, MaxInterventions: 5,
			},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: phase, ModeRevision: 1},
	}
}

func TestDetachOverseerWaitsForStandingRunFinalizer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := overseerTestScheme(t)
	primary := testPrimaryRun(platformv1alpha1.AgentRunPhaseRunning, platformv1alpha1.AgentRunOverseerAuthorityAdvise)
	primary.Spec.Overseer = nil
	standing := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       orchestration.StandingRunName(primary.Name, orchestration.StandingRunRoleOverseer),
			Namespace:  primary.Namespace,
			Finalizers: []string{platformv1alpha1.AgentRunCleanupFinalizer},
			Labels: map[string]string{
				orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleOverseer,
				orchestration.SupervisedRunLabel:   primary.Name,
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(primary, platformv1alpha1.GroupVersion.WithKind("AgentRun"))},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(primary, standing).Build()
	reconciler := &AgentRunOverseerReconciler{Client: k8sClient, Scheme: scheme}

	result, err := reconciler.detachOverseer(ctx, primary)
	if err != nil {
		t.Fatalf("detachOverseer() error = %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("detachOverseer() did not requeue while finalizer cleanup was pending")
	}
	freshPrimary := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), freshPrimary); err != nil {
		t.Fatal(err)
	}
	if freshPrimary.Annotations[platformv1alpha1.OverseerDetachingAnnotation] == "" || freshPrimary.Status.OverseerSummary == nil || freshPrimary.Status.OverseerSummary.State != overseerStateDetaching {
		t.Fatalf("detaching primary = %#v", freshPrimary)
	}
	freshStanding := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(standing), freshStanding); err != nil {
		t.Fatalf("standing run disappeared before finalizer cleanup: %v", err)
	}
	if freshStanding.DeletionTimestamp.IsZero() {
		t.Fatal("standing run deletion was not requested")
	}

	if _, err := reconciler.detachOverseer(ctx, freshPrimary); err != nil {
		t.Fatalf("second detachOverseer() error = %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), freshPrimary); err != nil {
		t.Fatal(err)
	}
	if freshPrimary.Status.OverseerSummary == nil || freshPrimary.Annotations[platformv1alpha1.OverseerDetachingAnnotation] == "" {
		t.Fatal("detach was acknowledged before the standing run disappeared")
	}

	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(standing), freshStanding); err != nil {
		t.Fatal(err)
	}
	freshStanding.Finalizers = nil
	if err := k8sClient.Update(ctx, freshStanding); err != nil {
		t.Fatalf("clear standing finalizer: %v", err)
	}
	if err := k8sClient.Delete(ctx, freshStanding); client.IgnoreNotFound(err) != nil {
		t.Fatalf("delete finalized standing run: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), freshPrimary); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.detachOverseer(ctx, freshPrimary); err != nil {
		t.Fatalf("final detachOverseer() error = %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), freshPrimary); err != nil {
		t.Fatal(err)
	}
	if freshPrimary.Status.OverseerSummary != nil || freshPrimary.Annotations[platformv1alpha1.OverseerDetachingAnnotation] != "" {
		t.Fatalf("completed detach state = %#v", freshPrimary)
	}
}

func TestAgentRunOverseerReconcileCreatesStandingRunOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := overseerTestScheme(t)
	primary := testPrimaryRun(platformv1alpha1.AgentRunPhaseRunning, platformv1alpha1.AgentRunOverseerAuthorityAdvise)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(primary, testOverseerMode()).Build()
	stateStore := newOverseerTestStore()
	_, _ = stateStore.CreateSession(ctx, primary.Name, primary.Namespace, "running", "auto")
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	reconciler := &AgentRunOverseerReconciler{Client: k8sClient, Scheme: scheme, StateStore: stateStore, Now: func() time.Time { return now }}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(primary)}

	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	list := &platformv1alpha1.AgentRunList{}
	if err := k8sClient.List(ctx, list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("AgentRun count = %d, want primary + one overseer", len(list.Items))
	}
	name := orchestration.StandingRunName(primary.Name, orchestration.StandingRunRoleOverseer)
	standing := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: primary.Namespace, Name: name}, standing); err != nil {
		t.Fatal(err)
	}
	if standing.Spec.Overseer != nil || standing.Spec.ModeRef == nil || standing.Spec.ModeRef.Name != defaultOverseerModeName || !metav1.IsControlledBy(standing, primary) {
		t.Fatalf("standing run is not fenced/configured correctly: %#v", standing.Spec)
	}
	if got := stateStore.appendCounts[overseerStoreKey(name, primary.Namespace)]; got != 1 {
		t.Fatalf("seed messages = %d, want 1", got)
	}
	freshPrimary := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), freshPrimary); err != nil {
		t.Fatal(err)
	}
	if freshPrimary.Status.OverseerSummary == nil || freshPrimary.Status.OverseerSummary.RunName != name {
		t.Fatalf("overseer summary = %#v", freshPrimary.Status.OverseerSummary)
	}
}

func newVerdictFixture(t *testing.T, verdict string, authority platformv1alpha1.AgentRunOverseerAuthority, primaryPhase platformv1alpha1.AgentRunPhase) (*AgentRunOverseerReconciler, *platformv1alpha1.AgentRun, *platformv1alpha1.AgentRun, client.Client, *overseerTestStore) {
	t.Helper()
	scheme := overseerTestScheme(t)
	primary := testPrimaryRun(primaryPhase, authority)
	primary.Status.OverseerSummary = &platformv1alpha1.AgentRunOverseerStatus{RunName: "primary-overseer"}
	now := time.Date(2026, 7, 11, 13, 0, 0, 0, time.UTC)
	controller := true
	standing := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: "primary-overseer", Namespace: "default", UID: types.UID("overseer-uid"),
			Labels: map[string]string{orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleOverseer, orchestration.SupervisedRunLabel: primary.Name},
			Annotations: map[string]string{
				orchestration.CheckpointSeqAnnotation:       "1",
				orchestration.CheckpointReasonAnnotation:    encodeObservation(observationFor(primary, "attached")),
				orchestration.CheckpointTimeAnnotation:      now.Format(time.RFC3339Nano),
				platformv1alpha1.OverseerVerdictAnnotation:  verdict,
				platformv1alpha1.OverseerSummaryAnnotation:  "evidence summary",
				platformv1alpha1.OverseerGuidanceAnnotation: "follow this exact guidance",
			},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: primary.Name, UID: primary.UID, Controller: &controller}},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded},
	}
	if verdict == platformv1alpha1.OverseerVerdictAllClear {
		delete(standing.Annotations, platformv1alpha1.OverseerGuidanceAnnotation)
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(primary, standing).Build()
	stateStore := newOverseerTestStore()
	_, _ = stateStore.CreateSession(context.Background(), primary.Name, primary.Namespace, "running", "auto")
	_, _ = stateStore.CreateSession(context.Background(), standing.Name, standing.Namespace, "succeeded", "done")
	reconciler := &AgentRunOverseerReconciler{Client: k8sClient, Scheme: scheme, StateStore: stateStore, Now: func() time.Time { return now }}
	return reconciler, primary, standing, k8sClient, stateStore
}

func setOverseerInputResponse(t *testing.T, standing *platformv1alpha1.AgentRun, session *store.Session, actionID, response string, run ...*platformv1alpha1.AgentRun) string {
	t.Helper()
	if strings.TrimSpace(session.PendingRequestID) == "" {
		session.PendingRequestID = uuid.NewString()
	}
	request := orchestration.PendingUserInputForSession(session)
	if len(run) > 0 {
		var err error
		request, err = pendingUserInputForRun(run[0], session)
		if err != nil {
			t.Fatal(err)
		}
	}
	if request == nil {
		t.Fatal("test session has no pending input request")
	}
	payload, err := json.Marshal(platformv1alpha1.OverseerInputResponse{
		RequestID: request.ID,
		ActionID:  actionID,
		Response:  response,
	})
	if err != nil {
		t.Fatal(err)
	}
	standing.Annotations[platformv1alpha1.OverseerInputResponseAnnotation] = string(payload)
	return request.ID
}

func TestAgentRunOverseerRoutesAllVerdicts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		verdict      string
		authority    platformv1alpha1.AgentRunOverseerAuthority
		phase        platformv1alpha1.AgentRunPhase
		completion   bool
		wantMessages int
		wantWake     int64
		wantBlocked  bool
	}{
		{name: "all clear", verdict: platformv1alpha1.OverseerVerdictAllClear, authority: platformv1alpha1.AgentRunOverseerAuthorityAdvise, phase: platformv1alpha1.AgentRunPhaseRunning},
		{name: "steer", verdict: platformv1alpha1.OverseerVerdictSteer, authority: platformv1alpha1.AgentRunOverseerAuthorityAdvise, phase: platformv1alpha1.AgentRunPhaseRunning, wantMessages: 1},
		{name: "reject completion", verdict: platformv1alpha1.OverseerVerdictRejectCompletion, authority: platformv1alpha1.AgentRunOverseerAuthorityEnforce, phase: platformv1alpha1.AgentRunPhaseSucceeded, completion: true, wantMessages: 1, wantWake: 1},
		{name: "escalate", verdict: platformv1alpha1.OverseerVerdictEscalate, authority: platformv1alpha1.AgentRunOverseerAuthorityEnforce, phase: platformv1alpha1.AgentRunPhaseRunning, wantBlocked: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, tc.verdict, tc.authority, tc.phase)
			if tc.completion {
				primary.Status.CompletionRequested = true
				if err := k8sClient.Status().Update(ctx, primary); err != nil {
					t.Fatal(err)
				}
			}
			handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing)
			if err != nil || !handled || wait {
				t.Fatalf("routeCompletedCheckpoint() = (%v, %v, %v)", handled, wait, err)
			}
			fresh := &platformv1alpha1.AgentRun{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
				t.Fatal(err)
			}
			if fresh.Status.OverseerSummary == nil || fresh.Status.OverseerSummary.CheckpointsHandled != 1 || fresh.Status.OverseerSummary.LastVerdict != tc.verdict {
				t.Fatalf("summary = %#v", fresh.Status.OverseerSummary)
			}
			if fresh.Spec.WakeRequests != tc.wantWake {
				t.Fatalf("WakeRequests = %d, want %d", fresh.Spec.WakeRequests, tc.wantWake)
			}
			primarySession := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
			if got := len(stateStore.messages[primarySession.ID]); got != tc.wantMessages {
				t.Fatalf("primary messages = %d, want %d", got, tc.wantMessages)
			}
			if tc.wantBlocked && (fresh.Status.Phase != platformv1alpha1.AgentRunPhaseBlocked || stateStore.pending[primarySession.ID] == "") {
				t.Fatalf("escalation did not block: phase=%s pending=%q", fresh.Status.Phase, stateStore.pending[primarySession.ID])
			}
		})
	}
}

func TestAgentRunOverseerWaitsForPendingWakeBeforeJudgingCheckpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, _ := newVerdictFixture(t, platformv1alpha1.OverseerVerdictEscalate, platformv1alpha1.AgentRunOverseerAuthorityAdvise, platformv1alpha1.AgentRunPhaseRunning)

	// Simulate a freshly scheduled checkpoint: the verdict annotations were
	// cleared and the sequence advanced, but the run controller has not yet
	// consumed the wake, so the standing run still reports the previous
	// episode's terminal phase.
	primary.Status.OverseerSummary.CheckpointsHandled = 1
	standing.Annotations[orchestration.CheckpointSeqAnnotation] = "2"
	delete(standing.Annotations, platformv1alpha1.OverseerVerdictAnnotation)
	delete(standing.Annotations, platformv1alpha1.OverseerSummaryAnnotation)
	delete(standing.Annotations, platformv1alpha1.OverseerGuidanceAnnotation)
	standing.Spec.WakeRequests = 1
	standing.Status.WakeRequestsHandled = 0

	handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing)
	if err != nil || handled || !wait {
		t.Fatalf("routeCompletedCheckpoint() = (%v, %v, %v), want wait for pending wake", handled, wait, err)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Status.OverseerSummary != nil && fresh.Status.OverseerSummary.State == "degraded" {
		t.Fatalf("stale terminal phase was judged fail-open: %#v", fresh.Status.OverseerSummary)
	}
}

func TestAgentRunOverseerResolvesFreeformInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictResolveInput, platformv1alpha1.AgentRunOverseerAuthorityEnforce, platformv1alpha1.AgentRunPhaseQuestion)
	session := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
	session.PendingInputType = string(platformv1alpha1.UserInputQuestion)
	session.PendingQuestion = "Which database should the service use?"
	setOverseerInputResponse(t, standing, session, "", "Use the existing PostgreSQL store.")

	handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing)
	if err != nil || !handled || wait {
		t.Fatalf("routeCompletedCheckpoint() = (%v, %v, %v)", handled, wait, err)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	messages := stateStore.messages[session.ID]
	if len(messages) != 1 || messages[0].Content != "Use the existing PostgreSQL store." {
		t.Fatalf("resolved messages = %#v", messages)
	}
	if session.PendingInputType != "" || session.PendingQuestion != "" {
		t.Fatalf("pending input was not cleared: %#v", session)
	}
	if fresh.Status.Phase != platformv1alpha1.AgentRunPhaseRunning || fresh.Status.OverseerSummary.InterventionsUsed != 1 {
		t.Fatalf("resolved run status = %#v", fresh.Status)
	}
}

func TestAgentRunOverseerReplaysReservedInputWithoutDuplicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictResolveInput, platformv1alpha1.AgentRunOverseerAuthorityEnforce, platformv1alpha1.AgentRunPhaseQuestion)
	session := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
	session.PendingInputType = string(platformv1alpha1.UserInputQuestion)
	session.PendingQuestion = "Continue?"
	setOverseerInputResponse(t, standing, session, "", "Continue with the verified approach.")
	stateStore.releaseErrOnce = fmt.Errorf("transient release failure")

	if handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing); err == nil || handled || wait {
		t.Fatalf("first route = (%v, %v, %v), want retryable error", handled, wait, err)
	}
	if session.PendingInputType != "" || len(stateStore.messages[session.ID]) != 1 {
		t.Fatalf("request was not atomically reserved: session=%#v messages=%#v", session, stateStore.messages[session.ID])
	}

	handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing)
	if err != nil || !handled || wait {
		t.Fatalf("replayed route = (%v, %v, %v)", handled, wait, err)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	if len(stateStore.messages[session.ID]) != 1 || fresh.Status.OverseerSummary.InterventionsUsed != 1 {
		t.Fatalf("replay duplicated or lost resolution: messages=%#v summary=%#v", stateStore.messages[session.ID], fresh.Status.OverseerSummary)
	}
	var metadata map[string]any
	if err := json.Unmarshal(stateStore.messages[session.ID][0].Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if _, held := metadata["overseer_held"]; held {
		t.Fatalf("replayed message remained held: %#v", metadata)
	}
}

func TestAgentRunOverseerOnlyResolvesInputWithEnforceAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictResolveInput, platformv1alpha1.AgentRunOverseerAuthorityAdvise, platformv1alpha1.AgentRunPhaseQuestion)
	session := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
	session.PendingInputType = string(platformv1alpha1.UserInputQuestion)
	session.PendingQuestion = "Choose a region"
	setOverseerInputResponse(t, standing, session, "", "Use us-east-1.")

	handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing)
	if err != nil || !handled || wait {
		t.Fatalf("routeCompletedCheckpoint() = (%v, %v, %v)", handled, wait, err)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	if len(stateStore.messages[session.ID]) != 0 || session.PendingInputType == "" {
		t.Fatalf("advise authority resolved input: session=%#v messages=%#v", session, stateStore.messages[session.ID])
	}
	if fresh.Status.OverseerSummary.State != overseerStateObserving || fresh.Status.OverseerSummary.InterventionsUsed != 0 {
		t.Fatalf("advise authority status = %#v", fresh.Status.OverseerSummary)
	}
}

func TestAgentRunOverseerRejectsStaleInputResponse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictResolveInput, platformv1alpha1.AgentRunOverseerAuthorityEnforce, platformv1alpha1.AgentRunPhaseQuestion)
	session := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
	session.PendingInputType = string(platformv1alpha1.UserInputQuestion)
	session.PendingQuestion = "First question"
	setOverseerInputResponse(t, standing, session, "", "First answer")
	session.PendingRequestID = uuid.NewString()
	// A repeated prompt is still a distinct request because the persisted nonce changed.
	session.PendingQuestion = "First question"

	handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing)
	if err != nil || !handled || wait {
		t.Fatalf("routeCompletedCheckpoint() = (%v, %v, %v)", handled, wait, err)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	if len(stateStore.messages[session.ID]) != 0 || session.PendingQuestion != "First question" {
		t.Fatalf("stale response changed live request: session=%#v messages=%#v", session, stateStore.messages[session.ID])
	}
	if fresh.Status.OverseerSummary.State != overseerStateEscalated || fresh.Status.OverseerSummary.InterventionsUsed != 0 {
		t.Fatalf("stale response status = %#v", fresh.Status.OverseerSummary)
	}
}

func TestAgentRunOverseerDoesNotClearReplacementRacingReservation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictResolveInput, platformv1alpha1.AgentRunOverseerAuthorityEnforce, platformv1alpha1.AgentRunPhaseQuestion)
	session := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
	session.PendingInputType = string(platformv1alpha1.UserInputQuestion)
	session.PendingQuestion = "Repeated question"
	setOverseerInputResponse(t, standing, session, "", "Old answer")
	newRequestID := uuid.NewString()
	stateStore.beforeReserve = func(live *store.Session) {
		live.PendingRequestID = newRequestID
		live.PendingQuestion = "Repeated question"
	}

	handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing)
	if err != nil || !handled || wait {
		t.Fatalf("routeCompletedCheckpoint() = (%v, %v, %v)", handled, wait, err)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	if session.PendingRequestID != newRequestID || session.PendingInputType == "" || len(stateStore.messages[session.ID]) != 0 {
		t.Fatalf("racing replacement was consumed: session=%#v messages=%#v", session, stateStore.messages[session.ID])
	}
	if fresh.Status.OverseerSummary.State != overseerStateEscalated || fresh.Status.OverseerSummary.InterventionsUsed != 0 {
		t.Fatalf("racing replacement status = %#v", fresh.Status.OverseerSummary)
	}
}

func TestManagedInputNeedsPlanSnapshotRefresh(t *testing.T) {
	t.Parallel()
	approved := managedInputResolutionRecord{PlanApproval: true}
	for _, tc := range []struct {
		name string
		run  *platformv1alpha1.AgentRun
		want bool
	}{
		{name: "plan", run: &platformv1alpha1.AgentRun{Status: platformv1alpha1.AgentRunStatus{ModeName: "plan"}}, want: true},
		{name: "legacy chat alias", run: &platformv1alpha1.AgentRun{Status: platformv1alpha1.AgentRunStatus{ModeName: "chat"}}, want: false},
		{name: "autopilot", run: &platformv1alpha1.AgentRun{Status: platformv1alpha1.AgentRunStatus{ModeName: "autopilot"}}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := managedInputNeedsPlanSnapshotRefresh(approved, tc.run); got != tc.want {
				t.Fatalf("managedInputNeedsPlanSnapshotRefresh() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestManagedInputIsPlanApproval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		inputType string
		actionID  string
		want      bool
	}{
		{name: "new approval", inputType: string(platformv1alpha1.UserInputPlanReview), actionID: "accept_plan", want: true},
		{name: "agent-authored approval", inputType: string(platformv1alpha1.UserInputPlanReview), actionID: "implement_pr", want: true},
		{name: "request changes", inputType: string(platformv1alpha1.UserInputPlanReview), actionID: "request_changes", want: false},
		{name: "reject", inputType: string(platformv1alpha1.UserInputPlanReview), actionID: "reject", want: false},
		{name: "legacy approval stored as question", inputType: string(platformv1alpha1.UserInputQuestion), actionID: "accept_build_auto", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := &orchestration.PendingUserAction{ID: tc.actionID}
			if got := managedInputIsPlanApproval(tc.inputType, action); got != tc.want {
				t.Fatalf("managedInputIsPlanApproval(%q, %q) = %v, want %v", tc.inputType, tc.actionID, got, tc.want)
			}
		})
	}
}

func TestAgentRunOverseerLegacyPlanApprovalStaysInCurrentMode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictResolveInput, platformv1alpha1.AgentRunOverseerAuthorityEnforce, platformv1alpha1.AgentRunPhaseQuestion)
	primary.Status.ModeName = "plan"
	primary.Status.ModeRevision = 4
	primary.Status.ModeSnapshot = &platformv1alpha1.ModeTemplateSpec{Name: "plan", Version: "v1", PermissionMode: platformv1alpha1.PermissionModeReadOnly}
	if err := k8sClient.Status().Update(ctx, primary); err != nil {
		t.Fatal(err)
	}
	planMode := &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "plan"},
		Spec:       platformv1alpha1.ModeTemplateSpec{Name: "plan", Version: "v2"},
	}
	if err := k8sClient.Create(ctx, planMode); err != nil {
		t.Fatal(err)
	}
	session := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
	// Older SDKs persisted present_plan as a question and allowed a mode target.
	// The legacy action must still be treated as in-place plan approval.
	session.PendingInputType = string(platformv1alpha1.UserInputQuestion)
	session.PendingQuestion = "Approve the implementation plan?"
	session.PendingActions = json.RawMessage(`[{"id":"accept_build_auto","label":"Build on auto mode","mode":"autopilot"},{"id":"request_changes","label":"Request changes"}]`)
	setOverseerInputResponse(t, standing, session, "accept_build_auto", "Keep the migration backwards compatible.")

	handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing)
	if err != nil || !handled || wait {
		t.Fatalf("routeCompletedCheckpoint() = (%v, %v, %v)", handled, wait, err)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	messages := stateStore.messages[session.ID]
	if fresh.Status.ModeName != "plan" || fresh.Status.ModeRevision != 5 || fresh.Status.ModeVersion != "v2" || fresh.Status.ModeSnapshot == nil || !fresh.Status.ModeSnapshot.Autonomous || fresh.Status.ModeSnapshot.PermissionMode == platformv1alpha1.PermissionModeReadOnly {
		t.Fatalf("plan mode refresh = %q revision %d version %q snapshot %#v", fresh.Status.ModeName, fresh.Status.ModeRevision, fresh.Status.ModeVersion, fresh.Status.ModeSnapshot)
	}
	if len(messages) != 1 || messages[0].Content != "Plan approved. Continue with implementation. Notes: Keep the migration backwards compatible." {
		t.Fatalf("plan approval messages = %#v", messages)
	}
	if session.PendingInputType != "" || fresh.Status.OverseerSummary.InterventionsUsed != 1 {
		t.Fatalf("plan approval did not complete: session=%#v summary=%#v", session, fresh.Status.OverseerSummary)
	}
}

func TestAgentRunOverseerPreservesAdminMediatedMCPBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictResolveInput, platformv1alpha1.AgentRunOverseerAuthorityEnforce, platformv1alpha1.AgentRunPhaseWaitingApproval)
	primary.Spec.MCPPolicyRef = &platformv1alpha1.NamedRef{Name: "locked-mcp"}
	if primary.Annotations == nil {
		primary.Annotations = map[string]string{}
	}
	request := mcppolicy.BreakGlassRequest{ID: "mcp-request-1", Server: "github", Tool: "delete_repository", Reason: "cleanup", RequestedAt: "2026-07-11T13:00:00Z"}
	if err := mcppolicy.SetPendingRequest(primary.Annotations, request); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Update(ctx, primary); err != nil {
		t.Fatal(err)
	}
	policy := &platformv1alpha1.MCPPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "locked-mcp", Namespace: primary.Namespace},
		Spec: platformv1alpha1.MCPPolicySpec{BreakGlass: &platformv1alpha1.MCPBreakGlass{
			Enabled: true, RequireAuditReason: true, AdminMediated: true,
		}},
	}
	if err := k8sClient.Create(ctx, policy); err != nil {
		t.Fatal(err)
	}
	session := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
	session.PendingInputType = string(platformv1alpha1.UserInputApproval)
	session.PendingQuestion = "Approve privileged MCP access?"
	session.PendingActions = json.RawMessage(`[{"id":"approve","label":"Approve"},{"id":"reject","label":"Reject"}]`)
	setOverseerInputResponse(t, standing, session, "approve", "Needed for cleanup.", primary)

	handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing)
	if err != nil || !handled || wait {
		t.Fatalf("routeCompletedCheckpoint() = (%v, %v, %v)", handled, wait, err)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	pending, err := mcppolicy.PendingRequest(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if pending == nil || len(stateStore.messages[session.ID]) != 0 || session.PendingInputType == "" {
		t.Fatalf("admin-mediated request was changed: pending=%#v session=%#v messages=%#v", pending, session, stateStore.messages[session.ID])
	}
	if fresh.Status.OverseerSummary.State != overseerStateEscalated || fresh.Status.OverseerSummary.InterventionsUsed != 0 {
		t.Fatalf("admin-mediated status = %#v", fresh.Status.OverseerSummary)
	}
}

func TestAgentRunOverseerRejectsCompletionAfterFinishFlagCleared(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictRejectCompletion, platformv1alpha1.AgentRunOverseerAuthorityEnforce, platformv1alpha1.AgentRunPhaseSucceeded)
	if primary.Status.CompletionRequested {
		t.Fatal("fixture must model the agent loop having cleared CompletionRequested")
	}

	handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing)
	if err != nil || !handled || wait {
		t.Fatalf("routeCompletedCheckpoint() = (%v, %v, %v)", handled, wait, err)
	}

	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	primarySession := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
	if fresh.Spec.WakeRequests != 1 || len(stateStore.messages[primarySession.ID]) != 1 {
		t.Fatalf("post-hoc rejection did not wake primary: wakeRequests=%d messages=%d", fresh.Spec.WakeRequests, len(stateStore.messages[primarySession.ID]))
	}
	if fresh.Status.OverseerSummary.CompletionRejectionsUsed != 1 || fresh.Status.OverseerSummary.InterventionsUsed != 1 {
		t.Fatalf("rejection counters = %#v", fresh.Status.OverseerSummary)
	}
}

func TestCheckpointObservedCompletionRequiresDurableCompletionEvidence(t *testing.T) {
	t.Parallel()
	standing := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		orchestration.CheckpointReasonAnnotation: encodeObservation(overseerObservation{Trigger: "cadence", Phase: platformv1alpha1.AgentRunPhaseRunning}),
	}}}
	if checkpointObservedCompletion(standing) {
		t.Fatal("running cadence checkpoint was treated as completion")
	}
	standing.Annotations[orchestration.CheckpointReasonAnnotation] = encodeObservation(overseerObservation{Trigger: "phase_transition", Phase: platformv1alpha1.AgentRunPhaseSucceeded})
	if !checkpointObservedCompletion(standing) {
		t.Fatal("succeeded phase checkpoint did not preserve completion evidence")
	}
}

func TestAgentRunOverseerSteerRespectsAuthority(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name              string
		authority         platformv1alpha1.AgentRunOverseerAuthority
		wantMessage       bool
		wantInterrupt     bool
		wantInterventions int32
	}{
		{name: "observe records only", authority: platformv1alpha1.AgentRunOverseerAuthorityObserve},
		{name: "advise delivers", authority: platformv1alpha1.AgentRunOverseerAuthorityAdvise, wantMessage: true, wantInterventions: 1},
		{name: "enforce interrupts", authority: platformv1alpha1.AgentRunOverseerAuthorityEnforce, wantMessage: true, wantInterrupt: true, wantInterventions: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictSteer, tc.authority, platformv1alpha1.AgentRunPhaseRunning)
			if handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing); err != nil || !handled || wait {
				t.Fatalf("route = (%v, %v, %v)", handled, wait, err)
			}
			fresh := &platformv1alpha1.AgentRun{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
				t.Fatal(err)
			}
			session := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
			if got := len(stateStore.messages[session.ID]) > 0; got != tc.wantMessage {
				t.Fatalf("message delivered=%v, want %v", got, tc.wantMessage)
			}
			_, interrupted := stateStore.metadata[session.ID]["interrupt"]
			if interrupted != tc.wantInterrupt || fresh.Status.OverseerSummary.InterventionsUsed != tc.wantInterventions {
				t.Fatalf("interrupt=%v interventions=%d", interrupted, fresh.Status.OverseerSummary.InterventionsUsed)
			}
		})
	}
}

func TestAgentRunOverseerCapsInterventions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictSteer, platformv1alpha1.AgentRunOverseerAuthorityAdvise, platformv1alpha1.AgentRunPhaseRunning)
	primary.Spec.Overseer.MaxInterventions = 1
	if err := k8sClient.Update(ctx, primary); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), primary); err != nil {
		t.Fatal(err)
	}
	primary.Status.OverseerSummary.InterventionsUsed = 1
	if err := k8sClient.Status().Update(ctx, primary); err != nil {
		t.Fatal(err)
	}
	handled, _, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing)
	if err != nil || !handled {
		t.Fatalf("route = (%v, %v)", handled, err)
	}
	primarySession := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
	if got := len(stateStore.messages[primarySession.ID]); got != 0 {
		t.Fatalf("capped overseer delivered %d messages", got)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Status.OverseerSummary.State != "capped" || fresh.Status.OverseerSummary.InterventionsUsed != 1 {
		t.Fatalf("summary = %#v", fresh.Status.OverseerSummary)
	}
}

func TestAgentRunOverseerCapsThirdCompletionRejection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictRejectCompletion, platformv1alpha1.AgentRunOverseerAuthorityEnforce, platformv1alpha1.AgentRunPhaseSucceeded)
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), primary); err != nil {
		t.Fatal(err)
	}
	primary.Status.CompletionRequested = true
	primary.Status.OverseerSummary.InterventionsUsed = 2
	primary.Status.OverseerSummary.CompletionRejectionsUsed = maxCompletionRejections
	if err := k8sClient.Status().Update(ctx, primary); err != nil {
		t.Fatal(err)
	}
	handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing)
	if err != nil || !handled || wait {
		t.Fatalf("route = (%v, %v, %v)", handled, wait, err)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	primarySession := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
	if fresh.Spec.WakeRequests != 0 || len(stateStore.messages[primarySession.ID]) != 0 || fresh.Status.OverseerSummary.State != "capped" || fresh.Status.OverseerSummary.CompletionRejectionsUsed != maxCompletionRejections {
		t.Fatalf("capped rejection state: run=%#v messages=%#v", fresh, stateStore.messages[primarySession.ID])
	}
}

func TestOverseerRejectionWakeClearsCompletionThroughAgentRunController(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, _ := newVerdictFixture(t, platformv1alpha1.OverseerVerdictRejectCompletion, platformv1alpha1.AgentRunOverseerAuthorityEnforce, platformv1alpha1.AgentRunPhaseSucceeded)
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), primary); err != nil {
		t.Fatal(err)
	}
	completed := metav1.Now()
	primary.Status.CompletionRequested = true
	primary.Status.CompletedAt = &completed
	if err := k8sClient.Status().Update(ctx, primary); err != nil {
		t.Fatal(err)
	}
	if handled, wait, err := reconciler.routeCompletedCheckpoint(ctx, primary, standing); err != nil || !handled || wait {
		t.Fatalf("route = (%v, %v, %v)", handled, wait, err)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	runReconciler := &AgentRunReconciler{Client: k8sClient}
	handled, err := runReconciler.handleWakeRequest(ctx, fresh)
	if err != nil || !handled {
		t.Fatalf("handleWakeRequest() = (%v, %v)", handled, err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Status.CompletionRequested || fresh.Status.CompletedAt != nil || fresh.Status.Phase != platformv1alpha1.AgentRunPhasePending || fresh.Status.WakeRequestsHandled != 1 {
		t.Fatalf("wake did not clear completion: %#v", fresh.Status)
	}
}

func TestAgentRunOverseerStopsWhenPrimaryIsCancelled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, _ := newVerdictFixture(t, platformv1alpha1.OverseerVerdictAllClear, platformv1alpha1.AgentRunOverseerAuthorityAdvise, platformv1alpha1.AgentRunPhaseCancelled)
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(primary)}); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(standing), &platformv1alpha1.AgentRun{}); err == nil {
		t.Fatal("standing overseer still exists after primary cancellation")
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Status.OverseerSummary == nil || fresh.Status.OverseerSummary.State != "cancelled" {
		t.Fatalf("summary = %#v", fresh.Status.OverseerSummary)
	}
}

func TestOverseerBoundsDefendAgainstInvalidStoredValues(t *testing.T) {
	t.Parallel()
	run := testPrimaryRun(platformv1alpha1.AgentRunPhaseRunning, platformv1alpha1.AgentRunOverseerAuthorityAdvise)
	run.Spec.Overseer.IntervalMinutes = 1<<31 - 1
	run.Spec.Overseer.MaxInterventions = 1<<31 - 1
	if got := overseerInterval(run); got != defaultOverseerInterval {
		t.Fatalf("overseerInterval() = %s, want default %s", got, defaultOverseerInterval)
	}
	if got := overseerMaxInterventions(run); got != platformv1alpha1.AgentRunOverseerMaxInterventions {
		t.Fatalf("overseerMaxInterventions() = %d, want %d", got, platformv1alpha1.AgentRunOverseerMaxInterventions)
	}

	run.Spec.Overseer.IntervalMinutes = platformv1alpha1.AgentRunOverseerMaxIntervalMinutes
	if got := overseerInterval(run); got != 24*time.Hour {
		t.Fatalf("overseerInterval(max) = %s, want 24h", got)
	}
}

func TestMinimalOverseerSecretsPreserveLegacyAPIKeyFallback(t *testing.T) {
	t.Parallel()
	in := &platformv1alpha1.AgentRunSecrets{
		ClaudeAPIKeySecret: "legacy-model-key",
		GitHubTokenSecret:  "github",
		SlackTokensSecret:  "slack",
	}
	for _, provider := range []string{"openai", "gemini", "openrouter", "groq", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			got := minimalOverseerSecrets(in, provider+"/test-model", platformv1alpha1.AgentRunAuthModeAPIKey)
			if got.ClaudeAPIKeySecret != "legacy-model-key" || got.GitHubTokenSecret != "github" || got.SlackTokensSecret != "" {
				t.Fatalf("minimal %s API-key secrets = %#v", provider, got)
			}
		})
	}
}

func TestMinimalOverseerSecretsPreferProviderScopedCredentials(t *testing.T) {
	t.Parallel()
	in := &platformv1alpha1.AgentRunSecrets{
		ClaudeAPIKeySecret: "legacy-model-key", OpenAIOAuthSecret: "openai-oauth", GitHubTokenSecret: "github", SlackTokensSecret: "slack",
		ProviderKeys:         []platformv1alpha1.ProviderKeyRef{{Provider: "openai", SecretName: "openai-key"}, {Provider: "anthropic", SecretName: "anthropic-key"}},
		ProviderOAuthSecrets: []platformv1alpha1.ProviderOAuthSecretRef{{Provider: "openai", SecretName: "openai-extra"}, {Provider: "copilot", SecretName: "copilot-extra"}},
	}
	got := minimalOverseerSecrets(in, "openai/gpt-test", platformv1alpha1.AgentRunAuthModeAPIKey)
	if got.ClaudeAPIKeySecret != "" || got.OpenAIOAuthSecret != "" || len(got.ProviderKeys) != 1 || got.ProviderKeys[0].Provider != "openai" || len(got.ProviderOAuthSecrets) != 0 {
		t.Fatalf("minimal OpenAI API-key secrets = %#v", got)
	}

	got = minimalOverseerSecrets(in, "openai/gpt-test", platformv1alpha1.AgentRunAuthModeOAuth)
	if got.ClaudeAPIKeySecret != "" || got.OpenAIOAuthSecret != "openai-oauth" || len(got.ProviderKeys) != 0 || len(got.ProviderOAuthSecrets) != 1 || got.ProviderOAuthSecrets[0].Provider != "openai" {
		t.Fatalf("minimal OpenAI OAuth secrets = %#v", got)
	}
}

func TestValidateOverseerModeRejectsUnsafeTemplate(t *testing.T) {
	t.Parallel()
	scheme := overseerTestScheme(t)
	unsafe := testOverseerMode()
	unsafe.Name = "unsafe-overseer"
	unsafe.Spec.Name = unsafe.Name
	unsafe.Spec.PermissionMode = platformv1alpha1.PermissionModeWorkspaceWrite
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(unsafe).Build()
	reconciler := &AgentRunOverseerReconciler{Client: k8sClient}
	if err := reconciler.validateOverseerMode(context.Background(), &platformv1alpha1.ModeRef{Name: unsafe.Name}); err == nil {
		t.Fatal("unsafe overseer ModeTemplate was accepted")
	}
}

func TestAgentRunOverseerSchedulesCheckpointTriggers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		trigger string
		advance time.Duration
		mutate  func(*platformv1alpha1.AgentRun)
	}{
		{name: "completion", trigger: "completion_requested", mutate: func(run *platformv1alpha1.AgentRun) { run.Status.CompletionRequested = true }},
		{name: "mode", trigger: "mode_transition", mutate: func(run *platformv1alpha1.AgentRun) { run.Status.ModeRevision++ }},
		{name: "budget 50", trigger: "budget_50_percent", mutate: func(run *platformv1alpha1.AgentRun) {
			run.Spec.Limits = &platformv1alpha1.AgentRunLimits{MaxCostUsd: "10"}
			run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{CostUsd: "5"}
		}},
		{name: "budget 90", trigger: "budget_90_percent", mutate: func(run *platformv1alpha1.AgentRun) {
			run.Spec.Limits = &platformv1alpha1.AgentRunLimits{MaxCostUsd: "10"}
			run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{CostUsd: "9"}
		}},
		{name: "cadence", trigger: "cadence", advance: 10 * time.Minute, mutate: func(*platformv1alpha1.AgentRun) {}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			reconciler, primary, standing, k8sClient, _ := newVerdictFixture(t, platformv1alpha1.OverseerVerdictAllClear, platformv1alpha1.AgentRunOverseerAuthorityAdvise, platformv1alpha1.AgentRunPhaseRunning)
			primary.Status.OverseerSummary.CheckpointsHandled = 1
			standing.Annotations[orchestration.CheckpointHandledAnnotation] = "1"
			tc.mutate(primary)
			result, err := reconciler.maybeScheduleCheckpoint(ctx, primary, standing, reconciler.now().Add(tc.advance))
			if err != nil || result.RequeueAfter == 0 {
				t.Fatalf("maybeScheduleCheckpoint() = (%#v, %v)", result, err)
			}
			fresh := &platformv1alpha1.AgentRun{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(standing), fresh); err != nil {
				t.Fatal(err)
			}
			observation, ok := decodeObservation(fresh.Annotations[orchestration.CheckpointReasonAnnotation])
			if !ok || observation.Trigger != tc.trigger {
				t.Fatalf("trigger = %#v, ok=%v, want %q", observation, ok, tc.trigger)
			}
		})
	}
}

func TestAgentRunOverseerSchedulesPhaseCheckpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictAllClear, platformv1alpha1.AgentRunOverseerAuthorityAdvise, platformv1alpha1.AgentRunPhaseRunning)
	primary.Status.OverseerSummary.CheckpointsHandled = 1
	primary.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	standing.Annotations[orchestration.CheckpointHandledAnnotation] = "1"
	if err := k8sClient.Status().Update(ctx, primary); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Update(ctx, standing); err != nil {
		t.Fatal(err)
	}
	result, err := reconciler.maybeScheduleCheckpoint(ctx, primary, standing, reconciler.now())
	if err != nil {
		t.Fatalf("maybeScheduleCheckpoint() error = %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected follow-up requeue")
	}
	freshStanding := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(standing), freshStanding); err != nil {
		t.Fatal(err)
	}
	if freshStanding.Spec.WakeRequests != 1 || freshStanding.Annotations[orchestration.CheckpointSeqAnnotation] != "2" {
		t.Fatalf("standing checkpoint state: wake=%d annotations=%#v", freshStanding.Spec.WakeRequests, freshStanding.Annotations)
	}
	observation, ok := decodeObservation(freshStanding.Annotations[orchestration.CheckpointReasonAnnotation])
	if !ok || observation.Trigger != "phase_transition" || observation.Phase != platformv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("checkpoint observation = %#v, ok=%v", observation, ok)
	}
	standingSession := stateStore.sessions[overseerStoreKey(standing.Name, standing.Namespace)]
	if got := len(stateStore.messages[standingSession.ID]); got != 1 {
		t.Fatalf("checkpoint messages = %d, want 1", got)
	}
}

func TestAgentRunOverseerSchedulesInputCheckpointWithoutPhaseChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reconciler, primary, standing, k8sClient, stateStore := newVerdictFixture(t, platformv1alpha1.OverseerVerdictAllClear, platformv1alpha1.AgentRunOverseerAuthorityEnforce, platformv1alpha1.AgentRunPhaseRunning)
	primary.Status.OverseerSummary.CheckpointsHandled = 1
	standing.Annotations[orchestration.CheckpointHandledAnnotation] = "1"
	if err := k8sClient.Update(ctx, standing); err != nil {
		t.Fatal(err)
	}
	session := stateStore.sessions[overseerStoreKey(primary.Name, primary.Namespace)]
	session.PendingRequestID = uuid.NewString()
	session.PendingInputType = string(platformv1alpha1.UserInputIdle)
	session.PendingQuestion = ""
	request := orchestration.PendingUserInputForSession(session)

	result, err := reconciler.maybeScheduleCheckpoint(ctx, primary, standing, reconciler.now())
	if err != nil {
		t.Fatalf("maybeScheduleCheckpoint() error = %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected follow-up requeue")
	}
	freshStanding := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(standing), freshStanding); err != nil {
		t.Fatal(err)
	}
	observation, ok := decodeObservation(freshStanding.Annotations[orchestration.CheckpointReasonAnnotation])
	if !ok || observation.Trigger != "input_requested" || observation.InputRequestID != request.ID {
		t.Fatalf("input checkpoint observation = %#v, ok=%v", observation, ok)
	}
	standingSession := stateStore.sessions[overseerStoreKey(standing.Name, standing.Namespace)]
	messages := stateStore.messages[standingSession.ID]
	if len(messages) != 1 || !strings.Contains(messages[0].Content, request.ID) {
		t.Fatalf("input checkpoint message = %#v", messages)
	}
}
