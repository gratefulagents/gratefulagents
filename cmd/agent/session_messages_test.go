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
