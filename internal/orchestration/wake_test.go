package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type wakeTestStore struct {
	store.StateStore
	session         *store.Session
	appendSessionID uuid.UUID
	appendRole      string
	appendContent   string
	appendMetadata  json.RawMessage
	appendCount     int
	appendErr       error
	messages        []store.Message
}

func (s *wakeTestStore) GetSessionByRun(ctx context.Context, agentRunName, agentRunNS string) (*store.Session, error) {
	return s.session, nil
}

func (s *wakeTestStore) AppendMessage(ctx context.Context, sessionID uuid.UUID, role, content string, metadata json.RawMessage) (*store.Message, error) {
	if s.appendErr != nil {
		return nil, s.appendErr
	}
	s.appendSessionID = sessionID
	s.appendRole = role
	s.appendContent = content
	s.appendMetadata = append(json.RawMessage(nil), metadata...)
	s.appendCount++
	message := store.Message{ID: int64(s.appendCount), SessionID: sessionID, Role: role, Content: content, Metadata: append(json.RawMessage(nil), metadata...)}
	s.messages = append(s.messages, message)
	return &message, nil
}

func (s *wakeTestStore) GetMessages(context.Context, uuid.UUID) ([]store.Message, error) {
	return append([]store.Message(nil), s.messages...), nil
}

func TestCheckpointCoalescesAndIncrementsCounter(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "standing", Namespace: "default", Annotations: map[string]string{
			platformv1alpha1.OverseerVerdictAnnotation: "continue", platformv1alpha1.OverseerGuidanceAnnotation: "old", platformv1alpha1.OverseerSummaryAnnotation: "old",
		}},
		Spec:   platformv1alpha1.AgentRunSpec{WakeRequests: 4},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	stateStore := &wakeTestStore{session: &store.Session{ID: uuid.New()}}
	key := client.ObjectKeyFromObject(run)

	scheduled, err := Checkpoint(context.Background(), k8sClient, stateStore, key, 12, "periodic", "Inspect current progress.")
	if err != nil || !scheduled {
		t.Fatalf("Checkpoint() = (%v, %v), want (true, nil)", scheduled, err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), key, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.WakeRequests != 5 || updated.Annotations[CheckpointSeqAnnotation] != "12" || stateStore.appendCount != 1 {
		t.Fatalf("checkpoint state = counter %d annotations %#v appends %d", updated.Spec.WakeRequests, updated.Annotations, stateStore.appendCount)
	}
	if _, ok := updated.Annotations[platformv1alpha1.OverseerVerdictAnnotation]; ok {
		t.Fatalf("old overseer annotations were not cleared: %#v", updated.Annotations)
	}

	scheduled, err = Checkpoint(context.Background(), k8sClient, stateStore, key, 12, "periodic", "Inspect current progress.")
	if err != nil || scheduled || stateStore.appendCount != 1 {
		t.Fatalf("coalesced Checkpoint() = (%v, %v), appends %d", scheduled, err, stateStore.appendCount)
	}
}

func TestCheckpointRepairsAfterMessageWasAlreadyDelivered(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "standing-repair", Namespace: "default"},
		Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	sessionID := uuid.New()
	metadata := json.RawMessage(`{"mode":"enqueue","checkpoint_seq":3}`)
	stateStore := &wakeTestStore{
		session:     &store.Session{ID: sessionID},
		messages:    []store.Message{{ID: 1, SessionID: sessionID, Role: "user", Content: "checkpoint", Metadata: metadata}},
		appendCount: 1,
	}
	scheduled, err := Checkpoint(context.Background(), k8sClient, stateStore, client.ObjectKeyFromObject(run), 3, "repair", "checkpoint")
	if err != nil || !scheduled {
		t.Fatalf("Checkpoint() = (%v, %v)", scheduled, err)
	}
	if stateStore.appendCount != 1 {
		t.Fatalf("checkpoint message was duplicated: appends=%d", stateStore.appendCount)
	}
}

func TestDeliverImmediateMessageOnceIsIdempotent(t *testing.T) {
	t.Parallel()
	stateStore := &wakeTestStore{session: &store.Session{ID: uuid.New()}}
	for range 2 {
		if err := DeliverImmediateMessageOnce(context.Background(), stateStore, "default", "primary", "guidance", "overseer", "delivery-1"); err != nil {
			t.Fatal(err)
		}
	}
	if stateStore.appendCount != 1 {
		t.Fatalf("appends = %d, want 1", stateStore.appendCount)
	}
}

func TestWakeAgentRunOnceIsIdempotent(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "primary", Namespace: "default"}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	stateStore := &wakeTestStore{session: &store.Session{ID: uuid.New()}}
	for range 2 {
		if err := WakeAgentRunOnce(context.Background(), k8sClient, stateStore, run.Namespace, run.Name, "resume", "rejection-1"); err != nil {
			t.Fatal(err)
		}
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatal(err)
	}
	if stateStore.appendCount != 1 || updated.Spec.WakeRequests != 1 || updated.Annotations[LastWakeDeliveryAnnotation] != "rejection-1" {
		t.Fatalf("appends=%d wakeRequests=%d annotations=%#v", stateStore.appendCount, updated.Spec.WakeRequests, updated.Annotations)
	}
}

func TestNudgeAgentRunSessionOnceDeliversWithoutProvisioningRunner(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "primary", Namespace: "default"}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	stateStore := &wakeTestStore{session: &store.Session{ID: uuid.New()}}
	for range 2 {
		if err := NudgeAgentRunSessionOnce(context.Background(), k8sClient, stateStore, run.Namespace, run.Name, "resume", "nudge-1"); err != nil {
			t.Fatal(err)
		}
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatal(err)
	}
	if stateStore.appendCount != 1 || updated.Spec.WakeRequests != 0 || updated.Annotations[LastWakeDeliveryAnnotation] != "nudge-1" {
		t.Fatalf("appends=%d wakeRequests=%d annotations=%#v", stateStore.appendCount, updated.Spec.WakeRequests, updated.Annotations)
	}
}

func TestDeliverImmediateMessageMetadata(t *testing.T) {
	t.Parallel()
	stateStore := &wakeTestStore{session: &store.Session{ID: uuid.New()}}
	if err := DeliverImmediateMessage(context.Background(), stateStore, "default", "standing", "New event", "github/webhook"); err != nil {
		t.Fatalf("DeliverImmediateMessage() error = %v", err)
	}
	var metadata map[string]string
	if err := json.Unmarshal(stateStore.appendMetadata, &metadata); err != nil {
		t.Fatalf("metadata is invalid JSON: %v", err)
	}
	if metadata["mode"] != "immediate" || metadata["source"] != "github/webhook" {
		t.Fatalf("metadata = %#v, want immediate mode and source", metadata)
	}
}

func TestWakeAgentRunFromPhasesRejectsIneligibleRunWithoutMessage(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "active-run", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WakeRequests: 2},
		Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	stateStore := &wakeTestStore{session: &store.Session{ID: uuid.New()}}

	err := WakeAgentRunFromPhases(context.Background(), k8sClient, stateStore, "default", "active-run", "Resume.", platformv1alpha1.AgentRunPhaseCancelled)
	if err == nil {
		t.Fatal("WakeAgentRunFromPhases() error = nil, want phase rejection")
	}
	if stateStore.appendCount != 0 {
		t.Fatalf("appended messages = %d, want 0", stateStore.appendCount)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Spec.WakeRequests != 2 {
		t.Fatalf("WakeRequests = %d, want 2", updated.Spec.WakeRequests)
	}
}

func TestWakeAgentRunDoesNotPublishCounterWhenMessageAppendFails(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "stopped-run", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WakeRequests: 2},
		Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseCancelled},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	appendErr := errors.New("database unavailable")
	stateStore := &wakeTestStore{session: &store.Session{ID: uuid.New()}, appendErr: appendErr}

	err := WakeAgentRunFromPhases(context.Background(), k8sClient, stateStore, "default", "stopped-run", "Resume.", platformv1alpha1.AgentRunPhaseCancelled)
	if !errors.Is(err, appendErr) {
		t.Fatalf("WakeAgentRunFromPhases() error = %v, want append error", err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Spec.WakeRequests != 2 {
		t.Fatalf("WakeRequests = %d, want 2", updated.Spec.WakeRequests)
	}
}

func TestWakeAgentRunAppendsMessageAndIncrementsCounter(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add platform scheme: %v", err)
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "review-run", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WakeRequests: 2},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		Build()
	sessionID := uuid.New()
	stateStore := &wakeTestStore{session: &store.Session{ID: sessionID, AgentRunName: "review-run", AgentRunNS: "default"}}

	if err := WakeAgentRun(context.Background(), k8sClient, stateStore, "default", "review-run", "Please address this review."); err != nil {
		t.Fatalf("WakeAgentRun() error = %v", err)
	}

	if stateStore.appendSessionID != sessionID || stateStore.appendRole != "user" || stateStore.appendContent != "Please address this review." {
		t.Fatalf("appended message = (%s, %q, %q), want (%s, user, context)", stateStore.appendSessionID, stateStore.appendRole, stateStore.appendContent, sessionID)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "review-run", Namespace: "default"}, updated); err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if updated.Spec.WakeRequests != 3 {
		t.Fatalf("WakeRequests = %d, want 3", updated.Spec.WakeRequests)
	}
}
