package main

import (
	"strings"
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

func TestBuildConversationTailRespectsHistoryFloorAndCurrentMessage(t *testing.T) {
	t.Parallel()

	messages := []store.Message{
		{ID: 1, Role: "user", Content: "old task"},
		{ID: 2, Role: "assistant", Content: "old summary"},
		{ID: 3, Role: "user", Content: "current user message"},
		{ID: 4, Role: "system", Content: "[SYSTEM] continue with shipping"},
		{ID: 5, Role: "assistant", Content: "recent assistant summary"},
	}

	items := buildConversationTail(messages, sessionclient.WorkingState{HistoryFloorMessageID: 2}, 3, 8)
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].Agent == nil || items[0].Agent.Name != "system-summary" {
		t.Fatalf("items[0].Agent = %#v, want system-summary", items[0].Agent)
	}
	if got := items[0].Message.Text; got != "[SYSTEM] continue with shipping" {
		t.Fatalf("items[0].Message.Text = %q", got)
	}
	if items[1].Agent == nil || items[1].Agent.Name != "assistant-summary" {
		t.Fatalf("items[1].Agent = %#v, want assistant-summary", items[1].Agent)
	}
}

func TestBuildWorkingStateContextUsesMostRecentProgress(t *testing.T) {
	t.Parallel()

	state := sessionclient.WorkingState{
		Goal:                 "finish github auth end to end",
		CurrentMode:          "deep",
		LastUserMessage:      "also wire github app callbacks",
		LastAssistantSummary: "implemented oauth callback plumbing",
		RecentTurnSummaries:  []string{"summary-1", "summary-2", "summary-3", "summary-4", "summary-5"},
	}

	context := buildWorkingStateContext(state)
	if !strings.Contains(context, "Current objective: finish github auth end to end") {
		t.Fatalf("context = %q, want objective", context)
	}
	if !strings.Contains(context, "Mode: deep") {
		t.Fatalf("context = %q, want mode", context)
	}
	if strings.Contains(context, "summary-1") {
		t.Fatalf("context = %q, want only last four summaries", context)
	}
	if !strings.Contains(context, "summary-5") {
		t.Fatalf("context = %q, want newest summary", context)
	}
}

func TestBuildAssistantTurnSummaryCapturesToolsAndIssues(t *testing.T) {
	t.Parallel()

	items := []agent.RunItem{
		{
			Type:    agent.RunItemMessage,
			Agent:   &agent.Agent{Name: "assistant"},
			Message: &agent.MessageOutput{Text: "Investigated the auth wiring and found the missing callback registration."},
		},
		{Type: agent.RunItemToolCall, ToolCall: &agent.ToolCallData{Name: "grep"}},
		{Type: agent.RunItemToolCall, ToolCall: &agent.ToolCallData{Name: "bash"}},
		{Type: agent.RunItemToolCall, ToolCall: &agent.ToolCallData{Name: "bash"}},
		{Type: agent.RunItemToolOutput, ToolOutput: &agent.ToolOutputData{CallID: "1", IsError: true, Content: "oauth token missing"}},
	}

	summary := buildAssistantTurnSummary(items)
	if !strings.Contains(summary, "Investigated the auth wiring") {
		t.Fatalf("summary = %q, want assistant text", summary)
	}
	if !strings.Contains(summary, "bash x 2") || !strings.Contains(summary, "grep x 1") {
		t.Fatalf("summary = %q, want tool counts", summary)
	}
	if !strings.Contains(summary, "Issues: oauth token missing") {
		t.Fatalf("summary = %q, want error summary", summary)
	}
}

func TestDeriveWorkingStateGoalUsesEffectivePromptForApprovalShorthand(t *testing.T) {
	t.Parallel()

	goal := deriveWorkingStateGoal("approve", "Continue with approved phase 'shipping'.")
	if goal != "Continue with approved phase 'shipping'." {
		t.Fatalf("goal = %q", goal)
	}
}

func TestBuildTurnInputPrefersTranscriptOverDurableTail(t *testing.T) {
	t.Parallel()

	transcript := []agent.RunItem{
		{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: "prior user message"}},
		{Type: agent.RunItemToolCall, ToolCall: &agent.ToolCallData{ID: "call1", Name: "bash"}},
		{Type: agent.RunItemToolOutput, ToolOutput: &agent.ToolOutputData{CallID: "call1", Content: "full tool output survives"}},
		{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: "prior assistant answer"}},
	}
	messages := []store.Message{
		{ID: 1, Role: "user", Content: "prior user message"},
		{ID: 2, Role: "assistant", Content: "prior assistant answer"},
	}

	items := buildTurnInput(transcript, messages, sessionclient.WorkingState{}, 0, 8)
	if len(items) != len(transcript) {
		t.Fatalf("len(items) = %d, want %d (verbatim transcript replay)", len(items), len(transcript))
	}
	if items[1].ToolCall == nil || items[1].ToolCall.ID != "call1" {
		t.Fatalf("items[1] = %#v, want tool call to survive the turn boundary", items[1])
	}
	if items[2].ToolOutput == nil || items[2].ToolOutput.Content != "full tool output survives" {
		t.Fatalf("items[2] = %#v, want verbatim tool output", items[2])
	}
	// The returned slice must be a copy so later appends cannot mutate the
	// stored transcript.
	items = append(items, agent.RunItem{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: "new user message"}})
	if len(transcript) != 4 {
		t.Fatalf("transcript mutated by append: len = %d", len(transcript))
	}
}

func TestBuildTurnInputFallsBackToDurableTail(t *testing.T) {
	t.Parallel()

	messages := []store.Message{
		{ID: 1, Role: "user", Content: "old task"},
		{ID: 2, Role: "assistant", Content: "old summary"},
	}
	state := sessionclient.WorkingState{}

	items := buildTurnInput(nil, messages, state, 0, 8)
	want := buildConversationTail(messages, state, 0, 8)
	if len(items) != len(want) {
		t.Fatalf("len(items) = %d, want %d (durable-tail fallback)", len(items), len(want))
	}
	if len(items) == 0 {
		t.Fatal("expected non-empty durable-tail fallback")
	}

	if got := buildTurnInput(nil, nil, state, 0, 8); got != nil {
		t.Fatalf("buildTurnInput with no transcript and no messages = %#v, want nil", got)
	}
}

func TestTranscriptAfterRun(t *testing.T) {
	t.Parallel()

	if got := transcriptAfterRun(nil); got != nil {
		t.Fatalf("transcriptAfterRun(nil) = %#v, want nil", got)
	}

	interrupted := &agent.RunResult{
		Interruption: &agent.Interruption{ToolName: "bash", ToolCallID: "call1"},
		FinalHistory: []agent.RunItem{{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: "x"}}},
	}
	if got := transcriptAfterRun(interrupted); got != nil {
		t.Fatalf("transcriptAfterRun(interrupted) = %#v, want nil (unpaired tool_use must not be replayed)", got)
	}

	history := []agent.RunItem{
		{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: "user"}},
		{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: "assistant"}},
	}
	got := transcriptAfterRun(&agent.RunResult{FinalHistory: history})
	if len(got) != len(history) {
		t.Fatalf("len = %d, want %d", len(got), len(history))
	}
	if got[1].Message == nil || got[1].Message.Text != "assistant" {
		t.Fatalf("got[1] = %#v, want assistant message", got[1])
	}
}

func TestOutOfBandMessageItemsFoldsSystemAndAssistantNotes(t *testing.T) {
	t.Parallel()

	messages := []store.Message{
		{ID: 5, Role: "user", Content: "prior user message"},
		{ID: 6, Role: "assistant", Content: "prior assistant reply"}, // loop's own append
		{ID: 7, Role: "system", Content: "Plan rejected: scope too broad\nWaiting for your next message."},
		{ID: 8, Role: "assistant", Content: "Mode switched to **plan**."},
		{ID: 9, Role: "user", Content: "current user message"},
	}

	items := outOfBandMessageItems(messages, 5, 6, sessionclient.WorkingState{})
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2 (out-of-band system + assistant notes)", len(items))
	}
	if items[0].Agent == nil || items[0].Agent.Name != "system-summary" {
		t.Fatalf("items[0].Agent = %#v, want system-summary", items[0].Agent)
	}
	if !strings.Contains(items[0].Message.Text, "Plan rejected: scope too broad") {
		t.Fatalf("items[0].Message.Text = %q, want plan rejection", items[0].Message.Text)
	}
	if items[1].Agent == nil || items[1].Agent.Name != "assistant-summary" {
		t.Fatalf("items[1].Agent = %#v, want assistant-summary (dashboard mode note)", items[1].Agent)
	}
	if !strings.Contains(items[1].Message.Text, "Mode switched") {
		t.Fatalf("items[1].Message.Text = %q, want mode-switch note", items[1].Message.Text)
	}
}

func TestOutOfBandMessageItemsSkipsSeenSelfAndUserMessages(t *testing.T) {
	t.Parallel()

	messages := []store.Message{
		{ID: 5, Role: "system", Content: "already represented"},
		{ID: 6, Role: "assistant", Content: "loop's own reply"},
		{ID: 7, Role: "user", Content: "queued user message gets its own turn"},
	}

	if items := outOfBandMessageItems(messages, 5, 6, sessionclient.WorkingState{}); items != nil {
		t.Fatalf("items = %#v, want nil (seen, self-append, and user messages all skipped)", items)
	}
	if items := outOfBandMessageItems(nil, 0, 0, sessionclient.WorkingState{}); items != nil {
		t.Fatalf("items = %#v, want nil for no messages", items)
	}
}

func TestMaxSeenMessageID(t *testing.T) {
	t.Parallel()

	messages := []store.Message{{ID: 3}, {ID: 9}, {ID: 7}}
	if got := maxSeenMessageID(5, messages); got != 9 {
		t.Fatalf("maxSeenMessageID = %d, want 9", got)
	}
	if got := maxSeenMessageID(12, messages); got != 12 {
		t.Fatalf("maxSeenMessageID = %d, want 12 (never regresses)", got)
	}
	if got := maxSeenMessageID(4, nil); got != 4 {
		t.Fatalf("maxSeenMessageID = %d, want 4 for no messages", got)
	}
}
