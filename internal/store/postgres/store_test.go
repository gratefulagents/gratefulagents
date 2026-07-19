package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/store"
	pgstore "github.com/gratefulagents/gratefulagents/internal/store/postgres"
)

func setupTestStore(t *testing.T) store.StateStore {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to test db: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("running migrations: %v", err)
	}

	// Clean tables for test isolation.
	for _, table := range []string{"agent_run_wake_intents", "session_interrupts", "agent_artifacts", "activity_events", "conversation_messages", "agent_sessions"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("cleaning table %s: %v", table, err)
		}
	}

	return pgstore.NewFromPool(pool)
}

func TestSessionLifecycle(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	ctx := context.Background()

	sess, err := s.CreateSession(ctx, "test-run", "default", "pending", "setup")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.AgentRunName != "test-run" {
		t.Errorf("AgentRunName = %q, want %q", sess.AgentRunName, "test-run")
	}
	if sess.Phase != "pending" {
		t.Errorf("Phase = %q, want %q", sess.Phase, "pending")
	}

	got, err := s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != sess.ID {
		t.Errorf("GetSession ID mismatch")
	}

	gotByRun, err := s.GetSessionByRun(ctx, "test-run", "default")
	if err != nil {
		t.Fatalf("GetSessionByRun: %v", err)
	}
	if gotByRun.ID != sess.ID {
		t.Errorf("GetSessionByRun ID mismatch")
	}

	if err := s.UpdatePhase(ctx, sess.ID, "running", "exploring"); err != nil {
		t.Fatalf("UpdatePhase: %v", err)
	}
	updated, _ := s.GetSession(ctx, sess.ID)
	if updated.Phase != "running" || updated.CurrentStep != "exploring" {
		t.Errorf("after UpdatePhase: phase=%q currentStep=%q", updated.Phase, updated.CurrentStep)
	}

	if err := s.SetPendingQuestion(ctx, sess.ID, "question", "What should I do?", "question"); err != nil {
		t.Fatalf("SetPendingQuestion: %v", err)
	}
	updated, _ = s.GetSession(ctx, sess.ID)
	if updated.PendingQuestion != "What should I do?" || updated.Phase != "question" || updated.PendingRequestID == "" {
		t.Errorf("after SetPendingQuestion: question=%q phase=%q requestID=%q", updated.PendingQuestion, updated.Phase, updated.PendingRequestID)
	}
	firstRequestID := updated.PendingRequestID
	if err := s.SetPendingQuestion(ctx, sess.ID, "question", "What should I do?", "question"); err != nil {
		t.Fatalf("replace SetPendingQuestion: %v", err)
	}
	updated, _ = s.GetSession(ctx, sess.ID)
	if updated.PendingRequestID == firstRequestID {
		t.Fatalf("replacement request reused ID %q", firstRequestID)
	}

	if err := s.ClearPendingQuestion(ctx, sess.ID, "running"); err != nil {
		t.Fatalf("ClearPendingQuestion: %v", err)
	}
	updated, _ = s.GetSession(ctx, sess.ID)
	if updated.PendingQuestion != "" || updated.Phase != "running" || updated.PendingRequestID != "" {
		t.Errorf("after ClearPendingQuestion: question=%q phase=%q requestID=%q", updated.PendingQuestion, updated.Phase, updated.PendingRequestID)
	}
}

func TestPendingRequestIDTriggerSupportsLegacyWriters(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	ctx := context.Background()
	postgresStore, ok := s.(*pgstore.Store)
	if !ok {
		t.Fatal("test store is not Postgres-backed")
	}
	sess, err := s.CreateSession(ctx, "legacy-writer-run", "default", "running", "auto")
	if err != nil {
		t.Fatal(err)
	}
	legacySet := `UPDATE agent_sessions SET phase = 'question', pending_question = 'Repeated', pending_input_type = 'question' WHERE id = $1`
	legacyClear := `UPDATE agent_sessions SET phase = 'running', pending_question = '', pending_actions = '[]', pending_input_type = '' WHERE id = $1`
	if _, err := postgresStore.Pool().Exec(ctx, legacySet, sess.ID); err != nil {
		t.Fatal(err)
	}
	first, _ := s.GetSession(ctx, sess.ID)
	if first.PendingRequestID == "" {
		t.Fatal("legacy writer did not receive a request nonce")
	}
	if _, err := postgresStore.Pool().Exec(ctx, legacyClear, sess.ID); err != nil {
		t.Fatal(err)
	}
	cleared, _ := s.GetSession(ctx, sess.ID)
	if cleared.PendingRequestID != "" {
		t.Fatalf("legacy clear retained nonce %q", cleared.PendingRequestID)
	}
	if _, err := postgresStore.Pool().Exec(ctx, legacySet, sess.ID); err != nil {
		t.Fatal(err)
	}
	second, _ := s.GetSession(ctx, sess.ID)
	if second.PendingRequestID == "" || second.PendingRequestID == first.PendingRequestID {
		t.Fatalf("legacy replacement reused nonce: first=%q second=%q", first.PendingRequestID, second.PendingRequestID)
	}
}

func TestReservePendingInputResponseIsAtomicAndReplaySafe(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	ctx := context.Background()
	resolver, ok := s.(store.PendingInputResolver)
	if !ok {
		t.Fatal("Postgres store does not implement PendingInputResolver")
	}
	sess, err := s.CreateSession(ctx, "resolve-run", "default", "running", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPendingQuestion(ctx, sess.ID, "question", "Repeated prompt", "question"); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.GetSession(ctx, sess.ID)
	metadata := json.RawMessage(`{"mode":"enqueue","source":"overseer","delivery_id":"delivery-1","overseer_resolution":{"request_id":"public-1"}}`)
	message, accepted, err := resolver.ReservePendingInputResponse(ctx, sess.ID, store.PendingInputResolution{
		RequestID: pending.PendingRequestID, Phase: "running", Role: "user", Content: "Proceed", Metadata: metadata,
	})
	if err != nil || !accepted || message == nil {
		t.Fatalf("ReservePendingInputResponse() = (%#v, %v, %v)", message, accepted, err)
	}
	if polled, err := s.PollNewUserMessages(ctx, sess.ID, 0); err != nil || len(polled) != 0 {
		t.Fatalf("held response was pollable: messages=%#v err=%v", polled, err)
	}
	if visible, err := s.GetMessages(ctx, sess.ID); err != nil || len(visible) != 0 {
		t.Fatalf("held response leaked through conversation read: messages=%#v err=%v", visible, err)
	}
	later, err := s.AppendMessage(ctx, sess.ID, "user", "Later user input", nil)
	if err != nil {
		t.Fatal(err)
	}
	if polled, err := s.PollNewUserMessages(ctx, sess.ID, 0); err != nil || len(polled) != 0 {
		t.Fatalf("later message crossed held-response barrier: messages=%#v err=%v", polled, err)
	}

	if err := s.SetPendingQuestion(ctx, sess.ID, "question", "Repeated prompt", "question"); err != nil {
		t.Fatal(err)
	}
	replacement, _ := s.GetSession(ctx, sess.ID)
	replayed, accepted, err := resolver.ReservePendingInputResponse(ctx, sess.ID, store.PendingInputResolution{
		RequestID: pending.PendingRequestID, Phase: "running", Role: "user", Content: "Proceed", Metadata: metadata,
	})
	if err != nil || !accepted || replayed == nil || replayed.ID != message.ID {
		t.Fatalf("replayed reservation = (%#v, %v, %v)", replayed, accepted, err)
	}
	stillPending, _ := s.GetSession(ctx, sess.ID)
	if stillPending.PendingRequestID != replacement.PendingRequestID {
		t.Fatalf("replay cleared replacement request: got %q want %q", stillPending.PendingRequestID, replacement.PendingRequestID)
	}
	if err := resolver.ReleasePendingInputResponse(ctx, sess.ID, message.ID, "delivery-1"); err != nil {
		t.Fatal(err)
	}
	if polled, err := s.PollNewUserMessages(ctx, sess.ID, 0); err != nil || len(polled) != 2 || polled[0].ID != message.ID || polled[1].ID != later.ID {
		t.Fatalf("released response did not preserve message ordering: messages=%#v err=%v", polled, err)
	}
}

func TestCreateSessionReturnsExistingOnDuplicateRun(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	ctx := context.Background()

	first, err := s.CreateSession(ctx, "duplicate-run", "default", "running", "implement")
	if err != nil {
		t.Fatalf("CreateSession first: %v", err)
	}
	second, err := s.CreateSession(ctx, "duplicate-run", "default", "pending", "setup")
	if err != nil {
		t.Fatalf("CreateSession duplicate: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate CreateSession ID = %s, want %s", second.ID, first.ID)
	}
	if second.Phase != first.Phase || second.CurrentStep != first.CurrentStep {
		t.Fatalf("duplicate CreateSession mutated session: got phase=%q step=%q, want phase=%q step=%q",
			second.Phase, second.CurrentStep, first.Phase, first.CurrentStep)
	}
}

func TestConversation(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	ctx := context.Background()

	sess, err := s.CreateSession(ctx, "conv-test", "default", "pending", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	msg1, err := s.AppendMessage(ctx, sess.ID, "user", "Hello", nil)
	if err != nil {
		t.Fatalf("AppendMessage 1: %v", err)
	}
	if msg1.Role != "user" || msg1.Content != "Hello" {
		t.Errorf("msg1: role=%q content=%q", msg1.Role, msg1.Content)
	}

	_, err = s.AppendMessage(ctx, sess.ID, "assistant", "Hi there!", nil)
	if err != nil {
		t.Fatalf("AppendMessage 2: %v", err)
	}

	msg3, err := s.AppendMessage(ctx, sess.ID, "user", "Do the thing", nil)
	if err != nil {
		t.Fatalf("AppendMessage 3: %v", err)
	}

	all, err := s.GetMessages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("GetMessages: got %d, want 3", len(all))
	}

	since, err := s.GetMessagesSince(ctx, sess.ID, msg1.ID)
	if err != nil {
		t.Fatalf("GetMessagesSince: %v", err)
	}
	if len(since) != 2 {
		t.Fatalf("GetMessagesSince: got %d, want 2", len(since))
	}

	userMsgs, err := s.PollNewUserMessages(ctx, sess.ID, msg1.ID)
	if err != nil {
		t.Fatalf("PollNewUserMessages: %v", err)
	}
	if len(userMsgs) != 1 || userMsgs[0].ID != msg3.ID {
		t.Errorf("PollNewUserMessages: got %d messages, want 1 with id %d", len(userMsgs), msg3.ID)
	}
}

func TestCancelledUserMessageExcludedFromAgentHistory(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	ctx := context.Background()

	sess, err := s.CreateSession(ctx, "cancelled-history-test", "default", "running", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cancelled, err := s.AppendMessage(ctx, sess.ID, "user", "withdrawn", json.RawMessage(`{"mode":"enqueue","cancelled_at_unix":123}`))
	if err != nil {
		t.Fatalf("AppendMessage cancelled: %v", err)
	}
	visible, err := s.AppendMessage(ctx, sess.ID, "assistant", "still visible", nil)
	if err != nil {
		t.Fatalf("AppendMessage visible: %v", err)
	}

	all, err := s.GetMessages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(all) != 1 || all[0].ID != visible.ID {
		t.Fatalf("GetMessages = %#v, want only visible message %d (cancelled=%d)", all, visible.ID, cancelled.ID)
	}
	since, err := s.GetMessagesSince(ctx, sess.ID, 0)
	if err != nil {
		t.Fatalf("GetMessagesSince: %v", err)
	}
	if len(since) != 1 || since[0].ID != visible.ID {
		t.Fatalf("GetMessagesSince = %#v, want only visible message %d", since, visible.ID)
	}
}

func TestCancelledReservedResponseIsNotPolled(t *testing.T) {
	state := setupTestStore(t)
	defer state.Close()
	ctx := context.Background()
	sess, err := state.CreateSession(ctx, "cancel-reserved", "default", "waiting", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetPendingQuestion(ctx, sess.ID, "question", "Proceed?", "question"); err != nil {
		t.Fatal(err)
	}
	fresh, _ := state.GetSession(ctx, sess.ID)
	resolver := state.(store.PendingInputResolver)
	message, reserved, err := resolver.ReservePendingInputResponse(ctx, sess.ID, store.PendingInputResolution{
		RequestID: fresh.PendingRequestID,
		Phase:     "running",
		Role:      "user",
		Content:   "discarded",
		Metadata:  json.RawMessage(`{"delivery_id":"cancel-reserved-delivery"}`),
	})
	if err != nil || !reserved {
		t.Fatalf("reserve = %#v, %v, %v", message, reserved, err)
	}
	if err := resolver.CancelPendingInputResponse(ctx, sess.ID, message.ID, "cancel-reserved-delivery"); err != nil {
		t.Fatal(err)
	}
	polled, err := state.PollNewUserMessages(ctx, sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(polled) != 0 {
		t.Fatalf("cancelled reserved response was polled: %#v", polled)
	}
}

func TestClaimAndCancelExactlyOneWins(t *testing.T) {
	state := setupTestStore(t)
	defer state.Close()
	ctx := context.Background()
	sess, err := state.CreateSession(ctx, "claim-cancel", "default", "running", "")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := state.AppendMessage(ctx, sess.ID, "user", "race", json.RawMessage(`{"mode":"enqueue"}`))
	if err != nil {
		t.Fatal(err)
	}
	claimer := state.(store.MessageClaimer)
	start := make(chan struct{})
	var claimed bool
	var claimErr, cancelErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, claimed, claimErr = claimer.ClaimUserMessage(ctx, sess.ID, msg.ID, uuid.New())
	}()
	go func() {
		defer wg.Done()
		<-start
		cancelErr = state.CancelUndeliveredUserMessage(ctx, sess.ID, msg.ID)
	}()
	close(start)
	wg.Wait()
	claimWon := claimed && claimErr == nil
	cancelWon := cancelErr == nil
	if claimWon == cancelWon {
		t.Fatalf("claimWon=%v claimErr=%v cancelWon=%v cancelErr=%v; want exactly one winner", claimWon, claimErr, cancelWon, cancelErr)
	}
}

func TestPollPendingPreservesHoleBeforeAssistantCursor(t *testing.T) {
	state := setupTestStore(t)
	defer state.Close()
	ctx := context.Background()
	sess, _ := state.CreateSession(ctx, "pending-hole", "default", "running", "")
	kickoff, _ := state.AppendMessage(ctx, sess.ID, "user", "kickoff", json.RawMessage(`{"mode":"enqueue"}`))
	_, won, err := state.(store.MessageClaimer).ClaimUserMessage(ctx, sess.ID, kickoff.ID, uuid.New())
	if err != nil || !won {
		t.Fatalf("claim kickoff = %v, %v", won, err)
	}
	hole, _ := state.AppendMessage(ctx, sess.ID, "user", "queued", json.RawMessage(`{"mode":"enqueue"}`))
	assistant, _ := state.AppendMessage(ctx, sess.ID, "assistant", "old reply", nil)
	polled, err := state.PollNewUserMessages(ctx, sess.ID, assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(polled) != 1 || polled[0].ID != hole.ID {
		t.Fatalf("polled=%#v, want pending hole %d before cursor %d", polled, hole.ID, assistant.ID)
	}
}

func TestRecoverClaimedMessagesRespectsStoppedFloor(t *testing.T) {
	state := setupTestStore(t)
	defer state.Close()
	ctx := context.Background()
	sess, _ := state.CreateSession(ctx, "claim-recovery", "default", "running", "")
	stopped, _ := state.AppendMessage(ctx, sess.ID, "user", "stopped", json.RawMessage(`{"mode":"enqueue"}`))
	crashed, _ := state.AppendMessage(ctx, sess.ID, "user", "crashed", json.RawMessage(`{"mode":"enqueue"}`))
	claimer := state.(store.MessageClaimer)
	stoppedToken := uuid.New()
	crashedToken := uuid.New()
	if _, won, _ := claimer.ClaimUserMessage(ctx, sess.ID, stopped.ID, stoppedToken); !won {
		t.Fatal("failed to claim stopped message")
	}
	if _, won, _ := claimer.ClaimUserMessage(ctx, sess.ID, crashed.ID, crashedToken); !won {
		t.Fatal("failed to claim crash message")
	}
	if err := claimer.CompleteClaims(ctx, sess.ID, stoppedToken); err != nil {
		t.Fatal(err)
	}
	if err := claimer.RecoverClaimedUserMessages(ctx, sess.ID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	pending, err := state.PollNewUserMessages(ctx, sess.ID, stopped.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != crashed.ID {
		t.Fatalf("pending=%#v, want only crash-recovered message %d", pending, crashed.ID)
	}
}

func TestInterruptQueuePreservesConcurrentRequests(t *testing.T) {
	state := setupTestStore(t)
	defer state.Close()
	ctx := context.Background()
	sess, _ := state.CreateSession(ctx, "interrupt-queue", "default", "running", "")
	interrupts := state.(store.InterruptStore)
	if _, _, err := interrupts.AppendInterrupt(ctx, sess.ID, "first"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := interrupts.AppendInterrupt(ctx, sess.ID, "second"); err != nil {
		t.Fatal(err)
	}
	_, _, who, ok, err := interrupts.ConsumeInterrupt(ctx, sess.ID)
	if err != nil || !ok || who != "first" {
		t.Fatalf("first consume = %q,%v,%v", who, ok, err)
	}
	_, _, who, ok, err = interrupts.ConsumeInterrupt(ctx, sess.ID)
	if err != nil || !ok || who != "second" {
		t.Fatalf("second consume = %q,%v,%v", who, ok, err)
	}
}

func TestConcurrentMessageAppendDeduplicatesGitHubEventKey(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	ctx := context.Background()

	sess, err := s.CreateSession(ctx, "event-dedupe-test", "default", "pending", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	metadata := json.RawMessage(`{"github_event_key":"acme/widgets#42:review_submitted:8101"}`)

	errorsByAppend := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, appendErr := s.AppendMessage(ctx, sess.ID, "user", "same feedback", metadata)
			errorsByAppend <- appendErr
		}()
	}
	close(start)
	wg.Wait()
	close(errorsByAppend)

	var inserted, duplicate int
	for appendErr := range errorsByAppend {
		switch {
		case appendErr == nil:
			inserted++
		case errors.Is(appendErr, store.ErrMessageAlreadyExists):
			duplicate++
		default:
			t.Fatalf("AppendMessage error = %v", appendErr)
		}
	}
	if inserted != 1 || duplicate != 1 {
		t.Fatalf("append results = %d inserted, %d duplicate; want 1/1", inserted, duplicate)
	}
	messages, err := s.GetMessages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
}

func TestActivityEvents(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	ctx := context.Background()

	sess, _ := s.CreateSession(ctx, "activity-test", "default", "pending", "")

	detail := json.RawMessage(`{"tool":"bash","args":"ls"}`)
	ev, err := s.WriteActivityEvent(ctx, sess.ID, "tool_use", "Running bash", detail)
	if err != nil {
		t.Fatalf("WriteActivityEvent: %v", err)
	}
	if ev.EventType != "tool_use" {
		t.Errorf("EventType = %q, want %q", ev.EventType, "tool_use")
	}

	_, _ = s.WriteActivityEvent(ctx, sess.ID, "assistant_text", "Analyzing code", nil)
	_, _ = s.WriteActivityEvent(ctx, sess.ID, "tool_result", "Found 3 files", nil)

	recent, err := s.GetRecentActivity(ctx, sess.ID, 2)
	if err != nil {
		t.Fatalf("GetRecentActivity: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("GetRecentActivity: got %d, want 2", len(recent))
	}

	other, _ := s.CreateSession(ctx, "activity-test-2", "default", "pending", "")
	_, _ = s.WriteActivityEvent(ctx, other.ID, "tool_use", "Other latest", nil)
	bulk, ok := s.(interface {
		GetLatestActivityBySessions(context.Context, []uuid.UUID) (map[uuid.UUID]store.ActivityEvent, error)
	})
	if !ok {
		t.Fatal("store does not implement GetLatestActivityBySessions")
	}
	latest, err := bulk.GetLatestActivityBySessions(ctx, []uuid.UUID{sess.ID, other.ID})
	if err != nil {
		t.Fatalf("GetLatestActivityBySessions: %v", err)
	}
	if got := latest[sess.ID].Summary; got != "Found 3 files" {
		t.Errorf("latest first session = %q, want Found 3 files", got)
	}
	if got := latest[other.ID].Summary; got != "Other latest" {
		t.Errorf("latest second session = %q, want Other latest", got)
	}

	versions, ok := s.(interface {
		GetAgentRunSummaryVersions(context.Context, string) (map[string]string, error)
	})
	if !ok {
		t.Fatal("store does not implement GetAgentRunSummaryVersions")
	}
	before, err := versions.GetAgentRunSummaryVersions(ctx, "default")
	if err != nil {
		t.Fatalf("GetAgentRunSummaryVersions before activity: %v", err)
	}
	_, _ = s.WriteActivityEvent(ctx, sess.ID, "tool_use", "Newest event", nil)
	after, err := versions.GetAgentRunSummaryVersions(ctx, "default")
	if err != nil {
		t.Fatalf("GetAgentRunSummaryVersions after activity: %v", err)
	}
	if before["default/activity-test"] == after["default/activity-test"] {
		t.Errorf("summary version did not change after latest activity: %q", after["default/activity-test"])
	}

	latestIDs, ok := s.(interface {
		GetLatestActivityEventID(context.Context, uuid.UUID) (int64, error)
	})
	if !ok {
		t.Fatal("store does not implement GetLatestActivityEventID")
	}
	all, err := s.GetAllActivity(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetAllActivity: %v", err)
	}
	lastID, err := latestIDs.GetLatestActivityEventID(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetLatestActivityEventID: %v", err)
	}
	if want := all[len(all)-1].ID; lastID != want {
		t.Errorf("GetLatestActivityEventID = %d, want %d", lastID, want)
	}
	empty, _ := s.CreateSession(ctx, "activity-test-empty", "default", "pending", "")
	if id, err := latestIDs.GetLatestActivityEventID(ctx, empty.ID); err != nil || id != 0 {
		t.Errorf("GetLatestActivityEventID(no events) = (%d, %v), want (0, nil)", id, err)
	}
}

func TestArtifacts(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	ctx := context.Background()

	sess, _ := s.CreateSession(ctx, "artifact-test", "default", "pending", "")

	art, err := s.UpsertArtifact(ctx, sess.ID, "plan", "# My Plan\n\nDo stuff", "", "abc123", nil)
	if err != nil {
		t.Fatalf("UpsertArtifact: %v", err)
	}
	if art.Kind != "plan" || art.ContentHash != "abc123" {
		t.Errorf("artifact: kind=%q hash=%q", art.Kind, art.ContentHash)
	}

	// Upsert same kind should update.
	art2, err := s.UpsertArtifact(ctx, sess.ID, "plan", "# Updated Plan", "s3://bucket/plan.md", "def456", nil)
	if err != nil {
		t.Fatalf("UpsertArtifact update: %v", err)
	}
	if art2.Content != "# Updated Plan" || art2.S3URL != "s3://bucket/plan.md" {
		t.Errorf("after upsert: content=%q s3url=%q", art2.Content, art2.S3URL)
	}

	got, err := s.GetArtifact(ctx, sess.ID, "plan")
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if got.ContentHash != "def456" {
		t.Errorf("GetArtifact hash = %q, want %q", got.ContentHash, "def456")
	}

	all, err := s.GetArtifacts(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetArtifacts: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("GetArtifacts: got %d, want 1", len(all))
	}
}

func TestSessionTranscripts(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	sess, err := s.CreateSession(ctx, "transcript-run", "default", "running", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Absent snapshot reads as (nil, nil).
	data, err := s.GetSessionTranscript(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSessionTranscript (absent): %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil for absent transcript, got %d bytes", len(data))
	}

	if err := s.UpsertSessionTranscript(ctx, sess.ID, []byte("v1"), 3); err != nil {
		t.Fatalf("UpsertSessionTranscript: %v", err)
	}
	// Upsert replaces in place — one bounded row per session.
	if err := s.UpsertSessionTranscript(ctx, sess.ID, []byte("v2-longer"), 5); err != nil {
		t.Fatalf("UpsertSessionTranscript update: %v", err)
	}
	data, err = s.GetSessionTranscript(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSessionTranscript: %v", err)
	}
	if string(data) != "v2-longer" {
		t.Fatalf("transcript = %q, want %q", data, "v2-longer")
	}

	if err := s.DeleteSessionTranscript(ctx, sess.ID); err != nil {
		t.Fatalf("DeleteSessionTranscript: %v", err)
	}
	data, err = s.GetSessionTranscript(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSessionTranscript after delete: %v", err)
	}
	if data != nil {
		t.Fatal("transcript not deleted")
	}
	// Deleting an absent row is a no-op, not an error.
	if err := s.DeleteSessionTranscript(ctx, sess.ID); err != nil {
		t.Fatalf("DeleteSessionTranscript (absent): %v", err)
	}
}
