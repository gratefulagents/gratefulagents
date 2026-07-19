package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/jackc/pgx/v5"
)

type planToolStore struct {
	artifacts         map[string]*store.Artifact
	upsertArtifactErr error
	getArtifactErr    error
}

func newPlanToolStore() *planToolStore {
	return &planToolStore{artifacts: make(map[string]*store.Artifact)}
}

func (s *planToolStore) CreateSession(context.Context, string, string, string, string) (*store.Session, error) {
	return nil, nil
}
func (s *planToolStore) GetSession(context.Context, uuid.UUID) (*store.Session, error) {
	return nil, nil
}
func (s *planToolStore) GetSessionByRun(context.Context, string, string) (*store.Session, error) {
	return nil, nil
}
func (s *planToolStore) ListSessionsByNamespace(context.Context, string) ([]store.Session, error) {
	return nil, nil
}
func (s *planToolStore) UpdatePhase(context.Context, uuid.UUID, string, string) error {
	return nil
}
func (s *planToolStore) SetPendingQuestion(context.Context, uuid.UUID, string, string, string) error {
	return nil
}
func (s *planToolStore) ClearPendingQuestion(context.Context, uuid.UUID, string) error { return nil }
func (s *planToolStore) SetPendingAction(context.Context, uuid.UUID, string, string, json.RawMessage, string) error {
	return nil
}
func (s *planToolStore) ClearPendingAction(context.Context, uuid.UUID, string) error { return nil }
func (s *planToolStore) UpdateMetadata(context.Context, uuid.UUID, json.RawMessage) error {
	return nil
}
func (s *planToolStore) MergeSessionMetadata(context.Context, uuid.UUID, string, json.RawMessage) error {
	return nil
}
func (s *planToolStore) ListAllSessionMetrics(context.Context) ([]store.SessionMetricsEntry, error) {
	return nil, nil
}
func (s *planToolStore) DeleteAgentRunData(context.Context, string, string, string) error {
	return nil
}
func (s *planToolStore) AppendMessage(context.Context, uuid.UUID, string, string, json.RawMessage) (*store.Message, error) {
	return nil, nil
}
func (s *planToolStore) GetMessages(context.Context, uuid.UUID) ([]store.Message, error) {
	return nil, nil
}
func (s *planToolStore) GetMessagesIncludingCancelled(ctx context.Context, sessionID uuid.UUID) ([]store.Message, error) {
	return s.GetMessages(ctx, sessionID)
}
func (s *planToolStore) GetMessagesSince(context.Context, uuid.UUID, int64) ([]store.Message, error) {
	return nil, nil
}
func (s *planToolStore) PollNewUserMessages(context.Context, uuid.UUID, int64) ([]store.Message, error) {
	return nil, nil
}
func (s *planToolStore) MarkMessagesDelivered(context.Context, uuid.UUID, []int64) error {
	return nil
}

func (s *planToolStore) CancelUndeliveredUserMessage(context.Context, uuid.UUID, int64) error {
	return nil
}
func (s *planToolStore) UpsertSessionTranscript(context.Context, uuid.UUID, []byte, int32) error {
	return nil
}
func (s *planToolStore) GetSessionTranscript(context.Context, uuid.UUID) ([]byte, error) {
	return nil, nil
}
func (s *planToolStore) DeleteSessionTranscript(context.Context, uuid.UUID) error {
	return nil
}
func (s *planToolStore) WriteActivityEvent(context.Context, uuid.UUID, string, string, json.RawMessage) (*store.ActivityEvent, error) {
	return nil, nil
}
func (s *planToolStore) GetRecentActivity(context.Context, uuid.UUID, int32) ([]store.ActivityEvent, error) {
	return nil, nil
}
func (s *planToolStore) GetAllActivity(context.Context, uuid.UUID) ([]store.ActivityEvent, error) {
	return nil, nil
}
func (s *planToolStore) GetActivityEventsSince(context.Context, uuid.UUID, int64) ([]store.ActivityEvent, error) {
	return nil, nil
}
func (s *planToolStore) GetSessionFingerprint(context.Context, uuid.UUID) (string, error) {
	return "", nil
}
func (s *planToolStore) UpsertArtifact(_ context.Context, sessionID uuid.UUID, kind, content, s3URL, contentHash string, metadata json.RawMessage) (*store.Artifact, error) {
	if s.upsertArtifactErr != nil {
		return nil, s.upsertArtifactErr
	}
	art := &store.Artifact{
		ID:          uuid.New(),
		SessionID:   sessionID,
		Kind:        kind,
		Content:     content,
		S3URL:       s3URL,
		ContentHash: contentHash,
		Metadata:    append(json.RawMessage(nil), metadata...),
	}
	s.artifacts[kind] = art
	return art, nil
}
func (s *planToolStore) GetArtifact(_ context.Context, _ uuid.UUID, kind string) (*store.Artifact, error) {
	if s.getArtifactErr != nil {
		return nil, s.getArtifactErr
	}
	art, ok := s.artifacts[kind]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return art, nil
}
func (s *planToolStore) GetArtifacts(context.Context, uuid.UUID) ([]store.Artifact, error) {
	return nil, nil
}
func (s *planToolStore) SetResourceOwner(context.Context, string, string, string, string) error {
	return nil
}
func (s *planToolStore) GetResourceOwner(context.Context, string, string, string) (*store.ResourceOwnership, error) {
	return nil, nil
}
func (s *planToolStore) ListOwnedResources(context.Context, string, string) ([]store.ResourceOwnership, error) {
	return nil, nil
}
func (s *planToolStore) ShareResource(context.Context, *store.ResourceShare) (*store.ResourceShare, error) {
	return nil, nil
}
func (s *planToolStore) RevokeShare(context.Context, string) error                   { return nil }
func (s *planToolStore) UpdateSharePermission(context.Context, string, string) error { return nil }
func (s *planToolStore) ListSharesForResource(context.Context, string, string, string) ([]store.ResourceShare, error) {
	return nil, nil
}
func (s *planToolStore) ListSharedWithMe(context.Context, string, string) ([]store.ResourceShare, error) {
	return nil, nil
}
func (s *planToolStore) GetSharePermission(context.Context, string, string, string, string) (*store.ResourceShare, error) {
	return nil, nil
}
func (s *planToolStore) CreateNotification(context.Context, *store.Notification) error { return nil }
func (s *planToolStore) HasUnreadNotification(context.Context, string, string, string, string) (bool, error) {
	return false, nil
}
func (s *planToolStore) ListNotifications(context.Context, string, bool, int32) ([]store.Notification, error) {
	return nil, nil
}
func (s *planToolStore) MarkNotificationRead(context.Context, string) error     { return nil }
func (s *planToolStore) MarkAllNotificationsRead(context.Context, string) error { return nil }
func (s *planToolStore) GetUnreadNotificationCount(context.Context, string) (int32, error) {
	return 0, nil
}
func (s *planToolStore) Close() error { return nil }

func TestSavePlanAndGetPlanUsePostgresArtifact(t *testing.T) {
	t.Parallel()

	stateStore := newPlanToolStore()
	sessionID := uuid.New()
	registry := NewRegistry(t.TempDir())
	RegisterPlanTools(registry, stateStore, sessionID)
	saveTool := registry.Get("save_plan")
	getTool := registry.Get("get_plan")
	if saveTool == nil || getTool == nil {
		t.Fatalf("plan tools were not registered")
	}

	input, _ := json.Marshal(map[string]string{
		"plan":    "# Plan\n\n1. Do the thing.",
		"summary": "Short summary",
	})

	saveResult, err := saveTool.Execute(context.Background(), input, t.TempDir())
	if err != nil {
		t.Fatalf("save_plan Execute() error = %v", err)
	}
	if saveResult.IsError {
		t.Fatalf("save_plan returned error result: %s", saveResult.Content)
	}

	art, err := stateStore.GetArtifact(context.Background(), sessionID, "plan")
	if err != nil {
		t.Fatalf("GetArtifact() error = %v", err)
	}
	if art.Content != "# Plan\n\n1. Do the thing." {
		t.Fatalf("artifact content = %q", art.Content)
	}
	var meta map[string]string
	if err := json.Unmarshal(art.Metadata, &meta); err != nil {
		t.Fatalf("json.Unmarshal(metadata) error = %v", err)
	}
	if meta["summary"] != "Short summary" {
		t.Fatalf("metadata summary = %q, want %q", meta["summary"], "Short summary")
	}

	getResult, err := getTool.Execute(context.Background(), json.RawMessage(`{}`), t.TempDir())
	if err != nil {
		t.Fatalf("get_plan Execute() error = %v", err)
	}
	if getResult.IsError {
		t.Fatalf("get_plan returned error result: %s", getResult.Content)
	}
	if getResult.Content != art.Content {
		t.Fatalf("get_plan content = %q, want %q", getResult.Content, art.Content)
	}
}

func TestGetPlanReturnsNoPlanFoundWhenArtifactMissing(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(t.TempDir())
	RegisterPlanTools(registry, newPlanToolStore(), uuid.New())
	tool := registry.Get("get_plan")
	if tool == nil {
		t.Fatalf("get_plan was not registered")
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`), t.TempDir())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("expected non-error result, got: %s", result.Content)
	}
	if result.Content != "No plan found. Use save_plan to create one first." {
		t.Fatalf("Content = %q", result.Content)
	}
}

func TestSavePlanRequiresStateStore(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(t.TempDir())
	RegisterPlanTools(registry, nil, uuid.New())
	tool := registry.Get("save_plan")
	if tool == nil {
		t.Fatalf("save_plan was not registered")
	}
	input, _ := json.Marshal(map[string]string{"plan": "# Plan"})
	result, err := tool.Execute(context.Background(), input, t.TempDir())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true when state store is missing")
	}
}
