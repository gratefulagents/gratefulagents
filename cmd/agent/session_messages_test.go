package main

import (
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
)

func TestNextPendingUserMessageSkipsConsumedImmediate(t *testing.T) {
	messages := []sessionclient.UserMessage{
		{Message: store.Message{ID: 10, Content: "steer now"}, Mode: sessionclient.UserMessageModeImmediate},
		{Message: store.Message{ID: 11, Content: "queued next"}, Mode: sessionclient.UserMessageModeEnqueue},
	}
	consumed := map[int64]struct{}{10: {}}

	msg, ok, skipCursor, immediate := nextPendingUserMessage(messages, consumed)
	if !ok {
		t.Fatal("expected pending message")
	}
	if msg.ID != 11 {
		t.Fatalf("message ID = %d, want 11", msg.ID)
	}
	if skipCursor != 10 {
		t.Fatalf("skipCursor = %d, want 10", skipCursor)
	}
	if immediate {
		t.Fatal("expected queued message after consumed immediate")
	}
}

func TestNextPendingUserMessagePrioritizesImmediateOverEarlierQueued(t *testing.T) {
	messages := []sessionclient.UserMessage{
		{Message: store.Message{ID: 30, Content: "queued next"}, Mode: sessionclient.UserMessageModeEnqueue},
		{Message: store.Message{ID: 31, Content: "steer now"}, Mode: sessionclient.UserMessageModeImmediate},
	}

	msg, ok, skipCursor, immediate := nextPendingUserMessage(messages, map[int64]struct{}{})
	if !ok {
		t.Fatal("expected pending message")
	}
	if msg.ID != 31 {
		t.Fatalf("message ID = %d, want immediate message 31", msg.ID)
	}
	if skipCursor != 0 {
		t.Fatalf("skipCursor = %d, want 0 because queued message must remain pending", skipCursor)
	}
	if !immediate {
		t.Fatal("expected immediate message to win")
	}
}

func TestCollectImmediateRunItemsPreservesOrderAndCursor(t *testing.T) {
	t.Parallel()
	messages := []sessionclient.UserMessage{
		{Message: store.Message{ID: 20, Content: "queued"}, Mode: sessionclient.UserMessageModeEnqueue},
		{Message: store.Message{ID: 21, Content: "first immediate"}, Mode: sessionclient.UserMessageModeImmediate},
		{Message: store.Message{ID: 22, Content: "second immediate"}, Mode: sessionclient.UserMessageModeImmediate},
	}
	consumed := map[int64]struct{}{}

	items, consumedIDs, cursor := collectImmediateRunItems(messages, consumed)
	// The queued message (ID 20) is still pending, so the cursor must not
	// advance past it: feeding the cursor back into a future peek would
	// otherwise drop the queued input (SDK v0.0.88 cursor semantics).
	if cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (queued message 20 must remain pending)", cursor)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].Message == nil || items[0].Message.Text != "first immediate" {
		t.Fatalf("first item = %#v, want first immediate", items[0].Message)
	}
	if items[1].Message == nil || items[1].Message.Text != "second immediate" {
		t.Fatalf("second item = %#v, want second immediate", items[1].Message)
	}
	if len(consumedIDs) != 2 || consumedIDs[0] != 21 || consumedIDs[1] != 22 {
		t.Fatalf("consumedIDs = %v, want [21 22]", consumedIDs)
	}
	if len(consumed) != 0 {
		t.Fatalf("selection must not mutate durable-consumption map before claim: %v", consumed)
	}
}

// A turn boundary has nothing to interrupt, so the oldest pending message
// opens the turn even when a newer immediate steer is queued behind it. The
// steer is not skipped or marked handled: the runner's immediate-input poll
// folds it into the same turn before the first model call.
func TestNextTurnStartingUserMessageKeepsCreationOrder(t *testing.T) {
	t.Parallel()
	messages := []sessionclient.UserMessage{
		{Message: store.Message{ID: 30, Content: "seeded kickoff request"}, Mode: sessionclient.UserMessageModeEnqueue},
		{Message: store.Message{ID: 31, Content: "steer typed during startup"}, Mode: sessionclient.UserMessageModeImmediate},
	}

	msg, ok, immediate := nextTurnStartingUserMessage(messages, map[int64]struct{}{})
	if !ok {
		t.Fatal("expected pending message")
	}
	if msg.ID != 30 {
		t.Fatalf("message ID = %d, want the older kickoff message 30", msg.ID)
	}
	if immediate {
		t.Fatal("kickoff message must not be reported as immediate")
	}
}

func TestNextTurnStartingUserMessageSkipsBlankAndHandledImmediate(t *testing.T) {
	t.Parallel()
	messages := []sessionclient.UserMessage{
		{Message: store.Message{ID: 40, Content: "   "}, Mode: sessionclient.UserMessageModeEnqueue},
		{Message: store.Message{ID: 41, Content: "already folded into last turn"}, Mode: sessionclient.UserMessageModeImmediate},
		{Message: store.Message{ID: 42, Content: "fresh steer"}, Mode: sessionclient.UserMessageModeImmediate},
	}

	msg, ok, immediate := nextTurnStartingUserMessage(messages, map[int64]struct{}{41: {}})
	if !ok {
		t.Fatal("expected pending message")
	}
	if msg.ID != 42 {
		t.Fatalf("message ID = %d, want 42", msg.ID)
	}
	if !immediate {
		t.Fatal("an immediate message that opens a turn must be reported as immediate")
	}
	if _, ok, _ := nextTurnStartingUserMessage(messages[:2], map[int64]struct{}{41: {}}); ok {
		t.Fatal("blank and handled messages must not open a turn")
	}
}

func TestCollectImmediateRunItemsReportsOnlyNewlyConsumedIDs(t *testing.T) {
	messages := []sessionclient.UserMessage{
		{Message: store.Message{ID: 30, Content: "already handled"}, Mode: sessionclient.UserMessageModeImmediate},
		{Message: store.Message{ID: 31, Content: "fresh steer"}, Mode: sessionclient.UserMessageModeImmediate},
	}
	consumed := map[int64]struct{}{30: {}}

	items, consumedIDs, _ := collectImmediateRunItems(messages, consumed)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if len(consumedIDs) != 1 || consumedIDs[0] != 31 {
		t.Fatalf("consumedIDs = %v, want [31]", consumedIDs)
	}
}

// Only the exact stopped prompt is removed: an earlier queued message that a
// later immediate steer overtook must stay pending, so this is not a floor.
func TestWithoutStoppedMessageRemovesOnlyTheStoppedPrompt(t *testing.T) {
	messages := []sessionclient.UserMessage{
		{Message: store.Message{ID: 30, Content: "queued earlier"}, Mode: sessionclient.UserMessageModeEnqueue},
		{Message: store.Message{ID: 31, Content: "stopped steer"}, Mode: sessionclient.UserMessageModeImmediate},
		{Message: store.Message{ID: 32, Content: "newer"}, Mode: sessionclient.UserMessageModeEnqueue},
	}

	got := withoutStoppedMessage(messages, 31)
	if len(got) != 2 || got[0].ID != 30 || got[1].ID != 32 {
		t.Fatalf("withoutStoppedMessage() IDs = %v, want [30 32]", messageIDs(got))
	}
	if same := withoutStoppedMessage(messages, 0); len(same) != 3 {
		t.Fatalf("zero stopped ID must keep every message, got %d", len(same))
	}
}

func messageIDs(messages []sessionclient.UserMessage) []int64 {
	ids := make([]int64, 0, len(messages))
	for _, msg := range messages {
		ids = append(ids, msg.ID)
	}
	return ids
}
